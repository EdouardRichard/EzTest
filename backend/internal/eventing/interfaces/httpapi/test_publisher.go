package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// AuditActionTestPublished is the audit action for the admin test
	// publisher, from design §6.4.
	AuditActionTestPublished = "sse.test_published"
	// testEventChannel and testEventType are fixed by the event contract: the
	// test publisher only writes synthetic system events.
	testEventChannel = "system"
	testEventType    = "system.test"
	// TestEventMinCount and TestEventMaxCount bound one publish request.
	TestEventMinCount = 1
	TestEventMaxCount = 100
	// maxTestEventBodyBytes caps the publisher request body. The detail object
	// may be up to 64 KiB; 128 KiB leaves room for the rest of the envelope.
	maxTestEventBodyBytes = 128 << 10
)

// Publisher is the write use case consumed by the test publisher. The eventing
// application Service implements it.
type Publisher interface {
	Publish(ctx context.Context, event eventing.NewEvent) (eventing.Event, error)
}

// TestPublisher serves POST /api/admin/stream/test-events. The server mounts it
// only when EZTEST_ENABLE_TEST_ENDPOINTS=true and always behind auth plus the
// admin guard; the handler re-checks the flag as defence in depth.
type TestPublisher struct {
	publisher Publisher
	auditor   auditapp.Recorder
	enabled   bool
	now       func() time.Time
}

// NewTestPublisher returns the admin test publisher. A nil auditor discards the
// audit entry.
func NewTestPublisher(publisher Publisher, auditor auditapp.Recorder, enabled bool) *TestPublisher {
	if auditor == nil {
		auditor = auditapp.NopRecorder{}
	}
	return &TestPublisher{publisher: publisher, auditor: auditor, enabled: enabled, now: time.Now}
}

// ServeHTTP publishes count system.test events and returns their seqs. Success
// is audited once; an audit failure is logged but does not fail the publish,
// because the events are already durable.
func (p *TestPublisher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.enabled {
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "not found")
		return
	}
	var request testEventRequest
	if err := platform.DecodeJSON(r, &request, maxTestEventBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	count := 1
	if request.Count != nil {
		count = *request.Count
	}
	if count < TestEventMinCount || count > TestEventMaxCount {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
			fmt.Sprintf("count must be between %d and %d", TestEventMinCount, TestEventMaxCount))
		return
	}
	detail := request.Detail
	trimmed := bytes.TrimSpace(detail)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		detail = json.RawMessage(`{}`)
	}

	seqs := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		event, err := p.publisher.Publish(r.Context(), eventing.NewEvent{
			Channel: testEventChannel,
			Type:    testEventType,
			Summary: request.Summary,
			Detail:  detail,
			TS:      p.now().UTC(),
		})
		if err != nil {
			platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
			return
		}
		seqs = append(seqs, event.Seq)
	}

	p.recordAudit(r.Context(), count)
	writeJSON(w, http.StatusOK, testPublisherEnvelope{Data: testPublisherData{Seqs: seqs}})
}

// recordAudit writes the single sse.test_published entry for the request. The
// actor and the HTTP metadata come from the context the server attached.
func (p *TestPublisher) recordAudit(ctx context.Context, count int) {
	entry := auditdomain.Entry{
		Action:     AuditActionTestPublished,
		TargetType: "channel",
		TargetID:   testEventChannel,
		Metadata: map[string]any{
			"channel": testEventChannel,
			"type":    testEventType,
			"count":   count,
		},
	}
	if meta := auditapp.MetaFromContext(ctx); meta != (auditapp.RequestMeta{}) {
		entry.RequestID = meta.RequestID
		entry.IP = meta.IP
		entry.UserAgent = meta.UserAgent
	}
	if actorID, ok := auditapp.ActorIDFromContext(ctx); ok {
		actor := actorID
		entry.ActorUserID = &actor
	}
	if err := p.auditor.Record(ctx, entry); err != nil {
		slog.Default().Error("eventing: record sse test publish audit", "error", err)
	}
}

// testEventRequest is the POST /api/admin/stream/test-events body. The pointer
// count distinguishes an omitted count (default 1) from an explicit 0, which is
// rejected.
type testEventRequest struct {
	Count   *int            `json:"count"`
	Summary string          `json:"summary"`
	Detail  json.RawMessage `json:"detail"`
}

type testPublisherEnvelope struct {
	Data testPublisherData `json:"data"`
}

type testPublisherData struct {
	Seqs []int64 `json:"seqs"`
}

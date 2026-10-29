package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// handlers_version_changes_test.go pins the P8 §7.2 batchTimeline addition to
// GET /api/versions/{id}/code/changes: the legacy ChangeList fields stay exactly
// where they were and the new array is additive.

// versionChangeCommitC is the head of the second timeline batch.
const versionChangeCommitC = "cccccccccccccccccccccccccccccccccccccccc"

// fakeVersionChangeService is the hand-written VersionChangeService double.
type fakeVersionChangeService struct {
	timelineFn func(context.Context, int64) ([]application.VersionChangeTimelineEntry, error)
	calls      []int64
}

func (f *fakeVersionChangeService) Timeline(ctx context.Context, versionID int64) ([]application.VersionChangeTimelineEntry, error) {
	f.calls = append(f.calls, versionID)
	if f.timelineFn == nil {
		return nil, nil
	}
	return f.timelineFn(ctx, versionID)
}

// versionChangeWire is the wire shape of one batchTimeline entry.
type versionChangeWire struct {
	BatchID          string            `json:"batchId"`
	BatchNo          int               `json:"batchNo"`
	BatchStatus      string            `json:"batchStatus"`
	BaseCommit       string            `json:"baseCommit"`
	HeadCommit       string            `json:"headCommit"`
	HistoryRewritten bool              `json:"historyRewritten"`
	ChangedFiles     int               `json:"changedFiles"`
	ImpactedCases    int               `json:"impactedCases"`
	UnmappedCount    int               `json:"unmappedCount"`
	Items            []json.RawMessage `json:"items"`
	ComputedAt       string            `json:"computedAt"`
	Provisional      bool              `json:"provisional"`
	Unavailable      bool              `json:"unavailable"`
}

// TestVersionChangesIncludesBatchTimelineAndLegacyFields pins the additive
// contract: every pre-P8 field survives and batchTimeline carries the batch
// commit pair, counters, rewritten flag and summary items.
func TestVersionChangesIncludesBatchTimelineAndLegacyFields(t *testing.T) {
	preview := domain.ChangeSetData{
		Stats: []byte(`{"changedFiles":1,"additions":4,"deletions":1,"truncated":false}`),
		Files: []domain.ChangedFile{
			{Path: "src/api/refund.ts", Status: "M", Additions: 4, Deletions: 1, HeadBlobHash: "git-sha1:" + commitA},
		},
	}
	query := &fakeQueryService{versionChangesFn: func(_ context.Context, versionID int64, head string) (domain.ChangeSetData, string, string, error) {
		if versionID != 42 || head != "" {
			t.Fatalf("VersionChanges(%d, %q), want (42, empty)", versionID, head)
		}
		return preview, commitA, commitB, nil
	}}
	computedAt := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	versionChanges := &fakeVersionChangeService{
		timelineFn: func(_ context.Context, versionID int64) ([]application.VersionChangeTimelineEntry, error) {
			if versionID != 42 {
				t.Fatalf("Timeline version = %d, want 42", versionID)
			}
			return []application.VersionChangeTimelineEntry{
				{
					BatchNo: 1, BatchStatus: "done", BatchKind: "baseline",
					Change: domain.VersionChange{
						ID: 7001, VersionID: 42, BatchID: 11,
						BaseCommit: commitA, HeadCommit: commitB,
						ChangedFiles: 3, ImpactedCases: 2, UnmappedCount: 1,
						Items: []domain.VersionChangeItem{
							{
								File: "src/api/refund.ts", ChangeKind: domain.VersionChangeModified,
								Subjects: []domain.VersionChangeSubject{
									{Kind: domain.KindAPI, Name: "POST /api/refund"},
								},
								CaseIDs: []string{"501"},
							},
							{
								File: "src/deleted.ts", ChangeKind: domain.VersionChangeDeleted,
								Unmapped: true,
							},
						},
						ComputedAt: computedAt,
					},
				},
				{
					BatchNo: 2, BatchStatus: "done", BatchKind: "incremental",
					Change: domain.VersionChange{
						ID: 7002, VersionID: 42, BatchID: 12,
						BaseCommit: commitB, HeadCommit: versionChangeCommitC,
						HistoryRewritten: true,
						ComputedAt:       computedAt.Add(time.Minute),
					},
				},
			}, nil
		},
	}

	handler := newTestHandler(t, HandlerDeps{Query: query, VersionChanges: versionChanges})
	recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/changes", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}

	// Decode into the legacy wire plus the additive field so a removed legacy
	// field would fail this test rather than silently rendering an empty UI.
	var body struct {
		Data struct {
			changeListWire
			BatchTimeline []versionChangeWire `json:"batchTimeline"`
		} `json:"data"`
	}
	decodeBody(t, recorder, &body)
	data := body.Data
	if data.BaseCommit != commitA || data.HeadCommit != commitB {
		t.Fatalf("legacy commit pair = %q..%q", data.BaseCommit, data.HeadCommit)
	}
	if data.Stats.Files != 1 || data.Stats.Additions != 4 || data.Stats.Deletions != 1 {
		t.Fatalf("legacy stats = %+v", data.Stats)
	}
	if len(data.Files) != 1 || data.Files[0].Path != "src/api/refund.ts" {
		t.Fatalf("legacy files = %+v", data.Files)
	}
	if data.ChangeSetID != nil || data.NextCursor != nil || data.HasMore {
		t.Fatalf("legacy page fields = %+v", data)
	}

	if len(data.BatchTimeline) != 2 {
		t.Fatalf("batchTimeline = %+v, want 2 entries", data.BatchTimeline)
	}
	first := data.BatchTimeline[0]
	if first.BatchID != "11" || first.BatchNo != 1 || first.BatchStatus != "done" ||
		first.BaseCommit != commitA || first.HeadCommit != commitB {
		t.Fatalf("batch 1 = %+v", first)
	}
	if first.HistoryRewritten || first.ChangedFiles != 3 || first.ImpactedCases != 2 ||
		first.UnmappedCount != 1 {
		t.Fatalf("batch 1 counters = %+v", first)
	}
	if first.ComputedAt != computedAt.Format(time.RFC3339) {
		t.Fatalf("batch 1 computedAt = %q, want the RFC3339 clock value", first.ComputedAt)
	}
	if len(first.Items) != 2 {
		t.Fatalf("batch 1 items = %d, want 2", len(first.Items))
	}
	var refundItem struct {
		File       string `json:"file"`
		ChangeKind string `json:"changeKind"`
		Subjects   []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"subjects"`
		CaseIDs  []string `json:"caseIds"`
		Unmapped bool     `json:"unmapped"`
	}
	if err := json.Unmarshal(first.Items[0], &refundItem); err != nil {
		t.Fatalf("decode refund item: %v", err)
	}
	if refundItem.File != "src/api/refund.ts" || refundItem.ChangeKind != "modified" ||
		len(refundItem.Subjects) != 1 || refundItem.Subjects[0].Kind != "api" ||
		refundItem.Subjects[0].Name != "POST /api/refund" ||
		len(refundItem.CaseIDs) != 1 || refundItem.CaseIDs[0] != "501" || refundItem.Unmapped {
		t.Fatalf("refund item = %+v", refundItem)
	}
	var deletedItem struct {
		Unmapped bool `json:"unmapped"`
	}
	if err := json.Unmarshal(first.Items[1], &deletedItem); err != nil {
		t.Fatalf("decode deleted item: %v", err)
	}
	if !deletedItem.Unmapped {
		t.Fatalf("deleted item = %+v, want unmapped", deletedItem)
	}
	second := data.BatchTimeline[1]
	if second.BatchID != "12" || !second.HistoryRewritten || len(second.Items) != 0 {
		t.Fatalf("batch 2 = %+v", second)
	}
	if len(versionChanges.calls) != 1 || versionChanges.calls[0] != 42 {
		t.Fatalf("Timeline calls = %+v, want one for version 42", versionChanges.calls)
	}
}

// TestVersionChangesEmptyTimelineIsAnArray pins the empty state: a version with
// no batches (or no code repository) returns batchTimeline: [] and still reports
// the legacy change list, never a 500.
func TestVersionChangesEmptyTimelineIsAnArray(t *testing.T) {
	query := &fakeQueryService{versionChangesFn: func(context.Context, int64, string) (domain.ChangeSetData, string, string, error) {
		return domain.ChangeSetData{}, commitA, commitB, nil
	}}
	versionChanges := &fakeVersionChangeService{
		timelineFn: func(context.Context, int64) ([]application.VersionChangeTimelineEntry, error) {
			return nil, nil
		},
	}

	handler := newTestHandler(t, HandlerDeps{Query: query, VersionChanges: versionChanges})
	recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/changes", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	decodeBody(t, recorder, &body)
	raw, ok := body.Data["batchTimeline"]
	if !ok {
		t.Fatalf("data = %v, want batchTimeline", body.Data)
	}
	var timeline []any
	if err := json.Unmarshal(raw, &timeline); err != nil {
		t.Fatalf("batchTimeline = %s, want an array", raw)
	}
	if timeline == nil || len(timeline) != 0 {
		t.Fatalf("batchTimeline = %s, want []", raw)
	}
}

// TestVersionChangesTimelineFailureMapsOntoTheSharedEnvelope pins the failure
// path: a timeline computation failure is a real error (retryable 503), not a
// silently empty array.
func TestVersionChangesTimelineFailureMapsOntoTheSharedEnvelope(t *testing.T) {
	query := &fakeQueryService{versionChangesFn: func(context.Context, int64, string) (domain.ChangeSetData, string, string, error) {
		return domain.ChangeSetData{}, commitA, commitB, nil
	}}
	versionChanges := &fakeVersionChangeService{
		timelineFn: func(context.Context, int64) ([]application.VersionChangeTimelineEntry, error) {
			return nil, errors.New("git diff failed")
		},
	}

	handler := newTestHandler(t, HandlerDeps{Query: query, VersionChanges: versionChanges})
	recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/changes", userToken)
	assertErrorResponse(t, recorder, http.StatusServiceUnavailable, platform.CodeUnavailable, "git_unavailable")
}

// TestVersionChangesCarriesProvisionalAndUnavailableFlags pins the additive
// degraded flags of the batch timeline: the UI needs them to render
// 「映射生成中」/「本批变更暂不可用」 instead of a misleading empty or all-unmapped
// batch.
func TestVersionChangesCarriesProvisionalAndUnavailableFlags(t *testing.T) {
	query := &fakeQueryService{versionChangesFn: func(context.Context, int64, string) (domain.ChangeSetData, string, string, error) {
		return domain.ChangeSetData{}, commitA, commitB, nil
	}}
	versionChanges := &fakeVersionChangeService{
		timelineFn: func(context.Context, int64) ([]application.VersionChangeTimelineEntry, error) {
			return []application.VersionChangeTimelineEntry{
				{
					BatchNo: 1, BatchStatus: "done", Provisional: true,
					Change: domain.VersionChange{
						BatchID: 11, VersionID: 42, BaseCommit: commitA, HeadCommit: commitB,
						ChangedFiles: 2, UnmappedCount: 2,
						Items: []domain.VersionChangeItem{
							{File: "src/api/refund.ts", ChangeKind: domain.VersionChangeModified, Unmapped: true},
						},
					},
				},
				{
					BatchNo: 2, BatchStatus: "done", Unavailable: true,
					Change: domain.VersionChange{
						BatchID: 12, VersionID: 42, BaseCommit: commitB, HeadCommit: versionChangeCommitC,
					},
				},
			}, nil
		},
	}

	handler := newTestHandler(t, HandlerDeps{Query: query, VersionChanges: versionChanges})
	recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/changes", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data struct {
			BatchTimeline []versionChangeWire `json:"batchTimeline"`
		} `json:"data"`
	}
	decodeBody(t, recorder, &body)
	if len(body.Data.BatchTimeline) != 2 {
		t.Fatalf("batchTimeline = %+v, want 2 entries", body.Data.BatchTimeline)
	}
	provisional := body.Data.BatchTimeline[0]
	if !provisional.Provisional || provisional.Unavailable {
		t.Fatalf("batch 1 flags = provisional %v unavailable %v, want provisional only",
			provisional.Provisional, provisional.Unavailable)
	}
	if len(provisional.Items) != 1 || provisional.UnmappedCount != 2 {
		t.Fatalf("provisional batch = %+v, want its computed shell and counters", provisional)
	}
	unavailable := body.Data.BatchTimeline[1]
	if unavailable.Provisional || !unavailable.Unavailable {
		t.Fatalf("batch 2 flags = provisional %v unavailable %v, want unavailable only",
			unavailable.Provisional, unavailable.Unavailable)
	}
	if len(unavailable.Items) != 0 {
		t.Fatalf("unavailable batch items = %+v, want []", unavailable.Items)
	}
}

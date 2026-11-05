package versionadapter_test

// v0.22 W0 fix round 1 (Task 36 review Critical 2): the production
// agentapp.SummaryStore adapter. It forwards the R-A5-12 commit to the version
// ConversationService and maps the stale-watermark sentinel to the agentapp
// contract.

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type fakeCompactionSummaryWriter struct {
	write versionapp.CompactionSummaryWrite
	view  versionapp.SummaryView
	err   error
}

func (f *fakeCompactionSummaryWriter) AppendCompactionSummary(_ context.Context, write versionapp.CompactionSummaryWrite) (versionapp.SummaryView, error) {
	f.write = write
	return f.view, f.err
}

func TestSummaryStoreMapsCompactionCommit(t *testing.T) {
	writer := &fakeCompactionSummaryWriter{view: versionapp.SummaryView{
		ConversationID: 20, Version: 3, CoveredMessageID: 812, UpdatedAt: testTime,
	}}
	store := versionadapter.NewSummaryStore(writer)
	record, err := store.AppendSummary(context.Background(), agentapp.SummaryCommit{
		VersionID: 10, ConversationID: 20, CompactionID: "compaction-20-1",
		Trigger: agentcontext.Pressure, Summary: "nine-section summary",
		PinnedFacts: []string{"fact"}, CoveredMessageID: 812, TokenEstimate: 42,
		SummaryMeta: map[string]any{"trigger": "pressure"},
	})
	if err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}
	if record.Version != 3 || record.CoveredMessageID != 812 {
		t.Fatalf("record = %#v", record)
	}
	if writer.write.ConversationID != 20 || writer.write.CoveredMessageID != 812 ||
		writer.write.CompactionID != "compaction-20-1" || writer.write.Trigger != "pressure" ||
		writer.write.Summary != "nine-section summary" || writer.write.TokenEstimate != 42 {
		t.Fatalf("forwarded write = %#v", writer.write)
	}
}

func TestSummaryStoreMapsStaleWatermark(t *testing.T) {
	writer := &fakeCompactionSummaryWriter{err: version.ErrSummaryWatermarkStale}
	store := versionadapter.NewSummaryStore(writer)
	_, err := store.AppendSummary(context.Background(), agentapp.SummaryCommit{
		ConversationID: 20, Summary: "summary", CoveredMessageID: 100,
	})
	if !errors.Is(err, agentapp.ErrSummaryWatermarkStale) {
		t.Fatalf("error = %v, want ErrSummaryWatermarkStale", err)
	}
}

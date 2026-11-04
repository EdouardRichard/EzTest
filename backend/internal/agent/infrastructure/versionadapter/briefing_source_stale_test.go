package versionadapter

import (
	"context"
	"strings"
	"testing"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
)

// TestBriefingReaderSourceStale pins the R87 read-model contract at the unit
// boundary of the production briefing reader: the reader carries the knowledge
// review status and the read-time source_stale marker through its normalization,
// and the briefing builder never injects an item whose source version is gone
// (or that is not approved), even though the row is still returned by the read
// source. The SQL projection of these fields is covered end-to-end by the
// integration test in briefing_integration_test.go.
func TestBriefingReaderSourceStale(t *testing.T) {
	ctx := context.Background()
	source := agentcontext.Source{
		Knowledge: []agentcontext.KnowledgeSnapshot{
			{ID: 1, Title: "源版本存在知识", Summary: "应注入简报", Status: "approved", SourceStale: false},
			{ID: 2, Title: "源版本已删除知识", Summary: "不应注入简报", Status: "approved", SourceStale: true},
			{ID: 3, Title: "疑似知识", Summary: "不应注入简报", Status: "suspect", SourceStale: false},
			{ID: 4, Title: "无状态知识", Summary: "兼容旧读端", SourceStale: false},
		},
	}
	reader := newBriefingReader(&fakeBriefingSource{source: source})

	loaded, err := reader.Load(ctx, 7)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Knowledge) != 4 {
		t.Fatalf("knowledge rows = %d, want the reader to preserve every row", len(loaded.Knowledge))
	}
	byID := make(map[int64]agentcontext.KnowledgeSnapshot, len(loaded.Knowledge))
	for _, item := range loaded.Knowledge {
		byID[item.ID] = item
	}
	if item, ok := byID[2]; !ok || !item.SourceStale || item.Status != "approved" {
		t.Fatalf("source-stale knowledge snapshot = %#v, want SourceStale=true and the approved status", item)
	}
	if item, ok := byID[3]; !ok || item.SourceStale || item.Status != "suspect" {
		t.Fatalf("suspect knowledge snapshot = %#v, want Status=suspect", item)
	}

	briefing, err := agentcontext.NewBriefingBuilder(reader).BuildBriefing(ctx, 7)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	if len(briefing.Knowledge) != 1 {
		t.Fatalf("knowledge sections = %d, want exactly one", len(briefing.Knowledge))
	}
	content := briefing.Knowledge[0].Content
	if !strings.Contains(content, "源版本存在知识") || !strings.Contains(content, "无状态知识") {
		t.Fatalf("briefing knowledge = %q, want the injectable rows", content)
	}
	for _, hidden := range []string{"源版本已删除知识", "疑似知识"} {
		if strings.Contains(content, hidden) {
			t.Fatalf("briefing injected a non-injectable knowledge row %q: %q", hidden, content)
		}
	}
}

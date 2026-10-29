package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// fakeKnowledgeReader is an in-memory application.KnowledgeReader returning a
// scripted "版本知识" section.
type fakeKnowledgeReader struct {
	section application.KnowledgeSection
	err     error
	calls   int
}

// ApprovedKnowledgeSection records the call and returns the scripted section.
func (f *fakeKnowledgeReader) ApprovedKnowledgeSection(_ context.Context, versionID int64) (application.KnowledgeSection, error) {
	f.calls++
	if f.err != nil {
		return application.KnowledgeSection{}, f.err
	}
	return f.section, nil
}

// knowledgeRunStats decodes one run's stats JSONB document.
func knowledgeRunStats(t *testing.T, run domain.CaseDesignRun) map[string]any {
	t.Helper()
	document := map[string]any{}
	if len(run.Stats) == 0 {
		return document
	}
	if err := json.Unmarshal(run.Stats, &document); err != nil {
		t.Fatalf("decode run stats %q: %v", run.Stats, err)
	}
	return document
}

// TestDesignRunInjectsApprovedKnowledge covers the P5 side of spec §9.4: a
// design run adds the approved "版本知识" segment to the instruction and records
// the injected ids as knowledgeRefs on the run, while the prompt version stays
// p5-design-v1.
func TestDesignRunInjectsApprovedKnowledge(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: runTestVersionID, Title: "Checkout", Status: "clarified"},
	}
	fixture.knowledge = &fakeKnowledgeReader{section: application.KnowledgeSection{
		Text:      "## 版本知识（已批准）\nknowledgeRefs: 501,502\n- kind=pitfall confidence=high title=\"Timeout\"\n  use the fake clock\n",
		Refs:      []int64{501, 502},
		Truncated: true,
	}}
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	if err != nil {
		t.Fatalf("start design run: %v", err)
	}
	if run.PromptVersion != domain.PromptVersionDesign {
		t.Fatalf("prompt version = %q, want %q", run.PromptVersion, domain.PromptVersionDesign)
	}
	if len(fixture.texts) != 1 || !strings.Contains(fixture.texts[0], "## 版本知识（已批准）") ||
		!strings.Contains(fixture.texts[0], "knowledgeRefs: 501,502") {
		t.Fatalf("design instruction did not carry the knowledge section: %q", fixture.texts)
	}
	stats := knowledgeRunStats(t, run)
	refs, ok := stats["knowledgeRefs"].([]any)
	if !ok || len(refs) != 2 || refs[0] != "501" || refs[1] != "502" {
		t.Fatalf("stats knowledgeRefs = %#v", stats["knowledgeRefs"])
	}
	if stats["knowledgeTruncated"] != true {
		t.Fatalf("stats knowledgeTruncated = %#v", stats["knowledgeTruncated"])
	}
	warnings, ok := stats["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("stats warnings = %#v", stats["warnings"])
	}
	warning, _ := warnings[0].(map[string]any)
	if warning["code"] != "knowledge_truncated" {
		t.Fatalf("warning = %#v", warning)
	}
}

// TestReviewAndDataPlanRunsDoNotInjectKnowledge pins the P5 design-only rule:
// review and data_plan runs neither read nor render the version knowledge.
func TestReviewAndDataPlanRunsDoNotInjectKnowledge(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{domain.RunKindReview, domain.RunKindDataPlan} {
		t.Run(kind, func(t *testing.T) {
			fixture := newRunFixture()
			fixture.confirmed = []application.RequirementSnapshot{
				{ID: 7, VersionID: runTestVersionID, Title: "Checkout", Status: "clarified"},
			}
			reader := &fakeKnowledgeReader{section: application.KnowledgeSection{Text: "## 版本知识\nleak"}}
			fixture.knowledge = reader
			service := fixture.service()

			run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: kind})
			if err != nil {
				t.Fatalf("start %s run: %v", kind, err)
			}
			if reader.calls != 0 {
				t.Fatalf("%s run read the knowledge section %d times", kind, reader.calls)
			}
			if len(fixture.texts) != 1 || strings.Contains(fixture.texts[0], "版本知识") {
				t.Fatalf("%s instruction = %q", kind, fixture.texts)
			}
			if _, present := knowledgeRunStats(t, run)["knowledgeRefs"]; present {
				t.Fatalf("%s run stats = %s", kind, run.Stats)
			}
		})
	}
}

// TestDesignRunKnowledgeReadFailureDegradesGracefully pins that an unavailable
// knowledge read never blocks a design run: the run starts without a section.
func TestDesignRunKnowledgeReadFailureDegradesGracefully(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: runTestVersionID, Title: "Checkout", Status: "clarified"},
	}
	fixture.knowledge = &fakeKnowledgeReader{err: errors.New("database down")}
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	if err != nil {
		t.Fatalf("design run with a failing knowledge read: %v", err)
	}
	if strings.Contains(fixture.texts[0], "版本知识") {
		t.Fatalf("instruction = %q", fixture.texts[0])
	}
	if _, present := knowledgeRunStats(t, run)["knowledgeRefs"]; present {
		t.Fatalf("run stats = %s", run.Stats)
	}
}

// TestDesignRunWithoutApprovedKnowledgeKeepsTheLegacyInstruction pins that an
// empty section leaves the instruction and stats unchanged.
func TestDesignRunWithoutApprovedKnowledgeKeepsTheLegacyInstruction(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: runTestVersionID, Title: "Checkout", Status: "clarified"},
	}
	reader := &fakeKnowledgeReader{section: application.KnowledgeSection{}}
	fixture.knowledge = reader
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	if err != nil {
		t.Fatalf("start design run: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("knowledge reader calls = %d, want 1", reader.calls)
	}
	if strings.Contains(fixture.texts[0], "版本知识") {
		t.Fatalf("instruction = %q", fixture.texts[0])
	}
	if _, present := knowledgeRunStats(t, run)["knowledgeRefs"]; present {
		t.Fatalf("run stats = %s", run.Stats)
	}
}

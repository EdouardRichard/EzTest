package agentcontext_test

import (
	"context"
	"strings"
	"testing"
	"time"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// authorNarrativeFindings seeds the findings group with the two fact families
// that actually reach it: an agent completion fact (kind=progress, whose
// payload.summary IS the author's own conclusion — task.complete /
// settlement write it) and an objective version fact (a discovered page settled
// by an admin resource approval).
func authorNarrativeFindings() []agentcontext.FactSnapshot {
	base := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	return []agentcontext.FactSnapshot{
		{
			ID: 9101, Kind: version.FactKindProgress, Key: "task.complete:run:9001",
			Summary: "作者完成结论：登录用例已覆盖全部验收标准，质量达标。", CreatedAt: base.Add(2 * time.Minute),
		},
		{
			ID: 9102, Kind: "page", Key: "page:5001",
			Summary: "已批准页面：/login", CreatedAt: base.Add(time.Minute),
		},
		{
			ID: 9103, Kind: version.FactKindProgress, Key: "task.complete:run:9002",
			Summary: "作者结算结论：异常路径已补齐，无需再设计。", CreatedAt: base,
		},
	}
}

// TestBriefingWithoutAgentNarrativeDropsAuthorConclusions is the whole-branch
// review Important #3: the independent judge's briefing must not carry the
// author's own completion narrative. Stripping only the handoff group left the
// findings group rendering every current version_fact — including kind=progress,
// whose summary is the agent's conclusion — which anchors the very judgement
// I-B4 keeps independent.
func TestBriefingWithoutAgentNarrativeDropsAuthorConclusions(t *testing.T) {
	source := agentcontext.Source{
		Version:  agentcontext.VersionSnapshot{Goal: "订单系统", Stage: "designing"},
		Findings: authorNarrativeFindings(),
		Handoff:  &agentcontext.HandoffSnapshot{Revision: 1, Document: "澄清交接包正文"},
	}
	builder := agentcontext.NewBriefingBuilder(&fakeBriefingReader{source: source})
	briefing, err := builder.BuildBriefing(context.Background(), 101)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}

	// Control: the unfiltered briefing DOES carry the author conclusions, so the
	// fixture is discriminating (a starving fixture would prove nothing).
	full := briefing.Render()
	for _, marker := range []string{"作者完成结论", "作者结算结论", "已批准页面"} {
		if !strings.Contains(full, marker) {
			t.Fatalf("unfiltered briefing does not carry %q; the fixture is not discriminating:\n%s", marker, full)
		}
	}

	judge := briefing.WithoutHandoff().WithoutAgentNarrative().Render()
	for _, marker := range []string{"作者完成结论", "作者结算结论", "task.complete:run:", "[" + version.FactKindProgress + "]"} {
		if strings.Contains(judge, marker) {
			t.Fatalf("judge briefing leaked the author completion narrative %q:\n%s", marker, judge)
		}
	}
	// Objective version facts stay: the judge still sees the approved resource.
	if !strings.Contains(judge, "已批准页面") {
		t.Fatalf("judge briefing dropped an objective version fact:\n%s", judge)
	}
	if strings.Contains(judge, "澄清交接包正文") {
		t.Fatalf("judge briefing still carries the handoff document:\n%s", judge)
	}
	// The value receiver must not mutate the original briefing.
	if again := briefing.Render(); again != full {
		t.Fatal("WithoutAgentNarrative mutated the receiver briefing")
	}
}

// TestBriefingWithoutAgentNarrativeRederivesProtectedPrefix pins the subtle half
// of the filter: the findings group has a PROTECTED top-3 prefix that no
// truncation may cut. Filtering the rendered text (or leaving the stale mandatory
// prefix behind) would either resurrect the dropped facts or protect content that
// is no longer in the body. The prefix must be re-derived from the filtered set.
func TestBriefingWithoutAgentNarrativeRederivesProtectedPrefix(t *testing.T) {
	// Six objective facts plus three author conclusions: the author facts rank
	// first by recency, so the unfiltered protected top-3 is entirely author
	// narrative.
	base := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	findings := authorNarrativeFindings()
	for index := 0; index < 6; index++ {
		findings = append(findings, agentcontext.FactSnapshot{
			ID: int64(9200 + index), Kind: "page", Key: "page:" + string(rune('a'+index)),
			Summary: "已批准页面：/p" + string(rune('a'+index)), CreatedAt: base.Add(-time.Duration(index) * time.Minute),
		})
	}
	builder := agentcontext.NewBriefingBuilder(&fakeBriefingReader{source: agentcontext.Source{
		Version:  agentcontext.VersionSnapshot{Goal: "订单系统", Stage: "designing"},
		Findings: findings,
	}})
	briefing, err := builder.BuildBriefing(context.Background(), 101)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	judge := briefing.WithoutAgentNarrative()
	rendered := judge.Render()
	if strings.Contains(rendered, "作者") {
		t.Fatalf("judge briefing leaked author narrative through the protected prefix:\n%s", rendered)
	}
	// The protected prefix is now the top-3 OBJECTIVE facts, and it is a literal
	// prefix of the rendered findings body (the Render contract).
	body := judge.Findings[0].Content
	if !strings.HasPrefix(body, "- [page] page:5001：已批准页面：/login") {
		t.Fatalf("filtered findings body does not start with the newest objective fact:\n%s", body)
	}
	if strings.Contains(body, version.FactKindProgress) {
		t.Fatalf("filtered findings body still names the progress kind:\n%s", body)
	}
}

// TestBriefingWithoutAgentNarrativeEmptyGroup keeps the "no data" rendering: an
// all-author findings group renders noneText, not an empty section.
func TestBriefingWithoutAgentNarrativeEmptyGroup(t *testing.T) {
	builder := agentcontext.NewBriefingBuilder(&fakeBriefingReader{source: agentcontext.Source{
		Version:  agentcontext.VersionSnapshot{Goal: "订单系统", Stage: "designing"},
		Findings: []agentcontext.FactSnapshot{authorNarrativeFindings()[0]},
	}})
	briefing, err := builder.BuildBriefing(context.Background(), 101)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	judge := briefing.WithoutAgentNarrative()
	if got := judge.Findings[0].Content; got != "无" {
		t.Fatalf("emptied findings group = %q, want 无", got)
	}
	if strings.Contains(judge.Render(), "作者完成结论") {
		t.Fatalf("emptied findings group still rendered the author conclusion:\n%s", judge.Render())
	}
}

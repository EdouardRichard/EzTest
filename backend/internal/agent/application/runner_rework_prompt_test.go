package agentapp_test

import (
	"encoding/json"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// reworkFindingsSummary is the production findings-summary shape the design
// scheduler puts into the design.rework payload (judge_scheduler.go
// handleJudgeFail -> judgmentFindingSummary: "<dimension>: <message>" joined
// by "; ", minor findings skipped). The author must SEE this text to know what
// to fix (SC-E4); before the T19 fix round 1 the rework task context rendered
// neither the findings nor the round.
const reworkFindingsSummary = "coverage: 用例未覆盖该需求验收标准的反向/边界场景，且断言不足以证明 AC 的 then 成立；请补齐覆盖与可观察断言。"

// designReworkWorkItem builds a design.rework work item carrying the REAL
// scheduler payload (handleJudgeFail): requirement/case ids as decimal strings,
// round = the next judgment round, findings = the judge findings summary. It
// deliberately also carries a conversation_id so the context-scoping test
// exercises the bounded authoring path even if a future scheduler attaches one.
func designReworkWorkItem(conversationID int64) agent.WorkItem {
	payload := map[string]any{
		"requirement_id":         "501",
		"requirement_version_id": "700",
		"case_id":                "7001",
		"case_version_id":        "8001",
		"round":                  2,
		"findings":               reworkFindingsSummary,
	}
	if conversationID > 0 {
		payload["conversation_id"] = conversationID
	}
	raw, _ := json.Marshal(payload)
	item := fakeWorkItem(agentapp.WorkTypeDesignRework, agentapp.WorkItemPayload{})
	item.Payload = raw
	item.Team = agent.TeamDesign
	return item
}

// TestDesignReworkPayloadParsesFindingsAndRound pins the payload contract: the
// scheduler's round/findings fields must survive ParseWorkItemPayload so the
// task context can render them (the pre-fix parser dropped both silently).
func TestDesignReworkPayloadParsesFindingsAndRound(t *testing.T) {
	payload, err := agentapp.ParseWorkItemPayload(designReworkWorkItem(0).Payload)
	if err != nil {
		t.Fatalf("parse rework payload: %v", err)
	}
	if payload.CaseID != 7001 || payload.CaseVersionID != 8001 {
		t.Fatalf("rework payload case binding = %d/%d, want 7001/8001", payload.CaseID, payload.CaseVersionID)
	}
	if payload.ReworkRound != 2 {
		t.Fatalf("ReworkRound = %d, want 2 (the scheduler's next judgment round)", payload.ReworkRound)
	}
	if payload.ReworkFindings != reworkFindingsSummary {
		t.Fatalf("ReworkFindings = %q, want the judge findings summary %q", payload.ReworkFindings, reworkFindingsSummary)
	}
}

// TestDesignReworkPromptRendersFindingsAndRound is the SC-E4 rework-quality
// pin: a real LLM author must see WHY the case failed. The rendered prompt of a
// design.rework run must carry the judge findings text, the rework round and a
// revision goal — not the generic "请根据当前任务继续执行。" default. RED before
// the fix (findings/round absent), GREEN after; it guards the rendering against
// silent regression.
func TestDesignReworkPromptRendersFindingsAndRound(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已按裁判 findings 修订并重新提交 draft。"}}

	if _, err := mustRun(t, fixture, designReworkWorkItem(0)); err != nil {
		t.Fatalf("rework run: %v", err)
	}
	prompt := requestPrompt(fixture.chat.request(0))

	if !strings.Contains(prompt, reworkFindingsSummary) {
		t.Fatalf("rework prompt does not render the judge findings summary (SC-E4 author is blind):\n%s", prompt)
	}
	if !strings.Contains(prompt, "rework_round=2") {
		t.Fatalf("rework prompt does not render the rework round:\n%s", prompt)
	}
	if !strings.Contains(prompt, "打回") {
		t.Fatalf("rework prompt has no revision goal naming the judge rejection:\n%s", prompt)
	}
	if !strings.Contains(prompt, "case.graph_propose") {
		t.Fatalf("rework prompt does not name the resubmission tool:\n%s", prompt)
	}
	if strings.Contains(prompt, "请根据当前任务继续执行。") {
		t.Fatalf("rework prompt fell back to the generic default user message:\n%s", prompt)
	}
	// The scoped case binding must survive alongside the findings.
	if !strings.Contains(prompt, "case_id=7001") || !strings.Contains(prompt, "case_version_id=8001") {
		t.Fatalf("rework prompt lost the scoped case binding:\n%s", prompt)
	}
}

// TestDesignReworkContextIsBounded folds the Task 79 scoping onto design.rework
// (T19 fix round 1 Minor#4): a rework run that carries a design conversation id
// must get the bounded authoring context (newest human instructions only, newest
// clipped team-room posts), not the design-Host replay of previous authors'
// projections. RED before the fix (rework was not an authoring work type, so the
// design-conversation branch replayed the Host history including author
// projections), GREEN after.
func TestDesignReworkContextIsBounded(t *testing.T) {
	fixture := newDesignFloodFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已按裁判 findings 修订并重新提交 draft。"}}

	if _, err := mustRun(t, fixture, designReworkWorkItem(20)); err != nil {
		t.Fatalf("rework run: %v", err)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	packet := decodeStoredPacket(t, fixture.repo.packets[0])

	history := historySegments(packet)
	if len(history) != 3 {
		t.Fatalf("rework replayed %d history messages, want the newest 3 human instructions", len(history))
	}
	for _, segment := range history {
		if segment.Role != "user" {
			t.Fatalf("rework history contains a %q message, want only human instructions", segment.Role)
		}
	}
	if strings.Contains(prompt, "历史作者摘要") {
		t.Fatal("previous author projections are replayed into a rework prompt")
	}
	for _, kept := range []string{"人工指令三", "人工指令四", "人工指令五"} {
		if !strings.Contains(prompt, kept) {
			t.Fatalf("%q (a retained human instruction) was dropped from the rework prompt", kept)
		}
	}
	assertNewestTeamPostsKept(t, prompt)
	// The findings must survive the bounded context (they ride the task block,
	// not the replayed history).
	if !strings.Contains(prompt, reworkFindingsSummary) {
		t.Fatal("the bounded rework context lost the judge findings summary")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type fakeJudgeRepo struct{ graph design.GraphData }

func (f *fakeJudgeRepo) ReadGraph(_ context.Context, _ int64) (design.GraphData, error) {
	return f.graph, nil
}
func (f *fakeJudgeRepo) ReadCase(_ context.Context, _, _ int64, _ *int64) (design.CaseData, error) {
	return design.CaseData{}, nil
}

type fakeJudgeSettings struct{ maxRounds int64 }

func (f *fakeJudgeSettings) Int(_ context.Context, _ string) (int64, error) { return f.maxRounds, nil }

// TestBuildDesignJudgeWiringSmoke is the T13 production-wiring smoke test: the
// judge components are constructed non-nil, the shared review gate is FUNCTIONAL
// (it opens on an empty graph rather than failing closed as an unwired port),
// and the SAME review-gate instance is accepted by both advance edges (planner
// converge + version manual advance) plus the confirmation gate. This is what
// turns the quality gate from fail-closed/no-op into live production wiring.
func TestBuildDesignJudgeWiringSmoke(t *testing.T) {
	repo := &fakeJudgeRepo{}
	settings := &fakeJudgeSettings{maxRounds: 1}
	c := buildDesignJudge(nil, nil, nil, nil, settings, repo, nil)

	if c.Store == nil || c.Service == nil || c.Gate == nil || c.ReviewGate == nil || c.Exhausted == nil {
		t.Fatalf("judge components must all be wired: %#v", c)
	}

	// The shared review gate is functional: an empty graph has no activated
	// requirement, so the gate opens (nil), NOT the unwired 503.
	if err := c.ReviewGate.ReviewGate(context.Background(), 1001); err != nil {
		t.Fatalf("wired review gate must open on an empty graph, got %v", err)
	}
	// Contrast: an unwired review gate fails closed (proves the wiring matters).
	unwired := designapp.NewReviewGateService(nil, nil)
	if err := unwired.ReviewGate(context.Background(), 1001); err == nil {
		t.Fatal("unwired review gate must fail closed")
	}

	// Both advance edges accept the SAME review-gate instance (one predicate).
	planner := designapp.NewPlanner(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if got := planner.SetJudgeScheduler(c.Store, settings); got != planner {
		t.Fatal("SetJudgeScheduler must return the planner")
	}
	// The escalation-budget reader is OPTIONAL and its nil fallback is silent,
	// so the wiring itself is what must be guarded: both consumers accept the
	// composition-root reader (spec SC-E4 no-permanent-pending).
	budget := &judgeReworkBudgetReader{source: &judgeReworkSQLSource{}}
	if got := planner.SetReworkBudgetReader(budget); got != planner {
		t.Fatal("SetReworkBudgetReader must return the planner")
	}
	if got := c.Service.SetReworkBudgetReader(budget); got != c.Service {
		t.Fatal("SetReworkBudgetReader must return the judgment service")
	}
	if got := c.Service.SetGraphReader(repo); got != c.Service {
		t.Fatal("SetGraphReader must return the judgment service")
	}
	if got := planner.SetReviewGate(c.ReviewGate); got != planner {
		t.Fatal("SetReviewGate must return the planner")
	}
	stageService := versionapp.NewStageService(versionapp.StageServiceDeps{})
	if got := stageService.SetDesignReviewGate(c.ReviewGate); got != stageService {
		t.Fatal("SetDesignReviewGate must return the stage service")
	}
	// The confirmation gate installs onto the graph service.
	graphService := designapp.NewGraphService(nil, nil, nil, nil, nil, nil)
	if got := graphService.SetJudgmentGate(c.Gate); got != graphService {
		t.Fatal("SetJudgmentGate must return the graph service")
	}
}

type fakeJudgeModelSettings struct {
	value string
	err   error
}

func (f *fakeJudgeModelSettings) String(_ context.Context, _ string) (string, error) {
	return f.value, f.err
}

type fakeJudgeModelCatalog struct {
	resolved string
	err      error
}

func (f *fakeJudgeModelCatalog) ResolveModel(_ context.Context, _ int64, modelID string, _ ...llmapp.ModelRequirements) (llmapp.ModelInfo, error) {
	f.resolved = modelID
	return llmapp.ModelInfo{}, f.err
}

type fakeJudgeProfileWriter struct {
	calls  int
	key    string
	update agent.ProfileUpdate
}

func (f *fakeJudgeProfileWriter) UpdateProfile(_ context.Context, key string, update agent.ProfileUpdate) (agent.AgentProfile, bool, error) {
	f.calls++
	f.key = key
	f.update = update
	return agent.AgentProfile{}, true, nil
}

// TestWireJudgeModel pins the design.judge_model -> design_judge profile.Model
// reconciliation: a non-empty configured model is validated and written, an empty
// value clears to nil (inherit default), and an invalid model fails closed
// without writing.
func TestWireJudgeModel(t *testing.T) {
	t.Run("non-empty configured model is written", func(t *testing.T) {
		settings := &fakeJudgeModelSettings{value: "judge-model-x"}
		catalog := &fakeJudgeModelCatalog{}
		writer := &fakeJudgeProfileWriter{}
		if err := wireJudgeModel(context.Background(), settings, catalog, writer); err != nil {
			t.Fatalf("wireJudgeModel: %v", err)
		}
		if catalog.resolved != "judge-model-x" {
			t.Fatalf("model not validated against catalog: %q", catalog.resolved)
		}
		if writer.calls != 1 || writer.key != agentapp.ProfileKeyDesignJudge {
			t.Fatalf("update calls/key = %d/%q", writer.calls, writer.key)
		}
		if !writer.update.SetModel || writer.update.Model == nil || *writer.update.Model != "judge-model-x" {
			t.Fatalf("update = %#v", writer.update)
		}
	})
	t.Run("empty clears to default", func(t *testing.T) {
		settings := &fakeJudgeModelSettings{value: "  "}
		writer := &fakeJudgeProfileWriter{}
		if err := wireJudgeModel(context.Background(), settings, &fakeJudgeModelCatalog{}, writer); err != nil {
			t.Fatalf("wireJudgeModel: %v", err)
		}
		if !writer.update.SetModel || writer.update.Model != nil {
			t.Fatalf("empty setting must clear model to nil: %#v", writer.update)
		}
	})
	t.Run("invalid model fails closed without writing", func(t *testing.T) {
		settings := &fakeJudgeModelSettings{value: "not-a-model"}
		catalog := &fakeJudgeModelCatalog{err: errors.New("unknown model")}
		writer := &fakeJudgeProfileWriter{}
		if err := wireJudgeModel(context.Background(), settings, catalog, writer); err == nil {
			t.Fatal("invalid model must fail closed")
		}
		if writer.calls != 0 {
			t.Fatalf("invalid model must not write, calls = %d", writer.calls)
		}
	})
}

// fakeReworkSource is the targeted row source the rework provenance reader
// needs (satisfied by judgeReworkSQLSource in production). It supplies rows
// only — the matching strategy under test (from-version binding, no status
// filter, WorkTypeForItem convention) lives in the reader itself.
type fakeReworkSource struct {
	items        []agent.WorkItem
	err          error
	gotVersionID int64
	gotCaseID    int64
}

func (f *fakeReworkSource) ReworkItemsForCase(_ context.Context, versionID, caseID int64) ([]agent.WorkItem, error) {
	f.gotVersionID = versionID
	f.gotCaseID = caseID
	return f.items, f.err
}

// TestJudgeReworkRequestedAt pins the T14 fix-round-1 round-provenance read
// (SC-E4: a Host edit rejudges at the SAME round, only a judge-triggered
// rework increments it). The rework payload's case_version_id is the FAILED
// VERSION BEING REWORKED FROM (handleJudgeFail, dedupe rework:<case>:<ver>);
// matching that from-version identifies the rework responding to one specific
// fail. STATUS IS IRRELEVANT: the request exists at creation, and the judge is
// enqueued by the reconciler while the rework item is still open/running —
// filtering on terminal status let the judge settle at round 0 and silently
// bypass the rework cap (review Important).
func TestJudgeReworkRequestedAt(t *testing.T) {
	// reworkPayload responds to the fail on version 8001 of case 7001.
	reworkPayload, _ := json.Marshal(map[string]any{"case_id": "7001", "case_version_id": "8001", "round": 2})
	olderVersionPayload, _ := json.Marshal(map[string]any{"case_id": "7001", "case_version_id": "7999", "round": 1})
	otherCasePayload, _ := json.Marshal(map[string]any{"case_id": "7002", "case_version_id": "8003", "round": 2})
	base := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	const versionID, caseID, failedVersion = int64(1001), int64(7001), int64(8001)

	t.Run("no rework item returns zero", func(t *testing.T) {
		src := &fakeReworkSource{}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.IsZero() {
			t.Fatalf("got %v err %v, want zero time nil error", got, err)
		}
		if src.gotVersionID != versionID || src.gotCaseID != caseID {
			t.Fatalf("source queried with (%d,%d), want (%d,%d)", src.gotVersionID, src.gotCaseID, versionID, caseID)
		}
	})
	t.Run("matching done rework item returns its creation time", func(t *testing.T) {
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemDone, Payload: reworkPayload, CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.Equal(base) {
			t.Fatalf("got %v err %v, want %v", got, err, base)
		}
	})
	t.Run("most recent matching item wins over an older one", func(t *testing.T) {
		older := base.Add(-time.Hour)
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemDone, Payload: reworkPayload, CreatedAt: base},
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemDone, Payload: reworkPayload, CreatedAt: older},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.Equal(base) {
			t.Fatalf("got %v err %v, want the newest %v", got, err, base)
		}
	})
	t.Run("non-terminal rework responding to latest fail counts", func(t *testing.T) {
		// Fix round 1 (review Important): the judge is enqueued by the reconciler
		// as soon as the rework author's case.graph_propose settles a draft —
		// WHILE the design.rework item is still RUNNING. The request exists at
		// creation; skipping non-terminal items let the judge settle at round 0
		// and silently bypass the SC-E4 rework cap.
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemRunning, Payload: reworkPayload, CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.Equal(base) {
			t.Fatalf("got %v err %v, want %v (a running rework request still consumes the round)", got, err, base)
		}
	})
	t.Run("open rework item counts (race window)", func(t *testing.T) {
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemOpen, Payload: reworkPayload, CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.Equal(base) {
			t.Fatalf("got %v err %v, want %v (open rework request counts)", got, err, base)
		}
	})
	t.Run("rework bound to an older version does not answer for the latest fail", func(t *testing.T) {
		// From-version binding: a rework responding to an EARLIER fail (7999)
		// is not provenance for the latest fail (8001).
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemDone, Payload: olderVersionPayload, CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.IsZero() {
			t.Fatalf("got %v err %v, want zero (rework bound to an older fail)", got, err)
		}
	})
	t.Run("host edit between rework request and rework-draft judgment", func(t *testing.T) {
		// A rework was requested from 8001, then a Host edit landed 8002 and was
		// judged FAIL (the new latest fail). No rework responds to 8002 yet, so
		// the provenance for the latest fail is zero — the next judgment of a
		// Host-edited version keeps its round. The stale 8001-bound request must
		// not answer for the 8002 fail.
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemDone, Payload: reworkPayload, CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, 8002)
		if err != nil || !got.IsZero() {
			t.Fatalf("got %v err %v, want zero (no rework responds to the 8002 fail)", got, err)
		}
		// The same request DOES answer for its own fail (8001).
		got, err = reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.Equal(base) {
			t.Fatalf("got %v err %v, want %v for the 8001 fail", got, err, base)
		}
	})
	t.Run("different case is ignored", func(t *testing.T) {
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemDone, Payload: otherCasePayload, CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.IsZero() {
			t.Fatalf("got %v err %v, want zero (different case)", got, err)
		}
	})
	t.Run("different work type is ignored", func(t *testing.T) {
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignJudge, Status: agent.WorkItemDone, Payload: reworkPayload, CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.IsZero() {
			t.Fatalf("got %v err %v, want zero (judge item, not rework)", got, err)
		}
	})
	t.Run("kind-authoritative work type is honored", func(t *testing.T) {
		// WorkTypeForItem convention: a row whose Kind is set is classified by
		// Kind, not Type — a contract-kind row typed design.rework is not a
		// rework under the single effective-work-type convention.
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Kind: "design.cases", Status: agent.WorkItemDone, Payload: reworkPayload, CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.IsZero() {
			t.Fatalf("got %v err %v, want zero (kind authoritative)", got, err)
		}
	})
	t.Run("malformed payload is skipped", func(t *testing.T) {
		src := &fakeReworkSource{items: []agent.WorkItem{
			{Type: designapp.TaskTypeDesignRework, Status: agent.WorkItemDone, Payload: json.RawMessage(`{"case_id":"not-a-number"}`), CreatedAt: base},
		}}
		reader := &judgeReworkWorkItemReader{source: src}
		got, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion)
		if err != nil || !got.IsZero() {
			t.Fatalf("got %v err %v, want zero (unparsable payload skipped)", got, err)
		}
	})
	t.Run("nil source fails closed", func(t *testing.T) {
		reader := &judgeReworkWorkItemReader{}
		if _, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion); err == nil {
			t.Fatal("nil source must fail closed, not silently return zero")
		}
	})
	t.Run("source read error propagates", func(t *testing.T) {
		src := &fakeReworkSource{err: errors.New("db down")}
		reader := &judgeReworkWorkItemReader{source: src}
		if _, err := reader.ReworkRequestedAt(context.Background(), versionID, caseID, failedVersion); err == nil {
			t.Fatal("source read error must propagate")
		}
	})
}

// TestJudgeReworkBudgetReader pins the ESCALATION rework budget read (spec
// SC-E4: the rework budget derives from the logical case's TERMINAL
// design.rework work items, not from max(round)-1). A rework that retries to
// exhaustion is terminal but produces no new judgment round, so the
// round-derived count never sees it and the case strands in reworking forever.
// "Terminal" here means BUDGET-CONSUMING: done, dead, or failed with attempts
// at the cap. A retryable failed item, and every in-flight status, still has
// work owed to the case and must not spend the budget
// (WorkItemStatus.Terminal() alone is wrong: it is true for retryable failed).
func TestJudgeReworkBudgetReader(t *testing.T) {
	const versionID, caseID = int64(1001), int64(7001)
	payload, _ := json.Marshal(map[string]any{"case_id": "7001", "case_version_id": "8001", "round": 2})
	otherCasePayload, _ := json.Marshal(map[string]any{"case_id": "7002", "case_version_id": "8003", "round": 2})
	base := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	rework := func(status agent.WorkItemStatus, attempts, maxAttempts int) agent.WorkItem {
		return agent.WorkItem{
			Type: designapp.TaskTypeDesignRework, Status: status,
			Attempts: attempts, MaxAttempts: maxAttempts, Payload: payload, CreatedAt: base,
		}
	}

	t.Run("budget-consuming statuses count", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			item agent.WorkItem
			want int
		}{
			{"done", rework(agent.WorkItemDone, 1, 3), 1},
			{"dead", rework(agent.WorkItemDead, 3, 3), 1},
			{"failed at the attempt cap", rework(agent.WorkItemFailed, 3, 3), 1},
			{"failed above the attempt cap", rework(agent.WorkItemFailed, 4, 3), 1},
			{"retryable failed", rework(agent.WorkItemFailed, 1, 3), 0},
			{"open", rework(agent.WorkItemOpen, 0, 3), 0},
			{"claimed", rework(agent.WorkItemClaimed, 1, 3), 0},
			{"running", rework(agent.WorkItemRunning, 1, 3), 0},
			{"waiting_human", rework(agent.WorkItemWaitingHuman, 1, 3), 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				src := &fakeReworkSource{items: []agent.WorkItem{tc.item}}
				reader := &judgeReworkBudgetReader{source: src}
				got, err := reader.TerminalReworkCount(context.Background(), versionID, caseID)
				if err != nil {
					t.Fatalf("TerminalReworkCount: %v", err)
				}
				if got != tc.want {
					t.Fatalf("terminal rework count = %d, want %d", got, tc.want)
				}
				if src.gotVersionID != versionID || src.gotCaseID != caseID {
					t.Fatalf("source queried with (%d,%d), want (%d,%d)", src.gotVersionID, src.gotCaseID, versionID, caseID)
				}
			})
		}
	})
	t.Run("every terminal rework of the case counts", func(t *testing.T) {
		src := &fakeReworkSource{items: []agent.WorkItem{
			rework(agent.WorkItemDead, 3, 3),
			rework(agent.WorkItemDone, 1, 3),
			rework(agent.WorkItemRunning, 1, 3),
		}}
		reader := &judgeReworkBudgetReader{source: src}
		got, err := reader.TerminalReworkCount(context.Background(), versionID, caseID)
		if err != nil || got != 2 {
			t.Fatalf("got %d err %v, want 2 (dead + done; the running one is still owed)", got, err)
		}
	})
	t.Run("no rework items is zero", func(t *testing.T) {
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{}}
		got, err := reader.TerminalReworkCount(context.Background(), versionID, caseID)
		if err != nil || got != 0 {
			t.Fatalf("got %d err %v, want 0/nil", got, err)
		}
	})
	t.Run("another case does not count", func(t *testing.T) {
		item := rework(agent.WorkItemDead, 3, 3)
		item.Payload = otherCasePayload
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{items: []agent.WorkItem{item}}}
		got, err := reader.TerminalReworkCount(context.Background(), versionID, caseID)
		if err != nil || got != 0 {
			t.Fatalf("got %d err %v, want 0 (different case)", got, err)
		}
	})
	t.Run("another work type does not count", func(t *testing.T) {
		item := rework(agent.WorkItemDead, 3, 3)
		item.Type = designapp.TaskTypeDesignJudge
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{items: []agent.WorkItem{item}}}
		got, err := reader.TerminalReworkCount(context.Background(), versionID, caseID)
		if err != nil || got != 0 {
			t.Fatalf("got %d err %v, want 0 (judge item, not rework)", got, err)
		}
	})
	t.Run("kind-authoritative work type is honored", func(t *testing.T) {
		item := rework(agent.WorkItemDead, 3, 3)
		item.Kind = "design.cases"
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{items: []agent.WorkItem{item}}}
		got, err := reader.TerminalReworkCount(context.Background(), versionID, caseID)
		if err != nil || got != 0 {
			t.Fatalf("got %d err %v, want 0 (kind authoritative)", got, err)
		}
	})
	t.Run("malformed payload is skipped", func(t *testing.T) {
		item := rework(agent.WorkItemDead, 3, 3)
		item.Payload = json.RawMessage(`{"case_id":"not-a-number"}`)
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{items: []agent.WorkItem{item}}}
		got, err := reader.TerminalReworkCount(context.Background(), versionID, caseID)
		if err != nil || got != 0 {
			t.Fatalf("got %d err %v, want 0 (unparsable payload skipped)", got, err)
		}
	})
	t.Run("nil source fails closed", func(t *testing.T) {
		reader := &judgeReworkBudgetReader{}
		if _, err := reader.TerminalReworkCount(context.Background(), versionID, caseID); err == nil {
			t.Fatal("nil source must fail closed, never silently report zero budget spent")
		}
	})
	t.Run("nil reader fails closed", func(t *testing.T) {
		var reader *judgeReworkBudgetReader
		if _, err := reader.TerminalReworkCount(context.Background(), versionID, caseID); err == nil {
			t.Fatal("nil reader must fail closed")
		}
	})
	t.Run("source read error propagates", func(t *testing.T) {
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{err: errors.New("db down")}}
		if _, err := reader.TerminalReworkCount(context.Background(), versionID, caseID); err == nil {
			t.Fatal("source read error must propagate")
		}
	})
}

// TestJudgeReworkTerminalForVersion pins the second half of the
// no-permanent-pending invariant (spec SC-E4): the rework dedupe key is
// rework:<case>:<failedVersion> with NO generation, and Ensure never revives a
// terminal row. So once the rework responding to the LATEST fail is TERMINAL, no
// further rework can ever be created for that fail — even when
// design.max_rework_rounds leaves budget. Terminal = done OR dead OR failed at
// the attempt cap: a rework that DIED and a rework that completed DONE WITHOUT
// proposing a revision (DEV-E4) both strand the loop. A rework that DID revise
// advances the current version, so the fail is no longer bound to it and the
// normal loop never consults this fact. In-flight and retryable-failed reworks
// still owe work.
func TestJudgeReworkTerminalForVersion(t *testing.T) {
	const versionID, caseID, failedVersion = int64(1001), int64(7001), int64(8001)
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	payloadFor := func(caseID, caseVersionID int64) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"case_id":         fmt.Sprint(caseID),
			"case_version_id": fmt.Sprint(caseVersionID),
			"round":           2,
		})
		return raw
	}
	rework := func(status agent.WorkItemStatus, attempts, maxAttempts int, payload json.RawMessage) agent.WorkItem {
		return agent.WorkItem{
			Type: designapp.TaskTypeDesignRework, Status: status,
			Attempts: attempts, MaxAttempts: maxAttempts, Payload: payload, CreatedAt: base,
		}
	}
	thisFail := payloadFor(caseID, failedVersion)

	for _, tc := range []struct {
		name string
		item agent.WorkItem
		want bool
	}{
		{"dead rework bound to this fail", rework(agent.WorkItemDead, 3, 3, thisFail), true},
		{"failed at the attempt cap", rework(agent.WorkItemFailed, 3, 3, thisFail), true},
		{"done rework bound to this fail (no revision)", rework(agent.WorkItemDone, 1, 3, thisFail), true},
		{"retryable failed still owes work", rework(agent.WorkItemFailed, 1, 3, thisFail), false},
		{"open", rework(agent.WorkItemOpen, 0, 3, thisFail), false},
		{"claimed", rework(agent.WorkItemClaimed, 1, 3, thisFail), false},
		{"running", rework(agent.WorkItemRunning, 1, 3, thisFail), false},
		{"waiting_human", rework(agent.WorkItemWaitingHuman, 1, 3, thisFail), false},
		{"terminal rework bound to an older fail", rework(agent.WorkItemDead, 3, 3, payloadFor(caseID, failedVersion-1)), false},
		{"terminal rework of another case", rework(agent.WorkItemDead, 3, 3, payloadFor(caseID+1, failedVersion)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeReworkSource{items: []agent.WorkItem{tc.item}}
			reader := &judgeReworkBudgetReader{source: src}
			got, err := reader.HasTerminalReworkForVersion(context.Background(), versionID, caseID, failedVersion)
			if err != nil {
				t.Fatalf("HasTerminalReworkForVersion: %v", err)
			}
			if got != tc.want {
				t.Fatalf("terminal rework for version = %v, want %v", got, tc.want)
			}
			if src.gotVersionID != versionID || src.gotCaseID != caseID {
				t.Fatalf("source queried with (%d,%d), want (%d,%d)", src.gotVersionID, src.gotCaseID, versionID, caseID)
			}
		})
	}
	t.Run("no rework items", func(t *testing.T) {
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{}}
		got, err := reader.HasTerminalReworkForVersion(context.Background(), versionID, caseID, failedVersion)
		if err != nil || got {
			t.Fatalf("got %v err %v, want false/nil", got, err)
		}
	})
	t.Run("another work type is ignored", func(t *testing.T) {
		item := rework(agent.WorkItemDead, 3, 3, thisFail)
		item.Type = designapp.TaskTypeDesignJudge
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{items: []agent.WorkItem{item}}}
		got, err := reader.HasTerminalReworkForVersion(context.Background(), versionID, caseID, failedVersion)
		if err != nil || got {
			t.Fatalf("got %v err %v, want false (judge item, not rework)", got, err)
		}
	})
	t.Run("kind-authoritative work type is honored", func(t *testing.T) {
		item := rework(agent.WorkItemDead, 3, 3, thisFail)
		item.Kind = "design.cases"
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{items: []agent.WorkItem{item}}}
		got, err := reader.HasTerminalReworkForVersion(context.Background(), versionID, caseID, failedVersion)
		if err != nil || got {
			t.Fatalf("got %v err %v, want false (kind authoritative)", got, err)
		}
	})
	t.Run("malformed payload is skipped", func(t *testing.T) {
		item := rework(agent.WorkItemDead, 3, 3, json.RawMessage(`{"case_id":"not-a-number"}`))
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{items: []agent.WorkItem{item}}}
		got, err := reader.HasTerminalReworkForVersion(context.Background(), versionID, caseID, failedVersion)
		if err != nil || got {
			t.Fatalf("got %v err %v, want false (unparsable payload skipped)", got, err)
		}
	})
	t.Run("nil source fails closed", func(t *testing.T) {
		reader := &judgeReworkBudgetReader{}
		if _, err := reader.HasTerminalReworkForVersion(context.Background(), versionID, caseID, failedVersion); err == nil {
			t.Fatal("nil source must fail closed, never silently report no terminal rework")
		}
	})
	t.Run("nil reader fails closed", func(t *testing.T) {
		var reader *judgeReworkBudgetReader
		if _, err := reader.HasTerminalReworkForVersion(context.Background(), versionID, caseID, failedVersion); err == nil {
			t.Fatal("nil reader must fail closed")
		}
	})
	t.Run("source read error propagates", func(t *testing.T) {
		reader := &judgeReworkBudgetReader{source: &fakeReworkSource{err: errors.New("db down")}}
		if _, err := reader.HasTerminalReworkForVersion(context.Background(), versionID, caseID, failedVersion); err == nil {
			t.Fatal("source read error must propagate")
		}
	})
}

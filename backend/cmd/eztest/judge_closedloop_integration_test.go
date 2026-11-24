//go:build integration

package main

// v0.2.4-E T19 (spec SC-E12, design §8.5 Q1=A "必须执行"): the fake-LLM judge
// closed-loop integration suite. Every loop drives the REAL production paths
// against an isolated PostgreSQL schema:
//
//   - scheduling: design Planner.StartStageRound / Reconcile -> ensureCriticTasks
//     -> ensureJudgeTasks -> designTaskEnqueuer.EnsureJudgeGeneration (the real
//     composition-root adapter, incl. the bounded generation budget and the
//     judge_exhausted escalation facts over the version FactService);
//   - judge runs: the real agent Runner claiming real work_item rows and talking
//     to scripts/fake-llm.cjs over HTTP through the real LLM gateway/adapter
//     (openai_chat_completions, streaming). The fake emits design.judgment_submit
//     per the T13 contract; the real tool -> designadapter -> JudgmentService
//     settles the append-only facts;
//   - confirmation: GraphService.Confirm with the T7 judgment gate;
//   - override + judgment report: the T16 HTTP handlers over the real service/DB;
//   - review_state: JudgmentService.ReviewStateFor (T6 single derivation);
//   - stage advance: the real StageService with the T9 shared review gate wired
//     into BOTH the manual edge and the planner converge edge.
//
// The six loops (spec SC-E12):
//   ① pass -> case_review -> human Confirm -> confirmed
//   ② fail(blocker) -> design.rework -> author revises -> round 2 pass -> Confirm
//   ③ fail x2 (rework exhausted) -> escalated_fail -> case_review override -> Confirm
//   ④ judge run failure (LLM unavailable) -> judge_exhausted -> judge_unavailable
//      -> case_review human override
//   ⑤ discovery draft (OnResourceConfirmed predicates) -> judge -> human Confirm
//      (NO auto-confirm)
//   ⑥ partial coverage -> AC-gap repair -> author completes -> coverage complete
//      -> judge enqueued
//
// Test-only accelerations (no production logic is bypassed):
//   - retry backoff wait is fast-forwarded by resetting work_item.available_at
//     (the real Fail/claim/retry predicates still run);
//   - the worker's claim->MarkRunning->Run->Complete/Fail write-back contract is
//     mirrored synchronously so each loop can assert between deterministic steps.
//
// Run: cd backend && go test -tags=integration -p 1 ./cmd/eztest/ -run TestJudgeClosedLoop -v
//      (EZTEST_TEST_DATABASE_URL must point at the isolated test database.)

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/designadapter"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditpg "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	designapi "github.com/EdouardRichard/EzTest/backend/internal/design/interfaces/httpapi"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llminfra "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcepg "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure"
	settingsapp "github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	settingspg "github.com/EdouardRichard/EzTest/backend/internal/settings/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versioninfra "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	versionpostgres "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// judgeLoopHarness wires the judge closed-loop production assembly over one
// isolated schema and one fake-llm child process.
type judgeLoopHarness struct {
	t            *testing.T
	pool         *pgxpool.Pool
	ids          platformid.Generator
	queue        *agentapp.WorkQueue
	runner       *agentapp.Runner
	planner      *designapp.Planner
	graph        *designapp.GraphService
	judge        designJudgeComponents
	stages       *versionapp.StageService
	requirements *versionapp.RequirementService
	designRepo   *designpg.Repository
	// events is the real event service the assembly publishes through. A test
	// can Observe it to wire the production wake edge (main.go eventSvc.Observe
	// -> reconciler.Notify -> reconcile pass) and prove an event storm does or
	// does not sustain itself.
	events        *eventingapp.Service
	fakeURL       string
	versionID     int64
	environmentID int64
	// llm/llmConfigID let a loop take the LLM endpoint down through the real
	// admin config use case (an unreachable base URL), so a work item's runs
	// fail and retry to their attempt cap exactly as in production.
	llm         *llmapp.Service
	llmConfigID int64
	// settings lets a loop change a registered setting through the real admin
	// use case (e.g. design.max_rework_rounds) instead of a fixture shortcut.
	settings *settingsapp.Service
}

// startFakeLLM spawns scripts/fake-llm.cjs on an ephemeral loopback port and
// returns its base URL. The child is killed at test cleanup.
func startFakeLLM(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the fake-LLM judge integration cannot run")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "fake-llm.cjs"))
	if err != nil {
		t.Fatalf("resolve fake-llm script: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("fake-llm script missing: %v", err)
	}
	cmd := exec.Command(node, script)
	cmd.Env = append(os.Environ(), "FAKE_LLM_PORT=0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("fake-llm stdout pipe: %v", err)
	}
	// Do NOT inherit the test binary's stderr: a surviving child handle keeps
	// the test I/O open after the process is killed (Windows WaitDelay hang).
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake-llm: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // closes the stdout pipe so the scanner goroutine exits
	})
	urlCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if idx := strings.Index(line, "listening on "); idx >= 0 {
				urlCh <- strings.TrimSpace(line[idx+len("listening on "):])
				return
			}
		}
	}()
	select {
	case url := <-urlCh:
		return url
	case <-time.After(15 * time.Second):
		t.Fatal("fake-llm did not report its listening address within 15s")
		return ""
	}
}

// configureFakeLLM registers the fake as the default LLM configuration through
// the real config/catalog use cases (create -> refresh -> capability patch ->
// default model), exactly the admin flow the acceptance scripts drive. It
// returns the configuration id so a loop can patch the endpoint later.
func configureFakeLLM(t *testing.T, svc *llmapp.Service, fakeURL string) int64 {
	t.Helper()
	ctx := context.Background()
	name, protocol, baseURL, apiKey := "fake-judge", "openai_chat_completions", fakeURL+"/v1", "fake-key"
	isDefault, maxRetries, timeout := true, 0, 15
	cfg, err := svc.CreateConfig(ctx, llmapp.ConfigInput{
		Name: &name, Protocol: &protocol, BaseURL: &baseURL, APIKey: &apiKey,
		IsDefault: &isDefault, MaxRetries: &maxRetries, TimeoutSeconds: &timeout,
	}, 1)
	if err != nil {
		t.Fatalf("create fake LLM config: %v", err)
	}
	models, err := svc.RefreshModels(ctx, cfg.ID, 1)
	if err != nil {
		t.Fatalf("refresh fake LLM models: %v", err)
	}
	var modelID int64
	for _, model := range models {
		if model.Model == "fake-llm" {
			modelID = model.ID
		}
	}
	if modelID == 0 {
		t.Fatalf("fake-llm model not in refreshed catalog: %+v", models)
	}
	yes := true
	if _, err := svc.UpdateModel(ctx, cfg.ID, modelID, llmapp.ModelPatch{
		SupportsTools: &yes, SupportsVision: &yes,
	}, 1); err != nil {
		t.Fatalf("patch fake model capabilities: %v", err)
	}
	if _, err := svc.UpdateConfig(ctx, cfg.ID, llmapp.ConfigInput{DefaultModelID: &modelID}, 1); err != nil {
		t.Fatalf("set fake default model: %v", err)
	}
	return cfg.ID
}

func newJudgeLoopHarness(t *testing.T) *judgeLoopHarness {
	t.Helper()
	pool := testutil.NewDB(t)
	fakeURL := startFakeLLM(t)
	ctx := context.Background()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	clk := clock.System{}

	settingsRepo := settingspg.New(pool)
	definitions := settingsapp.Definitions("")
	if err := settingsRepo.Seed(ctx, definitions); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	eventSvc := eventingapp.New(eventingpg.New(pool), eventingapp.NewHub(eventingapp.DefaultHubBuffer), eventingpg.NewNotifier(pool))
	auditSvc := auditapp.New(auditpg.New(pool), ids, clk)
	settingsSvc := settingsapp.New(settingsRepo, definitions, ids, clk, auditSvc, eventSvc)
	if err := settingsSvc.Reload(ctx); err != nil {
		t.Fatalf("reload settings: %v", err)
	}

	llmSvc := llmapp.New(llminfra.NewRepository(pool), llminfra.NewClient(), ids, clk, auditSvc, eventSvc,
		llmapp.WithChatClient(llminfra.NewClient()), llmapp.WithSettingsReader(settingsSvc))
	llmConfigID := configureFakeLLM(t, llmSvc, fakeURL)

	resourceSvc := resourceapp.New(resourcepg.New(pool), ids, clk, nil, auditSvc, eventSvc)
	versionRepo := versioninfra.New(pool, ids)
	versionSvc := versionapp.New(versionRepo, resourceSvc, ids, clk, auditSvc, eventSvc, nil)
	designRepo := designpg.New(pool)
	designStale := designapp.NewStaleService(designRepo, clk, auditSvc, eventSvc)
	requirementSvc, err := newRequirementService(
		versioninfra.NewRequirementRepository(pool), versionSvc,
		designStale, auditSvc, eventSvc, ids, clk)
	if err != nil {
		t.Fatalf("wire requirement service: %v", err)
	}
	requirementSvc.SetConfirmationSettings(&codeIntelSettings{settings: settingsSvc})

	workQueue := agentapp.NewWorkQueue(agentpg.NewWorkItemRepository(pool, ids), clk, 30*time.Second, 3)
	workQueue.SetEvents(eventSvc)
	teamRoom := agentapp.NewTeamRoom(agentpg.NewTeamRoomRepository(pool, ids), clk, ids, settingsSvc, eventSvc)
	conversationSvc := versionapp.NewConversationService(
		versioninfra.NewConversationRepository(pool, ids), versionTaskEnqueuer{queue: workQueue},
		nil, versionSvc, clk, ids, eventSvc)
	conversationSvc.SetClarifiedRequirementCounter(requirementSvc)
	factSvc := versionapp.NewFactService(versionpostgres.NewFactRepository(pool, ids), ids, eventSvc)

	designJudge := buildDesignJudge(pool, ids, clk, eventSvc, settingsSvc, designRepo, factSvc)
	// H-F5/H-F6: the same targeted design.requirement source feeds the
	// coverage-repair counter and the authoring-batch settle read, exactly as
	// main.go wires them. authorExhausted mirrors main.go's author_exhausted
	// fact store (T11c M5: the planner-level stranded leg escalates through it).
	designAuthorItems := &authorBatchSQLSource{pool: pool}
	designPlanner := designapp.NewPlanner(
		designRepo, ids, clk, eventSvc,
		designTaskEnqueuer{
			queue: workQueue, judgeExhausted: designJudge.Exhausted,
			judgeGenerations: &judgeGenerationSQLSource{pool: pool},
			coverageRepairs:  designAuthorItems,
			authorExhausted:  &authorExhaustedFactStore{facts: factSvc},
			events:           eventSvc,
		},
		designTeamRoomAdapter{room: teamRoom},
		designConversationCreator{svc: conversationSvc},
		designVersionStarter{svc: versionSvc},
		designStale,
	)
	designGraph := designapp.NewGraphService(designRepo, ids, clk, auditSvc, eventSvc, designTeamRoomAdapter{room: teamRoom})
	designGraph.SetJudgmentGate(designJudge.Gate)
	designPlanner.SetJudgeScheduler(designJudge.Store, settingsSvc)
	designPlanner.SetReworkBudgetReader(&judgeReworkBudgetReader{source: &judgeReworkSQLSource{pool: pool}})
	designPlanner.SetAuthorBatchReader(&authorBatchReader{source: designAuthorItems})
	designPlanner.SetReviewGate(designJudge.ReviewGate)
	designJudge.Service.SetGraphReader(designRepo)
	designJudge.Service.SetReworkBudgetReader(&judgeReworkBudgetReader{source: &judgeReworkSQLSource{pool: pool}})
	designGraph.SetDiscoveryRevisionStore(designpg.NewRevisionStore(pool))

	stageService := versionapp.NewStageService(versionapp.StageServiceDeps{
		Repo:   versionRepo,
		Audit:  auditSvc,
		Events: eventSvc,
		Clock:  clk,
		Gates: newV022StageGates(v022StageGateDeps{
			Versions:     versionSvc,
			Requirements: requirementSvc,
			Planner:      designPlanner,
			Design:       designGraph,
		}),
	})
	stageService.SetDesignReviewGate(designJudge.ReviewGate)
	designPlanner.SetDesignConverger(stageAdvancer{service: stageService})

	runtimeRepo := agentpg.NewRuntimeRepository(pool, ids)
	if _, err := agentapp.EnsureProfiles(ctx, runtimeRepo); err != nil {
		t.Fatalf("seed agent profiles: %v", err)
	}
	vAdapter := versionadapter.New(requirementSvc, conversationSvc, versionSvc, nil, nil)
	dAdapter := designadapter.New(designGraph).
		SetJudgment(designJudge.Service, designJudge.Store, settingsSvc).
		SetReworkProvenance(&judgeReworkWorkItemReader{source: &judgeReworkSQLSource{pool: pool}})
	runner := agentapp.NewRunner(agentapp.RunnerDeps{
		Runtime:      runtimeRepo,
		Chat:         llmSvc,
		Settings:     settingsSvc,
		Clock:        clk,
		IDs:          ids,
		Events:       eventSvc,
		Audit:        auditSvc,
		Conversation: vAdapter,
		Versions:     vAdapter,
		TeamRoom:     teamRoom,
		ToolDeps: tools.Deps{
			Requirement: vAdapter,
			Resource:    vAdapter,
			Design:      dAdapter,
		},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstanceID: "judge-loop-test",
	})

	h := &judgeLoopHarness{
		t: t, pool: pool, ids: ids, queue: workQueue, runner: runner,
		planner: designPlanner, graph: designGraph, judge: designJudge,
		stages: stageService, requirements: requirementSvc, designRepo: designRepo,
		events:  eventSvc,
		fakeURL: fakeURL,
		llm:     llmSvc, llmConfigID: llmConfigID,
		settings: settingsSvc,
	}
	h.environmentID = ids.Next()
	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES ($1, 'judge-loop env', 'active')`, h.environmentID); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	h.versionID = ids.Next()
	now := clk.Now().UTC()
	if err := versionRepo.Create(ctx, versiondomain.Version{
		ID: h.versionID, Title: "Judge closed-loop version", Status: versiondomain.StatusInitial,
		CreatedBy: 1, CreatedAt: now, UpdatedAt: now, EnvironmentID: h.environmentID,
	}, nil); err != nil {
		t.Fatalf("create version: %v", err)
	}
	return h
}

// --- harness helpers ----------------------------------------------------------

func (h *judgeLoopHarness) ctx() context.Context { return context.Background() }

func (h *judgeLoopHarness) setFakeMode(route, mode string) {
	h.t.Helper()
	resp, err := http.Post(h.fakeURL+route, "application/json", strings.NewReader(`{"mode":"`+mode+`"}`))
	if err != nil {
		h.t.Fatalf("set fake mode %s=%s: %v", route, mode, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("set fake mode %s=%s: status %d body %s", route, mode, resp.StatusCode, body)
	}
}

func (h *judgeLoopHarness) setJudgeMode(mode string)  { h.setFakeMode("/__judge-mode", mode) }
func (h *judgeLoopHarness) setAuthorMode(mode string) { h.setFakeMode("/__author-mode", mode) }

// createClarifiedRequirement creates one requirement with acCount acceptance
// criteria through the real Create/Confirm use cases (V1 contract + the T11
// settings-backed confirmation gate).
func (h *judgeLoopHarness) createClarifiedRequirement(acCount int) (int64, int64) {
	h.t.Helper()
	criteria := make([]versionapp.RequirementAcceptanceCriterionInput, 0, acCount)
	for i := 0; i < acCount; i++ {
		criteria = append(criteria, versionapp.RequirementAcceptanceCriterionInput{
			Given: fmt.Sprintf("已注册账号且前置条件 %d 成立", i+1),
			When:  fmt.Sprintf("用户执行核心操作 %d", i+1),
			Then:  fmt.Sprintf("系统返回操作 %d 的成功结果", i+1),
		})
	}
	detail, err := h.requirements.Create(h.ctx(), h.versionID, versionapp.RequirementDraft{
		Title:              fmt.Sprintf("登录功能（%d 条验收标准）", acCount),
		UserStory:          versionapp.RequirementUserStoryView{AsA: "注册用户", IWant: "完成登录", SoThat: "使用系统功能"},
		AcceptanceCriteria: criteria,
		Priority:           "P2",
		SutDescription:     "被测系统的登录页面与登录接口，包含账号密码校验与锁定策略。",
	}, 1)
	if err != nil {
		h.t.Fatalf("create requirement: %v", err)
	}
	if _, err := h.requirements.Confirm(h.ctx(), h.versionID, detail.ID, 1); err != nil {
		h.t.Fatalf("confirm requirement: %v", err)
	}
	return detail.ID, detail.CurrentVersionID
}

// advanceToDesigning walks the real stage edges created -> clarifying ->
// requirement_review -> designing through the StageService gates.
func (h *judgeLoopHarness) advanceToDesigning() {
	h.t.Helper()
	for _, stage := range []versiondomain.Stage{
		versiondomain.StageClarifying, versiondomain.StageRequirementReview, versiondomain.StageDesigning,
	} {
		if err := h.stages.Request(h.ctx(), h.versionID, stage); err != nil {
			h.t.Fatalf("advance to %s: %v", stage, err)
		}
	}
}

// setupDesigningRequirement is the common prefix of every loop: one clarified
// requirement, the version at designing, and the design round started (the
// deterministic design.cases stage-entry action), which enqueues the real
// design.requirement author work item.
func (h *judgeLoopHarness) setupDesigningRequirement(acCount int) (int64, int64) {
	h.t.Helper()
	reqID, reqVerID := h.createClarifiedRequirement(acCount)
	h.advanceToDesigning()
	if _, err := h.planner.StartStageRound(h.ctx(), h.versionID, nil); err != nil {
		h.t.Fatalf("start design round: %v", err)
	}
	return reqID, reqVerID
}

// fastForwardRetries removes only the retry-backoff WAIT (available_at) so the
// suite does not sleep through min(2^attempts,30s); the real Fail/claim/retry
// predicates are untouched.
func (h *judgeLoopHarness) fastForwardRetries() {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx(),
		`UPDATE work_item SET available_at = now() WHERE version_id = $1 AND status = 'open' AND available_at > now()`,
		h.versionID); err != nil {
		h.t.Fatalf("fast-forward retry backoff: %v", err)
	}
}

type judgeLoopRun struct {
	item   agent.WorkItem
	result agentapp.RunResult
	err    error
}

// runWorkItems drains every claimable work item of one type through the real
// runner, mirroring the worker's write-back contract (MarkRunning -> Run ->
// Complete on done / Fail with the classified retryability otherwise). Failed
// retryable items are fast-forwarded and re-claimed until they reach their
// attempt cap, so one call fully settles a generation.
func (h *judgeLoopHarness) runWorkItems(workType string) []judgeLoopRun {
	h.t.Helper()
	runs := make([]judgeLoopRun, 0)
	for {
		item, ok, err := h.queue.ClaimForTypes(h.ctx(), "judge-loop-test", []string{workType}, 0)
		if err != nil {
			h.t.Fatalf("claim %s: %v", workType, err)
		}
		if !ok {
			return runs
		}
		if err := h.queue.MarkRunning(h.ctx(), item.ID, item.LeaseEpoch); err != nil {
			h.t.Fatalf("mark running %s: %v", workType, err)
		}
		result, runErr := h.runner.Run(h.ctx(), item)
		if runErr == nil && result.Status == agent.AgentRunDone {
			if err := h.queue.Complete(h.ctx(), item.ID, item.LeaseEpoch); err != nil {
				h.t.Fatalf("complete %s: %v", workType, err)
			}
		} else {
			retryable := agentapp.IsRetryable(runErr)
			if runErr == nil {
				retryable = true
				runErr = fmt.Errorf("agent run finished with status %s", result.Status)
			}
			if err := h.queue.Fail(h.ctx(), item.ID, item.LeaseEpoch, runErr, retryable); err != nil {
				h.t.Fatalf("fail %s: %v", workType, err)
			}
			h.fastForwardRetries()
		}
		runs = append(runs, judgeLoopRun{item: item, result: result, err: runErr})
	}
}

func (h *judgeLoopHarness) requireRunDone(runs []judgeLoopRun, want int, label string) {
	h.t.Helper()
	if len(runs) != want {
		h.t.Fatalf("%s: ran %d work items, want %d", label, len(runs), want)
	}
	for _, run := range runs {
		if run.err != nil || run.result.Status != agent.AgentRunDone {
			h.dumpRunTranscript(run.result.AgentRunID)
			h.t.Fatalf("%s: run %d status=%s err=%v, want done", label, run.item.ID, run.result.Status, run.err)
		}
	}
}

// dumpRunTranscript logs the persisted transcript of a failed run so a loop
// failure names the exact tool error the model saw (diagnostics only).
func (h *judgeLoopHarness) dumpRunTranscript(runID int64) {
	h.t.Helper()
	if runID == 0 {
		return
	}
	rows, err := h.pool.Query(h.ctx(),
		`SELECT role, COALESCE(parts::text, ''), COALESCE(tool_calls::text, '') FROM agent_message WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var role, parts, calls string
		if err := rows.Scan(&role, &parts, &calls); err != nil {
			return
		}
		if len(parts) > 600 {
			parts = parts[:600] + "…"
		}
		if len(calls) > 600 {
			calls = calls[:600] + "…"
		}
		h.t.Logf("run %d [%s] parts=%s calls=%s", runID, role, parts, calls)
	}
}

// reconcile is one production design-reconciler tick (ScanVersion + converge).
func (h *judgeLoopHarness) reconcile() {
	h.t.Helper()
	if err := h.planner.Reconcile(h.ctx()); err != nil {
		h.t.Fatalf("design reconcile: %v", err)
	}
}

// draftCards returns the current draft cases of the version's design graph.
func (h *judgeLoopHarness) draftCards() []designapp.CaseCardView {
	h.t.Helper()
	graph, err := h.graph.ReadGraph(h.ctx(), h.versionID)
	if err != nil {
		h.t.Fatalf("read graph: %v", err)
	}
	cards := make([]designapp.CaseCardView, 0)
	for _, requirement := range graph.Requirements {
		for _, scenario := range requirement.Scenarios {
			for _, card := range scenario.Cases {
				if card.Status == design.CaseStatusDraft {
					cards = append(cards, card)
				}
			}
		}
	}
	return cards
}

func (h *judgeLoopHarness) reviewState(caseID, currentVersionID int64) design.ReviewState {
	h.t.Helper()
	state, err := h.judge.Service.ReviewStateFor(h.ctx(), h.versionID, caseID, currentVersionID)
	if err != nil {
		h.t.Fatalf("review state for case %d: %v", caseID, err)
	}
	return state
}

func (h *judgeLoopHarness) latestJudgment(caseID int64) *design.Judgment {
	h.t.Helper()
	judgment, err := h.judge.Store.LatestJudgment(h.ctx(), caseID)
	if err != nil {
		h.t.Fatalf("latest judgment for case %d: %v", caseID, err)
	}
	return judgment
}

func (h *judgeLoopHarness) currentContentHash(caseID int64) (int64, string) {
	h.t.Helper()
	data, err := h.designRepo.ReadCase(h.ctx(), h.versionID, caseID, nil)
	if err != nil {
		h.t.Fatalf("read case %d: %v", caseID, err)
	}
	return data.Selected.ID, data.Selected.ContentHash
}

func (h *judgeLoopHarness) confirmRequirement(reqID int64) designapp.ConfirmResult {
	h.t.Helper()
	result, err := h.graph.Confirm(h.ctx(), h.versionID, designapp.ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: reqID,
	}, 1)
	if err != nil {
		h.t.Fatalf("confirm requirement %d: %v", reqID, err)
	}
	return result
}

func (h *judgeLoopHarness) stage() versiondomain.Stage {
	h.t.Helper()
	view, err := h.stages.Stage(h.ctx(), h.versionID)
	if err != nil {
		h.t.Fatalf("read stage: %v", err)
	}
	return view.Stage
}

func (h *judgeLoopHarness) workItemsOfType(workType string) []agent.WorkItem {
	h.t.Helper()
	items, err := h.queue.ListByVersion(h.ctx(), h.versionID, agentapp.MaxListLimit)
	if err != nil {
		h.t.Fatalf("list work items: %v", err)
	}
	out := make([]agent.WorkItem, 0)
	for _, item := range items {
		if agentapp.WorkTypeForItem(item) == workType {
			out = append(out, item)
		}
	}
	return out
}

func (h *judgeLoopHarness) confirmationCount(caseVersionID int64) int {
	h.t.Helper()
	var count int
	if err := h.pool.QueryRow(h.ctx(),
		`SELECT count(*) FROM case_confirmation WHERE case_version_id = $1`, caseVersionID).Scan(&count); err != nil {
		h.t.Fatalf("count confirmations: %v", err)
	}
	return count
}

func (h *judgeLoopHarness) eventCount(eventType string) int {
	h.t.Helper()
	var count int
	if err := h.pool.QueryRow(h.ctx(),
		`SELECT count(*) FROM event WHERE channel = $1 AND type = $2`,
		fmt.Sprintf("version:%d", h.versionID), eventType).Scan(&count); err != nil {
		h.t.Fatalf("count events: %v", err)
	}
	return count
}

// judgmentMux mounts the REAL T16 routes on the real handler + service + DB.
func (h *judgeLoopHarness) judgmentMux() *http.ServeMux {
	handler := designapi.New(h.graph, nil)
	handler.SetJudgmentService(h.judge.Service)
	mux := http.NewServeMux()
	mux.Handle("POST /api/versions/{id}/design/judgments/{caseId}/override", http.HandlerFunc(handler.OverrideJudgment))
	mux.Handle("GET /api/versions/{id}/design/judgments", http.HandlerFunc(handler.ListJudgments))
	return mux
}

func (h *judgeLoopHarness) overrideRoute(caseID int64, role, reason string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest("POST",
		fmt.Sprintf("/api/versions/%d/design/judgments/%d/override", h.versionID, caseID),
		strings.NewReader(`{"reason":"`+reason+`"}`))
	request = request.WithContext(platform.WithActor(request.Context(), platform.Actor{ID: 7, Role: role}))
	recorder := httptest.NewRecorder()
	h.judgmentMux().ServeHTTP(recorder, request)
	return recorder
}

func (h *judgeLoopHarness) judgmentsRoute(role string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest("GET",
		fmt.Sprintf("/api/versions/%d/design/judgments", h.versionID), nil)
	request = request.WithContext(platform.WithActor(request.Context(), platform.Actor{ID: 7, Role: role}))
	recorder := httptest.NewRecorder()
	h.judgmentMux().ServeHTTP(recorder, request)
	return recorder
}

// decodeDataEnvelope unwraps the {"data":...} REST envelope into a generic map.
func decodeDataEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response envelope: %v (body %s)", err, recorder.Body.String())
	}
	return envelope.Data
}

func decodeErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (body %s)", err, recorder.Body.String())
	}
	return envelope.Error.Code
}

// --- ① pass -> case_review -> human Confirm -> confirmed ----------------------

func TestJudgeClosedLoopPassConfirm(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")

	// Real author run through the fake LLM: drafts land via case.graph_propose.
	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("author produced %d drafts, want 2", len(drafts))
	}
	for _, card := range drafts {
		if len(card.CoveredACs) == 0 {
			t.Fatalf("draft %d covers no AC", card.CaseID)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePending {
			t.Fatalf("pre-judge review state = %s, want pending", state)
		}
	}

	// Real scheduler tick enqueues the judge; the real judge run settles facts.
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge")
	for _, card := range drafts {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePass {
			t.Fatalf("post-judge review state = %s, want pass", state)
		}
		judgment := h.latestJudgment(card.CaseID)
		if judgment == nil || judgment.Verdict != design.VerdictPass || judgment.Round != 1 {
			t.Fatalf("judgment = %+v, want round-1 pass", judgment)
		}
		versionID, contentHash := h.currentContentHash(card.CaseID)
		if judgment.CaseVersionID != versionID || judgment.ContentHash != contentHash {
			t.Fatalf("judgment not bound to current content: %+v (current %d/%s)", judgment, versionID, contentHash)
		}
		// Judge pass is NOT auto-confirm (R-E4r): still draft, no confirmation.
		if h.confirmationCount(card.CurrentVersionID) != 0 {
			t.Fatalf("judge pass auto-confirmed case %d", card.CaseID)
		}
	}
	if got := h.eventCount("design.judgment_submitted"); got != 2 {
		t.Fatalf("judgment_submitted events = %d, want 2", got)
	}

	// Converge through the shared review gate, then the human Confirm.
	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review", stage)
	}
	result := h.confirmRequirement(reqID)
	if len(result.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases, want 2", len(result.Confirmed))
	}
	for _, card := range drafts {
		if h.confirmationCount(card.CurrentVersionID) != 1 {
			t.Fatalf("case %d missing confirmation row after human Confirm", card.CaseID)
		}
	}
	if len(h.draftCards()) != 0 {
		t.Fatal("drafts remain after requirement-scope confirm")
	}
}

// --- ② fail(blocker) -> rework -> author revises -> round 2 pass -> Confirm ---

func TestJudgeClosedLoopFailReworkPass(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("fail")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	firstDrafts := h.draftCards()
	if len(firstDrafts) != 2 {
		t.Fatalf("author produced %d drafts, want 2", len(firstDrafts))
	}
	failedVersions := map[int64]int64{}
	for _, card := range firstDrafts {
		failedVersions[card.CaseID] = card.CurrentVersionID
	}

	// Round 1: the judge fails every draft with a blocker finding.
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 1")
	for _, card := range firstDrafts {
		judgment := h.latestJudgment(card.CaseID)
		if judgment == nil || judgment.Verdict != design.VerdictFail || judgment.Round != 1 {
			t.Fatalf("round-1 judgment = %+v, want fail round 1", judgment)
		}
		if len(judgment.Findings) == 0 || judgment.Findings[0].Severity != design.SeverityBlocker {
			t.Fatalf("round-1 findings = %+v, want a blocker", judgment.Findings)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateReworking {
			t.Fatalf("post-fail review state = %s, want reworking", state)
		}
	}

	// The scheduler observes the fails and enqueues one rework per logical case.
	h.reconcile()
	reworks := h.workItemsOfType(agentapp.WorkTypeDesignRework)
	if len(reworks) != 2 {
		t.Fatalf("rework items = %d, want 2", len(reworks))
	}
	for _, item := range reworks {
		payload, err := agentapp.ParseWorkItemPayload(item.Payload)
		if err != nil {
			t.Fatalf("parse rework payload: %v", err)
		}
		if payload.CaseVersionID != failedVersions[payload.CaseID] {
			t.Fatalf("rework payload not bound to the failed version: %+v", payload)
		}
		// SC-E4 "payload 带 findings 摘要 + round" pinned at integration level
		// (T19 fix round 1): the rework request must carry the next judgment
		// round and the actual judge findings summary the author will see.
		if payload.ReworkRound != 2 {
			t.Fatalf("rework payload round = %d, want 2", payload.ReworkRound)
		}
		if strings.TrimSpace(payload.ReworkFindings) == "" {
			t.Fatalf("rework payload carries no findings summary: %+v", payload)
		}
		judgment := h.latestJudgment(payload.CaseID)
		if judgment == nil || len(judgment.Findings) == 0 {
			t.Fatalf("case %d has no round-1 findings to summarize", payload.CaseID)
		}
		if !strings.Contains(payload.ReworkFindings, judgment.Findings[0].Message) {
			t.Fatalf("rework findings summary %q does not carry the judge finding %q",
				payload.ReworkFindings, judgment.Findings[0].Message)
		}
	}
	if got := h.eventCount("design.rework_requested"); got != 2 {
		t.Fatalf("rework_requested events = %d, want 2", got)
	}
	// The rework is still in flight: further passes must not re-announce it
	// (whole-branch review Critical #1).
	h.reconcile()
	h.reconcile()
	if got := h.eventCount("design.rework_requested"); got != 2 {
		t.Fatalf("rework_requested events after extra reconciles = %d, want 2", got)
	}
	// Reworking drafts block the case-review gate (no premature advance).
	if err := h.judge.ReviewGate.ReviewGate(h.ctx(), h.versionID); err == nil {
		t.Fatal("review gate open while drafts are reworking")
	}

	// The author revises each scoped case through the fake LLM (new content).
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems("design.rework"), 2, "author rework")
	revised := h.draftCards()
	if len(revised) != 2 {
		t.Fatalf("revised drafts = %d, want 2", len(revised))
	}
	for _, card := range revised {
		if card.CurrentVersionID == failedVersions[card.CaseID] {
			t.Fatalf("rework did not produce a new version for case %d", card.CaseID)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePending {
			t.Fatalf("revised draft review state = %s, want pending (content binding)", state)
		}
	}

	// Round 2: a fresh judge generation reviews the new versions and passes.
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 2")
	for _, card := range revised {
		judgment := h.latestJudgment(card.CaseID)
		if judgment == nil || judgment.Verdict != design.VerdictPass || judgment.Round != 2 {
			t.Fatalf("round-2 judgment = %+v, want pass round 2", judgment)
		}
		if judgment.CaseVersionID != card.CurrentVersionID {
			t.Fatalf("round-2 judgment bound to stale version: %+v", judgment)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePass {
			t.Fatalf("round-2 review state = %s, want pass", state)
		}
	}
	count, err := h.judge.Store.CountReworks(h.ctx(), revised[0].CaseID)
	if err != nil || count != 1 {
		t.Fatalf("CountReworks = %d/%v, want 1 (one rework consumed)", count, err)
	}

	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review", stage)
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases, want 2", len(result.Confirmed))
	}
}

// --- ③ fail x2 -> escalated_fail -> case_review override (admin) -> Confirm ---

func TestJudgeClosedLoopFailTwiceOverride(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("fail")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 1")
	h.reconcile() // enqueues the one allowed rework per case
	h.requireRunDone(h.runWorkItems("design.rework"), 2, "author rework")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 2")

	// Second fail with the budget spent: escalated_fail, no third rework.
	h.reconcile()
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("drafts = %d, want 2", len(drafts))
	}
	for _, card := range drafts {
		judgment := h.latestJudgment(card.CaseID)
		if judgment == nil || judgment.Verdict != design.VerdictFail || judgment.Round != 2 {
			t.Fatalf("round-2 judgment = %+v, want fail round 2", judgment)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateEscalatedFail {
			t.Fatalf("review state = %s, want escalated_fail", state)
		}
	}
	if items := h.workItemsOfType(agentapp.WorkTypeDesignRework); len(items) != 2 {
		t.Fatalf("rework items = %d, want exactly 2 (budget exhausted, no third)", len(items))
	}

	// Escalated drafts still reach case_review (R-E4r human decision point).
	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review (escalation must be reachable)", stage)
	}

	// Confirm without an override is rejected with judgment_failed.
	_, err := h.graph.Confirm(h.ctx(), h.versionID, designapp.ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: reqID,
	}, 1)
	if err == nil || decodePlatformCode(err) != designapp.JudgmentFailedCode {
		t.Fatalf("confirm without override: err=%v, want 409 %s", err, designapp.JudgmentFailedCode)
	}

	// T16 route: non-admin is rejected; admin override succeeds and is bound to
	// the current content; the report shows the escalated state + findings.
	target := drafts[0]
	if recorder := h.overrideRoute(target.CaseID, "user", "普通用户尝试 override"); recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin override status = %d, want 403 (body %s)", recorder.Code, recorder.Body.String())
	}
	recorder := h.overrideRoute(target.CaseID, "admin", "评审后接受该用例的剩余风险")
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin override status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	override := decodeDataEnvelope(t, recorder)
	if override["condition"] != designapp.OverrideConditionFailExhausted {
		t.Fatalf("override condition = %v, want fail_exhausted", override["condition"])
	}
	versionID, contentHash := h.currentContentHash(target.CaseID)
	if override["case_version_id"] != platformid.Format(versionID) || override["content_hash"] != contentHash {
		t.Fatalf("override not bound to current content: %v", override)
	}
	if override["judgment_id"] == nil || override["judgment_id"] == "" {
		t.Fatalf("fail_exhausted override must reference the judgment: %v", override)
	}

	report := h.judgmentsRoute("user")
	if report.Code != http.StatusOK {
		t.Fatalf("judgment report status = %d, want 200", report.Code)
	}
	data := decodeDataEnvelope(t, report)
	cases, _ := data["cases"].([]any)
	if len(cases) != 2 {
		t.Fatalf("report cases = %d, want 2", len(cases))
	}
	for _, entry := range cases {
		card := entry.(map[string]any)
		if card["review_state"] != string(design.ReviewStateEscalatedFail) {
			t.Fatalf("report review_state = %v, want escalated_fail", card["review_state"])
		}
		judgment, ok := card["judgment"].(map[string]any)
		if !ok || judgment["verdict"] != design.VerdictFail {
			t.Fatalf("report judgment = %v, want the fail fact", card["judgment"])
		}
	}

	// Only the overridden case confirms; the sibling still blocks the batch.
	if _, err := h.graph.Confirm(h.ctx(), h.versionID, designapp.ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: target.CaseID,
	}, 1); err != nil {
		t.Fatalf("confirm overridden case: %v", err)
	}
	if h.confirmationCount(target.CurrentVersionID) != 1 {
		t.Fatal("overridden case missing confirmation row")
	}
	sibling := drafts[1]
	_, err = h.graph.Confirm(h.ctx(), h.versionID, designapp.ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: sibling.CaseID,
	}, 1)
	if err == nil || decodePlatformCode(err) != designapp.JudgmentFailedCode {
		t.Fatalf("un-overridden sibling confirm: err=%v, want 409 %s", err, designapp.JudgmentFailedCode)
	}
	if recorder := h.overrideRoute(sibling.CaseID, "admin", "同样接受剩余风险"); recorder.Code != http.StatusOK {
		t.Fatalf("sibling override status = %d, want 200", recorder.Code)
	}
	if _, err := h.graph.Confirm(h.ctx(), h.versionID, designapp.ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: sibling.CaseID,
	}, 1); err != nil {
		t.Fatalf("confirm sibling after override: %v", err)
	}
	if got := h.eventCount("design.judgment_overridden"); got != 2 {
		t.Fatalf("judgment_overridden events = %d, want 2", got)
	}
}

// decodePlatformCode extracts the code of a typed platform error.
func decodePlatformCode(err error) string {
	if apiErr, ok := err.(*platform.Error); ok {
		return apiErr.Code
	}
	return ""
}

// --- ④ judge run failure -> judge_exhausted -> judge_unavailable -> override --

func TestJudgeClosedLoopJudgeUnavailable(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("error") // the fake answers HTTP 500 for every judge request

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("author produced %d drafts, want 2", len(drafts))
	}

	// Drain the bounded judge generations: each generation's run fails (LLM
	// unavailable), retries to its attempt cap, and the next reconcile opens a
	// new generation until the draft-set budget is spent.
	totalFailed := 0
	for generation := 0; generation < 6; generation++ {
		h.reconcile()
		runs := h.runWorkItems("design.judge")
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			if run.err == nil {
				t.Fatalf("judge run unexpectedly succeeded in error mode: %+v", run.result)
			}
			totalFailed++
		}
	}
	if totalFailed == 0 {
		t.Fatal("no judge runs were attempted")
	}
	// The drain loop's final reconcile already hit the exhausted branch and wrote
	// one judge_exhausted fact per pending draft (FactService dedupe keeps it
	// idempotent). The degradation event follows the SAME creation transition, so
	// further passes in the exhausted state append neither facts nor events
	// (whole-branch review Critical #1; see
	// TestJudgeExhaustedEventEmitsOnlyOnTransition for the wake-edge proof).
	for _, card := range drafts {
		var count int
		if err := h.pool.QueryRow(h.ctx(),
			`SELECT count(*) FROM version_fact WHERE version_id = $1 AND kind = $2 AND key = $3 AND superseded_by IS NULL`,
			h.versionID, judgeExhaustedFactKind, judgeExhaustedFactKey(card.CaseID, card.CurrentVersionID)).Scan(&count); err != nil {
			t.Fatalf("read judge_exhausted fact: %v", err)
		}
		if count != 1 {
			t.Fatalf("judge_exhausted facts for case %d = %d, want 1", card.CaseID, count)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateJudgeUnavailable {
			t.Fatalf("review state = %s, want judge_unavailable (no permanent pending)", state)
		}
	}
	if got := h.eventCount("design.judge_exhausted"); got != 2 {
		t.Fatalf("judge_exhausted events = %d, want 2", got)
	}
	// Reconciling again in the exhausted state is silent (emit-on-transition).
	h.reconcile()
	h.reconcile()
	if got := h.eventCount("design.judge_exhausted"); got != 2 {
		t.Fatalf("judge_exhausted events after extra reconciles = %d, want 2", got)
	}

	// Escalation reaches case_review via the shared review gate.
	if err := h.stages.Request(h.ctx(), h.versionID, versiondomain.StageCaseReview); err != nil {
		t.Fatalf("advance to case_review after escalation: %v", err)
	}
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review", stage)
	}
	_, err := h.graph.Confirm(h.ctx(), h.versionID, designapp.ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: reqID,
	}, 1)
	if err == nil || decodePlatformCode(err) != designapp.JudgmentFailedCode {
		t.Fatalf("confirm without override: err=%v, want 409 %s", err, designapp.JudgmentFailedCode)
	}
	for _, card := range drafts {
		recorder := h.overrideRoute(card.CaseID, "admin", "裁判不可用，人工审查后确认")
		if recorder.Code != http.StatusOK {
			t.Fatalf("override status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		override := decodeDataEnvelope(t, recorder)
		if override["condition"] != designapp.OverrideConditionJudgeExhausted {
			t.Fatalf("override condition = %v, want judge_exhausted", override["condition"])
		}
		if _, present := override["judgment_id"]; present {
			t.Fatalf("judge_exhausted override must not reference a judgment: %v", override)
		}
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases after overrides, want 2", len(result.Confirmed))
	}
}

// --- ⑤ discovery draft -> judge -> human Confirm (NO auto-confirm) ------------

// seedDiscoveryObservationChain writes the execution-side provenance of one
// approved discovered page against an EXECUTED confirmed case version: intent ->
// wave -> case_run -> observation + evidence rows. The requirement link and the
// inheritable covered_acs come from the real author-produced case version.
func (h *judgeLoopHarness) seedDiscoveryObservationChain(executedCaseVersionID int64) (int64, int64) {
	h.t.Helper()
	ctx := h.ctx()
	sutID := h.ids.Next()
	pageID := h.ids.Next()
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO sut (id, name, created_by, environment_id, base_url)
		VALUES ($1, 'discovered-sut', 1, $2, 'https://discovered.example.test')`,
		sutID, h.environmentID); err != nil {
		h.t.Fatalf("seed sut: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES ($1, $2, '订单详情页', 'https://discovered.example.test/orders', '/orders/{id}', 'approved')`,
		pageID, sutID); err != nil {
		h.t.Fatalf("seed approved page: %v", err)
	}
	intentID, waveID, caseRunID, observationID := h.ids.Next(), h.ids.Next(), h.ids.Next(), h.ids.Next()
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids, environment_fingerprint, dedupe_key)
		VALUES ($1, $2, $3, '[]'::jsonb, repeat('a', 64), $4)`,
		intentID, h.versionID, executedCaseVersionID, fmt.Sprintf("intent-%d", intentID)); err != nil {
		h.t.Fatalf("seed intent: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO run_wave (id, version_id, status) VALUES ($1, $2, 'done')`,
		waveID, h.versionID); err != nil {
		h.t.Fatalf("seed wave: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status)
		VALUES ($1, $2, $3, $4, 'passed')`,
		caseRunID, waveID, intentID, executedCaseVersionID); err != nil {
		h.t.Fatalf("seed case run: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO execution_discovery_observation (
			id, version_id, wave_id, case_run_id, sut_id, environment_id,
			kind, method, host, resource_template, decision, resource_id
		) VALUES ($1, $2, $3, $4, $5, $6, 'page', '', 'discovered.example.test', '/orders/{id}', 'approved', $7)`,
		observationID, h.versionID, waveID, caseRunID, sutID, h.environmentID, pageID); err != nil {
		h.t.Fatalf("seed observation: %v", err)
	}
	for i := 0; i < 2; i++ { // R-EX-6 evidence floor: >= 2 rows
		if _, err := h.pool.Exec(ctx, `
			INSERT INTO evidence (id, case_run_id, kind, content_ref_id, sha256, size_bytes)
			VALUES ($1, $2, 'screenshot', $1, repeat('b', 64), 10)`,
			h.ids.Next(), caseRunID); err != nil {
			h.t.Fatalf("seed evidence: %v", err)
		}
	}
	return pageID, observationID
}

func TestJudgeClosedLoopDiscoveryDraftHumanConfirm(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")

	// Author -> judge pass -> human Confirm produces a real executed-able case.
	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge")
	h.reconcile()
	confirmed := h.confirmRequirement(reqID)
	if len(confirmed.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases, want 2", len(confirmed.Confirmed))
	}
	executedVersion := confirmed.Confirmed[0].CaseVersionID

	// The discovery pass: every predicate passes -> DRAFT, never auto-confirm.
	pageID, observationID := h.seedDiscoveryObservationChain(executedVersion)
	view, err := h.graph.OnResourceConfirmed(h.ctx(), h.versionID, pageID, observationID, reqID)
	if err != nil {
		h.t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if view.Status != design.CaseStatusDraft || view.LowConfidenceReason != "judgment_pending" {
		t.Fatalf("discovery view = %+v, want draft judgment_pending", view)
	}
	if view.CaseID == 0 || view.CaseVersionID == 0 {
		t.Fatalf("discovery draft not persisted: %+v", view)
	}
	if h.confirmationCount(view.CaseVersionID) != 0 {
		t.Fatal("discovery pass wrote a confirmation (auto-confirm abolished, R-E2r)")
	}
	if got := h.eventCount("case.revision.auto_confirmed"); got != 0 {
		t.Fatalf("auto_confirmed events = %d, want 0", got)
	}
	if state := h.reviewState(view.CaseID, view.CaseVersionID); state != design.ReviewStatePending {
		t.Fatalf("discovery draft review state = %s, want pending", state)
	}

	// The discovery draft enters the SAME judge scheduling as author drafts.
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge discovery draft")
	judgment := h.latestJudgment(view.CaseID)
	if judgment == nil || judgment.Verdict != design.VerdictPass || judgment.CaseVersionID != view.CaseVersionID {
		t.Fatalf("discovery judgment = %+v, want pass bound to the draft", judgment)
	}
	if state := h.reviewState(view.CaseID, view.CaseVersionID); state != design.ReviewStatePass {
		t.Fatalf("discovery review state = %s, want pass", state)
	}
	// Judge pass still does NOT confirm: the draft waits for the human.
	if h.confirmationCount(view.CaseVersionID) != 0 {
		t.Fatal("judge pass auto-confirmed the discovery draft")
	}
	data, err := h.designRepo.ReadCase(h.ctx(), h.versionID, view.CaseID, nil)
	if err != nil || data.Selected.Status != design.CaseStatusDraft {
		t.Fatalf("discovery case status = %s (err %v), want draft until human Confirm", data.Selected.Status, err)
	}

	// Human Confirm is the only terminal entry.
	if _, err := h.graph.Confirm(h.ctx(), h.versionID, designapp.ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: view.CaseID,
	}, 1); err != nil {
		t.Fatalf("human confirm discovery draft: %v", err)
	}
	if h.confirmationCount(view.CaseVersionID) != 1 {
		t.Fatal("discovery draft missing confirmation after human Confirm")
	}
}

// --- ⑥ partial coverage -> AC-gap repair -> author completes -> judge ---------

func TestJudgeClosedLoopCoverageGapRepair(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, _ := h.setupDesigningRequirement(2) // AC-1 + AC-2
	h.setAuthorMode("partial")                 // first pass covers only AC-1
	h.setJudgeMode("pass")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author partial")
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("author produced %d drafts, want 2", len(drafts))
	}
	for _, card := range drafts {
		if len(card.CoveredACs) != 1 || card.CoveredACs[0] != "AC-1" {
			t.Fatalf("partial draft covered_acs = %v, want [AC-1]", card.CoveredACs)
		}
	}

	// The scheduler sees the AC gap: repair task, NO judge yet.
	h.reconcile()
	if items := h.workItemsOfType(agentapp.WorkTypeDesignJudge); len(items) != 0 {
		t.Fatalf("judge items = %d, want 0 while coverage is incomplete", len(items))
	}
	repairItems := h.workItemsOfType(agentapp.WorkTypeDesignRequirement)
	if len(repairItems) != 2 {
		t.Fatalf("design.requirement items = %d, want 2 (original + AC-gap repair)", len(repairItems))
	}
	var repair *agent.WorkItem
	for i := range repairItems {
		if strings.Contains(repairItems[i].DedupeKey, ":repair:") {
			repair = &repairItems[i]
		}
	}
	if repair == nil {
		t.Fatal("no AC-gap repair generation was enqueued")
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(repair.Payload, &payload); err != nil || payload.Reason != "ac_gap" {
		t.Fatalf("repair payload reason = %q (err %v), want ac_gap", payload.Reason, err)
	}

	// Manual advance is blocked by the SAME shared gate, naming the missing AC.
	err := h.stages.Request(h.ctx(), h.versionID, versiondomain.StageCaseReview)
	if err == nil {
		t.Fatal("manual advance succeeded with an AC coverage gap")
	}
	apiErr, ok := err.(*platform.Error)
	if !ok || apiErr.Code != "ac_coverage_gap" {
		t.Fatalf("advance error = %v, want 409 ac_coverage_gap", err)
	}
	if !strings.Contains(apiErr.Message, "AC-2") {
		t.Fatalf("gap error does not name AC-2: %s", apiErr.Message)
	}

	// The author completes coverage through the repair run (full AC set).
	h.setAuthorMode("full")
	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author repair")
	completed := h.draftCards()
	union := map[string]bool{}
	for _, card := range completed {
		for _, ac := range card.CoveredACs {
			union[ac] = true
		}
	}
	if !union["AC-1"] || !union["AC-2"] {
		t.Fatalf("coverage union after repair = %v, want AC-1 + AC-2", union)
	}

	// Coverage complete -> the judge is enqueued and reviews every pending draft.
	h.reconcile()
	judgeItems := h.workItemsOfType(agentapp.WorkTypeDesignJudge)
	if len(judgeItems) != 1 {
		t.Fatalf("judge items = %d, want 1 after coverage completion", len(judgeItems))
	}
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge after repair")
	for _, card := range h.draftCards() {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePass {
			t.Fatalf("post-repair review state = %s, want pass", state)
		}
	}
	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review", stage)
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) == 0 {
		t.Fatal("no cases confirmed after the repaired coverage passed review")
	}
}

// --- T16 real-DB route coverage: admin guard + report shape -------------------

func TestJudgmentRoutesRealDB(t *testing.T) {
	h := newJudgeLoopHarness(t)
	h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("drafts = %d, want 2", len(drafts))
	}

	// Unauthenticated reads are rejected; a user reads the pending report.
	request := httptest.NewRequest("GET", fmt.Sprintf("/api/versions/%d/design/judgments", h.versionID), nil)
	recorder := httptest.NewRecorder()
	h.judgmentMux().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated report status = %d, want 401", recorder.Code)
	}
	report := h.judgmentsRoute("user")
	if report.Code != http.StatusOK {
		t.Fatalf("report status = %d, want 200 (body %s)", report.Code, report.Body.String())
	}
	data := decodeDataEnvelope(t, report)
	cases, _ := data["cases"].([]any)
	if len(cases) != 2 {
		t.Fatalf("report cases = %d, want 2 (every current case appears)", len(cases))
	}
	for _, entry := range cases {
		card := entry.(map[string]any)
		if card["review_state"] != string(design.ReviewStatePending) {
			t.Fatalf("pre-judge report review_state = %v, want pending", card["review_state"])
		}
		if card["judgment"] != nil {
			t.Fatalf("pre-judge report carries a judgment: %v", card["judgment"])
		}
	}

	// Override precondition: a pending case is not overridable (409), and a
	// non-admin never reaches the service (403).
	if recorder := h.overrideRoute(drafts[0].CaseID, "user", "尝试"); recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin override status = %d, want 403", recorder.Code)
	}
	recorder = h.overrideRoute(drafts[0].CaseID, "admin", "尝试提前 override")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("premature override status = %d, want 409 (body %s)", recorder.Code, recorder.Body.String())
	}

	// After the judge passes, the report carries the current-version fact.
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge")
	report = h.judgmentsRoute("user")
	data = decodeDataEnvelope(t, report)
	cases, _ = data["cases"].([]any)
	for _, entry := range cases {
		card := entry.(map[string]any)
		if card["review_state"] != string(design.ReviewStatePass) {
			t.Fatalf("post-judge report review_state = %v, want pass", card["review_state"])
		}
		judgment, ok := card["judgment"].(map[string]any)
		if !ok {
			t.Fatalf("post-judge report missing judgment: %v", card)
		}
		if judgment["verdict"] != design.VerdictPass || judgment["round"] != float64(1) {
			t.Fatalf("report judgment = %v, want round-1 pass", judgment)
		}
		if judgment["content_hash"] == "" || judgment["case_version_id"] != card["current_version_id"] {
			t.Fatalf("report judgment not bound to current version: %v", judgment)
		}
	}
	// A passed case is still not overridable (override is escalation-only).
	if recorder := h.overrideRoute(drafts[0].CaseID, "admin", "对通过用例 override"); recorder.Code != http.StatusConflict {
		t.Fatalf("override on pass status = %d, want 409", recorder.Code)
	}
	// Empty reason is a field error before any state check.
	badRequest := httptest.NewRequest("POST",
		fmt.Sprintf("/api/versions/%d/design/judgments/%d/override", h.versionID, drafts[0].CaseID),
		strings.NewReader(`{"reason":"  "}`))
	badRequest = badRequest.WithContext(platform.WithActor(badRequest.Context(), platform.Actor{ID: 7, Role: "admin"}))
	badRecorder := httptest.NewRecorder()
	h.judgmentMux().ServeHTTP(badRecorder, badRequest)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("empty-reason override status = %d, want 400", badRecorder.Code)
	}
}

package agentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// fakeTaskProvider implements agentapp.TaskContextProvider.
type fakeTaskProvider struct {
	task  agentapp.ExecutionTask
	err   error
	calls int
	item  agent.WorkItem
}

func (f *fakeTaskProvider) ExecutionTask(_ context.Context, item agent.WorkItem) (agentapp.ExecutionTask, error) {
	f.calls++
	f.item = item
	return f.task, f.err
}

// executionTaskFixture is the P3 execution task snapshot used by the tests.
func executionTaskFixture() agentapp.ExecutionTask {
	return agentapp.ExecutionTask{
		CaseRunID:     3001,
		WaveID:        2001,
		IntentID:      1001,
		CaseVersionID: 41,
		CaseTitle:     "登录流程",
		Description:   "用户使用账号密码登录",
		Preconditions: "已创建测试账号",
		HumanResponse: "验证码 246810",
		UserMessage:   "请逐步执行用例并记录结果",
		VersionSUTs: []agentapp.ExecutionSUT{{
			VersionSUTID:    7,
			SUTName:         "orders",
			EnvironmentName: "test",
			BaseURL:         "http://127.0.0.1:13335",
			PageURLs:        []string{"http://127.0.0.1:13335/login"},
			AccountRoles:    []string{"admin"},
		}},
		Steps: []agentapp.ExecutionStep{{
			Seq: 1, Action: "打开登录页", Expected: "出现登录表单",
			AccountRole: "admin", TargetVersionSUTID: 7,
		}},
		Assertions: []agentapp.ExecutionAssertion{{
			StepSeq: 1, AssertionSeq: 1, Kind: "ui",
			Expected: "Login 按钮可见", TargetVersionSUTID: 7,
		}},
		PriorSteps: []agentapp.ExecutionPriorStep{{
			Seq: 1, Status: "passed", Observed: "登录表单可见", TargetVersionSUTID: 7,
		}},
		PriorAssertions: []agentapp.ExecutionPriorAssertion{{
			StepSeq: 1, AssertionSeq: 1, Kind: "ui", Verdict: "passed",
			Expected: "Login 按钮可见", Actual: "Login 按钮可见", TargetVersionSUTID: 7,
		}},
	}
}

func p3CaseItem() agent.WorkItem {
	return fakeWorkItem(agentapp.WorkTypeExecuteCase, agentapp.WorkItemPayload{
		IntentID: 1001, WaveID: 2001, CaseRunID: 3001, CaseVersionID: 41,
		DispatchEpoch: 3, DispatchToken: "dispatch-token",
	})
}

func profileByKey(profiles []agent.AgentProfile, key string) (agent.AgentProfile, bool) {
	for _, profile := range profiles {
		if profile.Key == key {
			return profile, true
		}
	}
	return agent.AgentProfile{}, false
}

func hasTool(profile agent.AgentProfile, name string) bool {
	for _, allowed := range profile.AllowedTools {
		if allowed == name {
			return true
		}
	}
	return false
}

func TestParseP3WorkItemPayload(t *testing.T) {
	payload := json.RawMessage(`{
		"intent_id":"11","wave_id":"22","case_run_id":"33","case_version_id":"44",
		"dispatch_epoch":"5","dispatch_token":"token-abc"
	}`)
	parsed, err := agentapp.ParseWorkItemPayload(payload)
	if err != nil {
		t.Fatalf("ParseWorkItemPayload: %v", err)
	}
	if parsed.IntentID != 11 || parsed.WaveID != 22 || parsed.CaseRunID != 33 ||
		parsed.CaseVersionID != 44 || parsed.DispatchEpoch != 5 || parsed.DispatchToken != "token-abc" {
		t.Fatalf("payload = %+v", parsed)
	}

	if _, err := agentapp.ParseWorkItemPayload(json.RawMessage(`{"case_run_id":"not-a-number"}`)); err == nil {
		t.Fatalf("expected an error for a non-decimal case_run_id")
	}
	if _, err := agentapp.ParseWorkItemPayload(json.RawMessage(`{"dispatch_epoch":"-1"}`)); err == nil {
		t.Fatalf("expected an error for a negative dispatch_epoch")
	}
}

func TestProfileKeyForWorkTypeP3(t *testing.T) {
	cases := map[string]string{
		agentapp.WorkTypeExecuteCase:     agentapp.ProfileKeyCaseExecutor,
		agentapp.WorkTypeAnalyzeFailure:  agentapp.ProfileKeyFailureAnalyst,
		agentapp.WorkTypeAnalyzeCoverage: agentapp.ProfileKeyCoverageAnalyst,
	}
	for workType, want := range cases {
		got, ok := agentapp.ProfileKeyForWorkType(workType)
		if !ok || got != want {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q, %v; want %q", workType, got, ok, want)
		}
	}
	if _, ok := agentapp.ProfileKeyForWorkType("case.confirm"); ok {
		t.Fatalf("case.confirm must not map to a P3 profile")
	}
}

func TestP3ProfilesToolsAndBudgets(t *testing.T) {
	profiles := agentapp.P3Profiles()
	if len(profiles) != 3 {
		t.Fatalf("P3Profiles() = %d profiles, want 3", len(profiles))
	}
	executor, ok := profileByKey(profiles, agentapp.ProfileKeyCaseExecutor)
	if !ok {
		t.Fatalf("case_executor profile is missing")
	}
	if executor.Budgets.MaxTurns != 60 || executor.Budgets.SoftSeconds != 780 || executor.Budgets.HardSeconds != 900 {
		t.Fatalf("case_executor budgets = %+v", executor.Budgets)
	}
	for _, name := range []string{
		"execution.wait", "browser.goto", "browser.screenshot", "human.request",
		"execution.step_record", "execution.assertion_record", "execution.case_finish",
		"account.credentials", "db.query", "http.request",
	} {
		if !hasTool(executor, name) {
			t.Fatalf("case_executor is missing tool %q", name)
		}
	}
	if hasTool(executor, "case.confirm") || hasTool(executor, "requirement.draft_create") {
		t.Fatalf("case_executor must not hold write tools it does not own")
	}

	failure, ok := profileByKey(profiles, agentapp.ProfileKeyFailureAnalyst)
	if !ok {
		t.Fatalf("failure_analyst profile is missing")
	}
	if failure.Budgets.MaxTurns != 6 || failure.Budgets.SoftSeconds != 180 || failure.Budgets.HardSeconds != 300 {
		t.Fatalf("failure_analyst budgets = %+v", failure.Budgets)
	}
	for _, required := range []string{"context.task_pack", "context.team_room_read", "execution.result_read", "execution.evidence_read", "analysis.failure_submit"} {
		if !hasTool(failure, required) {
			t.Fatalf("failure_analyst is missing tool %q", required)
		}
	}

	coverage, ok := profileByKey(profiles, agentapp.ProfileKeyCoverageAnalyst)
	if !ok {
		t.Fatalf("coverage_analyst profile is missing")
	}
	for _, required := range []string{"context.task_pack", "requirement.read", "execution.coverage_read", "analysis.coverage_submit"} {
		if !hasTool(coverage, required) {
			t.Fatalf("coverage_analyst is missing tool %q", required)
		}
	}

	for _, profile := range []agent.AgentProfile{failure, coverage} {
		for _, allowed := range profile.AllowedTools {
			if strings.HasPrefix(allowed, "browser.") || allowed == "human.request" || allowed == "execution.wait" {
				t.Fatalf("%s must not hold execution tool %q", profile.Key, allowed)
			}
		}
	}

	for _, profile := range profiles {
		if !strings.Contains(profile.SystemPrompt, "[profile:"+profile.Key+"]") {
			t.Fatalf("%s prompt is missing the [profile:%s] marker", profile.Key, profile.Key)
		}
	}
	if !strings.Contains(executor.SystemPrompt, "execution.wait") {
		t.Fatalf("case_executor prompt must instruct the model about execution.wait")
	}
}

func TestProfilesSeedP3(t *testing.T) {
	all := agentapp.Profiles()
	repo := newFakeRuntimeRepo()
	t.Cleanup(func() {})
	if _, err := agentapp.EnsureProfiles(context.Background(), repo); err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	for _, key := range []string{
		agentapp.ProfileKeyCaseExecutor,
		agentapp.ProfileKeyFailureAnalyst,
		agentapp.ProfileKeyCoverageAnalyst,
	} {
		if _, ok := profileByKey(all, key); !ok {
			t.Fatalf("Profiles() is missing %s", key)
		}
		if _, err := repo.GetProfile(context.Background(), key); err != nil {
			t.Fatalf("EnsureProfiles did not seed %s: %v", key, err)
		}
	}
}

func TestBuildTaskBlockIncludesExecution(t *testing.T) {
	builder := agentapp.NewContextBuilder(&fakeSettings{}, nil)
	task := executionTaskFixture()
	packet, _, err := builder.Build(context.Background(), agentapp.ContextInput{
		Profile: agent.AgentProfile{
			Key:          agentapp.ProfileKeyCaseExecutor,
			SystemPrompt: "你是用例执行器。[profile:case_executor]",
		},
		Task:      agentapp.TaskContext{Type: agentapp.WorkTypeExecuteCase, Goal: "按步骤执行用例"},
		Execution: &task,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var content strings.Builder
	for _, section := range packet.Sections {
		content.WriteString(section.Content)
		content.WriteString("\n")
	}
	text := content.String()
	for _, want := range []string{
		"登录流程", "用户使用账号密码登录", "已创建测试账号",
		"打开登录页", "出现登录表单", "Login 按钮可见",
		"登录表单可见", "验证码 246810", "orders",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("context is missing %q:\n%s", want, text)
		}
	}
}

func TestRunnerUsesExecutionTaskProvider(t *testing.T) {
	provider := &fakeTaskProvider{task: executionTaskFixture()}
	fixture := newRunnerFixtureWithTaskProvider(t, provider)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "用例执行完成"}}

	item := p3CaseItem()
	result, err := fixture.runner.Run(context.Background(), item)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %+v", result)
	}
	if provider.calls != 1 || provider.item.ID != item.ID {
		t.Fatalf("provider calls = %d, item = %+v", provider.calls, provider.item)
	}
	if fixture.chat.requestCount() != 1 {
		t.Fatalf("chat calls = %d", fixture.chat.requestCount())
	}
	prompt := requestPrompt(fixture.chat.request(0))
	for _, want := range []string{"登录流程", "打开登录页", "请逐步执行用例并记录结果"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}
	if len(fixture.conv.appended) != 0 {
		t.Fatalf("P3 profile must not write conversation messages, got %+v", fixture.conv.appended)
	}
}

func TestRunnerP3WithoutTaskProviderIsToolFailure(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "unused"}}

	_, err := fixture.runner.Run(context.Background(), p3CaseItem())
	var runErr *agentapp.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("error %v is not a *RunError", err)
	}
	if runErr.Reason != "tool_failure" || runErr.Retryable {
		t.Fatalf("run error = %+v", runErr)
	}
	if fixture.chat.requestCount() != 0 {
		t.Fatalf("model must not be called without a task provider")
	}
}

func TestRunnerP3PassesScopeAndUsesP3Registry(t *testing.T) {
	provider := &fakeTaskProvider{task: executionTaskFixture()}
	fixture := newRunnerFixtureWithTaskProvider(t, provider)
	// First turn records a step through the P3 execution port, second finishes.
	fixture.chat.responses = []llmapp.ChatResponse{
		{
			ToolCalls: []llmapp.ToolCall{{
				ID:        "call_1",
				Name:      tools.ToolExecutionStepRecord,
				Arguments: `{"seq":1,"status":"passed","observed":"登录表单可见"}`,
			}},
			FinishReason: "tool_calls",
		},
		{Content: "完成"},
	}
	execution := &fakeExecutionPort{
		stepResult: map[string]any{"ok": true},
	}
	fixture.runner = agentapp.NewRunner(agentapp.RunnerDeps{
		Runtime:      fixture.repo,
		Chat:         fixture.chat,
		Settings:     fixture.settings,
		Clock:        fixture.clock,
		IDs:          &seqIDGenerator{},
		Events:       fixture.events,
		Audit:        fixture.audit,
		Versions:     fixture.versions,
		TeamRoom:     fixture.team,
		Content:      fixture.content,
		TaskProvider: provider,
		ToolDeps: tools.Deps{
			Requirement: fixture.requirement,
			Attachment:  &fakeAttachmentPort{},
			Resource:    &fakeResourcePort{},
			Design:      fixture.design,
			Execution:   execution,
		},
		InstanceID: "instance-1",
	})

	if _, err := fixture.runner.Run(context.Background(), p3CaseItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execution.stepCalls != 1 {
		t.Fatalf("step record calls = %d", execution.stepCalls)
	}
	scope := execution.lastScope
	if scope.VersionID != 10 || scope.WaveID != 2001 || scope.CaseRunID != 3001 ||
		scope.IntentID != 1001 || scope.DispatchEpoch != 3 || scope.DispatchToken != "dispatch-token" {
		t.Fatalf("scope = %+v", scope)
	}
}

func TestRunnerRegistersExecutionRunCanceller(t *testing.T) {
	provider := &fakeTaskProvider{task: executionTaskFixture()}
	fixture := newRunnerFixture(t)
	canceller := &fakeRunnerCanceller{}
	fixture.runner = agentapp.NewRunner(agentapp.RunnerDeps{
		Runtime:      fixture.repo,
		Chat:         fixture.chat,
		Settings:     fixture.settings,
		Clock:        fixture.clock,
		IDs:          &seqIDGenerator{},
		Events:       fixture.events,
		Audit:        fixture.audit,
		Versions:     fixture.versions,
		TeamRoom:     fixture.team,
		Content:      fixture.content,
		TaskProvider: provider,
		RunCanceller: canceller,
		InstanceID:   "instance-1",
	})
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "完成"}}
	if _, err := fixture.runner.Run(context.Background(), p3CaseItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if canceller.registeredCaseRunID != 3001 {
		t.Fatalf("registered case_run = %d", canceller.registeredCaseRunID)
	}
	if canceller.cancelFn == nil {
		t.Fatalf("runner did not register its cancel function")
	}
	if canceller.clearedCaseRunID != 3001 {
		t.Fatalf("cleared case_run = %d", canceller.clearedCaseRunID)
	}
}

// fakeRunnerCanceller records the pause/interrupt registration of one run.
type fakeRunnerCanceller struct {
	registeredCaseRunID int64
	clearedCaseRunID    int64
	cancelFn            context.CancelFunc
}

func (f *fakeRunnerCanceller) RegisterRun(caseRunID int64, cancel context.CancelFunc) {
	f.registeredCaseRunID = caseRunID
	f.cancelFn = cancel
}

func (f *fakeRunnerCanceller) ClearRun(caseRunID int64) {
	f.clearedCaseRunID = caseRunID
	if f.cancelFn != nil {
		f.cancelFn()
	}
}

// fakeExecutionPort records the P3 execution tool calls made by the runner.
type fakeExecutionPort struct {
	stepCalls    int
	lastScope    tools.Scope
	stepInput    tools.StepRecordInput
	assertInput  tools.AssertionRecordInput
	finishInput  tools.CaseFinishInput
	waitInput    tools.WaitInput
	humanInput   tools.HumanRequestInput
	credInput    tools.AccountCredentialsInput
	browserCalls []string
	stepResult   any
	stepErr      error
}

func (f *fakeExecutionPort) StepRecord(_ context.Context, scope tools.Scope, in tools.StepRecordInput) (any, error) {
	f.stepCalls++
	f.lastScope = scope
	f.stepInput = in
	return f.stepResult, f.stepErr
}

func (f *fakeExecutionPort) AssertionRecord(_ context.Context, scope tools.Scope, in tools.AssertionRecordInput) (any, error) {
	f.lastScope = scope
	f.assertInput = in
	return map[string]any{"ok": true}, nil
}

func (f *fakeExecutionPort) EvidenceText(_ context.Context, _ tools.Scope, _ tools.EvidenceTextInput) (any, error) {
	return map[string]any{"ok": true}, nil
}

func (f *fakeExecutionPort) CaseFinish(_ context.Context, scope tools.Scope, in tools.CaseFinishInput) (any, error) {
	f.lastScope = scope
	f.finishInput = in
	return map[string]any{"ok": true}, nil
}

func (f *fakeExecutionPort) Wait(_ context.Context, scope tools.Scope, in tools.WaitInput) (any, error) {
	f.lastScope = scope
	f.waitInput = in
	return map[string]any{"waited_seconds": in.Seconds}, nil
}

func (f *fakeExecutionPort) HumanRequest(_ context.Context, scope tools.Scope, in tools.HumanRequestInput) (any, error) {
	f.lastScope = scope
	f.humanInput = in
	return map[string]any{"status": "open"}, nil
}

func (f *fakeExecutionPort) Browser(_ context.Context, scope tools.Scope, action string, _ json.RawMessage) (any, error) {
	f.lastScope = scope
	f.browserCalls = append(f.browserCalls, action)
	return map[string]any{"ok": true}, nil
}

func (f *fakeExecutionPort) HTTP(_ context.Context, _ tools.Scope, _ json.RawMessage) (any, error) {
	return map[string]any{"status": 200}, nil
}

func (f *fakeExecutionPort) DBQuery(_ context.Context, _ tools.Scope, _ json.RawMessage) (any, error) {
	return map[string]any{"rows": []any{}}, nil
}

func (f *fakeExecutionPort) SchemaRead(_ context.Context, _ tools.Scope, _ json.RawMessage) (any, error) {
	return map[string]any{"tables": []any{}}, nil
}

func (f *fakeExecutionPort) AccountCredentials(_ context.Context, scope tools.Scope, in tools.AccountCredentialsInput) (any, error) {
	f.lastScope = scope
	f.credInput = in
	return map[string]any{"username": "u", "password": "p"}, nil
}

func (f *fakeExecutionPort) ResultRead(_ context.Context, _ tools.Scope, _ tools.ResultReadInput) (any, error) {
	return map[string]any{"results": []any{}}, nil
}

func (f *fakeExecutionPort) EvidenceRead(_ context.Context, _ tools.Scope, _ tools.EvidenceReadInput) (any, error) {
	return map[string]any{"text": ""}, nil
}

func (f *fakeExecutionPort) CoverageRead(_ context.Context, _ tools.Scope) (any, error) {
	return map[string]any{"requirements": []any{}}, nil
}

func (f *fakeExecutionPort) FailureSubmit(_ context.Context, _ tools.Scope, _ tools.FailureSubmitInput) (any, error) {
	return map[string]any{"ok": true}, nil
}

func (f *fakeExecutionPort) CoverageSubmit(_ context.Context, _ tools.Scope, _ tools.CoverageSubmitInput) (any, error) {
	return map[string]any{"ok": true}, nil
}

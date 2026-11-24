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
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// roleToolSpy is one registered tool used to prove which tools a role may see
// and call. Calls increments only when the tool actually executes.
type roleToolSpy struct {
	name  string
	calls int
}

func (s *roleToolSpy) Name() string        { return s.name }
func (s *roleToolSpy) Description() string { return "role test tool " + s.name }
func (s *roleToolSpy) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (s *roleToolSpy) Execute(context.Context, tools.Scope, json.RawMessage) (any, error) {
	s.calls++
	return map[string]any{"tool": s.name}, nil
}

func TestRoleCatalogComplete(t *testing.T) {
	type wantRole struct {
		key       agentapp.RoleKey
		name      string
		kinds     []string
		vision    bool
		reasoning bool
	}
	// The seven catalog keys, their product names, executor kinds and model
	// requirements: the six converged v0.22 roles plus the v0.2.4-E design
	// judge (type-path role, no kind-path work item kinds).
	want := []wantRole{
		{agentapp.RoleAssistant, "测试助手", []string{agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases}, true, true},
		{agentapp.RoleCaseAuthor, "设计 Author", []string{agent.WorkItemKindDesignCases}, false, true},
		{agentapp.RoleCaseReviewer, "用例评审员", []string{agent.WorkItemKindReviewCases}, false, true},
		{agentapp.RoleCaseJudge, "用例裁判", nil, false, true},
		{agentapp.RoleFailureAnalyst, "失败分析师", []string{agent.WorkItemKindAnalyzeFailure}, false, true},
		{agentapp.RoleReportWriter, "报告撰写员", []string{agent.WorkItemKindReportVersion}, false, false},
		{agentapp.RoleKnowledgeCurator, "知识维护员", []string{agent.WorkItemKindKnowledgePropose}, false, false},
	}

	catalog := agentapp.RoleCatalog()
	if len(catalog) != 7 {
		t.Fatalf("role catalog = %d roles, want 7", len(catalog))
	}
	keys := make([]agentapp.RoleKey, 0, len(catalog))
	seenKeys := map[agentapp.RoleKey]bool{}
	for _, spec := range catalog {
		if seenKeys[spec.Key] {
			t.Fatalf("duplicate role key %q", spec.Key)
		}
		seenKeys[spec.Key] = true
		keys = append(keys, spec.Key)
	}
	for i, role := range want {
		if keys[i] != role.key {
			t.Fatalf("role catalog[%d] = %q, want %q", i, keys[i], role.key)
		}
	}

	// The seeded production assembly must stay inside the role whitelists: the
	// startup validation is part of the permission-bypass defence.
	seeded, err := agentapp.EnsureProfiles(context.Background(), newFakeRuntimeRepo())
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	if err := agentapp.ValidateRoleProfiles(seeded); err != nil {
		t.Fatalf("ValidateRoleProfiles(seeded) = %v", err)
	}
	seededByKey := make(map[string]agent.AgentProfile, len(seeded))
	for _, profile := range seeded {
		seededByKey[profile.Key] = profile
	}

	for _, wantRole := range want {
		spec, ok := agentapp.Role(wantRole.key)
		if !ok {
			t.Fatalf("Role(%q) is missing", wantRole.key)
		}
		if spec.Key != wantRole.key {
			t.Fatalf("Role(%q).Key = %q", wantRole.key, spec.Key)
		}
		if !strings.Contains(spec.Name, wantRole.name) {
			t.Fatalf("role %q name = %q, want it to contain %q", spec.Key, spec.Name, wantRole.name)
		}
		if spec.Model.Tools != true {
			t.Fatalf("role %q must require a tools-capable model", spec.Key)
		}
		if spec.Model.Vision != wantRole.vision {
			t.Fatalf("role %q vision requirement = %v, want %v", spec.Key, spec.Model.Vision, wantRole.vision)
		}
		if spec.Model.Reasoning != wantRole.reasoning {
			t.Fatalf("role %q reasoning requirement = %v, want %v", spec.Key, spec.Model.Reasoning, wantRole.reasoning)
		}
		if spec.RunSeconds <= 0 || spec.MaxTurnsFuse <= 0 {
			t.Fatalf("role %q run guards = {%d,%d}, want positive defaults",
				spec.Key, spec.RunSeconds, spec.MaxTurnsFuse)
		}
		if !strings.Contains(spec.SystemPrompt, "[role:"+string(spec.Key)+"]") {
			t.Fatalf("role %q system prompt missing role marker: %s", spec.Key, spec.SystemPrompt)
		}
		if len(spec.Tools) == 0 {
			t.Fatalf("role %q has an empty tool whitelist", spec.Key)
		}
		for _, name := range spec.Tools {
			if strings.Contains(name, "*") && !strings.HasSuffix(name, ".*") {
				t.Fatalf("role %q tool %q must use exact names or a trailing .* wildcard", spec.Key, name)
			}
			if !agentapp.ToolAllowed(spec.Key, name) {
				t.Fatalf("role %q does not allow its own tool %q", spec.Key, name)
			}
		}
		if agentapp.ToolAllowed(spec.Key, "definitely.unknown") {
			t.Fatalf("role %q allowed an unknown tool", spec.Key)
		}

		if got, wantKinds := agentapp.RoleWorkItemKinds(spec.Key), wantRole.kinds; strings.Join(got, ",") != strings.Join(wantKinds, ",") {
			t.Fatalf("role %q kinds = %v, want %v", spec.Key, got, wantKinds)
		}
		for _, kind := range spec.WorkItemKinds {
			if !agent.ValidWorkItemKind(kind) {
				t.Fatalf("role %q lists invalid work item kind %q", spec.Key, kind)
			}
			if !agentapp.RoleAllowsWorkItemKind(spec.Key, kind) {
				t.Fatalf("role %q does not allow its own kind %q", spec.Key, kind)
			}
		}

		for _, profileKey := range spec.Profiles {
			profile, ok := seededByKey[profileKey]
			if !ok {
				t.Fatalf("role %q maps unknown seed profile %q", spec.Key, profileKey)
			}
			for _, name := range profile.AllowedTools {
				if !agentapp.ToolAllowed(spec.Key, name) {
					t.Fatalf("seed profile %q tool %q is outside role %q whitelist", profileKey, name, spec.Key)
				}
			}
		}
	}

	if _, ok := agentapp.Role(agentapp.RoleKey("case_executor")); ok {
		t.Fatal("case_executor is not one of the catalog roles")
	}
	if agentapp.ToolAllowed(agentapp.RoleKey("unknown"), tools.ToolContextVersionPack) {
		t.Fatal("unknown role allowed a tool")
	}
	if agentapp.RoleAllowsWorkItemKind(agentapp.RoleKey("unknown"), agent.WorkItemKindDesignCases) {
		t.Fatal("unknown role allowed a work item kind")
	}
}

// TestEnsureProfilesRejectsStoredProfileOutsideRoleWhitelist is the startup
// half of the permission-bypass defence (Task 8 fix round 1): a stored
// agent_profile row that exposes a tool outside its role — edited around the
// admin PATCH guard or written before this fix — must fail closed on the next
// startup instead of being returned to the runner.
func TestEnsureProfilesRejectsStoredProfileOutsideRoleWhitelist(t *testing.T) {
	repo := newFakeRuntimeRepo()
	ctx := context.Background()
	if _, err := agentapp.EnsureProfiles(ctx, repo); err != nil {
		t.Fatalf("seed EnsureProfiles: %v", err)
	}

	host := repo.profiles[agentapp.ProfileKeyClarificationHost]
	host.AllowedTools = append(append([]string(nil), host.AllowedTools...), tools.ToolExecutionCaseFinish)
	repo.profiles[agentapp.ProfileKeyClarificationHost] = host

	stored, err := agentapp.EnsureProfiles(ctx, repo)
	if err == nil {
		t.Fatalf("EnsureProfiles accepted a widened stored profile: %v", stored)
	}
	if stored != nil {
		t.Fatalf("EnsureProfiles returned %d profiles with error %v", len(stored), err)
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" || apiErr.Details["field"] != "allowed_tools" {
		t.Fatalf("stored-profile validation error = %#v", apiErr)
	}
	if !strings.Contains(apiErr.Message, agentapp.ProfileKeyClarificationHost) {
		t.Fatalf("error message = %q, want the profile key", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, tools.ToolExecutionCaseFinish) {
		t.Fatalf("error message = %q, want the offending tool", apiErr.Message)
	}

	// Restore the role profile and prove the startup check is role-scoped:
	// unmapped profiles (case_executor is deterministic, not one of the seven
	// roles) keep their own contract whitelist and must not block startup.
	restored := make([]string, 0, len(host.AllowedTools))
	for _, name := range host.AllowedTools {
		if name != tools.ToolExecutionCaseFinish {
			restored = append(restored, name)
		}
	}
	host.AllowedTools = restored
	repo.profiles[agentapp.ProfileKeyClarificationHost] = host
	executor := repo.profiles[agentapp.ProfileKeyCaseExecutor]
	executor.AllowedTools = append(append([]string(nil), executor.AllowedTools...), "future.unmapped.tool")
	repo.profiles[agentapp.ProfileKeyCaseExecutor] = executor
	if _, err := agentapp.EnsureProfiles(ctx, repo); err != nil {
		t.Fatalf("EnsureProfiles rejected an unmapped profile edit: %v", err)
	}
}

// TestRoleToolMatrix is the plan-named role tool-matrix test. It is the
// existing whitelist matrix (the brief's TestRoleToolMatrix) plus the frozen
// 0.2.1 work-item budget defaults.
func TestRoleToolMatrix(t *testing.T) {
	allowed := map[agentapp.RoleKey][]string{
		agentapp.RoleAssistant: {
			tools.ToolContextVersionPack, tools.ToolDesignGraphRead, tools.ToolCaseGraphPropose,
			tools.ToolRequirementDraftCreate, tools.ToolKnowledgePropose,
		},
		agentapp.RoleCaseAuthor: {
			tools.ToolCaseGraphPropose, tools.ToolDesignGraphRead, tools.ToolCodeFacts,
		},
		agentapp.RoleCaseReviewer: {
			tools.ToolDesignReviewSubmit, tools.ToolDesignGraphRead,
		},
		agentapp.RoleFailureAnalyst: {
			tools.ToolAnalysisFailureSubmit, tools.ToolExecutionResultRead, tools.ToolIssuePropose,
		},
		agentapp.RoleReportWriter: {
			tools.ToolReportDataRead, tools.ToolReportNarrativePropose,
		},
		agentapp.RoleKnowledgeCurator: {
			tools.ToolKnowledgePropose,
		},
	}
	denied := map[agentapp.RoleKey][]string{
		agentapp.RoleAssistant: {
			"exec.run", "execution.case_finish", "case.confirm", "add_intent",
			tools.ToolReportDataRead, tools.ToolIssuePropose,
		},
		agentapp.RoleCaseAuthor: {
			"exec.run", "execution.step_record", "case.confirm",
			tools.ToolDesignReviewSubmit, tools.ToolReportNarrativePropose,
		},
		agentapp.RoleCaseReviewer: {
			tools.ToolCaseGraphPropose, "execution.case_finish", "case.confirm",
		},
		agentapp.RoleFailureAnalyst: {
			"exec.run", "execution.step_record", "case.confirm",
		},
		agentapp.RoleReportWriter: {
			"exec.run", "execution.step_record", tools.ToolCaseGraphPropose, "case.confirm",
		},
		agentapp.RoleKnowledgeCurator: {
			tools.ToolRequirementDraftCreate, tools.ToolCaseGraphPropose, "exec.run", "case.confirm",
		},
	}

	// Spec §5.4 default work-item run guards are frozen for every role; the
	// legacy token cap is telemetry-only metadata and was removed (Task 22).
	for _, spec := range agentapp.RoleCatalog() {
		if spec.RunSeconds != 120 || spec.MaxTurnsFuse != 24 {
			t.Fatalf("role %q run guards = {%d,%d}, want {120,24}",
				spec.Key, spec.RunSeconds, spec.MaxTurnsFuse)
		}
	}

	for role, names := range allowed {
		for _, name := range names {
			if !agentapp.ToolAllowed(role, name) {
				t.Fatalf("role %q must allow %q", role, name)
			}
		}
	}
	for role, names := range denied {
		for _, name := range names {
			if agentapp.ToolAllowed(role, name) {
				t.Fatalf("role %q must not allow %q", role, name)
			}
		}
	}
	// Every role is a closed whitelist: unlisted registered tools and fully
	// unknown names are denied.
	for _, role := range agentapp.RoleKeys() {
		for _, name := range []string{"confirm.*", "add_intent", "exec.run", "execution.case_finish"} {
			if agentapp.ToolAllowed(role, name) {
				t.Fatalf("role %q must not allow %q", role, name)
			}
		}
		if agentapp.ToolAllowed(role, "totally.unregistered") {
			t.Fatalf("role %q allowed an unregistered tool", role)
		}
	}

	// See only allowed tools: Definitions must be the role's intersection with
	// the registry, and execution must return a typed forbidden error for a
	// denied (but registered) tool without executing it.
	versionPack := &roleToolSpy{name: tools.ToolContextVersionPack}
	graphRead := &roleToolSpy{name: tools.ToolDesignGraphRead}
	execRunSpy := &roleToolSpy{name: "exec.run"}
	caseConfirm := &roleToolSpy{name: "case.confirm"}
	registry := tools.NewRegistry(versionPack, graphRead, execRunSpy, caseConfirm)

	roleTools, ok := agentapp.RoleToolSet(agentapp.RoleAssistant)
	if !ok {
		t.Fatal("assistant role tool set is missing")
	}
	definitions := roleTools.Definitions(registry)
	seen := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		seen[definition.Name] = true
	}
	if !seen[tools.ToolContextVersionPack] || !seen[tools.ToolDesignGraphRead] {
		t.Fatalf("assistant definitions = %v, want its allowed registered tools", seen)
	}
	if seen["exec.run"] || seen["case.confirm"] {
		t.Fatalf("assistant definitions leaked denied tools: %v", seen)
	}

	_, err := roleTools.Execute(context.Background(), registry, tools.Scope{}, "exec.run", nil)
	var toolErr *tools.Error
	if !errors.As(err, &toolErr) || toolErr.Code != tools.CodeForbidden {
		t.Fatalf("denied tool error = %v, want typed %q", err, tools.CodeForbidden)
	}
	if execRunSpy.calls != 0 {
		t.Fatalf("denied tool executed %d times", execRunSpy.calls)
	}
	payload, err := roleTools.Execute(context.Background(), registry, tools.Scope{}, tools.ToolContextVersionPack, nil)
	if err != nil {
		t.Fatalf("allowed tool execute: %v", err)
	}
	if versionPack.calls != 1 || payload == nil {
		t.Fatalf("allowed tool calls = %d, payload = %v", versionPack.calls, payload)
	}

	// A role guard records one audit event on denial (review focus 5), never
	// the tool arguments, and delegates allowed calls.
	audit := &fakeAuditRecorder{}
	guard, err := agentapp.NewRoleToolGuard(agentapp.RoleAssistant, audit)
	if err != nil {
		t.Fatalf("NewRoleToolGuard: %v", err)
	}
	_, err = guard.Execute(context.Background(), registry, tools.Scope{VersionID: 7}, "case.confirm", nil)
	if !errors.As(err, &toolErr) || toolErr.Code != tools.CodeForbidden {
		t.Fatalf("guarded denied tool error = %v, want typed %q", err, tools.CodeForbidden)
	}
	if len(audit.events) != 1 {
		t.Fatalf("denial audit events = %d, want 1", len(audit.events))
	}
	event := audit.events[0]
	if event.Action != agentapp.ActionRoleToolDenied {
		t.Fatalf("audit action = %q, want %q", event.Action, agentapp.ActionRoleToolDenied)
	}
	if event.Metadata["role"] != string(agentapp.RoleAssistant) || event.Metadata["tool"] != "case.confirm" {
		t.Fatalf("audit metadata = %#v", event.Metadata)
	}
	if _, err := guard.Execute(context.Background(), registry, tools.Scope{}, tools.ToolContextVersionPack, nil); err != nil {
		t.Fatalf("guarded allowed tool: %v", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("allowed tool wrote audit: %d events", len(audit.events))
	}
	if _, err := agentapp.NewRoleToolGuard(agentapp.RoleKey("unknown"), nil); err == nil {
		t.Fatal("NewRoleToolGuard accepted an unknown role")
	}
}

func TestRoleWorkItemMatrix(t *testing.T) {
	want := map[agentapp.RoleKey][]string{
		agentapp.RoleAssistant:    {agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases},
		agentapp.RoleCaseAuthor:   {agent.WorkItemKindDesignCases},
		agentapp.RoleCaseReviewer: {agent.WorkItemKindReviewCases},
		// The judge consumes the TYPE-path design.judge/design.rework work; it
		// owns no kind-path kinds and stays outside the creator matrix below.
		agentapp.RoleCaseJudge:        nil,
		agentapp.RoleFailureAnalyst:   {agent.WorkItemKindAnalyzeFailure},
		agentapp.RoleReportWriter:     {agent.WorkItemKindReportVersion},
		agentapp.RoleKnowledgeCurator: {agent.WorkItemKindKnowledgePropose},
	}

	// Task 7's four-way creator matrix (dispatch.go actorMayCreate). A role may
	// only request/execute kinds its creator is allowed to create; the role
	// union must never exceed that matrix. The planner actor no longer creates
	// any kind after the explore.* removal.
	actorKinds := map[string][]string{
		agent.WorkItemCreatedByStateMachine: {agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases, agent.WorkItemKindExecuteBatch, agent.WorkItemKindReportVersion},
		agent.WorkItemCreatedByAssistant:    {agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases},
		agent.WorkItemCreatedByTrigger:      {agent.WorkItemKindReportVersion, agent.WorkItemKindAnalyzeFailure, agent.WorkItemKindKnowledgePropose},
	}
	roleCreator := map[agentapp.RoleKey]string{
		agentapp.RoleAssistant:        agent.WorkItemCreatedByAssistant,
		agentapp.RoleCaseAuthor:       agent.WorkItemCreatedByStateMachine,
		agentapp.RoleCaseReviewer:     agent.WorkItemCreatedByStateMachine,
		agentapp.RoleFailureAnalyst:   agent.WorkItemCreatedByTrigger,
		agentapp.RoleReportWriter:     agent.WorkItemCreatedByTrigger,
		agentapp.RoleKnowledgeCurator: agent.WorkItemCreatedByTrigger,
	}

	if len(want) != len(agentapp.RoleKeys()) {
		t.Fatalf("matrix covers %d roles, catalog has %d", len(want), len(agentapp.RoleKeys()))
	}
	for role, kinds := range want {
		if got := agentapp.RoleWorkItemKinds(role); strings.Join(got, ",") != strings.Join(kinds, ",") {
			t.Fatalf("role %q kinds = %v, want %v", role, got, kinds)
		}
		creatorKinds := make(map[string]bool, len(actorKinds[roleCreator[role]]))
		for _, kind := range actorKinds[roleCreator[role]] {
			creatorKinds[kind] = true
		}
		for _, kind := range kinds {
			if !creatorKinds[kind] {
				t.Fatalf("role %q kind %q is outside the Task 7 %s matrix", role, kind, roleCreator[role])
			}
		}
		if agentapp.RoleAllowsWorkItemKind(role, agent.WorkItemKindExecuteBatch) {
			t.Fatalf("role %q must not own execute.batch (deterministic executor)", role)
		}
	}

	// The assistant subset must match exactly what Task 7's controlled bypass
	// accepts; the negative kinds are rejected with 403 before any stage check.
	dispatcher, repo, _, _ := newTestDispatcher(version.StageDesigning)
	for _, kind := range want[agentapp.RoleAssistant] {
		if _, err := dispatcher.RequestJob(context.Background(), 42, kind, json.RawMessage(`{"requirement_ids":["1"]}`)); err != nil {
			t.Fatalf("assistant RequestJob(%s): %v", kind, err)
		}
	}
	for _, kind := range []string{agent.WorkItemKindExecuteBatch, agent.WorkItemKindReportVersion} {
		if _, err := dispatcher.RequestJob(context.Background(), 42, kind, nil); err == nil {
			t.Fatalf("assistant RequestJob(%s) succeeded, want forbidden", kind)
		} else {
			requirePlatformStatus(t, err, 403)
		}
	}
	// The removed explore kinds are rejected before any stage check.
	for _, kind := range []string{"explore.login", "explore.page", "explore.api", "explore.coverage", "explore.gap"} {
		if _, err := dispatcher.RequestJob(context.Background(), 42, kind, nil); err == nil {
			t.Fatalf("assistant RequestJob(%s) succeeded, want forbidden/invalid", kind)
		}
	}
	if got := repo.count(); got != len(want[agentapp.RoleAssistant]) {
		t.Fatalf("assistant work items = %d, want %d", got, len(want[agentapp.RoleAssistant]))
	}

	// No catalog role may execute or dispatch an executor kind that is not in
	// its matrix.
	for _, role := range agentapp.RoleKeys() {
		for _, kind := range []string{agent.WorkItemKindExecuteBatch} {
			if agentapp.RoleAllowsWorkItemKind(role, kind) {
				t.Fatalf("role %q allowed executor kind %q", role, kind)
			}
		}
	}
}

// TestRoleCatalogRemovesDeadRolesKeepsExecutors pins the v0.22 D-B1 role
// convergence (R-B1-1..R-B1-3): the Explorer role and the requirement-curator
// mapping are gone, while the execution/analysis profiles (case_executor and
// knowledge_curator included) are explicitly retained. Profile is the seed
// lookup over Profiles(); it must never resolve a removed profile.
func TestRoleCatalogRemovesDeadRolesKeepsExecutors(t *testing.T) {
	if _, ok := agentapp.Role("explorer"); ok {
		t.Fatal("explorer must be removed")
	}
	if _, ok := agentapp.Role("requirement_curator"); ok {
		t.Fatal("requirement_curator must be removed")
	}
	if _, ok := agentapp.Profile(agentapp.ProfileKeyCaseExecutor); !ok {
		t.Fatal("case_executor profile must remain")
	}
	if _, ok := agentapp.Profile(agentapp.ProfileKeyKnowledgeCurator); !ok {
		t.Fatal("knowledge_curator profile must remain")
	}
}

// TestExploreWorkItemKindsRemoved pins R-W1-5/D-B1: the five explore work item
// kinds leave the 0.2.1 kind enum and its claim-default matrix. The server-side
// P9 discovery pipeline stays, but no agent-facing explore kind is claimable.
func TestExploreWorkItemKindsRemoved(t *testing.T) {
	for _, kind := range []string{
		"explore.login", "explore.page", "explore.api", "explore.coverage", "explore.gap",
	} {
		if agent.ValidWorkItemKind(kind) {
			t.Fatalf("%s is still a valid work item kind", kind)
		}
		if _, _, ok := agent.ClaimDefaultsForKind(kind); ok {
			t.Fatalf("%s still has claim defaults", kind)
		}
	}
}

func TestAssistantCannotExecutor(t *testing.T) {
	for _, name := range []string{
		"execution.case_finish", "execution.step_record",
		"exec.run", "shell.read", "case.confirm", "add_intent",
	} {
		if agentapp.ToolAllowed(agentapp.RoleAssistant, name) {
			t.Fatalf("assistant may not call %q", name)
		}
	}
	for _, tool := range []string{
		tools.ToolExecutionCaseFinish, tools.ToolExecRun, tools.ToolShellRead,
	} {
		if agentapp.ToolAllowed(agentapp.RoleAssistant, tool) {
			t.Fatalf("assistant may not call executor tool %q", tool)
		}
	}
	if !agentapp.ToolAllowed(agentapp.RoleAssistant, tools.ToolCaseGraphPropose) {
		t.Fatal("assistant must keep the design tool set (case.graph_propose)")
	}
	if !agentapp.ToolAllowed(agentapp.RoleAssistant, tools.ToolContextVersionPack) {
		t.Fatal("assistant must keep the clarification tool set (context.version_pack)")
	}
	if !agentapp.ToolAllowed(agentapp.RoleAssistant, tools.ToolRequirementDraftCreate) {
		t.Fatal("assistant must be able to submit requirement drafts")
	}

	for _, kind := range []string{
		agent.WorkItemKindExecuteBatch, agent.WorkItemKindReportVersion,
		"explore.page", agent.WorkItemKindAnalyzeFailure,
		agent.WorkItemKindKnowledgePropose,
	} {
		if agentapp.RoleAllowsWorkItemKind(agentapp.RoleAssistant, kind) {
			t.Fatalf("assistant may not dispatch executor kind %q", kind)
		}
	}

	dispatcher, repo, _, _ := newTestDispatcher(version.StageDesigning)
	if _, err := dispatcher.RequestJob(context.Background(), 7, agent.WorkItemKindExecuteBatch, nil); err == nil {
		t.Fatal("assistant RequestJob(execute.batch) succeeded")
	} else {
		requirePlatformStatus(t, err, 403)
	}
	if repo.count() != 0 {
		t.Fatalf("forbidden job.start created %d work items", repo.count())
	}
	item, err := dispatcher.RequestJob(context.Background(), 7, agent.WorkItemKindDesignCases, json.RawMessage(`{"requirement_ids":["1"]}`))
	if err != nil {
		t.Fatalf("assistant RequestJob(design.cases): %v", err)
	}
	if item.Kind != agent.WorkItemKindDesignCases || item.CreatedBy != agent.WorkItemCreatedByAssistant {
		t.Fatalf("allowed job.start = %#v", item)
	}
}

// TestDesignRolePromptsTeachTheNaturalLanguageCaseContract pins the two design
// role prompts against the v0.2.4-B case contract.
//
// This is the roles.go equivalent of the mustSay table profiles_test.go uses for
// the design profiles, and it exists because roles.go had no such guard: the
// case_author and case_reviewer prompts are the exact twins of designAuthorPrompt
// / designCriticPrompt, and they were left teaching the pre-B contract -- the
// author was told a case must carry a "目标 SUT" while omitting all three now
// required elements, and the reviewer was told to file findings against "目标 SUT
// 错误", a field that no longer exists, while not checking covered_acs, the rule
// that replaced it. Without this table the fix silently re-drifts.
func TestDesignRolePromptsTeachTheNaturalLanguageCaseContract(t *testing.T) {
	specs := map[agentapp.RoleKey]agentapp.RoleSpec{}
	for _, spec := range agentapp.RoleCatalog() {
		specs[spec.Key] = spec
	}

	want := map[agentapp.RoleKey]struct {
		// mustSay are substrings the prompt must carry: the required contract
		// elements and the replacement review rule.
		mustSay []string
		// mustNotSay are substrings that would mean the prompt is still teaching
		// the retired resource-target contract.
		mustNotSay []string
	}{
		agentapp.RoleCaseAuthor: {
			mustSay: []string{
				"sut_description", "preconditions", "covered_acs",
				"target_description", "sut_id",
				// The escape hatch has to be spelled out or the author leaves
				// preconditions blank and the proposal gate rejects it.
				"无",
				// covered_acs must be drawn from the requirement's own set, or
				// the author invents ids and the linkage gate rejects them.
				"该需求当前的 AC 集",
			},
			mustNotSay: []string{"目标 SUT", "version_sut_ids", "default_version_sut_id", "database_id"},
		},
		agentapp.RoleCaseReviewer: {
			mustSay: []string{
				// The rule that replaced the retired ownership check.
				"covered_acs", "sut_description",
			},
			mustNotSay: []string{"目标 SUT 错误", "version_sut", "database_id"},
		},
	}

	for role, expectation := range want {
		spec, ok := specs[role]
		if !ok {
			t.Fatalf("role %q is missing from the catalog", role)
		}
		for _, want := range expectation.mustSay {
			if !strings.Contains(spec.SystemPrompt, want) {
				t.Fatalf("role %q prompt no longer teaches %q -- the case contract has re-drifted:\n%s",
					role, want, spec.SystemPrompt)
			}
		}
		for _, gone := range expectation.mustNotSay {
			if strings.Contains(spec.SystemPrompt, gone) {
				t.Fatalf("role %q prompt still teaches the retired %q:\n%s", role, gone, spec.SystemPrompt)
			}
		}
	}
}

// TestDesignRolePromptsAgreeWithTheDesignProfilePrompts pins the twin relationship
// the audit found broken: roles.go case_author and profiles.go designAuthorPrompt
// reach the same model on the same job, so a contract term present in one and
// absent in the other means the model is being told two different things. The
// check is per-pair (the author pair on four contract terms, the critic pair on
// two) plus the retired "目标 SUT" phrase, which is enough to catch a one-sided
// edit of either twin.
func TestDesignRolePromptsAgreeWithTheDesignProfilePrompts(t *testing.T) {
	roles := map[agentapp.RoleKey]agentapp.RoleSpec{}
	for _, spec := range agentapp.RoleCatalog() {
		roles[spec.Key] = spec
	}
	profiles := map[string]agent.AgentProfile{}
	for _, profile := range agentapp.DesignProfiles() {
		profiles[profile.Key] = profile
	}

	// Each pair carries its OWN term list, because the two roles legitimately
	// teach different things: the author must state every contract element it has
	// to produce, while the critic reviews coverage and consistency and has no
	// reason to spell out preconditions or sut_id. One shared list would either
	// under-check the author or demand text the critic should not carry.
	pairs := []struct {
		role    agentapp.RoleKey
		profile string
		terms   []string
	}{
		{agentapp.RoleCaseAuthor, agentapp.ProfileKeyDesignAuthor,
			// All THREE required elements plus the db-assertion half. An earlier
			// revision compared only sut_description and covered_acs while its
			// comment claimed three, so a one-sided edit dropping preconditions
			// survived; sut_id drifted one-sidedly once already (M-4).
			[]string{"sut_description", "preconditions", "covered_acs", "sut_id"}},
		{agentapp.RoleCaseReviewer, agentapp.ProfileKeyDesignCritic,
			// The critic must check the rule that replaced the retired ownership
			// check (covered_acs) and the consistency of the system under test.
			[]string{"sut_description", "covered_acs"}},
	}
	for _, pair := range pairs {
		roleSpec, ok := roles[pair.role]
		if !ok {
			t.Fatalf("role %q missing from the catalog", pair.role)
		}
		profile, ok := profiles[pair.profile]
		if !ok {
			t.Fatalf("profile %q missing from DesignProfiles()", pair.profile)
		}
		for _, term := range pair.terms {
			roleHas := strings.Contains(roleSpec.SystemPrompt, term)
			profileHas := strings.Contains(profile.SystemPrompt, term)
			if roleHas != profileHas {
				t.Fatalf("role %q (has %t) and profile %q (has %t) disagree about %q -- the model gets contradictory instructions",
					pair.role, roleHas, pair.profile, profileHas, term)
			}
			if !roleHas {
				t.Fatalf("neither role %q nor profile %q teaches the required %q", pair.role, pair.profile, term)
			}
		}
		for _, gone := range []string{"目标 SUT"} {
			if strings.Contains(roleSpec.SystemPrompt, gone) || strings.Contains(profile.SystemPrompt, gone) {
				t.Fatalf("role %q / profile %q still teaches the retired %q", pair.role, pair.profile, gone)
			}
		}
	}
}

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
	// The seven D19 keys, their spec §5.2 product names, executor kinds and
	// model requirements.
	want := []wantRole{
		{agentapp.RoleAssistant, "测试助手", []string{agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases}, true, true},
		{agentapp.RoleCaseAuthor, "用例编写员", []string{agent.WorkItemKindDesignCases}, false, true},
		{agentapp.RoleCaseReviewer, "用例评审员", []string{agent.WorkItemKindReviewCases}, false, true},
		{agentapp.RoleExplorer, "探索者", []string{
			agent.WorkItemKindExploreLogin, agent.WorkItemKindExplorePage,
			agent.WorkItemKindExploreAPI, agent.WorkItemKindExploreCoverage,
			agent.WorkItemKindExploreGap,
		}, true, true},
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
		if spec.MaxTokens <= 0 || spec.RunSeconds <= 0 || spec.MaxTurnsFuse <= 0 {
			t.Fatalf("role %q budget = {%d,%d,%d}, want positive defaults",
				spec.Key, spec.MaxTokens, spec.RunSeconds, spec.MaxTurnsFuse)
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
		t.Fatal("case_executor is not one of the seven D19 roles")
	}
	if agentapp.ToolAllowed(agentapp.RoleKey("unknown"), tools.ToolContextVersionPack) {
		t.Fatal("unknown role allowed a tool")
	}
	if agentapp.RoleAllowsWorkItemKind(agentapp.RoleKey("unknown"), agent.WorkItemKindDesignCases) {
		t.Fatal("unknown role allowed a work item kind")
	}
}

func TestToolWhitelist(t *testing.T) {
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
		agentapp.RoleExplorer: {
			"explore.open", "explore.snapshot", tools.ToolCodeTree, tools.ToolSUTPagePropose,
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
			"browser.goto", "execution.case_finish", "case.confirm", "add_intent",
			tools.ToolReportDataRead, tools.ToolVersionClosePropose,
		},
		agentapp.RoleCaseAuthor: {
			"browser.goto", "execution.step_record", "case.confirm",
			tools.ToolDesignReviewSubmit, tools.ToolReportNarrativePropose,
		},
		agentapp.RoleCaseReviewer: {
			tools.ToolCaseGraphPropose, "execution.case_finish", "case.confirm",
		},
		agentapp.RoleExplorer: {
			"browser.goto", "add_intent", "case.confirm", tools.ToolDesignReviewSubmit,
		},
		agentapp.RoleFailureAnalyst: {
			"browser.goto", "execution.step_record", "case.confirm",
		},
		agentapp.RoleReportWriter: {
			"browser.goto", "execution.step_record", tools.ToolCaseGraphPropose, "case.confirm",
		},
		agentapp.RoleKnowledgeCurator: {
			tools.ToolRequirementDraftCreate, tools.ToolCaseGraphPropose, "browser.goto", "case.confirm",
		},
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
		for _, name := range []string{"confirm.*", "add_intent", "browser.goto", "execution.case_finish"} {
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
	browserGoto := &roleToolSpy{name: "browser.goto"}
	caseConfirm := &roleToolSpy{name: "case.confirm"}
	registry := tools.NewRegistry(versionPack, graphRead, browserGoto, caseConfirm)

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
	if seen["browser.goto"] || seen["case.confirm"] {
		t.Fatalf("assistant definitions leaked denied tools: %v", seen)
	}

	_, err := roleTools.Execute(context.Background(), registry, tools.Scope{}, "browser.goto", nil)
	var toolErr *tools.Error
	if !errors.As(err, &toolErr) || toolErr.Code != tools.CodeForbidden {
		t.Fatalf("denied tool error = %v, want typed %q", err, tools.CodeForbidden)
	}
	if browserGoto.calls != 0 {
		t.Fatalf("denied tool executed %d times", browserGoto.calls)
	}
	payload, err := roleTools.Execute(context.Background(), registry, tools.Scope{}, tools.ToolContextVersionPack, nil)
	if err != nil {
		t.Fatalf("allowed tool execute: %v", err)
	}
	if versionPack.calls != 1 || payload == nil {
		t.Fatalf("allowed tool calls = %d, payload = %v", versionPack.calls, payload)
	}

	// Unknown names are also a typed failure; wildcard roles still let the
	// registry reject a not-yet-registered explore tool instead of silently
	// pretending it exists.
	explorerTools, ok := agentapp.RoleToolSet(agentapp.RoleExplorer)
	if !ok {
		t.Fatal("explorer role tool set is missing")
	}
	_, err = explorerTools.Execute(context.Background(), registry, tools.Scope{}, "explore.unregistered", nil)
	if !errors.As(err, &toolErr) || toolErr.Code != tools.CodeInvalidArgument {
		t.Fatalf("unknown explore tool error = %v, want typed %q", err, tools.CodeInvalidArgument)
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
		agentapp.RoleAssistant:        {agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases},
		agentapp.RoleCaseAuthor:       {agent.WorkItemKindDesignCases},
		agentapp.RoleCaseReviewer:     {agent.WorkItemKindReviewCases},
		agentapp.RoleExplorer:         {agent.WorkItemKindExploreLogin, agent.WorkItemKindExplorePage, agent.WorkItemKindExploreAPI, agent.WorkItemKindExploreCoverage, agent.WorkItemKindExploreGap},
		agentapp.RoleFailureAnalyst:   {agent.WorkItemKindAnalyzeFailure},
		agentapp.RoleReportWriter:     {agent.WorkItemKindReportVersion},
		agentapp.RoleKnowledgeCurator: {agent.WorkItemKindKnowledgePropose},
	}

	// Task 7's four-way creator matrix (dispatch.go actorMayCreate). A role may
	// only request/execute kinds its creator is allowed to create; the role
	// union must never exceed that matrix.
	actorKinds := map[string][]string{
		agent.WorkItemCreatedByStateMachine: {agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases, agent.WorkItemKindExecuteBatch, agent.WorkItemKindReportVersion},
		agent.WorkItemCreatedByPlanner:      {agent.WorkItemKindExploreLogin, agent.WorkItemKindExplorePage, agent.WorkItemKindExploreAPI, agent.WorkItemKindExploreCoverage, agent.WorkItemKindExploreGap},
		agent.WorkItemCreatedByAssistant:    {agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases},
		agent.WorkItemCreatedByTrigger:      {agent.WorkItemKindReportVersion, agent.WorkItemKindAnalyzeFailure, agent.WorkItemKindKnowledgePropose},
	}
	roleCreator := map[agentapp.RoleKey]string{
		agentapp.RoleAssistant:        agent.WorkItemCreatedByAssistant,
		agentapp.RoleCaseAuthor:       agent.WorkItemCreatedByStateMachine,
		agentapp.RoleCaseReviewer:     agent.WorkItemCreatedByStateMachine,
		agentapp.RoleExplorer:         agent.WorkItemCreatedByPlanner,
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
	for _, kind := range []string{agent.WorkItemKindExecuteBatch, agent.WorkItemKindReportVersion, agent.WorkItemKindExplorePage} {
		if _, err := dispatcher.RequestJob(context.Background(), 42, kind, nil); err == nil {
			t.Fatalf("assistant RequestJob(%s) succeeded, want forbidden", kind)
		} else {
			requirePlatformStatus(t, err, 403)
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

func TestAssistantCannotExecutor(t *testing.T) {
	for _, name := range []string{
		"execution.case_finish", "execution.step_record",
		"browser.goto", "browser.snapshot", "case.confirm", "add_intent",
	} {
		if agentapp.ToolAllowed(agentapp.RoleAssistant, name) {
			t.Fatalf("assistant may not call %q", name)
		}
	}
	for _, tool := range []string{
		tools.ToolExecutionCaseFinish, tools.ToolBrowserGoto, tools.ToolBrowserSnapshot,
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
		agent.WorkItemKindExplorePage, agent.WorkItemKindAnalyzeFailure,
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

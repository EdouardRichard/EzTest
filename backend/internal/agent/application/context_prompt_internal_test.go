package agentapp

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func internalPromptInput() ContextInput {
	return ContextInput{
		Profile: agent.AgentProfile{
			Key:          ProfileKeyClarificationHost,
			Name:         "澄清 Host",
			SystemPrompt: "[profile:clarification_host] 版本 {{.VersionTitle}}",
		},
		Task: TaskContext{
			Type:        WorkTypeChatTurn,
			Goal:        "回答用户问题",
			Constraints: []string{"不能替用户确认需求"},
		},
		VersionPack: VersionPack{VersionID: 10, Title: "订单系统 Q4", Status: "running"},
	}
}

func TestSystemContentRendersPromptBeforeTaskBlock(t *testing.T) {
	in := internalPromptInput()
	system := systemContent(in)

	if !strings.Contains(system, "[profile:clarification_host] 版本 订单系统 Q4") {
		t.Fatalf("rendered prompt missing: %s", system)
	}
	promptIndex := strings.Index(system, "[profile:clarification_host]")
	taskIndex := strings.Index(system, "# 任务")
	if promptIndex < 0 || taskIndex < 0 || promptIndex > taskIndex {
		t.Fatalf("system order = %d/%d: %s", promptIndex, taskIndex, system)
	}
}

func TestMinimalSystemContentRendersPromptAfterTaskBlock(t *testing.T) {
	in := internalPromptInput()
	in.Execution = &ExecutionTask{
		CaseRunID: 3001, CaseTitle: "登录流程", Priority: "P1",
	}
	in.Profile.Key = ProfileKeyCaseExecutor
	in.Profile.SystemPrompt = "[profile:case_executor] 版本 {{.VersionTitle}}（{{.VersionStatus}}）"

	system := minimalSystemContent(in)
	if !strings.Contains(system, "版本 订单系统 Q4（running）") {
		t.Fatalf("minimal prompt not rendered: %s", system)
	}
	taskIndex := strings.Index(system, "# 任务")
	executionIndex := strings.Index(system, "# 执行任务")
	promptIndex := strings.Index(system, "[profile:case_executor]")
	if taskIndex < 0 || executionIndex < 0 || promptIndex < 0 {
		t.Fatalf("minimal system blocks missing: %s", system)
	}
	if !(taskIndex < executionIndex && executionIndex < promptIndex) {
		t.Fatalf("minimal block order = %d/%d/%d: %s", taskIndex, executionIndex, promptIndex, system)
	}
}

// TestStableSystemContentExcludesTaskContext pins the Task 80 split: the part
// of the system content that feeds the cache-stable prefix is the rendered
// profile prompt only. The per-work-item task block and the P3 execution block
// must never leak into it, or every work item gets a unique provider prefix.
func TestStableSystemContentExcludesTaskContext(t *testing.T) {
	in := internalPromptInput()
	in.Task.Constraints = []string{"work_item_id=1001 不能替用户确认需求"}
	in.Task.Scope = []string{"work_item_id=1001", "requirement_id=501"}
	in.Execution = &ExecutionTask{
		CaseRunID: 3001, CaseVersionID: 41, CaseTitle: "登录流程", Priority: "P1",
		Steps: []ExecutionStep{{Seq: 1, Action: "打开登录页", Expected: "出现登录表单"}},
	}

	stable := stableSystemContent(in)
	if !strings.Contains(stable, "[profile:clarification_host] 版本 订单系统 Q4") {
		t.Fatalf("stable system content lost the rendered profile prompt: %s", stable)
	}
	for _, leaked := range []string{
		"# 任务", "# 执行任务", "work_item_id=1001", "requirement_id=501",
		"不能替用户确认需求", "登录流程", "打开登录页",
	} {
		if strings.Contains(stable, leaked) {
			t.Fatalf("per-work-item %q leaked into the stable system content:\n%s", leaked, stable)
		}
	}
}

// TestTaskContextBlockKeepsTaskAndExecution pins that the extracted task
// context still carries goal, constraints, acceptance, scope and the P3
// execution snapshot, and that the legacy context path (renderSections/minimal
// fallback) keeps rendering both blocks in the system section.
func TestTaskContextBlockKeepsTaskAndExecution(t *testing.T) {
	in := internalPromptInput()
	in.Execution = &ExecutionTask{
		CaseRunID: 3001, CaseVersionID: 41, CaseTitle: "登录流程", Priority: "P1",
		Steps: []ExecutionStep{{Seq: 1, Action: "打开登录页", Expected: "出现登录表单"}},
	}

	block := taskContextBlock(in)
	for _, want := range []string{
		"# 任务", "类型: chat.turn", "目标: 回答用户问题",
		"约束:", "不能替用户确认需求", "# 执行任务", "登录流程", "打开登录页",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("task context block misses %q:\n%s", want, block)
		}
	}

	legacy := systemContent(in)
	if !strings.Contains(legacy, "[profile:clarification_host] 版本 订单系统 Q4") ||
		!strings.Contains(legacy, block) {
		t.Fatalf("legacy system content lost the prompt or the task context:\n%s", legacy)
	}
}

func TestSystemContentFallsBackToSeedOnRenderFailure(t *testing.T) {
	in := internalPromptInput()
	in.Profile.SystemPrompt = "[profile:clarification_host] {{.Bogus}}"

	system := systemContent(in)
	if strings.Contains(system, "{{.Bogus}}") {
		t.Fatalf("broken template rendered: %s", system)
	}
	defaultPrompt, ok := DefaultPrompt(ProfileKeyClarificationHost)
	if !ok {
		t.Fatal("default prompt missing")
	}
	firstLine := strings.SplitN(defaultPrompt, "\n", 2)[0]
	if !strings.Contains(system, firstLine) {
		t.Fatalf("default prompt fallback missing: %s", system)
	}
	if !strings.Contains(system, "[profile:clarification_host]") {
		t.Fatalf("marker lost in fallback: %s", system)
	}
}

func TestSystemContentKeepsConfiguredBodyForUnknownProfile(t *testing.T) {
	in := internalPromptInput()
	in.Profile.Key = "future_profile"
	in.Profile.SystemPrompt = "[profile:future_profile] {{.Bogus}}"

	system := systemContent(in)
	if !strings.Contains(system, "[profile:future_profile] {{.Bogus}}") {
		t.Fatalf("unknown profile body not retained: %s", system)
	}
}

func TestSystemContentKeepsPlainPromptUnchanged(t *testing.T) {
	in := internalPromptInput()
	in.Profile.SystemPrompt = "[profile:clarification_host] 你是澄清 Host。"
	system := systemContent(in)
	if !strings.HasPrefix(system, "[profile:clarification_host] 你是澄清 Host。") {
		t.Fatalf("plain prompt changed: %s", system)
	}
}

// TestTaskContextNotDuplicatedOnEmptyPrefix pins the defensive system-message
// fallback of the cache-first runner. When an assembled packet carries no
// prefix, the runner falls back to the stable system content only: the
// per-work-item task block already lives in the user turn, so a fallback that
// rendered the legacy systemContent (prompt + task context) would send the
// block twice, and an oversized block duplicated in the prompt/transcript can
// push a run over the window.
func TestTaskContextNotDuplicatedOnEmptyPrefix(t *testing.T) {
	in := internalPromptInput()
	in.Task.Constraints = []string{"work_item_id=1001 不能替用户确认需求"}
	in.Task.AcceptanceCriteria = []string{"给出可执行的下一步"}
	in.Task.Scope = []string{"work_item_id=1001", "requirement_id=501"}
	in.Execution = &ExecutionTask{
		CaseRunID: 3001, CaseVersionID: 41, CaseTitle: "登录流程", Priority: "P1",
		Steps: []ExecutionStep{{Seq: 1, Action: "打开登录页", Expected: "出现登录表单"}},
	}
	const base = "请继续执行当前任务。"

	system := systemMessageText(agentcontext.ContextPacket{}, in)
	if !strings.Contains(system, "[profile:clarification_host] 版本 订单系统 Q4") {
		t.Fatalf("empty-prefix fallback lost the stable prompt:\n%s", system)
	}
	for _, leaked := range []string{
		"# 任务", "# 执行任务", "work_item_id=1001", "requirement_id=501",
		"不能替用户确认需求", "登录流程", "打开登录页",
	} {
		if strings.Contains(system, leaked) {
			t.Fatalf("empty-prefix fallback re-added per-work-item %q:\n%s", leaked, system)
		}
	}

	user := taskContextMessage(base, in)
	combined := system + "\n\n" + user
	if got := strings.Count(combined, "# 任务"); got != 1 {
		t.Fatalf("task block occurrences = %d, want exactly 1:\n%s", got, combined)
	}
	if got := strings.Count(combined, "# 执行任务"); got != 1 {
		t.Fatalf("execution block occurrences = %d, want exactly 1:\n%s", got, combined)
	}
	for _, want := range []string{
		"目标: 回答用户问题", "约束:", "不能替用户确认需求", "验收:",
		"作用域:", "work_item_id=1001", "requirement_id=501",
		"登录流程", "打开登录页", base,
	} {
		if !strings.Contains(user, want) {
			t.Fatalf("user turn misses task material %q:\n%s", want, user)
		}
	}

	// A non-empty prefix is used verbatim; the fallback never applies.
	packet := agentcontext.ContextPacket{Segments: []agentcontext.Segment{{
		Name: agentcontext.SegmentPrefix, Role: "system", Content: "[stable-prefix]", Prefix: true,
	}}}
	if got := systemMessageText(packet, in); got != "[stable-prefix]" {
		t.Fatalf("systemMessageText(packet) = %q, want the persisted prefix", got)
	}
}

// TestVersionPackRendersEnvironmentDatabases pins that the bound databases reach
// the model through BOTH surfaces of the version pack, and that the two agree.
//
// There are two surfaces and they had drifted apart:
//   - the prompt section rendered by versionPackContent, and
//   - the context.version_pack TOOL result built by versionPackResult, which emits
//     pack.Databases raw and therefore depends on DatabaseSummary's json tags.
//
// The tool surface had no tags at all, so it returned {"ID":"1501","SUTID":9001}
// in PascalCase while the design hints promised a `sut_id` field. RoleCaseAuthor
// holds ToolContextVersionPack, so the exact role the hints address could call the
// tool, find no sut_id, and fall back to the only decimal id present -- the
// database id -- which presence-only validation accepts. That is the same
// wrong-but-green path the header fix closed, surviving on the second surface.
// Asserting the tool's key set alongside the rendered section is what keeps them
// from diverging again.
func TestVersionPackRendersEnvironmentDatabases(t *testing.T) {
	pack := VersionPack{
		VersionID: 10, Title: "订单系统 Q4", Status: "running",
		Databases: []tools.DatabaseSummary{
			{ID: "1501", SUTID: 9001, Name: "order_db", Kind: "postgres", ReadOnly: true},
			{ID: "1502", SUTID: 9002, Name: "audit_db", Kind: "postgres", ReadOnly: false},
		},
	}
	content := versionPackContent(pack)

	// --- surface 1: the rendered prompt section ---
	// Both id spaces must be present and attributable per row.
	for _, want := range []string{
		"id=1501", "sut_id=9001", "order_db", "postgres", "read_only=true",
		"id=1502", "sut_id=9002", "audit_db", "read_only=false",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("version pack misses database material %q:\n%s", want, content)
		}
	}
	header := versionPackDatabaseHeader(t, content)
	// The header must name sut_id as the db-assertion value...
	if !strings.Contains(header, "sut_id 才是 design 的 db 断言") {
		t.Fatalf("database section header does not say which id a db assertion takes: %q", header)
	}
	// ...must NOT teach the retired database_id-for-assertions rule. The ban is
	// scoped to the HEADER, not the whole pack: other lines legitimately mention
	// sut_id and the no-database line mentions it too, so a pack-wide token ban
	// would trip the very fix it is meant to protect.
	if strings.Contains(header, "database_id") {
		t.Fatalf("database section header still teaches the retired database_id contract: %q", header)
	}
	// The database id must still be labelled with the execution tools that take
	// it. Both names are asserted because the section once named a tool
	// (db.schema_read) that is not registered -- the real one is
	// database.schema_read (case_execution_tools.go ToolDatabaseSchemaRead).
	if !strings.Contains(header, "db.query") || !strings.Contains(header, "database.schema_read") {
		t.Fatalf("header does not name both execution db tools (db.query, database.schema_read): %q", header)
	}
	if strings.Contains(header, "db.schema_read") {
		t.Fatalf("header names the unregistered tool db.schema_read: %q", header)
	}

	// --- surface 2: the context.version_pack tool result ---
	result := versionPackResult(pack)
	encoded, err := json.Marshal(result["databases"])
	if err != nil {
		t.Fatalf("marshal tool databases: %v", err)
	}
	rows := []map[string]any{}
	if err := json.Unmarshal(encoded, &rows); err != nil {
		t.Fatalf("tool databases did not round-trip as objects: %v (%s)", err, encoded)
	}
	if len(rows) != 2 {
		t.Fatalf("tool databases = %d, want 2 (%s)", len(rows), encoded)
	}
	for _, row := range rows {
		for _, key := range []string{"id", "sut_id", "name", "kind", "read_only"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("tool database row is missing the %q key (the hints promise it): %v", key, row)
			}
		}
		// The untagged PascalCase keys are the actual defect: if they come back,
		// the hints and the tool disagree again.
		for _, gone := range []string{"ID", "SUTID", "Name", "Kind", "ReadOnly"} {
			if _, ok := row[gone]; ok {
				t.Fatalf("tool database row exposes the untagged key %q; DatabaseSummary lost its json tags: %v", gone, row)
			}
		}
	}
	// The VALUE TYPE is part of the contract, not just the key name. The type
	// expectation is DERIVED from the consumer's own schema (case.graph_propose
	// assertions[].sut_id, the single idStringSchema authority) rather than a
	// hand-copied "want a STRING" literal (T5 minor④): a bare number here makes a
	// model that copies the pack verbatim hit an unmarshal type error and burn a
	// turn. Asserting against the consumer schema is what keeps `,string` on the
	// tag from being dropped as a no-op, and ties this surface to the same
	// authority the tools-package pin uses.
	if rows[0]["id"] != "1501" {
		t.Fatalf("tool database row id = %#v, want the string \"1501\"", rows[0]["id"])
	}
	requirePackSUTIDMatchesConsumerSchema(t, rows[0]["sut_id"])
	if rows[0]["sut_id"] != "9001" {
		t.Fatalf("tool database row sut_id = %#v, want the fixture SUT id \"9001\"", rows[0]["sut_id"])
	}

	// The two surfaces must not disagree about which id is which, nor about its
	// shape: the prompt renders sut_id=9001 and the tool emits "9001".
	if !strings.Contains(content, "sut_id=9001") || rows[0]["sut_id"] != "9001" {
		t.Fatalf("prompt section and tool result disagree about the sut_id of database 1501")
	}
}

// versionPackDatabaseHeader extracts the rendered 数据库(...) header line.
func versionPackDatabaseHeader(t *testing.T, content string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "数据库(") {
			return line
		}
	}
	t.Fatalf("no 数据库(...) header in the rendered pack:\n%s", content)
	return ""
}

// requirePackSUTIDMatchesConsumerSchema asserts a task_pack sut_id value against
// the CONSUMER's published schema — case.graph_propose assertions[].sut_id,
// which is built from the single idStringSchema() authority (T5 minor④). The
// expectation (type=string + decimal pattern) is read from the consumer tool's
// Parameters(), not hand-copied here, so this cross-package surface guard shares
// the same authority as the tools-package pin instead of embedding a second
// "want a STRING" literal. Navigation fails loud if the schema shape moves.
func requirePackSUTIDMatchesConsumerSchema(t *testing.T, value any) {
	t.Helper()
	tool, ok := tools.NewDesignRegistry(tools.Deps{}).Get(tools.ToolCaseGraphPropose)
	if !ok {
		t.Fatal("case.graph_propose tool not found in the P2 registry; the sut_id consumer authority is unavailable")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("parse case.graph_propose parameters: %v", err)
	}
	node := schema
	for _, segment := range []string{"properties", "cases", "items", "properties", "assertions", "items", "properties", "sut_id"} {
		next, ok := node[segment].(map[string]any)
		if !ok {
			t.Fatalf("case.graph_propose schema navigation lost segment %q; the sut_id consumer authority moved (fail loud, not silent skip)", segment)
		}
		node = next
	}
	if node["type"] != "string" {
		t.Fatalf("consumer sut_id schema type = %v, want string (idStringSchema authority)", node["type"])
	}
	s, ok := value.(string)
	if !ok {
		t.Fatalf("pack sut_id = %#v (%T), want a decimal STRING per the case.graph_propose consumer schema -- a number breaks the proposal parse", value, value)
	}
	pattern, _ := node["pattern"].(string)
	if pattern == "" {
		t.Fatal("consumer sut_id schema has no pattern; the authority is incomplete")
	}
	if !regexp.MustCompile(pattern).MatchString(s) {
		t.Fatalf("pack sut_id = %q does not match the consumer schema pattern %q", s, pattern)
	}
}

// TestVersionPackOmitsZeroSUTID pins M-5: the prompt section mirrors the guard
// sut.database_list uses, so a row with no resolved SUT does not render
// "sut_id=0". Rendering a zero would hand the model a value that fails the design
// gate's positive-id presence check, and would make the two surfaces of the same
// inventory disagree (the tool omits the key entirely in that case).
func TestVersionPackOmitsZeroSUTID(t *testing.T) {
	pack := VersionPack{
		VersionID: 10, Title: "订单系统 Q4", Status: "running",
		Databases: []tools.DatabaseSummary{
			{ID: "1501", SUTID: 0, Name: "orphan_db", Kind: "postgres", ReadOnly: true},
		},
	}
	content := versionPackContent(pack)
	if strings.Contains(content, "sut_id=0") {
		t.Fatalf("an unresolved SUT must not render sut_id=0:\n%s", content)
	}
	if !strings.Contains(content, "id=1501") || !strings.Contains(content, "orphan_db") {
		t.Fatalf("the row itself must still render:\n%s", content)
	}
	// The tool surface omits the key rather than emitting a zero, matching
	// sut.database_list's `if item.SUTID > 0` guard. Both encodings are checked
	// because the tag carries `,string`: a regression that drops omitempty would
	// emit "0" (a string) rather than 0, and a regression that drops `,string`
	// would emit 0. Either way the model gets a value the design gate rejects.
	result := versionPackResult(pack)
	encoded, err := json.Marshal(result["databases"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, bad := range []string{`"sut_id":0`, `"sut_id":"0"`} {
		if strings.Contains(string(encoded), bad) {
			t.Fatalf("the tool result must not emit a zero sut_id (%s): %s", bad, encoded)
		}
	}
	// The rest of the row must survive: omission is scoped to sut_id alone.
	if !strings.Contains(string(encoded), `"id":"1501"`) || !strings.Contains(string(encoded), `"name":"orphan_db"`) {
		t.Fatalf("the tool row lost more than sut_id: %s", encoded)
	}
}

// TestVersionPackWithoutDatabasesSaysSoExplicitly pins the ruling that the
// absence of an inventory must be stated, not merely implied. Before this, the
// section's absence was the model's only signal that no database is bound, so it
// could still emit a kind=db assertion and burn a turn on the rejection. The line
// is gated on SUTs being present: a pack with no bound environment at all should
// not grow a spurious database line.
func TestVersionPackWithoutDatabasesSaysSoExplicitly(t *testing.T) {
	content := versionPackContent(VersionPack{
		VersionID: 10, Title: "订单系统 Q4", Status: "running",
		SUTs: []string{"商城前台 (预发)"},
	})
	if strings.Contains(content, "数据库(") {
		t.Fatalf("an empty database inventory must not render the inventory header:\n%s", content)
	}
	if !strings.Contains(content, "本版本未绑定数据库") {
		t.Fatalf("the pack must state explicitly that no database is bound:\n%s", content)
	}
	if !strings.Contains(content, "不要写 kind=db 的断言") {
		t.Fatalf("the no-database line must tell the model not to write db assertions:\n%s", content)
	}
	if !strings.Contains(content, "SUT: 商城前台 (预发)") {
		t.Fatalf("SUT list missing from the pack:\n%s", content)
	}

	// With no SUTs either, the pack says nothing about databases at all.
	empty := versionPackContent(VersionPack{VersionID: 10, Title: "空", Status: "running"})
	if strings.Contains(empty, "数据库") {
		t.Fatalf("a pack with no bound environment must not mention databases:\n%s", empty)
	}
}

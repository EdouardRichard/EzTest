package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

/* --------------------------------- fakes ---------------------------------- */

type fakeSkillReader struct {
	skills map[string]SkillContent
	err    error
	calls  []string
}

func (r *fakeSkillReader) ReadSkill(_ context.Context, name string) (SkillContent, bool, error) {
	r.calls = append(r.calls, name)
	if r.err != nil {
		return SkillContent{}, false, r.err
	}
	content, ok := r.skills[name]
	return content, ok, nil
}

type fakeSkillUsage struct {
	rows  []usageCall
	err   error
	calls int
}

type usageCall struct {
	runID int64
	name  string
	found bool
}

func (u *fakeSkillUsage) RecordUsage(_ context.Context, runID int64, name string, found bool) error {
	u.calls++
	if u.err != nil {
		return u.err
	}
	u.rows = append(u.rows, usageCall{runID: runID, name: name, found: found})
	return nil
}

func skillTestDeps() (Deps, *fakeSkillReader, *fakeSkillUsage) {
	reader := &fakeSkillReader{skills: map[string]SkillContent{
		"playwright-cli": {
			Name: "playwright-cli", Description: "CLI usage",
			Body: "# playwright-cli\n\nUse browser.cli ...", Version: 3,
		},
	}}
	usage := &fakeSkillUsage{}
	return Deps{Skill: SkillDeps{Reader: reader, Usage: usage}}, reader, usage
}

/* --------------------------------- tool ----------------------------------- */

// TestSkillReadReturnsBodyOnDemand pins the on-demand body contract: the tool
// returns the full SKILL.md body plus metadata, and records a found=true fact.
func TestSkillReadReturnsBodyOnDemand(t *testing.T) {
	deps, _, usage := skillTestDeps()
	tool := &skillReadTool{deps: deps}
	result, err := tool.Execute(context.Background(), Scope{AgentRunID: 42},
		json.RawMessage(`{"name":"playwright-cli"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Surface ③: the result struct's json tags are the model-facing contract.
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal result: %v", marshalErr)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	for _, key := range []string{"name", "description", "body", "version"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("skill.read result missing json key %q: %s", key, raw)
		}
	}
	if payload["body"] != "# playwright-cli\n\nUse browser.cli ..." {
		t.Fatalf("body = %v", payload["body"])
	}
	if payload["name"] != "playwright-cli" || payload["version"] != float64(3) {
		t.Fatalf("metadata = %v", payload)
	}
	if len(usage.rows) != 1 || usage.rows[0] != (usageCall{runID: 42, name: "playwright-cli", found: true}) {
		t.Fatalf("usage rows = %#v, want one found=true row for run 42", usage.rows)
	}
}

// TestSkillReadMissRecordsNotFound: an unknown package is a not_found business
// error AND a found=false ledger fact (the miss is auditable, not silent).
func TestSkillReadMissRecordsNotFound(t *testing.T) {
	deps, _, usage := skillTestDeps()
	tool := &skillReadTool{deps: deps}
	_, err := tool.Execute(context.Background(), Scope{AgentRunID: 42},
		json.RawMessage(`{"name":"no-such-skill"}`))
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Code != CodeNotFound {
		t.Fatalf("miss error = %v, want not_found", err)
	}
	if len(usage.rows) != 1 || usage.rows[0] != (usageCall{runID: 42, name: "no-such-skill", found: false}) {
		t.Fatalf("usage rows = %#v, want one found=false row", usage.rows)
	}
}

func TestSkillReadRejectsEmptyName(t *testing.T) {
	deps, reader, usage := skillTestDeps()
	tool := &skillReadTool{deps: deps}
	_, err := tool.Execute(context.Background(), Scope{AgentRunID: 42}, json.RawMessage(`{"name":"  "}`))
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument {
		t.Fatalf("empty name error = %v, want invalid_argument", err)
	}
	if len(reader.calls) != 0 || usage.calls != 0 {
		t.Fatal("empty name reached the reader or the ledger")
	}
}

func TestSkillReadRejectsUnknownFields(t *testing.T) {
	deps, _, _ := skillTestDeps()
	tool := &skillReadTool{deps: deps}
	_, err := tool.Execute(context.Background(), Scope{AgentRunID: 42},
		json.RawMessage(`{"name":"playwright-cli","run_id":"9"}`))
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument {
		t.Fatalf("unknown field error = %v, want invalid_argument", err)
	}
}

// TestSkillReadLedgerFailureIsNotSilent: a failed fact write must not return
// the body as if nothing happened (constitution art.7.6); it is a retryable
// error so the same read can be retried.
func TestSkillReadLedgerFailureIsNotSilent(t *testing.T) {
	deps, _, usage := skillTestDeps()
	usage.err = errors.New("ledger down")
	tool := &skillReadTool{deps: deps}
	result, err := tool.Execute(context.Background(), Scope{AgentRunID: 42},
		json.RawMessage(`{"name":"playwright-cli"}`))
	if err == nil || result != nil {
		t.Fatalf("ledger failure returned a result: %#v, %v", result, err)
	}
	var toolErr *Error
	if !errors.As(err, &toolErr) || !toolErr.Retryable {
		t.Fatalf("ledger failure = %v, want retryable tool error", err)
	}
}

func TestSkillReadInternalReaderErrorStaysInternal(t *testing.T) {
	deps, reader, usage := skillTestDeps()
	reader.err = errors.New("db down")
	tool := &skillReadTool{deps: deps}
	_, err := tool.Execute(context.Background(), Scope{AgentRunID: 42},
		json.RawMessage(`{"name":"playwright-cli"}`))
	if err == nil {
		t.Fatal("internal reader error swallowed")
	}
	var toolErr *Error
	if errors.As(err, &toolErr) && toolErr.Code == CodeNotFound {
		t.Fatal("internal error masked as not_found (would record a false miss)")
	}
	if usage.calls != 0 {
		t.Fatal("usage recorded for an unknown outcome")
	}
}

/* ----------------------------- registration ------------------------------- */

// TestSkillToolRegistrationGating: skill.read registers only when BOTH the
// reader and the usage ledger are wired (never half-wired: a read without
// accounting would silently lose facts).
func TestSkillToolRegistrationGating(t *testing.T) {
	full, _, _ := skillTestDeps()
	if _, ok := NewKnowledgeRegistry(full).Get(ToolSkillRead); !ok {
		t.Fatal("skill.read not registered with full skill deps")
	}
	readerOnly := Deps{Skill: SkillDeps{Reader: full.Skill.Reader}}
	if _, ok := NewKnowledgeRegistry(readerOnly).Get(ToolSkillRead); ok {
		t.Fatal("skill.read registered without the usage ledger")
	}
	usageOnly := Deps{Skill: SkillDeps{Usage: full.Skill.Usage}}
	if _, ok := NewKnowledgeRegistry(usageOnly).Get(ToolSkillRead); ok {
		t.Fatal("skill.read registered without the reader")
	}
	if _, ok := NewKnowledgeRegistry(Deps{}).Get(ToolSkillRead); ok {
		t.Fatal("skill.read registered with no skill deps")
	}
}

// TestSkillDomainInAdminCatalog: the skill capability domain is visible to the
// admin profile editor (metadata-only catalog, Execute fails closed).
func TestSkillDomainInAdminCatalog(t *testing.T) {
	catalog := AdminCatalogRegistry()
	tool, ok := catalog.Get(ToolSkillRead)
	if !ok {
		t.Fatal("skill.read missing from AdminCatalogRegistry")
	}
	if ToolDomain(tool.Name()) != "skill" {
		t.Fatalf("skill.read domain = %q, want skill", ToolDomain(tool.Name()))
	}
	if !skillContains(catalog.NamesInDomain("skill"), ToolSkillRead) {
		t.Fatalf("skill domain members = %v", catalog.NamesInDomain("skill"))
	}
	if _, err := tool.Execute(context.Background(), Scope{}, json.RawMessage(`{"name":"playwright-cli"}`)); err == nil {
		t.Fatal("catalog tool with nil deps executed successfully")
	}
}

func skillContains(list []string, want string) bool {
	for _, entry := range list {
		if entry == want {
			return true
		}
	}
	return false
}

// TestSkillReadSchema pins the model-facing schema surface: exactly one
// required string parameter, no scope fields.
func TestSkillReadSchema(t *testing.T) {
	tool := &skillReadTool{deps: Deps{}}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("schema unmarshal: %v", err)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "name" {
		t.Fatalf("required = %v, want [name]", required)
	}
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) != 1 {
		t.Fatalf("properties = %v, want only name", properties)
	}
	if !strings.Contains(tool.Description(), "skill.read") && !strings.Contains(tool.Description(), "skill") {
		t.Fatal("description does not describe the skill domain")
	}
}

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// P3 execution tool names (contracts/agent-profiles-tools.md P3 increment).
// The 13 browser.* atomic actions were retired in v0.2.4-C T13: browser
// operation moves to the official playwright CLI over the general-execution
// domain (browser.cli, T15), never a fixed action enumeration.
const (
	ToolHTTPRequest              = "http.request"
	ToolDBQuery                  = "db.query"
	ToolDatabaseSchemaRead       = "database.schema_read"
	ToolAccountCredentials       = "account.credentials"
	ToolExecutionStepRecord      = "execution.step_record"
	ToolExecutionAssertionRecord = "execution.assertion_record"
	ToolExecutionEvidence        = "execution.evidence"
	ToolExecutionWait            = "execution.wait"
	ToolExecutionCaseFinish      = "execution.case_finish"
	ToolHumanRequest             = "human.request"
	ToolExecutionResultRead      = "execution.result_read"
	ToolExecutionEvidenceRead    = "execution.evidence_read"
	ToolExecutionCoverageRead    = "execution.coverage_read"
	ToolAnalysisFailureSubmit    = "analysis.failure_submit"
	ToolAnalysisCoverageSubmit   = "analysis.coverage_submit"
)

// caseExecutionToolNames is the frozen P3 registration order.
var caseExecutionToolNames = []string{
	ToolHTTPRequest,
	ToolDBQuery,
	ToolDatabaseSchemaRead,
	ToolAccountCredentials,
	ToolExecutionStepRecord,
	ToolExecutionAssertionRecord,
	ToolExecutionEvidence,
	ToolExecutionWait,
	ToolExecutionCaseFinish,
	ToolHumanRequest,
	ToolExecutionResultRead,
	ToolExecutionEvidenceRead,
	ToolExecutionCoverageRead,
	ToolAnalysisFailureSubmit,
	ToolAnalysisCoverageSubmit,
}

// CaseExecutionToolNames returns a copy of the frozen P3 tool names.
func CaseExecutionToolNames() []string {
	return append([]string(nil), caseExecutionToolNames...)
}

// ExecutionPort is the execution context surface used by the P3 tools. Every
// method receives the runtime-injected Scope (never model-provided) and validates
// the case_run dispatch fence internally; adapters return *Error (business) or
// *platform.Error and let unexpected failures become internal run failures.
//
// The retired browser.* tool binding is gone (v0.2.4-C T13). The execution-side
// CaseRunner.Browser bridge method still exists server-internal for the
// transitional terminal-evidence/discovery paths until T15/T16 replace them; it
// is deliberately NOT part of this model-facing port any more.
type ExecutionPort interface {
	StepRecord(ctx context.Context, scope Scope, in StepRecordInput) (any, error)
	AssertionRecord(ctx context.Context, scope Scope, in AssertionRecordInput) (any, error)
	EvidenceText(ctx context.Context, scope Scope, in EvidenceTextInput) (any, error)
	CaseFinish(ctx context.Context, scope Scope, in CaseFinishInput) (any, error)
	Wait(ctx context.Context, scope Scope, in WaitInput) (any, error)
	HumanRequest(ctx context.Context, scope Scope, in HumanRequestInput) (any, error)
	HTTP(ctx context.Context, scope Scope, args json.RawMessage) (any, error)
	DBQuery(ctx context.Context, scope Scope, args json.RawMessage) (any, error)
	SchemaRead(ctx context.Context, scope Scope, args json.RawMessage) (any, error)
	AccountCredentials(ctx context.Context, scope Scope, in AccountCredentialsInput) (any, error)
	ResultRead(ctx context.Context, scope Scope, in ResultReadInput) (any, error)
	EvidenceRead(ctx context.Context, scope Scope, in EvidenceReadInput) (any, error)
	CoverageRead(ctx context.Context, scope Scope) (any, error)
	FailureSubmit(ctx context.Context, scope Scope, in FailureSubmitInput) (any, error)
	CoverageSubmit(ctx context.Context, scope Scope, in CoverageSubmitInput) (any, error)
}

// StepRecordInput is one execution.step_record call. Observed is a short
// human-readable summary; the server stores it as step_result.observed.summary.
type StepRecordInput struct {
	Seq          int
	Status       string
	Observed     string
	Error        string
	VersionSUTID *int64
}

// AssertionRecordInput is one execution.assertion_record call. Expected is a
// model-supplied hint that the server always overwrites with the P2 assertion.
type AssertionRecordInput struct {
	StepSeq      int
	AssertionSeq int
	Verdict      string
	Expected     string
	Actual       string
	Reason       string
	VersionSUTID *int64
}

// EvidenceTextInput is one execution.evidence text upload.
type EvidenceTextInput struct {
	Kind         string
	Text         string
	StepSeq      *int
	AssertionSeq *int
}

// CaseFinishInput is the model's candidate verdict; the server aggregates the
// deterministic four-state result and may override it.
type CaseFinishInput struct {
	Verdict      string
	Summary      string
	FailureClass string
}

// WaitInput is one execution.wait call (API-side interruptible wait).
type WaitInput struct {
	Seconds int
	Reason  string
}

// HumanRequestInput is one human.request call (login takeover).
type HumanRequestInput struct {
	Kind          string
	Reason        string
	VersionSUTID  *int64
	AccountRole   string
	ChallengeKind string
}

// AccountCredentialsInput resolves one test account by (SUT, role).
type AccountCredentialsInput struct {
	VersionSUTID *int64
	Role         string
}

// ResultReadInput reads wave-scoped case_run results for analysis.
type ResultReadInput struct {
	CaseRunID *int64
}

// EvidenceReadInput reads one evidence text fragment.
type EvidenceReadInput struct {
	EvidenceID int64
	Offset     int
	MaxChars   int
}

// FailureSubmitInput is one analysis.failure_submit candidate refinement.
type FailureSubmitInput struct {
	CaseRunID    int64
	FailureClass string
	Category     string
	Severity     string
	Summary      string
	DedupeHint   string
}

// CoverageSubmitInput is one analysis.coverage_submit gap candidate.
type CoverageSubmitInput struct {
	RequirementID      int64
	Reason             string
	EvidenceCaseRunIDs []int64
}

// NewExecutionRegistry registers the P1/P2 tools plus the P3 execution tools. Profiles
// filter the registry by allowed_tools, so P1/P2 profiles keep their old tool
// surface while case_executor/analysts see the P3 additions.
func NewExecutionRegistry(deps Deps) *Registry {
	base := NewDesignRegistry(deps)
	extra := executionTools(deps)
	all := make([]Tool, 0, len(base.byName)+len(extra))
	all = append(all, extra...)
	for _, name := range base.Names() {
		tool, _ := base.Get(name)
		all = append(all, tool)
	}
	return NewRegistry(all...)
}

// --- http / db tools -------------------------------------------------------

type httpArgs struct {
	VersionSUTID string            `json:"sut_id,omitempty"`
	APIID        string            `json:"api_id,omitempty"`
	Method       string            `json:"method,omitempty"`
	Path         string            `json:"path"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         string            `json:"body,omitempty"`
	TimeoutMS    *int              `json:"timeout_ms,omitempty"`
}

type httpRequestTool struct{ deps Deps }

func (t *httpRequestTool) Name() string { return ToolHTTPRequest }
func (t *httpRequestTool) Description() string {
	return "Send an HTTP request to ANY host: an absolute http(s) URL is used verbatim, a relative path prepends the SUT base URL. The full response body spills to the content ledger with an explicit truncated flag and content_ref (read it back with tool_result.read); large headers are size-bounded explicitly. http evidence is stored automatically; headers and bodies are recorded plaintext (credentials may appear by design)."
}
func (t *httpRequestTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id":  map[string]any{"type": "string"},
		"api_id":  map[string]any{"type": "string"},
		"method":  map[string]any{"type": "string", "enum": []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}},
		"path":    map[string]any{"type": "string"},
		"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"body":    map[string]any{"type": "string"},
		"timeout_ms": map[string]any{
			"type": "integer", "minimum": 1, "maximum": 900000,
			"description": "whole-request deadline in ms (default 15000 = 15s, API-side)",
		},
	}, "path")
}

func (t *httpRequestTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args httpArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if err := validateText("path", args.Path, 1, 2000); err != nil {
		return nil, err
	}
	if args.VersionSUTID != "" {
		if _, err := parseIDString("sut_id", args.VersionSUTID); err != nil {
			return nil, err
		}
	}
	if args.APIID != "" {
		if _, err := parseIDString("api_id", args.APIID); err != nil {
			return nil, err
		}
	}
	if args.Method != "" {
		args.Method = strings.ToUpper(args.Method)
		switch args.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			return nil, Invalid("method", "unsupported HTTP method")
		}
	}
	if len(args.Headers) > 50 {
		return nil, Invalid("headers", "at most 50 headers are allowed")
	}
	// P8 (v0.2.4-C T12): the request-body 64KB cap is lifted; the body is sent
	// verbatim (the evidence envelope keeps a bounded preview, the full response
	// spills). timeout_ms is configurable per call (default 15s API-side).
	if args.TimeoutMS != nil && (*args.TimeoutMS < 1 || *args.TimeoutMS > 900000) {
		return nil, Invalid("timeout_ms", "must be between 1 and 900000")
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode http arguments failed: %w", err)
	}
	return portResult(t.deps.Execution.HTTP(ctx, scope, normalized))
}

type dbQueryArgs struct {
	VersionSUTID       string `json:"sut_id,omitempty"`
	DatabaseID         string `json:"database_id,omitempty"`
	SQL                string `json:"sql"`
	MaxRows            *int   `json:"max_rows,omitempty"`
	StatementTimeoutMS *int   `json:"statement_timeout_ms,omitempty"`
}

type dbQueryTool struct{ deps Deps }

func (t *dbQueryTool) Name() string { return ToolDBQuery }
func (t *dbQueryTool) Description() string {
	return "Run any SQL statement (read OR write: SELECT/INSERT/UPDATE/DELETE/DDL) on a PostgreSQL database bound to the SUT. Rows are unbounded by default; pass max_rows to bound them. The statement timeout defaults to 5s and is configurable via statement_timeout_ms. db evidence is stored automatically; a large result spills to the content ledger with an explicit truncated flag and content_ref."
}
func (t *dbQueryTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id":      map[string]any{"type": "string"},
		"database_id": map[string]any{"type": "string"},
		"sql":         map[string]any{"type": "string"},
		"max_rows":    map[string]any{"type": "integer", "minimum": 1, "description": "optional row cap; omitted means unlimited"},
		"statement_timeout_ms": map[string]any{
			"type": "integer", "minimum": 1, "maximum": 900000,
			"description": "statement timeout in ms (default 5000 = 5s)",
		},
	}, "sql")
}

func (t *dbQueryTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args dbQueryArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if err := validateText("sql", args.SQL, 1, 20000); err != nil {
		return nil, err
	}
	if args.VersionSUTID != "" {
		if _, err := parseIDString("sut_id", args.VersionSUTID); err != nil {
			return nil, err
		}
	}
	if args.DatabaseID != "" {
		if _, err := parseIDString("database_id", args.DatabaseID); err != nil {
			return nil, err
		}
	}
	// P8 (v0.2.4-C T12): the 200-row cap is lifted — max_rows is any positive
	// integer (omitted = unlimited). statement_timeout_ms is configurable per call.
	if args.MaxRows != nil && *args.MaxRows < 1 {
		return nil, Invalid("max_rows", "must be a positive integer (omit for unlimited)")
	}
	if args.StatementTimeoutMS != nil && (*args.StatementTimeoutMS < 1 || *args.StatementTimeoutMS > 900000) {
		return nil, Invalid("statement_timeout_ms", "must be between 1 and 900000")
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode db arguments failed: %w", err)
	}
	return portResult(t.deps.Execution.DBQuery(ctx, scope, normalized))
}

type schemaReadArgs struct {
	VersionSUTID string `json:"sut_id,omitempty"`
	DatabaseID   string `json:"database_id,omitempty"`
	Schema       string `json:"schema,omitempty"`
}

type schemaReadTool struct{ deps Deps }

func (t *schemaReadTool) Name() string { return ToolDatabaseSchemaRead }
func (t *schemaReadTool) Description() string {
	return "Read a tables/columns summary of the target database (bounded to 200 tables and 200 columns per schema)."
}
func (t *schemaReadTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id":      map[string]any{"type": "string"},
		"database_id": map[string]any{"type": "string"},
		"schema":      map[string]any{"type": "string"},
	})
}

func (t *schemaReadTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args schemaReadArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if args.VersionSUTID != "" {
		if _, err := parseIDString("sut_id", args.VersionSUTID); err != nil {
			return nil, err
		}
	}
	if args.DatabaseID != "" {
		if _, err := parseIDString("database_id", args.DatabaseID); err != nil {
			return nil, err
		}
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode schema arguments failed: %w", err)
	}
	return portResult(t.deps.Execution.SchemaRead(ctx, scope, normalized))
}

// --- execution lifecycle tools --------------------------------------------

type stepRecordArgs struct {
	Seq          int    `json:"seq"`
	Status       string `json:"status"`
	Observed     string `json:"observed"`
	Error        string `json:"error"`
	VersionSUTID string `json:"sut_id,omitempty"`
}

type stepRecordTool struct{ deps Deps }

func (t *stepRecordTool) Name() string { return ToolExecutionStepRecord }
func (t *stepRecordTool) Description() string {
	return "Record one executed step (upserted to the current case_run; stale dispatch epoch/token writes are rejected)."
}
func (t *stepRecordTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"seq":      map[string]any{"type": "integer", "minimum": 1},
		"status":   map[string]any{"type": "string", "enum": []string{"pending", "running", "passed", "failed", "blocked", "skipped"}},
		"observed": map[string]any{"type": "string"},
		"error":    map[string]any{"type": "string"},
		"sut_id":   map[string]any{"type": "string"},
	}, "seq", "status")
}

func (t *stepRecordTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args stepRecordArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if args.Seq < 1 {
		return nil, Invalid("seq", "must be a positive step sequence")
	}
	switch args.Status {
	case "pending", "running", "passed", "failed", "blocked", "skipped":
	default:
		return nil, Invalid("status", "unsupported step status")
	}
	if err := validateText("observed", args.Observed, 0, 4000); err != nil {
		return nil, err
	}
	if err := validateText("error", args.Error, 0, 2000); err != nil {
		return nil, err
	}
	in := StepRecordInput{Seq: args.Seq, Status: args.Status, Observed: args.Observed, Error: args.Error}
	if args.VersionSUTID != "" {
		id, err := parseIDString("sut_id", args.VersionSUTID)
		if err != nil {
			return nil, err
		}
		in.VersionSUTID = &id
	}
	return portResult(t.deps.Execution.StepRecord(ctx, scope, in))
}

type assertionRecordArgs struct {
	StepSeq      int    `json:"step_seq"`
	AssertionSeq int    `json:"assertion_seq"`
	Verdict      string `json:"verdict"`
	Expected     string `json:"expected"`
	Actual       string `json:"actual"`
	Reason       string `json:"reason"`
	VersionSUTID string `json:"sut_id,omitempty"`
}

type assertionRecordTool struct{ deps Deps }

func (t *assertionRecordTool) Name() string { return ToolExecutionAssertionRecord }
func (t *assertionRecordTool) Description() string {
	return "Record one assertion result; the server overwrites expected with the P2 assertion truth."
}
func (t *assertionRecordTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"step_seq":      map[string]any{"type": "integer", "minimum": 1},
		"assertion_seq": map[string]any{"type": "integer", "minimum": 1},
		"verdict":       map[string]any{"type": "string", "enum": []string{"passed", "failed", "inconclusive", "skipped"}},
		"expected":      map[string]any{"type": "string"},
		"actual":        map[string]any{"type": "string"},
		"reason":        map[string]any{"type": "string"},
		"sut_id":        map[string]any{"type": "string"},
	}, "step_seq", "assertion_seq", "verdict")
}

func (t *assertionRecordTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args assertionRecordArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if args.StepSeq < 1 {
		return nil, Invalid("step_seq", "must be a positive step sequence")
	}
	if args.AssertionSeq < 1 {
		return nil, Invalid("assertion_seq", "must be a positive assertion sequence")
	}
	switch args.Verdict {
	case "passed", "failed", "inconclusive", "skipped":
	default:
		return nil, Invalid("verdict", "unsupported assertion verdict")
	}
	if err := validateText("expected", args.Expected, 0, 4000); err != nil {
		return nil, err
	}
	if err := validateText("actual", args.Actual, 0, 4000); err != nil {
		return nil, err
	}
	if err := validateText("reason", args.Reason, 0, 2000); err != nil {
		return nil, err
	}
	in := AssertionRecordInput{
		StepSeq: args.StepSeq, AssertionSeq: args.AssertionSeq, Verdict: args.Verdict,
		Expected: args.Expected, Actual: args.Actual, Reason: args.Reason,
	}
	if args.VersionSUTID != "" {
		id, err := parseIDString("sut_id", args.VersionSUTID)
		if err != nil {
			return nil, err
		}
		in.VersionSUTID = &id
	}
	return portResult(t.deps.Execution.AssertionRecord(ctx, scope, in))
}

type evidenceArgs struct {
	Kind         string `json:"kind"`
	Text         string `json:"text"`
	StepSeq      *int   `json:"step_seq,omitempty"`
	AssertionSeq *int   `json:"assertion_seq,omitempty"`
}

type evidenceTool struct{ deps Deps }

func (t *evidenceTool) Name() string { return ToolExecutionEvidence }
func (t *evidenceTool) Description() string {
	return "Upload text evidence (binary evidence is stored automatically by screenshot/download tools)."
}
func (t *evidenceTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"kind":          map[string]any{"type": "string", "enum": []string{"text"}},
		"text":          map[string]any{"type": "string"},
		"step_seq":      map[string]any{"type": "integer", "minimum": 1},
		"assertion_seq": map[string]any{"type": "integer", "minimum": 1},
	}, "kind", "text")
}

func (t *evidenceTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args evidenceArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if args.Kind != "text" {
		return nil, Invalid("kind", "only text evidence is accepted by this tool")
	}
	if err := validateText("text", args.Text, 1, 5<<20); err != nil {
		return nil, err
	}
	if args.StepSeq != nil && *args.StepSeq < 1 {
		return nil, Invalid("step_seq", "must be a positive step sequence")
	}
	if args.AssertionSeq != nil && *args.AssertionSeq < 1 {
		return nil, Invalid("assertion_seq", "must be a positive assertion sequence")
	}
	return portResult(t.deps.Execution.EvidenceText(ctx, scope, EvidenceTextInput{
		Kind: args.Kind, Text: args.Text, StepSeq: args.StepSeq, AssertionSeq: args.AssertionSeq,
	}))
}

type waitTool struct{ deps Deps }

func (t *waitTool) Name() string { return ToolExecutionWait }
func (t *waitTool) Description() string {
	return "Wait a fixed number of seconds (1-300) inside the API process; interrupted by pause/cancel/lease loss. Page timers keep running. Use this for time-window steps instead of repeated snapshots; always verify the outcome afterwards."
}
func (t *waitTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300},
		"reason":  map[string]any{"type": "string", "maxLength": 200},
	}, "seconds")
}

func (t *waitTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		Seconds int    `json:"seconds"`
		Reason  string `json:"reason"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if args.Seconds < 1 || args.Seconds > 300 {
		return nil, Invalid("seconds", "must be between 1 and 300")
	}
	if err := validateText("reason", args.Reason, 0, 200); err != nil {
		return nil, err
	}
	return portResult(t.deps.Execution.Wait(ctx, scope, WaitInput{Seconds: args.Seconds, Reason: args.Reason}))
}

type caseFinishTool struct{ deps Deps }

func (t *caseFinishTool) Name() string { return ToolExecutionCaseFinish }
func (t *caseFinishTool) Description() string {
	return "Submit a candidate case_run verdict; the server computes the deterministic four-state result and overrides inconsistencies. Submit blocked when execution cannot continue."
}
func (t *caseFinishTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"verdict":       map[string]any{"type": "string", "enum": []string{"passed", "failed", "blocked", "inconclusive"}},
		"summary":       map[string]any{"type": "string", "maxLength": 2000},
		"failure_class": map[string]any{"type": "string", "enum": []string{"application", "environment", "session", "model", "automation", "timeout", "executor_lost", "blocked", "unknown"}},
	}, "verdict")
}

func (t *caseFinishTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		Verdict      string `json:"verdict"`
		Summary      string `json:"summary"`
		FailureClass string `json:"failure_class"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	switch args.Verdict {
	case "passed", "failed", "blocked", "inconclusive":
	default:
		return nil, Invalid("verdict", "must be one of passed, failed, blocked, inconclusive")
	}
	if args.FailureClass != "" {
		switch args.FailureClass {
		case "application", "environment", "session", "model", "automation", "timeout", "executor_lost", "blocked", "unknown":
		default:
			return nil, Invalid("failure_class", "unsupported failure class")
		}
		if args.Verdict != "failed" && args.Verdict != "blocked" && args.Verdict != "inconclusive" {
			return nil, Invalid("failure_class", "a failure class is only valid with a failed, blocked or inconclusive verdict")
		}
	}
	if err := validateText("summary", args.Summary, 0, 2000); err != nil {
		return nil, err
	}
	return portResult(t.deps.Execution.CaseFinish(ctx, scope, CaseFinishInput{
		Verdict: args.Verdict, Summary: args.Summary, FailureClass: args.FailureClass,
	}))
}

type humanRequestTool struct{ deps Deps }

func (t *humanRequestTool) Name() string { return ToolHumanRequest }
func (t *humanRequestTool) Description() string {
	return "Request a human takeover (for example login 2FA/captcha). The case_run pauses until the request is resolved; the model cannot resolve it itself."
}
func (t *humanRequestTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"kind":           map[string]any{"type": "string", "enum": []string{"login_takeover", "conflict", "other"}},
		"reason":         map[string]any{"type": "string", "maxLength": 500},
		"sut_id":         map[string]any{"type": "string"},
		"account_role":   map[string]any{"type": "string"},
		"challenge_kind": map[string]any{"type": "string"},
	}, "kind", "reason")
}

func (t *humanRequestTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		Kind          string `json:"kind"`
		Reason        string `json:"reason"`
		VersionSUTID  string `json:"sut_id"`
		AccountRole   string `json:"account_role"`
		ChallengeKind string `json:"challenge_kind"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	switch args.Kind {
	case "login_takeover", "conflict", "other":
	default:
		return nil, Invalid("kind", "unsupported human request kind")
	}
	if err := validateText("reason", args.Reason, 1, 500); err != nil {
		return nil, err
	}
	if err := validateText("account_role", args.AccountRole, 0, 100); err != nil {
		return nil, err
	}
	if err := validateText("challenge_kind", args.ChallengeKind, 0, 100); err != nil {
		return nil, err
	}
	in := HumanRequestInput{Kind: args.Kind, Reason: args.Reason, AccountRole: args.AccountRole, ChallengeKind: args.ChallengeKind}
	if args.VersionSUTID != "" {
		id, err := parseIDString("sut_id", args.VersionSUTID)
		if err != nil {
			return nil, err
		}
		in.VersionSUTID = &id
	}
	return portResult(t.deps.Execution.HumanRequest(ctx, scope, in))
}

type accountCredentialsTool struct{ deps Deps }

func (t *accountCredentialsTool) Name() string { return ToolAccountCredentials }
func (t *accountCredentialsTool) Description() string {
	return "Read the test account credentials of a (SUT, role) binding for login fill. Credentials are plaintext by design (GD-01) and may be recorded in evidence; only the API output is masked."
}
func (t *accountCredentialsTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id": map[string]any{"type": "string"},
		"role":   map[string]any{"type": "string"},
	}, "role")
}

func (t *accountCredentialsTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		VersionSUTID string `json:"sut_id"`
		Role         string `json:"role"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if err := validateText("role", args.Role, 1, 100); err != nil {
		return nil, err
	}
	in := AccountCredentialsInput{Role: args.Role}
	if args.VersionSUTID != "" {
		id, err := parseIDString("sut_id", args.VersionSUTID)
		if err != nil {
			return nil, err
		}
		in.VersionSUTID = &id
	}
	return portResult(t.deps.Execution.AccountCredentials(ctx, scope, in))
}

// --- analysis tools --------------------------------------------------------

type resultReadTool struct{ deps Deps }

func (t *resultReadTool) Name() string { return ToolExecutionResultRead }
func (t *resultReadTool) Description() string {
	return "Read the wave-scoped case_run/assertion/evidence result summary (read-only)."
}
func (t *resultReadTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{"case_run_id": map[string]any{"type": "string"}})
}

func (t *resultReadTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		CaseRunID string `json:"case_run_id"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	in := ResultReadInput{}
	if args.CaseRunID != "" {
		id, err := parseIDString("case_run_id", args.CaseRunID)
		if err != nil {
			return nil, err
		}
		in.CaseRunID = &id
	}
	return portResult(t.deps.Execution.ResultRead(ctx, scope, in))
}

type evidenceReadTool struct{ deps Deps }

func (t *evidenceReadTool) Name() string { return ToolExecutionEvidenceRead }
func (t *evidenceReadTool) Description() string {
	return "Read evidence metadata/a text fragment (never returns executor paths or credentials)."
}
func (t *evidenceReadTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"evidence_id": map[string]any{"type": "string"},
		"offset":      map[string]any{"type": "integer", "minimum": 0},
		"max_chars":   map[string]any{"type": "integer", "minimum": 1, "maximum": 20000},
	}, "evidence_id")
}

func (t *evidenceReadTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		EvidenceID string `json:"evidence_id"`
		Offset     int    `json:"offset"`
		MaxChars   int    `json:"max_chars"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	id, err := parseIDString("evidence_id", args.EvidenceID)
	if err != nil {
		return nil, err
	}
	if args.Offset < 0 {
		return nil, Invalid("offset", "must not be negative")
	}
	if args.MaxChars < 0 || args.MaxChars > 20000 {
		return nil, Invalid("max_chars", "must be between 1 and 20000")
	}
	return portResult(t.deps.Execution.EvidenceRead(ctx, scope, EvidenceReadInput{
		EvidenceID: id, Offset: args.Offset, MaxChars: args.MaxChars,
	}))
}

type coverageReadTool struct{ deps Deps }

func (t *coverageReadTool) Name() string { return ToolExecutionCoverageRead }
func (t *coverageReadTool) Description() string {
	return "Read the version execution coverage summary (requirements/cases/recent results)."
}
func (t *coverageReadTool) Parameters() json.RawMessage {
	return schemaObject(nil)
}

func (t *coverageReadTool) Execute(ctx context.Context, scope Scope, _ json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	return portResult(t.deps.Execution.CoverageRead(ctx, scope))
}

type failureSubmitTool struct{ deps Deps }

func (t *failureSubmitTool) Name() string { return ToolAnalysisFailureSubmit }
func (t *failureSubmitTool) Description() string {
	return "Submit a failure analysis candidate (refine only; never upgrade a non-application class to application; wave scope is validated by the server). The server derives and returns the canonical category/severity from the case priority; the summary must describe evidence, not a different severity."
}
func (t *failureSubmitTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"case_run_id":   map[string]any{"type": "string"},
		"failure_class": map[string]any{"type": "string", "enum": []string{"application", "environment", "session", "model", "automation", "timeout", "executor_lost", "blocked", "unknown"}},
		"category":      map[string]any{"type": "string", "enum": []string{"application", "data", "ui", "api", "db"}},
		"severity":      map[string]any{"type": "string", "enum": []string{"blocker", "major", "minor", "trivial"}},
		"summary":       map[string]any{"type": "string", "maxLength": 2000},
		"dedupe_hint":   map[string]any{"type": "string", "maxLength": 128},
	}, "case_run_id", "failure_class", "category", "severity", "summary")
}

func (t *failureSubmitTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		CaseRunID    string `json:"case_run_id"`
		FailureClass string `json:"failure_class"`
		Category     string `json:"category"`
		Severity     string `json:"severity"`
		Summary      string `json:"summary"`
		DedupeHint   string `json:"dedupe_hint"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	caseRunID, err := parseIDString("case_run_id", args.CaseRunID)
	if err != nil {
		return nil, err
	}
	if err := validateText("summary", args.Summary, 1, 2000); err != nil {
		return nil, err
	}
	if err := validateText("dedupe_hint", args.DedupeHint, 0, 128); err != nil {
		return nil, err
	}
	return portResult(t.deps.Execution.FailureSubmit(ctx, scope, FailureSubmitInput{
		CaseRunID: caseRunID, FailureClass: args.FailureClass, Category: args.Category,
		Severity: args.Severity, Summary: args.Summary, DedupeHint: args.DedupeHint,
	}))
}

type coverageSubmitTool struct{ deps Deps }

func (t *coverageSubmitTool) Name() string { return ToolAnalysisCoverageSubmit }
func (t *coverageSubmitTool) Description() string {
	return "Submit an execution coverage gap candidate (ensures design.gap; the server validates requirement/wave ownership)."
}
func (t *coverageSubmitTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"requirement_id":        map[string]any{"type": "string"},
		"reason":                map[string]any{"type": "string", "enum": []string{"execution_not_run", "execution_failed_coverage"}},
		"evidence_case_run_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, "requirement_id", "reason")
}

func (t *coverageSubmitTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		RequirementID      string   `json:"requirement_id"`
		Reason             string   `json:"reason"`
		EvidenceCaseRunIDs []string `json:"evidence_case_run_ids"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	requirementID, err := parseIDString("requirement_id", args.RequirementID)
	if err != nil {
		return nil, err
	}
	switch args.Reason {
	case "execution_not_run", "execution_failed_coverage":
	default:
		return nil, Invalid("reason", "unsupported coverage gap reason")
	}
	ids := make([]int64, 0, len(args.EvidenceCaseRunIDs))
	for _, rawID := range args.EvidenceCaseRunIDs {
		id, err := parseIDString("evidence_case_run_ids", rawID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return portResult(t.deps.Execution.CoverageSubmit(ctx, scope, CoverageSubmitInput{
		RequirementID: requirementID, Reason: args.Reason, EvidenceCaseRunIDs: ids,
	}))
}

// executionTools returns the frozen P3 tool set in registration order.
func executionTools(deps Deps) []Tool {
	return []Tool{
		&httpRequestTool{deps: deps},
		&dbQueryTool{deps: deps},
		&schemaReadTool{deps: deps},
		&accountCredentialsTool{deps: deps},
		&stepRecordTool{deps: deps},
		&assertionRecordTool{deps: deps},
		&evidenceTool{deps: deps},
		&waitTool{deps: deps},
		&caseFinishTool{deps: deps},
		&humanRequestTool{deps: deps},
		&resultReadTool{deps: deps},
		&evidenceReadTool{deps: deps},
		&coverageReadTool{deps: deps},
		&failureSubmitTool{deps: deps},
		&coverageSubmitTool{deps: deps},
	}
}

// --- shared ID/validation helpers -----------------------------------------

// parseIDString parses a positive decimal ID from a model-supplied tool
// argument. The frozen contract keeps tool parameter IDs as decimal strings.
func parseIDString(field, value string) (int64, *Error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, Invalid(field, "is required")
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, Invalid(field, "must be a positive decimal ID")
	}
	return parsed, nil
}

// portResult maps an execution port failure onto a model-visible tool error and
// passes successful results through.
func portResult(value any, err error) (any, error) {
	if err != nil {
		return nil, mapPortError(err)
	}
	return value, nil
}

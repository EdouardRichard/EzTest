package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// P3 execution tool names (contracts/agent-profiles-tools.md P3 increment).
const (
	ToolBrowserGoto              = "browser.goto"
	ToolBrowserSnapshot          = "browser.snapshot"
	ToolBrowserClick             = "browser.click"
	ToolBrowserFill              = "browser.fill"
	ToolBrowserPress             = "browser.press"
	ToolBrowserWaitFor           = "browser.wait_for"
	ToolBrowserScreenshot        = "browser.screenshot"
	ToolBrowserConsole           = "browser.console"
	ToolBrowserNetwork           = "browser.network"
	ToolBrowserDownload          = "browser.download"
	ToolBrowserStorageState      = "browser.storage_state"
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

// p3ToolNames is the frozen P3 registration order.
var p3ToolNames = []string{
	ToolBrowserGoto,
	ToolBrowserSnapshot,
	ToolBrowserClick,
	ToolBrowserFill,
	ToolBrowserPress,
	ToolBrowserWaitFor,
	ToolBrowserScreenshot,
	ToolBrowserConsole,
	ToolBrowserNetwork,
	ToolBrowserDownload,
	ToolBrowserStorageState,
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

// P3ToolNames returns a copy of the frozen P3 tool names.
func P3ToolNames() []string {
	return append([]string(nil), p3ToolNames...)
}

// ExecutionPort is the execution context surface used by the P3 tools. Every
// method receives the runtime-injected Scope (never model-provided) and validates
// the case_run dispatch fence internally; adapters return *Error (business) or
// *platform.Error and let unexpected failures become internal run failures.
type ExecutionPort interface {
	StepRecord(ctx context.Context, scope Scope, in StepRecordInput) (any, error)
	AssertionRecord(ctx context.Context, scope Scope, in AssertionRecordInput) (any, error)
	EvidenceText(ctx context.Context, scope Scope, in EvidenceTextInput) (any, error)
	CaseFinish(ctx context.Context, scope Scope, in CaseFinishInput) (any, error)
	Wait(ctx context.Context, scope Scope, in WaitInput) (any, error)
	HumanRequest(ctx context.Context, scope Scope, in HumanRequestInput) (any, error)
	Browser(ctx context.Context, scope Scope, action string, args json.RawMessage) (any, error)
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

// AccountCredentialsInput resolves one test account by (version_sut, role).
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

// NewP3Registry registers the P1/P2 tools plus the P3 execution tools. Profiles
// filter the registry by allowed_tools, so P1/P2 profiles keep their old tool
// surface while case_executor/analysts see the P3 additions.
func NewP3Registry(deps Deps) *Registry {
	base := NewP2Registry(deps)
	extra := p3Tools(deps)
	all := make([]Tool, 0, len(base.byName)+len(extra))
	all = append(all, extra...)
	for _, name := range base.Names() {
		tool, _ := base.Get(name)
		all = append(all, tool)
	}
	return NewRegistry(all...)
}

// --- browser tools ---------------------------------------------------------

type browserTarget struct {
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
	Exact    *bool  `json:"exact,omitempty"`
	Text     string `json:"text,omitempty"`
	TestID   string `json:"test_id,omitempty"`
	Selector string `json:"selector,omitempty"`
}

func (t *browserTarget) validate() *Error {
	if t == nil {
		return Invalid("target", "target is required")
	}
	kinds := 0
	if t.Role != "" {
		kinds++
	}
	if t.Text != "" {
		kinds++
	}
	if t.TestID != "" {
		kinds++
	}
	if t.Selector != "" {
		kinds++
	}
	if kinds != 1 {
		return Invalid("target", "target must specify exactly one of role, text, test_id or selector")
	}
	for field, value := range map[string]string{
		"target.role": t.Role, "target.name": t.Name, "target.text": t.Text,
		"target.test_id": t.TestID, "target.selector": t.Selector,
	} {
		if err := validateText(field, value, 0, 500); err != nil {
			return err
		}
	}
	return nil
}

type browserArgs struct {
	VersionSUTID string         `json:"version_sut_id,omitempty"`
	AccountRole  string         `json:"account_role,omitempty"`
	URL          string         `json:"url,omitempty"`
	Target       *browserTarget `json:"target,omitempty"`
	Value        *string        `json:"value,omitempty"`
	Secret       bool           `json:"secret,omitempty"`
	Key          string         `json:"key,omitempty"`
	TimeoutMS    *int           `json:"timeout_ms,omitempty"`
	FullPage     bool           `json:"full_page,omitempty"`
	Limit        *int           `json:"limit,omitempty"`
	Filename     string         `json:"filename,omitempty"`
}

func validateBrowserArgs(action string, args *browserArgs) *Error {
	if args.VersionSUTID != "" {
		if _, err := parseIDString("version_sut_id", args.VersionSUTID); err != nil {
			return err
		}
	}
	if err := validateText("account_role", args.AccountRole, 0, 100); err != nil {
		return err
	}
	switch action {
	case "goto":
		if strings.TrimSpace(args.URL) == "" {
			return Invalid("url", "url is required")
		}
		if err := validateText("url", args.URL, 0, 2000); err != nil {
			return err
		}
	case "snapshot", "storage_state":
	case "click", "download":
		if err := args.Target.validate(); err != nil {
			return err
		}
		if action == "download" {
			if err := validateText("filename", args.Filename, 0, 255); err != nil {
				return err
			}
		}
	case "fill":
		if err := args.Target.validate(); err != nil {
			return err
		}
		if args.Value == nil {
			return Invalid("value", "value is required")
		}
		if err := validateText("value", *args.Value, 0, 4096); err != nil {
			return err
		}
	case "press":
		if strings.TrimSpace(args.Key) == "" {
			return Invalid("key", "key is required")
		}
		if err := validateText("key", args.Key, 0, 50); err != nil {
			return err
		}
		if args.Target != nil {
			if err := args.Target.validate(); err != nil {
				return err
			}
		}
	case "wait_for":
		if args.Target != nil {
			if err := args.Target.validate(); err != nil {
				return err
			}
		}
		if args.TimeoutMS != nil && (*args.TimeoutMS < 100 || *args.TimeoutMS > 60000) {
			return Invalid("timeout_ms", "must be between 100 and 60000")
		}
	case "screenshot":
	case "console", "network":
		if args.Limit != nil && (*args.Limit < 1 || *args.Limit > 200) {
			return Invalid("limit", "must be between 1 and 200")
		}
	default:
		return Invalid("action", "unknown browser action: "+action)
	}
	return nil
}

type browserTool struct {
	deps        Deps
	name        string
	description string
	parameters  json.RawMessage
	action      string
}

func (t *browserTool) Name() string                { return t.name }
func (t *browserTool) Description() string         { return t.description }
func (t *browserTool) Parameters() json.RawMessage { return t.parameters }

func (t *browserTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args browserArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if err := validateBrowserArgs(t.action, &args); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode browser arguments failed: %w", err)
	}
	return portResult(t.deps.Execution.Browser(ctx, scope, t.action, normalized))
}

func browserParameters(extra map[string]any) map[string]any {
	properties := map[string]any{
		"version_sut_id": map[string]any{"type": "string", "description": "decimal version_sut ID; defaults to the case default"},
		"account_role":   map[string]any{"type": "string", "description": "account role of the browser context"},
	}
	for name, schema := range extra {
		properties[name] = schema
	}
	return properties
}

func browserToolSet(deps Deps) []Tool {
	stringSchema := map[string]any{"type": "string"}
	targetRef := map[string]any{
		"type":        "object",
		"description": "exactly one of role, text, test_id or selector",
		"properties": map[string]any{
			"role":     map[string]any{"type": "string"},
			"name":     map[string]any{"type": "string"},
			"exact":    map[string]any{"type": "boolean"},
			"text":     map[string]any{"type": "string"},
			"test_id":  map[string]any{"type": "string"},
			"selector": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
	limitSchema := map[string]any{"type": "integer", "minimum": 1, "maximum": 200}
	timeoutSchema := map[string]any{"type": "integer", "minimum": 100, "maximum": 60000}

	return []Tool{
		&browserTool{deps: deps, name: ToolBrowserGoto, action: "goto",
			description: "Open a page in the browser context of a SUT (the context is created lazily; the host must belong to that SUT environment).",
			parameters:  schemaObject(browserParameters(map[string]any{"url": stringSchema}), "url")},
		&browserTool{deps: deps, name: ToolBrowserSnapshot, action: "snapshot",
			description: "Read the accessibility snapshot of the current page (at most 12KB).",
			parameters:  schemaObject(browserParameters(nil))},
		&browserTool{deps: deps, name: ToolBrowserClick, action: "click",
			description: "Click the single element matching target. target must specify exactly one locator kind.",
			parameters:  schemaObject(browserParameters(map[string]any{"target": targetRef}), "target")},
		&browserTool{deps: deps, name: ToolBrowserFill, action: "fill",
			description: "Fill an input. secret=true makes the executor scrub the value; never put secrets into evidence.",
			parameters: schemaObject(browserParameters(map[string]any{
				"target": targetRef, "value": stringSchema, "secret": map[string]any{"type": "boolean"},
			}), "target", "value")},
		&browserTool{deps: deps, name: ToolBrowserPress, action: "press",
			description: "Press a key (Enter/Tab, ...) on the page or on a target element.",
			parameters:  schemaObject(browserParameters(map[string]any{"target": targetRef, "key": stringSchema}), "key")},
		&browserTool{deps: deps, name: ToolBrowserWaitFor, action: "wait_for",
			description: "Wait for an element to become visible (at most 60s); for longer time windows use execution.wait.",
			parameters:  schemaObject(browserParameters(map[string]any{"target": targetRef, "timeout_ms": timeoutSchema}))},
		&browserTool{deps: deps, name: ToolBrowserScreenshot, action: "screenshot",
			description: "Take a screenshot; the API stores it automatically as screenshot evidence.",
			parameters:  schemaObject(browserParameters(map[string]any{"full_page": map[string]any{"type": "boolean"}}))},
		&browserTool{deps: deps, name: ToolBrowserConsole, action: "console",
			description: "Read console messages; the API stores them automatically as console evidence.",
			parameters:  schemaObject(browserParameters(map[string]any{"limit": limitSchema}))},
		&browserTool{deps: deps, name: ToolBrowserNetwork, action: "network",
			description: "Read network requests; the API stores them automatically as network evidence.",
			parameters:  schemaObject(browserParameters(map[string]any{"limit": limitSchema}))},
		&browserTool{deps: deps, name: ToolBrowserDownload, action: "download",
			description: "Click a download target; the API stores the file automatically as download evidence.",
			parameters:  schemaObject(browserParameters(map[string]any{"target": targetRef, "filename": stringSchema}), "target")},
		&browserTool{deps: deps, name: ToolBrowserStorageState, action: "storage_state",
			description: "Export a storageState summary (P3 returns no credentials and stores nothing).",
			parameters:  schemaObject(browserParameters(nil))},
	}
}

// --- http / db tools -------------------------------------------------------

type httpArgs struct {
	VersionSUTID string            `json:"version_sut_id,omitempty"`
	APIID        string            `json:"api_id,omitempty"`
	Method       string            `json:"method,omitempty"`
	Path         string            `json:"path"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         string            `json:"body,omitempty"`
}

type httpRequestTool struct{ deps Deps }

func (t *httpRequestTool) Name() string { return ToolHTTPRequest }
func (t *httpRequestTool) Description() string {
	return "Send an HTTP request to the current SUT environment (only that SUT host; http evidence is stored automatically; credential headers are redacted)."
}
func (t *httpRequestTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"version_sut_id": map[string]any{"type": "string"},
		"api_id":         map[string]any{"type": "string"},
		"method":         map[string]any{"type": "string", "enum": []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}},
		"path":           map[string]any{"type": "string"},
		"headers":        map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"body":           map[string]any{"type": "string"},
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
		if _, err := parseIDString("version_sut_id", args.VersionSUTID); err != nil {
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
	if len(args.Body) > 64<<10 {
		return nil, Invalid("body", "body must be at most 64KB")
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode http arguments failed: %w", err)
	}
	return portResult(t.deps.Execution.HTTP(ctx, scope, normalized))
}

type dbQueryArgs struct {
	VersionSUTID string `json:"version_sut_id,omitempty"`
	DatabaseID   string `json:"database_id,omitempty"`
	SQL          string `json:"sql"`
	MaxRows      *int   `json:"max_rows,omitempty"`
}

type dbQueryTool struct{ deps Deps }

func (t *dbQueryTool) Name() string { return ToolDBQuery }
func (t *dbQueryTool) Description() string {
	return "Run one read-only SELECT/WITH query on a PostgreSQL database bound to the SUT (at most 200 rows / 5s; db evidence is stored automatically)."
}
func (t *dbQueryTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"version_sut_id": map[string]any{"type": "string"},
		"database_id":    map[string]any{"type": "string"},
		"sql":            map[string]any{"type": "string"},
		"max_rows":       map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
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
		if _, err := parseIDString("version_sut_id", args.VersionSUTID); err != nil {
			return nil, err
		}
	}
	if args.DatabaseID != "" {
		if _, err := parseIDString("database_id", args.DatabaseID); err != nil {
			return nil, err
		}
	}
	if args.MaxRows != nil && (*args.MaxRows < 1 || *args.MaxRows > 200) {
		return nil, Invalid("max_rows", "must be between 1 and 200")
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode db arguments failed: %w", err)
	}
	return portResult(t.deps.Execution.DBQuery(ctx, scope, normalized))
}

type schemaReadArgs struct {
	VersionSUTID string `json:"version_sut_id,omitempty"`
	DatabaseID   string `json:"database_id,omitempty"`
	Schema       string `json:"schema,omitempty"`
}

type schemaReadTool struct{ deps Deps }

func (t *schemaReadTool) Name() string { return ToolDatabaseSchemaRead }
func (t *schemaReadTool) Description() string {
	return "Read a tables/columns summary of the target database (read-only, at most 64KB)."
}
func (t *schemaReadTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"version_sut_id": map[string]any{"type": "string"},
		"database_id":    map[string]any{"type": "string"},
		"schema":         map[string]any{"type": "string"},
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
		if _, err := parseIDString("version_sut_id", args.VersionSUTID); err != nil {
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
	VersionSUTID string `json:"version_sut_id,omitempty"`
}

type stepRecordTool struct{ deps Deps }

func (t *stepRecordTool) Name() string { return ToolExecutionStepRecord }
func (t *stepRecordTool) Description() string {
	return "Record one executed step (upserted to the current case_run; stale dispatch epoch/token writes are rejected)."
}
func (t *stepRecordTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"seq":            map[string]any{"type": "integer", "minimum": 1},
		"status":         map[string]any{"type": "string", "enum": []string{"pending", "running", "passed", "failed", "blocked", "skipped"}},
		"observed":       map[string]any{"type": "string"},
		"error":          map[string]any{"type": "string"},
		"version_sut_id": map[string]any{"type": "string"},
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
		id, err := parseIDString("version_sut_id", args.VersionSUTID)
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
	VersionSUTID string `json:"version_sut_id,omitempty"`
}

type assertionRecordTool struct{ deps Deps }

func (t *assertionRecordTool) Name() string { return ToolExecutionAssertionRecord }
func (t *assertionRecordTool) Description() string {
	return "Record one assertion result; the server overwrites expected with the P2 assertion truth."
}
func (t *assertionRecordTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"step_seq":       map[string]any{"type": "integer", "minimum": 1},
		"assertion_seq":  map[string]any{"type": "integer", "minimum": 1},
		"verdict":        map[string]any{"type": "string", "enum": []string{"passed", "failed", "inconclusive", "skipped"}},
		"expected":       map[string]any{"type": "string"},
		"actual":         map[string]any{"type": "string"},
		"reason":         map[string]any{"type": "string"},
		"version_sut_id": map[string]any{"type": "string"},
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
		id, err := parseIDString("version_sut_id", args.VersionSUTID)
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
		"version_sut_id": map[string]any{"type": "string"},
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
		VersionSUTID  string `json:"version_sut_id"`
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
		id, err := parseIDString("version_sut_id", args.VersionSUTID)
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
	return "Read the test account credentials of a (version_sut, role) binding. Use only for login fill; never put them into evidence or a reply."
}
func (t *accountCredentialsTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"version_sut_id": map[string]any{"type": "string"},
		"role":           map[string]any{"type": "string"},
	}, "role")
}

func (t *accountCredentialsTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Execution == nil {
		return nil, missingDep("execution")
	}
	var args struct {
		VersionSUTID string `json:"version_sut_id"`
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
		id, err := parseIDString("version_sut_id", args.VersionSUTID)
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
	return "Submit a failure analysis candidate (refine only; never upgrade a non-application class to application; wave scope is validated by the server)."
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

// p3Tools returns the frozen P3 tool set in registration order.
func p3Tools(deps Deps) []Tool {
	tools := browserToolSet(deps)
	tools = append(tools,
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
	)
	return tools
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

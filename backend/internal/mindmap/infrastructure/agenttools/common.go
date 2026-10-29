// Package agenttools implements the six P5 mindmap agent tools of design spec
// §8.2: case.graph_read, case.graph_propose, case.review_submit,
// case.data_plan_submit, app_model_propose and explore.http.
//
// The tools are thin shells over the mindmap application ports: they validate
// the model arguments, resolve the calling run from the P1 tool-invocation
// context (never from a model-supplied runId), cap every payload and never touch
// the business tables. A submit tool only writes the active run's result/stats;
// materialization stays in the run worker (T10) and the exploration service
// (T13). The package declares narrow local ports so the composition root can
// inject the committed services without this package importing any other
// context's infrastructure.
package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Tool names, exactly as specified by design spec §8.2.
const (
	toolNameGraphRead      = "case.graph_read"
	toolNameGraphPropose   = "case.graph_propose"
	toolNameReviewSubmit   = "case.review_submit"
	toolNameDataPlanSubmit = "case.data_plan_submit"
	toolNameAppModelSubmit = "app_model_propose"
	toolNameExploreHTTP    = "explore.http"
)

// actionToolInvoked is the audit action recorded for every tool invocation. The
// metadata carries tool/run/actor/counts only: never a proposal or a page body.
const actionToolInvoked = "mindmap.tool.invoked"

// Tool error codes from design spec §8.2.
const (
	toolErrorInvalidArguments = "invalid_arguments"
	toolErrorRunNotFound      = "run_not_found"
	toolErrorProposalTooLarge = "proposal_too_large"
	toolErrorExploreNotAllow  = "explore_not_allowed"
)

// Argument and output bounds from design spec §8.2/§6.7/§6.9.
const (
	// maxGraphReadNodes and maxGraphReadBytes bound one case.graph_read reply.
	maxGraphReadNodes = 300
	maxGraphReadBytes = 64 * 1024
	// maxProposalBytes is the §6.7 1 MiB submission cap.
	maxProposalBytes = 1 << 20
	// maxReviewFindings and maxFindingDetailRunes bound case.review_submit.
	maxReviewFindings     = 200
	maxFindingDetailRunes = 2000
	// maxDataPlanItems bounds case.data_plan_submit.
	maxDataPlanItems = 60
	// maxAppModelNodes bounds app_model_propose.
	maxAppModelNodes = 200
	// maxExploreSummaryBytes and the list caps bound explore.http.
	maxExploreSummaryBytes = 32 * 1024
	maxExploreLinks        = 200
	maxExploreForms        = 100
	// maxExploreLinkBytes bounds one link/form/title/url field.
	maxExploreFieldBytes = 2048
	maxExploreTitleRunes = 500

	// maxRunScan bounds the ListActive scan used to resolve the calling run.
	// The run repository has no get-by-session lookup, so the tool scans the
	// queued/running runs (a single process keeps this set small).
	maxRunScan = 200

	// maxPreconditionsRunes matches the node content bound of §6.7.
	maxPreconditionsRunes = 4000
	// maxAppModelKeyLength matches the resource app-model key bound.
	maxAppModelKeyLength = 512
)

// GraphReader is the read side behind case.graph_read. *application.GraphService
// satisfies it.
type GraphReader interface {
	Get(ctx context.Context, versionID int64) (mindmapapp.GraphView, error)
}

// RunRepository is the narrow run-store slice behind the submit tools: the
// active-run scan that resolves the calling run and the CAS that stores the
// submission. *postgres.RunRepository satisfies it.
type RunRepository interface {
	ListActive(ctx context.Context, limit int) ([]mindmapdomain.CaseDesignRun, error)
	UpdateStatus(ctx context.Context, runID int64, from, to string, patch mindmapapp.RunPatch) (mindmapdomain.CaseDesignRun, error)
}

// AnchorReader supplies the per-node anchor count of case.graph_read.
// application.CodeAnchorGateway satisfies it. It is optional: without it the
// summary reports anchorCount=0.
type AnchorReader interface {
	List(ctx context.Context, versionID, nodeID int64) ([]mindmapapp.Anchor, error)
}

// FindingReader supplies the open blocker findings of case.graph_read.
// application.FindingRepository satisfies it. It is optional: without it the
// summary reports zero open blockers.
type FindingReader interface {
	List(ctx context.Context, versionID int64, status, severity string, nodeID *int64) ([]mindmapdomain.CaseReviewFinding, error)
}

// MethodProber is the optional method-aware extension of application.Prober.
// application.Prober only performs GET; an implementation that also supports
// HEAD exposes ProbeMethod with the same per-environment allowlist contract.
// explore.http rejects HEAD when the injected prober does not implement it,
// rather than silently downgrading to GET.
type MethodProber interface {
	ProbeMethod(ctx context.Context, method, rawURL, cookieHeader string, allowedHosts []string) (mindmapapp.ProbeResult, error)
}

// ToolDeps are the ports the six tools consume. Graphs, Runs and Prober are
// required: NewTools returns nil when one is missing, so a stale composition
// root cannot expose a half-working tool set to a profile. Catalog, Anchors,
// Findings and Audit are optional enrichments.
type ToolDeps struct {
	// Graphs is the graph read behind case.graph_read (GraphService.Get).
	Graphs GraphReader
	// Runs is the run store the submit tools resolve and write.
	Runs RunRepository
	// Catalog is the P2 resource catalog. app_model_propose reads it best-effort
	// to report how many proposed keys already exist; it never imports here.
	Catalog mindmapapp.CatalogGateway
	// Prober is the read-only HTTP prober behind explore.http.
	Prober mindmapapp.Prober
	// Anchors and Findings are the optional graph_read count sources.
	Anchors  AnchorReader
	Findings FindingReader
	// Audit records mindmap.tool.invoked. A nil recorder disables auditing.
	Audit mindmapapp.AuditRecorder
}

// toolDefinition is one tool's frozen metadata plus its execution function.
type toolDefinition struct {
	name        string
	description string
	schema      string
	run         func(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error)
}

// mindmapTool implements domain.Tool over one definition and the injected ports.
type mindmapTool struct {
	deps ToolDeps
	def  toolDefinition
}

// Name implements domain.Tool.
func (t mindmapTool) Name() string { return t.def.name }

// Description implements domain.Tool.
func (t mindmapTool) Description() string { return t.def.description }

// InputSchema implements domain.Tool.
func (t mindmapTool) InputSchema() json.RawMessage { return json.RawMessage(t.def.schema) }

// Execute implements domain.Tool.
func (t mindmapTool) Execute(ctx context.Context, args json.RawMessage) (agentdomain.ToolResult, error) {
	return t.def.run(ctx, t.deps, args)
}

// NewTools returns the six P5 tools in the frozen spec §8.2 order. A missing
// Graphs, Runs or Prober disables the whole set (nil), mirroring the P3/P4 tool
// constructors.
func NewTools(deps ToolDeps) []agentdomain.Tool {
	if deps.Graphs == nil || deps.Runs == nil || deps.Prober == nil {
		return nil
	}
	definitions := []toolDefinition{
		{
			name:        toolNameGraphRead,
			description: "Read the current mindmap summary (nodes, coverage, baseline, open blockers); never returns full node text. At most 300 nodes / 64 KiB.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"}},"required":["versionId"],"additionalProperties":false}`,
			run:         runGraphRead,
		},
		{
			name:        toolNameGraphPropose,
			description: "Submit the structured case proposal of a design run: flat spec §6.7 scenarios/cases at the top level (or a nested payload object). Stores it in the active run result; the server materializes it after the run finishes.",
			schema:      graphProposeSchema,
			run:         runGraphPropose,
		},
		{
			name:        toolNameReviewSubmit,
			description: "Submit the review findings of a review run (max 200; category/severity enums; detail <=2000 chars). Stores them in the active run result only.",
			schema:      reviewSubmitSchema,
			run:         runReviewSubmit,
		},
		{
			name:        toolNameDataPlanSubmit,
			description: "Submit preconditions/test data for existing cases of a data-plan run (max 60 items with nodeId+nodeRevision). Stores them in the active run result only.",
			schema:      dataPlanSubmitSchema,
			run:         runDataPlanSubmit,
		},
		{
			name:        toolNameAppModelSubmit,
			description: "Submit the app model elements discovered during an exploration run (max 200 nodes). Stores them in the active run result; the server imports them later.",
			schema:      appModelProposeSchema,
			run:         runAppModelPropose,
		},
		{
			name:        toolNameExploreHTTP,
			description: "Probe one allowlisted application URL read-only with GET or HEAD and return a status/title/links/forms summary (<=32 KiB). Non-allowlisted requests are refused.",
			schema:      exploreHTTPSchema,
			run:         runExploreHTTP,
		},
	}
	tools := make([]agentdomain.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, mindmapTool{deps: deps, def: definition})
	}
	return tools
}

// decodeToolArguments unmarshals the model arguments into out. An empty
// argument string is the empty object the executor normalizes to; anything that
// is not a JSON object is a correctable invalid_arguments failure.
func decodeToolArguments(args json.RawMessage, out any) *agentdomain.ToolError {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if trimmed[0] != '{' {
		return invalidToolArguments("arguments must be a JSON object with the documented fields")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return invalidToolArguments("arguments must be a JSON object with the documented fields")
	}
	if err := json.Unmarshal(trimmed, out); err != nil {
		return invalidToolArguments("arguments do not match the documented fields: %v", err)
	}
	return nil
}

// requireJSONObject reports an invalid_arguments failure when raw is not a JSON
// object.
func requireJSONObject(raw json.RawMessage, field string) *agentdomain.ToolError {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return invalidToolArguments("%s must be a JSON object", field)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return invalidToolArguments("%s must be a JSON object", field)
	}
	return nil
}

// requireJSONArray decodes raw as a JSON array and reports an invalid_arguments
// failure otherwise. A missing field or an explicit null is rejected here; call
// sites that treat the field as optional check presence first.
func requireJSONArray(raw json.RawMessage, field string) ([]json.RawMessage, *agentdomain.ToolError) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, invalidToolArguments("%s must be a JSON array", field)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil || items == nil {
		return nil, invalidToolArguments("%s must be a JSON array", field)
	}
	return items, nil
}

// invalidToolArguments builds a correctable invalid_arguments tool error.
func invalidToolArguments(format string, args ...any) *agentdomain.ToolError {
	return &agentdomain.ToolError{Code: toolErrorInvalidArguments, Message: fmt.Sprintf(format, args...)}
}

// toolFailure builds one typed tool error.
func toolFailure(code, format string, args ...any) *agentdomain.ToolError {
	return &agentdomain.ToolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// parsePositiveID parses a positive decimal id. The tool contract carries every
// mindmap id as a JSON string (D29).
func parsePositiveID(raw string) (int64, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 19 {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// formatID renders one application-generated id as the decimal string the tool
// contract uses (D29).
func formatID(id int64) string { return strconv.FormatInt(id, 10) }

// resolveActiveRun finds the queued/running run of kind+version whose agent
// session is the one the P1 executor attached to ctx. The model can never name
// its own run: the session id comes from the invocation context only.
func resolveActiveRun(ctx context.Context, deps ToolDeps, kind string, versionID int64) (mindmapdomain.CaseDesignRun, error) {
	invocation, ok := agentapp.InvocationFromContext(ctx)
	if !ok || invocation.SessionID <= 0 {
		return mindmapdomain.CaseDesignRun{}, toolFailure(toolErrorRunNotFound,
			"no active run can be resolved: this tool must be called inside an agent run")
	}
	runs, err := deps.Runs.ListActive(ctx, maxRunScan)
	if err != nil {
		return mindmapdomain.CaseDesignRun{}, fmt.Errorf("mindmap: list active runs: %w", err)
	}
	for _, run := range runs {
		if run.AgentSessionID == nil || *run.AgentSessionID != invocation.SessionID {
			continue
		}
		// A non-positive versionID means "any version": app_model_propose is
		// addressed by systemId, not by versionId.
		if versionID > 0 && run.VersionID != versionID {
			continue
		}
		if run.Kind != kind {
			continue
		}
		return run, nil
	}
	if versionID > 0 {
		return mindmapdomain.CaseDesignRun{}, toolFailure(toolErrorRunNotFound,
			"no active %s run of version %d is bound to this agent session", kind, versionID)
	}
	return mindmapdomain.CaseDesignRun{}, toolFailure(toolErrorRunNotFound,
		"no active %s run is bound to this agent session", kind)
}

// storeRunResult writes the submission into the active run's result and bumps
// the named stats counter. It is a CAS on the status the run was resolved with,
// so a run that finished concurrently is reported as run_not_found instead of
// being overwritten. Only the run row is touched: no business table is written.
func storeRunResult(ctx context.Context, deps ToolDeps, run mindmapdomain.CaseDesignRun, payload json.RawMessage, statKey string) (int, error) {
	stats, count := incrementRunStat(run.Stats, statKey)
	_, err := deps.Runs.UpdateStatus(ctx, run.ID, run.Status, run.Status, mindmapapp.RunPatch{
		Result: payload,
		Stats:  stats,
	})
	if err != nil {
		if isRunGone(err) {
			return 0, toolFailure(toolErrorRunNotFound,
				"the active run is no longer available: %v", err)
		}
		return 0, fmt.Errorf("mindmap: store run result: %w", err)
	}
	return count, nil
}

// isRunGone reports whether err means the resolved run already moved on.
func isRunGone(err error) bool {
	var domainErr *mindmapdomain.Error
	if !errors.As(err, &domainErr) {
		return false
	}
	return domainErr.Kind == mindmapdomain.KindNotFound || domainErr.Reason == "run_state_conflict"
}

// incrementRunStat merges one numeric counter into the stored stats document and
// returns the new document and counter value. A malformed existing document is
// replaced by a fresh one, never fatal.
func incrementRunStat(raw []byte, key string) (json.RawMessage, int) {
	document := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &document)
	}
	count := 0
	switch value := document[key].(type) {
	case float64:
		count = int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			count = int(parsed)
		}
	case int:
		count = value
	}
	if count < 0 {
		count = 0
	}
	count++
	document[key] = count
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, count
	}
	return encoded, count
}

// recordToolAudit appends one mindmap.tool.invoked entry. Auditing is
// best-effort and never fails the tool; the metadata never carries content.
func recordToolAudit(ctx context.Context, deps ToolDeps, toolName string, runID int64, metadata map[string]any) {
	if deps.Audit == nil {
		return
	}
	actor := invocationActorID(ctx)
	details := map[string]any{
		"tool":  toolName,
		"runId": runID,
		"actor": actor,
	}
	for key, value := range metadata {
		details[key] = value
	}
	targetID := ""
	if runID > 0 {
		targetID = formatID(runID)
	}
	_ = deps.Audit.Record(ctx, mindmapapp.AuditEvent{
		Action:     actionToolInvoked,
		ActorID:    actor,
		TargetType: "case_design_run",
		TargetID:   targetID,
		Metadata:   details,
	})
}

// invocationActorID returns the run-scoped actor, or 0 when the invocation did
// not carry one.
func invocationActorID(ctx context.Context) int64 {
	invocation, ok := agentapp.InvocationFromContext(ctx)
	if !ok || invocation.ActorUserID == nil || *invocation.ActorUserID <= 0 {
		return 0
	}
	return *invocation.ActorUserID
}

// encodeToolJSON marshals one successful tool payload.
func encodeToolJSON(payload any) (agentdomain.ToolResult, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return agentdomain.ToolResult{}, fmt.Errorf("mindmap: encode tool result: %w", err)
	}
	return agentdomain.ToolResult{Content: string(encoded)}, nil
}

// runeCount is the rune length used by the text bounds.
func runeCount(value string) int { return utf8.RuneCountInString(value) }

// truncateRunes cuts value to at most max runes.
func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	return string([]rune(value)[:max])
}

// truncateBytes cuts value to at most max bytes on a UTF-8 boundary.
func truncateBytes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && cut < len(value) && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// formatIDs renders ids as decimal strings with a stable order.
func formatIDs(ids []int64) []string {
	rendered := make([]string, 0, len(ids))
	for _, id := range ids {
		rendered = append(rendered, formatID(id))
	}
	return rendered
}

// optionalString returns a pointer to value when it is non-empty.
func optionalString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

// validAppModelKind reports whether kind is one of the P2 app-model kinds. The
// tool validates only the shape; the resource layer re-validates the key rules
// while importing.
func validAppModelKind(kind string) bool {
	switch kind {
	case "page", "route", "api", "component":
		return true
	default:
		return false
	}
}

// scrubOptionalTime renders the baseline frozen time.
func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

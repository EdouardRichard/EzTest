// Package agent_tool implements the supplement agent's tool set (design spec
// §9.2): the read-only context tool, the two proposal tools, the requirement-gap
// escalation and the optional finish. The tools are thin shells over the mindmap
// application repository ports: they validate model arguments, resolve the
// calling run from the P1 tool-invocation scope (never from model-supplied ids),
// cap every payload and accumulate the validated proposals in the active run's
// result document. They never write a business table, the baseline or the graph.
package agent_tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Tool names, exactly as specified by design spec §9.2.
const (
	toolNameContextRead = "supplement.context_read"
	toolNameCasePropose = "supplement.case_propose"
	toolNameAffected    = "supplement.affected_cases_propose"
	toolNameEscalate    = "supplement.escalate"
	toolNameFinish      = "supplement.finish"
)

// Per-run caps of spec §6.4.5/§9.2.
const (
	supplementMaxCases       = 5
	supplementMaxAffected    = 20
	supplementMaxEscalations = 3
)

// Bounds mirrored from the mindmap graph validators (spec §6.7/§9.2).
const (
	maxTitleRunes              = 200
	maxDescriptionRunes        = 4000
	maxPreconditionsRunes      = 4000
	maxStepActionRunes         = 2000
	maxStepExpectedRunes       = 2000
	maxStepDataRunes           = 4000
	maxAssertionTargetRunes    = 1000
	maxAssertionExpressionRune = 4000
	maxAssertionExpectedRunes  = 2000
	maxAssertionsPerCase       = 30
	maxRequirementIDsPerItem   = 20
	maxCodeRefsPerAffected     = 20
	maxCodeRefPathRunes        = 1000
	maxCodeRefReasonRunes      = 2000
	maxEscalationTitleRunes    = 200
	maxEscalationDetailRunes   = 4000
	maxSummaryRunes            = 2000
	maxContextRequirements     = 20
	maxContextCases            = 300
	maxContextCandidates       = 20
	maxContextFieldRunes       = 2000
	maxRunScan                 = 200
)

// Tool error codes (the P1 tool-error vocabulary of spec §8.2/§9.6).
const (
	toolErrorInvalidArguments = "invalid_arguments"
	toolErrorRunNotFound      = "run_not_found"
	toolErrorInternal         = "internal"
)

// actionToolInvoked is the audit action recorded for every supplement tool call;
// the metadata carries tool/run/actor/counts only, never proposal content.
const actionToolInvoked = "mindmap.tool.invoked"

// GraphReader is the graph summary read behind supplement.context_read and the
// affected-case ownership validation. *application.GraphService satisfies it.
type GraphReader interface {
	Get(ctx context.Context, versionID int64) (mindmapapp.GraphView, error)
}

// BatchResultSummary is the read-only "recent results" projection injected into
// the supplement context. The execution context supplies it through an adapter;
// the mindmap context never reads execution tables.
type BatchResultSummary struct {
	BatchID      int64
	Status       string
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
	Skipped      int
	Cases        []BatchResultCase
}

// BatchResultCase is one failed/skipped case summary of the recent batch.
type BatchResultCase struct {
	CaseNodeID   int64
	Title        string
	Status       string
	FailureClass string
}

// BatchResultReader is the optional execution read port behind the recent-result
// summary. Without it the context tool reports an empty summary and a warning.
type BatchResultReader interface {
	LatestBatchResults(ctx context.Context, versionID, batchID int64) (BatchResultSummary, error)
}

// Deps are the ports of the supplement tool set. Runs, Graphs and Requirements
// are required: NewTools returns nil when one is missing so a stale composition
// root cannot expose a half-working profile. Impact, Results and Audit are
// optional enrichments.
type Deps struct {
	// Runs is the mindmap run repository: the tools resolve the calling run and
	// accumulate the proposals in its result document.
	Runs mindmapapp.RunRepository
	// Graphs is the graph summary read (existing cases, coverage).
	Graphs GraphReader
	// Requirements validates the clarified requirement sources of a proposal.
	Requirements mindmapapp.RequirementReader
	// Impact is the read-only P4 impact summary. Optional.
	Impact mindmapapp.CodeFactsGateway
	// Results is the recent terminal batch summary. Optional.
	Results BatchResultReader
	// Audit records mindmap.tool.invoked. Optional.
	Audit mindmapapp.AuditRecorder
}

// toolDefinition is one tool's frozen metadata and its execution function.
type toolDefinition struct {
	name        string
	description string
	schema      string
	run         func(ctx context.Context, deps Deps, args json.RawMessage) (agentdomain.ToolResult, error)
}

// supplementTool implements domain.Tool over one definition and the injected
// ports.
type supplementTool struct {
	deps Deps
	def  toolDefinition
}

// Name implements domain.Tool.
func (t supplementTool) Name() string { return t.def.name }

// Description implements domain.Tool.
func (t supplementTool) Description() string { return t.def.description }

// InputSchema implements domain.Tool.
func (t supplementTool) InputSchema() json.RawMessage { return json.RawMessage(t.def.schema) }

// Execute implements domain.Tool.
func (t supplementTool) Execute(ctx context.Context, args json.RawMessage) (agentdomain.ToolResult, error) {
	return t.def.run(ctx, t.deps, args)
}

// NewTools returns the five supplement tools of spec §9.2 in their frozen order.
// A missing Runs, Graphs or Requirements port disables the whole set (nil).
func NewTools(deps Deps) []agentdomain.Tool {
	if deps.Runs == nil || deps.Graphs == nil || deps.Requirements == nil {
		return nil
	}
	definitions := []toolDefinition{
		{
			name:        toolNameContextRead,
			description: "Read the confirmed requirements, current cases, requirement coverage, recent batch result summary and P4 impact summary of the calling supplement run. Never returns evidence bodies or credentials. At most 300 cases / 20 requirements.",
			schema:      schemaContextRead,
			run:         runContextRead,
		},
		{
			name:        toolNameCasePropose,
			description: "Propose new supplement cases (at most 5 per run). Every case needs a title, at least one step, at least one assertion and at least one clarified requirement id; duplicates and invalid items are skipped with warnings. Stored in the active run only; the server materializes it after the run finishes.",
			schema:      schemaCasePropose,
			run:         runCasePropose,
		},
		{
			name:        toolNameAffected,
			description: "Propose affected existing cases of an incremental_design run (at most 20). Only incremental_design runs may call it; each entry needs an existing non-draft case node id, confidence high/medium/low, a reason, the source (requirement|code) and may carry up to 20 codeRefs {path, line?, reason?} for the changed code locations.",
			schema:      schemaAffectedCases,
			run:         runAffectedCasesPropose,
		},
		{
			name:        toolNameEscalate,
			description: "Escalate a requirement gap as a requirement_gap issue (at most 3 per run). Accepts either one {title,detail,requirementIds} object or an {escalations:[...]} array. Every escalation needs a title and at least one clarified requirement id. Never invent requirements.",
			schema:      schemaEscalate,
			run:         runEscalate,
		},
		{
			name:        toolNameFinish,
			description: "Optionally finish the supplement run with a short summary. The server materializes the stored proposals after the run ends; an empty submission is valid.",
			schema:      schemaFinish,
			run:         runFinish,
		},
	}
	tools := make([]agentdomain.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, supplementTool{deps: deps, def: definition})
	}
	return tools
}

// ValidateTools verifies that every tool the supplement-agent profile allows is
// registered. The composition root must call it before seeding and treat a
// failure as fatal (fail closed, spec §9.1).
func ValidateTools(registry *agentapp.ToolRegistry) error {
	for _, name := range mindmapdomain.SupplementAllowedTools() {
		if tool, ok := registry.Get(name); !ok || tool == nil {
			return fmt.Errorf("mindmap: supplement-agent profile allows tool %q, which is not registered", name)
		}
	}
	return nil
}

// --- run scope -------------------------------------------------------------

// resolveRun resolves the active supplement/incremental_design run of the agent
// session carried by the P1 invocation context. The explicit scoped run id is
// preferred; the active-run scan is the fallback used before the scoped runtime
// adapter is wired.
func resolveRun(ctx context.Context, deps Deps) (mindmapdomain.CaseDesignRun, error) {
	invocation, ok := agentapp.InvocationFromContext(ctx)
	if !ok {
		return mindmapdomain.CaseDesignRun{}, runNotFoundError("this tool must be called inside a supplement agent run")
	}
	if invocation.SupplementRunID > 0 && invocation.VersionID > 0 {
		run, err := deps.Runs.Get(ctx, invocation.VersionID, invocation.SupplementRunID)
		if err == nil && isSupplementRunKind(run.Kind) && run.IsActive() {
			return run, nil
		}
	}
	if invocation.SessionID <= 0 {
		return mindmapdomain.CaseDesignRun{}, runNotFoundError("no active supplement run is bound to this agent session")
	}
	runs, err := deps.Runs.ListActive(ctx, maxRunScan)
	if err != nil {
		slog.ErrorContext(ctx, "mindmap: supplement tool could not list active runs", "error", err)
		return mindmapdomain.CaseDesignRun{}, internalError("the active supplement run could not be resolved")
	}
	for _, run := range runs {
		if run.AgentSessionID == nil || *run.AgentSessionID != invocation.SessionID {
			continue
		}
		if invocation.VersionID > 0 && run.VersionID != invocation.VersionID {
			continue
		}
		if !isSupplementRunKind(run.Kind) {
			continue
		}
		return run, nil
	}
	return mindmapdomain.CaseDesignRun{}, runNotFoundError(
		"no active supplement or incremental_design run is bound to this agent session")
}

// isSupplementRunKind reports whether kind is one of the P7 supplement kinds.
func isSupplementRunKind(kind string) bool {
	return kind == mindmapapp.RunKindSupplement || kind == mindmapapp.RunKindIncrementalDesign
}

// runBatchID returns the parent batch id of a supplement run.
func runBatchID(run mindmapdomain.CaseDesignRun) int64 {
	if run.BatchID == nil {
		return 0
	}
	return *run.BatchID
}

// runIsGone reports whether a run CAS failure means the run is no longer active.
func runIsGone(err error) bool {
	var domainErr *mindmapdomain.Error
	if !errors.As(err, &domainErr) {
		return false
	}
	return domainErr.Kind == mindmapdomain.KindNotFound || domainErr.Reason == "run_state_conflict"
}

// --- submission document ---------------------------------------------------

// submissionFields is the tool-owned subset of the run.result document. It is a
// separate type so the unknown-key-preserving encode can marshal it without
// recursing into submissionDocument.
type submissionFields struct {
	Cases         []caseProposal     `json:"cases,omitempty"`
	AffectedCases []affectedProposal `json:"affectedCases,omitempty"`
	Escalations   []escalation       `json:"escalations,omitempty"`
	Summary       string             `json:"summary,omitempty"`
}

// submissionDocument is the tool-owned view of the run.result shape shared with
// the T9 materializer and the incremental plan. The keys this document does not
// model are starter-owned: T16 presets impactAvailable/unmappedChanges (and may
// record degraded/warnings) before the agent run, and T17 wires the P4 impact
// reader that supplies them; the supplement tools must carry those keys through
// every rewrite untouched instead of dropping or fabricating them.
type submissionDocument struct {
	submissionFields
	// extra holds every run.result key owned by the starter or a future writer.
	extra map[string]json.RawMessage
}

// caseProposal is one supplement.case_propose entry. Ids are decimal strings
// (D29) and the materializer accepts the same JSON shape.
type caseProposal struct {
	Title          string          `json:"title"`
	Description    string          `json:"description,omitempty"`
	Priority       string          `json:"priority"`
	Preconditions  string          `json:"preconditions,omitempty"`
	TestData       json.RawMessage `json:"testData,omitempty"`
	RequirementIDs []string        `json:"requirementIds"`
	Steps          []caseStep      `json:"steps"`
	Assertions     []caseAssertion `json:"assertions"`
	Confidence     string          `json:"confidence"`
	// Rationale is the optional agent explanation of spec §9.2; it is stored in
	// run.result only (the materializer does not persist it).
	Rationale string `json:"rationale,omitempty"`
}

// caseStep is one proposed step.
type caseStep struct {
	Action   string `json:"action"`
	Expected string `json:"expected"`
	Data     string `json:"data"`
}

// caseAssertion is one proposed assertion; StepIndex is the optional 0-based
// index into the case steps.
type caseAssertion struct {
	StepIndex  *int   `json:"stepIndex,omitempty"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Expression string `json:"expression"`
	Expected   string `json:"expected"`
}

// affectedProposal is one supplement.affected_cases_propose entry.
type affectedProposal struct {
	CaseNodeID     string    `json:"caseNodeId"`
	Confidence     string    `json:"confidence"`
	Reason         string    `json:"reason"`
	Source         string    `json:"source"`
	RequirementIDs []string  `json:"requirementIds,omitempty"`
	CodeRefs       []codeRef `json:"codeRefs,omitempty"`
}

// codeRef is one affected-case code location of the spec §5.8/§9.2 payload. It
// is the same JSON shape the T9 materializer parses into its revalidation event
// refs.code list; Line and Reason are optional.
type codeRef struct {
	Path   string `json:"path"`
	Line   *int   `json:"line,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// escalation is one supplement.escalate entry.
type escalation struct {
	Title          string   `json:"title"`
	Detail         string   `json:"detail,omitempty"`
	RequirementIDs []string `json:"requirementIds"`
}

// warning is one skipped-item warning returned to the model and stored in the
// run stats counts only.
type warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// decodeSubmission reads the stored result document, tolerating an empty or
// malformed document (a fresh run has none). Every key outside the tool-owned
// subset is kept verbatim so a later encode preserves it.
func decodeSubmission(raw []byte) submissionDocument {
	var document submissionDocument
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return document
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &all); err != nil {
		return submissionDocument{}
	}
	var fields submissionFields
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return submissionDocument{}
	}
	document.submissionFields = fields
	for key, value := range all {
		switch key {
		case "cases", "affectedCases", "escalations", "summary":
			continue
		}
		if document.extra == nil {
			document.extra = make(map[string]json.RawMessage, len(all))
		}
		document.extra[key] = append(json.RawMessage(nil), value...)
	}
	return document
}

// encode serializes the tool-owned fields and re-attaches every preserved key.
func (d submissionDocument) encode() ([]byte, error) {
	known, err := json.Marshal(d.submissionFields)
	if err != nil || len(d.extra) == 0 {
		return known, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(known, &fields); err != nil {
		return nil, err
	}
	merged := make(map[string]json.RawMessage, len(d.extra)+len(fields))
	for key, value := range d.extra {
		merged[key] = value
	}
	for key, value := range fields {
		merged[key] = value
	}
	return json.Marshal(merged)
}

// storeSubmission persists the accumulated document through the run status CAS
// and bumps one stats counter. Only the run row is written.
func storeSubmission(ctx context.Context, deps Deps, run mindmapdomain.CaseDesignRun, document submissionDocument, statKey string, statDelta int) error {
	encoded, err := document.encode()
	if err != nil {
		return internalError("encode the supplement submission")
	}
	stats := mergeStat(run.Stats, statKey, statDelta)
	_, err = deps.Runs.UpdateStatus(ctx, run.ID, run.Status, run.Status, mindmapapp.RunPatch{
		Result: encoded,
		Stats:  stats,
	})
	if err != nil {
		if runIsGone(err) {
			return runNotFoundError("the active run is no longer available")
		}
		slog.ErrorContext(ctx, "mindmap: supplement tool could not store the submission",
			"run_id", run.ID, "error", err)
		return internalError("the supplement submission could not be stored")
	}
	return nil
}

// mergeStat merges a numeric delta into the stored stats document and returns
// the encoded result.
func mergeStat(raw []byte, key string, delta int) json.RawMessage {
	document := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &document)
	}
	count := 0
	switch value := document[key].(type) {
	case float64:
		count = int(value)
	case int:
		count = value
	}
	count += delta
	if count < 0 {
		count = 0
	}
	document[key] = count
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil
	}
	return encoded
}

// --- validation helpers ----------------------------------------------------

// caseProposeInput is the validated argument shape of supplement.case_propose.
type caseProposeInput struct {
	Cases []rawCaseProposal `json:"cases"`
}

// rawCaseProposal is the model-supplied case shape; its fields are validated and
// normalized into a caseProposal.
type rawCaseProposal struct {
	Title          string          `json:"title"`
	Description    string          `json:"description"`
	Priority       string          `json:"priority"`
	Preconditions  string          `json:"preconditions"`
	TestData       json.RawMessage `json:"testData"`
	RequirementIDs []string        `json:"requirementIds"`
	Steps          []caseStep      `json:"steps"`
	Assertions     []caseAssertion `json:"assertions"`
	Confidence     string          `json:"confidence"`
	Rationale      string          `json:"rationale"`
}

// affectedProposeInput is the validated argument shape of
// supplement.affected_cases_propose.
type affectedProposeInput struct {
	Cases []affectedProposal `json:"cases"`
}

// escalateInput is the validated argument shape of supplement.escalate. The
// original tool accepted only the array wrapper; the spec §9.2 table documents a
// single {title,detail,requirementIds} object, so both shapes are accepted (but
// never mixed in one call).
type escalateInput struct {
	Escalations    []escalation `json:"escalations"`
	Title          string       `json:"title"`
	Detail         string       `json:"detail"`
	RequirementIDs []string     `json:"requirementIds"`
}

// finishInput is the validated argument shape of supplement.finish.
type finishInput struct {
	Summary string `json:"summary"`
}

// confirmedRequirements loads the clarified requirement ids of the run version.
func confirmedRequirements(ctx context.Context, deps Deps, versionID int64) (map[int64]struct{}, error) {
	requirements, err := deps.Requirements.ListConfirmed(ctx, versionID)
	if err != nil {
		slog.ErrorContext(ctx, "mindmap: supplement tool could not read the clarified requirements",
			"version_id", versionID, "error", err)
		return nil, internalError("the clarified requirements could not be read")
	}
	confirmed := make(map[int64]struct{}, len(requirements))
	for _, requirement := range requirements {
		confirmed[requirement.ID] = struct{}{}
	}
	return confirmed, nil
}

// normalizeRequirementIDs validates one decimal string id list against the
// confirmed set and returns the normalized strings.
func normalizeRequirementIDs(raw []string, confirmed map[int64]struct{}) ([]string, error) {
	if len(raw) == 0 {
		return nil, invalidArguments("requirementIds must contain at least one clarified requirement id")
	}
	if len(raw) > maxRequirementIDsPerItem {
		return nil, invalidArguments(fmt.Sprintf("at most %d requirement ids are allowed per entry", maxRequirementIDsPerItem))
	}
	ids := make([]string, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for _, value := range raw {
		id, err := parsePositiveID(value)
		if err != nil {
			return nil, invalidArguments("requirementIds must be positive decimal strings")
		}
		if _, ok := confirmed[id]; !ok {
			return nil, invalidArguments(fmt.Sprintf("requirement %d is not a clarified requirement of this version", id))
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	return ids, nil
}

// normalizeCodeRefs validates the optional affected-case code refs of spec
// §5.8/§9.2: at most 20 refs, every path non-empty (trimmed), a positive line
// when present and bounded path/reason text. The shape matches the T9
// materializer parser so the refs reach case.revalidation_marked refs.code.
func normalizeCodeRefs(raw []codeRef) ([]codeRef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxCodeRefsPerAffected {
		return nil, invalidArguments(fmt.Sprintf("at most %d codeRefs are allowed per affected case", maxCodeRefsPerAffected))
	}
	refs := make([]codeRef, 0, len(raw))
	for index, ref := range raw {
		path := strings.TrimSpace(ref.Path)
		if path == "" {
			return nil, invalidArguments(fmt.Sprintf("codeRefs[%d].path must not be empty", index))
		}
		if err := checkRunes("codeRefs.path", path, maxCodeRefPathRunes); err != nil {
			return nil, err
		}
		if ref.Line != nil && *ref.Line <= 0 {
			return nil, invalidArguments(fmt.Sprintf("codeRefs[%d].line must be a positive line number", index))
		}
		reason := strings.TrimSpace(ref.Reason)
		if err := checkRunes("codeRefs.reason", reason, maxCodeRefReasonRunes); err != nil {
			return nil, err
		}
		var line *int
		if ref.Line != nil {
			value := *ref.Line
			line = &value
		}
		refs = append(refs, codeRef{Path: path, Line: line, Reason: reason})
	}
	return refs, nil
}

// normalizeCase validates one proposed case and returns its canonical form.
func normalizeCase(raw rawCaseProposal, confirmed map[int64]struct{}) (caseProposal, error) {
	title := strings.TrimSpace(raw.Title)
	if title == "" {
		return caseProposal{}, invalidArguments("a supplement case needs a title")
	}
	if err := checkRunes("title", title, maxTitleRunes); err != nil {
		return caseProposal{}, err
	}
	if err := checkRunes("description", raw.Description, maxDescriptionRunes); err != nil {
		return caseProposal{}, err
	}
	if err := checkRunes("preconditions", raw.Preconditions, maxPreconditionsRunes); err != nil {
		return caseProposal{}, err
	}
	if err := checkRunes("rationale", raw.Rationale, maxDescriptionRunes); err != nil {
		return caseProposal{}, err
	}
	priority := strings.TrimSpace(raw.Priority)
	if priority == "" {
		priority = mindmapdomain.PriorityP2
	}
	switch priority {
	case mindmapdomain.PriorityP0, mindmapdomain.PriorityP1, mindmapdomain.PriorityP2, mindmapdomain.PriorityP3:
	default:
		return caseProposal{}, invalidArguments("priority must be P0, P1, P2 or P3")
	}
	testData, err := normalizeTestData(raw.TestData)
	if err != nil {
		return caseProposal{}, err
	}
	if len(raw.Steps) == 0 {
		return caseProposal{}, invalidArguments("a supplement case needs at least one step")
	}
	if len(raw.Steps) > mindmapdomain.MaxStepsPerCase {
		return caseProposal{}, invalidArguments(fmt.Sprintf("a supplement case has at most %d steps", mindmapdomain.MaxStepsPerCase))
	}
	steps := make([]caseStep, 0, len(raw.Steps))
	for index, step := range raw.Steps {
		action := strings.TrimSpace(step.Action)
		if action == "" {
			return caseProposal{}, invalidArguments(fmt.Sprintf("steps[%d].action must not be empty", index))
		}
		if err := checkRunes("steps.action", action, maxStepActionRunes); err != nil {
			return caseProposal{}, err
		}
		if err := checkRunes("steps.expected", step.Expected, maxStepExpectedRunes); err != nil {
			return caseProposal{}, err
		}
		if err := checkRunes("steps.data", step.Data, maxStepDataRunes); err != nil {
			return caseProposal{}, err
		}
		steps = append(steps, caseStep{Action: action, Expected: step.Expected, Data: step.Data})
	}
	if len(raw.Assertions) == 0 {
		return caseProposal{}, invalidArguments("a supplement case needs at least one assertion")
	}
	if len(raw.Assertions) > maxAssertionsPerCase {
		return caseProposal{}, invalidArguments(fmt.Sprintf("a supplement case has at most %d assertions", maxAssertionsPerCase))
	}
	assertions := make([]caseAssertion, 0, len(raw.Assertions))
	for index, assertion := range raw.Assertions {
		kind := strings.TrimSpace(assertion.Kind)
		switch kind {
		case mindmapdomain.AssertionKindUI, mindmapdomain.AssertionKindAPI, mindmapdomain.AssertionKindDB,
			mindmapdomain.AssertionKindText, mindmapdomain.AssertionKindVisual:
		default:
			return caseProposal{}, invalidArguments(fmt.Sprintf("assertions[%d].kind must be ui, api, db, text or visual", index))
		}
		if assertion.StepIndex != nil && (*assertion.StepIndex < 0 || *assertion.StepIndex >= len(steps)) {
			return caseProposal{}, invalidArguments(fmt.Sprintf("assertions[%d].stepIndex is outside the case steps", index))
		}
		if err := checkRunes("assertions.target", assertion.Target, maxAssertionTargetRunes); err != nil {
			return caseProposal{}, err
		}
		if strings.TrimSpace(assertion.Expression) == "" {
			return caseProposal{}, invalidArguments(
				fmt.Sprintf("assertions[%d].expression must not be empty", index))
		}
		if err := checkRunes("assertions.expression", assertion.Expression, maxAssertionExpressionRune); err != nil {
			return caseProposal{}, err
		}
		if err := checkRunes("assertions.expected", assertion.Expected, maxAssertionExpectedRunes); err != nil {
			return caseProposal{}, err
		}
		assertions = append(assertions, caseAssertion{
			StepIndex:  assertion.StepIndex,
			Kind:       kind,
			Target:     assertion.Target,
			Expression: assertion.Expression,
			Expected:   assertion.Expected,
		})
	}
	requirementIDs, err := normalizeRequirementIDs(raw.RequirementIDs, confirmed)
	if err != nil {
		return caseProposal{}, err
	}
	confidence := strings.TrimSpace(raw.Confidence)
	if confidence == "" {
		confidence = mindmapdomain.ConfidenceMedium
	}
	switch confidence {
	case mindmapdomain.ConfidenceHigh, mindmapdomain.ConfidenceMedium, mindmapdomain.ConfidenceLow:
	default:
		return caseProposal{}, invalidArguments("confidence must be high, medium or low")
	}
	return caseProposal{
		Title:          title,
		Description:    raw.Description,
		Priority:       priority,
		Preconditions:  raw.Preconditions,
		TestData:       testData,
		RequirementIDs: requirementIDs,
		Steps:          steps,
		Assertions:     assertions,
		Confidence:     confidence,
		Rationale:      raw.Rationale,
	}, nil
}

// normalizeTestData validates the optional free-form test data document.
func normalizeTestData(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &object) != nil {
		return nil, invalidArguments("testData must be a JSON object")
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

// checkRunes bounds one free-form string field.
func checkRunes(field, value string, max int) error {
	if utf8.RuneCountInString(value) > max {
		return invalidArguments(fmt.Sprintf("%s must be at most %d characters", field, max))
	}
	return nil
}

// --- argument and result helpers -------------------------------------------

// decodeArguments strictly decodes one tool argument object. Unknown fields are
// rejected because every schema sets additionalProperties=false.
func decodeArguments[T any](args json.RawMessage) (T, error) {
	var value T
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, invalidArguments("invalid arguments: " + err.Error())
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return value, invalidArguments("invalid arguments: unexpected trailing data")
	}
	return value, nil
}

// parsePositiveID parses one positive decimal id (D29).
func parsePositiveID(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 19 {
		return 0, invalidArguments("id must be a positive decimal string")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, invalidArguments("id must be a positive decimal string")
	}
	return id, nil
}

// jsonResult marshals one successful tool result.
func jsonResult(value any) (agentdomain.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return agentdomain.ToolResult{}, internalError("encode the tool result")
	}
	return agentdomain.ToolResult{Content: string(encoded)}, nil
}

// invalidArguments builds the correctable invalid_arguments tool error.
func invalidArguments(message string) error {
	return &agentdomain.ToolError{Code: toolErrorInvalidArguments, Message: message}
}

// runNotFoundError builds the stable run_not_found tool error.
func runNotFoundError(message string) error {
	return &agentdomain.ToolError{Code: toolErrorRunNotFound, Message: message}
}

// internalError builds an internal tool error; internal messages never carry
// business payloads.
func internalError(message string) error {
	return &agentdomain.ToolError{Code: toolErrorInternal, Message: message}
}

// recordToolAudit records mindmap.tool.invoked best-effort (ids/counts only).
func recordToolAudit(ctx context.Context, deps Deps, toolName string, run mindmapdomain.CaseDesignRun, metadata map[string]any) {
	if deps.Audit == nil {
		return
	}
	details := map[string]any{
		"tool":  toolName,
		"runId": run.ID,
	}
	for key, value := range metadata {
		details[key] = value
	}
	actor := int64(0)
	if invocation, ok := agentapp.InvocationFromContext(ctx); ok && invocation.ActorUserID != nil {
		actor = *invocation.ActorUserID
	}
	_ = deps.Audit.Record(ctx, mindmapapp.AuditEvent{
		Action:     actionToolInvoked,
		ActorID:    actor,
		TargetType: "case_design_run",
		TargetID:   strconv.FormatInt(run.ID, 10),
		Metadata:   details,
	})
}

// contextGraphView loads the current graph view of the run version.
func contextGraphView(ctx context.Context, deps Deps, versionID int64) (mindmapapp.GraphView, error) {
	view, err := deps.Graphs.Get(ctx, versionID)
	if err != nil {
		slog.ErrorContext(ctx, "mindmap: supplement tool could not read the current mindmap",
			"version_id", versionID, "error", err)
		return mindmapapp.GraphView{}, internalError("the current mindmap could not be read")
	}
	return view, nil
}

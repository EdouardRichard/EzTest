// Package agent_tool implements the P7 delivery agent tools of design spec §9.3
// (failure analysis) and §9.4 (issue triage).
//
// The tools are thin shells over the delivery application ports: they validate
// the model arguments, resolve the calling analysis run from the P1 tool
// Invocation context (never from a model-supplied analysis run id), enforce the
// batch/version ownership and the documented caps, and submit structured
// proposals through the analysis service, which validates and materializes them
// later. They never write issue rows directly, never re-materialize, and never
// return evidence bytes, prompts or credentials.
//
// The package declares narrow local read ports (case detail, code window and
// evidence metadata) so the composition root can inject the committed execution
// and codeintel adapters without this package importing another context's
// infrastructure.
package agent_tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// Tool error codes returned by the delivery tools. They mirror the P6 execution
// tool vocabulary; the P1 executor maps them to the tool_result status.
const (
	toolErrorInvalidArguments = "invalid_arguments"
	toolErrorNotFound         = "not_found"
	toolErrorInternal         = "internal"
	toolErrorRunNotActive     = "run_not_active"
	toolErrorNotApplicable    = "not_applicable"
	toolErrorConflict         = "conflict"
)

// Argument and output bounds of design spec §9.3/§9.4/§5.6.
const (
	// maxEvidenceMetaIDs bounds one analysis.evidence_meta request.
	maxEvidenceMetaIDs = 100
	// maxCodeReadRefs and maxCodeReadLinesPerRef implement the ≤10 files /
	// ≤80 lines per file bound of spec §9.3.
	maxCodeReadRefs        = 10
	maxCodeReadLinesPerRef = 80
	// maxCodeReadBytes bounds the total content returned by one code read.
	maxCodeReadBytes = 64 << 10
	// maxCodePathBytes bounds one model-supplied repository path.
	maxCodePathBytes = 1024
	// maxCandidatesPerCall and maxDraftsPerCall implement the ≤20 bound of
	// spec §9.3/§9.4.
	maxCandidatesPerCall = domain.AnalysisMaxFailureCandidates
	maxDraftsPerCall     = domain.AnalysisMaxIssueDrafts
	// maxFinishSummaryRunes bounds the optional finish summary.
	maxFinishSummaryRunes = 4000
	// maxContextCaseRunIDs, maxContextEvidenceIDs and maxContextCodeRefs bound
	// one triage.context_read request/response.
	maxContextCaseRunIDs  = 20
	maxContextEvidenceIDs = 100
	maxContextCodeRefs    = 100
)

// AnalysisRunReader is the narrow analysis-run read the tools need to resolve
// their own run from the invocation scope. *application.AnalysisStore (the
// postgres repository) satisfies it.
type AnalysisRunReader interface {
	GetByID(ctx context.Context, analysisID int64) (domain.AnalysisRun, error)
}

// SubmissionService is the analysis-run submission surface. The P5/P6 pattern
// applies: the tool only proposes; *application.AnalysisService validates,
// persists and later materializes the proposals.
type SubmissionService interface {
	ProposeFailureCandidates(ctx context.Context, analysisID int64, candidates []domain.FailureCandidate) (domain.AnalysisRun, []string, error)
	ProposeIssueDrafts(ctx context.Context, analysisID int64, drafts []domain.IssueDraft) (domain.AnalysisRun, []string, error)
}

// CaseDetailStep is one recorded step of a failed case run.
type CaseDetailStep struct {
	Seq           int
	Action        string
	Expected      string
	Actual        string
	Status        string
	EvidenceCount int
}

// CaseDetailAssertion is one recorded assertion of a failed case run.
type CaseDetailAssertion struct {
	AssertionID   int64
	Kind          string
	Target        string
	Expected      string
	Actual        string
	Verdict       string
	FailureReason string
}

// CaseDetail is the read-only detail of one failed(application) case run: its
// classification, recorded steps/assertions and the evidence ids that may be
// cited. It never carries evidence bytes.
type CaseDetail struct {
	CaseRunID     int64
	CaseNodeID    int64
	Title         string
	Status        string
	FailureClass  string
	FailureReason string
	Steps         []CaseDetailStep
	Assertions    []CaseDetailAssertion
	EvidenceIDs   []int64
}

// CaseDetailReader reads one case run detail within the version/batch scope. The
// adapter must return found=false for a run outside that scope.
type CaseDetailReader interface {
	CaseDetail(ctx context.Context, versionID, batchID, caseRunID int64) (CaseDetail, bool, error)
}

// CodeWindow is one commit-pinned code window. Content is the untrusted file
// excerpt; Truncated reports that the file continues past EndLine.
type CodeWindow struct {
	Path      string
	StartLine int
	EndLine   int
	Content   string
	Truncated bool
}

// CodeReader reads bounded commit-pinned code windows of the version's bound
// repository. found=false reports a path outside the repository/commit.
type CodeReader interface {
	ReadCode(ctx context.Context, versionID int64, path string, startLine, endLine int) (CodeWindow, bool, error)
}

// EvidenceMeta is the only artifact information the failure/triage agents may
// read: kind, content hash, size and mime. Never the artifact bytes.
type EvidenceMeta struct {
	EvidenceID int64
	Kind       string
	SHA256     string
	SizeBytes  int64
	Mime       string
}

// EvidenceMetaReader returns metadata for known evidence ids of the batch. The
// tool already filtered the ids against the failed(application) case runs; the
// adapter must not widen that set.
type EvidenceMetaReader interface {
	EvidenceMeta(ctx context.Context, versionID, batchID int64, evidenceIDs []int64) ([]EvidenceMeta, error)
}

// ToolDeps are the ports the eleven tools consume. Every read and the submission
// service are required: NewTools returns nil when one is missing, so a stale
// composition root cannot expose a half-working tool set to a profile.
type ToolDeps struct {
	Runs        AnalysisRunReader
	Cases       deliveryapp.AnalysisExecutionReader
	Submissions SubmissionService
	Details     CaseDetailReader
	Evidence    EvidenceMetaReader
	Code        CodeReader
}

// ready reports whether every required port is wired.
func (d ToolDeps) ready() bool {
	return d.Runs != nil && d.Cases != nil && d.Submissions != nil &&
		d.Details != nil && d.Evidence != nil && d.Code != nil
}

// toolDefinition is one tool's frozen metadata plus its execution function.
type toolDefinition struct {
	name        string
	description string
	schema      string
	run         func(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error)
}

// analysisTool implements agent/domain.Tool over one definition and the injected
// ports.
type analysisTool struct {
	deps ToolDeps
	def  toolDefinition
}

// Name implements domain.Tool.
func (t analysisTool) Name() string { return t.def.name }

// Description implements domain.Tool.
func (t analysisTool) Description() string { return t.def.description }

// InputSchema implements domain.Tool.
func (t analysisTool) InputSchema() json.RawMessage { return json.RawMessage(t.def.schema) }

// Execute implements domain.Tool. The analysis run scope is read from the P1
// invocation context the tool executor injects.
func (t analysisTool) Execute(ctx context.Context, args json.RawMessage) (agentdomain.ToolResult, error) {
	return t.def.run(ctx, t.deps, args)
}

// decodeArguments strictly decodes one tool argument object. Unknown fields are
// rejected because every schema sets additionalProperties=false.
func decodeArguments[T any](args json.RawMessage) (T, error) {
	var value T
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if trimmed[0] != '{' {
		return value, invalidArgumentsf("arguments must be a JSON object with the documented fields")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, invalidArgumentsf("invalid arguments: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return value, invalidArgumentsf("invalid arguments: unexpected trailing data")
	}
	return value, nil
}

// runScope reads and validates the analysis run scope from the P1 invocation
// context. A missing scope is a correctable tool error.
func runScope(ctx context.Context) (agentapp.Invocation, error) {
	invocation, ok := agentapp.InvocationFromContext(ctx)
	if !ok || invocation.SessionID <= 0 || invocation.VersionID <= 0 ||
		invocation.BatchID <= 0 || invocation.AnalysisRunID <= 0 {
		return agentapp.Invocation{}, invalidArgumentsf(
			"this tool requires an analysis run scope (version, batch and analysis run ids)")
	}
	return invocation, nil
}

// scopeRun loads the calling analysis run and validates its kind and
// version/batch ownership. The model can never address another run or version.
func scopeRun(ctx context.Context, deps ToolDeps, kind string) (agentapp.Invocation, domain.AnalysisRun, error) {
	invocation, err := runScope(ctx)
	if err != nil {
		return agentapp.Invocation{}, domain.AnalysisRun{}, err
	}
	run, err := deps.Runs.GetByID(ctx, invocation.AnalysisRunID)
	if err != nil {
		if errors.Is(err, deliveryapp.ErrAnalysisRunNotFound) {
			return agentapp.Invocation{}, domain.AnalysisRun{}, notFoundf("analysis run %d not found", invocation.AnalysisRunID)
		}
		return agentapp.Invocation{}, domain.AnalysisRun{}, internalErrorf("read analysis run %d: %v", invocation.AnalysisRunID, err)
	}
	if run.Kind != kind {
		return agentapp.Invocation{}, domain.AnalysisRun{}, invalidArgumentsf(
			"analysis run %d is %q, not %q", run.ID, run.Kind, kind)
	}
	if run.VersionID != invocation.VersionID || run.BatchID != invocation.BatchID {
		return agentapp.Invocation{}, domain.AnalysisRun{}, invalidArgumentsf(
			"analysis run %d does not belong to the calling version/batch scope", run.ID)
	}
	return invocation, run, nil
}

// scopeRunningRun is scopeRun plus the submission guard: only a running run
// accepts a proposal or an explicit finish.
func scopeRunningRun(ctx context.Context, deps ToolDeps, kind string) (agentapp.Invocation, domain.AnalysisRun, error) {
	invocation, run, err := scopeRun(ctx, deps, kind)
	if err != nil {
		return agentapp.Invocation{}, domain.AnalysisRun{}, err
	}
	if run.Status != domain.AnalysisRunning {
		return agentapp.Invocation{}, domain.AnalysisRun{}, toolErrorf(toolErrorRunNotActive,
			"analysis run %d is %s, not running", run.ID, run.Status)
	}
	return invocation, run, nil
}

// failedApplicationCases reads the batch's failed case runs, keeping only the
// failed(application) entries T4 analyzes (spec §5.6).
func failedApplicationCases(ctx context.Context, deps ToolDeps, run domain.AnalysisRun) ([]deliveryapp.AnalysisFailedCase, error) {
	cases, err := deps.Cases.ListFailedCases(ctx, run.VersionID, run.BatchID, domain.AnalysisMaxFailureCandidates+1)
	if err != nil {
		return nil, internalErrorf("list failed cases of batch %d: %v", run.BatchID, err)
	}
	filtered := make([]deliveryapp.AnalysisFailedCase, 0, len(cases))
	for _, failedCase := range cases {
		if failedCase.FailureClass != domain.FailureClassApplication ||
			failedCase.CaseRunID <= 0 || failedCase.CaseNodeID <= 0 {
			continue
		}
		filtered = append(filtered, failedCase)
	}
	return filtered, nil
}

// caseRunIndex projects the failed cases by case run id.
func caseRunIndex(cases []deliveryapp.AnalysisFailedCase) map[int64]deliveryapp.AnalysisFailedCase {
	index := make(map[int64]deliveryapp.AnalysisFailedCase, len(cases))
	for _, failedCase := range cases {
		index[failedCase.CaseRunID] = failedCase
	}
	return index
}

// allowedEvidenceIDs is the union of the evidence ids of the failed application
// cases (the T4 ownership whitelist, spec §5.6).
func allowedEvidenceIDs(cases []deliveryapp.AnalysisFailedCase) map[int64]struct{} {
	allowed := make(map[int64]struct{})
	for _, failedCase := range cases {
		for _, evidenceID := range failedCase.EvidenceIDs {
			if evidenceID > 0 {
				allowed[evidenceID] = struct{}{}
			}
		}
	}
	return allowed
}

// openIssueIDs projects the open issue summaries of the run input.
func openIssueIDs(issues []domain.OpenIssue) map[int64]struct{} {
	open := make(map[int64]struct{}, len(issues))
	for _, issue := range issues {
		if issue.IssueID > 0 {
			open[issue.IssueID] = struct{}{}
		}
	}
	return open
}

// jsonResult marshals one successful tool result.
func jsonResult(value any) (agentdomain.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return agentdomain.ToolResult{}, internalErrorf("marshal tool result: %v", err)
	}
	return agentdomain.ToolResult{Content: string(encoded)}, nil
}

// invalidArgumentsf builds the correctable invalid_arguments tool error.
func invalidArgumentsf(format string, args ...any) *agentdomain.ToolError {
	return toolErrorf(toolErrorInvalidArguments, format, args...)
}

// notFoundf builds a not_found tool error.
func notFoundf(format string, args ...any) *agentdomain.ToolError {
	return toolErrorf(toolErrorNotFound, format, args...)
}

// internalErrorf builds an internal tool error.
func internalErrorf(format string, args ...any) *agentdomain.ToolError {
	return toolErrorf(toolErrorInternal, format, args...)
}

// toolErrorf builds one typed tool error.
func toolErrorf(code, format string, args ...any) *agentdomain.ToolError {
	return &agentdomain.ToolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// mapAnalysisError translates a delivery analysis error into a tool error.
// Context cancellation is returned unchanged so the P1 executor reports it as
// cancelled/timeout.
func mapAnalysisError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var toolErr *agentdomain.ToolError
	if errors.As(err, &toolErr) {
		return err
	}
	switch domain.AnalysisReason(err) {
	case domain.ReasonAnalysisNotFound:
		return notFoundf("%v", err)
	case domain.ReasonAnalysisNotApplicable:
		return toolErrorf(toolErrorNotApplicable, "%v", err)
	case domain.ReasonAnalysisActive:
		return toolErrorf(toolErrorConflict, "%v", err)
	}
	if errors.Is(err, deliveryapp.ErrAnalysisRunNotFound) {
		return notFoundf("%v", err)
	}
	return internalErrorf("%v", err)
}

// parseID parses a positive decimal id. Every cross-boundary id is a decimal
// string (D29).
func parseID(raw string) (int64, bool) {
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

// parseOptionalID parses an optional decimal id pointer.
func parseOptionalID(raw *string) (int64, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return 0, true
	}
	return parseID(*raw)
}

// idString renders one application-generated id as the tool contract's decimal
// string.
func idString(id int64) string { return strconv.FormatInt(id, 10) }

// idStrings renders ids as decimal strings.
func idStrings(ids []int64) []string {
	rendered := make([]string, 0, len(ids))
	for _, id := range ids {
		rendered = append(rendered, idString(id))
	}
	return rendered
}

// sanitizeCodeRefs validates the model-supplied code references: path, positive
// line and reason are mandatory. Malformed refs are removed with one warning
// each; no file content is read or recorded.
func sanitizeCodeRefs(refs []codeRefInput, label string) ([]domain.CodeRef, []string) {
	if len(refs) == 0 {
		return nil, nil
	}
	valid := make([]domain.CodeRef, 0, len(refs))
	warnings := make([]string, 0)
	for index, ref := range refs {
		path := strings.TrimSpace(ref.Path)
		reason := strings.TrimSpace(ref.Reason)
		if path == "" || len(path) > maxCodePathBytes || ref.Line <= 0 || reason == "" {
			warnings = append(warnings, fmt.Sprintf(
				"%s: code_ref %d needs path, positive line and reason", label, index+1))
			continue
		}
		valid = append(valid, domain.CodeRef{Path: path, Line: ref.Line, Reason: reason})
	}
	return valid, warnings
}

// codeRefInput is the strict JSON shape of one model-supplied code reference.
type codeRefInput struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// evidenceMetaOutput is the analysis.evidence_meta projection.
type evidenceMetaOutput struct {
	Evidence []evidenceMetaValue `json:"evidence"`
	Warnings []string            `json:"warnings"`
}

// evidenceMetaValue is one evidence metadata record. It never carries bytes or
// storage paths.
type evidenceMetaValue struct {
	EvidenceID string `json:"evidenceId"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Mime       string `json:"mime"`
}

// newEvidenceMetaValue projects one metadata record to its JSON form.
func newEvidenceMetaValue(meta EvidenceMeta) evidenceMetaValue {
	return evidenceMetaValue{
		EvidenceID: idString(meta.EvidenceID),
		Kind:       meta.Kind,
		SHA256:     meta.SHA256,
		Size:       meta.SizeBytes,
		Mime:       meta.Mime,
	}
}

// evidenceMetaValues projects evidence metadata to its JSON form.
func evidenceMetaValues(metas []EvidenceMeta) []evidenceMetaValue {
	values := make([]evidenceMetaValue, 0, len(metas))
	for _, meta := range metas {
		values = append(values, newEvidenceMetaValue(meta))
	}
	return values
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

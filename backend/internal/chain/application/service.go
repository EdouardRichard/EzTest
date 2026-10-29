package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Node and result vocabulary the aggregation relies on. The values mirror the
// upstream contexts' persisted enums; the chain context reads them but never
// writes them.
const (
	caseSourceAISupplement      = "ai_supplement"
	caseStatusNeedsRevalidation = "needs_revalidation"
	resultStatusFailed          = "failed"

	searchMaxRunes = 100
)

// resultStatuses is the closed resultStatus query enum of spec §5.1.
var resultStatuses = map[string]struct{}{
	"passed": {}, "failed": {}, "blocked": {}, "inconclusive": {}, "skipped": {}, "interrupted": {},
}

// abnormalResultStatuses is the abnormalOnly subset of spec §5.1.
var abnormalResultStatuses = map[string]struct{}{
	"failed": {}, "blocked": {}, "inconclusive": {}, "interrupted": {},
}

// issueStatuses is the closed issueStatus query enum of spec §5.1.
var issueStatuses = map[string]struct{}{
	"open": {}, "confirmed": {}, "dismissed": {},
}

// issueSeverityRank orders the issue layer (spec §5.1: severity rank, then
// creation time). Unknown severities sort after the known ones.
var issueSeverityRank = map[string]int{
	"blocker": 0, "major": 1, "minor": 2, "info": 3,
}

// issueSeverities is the closed issueSeverity query enum of spec §5.1.
var issueSeverities = map[string]struct{}{
	"blocker": {}, "major": {}, "minor": {}, "info": {},
}

// Query is the exploration-chain read request (spec §5.1).
type Query struct {
	// VersionID is the addressed version as a decimal string (D29).
	VersionID string
	// BatchID is the result/issue batch scope: empty means the latest batch and
	// "all" means every batch of the version.
	BatchID string
	// RequirementID narrows the graph to one requirement group.
	RequirementID string
	// ResultStatus narrows the result layer to one terminal status.
	ResultStatus string
	// IssueStatus and IssueSeverity narrow the issue layer.
	IssueStatus   string
	IssueSeverity string
	// Search matches requirement and case titles (case-insensitive substring,
	// at most 100 runes).
	Search string
	// AbnormalOnly keeps only failed/blocked/inconclusive/interrupted results.
	AbnormalOnly bool
	// IncludeSupplement controls whether AI-supplement cases are returned. The
	// HTTP boundary defaults it to true.
	IncludeSupplement bool
	// RevalidationOnly keeps only cases whose status is needs_revalidation.
	RevalidationOnly bool
	// Limit is the total node budget and RequirementLimit the requirement-root
	// budget.
	Limit            int
	RequirementLimit int
}

// BatchDTO is the resolved batch context of the response.
type BatchDTO struct {
	ID      string `json:"id"`
	BatchNo int    `json:"batchNo"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
}

// NodeDTO is one exploration-chain node. Fields are kind-specific: a node only
// renders the attributes of its layer. Every id is a decimal string (D29).
type NodeDTO struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Status string `json:"status"`

	// Requirement layer.
	Priority          *string `json:"priority,omitempty"`
	Uncovered         *bool   `json:"uncovered,omitempty"`
	CaseCount         *int    `json:"caseCount,omitempty"`
	ExecutedCount     *int    `json:"executedCount,omitempty"`
	FailedCount       *int    `json:"failedCount,omitempty"`
	OpenQuestionCount *int    `json:"openQuestionCount,omitempty"`

	// Case layer.
	Source         *string   `json:"source,omitempty"`
	Revalidation   *bool     `json:"revalidation,omitempty"`
	RequirementIDs *[]string `json:"requirementIds,omitempty"`
	StepCount      *int      `json:"stepCount,omitempty"`
	AssertionCount *int      `json:"assertionCount,omitempty"`
	Impacted       *bool     `json:"impacted,omitempty"`
	ImpactReason   *string   `json:"impactReason,omitempty"`

	// Result layer.
	CaseRunID     *string `json:"caseRunId,omitempty"`
	CaseNodeID    *string `json:"caseNodeId,omitempty"`
	Attempt       *int    `json:"attempt,omitempty"`
	DurationMs    *int64  `json:"durationMs,omitempty"`
	FailureClass  *string `json:"failureClass,omitempty"`
	EvidenceCount *int    `json:"evidenceCount,omitempty"`
	BatchID       *string `json:"batchId,omitempty"`
	Summary       *string `json:"summary,omitempty"`

	// Issue layer.
	Severity     *string `json:"severity,omitempty"`
	Category     *string `json:"category,omitempty"`
	Confidence   *string `json:"confidence,omitempty"`
	CodeRefCount *int    `json:"codeRefCount,omitempty"`
}

// EdgeDTO is one exploration-chain edge. IDs follow
// "<kind>:<sourceDomainId>:<targetDomainId>".
type EdgeDTO struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

// CountsDTO carries the real, pre-truncation totals of the filtered graph
// (spec §5.2: truncation never loses the counts).
type CountsDTO struct {
	Requirements          int            `json:"requirements"`
	Cases                 int            `json:"cases"`
	Results               int            `json:"results"`
	Issues                int            `json:"issues"`
	ByResultStatus        map[string]int `json:"byResultStatus"`
	ByIssueSeverity       map[string]int `json:"byIssueSeverity"`
	UncoveredRequirements int            `json:"uncoveredRequirements"`
	RevalidationCases     int            `json:"revalidationCases"`
	SupplementCases       int            `json:"supplementCases"`
}

// TruncatedDTO reports which layers were cut by the node budget.
type TruncatedDTO struct {
	Requirements bool `json:"requirements"`
	Cases        bool `json:"cases"`
	Results      bool `json:"results"`
	Issues       bool `json:"issues"`
}

// Snapshot is the exploration-chain read model (spec §5.1).
type Snapshot struct {
	VersionID   string       `json:"versionId"`
	Batch       BatchDTO     `json:"batch"`
	Nodes       []NodeDTO    `json:"nodes"`
	Edges       []EdgeDTO    `json:"edges"`
	Counts      CountsDTO    `json:"counts"`
	Truncated   TruncatedDTO `json:"truncated"`
	GeneratedAt time.Time    `json:"generatedAt"`
}

// Service aggregates the provider snapshots into one read model. It holds no
// state: every Build call is independent and performs no cross-request caching
// (spec §5.1).
type Service struct {
	providers Providers
}

// NewService builds the aggregation service from the five layer providers.
func NewService(providers Providers) *Service {
	return &Service{providers: providers}
}

// Build assembles the exploration chain of one version. It returns
// ErrInvalidArgument for a malformed query and ErrNotFound when the version (or
// an explicit batch) does not exist or does not belong to the version.
func (s *Service) Build(ctx context.Context, q Query) (Snapshot, error) {
	normalized, err := normalizeQuery(q)
	if err != nil {
		return Snapshot{}, err
	}
	if s == nil {
		return Snapshot{}, errors.New("chain: service is not configured")
	}
	providers := s.providers
	if providers.Version == nil {
		return Snapshot{}, errors.New("chain: version provider is not configured")
	}
	if providers.Mindmap == nil {
		return Snapshot{}, errors.New("chain: mindmap provider is not configured")
	}
	if providers.Execution == nil {
		return Snapshot{}, errors.New("chain: execution provider is not configured")
	}

	batch, err := providers.Version.GetBatch(ctx, normalized.versionID, normalized.batchID)
	if err != nil {
		return Snapshot{}, providerError("resolve batch", err)
	}
	if normalized.batchID != nil && batch.ID == 0 {
		return Snapshot{}, &ErrNotFound{Resource: "batch", ID: *normalized.batchID}
	}

	requirements, err := providers.Version.Requirements(ctx, normalized.versionID)
	if err != nil {
		return Snapshot{}, providerError("load requirements", err)
	}
	cases, err := providers.Mindmap.Cases(ctx, normalized.versionID)
	if err != nil {
		return Snapshot{}, providerError("load cases", err)
	}
	coverage, err := providers.Mindmap.Coverage(ctx, normalized.versionID)
	if err != nil {
		return Snapshot{}, providerError("load coverage", err)
	}

	var scopeBatchID *int64
	if normalized.BatchID != "all" && batch.ID != 0 {
		id := batch.ID
		scopeBatchID = &id
	}
	results, err := providers.Execution.LatestResults(ctx, normalized.versionID, scopeBatchID)
	if err != nil {
		return Snapshot{}, providerError("load results", err)
	}
	var issues []IssueRow
	if providers.Delivery != nil {
		issues, err = providers.Delivery.Issues(ctx, normalized.versionID, scopeBatchID)
		if err != nil {
			return Snapshot{}, providerError("load issues", err)
		}
	}
	var impact []ImpactRow
	if providers.CodeIntel != nil {
		impact, err = providers.CodeIntel.Impact(ctx, normalized.versionID)
		if err != nil {
			return Snapshot{}, providerError("load impact", err)
		}
	}

	return assemble(buildInput{
		query:        normalized,
		batch:        batch,
		requirements: requirements,
		cases:        cases,
		coverage:     coverage,
		results:      results,
		issues:       issues,
		impact:       impact,
	}), nil
}

// ---------------------------------------------------------------------------
// Query validation
// ---------------------------------------------------------------------------

// normalizedQuery is the validated Query plus its parsed forms.
type normalizedQuery struct {
	Query
	versionID int64
	// batchID is the requested batch when the query names one; nil means "the
	// latest batch" (also used for "all", which displays the latest batch while
	// scoping the result/issue layers to every batch).
	batchID *int64
}

func normalizeQuery(q Query) (normalizedQuery, error) {
	normalized := normalizedQuery{Query: q}

	versionRaw := strings.TrimSpace(q.VersionID)
	versionID, err := parsePositiveID(versionRaw)
	if err != nil {
		return normalized, invalidArgument("versionId", 1, 0,
			"versionId must be a positive decimal id")
	}
	normalized.versionID = versionID
	normalized.VersionID = strconv.FormatInt(versionID, 10)

	batchRaw := strings.TrimSpace(q.BatchID)
	switch {
	case batchRaw == "":
	case strings.EqualFold(batchRaw, "all"):
		normalized.BatchID = "all"
	default:
		batchID, err := parsePositiveID(batchRaw)
		if err != nil {
			return normalized, invalidArgument("batchId", 1, 0,
				"batchId must be a positive decimal id or all")
		}
		normalized.batchID = &batchID
		normalized.BatchID = strconv.FormatInt(batchID, 10)
	}

	requirementRaw := strings.TrimSpace(q.RequirementID)
	if requirementRaw != "" {
		requirementID, err := parsePositiveID(requirementRaw)
		if err != nil {
			return normalized, invalidArgument("requirementId", 1, 0,
				"requirementId must be a positive decimal id")
		}
		normalized.RequirementID = strconv.FormatInt(requirementID, 10)
	} else {
		normalized.RequirementID = ""
	}

	normalized.ResultStatus = strings.TrimSpace(q.ResultStatus)
	if normalized.ResultStatus != "" {
		if _, ok := resultStatuses[normalized.ResultStatus]; !ok {
			return normalized, invalidArgument("resultStatus", 0, 0,
				"resultStatus must be one of passed, failed, blocked, inconclusive, skipped, interrupted")
		}
	}
	normalized.IssueStatus = strings.TrimSpace(q.IssueStatus)
	if normalized.IssueStatus != "" {
		if _, ok := issueStatuses[normalized.IssueStatus]; !ok {
			return normalized, invalidArgument("issueStatus", 0, 0,
				"issueStatus must be one of open, confirmed, dismissed")
		}
	}
	normalized.IssueSeverity = strings.TrimSpace(q.IssueSeverity)
	if normalized.IssueSeverity != "" {
		if _, ok := issueSeverities[normalized.IssueSeverity]; !ok {
			return normalized, invalidArgument("issueSeverity", 0, 0,
				"issueSeverity must be one of blocker, major, minor, info")
		}
	}

	normalized.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(normalized.Search) > searchMaxRunes {
		return normalized, invalidArgument("q", 0, searchMaxRunes,
			"q must be at most 100 characters")
	}

	if normalized.Limit <= 0 {
		return normalized, invalidArgument("limit", 1, 0,
			"limit must be a positive integer")
	}
	if normalized.RequirementLimit <= 0 {
		return normalized, invalidArgument("requirementLimit", 1, 0,
			"requirementLimit must be a positive integer")
	}
	return normalized, nil
}

func parsePositiveID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("not a positive decimal id")
	}
	return id, nil
}

func invalidArgument(field string, min, max int, message string) *ErrInvalidArgument {
	return &ErrInvalidArgument{Field: field, Reason: ReasonInvalidArgument, Min: min, Max: max, Message: message}
}

// providerError surfaces a provider's ErrNotFound unchanged (it carries the 404
// resource) and wraps every other failure for the 500 path.
func providerError(operation string, err error) error {
	var notFound *ErrNotFound
	if errors.As(err, &notFound) {
		return notFound
	}
	return fmt.Errorf("chain: %s: %w", operation, err)
}

// ---------------------------------------------------------------------------
// Aggregation
// ---------------------------------------------------------------------------

type buildInput struct {
	query        normalizedQuery
	batch        BatchRow
	requirements []RequirementRow
	cases        []CaseRow
	coverage     []CoverageCell
	results      []ResultRow
	issues       []IssueRow
	impact       []ImpactRow
}

func assemble(input buildInput) Snapshot {
	q := input.query

	caseByID := make(map[int64]CaseRow, len(input.cases))
	for _, row := range input.cases {
		caseByID[row.ID] = row
	}

	// requirement → case links are the union of case_source requirement refs and
	// the mindmap coverage matrix (spec §5.1).
	caseLinks := make(map[int64][]int64, len(input.cases))
	for _, row := range input.cases {
		caseLinks[row.ID] = append(caseLinks[row.ID], row.RequirementIDs...)
	}
	for _, cell := range input.coverage {
		for _, caseID := range cell.CaseIDs {
			if _, ok := caseByID[caseID]; !ok {
				continue
			}
			caseLinks[caseID] = append(caseLinks[caseID], cell.RequirementID)
		}
	}
	for caseID, ids := range caseLinks {
		caseLinks[caseID] = sortedUniqueIDs(ids)
	}
	casesByRequirement := make(map[int64][]int64, len(input.requirements))
	for _, row := range input.cases {
		for _, requirementID := range caseLinks[row.ID] {
			casesByRequirement[requirementID] = append(casesByRequirement[requirementID], row.ID)
		}
	}

	latestByCase := latestResultsByCase(input.results, caseByID)
	latestByRun := make(map[int64]ResultRow, len(latestByCase))
	for _, result := range latestByCase {
		latestByRun[result.ID] = result
	}

	impactByRequirement := make(map[int64]ImpactRow, len(input.impact))
	for _, candidate := range input.impact {
		if candidate.SubjectKind != NodeKindRequirement {
			// impact_case edges always start at a requirement node, so app-model
			// candidates cannot be represented in the graph (spec §5.1).
			continue
		}
		if !candidate.Selected && candidate.Confidence != "high" && candidate.Confidence != "medium" {
			continue
		}
		if _, exists := impactByRequirement[candidate.SubjectID]; exists {
			continue
		}
		impactByRequirement[candidate.SubjectID] = candidate
	}

	requirementFilterID, _ := parsePositiveID(q.RequirementID) // zero when unfiltered

	// Layer 0: requirements.
	requirements := make([]RequirementRow, 0, len(input.requirements))
	for _, row := range input.requirements {
		if requirementFilterID != 0 && row.ID != requirementFilterID {
			continue
		}
		requirements = append(requirements, row)
	}

	// Layer 1: cases after the structural filters.
	cases := make([]CaseRow, 0, len(input.cases))
	for _, row := range input.cases {
		if requirementFilterID != 0 && !containsID(caseLinks[row.ID], requirementFilterID) {
			continue
		}
		if !q.IncludeSupplement && row.Source == caseSourceAISupplement {
			continue
		}
		if q.RevalidationOnly && row.Status != caseStatusNeedsRevalidation {
			continue
		}
		cases = append(cases, row)
	}

	// Search keeps the requirements whose title matches and the cases whose own
	// title matches or whose requirements match; connected nodes stay in the
	// graph so no edge points at a filtered-out node.
	search := strings.ToLower(q.Search)
	if search != "" {
		matchingRequirements := make(map[int64]struct{}, len(requirements))
		for _, row := range requirements {
			if titleMatches(row.Title, search) {
				matchingRequirements[row.ID] = struct{}{}
			}
		}
		keptCases := make([]CaseRow, 0, len(cases))
		keptCaseIDs := make(map[int64]struct{}, len(cases))
		for _, row := range cases {
			if titleMatches(row.Title, search) || intersectsIDs(caseLinks[row.ID], matchingRequirements) {
				keptCases = append(keptCases, row)
				keptCaseIDs[row.ID] = struct{}{}
			}
		}
		keptRequirements := make([]RequirementRow, 0, len(requirements))
		for _, row := range requirements {
			if _, ok := matchingRequirements[row.ID]; ok {
				keptRequirements = append(keptRequirements, row)
				continue
			}
			for _, caseID := range casesByRequirement[row.ID] {
				if _, ok := keptCaseIDs[caseID]; ok {
					keptRequirements = append(keptRequirements, row)
					break
				}
			}
		}
		requirements, cases = keptRequirements, keptCases
	}

	// Result filter: a status filter keeps only cases whose latest attempt
	// matches; the requirement layer follows those cases unless a single
	// requirement was explicitly selected (which stays visible even when empty).
	finalRequirements := requirements
	finalCases := cases
	resultsByCase := make(map[int64]ResultRow, len(cases))
	resultFilterActive := q.ResultStatus != "" || q.AbnormalOnly
	if resultFilterActive {
		finalCases = make([]CaseRow, 0, len(cases))
		for _, row := range cases {
			result, ok := latestByCase[row.ID]
			if !ok || !resultMatches(result, q) {
				continue
			}
			finalCases = append(finalCases, row)
			resultsByCase[row.ID] = result
		}
		if requirementFilterID == 0 {
			caseSet := make(map[int64]struct{}, len(finalCases))
			for _, row := range finalCases {
				caseSet[row.ID] = struct{}{}
			}
			kept := make([]RequirementRow, 0, len(requirements))
			for _, row := range requirements {
				for _, caseID := range casesByRequirement[row.ID] {
					if _, ok := caseSet[caseID]; ok {
						kept = append(kept, row)
						break
					}
				}
			}
			finalRequirements = kept
		}
	} else {
		for _, row := range cases {
			if result, ok := latestByCase[row.ID]; ok {
				resultsByCase[row.ID] = result
			}
		}
	}

	// Issue layer: status/severity filters, then the issue follows its case when
	// any case-layer filter is active. Unlinked issues always stay visible (the
	// drawer lists them, spec §5.1).
	issues := make([]IssueRow, 0, len(input.issues))
	for _, issue := range input.issues {
		if q.IssueStatus != "" && issue.Status != q.IssueStatus {
			continue
		}
		if q.IssueSeverity != "" && issue.Severity != q.IssueSeverity {
			continue
		}
		issues = append(issues, issue)
	}
	caseScopeFiltered := requirementFilterID != 0 || search != "" || resultFilterActive ||
		!q.IncludeSupplement || q.RevalidationOnly
	if caseScopeFiltered {
		keptCaseIDs := make(map[int64]struct{}, len(finalCases))
		for _, row := range finalCases {
			keptCaseIDs[row.ID] = struct{}{}
		}
		kept := make([]IssueRow, 0, len(issues))
		for _, issue := range issues {
			if caseID, linked := issueCase(issue, latestByRun); linked {
				if _, ok := keptCaseIDs[caseID]; !ok {
					continue
				}
			}
			kept = append(kept, issue)
		}
		issues = kept
	}

	// Counts always describe the filtered graph before truncation (spec §5.2).
	counts := CountsDTO{
		Requirements:    len(finalRequirements),
		Cases:           len(finalCases),
		Results:         len(resultsByCase),
		Issues:          len(issues),
		ByResultStatus:  emptyResultStatusCounts(),
		ByIssueSeverity: emptyIssueSeverityCounts(),
	}
	for _, result := range resultsByCase {
		counts.ByResultStatus[result.Status]++
	}
	for _, issue := range issues {
		counts.ByIssueSeverity[issue.Severity]++
	}
	for _, row := range finalRequirements {
		if len(casesByRequirement[row.ID]) == 0 {
			counts.UncoveredRequirements++
		}
	}
	for _, row := range finalCases {
		if row.Status == caseStatusNeedsRevalidation {
			counts.RevalidationCases++
		}
		if row.Source == caseSourceAISupplement {
			counts.SupplementCases++
		}
	}

	// Stable per-layer ordering (spec §5.1).
	sortedRequirements := sortRequirements(finalRequirements)
	sortedCases := sortCases(finalCases)
	sortedResults := make([]ResultRow, 0, len(resultsByCase))
	for _, result := range resultsByCase {
		sortedResults = append(sortedResults, result)
	}
	sort.SliceStable(sortedResults, func(left, right int) bool {
		if sortedResults[left].Attempt != sortedResults[right].Attempt {
			return sortedResults[left].Attempt > sortedResults[right].Attempt
		}
		return sortedResults[left].ID < sortedResults[right].ID
	})
	sortedIssues := append([]IssueRow(nil), issues...)
	sort.SliceStable(sortedIssues, func(left, right int) bool {
		leftRank := severityRank(sortedIssues[left].Severity)
		rightRank := severityRank(sortedIssues[right].Severity)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if !sortedIssues[left].CreatedAt.Equal(sortedIssues[right].CreatedAt) {
			return sortedIssues[left].CreatedAt.After(sortedIssues[right].CreatedAt)
		}
		return sortedIssues[left].ID < sortedIssues[right].ID
	})

	// Truncation: requirements first (bounded by RequirementLimit and the global
	// budget), then cases, results and issues consume the remaining budget. The
	// counts above are already final, so truncation never changes them.
	truncated := TruncatedDTO{}
	requirementCap := q.RequirementLimit
	if requirementCap > q.Limit {
		requirementCap = q.Limit
	}
	keptRequirements := sortedRequirements
	if len(keptRequirements) > requirementCap {
		keptRequirements = keptRequirements[:requirementCap]
		truncated.Requirements = true
	}
	remaining := q.Limit - len(keptRequirements)
	if remaining < 0 {
		remaining = 0
	}
	keptCases := sortedCases
	if len(keptCases) > remaining {
		keptCases = keptCases[:remaining]
		truncated.Cases = true
	}
	remaining -= len(keptCases)

	keptCaseSet := make(map[int64]struct{}, len(keptCases))
	for _, row := range keptCases {
		keptCaseSet[row.ID] = struct{}{}
	}
	eligibleResults := make([]ResultRow, 0, len(sortedResults))
	for _, result := range sortedResults {
		if _, ok := keptCaseSet[result.CaseNodeID]; ok {
			eligibleResults = append(eligibleResults, result)
		}
	}
	keptResults := eligibleResults
	if len(keptResults) > remaining {
		keptResults = keptResults[:remaining]
		truncated.Results = true
	}
	remaining -= len(keptResults)

	keptIssues := sortedIssues
	if len(keptIssues) > remaining {
		keptIssues = keptIssues[:remaining]
		truncated.Issues = true
	}

	keptRequirementSet := make(map[int64]struct{}, len(keptRequirements))
	for _, row := range keptRequirements {
		keptRequirementSet[row.ID] = struct{}{}
	}
	keptResultByCase := make(map[int64]ResultRow, len(keptResults))
	keptResultByRun := make(map[int64]ResultRow, len(keptResults))
	for _, result := range keptResults {
		keptResultByCase[result.CaseNodeID] = result
		keptResultByRun[result.ID] = result
	}

	// Requirement aggregates describe the filtered (pre-truncation) children so
	// a truncated/large graph still explains its own totals.
	finalCasesByRequirement := make(map[int64][]int64, len(finalRequirements))
	for _, row := range finalCases {
		for _, requirementID := range caseLinks[row.ID] {
			finalCasesByRequirement[requirementID] = append(finalCasesByRequirement[requirementID], row.ID)
		}
	}

	nodes := make([]NodeDTO, 0,
		len(keptRequirements)+len(keptCases)+len(keptResults)+len(keptIssues))
	for _, row := range keptRequirements {
		linkedCases := finalCasesByRequirement[row.ID]
		executedCount := 0
		failedCount := 0
		for _, caseID := range linkedCases {
			result, ok := resultsByCase[caseID]
			if !ok {
				continue
			}
			executedCount++
			if result.Status == resultStatusFailed {
				failedCount++
			}
		}
		nodes = append(nodes, newRequirementNode(row, len(casesByRequirement[row.ID]) == 0,
			len(linkedCases), executedCount, failedCount))
	}

	for _, row := range keptCases {
		impacted := false
		impactReason := ""
		for _, requirementID := range caseLinks[row.ID] {
			candidate, ok := impactByRequirement[requirementID]
			if !ok {
				continue
			}
			impacted = true
			if impactReason == "" && candidate.Reason != "" {
				impactReason = candidate.Reason
			}
		}
		nodes = append(nodes, newCaseNode(row, caseLinks[row.ID], impacted, impactReason))
	}

	for _, result := range keptResults {
		title := ""
		if row, ok := caseByID[result.CaseNodeID]; ok {
			title = row.Title
		}
		nodes = append(nodes, newResultNode(result, title))
	}

	for _, issue := range keptIssues {
		nodes = append(nodes, newIssueNode(issue))
	}

	// Edges are emitted only between kept nodes, in the layer order of spec §5.1.
	// The edge id uses the domain ids ("<kind>:<sourceId>:<targetId>"); source
	// and target carry the node ids.
	edges := make([]EdgeDTO, 0)
	seenEdges := make(map[string]struct{})
	appendEdge := func(kind string, sourceKind string, sourceID int64, targetKind string, targetID int64) {
		id := kind + ":" + formatID(sourceID) + ":" + formatID(targetID)
		if _, ok := seenEdges[id]; ok {
			return
		}
		seenEdges[id] = struct{}{}
		edges = append(edges, EdgeDTO{
			ID:     id,
			Source: nodeID(sourceKind, sourceID),
			Target: nodeID(targetKind, targetID),
			Kind:   kind,
		})
	}
	for _, row := range keptCases {
		for _, requirementID := range caseLinks[row.ID] {
			if _, ok := keptRequirementSet[requirementID]; !ok {
				continue
			}
			appendEdge(EdgeKindRequirementCase, NodeKindRequirement, requirementID, NodeKindCase, row.ID)
		}
	}
	for _, row := range keptCases {
		result, ok := keptResultByCase[row.ID]
		if !ok {
			continue
		}
		appendEdge(EdgeKindCaseResult, NodeKindCase, row.ID, NodeKindResult, result.ID)
	}
	for _, issue := range keptIssues {
		if issue.CaseRunID != nil {
			if result, ok := keptResultByRun[*issue.CaseRunID]; ok {
				appendEdge(EdgeKindResultIssue, NodeKindResult, result.ID, NodeKindIssue, issue.ID)
				continue
			}
		}
		caseID, linked := issueCase(issue, latestByRun)
		if !linked {
			continue
		}
		if _, ok := keptCaseSet[caseID]; !ok {
			continue
		}
		appendEdge(EdgeKindCaseIssue, NodeKindCase, caseID, NodeKindIssue, issue.ID)
	}
	for _, row := range keptCases {
		for _, requirementID := range caseLinks[row.ID] {
			if _, ok := keptRequirementSet[requirementID]; !ok {
				continue
			}
			if _, ok := impactByRequirement[requirementID]; !ok {
				continue
			}
			appendEdge(EdgeKindImpactCase, NodeKindRequirement, requirementID, NodeKindCase, row.ID)
		}
	}

	return Snapshot{
		VersionID: q.VersionID,
		Batch: BatchDTO{
			ID:      optionalIDString(input.batch.ID),
			BatchNo: input.batch.BatchNo,
			Kind:    input.batch.Kind,
			Status:  input.batch.Status,
		},
		Nodes:       nodes,
		Edges:       edges,
		Counts:      counts,
		Truncated:   truncated,
		GeneratedAt: time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// Layer helpers
// ---------------------------------------------------------------------------

// latestResultsByCase keeps the newest attempt per case (attempt, then id) and
// drops rows for case nodes outside the fetched case layer.
func latestResultsByCase(results []ResultRow, caseByID map[int64]CaseRow) map[int64]ResultRow {
	latest := make(map[int64]ResultRow, len(results))
	for _, result := range results {
		if result.CaseNodeID == 0 {
			continue
		}
		if _, ok := caseByID[result.CaseNodeID]; !ok {
			continue
		}
		current, ok := latest[result.CaseNodeID]
		if !ok || result.Attempt > current.Attempt ||
			(result.Attempt == current.Attempt && result.ID > current.ID) {
			latest[result.CaseNodeID] = result
		}
	}
	return latest
}

// issueCase resolves the case an issue hangs off: a caseRunId hitting the latest
// result wins, otherwise an explicit case node (spec §5.1).
func issueCase(issue IssueRow, latestByRun map[int64]ResultRow) (int64, bool) {
	if issue.CaseRunID != nil {
		if result, ok := latestByRun[*issue.CaseRunID]; ok {
			return result.CaseNodeID, true
		}
	}
	if issue.CaseNodeID != nil {
		return *issue.CaseNodeID, true
	}
	return 0, false
}

func resultMatches(result ResultRow, q normalizedQuery) bool {
	if q.ResultStatus != "" && result.Status != q.ResultStatus {
		return false
	}
	if q.AbnormalOnly {
		if _, ok := abnormalResultStatuses[result.Status]; !ok {
			return false
		}
	}
	return true
}

func sortRequirements(rows []RequirementRow) []RequirementRow {
	sorted := append([]RequirementRow(nil), rows...)
	sort.SliceStable(sorted, func(left, right int) bool {
		if sorted[left].SortOrder != sorted[right].SortOrder {
			return sorted[left].SortOrder < sorted[right].SortOrder
		}
		return sorted[left].ID < sorted[right].ID
	})
	return sorted
}

func sortCases(rows []CaseRow) []CaseRow {
	sorted := append([]CaseRow(nil), rows...)
	sort.SliceStable(sorted, func(left, right int) bool {
		if sorted[left].SortOrder != sorted[right].SortOrder {
			return sorted[left].SortOrder < sorted[right].SortOrder
		}
		return sorted[left].ID < sorted[right].ID
	})
	return sorted
}

func severityRank(severity string) int {
	if rank, ok := issueSeverityRank[severity]; ok {
		return rank
	}
	return len(issueSeverityRank)
}

func emptyResultStatusCounts() map[string]int {
	return map[string]int{
		"pending": 0, "running": 0, "passed": 0, "failed": 0,
		"blocked": 0, "inconclusive": 0, "skipped": 0, "interrupted": 0,
	}
}

func emptyIssueSeverityCounts() map[string]int {
	return map[string]int{"blocker": 0, "major": 0, "minor": 0, "info": 0}
}

func sortedUniqueIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	unique := sorted[:1]
	for _, id := range sorted[1:] {
		if id != unique[len(unique)-1] {
			unique = append(unique, id)
		}
	}
	return unique
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func intersectsIDs(ids []int64, set map[int64]struct{}) bool {
	for _, id := range ids {
		if _, ok := set[id]; ok {
			return true
		}
	}
	return false
}

func titleMatches(title, lowerSearch string) bool {
	return strings.Contains(strings.ToLower(title), lowerSearch)
}

func nodeID(kind string, id int64) string {
	return kind + ":" + formatID(id)
}

func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}

func optionalIDString(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// ---------------------------------------------------------------------------
// Node constructors
// ---------------------------------------------------------------------------

func newRequirementNode(row RequirementRow, uncovered bool, caseCount, executedCount, failedCount int) NodeDTO {
	return NodeDTO{
		ID:                nodeID(NodeKindRequirement, row.ID),
		Kind:              NodeKindRequirement,
		Title:             row.Title,
		Status:            row.Status,
		Priority:          stringPointer(row.Priority),
		Uncovered:         boolPointer(uncovered),
		CaseCount:         intPointer(caseCount),
		ExecutedCount:     intPointer(executedCount),
		FailedCount:       intPointer(failedCount),
		OpenQuestionCount: intPointer(row.OpenQuestionCount),
	}
}

func newCaseNode(row CaseRow, requirementIDs []int64, impacted bool, impactReason string) NodeDTO {
	ids := make([]string, 0, len(requirementIDs))
	for _, requirementID := range requirementIDs {
		ids = append(ids, formatID(requirementID))
	}
	return NodeDTO{
		ID:             nodeID(NodeKindCase, row.ID),
		Kind:           NodeKindCase,
		Title:          row.Title,
		Status:         row.Status,
		Source:         stringPointer(row.Source),
		Revalidation:   boolPointer(row.Status == caseStatusNeedsRevalidation),
		Priority:       stringPointer(row.Priority),
		RequirementIDs: &ids,
		StepCount:      intPointer(row.StepCount),
		AssertionCount: intPointer(row.AssertionCount),
		Impacted:       boolPointer(impacted),
		ImpactReason:   stringPointer(impactReason),
	}
}

func newResultNode(row ResultRow, title string) NodeDTO {
	return NodeDTO{
		ID:            nodeID(NodeKindResult, row.ID),
		Kind:          NodeKindResult,
		Title:         title,
		Status:        row.Status,
		CaseRunID:     stringPointer(formatID(row.ID)),
		CaseNodeID:    stringPointer(formatID(row.CaseNodeID)),
		Attempt:       intPointer(row.Attempt),
		DurationMs:    int64Pointer(row.DurationMs),
		FailureClass:  stringPointer(row.FailureClass),
		EvidenceCount: intPointer(row.EvidenceCount),
		BatchID:       stringPointer(optionalIDString(row.BatchID)),
		Summary:       stringPointer(row.Summary),
	}
}

func newIssueNode(row IssueRow) NodeDTO {
	node := NodeDTO{
		ID:            nodeID(NodeKindIssue, row.ID),
		Kind:          NodeKindIssue,
		Title:         row.Title,
		Status:        row.Status,
		Severity:      stringPointer(row.Severity),
		Category:      stringPointer(row.Category),
		Confidence:    stringPointer(row.Confidence),
		EvidenceCount: intPointer(row.EvidenceCount),
		CodeRefCount:  intPointer(row.CodeRefCount),
	}
	if row.CaseRunID != nil {
		node.CaseRunID = stringPointer(formatID(*row.CaseRunID))
	}
	if row.CaseNodeID != nil {
		node.CaseNodeID = stringPointer(formatID(*row.CaseNodeID))
	}
	return node
}

func stringPointer(value string) *string {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}

func intPointer(value int) *int {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}

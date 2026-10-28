package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// IncrementalPlanView is GET /mindmap/incremental-plan (spec §7.6): the latest
// incremental_design run of a continuing version together with the materialized
// new cases, the affected-case proposals, the unmapped P4 changes, the
// deterministic default selection and the warnings. Ids stay int64; the HTTP DTO
// stringifies them (D29).
type IncrementalPlanView struct {
	VersionID       int64
	Phase           string
	ParentBatchID   *int64
	Run             *IncrementalPlanRun
	NewCases        []IncrementalPlanNewCase
	AffectedCases   []IncrementalPlanAffectedCase
	UnmappedChanges []IncrementalPlanChange
	DefaultCaseIDs  []int64
	Warnings        []IncrementalPlanWarning
	Degraded        bool
}

// IncrementalPlanRun is the run summary of the plan.
type IncrementalPlanRun struct {
	ID     int64
	Kind   string
	Status string
	Result json.RawMessage
}

// IncrementalPlanNewCase is one materialized ai_supplement case of the run.
type IncrementalPlanNewCase struct {
	NodeID         int64
	Title          string
	Priority       string
	Confidence     string
	RequirementIDs []int64
	Status         string
}

// IncrementalPlanAffectedCase is one affected existing case. Selected is true for
// high/medium confidence (spec §7.6 defaultCaseIds).
type IncrementalPlanAffectedCase struct {
	NodeID     int64
	Title      string
	Confidence string
	Reason     string
	Source     string
	Selected   bool
}

// IncrementalPlanChange is one P4 unmapped change.
type IncrementalPlanChange struct {
	Path    string
	Summary string
}

// IncrementalPlanWarning is one stable plan/run warning ({code, message}).
type IncrementalPlanWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// plan warning codes (spec §7.6 and the degraded matrix of §9.7).
const (
	planWarningImpactUnavailable = "impact_unavailable"
	planWarningInvalidResult     = "invalid_result"
)

// IncrementalPlan returns the incremental design plan of a version (spec §7.6).
// With no incremental_design run it returns an empty plan, not an error. The run
// is read through the graph transaction's run repository so the application port
// set stays unchanged.
func (s *GraphService) IncrementalPlan(ctx context.Context, versionID int64) (IncrementalPlanView, error) {
	if s.deps.Versions == nil || s.deps.Graph == nil || s.deps.Nodes == nil ||
		s.deps.Steps == nil || s.deps.Assertions == nil || s.deps.Sources == nil {
		return IncrementalPlanView{}, errors.New("mindmap: incremental plan ports are not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return IncrementalPlanView{}, err
	}
	view := IncrementalPlanView{
		VersionID: versionID,
		Phase:     planContinuePhase(ctx, s.deps.Versions, version),
	}

	var (
		run   domain.CaseDesignRun
		found bool
	)
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if tx.Runs() == nil {
			return errors.New("mindmap: graph transaction run repository is not configured")
		}
		latest, err := tx.Runs().LatestByKind(ctx, versionID, RunKindIncrementalDesign)
		if err != nil {
			if runIsNotFound(err) {
				return nil
			}
			return err
		}
		run = latest
		found = true
		return nil
	})
	if err != nil {
		return IncrementalPlanView{}, err
	}
	if !found {
		return view, nil
	}

	view.Run = &IncrementalPlanRun{
		ID:     run.ID,
		Kind:   run.Kind,
		Status: run.Status,
		Result: append(json.RawMessage(nil), run.Result...),
	}
	input := runParseInput(run.Input)
	if batchID := input.BatchID.Int64(); batchID > 0 {
		id := batchID
		view.ParentBatchID = &id
	}
	submission, parseErr := parseSupplementSubmission(run.Result)
	if parseErr != nil {
		view.Warnings = append(view.Warnings, IncrementalPlanWarning{
			Code:    planWarningInvalidResult,
			Message: "the incremental design run result is not valid JSON",
		})
	}
	view.Warnings = append(view.Warnings, submission.Warnings...)
	view.Degraded = run.Status == domain.RunDegraded || run.Status == domain.RunFailed || submission.Degraded
	if batchWarnings, err := planBatchWarnings(ctx, s.deps.Batches, versionID, view.ParentBatchID); err != nil {
		return IncrementalPlanView{}, err
	} else {
		view.Warnings = append(view.Warnings, batchWarnings...)
	}

	// A materialized run records impactAvailable explicitly; an agent-run result
	// only carries unmappedChanges when P4 supplied it. Both mean the same thing:
	// the absence of the field is "no P4 result", not "no changes".
	hasUnmappedChanges := supplementImpactAvailable(run.Result)
	if hasUnmappedChanges {
		for _, change := range submission.UnmappedChanges {
			view.UnmappedChanges = append(view.UnmappedChanges, IncrementalPlanChange{Path: change.Path, Summary: change.Summary})
		}
	} else if !planWarningsContainCode(view.Warnings, planWarningImpactUnavailable) {
		// The starter already records the same warning when it had no P4 result
		// (T16b); only synthesize it for runs that carry neither the warning nor
		// an impact signal, so the plan never lists it twice.
		view.Warnings = append(view.Warnings, IncrementalPlanWarning{
			Code:    planWarningImpactUnavailable,
			Message: "no P4 impact result is available for this version",
		})
	}

	state, err := loadGraphState(ctx, s.deps.Nodes, s.deps.Steps, s.deps.Assertions, s.deps.Sources, versionID)
	if err != nil {
		return IncrementalPlanView{}, err
	}
	view.NewCases = planNewCases(run.ID, submission.NewCaseIDs, state)
	view.AffectedCases = planAffectedCases(state, submission.AffectedCases)
	view.DefaultCaseIDs = planDefaultCaseIDs(view.NewCases, view.AffectedCases)
	return view, nil
}

// supplementImpactAvailable reports whether a run result carries a P4 impact
// result. The materializer writes impactAvailable, while a raw agent result only
// has unmappedChanges when the input included the impact summary; an explicit
// empty array counts as available.
func supplementImpactAvailable(raw []byte) bool {
	var document struct {
		ImpactAvailable *bool `json:"impactAvailable"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &document); err == nil && document.ImpactAvailable != nil {
			return *document.ImpactAvailable
		}
	}
	return rawFieldPresent(raw, "unmappedChanges")
}

// planBatchWarnings validates the parent batch of the plan's run against the
// optional execution-batch reader (spec §5.3/§7.6): the parent must belong to the
// version and be its latest terminal batch. A nil reader degrades the check with
// the batch_precondition_unavailable warning instead of silently bypassing it.
func planBatchWarnings(ctx context.Context, reader SupplementBatchReader, versionID int64, parentBatchID *int64) ([]IncrementalPlanWarning, error) {
	if reader == nil {
		return []IncrementalPlanWarning{{
			Code:    planWarningBatchUnavailable,
			Message: "the execution batch reader is not configured; the parent batch preconditions were not verified",
		}}, nil
	}
	if parentBatchID == nil {
		return []IncrementalPlanWarning{{
			Code:    planWarningInvalidParentBatch,
			Message: "the incremental_design run has no parent batch",
		}}, nil
	}
	batch, err := reader.GetBatch(ctx, *parentBatchID)
	if errors.Is(err, ErrSupplementBatchNotFound) {
		return []IncrementalPlanWarning{{
			Code:    planWarningInvalidParentBatch,
			Message: fmt.Sprintf("parent batch %d does not exist", *parentBatchID),
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mindmap: read parent batch %d: %w", *parentBatchID, err)
	}
	if batch.VersionID != versionID {
		return []IncrementalPlanWarning{{
			Code:    planWarningInvalidParentBatch,
			Message: fmt.Sprintf("parent batch %d belongs to version %d, not %d", batch.ID, batch.VersionID, versionID),
		}}, nil
	}
	if !supplementBatchTerminal(batch.Status) {
		return []IncrementalPlanWarning{{
			Code:    planWarningInvalidParentBatch,
			Message: fmt.Sprintf("parent batch %d is %s; a parent batch must be terminal", batch.ID, batch.Status),
		}}, nil
	}
	latest, err := reader.LatestTerminalBatch(ctx, versionID)
	if errors.Is(err, ErrSupplementBatchNotFound) {
		return []IncrementalPlanWarning{{
			Code:    planWarningInvalidParentBatch,
			Message: fmt.Sprintf("version %d has no terminal batch", versionID),
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mindmap: read the latest terminal batch of version %d: %w", versionID, err)
	}
	if latest.ID != batch.ID {
		return []IncrementalPlanWarning{{
			Code:    planWarningInvalidParentBatch,
			Message: fmt.Sprintf("parent batch %d is not the latest terminal batch %d of version %d", batch.ID, latest.ID, versionID),
		}}, nil
	}
	return nil, nil
}

// planContinuePhase returns the planning phase: the version adapter's
// continue_phase when the optional P7 port is available, otherwise the status
// fallback (continuing designs, everything else executes).
func planContinuePhase(ctx context.Context, versions VersionLifecycle, version VersionSnapshot) string {
	if reader, ok := versions.(ContinuePhaseReader); ok {
		if phase, err := reader.ContinuePhase(ctx, version.ID); err == nil {
			if phase == continuePhaseDesigning || phase == continuePhaseExecuting {
				return phase
			}
		}
	}
	if version.Status == versionStatusContinuing {
		return continuePhaseDesigning
	}
	return continuePhaseExecuting
}

// rawFieldPresent reports whether a JSON object has key, even when its value is
// an empty array: the empty P4 result is "available but no changes", which is not
// the same as "no P4 result".
func rawFieldPresent(raw []byte, key string) bool {
	if len(raw) == 0 {
		return false
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return false
	}
	_, ok := document[key]
	return ok
}

// planWarningsContainCode reports whether warnings already carry one code, so a
// warning synthesized by the plan never duplicates a warning already stored in
// the run result (for example the starter-seeded impact_unavailable).
func planWarningsContainCode(warnings []IncrementalPlanWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

// mergeIncrementalPlanWarnings returns the seeded warnings followed by the
// generated ones. Seeded warnings are deduped by code (first wins) and a
// generated warning never repeats a seeded code: materialization must not drop
// the input diagnostics (impact_unavailable, unmapped_truncated, a degraded
// diagnostic) nor duplicate them. Generated warnings keep their own codes as-is,
// because one run may legitimately report several skipped items with the same
// code (for example two invalid affected proposals).
func mergeIncrementalPlanWarnings(seeded, generated []IncrementalPlanWarning) []IncrementalPlanWarning {
	if len(seeded) == 0 && len(generated) == 0 {
		return nil
	}
	merged := make([]IncrementalPlanWarning, 0, len(seeded)+len(generated))
	seededCodes := make(map[string]struct{}, len(seeded))
	for _, warning := range seeded {
		if _, duplicate := seededCodes[warning.Code]; duplicate {
			continue
		}
		seededCodes[warning.Code] = struct{}{}
		merged = append(merged, warning)
	}
	for _, warning := range generated {
		if _, duplicate := seededCodes[warning.Code]; duplicate {
			continue
		}
		merged = append(merged, warning)
	}
	return merged
}

// planNewCases lists the materialized ai_supplement cases attributed to the run:
// a node is only listed when the run result declares it in newCaseIds or an
// ai_run source traces it to the run. An untagged manual supplement is never
// mixed in, and a run without any attribution lists no new cases. Merged and
// rejected supplement nodes are omitted (they left the new-case state).
func planNewCases(runID int64, declared []flexID, state graphState) []IncrementalPlanNewCase {
	declaredIDs := make(map[int64]struct{}, len(declared))
	for _, id := range declared {
		if id.Int64() > 0 {
			declaredIDs[id.Int64()] = struct{}{}
		}
	}
	runNodeIDs := make(map[int64]struct{}, len(state.sources))
	for _, source := range state.sources {
		if source.RefKind != domain.RefAIRun || source.RefID == nil {
			continue
		}
		if *source.RefID == runID {
			runNodeIDs[source.NodeID] = struct{}{}
		}
	}
	newCases := make([]IncrementalPlanNewCase, 0)
	for _, node := range state.nodes {
		if node.Kind != domain.NodeKindCase || node.Source != domain.NodeSourceAISupplement {
			continue
		}
		if node.Status != domain.NodeStatusSupplemented {
			continue
		}
		_, declaredByRun := declaredIDs[node.ID]
		_, tracedToRun := runNodeIDs[node.ID]
		if !declaredByRun && !tracedToRun {
			continue
		}
		requirementIDs := make([]int64, 0)
		confidence := domain.ConfidenceMedium
		for _, source := range state.sources {
			if source.NodeID != node.ID || source.RefKind != domain.RefRequirement || source.RefID == nil {
				continue
			}
			requirementIDs = append(requirementIDs, *source.RefID)
			if validConfidence(source.Confidence) {
				confidence = source.Confidence
			}
		}
		sort.Slice(requirementIDs, func(i, j int) bool { return requirementIDs[i] < requirementIDs[j] })
		newCases = append(newCases, IncrementalPlanNewCase{
			NodeID:         node.ID,
			Title:          node.Title,
			Priority:       node.Priority,
			Confidence:     confidence,
			RequirementIDs: requirementIDs,
			Status:         node.Status,
		})
	}
	sort.Slice(newCases, func(i, j int) bool { return newCases[i].NodeID < newCases[j].NodeID })
	return newCases
}

// planAffectedCases validates the affected proposals against the current graph:
// only existing case nodes are listed, and only high/medium confidence is
// selected by default (low is display-only, spec §5.8/§7.6).
func planAffectedCases(state graphState, proposals []supplementAffectedProposal) []IncrementalPlanAffectedCase {
	byID := make(map[int64]domain.CaseNode, len(state.nodes))
	for _, node := range state.nodes {
		byID[node.ID] = node
	}
	affected := make([]IncrementalPlanAffectedCase, 0, len(proposals))
	seen := make(map[int64]struct{}, len(proposals))
	for _, proposal := range proposals {
		nodeID := proposal.nodeID()
		if nodeID <= 0 {
			continue
		}
		node, ok := byID[nodeID]
		if !ok || node.Kind != domain.NodeKindCase {
			continue
		}
		if _, duplicate := seen[nodeID]; duplicate {
			continue
		}
		seen[nodeID] = struct{}{}
		if !validConfidence(proposal.Confidence) {
			continue
		}
		affected = append(affected, IncrementalPlanAffectedCase{
			NodeID:     nodeID,
			Title:      node.Title,
			Confidence: proposal.Confidence,
			Reason:     proposal.Reason,
			Source:     proposal.Source,
			Selected:   proposal.Confidence == domain.ConfidenceHigh || proposal.Confidence == domain.ConfidenceMedium,
		})
	}
	sort.Slice(affected, func(i, j int) bool { return affected[i].NodeID < affected[j].NodeID })
	return affected
}

// planDefaultCaseIDs is every new case plus every high/medium affected case,
// ascending and duplicate-free (spec §7.6).
func planDefaultCaseIDs(newCases []IncrementalPlanNewCase, affected []IncrementalPlanAffectedCase) []int64 {
	ids := make([]int64, 0, len(newCases)+len(affected))
	seen := make(map[int64]struct{}, len(newCases)+len(affected))
	appendID := func(id int64) {
		if id <= 0 {
			return
		}
		if _, duplicate := seen[id]; duplicate {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, newCase := range newCases {
		appendID(newCase.NodeID)
	}
	for _, affectedCase := range affected {
		if affectedCase.Selected {
			appendID(affectedCase.NodeID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

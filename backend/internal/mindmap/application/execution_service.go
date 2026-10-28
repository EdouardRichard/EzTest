package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// ExecutionDeps are the ports of the mindmap execution projection used by the
// P6 execution context (spec §4.2 MindmapPort). Graph is the transaction runner
// of MarkExecuted; the repository ports are the read model shared with
// GraphService.Get. Events is the existing mindmap publisher (spec §5.10).
type ExecutionDeps struct {
	Graph      GraphRepository
	Nodes      NodeRepository
	Steps      StepRepository
	Assertions AssertionRepository
	Sources    SourceRepository
	Baselines  BaselineRepository
	Events     EventPublisher
	Clock      Clock
}

// ExecutionService exposes the frozen mindmap to the execution context and
// records the executed marker of finished cases. It never writes the baseline,
// the steps, the assertions or the sources: execution reads a frozen graph and
// only moves case nodes from reviewed to executed (spec §5.2).
type ExecutionService struct {
	deps ExecutionDeps
}

// NewExecutionService wires the execution projection use cases to their ports.
func NewExecutionService(deps ExecutionDeps) *ExecutionService {
	return &ExecutionService{deps: deps}
}

// Baseline returns the frozen baseline of versionID: the canonical graph hash,
// the optional code baseline commit and every case node id of the current
// working graph in ascending order (the frozen content plus the P7 supplement
// nodes appended after the freeze). A version without a baseline is the stable
// 409 baseline_required (spec §5.1/§5.2).
func (s *ExecutionService) Baseline(ctx context.Context, versionID int64) (domain.ExecutionBaseline, error) {
	if err := s.readPorts(); err != nil {
		return domain.ExecutionBaseline{}, err
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return domain.ExecutionBaseline{}, err
	}
	if baseline == nil {
		return domain.ExecutionBaseline{}, executionBaselineRequiredError(versionID)
	}
	nodes, err := s.deps.Nodes.ListByVersion(ctx, versionID)
	if err != nil {
		return domain.ExecutionBaseline{}, err
	}
	caseIDs := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		if node.Kind == domain.NodeKindCase {
			caseIDs = append(caseIDs, node.ID)
		}
	}
	sort.Slice(caseIDs, func(i, j int) bool { return caseIDs[i] < caseIDs[j] })
	return domain.ExecutionBaseline{
		GraphHash:          baseline.GraphHash,
		CodeBaselineCommit: copyStringPointer(baseline.CodeBaselineCommit),
		CaseIDs:            caseIDs,
	}, nil
}

// SnapshotCases assembles the immutable execution snapshots of versionID. An
// empty caseIDs selects every case of the current working graph; explicit ids
// must be case nodes of the version (the frozen baseline plus the P7 supplement
// nodes). The result is ordered by case node id ascending and every case
// carries its steps by seq, its assertions by id, its requirement sources and
// the frozen graph hash (spec §5.2).
func (s *ExecutionService) SnapshotCases(ctx context.Context, versionID int64, caseIDs []int64) ([]domain.ExecutionCase, error) {
	if err := s.readPorts(); err != nil {
		return nil, err
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return nil, err
	}
	if baseline == nil {
		return nil, executionBaselineRequiredError(versionID)
	}
	state, err := loadGraphState(ctx, s.deps.Nodes, s.deps.Steps, s.deps.Assertions, s.deps.Sources, versionID)
	if err != nil {
		return nil, err
	}
	selected, err := selectExecutionCaseIDs(state.nodes, versionID, baseline.FrozenAt, caseIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]domain.CaseNode, len(state.nodes))
	for _, node := range state.nodes {
		byID[node.ID] = node
	}
	snapshots := make([]domain.ExecutionCase, 0, len(selected))
	for _, nodeID := range selected {
		snapshots = append(snapshots, buildExecutionCase(byID[nodeID], state, baseline.GraphHash))
	}
	return snapshots, nil
}

// MarkExecuted moves the given case nodes from reviewed, merged or
// needs_revalidation to executed after their execution finished (spec §5.2,
// extended by P7). Already-executed cases are an idempotent no-op; any other
// status is the stable 409 case_status_conflict. The write is one transaction
// that only touches the case_node status, revision and updated_at: the baseline
// rows, steps, assertions and sources are never written. One case.status_changed
// event per changed case is published after the commit through the existing
// mindmap publisher (spec §5.10).
//
// An empty caseIDs list is a successful no-op: there is no case to mark.
func (s *ExecutionService) MarkExecuted(ctx context.Context, versionID int64, caseIDs []int64) error {
	if len(caseIDs) == 0 {
		return nil
	}
	ids, err := normalizeExecutionCaseIDs(caseIDs, versionID)
	if err != nil {
		return err
	}
	if s.deps.Graph == nil {
		return errors.New("mindmap: execution graph transaction is not configured")
	}
	now := s.now()
	var changed []executionStatusChange
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		nodes := tx.Nodes()
		if nodes == nil {
			return errors.New("mindmap: execution node repository is not configured")
		}
		for _, nodeID := range ids {
			node, err := nodes.Get(ctx, versionID, nodeID)
			if err != nil {
				if executionIsNotFound(err) {
					return invalidCaseIDsError(versionID, nodeID)
				}
				return err
			}
			if node.Kind != domain.NodeKindCase {
				return invalidCaseIDsError(versionID, nodeID)
			}
			switch node.Status {
			case domain.NodeStatusExecuted:
				// Idempotent: the case already carries the executed marker.
				continue
			case domain.NodeStatusReviewed, domain.NodeStatusMerged, domain.NodeStatusNeedsRevalidation:
				candidate := node
				candidate.Status = domain.NodeStatusExecuted
				candidate.UpdatedAt = now
				updated, err := nodes.Update(ctx, candidate, node.Revision)
				if err != nil {
					return err
				}
				changed = append(changed, executionStatusChange{node: updated, from: node.Status})
			default:
				return domain.NewStateConflict("case_status_conflict",
					fmt.Sprintf("case %d is %s; only a reviewed, merged or needs_revalidation case can be marked executed",
						nodeID, node.Status))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, change := range changed {
		s.publishStatusChanged(ctx, versionID, change.node, change.from, domain.NodeStatusExecuted)
	}
	return nil
}

// MarkMerged marks the supplemented cases selected by an incremental or supplement
// batch as merged (spec §5.3: "选中补全节点 → merged"). It is idempotent for an
// already merged node; any other status is a case_status_conflict. Only the
// status column moves.
//
// The legacy signature has no actor (the T7 SupplementGraphPort predates the
// actor); it keeps the stored updated_by. MarkMergedBy is the actor-aware form
// the execution adapter should delegate to once the port carries the actor.
func (s *ExecutionService) MarkMerged(ctx context.Context, versionID int64, caseIDs []int64) error {
	return s.MarkMergedBy(ctx, versionID, 0, caseIDs)
}

// MarkMergedBy marks supplemented cases merged and records the acting user in
// updated_by. A non-positive actor keeps the stored updated_by (legacy callers).
func (s *ExecutionService) MarkMergedBy(ctx context.Context, versionID, actor int64, caseIDs []int64) error {
	if len(caseIDs) == 0 {
		return nil
	}
	ids, err := normalizeExecutionCaseIDs(caseIDs, versionID)
	if err != nil {
		return err
	}
	if s.deps.Graph == nil {
		return errors.New("mindmap: execution graph transaction is not configured")
	}
	now := s.now()
	var changed []executionStatusChange
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		nodes := tx.Nodes()
		if nodes == nil {
			return errors.New("mindmap: execution node repository is not configured")
		}
		for _, nodeID := range ids {
			node, err := nodes.Get(ctx, versionID, nodeID)
			if err != nil {
				if executionIsNotFound(err) {
					return invalidCaseIDsError(versionID, nodeID)
				}
				return err
			}
			if node.Kind != domain.NodeKindCase {
				return invalidCaseIDsError(versionID, nodeID)
			}
			switch node.Status {
			case domain.NodeStatusMerged:
				continue
			case domain.NodeStatusSupplemented:
				candidate := node
				candidate.Status = domain.NodeStatusMerged
				if actor > 0 {
					candidate.UpdatedBy = actor
				}
				candidate.UpdatedAt = now
				updated, err := nodes.Update(ctx, candidate, node.Revision)
				if err != nil {
					return err
				}
				changed = append(changed, executionStatusChange{node: updated, from: node.Status})
			default:
				return domain.NewStateConflict("case_status_conflict",
					fmt.Sprintf("case %d is %s; only a supplemented case can be marked merged", nodeID, node.Status))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, change := range changed {
		s.publishStatusChanged(ctx, versionID, change.node, change.from, domain.NodeStatusMerged)
	}
	return nil
}

// executionStatusChange is one committed status transition awaiting its event.
type executionStatusChange struct {
	node domain.CaseNode
	from string
}

// readPorts reports the mandatory read ports of the execution projection.
func (s *ExecutionService) readPorts() error {
	if s.deps.Nodes == nil || s.deps.Steps == nil || s.deps.Assertions == nil ||
		s.deps.Sources == nil || s.deps.Baselines == nil {
		return errors.New("mindmap: execution service ports are not configured")
	}
	return nil
}

// now returns the injected clock time (falling back to the wall clock).
func (s *ExecutionService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}

// publishStatusChanged emits the case.status_changed event of one status move
// after the commit. The envelope keeps the transition summary only; the case id
// is a decimal string like every other id of the execution context (spec
// §5.10/§5.12).
func (s *ExecutionService) publishStatusChanged(ctx context.Context, versionID int64, node domain.CaseNode, from, to string) {
	summary := "case status changed"
	if title := strings.TrimSpace(node.Title); title != "" {
		summary = fmt.Sprintf("case %q %s", truncateRunes(title, maxEventTitleRunes), to)
	}
	s.publish(ctx, Event{
		VersionID:  versionID,
		Type:       domain.EventCaseStatusChanged,
		Summary:    summary,
		RefCaseIDs: []int64{node.ID},
		Source:     node.Source,
		Detail: map[string]any{
			"nodeId":   strconv.FormatInt(node.ID, 10),
			"from":     from,
			"to":       to,
			"revision": node.Revision,
		},
	})
}

// publish sends one event after the commit. A publish failure is logged and
// never returned to the caller (spec §6.11).
func (s *ExecutionService) publish(ctx context.Context, event Event) {
	if s.deps.Events == nil {
		return
	}
	if err := s.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "mindmap event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"error", err,
		)
	}
}

// selectExecutionCaseIDs resolves the snapshot selection. The frozen baseline
// cases are always selectable; the P7 supplement nodes created after the freeze
// are selectable too because incremental/supplement batches execute them (spec
// §5.3: caseIds ⊆ the current working graph, including supplemented/merged/needs
// _revalidation). A late user-authored case still cannot masquerade as a baseline
// case: only ai_supplement content is the P7 exception.
func selectExecutionCaseIDs(nodes []domain.CaseNode, versionID int64, frozenAt time.Time, requested []int64) ([]int64, error) {
	baselineCases := make(map[int64]domain.CaseNode)
	for _, node := range nodes {
		if node.Kind == domain.NodeKindCase {
			baselineCases[node.ID] = node
		}
	}
	if len(requested) == 0 {
		ids := make([]int64, 0, len(baselineCases))
		for id := range baselineCases {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return ids, nil
	}
	seen := make(map[int64]struct{}, len(requested))
	ids := make([]int64, 0, len(requested))
	for _, id := range requested {
		if id <= 0 {
			return nil, invalidCaseIDsError(versionID, id)
		}
		node, ok := baselineCases[id]
		if !ok || (!supplementSelectableNode(node) && createdAfterFrozenBaseline(node, frozenAt)) {
			return nil, invalidCaseIDsError(versionID, id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// supplementSelectableNode reports whether a case created after the freeze is
// still part of the current working graph through the P7 supplement path.
func supplementSelectableNode(node domain.CaseNode) bool {
	if node.Source == domain.NodeSourceAISupplement {
		return true
	}
	switch node.Status {
	case domain.NodeStatusSupplemented, domain.NodeStatusMerged:
		return true
	default:
		return false
	}
}

// normalizeExecutionCaseIDs validates and deduplicates the MarkExecuted target
// ids and returns them in ascending order.
func normalizeExecutionCaseIDs(caseIDs []int64, versionID int64) ([]int64, error) {
	seen := make(map[int64]struct{}, len(caseIDs))
	ids := make([]int64, 0, len(caseIDs))
	for _, id := range caseIDs {
		if id <= 0 {
			return nil, invalidCaseIDsError(versionID, id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// buildExecutionCase assembles the frozen snapshot of one case node. Steps come
// ordered by seq, assertions by id; the requirement ids come from the node's
// requirement sources and are ordered ascending.
func buildExecutionCase(node domain.CaseNode, state graphState, graphHash string) domain.ExecutionCase {
	steps := sortedSteps(state.steps[node.ID])
	snapshotSteps := make([]domain.ExecutionStep, 0, len(steps))
	stepSeq := make(map[int64]int, len(steps))
	for _, step := range steps {
		stepSeq[step.ID] = step.Seq
		snapshotSteps = append(snapshotSteps, domain.ExecutionStep{
			Seq:      step.Seq,
			Action:   step.Action,
			Expected: step.Expected,
			Data:     step.Data,
		})
	}
	assertions := sortedExecutionAssertions(state.assertions[node.ID])
	snapshotAssertions := make([]domain.ExecutionAssertion, 0, len(assertions))
	for _, assertion := range assertions {
		var assertionStepSeq *int
		if assertion.StepID != nil {
			if seq, ok := stepSeq[*assertion.StepID]; ok {
				value := seq
				assertionStepSeq = &value
			}
		}
		snapshotAssertions = append(snapshotAssertions, domain.ExecutionAssertion{
			ID:         assertion.ID,
			StepSeq:    assertionStepSeq,
			Kind:       assertion.Kind,
			Target:     assertion.Target,
			Expression: assertion.Expression,
			Expected:   assertion.Expected,
		})
	}
	return domain.ExecutionCase{
		NodeID:         node.ID,
		Revision:       node.Revision,
		Title:          node.Title,
		Priority:       node.Priority,
		Preconditions:  node.Preconditions,
		TestData:       append(json.RawMessage(nil), node.TestData...),
		Steps:          snapshotSteps,
		Assertions:     snapshotAssertions,
		RequirementIDs: requirementIDsOfCase(state.sources, node.ID),
		GraphHash:      graphHash,
		Origin:         executionCaseOrigin(node),
		Status:         node.Status,
	}
}

// executionCaseOrigin normalizes the case_node source to the execution-side
// origin set (spec §5.2/§5.3): only the AI supplement path is not baseline.
func executionCaseOrigin(node domain.CaseNode) string {
	if node.Source == domain.NodeSourceAISupplement {
		return domain.ExecutionOriginAISupplement
	}
	return domain.ExecutionOriginBaseline
}

// requirementIDsOfCase returns the sorted requirement ids of the node's
// requirement sources.
func requirementIDsOfCase(sources []domain.CaseSource, nodeID int64) []int64 {
	ids := make([]int64, 0)
	seen := make(map[int64]struct{})
	for _, source := range sources {
		if source.NodeID != nodeID || source.RefKind != domain.RefRequirement || source.RefID == nil {
			continue
		}
		if _, duplicate := seen[*source.RefID]; duplicate {
			continue
		}
		seen[*source.RefID] = struct{}{}
		ids = append(ids, *source.RefID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// sortedExecutionAssertions copies and orders assertions by id.
func sortedExecutionAssertions(assertions []domain.CaseAssertion) []domain.CaseAssertion {
	ordered := append([]domain.CaseAssertion(nil), assertions...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	return ordered
}

// createdAfterFrozenBaseline reports whether node was created after the
// baseline freeze and therefore cannot belong to the baseline snapshot. Unset
// timestamps are tolerated so partially populated fixtures still validate.
func createdAfterFrozenBaseline(node domain.CaseNode, frozenAt time.Time) bool {
	if frozenAt.IsZero() || node.CreatedAt.IsZero() {
		return false
	}
	return node.CreatedAt.After(frozenAt)
}

// executionBaselineRequiredError is the stable 409 of an execution read on a
// version that was never frozen.
func executionBaselineRequiredError(versionID int64) error {
	return domain.NewStateConflict("baseline_required",
		fmt.Sprintf("version %d has no frozen baseline; execution requires one", versionID))
}

// invalidCaseIDsError is the stable 422 of an id outside the version's frozen
// case set.
func invalidCaseIDsError(versionID, caseID int64) error {
	return domain.NewInvalid("caseIds", "invalid_case_ids",
		fmt.Sprintf("case %d is not a case node of the current working graph of version %d", caseID, versionID))
}

// executionIsNotFound reports whether err is a mindmap not-found domain error.
func executionIsNotFound(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound
}

// copyStringPointer returns a copy of an optional string.
func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

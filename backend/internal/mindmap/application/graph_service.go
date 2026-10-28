package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Editability and text bounds used by the manual graph use cases. The version
// statuses mirror the P3 state machine (spec §6.3); the mindmap context does not
// import the version domain.
const (
	versionStatusDesigning = "designing"
	versionStatusReviewing = "reviewing"

	// maxGraphNodes is the P5 mindmap size cap of spec §7.3: above 300 nodes a
	// read or an add is rejected with graph_too_large (HTTP 409).
	maxGraphNodes = 300

	maxNodeTitleRunes       = 200
	maxNodeDescriptionRunes = 4000
	maxStepActionRunes      = 2000
	maxStepExpectedRunes    = 2000
	maxStepDataRunes        = 4000
	maxAssertionExprRunes   = 4000
	maxAssertionTargetRunes = 1000
	maxAssertionExpectRunes = 2000
	maxEventTitleRunes      = 120
	maxEventRefCaseIDs      = 20

	defaultNodePriority = domain.PriorityP2
	defaultConfidence   = domain.ConfidenceMedium
)

// GraphDeps are the ports of the manual mindmap graph use cases. Graph is the
// transaction runner; the repository ports are the read path (and the adapters
// the transaction is expected to expose through GraphTx). Baselines is read to
// derive editability and to re-check the freeze in the write transaction.
// Anchors is optional: when set, DeleteNode removes the code anchors of the
// deleted subtree best-effort (spec §6.1 rule 6).
type GraphDeps struct {
	Graph        GraphRepository
	Nodes        NodeRepository
	Steps        StepRepository
	Assertions   AssertionRepository
	Sources      SourceRepository
	Baselines    BaselineRepository
	Versions     VersionLifecycle
	Requirements RequirementReader
	Anchors      CodeAnchorGateway
	Events       EventPublisher
	IDs          IDGenerator
	Clock        Clock
}

// GraphNode is one node of the mindmap with its child collections attached.
// Scenarios carry no steps/assertions and never own sources (spec §6.1/§6.2).
type GraphNode struct {
	Node       domain.CaseNode
	Steps      []domain.CaseStep
	Assertions []domain.CaseAssertion
	Sources    []domain.CaseSource
}

// GraphView is the service projection of GET /mindmap (spec §7.3). Editable is
// derived only by the server: version status in {designing, reviewing} and no
// baseline. Anchors/findings/impact are owned by other P5 services.
type GraphView struct {
	VersionID     int64
	VersionStatus string
	Editable      bool
	Nodes         []GraphNode
	Coverage      domain.Coverage
	Baseline      *domain.CaseBaseline
}

// CreateNodeCommand is the POST /nodes body (spec §7.2 endpoint 2). Kind is
// required; a case must reference at least one clarified requirement of the
// version, a scenario must not carry requirementIds.
type CreateNodeCommand struct {
	Kind           string
	ParentID       *int64
	Title          string
	Description    string
	Priority       string
	Preconditions  string
	TestData       []byte
	RequirementIDs []int64
	Source         string
}

// UpdateNodeCommand is the PATCH /nodes/{nodeId} body (spec §7.2 endpoint 3).
// Nil fields keep their stored value. RequirementIDs, when non-nil, replaces the
// requirement source set of an existing case.
type UpdateNodeCommand struct {
	Revision       int
	Title          *string
	Description    *string
	Priority       *string
	Preconditions  *string
	TestData       *json.RawMessage
	RequirementIDs *[]int64
}

// AddStepCommand is the POST /nodes/{nodeId}/steps body (spec §7.2 endpoint 6).
// AfterSeq is optional; an absent value appends at the end.
type AddStepCommand struct {
	NodeRevision int
	Action       string
	Expected     string
	Data         string
	AfterSeq     *int
}

// UpdateStepCommand is the PATCH /steps/{stepId} body (spec §7.2 endpoint 7).
type UpdateStepCommand struct {
	NodeRevision int
	Action       *string
	Expected     *string
	Data         *string
}

// ReorderStepsCommand is the PUT /nodes/{nodeId}/steps/order body (spec §7.2
// endpoint 9). NodeRevision is the OCC token guarding the child set.
type ReorderStepsCommand struct {
	NodeRevision int
	StepIDs      []int64
}

// AddAssertionCommand is the POST /nodes/{nodeId}/assertions body (spec §7.2
// endpoint 10).
type AddAssertionCommand struct {
	NodeRevision int
	StepID       *int64
	Kind         string
	Target       string
	Expression   string
	Expected     string
}

// UpdateAssertionCommand is the PATCH /assertions/{assertionId} body (spec §7.2
// endpoint 11). ClearStep detaches the optional step link; when false a nil
// StepID keeps it.
type UpdateAssertionCommand struct {
	NodeRevision int
	StepID       *int64
	ClearStep    bool
	Kind         *string
	Target       *string
	Expression   *string
	Expected     *string
}

// AddSourceCommand is the POST /nodes/{nodeId}/sources body (spec §7.2 endpoint
// 13). A requirement source must reference a clarified requirement of the
// version.
type AddSourceCommand struct {
	RefKind    string
	RefID      *int64
	RefKey     string
	Source     string
	Confidence string
}

// ReorderCommand is the PUT /order body (spec §7.2 endpoint 5). NodeIDs must
// contain exactly every child of ParentID (nil means the version roots).
type ReorderCommand struct {
	ParentID *int64
	NodeIDs  []int64
}

// GraphService implements the manual mindmap CRUD of spec §6.1/§6.2/§6.5:
// version-state gating, optimistic concurrency, tree and requirement-source
// validation, and post-commit case events. It never writes the version table.
type GraphService struct {
	deps GraphDeps
}

// NewGraphService wires the manual graph use cases to their ports.
func NewGraphService(deps GraphDeps) *GraphService {
	return &GraphService{deps: deps}
}

// Get returns the mindmap projection of one version: nodes with their steps,
// assertions and sources, the requirement coverage matrix, the baseline (when
// frozen) and the server-derived editable flag (spec §7.3). A version above the
// 300-node cap is a graph_too_large conflict.
func (s *GraphService) Get(ctx context.Context, versionID int64) (GraphView, error) {
	if s.deps.Versions == nil {
		return GraphView{}, errors.New("mindmap: version lifecycle is not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return GraphView{}, err
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return GraphView{}, err
	}
	if s.deps.Nodes == nil || s.deps.Steps == nil || s.deps.Assertions == nil || s.deps.Sources == nil {
		return GraphView{}, errors.New("mindmap: graph repositories are not configured")
	}
	state, err := loadGraphState(ctx, s.deps.Nodes, s.deps.Steps, s.deps.Assertions, s.deps.Sources, versionID)
	if err != nil {
		return GraphView{}, err
	}
	if len(state.nodes) > maxGraphNodes {
		return GraphView{}, graphTooLargeError(versionID, len(state.nodes))
	}
	confirmed := make(map[int64]bool)
	if s.deps.Requirements != nil {
		requirements, err := s.deps.Requirements.ListConfirmed(ctx, versionID)
		if err != nil {
			return GraphView{}, err
		}
		for _, requirement := range requirements {
			confirmed[requirement.ID] = true
		}
	}
	return GraphView{
		VersionID:     versionID,
		VersionStatus: version.Status,
		Editable:      editableVersion(version.Status, baseline),
		Nodes:         groupGraphNodes(state),
		Coverage:      domain.BuildCoverage(versionID, confirmed, state.nodes, state.sources),
		Baseline:      baseline,
	}, nil
}

// CreateNode creates a scenario or a case. A case must reference at least one
// clarified requirement of the version and gets its requirement source rows in
// the same transaction; a scenario never owns sources (spec §6.1/§6.2). Adding
// the node that would exceed the 300-node cap is a graph_too_large conflict.
func (s *GraphService) CreateNode(ctx context.Context, versionID, actor int64, cmd CreateNodeCommand) (domain.CaseNode, error) {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return domain.CaseNode{}, err
	}
	if cmd.Kind != domain.NodeKindScenario && cmd.Kind != domain.NodeKindCase {
		return domain.CaseNode{}, invalidCaseTree("node kind %q is not scenario or case", cmd.Kind)
	}
	title, err := validateTitle(cmd.Title)
	if err != nil {
		return domain.CaseNode{}, err
	}
	if err := validateRunes("description", cmd.Description, maxNodeDescriptionRunes); err != nil {
		return domain.CaseNode{}, err
	}
	if err := validateRunes("preconditions", cmd.Preconditions, maxNodeDescriptionRunes); err != nil {
		return domain.CaseNode{}, err
	}
	priority := cmd.Priority
	if priority == "" {
		priority = defaultNodePriority
	}
	if err := validatePriority(priority); err != nil {
		return domain.CaseNode{}, err
	}
	testData, err := normalizeTestData(cmd.TestData)
	if err != nil {
		return domain.CaseNode{}, err
	}
	source := cmd.Source
	if source == "" {
		source = domain.NodeSourceUserInput
	}
	if !validNodeSource(source) {
		return domain.CaseNode{}, domain.NewInvalid("source", "invalid_source",
			fmt.Sprintf("node source %q is not user_input, ai_generated or ai_supplement", source))
	}
	if cmd.Kind == domain.NodeKindScenario && len(cmd.RequirementIDs) > 0 {
		return domain.CaseNode{}, invalidCaseTree("a scenario must not own requirement sources")
	}
	var requirementIDs []int64
	if cmd.Kind == domain.NodeKindCase {
		requirementIDs, err = s.requireConfirmedRequirements(ctx, versionID, cmd.RequirementIDs, true)
		if err != nil {
			return domain.CaseNode{}, err
		}
	}

	now := s.now()
	var created domain.CaseNode
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		if len(state.nodes) >= maxGraphNodes {
			return graphTooLargeError(versionID, len(state.nodes))
		}
		if cmd.ParentID != nil {
			parent, ok := findNode(state.nodes, *cmd.ParentID)
			if !ok || parent.VersionID != versionID {
				return domain.NewNotFound("node", *cmd.ParentID)
			}
			if parent.Kind != domain.NodeKindScenario {
				return invalidCaseTree("case node %d must not have children", parent.ID)
			}
		}
		id, err := s.nextID()
		if err != nil {
			return err
		}
		node := domain.CaseNode{
			ID:            id,
			VersionID:     versionID,
			ParentID:      copyInt64Ptr(cmd.ParentID),
			Kind:          cmd.Kind,
			Title:         title,
			Description:   cmd.Description,
			Priority:      priority,
			Preconditions: cmd.Preconditions,
			TestData:      testData,
			Source:        source,
			Status:        domain.NodeStatusDraft,
			SortOrder:     nextSortOrder(state.nodes, cmd.ParentID),
			Revision:      1,
			CreatedBy:     actor,
			UpdatedBy:     actor,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		state.nodes = append(state.nodes, node)
		sources := make([]domain.CaseSource, 0, len(requirementIDs))
		for _, requirementID := range requirementIDs {
			sourceID, err := s.nextID()
			if err != nil {
				return err
			}
			ref := requirementID
			sources = append(sources, domain.CaseSource{
				ID:         sourceID,
				VersionID:  versionID,
				NodeID:     node.ID,
				RefKind:    domain.RefRequirement,
				RefID:      &ref,
				Source:     source,
				Confidence: domain.ConfidenceHigh,
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
		state.sources = append(state.sources, sources...)
		if err := validateGraphState(state); err != nil {
			return err
		}
		if _, err := tx.Nodes().Create(ctx, node); err != nil {
			return err
		}
		for _, source := range sources {
			if _, err := tx.Sources().Create(ctx, source); err != nil {
				return err
			}
		}
		created = node
		return nil
	})
	if err != nil {
		return domain.CaseNode{}, err
	}
	s.publishNode(ctx, domain.EventCaseCreated, versionID, actor, created, "")
	return created, nil
}

// UpdateNode applies a partial edit under the node revision CAS. A
// requirementIds replacement is validated against the version's clarified
// requirements and written in the same transaction (spec §6.5/§6.2).
func (s *GraphService) UpdateNode(ctx context.Context, versionID, nodeID, actor int64, cmd UpdateNodeCommand) (domain.CaseNode, error) {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return domain.CaseNode{}, err
	}
	if cmd.Revision <= 0 {
		return domain.CaseNode{}, revisionConflict(nodeID, cmd.Revision, 0)
	}
	var title *string
	if cmd.Title != nil {
		value, err := validateTitle(*cmd.Title)
		if err != nil {
			return domain.CaseNode{}, err
		}
		title = &value
	}
	if cmd.Description != nil {
		if err := validateRunes("description", *cmd.Description, maxNodeDescriptionRunes); err != nil {
			return domain.CaseNode{}, err
		}
	}
	if cmd.Preconditions != nil {
		if err := validateRunes("preconditions", *cmd.Preconditions, maxNodeDescriptionRunes); err != nil {
			return domain.CaseNode{}, err
		}
	}
	if cmd.Priority != nil {
		if err := validatePriority(*cmd.Priority); err != nil {
			return domain.CaseNode{}, err
		}
	}
	var testData []byte
	if cmd.TestData != nil {
		value, err := normalizeTestData([]byte(*cmd.TestData))
		if err != nil {
			return domain.CaseNode{}, err
		}
		testData = value
	}
	var requirementIDs []int64
	if cmd.RequirementIDs != nil {
		ids, err := s.requireConfirmedRequirements(ctx, versionID, *cmd.RequirementIDs, false)
		if err != nil {
			return domain.CaseNode{}, err
		}
		requirementIDs = ids
	}

	now := s.now()
	var updated domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		current, ok := findNode(state.nodes, nodeID)
		if !ok || current.VersionID != versionID {
			return domain.NewNotFound("node", nodeID)
		}
		if current.Revision != cmd.Revision {
			return revisionConflict(nodeID, cmd.Revision, current.Revision)
		}
		candidate := current
		if title != nil {
			candidate.Title = *title
		}
		if cmd.Description != nil {
			candidate.Description = *cmd.Description
		}
		if cmd.Preconditions != nil {
			candidate.Preconditions = *cmd.Preconditions
		}
		if cmd.Priority != nil {
			candidate.Priority = *cmd.Priority
		}
		if cmd.TestData != nil {
			candidate.TestData = testData
		}
		candidate.UpdatedBy = actor
		candidate.UpdatedAt = now

		var removedSources []int64
		var addedSources []domain.CaseSource
		if cmd.RequirementIDs != nil {
			if current.Kind == domain.NodeKindScenario {
				if len(requirementIDs) > 0 {
					return invalidCaseTree("a scenario must not own requirement sources")
				}
			} else {
				if len(requirementIDs) == 0 {
					return invalidCaseTree("case %d must keep at least one requirement source", nodeID)
				}
				for _, source := range state.sources {
					if source.NodeID == nodeID && source.RefKind == domain.RefRequirement {
						removedSources = append(removedSources, source.ID)
					}
				}
				kept := state.sources[:0]
				for _, source := range state.sources {
					if source.NodeID == nodeID && source.RefKind == domain.RefRequirement {
						continue
					}
					kept = append(kept, source)
				}
				state.sources = kept
				for _, requirementID := range requirementIDs {
					sourceID, err := s.nextID()
					if err != nil {
						return err
					}
					ref := requirementID
					addedSources = append(addedSources, domain.CaseSource{
						ID:         sourceID,
						VersionID:  versionID,
						NodeID:     nodeID,
						RefKind:    domain.RefRequirement,
						RefID:      &ref,
						Source:     current.Source,
						Confidence: domain.ConfidenceHigh,
						CreatedAt:  now,
						UpdatedAt:  now,
					})
				}
				state.sources = append(state.sources, addedSources...)
			}
		}
		state.nodes = replaceNode(state.nodes, candidate)
		if err := validateGraphState(state); err != nil {
			return err
		}
		result, err := tx.Nodes().Update(ctx, candidate, cmd.Revision)
		if err != nil {
			return err
		}
		for _, sourceID := range removedSources {
			if err := tx.Sources().Delete(ctx, nodeID, sourceID); err != nil {
				return err
			}
		}
		for _, source := range addedSources {
			if _, err := tx.Sources().Create(ctx, source); err != nil {
				return err
			}
		}
		updated = result
		return nil
	})
	if err != nil {
		return domain.CaseNode{}, err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, updated, "")
	return updated, nil
}

// DeleteNode deletes one node under the revision CAS. Deleting a scenario
// cascades its subtree; the child rows are removed by the database FKs and the
// code anchors of the removed subtree are deleted best-effort through the
// optional CodeAnchorGateway after the commit (spec §6.1 rule 6).
func (s *GraphService) DeleteNode(ctx context.Context, versionID, nodeID, actor int64, revision int) error {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return err
	}
	var deleted domain.CaseNode
	var doomedIDs []int64
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		current, ok := findNode(state.nodes, nodeID)
		if !ok || current.VersionID != versionID {
			return domain.NewNotFound("node", nodeID)
		}
		if current.Revision != revision {
			return revisionConflict(nodeID, revision, current.Revision)
		}
		doomed := descendantIDs(state.nodes, nodeID)
		doomedIDs = orderedIDs(doomed)
		state = state.withoutNodes(doomed)
		if err := validateGraphState(state); err != nil {
			return err
		}
		if err := tx.Nodes().Delete(ctx, versionID, nodeID, revision); err != nil {
			return err
		}
		deleted = current
		return nil
	})
	if err != nil {
		return err
	}
	s.deleteAnchors(ctx, versionID, doomedIDs)
	s.publishNode(ctx, domain.EventCaseDeleted, versionID, actor, deleted, "")
	return nil
}

// AddStep appends a step to a case, or inserts it after AfterSeq, renumbering
// the sequence to 1..N and bumping the node revision in the same transaction
// (spec §6.1 rule 3, §6.5).
func (s *GraphService) AddStep(ctx context.Context, versionID, nodeID, actor int64, cmd AddStepCommand) (domain.CaseStep, error) {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return domain.CaseStep{}, err
	}
	if err := validateStepFields(cmd.Action, cmd.Expected, cmd.Data); err != nil {
		return domain.CaseStep{}, err
	}
	now := s.now()
	var created domain.CaseStep
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		node, ok := findNode(state.nodes, nodeID)
		if !ok || node.VersionID != versionID {
			return domain.NewNotFound("node", nodeID)
		}
		if err := s.checkNodeRevision(node, cmd.NodeRevision); err != nil {
			return err
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own steps", nodeID)
		}
		eventNode = node
		existing := sortedSteps(state.steps[nodeID])
		if len(existing) >= domain.MaxStepsPerCase {
			return invalidCaseTree("case %d already has %d steps", nodeID, len(existing))
		}
		insertAt := len(existing)
		if cmd.AfterSeq != nil {
			if *cmd.AfterSeq < 1 || *cmd.AfterSeq > len(existing) {
				return invalidCaseTree("afterSeq %d is outside 1..%d", *cmd.AfterSeq, len(existing))
			}
			insertAt = *cmd.AfterSeq
		}
		id, err := s.nextID()
		if err != nil {
			return err
		}
		step := domain.CaseStep{
			ID:        id,
			NodeID:    nodeID,
			Seq:       len(existing) + 1,
			Action:    cmd.Action,
			Expected:  cmd.Expected,
			Data:      cmd.Data,
			Source:    domain.StepSourceUserInput,
			CreatedAt: now,
			UpdatedAt: now,
		}
		ordered := make([]domain.CaseStep, 0, len(existing)+1)
		ordered = append(ordered, existing[:insertAt]...)
		ordered = append(ordered, step)
		ordered = append(ordered, existing[insertAt:]...)
		for index := range ordered {
			ordered[index].Seq = index + 1
		}
		state.steps[nodeID] = ordered
		if err := validateGraphState(state); err != nil {
			return err
		}
		stored, err := tx.Steps().Create(ctx, step)
		if err != nil {
			return err
		}
		if err := tx.Steps().ReplaceSeq(ctx, nodeID, stepIDs(ordered)); err != nil {
			return err
		}
		if _, err := tx.Nodes().Update(ctx, touchNode(node, actor, now), cmd.NodeRevision); err != nil {
			return err
		}
		stored.Seq = insertAt + 1
		created = stored
		return nil
	})
	if err != nil {
		return domain.CaseStep{}, err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "steps")
	return created, nil
}

// UpdateStep edits step content under the owning node revision CAS (spec §6.5).
func (s *GraphService) UpdateStep(ctx context.Context, versionID, stepID, actor int64, cmd UpdateStepCommand) (domain.CaseStep, error) {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return domain.CaseStep{}, err
	}
	if cmd.Action != nil {
		if strings.TrimSpace(*cmd.Action) == "" {
			return domain.CaseStep{}, invalidCaseTree("step action must not be empty")
		}
		if err := validateRunes("action", *cmd.Action, maxStepActionRunes); err != nil {
			return domain.CaseStep{}, err
		}
	}
	if cmd.Expected != nil {
		if err := validateRunes("expected", *cmd.Expected, maxStepExpectedRunes); err != nil {
			return domain.CaseStep{}, err
		}
	}
	if cmd.Data != nil {
		if err := validateRunes("data", *cmd.Data, maxStepDataRunes); err != nil {
			return domain.CaseStep{}, err
		}
	}
	now := s.now()
	var updated domain.CaseStep
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		step, ok := findStep(state, stepID)
		if !ok {
			return domain.NewNotFound("step", stepID)
		}
		node, ok := findNode(state.nodes, step.NodeID)
		if !ok {
			return domain.NewNotFound("node", step.NodeID)
		}
		if err := s.checkNodeRevision(node, cmd.NodeRevision); err != nil {
			return err
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own steps", node.ID)
		}
		eventNode = node
		candidate := step
		if cmd.Action != nil {
			candidate.Action = *cmd.Action
		}
		if cmd.Expected != nil {
			candidate.Expected = *cmd.Expected
		}
		if cmd.Data != nil {
			candidate.Data = *cmd.Data
		}
		candidate.UpdatedAt = now
		state.steps[node.ID] = replaceStep(state.steps[node.ID], candidate)
		if err := validateGraphState(state); err != nil {
			return err
		}
		if err := tx.Steps().Update(ctx, candidate); err != nil {
			return err
		}
		if _, err := tx.Nodes().Update(ctx, touchNode(node, actor, now), cmd.NodeRevision); err != nil {
			return err
		}
		updated = candidate
		return nil
	})
	if err != nil {
		return domain.CaseStep{}, err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "steps")
	return updated, nil
}

// DeleteStep deletes a step, its owned assertions, renumbers the remaining
// steps and bumps the node revision (spec §7.2 endpoint 8, §6.5).
func (s *GraphService) DeleteStep(ctx context.Context, versionID, stepID, actor int64, nodeRevision int) error {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return err
	}
	now := s.now()
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		step, ok := findStep(state, stepID)
		if !ok {
			return domain.NewNotFound("step", stepID)
		}
		node, ok := findNode(state.nodes, step.NodeID)
		if !ok {
			return domain.NewNotFound("node", step.NodeID)
		}
		if err := s.checkNodeRevision(node, nodeRevision); err != nil {
			return err
		}
		remaining := make([]domain.CaseStep, 0, len(state.steps[node.ID]))
		for _, existing := range sortedSteps(state.steps[node.ID]) {
			if existing.ID == stepID {
				continue
			}
			remaining = append(remaining, existing)
		}
		for index := range remaining {
			remaining[index].Seq = index + 1
		}
		var ownedAssertions []int64
		keptAssertions := make([]domain.CaseAssertion, 0, len(state.assertions[node.ID]))
		for _, assertion := range state.assertions[node.ID] {
			if assertion.StepID != nil && *assertion.StepID == stepID {
				ownedAssertions = append(ownedAssertions, assertion.ID)
				continue
			}
			keptAssertions = append(keptAssertions, assertion)
		}
		state.steps[node.ID] = remaining
		state.assertions[node.ID] = keptAssertions
		if err := validateGraphState(state); err != nil {
			return err
		}
		for _, assertionID := range ownedAssertions {
			if err := tx.Assertions().Delete(ctx, node.ID, assertionID); err != nil {
				return err
			}
		}
		if err := tx.Steps().Delete(ctx, node.ID, stepID); err != nil {
			return err
		}
		if len(remaining) > 0 {
			if err := tx.Steps().ReplaceSeq(ctx, node.ID, stepIDs(remaining)); err != nil {
				return err
			}
		}
		if _, err := tx.Nodes().Update(ctx, touchNode(node, actor, now), nodeRevision); err != nil {
			return err
		}
		eventNode = node
		return nil
	})
	if err != nil {
		return err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "steps")
	return nil
}

// ReorderSteps applies a complete step permutation under the node revision CAS.
// Missing, duplicate or foreign step ids are invalid_case_tree (spec §7.2
// endpoint 9, §6.5).
func (s *GraphService) ReorderSteps(ctx context.Context, versionID, nodeID, actor int64, cmd ReorderStepsCommand) error {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return err
	}
	now := s.now()
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		node, ok := findNode(state.nodes, nodeID)
		if !ok || node.VersionID != versionID {
			return domain.NewNotFound("node", nodeID)
		}
		if err := s.checkNodeRevision(node, cmd.NodeRevision); err != nil {
			return err
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own steps", nodeID)
		}
		eventNode = node
		byID := make(map[int64]domain.CaseStep, len(state.steps[nodeID]))
		for _, step := range state.steps[nodeID] {
			byID[step.ID] = step
		}
		if len(cmd.StepIDs) != len(byID) {
			return invalidCaseTree("step order must contain all %d steps of case %d", len(byID), nodeID)
		}
		seen := make(map[int64]struct{}, len(cmd.StepIDs))
		ordered := make([]domain.CaseStep, 0, len(cmd.StepIDs))
		for index, stepID := range cmd.StepIDs {
			step, exists := byID[stepID]
			if !exists {
				return invalidCaseTree("step %d does not belong to case %d", stepID, nodeID)
			}
			if _, duplicate := seen[stepID]; duplicate {
				return invalidCaseTree("step order repeats step %d", stepID)
			}
			seen[stepID] = struct{}{}
			step.Seq = index + 1
			ordered = append(ordered, step)
		}
		state.steps[nodeID] = ordered
		if err := validateGraphState(state); err != nil {
			return err
		}
		if len(ordered) > 0 {
			if err := tx.Steps().ReplaceSeq(ctx, nodeID, stepIDs(ordered)); err != nil {
				return err
			}
		}
		if _, err := tx.Nodes().Update(ctx, touchNode(node, actor, now), cmd.NodeRevision); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "steps")
	return nil
}

// AddAssertion appends an assertion to a case. The optional step must belong to
// the same node (spec §6.1 rule 4) and the write is guarded by the node revision
// (spec §6.5).
func (s *GraphService) AddAssertion(ctx context.Context, versionID, nodeID, actor int64, cmd AddAssertionCommand) (domain.CaseAssertion, error) {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return domain.CaseAssertion{}, err
	}
	if err := validateAssertionFields(cmd.Kind, cmd.Target, cmd.Expression, cmd.Expected); err != nil {
		return domain.CaseAssertion{}, err
	}
	now := s.now()
	var created domain.CaseAssertion
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		node, ok := findNode(state.nodes, nodeID)
		if !ok || node.VersionID != versionID {
			return domain.NewNotFound("node", nodeID)
		}
		if err := s.checkNodeRevision(node, cmd.NodeRevision); err != nil {
			return err
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own assertions", nodeID)
		}
		eventNode = node
		if cmd.StepID != nil && !stepBelongsTo(state.steps[nodeID], *cmd.StepID) {
			return invalidCaseTree("assertion references step %d of another case", *cmd.StepID)
		}
		id, err := s.nextID()
		if err != nil {
			return err
		}
		assertion := domain.CaseAssertion{
			ID:         id,
			NodeID:     nodeID,
			StepID:     copyInt64Ptr(cmd.StepID),
			Kind:       cmd.Kind,
			Target:     cmd.Target,
			Expression: cmd.Expression,
			Expected:   cmd.Expected,
			Source:     domain.NodeSourceUserInput,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		state.assertions[nodeID] = append(state.assertions[nodeID], assertion)
		if err := validateGraphState(state); err != nil {
			return err
		}
		stored, err := tx.Assertions().Create(ctx, assertion)
		if err != nil {
			return err
		}
		if _, err := tx.Nodes().Update(ctx, touchNode(node, actor, now), cmd.NodeRevision); err != nil {
			return err
		}
		created = stored
		return nil
	})
	if err != nil {
		return domain.CaseAssertion{}, err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "assertions")
	return created, nil
}

// UpdateAssertion edits assertion content under the owning node revision CAS.
func (s *GraphService) UpdateAssertion(ctx context.Context, versionID, assertionID, actor int64, cmd UpdateAssertionCommand) (domain.CaseAssertion, error) {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return domain.CaseAssertion{}, err
	}
	if cmd.Kind != nil {
		if err := validateAssertionKind(*cmd.Kind); err != nil {
			return domain.CaseAssertion{}, err
		}
	}
	if cmd.Expression != nil {
		if strings.TrimSpace(*cmd.Expression) == "" {
			return domain.CaseAssertion{}, invalidCaseTree("assertion expression must not be empty")
		}
		if err := validateRunes("expression", *cmd.Expression, maxAssertionExprRunes); err != nil {
			return domain.CaseAssertion{}, err
		}
	}
	if cmd.Target != nil {
		if err := validateRunes("target", *cmd.Target, maxAssertionTargetRunes); err != nil {
			return domain.CaseAssertion{}, err
		}
	}
	if cmd.Expected != nil {
		if err := validateRunes("expected", *cmd.Expected, maxAssertionExpectRunes); err != nil {
			return domain.CaseAssertion{}, err
		}
	}
	now := s.now()
	var updated domain.CaseAssertion
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		assertion, node, ok := findAssertion(state, assertionID)
		if !ok {
			return domain.NewNotFound("assertion", assertionID)
		}
		if err := s.checkNodeRevision(node, cmd.NodeRevision); err != nil {
			return err
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own assertions", node.ID)
		}
		eventNode = node
		candidate := assertion
		if cmd.ClearStep {
			candidate.StepID = nil
		} else if cmd.StepID != nil {
			if !stepBelongsTo(state.steps[node.ID], *cmd.StepID) {
				return invalidCaseTree("assertion references step %d of another case", *cmd.StepID)
			}
			candidate.StepID = copyInt64Ptr(cmd.StepID)
		}
		if cmd.Kind != nil {
			candidate.Kind = *cmd.Kind
		}
		if cmd.Target != nil {
			candidate.Target = *cmd.Target
		}
		if cmd.Expression != nil {
			candidate.Expression = *cmd.Expression
		}
		if cmd.Expected != nil {
			candidate.Expected = *cmd.Expected
		}
		candidate.UpdatedAt = now
		state.assertions[node.ID] = replaceAssertion(state.assertions[node.ID], candidate)
		if err := validateGraphState(state); err != nil {
			return err
		}
		if err := tx.Assertions().Update(ctx, candidate); err != nil {
			return err
		}
		if _, err := tx.Nodes().Update(ctx, touchNode(node, actor, now), cmd.NodeRevision); err != nil {
			return err
		}
		updated = candidate
		return nil
	})
	if err != nil {
		return domain.CaseAssertion{}, err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "assertions")
	return updated, nil
}

// DeleteAssertion deletes an assertion under the node revision CAS. The last
// assertion of a case cannot be deleted (invalid_case_tree; spec §7.2 endpoint
// 12).
func (s *GraphService) DeleteAssertion(ctx context.Context, versionID, assertionID, actor int64, nodeRevision int) error {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return err
	}
	now := s.now()
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		assertion, node, ok := findAssertion(state, assertionID)
		if !ok {
			return domain.NewNotFound("assertion", assertionID)
		}
		if err := s.checkNodeRevision(node, nodeRevision); err != nil {
			return err
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own assertions", node.ID)
		}
		if len(state.assertions[node.ID]) <= 1 {
			return invalidCaseTree("case %d must keep at least one assertion", node.ID)
		}
		kept := make([]domain.CaseAssertion, 0, len(state.assertions[node.ID])-1)
		for _, existing := range state.assertions[node.ID] {
			if existing.ID == assertion.ID {
				continue
			}
			kept = append(kept, existing)
		}
		state.assertions[node.ID] = kept
		if err := validateGraphState(state); err != nil {
			return err
		}
		if err := tx.Assertions().Delete(ctx, node.ID, assertion.ID); err != nil {
			return err
		}
		if _, err := tx.Nodes().Update(ctx, touchNode(node, actor, now), nodeRevision); err != nil {
			return err
		}
		eventNode = node
		return nil
	})
	if err != nil {
		return err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "assertions")
	return nil
}

// AddSource adds a source to a case. Sources only exist on case nodes and a
// requirement source must reference a clarified requirement of the version
// (spec §6.2).
func (s *GraphService) AddSource(ctx context.Context, versionID, nodeID, actor int64, cmd AddSourceCommand) (domain.CaseSource, error) {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return domain.CaseSource{}, err
	}
	if err := validateRefKind(cmd.RefKind); err != nil {
		return domain.CaseSource{}, err
	}
	if cmd.RefKind == domain.RefRequirement {
		if cmd.RefID == nil {
			return domain.CaseSource{}, invalidCaseTree("requirement source must carry a requirement id")
		}
		if _, err := s.requireConfirmedRequirements(ctx, versionID, []int64{*cmd.RefID}, true); err != nil {
			return domain.CaseSource{}, err
		}
	} else if cmd.RefID == nil && strings.TrimSpace(cmd.RefKey) == "" {
		return domain.CaseSource{}, domain.NewInvalid("refId", "invalid_source",
			fmt.Sprintf("%s source needs refId or refKey", cmd.RefKind))
	}
	sourceKind := cmd.Source
	if sourceKind == "" {
		sourceKind = domain.NodeSourceUserInput
	}
	if !validNodeSource(sourceKind) {
		return domain.CaseSource{}, domain.NewInvalid("source", "invalid_source",
			fmt.Sprintf("source %q is not user_input, ai_generated or ai_supplement", sourceKind))
	}
	confidence := cmd.Confidence
	if confidence == "" {
		confidence = defaultConfidence
	}
	if !validConfidence(confidence) {
		return domain.CaseSource{}, domain.NewInvalid("confidence", "invalid_confidence",
			fmt.Sprintf("confidence %q is not high, medium or low", confidence))
	}
	now := s.now()
	var created domain.CaseSource
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		node, ok := findNode(state.nodes, nodeID)
		if !ok || node.VersionID != versionID {
			return domain.NewNotFound("node", nodeID)
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own sources", nodeID)
		}
		eventNode = node
		id, err := s.nextID()
		if err != nil {
			return err
		}
		source := domain.CaseSource{
			ID:         id,
			VersionID:  versionID,
			NodeID:     nodeID,
			RefKind:    cmd.RefKind,
			RefID:      copyInt64Ptr(cmd.RefID),
			RefKey:     cmd.RefKey,
			Source:     sourceKind,
			Confidence: confidence,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		state.sources = append(state.sources, source)
		if err := validateGraphState(state); err != nil {
			return err
		}
		stored, err := tx.Sources().Create(ctx, source)
		if err != nil {
			return err
		}
		created = stored
		return nil
	})
	if err != nil {
		return domain.CaseSource{}, err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "sources")
	return created, nil
}

// DeleteSource deletes a source. Deleting the last requirement source of a case
// would leave it uncovered and is invalid_case_tree (spec §6.2 rule 4).
func (s *GraphService) DeleteSource(ctx context.Context, versionID, sourceID, actor int64) error {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return err
	}
	var eventNode domain.CaseNode
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		source, ok := findSource(state, sourceID)
		if !ok {
			return domain.NewNotFound("source", sourceID)
		}
		node, ok := findNode(state.nodes, source.NodeID)
		if !ok {
			return domain.NewNotFound("node", source.NodeID)
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own sources", node.ID)
		}
		if source.RefKind == domain.RefRequirement {
			requirementSources := 0
			for _, existing := range state.sources {
				if existing.NodeID == node.ID && existing.RefKind == domain.RefRequirement {
					requirementSources++
				}
			}
			if requirementSources <= 1 {
				return invalidCaseTree("case %d must keep at least one requirement source", node.ID)
			}
		}
		kept := make([]domain.CaseSource, 0, len(state.sources)-1)
		for _, existing := range state.sources {
			if existing.ID == source.ID {
				continue
			}
			kept = append(kept, existing)
		}
		state.sources = kept
		if err := validateGraphState(state); err != nil {
			return err
		}
		if err := tx.Sources().Delete(ctx, node.ID, source.ID); err != nil {
			return err
		}
		eventNode = node
		return nil
	})
	if err != nil {
		return err
	}
	s.publishNode(ctx, domain.EventCaseUpdated, versionID, actor, eventNode, "sources")
	return nil
}

// Reorder applies a complete child permutation. NodeIDs must contain exactly
// every child of ParentID (nil means the version roots) with no duplicates
// (spec §7.2 endpoint 5).
func (s *GraphService) Reorder(ctx context.Context, versionID, actor int64, cmd ReorderCommand) error {
	if err := s.beginWrite(ctx, versionID); err != nil {
		return err
	}
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if err := s.guardFrozenTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		if cmd.ParentID != nil {
			parent, ok := findNode(state.nodes, *cmd.ParentID)
			if !ok || parent.VersionID != versionID {
				return domain.NewNotFound("node", *cmd.ParentID)
			}
			if parent.Kind != domain.NodeKindScenario && len(cmd.NodeIDs) > 0 {
				return invalidCaseTree("case node %d must not have children", parent.ID)
			}
		}
		children := make(map[int64]struct{})
		for _, node := range state.nodes {
			if sameParent(node.ParentID, cmd.ParentID) {
				children[node.ID] = struct{}{}
			}
		}
		if len(cmd.NodeIDs) != len(children) {
			return invalidCaseTree("order must contain all %d children, got %d", len(children), len(cmd.NodeIDs))
		}
		seen := make(map[int64]struct{}, len(cmd.NodeIDs))
		for _, nodeID := range cmd.NodeIDs {
			if _, ok := children[nodeID]; !ok {
				return invalidCaseTree("node %d is not a child of this parent", nodeID)
			}
			if _, duplicate := seen[nodeID]; duplicate {
				return invalidCaseTree("order repeats node %d", nodeID)
			}
			seen[nodeID] = struct{}{}
		}
		if err := tx.Nodes().SetSortOrders(ctx, versionID, cmd.NodeIDs); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	refs := cmd.NodeIDs
	if len(refs) > maxEventRefCaseIDs {
		refs = refs[:maxEventRefCaseIDs]
	}
	detail := map[string]any{"change": "order", "count": len(cmd.NodeIDs)}
	if cmd.ParentID != nil {
		detail["parentId"] = *cmd.ParentID
	}
	s.publish(ctx, Event{
		VersionID:  versionID,
		Type:       domain.EventCaseUpdated,
		Summary:    "case order updated",
		RefCaseIDs: append([]int64(nil), refs...),
		ActorID:    actor,
		Source:     domain.NodeSourceUserInput,
		Detail:     detail,
	})
	return nil
}

// beginWrite applies the version gate shared by every graph write: the version
// must be designing or reviewing and must not be frozen yet (spec §6.3/§6.4).
func (s *GraphService) beginWrite(ctx context.Context, versionID int64) error {
	if s.deps.Versions == nil {
		return errors.New("mindmap: version lifecycle is not configured")
	}
	if s.deps.Graph == nil {
		return errors.New("mindmap: graph transaction is not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return err
	}
	switch version.Status {
	case versionStatusDesigning, versionStatusReviewing:
	default:
		return domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s; the mindmap is editable only in designing or reviewing", versionID, version.Status))
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return err
	}
	if baseline != nil {
		return baselineFrozenError(versionID)
	}
	return nil
}

// guardFrozenTx re-checks the baseline inside the write transaction so a freeze
// that committed after beginWrite still rejects the write.
func (s *GraphService) guardFrozenTx(ctx context.Context, tx GraphTx, versionID int64) error {
	baseline, err := loadBaseline(ctx, tx.Baselines(), versionID)
	if err != nil {
		return err
	}
	if baseline != nil {
		return baselineFrozenError(versionID)
	}
	return nil
}

// checkNodeRevision is the node-level optimistic-lock check shared by the node
// and child-collection writes (spec §6.5).
func (s *GraphService) checkNodeRevision(node domain.CaseNode, expected int) error {
	if expected <= 0 || node.Revision != expected {
		return revisionConflict(node.ID, expected, node.Revision)
	}
	return nil
}

// requireConfirmedRequirements resolves requirement ids of the version and
// rejects any id that is not a clarified requirement of it. When required is
// true an empty list is rejected too.
func (s *GraphService) requireConfirmedRequirements(ctx context.Context, versionID int64, ids []int64, required bool) ([]int64, error) {
	normalized, err := normalizeIDs(ids)
	if err != nil {
		return nil, err
	}
	if required && len(normalized) == 0 {
		return nil, domain.NewInvalid("requirementIds", "unconfirmed_requirements",
			"a case must reference at least one clarified requirement")
	}
	if len(normalized) == 0 {
		return normalized, nil
	}
	if s.deps.Requirements == nil {
		return nil, errors.New("mindmap: requirement reader is not configured")
	}
	requirements, err := s.deps.Requirements.ListConfirmed(ctx, versionID)
	if err != nil {
		return nil, err
	}
	confirmed := make(map[int64]struct{}, len(requirements))
	for _, requirement := range requirements {
		confirmed[requirement.ID] = struct{}{}
	}
	var missing []int64
	for _, id := range normalized {
		if _, ok := confirmed[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, domain.NewInvalid("requirementIds", "unconfirmed_requirements",
			fmt.Sprintf("requirements %v are not clarified in version %d", missing, versionID))
	}
	return normalized, nil
}

// nextID mints one application-generated Snowflake id (D29).
func (s *GraphService) nextID() (int64, error) {
	if s.deps.IDs == nil {
		return 0, errors.New("mindmap: id generator is not configured")
	}
	id := s.deps.IDs.Next()
	if id <= 0 {
		return 0, errors.New("mindmap: id generator returned a non-positive id")
	}
	return id, nil
}

// now returns the injected clock time (falling back to the wall clock).
func (s *GraphService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}

// publishNode emits one case.created/updated/deleted event after a committed
// write. change, when set, names the child collection that changed.
func (s *GraphService) publishNode(ctx context.Context, eventType string, versionID, actor int64, node domain.CaseNode, change string) {
	verb := "updated"
	switch eventType {
	case domain.EventCaseCreated:
		verb = "created"
	case domain.EventCaseDeleted:
		verb = "deleted"
	}
	detail := map[string]any{
		"nodeId": node.ID,
		"kind":   node.Kind,
		"title":  truncateRunes(node.Title, maxEventTitleRunes),
		"source": node.Source,
	}
	if change != "" {
		detail["change"] = change
	}
	summary := "case " + verb
	if title := strings.TrimSpace(node.Title); title != "" {
		summary = fmt.Sprintf("case %q %s", truncateRunes(title, maxEventTitleRunes), verb)
	}
	s.publish(ctx, Event{
		VersionID:  versionID,
		Type:       eventType,
		Summary:    summary,
		RefCaseIDs: []int64{node.ID},
		ActorID:    actor,
		Source:     node.Source,
		Detail:     detail,
	})
}

// deleteAnchors removes every code anchor of the given nodes through the
// optional CodeAnchorGateway. Anchor cleanup is best-effort (spec §6.1 rule 6):
// a nil gateway or a failed list/delete call never fails the node delete.
func (s *GraphService) deleteAnchors(ctx context.Context, versionID int64, nodeIDs []int64) {
	if s.deps.Anchors == nil {
		return
	}
	for _, nodeID := range nodeIDs {
		anchors, err := s.deps.Anchors.List(ctx, versionID, nodeID)
		if err != nil {
			continue
		}
		for _, anchor := range anchors {
			_ = s.deps.Anchors.Delete(ctx, versionID, anchor.ID)
		}
	}
}

// publish sends one event after the commit. A publish failure is logged and
// never returned to the caller (spec §6.11).
func (s *GraphService) publish(ctx context.Context, event Event) {
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

// graphState is the in-transaction graph snapshot used for tree and source
// validation before any write of a mutation.
type graphState struct {
	nodes      []domain.CaseNode
	steps      map[int64][]domain.CaseStep
	assertions map[int64][]domain.CaseAssertion
	sources    []domain.CaseSource
}

// loadGraphState reads the whole graph of versionID through the given
// repositories.
func loadGraphState(ctx context.Context, nodes NodeRepository, steps StepRepository, assertions AssertionRepository, sources SourceRepository, versionID int64) (graphState, error) {
	nodeList, err := nodes.ListByVersion(ctx, versionID)
	if err != nil {
		return graphState{}, err
	}
	stepList, err := steps.ListByVersion(ctx, versionID)
	if err != nil {
		return graphState{}, err
	}
	assertionList, err := assertions.ListByVersion(ctx, versionID)
	if err != nil {
		return graphState{}, err
	}
	sourceList, err := sources.ListByVersion(ctx, versionID)
	if err != nil {
		return graphState{}, err
	}
	state := graphState{
		nodes:      nodeList,
		steps:      make(map[int64][]domain.CaseStep),
		assertions: make(map[int64][]domain.CaseAssertion),
		sources:    sourceList,
	}
	for _, step := range stepList {
		state.steps[step.NodeID] = append(state.steps[step.NodeID], step)
	}
	for nodeID := range state.steps {
		state.steps[nodeID] = sortedSteps(state.steps[nodeID])
	}
	for _, assertion := range assertionList {
		state.assertions[assertion.NodeID] = append(state.assertions[assertion.NodeID], assertion)
	}
	for nodeID := range state.assertions {
		state.assertions[nodeID] = sortedAssertions(state.assertions[nodeID])
	}
	return state, nil
}

// loadGraphStateTx reads the graph through the transaction's repositories.
func loadGraphStateTx(ctx context.Context, tx GraphTx, versionID int64) (graphState, error) {
	nodes, steps, assertions, sources := tx.Nodes(), tx.Steps(), tx.Assertions(), tx.Sources()
	if nodes == nil || steps == nil || assertions == nil || sources == nil {
		return graphState{}, errors.New("mindmap: graph transaction repositories are not configured")
	}
	return loadGraphState(ctx, nodes, steps, assertions, sources, versionID)
}

// groupGraphNodes turns the flat graph snapshot into the ordered node
// projection of GET /mindmap: nodes by sort_order then id, children by their own
// order.
func groupGraphNodes(state graphState) []GraphNode {
	nodes := append([]domain.CaseNode(nil), state.nodes...)
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].SortOrder != nodes[j].SortOrder {
			return nodes[i].SortOrder < nodes[j].SortOrder
		}
		return nodes[i].ID < nodes[j].ID
	})
	sourcesByNode := make(map[int64][]domain.CaseSource)
	for _, source := range state.sources {
		sourcesByNode[source.NodeID] = append(sourcesByNode[source.NodeID], source)
	}
	result := make([]GraphNode, 0, len(nodes))
	for _, node := range nodes {
		view := GraphNode{Node: node}
		if node.Kind == domain.NodeKindCase {
			view.Steps = sortedSteps(state.steps[node.ID])
			view.Assertions = sortedAssertions(state.assertions[node.ID])
		}
		view.Sources = sortSources(sourcesByNode[node.ID])
		result = append(result, view)
	}
	return result
}

// validateGraphState enforces the tree rules of spec §6.1 and the source rules
// of §6.2 before a mutation writes anything.
func validateGraphState(state graphState) error {
	if err := validateTreeState(state); err != nil {
		return err
	}
	return validateSourceState(state)
}

// validateTreeState runs domain.ValidateGraph over the state while tolerating
// cases still being authored: a case with no steps or no assertions yet gets a
// placeholder child, because completeness is required at submit-review, not
// after every manual edit. All other structural rules stay enforced.
func validateTreeState(state graphState) error {
	steps := make(map[int64][]domain.CaseStep, len(state.steps))
	for nodeID, list := range state.steps {
		steps[nodeID] = append([]domain.CaseStep(nil), list...)
	}
	assertions := make(map[int64][]domain.CaseAssertion, len(state.assertions))
	for nodeID, list := range state.assertions {
		assertions[nodeID] = append([]domain.CaseAssertion(nil), list...)
	}
	for _, node := range state.nodes {
		if node.Kind != domain.NodeKindCase {
			continue
		}
		if len(steps[node.ID]) == 0 {
			steps[node.ID] = append(steps[node.ID], domain.CaseStep{
				ID:     -node.ID,
				NodeID: node.ID,
				Seq:    1,
				Action: "(pending)",
			})
		}
		if len(assertions[node.ID]) == 0 {
			assertions[node.ID] = append(assertions[node.ID], domain.CaseAssertion{
				ID:         -node.ID,
				NodeID:     node.ID,
				Expression: "(pending)",
			})
		}
	}
	return domain.ValidateGraph(state.nodes, steps, assertions)
}

// validateSourceState enforces spec §6.2: sources only exist on case nodes,
// every requirement source carries a requirement id, and every case keeps at
// least one requirement source.
func validateSourceState(state graphState) error {
	nodesByID := make(map[int64]domain.CaseNode, len(state.nodes))
	for _, node := range state.nodes {
		nodesByID[node.ID] = node
	}
	requirementSources := make(map[int64]int)
	for _, source := range state.sources {
		node, ok := nodesByID[source.NodeID]
		if !ok {
			return invalidCaseTree("source %d references missing node %d", source.ID, source.NodeID)
		}
		if node.Kind != domain.NodeKindCase {
			return invalidCaseTree("scenario %d must not own sources", node.ID)
		}
		if source.RefKind == domain.RefRequirement {
			if source.RefID == nil {
				return invalidCaseTree("requirement source %d has no requirement id", source.ID)
			}
			requirementSources[node.ID]++
		}
	}
	for _, node := range state.nodes {
		if node.Kind != domain.NodeKindCase {
			continue
		}
		if requirementSources[node.ID] == 0 {
			return invalidCaseTree("case %d has no requirement source", node.ID)
		}
	}
	return nil
}

// withoutNodes returns the state with the given nodes and their child rows
// removed.
func (g graphState) withoutNodes(doomed map[int64]struct{}) graphState {
	nodes := make([]domain.CaseNode, 0, len(g.nodes))
	for _, node := range g.nodes {
		if _, drop := doomed[node.ID]; drop {
			continue
		}
		nodes = append(nodes, node)
	}
	sources := make([]domain.CaseSource, 0, len(g.sources))
	for _, source := range g.sources {
		if _, drop := doomed[source.NodeID]; drop {
			continue
		}
		sources = append(sources, source)
	}
	steps := make(map[int64][]domain.CaseStep, len(g.steps))
	for nodeID, list := range g.steps {
		if _, drop := doomed[nodeID]; drop {
			continue
		}
		steps[nodeID] = append([]domain.CaseStep(nil), list...)
	}
	assertions := make(map[int64][]domain.CaseAssertion, len(g.assertions))
	for nodeID, list := range g.assertions {
		if _, drop := doomed[nodeID]; drop {
			continue
		}
		assertions[nodeID] = append([]domain.CaseAssertion(nil), list...)
	}
	return graphState{nodes: nodes, steps: steps, assertions: assertions, sources: sources}
}

// loadBaseline returns nil when the version has no baseline. Adapters may report
// absence either with a not-found domain error or with a zero row; a zero row
// is treated as absent so both conventions work.
func loadBaseline(ctx context.Context, repo BaselineRepository, versionID int64) (*domain.CaseBaseline, error) {
	if repo == nil {
		return nil, nil
	}
	baseline, err := repo.GetByVersion(ctx, versionID)
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound {
			return nil, nil
		}
		return nil, err
	}
	if baseline.ID == 0 {
		return nil, nil
	}
	return &baseline, nil
}

// editableVersion is the server-side editable derivation of spec §7.3.
func editableVersion(status string, baseline *domain.CaseBaseline) bool {
	if baseline != nil {
		return false
	}
	return status == versionStatusDesigning || status == versionStatusReviewing
}

// baselineFrozenError builds the stable 409 of a write after freezing.
func baselineFrozenError(versionID int64) error {
	return domain.NewStateConflict("baseline_frozen",
		fmt.Sprintf("version %d has a baseline; the mindmap is read-only", versionID))
}

// revisionConflict builds the stable 409 of a failed node revision CAS.
func revisionConflict(nodeID int64, expected, actual int) error {
	return domain.NewStateConflict("case_revision_conflict",
		fmt.Sprintf("case node %d revision is %d, expected %d", nodeID, actual, expected))
}

// graphTooLargeError builds the stable 409 of spec §7.3 for a graph above the
// 300-node P5 cap.
func graphTooLargeError(versionID int64, nodes int) error {
	return domain.NewConflict("graph_too_large",
		fmt.Sprintf("version %d has %d nodes; the mindmap is limited to %d nodes", versionID, nodes, maxGraphNodes))
}

// invalidCaseTree builds the domain-ruled invalid_case_tree error.
func invalidCaseTree(format string, args ...any) error {
	return domain.NewInvalid("graph", "invalid_case_tree", fmt.Sprintf(format, args...))
}

// findNode returns the node with id from the snapshot.
func findNode(nodes []domain.CaseNode, id int64) (domain.CaseNode, bool) {
	for _, node := range nodes {
		if node.ID == id {
			return node, true
		}
	}
	return domain.CaseNode{}, false
}

// replaceNode replaces the node with the same id in place.
func replaceNode(nodes []domain.CaseNode, node domain.CaseNode) []domain.CaseNode {
	for index := range nodes {
		if nodes[index].ID == node.ID {
			nodes[index] = node
			return nodes
		}
	}
	return append(nodes, node)
}

// descendantIDs returns id plus every transitive child id.
func descendantIDs(nodes []domain.CaseNode, id int64) map[int64]struct{} {
	doomed := map[int64]struct{}{id: {}}
	changed := true
	for changed {
		changed = false
		for _, node := range nodes {
			if node.ParentID == nil {
				continue
			}
			if _, parentDoomed := doomed[*node.ParentID]; !parentDoomed {
				continue
			}
			if _, already := doomed[node.ID]; !already {
				doomed[node.ID] = struct{}{}
				changed = true
			}
		}
	}
	return doomed
}

// orderedIDs returns the ids of a set in ascending order.
func orderedIDs(ids map[int64]struct{}) []int64 {
	ordered := make([]int64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered
}

// findStep scans the snapshot for one step.
func findStep(state graphState, stepID int64) (domain.CaseStep, bool) {
	for _, list := range state.steps {
		for _, step := range list {
			if step.ID == stepID {
				return step, true
			}
		}
	}
	return domain.CaseStep{}, false
}

// findAssertion scans the snapshot for one assertion and its owning node.
func findAssertion(state graphState, assertionID int64) (domain.CaseAssertion, domain.CaseNode, bool) {
	for _, list := range state.assertions {
		for _, assertion := range list {
			if assertion.ID != assertionID {
				continue
			}
			node, ok := findNode(state.nodes, assertion.NodeID)
			if !ok {
				return domain.CaseAssertion{}, domain.CaseNode{}, false
			}
			return assertion, node, true
		}
	}
	return domain.CaseAssertion{}, domain.CaseNode{}, false
}

// findSource scans the snapshot for one source.
func findSource(state graphState, sourceID int64) (domain.CaseSource, bool) {
	for _, source := range state.sources {
		if source.ID == sourceID {
			return source, true
		}
	}
	return domain.CaseSource{}, false
}

// stepBelongsTo reports whether stepID is one of the node's steps.
func stepBelongsTo(steps []domain.CaseStep, stepID int64) bool {
	for _, step := range steps {
		if step.ID == stepID {
			return true
		}
	}
	return false
}

// replaceStep replaces the step with the same id in place.
func replaceStep(steps []domain.CaseStep, step domain.CaseStep) []domain.CaseStep {
	for index := range steps {
		if steps[index].ID == step.ID {
			steps[index] = step
			return steps
		}
	}
	return append(steps, step)
}

// replaceAssertion replaces the assertion with the same id in place.
func replaceAssertion(assertions []domain.CaseAssertion, assertion domain.CaseAssertion) []domain.CaseAssertion {
	for index := range assertions {
		if assertions[index].ID == assertion.ID {
			assertions[index] = assertion
			return assertions
		}
	}
	return append(assertions, assertion)
}

// sortedSteps copies and orders steps by seq then id.
func sortedSteps(steps []domain.CaseStep) []domain.CaseStep {
	ordered := append([]domain.CaseStep(nil), steps...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Seq != ordered[j].Seq {
			return ordered[i].Seq < ordered[j].Seq
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

// sortedAssertions copies and orders assertions by step then id.
func sortedAssertions(assertions []domain.CaseAssertion) []domain.CaseAssertion {
	ordered := append([]domain.CaseAssertion(nil), assertions...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := refIDValue(ordered[i].StepID), refIDValue(ordered[j].StepID)
		if left != right {
			return left < right
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

// sortSources copies and orders sources by id.
func sortSources(sources []domain.CaseSource) []domain.CaseSource {
	ordered := append([]domain.CaseSource(nil), sources...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	return ordered
}

// stepIDs maps an ordered step list to its ids.
func stepIDs(steps []domain.CaseStep) []int64 {
	ids := make([]int64, 0, len(steps))
	for _, step := range steps {
		ids = append(ids, step.ID)
	}
	return ids
}

// refIDValue maps a nil reference id to 0 for ordering.
func refIDValue(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// nextSortOrder appends a new sibling after the current maximum sort_order.
func nextSortOrder(nodes []domain.CaseNode, parentID *int64) int {
	order := 0
	for _, node := range nodes {
		if sameParent(node.ParentID, parentID) && node.SortOrder >= order {
			order = node.SortOrder + 1
		}
	}
	return order
}

// sameParent compares two optional parent ids.
func sameParent(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// copyInt64Ptr returns a copy of an optional id.
func copyInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// touchNode stamps an update actor and time before a revision bump.
func touchNode(node domain.CaseNode, actor int64, now time.Time) domain.CaseNode {
	node.UpdatedBy = actor
	node.UpdatedAt = now
	return node
}

// normalizeIDs validates and deduplicates a list of application ids while
// preserving order.
func normalizeIDs(ids []int64) ([]int64, error) {
	normalized := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, domain.NewInvalid("requirementIds", "unconfirmed_requirements",
				fmt.Sprintf("requirement id %d is not a positive id", id))
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized, nil
}

// validateTitle trims and bounds a node title.
func validateTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", domain.NewInvalid("title", "invalid_title", "title must not be empty")
	}
	if utf8.RuneCountInString(trimmed) > maxNodeTitleRunes {
		return "", domain.NewInvalid("title", "invalid_title",
			fmt.Sprintf("title must be at most %d characters", maxNodeTitleRunes))
	}
	return trimmed, nil
}

// validateRunes bounds one optional text field.
func validateRunes(field, value string, max int) error {
	if utf8.RuneCountInString(value) > max {
		return domain.NewInvalid(field, "invalid_"+field,
			fmt.Sprintf("%s must be at most %d characters", field, max))
	}
	return nil
}

// validatePriority enforces the P0..P3 priority set.
func validatePriority(priority string) error {
	switch priority {
	case domain.PriorityP0, domain.PriorityP1, domain.PriorityP2, domain.PriorityP3:
		return nil
	default:
		return domain.NewInvalid("priority", "invalid_priority",
			fmt.Sprintf("priority %q is not P0, P1, P2 or P3", priority))
	}
}

// normalizeTestData validates and copies an optional JSONB document.
func normalizeTestData(raw []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	if !json.Valid(raw) {
		return nil, domain.NewInvalid("testData", "invalid_test_data", "testData must be valid JSON")
	}
	return append([]byte(nil), raw...), nil
}

// validNodeSource reports whether source is a node/source content origin.
func validNodeSource(source string) bool {
	switch source {
	case domain.NodeSourceUserInput, domain.NodeSourceAIGenerated, domain.NodeSourceAISupplement:
		return true
	default:
		return false
	}
}

// validateRefKind enforces the case_source.ref_kind set.
func validateRefKind(kind string) error {
	switch kind {
	case domain.RefRequirement, domain.RefAppModel, domain.RefAIRun, domain.RefManual:
		return nil
	default:
		return domain.NewInvalid("refKind", "invalid_source",
			fmt.Sprintf("refKind %q is not requirement, app_model, ai_run or manual", kind))
	}
}

// validConfidence reports whether confidence is a source confidence level.
func validConfidence(confidence string) bool {
	switch confidence {
	case domain.ConfidenceHigh, domain.ConfidenceMedium, domain.ConfidenceLow:
		return true
	default:
		return false
	}
}

// validateStepFields bounds the required and optional step text of a new step.
func validateStepFields(action, expected, data string) error {
	if strings.TrimSpace(action) == "" {
		return invalidCaseTree("step action must not be empty")
	}
	if err := validateRunes("action", action, maxStepActionRunes); err != nil {
		return err
	}
	if err := validateRunes("expected", expected, maxStepExpectedRunes); err != nil {
		return err
	}
	return validateRunes("data", data, maxStepDataRunes)
}

// validateAssertionFields enforces the required assertion fields.
func validateAssertionFields(kind, target, expression, expected string) error {
	if err := validateAssertionKind(kind); err != nil {
		return err
	}
	if strings.TrimSpace(expression) == "" {
		return invalidCaseTree("assertion expression must not be empty")
	}
	if err := validateRunes("expression", expression, maxAssertionExprRunes); err != nil {
		return err
	}
	if err := validateRunes("target", target, maxAssertionTargetRunes); err != nil {
		return err
	}
	return validateRunes("expected", expected, maxAssertionExpectRunes)
}

// validateAssertionKind enforces the case_assertion.kind set.
func validateAssertionKind(kind string) error {
	switch kind {
	case domain.AssertionKindUI, domain.AssertionKindAPI, domain.AssertionKindDB, domain.AssertionKindText:
		return nil
	default:
		return domain.NewInvalid("kind", "invalid_assertion_kind",
			fmt.Sprintf("assertion kind %q is not ui, api, db or text", kind))
	}
}

// truncateRunes truncates value to at most max runes.
func truncateRunes(value string, max int) string {
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

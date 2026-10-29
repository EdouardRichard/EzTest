package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// P7 mindmap constants: the two supplement run kinds of spec §4.2.7/§7.6, the
// supplement-agent profile seed (spec §9.1) and its prompt version (§16.2).
// The supplement/incremental_design write paths are owned here; the HTTP and
// agent-tool layers only call them.
const (
	RunKindSupplement        = "supplement"
	RunKindIncrementalDesign = "incremental_design"

	ProfileKeySupplement     = "supplement-agent"
	PromptVersionSupplement  = "p7-supplement-v1"
	supplementRunModePost    = "post_batch"
	supplementRunModeDesign  = "incremental_design"
	continuePhaseDesigning   = "designing"
	continuePhaseExecuting   = "executing"
	supplementMaxCases       = 5
	supplementMaxAffected    = 20
	supplementMaxEscalations = 3
)

// versionStatusExecuting and versionStatusContinuing are the P7 version states in
// which the supplement write paths are allowed (spec §5.2/§5.3). The mindmap
// context does not import the version domain, so the strings are mirrored here
// like versionStatusDesigning/reviewing are in graph_service.go.
const (
	versionStatusExecuting  = "executing"
	versionStatusContinuing = "continuing"
)

// ContinuePhaseReader is the optional P7 extension of VersionLifecycle: the
// version adapter exposes the continuing phase when the P3 read model carries
// it. The run gate and the incremental plan use it when present; a legacy
// adapter without the extension keeps the status-only behavior.
type ContinuePhaseReader interface {
	ContinuePhase(ctx context.Context, versionID int64) (string, error)
}

// SupplementBatch is the execution run_batch projection the P7 run gates need:
// the parent batch of a supplement/incremental_design run must belong to the
// version and be the latest terminal batch (spec §5.3/§7.6).
type SupplementBatch struct {
	ID        int64
	VersionID int64
	Kind      string
	Status    string
}

// SupplementBatchReader is the optional P7 read port into the execution batch
// table. The mindmap context owns no batch table, so the parent-batch
// preconditions of spec §7.6 are enforced only when the composition root wires
// this port (T14/T16/T17 supply it). Without it the gates degrade with the
// batch_precondition_unavailable warning instead of silently bypassing the
// checks. Implementations return ErrSupplementBatchNotFound for an unknown id
// or a version without a terminal batch.
type SupplementBatchReader interface {
	GetBatch(ctx context.Context, batchID int64) (SupplementBatch, error)
	LatestTerminalBatch(ctx context.Context, versionID int64) (SupplementBatch, error)
}

// ErrSupplementBatchNotFound is the SupplementBatchReader sentinel for an
// unknown batch or a version without a terminal batch.
var ErrSupplementBatchNotFound = errors.New("mindmap: supplement batch not found")

// P7 batch-precondition warnings. They are shared by the run gate (recorded in
// the run stats) and the incremental plan (exposed in its warnings).
const (
	planWarningBatchUnavailable   = "batch_precondition_unavailable"
	planWarningInvalidParentBatch = "invalid_parent_batch"
)

// supplementBatchTerminal reports whether a run_batch status is terminal (spec
// §6.2: done, failed or cancelled).
func supplementBatchTerminal(status string) bool {
	switch status {
	case "done", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// supplementManualRevalidationConfidence is the confidence reported when a user
// (or a non-AI path) marks a case for revalidation: there is no proposal
// confidence, so the spec default medium is used.
const supplementManualRevalidationConfidence = domain.ConfidenceMedium

// SupplementStep is one proposed step of a supplement node.
type SupplementStep struct {
	Action   string `json:"action"`
	Expected string `json:"expected"`
	Data     string `json:"data"`
}

// SupplementAssertion is one proposed assertion of a supplement node. StepIndex
// is the optional 0-based index into the case's steps.
type SupplementAssertion struct {
	StepIndex  *int   `json:"stepIndex"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Expression string `json:"expression"`
	Expected   string `json:"expected"`
}

// SupplementNodeCommand is the append-only supplement write of spec §5.2: a new
// case node with its steps, assertions and sources. It is accepted only after the
// case baseline froze the version; it never edits an existing node.
type SupplementNodeCommand struct {
	ParentID       *int64
	Title          string
	Description    string
	Priority       string
	Preconditions  string
	TestData       []byte
	RequirementIDs []int64
	Steps          []SupplementStep
	Assertions     []SupplementAssertion
	// Confidence is the proposed confidence of the requirement sources; an empty
	// value defaults to medium.
	Confidence string
	// RunID is the optional supplement run that produced the node; when positive
	// an ai_run traceability source is written too.
	RunID int64
}

// supplementCaseProposal is one supplement.case_propose entry accumulated in the
// run result (spec §9.2). Ids inside the payload are decimal strings (D29).
type supplementCaseProposal struct {
	Title          string                 `json:"title"`
	Description    string                 `json:"description"`
	Priority       string                 `json:"priority"`
	Preconditions  string                 `json:"preconditions"`
	TestData       json.RawMessage        `json:"testData"`
	RequirementIDs []flexID               `json:"requirementIds"`
	Steps          []agentDesignStep      `json:"steps"`
	Assertions     []agentDesignAssertion `json:"assertions"`
	Confidence     string                 `json:"confidence"`
}

// supplementAffectedProposal is one supplement.affected_cases_propose entry
// (caseNodeId) or one materialized affected record (nodeId); both shapes are
// tolerated so the plan can read a running run as well as a materialized one.
type supplementAffectedProposal struct {
	NodeID         flexID              `json:"nodeId"`
	CaseNodeID     flexID              `json:"caseNodeId"`
	Confidence     string              `json:"confidence"`
	Reason         string              `json:"reason"`
	Source         string              `json:"source"`
	RequirementIDs []flexID            `json:"requirementIds"`
	CodeRefs       []supplementCodeRef `json:"codeRefs"`
}

// supplementCodeRef is one affected-case code reference of the
// supplement.affected_cases_propose payload (spec §5.8/§9.2).
type supplementCodeRef struct {
	Path   string `json:"path"`
	Line   *int   `json:"line"`
	Reason string `json:"reason"`
}

// codePaths returns the repository-relative paths of the code refs, used for the
// event refs.code list (spec §8.1).
func (p supplementAffectedProposal) codePaths() []string {
	paths := make([]string, 0, len(p.CodeRefs))
	for _, ref := range p.CodeRefs {
		if ref.Path != "" {
			paths = append(paths, ref.Path)
		}
	}
	return paths
}

// nodeID resolves the affected entry's target id across the proposal and the
// materialized shapes.
func (p supplementAffectedProposal) nodeID() int64 {
	if p.NodeID.Int64() > 0 {
		return p.NodeID.Int64()
	}
	return p.CaseNodeID.Int64()
}

// supplementEscalation is one supplement.escalate entry: a requirement gap that
// T16 materializes as a delivery issue. T9 only validates and stores it.
type supplementEscalation struct {
	Title          string   `json:"title"`
	Detail         string   `json:"detail"`
	RequirementIDs []flexID `json:"requirementIds"`
}

// supplementUnmappedChange is one P4 change the supplement run could not map to
// a case (spec §7.6).
type supplementUnmappedChange struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

// supplementSubmission is the run result document. It is tolerant: the agent
// tools store the proposal shape, the materializer rewrites it into the
// materialized shape, and the incremental plan reads either. Fields that the
// materializer produces are only meaningful when Materialized is true.
type supplementSubmission struct {
	Materialized     bool                         `json:"materialized"`
	NewCaseIDs       []flexID                     `json:"newCaseIds"`
	DuplicateSkipped int                          `json:"duplicateSkipped"`
	Cases            []supplementCaseProposal     `json:"cases"`
	AffectedCases    []supplementAffectedProposal `json:"affectedCases"`
	Escalations      []supplementEscalation       `json:"escalations"`
	UnmappedChanges  []supplementUnmappedChange   `json:"unmappedChanges"`
	// ImpactAvailable is the explicit P4 availability flag the materializer
	// writes (and later accepts) so an absent impact result stays distinguishable
	// from an empty one (spec §7.6 impact_unavailable).
	ImpactAvailable *bool                    `json:"impactAvailable"`
	Warnings        []IncrementalPlanWarning `json:"warnings"`
	Summary         string                   `json:"summary"`
	Degraded        bool                     `json:"degraded"`
}

// parseSupplementSubmission decodes a run result document, tolerating an empty
// document. A malformed document is returned as an error so the caller can turn
// it into a warning instead of failing the run.
func parseSupplementSubmission(raw []byte) (supplementSubmission, error) {
	var submission supplementSubmission
	if len(strings.TrimSpace(string(raw))) == 0 {
		return submission, nil
	}
	if err := json.Unmarshal(raw, &submission); err != nil {
		return supplementSubmission{}, err
	}
	return submission, nil
}

// preparedSupplementNode is one validated supplement proposal ready to be
// written; key is the canonical supplement_key of spec §5.2.
type preparedSupplementNode struct {
	parentID       *int64
	title          string
	description    string
	priority       string
	preconditions  string
	testData       []byte
	requirementIDs []int64
	steps          []SupplementStep
	assertions     []SupplementAssertion
	confidence     string
	key            string
}

// prepareSupplementNode validates one append proposal and computes its dedupe
// key. confirmed is the set of clarified requirement ids of the version.
func prepareSupplementNode(versionID int64, cmd SupplementNodeCommand, confirmed map[int64]struct{}) (preparedSupplementNode, error) {
	var prepared preparedSupplementNode
	title, err := validateTitle(cmd.Title)
	if err != nil {
		return prepared, err
	}
	if err := validateRunes("description", cmd.Description, maxNodeDescriptionRunes); err != nil {
		return prepared, err
	}
	if err := validateRunes("preconditions", cmd.Preconditions, maxNodeDescriptionRunes); err != nil {
		return prepared, err
	}
	priority := cmd.Priority
	if priority == "" {
		priority = defaultNodePriority
	}
	if err := validatePriority(priority); err != nil {
		return prepared, err
	}
	testData, err := normalizeTestData(cmd.TestData)
	if err != nil {
		return prepared, err
	}
	if cmd.ParentID != nil && *cmd.ParentID <= 0 {
		return prepared, domain.NewInvalid("parentId", "invalid_case_tree", "parentId must be a positive id")
	}
	if len(cmd.Steps) == 0 {
		return prepared, domain.NewInvalid("steps", "invalid_supplement_case",
			"a supplement case needs at least one step")
	}
	if len(cmd.Steps) > domain.MaxStepsPerCase {
		return prepared, domain.NewInvalid("steps", "invalid_supplement_case",
			fmt.Sprintf("a supplement case has at most %d steps", domain.MaxStepsPerCase))
	}
	for index, step := range cmd.Steps {
		if err := validateStepFields(step.Action, step.Expected, step.Data); err != nil {
			return prepared, domain.NewInvalid("steps", "invalid_supplement_case",
				fmt.Sprintf("steps[%d]: %v", index, err))
		}
	}
	if len(cmd.Assertions) == 0 {
		return prepared, domain.NewInvalid("assertions", "invalid_supplement_case",
			"a supplement case needs at least one assertion")
	}
	for index, assertion := range cmd.Assertions {
		if assertion.StepIndex != nil && (*assertion.StepIndex < 0 || *assertion.StepIndex >= len(cmd.Steps)) {
			return prepared, domain.NewInvalid("assertions", "invalid_supplement_case",
				fmt.Sprintf("assertions[%d].stepIndex %d is outside the %d steps", index, *assertion.StepIndex, len(cmd.Steps)))
		}
		if err := validateAssertionFields(assertion.Kind, assertion.Target, assertion.Expression, assertion.Expected); err != nil {
			return prepared, domain.NewInvalid("assertions", "invalid_supplement_case",
				fmt.Sprintf("assertions[%d]: %v", index, err))
		}
	}
	requirementIDs, err := normalizeIDs(cmd.RequirementIDs)
	if err != nil {
		return prepared, err
	}
	if len(requirementIDs) == 0 {
		return prepared, domain.NewInvalid("requirementIds", "unconfirmed_requirements",
			"a supplement case must reference at least one clarified requirement")
	}
	var missing []int64
	for _, requirementID := range requirementIDs {
		if _, ok := confirmed[requirementID]; !ok {
			missing = append(missing, requirementID)
		}
	}
	if len(missing) > 0 {
		return prepared, domain.NewInvalid("requirementIds", "unconfirmed_requirements",
			fmt.Sprintf("supplement requirements %v are not clarified", missing))
	}
	confidence := cmd.Confidence
	if confidence == "" {
		confidence = defaultConfidence
	}
	if !validConfidence(confidence) {
		return prepared, domain.NewInvalid("confidence", "invalid_confidence",
			fmt.Sprintf("confidence %q is not high, medium or low", confidence))
	}

	prepared = preparedSupplementNode{
		parentID:       copyInt64Ptr(cmd.ParentID),
		title:          title,
		description:    cmd.Description,
		priority:       priority,
		preconditions:  cmd.Preconditions,
		testData:       testData,
		requirementIDs: requirementIDs,
		steps:          append([]SupplementStep(nil), cmd.Steps...),
		assertions:     append([]SupplementAssertion(nil), cmd.Assertions...),
		confidence:     confidence,
	}
	prepared.key = supplementKeyForProposal(versionID, prepared)
	return prepared, nil
}

// supplementKeyStep and supplementKeyAssertion are the canonical pieces of the
// supplement_key (spec §5.2: version | normalized_title | sorted steps | sorted
// assertions).
type supplementKeyStep struct {
	action   string
	expected string
	data     string
}

type supplementKeyAssertion struct {
	kind       string
	stepSeq    int
	target     string
	expression string
	expected   string
}

// supplementKey hashes the canonical supplement identity. Steps and assertions
// are intentionally sorted so a reordered but equivalent proposal dedupes.
func supplementKey(versionID int64, title string, steps []supplementKeyStep, assertions []supplementKeyAssertion) string {
	stepLines := make([]string, 0, len(steps))
	for _, step := range steps {
		stepLines = append(stepLines, strings.Join([]string{step.action, step.expected, step.data}, "\x1f"))
	}
	sort.Strings(stepLines)
	assertionLines := make([]string, 0, len(assertions))
	for _, assertion := range assertions {
		assertionLines = append(assertionLines, strings.Join([]string{
			assertion.kind, strconv.Itoa(assertion.stepSeq), assertion.target,
			assertion.expression, assertion.expected,
		}, "\x1f"))
	}
	sort.Strings(assertionLines)
	title = normalizeSupplementTitle(title)
	canonical := fmt.Sprintf("%d|%s|%s|%s", versionID, title,
		strings.Join(stepLines, "\n"), strings.Join(assertionLines, "\n"))
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// normalizeSupplementTitle lowercases and collapses the title whitespace for the
// dedupe key only; the stored title keeps its authored form.
func normalizeSupplementTitle(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// supplementKeyForProposal computes the key of one validated proposal.
func supplementKeyForProposal(versionID int64, prepared preparedSupplementNode) string {
	steps := make([]supplementKeyStep, 0, len(prepared.steps))
	for _, step := range prepared.steps {
		steps = append(steps, supplementKeyStep{action: step.Action, expected: step.Expected, data: step.Data})
	}
	assertions := make([]supplementKeyAssertion, 0, len(prepared.assertions))
	for _, assertion := range prepared.assertions {
		stepSeq := 0
		if assertion.StepIndex != nil {
			stepSeq = *assertion.StepIndex + 1
		}
		assertions = append(assertions, supplementKeyAssertion{
			kind: assertion.Kind, stepSeq: stepSeq, target: assertion.Target,
			expression: assertion.Expression, expected: assertion.Expected,
		})
	}
	return supplementKey(versionID, prepared.title, steps, assertions)
}

// supplementKeyIndex computes the supplement_key of every existing case node of
// the version so a new proposal can be deduped against the whole graph.
func supplementKeyIndex(versionID int64, state graphState) map[string]int64 {
	index := make(map[string]int64, len(state.nodes))
	for _, node := range state.nodes {
		if node.Kind != domain.NodeKindCase {
			continue
		}
		nodeSteps := state.steps[node.ID]
		stepSeq := make(map[int64]int, len(nodeSteps))
		steps := make([]supplementKeyStep, 0, len(nodeSteps))
		for _, step := range nodeSteps {
			stepSeq[step.ID] = step.Seq
			steps = append(steps, supplementKeyStep{action: step.Action, expected: step.Expected, data: step.Data})
		}
		nodeAssertions := state.assertions[node.ID]
		assertions := make([]supplementKeyAssertion, 0, len(nodeAssertions))
		for _, assertion := range nodeAssertions {
			seq := 0
			if assertion.StepID != nil {
				seq = stepSeq[*assertion.StepID]
			}
			assertions = append(assertions, supplementKeyAssertion{
				kind: assertion.Kind, stepSeq: seq, target: assertion.Target,
				expression: assertion.Expression, expected: assertion.Expected,
			})
		}
		index[supplementKey(versionID, node.Title, steps, assertions)] = node.ID
	}
	return index
}

// supplementGraphWriter builds supplement graph rows in memory against one
// transaction's graph state. The caller persists the collected rows and publishes
// the events only after the commit; the writer itself never writes the database
// or the baseline.
type supplementGraphWriter struct {
	versionID int64
	actor     int64
	now       time.Time
	ids       IDGenerator
	state     *graphState
	keys      map[string]int64

	nodes      []domain.CaseNode
	steps      []domain.CaseStep
	assertions []domain.CaseAssertion
	sources    []domain.CaseSource
}

// newSupplementGraphWriter indexes the current graph for dedupe.
func newSupplementGraphWriter(versionID, actor int64, now time.Time, ids IDGenerator, state *graphState) *supplementGraphWriter {
	return &supplementGraphWriter{
		versionID: versionID,
		actor:     actor,
		now:       now,
		ids:       ids,
		state:     state,
		keys:      supplementKeyIndex(versionID, *state),
	}
}

// nextID mints one application-generated Snowflake id (D29).
func (w *supplementGraphWriter) nextID() (int64, error) {
	if w.ids == nil {
		return 0, errors.New("mindmap: id generator is not configured")
	}
	id := w.ids.Next()
	if id <= 0 {
		return 0, errors.New("mindmap: id generator returned a non-positive id")
	}
	return id, nil
}

// append adds one validated supplement case to the in-memory graph. A duplicate
// is the stable 409 duplicate_supplement; a full graph is graph_too_large.
func (w *supplementGraphWriter) append(prepared preparedSupplementNode, runID int64) (domain.CaseNode, error) {
	if len(w.state.nodes) >= maxGraphNodes {
		return domain.CaseNode{}, graphTooLargeError(w.versionID, len(w.state.nodes))
	}
	if existingID, duplicate := w.keys[prepared.key]; duplicate {
		return domain.CaseNode{}, domain.NewStateConflict("duplicate_supplement",
			fmt.Sprintf("case %q duplicates node %d of version %d", prepared.title, existingID, w.versionID))
	}
	if prepared.parentID != nil {
		parent, ok := findNode(w.state.nodes, *prepared.parentID)
		if !ok || parent.VersionID != w.versionID {
			return domain.CaseNode{}, domain.NewNotFound("node", *prepared.parentID)
		}
		if parent.Kind != domain.NodeKindScenario {
			return domain.CaseNode{}, invalidCaseTree("case node %d must not have children", parent.ID)
		}
	}
	nodeID, err := w.nextID()
	if err != nil {
		return domain.CaseNode{}, err
	}
	node := domain.CaseNode{
		ID:            nodeID,
		VersionID:     w.versionID,
		ParentID:      copyInt64Ptr(prepared.parentID),
		Kind:          domain.NodeKindCase,
		Title:         prepared.title,
		Description:   prepared.description,
		Priority:      prepared.priority,
		Preconditions: prepared.preconditions,
		TestData:      prepared.testData,
		Source:        domain.NodeSourceAISupplement,
		Status:        domain.NodeStatusSupplemented,
		SortOrder:     nextSortOrder(w.state.nodes, prepared.parentID),
		Revision:      1,
		CreatedBy:     w.actor,
		UpdatedBy:     w.actor,
		CreatedAt:     w.now,
		UpdatedAt:     w.now,
	}

	steps := make([]domain.CaseStep, 0, len(prepared.steps))
	for index, proposalStep := range prepared.steps {
		stepID, err := w.nextID()
		if err != nil {
			return domain.CaseNode{}, err
		}
		step := domain.CaseStep{
			ID:        stepID,
			NodeID:    node.ID,
			Seq:       index + 1,
			Action:    proposalStep.Action,
			Expected:  proposalStep.Expected,
			Data:      proposalStep.Data,
			Source:    domain.StepSourceAIGenerated,
			CreatedAt: w.now,
			UpdatedAt: w.now,
		}
		steps = append(steps, step)
	}
	assertions := make([]domain.CaseAssertion, 0, len(prepared.assertions))
	for _, proposalAssertion := range prepared.assertions {
		assertionID, err := w.nextID()
		if err != nil {
			return domain.CaseNode{}, err
		}
		var stepID *int64
		if proposalAssertion.StepIndex != nil {
			linked := steps[*proposalAssertion.StepIndex].ID
			stepID = &linked
		}
		assertions = append(assertions, domain.CaseAssertion{
			ID:         assertionID,
			NodeID:     node.ID,
			StepID:     stepID,
			Kind:       proposalAssertion.Kind,
			Target:     proposalAssertion.Target,
			Expression: proposalAssertion.Expression,
			Expected:   proposalAssertion.Expected,
			Source:     domain.StepSourceAIGenerated,
			CreatedAt:  w.now,
			UpdatedAt:  w.now,
		})
	}
	sources := make([]domain.CaseSource, 0, len(prepared.requirementIDs)+1)
	for _, requirementID := range prepared.requirementIDs {
		sourceID, err := w.nextID()
		if err != nil {
			return domain.CaseNode{}, err
		}
		reference := requirementID
		sources = append(sources, domain.CaseSource{
			ID:         sourceID,
			VersionID:  w.versionID,
			NodeID:     node.ID,
			RefKind:    domain.RefRequirement,
			RefID:      &reference,
			Source:     domain.NodeSourceAISupplement,
			Confidence: prepared.confidence,
			CreatedAt:  w.now,
			UpdatedAt:  w.now,
		})
	}
	if runID > 0 {
		sourceID, err := w.nextID()
		if err != nil {
			return domain.CaseNode{}, err
		}
		reference := runID
		sources = append(sources, domain.CaseSource{
			ID:         sourceID,
			VersionID:  w.versionID,
			NodeID:     node.ID,
			RefKind:    domain.RefAIRun,
			RefID:      &reference,
			Source:     domain.NodeSourceAISupplement,
			Confidence: domain.ConfidenceHigh,
			CreatedAt:  w.now,
			UpdatedAt:  w.now,
		})
	}

	w.state.nodes = append(w.state.nodes, node)
	for _, step := range steps {
		w.state.steps[node.ID] = append(w.state.steps[node.ID], step)
	}
	for _, assertion := range assertions {
		w.state.assertions[node.ID] = append(w.state.assertions[node.ID], assertion)
	}
	w.state.sources = append(w.state.sources, sources...)
	w.keys[prepared.key] = node.ID

	w.nodes = append(w.nodes, node)
	w.steps = append(w.steps, steps...)
	w.assertions = append(w.assertions, assertions...)
	w.sources = append(w.sources, sources...)
	return node, nil
}

// confirmRequirements loads the clarified requirements of the version and returns
// them as a set the supplement validators can check requirement ids against.
func (s *GraphService) confirmRequirements(ctx context.Context, versionID int64) (map[int64]struct{}, error) {
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
	return confirmed, nil
}

// AppendSupplementNode is the first baseline-guard exception of spec §5.2: it
// inserts one new ai_supplement case with its steps, assertions and sources after
// the baseline froze the version. It never edits an existing node and never
// writes case_baseline or the frozen graph hash.
func (s *GraphService) AppendSupplementNode(ctx context.Context, versionID, actor int64, cmd SupplementNodeCommand) (domain.CaseNode, error) {
	if s.deps.Versions == nil || s.deps.Graph == nil || s.deps.Baselines == nil ||
		s.deps.Nodes == nil || s.deps.Steps == nil || s.deps.Assertions == nil ||
		s.deps.Sources == nil || s.deps.Requirements == nil || s.deps.IDs == nil {
		return domain.CaseNode{}, errors.New("mindmap: supplement write ports are not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return domain.CaseNode{}, err
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return domain.CaseNode{}, err
	}
	if baseline == nil {
		return domain.CaseNode{}, supplementBaselineRequiredError(versionID)
	}
	if err := supplementVersionWritable(version); err != nil {
		return domain.CaseNode{}, err
	}
	confirmed, err := s.confirmRequirements(ctx, versionID)
	if err != nil {
		return domain.CaseNode{}, err
	}
	prepared, err := prepareSupplementNode(versionID, cmd, confirmed)
	if err != nil {
		return domain.CaseNode{}, err
	}

	now := s.now()
	var created domain.CaseNode
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if tx.Nodes() == nil || tx.Steps() == nil || tx.Assertions() == nil || tx.Sources() == nil {
			return errors.New("mindmap: graph transaction repositories are not configured")
		}
		if err := s.guardSupplementTx(ctx, tx, versionID); err != nil {
			return err
		}
		state, err := loadGraphStateTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		writer := newSupplementGraphWriter(versionID, actor, now, s.deps.IDs, &state)
		created, err = writer.append(prepared, cmd.RunID)
		if err != nil {
			return err
		}
		if err := validateGraphState(state); err != nil {
			return err
		}
		if _, err := tx.Nodes().Create(ctx, created); err != nil {
			return err
		}
		for _, step := range writer.steps {
			if _, err := tx.Steps().Create(ctx, step); err != nil {
				return err
			}
		}
		for _, assertion := range writer.assertions {
			if _, err := tx.Assertions().Create(ctx, assertion); err != nil {
				return err
			}
		}
		for _, source := range writer.sources {
			if _, err := tx.Sources().Create(ctx, source); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.CaseNode{}, err
	}
	s.publishNode(ctx, domain.EventCaseCreated, versionID, actor, created, "")
	s.publishSupplementCreated(ctx, versionID, actor, created, prepared, cmd.RunID)
	return created, nil
}

// TransitionNodeStatus is the second baseline-guard exception of spec §5.2: it
// only moves status/updated_at/updated_by of one case node along the P7 matrix.
// The content, steps, assertions and sources are never touched, and the baseline
// is never written.
func (s *GraphService) TransitionNodeStatus(ctx context.Context, versionID, nodeID, actor int64, to string) (domain.CaseNode, error) {
	if s.deps.Versions == nil || s.deps.Graph == nil || s.deps.Baselines == nil {
		return domain.CaseNode{}, errors.New("mindmap: supplement write ports are not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return domain.CaseNode{}, err
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return domain.CaseNode{}, err
	}
	if baseline == nil {
		return domain.CaseNode{}, supplementBaselineRequiredError(versionID)
	}
	if err := supplementVersionWritable(version); err != nil {
		return domain.CaseNode{}, err
	}

	now := s.now()
	var (
		updated         domain.CaseNode
		from            string
		requirementIDs  []int64
		revalidationSrc string
	)
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if tx.Nodes() == nil {
			return errors.New("mindmap: graph transaction repositories are not configured")
		}
		if err := s.guardSupplementTx(ctx, tx, versionID); err != nil {
			return err
		}
		node, err := tx.Nodes().Get(ctx, versionID, nodeID)
		if err != nil {
			return err
		}
		if node.Kind != domain.NodeKindCase {
			return domain.NewInvalid("nodeId", "invalid_case_ids",
				fmt.Sprintf("node %d is a scenario; status transitions apply to cases", nodeID))
		}
		from = node.Status
		if !domain.CanTransitionNodeStatus(from, to) {
			return domain.NewStateConflict("case_status_conflict",
				fmt.Sprintf("case %d cannot move from %s to %s", nodeID, from, to))
		}
		if to == domain.NodeStatusNeedsRevalidation {
			// The revalidation event carries the requirement refs of the case
			// (spec §8.1: refs = case, requirement/code). A manual mark has no
			// affected-case proposal, so requirement refs are the best available
			// provenance and the source falls back to code when there are none.
			if tx.Sources() != nil {
				sources, err := tx.Sources().ListByVersion(ctx, versionID)
				if err != nil {
					return err
				}
				requirementIDs = requirementIDsOfCase(sources, nodeID)
			}
			revalidationSrc = supplementRevalidationSource(requirementIDs)
		}
		candidate := node
		candidate.Status = to
		candidate.UpdatedBy = actor
		candidate.UpdatedAt = now
		updated, err = tx.Nodes().Update(ctx, candidate, node.Revision)
		return err
	})
	if err != nil {
		return domain.CaseNode{}, err
	}
	s.publishStatusTransition(ctx, versionID, actor, updated, from, to, requirementIDs, revalidationSrc)
	return updated, nil
}

// supplementRevalidationSource maps the available references of a manual
// revalidation mark onto the spec §8.1 source enum (requirement/code).
func supplementRevalidationSource(requirementIDs []int64) string {
	if len(requirementIDs) > 0 {
		return "requirement"
	}
	return "code"
}

// guardSupplementTx re-checks the frozen baseline and the writable version state
// inside the write transaction, so a freeze/finalize committed after the
// pre-check still rejects the write.
func (s *GraphService) guardSupplementTx(ctx context.Context, tx GraphTx, versionID int64) error {
	if tx.Baselines() == nil {
		return errors.New("mindmap: graph transaction baseline repository is not configured")
	}
	baseline, err := loadBaseline(ctx, tx.Baselines(), versionID)
	if err != nil {
		return err
	}
	if baseline == nil {
		return supplementBaselineRequiredError(versionID)
	}
	if s.deps.Versions == nil {
		return errors.New("mindmap: version lifecycle is not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return err
	}
	return supplementVersionWritable(version)
}

// supplementVersionWritable reports whether the version accepts the P7 supplement
// write paths: only executing (post-batch) and continuing (incremental design)
// versions do.
func supplementVersionWritable(version VersionSnapshot) error {
	switch version.Status {
	case versionStatusExecuting, versionStatusContinuing:
		return nil
	default:
		return domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s; supplement writes need executing or continuing",
				version.ID, version.Status))
	}
}

// supplementBaselineRequiredError is the stable 409 of a supplement write on an
// unfrozen version.
func supplementBaselineRequiredError(versionID int64) error {
	return domain.NewStateConflict("baseline_required",
		fmt.Sprintf("version %d has no frozen baseline; supplement writes require one", versionID))
}

// publishSupplementCreated emits case.supplemented after the insert committed.
// The detail carries the spec §8.1 fields only: decimal-string ids, the source
// and the requirement count (D29/D30; no case body).
func (s *GraphService) publishSupplementCreated(ctx context.Context, versionID, actor int64, node domain.CaseNode, prepared preparedSupplementNode, runID int64) {
	detail := map[string]any{
		"nodeId":           strconv.FormatInt(node.ID, 10),
		"source":           node.Source,
		"requirementCount": len(prepared.requirementIDs),
		"steps":            len(prepared.steps),
		"assertions":       len(prepared.assertions),
	}
	if runID > 0 {
		detail["runId"] = strconv.FormatInt(runID, 10)
	}
	s.publish(ctx, Event{
		VersionID:  versionID,
		Type:       domain.EventCaseSupplemented,
		Summary:    fmt.Sprintf("case %q supplemented", truncateRunes(node.Title, maxEventTitleRunes)),
		RefCaseIDs: []int64{node.ID},
		ActorID:    actor,
		Source:     node.Source,
		Detail:     detail,
	})
}

// publishStatusTransition emits case.status_changed and, when the target is
// needs_revalidation, the additional case.revalidation_marked event (spec §5.2).
// The revalidation detail carries the spec §8.1 fields {nodeId,confidence,
// source} and its envelope refs include the requirement ids; a manual mark uses
// the medium confidence default.
func (s *GraphService) publishStatusTransition(ctx context.Context, versionID, actor int64, node domain.CaseNode, from, to string, requirementIDs []int64, revalidationSource string) {
	s.publish(ctx, Event{
		VersionID:  versionID,
		Type:       domain.EventCaseStatusChanged,
		Summary:    fmt.Sprintf("case status changed %s -> %s", from, to),
		RefCaseIDs: []int64{node.ID},
		ActorID:    actor,
		Source:     node.Source,
		Detail: map[string]any{
			"nodeId":   strconv.FormatInt(node.ID, 10),
			"from":     from,
			"to":       to,
			"revision": node.Revision,
		},
	})
	if to != domain.NodeStatusNeedsRevalidation {
		return
	}
	detail := map[string]any{
		"nodeId":     strconv.FormatInt(node.ID, 10),
		"confidence": supplementManualRevalidationConfidence,
	}
	if revalidationSource != "" {
		detail["source"] = revalidationSource
	}
	s.publish(ctx, Event{
		VersionID:         versionID,
		Type:              domain.EventCaseRevalidationMarked,
		Summary:           "case marked for revalidation",
		RefCaseIDs:        []int64{node.ID},
		RefRequirementIDs: requirementIDs,
		ActorID:           actor,
		Source:            node.Source,
		Detail:            detail,
	})
}

package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Materialization limits of spec §6.6/§6.7/§6.8.
const (
	// materializeMaxPayloadBytes is the 1 MiB submission cap (spec §6.7 rule 5).
	materializeMaxPayloadBytes = 1 << 20
	// materializeMaxCases is the per-design-run case cap (spec §6.7 rule 5).
	materializeMaxCases = 60
	// materializeMaxFindings is the per-review finding cap (spec §8.2).
	materializeMaxFindings = 200
	// materializeMaxFindingDetailRunes bounds one finding detail (spec §8.2).
	materializeMaxFindingDetailRunes = 2000
	// materializeMaxDataPlanItems is the per-data-plan item cap (spec §8.2).
	materializeMaxDataPlanItems = 60
	// materializeMaxValidationErrors is the reported validation-error cap.
	materializeMaxValidationErrors = 20
)

// flexID accepts a decimal id encoded either as a JSON string (D29) or as a
// JSON number. Agent tool payloads use strings; the parser stays tolerant.
type flexID int64

// UnmarshalJSON decodes a decimal string or a number into the id.
func (f *flexID) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return errors.New("id must not be null")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return fmt.Errorf("id %q is not a decimal id: %w", text, err)
		}
		*f = flexID(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	value, err := number.Int64()
	if err != nil {
		return fmt.Errorf("id %s is not an integer: %w", number, err)
	}
	*f = flexID(value)
	return nil
}

// Int64 returns the decoded id.
func (f flexID) Int64() int64 { return int64(f) }

// agentDesignProposal is the case.graph_propose payload of spec §6.7.
type agentDesignProposal struct {
	Mode      string                `json:"mode"`
	Scenarios []agentDesignScenario `json:"scenarios"`
	Cases     []agentDesignCase     `json:"cases"`
}

// agentDesignScenario is one structural scenario of a proposal.
type agentDesignScenario struct {
	Ref         string  `json:"ref"`
	Title       string  `json:"title"`
	ParentRef   *string `json:"parentRef"`
	Description string  `json:"description"`
}

// agentDesignCase is one leaf case of a proposal.
type agentDesignCase struct {
	Ref            string                 `json:"ref"`
	ScenarioRef    *string                `json:"scenarioRef"`
	Title          string                 `json:"title"`
	Priority       string                 `json:"priority"`
	RequirementIDs []flexID               `json:"requirementIds"`
	Preconditions  string                 `json:"preconditions"`
	TestData       json.RawMessage        `json:"testData"`
	Steps          []agentDesignStep      `json:"steps"`
	Assertions     []agentDesignAssertion `json:"assertions"`
	Anchors        []agentDesignAnchor    `json:"anchors"`
}

// agentDesignStep is one proposed step.
type agentDesignStep struct {
	Action   string `json:"action"`
	Expected string `json:"expected"`
	Data     string `json:"data"`
}

// agentDesignAssertion is one proposed assertion. StepIndex is optional.
type agentDesignAssertion struct {
	StepIndex  *int   `json:"stepIndex"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Expression string `json:"expression"`
	Expected   string `json:"expected"`
}

// agentDesignAnchor is one proposed code anchor.
type agentDesignAnchor struct {
	File      string `json:"file"`
	StartLine *int   `json:"startLine"`
	EndLine   *int   `json:"endLine"`
}

// agentReviewProposal is the case.review_submit payload of spec §8.2.
type agentReviewProposal struct {
	Findings []agentReviewFinding `json:"findings"`
}

// agentReviewFinding is one proposed review finding.
type agentReviewFinding struct {
	NodeID   *flexID `json:"nodeId"`
	Category string  `json:"category"`
	Severity string  `json:"severity"`
	Detail   string  `json:"detail"`
}

// agentDataPlanProposal is the case.data_plan_submit payload of spec §8.2.
type agentDataPlanProposal struct {
	Items []agentDataPlanItem `json:"items"`
}

// agentDataPlanItem is one node's planned preconditions/test data.
type agentDataPlanItem struct {
	NodeID        flexID          `json:"nodeId"`
	NodeRevision  int             `json:"nodeRevision"`
	Preconditions *string         `json:"preconditions"`
	TestData      json.RawMessage `json:"testData"`
}

// runStoredInput is the persisted start command read back during materialization; it
// carries the explicit replace confirmation of spec §6.7 rule 6.
type runStoredInput struct {
	Kind           string  `json:"kind"`
	Mode           string  `json:"mode"`
	ConfirmReplace bool    `json:"confirmReplace"`
	NodeIDs        []int64 `json:"nodeIds"`
}

// runParseInput decodes a stored run input, tolerating an empty document.
func runParseInput(raw []byte) runStoredInput {
	var input runStoredInput
	if len(bytes.TrimSpace(raw)) == 0 {
		return input
	}
	_ = json.Unmarshal(raw, &input)
	return input
}

// Materialize dispatches one active run's structured submission by kind
// (spec §6.7/§6.8). It is idempotent per run: a run that is no longer active is
// already terminal, so the call is a no-op. The run is resolved through the
// active-run scan because the repository port has no get-by-id lookup.
func (s *RunService) Materialize(ctx context.Context, runID int64) error {
	if s.deps.Runs == nil {
		return errors.New("mindmap: run repository is not configured")
	}
	runs, err := s.deps.Runs.ListActive(ctx, runMaterializeScanLimit)
	if err != nil {
		return fmt.Errorf("mindmap: list active runs: %w", err)
	}
	for _, run := range runs {
		if run.ID == runID {
			return s.materializeRun(ctx, run)
		}
	}
	return nil
}

// materializeRun dispatches by run kind.
func (s *RunService) materializeRun(ctx context.Context, run domain.CaseDesignRun) error {
	switch run.Kind {
	case domain.RunKindDesign:
		return s.materializeDesign(ctx, run)
	case domain.RunKindReview:
		return s.materializeReview(ctx, run)
	case domain.RunKindDataPlan:
		return s.materializeDataPlan(ctx, run)
	case domain.RunKindExplore:
		// Explore runs are materialized by the exploration service (T13).
		return nil
	default:
		return s.rejectRun(ctx, run, "invalid_run_kind", []string{fmt.Sprintf("run %d has unknown kind %q", run.ID, run.Kind)})
	}
}

// rejectRun fails a run after a validation/business rejection and returns the
// corresponding domain error. Because the failure happens before any graph
// transaction, zero business rows are written and the version state is
// untouched (spec §6.7/§8.5).
func (s *RunService) rejectRun(ctx context.Context, run domain.CaseDesignRun, reason string, messages []string) error {
	messages = runCapValidationErrors(messages)
	if reason == "invalid_case_proposal" && len(messages) == 0 {
		messages = []string{"the agent did not submit a valid proposal"}
	}
	if err := s.failRun(ctx, run, reason, messages); err != nil {
		return err
	}
	message := strings.Join(messages, "; ")
	switch reason {
	case "baseline_frozen":
		return domain.NewStateConflict(reason, message)
	case "graph_too_large":
		return domain.NewConflict(reason, message)
	case "invalid_run_kind":
		return domain.NewInvalid("kind", reason, message)
	default:
		return domain.NewInvalid("payload", "invalid_case_proposal", message)
	}
}

// requireMaterializableVersion re-checks the version gate before a graph-writing
// materialization, so a design/data-plan run that outlived a concurrent approval
// cannot write into a frozen version (spec §6.4 rule 3). Review materialization
// stays allowed after freezing because it only writes review rows.
//
// The baseline row is checked independently of the version snapshot: after a
// partially failed approval the baseline may exist while the snapshot still looks
// editable, and in that stale approved/executing mismatch the version is frozen.
func (s *RunService) requireMaterializableVersion(ctx context.Context, run domain.CaseDesignRun) error {
	baseline, err := loadBaseline(ctx, s.deps.Baselines, run.VersionID)
	if err != nil {
		return fmt.Errorf("mindmap: read baseline of version %d for materialization: %w", run.VersionID, err)
	}
	if baseline != nil {
		return s.rejectRun(ctx, run, "baseline_frozen", []string{
			fmt.Sprintf("version %d has a frozen baseline; the mindmap is read-only", run.VersionID),
		})
	}
	if s.deps.Versions == nil {
		return nil
	}
	version, err := s.deps.Versions.GetVersion(ctx, run.VersionID)
	if err != nil {
		return fmt.Errorf("mindmap: read version %d for materialization: %w", run.VersionID, err)
	}
	switch version.Status {
	case versionStatusDesigning, versionStatusReviewing:
		return nil
	default:
		return s.rejectRun(ctx, run, "baseline_frozen", []string{
			fmt.Sprintf("version %d is %s; the mindmap is read-only", run.VersionID, version.Status),
		})
	}
}

// materializeDesign validates and writes a case.graph_propose submission in one
// graph transaction together with the run status CAS (spec §6.7). Anchors are
// validated before the transaction (file existence) and created after the
// commit through the code-intel port; events are published last.
func (s *RunService) materializeDesign(ctx context.Context, run domain.CaseDesignRun) error {
	if err := s.requireMaterializableVersion(ctx, run); err != nil {
		return err
	}
	if len(bytes.TrimSpace(run.Result)) == 0 {
		return s.rejectRun(ctx, run, "invalid_case_proposal", []string{"the agent submitted no proposal"})
	}
	if len(run.Result) > materializeMaxPayloadBytes {
		return s.rejectRun(ctx, run, "invalid_case_proposal",
			[]string{fmt.Sprintf("proposal is %d bytes; the limit is %d", len(run.Result), materializeMaxPayloadBytes)})
	}
	var proposal agentDesignProposal
	if err := json.Unmarshal(run.Result, &proposal); err != nil {
		return s.rejectRun(ctx, run, "invalid_case_proposal", []string{"proposal is not valid JSON: " + err.Error()})
	}
	input := runParseInput(run.Input)
	mode := strings.TrimSpace(proposal.Mode)
	if mode == "" {
		// Fall back to the mode requested on POST /runs when the tool payload
		// omits it.
		mode = strings.TrimSpace(input.Mode)
	}
	if mode == "" {
		mode = "append"
	}
	if mode != "append" && mode != "replace" {
		return s.rejectRun(ctx, run, "invalid_case_proposal", []string{fmt.Sprintf("mode %q is not append or replace", proposal.Mode)})
	}
	if mode == "replace" && !input.ConfirmReplace {
		return s.rejectRun(ctx, run, "invalid_case_proposal",
			[]string{"mode=replace requires confirmReplace=true on the run input"})
	}
	if s.deps.Nodes == nil || s.deps.Graph == nil {
		return errors.New("mindmap: graph ports are not configured for materialization")
	}
	requirements, err := s.confirmedRequirements(ctx, run.VersionID)
	if err != nil {
		return err
	}
	confirmed := make(map[int64]struct{}, len(requirements))
	for _, requirement := range requirements {
		confirmed[requirement.ID] = struct{}{}
	}
	messages := runValidateDesignProposal(proposal, confirmed)
	if len(messages) > 0 {
		return s.rejectRun(ctx, run, "invalid_case_proposal", messages)
	}
	existing, err := s.deps.Nodes.ListByVersion(ctx, run.VersionID)
	if err != nil {
		return fmt.Errorf("mindmap: load graph for materialization: %w", err)
	}
	if mode == "replace" {
		for _, node := range existing {
			if node.Status != domain.NodeStatusDraft {
				return s.rejectRun(ctx, run, "baseline_frozen", []string{
					fmt.Sprintf("node %d is %s; replace only removes draft nodes", node.ID, node.Status),
				})
			}
		}
	}
	// Enforce the 300-node mindmap cap before any write (spec §7.3). Replace
	// drops the current graph, so only the proposal counts; append adds to it.
	proposedNodes := len(proposal.Scenarios) + len(proposal.Cases)
	totalNodes := len(existing) + proposedNodes
	if mode == "replace" {
		totalNodes = proposedNodes
	}
	if totalNodes > maxGraphNodes {
		return s.rejectRun(ctx, run, "graph_too_large", []string{
			fmt.Sprintf("version %d would have %d nodes; the mindmap is limited to %d nodes",
				run.VersionID, totalNodes, maxGraphNodes),
		})
	}
	built, err := s.buildDesignGraph(ctx, run, proposal, existing)
	if err != nil {
		return err
	}
	if err := runValidateBuiltDesign(built); err != nil {
		return s.rejectRun(ctx, run, "invalid_case_proposal", []string{err.Error()})
	}
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if tx.Runs() == nil || tx.Nodes() == nil {
			return errors.New("mindmap: graph transaction repositories are not configured")
		}
		if mode == "replace" {
			existingNodes, err := tx.Nodes().ListByVersion(ctx, run.VersionID)
			if err != nil {
				return err
			}
			for _, node := range existingNodes {
				if node.Status != domain.NodeStatusDraft {
					return domain.NewStateConflict("baseline_frozen",
						fmt.Sprintf("node %d is %s; replace only removes draft nodes", node.ID, node.Status))
				}
			}
			// case_node.parent_id is ON DELETE CASCADE: deleting the roots removes
			// the whole draft subtree. Deleting a descendant after its ancestor
			// would find a zero-row delete and fail the transaction.
			for _, node := range existingNodes {
				if node.ParentID != nil {
					continue
				}
				if err := tx.Nodes().Delete(ctx, run.VersionID, node.ID, node.Revision); err != nil {
					return err
				}
			}
		}
		for _, node := range built.nodes {
			if _, err := tx.Nodes().Create(ctx, node); err != nil {
				return err
			}
		}
		for _, step := range built.steps {
			if _, err := tx.Steps().Create(ctx, step); err != nil {
				return err
			}
		}
		for _, assertion := range built.assertions {
			if _, err := tx.Assertions().Create(ctx, assertion); err != nil {
				return err
			}
		}
		for _, source := range built.sources {
			if _, err := tx.Sources().Create(ctx, source); err != nil {
				return err
			}
		}
		finished := s.now()
		_, err := tx.Runs().UpdateStatus(ctx, run.ID, run.Status, domain.RunDone, RunPatch{
			Stats:      runMergeStats(run.Stats, built.stats),
			FinishedAt: &finished,
		})
		return err
	})
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Reason == "baseline_frozen" {
			return s.rejectRun(ctx, run, "baseline_frozen", []string{domainErr.Message})
		}
		return fmt.Errorf("mindmap: materialize design run %d: %w", run.ID, err)
	}

	for _, anchor := range built.anchors {
		if s.deps.Anchors == nil {
			break
		}
		if _, err := s.deps.Anchors.Create(ctx, run.VersionID, run.CreatedBy, anchor.params); err != nil {
			slog.WarnContext(ctx, "mindmap: create case anchor failed",
				"run_id", run.ID, "case_id", anchor.caseNodeID, "file", anchor.params.FilePath, "error", err)
		}
	}
	for _, node := range built.cases {
		s.publishCaseCreated(ctx, run, node)
	}
	doneRun := run
	doneRun.Status = domain.RunDone
	s.publishRunEvent(ctx, doneRun, domain.EventCaseRunFinished, "", built.stats)
	if s.enterReviewAfterDesign(ctx, run) {
		s.publishGatePending(ctx, run)
	}
	return nil
}

// builtAnchor is one validated anchor awaiting the commit (the case node id only
// exists after the transaction minted the node).
type builtAnchor struct {
	caseNodeID int64
	params     AnchorParams
}

// builtDesign is the in-memory design graph awaiting the transaction.
type builtDesign struct {
	nodes      []domain.CaseNode
	steps      []domain.CaseStep
	assertions []domain.CaseAssertion
	sources    []domain.CaseSource
	anchors    []builtAnchor
	cases      []domain.CaseNode
	stats      map[string]any
}

// runValidateDesignProposal applies the payload rules of spec §6.7 rules 1–5.
// Messages are capped at 20; each is truncated by runCapValidationErrors.
func runValidateDesignProposal(proposal agentDesignProposal, confirmed map[int64]struct{}) []string {
	messages := make([]string, 0, 8)
	add := func(format string, args ...any) {
		if len(messages) >= materializeMaxValidationErrors {
			return
		}
		messages = append(messages, fmt.Sprintf(format, args...))
	}

	if len(proposal.Cases) == 0 {
		add("proposal contains no cases")
	}
	if len(proposal.Cases) > materializeMaxCases {
		add("proposal contains %d cases; the limit is %d", len(proposal.Cases), materializeMaxCases)
	}

	scenarioRefs := make(map[string]struct{}, len(proposal.Scenarios))
	for index, scenario := range proposal.Scenarios {
		ref := strings.TrimSpace(scenario.Ref)
		switch {
		case ref == "":
			add("scenarios[%d].ref must not be empty", index)
		case runHasKey(scenarioRefs, ref):
			add("scenarios[%d].ref %q is duplicated", index, ref)
		default:
			scenarioRefs[ref] = struct{}{}
		}
	}
	for index, scenario := range proposal.Scenarios {
		title := strings.TrimSpace(scenario.Title)
		if title == "" {
			add("scenarios[%d].title must not be empty", index)
		} else if err := validateRunes("title", title, maxNodeTitleRunes); err != nil {
			add("scenarios[%d].%s", index, err.Error())
		}
		if err := validateRunes("description", scenario.Description, maxNodeDescriptionRunes); err != nil {
			add("scenarios[%d].%s", index, err.Error())
		}
		if scenario.ParentRef != nil {
			parent := strings.TrimSpace(*scenario.ParentRef)
			if _, ok := scenarioRefs[parent]; !ok {
				add("scenarios[%d].parentRef %q does not exist", index, parent)
			}
		}
	}

	caseRefs := make(map[string]struct{}, len(proposal.Cases))
	for index, proposalCase := range proposal.Cases {
		prefix := fmt.Sprintf("cases[%d]", index)
		ref := strings.TrimSpace(proposalCase.Ref)
		switch {
		case ref == "":
			add("%s.ref must not be empty", prefix)
		case runHasKey(caseRefs, ref):
			add("%s.ref %q is duplicated", prefix, ref)
		default:
			caseRefs[ref] = struct{}{}
		}
		if proposalCase.ScenarioRef != nil {
			scenarioRef := strings.TrimSpace(*proposalCase.ScenarioRef)
			if _, ok := scenarioRefs[scenarioRef]; !ok {
				add("%s.scenarioRef %q does not exist", prefix, scenarioRef)
			}
		}
		title := strings.TrimSpace(proposalCase.Title)
		if title == "" {
			add("%s.title must not be empty", prefix)
		} else if err := validateRunes("title", title, maxNodeTitleRunes); err != nil {
			add("%s.%s", prefix, err.Error())
		}
		if err := validatePriority(proposalCase.Priority); err != nil {
			add("%s.%s", prefix, err.Error())
		}
		if err := validateRunes("preconditions", proposalCase.Preconditions, maxNodeDescriptionRunes); err != nil {
			add("%s.%s", prefix, err.Error())
		}
		if _, _, err := runOptionalTestData(proposalCase.TestData); err != nil {
			add("%s.testData is not valid JSON", prefix)
		}
		if len(proposalCase.RequirementIDs) == 0 {
			add("%s.requirementIds must not be empty", prefix)
		}
		seenRequirements := make(map[int64]struct{}, len(proposalCase.RequirementIDs))
		for requirementIndex, requirementID := range proposalCase.RequirementIDs {
			id := requirementID.Int64()
			if id <= 0 {
				add("%s.requirementIds[%d] is not a positive id", prefix, requirementIndex)
				continue
			}
			if _, duplicate := seenRequirements[id]; duplicate {
				// Duplicates are deduplicated rather than rejected (spec §6.7
				// rule 2 requires the set, not the raw list).
				continue
			}
			seenRequirements[id] = struct{}{}
			if _, ok := confirmed[id]; !ok {
				add("%s.requirementIds contains unconfirmed requirement %d", prefix, id)
			}
		}
		if len(proposalCase.Steps) == 0 {
			add("%s.steps must not be empty", prefix)
		}
		if len(proposalCase.Steps) > domain.MaxStepsPerCase {
			add("%s.steps has %d entries; the limit is %d", prefix, len(proposalCase.Steps), domain.MaxStepsPerCase)
		}
		for stepIndex, step := range proposalCase.Steps {
			stepPrefix := fmt.Sprintf("%s.steps[%d]", prefix, stepIndex)
			if strings.TrimSpace(step.Action) == "" {
				add("%s.action must not be empty", stepPrefix)
			} else if err := validateRunes("action", step.Action, maxStepActionRunes); err != nil {
				add("%s.%s", stepPrefix, err.Error())
			}
			if err := validateRunes("expected", step.Expected, maxStepExpectedRunes); err != nil {
				add("%s.%s", stepPrefix, err.Error())
			}
			if err := validateRunes("data", step.Data, maxStepDataRunes); err != nil {
				add("%s.%s", stepPrefix, err.Error())
			}
		}
		if len(proposalCase.Assertions) == 0 {
			add("%s.assertions must not be empty", prefix)
		}
		for assertionIndex, assertion := range proposalCase.Assertions {
			assertionPrefix := fmt.Sprintf("%s.assertions[%d]", prefix, assertionIndex)
			if assertion.StepIndex != nil {
				if *assertion.StepIndex < 0 || *assertion.StepIndex >= len(proposalCase.Steps) {
					add("%s.stepIndex %d is outside the %d steps", assertionPrefix, *assertion.StepIndex, len(proposalCase.Steps))
				}
			}
			if err := validateAssertionKind(assertion.Kind); err != nil {
				add("%s.kind %q is not ui, api, db or text", assertionPrefix, assertion.Kind)
			}
			if strings.TrimSpace(assertion.Expression) == "" {
				add("%s.expression must not be empty", assertionPrefix)
			} else if err := validateRunes("expression", assertion.Expression, maxAssertionExprRunes); err != nil {
				add("%s.%s", assertionPrefix, err.Error())
			}
			if err := validateRunes("target", assertion.Target, maxAssertionTargetRunes); err != nil {
				add("%s.%s", assertionPrefix, err.Error())
			}
			if err := validateRunes("expected", assertion.Expected, maxAssertionExpectRunes); err != nil {
				add("%s.%s", assertionPrefix, err.Error())
			}
		}
	}
	return messages
}

// runHasKey reports whether key exists in set.
func runHasKey[K comparable, V any](set map[K]V, key K) bool {
	_, ok := set[key]
	return ok
}

// runOptionalTestData normalizes an optional JSONB document. A nil/empty/null
// document means "no value".
func runOptionalTestData(raw json.RawMessage) ([]byte, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, false, nil
	}
	value, err := normalizeTestData(raw)
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

// buildDesignGraph converts a validated proposal into domain rows, minting every
// application-generated id (D29). Anchors are validated against the code
// baseline here: a missing path is dropped and counted, never fatal.
func (s *RunService) buildDesignGraph(ctx context.Context, run domain.CaseDesignRun, proposal agentDesignProposal, existing []domain.CaseNode) (*builtDesign, error) {
	now := s.now()
	built := &builtDesign{}
	scenarioIDs := make(map[string]int64, len(proposal.Scenarios))
	scenarioOrder := make(map[string]int, len(proposal.Scenarios)+1)
	rootOrder := runNextRootOrder(existing)
	scenarioOrder[""] = rootOrder

	for _, proposalScenario := range proposal.Scenarios {
		ref := strings.TrimSpace(proposalScenario.Ref)
		id, err := s.nextID()
		if err != nil {
			return nil, err
		}
		scenarioIDs[ref] = id
	}

	for _, proposalScenario := range proposal.Scenarios {
		ref := strings.TrimSpace(proposalScenario.Ref)
		parentKey := ""
		var parentID *int64
		if proposalScenario.ParentRef != nil {
			parentKey = strings.TrimSpace(*proposalScenario.ParentRef)
			parent := scenarioIDs[parentKey]
			parentID = &parent
		}
		order := scenarioOrder[parentKey]
		scenarioOrder[parentKey] = order + 1
		built.nodes = append(built.nodes, domain.CaseNode{
			ID:          scenarioIDs[ref],
			VersionID:   run.VersionID,
			ParentID:    parentID,
			Kind:        domain.NodeKindScenario,
			Title:       strings.TrimSpace(proposalScenario.Title),
			Description: proposalScenario.Description,
			Priority:    domain.PriorityP2,
			Source:      domain.NodeSourceAIGenerated,
			Status:      domain.NodeStatusDraft,
			SortOrder:   order,
			Revision:    1,
			CreatedBy:   run.CreatedBy,
			UpdatedBy:   run.CreatedBy,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	anchorsDropped := 0
	for _, proposalCase := range proposal.Cases {
		var parentID *int64
		parentKey := ""
		if proposalCase.ScenarioRef != nil {
			parentKey = strings.TrimSpace(*proposalCase.ScenarioRef)
			parent := scenarioIDs[parentKey]
			parentID = &parent
		}
		order := scenarioOrder[parentKey]
		scenarioOrder[parentKey] = order + 1

		nodeID, err := s.nextID()
		if err != nil {
			return nil, err
		}
		testData, _, _ := runOptionalTestData(proposalCase.TestData)
		node := domain.CaseNode{
			ID:            nodeID,
			VersionID:     run.VersionID,
			ParentID:      parentID,
			Kind:          domain.NodeKindCase,
			Title:         strings.TrimSpace(proposalCase.Title),
			Priority:      proposalCase.Priority,
			Preconditions: proposalCase.Preconditions,
			TestData:      testData,
			Source:        domain.NodeSourceAIGenerated,
			Status:        domain.NodeStatusDraft,
			SortOrder:     order,
			Revision:      1,
			CreatedBy:     run.CreatedBy,
			UpdatedBy:     run.CreatedBy,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		built.nodes = append(built.nodes, node)
		built.cases = append(built.cases, node)

		steps := make([]domain.CaseStep, 0, len(proposalCase.Steps))
		for index, proposalStep := range proposalCase.Steps {
			stepID, err := s.nextID()
			if err != nil {
				return nil, err
			}
			step := domain.CaseStep{
				ID:        stepID,
				NodeID:    nodeID,
				Seq:       index + 1,
				Action:    proposalStep.Action,
				Expected:  proposalStep.Expected,
				Data:      proposalStep.Data,
				Source:    domain.NodeSourceAIGenerated,
				CreatedAt: now,
				UpdatedAt: now,
			}
			steps = append(steps, step)
			built.steps = append(built.steps, step)
		}
		for _, proposalAssertion := range proposalCase.Assertions {
			assertionID, err := s.nextID()
			if err != nil {
				return nil, err
			}
			var stepID *int64
			if proposalAssertion.StepIndex != nil {
				linked := steps[*proposalAssertion.StepIndex].ID
				stepID = &linked
			}
			built.assertions = append(built.assertions, domain.CaseAssertion{
				ID:         assertionID,
				NodeID:     nodeID,
				StepID:     stepID,
				Kind:       proposalAssertion.Kind,
				Target:     proposalAssertion.Target,
				Expression: proposalAssertion.Expression,
				Expected:   proposalAssertion.Expected,
				Source:     domain.NodeSourceAIGenerated,
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
		seenRequirements := make(map[int64]struct{}, len(proposalCase.RequirementIDs))
		for _, requirementID := range proposalCase.RequirementIDs {
			reference := requirementID.Int64()
			if _, duplicate := seenRequirements[reference]; duplicate {
				continue
			}
			seenRequirements[reference] = struct{}{}
			sourceID, err := s.nextID()
			if err != nil {
				return nil, err
			}
			built.sources = append(built.sources, domain.CaseSource{
				ID:         sourceID,
				VersionID:  run.VersionID,
				NodeID:     nodeID,
				RefKind:    domain.RefRequirement,
				RefID:      &reference,
				Source:     domain.NodeSourceAIGenerated,
				Confidence: domain.ConfidenceHigh,
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
		runReference := run.ID
		runSourceID, err := s.nextID()
		if err != nil {
			return nil, err
		}
		built.sources = append(built.sources, domain.CaseSource{
			ID:         runSourceID,
			VersionID:  run.VersionID,
			NodeID:     nodeID,
			RefKind:    domain.RefAIRun,
			RefID:      &runReference,
			Source:     domain.NodeSourceAIGenerated,
			Confidence: domain.ConfidenceHigh,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
		for _, proposalAnchor := range proposalCase.Anchors {
			filePath := strings.TrimSpace(proposalAnchor.File)
			if filePath == "" || s.deps.CodeFacts == nil {
				anchorsDropped++
				continue
			}
			exists, err := s.deps.CodeFacts.FileExists(ctx, run.VersionID, filePath)
			if err != nil || !exists {
				anchorsDropped++
				continue
			}
			built.anchors = append(built.anchors, builtAnchor{
				caseNodeID: nodeID,
				params: AnchorParams{
					SubjectID:  nodeID,
					FilePath:   filePath,
					StartLine:  proposalAnchor.StartLine,
					EndLine:    proposalAnchor.EndLine,
					Confidence: domain.ConfidenceHigh,
				},
			})
		}
	}

	built.stats = map[string]any{
		"materialized":   true,
		"scenarios":      len(proposal.Scenarios),
		"cases":          len(built.cases),
		"steps":          len(built.steps),
		"assertions":     len(built.assertions),
		"sources":        len(built.sources),
		"anchors":        len(built.anchors),
		"anchorsDropped": anchorsDropped,
	}
	return built, nil
}

// runNextRootOrder appends new root nodes after the current maximum sibling
// order, matching GraphService.nextSortOrder.
func runNextRootOrder(nodes []domain.CaseNode) int {
	order := 0
	for _, node := range nodes {
		if node.ParentID == nil && node.SortOrder >= order {
			order = node.SortOrder + 1
		}
	}
	return order
}

// runValidateBuiltDesign re-checks the built tree with the domain invariant
// validator (scenario nesting, cycles, step sequences, assertion ownership).
func runValidateBuiltDesign(built *builtDesign) error {
	steps := make(map[int64][]domain.CaseStep, len(built.cases))
	assertions := make(map[int64][]domain.CaseAssertion, len(built.cases))
	for _, step := range built.steps {
		steps[step.NodeID] = append(steps[step.NodeID], step)
	}
	for _, assertion := range built.assertions {
		assertions[assertion.NodeID] = append(assertions[assertion.NodeID], assertion)
	}
	return domain.ValidateGraph(built.nodes, steps, assertions)
}

// materializeReview validates and writes a case.review_submit submission: a
// draft review plus its findings, with the previous open AI findings dismissed,
// all in one transaction (spec §6.8).
func (s *RunService) materializeReview(ctx context.Context, run domain.CaseDesignRun) error {
	if len(bytes.TrimSpace(run.Result)) == 0 {
		return s.rejectRun(ctx, run, "invalid_case_proposal", []string{"the agent submitted no review findings"})
	}
	if len(run.Result) > materializeMaxPayloadBytes {
		return s.rejectRun(ctx, run, "invalid_case_proposal",
			[]string{fmt.Sprintf("review payload is %d bytes; the limit is %d", len(run.Result), materializeMaxPayloadBytes)})
	}
	var proposal agentReviewProposal
	if err := json.Unmarshal(run.Result, &proposal); err != nil {
		return s.rejectRun(ctx, run, "invalid_case_proposal", []string{"review payload is not valid JSON: " + err.Error()})
	}
	messages := make([]string, 0, 4)
	add := func(format string, args ...any) {
		if len(messages) >= materializeMaxValidationErrors {
			return
		}
		messages = append(messages, fmt.Sprintf(format, args...))
	}
	if len(proposal.Findings) > materializeMaxFindings {
		add("review contains %d findings; the limit is %d", len(proposal.Findings), materializeMaxFindings)
	}
	var knownNodes map[int64]struct{}
	for index, finding := range proposal.Findings {
		prefix := fmt.Sprintf("findings[%d]", index)
		if finding.NodeID != nil {
			if knownNodes == nil {
				if s.deps.Nodes == nil {
					return errors.New("mindmap: node repository is not configured for review findings")
				}
				nodes, err := s.deps.Nodes.ListByVersion(ctx, run.VersionID)
				if err != nil {
					return fmt.Errorf("mindmap: load graph for review materialization: %w", err)
				}
				knownNodes = make(map[int64]struct{}, len(nodes))
				for _, node := range nodes {
					knownNodes[node.ID] = struct{}{}
				}
			}
			if _, ok := knownNodes[finding.NodeID.Int64()]; !ok {
				add("%s.nodeId %d does not exist in version %d", prefix, finding.NodeID.Int64(), run.VersionID)
			}
		}
		switch finding.Category {
		case domain.FindingCoverage, domain.FindingAssertion, domain.FindingExecutability,
			domain.FindingDuplicate, domain.FindingCodeUncovered, domain.FindingOther:
		default:
			add("%s.category %q is not coverage, assertion, executability, duplicate, code_uncovered or other", prefix, finding.Category)
		}
		switch finding.Severity {
		case domain.SeverityBlocker, domain.SeverityMajor, domain.SeverityMinor, domain.SeverityInfo:
		default:
			add("%s.severity %q is not blocker, major, minor or info", prefix, finding.Severity)
		}
		detail := strings.TrimSpace(finding.Detail)
		if detail == "" {
			add("%s.detail must not be empty", prefix)
		} else if err := validateRunes("detail", detail, materializeMaxFindingDetailRunes); err != nil {
			add("%s.%s", prefix, err.Error())
		}
	}
	if len(messages) > 0 {
		return s.rejectRun(ctx, run, "invalid_case_proposal", messages)
	}
	if s.deps.Graph == nil {
		return errors.New("mindmap: graph transaction is not configured")
	}

	now := s.now()
	reviewID, err := s.nextID()
	if err != nil {
		return err
	}
	review := domain.CaseReview{
		ID:         reviewID,
		VersionID:  run.VersionID,
		RunID:      &run.ID,
		Status:     domain.ReviewDraft,
		ReviewerID: run.CreatedBy,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	findings := make([]domain.CaseReviewFinding, 0, len(proposal.Findings))
	blockers := 0
	for _, proposalFinding := range proposal.Findings {
		findingID, err := s.nextID()
		if err != nil {
			return err
		}
		var nodeID *int64
		if proposalFinding.NodeID != nil {
			reference := proposalFinding.NodeID.Int64()
			nodeID = &reference
		}
		if proposalFinding.Severity == domain.SeverityBlocker {
			blockers++
		}
		findings = append(findings, domain.CaseReviewFinding{
			ID:        findingID,
			ReviewID:  reviewID,
			NodeID:    nodeID,
			Category:  proposalFinding.Category,
			Severity:  proposalFinding.Severity,
			Detail:    strings.TrimSpace(proposalFinding.Detail),
			Status:    domain.FindingOpen,
			Source:    domain.FindingSourceAI,
			CreatedBy: run.CreatedBy,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	stats := map[string]any{
		"materialized": true,
		"findings":     len(findings),
		"blockers":     blockers,
	}
	var dismissed int
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if tx.Findings() == nil || tx.Reviews() == nil || tx.Runs() == nil {
			return errors.New("mindmap: graph transaction repositories are not configured")
		}
		dismissedCount, err := tx.Findings().DismissOpenAI(ctx, run.VersionID)
		if err != nil {
			return err
		}
		dismissed = dismissedCount
		if _, err := tx.Reviews().Create(ctx, review); err != nil {
			return err
		}
		if len(findings) > 0 {
			if err := tx.Findings().CreateBatch(ctx, findings); err != nil {
				return err
			}
		}
		stats["dismissed"] = dismissed
		finished := s.now()
		_, err = tx.Runs().UpdateStatus(ctx, run.ID, run.Status, domain.RunDone, RunPatch{
			Stats:      runMergeStats(run.Stats, stats),
			FinishedAt: &finished,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("mindmap: materialize review run %d: %w", run.ID, err)
	}

	refs := make([]int64, 0, maxEventRefCaseIDs)
	for _, finding := range findings {
		if finding.NodeID == nil {
			continue
		}
		refs = runAppendUniqueID(refs, *finding.NodeID, maxEventRefCaseIDs)
	}
	s.publishEvent(ctx, Event{
		VersionID:  run.VersionID,
		Type:       domain.EventCaseReviewed,
		Summary:    fmt.Sprintf("case review finished: %d finding(s), %d blocker(s)", len(findings), blockers),
		RefCaseIDs: refs,
		ActorID:    run.CreatedBy,
		Source:     domain.NodeSourceAIGenerated,
		Detail: map[string]any{
			"reviewId": reviewID,
			"findings": len(findings),
			"blockers": blockers,
		},
	})
	// gate.pending means "the version entered reviewing" (spec §6.11): a review
	// materialized against a still-designing version must not emit it.
	if s.versionIsReviewing(ctx, run.VersionID) {
		s.publishGatePending(ctx, run)
	}
	doneRun := run
	doneRun.Status = domain.RunDone
	s.publishRunEvent(ctx, doneRun, domain.EventCaseRunFinished, "", stats)
	return nil
}

// runAppendUniqueID appends id when absent until the event reference cap is hit.
func runAppendUniqueID(ids []int64, id int64, limit int) []int64 {
	if len(ids) >= limit {
		return ids
	}
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

// materializeDataPlan validates and applies a case.data_plan_submit submission
// (spec §6.8): only preconditions/test_data of existing nodes are updated and
// only when the stored revision still matches; mismatches are counted as
// skipped. No node is ever created.
func (s *RunService) materializeDataPlan(ctx context.Context, run domain.CaseDesignRun) error {
	if err := s.requireMaterializableVersion(ctx, run); err != nil {
		return err
	}
	if len(bytes.TrimSpace(run.Result)) == 0 {
		return s.rejectRun(ctx, run, "invalid_case_proposal", []string{"the agent submitted no data-plan items"})
	}
	if len(run.Result) > materializeMaxPayloadBytes {
		return s.rejectRun(ctx, run, "invalid_case_proposal",
			[]string{fmt.Sprintf("data-plan payload is %d bytes; the limit is %d", len(run.Result), materializeMaxPayloadBytes)})
	}
	var proposal agentDataPlanProposal
	if err := json.Unmarshal(run.Result, &proposal); err != nil {
		return s.rejectRun(ctx, run, "invalid_case_proposal", []string{"data-plan payload is not valid JSON: " + err.Error()})
	}
	// A data-plan run started with an explicit nodeIds list only plans those
	// nodes (spec §6.8); an item outside the scope is a payload violation.
	input := runParseInput(run.Input)
	scope := make(map[int64]struct{}, len(input.NodeIDs))
	for _, nodeID := range input.NodeIDs {
		if nodeID > 0 {
			scope[nodeID] = struct{}{}
		}
	}
	messages := make([]string, 0, 4)
	add := func(format string, args ...any) {
		if len(messages) >= materializeMaxValidationErrors {
			return
		}
		messages = append(messages, fmt.Sprintf(format, args...))
	}
	if len(proposal.Items) == 0 {
		add("data-plan submission contains no items")
	}
	if len(proposal.Items) > materializeMaxDataPlanItems {
		add("data-plan submission contains %d items; the limit is %d", len(proposal.Items), materializeMaxDataPlanItems)
	}
	for index, item := range proposal.Items {
		prefix := fmt.Sprintf("items[%d]", index)
		if item.NodeID.Int64() <= 0 {
			add("%s.nodeId must be a positive id", prefix)
		} else if len(scope) > 0 {
			if _, ok := scope[item.NodeID.Int64()]; !ok {
				add("%s.nodeId %d is outside the run's nodeIds scope", prefix, item.NodeID.Int64())
			}
		}
		if item.NodeRevision <= 0 {
			add("%s.nodeRevision must be positive", prefix)
		}
		if item.Preconditions != nil {
			if err := validateRunes("preconditions", *item.Preconditions, maxNodeDescriptionRunes); err != nil {
				add("%s.%s", prefix, err.Error())
			}
		}
		if _, _, err := runOptionalTestData(item.TestData); err != nil {
			add("%s.testData is not valid JSON", prefix)
		}
	}
	if len(messages) > 0 {
		return s.rejectRun(ctx, run, "invalid_case_proposal", messages)
	}
	if s.deps.Graph == nil || s.deps.Nodes == nil {
		return errors.New("mindmap: graph ports are not configured for materialization")
	}

	updated, skipped := 0, 0
	stats := map[string]any{}
	err := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if tx.Nodes() == nil || tx.Runs() == nil {
			return errors.New("mindmap: graph transaction repositories are not configured")
		}
		nodes, err := tx.Nodes().ListByVersion(ctx, run.VersionID)
		if err != nil {
			return err
		}
		byID := make(map[int64]domain.CaseNode, len(nodes))
		for _, node := range nodes {
			byID[node.ID] = node
		}
		seen := make(map[int64]struct{}, len(proposal.Items))
		for _, item := range proposal.Items {
			nodeID := item.NodeID.Int64()
			node, ok := byID[nodeID]
			if !ok || node.Revision != item.NodeRevision {
				skipped++
				continue
			}
			if _, duplicate := seen[nodeID]; duplicate {
				skipped++
				continue
			}
			seen[nodeID] = struct{}{}
			testData, hasTestData, err := runOptionalTestData(item.TestData)
			if err != nil {
				skipped++
				continue
			}
			if item.Preconditions == nil && !hasTestData {
				skipped++
				continue
			}
			if item.Preconditions != nil {
				node.Preconditions = *item.Preconditions
			}
			if hasTestData {
				node.TestData = testData
			}
			node.UpdatedBy = run.CreatedBy
			if _, err := tx.Nodes().Update(ctx, node, item.NodeRevision); err != nil {
				var domainErr *domain.Error
				if errors.As(err, &domainErr) && domainErr.Reason == "case_revision_conflict" {
					skipped++
					continue
				}
				return err
			}
			updated++
		}
		stats["materialized"] = true
		stats["updated"] = updated
		stats["skipped"] = skipped
		finished := s.now()
		_, err = tx.Runs().UpdateStatus(ctx, run.ID, run.Status, domain.RunDone, RunPatch{
			Stats:      runMergeStats(run.Stats, stats),
			FinishedAt: &finished,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("mindmap: materialize data-plan run %d: %w", run.ID, err)
	}
	doneRun := run
	doneRun.Status = domain.RunDone
	s.publishRunEvent(ctx, doneRun, domain.EventCaseRunFinished, "", stats)
	return nil
}

// publishCaseCreated emits one case.created event for a materialized case
// (refs.case = case node id, spec §6.11).
func (s *RunService) publishCaseCreated(ctx context.Context, run domain.CaseDesignRun, node domain.CaseNode) {
	title := truncateRunes(node.Title, maxEventTitleRunes)
	s.publishEvent(ctx, Event{
		VersionID:  run.VersionID,
		Type:       domain.EventCaseCreated,
		Summary:    fmt.Sprintf("case %q created", title),
		RefCaseIDs: []int64{node.ID},
		ActorID:    run.CreatedBy,
		Source:     domain.NodeSourceAIGenerated,
		Detail: map[string]any{
			"nodeId": node.ID,
			"kind":   node.Kind,
			"title":  title,
			"source": node.Source,
		},
	})
}

// enterReviewAfterDesign moves a designing version into reviewing after a
// successful design materialization (spec §6.8). An already-reviewing version is
// tolerated; a failed transition is logged and never fails the committed
// materialization. It reports whether the version entered reviewing now.
func (s *RunService) enterReviewAfterDesign(ctx context.Context, run domain.CaseDesignRun) bool {
	if s.deps.Versions == nil {
		return false
	}
	version, err := s.deps.Versions.GetVersion(ctx, run.VersionID)
	if err != nil {
		slog.WarnContext(ctx, "mindmap: read version after design run failed", "version_id", run.VersionID, "error", err)
		return false
	}
	if version.Status != versionStatusDesigning {
		return false
	}
	if _, err := s.deps.Versions.EnterReview(ctx, run.VersionID, run.CreatedBy); err != nil {
		latest, getErr := s.deps.Versions.GetVersion(ctx, run.VersionID)
		if getErr == nil && latest.Status == versionStatusReviewing {
			return true
		}
		slog.WarnContext(ctx, "mindmap: auto enter-review failed", "version_id", run.VersionID, "error", err)
		return false
	}
	return true
}

// versionIsReviewing reports whether the version is currently reviewing. It is
// the gate.pending condition of review materialization; a read failure is logged
// and treated as "not reviewing" (the committed materialization is not failed by
// a missing event).
func (s *RunService) versionIsReviewing(ctx context.Context, versionID int64) bool {
	if s.deps.Versions == nil {
		return false
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		slog.WarnContext(ctx, "mindmap: read version for gate.pending failed", "version_id", versionID, "error", err)
		return false
	}
	return version.Status == versionStatusReviewing
}

// publishGatePending emits the mindmap review gate event (spec §6.11).
func (s *RunService) publishGatePending(ctx context.Context, run domain.CaseDesignRun) {
	s.publishEvent(ctx, Event{
		VersionID: run.VersionID,
		Type:      domain.EventGatePending,
		Summary:   "mindmap review gate pending",
		ActorID:   run.CreatedBy,
		Source:    domain.NodeSourceAIGenerated,
		Detail:    map[string]any{"gate": "mindmap_review", "runId": run.ID},
	})
}

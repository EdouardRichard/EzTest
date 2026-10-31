package design

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Case version / logical case statuses (spec §4.1). superseded only ever
// applies to historical versions; the current version of an entity is one of
// draft/confirmed/stale.
const (
	CaseStatusDraft      = "draft"
	CaseStatusConfirmed  = "confirmed"
	CaseStatusStale      = "stale"
	CaseStatusSuperseded = "superseded"
)

// Case priorities (spec §3.1).
const (
	PriorityP0 = "P0"
	PriorityP1 = "P1"
	PriorityP2 = "P2"
	PriorityP3 = "P3"
)

// Assertion kinds (spec §4.4).
const (
	AssertionUI     = "ui"
	AssertionAPI    = "api"
	AssertionDB     = "db"
	AssertionText   = "text"
	AssertionVisual = "visual"
)

// Source kinds (spec §4.4).
const (
	SourceRequirement = "requirement"
	SourceCode        = "code"
	SourcePage        = "page"
	SourceAPI         = "api"
	SourceKnowledge   = "knowledge"
)

// Content limits from spec §3/§5.3 and P2-D-18.
const (
	CaseTitleMax       = 200
	CaseDescriptionMax = 20000
	ScenarioNameMax    = 200
	StepActionMax      = 2000
	AssertionTargetMax = 2000
	AssertionExpectMax = 2000
	SourceRefIDMax     = 200
	SourcePathMax      = 1000
	AccountRoleMax     = 64
	GraphHashLength    = 64
)

// DefaultLimits are the P2-D-18 proposal/requirement limits.
var DefaultLimits = Limits{
	MaxScenariosPerProposal:    3,
	MaxCasesPerProposal:        10,
	MaxScenariosPerRequirement: 100,
	MaxCasesPerRequirement:     200,
	MaxStepsPerCase:            50,
	MaxAssertionsPerCase:       50,
	MaxSourcesPerCase:          20,
}

// Limits bounds one graph proposal and the accumulated requirement graph.
type Limits struct {
	MaxScenariosPerProposal    int
	MaxCasesPerProposal        int
	MaxScenariosPerRequirement int
	MaxCasesPerRequirement     int
	MaxStepsPerCase            int
	MaxAssertionsPerCase       int
	MaxSourcesPerCase          int
}

// Effective fills zero values from DefaultLimits.
func (l Limits) Effective() Limits {
	if l.MaxScenariosPerProposal <= 0 {
		l.MaxScenariosPerProposal = DefaultLimits.MaxScenariosPerProposal
	}
	if l.MaxCasesPerProposal <= 0 {
		l.MaxCasesPerProposal = DefaultLimits.MaxCasesPerProposal
	}
	if l.MaxScenariosPerRequirement <= 0 {
		l.MaxScenariosPerRequirement = DefaultLimits.MaxScenariosPerRequirement
	}
	if l.MaxCasesPerRequirement <= 0 {
		l.MaxCasesPerRequirement = DefaultLimits.MaxCasesPerRequirement
	}
	if l.MaxStepsPerCase <= 0 {
		l.MaxStepsPerCase = DefaultLimits.MaxStepsPerCase
	}
	if l.MaxAssertionsPerCase <= 0 {
		l.MaxAssertionsPerCase = DefaultLimits.MaxAssertionsPerCase
	}
	if l.MaxSourcesPerCase <= 0 {
		l.MaxSourcesPerCase = DefaultLimits.MaxSourcesPerCase
	}
	return l
}

// Scenario is one requirement-version-scoped grouping node in the design graph.
type Scenario struct {
	ID                   int64
	VersionID            int64
	RequirementID        int64
	RequirementVersionID int64
	Name                 string
	Description          string
	VersionSUTIDs        []int64
	PageIDs              []int64
	APIIDs               []int64
	SortOrder            int
	CreatedBy            *int64
	CreatedByRunID       *int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ScenarioProposal is one submitted scenario node. ID is set when updating an
// existing scenario; new scenarios omit it.
type ScenarioProposal struct {
	Ref           string
	ID            *int64
	Name          string
	Description   string
	VersionSUTIDs []int64
	PageIDs       []int64
	APIIDs        []int64
	SortOrder     int
}

// ScenarioNaturalKey is the natural key used to reuse/update an existing
// scenario instead of duplicating it (spec §3.1 uk_case_scenario_*).
func ScenarioNaturalKey(requirementVersionID int64, name string) string {
	return platformid.Format(requirementVersionID) + "|" + strings.TrimSpace(name)
}

// CaseNaturalKey is the in-scenario natural key used to reuse/update an
// existing logical case instead of duplicating it.
func CaseNaturalKey(scenarioID int64, title string) string {
	return platformid.Format(scenarioID) + "|" + strings.TrimSpace(title)
}

// CaseEntity is the logical case row. CurrentVersionID points at the current
// immutable case_version; Status is that version's status projection.
type CaseEntity struct {
	ID               int64
	ScenarioID       int64
	CurrentVersionID *int64
	Status           string
	CreatedBy        *int64
	CreatedByRunID   *int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CaseStep is one ordered step of an immutable case version.
type CaseStep struct {
	ID                 int64
	CaseVersionID      int64
	Seq                int
	Action             string
	Expected           string
	TargetVersionSUTID *int64
	TargetPageID       *int64
	TargetAPIID        *int64
	AccountRole        string
	Data               json.RawMessage
	CreatedAt          time.Time
}

// CaseAssertion is one assertion attached to a step sequence number.
type CaseAssertion struct {
	ID                 int64
	CaseVersionID      int64
	StepSeq            int
	Kind               string
	Target             string
	Expected           string
	TargetVersionSUTID *int64
	TargetAPIID        *int64
	DatabaseID         *int64
	CreatedAt          time.Time
}

// CaseSource is one provenance row of an immutable case version.
type CaseSource struct {
	ID            int64
	CaseVersionID int64
	Kind          string
	RefID         string
	Path          string
	Line          *int
	SHA           string
	CreatedAt     time.Time
}

// CaseVersion is one immutable snapshot of a logical case. Steps, assertions
// and sources belong to exactly one version and are never updated in place.
type CaseVersion struct {
	ID                  int64
	CaseID              int64
	Revision            int
	Title               string
	Description         string
	Preconditions       string
	TestData            json.RawMessage
	Priority            string
	DefaultVersionSUTID *int64
	VersionSUTIDs       []int64
	PageIDs             []int64
	APIIDs              []int64
	Status              string
	ContentHash         string
	CreatedBy           *int64
	CreatedByRunID      *int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Steps               []CaseStep
	Assertions          []CaseAssertion
	Sources             []CaseSource
}

// StepProposal is one submitted step.
type StepProposal struct {
	Seq                int
	Action             string
	Expected           string
	TargetVersionSUTID *int64
	TargetPageID       *int64
	TargetAPIID        *int64
	AccountRole        string
	Data               json.RawMessage
}

// AssertionProposal is one submitted assertion.
type AssertionProposal struct {
	StepSeq            int
	Kind               string
	Target             string
	Expected           string
	TargetVersionSUTID *int64
	TargetAPIID        *int64
	DatabaseID         *int64
}

// SourceProposal is one submitted provenance row.
type SourceProposal struct {
	Kind  string
	RefID string
	Path  string
	Line  *int
	SHA   string
}

// CaseProposal is one submitted logical case with its draft content.
type CaseProposal struct {
	Ref                 string
	ID                  *int64
	ScenarioRef         string
	Title               string
	Description         string
	Preconditions       string
	TestData            json.RawMessage
	Priority            string
	DefaultVersionSUTID *int64
	VersionSUTIDs       []int64
	PageIDs             []int64
	APIIDs              []int64
	Steps               []StepProposal
	Assertions          []AssertionProposal
	Sources             []SourceProposal
}

// Proposal is one case.graph_propose payload: a batch of scenario and case
// node writes scoped to one requirement version.
type Proposal struct {
	RequirementID        int64
	RequirementVersionID int64
	BaseGraphHash        string
	Scenarios            []ScenarioProposal
	Cases                []CaseProposal
}

// ProposalContext is the server-resolved validation context for one proposal.
// The maps are target -> version_sut_id bindings of the version.
type ProposalContext struct {
	VersionID             int64
	RequirementID         int64
	RequirementVersionID  int64
	CurrentHash           string
	AllowedVersionSUTs    []int64
	PageToSUT             map[int64]int64
	APIToSUT              map[int64]int64
	DBTOSUT               map[int64]int64
	ExistingScenarioCount int
	ExistingCaseCount     int
	Limits                Limits
}

// ValidateProposal applies the structural and target-ownership rules of spec
// §4.3/§4.4/§5.3. Existence and natural-key resolution against already-stored
// rows is performed by the application service before calling this function.
func ValidateProposal(in Proposal, ctx ProposalContext) error {
	if in.RequirementID <= 0 || in.RequirementVersionID <= 0 || ctx.VersionID <= 0 {
		return platform.Invalid("requirement_id", "requirement, requirement version and version are required")
	}
	if in.RequirementID != ctx.RequirementID || in.RequirementVersionID != ctx.RequirementVersionID {
		return platform.Invalid("requirement_version_id", "proposal does not match the requested requirement version")
	}

	baseHash := strings.TrimSpace(in.BaseGraphHash)
	if baseHash != "" {
		if !ValidGraphHash(baseHash) {
			return platform.Invalid("base_graph_hash", "must be a 64-character lowercase SHA-256")
		}
		if baseHash != ctx.CurrentHash {
			return platform.StateConflict("the design graph changed since it was read; re-read the graph and retry")
		}
	}

	limits := ctx.Limits.Effective()
	if len(in.Scenarios) == 0 {
		return platform.Invalid("scenarios", "at least one scenario is required")
	}
	if len(in.Scenarios) > limits.MaxScenariosPerProposal {
		return platform.Invalid("scenarios", "at most 3 scenarios may be proposed per call")
	}
	if len(in.Cases) == 0 {
		return platform.Invalid("cases", "at least one case is required")
	}
	if len(in.Cases) > limits.MaxCasesPerProposal {
		return platform.Invalid("cases", "at most 10 cases may be proposed per call")
	}
	if ctx.ExistingScenarioCount+len(in.Scenarios) > limits.MaxScenariosPerRequirement {
		return platform.Invalid("scenarios", "the requirement version exceeds the scenario limit")
	}
	if ctx.ExistingCaseCount+len(in.Cases) > limits.MaxCasesPerRequirement {
		return platform.Invalid("cases", "the requirement version exceeds the case limit")
	}

	allowedSUTs := make(map[int64]struct{}, len(ctx.AllowedVersionSUTs))
	for _, id := range ctx.AllowedVersionSUTs {
		if id > 0 {
			allowedSUTs[id] = struct{}{}
		}
	}

	scenarioRefs, scenarioNames := make(map[string]struct{}, len(in.Scenarios)), make(map[string]struct{}, len(in.Scenarios))
	for index, scenario := range in.Scenarios {
		field := proposalField("scenarios", index)
		if strings.TrimSpace(scenario.Ref) == "" {
			return platform.Invalid(field+".ref", "scenario ref is required")
		}
		if _, duplicate := scenarioRefs[scenario.Ref]; duplicate {
			return platform.Invalid(field+".ref", "scenario ref must be unique within the proposal")
		}
		scenarioRefs[scenario.Ref] = struct{}{}

		name := strings.TrimSpace(scenario.Name)
		if err := validation.String(field+".name", name, 1, ScenarioNameMax); err != nil {
			return err
		}
		if _, duplicate := scenarioNames[name]; duplicate {
			return platform.Invalid(field+".name", "scenario name must be unique within the requirement version")
		}
		scenarioNames[name] = struct{}{}
		if scenario.ID != nil && *scenario.ID <= 0 {
			return platform.Invalid(field+".id", "must be a positive decimal ID")
		}
		if err := validation.String(field+".description", scenario.Description, 0, CaseDescriptionMax); err != nil {
			return err
		}
		if scenario.SortOrder < 0 {
			return platform.Invalid(field+".sort_order", "must be zero or positive")
		}
		if err := validateTargetSet(field+".version_sut_ids", scenario.VersionSUTIDs, allowedSUTs, true); err != nil {
			return err
		}
		if err := validateTargetSet(field+".page_ids", scenario.PageIDs, nil, false); err != nil {
			return err
		}
		for _, pageID := range scenario.PageIDs {
			if _, ok := ctx.PageToSUT[pageID]; !ok {
				return platform.Invalid(field+".page_ids", "page does not belong to a version SUT")
			}
		}
		for _, apiID := range scenario.APIIDs {
			if _, ok := ctx.APIToSUT[apiID]; !ok {
				return platform.Invalid(field+".api_ids", "api does not belong to a version SUT")
			}
		}
	}

	caseRefs := make(map[string]struct{}, len(in.Cases))
	caseTitles := make(map[string]struct{}, len(in.Cases))
	for index, item := range in.Cases {
		field := proposalField("cases", index)
		if strings.TrimSpace(item.Ref) == "" {
			return platform.Invalid(field+".ref", "case ref is required")
		}
		if _, duplicate := caseRefs[item.Ref]; duplicate {
			return platform.Invalid(field+".ref", "case ref must be unique within the proposal")
		}
		caseRefs[item.Ref] = struct{}{}
		if _, ok := scenarioRefs[item.ScenarioRef]; !ok {
			return platform.Invalid(field+".scenario_ref", "must reference a scenario ref in the same proposal")
		}
		title := strings.TrimSpace(item.Title)
		naturalKey := item.ScenarioRef + "|" + title
		if _, duplicate := caseTitles[naturalKey]; duplicate {
			return platform.Invalid(field+".title", "case title must be unique within its scenario")
		}
		caseTitles[naturalKey] = struct{}{}
		if item.ID != nil && *item.ID <= 0 {
			return platform.Invalid(field+".id", "must be a positive decimal ID")
		}
		if err := validateCaseProposal(item, field, ctx, allowedSUTs, limits); err != nil {
			return err
		}
	}
	return nil
}

func validateCaseProposal(item CaseProposal, field string, ctx ProposalContext, allowedSUTs map[int64]struct{}, limits Limits) error {
	if err := validation.String(field+".title", strings.TrimSpace(item.Title), 1, CaseTitleMax); err != nil {
		return err
	}
	if err := validation.String(field+".description", item.Description, 0, CaseDescriptionMax); err != nil {
		return err
	}
	if err := validation.String(field+".preconditions", item.Preconditions, 0, CaseDescriptionMax); err != nil {
		return err
	}
	if _, err := NormalizePriority(item.Priority); err != nil {
		return err
	}
	if _, err := NormalizeJSONObject(item.TestData); err != nil {
		return platform.Invalid(field+".test_data", "must be a JSON object")
	}

	if len(item.VersionSUTIDs) == 0 {
		return platform.Invalid(field+".version_sut_ids", "at least one version SUT is required")
	}
	if err := validateTargetSet(field+".version_sut_ids", item.VersionSUTIDs, allowedSUTs, false); err != nil {
		return err
	}
	caseSUTs := make(map[int64]struct{}, len(item.VersionSUTIDs))
	for _, id := range item.VersionSUTIDs {
		caseSUTs[id] = struct{}{}
	}
	if item.DefaultVersionSUTID == nil {
		return platform.Invalid(field+".default_version_sut_id", "a default version SUT is required")
	}
	if _, ok := caseSUTs[*item.DefaultVersionSUTID]; !ok {
		return platform.Invalid(field+".default_version_sut_id", "must be one of the case version SUTs")
	}

	if err := validateBoundResourceIDs(field+".page_ids", item.PageIDs, ctx.PageToSUT, caseSUTs); err != nil {
		return err
	}
	if err := validateBoundResourceIDs(field+".api_ids", item.APIIDs, ctx.APIToSUT, caseSUTs); err != nil {
		return err
	}

	if len(item.Steps) == 0 {
		return platform.Invalid(field+".steps", "at least one step is required")
	}
	if len(item.Steps) > limits.MaxStepsPerCase {
		return platform.Invalid(field+".steps", "a case may have at most 50 steps")
	}
	stepSeqs := make(map[int]int, len(item.Steps))
	for index, step := range item.Steps {
		stepField := field + ".steps[" + platformid.Format(int64(index)) + "]"
		if step.Seq != index+1 {
			return platform.Invalid(stepField+".seq", "step seq must be contiguous starting at 1")
		}
		stepSeqs[step.Seq] = index
		if err := validation.String(stepField+".action", step.Action, 1, StepActionMax); err != nil {
			return err
		}
		if err := validation.String(stepField+".expected", step.Expected, 0, CaseDescriptionMax); err != nil {
			return err
		}
		if err := validation.String(stepField+".account_role", step.AccountRole, 0, AccountRoleMax); err != nil {
			return err
		}
		if _, err := NormalizeJSONObject(step.Data); err != nil {
			return platform.Invalid(stepField+".data", "must be a JSON object")
		}
		if step.TargetVersionSUTID != nil {
			if _, ok := caseSUTs[*step.TargetVersionSUTID]; !ok {
				return platform.Invalid(stepField+".target_version_sut_id", "must be one of the case version SUTs")
			}
		}
		targetSUT, err := stepTargetSUT(stepField, step.TargetVersionSUTID, step.TargetPageID, step.TargetAPIID, ctx, caseSUTs)
		if err != nil {
			return err
		}
		_ = targetSUT
	}

	if len(item.Assertions) > limits.MaxAssertionsPerCase {
		return platform.Invalid(field+".assertions", "a case may have at most 50 assertions")
	}
	for index, assertion := range item.Assertions {
		assertionField := field + ".assertions[" + platformid.Format(int64(index)) + "]"
		if _, ok := stepSeqs[assertion.StepSeq]; !ok {
			return platform.Invalid(assertionField+".step_seq", "must reference a step seq of the same case")
		}
		if !ValidAssertionKind(assertion.Kind) {
			return platform.Invalid(assertionField+".kind", "must be one of ui, api, db, text, visual")
		}
		if err := validation.String(assertionField+".target", assertion.Target, 1, AssertionTargetMax); err != nil {
			return err
		}
		if err := validation.String(assertionField+".expected", assertion.Expected, 0, AssertionExpectMax); err != nil {
			return err
		}
		targetSUT, err := assertionTargetSUT(assertionField, assertion, caseSUTs, ctx)
		if err != nil {
			return err
		}
		if assertion.Kind == AssertionDB {
			if assertion.DatabaseID == nil {
				return platform.Invalid(assertionField+".database_id", "db assertions require a database")
			}
			databaseSUT, ok := ctx.DBTOSUT[*assertion.DatabaseID]
			if !ok {
				return platform.Invalid(assertionField+".database_id", "database does not belong to a version SUT")
			}
			if targetSUT != nil && databaseSUT != *targetSUT {
				return platform.Invalid(assertionField+".database_id", "database does not belong to the assertion target SUT")
			}
			if targetSUT == nil {
				if _, ok := caseSUTs[databaseSUT]; !ok {
					return platform.Invalid(assertionField+".database_id", "database does not belong to the case version SUTs")
				}
			}
		}
	}

	if len(item.Sources) > limits.MaxSourcesPerCase {
		return platform.Invalid(field+".sources", "a case may have at most 20 sources")
	}
	sourceKeys := make(map[string]struct{}, len(item.Sources))
	for index, source := range item.Sources {
		sourceField := field + ".sources[" + platformid.Format(int64(index)) + "]"
		if !ValidSourceKind(source.Kind) {
			return platform.Invalid(sourceField+".kind", "must be one of requirement, code, page, api, knowledge")
		}
		if err := validation.String(sourceField+".ref_id", source.RefID, 1, SourceRefIDMax); err != nil {
			return err
		}
		if err := validation.String(sourceField+".path", source.Path, 0, SourcePathMax); err != nil {
			return err
		}
		if source.Line != nil && *source.Line <= 0 {
			return platform.Invalid(sourceField+".line", "must be a positive line number")
		}
		switch source.Kind {
		case SourceRequirement:
			if source.RefID != platformid.Format(ctx.RequirementVersionID) {
				return platform.Invalid(sourceField+".ref_id", "requirement source must reference the current requirement version")
			}
		case SourcePage:
			pageID, err := platformid.Parse(source.RefID)
			if err != nil {
				return platform.Invalid(sourceField+".ref_id", "must be a decimal page ID")
			}
			if _, ok := ctx.PageToSUT[pageID]; !ok {
				return platform.Invalid(sourceField+".ref_id", "page source does not belong to a version SUT")
			}
		case SourceAPI:
			apiID, err := platformid.Parse(source.RefID)
			if err != nil {
				return platform.Invalid(sourceField+".ref_id", "must be a decimal API ID")
			}
			if _, ok := ctx.APIToSUT[apiID]; !ok {
				return platform.Invalid(sourceField+".ref_id", "api source does not belong to a version SUT")
			}
		}
		key := source.Kind + "|" + source.RefID + "|" + source.Path + "|" + formatOptionalInt(source.Line)
		if _, duplicate := sourceKeys[key]; duplicate {
			return platform.Invalid(sourceField, "source is duplicated within the case version")
		}
		sourceKeys[key] = struct{}{}
	}
	return nil
}

// stepTargetSUT validates a step target and returns the explicit target SUT
// when set.
func stepTargetSUT(field string, explicit *int64, pageID, apiID *int64, ctx ProposalContext, caseSUTs map[int64]struct{}) (*int64, error) {
	if pageID != nil {
		sut, ok := ctx.PageToSUT[*pageID]
		if !ok {
			return nil, platform.Invalid(field+".target_page_id", "page does not belong to a version SUT")
		}
		if explicit != nil && sut != *explicit {
			return nil, platform.Invalid(field+".target_page_id", "page does not belong to the step target SUT")
		}
		if explicit == nil {
			if _, ok := caseSUTs[sut]; !ok {
				return nil, platform.Invalid(field+".target_page_id", "page does not belong to the case version SUTs")
			}
		}
	}
	if apiID != nil {
		sut, ok := ctx.APIToSUT[*apiID]
		if !ok {
			return nil, platform.Invalid(field+".target_api_id", "api does not belong to a version SUT")
		}
		if explicit != nil && sut != *explicit {
			return nil, platform.Invalid(field+".target_api_id", "api does not belong to the step target SUT")
		}
		if explicit == nil {
			if _, ok := caseSUTs[sut]; !ok {
				return nil, platform.Invalid(field+".target_api_id", "api does not belong to the case version SUTs")
			}
		}
	}
	return explicit, nil
}

// assertionTargetSUT validates an assertion's optional explicit target SUT and
// API binding and returns the explicit target when set.
func assertionTargetSUT(field string, assertion AssertionProposal, caseSUTs map[int64]struct{}, ctx ProposalContext) (*int64, error) {
	if assertion.TargetVersionSUTID != nil {
		if _, ok := caseSUTs[*assertion.TargetVersionSUTID]; !ok {
			return nil, platform.Invalid(field+".target_version_sut_id", "must be one of the case version SUTs")
		}
	}
	if assertion.TargetAPIID != nil {
		sut, ok := ctx.APIToSUT[*assertion.TargetAPIID]
		if !ok {
			return nil, platform.Invalid(field+".target_api_id", "api does not belong to a version SUT")
		}
		if assertion.TargetVersionSUTID != nil && sut != *assertion.TargetVersionSUTID {
			return nil, platform.Invalid(field+".target_api_id", "api does not belong to the assertion target SUT")
		}
		if assertion.TargetVersionSUTID == nil {
			if _, ok := caseSUTs[sut]; !ok {
				return nil, platform.Invalid(field+".target_api_id", "api does not belong to the case version SUTs")
			}
		}
	}
	return assertion.TargetVersionSUTID, nil
}

func validateTargetSet(field string, ids []int64, allowed map[int64]struct{}, checkAllowed bool) error {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return platform.Invalid(field, "must contain positive decimal IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			return platform.Invalid(field, "must not contain duplicate IDs")
		}
		seen[id] = struct{}{}
		if checkAllowed {
			if _, ok := allowed[id]; !ok {
				return platform.Invalid(field, "must reference version SUTs bound to this version")
			}
		}
	}
	return nil
}

func validateBoundResourceIDs(field string, ids []int64, sutByResource map[int64]int64, caseSUTs map[int64]struct{}) error {
	if err := validateTargetSet(field, ids, nil, false); err != nil {
		return err
	}
	for _, id := range ids {
		sut, ok := sutByResource[id]
		if !ok {
			return platform.Invalid(field, "resource does not belong to a version SUT")
		}
		if _, ok := caseSUTs[sut]; !ok {
			return platform.Invalid(field, "resource does not belong to the case version SUTs")
		}
	}
	return nil
}

// NormalizePriority trims and validates the priority enum, defaulting to P2.
func NormalizePriority(raw string) (string, error) {
	priority := strings.TrimSpace(raw)
	if priority == "" {
		return PriorityP2, nil
	}
	switch priority {
	case PriorityP0, PriorityP1, PriorityP2, PriorityP3:
		return priority, nil
	default:
		return "", platform.Invalid("priority", "must be one of P0, P1, P2, P3")
	}
}

// ValidAssertionKind reports whether kind is a P2 assertion kind.
func ValidAssertionKind(kind string) bool {
	switch kind {
	case AssertionUI, AssertionAPI, AssertionDB, AssertionText, AssertionVisual:
		return true
	default:
		return false
	}
}

// ValidSourceKind reports whether kind is a P2 source kind.
func ValidSourceKind(kind string) bool {
	switch kind {
	case SourceRequirement, SourceCode, SourcePage, SourceAPI, SourceKnowledge:
		return true
	default:
		return false
	}
}

// ValidGraphHash reports whether value is a lowercase hex SHA-256.
func ValidGraphHash(value string) bool {
	if len(value) != GraphHashLength {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// CanTransitionCaseVersion reports whether the P2 state machine allows moving a
// case version from one status to another (spec §4.1).
func CanTransitionCaseVersion(from, to string) bool {
	switch from {
	case CaseStatusDraft:
		return to == CaseStatusConfirmed
	case CaseStatusConfirmed:
		return to == CaseStatusStale || to == CaseStatusSuperseded
	case CaseStatusStale:
		return to == CaseStatusSuperseded
	default:
		return false
	}
}

// NormalizeJSONObject canonicalizes a JSON object column: a blank value is the
// empty object and arrays/scalars are rejected.
func NormalizeJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return nil, platform.Invalid("json", "must be a JSON object")
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, platform.Invalid("json", "must be a JSON object")
	}
	return normalized, nil
}

// NormalizeIDSet validates a list of positive IDs and returns it sorted unique.
func NormalizeIDSet(ids []int64) ([]int64, error) {
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, platform.Invalid("ids", "must contain positive decimal IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized, nil
}

func proposalField(kind string, index int) string {
	return kind + "[" + platformid.Format(int64(index)) + "]"
}

func formatOptionalInt(value *int) string {
	if value == nil {
		return ""
	}
	return platformid.Format(int64(*value))
}

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

// Case version/confirmation triggers (spec DB-28/DB-30). A case version is
// normally proposed by design; discovery auto-confirmation and a rollback
// replacement record their own trigger so the revision stays traceable. A
// human case_confirmation row carries CaseTriggerManual — the confirmation
// column's DB default (chk_case_confirmation_trigger, design_0002/0003), where
// the version column defaults to CaseTriggerNormal. The two CHECK sets are
// pinned against these constants by TestTriggerVocabularyMatchesSchema.
const (
	CaseTriggerNormal         = "normal"
	CaseTriggerDiscoveryDelta = "discovery_delta"
	CaseTriggerRollback       = "rollback"
	CaseTriggerManual         = "manual"
)

// AutoConfirmationActor is the auto_confirmed_by value of a discovery/rollback
// automatic confirmation (spec R-EX-6).
const AutoConfirmationActor = "system"

// Case priorities (spec §3.1).
const (
	PriorityP0 = "P0"
	PriorityP1 = "P1"
	PriorityP2 = "P2"
	PriorityP3 = "P3"
)

// Assertion kinds (spec §4.4).
const (
	AssertionUI   = "ui"
	AssertionAPI  = "api"
	AssertionDB   = "db"
	AssertionText = "text"
)

// Source kinds (spec §4.4).
const (
	SourceRequirement = "requirement"
	SourceCode        = "code"
	SourcePage        = "page"
	SourceAPI         = "api"
	SourceKnowledge   = "knowledge"
)

// Content limits from spec §3/§5.3 and P2-D-18, plus the v0.2.4-B case
// contract (design v1.3.1 §5.2).
const (
	CaseTitleMax       = 200
	CaseDescriptionMax = 20000
	// CaseSutDescriptionMax bounds case_version.sut_description (§5.2:
	// 1..2000). It is deliberately its own constant rather than a reuse of
	// CaseDescriptionMax: the SUT description is a short natural-language
	// locator for the execution team (§3.3), not a free-form case narrative.
	CaseSutDescriptionMax = 2000
	ScenarioNameMax       = 200
	StepActionMax         = 2000
	AssertionTargetMax    = 2000
	AssertionExpectMax    = 2000
	SourceRefIDMax        = 200
	SourcePathMax         = 1000
	AccountRoleMax        = 64
	GraphHashLength       = 64
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
//
// v0.2.4-B (§3.2): a scenario no longer carries resource ID arrays. The version
// binds an environment and the case says what it covers in prose
// (sut_description) and by acceptance-criterion id (covered_acs), so there is
// nothing left for a grouping node to select.
type Scenario struct {
	ID                   int64
	VersionID            int64
	RequirementID        int64
	RequirementVersionID int64
	Name                 string
	Description          string
	SortOrder            int
	CreatedBy            *int64
	CreatedByRunID       *int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ScenarioProposal is one submitted scenario node. ID is set when updating an
// existing scenario; new scenarios omit it.
type ScenarioProposal struct {
	Ref         string
	ID          *int64
	Name        string
	Description string
	SortOrder   int
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
//
// v0.2.4-B (§3.2): the step target is free text. target_version_sut_id /
// target_page_id / target_api_id are gone -- the execution team resolves where a
// step happens from the case's sut_description plus the environment SUT catalog
// (§3.3), so design records the intent ("订单列表页的搜索框") rather than a
// catalog row id.
type CaseStep struct {
	ID                int64
	CaseVersionID     int64
	Seq               int
	Action            string
	Expected          string
	TargetDescription string
	AccountRole       string
	Data              json.RawMessage
	CreatedAt         time.Time
}

// CaseAssertion is one assertion attached to a step sequence number.
//
// v0.2.4-B (§3.2, M-B4): SUTID replaces database_id. A kind=db assertion names
// the SUT whose data it reads; design only checks that one is present, never
// that it belongs to anything (there is no resource scope left to belong to).
type CaseAssertion struct {
	ID            int64
	CaseVersionID int64
	StepSeq       int
	AssertionSeq  int
	Kind          string
	Target        string
	Expected      string
	SUTID         *int64
	CreatedAt     time.Time
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
//
// v0.2.4-B (§3.2/§5.2) replaces the resource-ID target set with the
// natural-language case contract:
//
//	SutDescription  required, 1..2000 -- the system under test, in prose
//	CoveredACs      required, >= 1 and a subset of the requirement's AC id
//	                set -- which acceptance criteria this case implements
//	Preconditions   required, >= 1 -- the scenario-level Given; an explicit
//	                "无" satisfies it (there is nothing to set up)
//
// default_version_sut_id / version_sut_ids / page_ids / api_ids are gone.
type CaseVersion struct {
	ID             int64
	CaseID         int64
	Revision       int
	Title          string
	Description    string
	SutDescription string
	Preconditions  string
	CoveredACs     []string
	TestData       json.RawMessage
	Priority       string
	Status         string
	ContentHash    string
	// Trigger records how the version was produced. The DB column defaults to
	// "normal" (a manual propose); discovery auto-confirmation writes
	// "discovery_delta" and a rollback replacement writes "rollback" (spec
	// DB-28/DB-30, R-EX-6).
	Trigger string
	// SourceObservationID is the execution discovery observation that produced
	// a discovery_delta revision. It is a logical BIGINT reference into the
	// execution context (no cross-context FK).
	SourceObservationID *int64
	// AutoConfirmedBy names the system actor of an automatic confirmation
	// ("system"); empty for manual confirmations.
	AutoConfirmedBy string
	// RollbackOf is the auto-confirmed revision a rollback replacement replaces
	// (set on the replacement, never on the original).
	RollbackOf     *int64
	CreatedBy      *int64
	CreatedByRunID *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Steps          []CaseStep
	Assertions     []CaseAssertion
	Sources        []CaseSource
	// StepCount/AssertionCount/SourceCount are projection-only counts loaded
	// by list/confirmation queries that do not need the full child rows.
	StepCount      int
	AssertionCount int
	SourceCount    int
}

// Minimum executable case content. A case version without an assertion can
// only ever aggregate to inconclusive(no_assertions/incomplete), so it must be
// repaired before it is confirmed or executed (Task 95 / PO O8: the confirmed
// AC-FR-011-01 version carried 1 step and 0 assertions while the design
// reported 5 steps / 3 assertions, and its run ended inconclusive(incomplete)).
const (
	MinimumCaseVersionSteps      = 1
	MinimumCaseVersionAssertions = 1
)

// CaseVersionContentCounts returns the version's step and assertion counts.
// Projection reads (confirmation, graph and propose queries) carry the counts
// without loading the child rows; full reads carry the rows. Both shapes are
// supported so the completeness rule never depends on which query loaded the
// version.
func CaseVersionContentCounts(version CaseVersion) (steps, assertions int) {
	steps, assertions = version.StepCount, version.AssertionCount
	if steps == 0 {
		steps = len(version.Steps)
	}
	if assertions == 0 {
		assertions = len(version.Assertions)
	}
	return steps, assertions
}

// CaseVersionComplete reports whether the version carries the minimum
// executable content: at least one step and at least one assertion.
func CaseVersionComplete(version CaseVersion) bool {
	steps, assertions := CaseVersionContentCounts(version)
	return steps >= MinimumCaseVersionSteps && assertions >= MinimumCaseVersionAssertions
}

// StepProposal is one submitted step.
type StepProposal struct {
	Seq               int
	Action            string
	Expected          string
	TargetDescription string
	AccountRole       string
	Data              json.RawMessage
}

// AssertionProposal is one submitted assertion. SUTID is the SUT a kind=db
// assertion reads; it is ignored for every other kind.
type AssertionProposal struct {
	StepSeq  int
	Kind     string
	Target   string
	Expected string
	SUTID    *int64
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
	Ref            string
	ID             *int64
	ScenarioRef    string
	Title          string
	Description    string
	SutDescription string
	Preconditions  string
	CoveredACs     []string
	TestData       json.RawMessage
	Priority       string
	Steps          []StepProposal
	Assertions     []AssertionProposal
	Sources        []SourceProposal
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
//
// v0.2.4-B: the four resource maps (AllowedVersionSUTs / PageToSUT / APIToSUT /
// DBTOSUT) are gone together with the ownership rules they served (§5.2 "删 SUT
// 归属组后保留集"). What replaces them is RequirementACIDs -- the requirement's
// acceptance-criterion id set, read by GraphRepository.RequirementContext and
// the only thing a case's covered_acs is still checked against (linkage, not
// ownership).
type ProposalContext struct {
	VersionID            int64
	RequirementID        int64
	RequirementVersionID int64
	CurrentHash          string
	// RequirementACIDs is the requirement version's AC id set ("AC-<n>"), in
	// id order. covered_acs must be a non-empty subset of it (§5.2).
	RequirementACIDs      []string
	ExistingScenarioCount int
	ExistingCaseCount     int
	Limits                Limits
}

// ValidateProposal applies the structural rules of the v0.2.4-B case contract
// (design v1.3.1 §5.2 "删 SUT 归属组后保留集").
//
// RETAINED: base_graph_hash CAS, the per-proposal and per-requirement limits,
// scenario/case ref uniqueness, the scenario-name and case-title natural keys,
// positive-ID checks, every field length/enum rule, contiguous step seq,
// assertion step_seq back-reference, source kind/length and the requirement
// source revision binding, and source deduplication.
//
// RETIRED (not silently dropped): the whole SUT/page/api/database ownership
// group. A version binds an environment and a case now says what it covers in
// prose (sut_description) and by acceptance-criterion id (covered_acs), so
// there is no resource scope left for a proposal to be checked against. What
// replaces it is the covered_acs linkage rule below: every id must exist in the
// requirement's AC set. Scope validation as such no longer exists in design.
//
// Existence and natural-key resolution against already-stored rows is performed
// by the application service before calling this function.
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
		if err := validateCaseProposal(item, field, ctx, limits); err != nil {
			return err
		}
	}
	return nil
}

// validateCaseProposal applies the §5.2 case contract to one proposed case.
//
// The ownership group is gone (see ValidateProposal). What is added in its
// place: sut_description required (1..2000), preconditions required (>= 1, an
// explicit "无" counts as a stated precondition), and covered_acs required
// (>= 1) with every id present in the requirement's AC set.
func validateCaseProposal(item CaseProposal, field string, ctx ProposalContext, limits Limits) error {
	if err := validation.String(field+".title", strings.TrimSpace(item.Title), 1, CaseTitleMax); err != nil {
		return err
	}
	if err := validation.String(field+".description", item.Description, 0, CaseDescriptionMax); err != nil {
		return err
	}
	if err := validation.String(field+".sut_description", strings.TrimSpace(item.SutDescription), 1, CaseSutDescriptionMax); err != nil {
		return err
	}
	// §5.2: preconditions is the scenario-level Given and is required. An
	// explicit "无" satisfies it -- the author states there is nothing to set
	// up, which is information; a blank value states nothing at all.
	if err := validation.String(field+".preconditions", strings.TrimSpace(item.Preconditions), 1, CaseDescriptionMax); err != nil {
		return err
	}
	if err := validateCoveredACs(field+".covered_acs", item.CoveredACs, ctx.RequirementACIDs); err != nil {
		return err
	}
	if _, err := NormalizePriority(item.Priority); err != nil {
		return err
	}
	if _, err := NormalizeJSONObject(item.TestData); err != nil {
		return platform.Invalid(field+".test_data", "must be a JSON object")
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
		if err := validation.String(stepField+".expected", step.Expected, 1, CaseDescriptionMax); err != nil {
			return err
		}
		// target_description is optional (§5.2) but bounded like the other step
		// text: it is a locator for the execution team, not a narrative.
		if err := validation.String(stepField+".target_description", step.TargetDescription, 0, StepActionMax); err != nil {
			return err
		}
		if err := validation.String(stepField+".account_role", step.AccountRole, 0, AccountRoleMax); err != nil {
			return err
		}
		if _, err := NormalizeJSONObject(step.Data); err != nil {
			return platform.Invalid(stepField+".data", "must be a JSON object")
		}
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
			return platform.Invalid(assertionField+".kind", "must be one of ui, api, db, text")
		}
		if err := validation.String(assertionField+".target", assertion.Target, 1, AssertionTargetMax); err != nil {
			return err
		}
		if err := validation.String(assertionField+".expected", assertion.Expected, 1, AssertionExpectMax); err != nil {
			return err
		}
		// §5.2 / Lead ruling 6: a db assertion must name the SUT it reads.
		// Presence only -- there is no resource scope left to check ownership
		// against, and inventing one would re-introduce exactly the coupling
		// v0.2.4-B removes.
		if assertion.Kind == AssertionDB && (assertion.SUTID == nil || *assertion.SUTID <= 0) {
			return platform.Invalid(assertionField+".sut_id", "db assertions require a sut_id")
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
		// Only the requirement source keeps a binding rule: it must point at the
		// requirement version being designed, which is what makes a case stale
		// when the requirement moves. The page/api sources keep their kind and
		// shape but lost the ownership lookup (§5.2 "page/api/knowledge 可选引用
		// 校验"): with no resource scope in design there is nothing to resolve a
		// page/api ref_id against, so they are recorded as provenance text.
		if source.Kind == SourceRequirement && source.RefID != platformid.Format(ctx.RequirementVersionID) {
			return platform.Invalid(sourceField+".ref_id", "requirement source must reference the current requirement version")
		}
		key := source.Kind + "|" + source.RefID + "|" + source.Path + "|" + formatOptionalInt(source.Line)
		if _, duplicate := sourceKeys[key]; duplicate {
			return platform.Invalid(sourceField, "source is duplicated within the case version")
		}
		sourceKeys[key] = struct{}{}
	}
	return nil
}

// validateCoveredACs enforces the §5.2 covered_acs linkage rule: at least one
// id, and every id present in the requirement's acceptance-criterion set.
//
// Both sides go through NormalizeACIDSet -- the SAME function the content hash
// (hash.go) and the propose write path (graph.go buildCaseVersion) use. That is
// deliberate and load-bearing: if the gate validated a differently-normalized set
// than the one that gets hashed and stored, a proposal could pass the gate and
// then hash to something else. Normalization is not re-implemented here.
//
// Blanks are rejected BEFORE normalizing, because NormalizeACIDSet drops them as
// noise -- correct for canonicalization, wrong for a gate, where an empty element
// is a caller error that must be reported rather than silently discarded.
//
// This is the only thing that replaced the retired ownership group, and it
// fails closed in both directions. An empty requirementACIDs rejects every
// proposal rather than vacuously accepting one: a case that claims to cover an
// acceptance criterion of a requirement that has none is not a case this
// contract can describe, and silently accepting it would hide a requirement that
// was never clarified into structured criteria.
//
// DANGLING IDS FAIL LOUDLY, NEVER SILENTLY. AC ids are server-assigned and
// stable across revisions by identical content (§3.6), but two paths can still
// retire an id a case points at: editing a criterion's g/w/t holes the old id and
// mints a new one, and version-side Merge re-numbers an absorbed requirement's
// criteria against the target's set. Both move the requirement's current
// revision, so the affected cases are already refused by the confirmation gate
// ("bound to an older requirement version") and marked stale; this check is what
// stops the redesign from silently re-proposing the dead id. A dangling link is
// therefore a 400 naming the id, not a dropped element.
//
// Duplicates are not an error. covered_acs is a set (§3.6 normalizes it
// sorted-unique for the content hash), so a repeated id carries no extra
// meaning and rejecting it would make the gate stricter than the value it
// guards. This is NOT a workaround for requirement-side AC id churn: deduping
// duplicate identical criteria belongs to the requirement write path
// (version/domain/acceptance_criteria.go), which is the single source of that
// rule.
func validateCoveredACs(field string, coveredACs, requirementACIDs []string) error {
	for _, raw := range coveredACs {
		if strings.TrimSpace(raw) == "" {
			return platform.Invalid(field, "must not contain an empty acceptance-criterion id")
		}
	}
	normalized := NormalizeACIDSet(coveredACs)
	if len(normalized) == 0 {
		return platform.Invalid(field, "at least one covered acceptance criterion is required")
	}
	allowed := make(map[string]struct{}, len(requirementACIDs))
	for _, acID := range NormalizeACIDSet(requirementACIDs) {
		allowed[acID] = struct{}{}
	}
	if len(allowed) == 0 {
		return platform.Invalid(field, "the requirement has no acceptance criteria to cover; clarify the requirement first")
	}
	for _, acID := range normalized {
		if _, ok := allowed[acID]; !ok {
			return platform.Invalid(field, "acceptance criterion "+acID+
				" is not in this requirement version's acceptance criteria; it was retired by an edit or renumbered by a merge -- re-read the requirement and set covered_acs to current ids")
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
	case AssertionUI, AssertionAPI, AssertionDB, AssertionText:
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

// NormalizeACIDSet canonicalizes a covered_acs value: each id trimmed, blanks
// dropped, duplicates removed, result sorted.
//
// It is THE single normalization for AC id sets, and all three consumers call it
// rather than re-implementing it, so "what the gate validated", "what got stored"
// and "what got hashed" cannot drift apart:
//
//   - validateCoveredACs (this file) -- the §5.2 linkage gate, both the
//     proposal's covered_acs and the requirement's AC set
//   - ComputeCaseContentHash (hash.go) -- the canonical payload member
//   - buildCaseVersion (application/graph.go) -- the propose write path
//
// A fourth caller, InsertCaseVersion in the PostgreSQL adapter, normalizes again
// before encoding, so a version built outside the propose path still stores the
// canonical form.
//
// Blanks are dropped here because canonicalization treats an empty element as
// noise; a GATE that wants to reject blanks must check for them before calling
// (validateCoveredACs does).
//
// The sort is plain lexicographic. All the hash needs is one deterministic
// total order so that {AC-1, AC-2} and {AC-2, AC-1} produce identical bytes;
// the numeric-suffix ordering the requirement context uses to *present* its AC
// list (AC-2 before AC-10) is a display concern of that context and deliberately
// not duplicated here.
//
// It replaces the v0.22 NormalizeIDSet, whose positive-decimal-ID rule has no
// meaning for "AC-<n>" strings.
func NormalizeACIDSet(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Strings(normalized)
	return normalized
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

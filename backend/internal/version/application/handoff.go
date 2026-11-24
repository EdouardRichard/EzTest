package versionapp

// Task 27 (v0.22 B §8.1/spec R-B5-1/R-B5-2): the clarification -> design
// handoff pack.
//
// HandoffService builds one version-scoped JSON document with the six blocks
// the Design Host needs (requirements, qa, decisions, open, plan, resources),
// persists it as the next clarification_handoff revision and publishes
// handoff.updated when the content actually changed. Identical content is a
// no-op: no new revision and no event, so re-running the generator on an
// unchanged clarification state is idempotent.
//
// Wiring: the stage machine owns the requirement_review -> designing transition
// (Task 25) and must call Generate when it advances, or the composition root
// subscribes to version.stage.changed and does the same (Task 36). This task
// deliberately does not register the handler route or call the service from the
// stage machine.
//
// The read side is two injected ports: HandoffReader is implemented over the
// version context's own stores (requirements/requirements_facts/plan) plus the
// clarification conversation/question rows, and resourceapp.HandoffResourceReader
// supplies the resource-owned half of the resources block (H-B1: the resource
// catalog is reached through the resource context's port, never by reading its
// tables). The application layer never touches SQL. The pack is a FUNCTIONAL
// PROJECTION, not credential scrubbing (GD-01 T5b-D ruling, A/B/C framework:
// C-class scope summary): it carries requirement/resource summaries only — no raw
// chat transcripts, and neither port carries password/DSN columns. Every
// free-text field is bounded and slices are capped before persistence.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Handoff artifact errors.
var (
	// ErrHandoffNotFound reports that a version has no clarification_handoff row
	// (or no row for the requested revision).
	ErrHandoffNotFound = errors.New("clarification handoff not found")
	// ErrHandoffRevisionConflict reports that another generator wrote the same
	// next revision. HandoffService retries against the refreshed latest row.
	ErrHandoffRevisionConflict = errors.New("clarification handoff revision conflict")
)

// Handoff content bounds. They keep the persisted JSONB document small and
// guarantee that a malformed/oversized read model can never blow up the
// designing context or leak an unbounded chat transcript.
const (
	HandoffMaxRequirements = 200
	HandoffMaxQA           = 100
	HandoffMaxDecisions    = 100
	HandoffMaxOpen         = 100
	HandoffMaxResources    = 200
	HandoffMaxSourceRefs   = 10

	HandoffTitleMax          = 500
	HandoffUserStoryPartMax  = 500
	HandoffRulesMax          = 20000
	HandoffSutDescriptionMax = 2000
	HandoffScopeOutMax       = 2000
	HandoffACPartMax         = 2000
	// HandoffMaxAC mirrors the domain cap so the handoff can never silently
	// truncate a criterion set the judge still scores in full (single source).
	HandoffMaxAC             = version.MaxAcceptanceCriteria
	HandoffAccountRoleMax    = 64
	HandoffMaxAccountRoles   = 20
	HandoffSourceQuoteMax    = 1000
	HandoffQuestionMax       = 2000
	HandoffOptionLabelMax    = 200
	HandoffAnswerMax         = 2000
	HandoffFactSummaryMax    = 1000
	HandoffKeyMax            = 200
	HandoffResourceNameMax   = 300
	HandoffResourceURLMax    = 500
	HandoffEnvironmentMax    = 200
	HandoffStatusMax         = 64
	HandoffSourceMax         = 100
	handoffCreateAttempts    = 3
	handoffUpdatedEventType  = "handoff.updated"
	handoffVersionChannelTag = "version:"
)

// Handoff source-reference schemes: the typed prefixes of the source refs in the
// open/decisions blocks and the clarification.search hits. They live here in the
// producer (versionapp) so the versionpg reader references the same single source
// (versionpg imports versionapp, not the reverse). The requirement prefix is the
// same requirement-reference vocabulary as the version_fact key, so it aliases
// the version domain constant instead of repeating the literal (R-H9; the handoff
// vocabulary lint gate enforces this).
const (
	HandoffSourceQA          = "human_request:"
	HandoffSourceFact        = "version_fact:"
	HandoffSourceMessage     = "conversation_message:"
	HandoffSourceRequirement = version.RequirementFactKeyPrefix
)

// HandoffRepositorySourceSUT is the Source value of a handoff repository row: every
// repository is SUT-inherited from the environment code pool (the version
// repository overrides were removed with the version_sut chain).
const HandoffRepositorySourceSUT = "sut"

// Handoff is the version-level clarification handoff document (B §8.1, DB-22).
// VersionID/Revision/GeneratedAt identify the artifact revision; the six blocks
// are the payload injected into the Design Host (R-B5-2). The JSON tags are the
// persisted clarification_handoff.content shape.
type Handoff struct {
	VersionID   int64     `json:"version_id"`
	Revision    int       `json:"revision"`
	GeneratedAt time.Time `json:"generated_at"`

	Requirements []HandoffRequirement `json:"requirements"`
	QA           []HandoffQAItem      `json:"qa"`
	Decisions    []HandoffDecision    `json:"decisions"`
	Open         []HandoffOpenItem    `json:"open"`
	Plan         HandoffPlan          `json:"plan"`
	Resources    HandoffResources     `json:"resources"`
}

// HandoffUserStory is the three-part user story of the requirement V1 contract
// (design v1.3.1 §5.1). It is a TYPE ALIAS of the domain UserStory, so the handoff
// projection is structurally the requirement contract itself (constitution
// 五.1/五.2: single source, derived representation) — the JSON keys and field set
// cannot drift from requirement_version because they are the same type.
type HandoffUserStory = version.UserStory

// HandoffAcceptanceCriterion is one Gherkin-shaped acceptance criterion of the
// requirement V1 contract (§5.1), projected from requirement_version.acceptance_criteria.
// It is a TYPE ALIAS of the domain AcceptanceCriterion for the same single-source
// reason as HandoffUserStory.
//
// ID is the server-assigned "AC-<n>" carried verbatim from the stored revision.
// It is NOT a durable identifier: completing an extraction stub's given/when is
// an edit under §3.6, so the old id retires and a new one is appended (T8 §13).
// The handoff carries whatever id the current revision holds and never treats it
// as stable across revisions.
type HandoffAcceptanceCriterion = version.AcceptanceCriterion

// HandoffRequirement is one draft/clarified requirement of the handoff, as a V1
// structural projection of requirement_version (R-B1, design §5.1). It replaces
// the old flat Description/AcceptanceCriteria strings with the user story, the
// structured acceptance-criteria array and the natural-language sut_description.
// Rules/ScopeOut/AccountRolesHint carry the optional §5.1 contract fields the
// Design Host briefs on (business rules, the over-coverage boundary and the
// account-role hint); see the task-17 report for the field-by-field rationale.
type HandoffRequirement struct {
	ID                 int64                        `json:"id"`
	CurrentVersionID   int64                        `json:"current_version_id"`
	Revision           int                          `json:"revision"`
	Title              string                       `json:"title"`
	UserStory          HandoffUserStory             `json:"user_story"`
	Rules              string                       `json:"rules"`
	AcceptanceCriteria []HandoffAcceptanceCriterion `json:"acceptance_criteria"`
	Priority           string                       `json:"priority"`
	SutDescription     string                       `json:"sut_description"`
	ScopeOut           string                       `json:"scope_out"`
	AccountRolesHint   []string                     `json:"account_roles_hint"`
	Status             string                       `json:"status"`
	SourceQuote        string                       `json:"source_quote"`
	Confidence         *float64                     `json:"confidence"`
	ConfirmedAt        *time.Time                   `json:"confirmed_at"`
}

// HandoffQAOption is one option of a structured question.
type HandoffQAOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// HandoffQAItem is one structured question/answer pair. Pending and expired
// questions are included (status pending/expired) and are also listed in the
// open block as unresolved items (B §8.1, R-B3-4).
type HandoffQAItem struct {
	RequestID       int64             `json:"request_id"`
	MessageID       int64             `json:"message_id"`
	ConversationID  int64             `json:"conversation_id"`
	QuestionKey     string            `json:"question_key"`
	Question        string            `json:"question"`
	Options         []HandoffQAOption `json:"options"`
	SourceRefs      []string          `json:"source_refs"`
	RequirementID   int64             `json:"requirement_id"`
	RequirementRefs []int64           `json:"requirement_refs"`
	Status          string            `json:"status"`
	AnswerSelected  []string          `json:"answer_selected"`
	AnswerText      string            `json:"answer_text"`
	AskedAt         time.Time         `json:"asked_at"`
	AnsweredAt      *time.Time        `json:"answered_at"`
}

// HandoffDecision is one current version_fact of kind decision/constraint.
type HandoffDecision struct {
	FactID     int64     `json:"fact_id"`
	Kind       string    `json:"kind"`
	Key        string    `json:"key"`
	Summary    string    `json:"summary"`
	Confidence *float64  `json:"confidence"`
	Source     string    `json:"source"`
	SourceRef  string    `json:"source_ref"`
	CreatedAt  time.Time `json:"created_at"`
}

// HandoffOpenItem is one unresolved question/gap/assumption or unconfirmed
// draft requirement. QuestionKey identifies which question of a multi-question
// human_request batch this item is (empty for non-question items); it is part of
// the item identity so a batch never collapses to a single open item.
type HandoffOpenItem struct {
	Kind        string     `json:"kind"`
	Summary     string     `json:"summary"`
	Status      string     `json:"status"`
	Source      string     `json:"source"`
	ReferenceID int64      `json:"reference_id"`
	QuestionKey string     `json:"question_key,omitempty"`
	CreatedAt   *time.Time `json:"created_at"`
}

// HandoffPlanItem is one plan item shown in the plan block.
type HandoffPlanItem struct {
	ID       int64  `json:"id"`
	Phase    string `json:"phase"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Decision string `json:"decision"`
}

// HandoffPlan is the current stage plus the current/next plan items (B §8.1).
type HandoffPlan struct {
	Stage   string           `json:"stage"`
	Current *HandoffPlanItem `json:"current"`
	Next    *HandoffPlanItem `json:"next"`
}

// HandoffEnvironment is the environment summary at the head of the resources
// block (design §3.2 C-A1: "resources 块去 SUT ID→环境摘要，落到字段"). A version
// binds one environment directly (version.environment_id); the block describes
// that environment and its SUTs, never a version_sut binding-row id.
type HandoffEnvironment struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// HandoffSUT is one SUT of the bound environment, carrying its EFFECTIVE asset
// scope (approved pages/APIs, active databases). It has no version_sut_id: the
// version_sut chain was removed (design §3.1/§3.2) and the SUT is reached through
// the environment, not a binding row. The nested assets are the effective
// projection (the same rule as resourceapp.SUTView.EffectiveAssets), never the
// raw catalog, so a proposed page or a disabled database can never enter the
// design pack (the trust-boundary invariant of interfaces-w2-ports.md).
type HandoffSUT struct {
	SUTID     int64             `json:"sut_id"`
	Name      string            `json:"name"`
	BaseURL   string            `json:"base_url"`
	Pages     []HandoffPage     `json:"pages"`
	APIs      []HandoffAPI      `json:"apis"`
	Databases []HandoffDatabase `json:"databases"`
}

// HandoffPage is one approved page of a SUT. It carries no version_sut_id.
type HandoffPage struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	RouteTemplate string `json:"route_template"`
}

// HandoffAPI is one approved API of a SUT. It carries no version_sut_id.
type HandoffAPI struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

// HandoffAccount is one enabled version account. It never carries a password.
type HandoffAccount struct {
	ID       int64    `json:"id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	Role     string   `json:"role"`
}

// HandoffDatabase is one active target database of a SUT. It never carries a DSN
// or credentials. SUTID is rendered per row because a design db assertion
// (assertions[].kind=db) takes sut_id, not the database id: a row carrying only the
// database id invites the model to send the wrong one under presence-only
// validation (the same hazard T9 fixed on the pack in agent context.go).
type HandoffDatabase struct {
	ID       int64  `json:"id"`
	SUTID    int64  `json:"sut_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	ReadOnly bool   `json:"read_only"`
}

// HandoffRepository is one repository of the environment code pool (GD-03): the
// union of the enabled SUT repositories of the bound environment. The version
// repository overrides were removed with the version_sut chain, so Source is
// always "sut".
type HandoffRepository struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Branch string `json:"branch"`
	Source string `json:"source"`
}

// HandoffResources is the environment summary block (C resources, spec R-B5-1;
// design §3.2 C-A1). It is headed by the bound environment and its SUTs (each
// with its effective pages/APIs/databases), plus the version's enabled accounts
// and the environment code pool. There is no version_sut_id anywhere and no
// password/DSN: the block is a scope summary for the Design Host, derived from
// the effective projection of the bound environment, never the raw catalog.
//
// The SUT assets share ONE aggregate budget of HandoffMaxResources entries across
// every SUT and kind, consumed in catalog order, so the JSONB document fed to the
// Design Host prompt stays bounded. Truncated records that the budget (or a block
// cap) dropped rows, so the Host knows the set is partial rather than silently
// receiving a partial truth (constitution 七.1).
type HandoffResources struct {
	Environment  HandoffEnvironment  `json:"environment"`
	SUTs         []HandoffSUT        `json:"suts"`
	Accounts     []HandoffAccount    `json:"accounts"`
	Repositories []HandoffRepository `json:"repositories"`
	Truncated    bool                `json:"truncated"`
}

// HandoffFact is one current version_fact row of the handoff read port. The
// service projects decision/constraint facts into the decisions block and
// gap/assumption/risk facts into the open block.
type HandoffFact struct {
	ID         int64
	Kind       string
	Key        string
	Summary    string
	Confidence *float64
	Source     string
	SourceRef  string
	CreatedAt  time.Time
}

// HandoffRecord is one persisted clarification_handoff row: the document plus
// the row identity/timestamps.
type HandoffRecord struct {
	ID        int64
	Handoff   Handoff
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HandoffStore persists the append-only revision sequence of one version's
// clarification handoff. Create must enforce the unique (version_id, revision)
// key and return ErrHandoffRevisionConflict on a race; Latest with revision 0
// returns the newest revision and ErrHandoffNotFound when the version has none.
type HandoffStore interface {
	Latest(ctx context.Context, versionID int64, revision int) (HandoffRecord, error)
	Create(ctx context.Context, record HandoffRecord) (HandoffRecord, error)
}

// HandoffReader is the read-only source of the six handoff blocks. The
// production adapter reads the version context's own stores and the
// clarification question rows; it returns functional summaries only — never
// raw chat transcripts, and never password/DSN columns (C-class projection,
// GD-01 T5b-D; not a redaction mechanism).
type HandoffReader interface {
	VersionExists(ctx context.Context, versionID int64) (bool, error)
	// Requirements returns the current draft/clarified requirement revisions
	// with acceptance criteria, priority and source_quote. Closed (historical)
	// rows are excluded per B §8.1.
	Requirements(ctx context.Context, versionID int64) ([]HandoffRequirement, error)
	// QA returns the version's structured clarification questions, answered
	// and pending/expired included.
	QA(ctx context.Context, versionID int64) ([]HandoffQAItem, error)
	// Facts returns current version_fact rows of the kinds the handoff needs:
	// decision, constraint, gap, assumption, risk.
	Facts(ctx context.Context, versionID int64) ([]HandoffFact, error)
	// Plan returns the current stage and the current/next plan items.
	Plan(ctx context.Context, versionID int64) (HandoffPlan, error)
	// ResourceScope returns the VERSION-OWNED input to the resources block: the
	// bound environment id and the enabled account association ids. The resource
	// catalog itself is read through resourceapp.HandoffResourceReader (H-B1), so
	// this port never reads another context's tables.
	ResourceScope(ctx context.Context, versionID int64) (HandoffResourceScope, error)
}

// HandoffService builds and reads the version-level clarification handoff.
type HandoffService struct {
	store     HandoffStore
	reader    HandoffReader
	resources resourceapp.HandoffResourceReader
	ids       platformid.Generator
	events    eventingapp.Publisher
}

// NewHandoffService wires the handoff store, the version-owned read port and the
// resource read port. ids assigns clarification_handoff primary keys; events may
// be nil in degraded assemblies (event delivery is best effort and never rolls
// back the stored revision).
func NewHandoffService(
	store HandoffStore,
	reader HandoffReader,
	resources resourceapp.HandoffResourceReader,
	ids platformid.Generator,
	events eventingapp.Publisher,
) *HandoffService {
	return &HandoffService{store: store, reader: reader, resources: resources, ids: ids, events: events}
}

// Generate builds the handoff document from the current clarification state,
// persists it as a new revision when the six blocks changed and publishes
// handoff.updated on version:{id}. Identical content is idempotent: the latest
// stored revision is returned without writing a row or publishing an event.
// The revision starts at 1 and increments by one per changed regeneration.
func (s *HandoffService) Generate(ctx context.Context, versionID int64) (Handoff, error) {
	if versionID <= 0 {
		return Handoff{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if s == nil || s.reader == nil {
		return Handoff{}, platform.Unavailable("clarification handoff reader is not configured")
	}
	if s.store == nil {
		return Handoff{}, platform.Unavailable("clarification handoff store is not configured")
	}
	if s.resources == nil {
		return Handoff{}, platform.Unavailable("clarification handoff resource reader is not configured")
	}
	document, err := s.build(ctx, versionID)
	if err != nil {
		return Handoff{}, err
	}

	latest, latestErr := s.store.Latest(ctx, versionID, 0)
	if latestErr != nil && !errors.Is(latestErr, ErrHandoffNotFound) {
		return Handoff{}, latestErr
	}
	if latestErr == nil && handoffSignature(latest.Handoff) == handoffSignature(document) {
		return latest.Handoff, nil
	}

	for attempt := 0; attempt < handoffCreateAttempts; attempt++ {
		revision := 1
		if latestErr == nil {
			revision = latest.Handoff.Revision + 1
		}
		candidate := document
		candidate.VersionID = versionID
		candidate.Revision = revision
		candidate.GeneratedAt = time.Now().UTC()

		stored, createErr := s.store.Create(ctx, HandoffRecord{ID: s.ids.Next(), Handoff: candidate})
		if createErr == nil {
			s.publishUpdated(ctx, stored.Handoff)
			return stored.Handoff, nil
		}
		if !errors.Is(createErr, ErrHandoffRevisionConflict) {
			return Handoff{}, createErr
		}
		// Another generator won the revision race. Re-read and retry; if it
		// stored identical content the idempotent no-op wins and no duplicate
		// revision is written.
		latest, latestErr = s.store.Latest(ctx, versionID, 0)
		if errors.Is(latestErr, ErrHandoffNotFound) {
			latestErr = nil
			continue
		}
		if latestErr != nil {
			return Handoff{}, latestErr
		}
		if handoffSignature(latest.Handoff) == handoffSignature(document) {
			return latest.Handoff, nil
		}
	}
	return Handoff{}, platform.Conflict("clarification handoff revision changed concurrently")
}

// Latest returns the newest persisted handoff of a version, or a specific
// revision when revision > 0. A version without a handoff (or without that
// revision) is a typed 404; the HTTP handler relies on it.
func (s *HandoffService) Latest(ctx context.Context, versionID int64, revision int) (Handoff, error) {
	if versionID <= 0 {
		return Handoff{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if revision < 0 {
		return Handoff{}, platform.Invalid("revision", "must be a positive integer")
	}
	if s == nil || s.store == nil {
		return Handoff{}, platform.Unavailable("clarification handoff store is not configured")
	}
	record, err := s.store.Latest(ctx, versionID, revision)
	if err != nil {
		if errors.Is(err, ErrHandoffNotFound) {
			return Handoff{}, platform.NotFound("clarification handoff")
		}
		return Handoff{}, err
	}
	return record.Handoff, nil
}

// build assembles and bounds the six-block document from the read ports.
func (s *HandoffService) build(ctx context.Context, versionID int64) (Handoff, error) {
	exists, err := s.reader.VersionExists(ctx, versionID)
	if err != nil {
		return Handoff{}, err
	}
	if !exists {
		return Handoff{}, platform.NotFound("version")
	}
	requirements, err := s.reader.Requirements(ctx, versionID)
	if err != nil {
		return Handoff{}, err
	}
	qa, err := s.reader.QA(ctx, versionID)
	if err != nil {
		return Handoff{}, err
	}
	facts, err := s.reader.Facts(ctx, versionID)
	if err != nil {
		return Handoff{}, err
	}
	plan, err := s.reader.Plan(ctx, versionID)
	if err != nil {
		return Handoff{}, err
	}
	resources, err := s.handoffResources(ctx, versionID)
	if err != nil {
		return Handoff{}, err
	}

	document := Handoff{
		VersionID:    versionID,
		Requirements: normalizeHandoffRequirements(requirements),
		QA:           normalizeHandoffQA(qa),
		Plan:         normalizeHandoffPlan(plan),
		Resources:    normalizeHandoffResources(resources),
	}
	decisions, openFromFacts := splitHandoffFacts(normalizeHandoffFacts(facts))
	document.Decisions = decisions
	open := openHandoffQAItems(document.QA)
	open = append(open, openFromFacts...)
	open = append(open, openHandoffDraftRequirements(document.Requirements)...)
	document.Open = normalizeHandoffOpen(open)
	return document, nil
}

// publishUpdated emits the handoff.updated contract event (B §8.1, §10.2). The
// payload is a summary only: version, revision, timestamp and the block names.
func (s *HandoffService) publishUpdated(ctx context.Context, document Handoff) {
	if s.events == nil {
		return
	}
	versionID := platformid.Format(document.VersionID)
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel: handoffVersionChannelTag + versionID,
		Type:    handoffUpdatedEventType,
		Scope:   map[string]any{"version_id": versionID},
		Payload: map[string]any{
			"version_id":   versionID,
			"revision":     document.Revision,
			"generated_at": document.GeneratedAt.UTC().Format(time.RFC3339),
			"sections": []string{
				"requirements", "qa", "decisions", "open", "plan", "resources",
			},
		},
		RequestID: requestid.From(ctx),
	})
}

// ---------- normalization (text/JSON safety) ----------

func normalizeHandoffRequirements(items []HandoffRequirement) []HandoffRequirement {
	normalized := make([]HandoffRequirement, 0, len(items))
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		item.Status = strings.ToLower(strings.TrimSpace(truncateHandoffText(item.Status, HandoffStatusMax)))
		// B §8.1: the handoff carries confirmed and draft requirements only.
		// Closed rows are historical and must not enter the design pack.
		if !HandoffRequirementStatusIncluded(item.Status) {
			continue
		}
		item.Title = truncateHandoffText(item.Title, HandoffTitleMax)
		item.UserStory = normalizeHandoffUserStory(item.UserStory)
		item.Rules = truncateHandoffText(item.Rules, HandoffRulesMax)
		item.AcceptanceCriteria = normalizeHandoffAcceptanceCriteria(item.AcceptanceCriteria)
		item.Priority = truncateHandoffText(item.Priority, HandoffStatusMax)
		item.SutDescription = truncateHandoffText(item.SutDescription, HandoffSutDescriptionMax)
		item.ScopeOut = truncateHandoffText(item.ScopeOut, HandoffScopeOutMax)
		item.AccountRolesHint = normalizeHandoffRefs(item.AccountRolesHint, HandoffMaxAccountRoles, HandoffAccountRoleMax)
		item.SourceQuote = truncateHandoffText(item.SourceQuote, HandoffSourceQuoteMax)
		item.Confidence = normalizeHandoffConfidence(item.Confidence)
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	// Keep the newest rows when the block overflows: an old requirement must
	// not crowd out the current clarification state.
	if len(normalized) > HandoffMaxRequirements {
		normalized = normalized[len(normalized)-HandoffMaxRequirements:]
	}
	if normalized == nil {
		return []HandoffRequirement{}
	}
	return normalized
}

// normalizeHandoffUserStory bounds each part of the projected user story. The
// values are carried verbatim from requirement_version (a derived projection);
// the bound only defends the persisted document against a malformed read model.
func normalizeHandoffUserStory(story HandoffUserStory) HandoffUserStory {
	return HandoffUserStory{
		AsA:    truncateHandoffText(story.AsA, HandoffUserStoryPartMax),
		IWant:  truncateHandoffText(story.IWant, HandoffUserStoryPartMax),
		SoThat: truncateHandoffText(story.SoThat, HandoffUserStoryPartMax),
	}
}

// normalizeHandoffAcceptanceCriteria bounds each projected criterion and caps the
// array. The server-assigned ids are carried verbatim in the stored order; the
// handoff is a projection, not a numbering authority, and never re-assigns or
// re-sorts ids (the stored revision is already id-sorted by the domain).
func normalizeHandoffAcceptanceCriteria(criteria []HandoffAcceptanceCriterion) []HandoffAcceptanceCriterion {
	normalized := make([]HandoffAcceptanceCriterion, 0, len(criteria))
	for _, criterion := range criteria {
		item := HandoffAcceptanceCriterion{
			ID:    truncateHandoffText(criterion.ID, HandoffStatusMax),
			Given: truncateHandoffText(criterion.Given, HandoffACPartMax),
			When:  truncateHandoffText(criterion.When, HandoffACPartMax),
			Then:  truncateHandoffText(criterion.Then, HandoffACPartMax),
		}
		if item.ID == "" && item.Given == "" && item.When == "" && item.Then == "" {
			continue
		}
		normalized = append(normalized, item)
		if len(normalized) == HandoffMaxAC {
			break
		}
	}
	if normalized == nil {
		return []HandoffAcceptanceCriterion{}
	}
	return normalized
}

// HandoffRequirementStatusIncluded reports whether a requirement status belongs
// in the requirements block (draft/clarified only, B §8.1). The statuses are the
// version domain's own constants, the same source the reader's SQL filter uses.
// Exported so tests assert the real inclusion rule rather than re-implementing it.
func HandoffRequirementStatusIncluded(status string) bool {
	switch status {
	case string(version.RequirementDraft), string(version.RequirementClarified):
		return true
	default:
		return false
	}
}

func normalizeHandoffQA(items []HandoffQAItem) []HandoffQAItem {
	normalized := make([]HandoffQAItem, 0, len(items))
	for _, item := range items {
		if item.RequestID <= 0 {
			continue
		}
		item.QuestionKey = truncateHandoffText(item.QuestionKey, 64)
		item.Question = truncateHandoffText(item.Question, HandoffQuestionMax)
		item.Status = truncateHandoffText(item.Status, HandoffStatusMax)
		item.AnswerText = truncateHandoffText(item.AnswerText, HandoffAnswerMax)
		item.Options = normalizeHandoffQOptions(item.Options)
		item.SourceRefs = normalizeHandoffRefs(item.SourceRefs, HandoffMaxSourceRefs, HandoffResourceURLMax)
		item.AnswerSelected = normalizeHandoffRefs(item.AnswerSelected, HandoffMaxSourceRefs, 64)
		item.RequirementRefs = uniqueHandoffIDs(item.RequirementRefs)
		if item.RequirementID <= 0 && len(item.RequirementRefs) > 0 {
			item.RequirementID = item.RequirementRefs[0]
		}
		if item.RequirementID > 0 {
			item.RequirementRefs = uniqueHandoffIDs(append([]int64{item.RequirementID}, item.RequirementRefs...))
		}
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if !normalized[i].AskedAt.Equal(normalized[j].AskedAt) {
			return normalized[i].AskedAt.Before(normalized[j].AskedAt)
		}
		return normalized[i].RequestID < normalized[j].RequestID
	})
	if len(normalized) > HandoffMaxQA {
		normalized = normalized[:HandoffMaxQA]
	}
	if normalized == nil {
		return []HandoffQAItem{}
	}
	return normalized
}

func normalizeHandoffQOptions(options []HandoffQAOption) []HandoffQAOption {
	normalized := make([]HandoffQAOption, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for _, option := range options {
		id := truncateHandoffText(option.ID, 64)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, HandoffQAOption{
			ID:    id,
			Label: truncateHandoffText(option.Label, HandoffOptionLabelMax),
		})
	}
	if normalized == nil {
		return []HandoffQAOption{}
	}
	return normalized
}

func normalizeHandoffFacts(items []HandoffFact) []HandoffFact {
	normalized := make([]HandoffFact, 0, len(items))
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		item.Kind = strings.ToLower(truncateHandoffText(item.Kind, HandoffStatusMax))
		item.Key = truncateHandoffText(item.Key, HandoffKeyMax)
		item.Summary = truncateHandoffText(item.Summary, HandoffFactSummaryMax)
		item.Source = truncateHandoffText(item.Source, HandoffSourceMax)
		item.SourceRef = truncateHandoffText(item.SourceRef, HandoffResourceURLMax)
		item.Confidence = normalizeHandoffConfidence(item.Confidence)
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return normalized
}

func splitHandoffFacts(facts []HandoffFact) ([]HandoffDecision, []HandoffOpenItem) {
	decisions := make([]HandoffDecision, 0, len(facts))
	open := make([]HandoffOpenItem, 0, len(facts))
	for _, fact := range facts {
		switch fact.Kind {
		case version.FactKindDecision, version.FactKindConstraint:
			decisions = append(decisions, HandoffDecision{
				FactID:     fact.ID,
				Kind:       fact.Kind,
				Key:        fact.Key,
				Summary:    fact.Summary,
				Confidence: fact.Confidence,
				Source:     fact.Source,
				SourceRef:  fact.SourceRef,
				CreatedAt:  fact.CreatedAt,
			})
		case version.FactKindGap, version.FactKindAssumption, version.FactKindRisk:
			createdAt := fact.CreatedAt
			open = append(open, HandoffOpenItem{
				Kind:        fact.Kind,
				Summary:     fact.Summary,
				Status:      "open",
				Source:      HandoffSourceFact + formatHandoffID(fact.ID),
				ReferenceID: fact.ID,
				CreatedAt:   &createdAt,
			})
		}
	}
	// facts arrive in id order; keep the newest rows when a block overflows.
	if len(decisions) > HandoffMaxDecisions {
		decisions = decisions[len(decisions)-HandoffMaxDecisions:]
	}
	if len(open) > HandoffMaxOpen {
		open = open[len(open)-HandoffMaxOpen:]
	}
	return decisions, open
}

func openHandoffQAItems(items []HandoffQAItem) []HandoffOpenItem {
	open := make([]HandoffOpenItem, 0)
	for _, item := range items {
		// The question lifecycle statuses are the execution domain's own constants
		// (the human_request state machine), not a handoff-local vocabulary: a
		// rename there must move this predicate with it.
		if item.Status != string(execution.HumanPending) && item.Status != string(execution.HumanExpired) {
			continue
		}
		summary := item.Question
		if summary == "" {
			summary = "待回答问题"
		}
		// A human_request batch carries 1-5 questions (B §6.1). The question
		// key is part of the identity so every question of the batch becomes
		// its own open item instead of collapsing on the request id.
		questionKey := strings.TrimSpace(item.QuestionKey)
		source := HandoffSourceQA + formatHandoffID(item.RequestID)
		if questionKey != "" {
			source += ":" + questionKey
		}
		createdAt := item.AskedAt
		open = append(open, HandoffOpenItem{
			Kind:        "question",
			Summary:     summary,
			Status:      item.Status,
			Source:      source,
			ReferenceID: item.RequestID,
			QuestionKey: questionKey,
			CreatedAt:   &createdAt,
		})
		if len(open) == HandoffMaxOpen {
			break
		}
	}
	return open
}

func openHandoffDraftRequirements(items []HandoffRequirement) []HandoffOpenItem {
	open := make([]HandoffOpenItem, 0)
	for _, item := range items {
		if item.Status != string(version.RequirementDraft) {
			continue
		}
		summary := "待确认需求：" + item.Title
		open = append(open, HandoffOpenItem{
			Kind:        "requirement",
			Summary:     truncateHandoffText(summary, HandoffFactSummaryMax),
			Status:      string(version.RequirementDraft),
			Source:      HandoffSourceRequirement + formatHandoffID(item.ID),
			ReferenceID: item.ID,
		})
		if len(open) == HandoffMaxOpen {
			break
		}
	}
	return open
}

func normalizeHandoffOpen(items []HandoffOpenItem) []HandoffOpenItem {
	normalized := make([]HandoffOpenItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item.Kind = truncateHandoffText(item.Kind, HandoffStatusMax)
		item.Summary = truncateHandoffText(item.Summary, HandoffFactSummaryMax)
		item.Status = truncateHandoffText(item.Status, HandoffStatusMax)
		item.Source = truncateHandoffText(item.Source, HandoffSourceMax)
		item.QuestionKey = truncateHandoffText(item.QuestionKey, 64)
		if item.Kind == "" || item.Summary == "" {
			continue
		}
		key := item.Kind + "\x00" + item.Source + "\x00" + item.QuestionKey
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, item)
		if len(normalized) == HandoffMaxOpen {
			break
		}
	}
	if normalized == nil {
		return []HandoffOpenItem{}
	}
	return normalized
}

func normalizeHandoffPlan(plan HandoffPlan) HandoffPlan {
	plan.Stage = truncateHandoffText(plan.Stage, HandoffStatusMax)
	plan.Current = normalizeHandoffPlanItem(plan.Current)
	plan.Next = normalizeHandoffPlanItem(plan.Next)
	return plan
}

func normalizeHandoffPlanItem(item *HandoffPlanItem) *HandoffPlanItem {
	if item == nil || item.ID <= 0 {
		return nil
	}
	normalized := *item
	normalized.Phase = truncateHandoffText(normalized.Phase, HandoffStatusMax)
	normalized.Title = truncateHandoffText(normalized.Title, HandoffTitleMax)
	normalized.Status = truncateHandoffText(normalized.Status, HandoffStatusMax)
	normalized.Decision = truncateHandoffText(normalized.Decision, HandoffQuestionMax)
	return &normalized
}

func normalizeHandoffResources(resources HandoffResources) HandoffResources {
	normalized := HandoffResources{
		Environment:  normalizeHandoffEnvironment(resources.Environment),
		SUTs:         make([]HandoffSUT, 0, len(resources.SUTs)),
		Accounts:     make([]HandoffAccount, 0, len(resources.Accounts)),
		Repositories: make([]HandoffRepository, 0, len(resources.Repositories)),
	}
	for _, sut := range resources.SUTs {
		if sut.SUTID <= 0 {
			continue
		}
		sut.Name = truncateHandoffText(sut.Name, HandoffResourceNameMax)
		sut.BaseURL = truncateHandoffText(sut.BaseURL, HandoffResourceURLMax)
		sut.Pages = normalizeHandoffPages(sut.Pages)
		sut.APIs = normalizeHandoffAPIs(sut.APIs)
		sut.Databases = normalizeHandoffDatabases(sut.Databases)
		normalized.SUTs = append(normalized.SUTs, sut)
	}
	for _, item := range resources.Accounts {
		if item.ID <= 0 {
			continue
		}
		item.Username = truncateHandoffText(item.Username, HandoffResourceNameMax)
		item.Role = truncateHandoffText(item.Role, HandoffSourceMax)
		item.Roles = normalizeHandoffRefs(item.Roles, 20, 100)
		normalized.Accounts = append(normalized.Accounts, item)
	}
	for _, item := range resources.Repositories {
		if item.ID <= 0 {
			continue
		}
		item.Name = truncateHandoffText(item.Name, HandoffResourceNameMax)
		item.Role = truncateHandoffText(item.Role, HandoffSourceMax)
		item.Branch = truncateHandoffText(item.Branch, HandoffEnvironmentMax)
		item.Source = truncateHandoffText(item.Source, HandoffStatusMax)
		normalized.Repositories = append(normalized.Repositories, item)
	}
	sort.SliceStable(normalized.SUTs, func(i, j int) bool { return normalized.SUTs[i].SUTID < normalized.SUTs[j].SUTID })
	sort.SliceStable(normalized.Accounts, func(i, j int) bool { return normalized.Accounts[i].ID < normalized.Accounts[j].ID })
	sort.SliceStable(normalized.Repositories, func(i, j int) bool { return normalized.Repositories[i].ID < normalized.Repositories[j].ID })

	truncated := false
	if len(normalized.SUTs) > HandoffMaxResources {
		normalized.SUTs = normalized.SUTs[:HandoffMaxResources]
		truncated = true
	}
	// ONE aggregate asset budget shared across every SUT and kind, consumed in
	// catalog order (SUT id, then pages -> APIs -> databases, each already sorted
	// by id). This bounds the whole resources block — not each SUT independently —
	// so the JSONB document fed to the Design Host prompt cannot grow with the
	// catalog. Once the budget is spent the remaining assets are dropped and
	// Truncated records the partial set (constitution 七.1: no silent partial truth).
	budget := HandoffMaxResources
	for index := range normalized.SUTs {
		sut := &normalized.SUTs[index]
		keep, remaining := takeHandoffBudget(len(sut.Pages), budget)
		if keep < len(sut.Pages) {
			truncated = true
		}
		sut.Pages, budget = sut.Pages[:keep], remaining
		keep, remaining = takeHandoffBudget(len(sut.APIs), budget)
		if keep < len(sut.APIs) {
			truncated = true
		}
		sut.APIs, budget = sut.APIs[:keep], remaining
		keep, remaining = takeHandoffBudget(len(sut.Databases), budget)
		if keep < len(sut.Databases) {
			truncated = true
		}
		sut.Databases, budget = sut.Databases[:keep], remaining
	}
	if len(normalized.Accounts) > HandoffMaxResources {
		normalized.Accounts = normalized.Accounts[:HandoffMaxResources]
		truncated = true
	}
	if len(normalized.Repositories) > HandoffMaxResources {
		normalized.Repositories = normalized.Repositories[:HandoffMaxResources]
		truncated = true
	}
	normalized.Truncated = truncated
	return normalized
}

// takeHandoffBudget reports how many of n items fit in the remaining shared
// budget and the budget left after taking them.
func takeHandoffBudget(n, budget int) (keep, remaining int) {
	if n <= budget {
		return n, budget - n
	}
	return budget, 0
}

func normalizeHandoffEnvironment(env HandoffEnvironment) HandoffEnvironment {
	env.Name = truncateHandoffText(env.Name, HandoffEnvironmentMax)
	env.Status = truncateHandoffText(env.Status, HandoffStatusMax)
	return env
}

func normalizeHandoffPages(items []HandoffPage) []HandoffPage {
	normalized := make([]HandoffPage, 0, len(items))
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		item.Name = truncateHandoffText(item.Name, HandoffResourceNameMax)
		item.URL = truncateHandoffText(item.URL, HandoffResourceURLMax)
		item.RouteTemplate = truncateHandoffText(item.RouteTemplate, HandoffResourceURLMax)
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return normalized
}

func normalizeHandoffAPIs(items []HandoffAPI) []HandoffAPI {
	normalized := make([]HandoffAPI, 0, len(items))
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		item.Name = truncateHandoffText(item.Name, HandoffResourceNameMax)
		item.Method = truncateHandoffText(item.Method, 16)
		item.Path = truncateHandoffText(item.Path, HandoffResourceURLMax)
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return normalized
}

func normalizeHandoffDatabases(items []HandoffDatabase) []HandoffDatabase {
	normalized := make([]HandoffDatabase, 0, len(items))
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		item.Name = truncateHandoffText(item.Name, HandoffResourceNameMax)
		item.Kind = truncateHandoffText(item.Kind, HandoffStatusMax)
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return normalized
}

// ---------- helpers ----------

func truncateHandoffText(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if max <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= max {
		return trimmed
	}
	return string(runes[:max])
}

func normalizeHandoffRefs(values []string, maxItems, maxLength int) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := truncateHandoffText(value, maxLength)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
		if len(normalized) == maxItems {
			break
		}
	}
	if normalized == nil {
		return []string{}
	}
	return normalized
}

func uniqueHandoffIDs(values []int64) []int64 {
	unique := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	if unique == nil {
		return []int64{}
	}
	return unique
}

func normalizeHandoffConfidence(confidence *float64) *float64 {
	if confidence == nil {
		return nil
	}
	value := *confidence
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return nil
	}
	return &value
}

func formatHandoffID(value int64) string {
	return platformid.Format(value)
}

// handoffSignaturePayload is the revision-identity projection of the document:
// the six blocks, excluding the row revision and generated_at. Identical
// clarification state therefore produces the same signature and no new
// revision.
type handoffSignaturePayload struct {
	Requirements []HandoffRequirement `json:"requirements"`
	QA           []HandoffQAItem      `json:"qa"`
	Decisions    []HandoffDecision    `json:"decisions"`
	Open         []HandoffOpenItem    `json:"open"`
	Plan         HandoffPlan          `json:"plan"`
	Resources    HandoffResources     `json:"resources"`
}

func handoffSignature(document Handoff) string {
	raw, err := json.Marshal(handoffSignaturePayload{
		Requirements: document.Requirements,
		QA:           document.QA,
		Decisions:    document.Decisions,
		Open:         document.Open,
		Plan:         document.Plan,
		Resources:    document.Resources,
	})
	if err != nil {
		// The document types are all JSON-marshalable after normalization; a
		// marshal failure can only come from corrupted data, so never treat two
		// documents as identical in that case.
		return "unmarshalable:" + err.Error()
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

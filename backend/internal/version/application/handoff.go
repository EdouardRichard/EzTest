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
// The read side is an injected port: HandoffReader is implemented over the
// version context's own stores (requirements/requirements_facts/plan) plus the
// clarification conversation/question rows. The application layer never
// touches SQL and never returns raw chat, credentials or secrets: every
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
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
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
	HandoffDescriptionMax    = 4000
	HandoffAcceptanceMax     = 4000
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

// HandoffRequirement is one confirmed/draft requirement of the handoff.
type HandoffRequirement struct {
	ID                 int64      `json:"id"`
	CurrentVersionID   int64      `json:"current_version_id"`
	Revision           int        `json:"revision"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	AcceptanceCriteria string     `json:"acceptance_criteria"`
	Priority           string     `json:"priority"`
	Status             string     `json:"status"`
	SourceQuote        string     `json:"source_quote"`
	Confidence         *float64   `json:"confidence"`
	ConfirmedAt        *time.Time `json:"confirmed_at"`
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

// HandoffSUT is one version_sut binding summary.
type HandoffSUT struct {
	VersionSUTID  int64  `json:"version_sut_id"`
	SUTID         int64  `json:"sut_id"`
	Name          string `json:"name"`
	EnvironmentID int64  `json:"environment_id"`
	Environment   string `json:"environment"`
	BaseURL       string `json:"base_url"`
}

// HandoffPage is one approved page of a bound environment.
type HandoffPage struct {
	ID            int64  `json:"id"`
	VersionSUTID  int64  `json:"version_sut_id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	RouteTemplate string `json:"route_template"`
}

// HandoffAPI is one approved API of a bound environment.
type HandoffAPI struct {
	ID           int64  `json:"id"`
	VersionSUTID int64  `json:"version_sut_id"`
	Name         string `json:"name"`
	Method       string `json:"method"`
	Path         string `json:"path"`
}

// HandoffAccount is one enabled version account. It never carries a password.
type HandoffAccount struct {
	ID       int64    `json:"id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	Role     string   `json:"role"`
}

// HandoffDatabase is one target database of a bound environment. It never
// carries a DSN or credentials.
type HandoffDatabase struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	ReadOnly bool   `json:"read_only"`
}

// HandoffRepository is one code repository inherited or added for the version.
type HandoffRepository struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Branch string `json:"branch"`
	Source string `json:"source"`
}

// HandoffResources is the resource summary block (C resources, spec R-B5-1).
type HandoffResources struct {
	SUTs         []HandoffSUT        `json:"suts"`
	Pages        []HandoffPage       `json:"pages"`
	APIs         []HandoffAPI        `json:"apis"`
	Accounts     []HandoffAccount    `json:"accounts"`
	Databases    []HandoffDatabase   `json:"databases"`
	Repositories []HandoffRepository `json:"repositories"`
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
// clarification question rows; it must never return raw chat transcripts,
// passwords, DSNs or other secrets.
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
	// Resources returns the bound SUT/page/API/account/database/repository
	// summaries of the version.
	Resources(ctx context.Context, versionID int64) (HandoffResources, error)
}

// HandoffService builds and reads the version-level clarification handoff.
type HandoffService struct {
	store  HandoffStore
	reader HandoffReader
	ids    platformid.Generator
	events eventingapp.Publisher
}

// NewHandoffService wires the handoff store and read port. ids assigns
// clarification_handoff primary keys; events may be nil in degraded assemblies
// (event delivery is best effort and never rolls back the stored revision).
func NewHandoffService(store HandoffStore, reader HandoffReader, ids platformid.Generator, events eventingapp.Publisher) *HandoffService {
	return &HandoffService{store: store, reader: reader, ids: ids, events: events}
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
	resources, err := s.reader.Resources(ctx, versionID)
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
		if !handoffRequirementStatusIncluded(item.Status) {
			continue
		}
		item.Title = truncateHandoffText(item.Title, HandoffTitleMax)
		item.Description = truncateHandoffText(item.Description, HandoffDescriptionMax)
		item.AcceptanceCriteria = truncateHandoffText(item.AcceptanceCriteria, HandoffAcceptanceMax)
		item.Priority = truncateHandoffText(item.Priority, HandoffStatusMax)
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

// handoffRequirementStatusIncluded reports whether a requirement status belongs
// in the requirements block (confirmed/draft only, B §8.1).
func handoffRequirementStatusIncluded(status string) bool {
	switch status {
	case "draft", "clarified":
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
		case "decision", "constraint":
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
		case "gap", "assumption", "risk":
			createdAt := fact.CreatedAt
			open = append(open, HandoffOpenItem{
				Kind:        fact.Kind,
				Summary:     fact.Summary,
				Status:      "open",
				Source:      "version_fact:" + formatHandoffID(fact.ID),
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
		if item.Status != "pending" && item.Status != "expired" {
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
		source := "human_request:" + formatHandoffID(item.RequestID)
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
		if item.Status != "draft" {
			continue
		}
		summary := "待确认需求：" + item.Title
		open = append(open, HandoffOpenItem{
			Kind:        "requirement",
			Summary:     truncateHandoffText(summary, HandoffFactSummaryMax),
			Status:      "draft",
			Source:      "requirement:" + formatHandoffID(item.ID),
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
		SUTs:         make([]HandoffSUT, 0, len(resources.SUTs)),
		Pages:        make([]HandoffPage, 0, len(resources.Pages)),
		APIs:         make([]HandoffAPI, 0, len(resources.APIs)),
		Accounts:     make([]HandoffAccount, 0, len(resources.Accounts)),
		Databases:    make([]HandoffDatabase, 0, len(resources.Databases)),
		Repositories: make([]HandoffRepository, 0, len(resources.Repositories)),
	}
	for _, item := range resources.SUTs {
		if item.VersionSUTID <= 0 {
			continue
		}
		item.Name = truncateHandoffText(item.Name, HandoffResourceNameMax)
		item.Environment = truncateHandoffText(item.Environment, HandoffEnvironmentMax)
		item.BaseURL = truncateHandoffText(item.BaseURL, HandoffResourceURLMax)
		normalized.SUTs = append(normalized.SUTs, item)
	}
	for _, item := range resources.Pages {
		if item.ID <= 0 {
			continue
		}
		item.Name = truncateHandoffText(item.Name, HandoffResourceNameMax)
		item.URL = truncateHandoffText(item.URL, HandoffResourceURLMax)
		item.RouteTemplate = truncateHandoffText(item.RouteTemplate, HandoffResourceURLMax)
		normalized.Pages = append(normalized.Pages, item)
	}
	for _, item := range resources.APIs {
		if item.ID <= 0 {
			continue
		}
		item.Name = truncateHandoffText(item.Name, HandoffResourceNameMax)
		item.Method = truncateHandoffText(item.Method, 16)
		item.Path = truncateHandoffText(item.Path, HandoffResourceURLMax)
		normalized.APIs = append(normalized.APIs, item)
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
	for _, item := range resources.Databases {
		if item.ID <= 0 {
			continue
		}
		item.Name = truncateHandoffText(item.Name, HandoffResourceNameMax)
		item.Kind = truncateHandoffText(item.Kind, HandoffStatusMax)
		normalized.Databases = append(normalized.Databases, item)
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
	sort.SliceStable(normalized.SUTs, func(i, j int) bool { return normalized.SUTs[i].VersionSUTID < normalized.SUTs[j].VersionSUTID })
	sort.SliceStable(normalized.Pages, func(i, j int) bool { return normalized.Pages[i].ID < normalized.Pages[j].ID })
	sort.SliceStable(normalized.APIs, func(i, j int) bool { return normalized.APIs[i].ID < normalized.APIs[j].ID })
	sort.SliceStable(normalized.Accounts, func(i, j int) bool { return normalized.Accounts[i].ID < normalized.Accounts[j].ID })
	sort.SliceStable(normalized.Databases, func(i, j int) bool { return normalized.Databases[i].ID < normalized.Databases[j].ID })
	sort.SliceStable(normalized.Repositories, func(i, j int) bool { return normalized.Repositories[i].ID < normalized.Repositories[j].ID })
	normalized.SUTs = capHandoffSUTs(normalized.SUTs)
	normalized.Pages = capHandoffPages(normalized.Pages)
	normalized.APIs = capHandoffAPIs(normalized.APIs)
	normalized.Accounts = capHandoffAccounts(normalized.Accounts)
	normalized.Databases = capHandoffDatabases(normalized.Databases)
	normalized.Repositories = capHandoffRepositories(normalized.Repositories)
	return normalized
}

func capHandoffSUTs(items []HandoffSUT) []HandoffSUT {
	if len(items) > HandoffMaxResources {
		return items[:HandoffMaxResources]
	}
	return items
}

func capHandoffPages(items []HandoffPage) []HandoffPage {
	if len(items) > HandoffMaxResources {
		return items[:HandoffMaxResources]
	}
	return items
}

func capHandoffAPIs(items []HandoffAPI) []HandoffAPI {
	if len(items) > HandoffMaxResources {
		return items[:HandoffMaxResources]
	}
	return items
}

func capHandoffAccounts(items []HandoffAccount) []HandoffAccount {
	if len(items) > HandoffMaxResources {
		return items[:HandoffMaxResources]
	}
	return items
}

func capHandoffDatabases(items []HandoffDatabase) []HandoffDatabase {
	if len(items) > HandoffMaxResources {
		return items[:HandoffMaxResources]
	}
	return items
}

func capHandoffRepositories(items []HandoffRepository) []HandoffRepository {
	if len(items) > HandoffMaxResources {
		return items[:HandoffMaxResources]
	}
	return items
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

package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Knowledge domain aliases keep the service signatures of the plan's Shared
// Interfaces unqualified while the single source of truth stays in
// version/domain.
type (
	// KnowledgeEntry is one version-scoped knowledge note.
	KnowledgeEntry = domain.KnowledgeEntry
	// KnowledgeSourceRef points at the requirement/case/issue/evidence a note
	// was derived from.
	KnowledgeSourceRef = domain.KnowledgeSourceRef
	// KnowledgeFilter selects a page of one version's entries.
	KnowledgeFilter = domain.KnowledgeFilter
	// GlobalKnowledgeFilter selects a page of the cross-version read.
	GlobalKnowledgeFilter = domain.GlobalKnowledgeFilter
	// KnowledgeRepository is the persistence port for knowledge entries.
	KnowledgeRepository = domain.KnowledgeRepository
)

// KnowledgeLimits is the prompt-injection budget of spec §9.4/§11.4:
// EZTEST_KNOWLEDGE_MAX_INJECT entries and EZTEST_KNOWLEDGE_MAX_BYTES rendered
// bytes. Zero disables the corresponding budget; the defaults are 5 and 4096.
type KnowledgeLimits struct {
	MaxInject int
	MaxBytes  int
}

// DefaultKnowledgeLimits returns the spec §11.4 defaults.
func DefaultKnowledgeLimits() KnowledgeLimits {
	return KnowledgeLimits{
		MaxInject: domain.DefaultKnowledgeMaxInject,
		MaxBytes:  domain.DefaultKnowledgeMaxBytes,
	}
}

// KnowledgeLimitsFromEnv parses EZTEST_KNOWLEDGE_MAX_INJECT and
// EZTEST_KNOWLEDGE_MAX_BYTES through getenv (os.Getenv in the composition
// root). A missing or malformed value falls back to the default; an
// out-of-range value is clamped to the spec §11.4 range.
func KnowledgeLimitsFromEnv(getenv func(string) string) KnowledgeLimits {
	limits := DefaultKnowledgeLimits()
	if getenv == nil {
		return limits
	}
	if value, ok := knowledgeEnvInt(getenv("EZTEST_KNOWLEDGE_MAX_INJECT")); ok {
		limits.MaxInject = clampInt(value, 0, domain.MaxKnowledgeMaxInject)
	}
	if value, ok := knowledgeEnvInt(getenv("EZTEST_KNOWLEDGE_MAX_BYTES")); ok {
		limits.MaxBytes = clampInt(value, 0, domain.MaxKnowledgeMaxBytes)
	}
	return limits
}

// knowledgeEnvInt parses one non-negative environment integer. A negative or
// malformed value reports ok=false so the default applies instead of silently
// disabling injection.
func knowledgeEnvInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// clampInt bounds value to [low, high].
func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// KnowledgeSection is the rendered "版本知识" prompt segment of one version. Refs
// lists the ids of the entries that actually made it into Text (audit trail);
// Truncated reports that the entry count or byte budget cut the list.
type KnowledgeSection struct {
	Text      string
	Refs      []int64
	Truncated bool
}

// KnowledgeInjector is the narrow read the clarification runs consume. It is
// implemented by *KnowledgeService; tests inject a fake.
type KnowledgeInjector interface {
	ApprovedKnowledgeSection(ctx context.Context, versionID int64) (KnowledgeSection, error)
}

// CreateKnowledgeInput is the POST body of a knowledge draft. Confidence is
// optional and defaults to medium.
type CreateKnowledgeInput struct {
	Kind       string
	Title      string
	Body       string
	Tags       []string
	SourceRefs []KnowledgeSourceRef
	Confidence string
}

// UpdateKnowledgeInput is the PATCH body of a draft edit. Revision is the
// expected revision (mandatory); the other fields are partial updates.
type UpdateKnowledgeInput struct {
	Revision   int
	Kind       *string
	Title      *string
	Body       *string
	Tags       *[]string
	SourceRefs *[]KnowledgeSourceRef
	Confidence *string
}

// ReviewKnowledgeInput is the admin review body: approve or reject one draft at
// the expected revision, with a mandatory reason when rejecting.
type ReviewKnowledgeInput struct {
	Revision int
	Approve  bool
	Reason   string
}

// KnowledgeService implements the version knowledge use cases of spec §9.4:
// draft creation, the creator/admin draft edit, the admin review transition, the
// version/global reads and the approved-only prompt injection read. Writes
// publish knowledge.created/knowledge.reviewed after the repository call
// returns; the event detail never carries the title or body (spec §11.2).
type KnowledgeService struct {
	repo     KnowledgeRepository
	versions VersionRepository
	events   EventPublisher
	ids      IDGenerator
	limits   KnowledgeLimits
}

// KnowledgeServiceOption customises the knowledge service dependencies.
type KnowledgeServiceOption func(*KnowledgeService)

// WithKnowledgeIDGenerator injects the process-wide Snowflake generator.
func WithKnowledgeIDGenerator(ids IDGenerator) KnowledgeServiceOption {
	return func(s *KnowledgeService) {
		if ids != nil {
			s.ids = ids
		}
	}
}

// WithKnowledgeLimits overrides the injection budget (env-driven in the
// composition root).
func WithKnowledgeLimits(limits KnowledgeLimits) KnowledgeServiceOption {
	return func(s *KnowledgeService) {
		s.limits = normalizeKnowledgeLimits(limits)
	}
}

// NewKnowledgeService wires the knowledge use cases to their repository, the
// version repository (existence gate), the event publisher and the injection
// budget.
func NewKnowledgeService(repo KnowledgeRepository, versions VersionRepository, events EventPublisher, opts ...KnowledgeServiceOption) *KnowledgeService {
	service := &KnowledgeService{
		repo:     repo,
		versions: versions,
		events:   events,
		ids:      DefaultIDGenerator(),
		limits:   DefaultKnowledgeLimits(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Create records one draft. Any authenticated user may create one inside an
// existing version (spec §9.4); the knowledge.created event carries the entry id,
// kind and status only.
func (s *KnowledgeService) Create(ctx context.Context, versionID int64, input CreateKnowledgeInput, actor int64) (*KnowledgeEntry, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	id := s.ids.Next()
	if id <= 0 {
		return nil, errors.New("version: id generator returned a non-positive id")
	}
	confidence := input.Confidence
	if strings.TrimSpace(confidence) == "" {
		confidence = domain.KnowledgeConfidenceMedium
	}
	entry := &KnowledgeEntry{
		ID:         id,
		VersionID:  versionID,
		Kind:       input.Kind,
		Title:      input.Title,
		Body:       input.Body,
		Tags:       knowledgeCloneTags(input.Tags),
		SourceRefs: knowledgeCloneSourceRefs(input.SourceRefs),
		Status:     domain.KnowledgeDraft,
		Confidence: confidence,
		CreatedBy:  actor,
		Revision:   1,
	}
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, entry); err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: versionID,
		Type:      "knowledge.created",
		Summary:   "knowledge draft created",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail: map[string]any{
			"knowledgeId": eventID(entry.ID),
			"kind":        entry.Kind,
			"status":      entry.Status,
		},
	})
	return entry, nil
}

// Update rewrites a draft under the revision CAS. Only the creator (or an
// administrator) may edit, and only a draft is editable (spec §9.4).
func (s *KnowledgeService) Update(ctx context.Context, versionID, knowledgeID int64, input UpdateKnowledgeInput, actor int64, admin bool) (*KnowledgeEntry, error) {
	if input.Revision <= 0 {
		return nil, domain.NewInvalid("revision", "invalid_revision", "revision is required")
	}
	current, err := s.load(ctx, versionID, knowledgeID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.KnowledgeDraft {
		return nil, knowledgeStateConflict(knowledgeID, current.Status)
	}
	if !admin && current.CreatedBy != actor {
		return nil, domain.NewForbidden("knowledge_forbidden",
			"only the creator or an administrator may edit this knowledge entry")
	}
	candidate := *current
	if input.Kind != nil {
		candidate.Kind = *input.Kind
	}
	if input.Title != nil {
		candidate.Title = *input.Title
	}
	if input.Body != nil {
		candidate.Body = *input.Body
	}
	if input.Tags != nil {
		candidate.Tags = knowledgeCloneTags(*input.Tags)
	}
	if input.SourceRefs != nil {
		candidate.SourceRefs = knowledgeCloneSourceRefs(*input.SourceRefs)
	}
	if input.Confidence != nil {
		candidate.Confidence = *input.Confidence
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateContent(ctx, &candidate, input.Revision); err != nil {
		return nil, err
	}
	return &candidate, nil
}

// Review approves or rejects one draft under the revision CAS. The route is
// administrator-guarded; rejecting requires a non-empty reason (spec §9.4).
func (s *KnowledgeService) Review(ctx context.Context, versionID, knowledgeID int64, input ReviewKnowledgeInput, actor int64) (*KnowledgeEntry, error) {
	if input.Revision <= 0 {
		return nil, domain.NewInvalid("revision", "invalid_revision", "revision is required")
	}
	reason := strings.TrimSpace(input.Reason)
	if !input.Approve && reason == "" {
		return nil, domain.NewInvalid("reason", "invalid_review_note",
			"reason is required to reject a knowledge entry")
	}
	current, err := s.load(ctx, versionID, knowledgeID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.KnowledgeDraft {
		return nil, knowledgeStateConflict(knowledgeID, current.Status)
	}
	candidate := *current
	candidate.ReviewedBy = clarificationIDPtr(actor)
	candidate.ReviewNote = input.Reason
	if input.Approve {
		candidate.Status = domain.KnowledgeApproved
	} else {
		candidate.Status = domain.KnowledgeRejected
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.Review(ctx, &candidate, input.Revision); err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: versionID,
		Type:      "knowledge.reviewed",
		Summary:   "knowledge entry reviewed",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail: map[string]any{
			"knowledgeId": eventID(candidate.ID),
			"status":      candidate.Status,
			"reviewedBy":  actor,
		},
	})
	return &candidate, nil
}

// List returns one page of a version's entries ordered by id ascending. A
// non-admin viewer only sees approved entries plus the drafts/rejections they
// created; administrators see every status (spec §9.4).
func (s *KnowledgeService) List(ctx context.Context, versionID int64, status, kind string, page domain.Page, viewer domain.KnowledgeViewer) ([]KnowledgeEntry, bool, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, false, err
	}
	if err := validateKnowledgeFilters(status, kind); err != nil {
		return nil, false, err
	}
	filter := KnowledgeFilter{
		VersionID: versionID,
		Status:    status,
		Kind:      kind,
		Page:      page,
	}
	if !viewer.Admin {
		viewerID := viewer.ID
		filter.ViewerID = &viewerID
	}
	return s.repo.List(ctx, filter)
}

// ListGlobal returns the cross-version read. An empty status means approved
// only; the HTTP layer lets administrators ask for the draft/rejected queue.
func (s *KnowledgeService) ListGlobal(ctx context.Context, versionID *int64, status, kind string, page domain.Page) ([]KnowledgeEntry, bool, error) {
	if status == "" {
		status = domain.KnowledgeApproved
	}
	if err := validateKnowledgeFilters(status, kind); err != nil {
		return nil, false, err
	}
	return s.repo.ListGlobal(ctx, GlobalKnowledgeFilter{
		VersionID: versionID,
		Status:    status,
		Kind:      kind,
		Page:      page,
	})
}

// ApprovedKnowledgeSection renders the approved-only "版本知识" prompt segment of
// one version. Only approved entries are read; at most limits.MaxInject entries
// and limits.MaxBytes rendered bytes are injected. When the budget cuts the
// list the returned section is marked truncated so the caller can record the
// knowledge_truncated warning (spec §9.4).
func (s *KnowledgeService) ApprovedKnowledgeSection(ctx context.Context, versionID int64) (KnowledgeSection, error) {
	limits := normalizeKnowledgeLimits(s.limits)
	queryLimit := limits.MaxInject
	if queryLimit <= 0 {
		// Still detect that entries existed so the caller records truncation.
		queryLimit = 1
	}
	if s.repo == nil {
		return KnowledgeSection{}, errors.New("version: knowledge repository is not configured")
	}
	// Read one extra row so a count-truncated list is distinguishable.
	entries, err := s.repo.ListApproved(ctx, versionID, queryLimit+1)
	if err != nil {
		return KnowledgeSection{}, err
	}
	return renderKnowledgeSection(entries, limits), nil
}

// ensureVersion loads the version and reports a missing one as a 404.
func (s *KnowledgeService) ensureVersion(ctx context.Context, versionID int64) (*Version, error) {
	if s.versions == nil {
		return nil, errors.New("version: version repository is not configured")
	}
	return s.versions.Get(ctx, versionID)
}

// load returns one entry scoped to versionID.
func (s *KnowledgeService) load(ctx context.Context, versionID, knowledgeID int64) (*KnowledgeEntry, error) {
	entry, err := s.repo.Get(ctx, knowledgeID)
	if err != nil {
		return nil, err
	}
	if entry.VersionID != versionID {
		return nil, domain.NewNotFound("knowledge", knowledgeID)
	}
	return entry, nil
}

// publish emits one event after a committed write. Publication failures are
// logged and swallowed (spec §6.10).
func (s *KnowledgeService) publish(ctx context.Context, event VersionEvent) {
	if s.events == nil {
		return
	}
	if err := s.events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "knowledge event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"error", err,
		)
	}
}

// knowledgeSectionHeader is the fixed heading of the injected segment.
const knowledgeSectionHeader = "## 版本知识（已批准）\n"

// knowledgeMaxInjectedTitleChars bounds the quoted title inside one injected
// entry so a hostile title cannot consume the whole byte budget.
const knowledgeMaxInjectedTitleChars = 120

// knowledgeRefsLine renders the injected entry ids, or "" for no entries.
func knowledgeRefsLine(refs []int64) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs))
	for _, id := range refs {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return "knowledgeRefs: " + strings.Join(parts, ",") + "\n"
}

// knowledgeEntryPrefix renders the heading line and body indentation of one
// entry. The title is bounded so a hostile title cannot eat the whole budget.
func knowledgeEntryPrefix(entry KnowledgeEntry) string {
	return fmt.Sprintf("- kind=%s confidence=%s title=%q\n  ",
		entry.Kind, entry.Confidence, truncateKnowledgeRunes(entry.Title, knowledgeMaxInjectedTitleChars))
}

// knowledgeEntryBlock renders one complete entry.
func knowledgeEntryBlock(entry KnowledgeEntry) string {
	return knowledgeEntryPrefix(entry) + entry.Body + "\n"
}

// renderKnowledgeSection applies the count and byte budgets. It always returns
// Text of at most limits.MaxBytes bytes; the first entry may have its body cut
// on a rune boundary when the budget cannot hold it whole.
func renderKnowledgeSection(entries []KnowledgeEntry, limits KnowledgeLimits) KnowledgeSection {
	if len(entries) == 0 {
		return KnowledgeSection{}
	}
	if limits.MaxInject <= 0 {
		return KnowledgeSection{Truncated: true}
	}
	if limits.MaxBytes <= 0 {
		return KnowledgeSection{Truncated: true}
	}
	candidates := entries
	truncated := false
	if len(candidates) > limits.MaxInject {
		candidates = candidates[:limits.MaxInject]
		truncated = true
	}
	refs := make([]int64, 0, limits.MaxInject)
	var blocks strings.Builder
	for _, entry := range candidates {
		candidateRefs := append(append([]int64{}, refs...), entry.ID)
		refsLineLen := len(knowledgeRefsLine(candidateRefs))
		block := knowledgeEntryBlock(entry)
		if len(knowledgeSectionHeader)+refsLineLen+blocks.Len()+len(block) <= limits.MaxBytes {
			refs = candidateRefs
			blocks.WriteString(block)
			continue
		}
		// The budget is exhausted. The first entry may still contribute a body
		// prefix; later entries are dropped whole.
		if len(refs) == 0 {
			prefix := knowledgeEntryPrefix(entry)
			available := limits.MaxBytes - len(knowledgeSectionHeader) - refsLineLen -
				blocks.Len() - len(prefix) - 1
			if available > 0 {
				if body := truncateKnowledgeBytes(entry.Body, available); body != "" {
					blocks.WriteString(prefix + body + "\n")
					refs = candidateRefs
				}
			}
		}
		truncated = true
		break
	}
	if len(refs) == 0 {
		return KnowledgeSection{Truncated: true}
	}
	text := knowledgeSectionHeader + knowledgeRefsLine(refs) + blocks.String()
	if len(text) > limits.MaxBytes {
		// Defensive: the byte accounting above must already guarantee the cap.
		text = truncateKnowledgeBytes(text, limits.MaxBytes)
		truncated = true
	}
	text = strings.TrimSuffix(text, "\n") + "\n"
	return KnowledgeSection{Text: text, Refs: refs, Truncated: truncated}
}

// truncateKnowledgeBytes cuts s to at most maxBytes without splitting a UTF-8
// rune.
func truncateKnowledgeBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := 0
	for index := range s {
		if index > maxBytes {
			break
		}
		cut = index
	}
	if cut == 0 {
		return ""
	}
	return s[:cut]
}

// truncateKnowledgeRunes cuts s to at most maxRunes characters.
func truncateKnowledgeRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// normalizeKnowledgeLimits applies the spec §11.4 ranges to a caller-provided
// budget.
func normalizeKnowledgeLimits(limits KnowledgeLimits) KnowledgeLimits {
	return KnowledgeLimits{
		MaxInject: clampInt(limits.MaxInject, 0, domain.MaxKnowledgeMaxInject),
		MaxBytes:  clampInt(limits.MaxBytes, 0, domain.MaxKnowledgeMaxBytes),
	}
}

// validateKnowledgeFilters rejects unknown status/kind query values with the
// stable invalid_* reasons. An empty value matches everything.
func validateKnowledgeFilters(status, kind string) error {
	if status != "" && !knowledgeStatusKnown(status) {
		return domain.NewInvalid("status", "invalid_knowledge_status",
			"status must be draft, approved or rejected")
	}
	if kind != "" && !knowledgeKindKnown(kind) {
		return domain.NewInvalid("kind", "invalid_knowledge_kind",
			"kind must be pattern, pitfall, decision or case_idea")
	}
	return nil
}

// knowledgeStatusKnown mirrors the frozen status enum.
func knowledgeStatusKnown(status string) bool {
	switch status {
	case domain.KnowledgeDraft, domain.KnowledgeApproved, domain.KnowledgeRejected:
		return true
	default:
		return false
	}
}

// knowledgeKindKnown mirrors the frozen kind enum.
func knowledgeKindKnown(kind string) bool {
	switch kind {
	case domain.KnowledgeKindPattern, domain.KnowledgeKindPitfall,
		domain.KnowledgeKindDecision, domain.KnowledgeKindCaseIdea:
		return true
	default:
		return false
	}
}

// knowledgeStateConflict is the 409 of an edit/review outside the draft state.
func knowledgeStateConflict(id int64, status string) error {
	return domain.NewStateConflict("knowledge_state_conflict",
		fmt.Sprintf("knowledge entry %d is %s, which does not allow this transition", id, status))
}

// knowledgeCloneTags copies a tag list so stored rows never alias caller state.
func knowledgeCloneTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}
	return append([]string{}, tags...)
}

// knowledgeCloneSourceRefs copies a source-ref list.
func knowledgeCloneSourceRefs(refs []KnowledgeSourceRef) []KnowledgeSourceRef {
	if len(refs) == 0 {
		return []KnowledgeSourceRef{}
	}
	return append([]KnowledgeSourceRef{}, refs...)
}

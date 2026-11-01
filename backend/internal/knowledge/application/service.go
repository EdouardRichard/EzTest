package knowledgeapp

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// Service implements the knowledge application surface.
type Service struct {
	repo     Repository
	usage    UsageRepository
	settings SettingsReader
	audit    AuditRecorder
	events   EventPublisher
	sources  SourceVersionReader
	clock    Clock
	ids      IDGenerator
}

// NewService wires the knowledge application service. settings, audit, events
// and sources may be nil before the assembly-root wiring: a nil settings
// reader applies the documented defaults (review required, 5 items, 4000
// tokens), a nil audit/event sink keeps mutations successful, and a nil source
// reader leaves the read-time source_stale marker false.
func NewService(
	repo Repository,
	usage UsageRepository,
	settings SettingsReader,
	audit AuditRecorder,
	events EventPublisher,
	sources SourceVersionReader,
	clk Clock,
	ids IDGenerator,
) *Service {
	return &Service{
		repo:     repo,
		usage:    usage,
		settings: settings,
		audit:    audit,
		events:   events,
		sources:  sources,
		clock:    clk,
		ids:      ids,
	}
}

// List returns one id-desc page of knowledge items with the read-time
// source_stale marker.
func (s *Service) List(ctx context.Context, filter ListFilter) (Page, error) {
	if err := s.requireRepo(); err != nil {
		return Page{}, err
	}
	scope, systemKey, err := normalizeListScope(filter.Scope, filter.SystemKey)
	if err != nil {
		return Page{}, err
	}
	status := filter.Status
	if status != "" && !status.Valid() {
		return Page{}, platform.Invalid("status", "status must be draft, approved, rejected or revoked")
	}
	if filter.Cursor < 0 {
		return Page{}, platform.Invalid("cursor", "must not be negative")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}

	items, err := s.repo.List(ctx, knowledge.Filter{
		Scope:     scope,
		Status:    status,
		SystemKey: systemKey,
		Tag:       strings.TrimSpace(filter.Tag),
		Query:     strings.TrimSpace(filter.Query),
		BeforeID:  filter.Cursor,
		Limit:     limit + 1,
	})
	if err != nil {
		return Page{}, err
	}

	page := Page{Items: items, HasMore: len(items) > limit}
	if page.HasMore {
		page.Items = items[:limit]
		page.NextCursor = items[limit-1].ID
	}
	s.decorateSourceStale(ctx, page.Items)
	return page, nil
}

// Get returns one item with the read-time source_stale marker.
func (s *Service) Get(ctx context.Context, id int64) (knowledge.Item, error) {
	if err := s.requireID(id); err != nil {
		return knowledge.Item{}, err
	}
	if err := s.requireRepo(); err != nil {
		return knowledge.Item{}, err
	}
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return knowledge.Item{}, mapRepoError(err)
	}
	s.decorateItemStale(ctx, &item)
	return item, nil
}

// Create stores a draft (or an admin one-step approved item when
// knowledge.review_required is false), its initial revision, the audit entry
// and the knowledge.proposed/approved event.
func (s *Service) Create(ctx context.Context, actor platform.Actor, input CreateInput) (knowledge.Item, error) {
	if err := s.requireRepo(); err != nil {
		return knowledge.Item{}, err
	}
	title, err := knowledge.NormalizeTitle(input.Title)
	if err != nil {
		return knowledge.Item{}, err
	}
	content, err := knowledge.NormalizeContent(input.Content)
	if err != nil {
		return knowledge.Item{}, err
	}
	tags, err := knowledge.NormalizeTags(input.Tags)
	if err != nil {
		return knowledge.Item{}, err
	}
	scope := input.Scope
	if scope == "" {
		if input.SystemKey != nil && strings.TrimSpace(*input.SystemKey) != "" {
			scope = knowledge.ScopeSystem
		} else {
			scope = knowledge.ScopeGlobal
		}
	}
	scope, systemKey, err := knowledge.NormalizeScope(scope, input.SystemKey)
	if err != nil {
		return knowledge.Item{}, err
	}
	if err := knowledge.Validate(scope, systemKey, title, content, tags); err != nil {
		return knowledge.Item{}, err
	}
	if err := s.requireIDs(); err != nil {
		return knowledge.Item{}, err
	}
	if input.SourceVersionID != nil && *input.SourceVersionID <= 0 {
		return knowledge.Item{}, platform.Invalid("source_version_id", "must be a positive decimal ID")
	}
	if input.PromotedFromVersionID != nil && *input.PromotedFromVersionID <= 0 {
		return knowledge.Item{}, platform.Invalid("promoted_from_version_id", "must be a positive decimal ID")
	}

	now := s.now()
	item := knowledge.Item{
		ID:              s.ids.Next(),
		Scope:           scope,
		SystemKey:       systemKey,
		Title:           title,
		Content:         content,
		Tags:            tags,
		Status:          knowledge.StatusDraft,
		SourceVersionID: input.SourceVersionID,
		// Promoting from a version defaults the promoted marker to the source
		// version (spec §4.3).
		PromotedFromVersionID: input.PromotedFromVersionID,
		Revision:              1,
		CreatedBy:             int64Ptr(actor.ID),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if item.PromotedFromVersionID == nil {
		item.PromotedFromVersionID = input.SourceVersionID
	}
	autoApprove := actor.IsAdmin() && !s.reviewRequired(ctx)
	if autoApprove {
		item.Status = knowledge.StatusApproved
		item.ReviewedBy = int64Ptr(actor.ID)
		item.ReviewedAt = &now
	}

	revision := knowledge.Revision{
		ID:          s.ids.Next(),
		KnowledgeID: item.ID,
		Revision:    1,
		Title:       title,
		Content:     content,
		Tags:        tags,
		ChangedBy:   int64Ptr(actor.ID),
		CreatedAt:   now,
	}
	if err := s.repo.Insert(ctx, item, revision); err != nil {
		return knowledge.Item{}, err
	}

	metadata := map[string]any{
		"knowledge_id": platformid.Format(item.ID),
		"scope":        string(item.Scope),
	}
	if item.SystemKey != nil {
		metadata["system_key"] = *item.SystemKey
	}
	if item.SourceVersionID != nil {
		metadata["source_version_id"] = platformid.Format(*item.SourceVersionID)
	}
	if input.AgentRunID != nil && *input.AgentRunID > 0 {
		metadata["agent_run_id"] = platformid.Format(*input.AgentRunID)
	}
	s.recordAudit(ctx, actor.ID, ActionCreate, item.ID, metadata)

	if autoApprove {
		s.publish(ctx, actor, "knowledge.approved", map[string]any{
			"knowledge_id": platformid.Format(item.ID),
			"scope":        string(item.Scope),
			"reviewer_id":  platformid.Format(actor.ID),
		})
	} else {
		payload := map[string]any{
			"knowledge_id": platformid.Format(item.ID),
			"scope":        string(item.Scope),
		}
		if item.SourceVersionID != nil {
			payload["source_version_id"] = platformid.Format(*item.SourceVersionID)
		}
		s.publish(ctx, actor, "knowledge.proposed", payload)
	}
	return item, nil
}

// Update edits a draft/rejected item (author or admin), appends the next
// revision and turns a rejected item back into a draft.
func (s *Service) Update(ctx context.Context, actor platform.Actor, id int64, input UpdateInput) (knowledge.Item, error) {
	if err := s.requireID(id); err != nil {
		return knowledge.Item{}, err
	}
	if err := s.requireRepo(); err != nil {
		return knowledge.Item{}, err
	}
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return knowledge.Item{}, mapRepoError(err)
	}
	if item.Status != knowledge.StatusDraft && item.Status != knowledge.StatusRejected {
		return knowledge.Item{}, stateConflict("invalid_transition")
	}
	if !actor.IsAdmin() && (item.CreatedBy == nil || *item.CreatedBy != actor.ID) {
		return knowledge.Item{}, platform.Forbidden()
	}
	if err := s.requireIDs(); err != nil {
		return knowledge.Item{}, err
	}

	title := item.Title
	if input.Title != nil {
		if title, err = knowledge.NormalizeTitle(*input.Title); err != nil {
			return knowledge.Item{}, err
		}
	}
	content := item.Content
	if input.Content != nil {
		if content, err = knowledge.NormalizeContent(*input.Content); err != nil {
			return knowledge.Item{}, err
		}
	}
	tags := item.Tags
	if input.Tags != nil {
		if tags, err = knowledge.NormalizeTags(*input.Tags); err != nil {
			return knowledge.Item{}, err
		}
	}
	scope := item.Scope
	if input.Scope != nil {
		scope = *input.Scope
	}
	systemKey := item.SystemKey
	if input.SystemKey != nil {
		systemKey = input.SystemKey
	}
	if scope == knowledge.ScopeGlobal {
		systemKey = nil
	}
	scope, systemKey, err = knowledge.NormalizeScope(scope, systemKey)
	if err != nil {
		return knowledge.Item{}, err
	}
	if err := knowledge.Validate(scope, systemKey, title, content, tags); err != nil {
		return knowledge.Item{}, err
	}

	changed := changedFields(item, title, content, tags, scope, systemKey)
	now := s.now()
	item.Title = title
	item.Content = content
	item.Tags = tags
	item.Scope = scope
	item.SystemKey = systemKey
	item.Revision++
	item.UpdatedAt = now
	if item.Status == knowledge.StatusRejected {
		item.Status = knowledge.StatusDraft
		item.ReviewReason = nil
		item.ReviewedBy = nil
		item.ReviewedAt = nil
		item.RevokedAt = nil
	}

	revision := knowledge.Revision{
		ID:          s.ids.Next(),
		KnowledgeID: item.ID,
		Revision:    item.Revision,
		Title:       title,
		Content:     content,
		Tags:        tags,
		ChangedBy:   int64Ptr(actor.ID),
		CreatedAt:   now,
	}
	if err := s.repo.Update(ctx, item, revision); err != nil {
		return knowledge.Item{}, mapRepoError(err)
	}
	s.recordAudit(ctx, actor.ID, ActionUpdate, item.ID, map[string]any{
		"knowledge_id":   platformid.Format(item.ID),
		"revision":       item.Revision,
		"changed_fields": changed,
	})
	return item, nil
}

// Approve moves a draft or revoked item to approved (admin only, re-review).
func (s *Service) Approve(ctx context.Context, actor platform.Actor, id int64, _ string) (knowledge.Item, error) {
	item, from, err := s.reviewTarget(ctx, actor, id)
	if err != nil {
		return knowledge.Item{}, err
	}
	if !knowledge.CanTransition(from, knowledge.StatusApproved) {
		return knowledge.Item{}, stateConflict("invalid_transition")
	}
	now := s.now()
	item.Status = knowledge.StatusApproved
	item.ReviewReason = nil
	item.RevokedAt = nil
	item.ReviewedBy = int64Ptr(actor.ID)
	item.ReviewedAt = &now
	item.UpdatedAt = now
	if err := s.repo.UpdateStatus(ctx, item, from); err != nil {
		return knowledge.Item{}, mapRepoError(err)
	}
	s.recordAudit(ctx, actor.ID, ActionApprove, item.ID, map[string]any{
		"knowledge_id": platformid.Format(item.ID),
		"from_status":  string(from),
		"to_status":    string(knowledge.StatusApproved),
		"reviewer_id":  platformid.Format(actor.ID),
	})
	s.publish(ctx, actor, "knowledge.approved", map[string]any{
		"knowledge_id": platformid.Format(item.ID),
		"scope":        string(item.Scope),
		"reviewer_id":  platformid.Format(actor.ID),
	})
	return item, nil
}

// Reject moves a draft item to rejected with a mandatory reason (admin only).
func (s *Service) Reject(ctx context.Context, actor platform.Actor, id int64, reason string) (knowledge.Item, error) {
	item, from, err := s.reviewTarget(ctx, actor, id)
	if err != nil {
		return knowledge.Item{}, err
	}
	normalizedReason, err := knowledge.ValidateReason(reason)
	if err != nil {
		return knowledge.Item{}, err
	}
	if !knowledge.CanTransition(from, knowledge.StatusRejected) {
		return knowledge.Item{}, stateConflict("invalid_transition")
	}
	now := s.now()
	item.Status = knowledge.StatusRejected
	item.ReviewReason = &normalizedReason
	item.RevokedAt = nil
	item.ReviewedBy = int64Ptr(actor.ID)
	item.ReviewedAt = &now
	item.UpdatedAt = now
	if err := s.repo.UpdateStatus(ctx, item, from); err != nil {
		return knowledge.Item{}, mapRepoError(err)
	}
	s.recordAudit(ctx, actor.ID, ActionReject, item.ID, map[string]any{
		"knowledge_id": platformid.Format(item.ID),
		"from_status":  string(from),
		"to_status":    string(knowledge.StatusRejected),
		"reason":       normalizedReason,
	})
	s.publish(ctx, actor, "knowledge.rejected", map[string]any{
		"knowledge_id": platformid.Format(item.ID),
		"reason":       normalizedReason,
	})
	return item, nil
}

// Revoke removes an approved item from future injections with a mandatory
// reason (admin only).
func (s *Service) Revoke(ctx context.Context, actor platform.Actor, id int64, reason string) (knowledge.Item, error) {
	item, from, err := s.reviewTarget(ctx, actor, id)
	if err != nil {
		return knowledge.Item{}, err
	}
	normalizedReason, err := knowledge.ValidateReason(reason)
	if err != nil {
		return knowledge.Item{}, err
	}
	if !knowledge.CanTransition(from, knowledge.StatusRevoked) {
		return knowledge.Item{}, stateConflict("invalid_transition")
	}
	now := s.now()
	item.Status = knowledge.StatusRevoked
	item.ReviewReason = &normalizedReason
	item.RevokedAt = &now
	item.ReviewedBy = int64Ptr(actor.ID)
	item.ReviewedAt = &now
	item.UpdatedAt = now
	if err := s.repo.UpdateStatus(ctx, item, from); err != nil {
		return knowledge.Item{}, mapRepoError(err)
	}
	s.recordAudit(ctx, actor.ID, ActionRevoke, item.ID, map[string]any{
		"knowledge_id": platformid.Format(item.ID),
		"from_status":  string(from),
		"to_status":    string(knowledge.StatusRevoked),
		"reason":       normalizedReason,
	})
	s.publish(ctx, actor, "knowledge.revoked", map[string]any{
		"knowledge_id": platformid.Format(item.ID),
		"reason":       normalizedReason,
	})
	return item, nil
}

// Revisions returns the append-only content history of one item.
func (s *Service) Revisions(ctx context.Context, id int64) ([]knowledge.Revision, error) {
	if err := s.requireID(id); err != nil {
		return nil, err
	}
	if err := s.requireRepo(); err != nil {
		return nil, err
	}
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, mapRepoError(err)
	}
	return s.repo.ListRevisions(ctx, id)
}

// Inject selects approved, non-stale knowledge visible to the version, orders
// it by ts_rank (repository order) and truncates it by the settings-driven
// item count and token budget.
func (s *Service) Inject(ctx context.Context, input InjectInput) ([]Injected, error) {
	if input.VersionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if input.MaxItems < 0 {
		return nil, platform.Invalid("max_items", "must not be negative")
	}
	if input.BudgetTokens < 0 {
		return nil, platform.Invalid("budget_tokens", "must not be negative")
	}
	if err := s.requireRepo(); err != nil {
		return nil, err
	}

	maxItems := input.MaxItems
	if maxItems == 0 {
		maxItems = s.settingInt(ctx, SettingInjectionMaxItems, DefaultInjectionMaxItems)
	}
	if maxItems > MaxInjectionMaxItems {
		maxItems = MaxInjectionMaxItems
	}
	if maxItems == 0 {
		return []Injected{}, nil
	}
	budget := input.BudgetTokens
	if budget == 0 {
		budget = s.settingInt(ctx, SettingInjectionBudgetTokens, DefaultInjectionBudgetTokens)
	}

	candidates := maxItems * 4
	if candidates < DefaultListLimit {
		candidates = DefaultListLimit
	}
	if candidates > MaxListLimit {
		candidates = MaxListLimit
	}
	systemKeys := normalizeSystemKeys(input.SystemKeys)
	items, err := s.repo.ListForInjection(ctx, knowledge.InjectionQuery{
		SystemKeys: systemKeys,
		Query:      strings.TrimSpace(input.Query),
		Limit:      candidates,
	})
	if err != nil {
		return nil, err
	}

	injected := make([]Injected, 0, maxItems)
	remaining := budget
	for _, item := range items {
		if len(injected) >= maxItems {
			break
		}
		if item.Status != knowledge.StatusApproved || !scopeMatches(item, systemKeys) {
			continue
		}
		if s.itemStale(ctx, item) {
			continue
		}
		tokens := item.TokenEstimate()
		if tokens > remaining {
			// Contracts §10.3: the first item over budget truncates the rest.
			break
		}
		remaining -= tokens
		injected = append(injected, Injected{
			ID:      item.ID,
			Title:   item.Title,
			Snippet: item.Snippet(),
			Scope:   item.Scope,
			Tokens:  tokens,
		})
	}
	return injected, nil
}

// RecordUsage appends one knowledge_usage row per injected item. The caller
// treats a failure as log-only (contracts/agent-profiles-tools.md §10.3).
func (s *Service) RecordUsage(ctx context.Context, agentRunID, workItemID int64, items []Injected) error {
	if len(items) == 0 {
		return nil
	}
	if agentRunID < 0 {
		return platform.Invalid("agent_run_id", "must not be negative")
	}
	if workItemID < 0 {
		return platform.Invalid("work_item_id", "must not be negative")
	}
	if s.usage == nil {
		return platform.Unavailable("knowledge usage repository is not configured")
	}
	if err := s.requireIDs(); err != nil {
		return err
	}
	now := s.now()
	usages := make([]knowledge.Usage, 0, len(items))
	for _, item := range items {
		tokens := item.Tokens
		if tokens < 0 {
			tokens = 0
		}
		usages = append(usages, knowledge.Usage{
			ID:            s.ids.Next(),
			KnowledgeID:   item.ID,
			AgentRunID:    int64Ptr(agentRunID),
			WorkItemID:    int64Ptr(workItemID),
			TokenEstimate: tokens,
			InjectedAt:    now,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	return s.usage.Insert(ctx, usages)
}

/* --------------------------------- helpers -------------------------------- */

func (s *Service) reviewTarget(ctx context.Context, actor platform.Actor, id int64) (knowledge.Item, knowledge.Status, error) {
	if !actor.IsAdmin() {
		return knowledge.Item{}, "", platform.Forbidden()
	}
	if err := s.requireID(id); err != nil {
		return knowledge.Item{}, "", err
	}
	if err := s.requireRepo(); err != nil {
		return knowledge.Item{}, "", err
	}
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return knowledge.Item{}, "", mapRepoError(err)
	}
	return item, item.Status, nil
}

func (s *Service) reviewRequired(ctx context.Context) bool {
	if s.settings == nil {
		return true
	}
	value, err := s.settings.Bool(ctx, SettingReviewRequired)
	if err != nil {
		// Fail closed: an unreadable setting must not auto-approve knowledge.
		return true
	}
	return value
}

func (s *Service) settingInt(ctx context.Context, key string, def int) int {
	if s.settings == nil {
		return def
	}
	value, err := s.settings.Int(ctx, key)
	if err != nil || value < 0 {
		return def
	}
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(value)
}

func (s *Service) decorateSourceStale(ctx context.Context, items []knowledge.Item) {
	if s.sources == nil || len(items) == 0 {
		return
	}
	cache := make(map[int64]bool, len(items))
	for index := range items {
		sourceID := items[index].SourceVersionID
		if sourceID == nil {
			continue
		}
		exists, checked := cache[*sourceID]
		if !checked {
			value, err := s.sources.SourceVersionExists(ctx, *sourceID)
			if err != nil {
				continue
			}
			exists = value
			cache[*sourceID] = value
		}
		items[index].SourceStale = !exists
	}
}

func (s *Service) decorateItemStale(ctx context.Context, item *knowledge.Item) {
	items := []knowledge.Item{*item}
	s.decorateSourceStale(ctx, items)
	*item = items[0]
}

func (s *Service) itemStale(ctx context.Context, item knowledge.Item) bool {
	if s.sources == nil || item.SourceVersionID == nil {
		return false
	}
	exists, err := s.sources.SourceVersionExists(ctx, *item.SourceVersionID)
	if err != nil {
		return false
	}
	return !exists
}

func (s *Service) requireRepo() error {
	if s.repo == nil {
		return platform.Unavailable("knowledge repository is not configured")
	}
	return nil
}

func (s *Service) requireIDs() error {
	if s.ids == nil {
		return platform.NewError(500, "internal", "id generator is not configured")
	}
	return nil
}

func (s *Service) requireID(id int64) error {
	if id <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	return nil
}

func (s *Service) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now().UTC()
}

func (s *Service) recordAudit(ctx context.Context, actorID int64, action string, targetID int64, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	payload := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		payload[key] = value
	}
	if actorID > 0 {
		payload["actor_id"] = platformid.Format(actorID)
	}
	_ = s.audit.Record(ctx, action, targetID, payload)
}

func (s *Service) publish(ctx context.Context, actor platform.Actor, eventType string, payload map[string]any) {
	if s.events == nil {
		return
	}
	event := eventingapp.Event{
		Channel:   "system",
		Type:      eventType,
		Scope:     map[string]any{},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	}
	event.ActorID = int64Ptr(actor.ID)
	_ = s.events.Publish(ctx, event)
}

func mapRepoError(err error) error {
	switch {
	case errors.Is(err, knowledge.ErrNotFound):
		return platform.NotFound("knowledge_item")
	case errors.Is(err, knowledge.ErrStateConflict):
		return stateConflict("concurrent_update")
	default:
		return err
	}
}

func stateConflict(reason string) *platform.Error {
	conflict := platform.StateConflict("knowledge item state conflict")
	conflict.Details = map[string]any{"reason": reason}
	return conflict
}

func normalizeListScope(scope knowledge.Scope, rawSystemKey string) (knowledge.Scope, string, error) {
	systemKey := strings.TrimSpace(rawSystemKey)
	if scope == "" {
		return "", systemKey, nil
	}
	if !scope.Valid() {
		return "", "", platform.Invalid("scope", "scope must be global or system")
	}
	if scope == knowledge.ScopeGlobal && systemKey != "" {
		return "", "", platform.Invalid("system_key", "must be empty when scope is global")
	}
	return scope, systemKey, nil
}

func changedFields(item knowledge.Item, title, content string, tags []string, scope knowledge.Scope, systemKey *string) []string {
	changed := make([]string, 0, 5)
	if title != item.Title {
		changed = append(changed, "title")
	}
	if content != item.Content {
		changed = append(changed, "content")
	}
	if !stringSlicesEqual(tags, item.Tags) {
		changed = append(changed, "tags")
	}
	if scope != item.Scope {
		changed = append(changed, "scope")
	}
	if !stringPointersEqual(systemKey, item.SystemKey) {
		changed = append(changed, "system_key")
	}
	return changed
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func stringPointersEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func normalizeSystemKeys(raw []string) []string {
	keys := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		key := strings.ToLower(strings.TrimSpace(item))
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func scopeMatches(item knowledge.Item, systemKeys []string) bool {
	if item.Scope == knowledge.ScopeGlobal {
		return true
	}
	if item.Scope != knowledge.ScopeSystem || item.SystemKey == nil {
		return false
	}
	for _, key := range systemKeys {
		if key == *item.SystemKey {
			return true
		}
	}
	return false
}

func int64Ptr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

// Package versionapp implements the version, requirement, conversation and
// attachment use cases.
package versionapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// maxDedupeRequirements bounds one duplicate-draft scan. Dedupe reads a single
// page of up to this many drafts (newest IDs first) and only collapses
// duplicates inside that page. A version with more drafts may keep duplicates in
// the unread older tail, and re-running is not guaranteed to advance: the same
// newest page is read again when it has no duplicates. Accepted limitation until
// dedupe paginates; the HTTP response correctly reports an empty page.
const maxDedupeRequirements = 200

// requirementSourceIDPattern matches the leading source identifier of a draft
// title ("FR-011 账号密码登录", "NFR-001 …", "FR-011: …"). The identifier is the
// deterministic source key: two curators extracting the same requirement from
// the same input produce the same key even when the surrounding title wording
// differs.
var requirementSourceIDPattern = regexp.MustCompile(`^([A-Za-z]{2,8}-[0-9]{1,6})(?:\s|$|[:：])`)

// requirementSourceKey derives the deterministic per-version requirement source
// key. Titles without a leading FR/NFR-style identifier keep an empty key: those
// rows are not auto-deduplicated (the explicit requirement.dedupe scan still
// works on them) so user-authored arbitrary titles keep their current escaping.
func requirementSourceKey(title string) string {
	match := requirementSourceIDPattern.FindStringSubmatch(strings.TrimSpace(title))
	if len(match) != 2 {
		return ""
	}
	return "source:" + strings.ToUpper(match[1])
}

// ConfirmationSettings is the narrow settings-center port for the requirement
// confirmation gate. The composition root satisfies it with the settings
// service adapter (Int + Raw, the same read surface codeintel's SettingsReader
// uses for JSON values); the domain gate itself stays pure and receives the
// resolved values as parameters.
type ConfirmationSettings interface {
	Int(ctx context.Context, key string) (int64, error)
	Raw(ctx context.Context, key string) (json.RawMessage, error)
}

// Confirmation-gate settings keys (registered in the settings catalog).
const (
	SettingPlaceholderTerms    = "validation.placeholder_terms"
	SettingMinDescriptionChars = "validation.min_description_chars"
)

// RequirementService implements requirement draft lifecycle, immutable
// revisions, single/batch confirmation, rollback and relations.
type RequirementService struct {
	repo     version.RequirementRepository
	versions VersionReader
	stale    CaseStaleHook
	audit    auditapp.Recorder
	events   eventingapp.Publisher
	ids      platformid.Generator
	clock    clock.Clock
	reopener RequirementVersionReopener
	settings ConfirmationSettings
}

// NewRequirementService wires the requirement use cases. audit and events may
// be nil in unit tests; stale defaults to the P1 no-op hook.
//
// The requirement contract of design v1.3.1 §5.1 has no resource targets, so
// the service resolves nothing against the resource catalog: the former
// ResourceCatalog port and its constructor parameter were removed (v0.2.4-H
// H-B3) once no caller needed to pass a catalog.
func NewRequirementService(
	repo version.RequirementRepository,
	versions VersionReader,
	stale CaseStaleHook,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
	ids platformid.Generator,
	clk clock.Clock,
) *RequirementService {
	if stale == nil {
		stale = NoopCaseStaleHook{}
	}
	return &RequirementService{
		repo: repo, versions: versions, stale: stale,
		audit: audit, events: events, ids: ids, clock: clk,
	}
}

// SetVersionReopener attaches the best-effort completed-version reopen
// projection. It returns the service for fluent wiring.
func (s *RequirementService) SetVersionReopener(reopener RequirementVersionReopener) *RequirementService {
	if s != nil {
		s.reopener = reopener
	}
	return s
}

// SetConfirmationSettings wires the settings-center reader for the
// confirmation gate (validation.placeholder_terms /
// validation.min_description_chars). It returns the service for fluent wiring.
// An unwired reader fails closed at confirm time: the gate never silently
// confirms without its configured increment.
func (s *RequirementService) SetConfirmationSettings(settings ConfirmationSettings) *RequirementService {
	if s != nil {
		s.settings = settings
	}
	return s
}

// confirmationRules reads the confirmation-gate settings at confirm time, so a
// settings change applies to the next confirmation (never a value cached at
// startup). An unwired reader, an unreadable key, a terms value that is not a
// JSON string array, or a floor below the catalog minimum (1) fails closed
// with an explicit error instead of silently relaxing the gate.
func (s *RequirementService) confirmationRules(ctx context.Context) ([]string, int, error) {
	if s == nil || s.settings == nil {
		return nil, 0, platform.Unavailable("the settings reader is not wired for requirement confirmation")
	}
	raw, err := s.settings.Raw(ctx, SettingPlaceholderTerms)
	if err != nil {
		return nil, 0, err
	}
	var terms []string
	if err := json.Unmarshal(raw, &terms); err != nil {
		return nil, 0, platform.Unavailable(SettingPlaceholderTerms + " must be a JSON array of strings")
	}
	// Drop empty/whitespace-only terms: a term normalizing to "" would
	// whole-value-match any empty optional field via IsPlaceholder.
	sanitized := make([]string, 0, len(terms))
	for _, term := range terms {
		if version.NormalizePlaceholderTerm(term) != "" {
			sanitized = append(sanitized, term)
		}
	}
	minChars, err := s.settings.Int(ctx, SettingMinDescriptionChars)
	if err != nil {
		return nil, 0, err
	}
	if minChars < 1 {
		return nil, 0, platform.Unavailable(SettingMinDescriptionChars + " must be at least 1")
	}
	return sanitized, int(minChars), nil
}

// reopenCompleted applies the §7.5 requirement_created/requirement_reopened
// trigger after the requirement write committed. Failures are ignored: the
// reconciler is the fallback.
func (s *RequirementService) reopenCompleted(ctx context.Context, versionID int64, trigger, reason string) {
	if s == nil || s.reopener == nil {
		return
	}
	_ = s.reopener.ReopenIfCompleted(ctx, versionID, trigger, reason)
}

// Create persists a new draft requirement with revision 1 and projects the
// version to running (idempotent, best effort).
func (s *RequirementService) Create(ctx context.Context, versionID int64, in RequirementDraft, actorID int64) (RequirementDetail, error) {
	if versionID <= 0 {
		return RequirementDetail{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return RequirementDetail{}, err
	}
	revision, err := normalizeDraft(in, nil)
	if err != nil {
		return RequirementDetail{}, err
	}

	now := s.clock.Now()
	requirementID := s.ids.Next()
	revisionID := s.ids.Next()
	revision.ID = revisionID
	revision.RequirementID = requirementID
	revision.Revision = 1
	revision.CreatedBy = optionalActor(actorID)
	revision.CreatedByRunID = agentRunID(ctx)
	revision.CreatedAt = now

	requirement := version.Requirement{
		ID:               requirementID,
		VersionID:        versionID,
		CurrentVersionID: &revisionID,
		Status:           version.RequirementDraft,
		SourceKey:        requirementSourceKey(revision.Title),
		CreatedBy:        optionalActor(actorID),
		CreatedByRunID:   agentRunID(ctx),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.repo.Create(ctx, requirement, revision); err != nil {
		if errors.Is(err, version.ErrRequirementDuplicate) && requirement.SourceKey != "" {
			return s.resolveDuplicateDraft(ctx, versionID, requirement.SourceKey, in, actorID)
		}
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}

	requirementIDString := platformid.Format(requirementID)
	revisionIDString := platformid.Format(revisionID)
	s.publishEvent(ctx, versionID, "requirement.created", map[string]any{
		"requirement_id": requirementIDString,
		"status":         string(version.RequirementDraft),
		"title":          revision.Title,
	}, map[string]any{"requirement_id": requirementIDString})
	s.publishEvent(ctx, versionID, "requirement.version_created", map[string]any{
		"requirement_id":         requirementIDString,
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"status":                 string(version.RequirementDraft),
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.created", "requirement", requirementIDString, map[string]any{
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"status":                 string(version.RequirementDraft),
		"title":                  revision.Title,
	})
	s.markRunning(ctx, versionID)
	s.reopenCompleted(ctx, versionID, VersionReopenTriggerRequirementCreated, "new requirement created")
	return s.Get(ctx, versionID, requirementID)
}

// resolveDuplicateDraft turns a duplicate-source Create into an update/no-op on
// the existing requirement instead of a second row. A create that says no more
// than the draft already there never replaces it; a richer one enriches it. A
// clarified/closed requirement is never rewritten by a draft_create (the
// Curator must use requirement.reopen_propose).
//
// The draft is normalized HERE rather than in Create, because acceptance-
// criterion ids are inherited from the revision that already exists: a re-emit
// of the same criteria must keep their ids (and therefore the same
// ContentHash) instead of minting new ones.
func (s *RequirementService) resolveDuplicateDraft(ctx context.Context, versionID int64, sourceKey string, in RequirementDraft, actorID int64) (RequirementDetail, error) {
	existing, err := s.repo.GetBySourceKey(ctx, versionID, sourceKey)
	if err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}
	current, ok := currentVersion(existing)
	if !ok {
		return RequirementDetail{}, platform.StateConflict("requirement has no current revision")
	}
	if existing.Requirement.Status != version.RequirementDraft {
		return s.detailFromAggregate(existing)
	}
	incoming, err := normalizeDraft(in, current.AcceptanceCriteria)
	if err != nil {
		return RequirementDetail{}, err
	}
	if requirementContentCount(incoming) <= requirementContentCount(current) {
		return s.detailFromAggregate(existing)
	}

	now := s.clock.Now()
	revisionID := s.ids.Next()
	revision := incoming
	revision.ID = revisionID
	revision.RequirementID = existing.Requirement.ID
	revision.Revision = current.Revision + 1
	revision.CreatedBy = optionalActor(actorID)
	revision.CreatedByRunID = agentRunID(ctx)
	revision.CreatedAt = now

	requirement := existing.Requirement
	requirement.Status = version.RequirementDraft
	requirement.CurrentVersionID = &revisionID
	requirement.UpdatedAt = now
	if err := s.repo.AddRevision(ctx, requirement, revision); err != nil {
		if errors.Is(err, version.ErrStateConflict) || errors.Is(err, version.ErrRequirementDuplicate) {
			// A concurrent duplicate create won the enrichment; return the
			// newest state instead of surfacing a duplicate/conflict.
			latest, getErr := s.repo.Get(ctx, versionID, requirement.ID)
			if getErr != nil {
				return RequirementDetail{}, mapRequirementRepositoryError(getErr)
			}
			return s.detailFromAggregate(latest)
		}
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}

	requirementIDString := platformid.Format(requirement.ID)
	revisionIDString := platformid.Format(revisionID)
	s.publishEvent(ctx, versionID, "requirement.version_created", map[string]any{
		"requirement_id":         requirementIDString,
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"status":                 string(version.RequirementDraft),
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.updated", "requirement", requirementIDString, map[string]any{
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"reason":                 "duplicate draft create enriched the requirement",
		"source_key":             sourceKey,
	})
	s.markRunning(ctx, versionID)
	s.reopenCompleted(ctx, versionID, VersionReopenTriggerRequirementReopened, "duplicate draft create enriched the requirement")
	return s.Get(ctx, versionID, requirement.ID)
}

// requirementContentCount measures how much of the §5.1 contract one revision
// fills in. It is the duplicate-draft enrichment measure that replaced the old
// target-ID count: a draft_create repeating an existing source key only adds a
// revision when it says strictly more than the draft already there, so a bare
// re-emit can never overwrite a richer clarification.
func requirementContentCount(revision version.RequirementVersion) int {
	count := len(revision.AcceptanceCriteria) + len(revision.AccountRolesHint)
	for _, text := range []string{revision.Rules, revision.ScopeOut, revision.SutDescription} {
		if strings.TrimSpace(text) != "" {
			count++
		}
	}
	if !revision.UserStory.Empty() {
		count++
	}
	return count
}

// CountClarifiedRequirements reports whether the version has at least one
// clarified requirement. It is the design-conversation gate: a design round
// without clarified requirements has nothing to design.
func (s *RequirementService) CountClarifiedRequirements(ctx context.Context, versionID int64) (int, error) {
	if versionID <= 0 {
		return 0, platform.Invalid("id", "must be a positive decimal ID")
	}
	items, _, err := s.List(ctx, versionID, string(version.RequirementClarified), 1, nil)
	if err != nil {
		return 0, err
	}
	if len(items) > 0 {
		return 1, nil
	}
	return 0, nil
}

// Update always creates a new immutable revision (revision+1). Editing a
// clarified requirement reopens it as draft; the old confirmation stays bound
// to the old revision.
func (s *RequirementService) Update(ctx context.Context, versionID, requirementID int64, in RequirementDraft, reason string, actorID int64) (RequirementDetail, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return RequirementDetail{}, err
	}
	reason = strings.TrimSpace(reason)
	if err := validationStringMax("reason", reason, version.RequirementReasonMax); err != nil {
		return RequirementDetail{}, err
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}
	requirement := aggregate.Requirement
	if requirement.Status != version.RequirementDraft && requirement.Status != version.RequirementClarified {
		return RequirementDetail{}, platform.StateConflict("requirement is not editable")
	}
	current, ok := currentVersion(aggregate)
	if !ok {
		return RequirementDetail{}, platform.StateConflict("requirement has no current revision")
	}
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return RequirementDetail{}, err
	}
	// The previous revision's criteria are the inheritance source for the AC
	// ids: an unchanged criterion keeps its id, a changed one retires it into a
	// hole and appends a new one (design v1.3.1 §3.6).
	revision, err := normalizeDraft(in, current.AcceptanceCriteria)
	if err != nil {
		return RequirementDetail{}, err
	}

	now := s.clock.Now()
	revisionID := s.ids.Next()
	revision.ID = revisionID
	revision.RequirementID = requirementID
	revision.Revision = current.Revision + 1
	revision.CreatedBy = optionalActor(actorID)
	revision.CreatedAt = now

	requirement.Status = version.RequirementDraft
	requirement.CurrentVersionID = &revisionID
	requirement.SourceKey = requirementSourceKey(revision.Title)
	requirement.UpdatedAt = now
	if err := s.repo.AddRevision(ctx, requirement, revision); err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}

	staleHook := s.stale
	if staleHook == nil {
		staleHook = NoopCaseStaleHook{}
	}
	affected, err := staleHook.MarkRequirementChanged(ctx, versionID, requirementID, reason, revision.Revision)
	if err != nil {
		return RequirementDetail{}, err
	}
	if affected == nil {
		affected = []string{}
	}

	requirementIDString := platformid.Format(requirementID)
	revisionIDString := platformid.Format(revisionID)
	s.publishEvent(ctx, versionID, "requirement.version_created", map[string]any{
		"requirement_id":            requirementIDString,
		"requirement_version_id":    revisionIDString,
		"revision":                  revision.Revision,
		"status":                    string(version.RequirementDraft),
		"affected_case_version_ids": affected,
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.updated", "requirement", requirementIDString, map[string]any{
		"requirement_version_id":    revisionIDString,
		"revision":                  revision.Revision,
		"reason":                    reason,
		"affected_case_version_ids": affected,
	})
	s.reopenCompleted(ctx, versionID, VersionReopenTriggerRequirementReopened, reason)
	return s.Get(ctx, versionID, requirementID)
}

// Get returns the full requirement detail.
func (s *RequirementService) Get(ctx context.Context, versionID, requirementID int64) (RequirementDetail, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return RequirementDetail{}, err
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}
	return s.detailFromAggregate(aggregate)
}

// List returns one id-descending page of current-revision requirement
// summaries.
func (s *RequirementService) List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]RequirementView, bool, error) {
	if versionID <= 0 {
		return nil, false, platform.Invalid("id", "must be a positive decimal ID")
	}
	if s.versions != nil {
		if _, err := s.versions.Get(ctx, versionID); err != nil {
			return nil, false, err
		}
	}
	statusFilter, err := requirementStatusFilter(status)
	if err != nil {
		return nil, false, err
	}
	limit = normalizeLimit(limit)
	items, err := s.repo.List(ctx, versionID, statusFilter, limit+1, beforeID)
	if err != nil {
		return nil, false, mapRequirementRepositoryError(err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	views := make([]RequirementView, 0, len(items))
	for _, item := range items {
		views = append(views, requirementViewFromListItem(item))
	}
	return views, hasMore, nil
}

// Confirm freezes the current revision. Repeated confirmation of the same
// revision is idempotent and reports confirmed=false.
func (s *RequirementService) Confirm(ctx context.Context, versionID, requirementID, actorID int64) (ConfirmResult, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return ConfirmResult{}, err
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return ConfirmResult{}, mapRequirementRepositoryError(err)
	}
	requirement := aggregate.Requirement
	current, ok := currentVersion(aggregate)
	if !ok {
		return ConfirmResult{}, platform.StateConflict("requirement has no current revision")
	}

	if requirement.Status == version.RequirementClarified {
		for _, confirmation := range aggregate.Confirmations {
			if confirmation.RequirementVersionID == current.ID {
				detail, err := s.detailFromAggregate(aggregate)
				if err != nil {
					return ConfirmResult{}, err
				}
				return ConfirmResult{Confirmed: false, Requirement: detail}, nil
			}
		}
		return ConfirmResult{}, platform.StateConflict("requirement is already clarified")
	}
	if requirement.Status != version.RequirementDraft {
		return ConfirmResult{}, platform.StateConflict("requirement is not confirmable")
	}
	// The confirmation gate reads validation.placeholder_terms /
	// validation.min_description_chars from the settings center at confirm
	// time and passes them to the pure domain gate (SC-E1).
	terms, minDescChars, err := s.confirmationRules(ctx)
	if err != nil {
		return ConfirmResult{}, err
	}
	if err := current.ValidateConfirmed(terms, minDescChars); err != nil {
		return ConfirmResult{}, err
	}
	// v0.2.4-B: the version lookup stays as the scope guard it always was, but
	// there is no target validation left to feed it -- the requirement contract
	// names its subject in prose (design v1.3.1 §5.1).
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return ConfirmResult{}, err
	}

	now := s.clock.Now()
	confirmation := version.Confirmation{
		ID:                     s.ids.Next(),
		RequirementVersionID:   current.ID,
		Scope:                  version.ConfirmationScopeRequirement,
		ActorID:                actorID,
		ConfirmedAt:            now,
		AffectedRequirementIDs: []string{},
	}
	requirement.Status = version.RequirementClarified
	requirement.UpdatedAt = now
	if err := s.repo.Confirm(ctx, requirement, confirmation); err != nil {
		return ConfirmResult{}, mapRequirementRepositoryError(err)
	}

	requirementIDString := platformid.Format(requirementID)
	revisionIDString := platformid.Format(current.ID)
	s.publishEvent(ctx, versionID, "requirement.clarified", map[string]any{
		"requirement_id":         requirementIDString,
		"requirement_version_id": revisionIDString,
		"scope":                  version.ConfirmationScopeRequirement,
		"batch_id":               nil,
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.confirmed", "requirement", requirementIDString, map[string]any{
		"requirement_version_id": revisionIDString,
		"scope":                  version.ConfirmationScopeRequirement,
	})
	s.markRunning(ctx, versionID)

	detail, err := s.Get(ctx, versionID, requirementID)
	if err != nil {
		return ConfirmResult{}, err
	}
	return ConfirmResult{Confirmed: true, Requirement: detail}, nil
}

// BatchConfirm validates every draft in the batch inside one transaction and
// skips already-confirmed requirements. Any invalid draft fails the whole
// batch (P1-D8).
func (s *RequirementService) BatchConfirm(ctx context.Context, versionID int64, requirementIDs []int64, actorID int64) (BatchConfirmResult, error) {
	if versionID <= 0 {
		return BatchConfirmResult{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	ids, err := dedupeRequirementIDs(requirementIDs)
	if err != nil {
		return BatchConfirmResult{}, err
	}
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return BatchConfirmResult{}, err
	}
	items, err := s.repo.GetMany(ctx, versionID, ids)
	if err != nil {
		return BatchConfirmResult{}, mapRequirementRepositoryError(err)
	}

	now := s.clock.Now()
	// Scope guard only: the requirement contract has no resource targets left
	// to validate against the version's bound environment (§5.1).
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return BatchConfirmResult{}, err
	}
	// One settings snapshot validates the whole batch: every draft is checked
	// against the same terms/floor, read at confirm time like the single path.
	// H-F10b #23: the read is LAZY, symmetric with Confirm — that path returns a
	// clarified requirement's idempotent replay before it ever touches settings.
	// An all-clarified batch is likewise a no-op replay and must not fail with a
	// settings 503 it never needed. The snapshot is still taken once, at the first
	// draft, so a mid-batch settings change cannot validate two drafts differently.
	var terms []string
	var minDescChars int
	rulesLoaded := false
	confirmed := make([]int64, 0, len(items))
	skipped := make([]int64, 0, len(items))
	toConfirm := make([]version.Requirement, 0, len(items))
	toConfirmIDs := make([]string, 0, len(items))
	for _, item := range items {
		switch item.Requirement.Status {
		case version.RequirementClarified:
			skipped = append(skipped, item.Requirement.ID)
		case version.RequirementDraft:
			if !rulesLoaded {
				var rulesErr error
				terms, minDescChars, rulesErr = s.confirmationRules(ctx)
				if rulesErr != nil {
					return BatchConfirmResult{}, rulesErr
				}
				rulesLoaded = true
			}
			if err := item.Current.ValidateConfirmed(terms, minDescChars); err != nil {
				return BatchConfirmResult{}, withRequirementID(err, item.Requirement.ID)
			}
			requirement := item.Requirement
			requirement.Status = version.RequirementClarified
			requirement.UpdatedAt = now
			toConfirm = append(toConfirm, requirement)
			confirmed = append(confirmed, item.Requirement.ID)
			toConfirmIDs = append(toConfirmIDs, platformid.Format(item.Requirement.ID))
		default:
			return BatchConfirmResult{}, withRequirementID(platform.StateConflict("requirement is not confirmable"), item.Requirement.ID)
		}
	}

	batchID := platformid.Format(s.ids.Next())
	confirmations := make([]version.Confirmation, 0, len(toConfirm))
	affected := append([]string{}, toConfirmIDs...)
	for _, requirement := range toConfirm {
		var revisionID int64
		for _, item := range items {
			if item.Requirement.ID == requirement.ID {
				revisionID = item.Current.ID
				break
			}
		}
		batchIDValue := batchID
		confirmations = append(confirmations, version.Confirmation{
			ID:                     s.ids.Next(),
			RequirementVersionID:   revisionID,
			Scope:                  version.ConfirmationScopeBatch,
			BatchID:                &batchIDValue,
			ActorID:                actorID,
			ConfirmedAt:            now,
			AffectedRequirementIDs: append([]string{}, affected...),
		})
	}

	if len(toConfirm) > 0 {
		if err := s.repo.BatchConfirm(ctx, toConfirm, confirmations); err != nil {
			return BatchConfirmResult{}, mapRequirementRepositoryError(err)
		}
		for _, requirement := range toConfirm {
			requirementIDString := platformid.Format(requirement.ID)
			revisionIDString := platformid.Format(*requirement.CurrentVersionID)
			s.publishEvent(ctx, versionID, "requirement.clarified", map[string]any{
				"requirement_id":         requirementIDString,
				"requirement_version_id": revisionIDString,
				"scope":                  version.ConfirmationScopeBatch,
				"batch_id":               batchID,
			}, map[string]any{"requirement_id": requirementIDString})
			s.recordAudit(ctx, actorID, "requirement.confirmed", "requirement", requirementIDString, map[string]any{
				"requirement_version_id": revisionIDString,
				"scope":                  version.ConfirmationScopeBatch,
				"batch_id":               batchID,
			})
		}
		s.markRunning(ctx, versionID)
	}

	if confirmed == nil {
		confirmed = []int64{}
	}
	if skipped == nil {
		skipped = []int64{}
	}
	return BatchConfirmResult{BatchID: batchID, Confirmed: confirmed, Skipped: skipped}, nil
}

// Dedupe collapses duplicate draft requirements of one version. Two drafts are
// duplicates when their normalized titles match, or when their normalized
// content matches -- user story, rules, system under test and acceptance
// criteria (a refined title is still the same requirement). The lowest id of
// each group survives; the rest become obsolete without adding a revision.
// Clarified requirements are never touched.
func (s *RequirementService) Dedupe(ctx context.Context, versionID int64, actorID int64) (RequirementDedupeResult, error) {
	if versionID <= 0 {
		return RequirementDedupeResult{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if s.versions != nil {
		if _, err := s.versions.Get(ctx, versionID); err != nil {
			return RequirementDedupeResult{}, err
		}
	}
	items, err := s.repo.List(ctx, versionID, version.RequirementDraft, maxDedupeRequirements, nil)
	if err != nil {
		return RequirementDedupeResult{}, mapRequirementRepositoryError(err)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Requirement.ID < items[j].Requirement.ID })
	result := RequirementDedupeResult{KeptIDs: []int64{}, ObsoleteIDs: []int64{}}
	byTitle := make(map[string]int64, len(items))
	byContent := make(map[string]int64, len(items))
	now := s.clock.Now()
	for _, item := range items {
		requirementID := item.Requirement.ID
		titleKey := normalizeRequirementText(item.Current.Title)
		contentKey := requirementContentKey(item.Current)
		hasContentKey := contentKey != ""
		if survivor, duplicate := byTitle[titleKey]; duplicate {
			if err := s.obsoleteRequirement(ctx, versionID, requirementID, "duplicate_title", actorID, now); err != nil {
				return RequirementDedupeResult{}, err
			}
			byContent[contentKey] = survivor
			result.ObsoleteIDs = append(result.ObsoleteIDs, requirementID)
			continue
		}
		if hasContentKey {
			if survivor, duplicate := byContent[contentKey]; duplicate {
				if err := s.obsoleteRequirement(ctx, versionID, requirementID, "duplicate_content", actorID, now); err != nil {
					return RequirementDedupeResult{}, err
				}
				byTitle[titleKey] = survivor
				result.ObsoleteIDs = append(result.ObsoleteIDs, requirementID)
				continue
			}
		}
		byTitle[titleKey] = requirementID
		if hasContentKey {
			byContent[contentKey] = requirementID
		}
		result.KeptIDs = append(result.KeptIDs, requirementID)
	}
	if len(result.ObsoleteIDs) > 0 {
		s.publishEvent(ctx, versionID, "requirement.deduped", map[string]any{
			"kept_ids":     formatStageIDs(result.KeptIDs),
			"obsolete_ids": formatStageIDs(result.ObsoleteIDs),
		}, map[string]any{})
		s.recordAudit(ctx, actorID, "requirement.deduped", "version", platformid.Format(versionID), map[string]any{
			"kept_ids":     formatStageIDs(result.KeptIDs),
			"obsolete_ids": formatStageIDs(result.ObsoleteIDs),
		})
	}
	return result, nil
}

// Delete retires a duplicate draft requirement. Only a draft can be deleted: a
// clarified requirement must be rolled back first, and closed/obsolete rows stay
// untouched (idempotent repeat on obsolete). It never adds a revision.
func (s *RequirementService) Delete(ctx context.Context, versionID, requirementID int64, reason string, actorID int64) (RequirementDetail, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return RequirementDetail{}, err
	}
	reason = strings.TrimSpace(reason)
	if err := validationStringMax("reason", reason, version.RequirementReasonMax); err != nil {
		return RequirementDetail{}, err
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}
	requirement := aggregate.Requirement
	switch requirement.Status {
	case version.RequirementObsolete:
		// Idempotent delete: the requirement is already retired.
		return s.Get(ctx, versionID, requirementID)
	case version.RequirementDraft:
	default:
		return RequirementDetail{}, platform.StateConflict("only a draft requirement can be deleted; roll back a clarified requirement first")
	}
	if err := s.obsoleteRequirement(ctx, versionID, requirementID, reason, actorID, s.clock.Now()); err != nil {
		return RequirementDetail{}, err
	}
	s.recordAudit(ctx, actorID, "requirement.deleted", "requirement", platformid.Format(requirementID), map[string]any{
		"reason": reason,
	})
	return s.Get(ctx, versionID, requirementID)
}

// Merge folds duplicate draft requirements into one canonical target. The
// target must still be a draft: absorbing duplicates rewrites the target and
// un-confirms it, so a clarified requirement must be rolled back first. The
// canonical target keeps its own title, user story, system description and
// scope_out, and absorbs each source's rules text, account-role hints and
// acceptance criteria (criteria deduped by normalized content, roles by value,
// rules by text) plus the highest priority; sources are retired as obsolete and
// a relation records the merge. The target gets a new draft revision only when
// its content actually changed; a target that already contains the sources just
// retires them.
func (s *RequirementService) Merge(ctx context.Context, versionID, targetID int64, sourceIDs []int64, actorID int64) (RequirementMergeResult, error) {
	if err := validateIDs(versionID, targetID); err != nil {
		return RequirementMergeResult{}, err
	}
	if len(sourceIDs) == 0 {
		return RequirementMergeResult{}, platform.Invalid("source_requirement_ids", "must contain at least one source requirement")
	}
	sources, err := dedupeRequirementIDs(sourceIDs)
	if err != nil {
		return RequirementMergeResult{}, err
	}
	kept := make([]int64, 0, len(sources))
	for _, sourceID := range sources {
		if sourceID == targetID {
			return RequirementMergeResult{}, platform.Invalid("source_requirement_ids", "must not contain the target requirement")
		}
		kept = append(kept, sourceID)
	}
	if len(kept) == 0 {
		return RequirementMergeResult{}, platform.Invalid("source_requirement_ids", "must contain at least one source requirement")
	}
	targetAggregate, err := s.repo.Get(ctx, versionID, targetID)
	if err != nil {
		return RequirementMergeResult{}, mapRequirementRepositoryError(err)
	}
	target := targetAggregate.Requirement
	if target.Status != version.RequirementDraft {
		return RequirementMergeResult{}, platform.StateConflict("only a draft requirement can be a merge target; roll back a clarified requirement first")
	}
	targetCurrent, ok := currentVersion(targetAggregate)
	if !ok {
		return RequirementMergeResult{}, platform.StateConflict("merge target has no current revision")
	}

	// The canonical target keeps its own title, user story, system description
	// and scope_out; a source folds in as extra rules text, extra account-role
	// hints and extra acceptance criteria, and the highest priority wins.
	// Scope_out is deliberately NOT concatenated: appending two out-of-scope
	// lists would easily push the merged revision past RequirementScopeOutMax
	// and fail its own length bound.
	merged := RequirementDraft{
		Title:            targetCurrent.Title,
		UserStory:        toUserStoryView(targetCurrent.UserStory),
		Rules:            targetCurrent.Rules,
		Priority:         targetCurrent.Priority,
		Constraints:      targetCurrent.Constraints,
		SutDescription:   targetCurrent.SutDescription,
		ScopeOut:         targetCurrent.ScopeOut,
		AccountRolesHint: append([]string{}, targetCurrent.AccountRolesHint...),
	}
	criteria := make([]RequirementAcceptanceCriterionInput, 0, len(targetCurrent.AcceptanceCriteria))
	seenCriteria := make(map[string]struct{}, len(targetCurrent.AcceptanceCriteria))
	// appendCriteria unions by normalized content, so a source that repeats one
	// of the target's criteria cannot add a second copy -- and, because the ids
	// are re-derived from the target's revision below, cannot smuggle in a
	// second id for content the target already has.
	appendCriteria := func(source []version.AcceptanceCriterion) {
		for _, criterion := range source {
			key := version.AcceptanceCriterionKey(criterion)
			if _, duplicate := seenCriteria[key]; duplicate {
				continue
			}
			seenCriteria[key] = struct{}{}
			criteria = append(criteria, RequirementAcceptanceCriterionInput{
				Given: criterion.Given,
				When:  criterion.When,
				Then:  criterion.Then,
			})
		}
	}
	appendCriteria(targetCurrent.AcceptanceCriteria)
	for _, sourceID := range kept {
		sourceAggregate, err := s.repo.Get(ctx, versionID, sourceID)
		if err != nil {
			return RequirementMergeResult{}, mapRequirementRepositoryError(err)
		}
		source := sourceAggregate.Requirement
		if source.Status != version.RequirementDraft {
			return RequirementMergeResult{}, platform.StateConflict("only draft requirements can be merged into another requirement")
		}
		sourceCurrent, ok := currentVersion(sourceAggregate)
		if !ok {
			return RequirementMergeResult{}, platform.StateConflict("merge source has no current revision")
		}
		merged.Rules = appendUniqueText(merged.Rules, sourceCurrent.Rules)
		merged.AccountRolesHint = appendUniqueStrings(merged.AccountRolesHint, sourceCurrent.AccountRolesHint)
		appendCriteria(sourceCurrent.AcceptanceCriteria)
		if priorityRank(sourceCurrent.Priority) < priorityRank(merged.Priority) {
			merged.Priority = sourceCurrent.Priority
		}
	}
	merged.AcceptanceCriteria = criteria
	now := s.clock.Now()
	revision, err := normalizeDraft(merged, targetCurrent.AcceptanceCriteria)
	if err != nil {
		return RequirementMergeResult{}, err
	}
	if revision.ContentHash != targetCurrent.ContentHash {
		revision.ID = s.ids.Next()
		revision.RequirementID = targetID
		revision.Revision = targetCurrent.Revision + 1
		revision.CreatedBy = optionalActor(actorID)
		revision.CreatedByRunID = agentRunID(ctx)
		revision.CreatedAt = now
		target.Status = version.RequirementDraft
		target.CurrentVersionID = &revision.ID
		target.UpdatedAt = now
		if err := s.repo.AddRevision(ctx, target, revision); err != nil {
			return RequirementMergeResult{}, mapRequirementRepositoryError(err)
		}
		staleHook := s.stale
		if staleHook == nil {
			staleHook = NoopCaseStaleHook{}
		}
		affected, err := staleHook.MarkRequirementChanged(ctx, versionID, targetID, "merged duplicate requirement", revision.Revision)
		if err != nil {
			return RequirementMergeResult{}, err
		}
		if affected == nil {
			affected = []string{}
		}
		s.publishEvent(ctx, versionID, "requirement.version_created", map[string]any{
			"requirement_id":            platformid.Format(targetID),
			"requirement_version_id":    platformid.Format(revision.ID),
			"revision":                  revision.Revision,
			"status":                    string(version.RequirementDraft),
			"affected_case_version_ids": affected,
		}, map[string]any{"requirement_id": platformid.Format(targetID)})
		s.reopenCompleted(ctx, versionID, VersionReopenTriggerRequirementReopened, "requirement merged")
	}

	for _, sourceID := range kept {
		if err := s.obsoleteRequirement(ctx, versionID, sourceID, "merged_into:"+platformid.Format(targetID), actorID, now); err != nil {
			return RequirementMergeResult{}, err
		}
		sourceRevisionID := int64(0)
		if sourceAggregate, err := s.repo.Get(ctx, versionID, sourceID); err == nil {
			if current, ok := currentVersion(sourceAggregate); ok {
				sourceRevisionID = current.ID
			}
		}
		relation := version.Relation{
			ID:                s.ids.Next(),
			VersionID:         versionID,
			FromRequirementID: targetID,
			ToRequirementID:   sourceID,
			Kind:              version.RelationOverlaps,
			Description:       "merged duplicate into " + platformid.Format(targetID),
			CreatedBy:         optionalActor(actorID),
			CreatedAt:         now,
		}
		if sourceRevisionID > 0 {
			relation.SourceRevisionID = &sourceRevisionID
		}
		if err := s.repo.CreateRelation(ctx, relation); err != nil && !errors.Is(err, version.ErrRelationDuplicate) {
			return RequirementMergeResult{}, mapRequirementRepositoryError(err)
		}
	}
	s.publishEvent(ctx, versionID, "requirement.merged", map[string]any{
		"requirement_id":         platformid.Format(targetID),
		"source_requirement_ids": formatStageIDs(kept),
	}, map[string]any{"requirement_id": platformid.Format(targetID)})
	s.recordAudit(ctx, actorID, "requirement.merged", "requirement", platformid.Format(targetID), map[string]any{
		"source_requirement_ids": formatStageIDs(kept),
	})
	detail, err := s.Get(ctx, versionID, targetID)
	if err != nil {
		return RequirementMergeResult{}, err
	}
	return RequirementMergeResult{Requirement: detail, MergedIDs: kept}, nil
}

// obsoleteRequirement retires one requirement without adding a revision and
// publishes/audits the change.
func (s *RequirementService) obsoleteRequirement(ctx context.Context, versionID, requirementID int64, reason string, actorID int64, now time.Time) error {
	if err := s.repo.SetStatus(ctx, versionID, requirementID, version.RequirementObsolete, now); err != nil {
		return mapRequirementRepositoryError(err)
	}
	requirementIDString := platformid.Format(requirementID)
	s.publishEvent(ctx, versionID, "requirement.obsoleted", map[string]any{
		"requirement_id": requirementIDString,
		"status":         string(version.RequirementObsolete),
		"reason":         reason,
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.obsoleted", "requirement", requirementIDString, map[string]any{
		"reason": reason,
	})
	return nil
}

// normalizeRequirementText lowercases and collapses whitespace so a duplicated
// draft with cosmetic differences still matches.
func normalizeRequirementText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

// requirementContentKey renders the dedupe identity of one revision: the user
// story, the rules, the system under test and every acceptance criterion,
// normalized and joined. It replaced the old description+acceptance-text pair
// when the flat description was split into user_story/rules/sut_description
// (design v1.3.1 §5.1). Criteria are taken in id order so two revisions holding
// the same criteria in a different order still collide.
//
// An empty result means the revision carries no comparable content, and the
// caller then never treats it as a duplicate of another empty one.
func requirementContentKey(revision version.RequirementVersion) string {
	parts := make([]string, 0, 5+len(revision.AcceptanceCriteria))
	parts = append(parts,
		normalizeRequirementText(revision.UserStory.AsA),
		normalizeRequirementText(revision.UserStory.IWant),
		normalizeRequirementText(revision.UserStory.SoThat),
		normalizeRequirementText(revision.Rules),
		normalizeRequirementText(revision.SutDescription),
	)
	for _, criterion := range version.SortAcceptanceCriteria(revision.AcceptanceCriteria) {
		parts = append(parts, normalizeRequirementText(criterion.Given)+">"+
			normalizeRequirementText(criterion.When)+">"+
			normalizeRequirementText(criterion.Then))
	}
	joined := strings.Join(parts, "|")
	if strings.Trim(joined, "|") == "" {
		return ""
	}
	return joined
}

// appendUniqueText appends extra unless it is empty or already contained.
func appendUniqueText(base, extra string) string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return base
	}
	if strings.Contains(base, extra) {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return extra
	}
	return strings.TrimRight(base, "\n") + "\n\n" + extra
}

// appendUniqueStrings unions target sets preserving order.
func appendUniqueStrings(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	merged := make([]string, 0, len(base)+len(extra))
	for _, value := range append(append([]string{}, base...), extra...) {
		if _, duplicate := seen[value]; duplicate || strings.TrimSpace(value) == "" {
			continue
		}
		seen[value] = struct{}{}
		merged = append(merged, value)
	}
	return merged
}

// priorityRank orders priorities with P0 highest.
func priorityRank(priority string) int {
	switch priority {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	default:
		return 4
	}
}

// Rollback copies the confirmed revision into a new draft revision, calls the
// P2 stale hook and publishes requirement.rolled_back plus
// requirement.version_created.
func (s *RequirementService) Rollback(ctx context.Context, versionID, requirementID int64, reason string, proposalMessageID *int64, actorID int64) (RequirementDetail, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return RequirementDetail{}, err
	}
	reason = strings.TrimSpace(reason)
	if err := validationStringMax("reason", reason, version.RequirementReasonMax); err != nil {
		return RequirementDetail{}, err
	}
	if proposalMessageID != nil && *proposalMessageID <= 0 {
		return RequirementDetail{}, platform.Invalid("proposal_message_id", "must be a positive decimal ID")
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}
	requirement := aggregate.Requirement
	if requirement.Status != version.RequirementClarified {
		return RequirementDetail{}, platform.StateConflict("only a clarified requirement can be rolled back")
	}
	current, ok := currentVersion(aggregate)
	if !ok {
		return RequirementDetail{}, platform.StateConflict("requirement has no current revision")
	}

	now := s.clock.Now()
	revisionID := s.ids.Next()
	// A rollback copies the confirmed content verbatim, acceptance-criterion ids
	// included: the criteria are unchanged, so AssignACIDs would return the same
	// ids anyway, and copying makes the "same revision, new number" intent
	// explicit instead of relying on the inheritance rule.
	revision := version.RequirementVersion{
		ID:                 revisionID,
		RequirementID:      requirementID,
		Revision:           current.Revision + 1,
		Title:              current.Title,
		UserStory:          current.UserStory,
		Rules:              current.Rules,
		AcceptanceCriteria: append([]version.AcceptanceCriterion{}, current.AcceptanceCriteria...),
		Priority:           current.Priority,
		Constraints:        current.Constraints,
		SutDescription:     current.SutDescription,
		ScopeOut:           current.ScopeOut,
		AccountRolesHint:   append([]string{}, current.AccountRolesHint...),
		CreatedBy:          optionalActor(actorID),
		CreatedByRunID:     agentRunID(ctx),
		CreatedAt:          now,
	}
	contentHash, err := revision.ComputeContentHash()
	if err != nil {
		return RequirementDetail{}, err
	}
	revision.ContentHash = contentHash

	requirement.Status = version.RequirementDraft
	requirement.CurrentVersionID = &revisionID
	requirement.UpdatedAt = now
	if err := s.repo.AddRevision(ctx, requirement, revision); err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}

	staleHook := s.stale
	if staleHook == nil {
		staleHook = NoopCaseStaleHook{}
	}
	affected, err := staleHook.MarkRequirementChanged(ctx, versionID, requirementID, reason, revision.Revision)
	if err != nil {
		return RequirementDetail{}, err
	}
	if affected == nil {
		affected = []string{}
	}

	requirementIDString := platformid.Format(requirementID)
	fromRevisionIDString := platformid.Format(current.ID)
	revisionIDString := platformid.Format(revisionID)
	s.publishEvent(ctx, versionID, "requirement.rolled_back", map[string]any{
		"requirement_id":            requirementIDString,
		"from_version_id":           fromRevisionIDString,
		"new_version_id":            revisionIDString,
		"reason":                    reason,
		"affected_case_version_ids": affected,
	}, map[string]any{"requirement_id": requirementIDString})
	s.publishEvent(ctx, versionID, "requirement.version_created", map[string]any{
		"requirement_id":            requirementIDString,
		"requirement_version_id":    revisionIDString,
		"revision":                  revision.Revision,
		"status":                    string(version.RequirementDraft),
		"affected_case_version_ids": affected,
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.rolled_back", "requirement", requirementIDString, map[string]any{
		"from_version_id":           fromRevisionIDString,
		"new_version_id":            revisionIDString,
		"reason":                    reason,
		"affected_case_version_ids": affected,
	})
	if proposalMessageID != nil {
		// proposal message is metadata only; AI never changes state directly.
		s.recordAudit(ctx, actorID, "requirement.rollback_proposal_used", "requirement", requirementIDString, map[string]any{
			"proposal_message_id": platformid.Format(*proposalMessageID),
		})
	}
	s.reopenCompleted(ctx, versionID, VersionReopenTriggerRequirementReopened, reason)
	return s.Get(ctx, versionID, requirementID)
}

// CreateRelation persists an AI-proposed relation after validating scope.
func (s *RequirementService) CreateRelation(ctx context.Context, versionID int64, in RelationInput, actorID int64) (RelationView, error) {
	if versionID <= 0 {
		return RelationView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if in.FromRequirementID <= 0 {
		return RelationView{}, platform.Invalid("from_requirement_id", "must be a positive decimal ID")
	}
	if in.ToRequirementID <= 0 {
		return RelationView{}, platform.Invalid("to_requirement_id", "must be a positive decimal ID")
	}
	if in.FromRequirementID == in.ToRequirementID {
		return RelationView{}, platform.Invalid("to_requirement_id", "a requirement cannot relate to itself")
	}
	if !version.ValidRelationKind(in.Kind) {
		return RelationView{}, platform.Invalid("kind", "must be one of depends_on, conflicts_with, overlaps, shares_scope")
	}
	description := strings.TrimSpace(in.Description)
	if err := validationStringMax("description", description, version.RequirementRelationDescriptionMax); err != nil {
		return RelationView{}, err
	}
	if in.SourceRevisionID != nil && *in.SourceRevisionID <= 0 {
		return RelationView{}, platform.Invalid("source_revision_id", "must be a positive decimal ID")
	}
	if _, err := s.repo.Get(ctx, versionID, in.FromRequirementID); err != nil {
		return RelationView{}, mapRequirementRepositoryError(err)
	}
	if _, err := s.repo.Get(ctx, versionID, in.ToRequirementID); err != nil {
		return RelationView{}, mapRequirementRepositoryError(err)
	}

	now := s.clock.Now()
	relation := version.Relation{
		ID:                s.ids.Next(),
		VersionID:         versionID,
		FromRequirementID: in.FromRequirementID,
		ToRequirementID:   in.ToRequirementID,
		Kind:              in.Kind,
		Description:       description,
		SourceRevisionID:  in.SourceRevisionID,
		CreatedBy:         optionalActor(actorID),
		CreatedByRunID:    agentRunID(ctx),
		CreatedAt:         now,
	}
	if err := s.repo.CreateRelation(ctx, relation); err != nil {
		return RelationView{}, mapRequirementRepositoryError(err)
	}

	fromString := platformid.Format(in.FromRequirementID)
	toString := platformid.Format(in.ToRequirementID)
	s.publishEvent(ctx, versionID, "requirement.relation_created", map[string]any{
		"from_requirement_id": fromString,
		"to_requirement_id":   toString,
		"kind":                in.Kind,
	}, map[string]any{"requirement_id": fromString})
	s.recordAudit(ctx, actorID, "requirement.relation_created", "requirement_relation", platformid.Format(relation.ID), map[string]any{
		"from_requirement_id": fromString,
		"to_requirement_id":   toString,
		"kind":                in.Kind,
	})
	return toRelationView(relation), nil
}

// ListRelations returns the relations touching one requirement.
func (s *RequirementService) ListRelations(ctx context.Context, versionID, requirementID int64) ([]RelationView, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return nil, err
	}
	if _, err := s.repo.Get(ctx, versionID, requirementID); err != nil {
		return nil, mapRequirementRepositoryError(err)
	}
	relations, err := s.repo.ListRelations(ctx, versionID, requirementID)
	if err != nil {
		return nil, mapRequirementRepositoryError(err)
	}
	views := make([]RelationView, 0, len(relations))
	for _, relation := range relations {
		views = append(views, toRelationView(relation))
	}
	return views, nil
}

// ---------- helpers ----------

// normalizeDraft canonicalizes one incoming draft into the revision that gets
// persisted, validated and hashed. prev is the previous revision's acceptance
// criteria -- the inheritance source for the AC ids -- and is nil for a brand
// new requirement.
//
// Everything the ContentHash depends on is normalized here, so a revision read
// back from storage always hashes to the value stored with it: whitespace in
// the title, the story, the rules, the criteria and the role hints is collapsed
// before validation, not after.
func normalizeDraft(in RequirementDraft, prev []version.AcceptanceCriterion) (version.RequirementVersion, error) {
	revision := version.RequirementVersion{
		Title: strings.TrimSpace(in.Title),
		UserStory: version.UserStory{
			AsA:    in.UserStory.AsA,
			IWant:  in.UserStory.IWant,
			SoThat: in.UserStory.SoThat,
		}.Normalize(),
		Rules:          strings.TrimSpace(in.Rules),
		Priority:       in.Priority,
		Constraints:    in.Constraints,
		SutDescription: strings.TrimSpace(in.SutDescription),
		ScopeOut:       strings.TrimSpace(in.ScopeOut),
	}
	priority, err := version.NormalizePriority(revision.Priority)
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.Priority = priority
	constraints, err := version.NormalizeConstraints(revision.Constraints)
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.Constraints = constraints
	revision.AccountRolesHint = version.NormalizeAccountRolesHint(in.AccountRolesHint)
	// Normalize, then de-duplicate, then assign ids -- in that order. Ids come
	// last so the inheritance match runs on the same canonical text the hash
	// will use, and de-duplication comes before that so one statement can never
	// occupy two ids (which would churn on every revision). This is the single
	// write-path rule; Merge applies the same content de-duplication when it
	// unions two revisions' criteria.
	revision.AcceptanceCriteria = version.AssignACIDs(prev,
		version.DedupeAcceptanceCriteria(
			version.NormalizeAcceptanceCriteria(toAcceptanceCriteria(in.AcceptanceCriteria))))
	if err := revision.ValidateDraft(); err != nil {
		return version.RequirementVersion{}, err
	}
	hash, err := revision.ComputeContentHash()
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.ContentHash = hash
	return revision, nil
}

func (s *RequirementService) detailFromAggregate(aggregate version.RequirementAggregate) (RequirementDetail, error) {
	requirement := aggregate.Requirement
	current, ok := currentVersion(aggregate)
	if !ok {
		return RequirementDetail{}, platform.StateConflict("requirement has no current revision")
	}
	detail := RequirementDetail{
		RequirementView: requirementViewFromRequirement(requirement, current, aggregate.Confirmations),
		CurrentVersion:  toRequirementVersionView(current),
		History:         make([]RequirementVersionView, 0, len(aggregate.Versions)),
		Confirmations:   make([]ConfirmationView, 0, len(aggregate.Confirmations)),
		Relations:       make([]RelationView, 0, len(aggregate.Relations)),
		Attachments:     make([]AttachmentView, 0, len(aggregate.Attachments)),
	}
	for _, revision := range aggregate.Versions {
		if revision.ID == current.ID {
			continue
		}
		detail.History = append(detail.History, toRequirementVersionView(revision))
	}
	sort.Slice(detail.History, func(i, j int) bool { return detail.History[i].Revision > detail.History[j].Revision })
	confirmations := append([]version.Confirmation{}, aggregate.Confirmations...)
	sort.Slice(confirmations, func(i, j int) bool { return confirmations[i].ID > confirmations[j].ID })
	for _, confirmation := range confirmations {
		detail.Confirmations = append(detail.Confirmations, toConfirmationView(confirmation))
	}
	for _, relation := range aggregate.Relations {
		detail.Relations = append(detail.Relations, toRelationView(relation))
	}
	for _, attachment := range aggregate.Attachments {
		detail.Attachments = append(detail.Attachments, toAttachmentView(attachment))
	}
	return detail, nil
}

func requirementViewFromListItem(item version.RequirementListItem) RequirementView {
	view := requirementViewFromRequirement(item.Requirement, item.Current, nil)
	if item.ConfirmedAt != nil {
		confirmedAt := *item.ConfirmedAt
		view.ConfirmedAt = &confirmedAt
	}
	return view
}

func requirementViewFromRequirement(requirement version.Requirement, current version.RequirementVersion, confirmations []version.Confirmation) RequirementView {
	view := RequirementView{
		ID:               requirement.ID,
		CurrentVersionID: current.ID,
		Revision:         current.Revision,
		Title:            current.Title,
		Status:           string(requirement.Status),
		Priority:         current.Priority,
		UpdatedAt:        requirement.UpdatedAt,
		ClosedAt:         requirement.ClosedAt,
		Waived:           requirement.Waived,
	}
	for _, confirmation := range confirmations {
		if confirmation.RequirementVersionID == current.ID {
			confirmedAt := confirmation.ConfirmedAt
			view.ConfirmedAt = &confirmedAt
			break
		}
	}
	return view
}

func currentVersion(aggregate version.RequirementAggregate) (version.RequirementVersion, bool) {
	if aggregate.Requirement.CurrentVersionID == nil {
		return version.RequirementVersion{}, false
	}
	currentID := *aggregate.Requirement.CurrentVersionID
	for _, revision := range aggregate.Versions {
		if revision.ID == currentID {
			return revision, true
		}
	}
	return version.RequirementVersion{}, false
}

func mapRequirementRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrRequirementNotFound):
		return platform.NotFound("requirement")
	case errors.Is(err, version.ErrStateConflict):
		return platform.StateConflict("requirement is not in a mutable state")
	case errors.Is(err, version.ErrRelationDuplicate):
		return platform.Conflict("requirement relation already exists")
	case errors.Is(err, version.ErrRequirementDuplicate):
		return platform.Conflict("requirement already exists for this source")
	default:
		return err
	}
}

func (s *RequirementService) markRunning(ctx context.Context, versionID int64) {
	// The reconciler is the fallback if this best-effort projection fails.
	_ = s.versions.MarkRunning(ctx, versionID)
}

func (s *RequirementService) publishEvent(ctx context.Context, versionID int64, eventType string, payload, scope map[string]any) {
	if s.events == nil {
		return
	}
	mergedScope := map[string]any{"version_id": platformid.Format(versionID)}
	for key, value := range scope {
		mergedScope[key] = value
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      eventType,
		Scope:     mergedScope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

func agentRunID(ctx context.Context) *int64 {
	run, ok := platform.AgentRunFrom(ctx)
	if !ok || run.RunID <= 0 {
		return nil
	}
	id := run.RunID
	return &id
}

func (s *RequirementService) recordAudit(ctx context.Context, actorID int64, action, targetKind, targetID string, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	if run, ok := platform.AgentRunFrom(ctx); ok {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["agent_run_id"] = platformid.Format(run.RunID)
		if run.WorkItemID > 0 {
			metadata["work_item_id"] = platformid.Format(run.WorkItemID)
		}
	}
	actor := actorID
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    &actor,
		Action:     action,
		TargetKind: targetKind,
		TargetID:   targetID,
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}

func optionalActor(actorID int64) *int64 {
	if actorID <= 0 {
		return nil
	}
	value := actorID
	return &value
}

func validateIDs(versionID, requirementID int64) error {
	if versionID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if requirementID <= 0 {
		return platform.Invalid("rid", "must be a positive decimal ID")
	}
	return nil
}

func requirementStatusFilter(raw string) (version.RequirementStatus, error) {
	status := version.RequirementStatus(strings.TrimSpace(raw))
	switch status {
	case "":
		return "", nil
	case version.RequirementDraft, version.RequirementClarified, version.RequirementClosed, version.RequirementObsolete:
		return status, nil
	default:
		return "", platform.Invalid("status", "must be one of draft, clarified, closed, obsolete")
	}
}

func dedupeRequirementIDs(raw []int64) ([]int64, error) {
	if len(raw) == 0 {
		return nil, platform.Invalid("requirement_ids", "must contain at least one requirement ID")
	}
	if len(raw) > version.MaxBatchConfirm {
		return nil, platform.Invalid("requirement_ids", fmt.Sprintf("must contain at most %d requirement IDs", version.MaxBatchConfirm))
	}
	ids := make([]int64, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for _, id := range raw {
		if id <= 0 {
			return nil, platform.Invalid("requirement_ids", "must contain positive decimal IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func withRequirementID(err error, requirementID int64) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		if apiErr.Details == nil {
			apiErr.Details = map[string]any{}
		}
		apiErr.Details["requirement_id"] = platformid.Format(requirementID)
		return apiErr
	}
	return err
}

func validationStringMax(field, value string, max int) error {
	if len([]rune(value)) > max {
		return platform.Invalid(field, fmt.Sprintf("must be at most %d characters", max))
	}
	return nil
}

package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// issueMaxEventRefs bounds the total refs of one issue event (spec §8.2:
// refs ≤ 20, case and evidence refs share the budget).
const issueMaxEventRefs = 20

// IssueFilter is the issue list filter of GET /versions/{id}/issues (spec §7.3).
// Empty status/severity/category match everything; a nil link matches every
// issue; Cursor is the exclusive id of the last item of the previous page.
type IssueFilter struct {
	Status     string
	Severity   string
	Category   string
	BatchID    *int64
	CaseRunID  *int64
	CaseNodeID *int64
	Query      string
	Cursor     int64
	Limit      int
}

// CreateIssueCommand is the human-issue body of POST /versions/{id}/issues
// (spec §7.3). Source is always human, the dedupe key is null, the category
// defaults to defect and the confidence defaults to medium.
type CreateIssueCommand struct {
	Title         string
	Description   string
	ReproSteps    string
	Expected      string
	Actual        string
	Severity      string
	Category      string
	Confidence    string
	CaseRunID     *int64
	CaseNodeID    *int64
	RequirementID *int64
}

// IssueService is the frozen issue use-case surface (plan Shared Interfaces).
// The delivery HTTP handlers and the analysis materialization consume it; the
// names, parameters and return semantics must not change.
type IssueService interface {
	CreateHuman(ctx context.Context, versionID, actor int64, cmd CreateIssueCommand) (domain.Issue, error)
	ApplyDrafts(ctx context.Context, versionID, batchID, actor int64, drafts []domain.IssueDraft) ([]domain.Issue, error)
	Confirm(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, comment string) (domain.Issue, error)
	Dismiss(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, reason string) (domain.Issue, error)
	Get(ctx context.Context, versionID, issueID int64) (domain.Issue, error)
	List(ctx context.Context, versionID int64, f IssueFilter) ([]domain.Issue, bool, error)
}

// IssueRefReader is the issue_ref read used by the detail handler (spec §7.3's
// refs field). It is deliberately separate from IssueService: the frozen
// interface returns the issue row only.
type IssueRefReader interface {
	ListRefs(ctx context.Context, versionID, issueID int64) ([]domain.IssueRef, error)
}

// IssueManager implements IssueService and IssueRefReader. It never writes
// across contexts: issue persistence goes through IssueStore and every other
// effect goes through an optional event/audit port.
type IssueManager struct {
	deps IssueServiceDeps
	now  func() time.Time
}

// NewIssueService wires the issue use cases to their ports. IDs must be injected
// for write use cases: P7 has no per-context Snowflake fallback, because two
// fallbacks on the same node id can mint colliding ids (D29).
func NewIssueService(deps IssueServiceDeps) *IssueManager {
	now := time.Now
	if deps.Clock != nil {
		now = deps.Clock.Now
	}
	return &IssueManager{deps: deps, now: now}
}

var _ IssueService = (*IssueManager)(nil)
var _ IssueRefReader = (*IssueManager)(nil)

// CreateHuman creates a human-authored open issue (spec §5.4, §7.3): source
// human, dedupe key null, category defect and confidence medium by default. The
// target version must exist (version_not_found otherwise) and the referenced
// case run/node/requirement must belong to the version.
func (s *IssueManager) CreateHuman(ctx context.Context, versionID, actor int64, cmd CreateIssueCommand) (domain.Issue, error) {
	if err := s.ready(); err != nil {
		return domain.Issue{}, err
	}
	if versionID <= 0 {
		return domain.Issue{}, domain.NewInvalid("versionId", domain.ReasonInvalidArgument,
			"versionId must be a positive id")
	}
	if err := s.validateVersion(ctx, versionID); err != nil {
		return domain.Issue{}, err
	}
	title, err := domain.ValidateTitle(cmd.Title)
	if err != nil {
		return domain.Issue{}, err
	}
	if err := domain.ValidateSeverity(cmd.Severity); err != nil {
		return domain.Issue{}, err
	}
	category := cmd.Category
	if category == "" {
		category = domain.CategoryDefect
	}
	if err := domain.ValidateCategory(category); err != nil {
		return domain.Issue{}, err
	}
	confidence := cmd.Confidence
	if confidence == "" {
		confidence = domain.ConfidenceMedium
	}
	if err := domain.ValidateConfidence(confidence); err != nil {
		return domain.Issue{}, err
	}

	refs := issueHumanRefs(cmd)
	if err := issueValidateRefFormats(refs); err != nil {
		return domain.Issue{}, err
	}
	if err := s.validateRefs(ctx, versionID, refs); err != nil {
		return domain.Issue{}, err
	}

	id, err := s.nextID()
	if err != nil {
		return domain.Issue{}, err
	}
	now := s.now()
	issue := domain.Issue{
		ID:              id,
		VersionID:       versionID,
		CaseRunID:       issueCopyInt64(cmd.CaseRunID),
		CaseNodeID:      issueCopyInt64(cmd.CaseNodeID),
		Source:          domain.SourceHuman,
		Category:        category,
		Severity:        cmd.Severity,
		Status:          domain.StatusOpen,
		Confidence:      confidence,
		Title:           title,
		Description:     cmd.Description,
		ReproSteps:      cmd.ReproSteps,
		Expected:        cmd.Expected,
		Actual:          cmd.Actual,
		RecurrenceCount: 0,
		Revision:        1,
		CreatedBy:       actor,
		UpdatedBy:       actor,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	created, err := s.deps.Store.Create(ctx, issue, issueWithID(refs, id))
	if err != nil {
		return domain.Issue{}, err
	}
	s.publishCreated(ctx, created, refs)
	s.auditCreated(ctx, created, actor)
	return created, nil
}

// ApplyDrafts materializes AI issue drafts (spec §5.4, §6.3): the target
// version must exist, then every draft is validated before anything is written,
// and one transaction dedupes by (version_id, dedupe_key), appends the new
// occurrence refs and raises the recurrence count, or inserts a new issue. A
// draft whose incoming refs are all already linked is a replay and changes
// nothing (Lead replay ruling: identical ref set = replay, no recurrence bump
// and no issue.updated). A lost unique-key race is retried once outside the
// failed transaction and merged instead of failing (spec §6.9). Events and audit
// are published only after the commit.
func (s *IssueManager) ApplyDrafts(ctx context.Context, versionID, batchID, actor int64, drafts []domain.IssueDraft) ([]domain.Issue, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if versionID <= 0 {
		return nil, domain.NewInvalid("versionId", domain.ReasonInvalidArgument,
			"versionId must be a positive id")
	}
	if len(drafts) == 0 {
		return []domain.Issue{}, nil
	}
	if err := s.validateVersion(ctx, versionID); err != nil {
		return nil, err
	}

	prepared := make([]issuePreparedDraft, len(drafts))
	for index, draft := range drafts {
		if err := domain.ValidateIssueDraft(draft); err != nil {
			return nil, err
		}
		refs := issueDraftRefs(draft)
		if err := issueValidateRefFormats(refs); err != nil {
			return nil, err
		}
		prepared[index] = issuePreparedDraft{draft: draft, refs: refs}
	}
	// Cross-context ownership is checked before the write transaction so no row
	// is written when a reference is foreign (spec §5.4).
	for _, item := range prepared {
		if err := s.validateRefs(ctx, versionID, item.refs); err != nil {
			return nil, err
		}
	}

	now := s.now()
	outcomes, err := s.applyDraftsTx(ctx, versionID, batchID, actor, prepared, now)
	if errors.Is(err, ErrIssueDedupeConflict) {
		// One safe retry outside the failed transaction: the concurrent writer's
		// issue is committed and visible now, so the fresh transaction's
		// FindByDedupeKey merges the occurrence instead of creating it (spec
		// §6.9: a dedupe conflict appends refs/recurrence, never a 409).
		outcomes, err = s.applyDraftsTx(ctx, versionID, batchID, actor, prepared, now)
	}
	if err != nil {
		return nil, err
	}

	issues := make([]domain.Issue, 0, len(outcomes))
	for _, outcome := range outcomes {
		issues = append(issues, outcome.issue)
		switch {
		case outcome.created:
			s.publishCreated(ctx, outcome.issue, outcome.refs)
			s.auditCreated(ctx, outcome.issue, actor)
		case outcome.updated:
			s.publishUpdated(ctx, outcome.issue, outcome.refs)
		}
	}
	return issues, nil
}

// applyDraftsTx runs one dedupe/create transaction over the prepared drafts.
// Outcomes are built per attempt so a retry after a dedupe conflict starts from
// a clean result set.
func (s *IssueManager) applyDraftsTx(ctx context.Context, versionID, batchID, actor int64, prepared []issuePreparedDraft, now time.Time) ([]issueDraftOutcome, error) {
	outcomes := make([]issueDraftOutcome, 0, len(prepared))
	indexByID := make(map[int64]int, len(prepared))
	record := func(issue domain.Issue, refs []domain.IssueRef, created, updated bool) {
		if index, exists := indexByID[issue.ID]; exists {
			outcomes[index].issue = issue
			outcomes[index].refs = refs
			outcomes[index].created = outcomes[index].created || created
			outcomes[index].updated = outcomes[index].updated || updated
			return
		}
		indexByID[issue.ID] = len(outcomes)
		outcomes = append(outcomes, issueDraftOutcome{
			issue: issue, refs: refs, created: created, updated: updated,
		})
	}

	err := s.deps.Store.InTx(ctx, func(tx IssueStore) error {
		for _, item := range prepared {
			existing, found, err := s.findDraftTarget(ctx, tx, versionID, item.draft)
			if err != nil {
				return err
			}
			if found {
				existingRefs, err := tx.ListRefs(ctx, existing.ID)
				if err != nil {
					return err
				}
				addedRefs := issueMissingRefs(item.refs, existingRefs)
				if len(addedRefs) == 0 {
					// Lead replay ruling: the incoming ref set is already fully
					// linked (the same occurrence was applied before), so this is
					// a replay: no recurrence bump, revision change or event.
					record(existing, existingRefs, false, false)
					continue
				}
				// A genuine recurrence carries at least one new ref and always
				// increments recurrence_count and emits issue.updated, even when
				// the issue already reached a terminal status.
				updated := domain.MergeOccurrence(existing, item.draft.Confidence, item.draft.Actual, actor, now)
				if err := tx.AddRefs(ctx, existing.ID, issueWithID(addedRefs, existing.ID)); err != nil {
					return err
				}
				mergedRefs := domain.NormalizeIssueRefs(append(append([]domain.IssueRef(nil), existingRefs...), addedRefs...))
				stored, err := tx.Update(ctx, updated, existing.Revision)
				if err != nil {
					return err
				}
				record(stored, mergedRefs, false, true)
				continue
			}

			id, err := s.nextID()
			if err != nil {
				return err
			}
			issue := s.newAIIssue(id, versionID, batchID, actor, item.draft, now)
			stored, err := tx.Create(ctx, issue, issueWithID(item.refs, id))
			if err != nil {
				return err
			}
			record(stored, item.refs, true, false)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outcomes, nil
}

// findDraftTarget resolves the issue a draft merges into: the explicit
// mergeIntoIssueId when the triage agent supplied one, otherwise the hard
// (version_id, dedupe_key) match of spec §5.4.
func (s *IssueManager) findDraftTarget(ctx context.Context, store IssueStore, versionID int64, draft domain.IssueDraft) (domain.Issue, bool, error) {
	if draft.MergeIntoIssueID != nil {
		issue, found, err := store.Get(ctx, versionID, *draft.MergeIntoIssueID)
		if err != nil {
			return domain.Issue{}, false, err
		}
		if !found {
			return domain.Issue{}, false, domain.NewInvalid("mergeIntoIssueId", domain.ReasonIssueRefInvalid,
				fmt.Sprintf("issue %d is not in version %d", *draft.MergeIntoIssueID, versionID))
		}
		return issue, true, nil
	}
	return store.FindByDedupeKey(ctx, versionID, draft.DedupeKey)
}

// Confirm moves an open issue to confirmed under the revision CAS (spec §5.4).
// A non-open issue or a stale expectedRevision is issue_state_conflict.
func (s *IssueManager) Confirm(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, comment string) (domain.Issue, error) {
	issue, err := s.loadOpenIssueForCAS(ctx, versionID, issueID, expectedRevision)
	if err != nil {
		return domain.Issue{}, err
	}
	now := s.now()
	confirmed := issue
	confirmed.Status = domain.StatusConfirmed
	confirmed.ConfirmedBy = issueCopyInt64(&actor)
	confirmed.ConfirmedAt = issueCopyTime(&now)
	confirmed.Revision++
	confirmed.UpdatedBy = actor
	confirmed.UpdatedAt = now
	stored, err := s.deps.Store.Update(ctx, confirmed, expectedRevision)
	if err != nil {
		return domain.Issue{}, err
	}
	s.publishUpdated(ctx, stored, s.refsForEvent(ctx, stored.ID))
	// §11.2 allows the free-text reason only for dismissals; a confirmation
	// comment is not an audit field, so it is intentionally omitted.
	s.auditTransition(ctx, "delivery.issue.confirmed", stored, actor, "")
	return stored, nil
}

// Dismiss moves an open issue to dismissed under the revision CAS (spec §5.4).
// The dismissal reason is mandatory (issue_dismiss_reason_required).
func (s *IssueManager) Dismiss(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, reason string) (domain.Issue, error) {
	if err := s.ready(); err != nil {
		return domain.Issue{}, err
	}
	trimmedReason := strings.TrimSpace(reason)
	if trimmedReason == "" {
		return domain.Issue{}, domain.NewInvalid("comment", domain.ReasonIssueDismissReasonRequired,
			"dismissing an issue requires a reason")
	}
	issue, err := s.loadOpenIssueForCAS(ctx, versionID, issueID, expectedRevision)
	if err != nil {
		return domain.Issue{}, err
	}
	now := s.now()
	dismissed := issue
	dismissed.Status = domain.StatusDismissed
	dismissed.DismissedReason = trimmedReason
	dismissed.DismissedBy = issueCopyInt64(&actor)
	dismissed.DismissedAt = issueCopyTime(&now)
	dismissed.Revision++
	dismissed.UpdatedBy = actor
	dismissed.UpdatedAt = now
	stored, err := s.deps.Store.Update(ctx, dismissed, expectedRevision)
	if err != nil {
		return domain.Issue{}, err
	}
	s.publishUpdated(ctx, stored, s.refsForEvent(ctx, stored.ID))
	s.auditTransition(ctx, "delivery.issue.dismissed", stored, actor, trimmedReason)
	return stored, nil
}

// loadOpenIssueForCAS loads the issue and enforces the open state plus the
// revision CAS precondition shared by Confirm and Dismiss.
func (s *IssueManager) loadOpenIssueForCAS(ctx context.Context, versionID, issueID int64, expectedRevision int) (domain.Issue, error) {
	if err := s.ready(); err != nil {
		return domain.Issue{}, err
	}
	if versionID <= 0 {
		return domain.Issue{}, domain.NewInvalid("versionId", domain.ReasonInvalidArgument,
			"versionId must be a positive id")
	}
	issue, found, err := s.deps.Store.Get(ctx, versionID, issueID)
	if err != nil {
		return domain.Issue{}, err
	}
	if !found {
		return domain.Issue{}, domain.NewNotFound("issue", issueID)
	}
	if issue.Status != domain.StatusOpen {
		return domain.Issue{}, domain.NewStateConflict(domain.ReasonIssueStateConflict,
			fmt.Sprintf("issue %d is %s; only an open issue can transition", issueID, issue.Status))
	}
	if issue.Revision != expectedRevision {
		return domain.Issue{}, domain.NewStateConflict(domain.ReasonIssueStateConflict,
			fmt.Sprintf("issue %d revision is %d, expected %d", issueID, issue.Revision, expectedRevision))
	}
	return issue, nil
}

// Get returns one issue of the version; a missing or foreign issue is
// issue_not_found.
func (s *IssueManager) Get(ctx context.Context, versionID, issueID int64) (domain.Issue, error) {
	if err := s.ready(); err != nil {
		return domain.Issue{}, err
	}
	if versionID <= 0 {
		return domain.Issue{}, domain.NewInvalid("versionId", domain.ReasonInvalidArgument,
			"versionId must be a positive id")
	}
	issue, found, err := s.deps.Store.Get(ctx, versionID, issueID)
	if err != nil {
		return domain.Issue{}, err
	}
	if !found {
		return domain.Issue{}, domain.NewNotFound("issue", issueID)
	}
	return issue, nil
}

// List returns one page of the version's issues ordered by severity weight then
// id descending (spec §7.3).
func (s *IssueManager) List(ctx context.Context, versionID int64, filter IssueFilter) ([]domain.Issue, bool, error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	if versionID <= 0 {
		return nil, false, domain.NewInvalid("versionId", domain.ReasonInvalidArgument,
			"versionId must be a positive id")
	}
	normalized, err := issueNormalizeFilter(filter)
	if err != nil {
		return nil, false, err
	}
	return s.deps.Store.List(ctx, versionID, normalized)
}

// ListRefs returns the refs of one issue of the version, sorted by kind then id.
func (s *IssueManager) ListRefs(ctx context.Context, versionID, issueID int64) ([]domain.IssueRef, error) {
	if _, err := s.Get(ctx, versionID, issueID); err != nil {
		return nil, err
	}
	refs, err := s.deps.Store.ListRefs(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return domain.NormalizeIssueRefs(refs), nil
}

// newAIIssue builds a new open AI issue from a validated draft.
func (s *IssueManager) newAIIssue(id, versionID, batchID, actor int64, draft domain.IssueDraft, now time.Time) domain.Issue {
	title, _ := domain.ValidateTitle(draft.Title) // already validated; just trimmed
	category := draft.Category
	if category == "" {
		category = domain.CategoryDefect
	}
	return domain.Issue{
		ID:              id,
		VersionID:       versionID,
		BatchID:         issueBatchID(batchID),
		CaseRunID:       issueCopyInt64(draft.CaseRunID),
		CaseNodeID:      issueCopyInt64(draft.CaseNodeID),
		Source:          domain.SourceAI,
		Category:        category,
		Severity:        draft.Severity,
		Status:          domain.StatusOpen,
		Confidence:      draft.Confidence,
		FailureClass:    draft.FailureClass,
		DedupeKey:       draft.DedupeKey,
		Title:           title,
		Description:     draft.Description,
		ReproSteps:      draft.ReproSteps,
		Expected:        draft.Expected,
		Actual:          draft.Actual,
		RerunSuggestion: draft.RerunSuggestion,
		ExternalRef:     "",
		RecurrenceCount: 0,
		Revision:        1,
		CreatedBy:       actor,
		UpdatedBy:       actor,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// issueDraftRefs converts a draft's links to durable issue_ref rows.
func issueDraftRefs(draft domain.IssueDraft) []domain.IssueRef {
	refs := make([]domain.IssueRef, 0, 4+len(draft.EvidenceIDs)+len(draft.RequirementIDs))
	if draft.CaseRunID != nil {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindCaseRun, RefID: issueFormatID(*draft.CaseRunID)})
	}
	if draft.CaseNodeID != nil {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindCaseNode, RefID: issueFormatID(*draft.CaseNodeID)})
	}
	for _, evidenceID := range draft.EvidenceIDs {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindEvidence, RefID: issueFormatID(evidenceID)})
	}
	for _, requirementID := range draft.RequirementIDs {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindRequirement, RefID: issueFormatID(requirementID)})
	}
	return domain.NormalizeIssueRefs(refs)
}

// issueHumanRefs converts the optional human links to issue_ref rows.
func issueHumanRefs(cmd CreateIssueCommand) []domain.IssueRef {
	refs := make([]domain.IssueRef, 0, 3)
	if cmd.CaseRunID != nil {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindCaseRun, RefID: issueFormatID(*cmd.CaseRunID)})
	}
	if cmd.CaseNodeID != nil {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindCaseNode, RefID: issueFormatID(*cmd.CaseNodeID)})
	}
	if cmd.RequirementID != nil {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindRequirement, RefID: issueFormatID(*cmd.RequirementID)})
	}
	return domain.NormalizeIssueRefs(refs)
}

// issueValidateRefFormats checks the per-kind ref format before the validator
// checks ownership, so a mis-wired validator can never persist a malformed ref.
func issueValidateRefFormats(refs []domain.IssueRef) error {
	for _, ref := range refs {
		if err := domain.ValidateIssueRef(ref.RefKind, ref.RefID); err != nil {
			return err
		}
	}
	return nil
}

// validateVersion rejects an issue write whose target version does not exist.
// The port is required: without it the check would silently disappear and an
// orphan issue could be written. A missing version is the stable
// version_not_found 404; any other lookup failure propagates.
func (s *IssueManager) validateVersion(ctx context.Context, versionID int64) error {
	if s.deps.Versions == nil {
		return errors.New("delivery: issue version reader is not configured")
	}
	exists, err := s.deps.Versions.VersionExists(ctx, versionID)
	if err != nil {
		return fmt.Errorf("delivery: validate version %d: %w", versionID, err)
	}
	if !exists {
		return domain.NewNotFound("version", versionID)
	}
	return nil
}

// validateRefs delegates cross-context ownership to the IssueRefValidator port
// (spec §3.2: delivery never reads another context's tables). A ref-less issue
// needs no validator; a ref-carrying issue fails closed when the port is not
// wired instead of silently skipping the ownership check.
func (s *IssueManager) validateRefs(ctx context.Context, versionID int64, refs []domain.IssueRef) error {
	if len(refs) == 0 {
		return nil
	}
	if s.deps.Refs == nil {
		return errors.New("delivery: issue ref validator is not configured")
	}
	return s.deps.Refs.ValidateRefs(ctx, versionID, refs)
}

// issueMissingRefs returns the incoming refs whose (kind, id) is not linked yet,
// sorted deterministically.
func issueMissingRefs(incoming, existing []domain.IssueRef) []domain.IssueRef {
	linked := make(map[string]struct{}, len(existing))
	for _, ref := range existing {
		linked[ref.RefKind+"\x00"+ref.RefID] = struct{}{}
	}
	missing := make([]domain.IssueRef, 0, len(incoming))
	for _, ref := range incoming {
		if _, exists := linked[ref.RefKind+"\x00"+ref.RefID]; exists {
			continue
		}
		missing = append(missing, ref)
	}
	return domain.NormalizeIssueRefs(missing)
}

// issueWithID stamps the persisted issue id onto the refs of a new issue.
func issueWithID(refs []domain.IssueRef, issueID int64) []domain.IssueRef {
	stamped := make([]domain.IssueRef, len(refs))
	for index, ref := range refs {
		ref.IssueID = issueID
		stamped[index] = ref
	}
	return stamped
}

// issueNormalizeFilter validates and bounds an issue list filter (spec §7.3).
func issueNormalizeFilter(filter IssueFilter) (IssueFilter, error) {
	if filter.Status != "" {
		switch filter.Status {
		case domain.StatusOpen, domain.StatusConfirmed, domain.StatusDismissed:
		default:
			return IssueFilter{}, domain.NewInvalid("status", domain.ReasonInvalidArgument,
				fmt.Sprintf("status %q is not open, confirmed or dismissed", filter.Status))
		}
	}
	if filter.Severity != "" {
		if err := domain.ValidateSeverity(filter.Severity); err != nil {
			return IssueFilter{}, domain.NewInvalid("severity", domain.ReasonInvalidArgument,
				fmt.Sprintf("severity %q is not blocker, major, minor or info", filter.Severity))
		}
	}
	if filter.Category != "" {
		if err := domain.ValidateCategory(filter.Category); err != nil {
			return IssueFilter{}, domain.NewInvalid("category", domain.ReasonInvalidArgument,
				fmt.Sprintf("category %q is not defect, requirement_gap or other", filter.Category))
		}
	}
	if utf8.RuneCountInString(filter.Query) > domain.MaxIssueQueryRunes {
		return IssueFilter{}, domain.NewInvalid("q", domain.ReasonInvalidArgument,
			fmt.Sprintf("q must be at most %d characters", domain.MaxIssueQueryRunes))
	}
	if filter.Cursor < 0 {
		return IssueFilter{}, domain.NewInvalid("cursor", domain.ReasonInvalidArgument,
			"cursor must not be negative")
	}
	if filter.BatchID != nil && *filter.BatchID <= 0 {
		return IssueFilter{}, domain.NewInvalid("batchId", domain.ReasonInvalidArgument,
			"batchId must be a positive id")
	}
	if filter.CaseRunID != nil && *filter.CaseRunID <= 0 {
		return IssueFilter{}, domain.NewInvalid("caseRunId", domain.ReasonInvalidArgument,
			"caseRunId must be a positive id")
	}
	if filter.CaseNodeID != nil && *filter.CaseNodeID <= 0 {
		return IssueFilter{}, domain.NewInvalid("caseNodeId", domain.ReasonInvalidArgument,
			"caseNodeId must be a positive id")
	}
	switch {
	case filter.Limit <= 0:
		filter.Limit = platform.DefaultLimit
	case filter.Limit > platform.MaxLimit:
		filter.Limit = platform.MaxLimit
	}
	return filter, nil
}

// publishCreated emits issue.created with decimal-string ids and no issue body.
func (s *IssueManager) publishCreated(ctx context.Context, issue domain.Issue, refs []domain.IssueRef) {
	caseIDs, evidenceIDs := issueEventRefs(refs)
	s.publish(ctx, IssueEvent{
		Type:      "issue.created",
		VersionID: issue.VersionID,
		IssueID:   issue.ID,
		Summary:   "issue created",
		Detail: map[string]any{
			"issueId":    issueFormatID(issue.ID),
			"severity":   issue.Severity,
			"status":     issue.Status,
			"source":     issue.Source,
			"confidence": issue.Confidence,
		},
		RefCaseIDs:     caseIDs,
		RefEvidenceIDs: evidenceIDs,
	})
}

// publishUpdated emits issue.updated for a recurrence or a state transition.
func (s *IssueManager) publishUpdated(ctx context.Context, issue domain.Issue, refs []domain.IssueRef) {
	caseIDs, evidenceIDs := issueEventRefs(refs)
	s.publish(ctx, IssueEvent{
		Type:      "issue.updated",
		VersionID: issue.VersionID,
		IssueID:   issue.ID,
		Summary:   "issue updated",
		Detail: map[string]any{
			"issueId":         issueFormatID(issue.ID),
			"status":          issue.Status,
			"revision":        issue.Revision,
			"recurrenceCount": issue.RecurrenceCount,
		},
		RefCaseIDs:     caseIDs,
		RefEvidenceIDs: evidenceIDs,
	})
}

// refsForEvent loads the refs of one issue for an event envelope. The read is
// enrichment only: a failure yields no refs and never blocks the transition.
func (s *IssueManager) refsForEvent(ctx context.Context, issueID int64) []domain.IssueRef {
	refs, err := s.deps.Store.ListRefs(ctx, issueID)
	if err != nil {
		slog.WarnContext(ctx, "delivery issue refs read for event failed",
			"issue_id", issueID, "error", err)
		return nil
	}
	return domain.NormalizeIssueRefs(refs)
}

// auditCreated records delivery.issue.created with the spec §11.2 fields only.
func (s *IssueManager) auditCreated(ctx context.Context, issue domain.Issue, actor int64) {
	s.audit(ctx, IssueAuditEvent{
		Action:     "delivery.issue.created",
		ActorID:    actor,
		TargetType: "issue",
		TargetID:   issueFormatID(issue.ID),
		Metadata: map[string]any{
			"version_id": issue.VersionID,
			"source":     issue.Source,
			"severity":   issue.Severity,
			"category":   issue.Category,
		},
	})
}

// auditTransition records delivery.issue.confirmed/dismissed. The reason is the
// only free text allowed by spec §11.2.
func (s *IssueManager) auditTransition(ctx context.Context, action string, issue domain.Issue, actor int64, reason string) {
	metadata := map[string]any{}
	if strings.TrimSpace(reason) != "" {
		metadata["reason"] = reason
	}
	s.audit(ctx, IssueAuditEvent{
		Action:     action,
		ActorID:    actor,
		TargetType: "issue",
		TargetID:   issueFormatID(issue.ID),
		Metadata:   metadata,
	})
}

// publish emits one issue event after the commit. A publication failure is
// logged and swallowed (spec §8.2).
func (s *IssueManager) publish(ctx context.Context, event IssueEvent) {
	if s.deps.Events == nil {
		return
	}
	if err := s.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "delivery issue event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"issue_id", event.IssueID,
			"error", err,
		)
	}
}

// audit records one issue audit entry. A failure is logged and never returned.
func (s *IssueManager) audit(ctx context.Context, event IssueAuditEvent) {
	if s.deps.Audit == nil {
		return
	}
	if err := s.deps.Audit.Record(ctx, event); err != nil {
		slog.WarnContext(ctx, "delivery issue audit failed",
			"action", event.Action,
			"issue_id", event.TargetID,
			"error", err,
		)
	}
}

// ready rejects a mis-wired manager before it touches the store.
func (s *IssueManager) ready() error {
	if s == nil || s.deps.Store == nil {
		return errors.New("delivery: issue store is not configured")
	}
	return nil
}

// nextID mints one application-generated Snowflake id (D29).
func (s *IssueManager) nextID() (int64, error) {
	if s.deps.IDs == nil {
		return 0, errors.New("delivery: id generator is not configured")
	}
	id := s.deps.IDs.Next()
	if id <= 0 {
		return 0, fmt.Errorf("delivery: id generator returned a non-positive id %d", id)
	}
	return id, nil
}

// issueFormatID renders an id for an event detail (D29/§8.2).
func issueFormatID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// issueEventRefs extracts the case and evidence targets of an event envelope.
// The two slices share the total refs bound of spec §8.2 (refs ≤ 20); refs are
// expected in normalized order so the truncated set is deterministic.
func issueEventRefs(refs []domain.IssueRef) (caseIDs, evidenceIDs []int64) {
	total := 0
	for _, ref := range refs {
		if total == issueMaxEventRefs {
			break
		}
		id, err := strconv.ParseInt(ref.RefID, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		switch ref.RefKind {
		case domain.RefKindCaseNode:
			caseIDs = append(caseIDs, id)
		case domain.RefKindEvidence:
			evidenceIDs = append(evidenceIDs, id)
		default:
			continue
		}
		total++
	}
	return caseIDs, evidenceIDs
}

// issueBatchID renders an optional ApplyDrafts batch id.
func issueBatchID(batchID int64) *int64 {
	if batchID <= 0 {
		return nil
	}
	return &batchID
}

// issueCopyInt64 copies an optional id so callers cannot mutate stored state.
func issueCopyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// issueCopyTime copies an optional timestamp.
func issueCopyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// issuePreparedDraft is one validated draft and its normalized refs.
type issuePreparedDraft struct {
	draft domain.IssueDraft
	refs  []domain.IssueRef
}

// issueDraftOutcome is the committed result of one draft, keyed by issue id so a
// merge of a just-created issue is published once with its final state.
type issueDraftOutcome struct {
	issue   domain.Issue
	refs    []domain.IssueRef
	created bool
	updated bool
}

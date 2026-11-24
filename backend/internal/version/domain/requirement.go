package version

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

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// RequirementStatus is the lifecycle state of one requirement. P1 only writes
// draft and clarified; closed/obsolete are reserved for later phases.
type RequirementStatus string

// Requirement statuses.
const (
	RequirementDraft     RequirementStatus = "draft"
	RequirementClarified RequirementStatus = "clarified"
	RequirementClosed    RequirementStatus = "closed"
	RequirementObsolete  RequirementStatus = "obsolete"
)

// CountableRequirementStatuses is the requirement-status set the closure
// DENOMINATOR counts: draft + clarified + closed, with obsolete excluded (an
// obsolete requirement is not part of the version's promised scope).
//
// It is a DOMAIN rule, not a repository detail (T11e-5 Part A5 / H-A22⑤): the
// infrastructure query reader used to own this set, so the day the domain grew its
// own countable-status authority the two would have become competing truths. The
// values come from the status constants above; there is no second literal set.
func CountableRequirementStatuses() []RequirementStatus {
	return []RequirementStatus{RequirementDraft, RequirementClarified, RequirementClosed}
}

// Confirmation scopes.
const (
	ConfirmationScopeRequirement = "requirement"
	ConfirmationScopeBatch       = "batch"
)

// Requirement content limits (design v1.3.1 §5.1, the V1 clarification
// contract). The confirmation gate mirrors the OpenAPI Create and Update
// request schema: every field marked required there is required here too.
//
// These are the STRUCTURAL bounds only. The clarity floor
// (validation.min_description_chars, applied to sut_description) and the
// placeholder-term blacklist (validation.placeholder_terms) are the
// confirmation-gate increment owned by subproject E; neither this file nor the
// settings table defines them (I-B1).
const (
	RequirementTitleMax          = 200
	RequirementUserStoryPartMax  = 500
	RequirementRulesMax          = 20000
	RequirementSutDescriptionMax = 2000
	RequirementScopeOutMax       = 2000
	RequirementAccountRoleMax    = 64
	RequirementReasonMax         = 2000
	// RequirementRelationDescriptionMax bounds a relation's free-text
	// description. It is the authority both the relation service and the agent
	// tool boundary derive from (U1g single source, v0.2.4-C T22).
	RequirementRelationDescriptionMax = 2000
	MaxBatchConfirm                   = 200
)

// UserStory is the three-part user story of §5.1. It replaces the flat
// description column: a requirement says who needs what and why, and the free
// text that is left over (business rules, exceptions, data constraints) goes to
// RequirementVersion.Rules.
type UserStory struct {
	AsA    string `json:"as_a"`
	IWant  string `json:"i_want"`
	SoThat string `json:"so_that"`
}

// Empty reports whether no part of the story carries content.
func (s UserStory) Empty() bool {
	return strings.TrimSpace(s.AsA) == "" &&
		strings.TrimSpace(s.IWant) == "" &&
		strings.TrimSpace(s.SoThat) == ""
}

// Validate checks the three parts against §5.1: each is required and 1..500.
func (s UserStory) Validate() error {
	for _, part := range []struct{ name, value string }{
		{"as_a", s.AsA},
		{"i_want", s.IWant},
		{"so_that", s.SoThat},
	} {
		if err := validation.String("user_story."+part.name, part.value, 1, RequirementUserStoryPartMax); err != nil {
			return err
		}
	}
	return nil
}

// Normalize trims every part, so what is stored is what is hashed.
func (s UserStory) Normalize() UserStory {
	return UserStory{
		AsA:    strings.TrimSpace(s.AsA),
		IWant:  strings.TrimSpace(s.IWant),
		SoThat: strings.TrimSpace(s.SoThat),
	}
}

// DefaultPriority is used when a draft omits the priority.
const DefaultPriority = "P2"

var validPriorities = map[string]struct{}{
	"P0": {},
	"P1": {},
	"P2": {},
	"P3": {},
}

// Requirement relation kinds.
const (
	RelationDependsOn     = "depends_on"
	RelationConflictsWith = "conflicts_with"
	RelationOverlaps      = "overlaps"
	RelationSharesScope   = "shares_scope"
)

// Requirement domain errors.
var (
	ErrRequirementNotFound  = errors.New("requirement not found")
	ErrStateConflict        = errors.New("requirement state conflict")
	ErrRelationDuplicate    = errors.New("requirement relation already exists")
	ErrRequirementDuplicate = errors.New("requirement source already exists")
)

// Requirement is the logical requirement object. Its content lives in an
// immutable RequirementVersion sequence and CurrentVersionID points at the
// latest revision.
//
// v0.2.4-B: the requirement no longer carries target resource IDs. A version
// binds an environment and the requirement describes its subject in prose
// (RequirementVersion.SutDescription), so there is nothing left to select.
type Requirement struct {
	ID               int64
	VersionID        int64
	CurrentVersionID *int64
	Status           RequirementStatus
	SourceKey        string
	ClosedAt         *time.Time
	ClosedByRunID    *int64
	// Waived marks a requirement whose closure completeness was overridden by an
	// admin waiver (v0.2.4-G SC-G11). WaiverService.WaiveRequirement sets it
	// (admin-only, with a waiver fact + event + audit); the deterministic
	// closure reconciler then closes the requirement through the normal Close
	// entry with the completeness checks overridden. Reopen (requirement or
	// version) clears it so a stale waiver can never re-close the requirement.
	Waived               bool
	CreatedBy            *int64
	CreatedByRunID       *int64
	SourceConversationID *int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// RequirementVersion is one immutable requirement snapshot: the §5.1 V1
// clarification contract. AcceptanceCriteria carries server-assigned, stable
// ids (see AssignACIDs), and ContentHash is computed over the normalized
// payload described on ComputeContentHash.
type RequirementVersion struct {
	ID                 int64
	RequirementID      int64
	Revision           int
	Title              string
	UserStory          UserStory
	Rules              string
	AcceptanceCriteria []AcceptanceCriterion
	Priority           string
	Constraints        json.RawMessage
	SutDescription     string
	ScopeOut           string
	AccountRolesHint   []string
	ContentHash        string
	CreatedBy          *int64
	CreatedByRunID     *int64
	CreatedAt          time.Time
}

// Confirmation freezes one requirement revision. Scope is "requirement" for a
// single confirmation and "batch" for a batch confirmation.
type Confirmation struct {
	ID                     int64
	RequirementVersionID   int64
	Scope                  string
	BatchID                *string
	ActorID                int64
	ConfirmedAt            time.Time
	AffectedRequirementIDs []string
}

// Relation links two requirements of the same version.
type Relation struct {
	ID                int64
	VersionID         int64
	FromRequirementID int64
	ToRequirementID   int64
	Kind              string
	Description       string
	SourceRevisionID  *int64
	CreatedBy         *int64
	CreatedByRunID    *int64
	CreatedAt         time.Time
}

// RequirementCounts are the per-status aggregates returned on the version
// detail (P1 increment).
type RequirementCounts struct {
	Total     int
	Draft     int
	Clarified int
	Closed    int
	Obsolete  int
}

// RequirementListItem is one list row: the logical requirement joined with its
// current revision and confirmation time.
type RequirementListItem struct {
	Requirement Requirement
	Current     RequirementVersion
	ConfirmedAt *time.Time
}

// RequirementWithCurrent pairs a requirement with its current revision. Used
// by batch confirmation validation.
type RequirementWithCurrent struct {
	Requirement Requirement
	Current     RequirementVersion
}

// RequirementAggregate is the detail read model assembled by the repository.
type RequirementAggregate struct {
	Requirement   Requirement
	Versions      []RequirementVersion
	Confirmations []Confirmation
	Relations     []Relation
	Attachments   []Attachment
}

// RequirementRepository persists the requirement aggregate. AddRevision and
// Confirm must each write their rows in one transaction; BatchConfirm must
// validate inside the transaction and be all-or-nothing.
type RequirementRepository interface {
	Create(ctx context.Context, requirement Requirement, revision RequirementVersion) error
	Get(ctx context.Context, versionID, requirementID int64) (RequirementAggregate, error)
	// GetBySourceKey returns the single non-obsolete requirement carrying a
	// deterministic source key in one version. Implementations must report
	// ErrRequirementNotFound when no active requirement matches.
	GetBySourceKey(ctx context.Context, versionID int64, sourceKey string) (RequirementAggregate, error)
	GetMany(ctx context.Context, versionID int64, ids []int64) ([]RequirementWithCurrent, error)
	List(ctx context.Context, versionID int64, status RequirementStatus, limit int, beforeID *int64) ([]RequirementListItem, error)
	AddRevision(ctx context.Context, requirement Requirement, revision RequirementVersion) error
	Confirm(ctx context.Context, requirement Requirement, confirmation Confirmation) error
	BatchConfirm(ctx context.Context, requirements []Requirement, confirmations []Confirmation) error
	CreateRelation(ctx context.Context, relation Relation) error
	ListRelations(ctx context.Context, versionID, requirementID int64) ([]Relation, error)
	// SetStatus projects a lifecycle status change that does not add a revision
	// (duplicate-draft deletion / merge retirement). Implementations must not
	// rewrite content and must report ErrRequirementNotFound for a foreign or
	// missing requirement.
	SetStatus(ctx context.Context, versionID, requirementID int64, status RequirementStatus, updatedAt time.Time) error
}

// CanTransitionRequirement reports whether the requirement state machine allows
// a move between two statuses. P1 writes draft <-> clarified; P4 adds
// closed -> draft (reopen). The clarified -> closed path is owned by the
// closure service after its server-side predicate passes. Obsolete stays
// read-only until P6.
func CanTransitionRequirement(from, to RequirementStatus) bool {
	switch from {
	case RequirementDraft:
		return to == RequirementClarified
	case RequirementClarified:
		return to == RequirementDraft
	case RequirementClosed:
		return to == RequirementDraft
	default:
		return false
	}
}

// NormalizePriority trims and validates the priority enum, defaulting an empty
// value to P2.
func NormalizePriority(raw string) (string, error) {
	priority := strings.TrimSpace(raw)
	if priority == "" {
		return DefaultPriority, nil
	}
	if _, ok := validPriorities[priority]; !ok {
		return "", platform.Invalid("priority", "must be one of P0, P1, P2, P3")
	}
	return priority, nil
}

// ValidRelationKind reports whether kind is a P1 relation kind.
func ValidRelationKind(kind string) bool {
	switch kind {
	case RelationDependsOn, RelationConflictsWith, RelationOverlaps, RelationSharesScope:
		return true
	default:
		return false
	}
}

// ValidateDraft checks one requirement revision against the §5.1 structural
// contract: presence, type and length of every field.
//
// It stays LAX on purpose (I-B1). What it does NOT check is the completeness
// and clarity increment -- the placeholder-term blacklist and the
// minimum-description-chars floor -- because iterative clarification must not be
// locked out of drafting. Those rules and their settings keys belong to the
// confirmation gate owned by subproject E.
func (v RequirementVersion) ValidateDraft() error {
	if err := validation.String("title", v.Title, 1, RequirementTitleMax); err != nil {
		return err
	}
	if err := v.UserStory.Validate(); err != nil {
		return err
	}
	if err := validation.String("rules", v.Rules, 0, RequirementRulesMax); err != nil {
		return err
	}
	if err := ValidateAcceptanceCriteria(v.AcceptanceCriteria); err != nil {
		return err
	}
	if _, err := NormalizePriority(v.Priority); err != nil {
		return err
	}
	if _, err := NormalizeConstraints(v.Constraints); err != nil {
		return err
	}
	if err := validation.String("sut_description", v.SutDescription, 1, RequirementSutDescriptionMax); err != nil {
		return err
	}
	if err := validation.String("scope_out", v.ScopeOut, 0, RequirementScopeOutMax); err != nil {
		return err
	}
	if len(v.AccountRolesHint) > 0 {
		if err := validation.StringList("account_roles_hint", v.AccountRolesHint, 0); err != nil {
			return err
		}
		for index, role := range v.AccountRolesHint {
			field := "account_roles_hint[" + strconv.Itoa(index) + "]"
			if err := validation.String(field, role, 1, RequirementAccountRoleMax); err != nil {
				return err
			}
		}
	}
	return nil
}

// NormalizePlaceholderTerm canonicalizes a value for whole-value placeholder match.
func NormalizePlaceholderTerm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// IsPlaceholder reports whether the whole trimmed+lowercased value equals a term.
func IsPlaceholder(value string, terms []string) bool {
	n := NormalizePlaceholderTerm(value)
	for _, term := range terms {
		if n == NormalizePlaceholderTerm(term) {
			return true
		}
	}
	return false
}

// ValidateConfirmed checks that a revision may be frozen: the §5.1 structural
// draft contract (ValidateDraft) plus the completeness/clarity increment owned
// by subproject E (I-B1) -- a whole-value placeholder-term blacklist on title,
// the three user-story parts, every acceptance criterion's given/when/then and
// sut_description, and a minimum-length floor on sut_description.
//
// terms and minDescChars are supplied by the caller (the application layer
// reads validation.placeholder_terms / validation.min_description_chars from
// settings); this layer stays pure and does not import the settings context.
// A nil terms slice and minDescChars == 0 apply no increment, so
// ValidateConfirmed(nil, 0) agrees with ValidateDraft field for field.
func (v RequirementVersion) ValidateConfirmed(terms []string, minDescChars int) error {
	if err := v.ValidateDraft(); err != nil {
		return err
	}
	fields := []struct{ name, value string }{
		{"title", v.Title},
		{"user_story.as_a", v.UserStory.AsA},
		{"user_story.i_want", v.UserStory.IWant},
		{"user_story.so_that", v.UserStory.SoThat},
		{"sut_description", v.SutDescription},
	}
	for _, f := range fields {
		if IsPlaceholder(f.value, terms) {
			return platform.Invalid(f.name, "must not be a placeholder term")
		}
	}
	for i, ac := range v.AcceptanceCriteria {
		for _, part := range []struct{ name, value string }{{"given", ac.Given}, {"when", ac.When}, {"then", ac.Then}} {
			field := fmt.Sprintf("acceptance_criteria[%d].%s", i, part.name)
			if IsPlaceholder(part.value, terms) {
				return platform.Invalid(field, "must not be a placeholder term")
			}
		}
	}
	if len([]rune(strings.TrimSpace(v.SutDescription))) < minDescChars {
		return platform.Invalid("sut_description", fmt.Sprintf("must be at least %d characters", minDescChars))
	}
	return nil
}

// ComputeContentHash returns the sha256 hex of the normalized requirement
// content (design v1.3.1 §3.6, extended per constitution 四.6).
//
// The payload field order is load-bearing -- it is the canonical form every
// caller hashes, including the extraction pipeline's extractionRevision, which
// must not grow a second implementation:
//
//	title (trimmed), user_story{as_a,i_want,so_that} (each trimmed),
//	rules (trimmed), acceptance_criteria (sorted by id, each given/when/then
//	normalized), priority, constraints (canonical JSON object),
//	sut_description (trimmed), scope_out (trimmed),
//	account_roles_hint (sorted unique, nil == [])
//
// Every text field is normalized INSIDE this function, so the hash never depends
// on the caller having trimmed it first: a third caller that passes untrimmed
// title/story/rules/sut_description gets the same hash as the write path
// (normalizeDraft) and the extraction pipeline (extractionRevision), which both
// trim before storing. This keeps the content address single-source (constitution
// 四.6/五.1) no matter how many callers hash a revision.
//
// The first seven are exactly the §3.6 list, in that order; scope_out and
// account_roles_hint are appended after them, so this is a pure extension of
// that order rather than a rearrangement of it. Constitution 四.6 requires every
// input a conclusion depends on to be inside the fingerprint, and both qualify:
// the §5.3 judge rubric reads scope_out for its over-coverage check, and
// account_roles_hint feeds execution account resolution. Leaving either out
// would let it change without moving the hash, so confirmed cases would not go
// stale and the judge/executor would act on content the freeze never captured.
//
// Compared with the pre-v0.2.4 payload it drops description (split into
// user_story + rules) and the three target arrays, and adds the five fields
// above. Two consequences are intended: reordering acceptance criteria does not
// change the hash, because the array is sorted by id, and reordering
// account_roles_hint does not change it either, because that list is sorted; and
// every hash value computed by an earlier release is invalid, so GD-05 discards
// old data with no backfill.
func (v RequirementVersion) ComputeContentHash() (string, error) {
	constraints, err := NormalizeConstraints(v.Constraints)
	if err != nil {
		return "", err
	}
	priority, err := NormalizePriority(v.Priority)
	if err != nil {
		return "", err
	}
	criteria := acceptanceCriteriaHashPayload(v.AcceptanceCriteria)
	normalizedStory := v.UserStory.Normalize()
	payload := struct {
		Title              string                    `json:"title"`
		UserStory          userStoryHash             `json:"user_story"`
		Rules              string                    `json:"rules"`
		AcceptanceCriteria []acceptanceCriterionHash `json:"acceptance_criteria"`
		Priority           string                    `json:"priority"`
		Constraints        json.RawMessage           `json:"constraints"`
		SutDescription     string                    `json:"sut_description"`
		ScopeOut           string                    `json:"scope_out"`
		AccountRolesHint   []string                  `json:"account_roles_hint"`
	}{
		Title: strings.TrimSpace(v.Title),
		UserStory: userStoryHash{
			AsA:    normalizedStory.AsA,
			IWant:  normalizedStory.IWant,
			SoThat: normalizedStory.SoThat,
		},
		Rules:              strings.TrimSpace(v.Rules),
		AcceptanceCriteria: criteria,
		Priority:           priority,
		Constraints:        constraints,
		SutDescription:     strings.TrimSpace(v.SutDescription),
		ScopeOut:           strings.TrimSpace(v.ScopeOut),
		AccountRolesHint:   sortedUniqueRoles(v.AccountRolesHint),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// sortedUniqueRoles canonicalizes the account-role hints for the ContentHash
// payload: trimmed, blanks dropped, de-duplicated and sorted, with a nil input
// yielding an empty (never null) array. Sorting is what makes the order a caller
// happens to supply irrelevant to the hash, and the empty-array convention is
// the one the payload already uses for its other arrays, so nil and [] hash
// identically.
//
// This is deliberately separate from NormalizeAccountRolesHint, which is the
// write-side normalizer and preserves first-occurrence order because that order
// is what a reader sees back.
func sortedUniqueRoles(roles []string) []string {
	unique := make([]string, 0, len(roles))
	seen := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		trimmed := strings.TrimSpace(role)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}
	sort.Strings(unique)
	return unique
}

// userStoryHash and acceptanceCriterionHash are the wire shapes of the
// ContentHash payload. They exist so the JSON key order of the hashed document
// is stated once, next to the hash, and cannot drift with the domain structs.
type userStoryHash struct {
	AsA    string `json:"as_a"`
	IWant  string `json:"i_want"`
	SoThat string `json:"so_that"`
}

type acceptanceCriterionHash struct {
	ID    string `json:"id"`
	Given string `json:"given"`
	When  string `json:"when"`
	Then  string `json:"then"`
}

// acceptanceCriteriaHashPayload is the SINGLE canonical AC normalization shared
// by ComputeContentHash and HashAcceptanceCriteria: criteria sorted by id
// (SortAcceptanceCriteria), each given/when/then whitespace-normalized
// (NormalizeACText). Both hashes derive from this one helper so the revision
// content hash and the report AC hash can never disagree about AC content
// (constitution art.5.1).
func acceptanceCriteriaHashPayload(criteria []AcceptanceCriterion) []acceptanceCriterionHash {
	sorted := SortAcceptanceCriteria(criteria)
	payload := make([]acceptanceCriterionHash, 0, len(sorted))
	for _, criterion := range sorted {
		payload = append(payload, acceptanceCriterionHash{
			ID:    criterion.ID,
			Given: NormalizeACText(criterion.Given),
			When:  NormalizeACText(criterion.When),
			Then:  NormalizeACText(criterion.Then),
		})
	}
	return payload
}

// HashAcceptanceCriteria returns the sha256 hex of the canonical acceptance-
// criteria SET alone (sorted by id, each part normalized — the same
// normalization ComputeContentHash applies to the AC array). It is the report
// fingerprint's AC hash carrier (v0.2.4-G P22, SC-G5): report conclusions quote
// AC given/when/then, so an AC content change must mark dependent reports stale
// (art.4.6) even when the rest of the revision is unchanged. Reordering criteria
// does not change the hash (id-sorted); editing a part does.
func HashAcceptanceCriteria(criteria []AcceptanceCriterion) (string, error) {
	raw, err := json.Marshal(acceptanceCriteriaHashPayload(criteria))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeConstraints canonicalizes the constraints JSON: a missing value is
// the empty object and only JSON objects are accepted.
func NormalizeConstraints(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return nil, platform.Invalid("constraints", "must be a JSON object")
	}
	// json.Unmarshal rejects arrays only for map targets; a JSON null slipped
	// through above already.
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, platform.Invalid("constraints", "must be a JSON object")
	}
	return normalized, nil
}

// NormalizeAccountRolesHint trims every role, drops the empty ones and removes
// duplicates while preserving first-occurrence order.
func NormalizeAccountRolesHint(roles []string) []string {
	normalized := make([]string, 0, len(roles))
	seen := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		trimmed := strings.TrimSpace(role)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

// SortRequirementsDesc sorts requirements by ID descending (defensive helper
// for adapters that assemble aggregates).
func SortRequirementsDesc(items []Requirement) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
}

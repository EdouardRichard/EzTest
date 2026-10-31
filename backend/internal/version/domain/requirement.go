package version

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// Confirmation scopes.
const (
	ConfirmationScopeRequirement = "requirement"
	ConfirmationScopeBatch       = "batch"
)

// Requirement content limits. The confirmation gate mirrors the OpenAPI Create
// and Update request schema: every content field is required.
const (
	RequirementTitleMax       = 200
	RequirementDescriptionMax = 20000
	RequirementAcceptanceMax  = 20000
	RequirementReasonMax      = 2000
	MaxBatchConfirm           = 200
)

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
	ErrRequirementNotFound = errors.New("requirement not found")
	ErrStateConflict       = errors.New("requirement state conflict")
	ErrRelationDuplicate   = errors.New("requirement relation already exists")
)

// Requirement is the logical requirement object. Its content lives in an
// immutable RequirementVersion sequence and CurrentVersionID points at the
// latest revision.
type Requirement struct {
	ID                   int64
	VersionID            int64
	CurrentVersionID     *int64
	Status               RequirementStatus
	TargetVersionSUTIDs  []string
	ClosedAt             *time.Time
	ClosedByRunID        *int64
	CreatedBy            *int64
	CreatedByRunID       *int64
	SourceConversationID *int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// RequirementVersion is one immutable requirement snapshot.
type RequirementVersion struct {
	ID                 int64
	RequirementID      int64
	Revision           int
	Title              string
	Description        string
	AcceptanceCriteria string
	Priority           string
	Constraints        json.RawMessage
	TargetVersionSUTs  []string
	TargetPages        []string
	TargetAPIs         []string
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
	GetMany(ctx context.Context, versionID int64, ids []int64) ([]RequirementWithCurrent, error)
	List(ctx context.Context, versionID int64, status RequirementStatus, limit int, beforeID *int64) ([]RequirementListItem, error)
	AddRevision(ctx context.Context, requirement Requirement, revision RequirementVersion) error
	Confirm(ctx context.Context, requirement Requirement, confirmation Confirmation) error
	BatchConfirm(ctx context.Context, requirements []Requirement, confirmations []Confirmation) error
	CreateRelation(ctx context.Context, relation Relation) error
	ListRelations(ctx context.Context, versionID, requirementID int64) ([]Relation, error)
}

// CanTransitionRequirement reports whether P1 may move a requirement between
// the two statuses. P1 only confirms (draft -> clarified) and re-opens
// (clarified -> draft); closed/obsolete are reserved.
func CanTransitionRequirement(from, to RequirementStatus) bool {
	switch from {
	case RequirementDraft:
		return to == RequirementClarified
	case RequirementClarified:
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

// ValidateDraft checks one requirement revision against the Create/Update
// contract. Targets are optional but must be decimal IDs.
func (v RequirementVersion) ValidateDraft() error {
	if err := validation.String("title", v.Title, 1, RequirementTitleMax); err != nil {
		return err
	}
	if err := validation.String("description", v.Description, 1, RequirementDescriptionMax); err != nil {
		return err
	}
	if err := validation.String("acceptance_criteria", v.AcceptanceCriteria, 1, RequirementAcceptanceMax); err != nil {
		return err
	}
	if _, err := NormalizePriority(v.Priority); err != nil {
		return err
	}
	if _, err := NormalizeConstraints(v.Constraints); err != nil {
		return err
	}
	for field, values := range map[string][]string{
		"target_version_suts": v.TargetVersionSUTs,
		"target_pages":        v.TargetPages,
		"target_apis":         v.TargetAPIs,
	} {
		if _, err := NormalizeTargetIDs(field, values); err != nil {
			return err
		}
	}
	return nil
}

// ValidateConfirmed checks that a revision may be frozen. The content contract
// is identical to drafts; confirmation is the state-machine gate.
func (v RequirementVersion) ValidateConfirmed() error {
	return v.ValidateDraft()
}

// ContentHash returns the sha256 hex of the normalized requirement content.
// Target sets are deduplicated and sorted and constraints are canonicalized so
// equivalent content always produces the same hash.
func (v RequirementVersion) ComputeContentHash() (string, error) {
	constraints, err := NormalizeConstraints(v.Constraints)
	if err != nil {
		return "", err
	}
	priority, err := NormalizePriority(v.Priority)
	if err != nil {
		return "", err
	}
	payload := struct {
		Title              string          `json:"title"`
		Description        string          `json:"description"`
		AcceptanceCriteria string          `json:"acceptance_criteria"`
		Priority           string          `json:"priority"`
		Constraints        json.RawMessage `json:"constraints"`
		TargetVersionSUTs  []string        `json:"target_version_suts"`
		TargetPages        []string        `json:"target_pages"`
		TargetAPIs         []string        `json:"target_apis"`
	}{
		Title:              v.Title,
		Description:        v.Description,
		AcceptanceCriteria: v.AcceptanceCriteria,
		Priority:           priority,
		Constraints:        constraints,
		TargetVersionSUTs:  sortedUnique(v.TargetVersionSUTs),
		TargetPages:        sortedUnique(v.TargetPages),
		TargetAPIs:         sortedUnique(v.TargetAPIs),
	}
	raw, err := json.Marshal(payload)
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

// NormalizeTargetIDs validates decimal ID strings and removes duplicates while
// preserving first-occurrence order.
func NormalizeTargetIDs(field string, raw []string) ([]string, error) {
	normalized := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		trimmed := strings.TrimSpace(value)
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, platform.Invalid(field, "must contain positive decimal IDs")
		}
		canonical := strconv.FormatInt(parsed, 10)
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	return normalized, nil
}

func sortedUnique(values []string) []string {
	copied := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		copied = append(copied, value)
	}
	sort.Strings(copied)
	return copied
}

// SortRequirementsDesc sorts requirements by ID descending (defensive helper
// for adapters that assemble aggregates).
func SortRequirementsDesc(items []Requirement) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
}

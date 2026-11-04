// Package knowledge is the P7 cross-version knowledge domain: scoped knowledge
// items, their review state machine, append-only content revisions, injection
// budget estimates and usage rows. It is pure: no I/O and no dependency on
// another bounded context.
package knowledge

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Scope is the visibility scope of a knowledge item (contracts/db-schema.md
// §16, spec §4.3). global items are visible to every version; system items are
// visible only to versions bound to a SUT with the matching family.
type Scope string

// Knowledge scopes.
const (
	ScopeGlobal Scope = "global"
	ScopeSystem Scope = "system"
)

// Status is the review and verification state of a knowledge item. StatusStale
// is the 0.2.1 binding-staleness state (page/route/account changed), not the
// read-time SourceStale marker of a missing source version.
type Status string

// Knowledge review and verification statuses.
const (
	StatusDraft    Status = "draft"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusRevoked  Status = "revoked"
	StatusSuspect  Status = "suspect"
	StatusStale    Status = "stale"
)

// Physical limits of the frozen P7 contract plus the 0.2.1 binding columns
// (contracts/openapi.yaml KnowledgeItemView).
const (
	MaxTitleRunes       = 200
	MaxContentRunes     = 20000
	MaxTags             = 20
	MaxTagRunes         = 40
	MaxSystemKeyRunes   = 64
	MaxReasonRunes      = 2000
	MaxBindingKeyRunes  = 500
	MaxAccountRoleRunes = 64

	// SnippetMaxRunes caps the excerpt handed to a prompt so one long item
	// cannot dominate the injected section.
	SnippetMaxRunes = 500
)

// Valid reports whether the value is a supported scope.
func (s Scope) Valid() bool { return s == ScopeGlobal || s == ScopeSystem }

// Valid reports whether the value is a supported review/verification status.
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusApproved, StatusRejected, StatusRevoked, StatusSuspect, StatusStale:
		return true
	default:
		return false
	}
}

// CanTransition reports whether the lifecycle allows from -> to. The 0.2.1
// verification states extend the review machine of spec §4.3 / contracts
// §16.1: approved -> suspect (failed reuse) | stale (bindings changed) |
// revoked; suspect -> approved (re-verified) | stale | revoked; stale ->
// approved (re-verified) | revoked; draft -> approved|rejected;
// revoked -> approved (re-review) and rejected -> draft (edit and resubmit).
func CanTransition(from, to Status) bool {
	switch from {
	case StatusDraft:
		return to == StatusApproved || to == StatusRejected
	case StatusApproved:
		return to == StatusSuspect || to == StatusStale || to == StatusRevoked
	case StatusSuspect:
		return to == StatusApproved || to == StatusStale || to == StatusRevoked
	case StatusStale:
		return to == StatusApproved || to == StatusRevoked
	case StatusRevoked:
		return to == StatusApproved
	case StatusRejected:
		return to == StatusDraft
	default:
		return false
	}
}

// Bindings are the structured SUT identifiers a knowledge item applies to
// (spec §10.2): system, environment, page, API and account role. Unset fields
// are dimensions the item is not bound to; the JSON shape is persisted in
// knowledge_item.bindings and never carries credentials.
type Bindings struct {
	SystemID      *int64  `json:"system_id,omitempty"`
	EnvironmentID *int64  `json:"environment_id,omitempty"`
	PageKey       *string `json:"page_key,omitempty"`
	APIKey        *string `json:"api_key,omitempty"`
	AccountRole   *string `json:"account_role,omitempty"`
}

// IsZero reports whether no binding dimension is set.
func (b Bindings) IsZero() bool {
	return b.SystemID == nil && b.EnvironmentID == nil && b.PageKey == nil &&
		b.APIKey == nil && b.AccountRole == nil
}

// Normalize trims the string bindings, treats empty values as unset, validates
// the identifier and key bounds and returns the canonical value.
func (b Bindings) Normalize() (Bindings, error) {
	out := b
	if out.SystemID != nil && *out.SystemID <= 0 {
		return Bindings{}, platform.Invalid("bindings.system_id", "must be a positive decimal ID")
	}
	if out.EnvironmentID != nil && *out.EnvironmentID <= 0 {
		return Bindings{}, platform.Invalid("bindings.environment_id", "must be a positive decimal ID")
	}
	pageKey, err := normalizeBindingString(out.PageKey, "bindings.page_key", MaxBindingKeyRunes,
		"must be at most 500 characters")
	if err != nil {
		return Bindings{}, err
	}
	apiKey, err := normalizeBindingString(out.APIKey, "bindings.api_key", MaxBindingKeyRunes,
		"must be at most 500 characters")
	if err != nil {
		return Bindings{}, err
	}
	accountRole, err := normalizeBindingString(out.AccountRole, "bindings.account_role", MaxAccountRoleRunes,
		"must be at most 64 characters")
	if err != nil {
		return Bindings{}, err
	}
	out.PageKey = pageKey
	out.APIKey = apiKey
	out.AccountRole = accountRole
	return out, nil
}

func normalizeBindingString(value *string, field string, maxRunes int, message string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > maxRunes {
		return nil, platform.Invalid(field, message)
	}
	return &trimmed, nil
}

// Item is one knowledge_item row plus the read-time SourceStale marker.
type Item struct {
	ID                    int64
	Scope                 Scope
	SystemKey             *string
	Title                 string
	Content               string
	Tags                  []string
	Status                Status
	Bindings              Bindings
	LastVerifiedAt        *time.Time
	Confidence            *float64
	SourceVersionID       *int64
	PromotedFromVersionID *int64
	Revision              int
	ReviewReason          *string
	ReviewedBy            *int64
	ReviewedAt            *time.Time
	RevokedAt             *time.Time
	CreatedBy             *int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	// SourceStale is computed by the application on reads: it is true when
	// SourceVersionID no longer exists. It is never persisted and never a
	// status.
	SourceStale bool
}

// Revision is one append-only knowledge_revision row.
type Revision struct {
	ID          int64
	KnowledgeID int64
	Revision    int
	Title       string
	Content     string
	Tags        []string
	ChangedBy   *int64
	CreatedAt   time.Time
}

// Usage is one knowledge_usage row written after a prompt injection.
type Usage struct {
	ID            int64
	KnowledgeID   int64
	AgentRunID    *int64
	WorkItemID    *int64
	TokenEstimate int
	InjectedAt    time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TokenEstimate is the deterministic prompt budget estimate of the whole item:
// (len(title) + len(content) + sum(len(tag))) / 3, with a floor of one. Byte
// lengths make the estimate conservative for CJK content (three bytes ≈ one
// token) without requiring a tokenizer.
func (i Item) TokenEstimate() int {
	size := len(i.Title) + len(i.Content)
	for _, tag := range i.Tags {
		size += len(tag)
	}
	estimate := size / 3
	if estimate < 1 {
		return 1
	}
	return estimate
}

// Snippet returns the excerpt injected into a prompt. Long content is cut at
// SnippetMaxRunes and marked with an ellipsis; the full content never leaves
// the knowledge API through the injection path.
func (i Item) Snippet() string {
	runes := []rune(i.Content)
	if len(runes) <= SnippetMaxRunes {
		return i.Content
	}
	return string(runes[:SnippetMaxRunes]) + "…"
}

// NormalizeTitle trims and validates the title length (1–200 characters).
func NormalizeTitle(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > MaxTitleRunes {
		return "", platform.Invalid("title", "title must be between 1 and 200 characters")
	}
	return value, nil
}

// NormalizeContent trims and validates the content length (1–20000
// characters).
func NormalizeContent(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > MaxContentRunes {
		return "", platform.Invalid("content", "content must be between 1 and 20000 characters")
	}
	return value, nil
}

// NormalizeTags trims every tag, rejects empty or oversized tags, removes
// duplicates keeping the first occurrence and enforces the 20-tag cap after
// dedupe. It always returns a non-nil slice so the NOT NULL column stays an
// empty array rather than NULL.
func NormalizeTags(raw []string) ([]string, error) {
	tags := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		tag := strings.TrimSpace(item)
		if tag == "" {
			return nil, platform.Invalid("tags", "tags must not contain empty items")
		}
		if utf8.RuneCountInString(tag) > MaxTagRunes {
			return nil, platform.Invalid("tags", "each tag must be at most 40 characters")
		}
		if _, duplicate := seen[tag]; duplicate {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) > MaxTags {
		return nil, platform.Invalid("tags", "must contain at most 20 tags")
	}
	return tags, nil
}

// NormalizeScope validates the scope/system_key combination of spec §4.3 and
// returns the normalized key (nil for global items).
func NormalizeScope(scope Scope, systemKey *string) (Scope, *string, error) {
	if !scope.Valid() {
		return "", nil, platform.Invalid("scope", "scope must be global or system")
	}
	key := ""
	if systemKey != nil {
		key = strings.TrimSpace(*systemKey)
	}
	if scope == ScopeGlobal {
		if key != "" {
			return "", nil, platform.Invalid("system_key", "must be empty when scope is global")
		}
		return ScopeGlobal, nil, nil
	}
	if key == "" {
		return "", nil, platform.Invalid("system_key", "is required when scope is system")
	}
	if utf8.RuneCountInString(key) > MaxSystemKeyRunes {
		return "", nil, platform.Invalid("system_key", "must be at most 64 characters")
	}
	if key != strings.ToLower(key) {
		return "", nil, platform.Invalid("system_key", "must be lowercase")
	}
	if strings.ContainsFunc(key, unicode.IsSpace) {
		return "", nil, platform.Invalid("system_key", "must not contain whitespace")
	}
	return ScopeSystem, &key, nil
}

// Validate applies every physical rule of a knowledge item: the
// scope/system_key combination and the title/content/tags bounds.
func Validate(scope Scope, systemKey *string, title, content string, tags []string) error {
	if _, _, err := NormalizeScope(scope, systemKey); err != nil {
		return err
	}
	if _, err := NormalizeTitle(title); err != nil {
		return err
	}
	if _, err := NormalizeContent(content); err != nil {
		return err
	}
	_, err := NormalizeTags(tags)
	return err
}

// ValidateReason trims and validates a review reason (1–2000 characters).
func ValidateReason(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	length := utf8.RuneCountInString(value)
	if length < 1 {
		return "", platform.Invalid("reason", "reason is required")
	}
	if length > MaxReasonRunes {
		return "", platform.Invalid("reason", "must be at most 2000 characters")
	}
	return value, nil
}

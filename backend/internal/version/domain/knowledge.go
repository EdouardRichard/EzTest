package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Knowledge entry kinds, mirrored from the knowledge_entry.kind CHECK
// constraint (design spec §4.2).
const (
	KnowledgeKindPattern  = "pattern"
	KnowledgeKindPitfall  = "pitfall"
	KnowledgeKindDecision = "decision"
	KnowledgeKindCaseIdea = "case_idea"
)

// Knowledge entry review statuses, mirrored from the knowledge_entry.status
// CHECK constraint. draft -> approved/rejected is the only transition.
const (
	KnowledgeDraft    = "draft"
	KnowledgeApproved = "approved"
	KnowledgeRejected = "rejected"
)

// Knowledge confidence values, mirrored from the knowledge_entry.confidence
// CHECK constraint.
const (
	KnowledgeConfidenceHigh   = "high"
	KnowledgeConfidenceMedium = "medium"
	KnowledgeConfidenceLow    = "low"
)

// Knowledge source reference kinds (design spec §4.2: source_refs stores ids and
// types only, never sensitive content).
const (
	KnowledgeRefRequirement = "requirement"
	KnowledgeRefCase        = "case"
	KnowledgeRefIssue       = "issue"
	KnowledgeRefEvidence    = "evidence"
)

// Knowledge entry bounds (design spec §4.2/§9.4). The body cap is 16 KiB; the
// title reuses the P3 title bound. The injection defaults and ranges are the
// EZTEST_KNOWLEDGE_MAX_INJECT / EZTEST_KNOWLEDGE_MAX_BYTES contract of spec
// §11.4; 0 is a legal value and disables the corresponding budget.
const (
	MaxKnowledgeBodyBytes          = 16 << 10
	MaxKnowledgeTitleChars         = 300
	MaxKnowledgeTags               = 20
	MaxKnowledgeTagChars           = 50
	MaxKnowledgeSourceRefs         = 20
	MaxKnowledgeReviewNoteChars    = 2000
	DefaultKnowledgeMaxInject      = 5
	MaxKnowledgeMaxInject          = 20
	DefaultKnowledgeMaxBytes       = 4096
	MaxKnowledgeMaxBytes           = 16384
	minKnowledgeMaxInject          = 0
	minKnowledgeMaxBytes           = 0
	knowledgeMaxInjectedTitleChars = 120
)

// KnowledgeSourceRef points one knowledge entry at the requirement, case, issue
// or evidence it was derived from. It never carries the referenced content.
type KnowledgeSourceRef struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

// KnowledgeEntry is one version-scoped knowledge note (design spec §4.2). The
// review fields stay nil/empty until an administrator approves or rejects it;
// revision is the compare-and-swap counter of every content and review write.
// VersionName is read-model only: the repositories fill it from the owning
// version row for the global list.
type KnowledgeEntry struct {
	ID          int64
	VersionID   int64
	VersionName string
	Kind        string
	Title       string
	Body        string
	Tags        []string
	SourceRefs  []KnowledgeSourceRef
	Status      string
	Confidence  string
	CreatedBy   int64
	ReviewedBy  *int64
	ReviewNote  string
	Revision    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// KnowledgeFilter selects a page of one version's knowledge entries ordered by
// id ascending. An empty Status matches every status; an empty Kind every kind.
// ViewerID restricts a non-admin read: when set, only approved entries and the
// entries created by that user are visible. Nil means every creator (admin or
// server read).
type KnowledgeFilter struct {
	VersionID int64
	Status    string
	Kind      string
	ViewerID  *int64
	Page      Page
}

// KnowledgeViewer is the authenticated caller of a version knowledge read. A
// non-admin only sees approved entries plus their own drafts/rejections; an
// administrator sees every status.
type KnowledgeViewer struct {
	ID    int64
	Admin bool
}

// GlobalKnowledgeFilter selects a page of the cross-version knowledge read. An
// empty Status means approved only (the default of GET /api/knowledge); a nil
// VersionID matches every version.
type GlobalKnowledgeFilter struct {
	VersionID *int64
	Status    string
	Kind      string
	Page      Page
}

// Validate enforces the frozen enums and the 16 KiB body / tag / source-ref
// bounds of spec §4.2/§9.4. It never trims or rewrites user content.
func (e *KnowledgeEntry) Validate() error {
	if e == nil {
		return NewInvalid("", "invalid_knowledge", "knowledge entry is required")
	}
	if e.VersionID <= 0 {
		return NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if !validKnowledgeKind(e.Kind) {
		return NewInvalid("kind", "invalid_knowledge_kind",
			"kind must be pattern, pitfall, decision or case_idea")
	}
	if strings.TrimSpace(e.Title) == "" {
		return NewInvalid("title", "invalid_knowledge_title", "title is required")
	}
	if utf8.RuneCountInString(e.Title) > MaxKnowledgeTitleChars {
		return NewInvalid("title", "invalid_knowledge_title",
			fmt.Sprintf("title must be at most %d characters", MaxKnowledgeTitleChars))
	}
	if strings.TrimSpace(e.Body) == "" {
		return NewInvalid("body", "invalid_knowledge_body", "body is required")
	}
	if len(e.Body) > MaxKnowledgeBodyBytes {
		return NewInvalid("body", "invalid_knowledge_body",
			fmt.Sprintf("body must be at most %d bytes", MaxKnowledgeBodyBytes))
	}
	if err := validateKnowledgeTags(e.Tags); err != nil {
		return err
	}
	if err := validateKnowledgeSourceRefs(e.SourceRefs); err != nil {
		return err
	}
	if !validKnowledgeStatus(e.Status) {
		return NewInvalid("status", "invalid_knowledge_status", "status must be draft, approved or rejected")
	}
	if !validKnowledgeConfidence(e.Confidence) {
		return NewInvalid("confidence", "invalid_knowledge_confidence",
			"confidence must be high, medium or low")
	}
	if utf8.RuneCountInString(e.ReviewNote) > MaxKnowledgeReviewNoteChars {
		return NewInvalid("reason", "invalid_review_note",
			fmt.Sprintf("review note must be at most %d characters", MaxKnowledgeReviewNoteChars))
	}
	return nil
}

// validateKnowledgeTags enforces at most MaxKnowledgeTags non-empty tags of at
// most MaxKnowledgeTagChars characters.
func validateKnowledgeTags(tags []string) error {
	if len(tags) > MaxKnowledgeTags {
		return NewInvalid("tags", "invalid_knowledge_tags",
			fmt.Sprintf("at most %d tags are allowed", MaxKnowledgeTags))
	}
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" {
			return NewInvalid("tags", "invalid_knowledge_tags", "tags must not be empty")
		}
		if utf8.RuneCountInString(tag) > MaxKnowledgeTagChars {
			return NewInvalid("tags", "invalid_knowledge_tags",
				fmt.Sprintf("tags must be at most %d characters", MaxKnowledgeTagChars))
		}
	}
	return nil
}

// validateKnowledgeSourceRefs enforces the frozen reference kinds, positive ids
// and the per-entry bound.
func validateKnowledgeSourceRefs(refs []KnowledgeSourceRef) error {
	if len(refs) > MaxKnowledgeSourceRefs {
		return NewInvalid("sourceRefs", "invalid_knowledge_source_refs",
			fmt.Sprintf("at most %d source references are allowed", MaxKnowledgeSourceRefs))
	}
	for _, ref := range refs {
		switch ref.Kind {
		case KnowledgeRefRequirement, KnowledgeRefCase, KnowledgeRefIssue, KnowledgeRefEvidence:
		default:
			return NewInvalid("sourceRefs", "invalid_knowledge_source_refs",
				"sourceRefs.kind must be requirement, case, issue or evidence")
		}
		if ref.ID <= 0 {
			return NewInvalid("sourceRefs", "invalid_knowledge_source_refs",
				"sourceRefs.id must be a positive id")
		}
	}
	return nil
}

// validKnowledgeKind reports whether kind is one of the frozen DDL values.
func validKnowledgeKind(kind string) bool {
	switch kind {
	case KnowledgeKindPattern, KnowledgeKindPitfall, KnowledgeKindDecision, KnowledgeKindCaseIdea:
		return true
	default:
		return false
	}
}

// validKnowledgeStatus reports whether status is one of the frozen DDL values.
func validKnowledgeStatus(status string) bool {
	switch status {
	case KnowledgeDraft, KnowledgeApproved, KnowledgeRejected:
		return true
	default:
		return false
	}
}

// validKnowledgeConfidence reports whether confidence is one of the frozen DDL
// values.
func validKnowledgeConfidence(confidence string) bool {
	switch confidence {
	case KnowledgeConfidenceHigh, KnowledgeConfidenceMedium, KnowledgeConfidenceLow:
		return true
	default:
		return false
	}
}

// KnowledgeSourceRefJSON is the persisted shape of KnowledgeSourceRef: the id is
// a decimal JSON string so a Snowflake id never round-trips through a float.
type KnowledgeSourceRefJSON struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// MarshalKnowledgeSourceRefs encodes refs for the source_refs JSONB column.
func MarshalKnowledgeSourceRefs(refs []KnowledgeSourceRef) ([]byte, error) {
	encoded := make([]KnowledgeSourceRefJSON, 0, len(refs))
	for _, ref := range refs {
		encoded = append(encoded, KnowledgeSourceRefJSON{Kind: ref.Kind, ID: fmt.Sprintf("%d", ref.ID)})
	}
	return json.Marshal(encoded)
}

// UnmarshalKnowledgeSourceRefs decodes a source_refs JSONB value. It accepts
// both the decimal-string shape above and the legacy numeric id shape so a
// hand-written row never breaks a read.
func UnmarshalKnowledgeSourceRefs(raw []byte) ([]KnowledgeSourceRef, error) {
	if len(raw) == 0 {
		return []KnowledgeSourceRef{}, nil
	}
	var document []KnowledgeSourceRefJSON
	if err := json.Unmarshal(raw, &document); err != nil {
		var legacy []struct {
			Kind string `json:"kind"`
			ID   int64  `json:"id"`
		}
		if legacyErr := json.Unmarshal(raw, &legacy); legacyErr != nil {
			return nil, err
		}
		refs := make([]KnowledgeSourceRef, 0, len(legacy))
		for _, ref := range legacy {
			refs = append(refs, KnowledgeSourceRef{Kind: ref.Kind, ID: ref.ID})
		}
		return refs, nil
	}
	refs := make([]KnowledgeSourceRef, 0, len(document))
	for _, ref := range document {
		var id int64
		if _, err := fmt.Sscanf(ref.ID, "%d", &id); err != nil {
			return nil, fmt.Errorf("knowledge source ref id %q: %w", ref.ID, err)
		}
		refs = append(refs, KnowledgeSourceRef{Kind: ref.Kind, ID: id})
	}
	return refs, nil
}

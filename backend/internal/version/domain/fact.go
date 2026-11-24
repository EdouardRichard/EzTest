package version

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// FactHashLength is the lowercase hex length of version_fact.content_hash
// (SHA-256, pinned by the DB CHECK in version_0007_stage_plan_fact.sql).
const FactHashLength = 64

// FactKindStageTransition is the version_fact kind that persists one stage
// transition's reason as a durable fact (V-0020). Unlike the version.stage_changed
// event, a fact is never removed by event retention, so the R49 stage-entry-job
// repair can still read the reason after retention.event_days deletes the event.
// The key is the visit's stage_entered_at (RFC3339, UTC); the payload is
// {from,to,reason,actor}. version_fact.kind is a free-text CHECK (non-empty), so
// this kind needs no migration.
const FactKindStageTransition = "stage_transition"

// Fact kinds the clarification handoff projects into its decisions/open blocks
// (design v0.2.4-B §8.1). version_fact.kind is a free-text CHECK (non-empty), so
// these need no migration. This is the VERSION domain's fact vocabulary,
// deliberately separate from the clarification.search tool's Host-facing
// result-kind contract (agenttools.ClarificationSearchKind*): the handoff blocks
// and every version_fact.kind filter source from here, so retuning the tool's
// result contract cannot silently draw a different row set of the same table.
const (
	FactKindDecision   = "decision"
	FactKindConstraint = "constraint"
	FactKindGap        = "gap"
	FactKindAssumption = "assumption"
	FactKindRisk       = "risk"
)

// FactKindProgress is the version_fact kind of an agent run's own completion /
// settlement narrative (task.complete writes it; payload.summary IS the agent's
// conclusion). It belongs to the VERSION domain's fact vocabulary so the
// producer (agent completion service) and every consumer that must exclude agent
// narrative — notably the independent design judge's briefing (I-B4) — read the
// same single authority instead of repeating the literal.
const FactKindProgress = "progress"

// FactKindWaiver is the version_fact kind of an admin waiver (v0.2.4-G SC-G11,
// GD-06/P23). It is the durable, append-only record of WHY a version or
// requirement was waived and WHO waived it — independent of the event ledger
// (constitution 五.7), so the report read model can project the waiver reason
// and operator even after event retention. version_fact.kind is a free-text
// CHECK (non-empty), so this kind needs no migration.
//
// Key convention: "version" for a version-level waive_close; "requirement:<id>"
// for a requirement-level waiver. Payload carries {scope, version_id, reason,
// actor_id, ...} with decimal-string IDs (D29). The CURRENT waiver state is the
// version.closure_kind/unverified and requirement.waived columns (cleared on
// reopen); waiver facts are append-only history and are never deleted — a
// reopened version's report gates on the columns, not on the presence of a fact.
const FactKindWaiver = "waiver"

// RequirementFactKeyPrefix is the version_fact key prefix of a requirement-scoped
// fact: the full key is the prefix plus the requirement id in decimal. The waiver
// producer (version/application waiver_service) and the delivery report read model
// (which projects the latest waiver fact per requirement) must agree on this pure
// vocabulary prefix, so the single source lives here (R-H9; registered in
// docs/design/architecture-constraints.md §5.1). It carries no entity state — it
// is a wire-key prefix, not a domain behaviour.
const RequirementFactKeyPrefix = "requirement:"

// Fact persistence errors. They are intentionally separate from ErrNotFound so
// an application error can name the right resource.
var (
	// ErrFactNotFound reports a missing version_fact row.
	ErrFactNotFound = errors.New("version fact not found")
	// ErrFactSuperseded reports that arbitration touched a fact that is no
	// longer current (superseded_by is already set).
	ErrFactSuperseded = errors.New("version fact is already superseded")
	// ErrFactMismatch reports that two facts do not share the same
	// (version_id, kind, key), so they cannot arbitrate each other.
	ErrFactMismatch = errors.New("version facts belong to different version, kind or key")
)

// Fact is one append-only finding of a version (spec §6.1, findings.md). Rows
// are never overwritten or deleted: a conflicting finding is a second row with
// a different ContentHash, and arbitration only writes SupersededBy on the
// losing row (D28). The content hash is the SHA-256 of the canonical JSON
// payload; callers normally leave it empty and let FactService compute it.
type Fact struct {
	ID         int64
	VersionID  int64
	Kind       string
	Key        string
	Payload    map[string]any
	Confidence *float64
	Source     string
	SourceRef  string
	// EvidenceIDs maps to version_fact.evidence_refs, a logical reference to
	// execution evidence / content-ledger IDs (no cross-context FK).
	EvidenceIDs  []int64
	ContentHash  string
	SupersededBy *int64
}

// Normalize trims the textual identity fields and installs the DB defaults an
// empty Go value cannot express (object payload, empty evidence array). It is
// safe to call more than once.
func (f *Fact) Normalize() {
	f.Kind = strings.TrimSpace(f.Kind)
	f.Key = strings.TrimSpace(f.Key)
	f.Source = strings.TrimSpace(f.Source)
	f.SourceRef = strings.TrimSpace(f.SourceRef)
	if f.Payload == nil {
		f.Payload = map[string]any{}
	}
	if f.EvidenceIDs == nil {
		f.EvidenceIDs = []int64{}
	}
}

// Validate checks the persisted invariants of one fact. Payload encoding is
// validated separately by FactContentHash because the hash is computed before
// the row is written.
func (f Fact) Validate() error {
	if f.ID < 0 {
		return platform.Invalid("id", "must not be negative")
	}
	if f.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if err := validation.String("kind", f.Kind, 1, 0); err != nil {
		return err
	}
	if err := validation.String("key", f.Key, 1, 0); err != nil {
		return err
	}
	if err := validation.String("source", f.Source, 1, 0); err != nil {
		return err
	}
	if f.Confidence != nil {
		switch {
		case math.IsNaN(*f.Confidence), math.IsInf(*f.Confidence, 0):
			return platform.Invalid("confidence", "must be a finite number")
		case *f.Confidence < 0 || *f.Confidence > 1:
			return platform.Invalid("confidence", "must be between 0 and 1")
		}
	}
	for _, evidenceID := range f.EvidenceIDs {
		if evidenceID <= 0 {
			return platform.Invalid("evidence_ids", "must contain positive decimal IDs")
		}
	}
	if !IsContentHash(f.ContentHash) {
		return platform.Invalid("content_hash", "must be a 64-character lowercase SHA-256 hex string")
	}
	if f.SupersededBy != nil && *f.SupersededBy <= 0 {
		return platform.Invalid("superseded_by", "must be a positive decimal ID")
	}
	return nil
}

// IsContentHash reports whether value is a lowercase 64-character hex SHA-256.
func IsContentHash(value string) bool {
	if len(value) != FactHashLength {
		return false
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
		case char >= 'a' && char <= 'f':
		default:
			return false
		}
	}
	return true
}

// FactContentHash computes the SHA-256 of the canonical JSON encoding of the
// payload. encoding/json sorts map keys recursively, so two payloads that only
// differ in Go map iteration order hash identically. A nil payload hashes as
// the empty JSON object, matching the DB default.
func FactContentHash(payload map[string]any) (string, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode fact payload: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// FactRepository persists the append-only version_fact store.
type FactRepository interface {
	// Record appends fact unless the same (version_id, kind, key, content_hash)
	// already exists, in which case the existing row is returned unchanged.
	// conflict reports whether a current fact with the same (version, kind,
	// key) but a different content_hash exists; such a fact is NOT modified,
	// it is left to Arbitrate (D28). The bool does not report whether a row was
	// inserted; applications detect an append by comparing the assigned ID.
	Record(ctx context.Context, fact Fact) (stored Fact, conflict bool, err error)

	// Current returns the currently effective facts (superseded_by IS NULL) of
	// one version, optionally narrowed to one kind (empty = all kinds),
	// ordered by append order (id ascending).
	Current(ctx context.Context, versionID int64, kind string) ([]Fact, error)

	// Arbitrate marks dropID superseded by keepID. Both facts must exist, be
	// current, and share the same (version_id, kind, key). No row is deleted
	// and no fact content is modified. keep/drop are positive decimal IDs.
	Arbitrate(ctx context.Context, keepID, dropID int64) error
}

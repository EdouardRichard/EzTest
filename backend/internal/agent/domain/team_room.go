package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Team is the version-scoped collaboration channel.
type Team string

// P1 team enum; code is the P5 codeintel worker team and ops is the P7
// deterministic worker team (both have no team room/digest).
const (
	TeamClarification Team = "clarification"
	TeamDesign        Team = "design"
	TeamExecution     Team = "execution"
	TeamDelivery      Team = "delivery"
	TeamSteward       Team = "steward"
	TeamCode          Team = "code"
	TeamOps           Team = "ops"
)

// Valid reports whether the team is part of the frozen enum.
func (t Team) Valid() bool {
	switch t {
	case TeamClarification, TeamDesign, TeamExecution, TeamDelivery, TeamSteward, TeamCode, TeamOps:
		return true
	default:
		return false
	}
}

// ParseTeam validates the external string form of a team.
func ParseTeam(raw string) (Team, error) {
	team := Team(strings.TrimSpace(raw))
	if !team.Valid() {
		return "", platform.Invalid("team", "must be one of clarification, design, execution, delivery, steward, code, ops")
	}
	return team, nil
}

// PostKind classifies a team-room post.
type PostKind string

// Team-room post kinds; decision/blocker posts are pinned in digests.
const (
	PostKindFinding  PostKind = "finding"
	PostKindDecision PostKind = "decision"
	PostKindBlocker  PostKind = "blocker"
	PostKindHandoff  PostKind = "handoff"
	PostKindStatus   PostKind = "status"
)

// Valid reports whether the kind is part of the frozen enum.
func (k PostKind) Valid() bool {
	switch k {
	case PostKindFinding, PostKindDecision, PostKindBlocker, PostKindHandoff, PostKindStatus:
		return true
	default:
		return false
	}
}

// MaxPostContentLength bounds a single team-room post.
const MaxPostContentLength = 20000

// PostRef is a structured reference attached to a team-room post. IDs are
// decimal strings so 64-bit values survive JSON without precision loss.
type PostRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

// Validate checks that the reference names something and, when present, the ID
// is a positive decimal value.
func (r PostRef) Validate() error {
	if strings.TrimSpace(r.Kind) == "" {
		return platform.Invalid("refs", "ref kind must not be empty")
	}
	if r.ID != "" {
		value, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil || value <= 0 {
			return platform.Invalid("refs", "ref id must be a positive decimal ID")
		}
	}
	return nil
}

// TeamRoomPost is one immutable team-room post.
type TeamRoomPost struct {
	ID         int64
	VersionID  int64
	Team       Team
	Kind       PostKind
	Content    string
	Refs       []PostRef
	AgentRunID *int64
	CreatedBy  *int64
	CreatedAt  time.Time
}

// Validate checks the post identity, enum fields and content length.
func (p TeamRoomPost) Validate() error {
	if p.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if !p.Team.Valid() {
		return platform.Invalid("team", "must be one of clarification, design, execution, delivery, steward, code, ops")
	}
	if !p.Kind.Valid() {
		return platform.Invalid("kind", "must be one of finding, decision, blocker, handoff, status")
	}
	if err := validation.String("content", p.Content, 1, MaxPostContentLength); err != nil {
		return err
	}
	for _, ref := range p.Refs {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// TeamRoomDigest is one deterministic digest version for a version/team pair.
type TeamRoomDigest struct {
	ID            int64
	VersionID     int64
	Team          Team
	Summary       string
	PinnedPostIDs []int64
	TokenEstimate int
	Version       int
	UpdatedAt     time.Time
}

// Validate checks the digest identity and version monotonicity fields.
func (d TeamRoomDigest) Validate() error {
	if d.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if !d.Team.Valid() {
		return platform.Invalid("team", "must be one of clarification, design, execution, delivery, steward, code, ops")
	}
	if strings.TrimSpace(d.Summary) == "" {
		return platform.Invalid("summary", "must not be empty")
	}
	if d.TokenEstimate < 0 {
		return platform.Invalid("token_estimate", "must not be negative")
	}
	if d.Version <= 0 {
		return platform.Invalid("version", "must be positive")
	}
	return nil
}

// EncodePinnedPostIDs renders the pinned ID list as a JSON array. Pinned IDs
// are internal DB JSON, so they stay numeric here and are formatted as decimal
// strings at the API boundary.
func (d TeamRoomDigest) EncodePinnedPostIDs() (json.RawMessage, error) {
	ids := d.PinnedPostIDs
	if ids == nil {
		ids = []int64{}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, platform.Invalid("pinned_post_ids", "must be JSON-encodable")
	}
	return raw, nil
}

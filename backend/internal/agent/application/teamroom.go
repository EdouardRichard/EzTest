package agentapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// Team-room limits and digest defaults. The digest thresholds are read from the
// frozen P0 settings catalog (team_room.*) with these values as fallbacks.
const (
	DefaultDigestEveryNPosts  = 20
	DefaultDigestInterval     = 10 * time.Minute
	DefaultDigestKeepVersions = 5
	MaxDigestPosts            = 200
	MaxDigestPostRunes        = 200
	DefaultTeamRoomLimit      = 20
	MaxTeamRoomLimit          = 100
	DefaultContextPosts       = 10
	MaxContextPosts           = 50
)

// TeamRoomContext is the read model handed to the context builder and the
// read-only HTTP API: the newest digest plus bounded recent posts.
type TeamRoomContext struct {
	Team   string
	Digest *agent.TeamRoomDigest
	Posts  []agent.TeamRoomPost
}

// TeamRoomView is the HTTP read model; it shares the team-room context shape.
type TeamRoomView = TeamRoomContext

// TeamRoom writes structured team posts and maintains bounded deterministic
// digests. Posting never triggers an agent run and P1 exposes no write API.
type TeamRoom struct {
	repo     agent.TeamRoomRepository
	clock    clock.Clock
	ids      platformid.Generator
	settings SettingsReader
	events   eventingapp.Publisher
}

// NewTeamRoom wires the team-room service.
func NewTeamRoom(repo agent.TeamRoomRepository, clk clock.Clock, ids platformid.Generator, settings SettingsReader, events eventingapp.Publisher) *TeamRoom {
	if clk == nil {
		clk = clock.System{}
	}
	return &TeamRoom{repo: repo, clock: clk, ids: ids, settings: settings, events: events}
}

// Post persists one team-room post, publishes team_room.post and (best effort)
// refreshes the digest when the configured trigger fires.
func (s *TeamRoom) Post(ctx context.Context, post agent.TeamRoomPost) (agent.TeamRoomPost, error) {
	if err := post.Validate(); err != nil {
		return agent.TeamRoomPost{}, err
	}
	if post.ID <= 0 {
		post.ID = s.ids.Next()
	}
	if post.CreatedAt.IsZero() {
		post.CreatedAt = s.clock.Now()
	}
	stored, err := s.repo.EnsurePost(ctx, post)
	if err != nil {
		if errors.Is(err, agent.ErrConflict) {
			return agent.TeamRoomPost{}, platform.Conflict("team room post already exists")
		}
		return agent.TeamRoomPost{}, err
	}
	s.publish(ctx, "team_room.post", stored.VersionID, map[string]any{
		"version_id": platformid.Format(stored.VersionID),
		"team":       string(stored.Team),
		"post_id":    platformid.Format(stored.ID),
		"kind":       string(stored.Kind),
	})
	// Digest maintenance is derived state: a failure must not undo the post.
	s.ensureDigest(ctx, stored.VersionID, stored.Team)
	return stored, nil
}

// Digest returns the newest digest or agent.ErrNotFound when none exists.
func (s *TeamRoom) Digest(ctx context.Context, versionID int64, team string) (agent.TeamRoomDigest, error) {
	parsedTeam, err := validateTeamTarget(versionID, team)
	if err != nil {
		return agent.TeamRoomDigest{}, err
	}
	return s.repo.LatestDigest(ctx, versionID, parsedTeam)
}

// Context returns the newest digest and up to maxPosts recent posts for the
// run context. maxPosts is clamped to the tool contract range (1..50).
func (s *TeamRoom) Context(ctx context.Context, versionID int64, team string, maxPosts int) (TeamRoomContext, error) {
	parsedTeam, err := validateTeamTarget(versionID, team)
	if err != nil {
		return TeamRoomContext{}, err
	}
	if maxPosts <= 0 {
		maxPosts = DefaultContextPosts
	}
	if maxPosts > MaxContextPosts {
		maxPosts = MaxContextPosts
	}
	return s.read(ctx, versionID, parsedTeam, maxPosts)
}

// Read returns the read-only API view: newest digest plus the latest posts.
func (s *TeamRoom) Read(ctx context.Context, versionID int64, team string, limit int) (TeamRoomView, error) {
	parsedTeam, err := validateTeamTarget(versionID, team)
	if err != nil {
		return TeamRoomView{}, err
	}
	if limit <= 0 {
		limit = DefaultTeamRoomLimit
	}
	if limit > MaxTeamRoomLimit {
		limit = MaxTeamRoomLimit
	}
	return s.read(ctx, versionID, parsedTeam, limit)
}

func (s *TeamRoom) read(ctx context.Context, versionID int64, team agent.Team, limit int) (TeamRoomContext, error) {
	view := TeamRoomContext{Team: string(team), Posts: []agent.TeamRoomPost{}}
	digest, err := s.repo.LatestDigest(ctx, versionID, team)
	switch {
	case err == nil:
		view.Digest = &digest
	case errors.Is(err, agent.ErrNotFound):
		// No digest yet: the API returns digest=null.
	default:
		return TeamRoomContext{}, err
	}
	posts, err := s.repo.ListPosts(ctx, versionID, team, limit)
	if err != nil {
		return TeamRoomContext{}, err
	}
	if posts != nil {
		view.Posts = posts
	}
	return view, nil
}

func (s *TeamRoom) ensureDigest(ctx context.Context, versionID int64, team agent.Team) {
	everyN := s.settingInt(ctx, "team_room.digest_every_n_posts", DefaultDigestEveryNPosts)
	intervalMinutes := s.settingInt(ctx, "team_room.digest_interval_minutes", int64(DefaultDigestInterval/time.Minute))
	keepVersions := s.settingInt(ctx, "team_room.digest_keep_versions", DefaultDigestKeepVersions)
	interval := time.Duration(intervalMinutes) * time.Minute

	latest, err := s.repo.LatestDigest(ctx, versionID, team)
	var since time.Time
	var count int
	overdue := false
	switch {
	case err == nil:
		since = latest.UpdatedAt
		count, err = s.repo.CountPostsSince(ctx, versionID, team, since)
		overdue = !s.clock.Now().Before(since.Add(interval))
	case errors.Is(err, agent.ErrNotFound):
		count, err = s.repo.CountPosts(ctx, versionID, team)
		// Without a digest the interval baseline is the oldest post, so a team
		// room that accumulated posts while digest generation was unavailable
		// still catches up on the next post.
		if err == nil && count > 0 {
			if oldest, listErr := s.repo.ListPostsSince(ctx, versionID, team, time.Time{}, 1); listErr == nil && len(oldest) == 1 {
				overdue = !s.clock.Now().Before(oldest[0].CreatedAt.Add(interval))
			}
		}
	default:
		return
	}
	if err != nil {
		return
	}
	if int64(count) < everyN && !overdue {
		return
	}

	posts, err := s.repo.ListPostsSince(ctx, versionID, team, since, MaxDigestPosts)
	if err != nil || len(posts) == 0 {
		return
	}
	summary, pinned := buildDigestSummary(team, posts)
	digest := agent.TeamRoomDigest{
		ID:            s.ids.Next(),
		VersionID:     versionID,
		Team:          team,
		Summary:       summary,
		PinnedPostIDs: pinned,
		TokenEstimate: EstimateTokens(summary),
		Version:       latest.Version + 1,
		UpdatedAt:     s.clock.Now(),
	}
	stored, saveErr := s.repo.SaveDigest(ctx, digest)
	if saveErr != nil {
		if !errors.Is(saveErr, agent.ErrConflict) {
			return
		}
		// A concurrent writer won the version race; recompute once.
		fresh, freshErr := s.repo.LatestDigest(ctx, versionID, team)
		if freshErr != nil {
			return
		}
		digest.Version = fresh.Version + 1
		digest.ID = s.ids.Next()
		stored, saveErr = s.repo.SaveDigest(ctx, digest)
		if saveErr != nil {
			return
		}
	}
	_ = s.repo.PruneDigests(ctx, versionID, team, int(keepVersions))
	s.publish(ctx, "team_room.digest_updated", versionID, map[string]any{
		"version_id":     platformid.Format(versionID),
		"team":           string(team),
		"digest_id":      platformid.Format(stored.ID),
		"digest_version": stored.Version,
		"token_estimate": stored.TokenEstimate,
	})
}

func (s *TeamRoom) settingInt(ctx context.Context, key string, fallback int64) int64 {
	if s.settings == nil {
		return fallback
	}
	value, err := s.settings.Int(ctx, key)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func (s *TeamRoom) publish(ctx context.Context, eventType string, versionID int64, payload map[string]any) {
	if s.events == nil {
		return
	}
	scope := map[string]any{
		"version_id": platformid.Format(versionID),
		"team":       payload["team"],
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      eventType,
		Scope:     scope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

func validateTeamTarget(versionID int64, team string) (agent.Team, error) {
	if versionID <= 0 {
		return "", platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return agent.ParseTeam(team)
}

// digestKindOrder fixes the deterministic grouping of a digest summary.
var digestKindOrder = []agent.PostKind{
	agent.PostKindDecision,
	agent.PostKindBlocker,
	agent.PostKindFinding,
	agent.PostKindHandoff,
	agent.PostKindStatus,
}

// buildDigestSummary renders a deterministic, bounded digest. Decision and
// blocker posts are pinned so compaction cannot lose them.
func buildDigestSummary(team agent.Team, posts []agent.TeamRoomPost) (string, []int64) {
	ordered := make([]agent.TeamRoomPost, len(posts))
	copy(ordered, posts)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := digestKindRank(ordered[i].Kind), digestKindRank(ordered[j].Kind)
		if left != right {
			return left < right
		}
		return ordered[i].ID < ordered[j].ID
	})

	var builder strings.Builder
	fmt.Fprintf(&builder, "团队 %s 摘要（%d 条）", team, len(ordered))
	pinned := make([]int64, 0)
	position := 0
	for _, kind := range digestKindOrder {
		start := position
		for position < len(ordered) && ordered[position].Kind == kind {
			position++
		}
		group := ordered[start:position]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&builder, "\n[%s] %d 条", kind, len(group))
		for _, post := range group {
			fmt.Fprintf(&builder, "\n- #%d %s", post.ID, truncateRunes(normalizePostContent(post.Content), MaxDigestPostRunes))
			if kind == agent.PostKindDecision || kind == agent.PostKindBlocker {
				pinned = append(pinned, post.ID)
			}
		}
	}
	sort.Slice(pinned, func(i, j int) bool { return pinned[i] < pinned[j] })
	return builder.String(), pinned
}

func digestKindRank(kind agent.PostKind) int {
	for index, candidate := range digestKindOrder {
		if candidate == kind {
			return index
		}
	}
	return len(digestKindOrder)
}

func normalizePostContent(content string) string {
	return strings.Join(strings.Fields(content), " ")
}

func truncateRunes(content string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(content)
	if len(runes) <= max {
		return content
	}
	return string(runes[:max])
}

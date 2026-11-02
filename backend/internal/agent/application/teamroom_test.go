package agentapp_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeSettings struct {
	ints   map[string]int64
	floats map[string]float64
	err    error
}

func (s *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	value, ok := s.ints[key]
	if !ok {
		return 0, platform.NotFound("setting")
	}
	return value, nil
}

func (s *fakeSettings) Float(_ context.Context, key string) (float64, error) {
	if s.err != nil {
		return 0, s.err
	}
	value, ok := s.floats[key]
	if !ok {
		return 0, platform.NotFound("setting")
	}
	return value, nil
}

type fakePublisher struct {
	events []eventingapp.Event
	err    error
}

func (p *fakePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return p.err
}

type fakeTeamRoomRepo struct {
	posts   []agent.TeamRoomPost
	digests []agent.TeamRoomDigest

	ensureErr  error
	listErr    error
	countErr   error
	latestErr  error
	saveErr    error
	pruneErr   error
	pruneCalls []int
	pruneTeam  []agent.Team
}

func (r *fakeTeamRoomRepo) EnsurePost(_ context.Context, post agent.TeamRoomPost) (agent.TeamRoomPost, error) {
	if r.ensureErr != nil {
		return agent.TeamRoomPost{}, r.ensureErr
	}
	for _, existing := range r.posts {
		if existing.ID == post.ID {
			return existing, nil
		}
	}
	r.posts = append(r.posts, post)
	return post, nil
}

func (r *fakeTeamRoomRepo) ListPosts(_ context.Context, versionID int64, team agent.Team, limit int) ([]agent.TeamRoomPost, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	posts := make([]agent.TeamRoomPost, 0)
	for _, post := range r.posts {
		if post.VersionID == versionID && post.Team == team {
			posts = append(posts, post)
		}
	}
	sort.Slice(posts, func(i, j int) bool { return posts[i].ID > posts[j].ID })
	if limit > 0 && len(posts) > limit {
		posts = posts[:limit]
	}
	return posts, nil
}

func (r *fakeTeamRoomRepo) ListPostsSince(_ context.Context, versionID int64, team agent.Team, since time.Time, limit int) ([]agent.TeamRoomPost, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	posts := make([]agent.TeamRoomPost, 0)
	for _, post := range r.posts {
		if post.VersionID == versionID && post.Team == team && post.CreatedAt.After(since) {
			posts = append(posts, post)
		}
	}
	sort.Slice(posts, func(i, j int) bool { return posts[i].ID < posts[j].ID })
	if limit > 0 && len(posts) > limit {
		posts = posts[:limit]
	}
	return posts, nil
}

func (r *fakeTeamRoomRepo) CountPosts(_ context.Context, versionID int64, team agent.Team) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	count := 0
	for _, post := range r.posts {
		if post.VersionID == versionID && post.Team == team {
			count++
		}
	}
	return count, nil
}

func (r *fakeTeamRoomRepo) CountPostsSince(_ context.Context, versionID int64, team agent.Team, since time.Time) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	count := 0
	for _, post := range r.posts {
		if post.VersionID == versionID && post.Team == team && post.CreatedAt.After(since) {
			count++
		}
	}
	return count, nil
}

func (r *fakeTeamRoomRepo) LatestDigest(_ context.Context, versionID int64, team agent.Team) (agent.TeamRoomDigest, error) {
	if r.latestErr != nil {
		return agent.TeamRoomDigest{}, r.latestErr
	}
	var latest agent.TeamRoomDigest
	found := false
	for _, digest := range r.digests {
		if digest.VersionID != versionID || digest.Team != team {
			continue
		}
		if !found || digest.Version > latest.Version {
			latest = digest
			found = true
		}
	}
	if !found {
		return agent.TeamRoomDigest{}, agent.ErrNotFound
	}
	return latest, nil
}

func (r *fakeTeamRoomRepo) SaveDigest(_ context.Context, digest agent.TeamRoomDigest) (agent.TeamRoomDigest, error) {
	if r.saveErr != nil {
		return agent.TeamRoomDigest{}, r.saveErr
	}
	for _, existing := range r.digests {
		if existing.VersionID == digest.VersionID && existing.Team == digest.Team && existing.Version == digest.Version {
			return agent.TeamRoomDigest{}, agent.ErrConflict
		}
	}
	if digest.ID == 0 {
		digest.ID = int64(len(r.digests) + 1)
	}
	r.digests = append(r.digests, digest)
	return digest, nil
}

func (r *fakeTeamRoomRepo) PruneDigests(_ context.Context, versionID int64, team agent.Team, keep int) error {
	if r.pruneErr != nil {
		return r.pruneErr
	}
	r.pruneCalls = append(r.pruneCalls, keep)
	r.pruneTeam = append(r.pruneTeam, team)
	if keep <= 0 {
		return nil
	}
	maxVersion := 0
	for _, digest := range r.digests {
		if digest.VersionID == versionID && digest.Team == team && digest.Version > maxVersion {
			maxVersion = digest.Version
		}
	}
	kept := make([]agent.TeamRoomDigest, 0, len(r.digests))
	for _, digest := range r.digests {
		if digest.VersionID == versionID && digest.Team == team && digest.Version <= maxVersion-keep {
			continue
		}
		kept = append(kept, digest)
	}
	r.digests = kept
	return nil
}

var _ agent.TeamRoomRepository = (*fakeTeamRoomRepo)(nil)

var testClock = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func newTeamRoom(repo *fakeTeamRoomRepo, settings *fakeSettings, events *fakePublisher) (*agentapp.TeamRoom, *fakeClock) {
	clk := &fakeClock{now: testClock}
	if settings == nil {
		settings = &fakeSettings{}
	}
	if events == nil {
		events = &fakePublisher{}
	}
	return agentapp.NewTeamRoom(repo, clk, &sequentialIDs{next: 1000}, settings, events), clk
}

type sequentialIDs struct{ next int64 }

func (g *sequentialIDs) Next() int64 {
	g.next++
	return g.next
}

func validPost(versionID int64, kind agent.PostKind, content string) agent.TeamRoomPost {
	return agent.TeamRoomPost{
		VersionID: versionID,
		Team:      agent.TeamClarification,
		Kind:      kind,
		Content:   content,
	}
}

func TestTeamRoomPostPersistsAndPublishes(t *testing.T) {
	repo := &fakeTeamRoomRepo{}
	publisher := &fakePublisher{}
	room, clk := newTeamRoom(repo, nil, publisher)
	ctx := context.Background()

	input := validPost(10, agent.PostKindFinding, "接口已就绪")
	input.Refs = []agent.PostRef{{Kind: "attachment", ID: "7001"}}
	post, err := room.Post(ctx, input)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if post.ID == 0 || post.CreatedAt != clk.Now() {
		t.Fatalf("post defaults = %#v", post)
	}
	if len(repo.posts) != 1 || repo.posts[0].ID != post.ID {
		t.Fatalf("stored posts = %#v", repo.posts)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("events = %#v", publisher.events)
	}
	event := publisher.events[0]
	if event.Channel != "version:10" || event.Type != "team_room.post" {
		t.Fatalf("event = %#v", event)
	}
	if event.Scope["version_id"] != "10" || event.Scope["team"] != "clarification" {
		t.Fatalf("scope = %#v", event.Scope)
	}
	if event.Payload["post_id"] != fmt.Sprintf("%d", post.ID) || event.Payload["kind"] != "finding" {
		t.Fatalf("payload = %#v", event.Payload)
	}
	refs, ok := event.Payload["refs"].([]map[string]string)
	if !ok || len(refs) != len(post.Refs) {
		t.Fatalf("payload refs = %#v", event.Payload["refs"])
	}
	for index, ref := range post.Refs {
		if refs[index]["kind"] != ref.Kind || refs[index]["id"] != ref.ID {
			t.Fatalf("payload refs = %#v", refs)
		}
	}

	if _, err := room.Post(ctx, validPost(10, "invalid", "x")); err == nil {
		t.Fatal("invalid post accepted")
	}
	if len(repo.posts) != 1 {
		t.Fatalf("repository called for invalid post: %#v", repo.posts)
	}

	repo.ensureErr = errors.New("db down")
	if _, err := room.Post(ctx, validPost(10, agent.PostKindStatus, "x")); err == nil {
		t.Fatal("repository error not propagated")
	}
}

func TestTeamRoomDigestTriggerEveryNPosts(t *testing.T) {
	repo := &fakeTeamRoomRepo{}
	settings := &fakeSettings{ints: map[string]int64{"team_room.digest_every_n_posts": 3}}
	room, clk := newTeamRoom(repo, settings, nil)
	ctx := context.Background()

	post := func(kind agent.PostKind, content string) agent.TeamRoomPost {
		clk.now = clk.now.Add(time.Minute)
		stored, err := room.Post(ctx, validPost(10, kind, content))
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
		return stored
	}

	post(agent.PostKindFinding, "first")
	post(agent.PostKindDecision, "second")
	if len(repo.digests) != 0 {
		t.Fatalf("digest generated too early: %#v", repo.digests)
	}
	third := post(agent.PostKindBlocker, "third")

	if len(repo.digests) != 1 {
		t.Fatalf("digests = %#v", repo.digests)
	}
	digest := repo.digests[0]
	if digest.Version != 1 || digest.Team != agent.TeamClarification || digest.VersionID != 10 {
		t.Fatalf("digest = %#v", digest)
	}
	if len(digest.PinnedPostIDs) != 2 {
		t.Fatalf("pinned = %#v", digest.PinnedPostIDs)
	}
	if !strings.Contains(digest.Summary, "[decision]") || !strings.Contains(digest.Summary, "[blocker]") {
		t.Fatalf("summary = %q", digest.Summary)
	}
	if !strings.Contains(digest.Summary, fmt.Sprintf("#%d", third.ID)) {
		t.Fatalf("summary misses newest post: %q", digest.Summary)
	}
	if digest.TokenEstimate != agentapp.EstimateTokens(digest.Summary) {
		t.Fatalf("token estimate = %d", digest.TokenEstimate)
	}

	// One more post stays below the threshold (digest only covers the first 3).
	post(agent.PostKindStatus, "fourth")
	if len(repo.digests) != 1 {
		t.Fatalf("unexpected digest: %#v", repo.digests)
	}
	post(agent.PostKindStatus, "fifth")
	post(agent.PostKindStatus, "sixth")
	if len(repo.digests) != 2 || repo.digests[1].Version != 2 {
		t.Fatalf("digests = %#v", repo.digests)
	}
	if len(repo.pruneCalls) == 0 || repo.pruneCalls[len(repo.pruneCalls)-1] != agentapp.DefaultDigestKeepVersions {
		t.Fatalf("prune calls = %#v", repo.pruneCalls)
	}
}

func TestTeamRoomDigestTriggerByInterval(t *testing.T) {
	repo := &fakeTeamRoomRepo{}
	settings := &fakeSettings{ints: map[string]int64{
		"team_room.digest_every_n_posts":    100,
		"team_room.digest_interval_minutes": 10,
		"team_room.digest_keep_versions":    2,
	}}
	room, clk := newTeamRoom(repo, settings, nil)
	ctx := context.Background()

	if _, err := room.Post(ctx, validPost(10, agent.PostKindFinding, "first")); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(repo.digests) != 0 {
		t.Fatalf("digest generated too early: %#v", repo.digests)
	}

	clk.now = clk.now.Add(11 * time.Minute)
	if _, err := room.Post(ctx, validPost(10, agent.PostKindFinding, "second")); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(repo.digests) != 1 {
		t.Fatalf("digests = %#v", repo.digests)
	}
	if repo.digests[0].Version != 1 || !strings.Contains(repo.digests[0].Summary, "second") {
		t.Fatalf("digest = %#v", repo.digests[0])
	}

	// The next digest is version 2 and old versions are pruned by the repo.
	clk.now = clk.now.Add(11 * time.Minute)
	if _, err := room.Post(ctx, validPost(10, agent.PostKindFinding, "third")); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(repo.digests) != 2 || repo.digests[1].Version != 2 {
		t.Fatalf("digests = %#v", repo.digests)
	}
}

func TestTeamRoomDigestKeepsNewestVersions(t *testing.T) {
	repo := &fakeTeamRoomRepo{}
	settings := &fakeSettings{ints: map[string]int64{
		"team_room.digest_every_n_posts": 1,
		"team_room.digest_keep_versions": 2,
	}}
	room, clk := newTeamRoom(repo, settings, nil)
	ctx := context.Background()

	for index := 0; index < 5; index++ {
		clk.now = clk.now.Add(time.Minute)
		if _, err := room.Post(ctx, validPost(10, agent.PostKindFinding, fmt.Sprintf("post-%d", index))); err != nil {
			t.Fatalf("Post: %v", err)
		}
	}
	if len(repo.digests) != 2 {
		t.Fatalf("digests = %#v", repo.digests)
	}
	if repo.digests[1].Version != 5 {
		t.Fatalf("latest version = %d, want 5", repo.digests[1].Version)
	}
	for _, call := range repo.pruneCalls {
		if call != 2 {
			t.Fatalf("prune keep = %d, want 2", call)
		}
	}
}

func TestTeamRoomDigestIsDeterministicAndTruncated(t *testing.T) {
	long := strings.Repeat("很长的团队帖子内容", 50)
	build := func() agent.TeamRoomDigest {
		repo := &fakeTeamRoomRepo{}
		settings := &fakeSettings{ints: map[string]int64{"team_room.digest_every_n_posts": 2}}
		room, clk := newTeamRoom(repo, settings, nil)
		ctx := context.Background()
		if _, err := room.Post(ctx, validPost(10, agent.PostKindFinding, long)); err != nil {
			t.Fatalf("Post: %v", err)
		}
		clk.now = clk.now.Add(time.Minute)
		if _, err := room.Post(ctx, validPost(10, agent.PostKindDecision, "决定 A")); err != nil {
			t.Fatalf("Post: %v", err)
		}
		if len(repo.digests) != 1 {
			t.Fatalf("digests = %#v", repo.digests)
		}
		return repo.digests[0]
	}
	first := build()
	second := build()
	if first.Summary != second.Summary || first.TokenEstimate != second.TokenEstimate {
		t.Fatalf("digest is not deterministic:\n%q\n%q", first.Summary, second.Summary)
	}

	truncated := ""
	for _, line := range strings.Split(first.Summary, "\n") {
		if strings.Contains(line, "[finding]") {
			continue
		}
		if strings.HasPrefix(line, "- #") && strings.Contains(line, "很长") {
			truncated = line
			break
		}
	}
	if truncated == "" {
		// The finding body is one of the "- #id ..." lines; locate it by content.
		for _, line := range strings.Split(first.Summary, "\n") {
			if strings.Contains(line, "很长") {
				truncated = line
				break
			}
		}
	}
	if truncated == "" {
		t.Fatalf("summary missing finding body: %q", first.Summary)
	}
	body := strings.SplitN(truncated, " ", 3)
	content := ""
	if len(body) == 3 {
		content = body[2]
	}
	if utf8.RuneCountInString(content) != agentapp.MaxDigestPostRunes {
		t.Fatalf("truncated content length = %d, want %d (%q)",
			utf8.RuneCountInString(content), agentapp.MaxDigestPostRunes, content)
	}
}

func TestTeamRoomDigestFailuresDoNotFailPost(t *testing.T) {
	repo := &fakeTeamRoomRepo{latestErr: errors.New("query failed")}
	settings := &fakeSettings{ints: map[string]int64{"team_room.digest_every_n_posts": 1}}
	room, _ := newTeamRoom(repo, settings, nil)
	ctx := context.Background()

	if _, err := room.Post(ctx, validPost(10, agent.PostKindFinding, "post")); err != nil {
		t.Fatalf("digest lookup error failed the post: %v", err)
	}
	if len(repo.digests) != 0 {
		t.Fatalf("digests = %#v", repo.digests)
	}

	repo = &fakeTeamRoomRepo{saveErr: errors.New("insert failed")}
	room, _ = newTeamRoom(repo, settings, nil)
	if _, err := room.Post(ctx, validPost(10, agent.PostKindFinding, "post")); err != nil {
		t.Fatalf("digest save error failed the post: %v", err)
	}
}

func TestTeamRoomContextAndRead(t *testing.T) {
	repo := &fakeTeamRoomRepo{}
	room, clk := newTeamRoom(repo, nil, nil)
	ctx := context.Background()

	for index := 0; index < 5; index++ {
		clk.now = clk.now.Add(time.Minute)
		if _, err := room.Post(ctx, validPost(10, agent.PostKindFinding, fmt.Sprintf("post-%d", index))); err != nil {
			t.Fatalf("Post: %v", err)
		}
	}
	digest := agent.TeamRoomDigest{
		ID: 5000, VersionID: 10, Team: agent.TeamClarification,
		Summary: "滚动摘要", PinnedPostIDs: []int64{repo.posts[1].ID},
		TokenEstimate: 30, Version: 1, UpdatedAt: clk.now,
	}
	repo.digests = append(repo.digests, digest)

	contextView, err := room.Context(ctx, 10, "clarification", 3)
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if contextView.Team != "clarification" || contextView.Digest == nil || contextView.Digest.ID != digest.ID {
		t.Fatalf("context = %#v", contextView)
	}
	if len(contextView.Posts) != 3 {
		t.Fatalf("context posts = %#v", contextView.Posts)
	}
	if contextView.Posts[0].ID != repo.posts[4].ID {
		t.Fatalf("context posts not newest first: %#v", contextView.Posts)
	}

	view, err := room.Read(ctx, 10, "clarification", 2)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if view.Team != "clarification" || view.Digest == nil || len(view.Posts) != 2 {
		t.Fatalf("view = %#v", view)
	}

	if _, err := room.Context(ctx, 10, "nope", 3); err == nil {
		t.Fatal("invalid team accepted")
	} else if field := platform.AsError(err).Details["field"]; field != "team" {
		t.Fatalf("field = %#v", field)
	}

	emptyRepo := &fakeTeamRoomRepo{}
	emptyRoom, _ := newTeamRoom(emptyRepo, nil, nil)
	empty, err := emptyRoom.Read(ctx, 10, "clarification", 20)
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if empty.Digest != nil || len(empty.Posts) != 0 {
		t.Fatalf("empty view = %#v", empty)
	}

	noDigest, err := emptyRoom.Context(ctx, 10, "clarification", 10)
	if err != nil {
		t.Fatalf("Context no digest: %v", err)
	}
	if noDigest.Digest != nil {
		t.Fatalf("digest = %#v", noDigest.Digest)
	}
}

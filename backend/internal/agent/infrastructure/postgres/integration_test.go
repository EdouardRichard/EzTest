//go:build integration

package agentpg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

var testTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func newIDs(t *testing.T) platformid.Generator {
	t.Helper()
	generator, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

func ptr[T any](value T) *T { return &value }

func ensureItem(t *testing.T, repo *agentpg.WorkItemRepository, versionID int64, dedupe string, priority int, maxAttempts int) agent.WorkItem {
	t.Helper()
	item, created, err := repo.Ensure(context.Background(), agent.NewWorkItem{
		VersionID:   versionID,
		Team:        "clarification",
		Type:        "chat.turn",
		Priority:    priority,
		DedupeKey:   dedupe,
		Payload:     map[string]any{"dedupe": dedupe},
		MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatalf("Ensure(%s): %v", dedupe, err)
	}
	if !created {
		t.Fatalf("Ensure(%s) did not create", dedupe)
	}
	return item
}

func TestWorkItemEnsureIsIdempotent(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()

	in := agent.NewWorkItem{
		VersionID: 10,
		Team:      "clarification",
		Type:      "chat.turn",
		DedupeKey: "opening:1",
		Payload:   map[string]any{"conversation_id": "1"},
	}
	first, created, err := repo.Ensure(ctx, in)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created || first.ID == 0 || first.Status != agent.WorkItemOpen {
		t.Fatalf("created=%v item=%#v", created, first)
	}
	if string(first.Payload) != `{"conversation_id": "1"}` {
		t.Fatalf("payload = %s", first.Payload)
	}

	second, created, err := repo.Ensure(ctx, in)
	if err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if created {
		t.Fatal("second Ensure reported created")
	}
	if second.ID != first.ID {
		t.Fatalf("second id = %d, want %d", second.ID, first.ID)
	}

	// The dedupe key is scoped by version and type.
	in.VersionID = 11
	third, created, err := repo.Ensure(ctx, in)
	if err != nil {
		t.Fatalf("Ensure other version: %v", err)
	}
	if !created || third.ID == first.ID {
		t.Fatalf("version scope not honoured: created=%v item=%#v", created, third)
	}

	missing, err := repo.Get(ctx, first.ID+1)
	if !errors.Is(err, agent.ErrNotFound) || missing.ID != 0 {
		t.Fatalf("Get missing = %#v, %v", missing, err)
	}
}

func TestWorkItemClaimOrderingLeaseAndFencing(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()

	low := ensureItem(t, repo, 10, "low", 0, 3)
	high := ensureItem(t, repo, 10, "high", 10, 3)
	mid := ensureItem(t, repo, 10, "mid", 5, 3)

	claimed, ok, err := repo.Claim(ctx, "worker-a", 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim = %#v, %v, %v", claimed, ok, err)
	}
	if claimed.ID != high.ID {
		t.Fatalf("claimed id = %d, want highest priority %d", claimed.ID, high.ID)
	}
	if claimed.Status != agent.WorkItemClaimed || claimed.Attempts != 1 || claimed.LeaseEpoch != 1 {
		t.Fatalf("claimed state = %#v", claimed)
	}
	if claimed.ClaimedBy == nil || *claimed.ClaimedBy != "worker-a" {
		t.Fatalf("claimed_by = %#v", claimed.ClaimedBy)
	}
	if claimed.LeaseUntil == nil || claimed.LeaseUntil.Before(time.Now()) {
		t.Fatalf("lease_until = %#v", claimed.LeaseUntil)
	}

	if err := repo.MarkRunning(ctx, claimed.ID, claimed.LeaseEpoch); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := repo.Renew(ctx, claimed.ID, claimed.LeaseEpoch, 3*time.Second); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if err := repo.Renew(ctx, claimed.ID, claimed.LeaseEpoch+1, 3*time.Second); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale Renew = %v, want ErrLeaseLost", err)
	}

	second, ok, err := repo.Claim(ctx, "worker-b", time.Second)
	if err != nil || !ok || second.ID != mid.ID {
		t.Fatalf("second claim = %#v, %v, %v", second, ok, err)
	}
	third, ok, err := repo.Claim(ctx, "worker-c", time.Second)
	if err != nil || !ok || third.ID != low.ID {
		t.Fatalf("third claim = %#v, %v, %v", third, ok, err)
	}
	if extra, ok, err := repo.Claim(ctx, "worker-d", time.Second); err != nil || ok || extra.ID != 0 {
		t.Fatalf("empty claim = %#v, %v, %v", extra, ok, err)
	}

	if err := repo.Complete(ctx, claimed.ID, claimed.LeaseEpoch+1); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale Complete = %v, want ErrLeaseLost", err)
	}
	if err := repo.Complete(ctx, claimed.ID, claimed.LeaseEpoch); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stored, err := repo.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("Get after complete: %v", err)
	}
	if stored.Status != agent.WorkItemDone || stored.LeaseUntil != nil || stored.ClaimedBy != nil {
		t.Fatalf("completed item = %#v", stored)
	}
	if err := repo.MarkRunning(ctx, claimed.ID, claimed.LeaseEpoch); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("MarkRunning after complete = %v, want ErrLeaseLost", err)
	}

	attempts, err := repo.ListAttempts(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Attempt != 1 || attempts[0].Status != agent.AttemptDone {
		t.Fatalf("attempts = %#v", attempts)
	}
	if attempts[0].FinishedAt == nil {
		t.Fatal("done attempt has no finished_at")
	}
}

func TestWorkItemFailBackoffAndDead(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()

	retryable := ensureItem(t, repo, 10, "retryable", 0, 3)
	claimed, ok, err := repo.Claim(ctx, "worker-a", 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim = %#v, %v, %v", claimed, ok, err)
	}
	if err := repo.Fail(ctx, claimed.ID, claimed.LeaseEpoch, "temporary", true, 80*time.Millisecond); err != nil {
		t.Fatalf("Fail retryable: %v", err)
	}
	stored, err := repo.Get(ctx, retryable.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != agent.WorkItemOpen || stored.LastError == nil || *stored.LastError != "temporary" {
		t.Fatalf("retryable item = %#v", stored)
	}
	if stored.AvailableAt.Before(time.Now()) {
		t.Fatalf("available_at = %v should be in the future", stored.AvailableAt)
	}
	blocked, ok, err := repo.Claim(ctx, "worker-b", time.Second)
	if err != nil || ok || blocked.ID != 0 {
		t.Fatalf("claim during backoff = %#v, %v, %v", blocked, ok, err)
	}

	time.Sleep(120 * time.Millisecond)
	reclaimed, ok, err := repo.Claim(ctx, "worker-b", time.Second)
	if err != nil || !ok || reclaimed.ID != retryable.ID {
		t.Fatalf("claim after backoff = %#v, %v, %v", reclaimed, ok, err)
	}
	if reclaimed.Attempts != 2 || reclaimed.LeaseEpoch != 2 {
		t.Fatalf("reclaimed = %#v", reclaimed)
	}

	if err := repo.Fail(ctx, reclaimed.ID, reclaimed.LeaseEpoch, "fatal", false, 0); err != nil {
		t.Fatalf("Fail fatal: %v", err)
	}
	stored, err = repo.Get(ctx, reclaimed.ID)
	if err != nil {
		t.Fatalf("Get dead: %v", err)
	}
	if stored.Status != agent.WorkItemDead {
		t.Fatalf("status = %q, want dead", stored.Status)
	}

	exhausted := ensureItem(t, repo, 10, "exhausted", 0, 1)
	claimedExhausted, ok, err := repo.Claim(ctx, "worker-c", time.Second)
	if err != nil || !ok || claimedExhausted.ID != exhausted.ID {
		t.Fatalf("claim exhausted = %#v, %v, %v", claimedExhausted, ok, err)
	}
	if err := repo.Fail(ctx, claimedExhausted.ID, claimedExhausted.LeaseEpoch, "gave up", true, time.Second); err != nil {
		t.Fatalf("Fail exhausted: %v", err)
	}
	stored, err = repo.Get(ctx, exhausted.ID)
	if err != nil {
		t.Fatalf("Get exhausted: %v", err)
	}
	if stored.Status != agent.WorkItemDead {
		t.Fatalf("exhausted status = %q, want dead", stored.Status)
	}

	attempts, err := repo.ListAttempts(ctx, exhausted.ID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != agent.AttemptDead {
		t.Fatalf("attempts = %#v", attempts)
	}
}

func TestWorkItemRecoverExpired(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()

	first := ensureItem(t, repo, 10, "expire-a", 10, 3)
	second := ensureItem(t, repo, 10, "expire-b", 5, 3)
	dead := ensureItem(t, repo, 10, "expire-dead", 1, 1)
	open := ensureItem(t, repo, 10, "still-open", 0, 3)

	for _, want := range []int64{first.ID, second.ID, dead.ID} {
		claimed, ok, err := repo.Claim(ctx, "worker-a", 40*time.Millisecond)
		if err != nil || !ok || claimed.ID != want {
			t.Fatalf("claim = %#v, %v, %v; want %d", claimed, ok, err, want)
		}
	}
	time.Sleep(80 * time.Millisecond)

	recovered, err := repo.RecoverExpired(ctx)
	if err != nil {
		t.Fatalf("RecoverExpired: %v", err)
	}
	if recovered != 3 {
		t.Fatalf("recovered = %d, want 3", recovered)
	}

	for _, id := range []int64{first.ID, second.ID} {
		stored, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get(%d): %v", id, err)
		}
		if stored.Status != agent.WorkItemOpen || stored.ClaimedBy != nil || stored.LeaseUntil != nil {
			t.Fatalf("recovered item = %#v", stored)
		}
		if stored.Attempts != 1 {
			t.Fatalf("attempts = %d, want preserved 1", stored.Attempts)
		}
	}
	stored, err := repo.Get(ctx, dead.ID)
	if err != nil {
		t.Fatalf("Get dead: %v", err)
	}
	if stored.Status != agent.WorkItemDead {
		t.Fatalf("dead item status = %q", stored.Status)
	}
	if stored.LastError == nil || *stored.LastError != "lease expired" {
		t.Fatalf("dead last_error = %#v", stored.LastError)
	}
	stored, err = repo.Get(ctx, open.ID)
	if err != nil {
		t.Fatalf("Get open: %v", err)
	}
	if stored.Status != agent.WorkItemOpen {
		t.Fatalf("untouched item status = %q", stored.Status)
	}

	// The recovered high-priority item is claimable again with a new epoch.
	reclaimed, ok, err := repo.Claim(ctx, "worker-b", time.Second)
	if err != nil || !ok || reclaimed.ID != first.ID {
		t.Fatalf("reclaim = %#v, %v, %v", reclaimed, ok, err)
	}
	if reclaimed.Attempts != 2 || reclaimed.LeaseEpoch != 2 {
		t.Fatalf("reclaimed = %#v", reclaimed)
	}

	// Recovery records the expired attempt as recovered.
	attempts, err := repo.ListAttempts(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 2 || attempts[0].Status != agent.AttemptRecovered {
		t.Fatalf("attempts = %#v", attempts)
	}
}

func TestWorkItemListByVersion(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()

	one := ensureItem(t, repo, 10, "one", 0, 3)
	ensureItem(t, repo, 11, "other", 0, 3)
	two := ensureItem(t, repo, 10, "two", 0, 3)

	items, err := repo.ListByVersion(ctx, 10, 10)
	if err != nil {
		t.Fatalf("ListByVersion: %v", err)
	}
	if len(items) != 2 || items[0].ID != two.ID || items[1].ID != one.ID {
		t.Fatalf("items = %#v", items)
	}
}

func TestTeamRoomPostsAndDigests(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := agentpg.NewTeamRoomRepository(pool, ids)
	ctx := context.Background()

	first := agent.TeamRoomPost{
		ID:         ids.Next(),
		VersionID:  10,
		Team:       agent.TeamClarification,
		Kind:       agent.PostKindDecision,
		Content:    "采用方案 A",
		Refs:       []agent.PostRef{{Kind: "requirement", ID: "100"}},
		AgentRunID: ptr(int64(900)),
		CreatedAt:  testTime,
	}
	stored, err := repo.EnsurePost(ctx, first)
	if err != nil {
		t.Fatalf("EnsurePost: %v", err)
	}
	if stored.ID != first.ID || len(stored.Refs) != 1 || stored.Refs[0].ID != "100" {
		t.Fatalf("stored = %#v", stored)
	}
	again, err := repo.EnsurePost(ctx, first)
	if err != nil {
		t.Fatalf("EnsurePost again: %v", err)
	}
	if again.ID != first.ID || again.Content != first.Content {
		t.Fatalf("idempotent post = %#v", again)
	}

	second := agent.TeamRoomPost{
		ID: ids.Next(), VersionID: 10, Team: agent.TeamClarification,
		Kind: agent.PostKindBlocker, Content: "接口未就绪",
		CreatedAt: testTime.Add(time.Minute),
	}
	if _, err := repo.EnsurePost(ctx, second); err != nil {
		t.Fatalf("EnsurePost second: %v", err)
	}
	other := agent.TeamRoomPost{
		ID: ids.Next(), VersionID: 10, Team: agent.TeamDesign,
		Kind: agent.PostKindStatus, Content: "设计待办",
		CreatedAt: testTime.Add(2 * time.Minute),
	}
	if _, err := repo.EnsurePost(ctx, other); err != nil {
		t.Fatalf("EnsurePost other team: %v", err)
	}

	posts, err := repo.ListPosts(ctx, 10, agent.TeamClarification, 10)
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if len(posts) != 2 || posts[0].ID != second.ID || posts[1].ID != first.ID {
		t.Fatalf("posts = %#v", posts)
	}
	since, err := repo.CountPostsSince(ctx, 10, agent.TeamClarification, testTime)
	if err != nil {
		t.Fatalf("CountPostsSince: %v", err)
	}
	if since != 1 {
		t.Fatalf("count since = %d, want 1", since)
	}
	total, err := repo.CountPosts(ctx, 10, agent.TeamClarification)
	if err != nil || total != 2 {
		t.Fatalf("CountPosts = %d, %v", total, err)
	}
	listedSince, err := repo.ListPostsSince(ctx, 10, agent.TeamClarification, testTime, 10)
	if err != nil || len(listedSince) != 1 || listedSince[0].ID != second.ID {
		t.Fatalf("ListPostsSince = %#v, %v", listedSince, err)
	}

	if _, err := repo.LatestDigest(ctx, 10, agent.TeamClarification); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("LatestDigest = %v, want ErrNotFound", err)
	}

	for version := 1; version <= 7; version++ {
		digest := agent.TeamRoomDigest{
			ID:            ids.Next(),
			VersionID:     10,
			Team:          agent.TeamClarification,
			Summary:       "摘要版本",
			PinnedPostIDs: []int64{first.ID},
			TokenEstimate: 12,
			Version:       version,
			UpdatedAt:     testTime.Add(time.Duration(version) * time.Minute),
		}
		if _, err := repo.SaveDigest(ctx, digest); err != nil {
			t.Fatalf("SaveDigest(%d): %v", version, err)
		}
	}
	latest, err := repo.LatestDigest(ctx, 10, agent.TeamClarification)
	if err != nil {
		t.Fatalf("LatestDigest: %v", err)
	}
	if latest.Version != 7 || len(latest.PinnedPostIDs) != 1 || latest.PinnedPostIDs[0] != first.ID {
		t.Fatalf("latest digest = %#v", latest)
	}
	if _, err := repo.SaveDigest(ctx, latest); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("duplicate digest = %v, want ErrConflict", err)
	}
	if err := repo.PruneDigests(ctx, 10, agent.TeamClarification, 5); err != nil {
		t.Fatalf("PruneDigests: %v", err)
	}
	remaining := countDigests(t, pool, 10)
	if remaining != 5 {
		t.Fatalf("remaining digests = %d, want 5", remaining)
	}
}

func countDigests(t *testing.T, pool *pgxpool.Pool, versionID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM team_room_digest WHERE version_id = $1`, versionID).Scan(&count); err != nil {
		t.Fatalf("count digests: %v", err)
	}
	return count
}

func TestRuntimeProfilesRunsMessagesAndPackets(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := agentpg.NewRuntimeRepository(pool, ids)
	ctx := context.Background()

	profile := agent.AgentProfile{
		Key:          "clarification_host",
		Name:         "澄清 Host",
		SystemPrompt: "[profile:clarification_host] 你是澄清 Host",
		AllowedTools: []string{"requirement.read", "context.version_pack"},
		Model:        ptr("deepseek-v4.1-flash"),
		Budgets:      agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
		Enabled:      true,
		CreatedAt:    testTime,
		UpdatedAt:    testTime,
	}
	stored, created, err := repo.EnsureProfile(ctx, profile)
	if err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if !created || stored.ID == 0 {
		t.Fatalf("profile = %#v created=%v", stored, created)
	}

	edited := profile
	edited.Name = "被管理员改名"
	edited.SystemPrompt = "edited"
	same, created, err := repo.EnsureProfile(ctx, edited)
	if err != nil {
		t.Fatalf("EnsureProfile again: %v", err)
	}
	if created || same.Name != profile.Name || same.SystemPrompt != profile.SystemPrompt {
		t.Fatalf("seed overwrote profile: %#v created=%v", same, created)
	}

	loaded, err := repo.GetProfile(ctx, "clarification_host")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if loaded.Model == nil || *loaded.Model != "deepseek-v4.1-flash" || len(loaded.AllowedTools) != 2 {
		t.Fatalf("loaded profile = %#v", loaded)
	}
	if loaded.Budgets.MaxTurns != 12 || loaded.Budgets.SoftSeconds != 300 || loaded.Budgets.HardSeconds != 600 {
		t.Fatalf("budgets = %#v", loaded.Budgets)
	}
	if _, err := repo.GetProfile(ctx, "missing"); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("GetProfile missing = %v", err)
	}
	profiles, err := repo.ListProfiles(ctx)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("ListProfiles = %#v, %v", profiles, err)
	}

	run := agent.AgentRun{
		ID:             ids.Next(),
		ProfileKey:     "clarification_host",
		WorkItemID:     ptr(int64(55)),
		ConversationID: ptr(int64(66)),
		Status:         agent.AgentRunRunning,
		OwnerInstance:  "instance-1",
		LeaseEpoch:     3,
		Usage:          map[string]int{},
		StartedAt:      testTime,
	}
	createdRun, err := repo.CreateRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if createdRun.ID != run.ID || createdRun.Status != agent.AgentRunRunning {
		t.Fatalf("created run = %#v", createdRun)
	}

	assistant := agent.AgentMessage{
		ID:             ids.Next(),
		RunID:          ptr(run.ID),
		ConversationID: ptr(int64(66)),
		Role:           agent.MessageRoleAssistant,
		Parts:          []agent.MessagePart{{Type: "text", Text: "hello"}},
		ToolCalls:      []agent.ToolCall{{ID: "call_1", Name: "requirement.read", Arguments: `{}`}},
		Usage:          map[string]int{"output_tokens": 4},
		CreatedAt:      testTime,
	}
	if _, err := repo.AppendMessage(ctx, assistant); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	tool := agent.AgentMessage{
		ID:         ids.Next(),
		RunID:      ptr(run.ID),
		Role:       agent.MessageRoleTool,
		Parts:      []agent.MessagePart{{Type: "text", Text: `{"items":[]}`}},
		ToolCallID: ptr("call_1"),
		CreatedAt:  testTime,
	}
	if _, err := repo.AppendMessage(ctx, tool); err != nil {
		t.Fatalf("AppendMessage tool: %v", err)
	}
	messages, err := repo.ListRunMessages(ctx, run.ID, 10)
	if err != nil || len(messages) != 2 {
		t.Fatalf("ListRunMessages = %#v, %v", messages, err)
	}
	if messages[0].ID != assistant.ID || messages[1].Role != agent.MessageRoleTool ||
		messages[1].ToolCallID == nil || *messages[1].ToolCallID != "call_1" {
		t.Fatalf("messages = %#v", messages)
	}

	packet := agent.ContextPacket{
		RunID:     run.ID,
		VersionID: 10,
		Sections: []agent.ContextSection{
			{Name: "system", Content: "scope", Refs: []string{"version:10"}},
			{Name: "version_pack", Content: "title", Optional: true},
		},
		TokenCounts: map[string]int{"system": 20, "version_pack": 18},
		Hash:        strings.Repeat("a", 64),
		CreatedAt:   testTime,
	}
	savedPacket, err := repo.SaveContextPacket(ctx, packet)
	if err != nil {
		t.Fatalf("SaveContextPacket: %v", err)
	}
	if savedPacket.ID == 0 {
		t.Fatal("saved packet has no ID")
	}
	loadedPacket, err := repo.GetContextPacket(ctx, savedPacket.ID)
	if err != nil {
		t.Fatalf("GetContextPacket: %v", err)
	}
	if len(loadedPacket.Sections) != 2 || loadedPacket.Sections[0].Name != "system" || loadedPacket.TokenCounts["system"] != 20 {
		t.Fatalf("loaded packet = %#v", loadedPacket)
	}
	var attached *int64
	if err := pool.QueryRow(ctx, `SELECT context_packet_id FROM agent_run WHERE id = $1`, run.ID).Scan(&attached); err != nil {
		t.Fatalf("query run packet: %v", err)
	}
	if attached == nil || *attached != savedPacket.ID {
		t.Fatalf("run context_packet_id = %#v, want %d", attached, savedPacket.ID)
	}

	wrongEpoch := run
	wrongEpoch.Status = agent.AgentRunDone
	wrongEpoch.LeaseEpoch = 2
	wrongEpoch.FinishedAt = ptr(testTime.Add(time.Minute))
	if err := repo.FinishRun(ctx, wrongEpoch); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("FinishRun stale = %v, want ErrLeaseLost", err)
	}
	finished := run
	finished.Status = agent.AgentRunDone
	finished.Turns = 3
	finished.Usage = map[string]int{"input_tokens": 30}
	finished.FinishedAt = ptr(testTime.Add(time.Minute))
	if err := repo.FinishRun(ctx, finished); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	var status, usage string
	if err := pool.QueryRow(ctx, `SELECT status, usage::text FROM agent_run WHERE id = $1`, run.ID).Scan(&status, &usage); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if status != "done" || !strings.Contains(usage, "input_tokens") {
		t.Fatalf("run status=%q usage=%s", status, usage)
	}

	if _, err := repo.GetContextPacket(ctx, savedPacket.ID+1); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("missing packet = %v, want ErrNotFound", err)
	}
}

// TestWorkItemClaimForTypesFiltersAndReclaimsExpiredLeases pins the P7
// ClaimForTypes contract: only the requested work item types are claimed, an
// expired claimed/running lease can be taken over (epoch/attempts bump), and an
// empty type set claims nothing.
func TestWorkItemClaimForTypesFiltersAndReclaimsExpiredLeases(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()

	ensureTyped := func(workType, team, dedupe string, priority int) agent.WorkItem {
		t.Helper()
		item, created, err := repo.Ensure(ctx, agent.NewWorkItem{
			VersionID: 10,
			Team:      team,
			Type:      workType,
			Priority:  priority,
			DedupeKey: dedupe,
		})
		if err != nil {
			t.Fatalf("Ensure(%s): %v", workType, err)
		}
		if !created {
			t.Fatalf("Ensure(%s) did not create", workType)
		}
		return item
	}

	renderPDF := ensureTyped("report.render_pdf", "ops", "report:1:rev:1", 0)
	// Higher-priority items of other types prove the type filter is applied.
	ensureTyped("chat.turn", "clarification", "reply:1", 100)
	ensureTyped("execute.case", "execution", "execute:1", 50)

	claimed, ok, err := repo.ClaimForTypes(ctx, "render-worker", []string{"report.render_pdf"}, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimForTypes = %#v, %v, %v", claimed, ok, err)
	}
	if claimed.ID != renderPDF.ID || claimed.Type != "report.render_pdf" {
		t.Fatalf("claimed %#v, want render PDF item %d", claimed, renderPDF.ID)
	}
	if claimed.LeaseEpoch != 1 || claimed.Attempts != 1 ||
		claimed.ClaimedBy == nil || *claimed.ClaimedBy != "render-worker" {
		t.Fatalf("first claim state = %#v", claimed)
	}

	// The only render item is leased, so a second claim finds nothing.
	if extra, ok, err := repo.ClaimForTypes(ctx, "render-worker-2", []string{"report.render_pdf"}, time.Second); err != nil || ok || extra.ID != 0 {
		t.Fatalf("second ClaimForTypes = %#v, %v, %v", extra, ok, err)
	}

	// An expired lease is claimable again with a bumped epoch and attempt.
	if _, err := pool.Exec(ctx, `
		UPDATE work_item SET status = 'running', lease_until = now() - interval '1 second'
		WHERE id = $1
	`, renderPDF.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	reclaimed, ok, err := repo.ClaimForTypes(ctx, "render-worker-2", []string{"report.render_pdf"}, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimForTypes(expired) = %#v, %v, %v", reclaimed, ok, err)
	}
	if reclaimed.ID != renderPDF.ID || reclaimed.LeaseEpoch != 2 || reclaimed.Attempts != 2 {
		t.Fatalf("reclaimed state = %#v, want epoch 2 attempt 2", reclaimed)
	}

	if empty, ok, err := repo.ClaimForTypes(ctx, "worker", nil, time.Second); err != nil || ok || empty.ID != 0 {
		t.Fatalf("empty types claim = %#v, %v, %v", empty, ok, err)
	}
}

//go:build integration

package versionadapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestContextPacketReaderVersionLatest pins the production read path against a
// real PostgreSQL schema: the query returns the newest cache-first packet of
// the requested version only (read-only, version-scoped), the persisted
// segment metadata survives the JSONB round-trip, provider cache statistics
// overwrite the local estimate, and P1-shaped legacy rows are reported as
// not-found instead of being mis-decoded.
func TestContextPacketReaderVersionLatest(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	repo := agentpg.NewRuntimeRepository(pool, ids)
	reader := NewContextPacketReader(repo)

	const versionID = int64(9001)
	const otherVersionID = int64(9002)

	older := contextPacketFixture(t, "system older", "older briefing")
	savedOlder, err := repo.SaveContextPacket(ctx, agentapp.EncodeContextPacket(ids.Next(), versionID, older))
	if err != nil {
		t.Fatalf("save older packet: %v", err)
	}

	latest := contextPacketFixture(t, "system latest", "latest briefing",
		agentcontext.Message{ID: 41, Role: "user", Content: "最新用户消息"},
		agentcontext.Message{ID: 42, Role: "assistant", Content: "最新助手消息"},
	)
	savedLatest, err := repo.SaveContextPacket(ctx, agentapp.EncodeContextPacket(ids.Next(), versionID, latest))
	if err != nil {
		t.Fatalf("save latest packet: %v", err)
	}
	if savedLatest.ID == savedOlder.ID {
		t.Fatal("saved packets share one ID")
	}

	other := contextPacketFixture(t, "system other", "other briefing")
	savedOther, err := repo.SaveContextPacket(ctx, agentapp.EncodeContextPacket(ids.Next(), otherVersionID, other))
	if err != nil {
		t.Fatalf("save other-version packet: %v", err)
	}

	// The later save for version X must win even though another version's
	// packet was persisted after it.
	got, err := reader.LatestContextPacket(ctx, versionID)
	if err != nil {
		t.Fatalf("LatestContextPacket: %v", err)
	}
	if got.PrefixHash != latest.PrefixHash || got.BriefingHash != latest.BriefingHash {
		t.Fatalf("hashes = %q/%q, want %q/%q",
			got.PrefixHash, got.BriefingHash, latest.PrefixHash, latest.BriefingHash)
	}
	if len(got.Segments) != len(latest.Segments) {
		t.Fatalf("segments = %d, want %d", len(got.Segments), len(latest.Segments))
	}
	for index, segment := range latest.Segments {
		if got.Segments[index] != segment {
			t.Fatalf("segment %d = %#v, want %#v", index, got.Segments[index], segment)
		}
	}

	otherGot, err := reader.LatestContextPacket(ctx, otherVersionID)
	if err != nil {
		t.Fatalf("LatestContextPacket(other): %v", err)
	}
	if otherGot.PrefixHash != other.PrefixHash || otherGot.PrefixHash == latest.PrefixHash {
		t.Fatalf("other-version prefix = %q, want %q", otherGot.PrefixHash, other.PrefixHash)
	}
	if savedOther.VersionID != otherVersionID {
		t.Fatalf("saved other version = %d", savedOther.VersionID)
	}

	// Provider cache statistics overwrite the persisted local estimate and the
	// next read reflects the updated token_counts bookkeeping.
	if got.CachedTokens != 0 || got.CacheHit {
		t.Fatalf("fresh cache bookkeeping = %d/%v, want 0/false", got.CachedTokens, got.CacheHit)
	}
	if err := repo.UpdateContextPacketCacheStats(ctx, savedLatest.ID, 321, true); err != nil {
		t.Fatalf("UpdateContextPacketCacheStats: %v", err)
	}
	updated, err := reader.LatestContextPacket(ctx, versionID)
	if err != nil {
		t.Fatalf("LatestContextPacket after cache update: %v", err)
	}
	if updated.CachedTokens != 321 || !updated.CacheHit {
		t.Fatalf("cache bookkeeping after provider usage = %d/%v, want 321/true",
			updated.CachedTokens, updated.CacheHit)
	}

	// A P1-shaped legacy row (no cache-first metadata) is not mis-decoded.
	legacy, err := repo.SaveContextPacket(ctx, agent.ContextPacket{
		RunID:     ids.Next(),
		VersionID: 9003,
		Sections: []agent.ContextSection{
			{Name: "system", Content: "legacy"},
			{Name: "conversation", Content: "legacy thread"},
		},
		TokenCounts: map[string]int{"system": 8, "conversation": 12},
		Hash:        strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("save legacy packet: %v", err)
	}
	if _, err := reader.LatestContextPacket(ctx, legacy.VersionID); !errors.Is(err, agentcontext.ErrContextPacketNotFound) {
		t.Fatalf("legacy packet error = %v, want ErrContextPacketNotFound", err)
	}

	// Unknown versions are not found; an invalid ID is a typed 400.
	if _, err := reader.LatestContextPacket(ctx, 999999); !errors.Is(err, agentcontext.ErrContextPacketNotFound) {
		t.Fatalf("unknown version error = %v, want ErrContextPacketNotFound", err)
	}
	if _, err := reader.LatestContextPacket(ctx, 0); err == nil || platform.AsError(err).Status != 400 {
		t.Fatalf("invalid version error = %v, want a 400", err)
	}
}

func contextPacketFixture(t *testing.T, system, briefing string, history ...agentcontext.Message) agentcontext.ContextPacket {
	t.Helper()
	built, err := agentcontext.NewAssembler().Build(context.Background(), agentcontext.AssemblyRequest{
		System:   system,
		Stage:    "executing",
		Briefing: briefing,
		History:  history,
		ToolResults: []agentcontext.ToolResult{
			{CallID: "call-7", Name: "read", Content: "工具结果"},
		},
	})
	if err != nil {
		t.Fatalf("assemble fixture: %v", err)
	}
	return built.Packet
}

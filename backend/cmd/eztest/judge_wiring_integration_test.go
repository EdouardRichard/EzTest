//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versioninfra "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// TestJudgeExhaustedFactStoreIntegration pins the T13 judge-exhausted adapter over
// the real version FactService: Insert writes a current judge_exhausted version
// fact bound to the exact case version; Has matches that exact case version and
// only current facts (superseded_by IS NULL), so a sibling version, a sibling
// case, or a superseded fact never answers "escalated".
func TestJudgeExhaustedFactStoreIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

	versionID := ids.Next()
	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (990002, 'judge fixture env', 'active') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed fixture environment: %v", err)
	}
	versionRepo := versioninfra.New(pool, ids)
	if err := versionRepo.Create(ctx, versiondomain.Version{
		ID: versionID, Title: "Judge version", Status: versiondomain.StatusInitial,
		CreatedBy: 42, CreatedAt: now, UpdatedAt: now, EnvironmentID: 990002,
	}, nil); err != nil {
		t.Fatalf("create version: %v", err)
	}

	factSvc := versionapp.NewFactService(versionpg.NewFactRepository(pool, ids), ids, nil)
	store := &judgeExhaustedFactStore{facts: factSvc, pool: pool}

	const caseID, caseVersionID = int64(7001), int64(5)
	const requirementID = int64(10)

	// Before any escalation: not exhausted.
	if has, err := store.HasJudgeExhausted(ctx, caseID, caseVersionID); err != nil || has {
		t.Fatalf("pre-insert Has = %v/%v, want false/nil", has, err)
	}

	if created, err := store.InsertJudgeExhausted(ctx, versionID, requirementID, caseID, caseVersionID); err != nil || !created {
		t.Fatalf("InsertJudgeExhausted = %v/%v, want true/nil (first append is a creation transition)", created, err)
	}
	if has, err := store.HasJudgeExhausted(ctx, caseID, caseVersionID); err != nil || !has {
		t.Fatalf("post-insert Has = %v/%v, want true/nil", has, err)
	}
	// Exact case_version match: a sibling version of the same case is not escalated.
	if has, err := store.HasJudgeExhausted(ctx, caseID, caseVersionID+1); err != nil || has {
		t.Fatalf("sibling version Has = %v/%v, want false/nil", has, err)
	}
	// A different case is not escalated.
	if has, err := store.HasJudgeExhausted(ctx, caseID+1, caseVersionID); err != nil || has {
		t.Fatalf("sibling case Has = %v/%v, want false/nil", has, err)
	}
	// Idempotent re-insert (FactService dedupe on version+kind+key+content_hash):
	// created=false is what keeps the degradation event from being re-published
	// on every exhausted reconcile pass (whole-branch review Critical #1).
	if created, err := store.InsertJudgeExhausted(ctx, versionID, requirementID, caseID, caseVersionID); err != nil || created {
		t.Fatalf("re-insert JudgeExhausted = %v/%v, want false/nil (dedupe is not a new escalation)", created, err)
	}

	// Current-only: once the fact is superseded it is no longer an escalation.
	var factID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM version_fact WHERE kind = $1 AND key = $2`,
		judgeExhaustedFactKind, judgeExhaustedFactKey(caseID, caseVersionID)).Scan(&factID); err != nil {
		t.Fatalf("read fact id: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE version_fact SET superseded_by = $1 WHERE id = $1`, factID); err != nil {
		t.Fatalf("supersede fact: %v", err)
	}
	if has, err := store.HasJudgeExhausted(ctx, caseID, caseVersionID); err != nil || has {
		t.Fatalf("superseded Has = %v/%v, want false/nil (current-only)", has, err)
	}
}

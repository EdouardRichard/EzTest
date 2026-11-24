//go:build integration

package agentpg_test

import (
	"context"
	"testing"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedAgentRun inserts one minimal agent_run row so skill_usage's real
// same-context FK has a target.
func seedAgentRun(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO agent_run (id, profile_key, owner_instance)
		VALUES ($1, 'case_executor', 'test-instance')`, id); err != nil {
		t.Fatalf("seed agent_run %d: %v", id, err)
	}
}

// TestSkillUsageLedgerAppendAndRead pins the fact table contract end to end:
// found/miss rows append with per-run seq, the read side returns them in order,
// and runs are isolated.
func TestSkillUsageLedgerAppendAndRead(t *testing.T) {
	repo, pool := newSkillRepo(t)
	ctx := context.Background()
	seedAgentRun(t, pool, 9001)
	seedAgentRun(t, pool, 9002)

	seq, err := repo.RecordSkillUsage(ctx, 9001, "playwright-cli", true)
	if err != nil || seq != 1 {
		t.Fatalf("RecordSkillUsage hit = %d, %v; want seq 1", seq, err)
	}
	seq, err = repo.RecordSkillUsage(ctx, 9001, "no-such-skill", false)
	if err != nil || seq != 2 {
		t.Fatalf("RecordSkillUsage miss = %d, %v; want seq 2", seq, err)
	}
	seq, err = repo.RecordSkillUsage(ctx, 9002, "api-recon", true)
	if err != nil || seq != 1 {
		t.Fatalf("second run seq = %d, %v; want 1 (per-run sequence)", seq, err)
	}

	rows, err := repo.ListSkillUsage(ctx, 9001)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListSkillUsage = %#v, %v", rows, err)
	}
	if rows[0].Seq != 1 || rows[0].SkillName != "playwright-cli" || !rows[0].Found || rows[0].RunID != 9001 {
		t.Fatalf("row 0 = %#v", rows[0])
	}
	if rows[1].Seq != 2 || rows[1].SkillName != "no-such-skill" || rows[1].Found {
		t.Fatalf("row 1 = %#v", rows[1])
	}
	if rows[0].ID == 0 || rows[0].CreatedAt.IsZero() {
		t.Fatalf("row identity/timestamp missing: %#v", rows[0])
	}

	// The service surface (production read/write path) observes the same rows.
	svc := skillapp.NewService(repo, nil)
	if err := svc.RecordUsage(ctx, 9001, "api-recon", true); err != nil {
		t.Fatalf("service RecordUsage: %v", err)
	}
	viaService, err := svc.Usage(ctx, 9001)
	if err != nil || len(viaService) != 3 || viaService[2].Seq != 3 {
		t.Fatalf("service Usage = %#v, %v", viaService, err)
	}
}

// TestSkillUsageRunSeqIdempotent pins the (run_id, seq) idempotency: a replayed
// write of the same fact is a no-op, never a duplicate or an error.
func TestSkillUsageRunSeqIdempotent(t *testing.T) {
	repo, pool := newSkillRepo(t)
	ctx := context.Background()
	seedAgentRun(t, pool, 9101)

	if _, err := repo.RecordSkillUsage(ctx, 9101, "playwright-cli", true); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Replay the exact (run_id, seq) pair with a fresh row id.
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO skill_usage (id, run_id, seq, skill_name, found)
			VALUES ($1, 9101, 1, 'playwright-cli', true)
			ON CONFLICT (run_id, seq) DO NOTHING`, 990000+int64(i)); err != nil {
			t.Fatalf("idempotent replay: %v", err)
		}
	}
	rows, err := repo.ListSkillUsage(ctx, 9101)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows after replay = %#v, %v; want exactly 1", rows, err)
	}
}

// TestSkillUsageFKEnforced: run_id is a real same-context FK to agent_run; an
// unknown run fails closed instead of writing an orphan fact.
func TestSkillUsageFKEnforced(t *testing.T) {
	repo, _ := newSkillRepo(t)
	if _, err := repo.RecordSkillUsage(context.Background(), 424242, "playwright-cli", true); err == nil {
		t.Fatal("usage row written for a nonexistent agent_run")
	}
}

// TestSkillUsageAppendOnly: the ledger has no update/delete production path;
// the repository surface exposes append + read only (compile-time pin via the
// service port shape is in the unit tests; here we pin that repeated reads
// never mutate rows).
func TestSkillUsageAppendOnly(t *testing.T) {
	repo, pool := newSkillRepo(t)
	ctx := context.Background()
	seedAgentRun(t, pool, 9201)
	if _, err := repo.RecordSkillUsage(ctx, 9201, "api-recon", false); err != nil {
		t.Fatalf("record: %v", err)
	}
	before, err := repo.ListSkillUsage(ctx, 9201)
	if err != nil || len(before) != 1 {
		t.Fatalf("before = %#v, %v", before, err)
	}
	after, err := repo.ListSkillUsage(ctx, 9201)
	if err != nil || len(after) != 1 || after[0].ID != before[0].ID || after[0].Found != before[0].Found {
		t.Fatalf("read mutated the ledger: %#v -> %#v (%v)", before, after, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM skill_usage WHERE run_id = 9201`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count = %d, %v", count, err)
	}
}

var _ interface {
	RecordSkillUsage(ctx context.Context, runID int64, skillName string, found bool) (int, error)
} = (*agentpg.SkillRepository)(nil)

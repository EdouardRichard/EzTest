//go:build integration

package designpg_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// capturePublisher records the design events of one test.
type capturePublisher struct {
	events []eventingapp.Event
}

func (p *capturePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return nil
}

func (p *capturePublisher) count(eventType string) int {
	matches := 0
	for _, event := range p.events {
		if event.Type == eventType {
			matches++
		}
	}
	return matches
}

// seedDiscoveryProvenance creates the executed case version + observation chain
// the additive revision is derived from. evidenceRows controls the R-EX-6 low
// confidence floor.
func seedDiscoveryProvenance(t *testing.T, pool *pgxpool.Pool, observationID, waveID, caseRunID, executedCaseVersionID, resourceID, evidenceRows int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, version_sut_ids,
			environment_fingerprint, dedupe_key)
		VALUES ($1, 1001, $2, '["9001"]'::jsonb, repeat('a', 64), $3)
	`, caseRunID-100, executedCaseVersionID, "intent-"+strings.Repeat("0", 1)); err != nil {
		t.Fatalf("seed execution intent: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_wave (id, version_id, status) VALUES ($1, 1001, 'done')
	`, waveID); err != nil {
		t.Fatalf("seed run wave: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status)
		VALUES ($1, $2, $3, $4, 'passed')
	`, caseRunID, waveID, caseRunID-100, executedCaseVersionID); err != nil {
		t.Fatalf("seed case run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_discovery_observation (
			id, version_id, wave_id, case_run_id, version_sut_id, environment_id,
			kind, method, host, resource_template, decision, resource_id
		) VALUES ($1, 1001, $2, $3, 9001, 9001, 'page', '', 'orders.example.test',
		          '/orders/{id}', 'approved', $4)
	`, observationID, waveID, caseRunID, resourceID); err != nil {
		t.Fatalf("seed discovery observation: %v", err)
	}
	for index := int64(0); index < evidenceRows; index++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO evidence (id, case_run_id, kind, content_ref_id, sha256, size_bytes)
			VALUES ($1, $2, 'screenshot', $1, repeat('b', 64), 10)
		`, observationID*10+index, caseRunID); err != nil {
			t.Fatalf("seed evidence: %v", err)
		}
	}
}

func seedRequirementSource(t *testing.T, pool *pgxpool.Pool, caseVersionID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_source (id, case_version_id, kind, ref_id, path)
		VALUES ($1, $2, 'requirement', '3002', 'requirement')
	`, caseVersionID+900000, caseVersionID); err != nil {
		t.Fatalf("seed case source: %v", err)
	}
}

func newDiscoveryRevisionService(t *testing.T, pool *pgxpool.Pool) (*designapp.GraphService, *capturePublisher) {
	t.Helper()
	generator, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	publisher := &capturePublisher{}
	service := designapp.NewGraphService(designpg.New(pool), generator, clock.System{}, nil, publisher, nil)
	service.SetDiscoveryRevisionStore(designpg.NewRevisionStore(pool))
	return service, publisher
}

func seedDiscoveryBase(t *testing.T, pool *pgxpool.Pool, evidenceRows int64) {
	t.Helper()
	seedVersion(t, pool, 1001, false)
	seedVersionSUT(t, pool, 1001, 9001, []int64{6001}, nil)
	seedRequirement(t, pool, 1001, 3001, 3002, "clarified", "订单详情", 1)
	// The executed case version is the provenance anchor of the observation.
	seedScenario(t, pool, 1001, 3001, 3002, 5001, "已有场景", 0)
	seedCase(t, pool, 1001, 3001, 3002, 5001, 7001, 7002, 1, design.CaseStatusConfirmed, "已有用例")
	seedRequirementSource(t, pool, 7002)
	seedDiscoveryProvenance(t, pool, 8101, 9201, 9301, 7002, 6001, evidenceRows)
}

func TestDiscoveryRevisionAutoConfirmAndRollbackIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	seedDiscoveryBase(t, pool, 2)
	service, publisher := newDiscoveryRevisionService(t, pool)
	ctx := context.Background()

	beforeWaveIDs := currentWaveCaseRunIDs(t, pool)

	view, err := service.OnResourceConfirmed(ctx, 1001, 6001, 8101, 3001)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if view.Status != design.CaseStatusConfirmed || view.Trigger != design.CaseTriggerDiscoveryDelta {
		t.Fatalf("view = %+v, want confirmed discovery_delta", view)
	}
	if after := currentWaveCaseRunIDs(t, pool); strings.Join(after, ",") != strings.Join(beforeWaveIDs, ",") {
		t.Fatalf("current wave case_run ids changed: %v -> %v", beforeWaveIDs, after)
	}

	var status, trigger, autoConfirmedBy, observationID string
	if err := pool.QueryRow(ctx, `
		SELECT status, trigger, COALESCE(auto_confirmed_by, ''), COALESCE(source_observation_id, 0)::text
		FROM case_version WHERE id = $1
	`, view.CaseVersionID).Scan(&status, &trigger, &autoConfirmedBy, &observationID); err != nil {
		t.Fatalf("read auto-confirmed revision: %v", err)
	}
	if status != "confirmed" || trigger != "discovery_delta" || autoConfirmedBy != "system" || observationID != "8101" {
		t.Fatalf("revision = status=%q trigger=%q auto=%q observation=%q", status, trigger, autoConfirmedBy, observationID)
	}
	var autoConfirmed bool
	var confirmationTrigger string
	if err := pool.QueryRow(ctx, `
		SELECT auto_confirmed, trigger FROM case_confirmation WHERE case_version_id = $1
	`, view.CaseVersionID).Scan(&autoConfirmed, &confirmationTrigger); err != nil {
		t.Fatalf("read auto confirmation: %v", err)
	}
	if !autoConfirmed || confirmationTrigger != "discovery_delta" {
		t.Fatalf("confirmation = auto=%v trigger=%q", autoConfirmed, confirmationTrigger)
	}
	if publisher.count("case.revision.auto_confirmed") != 1 {
		t.Fatalf("auto-confirm events = %d, want 1", publisher.count("case.revision.auto_confirmed"))
	}

	// The next wave sees the new confirmed case; the current wave never did.
	executables, err := designpg.New(pool).ListExecutableCases(ctx, 1001)
	if err != nil {
		t.Fatalf("ListExecutableCases: %v", err)
	}
	seen := false
	for _, executable := range executables {
		if executable.Version.ID == view.CaseVersionID {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("next-wave executable set does not contain %d: %+v", view.CaseVersionID, executables)
	}

	// Idempotent repeat: no second revision/event.
	again, err := service.OnResourceConfirmed(ctx, 1001, 6001, 8101, 3001)
	if err != nil || again.CaseVersionID != view.CaseVersionID {
		t.Fatalf("repeat = %+v err=%v", again, err)
	}
	if publisher.count("case.revision.auto_confirmed") != 1 {
		t.Fatalf("repeat emitted a second auto-confirm event")
	}

	// Rollback: original superseded, replacement confirmed with rollback_of.
	replacement, err := service.Rollback(ctx, 1001, view.CaseVersionID, "探索误判", 9)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if replacement.RollbackOf != view.CaseVersionID || replacement.Status != design.CaseStatusConfirmed {
		t.Fatalf("replacement = %+v", replacement)
	}
	if got := caseVersionStatus(t, pool, view.CaseVersionID); got != "superseded" {
		t.Fatalf("original status = %q, want superseded", got)
	}
	var rollbackTrigger, rollbackOf string
	if err := pool.QueryRow(ctx, `
		SELECT trigger, COALESCE(rollback_of, 0)::text FROM case_version WHERE id = $1
	`, replacement.CaseVersionID).Scan(&rollbackTrigger, &rollbackOf); err != nil {
		t.Fatalf("read rollback replacement: %v", err)
	}
	if rollbackTrigger != "rollback" || rollbackOf != strconv.FormatInt(view.CaseVersionID, 10) {
		t.Fatalf("replacement trigger=%q rollback_of=%q", rollbackTrigger, rollbackOf)
	}
	if publisher.count("case.revision.auto_confirmed") != 2 {
		t.Fatalf("rollback event missing: %d", publisher.count("case.revision.auto_confirmed"))
	}
	repeat, err := service.Rollback(ctx, 1001, view.CaseVersionID, "探索误判", 9)
	if err != nil || repeat.CaseVersionID != replacement.CaseVersionID {
		t.Fatalf("rollback not idempotent: repeat=%+v err=%v", repeat, err)
	}
}

func TestDiscoveryRevisionLowConfidenceStaysDraftIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	seedDiscoveryBase(t, pool, 1)
	service, publisher := newDiscoveryRevisionService(t, pool)

	view, err := service.OnResourceConfirmed(context.Background(), 1001, 6001, 8101, 3001)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if view.Status != design.CaseStatusDraft || view.LowConfidenceReason != "insufficient_evidence" {
		t.Fatalf("view = %+v, want draft insufficient_evidence", view)
	}
	if got := caseVersionStatus(t, pool, view.CaseVersionID); got != "draft" {
		t.Fatalf("stored status = %q, want draft", got)
	}
	if publisher.count("case.revision.auto_confirmed") != 0 {
		t.Fatalf("low confidence emitted an auto-confirm event")
	}
}

func currentWaveCaseRunIDs(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id::text FROM case_run WHERE wave_id = 9201 ORDER BY id
	`)
	if err != nil {
		t.Fatalf("read current wave case runs: %v", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan case run id: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func caseVersionStatus(t *testing.T, pool *pgxpool.Pool, caseVersionID int64) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM case_version WHERE id = $1`, caseVersionID).Scan(&status); err != nil {
		t.Fatalf("read case version status: %v", err)
	}
	return status
}

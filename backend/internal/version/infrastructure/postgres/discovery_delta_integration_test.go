//go:build integration

package versionpg_test

// W1-12 integration proof over a real PostgreSQL: one execution observation is
// classified into a proposed resource + version_fact, the observation decision
// carries the same resource/fact ids, a re-run is a no-op, and an admin reject
// is never re-proposed.

import (
	"context"
	"strconv"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executionadapters "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/adapters"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcepg "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// TestDiscoveryDeltaWritesResourceFactAndObservation pins the three-table
// consistency of the delta pass and its wave-level idempotency.
func TestDiscoveryDeltaWritesResourceFactAndObservation(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	ctx := context.Background()

	const (
		versionID     = int64(7001)
		sutID         = int64(7101)
		environmentID = int64(7201)
		versionSUTID  = int64(7301)
		waveID        = int64(7401)
		caseRunID     = int64(7501)
	)
	createVersion(t, pool, versionID)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO sut (id, name, created_by) VALUES ($1, 'delta sut', 1)`, []any{sutID}},
		{`INSERT INTO sut_environment (id, sut_id, name, base_url) VALUES ($1, $2, 'test', 'https://shop.example.com')`,
			[]any{environmentID, sutID}},
		{`INSERT INTO version_sut (id, version_id, sut_id, environment_id) VALUES ($1, $2, $3, $4)`,
			[]any{versionSUTID, versionID, sutID, environmentID}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed %q: %v", statement.sql, err)
		}
	}

	executionRepo := executionpg.NewRepository(pool, ids)
	if err := executionRepo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{{
		Kind: "page", Host: "shop.example.com", Path: "/orders/42",
		ResourceTemplate: "/orders/{id}", MIME: "text/html",
		VersionID: versionID, VersionSUTID: versionSUTID, EnvironmentID: environmentID,
		WaveID: waveID, CaseRunID: caseRunID, Occurrences: 1,
	}}); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}

	resourceSvc := resourceapp.New(resourcepg.New(pool), ids, clock.System{}, nil, nil, nil)
	factSvc := versionapp.NewFactService(versionpg.NewFactRepository(pool, ids), ids, nil)
	observationAdapter := executionadapters.NewDiscoveryObservationAdapter(executionRepo.Observations)
	delta := versionapp.NewDiscoveryDelta(versionapp.DiscoveryDeltaDeps{
		Observations: observationAdapter,
		Decisions:    observationAdapter,
		Resources:    resourceSvc,
		Facts:        factSvc,
	})

	result, err := delta.Evaluate(ctx, versionID, waveID)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(result.New) != 1 {
		t.Fatalf("delta new = %d, want 1: %+v", len(result.New), result)
	}
	page, found, err := resourceSvc.FindPageByRouteTemplate(ctx, environmentID, "/orders/{id}")
	if err != nil || !found {
		t.Fatalf("find proposed page found=%v err=%v", found, err)
	}
	if page.Status != "proposed" || page.Source != "ai" {
		t.Fatalf("page = %+v, want proposed/ai", page)
	}
	facts, err := factSvc.Current(ctx, versionID, "page")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("page facts = %d, want 1", len(facts))
	}
	provenance, ok := facts[0].Payload["provenance"].(map[string]any)
	if !ok || provenance["wave_id"] != "7401" || provenance["case_run_id"] != "7501" {
		t.Fatalf("fact provenance = %#v", facts[0].Payload["provenance"])
	}

	var decision string
	var resourceID, factID *int64
	if err := pool.QueryRow(ctx, `
		SELECT decision, resource_id, fact_id FROM execution_discovery_observation
		WHERE version_id = $1 AND version_sut_id = $2
	`, versionID, versionSUTID).Scan(&decision, &resourceID, &factID); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if decision != "proposed" || resourceID == nil || factID == nil || *factID != facts[0].ID {
		t.Fatalf("observation = decision %q resource %v fact %v (want fact %d)", decision, resourceID, factID, facts[0].ID)
	}
	if page.ID == "" || resourceID == nil || *resourceID == 0 {
		t.Fatalf("resource ids = page %q observation %v", page.ID, resourceID)
	}
	t.Logf("db: page id=%s status=%s fact id=%d provenance=%v observation decision=%s resource=%d fact=%d",
		page.ID, page.Status, facts[0].ID, provenance, decision, *resourceID, *factID)

	// Re-running the same wave creates no second resource/fact and keeps the
	// decision proposed (idempotent).
	if _, err := delta.Evaluate(ctx, versionID, waveID); err != nil {
		t.Fatalf("re-run Evaluate: %v", err)
	}
	factsAfter, err := factSvc.Current(ctx, versionID, "page")
	if err != nil {
		t.Fatalf("Current facts after re-run: %v", err)
	}
	if len(factsAfter) != 1 {
		t.Fatalf("page facts after re-run = %d, want 1", len(factsAfter))
	}
	var pageCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sut_page WHERE environment_id = $1`, environmentID).Scan(&pageCount); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if pageCount != 1 {
		t.Fatalf("pages after re-run = %d, want 1", pageCount)
	}

	// An admin reject is classified rejected and never re-proposed.
	pageID, err := strconv.ParseInt(page.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse page id: %v", err)
	}
	if _, err := resourceSvc.RejectPage(ctx, pageID, 1); err != nil {
		t.Fatalf("RejectPage: %v", err)
	}
	rejected, err := delta.Evaluate(ctx, versionID, waveID)
	if err != nil {
		t.Fatalf("Evaluate after reject: %v", err)
	}
	if len(rejected.New) != 0 || len(rejected.Rejected) != 1 {
		t.Fatalf("delta after reject = new %d rejected %d: %+v", len(rejected.New), len(rejected.Rejected), rejected)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sut_page WHERE environment_id = $1`, environmentID).Scan(&pageCount); err != nil {
		t.Fatalf("count pages after reject: %v", err)
	}
	if pageCount != 1 {
		t.Fatalf("rejected page was re-proposed: %d rows", pageCount)
	}
	if err := pool.QueryRow(ctx, `
		SELECT decision FROM execution_discovery_observation WHERE version_id = $1 AND version_sut_id = $2
	`, versionID, versionSUTID).Scan(&decision); err != nil {
		t.Fatalf("read decision after reject: %v", err)
	}
	if decision != "rejected" {
		t.Fatalf("observation decision after reject = %q, want rejected", decision)
	}
}

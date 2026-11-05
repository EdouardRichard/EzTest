//go:build integration

package executionpg

import (
	"context"
	"strings"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDiscoveryObservationUpsertIdempotentUniqueKey proves the DB-29 upsert:
// the natural key keeps one row, occurrences accumulate, last_seen_at moves and
// version_id is resolved from the wave when the observation does not carry it.
func TestDiscoveryObservationUpsertIdempotentUniqueKey(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	mustCreateWave(t, repo, 9101, 100)

	observation := executionapp.DiscoveryObservation{
		Kind:             "api",
		Method:           "GET",
		Host:             "shop.example.com",
		Path:             "/api/orders/42?token=secret",
		ResourceTemplate: "/api/orders/{id}",
		MIME:             "application/json",
		VersionSUTID:     5001,
		WaveID:           9101,
		CaseRunID:        3001,
		Occurrences:      1,
	}
	if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{observation}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	observation.Occurrences = 2
	if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{observation}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var (
		versionID     int64
		environmentID *int64
		waveID        *int64
		caseRunID     *int64
		method        string
		host          string
		template      string
		sourceKind    string
		decision      string
		occurrences   int
		firstSeen     string
		lastSeen      string
	)
	err := pool.QueryRow(ctx, `
		SELECT version_id, environment_id, wave_id, case_run_id, method, host,
		       resource_template, source_kind, decision, occurrences,
		       first_seen_at::text, last_seen_at::text
		FROM execution_discovery_observation
		WHERE version_sut_id = 5001
	`).Scan(&versionID, &environmentID, &waveID, &caseRunID, &method, &host,
		&template, &sourceKind, &decision, &occurrences, &firstSeen, &lastSeen)
	if err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if versionID != 100 {
		t.Fatalf("version_id = %d, want the wave version 100", versionID)
	}
	if waveID == nil || *waveID != 9101 || caseRunID == nil || *caseRunID != 3001 {
		t.Fatalf("wave/case_run = %v/%v, want 9101/3001", waveID, caseRunID)
	}
	if method != "GET" || host != "shop.example.com" || template != "/api/orders/{id}" {
		t.Fatalf("row = method %q host %q template %q", method, host, template)
	}
	if sourceKind != "execution" || decision != "new" {
		t.Fatalf("source_kind/decision = %q/%q, want execution/new", sourceKind, decision)
	}
	if occurrences != 3 {
		t.Fatalf("occurrences = %d, want 1+2 = 3", occurrences)
	}
	t.Logf("db row: version_id=%d version_sut_id=5001 kind=api method=%s host=%s resource_template=%s decision=%s source_kind=%s occurrences=%d",
		versionID, method, host, template, decision, sourceKind, occurrences)
	if firstSeen == "" || lastSeen == "" {
		t.Fatal("first_seen_at/last_seen_at must be set")
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM execution_discovery_observation`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want the natural key to collapse the two writes into 1", count)
	}
}

// TestDiscoveryObservationUniqueKeyKeepsSUTsIsolated: the same template on two
// version_sut bindings is two rows.
func TestDiscoveryObservationUniqueKeyKeepsSUTsIsolated(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	mustCreateWave(t, repo, 9102, 101)

	base := executionapp.DiscoveryObservation{
		Kind: "page", Method: "", Host: "shop.example.com", ResourceTemplate: "/orders/{id}",
		MIME: "text/html", VersionSUTID: 5001, WaveID: 9102, CaseRunID: 3002, Occurrences: 1,
	}
	if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{base}); err != nil {
		t.Fatalf("upsert sut 5001: %v", err)
	}
	base.VersionSUTID = 5002
	if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{base}); err != nil {
		t.Fatalf("upsert sut 5002: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM execution_discovery_observation`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("rows = %d, want 2 (one per version_sut)", count)
	}
}

// TestDiscoveryObservationMissingVersionIsSkipped: an observation with neither
// version_id nor a resolvable wave is skipped without failing the batch.
func TestDiscoveryObservationMissingVersionIsSkipped(t *testing.T) {
	repo, pool := newTestRepository(t)
	err := repo.Observations.UpsertObservations(context.Background(), []executionapp.DiscoveryObservation{{
		Kind: "page", Host: "shop.example.com", ResourceTemplate: "/home",
		VersionSUTID: 5001, Occurrences: 1,
	}})
	if err != nil {
		t.Fatalf("upsert without version must not fail: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM execution_discovery_observation`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("rows = %d, want the impossible row skipped", count)
	}
}

// TestDiscoveryIngestStoresNoSensitiveFields runs the real ingest service over
// the real repository: the query/userinfo/fragment query never reach a column
// and the table has no header/cookie/body columns at all.
func TestDiscoveryIngestStoresNoSensitiveFields(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	mustCreateWave(t, repo, 9103, 102)

	ingest := executionapp.NewDiscoveryIngest(executionapp.DiscoveryIngestDeps{Store: repo.Observations})
	err := ingest.WriteObservations(ctx, []executionapp.DiscoveryObservation{{
		Kind: "page",
		// The raw-ish observation may still carry a query/fragment query; the
		// ingest normalization must drop it before the insert.
		Host:         "shop.example.com",
		Path:         "/orders/42?token=secret#/receipt?token=secret",
		MIME:         "text/html",
		VersionSUTID: 5001,
		WaveID:       9103,
		CaseRunID:    3003,
	}})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	var host, template, mime string
	if err := pool.QueryRow(ctx, `
		SELECT host, resource_template, mime FROM execution_discovery_observation
	`).Scan(&host, &template, &mime); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if template != "/orders/{id}#/receipt" {
		t.Fatalf("resource_template = %q, want /orders/{id}#/receipt", template)
	}
	t.Logf("redacted row: host=%s resource_template=%s mime=%s (no query/token/userinfo)", host, template, mime)
	for _, leaked := range []string{"token", "secret", "?", "alice", "pw"} {
		for _, value := range []string{host, template, mime} {
			if strings.Contains(value, leaked) {
				t.Fatalf("stored value leaked %q: %q", leaked, value)
			}
		}
	}

	forbidden := []string{"query", "querystring", "header", "headers", "cookie", "cookies", "body", "request_body", "response_body"}
	var leakedColumn string
	err = pool.QueryRow(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'execution_discovery_observation'
		  AND column_name = ANY($1)
		LIMIT 1
	`, forbidden).Scan(&leakedColumn)
	if err == nil {
		t.Fatalf("sensitive column %q exists on execution_discovery_observation", leakedColumn)
	}
}

// TestDiscoveryObservationUpsertKeepsExistingMIME: an empty EXCLUDED.mime no
// longer erases the MIME learned by an earlier request (W1-11);
// COALESCE(NULLIF(EXCLUDED.mime,”), existing.mime) is the merge rule.
func TestDiscoveryObservationUpsertKeepsExistingMIME(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	mustCreateWave(t, repo, 9105, 104)

	observation := executionapp.DiscoveryObservation{
		Kind: "page", Host: "shop.example.com", ResourceTemplate: "/home",
		MIME: "text/html", VersionSUTID: 5001, WaveID: 9105, Occurrences: 1,
	}
	if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{observation}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	observation.MIME = ""
	if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{observation}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var mime string
	if err := pool.QueryRow(ctx, `
		SELECT mime FROM execution_discovery_observation WHERE version_sut_id = 5001
	`).Scan(&mime); err != nil {
		t.Fatalf("read mime: %v", err)
	}
	if mime != "text/html" {
		t.Fatalf("mime = %q, want the existing text/html kept", mime)
	}
}

// TestDiscoveryObservationDeltaReadAndMark: the W1-12 cross-context read and
// decision write keep the DB-29 row consistent (observed fields, proposed ids,
// then a rejected decision that preserves provenance).
func TestDiscoveryObservationDeltaReadAndMark(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	mustCreateWave(t, repo, 9106, 105)

	if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{{
		Kind: "api", Method: "GET", Host: "shop.example.com",
		ResourceTemplate: "/api/orders/{id}", MIME: "application/json",
		StatusCode: intPtr(200), VersionSUTID: 5001, WaveID: 9106, CaseRunID: 3006, Occurrences: 1,
	}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := repo.Observations.ListForDelta(ctx, 105, 9106)
	if err != nil {
		t.Fatalf("ListForDelta: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.ObservationID == 0 || row.Kind != "api" || row.Method != "GET" ||
		row.ResourceTemplate != "/api/orders/{id}" || row.Decision != "new" ||
		row.WaveID != 9106 || row.CaseRunID != 3006 {
		t.Fatalf("delta row = %+v", row)
	}

	if err := repo.Observations.MarkDiscoveryObservationDecision(ctx, row.ObservationID, "proposed", 9001, 9002); err != nil {
		t.Fatalf("mark proposed: %v", err)
	}
	var decision string
	var resourceID, factID *int64
	if err := pool.QueryRow(ctx, `
		SELECT decision, resource_id, fact_id FROM execution_discovery_observation WHERE id = $1
	`, row.ObservationID).Scan(&decision, &resourceID, &factID); err != nil {
		t.Fatalf("read decision: %v", err)
	}
	if decision != "proposed" || resourceID == nil || *resourceID != 9001 || factID == nil || *factID != 9002 {
		t.Fatalf("marked row = decision %q resource %v fact %v", decision, resourceID, factID)
	}

	// A later rejected decision keeps the proposal provenance.
	if err := repo.Observations.MarkDiscoveryObservationDecision(ctx, row.ObservationID, "rejected", 0, 0); err != nil {
		t.Fatalf("mark rejected: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT decision, resource_id, fact_id FROM execution_discovery_observation WHERE id = $1
	`, row.ObservationID).Scan(&decision, &resourceID, &factID); err != nil {
		t.Fatalf("read rejected decision: %v", err)
	}
	if decision != "rejected" || resourceID == nil || *resourceID != 9001 || factID == nil || *factID != 9002 {
		t.Fatalf("rejected row = decision %q resource %v fact %v", decision, resourceID, factID)
	}

	// A row of another wave is only returned when it is still unclassified.
	rows, err = repo.Observations.ListForDelta(ctx, 105, 9999)
	if err != nil {
		t.Fatalf("ListForDelta other wave: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows for an unrelated wave = %d, want 0: %+v", len(rows), rows)
	}
}

func intPtr(value int) *int { return &value }

// seedDiscoveryBinding inserts the logical catalog rows the repository and the
// SUT host resolver read: one version/sut/environment binding plus one
// registered page on a second host.
func seedDiscoveryBinding(t *testing.T, pool *pgxpool.Pool, versionID, versionSUTID, environmentID int64) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO version (id, title, status, created_by) VALUES ($1, 'v0.22', 'initial', 1)`, []any{versionID}},
		{`INSERT INTO sut (id, name, created_by) VALUES ($1, 'shop', 1)`, []any{9001}},
		{`INSERT INTO sut_environment (id, sut_id, name, base_url) VALUES ($1, 9001, 'test', 'https://shop.example.com')`, []any{environmentID}},
		{`INSERT INTO version_sut (id, version_id, sut_id, environment_id) VALUES ($1, $2, 9001, $3)`,
			[]any{versionSUTID, versionID, environmentID}},
		{`INSERT INTO sut_page (id, sut_id, environment_id, name, url, route_template)
		  VALUES ($1, 9001, $2, 'home', 'https://www.shop.example.com/home', '/home')`,
			[]any{int64(7101), environmentID}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed binding: %v (%s)", err, statement.sql)
		}
	}
}

// TestDiscoveryObservationResolvesEnvironmentFromBinding: an observation with no
// environment_id is resolved through version_sut.
func TestDiscoveryObservationResolvesEnvironmentFromBinding(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	const versionID, versionSUTID, environmentID = int64(200), int64(5201), int64(7201)
	seedDiscoveryBinding(t, pool, versionID, versionSUTID, environmentID)

	err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{{
		Kind: "page", Host: "shop.example.com", ResourceTemplate: "/home",
		VersionID: versionID, VersionSUTID: versionSUTID, Occurrences: 1,
	}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var got int64
	if err := pool.QueryRow(ctx, `
		SELECT environment_id FROM execution_discovery_observation WHERE version_sut_id = $1
	`, versionSUTID).Scan(&got); err != nil {
		t.Fatalf("read environment_id: %v", err)
	}
	if got != environmentID {
		t.Fatalf("environment_id = %d, want %d", got, environmentID)
	}
}

// TestDiscoverySUTHostResolverIncludesBaseURLAndPages: the host resolver allows
// the environment base URL host and every registered page host.
func TestDiscoverySUTHostResolverIncludesBaseURLAndPages(t *testing.T) {
	_, pool := newTestRepository(t)
	seedDiscoveryBinding(t, pool, 201, 5202, 7202)

	hosts, err := NewDiscoverySUTHostResolver(pool).AllowedHosts(context.Background(), 5202)
	if err != nil {
		t.Fatalf("AllowedHosts: %v", err)
	}
	for _, host := range []string{"shop.example.com", "www.shop.example.com"} {
		if !hosts[host] {
			t.Fatalf("allowed hosts = %v, want %s", hosts, host)
		}
	}
	t.Logf("allowed hosts: %v", hosts)
}

// TestDiscoveryObservationListByVersion covers the W1-14A read API query: the
// full DB-29 projection, the optional wave/kind/decision filters, the
// first_seen_at DESC, id DESC order and the limit<=0 "no limit" contract used by
// the coverage aggregation.
func TestDiscoveryObservationListByVersion(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	const versionID, versionSUTID, environmentID = int64(210), int64(5210), int64(7210)
	seedDiscoveryBinding(t, pool, versionID, versionSUTID, environmentID)

	batch := []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "shop.example.com", ResourceTemplate: "/orders/{id}", MIME: "text/html",
			StatusCode: intPtr(200), VersionID: versionID, VersionSUTID: versionSUTID, WaveID: 9201, CaseRunID: 3201, Occurrences: 2},
		{Kind: "api", Method: "GET", Host: "shop.example.com", ResourceTemplate: "/api/orders/{id}", MIME: "application/json",
			StatusCode: intPtr(200), VersionID: versionID, VersionSUTID: versionSUTID, WaveID: 9201, CaseRunID: 3201, Occurrences: 5},
		{Kind: "page", Host: "shop.example.com", ResourceTemplate: "/login", MIME: "text/html",
			StatusCode: intPtr(200), VersionID: versionID, VersionSUTID: versionSUTID, WaveID: 9202, CaseRunID: 3202, Occurrences: 1},
	}
	if err := repo.Observations.UpsertObservations(ctx, batch); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := repo.Observations.ListByVersion(ctx, versionID, nil, "", "", 0)
	if err != nil {
		t.Fatalf("ListByVersion: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].ResourceTemplate != "/login" {
		t.Fatalf("newest row = %q, want /login (first_seen_at DESC, id DESC)", rows[0].ResourceTemplate)
	}
	var page executionapp.DiscoveryObservationRow
	found := false
	for _, row := range rows {
		if row.Kind == "page" && row.ResourceTemplate == "/orders/{id}" {
			page, found = row, true
		}
	}
	if !found {
		t.Fatalf("page row /orders/{id} not found: %+v", rows)
	}
	if page.Kind != "page" || page.Method != "" || page.Host != "shop.example.com" ||
		page.ResourceTemplate != "/orders/{id}" || page.MIME != "text/html" ||
		page.StatusCode == nil || *page.StatusCode != 200 || page.Occurrences != 2 ||
		page.Decision != "new" || page.WaveID == nil || *page.WaveID != 9201 ||
		page.CaseRunID == nil || *page.CaseRunID != 3201 ||
		page.EnvironmentID == nil || *page.EnvironmentID != environmentID ||
		page.ResourceID != nil || page.FactID != nil ||
		page.FirstSeenAt.IsZero() || page.LastSeenAt.IsZero() {
		t.Fatalf("page row = %+v", page)
	}

	pageRows, err := repo.Observations.ListByVersion(ctx, versionID, nil, "page", "", 0)
	if err != nil || len(pageRows) != 2 {
		t.Fatalf("kind=page rows = %d err=%v, want 2", len(pageRows), err)
	}
	newRows, err := repo.Observations.ListByVersion(ctx, versionID, nil, "", "new", 0)
	if err != nil || len(newRows) != 2 {
		t.Fatalf("decision=new rows = %d err=%v, want 2", len(newRows), err)
	}
	waveID := int64(9201)
	waveRows, err := repo.Observations.ListByVersion(ctx, versionID, &waveID, "", "", 0)
	if err != nil || len(waveRows) != 2 {
		t.Fatalf("wave 9201 rows = %d err=%v, want 2", len(waveRows), err)
	}
	limited, err := repo.Observations.ListByVersion(ctx, versionID, nil, "", "", 1)
	if err != nil || len(limited) != 1 || limited[0].ResourceTemplate != "/login" {
		t.Fatalf("limit=1 rows = %+v err=%v, want the newest only", limited, err)
	}
}

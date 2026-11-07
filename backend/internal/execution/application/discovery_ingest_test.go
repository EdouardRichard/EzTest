package executionapp_test

import (
	"context"
	"errors"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// fakeDiscoveryStore records the normalized rows the ingest service hands to
// the postgres adapter.
type fakeDiscoveryStore struct {
	rows []executionapp.DiscoveryObservation
	err  error
}

func (f *fakeDiscoveryStore) UpsertObservations(_ context.Context, rows []executionapp.DiscoveryObservation) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, rows...)
	return nil
}

// fakeDiscoveryHostResolver returns the allowed hosts of one version_sut.
type fakeDiscoveryHostResolver struct {
	hosts map[int64]map[string]bool
	err   error
}

func (f *fakeDiscoveryHostResolver) AllowedHosts(_ context.Context, versionSUTID int64) (map[string]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.hosts[versionSUTID], nil
}

func newDiscoveryIngest(store executionapp.DiscoveryObservationStore, hosts executionapp.SUTHostResolver) *executionapp.DiscoveryIngest {
	return executionapp.NewDiscoveryIngest(executionapp.DiscoveryIngestDeps{Store: store, Hosts: hosts})
}

// TestDiscoveryIngestDedupesAndNormalizes: two requests of the same version_sut
// collapse into one observation whose occurrences count, and the stored api
// template is the path template (method lives in its own column).
func TestDiscoveryIngestDedupesAndNormalizes(t *testing.T) {
	store := &fakeDiscoveryStore{}
	ingest := newDiscoveryIngest(store, nil)
	ctx := context.Background()

	err := ingest.WriteObservations(ctx, []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "sut.test", Path: "/orders/42?tab=x", MIME: "text/html", VersionSUTID: 7},
		{Kind: "page", Host: "sut.test", Path: "/orders/43?tab=y", MIME: "text/html", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/api/orders/42?q=1", MIME: "application/json", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/api/orders/43?q=2", MIME: "application/json", VersionSUTID: 7},
		{Kind: "page", Host: "sut.test", Path: "/app#/reports/42", MIME: "text/html", VersionSUTID: 7},
	})
	if err != nil {
		t.Fatalf("WriteObservations: %v", err)
	}
	if len(store.rows) != 3 {
		t.Fatalf("stored rows = %d, want 3 (page, api, hash page): %+v", len(store.rows), store.rows)
	}

	page := store.rows[0]
	if page.Kind != "page" || page.Method != "" || page.ResourceTemplate != "/orders/{id}" || page.Occurrences != 2 {
		t.Fatalf("page row = %+v, want method empty, template /orders/{id}, occurrences 2", page)
	}
	if page.Host != "sut.test" || page.VersionSUTID != 7 {
		t.Fatalf("page row lost its identity: %+v", page)
	}
	api := store.rows[1]
	if api.Kind != "api" || api.Method != "GET" || api.ResourceTemplate != "/api/orders/{id}" || api.Occurrences != 2 {
		t.Fatalf("api row = %+v, want GET /api/orders/{id} with occurrences 2", api)
	}
	hash := store.rows[2]
	if hash.ResourceTemplate != "/app#/reports/{id}" {
		t.Fatalf("hash route template = %q, want /app#/reports/{id}", hash.ResourceTemplate)
	}
	if hash.Occurrences != 1 {
		t.Fatalf("hash route occurrences = %d, want 1", hash.Occurrences)
	}
}

// TestDiscoveryIngestKeepsSUTsIsolated: the same path on two version_sut
// bindings is two rows (the DB-29 unique key includes version_sut_id).
func TestDiscoveryIngestKeepsSUTsIsolated(t *testing.T) {
	store := &fakeDiscoveryStore{}
	ingest := newDiscoveryIngest(store, nil)

	err := ingest.WriteObservations(context.Background(), []executionapp.DiscoveryObservation{
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/api/orders/1", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/api/orders/1", VersionSUTID: 8},
	})
	if err != nil {
		t.Fatalf("WriteObservations: %v", err)
	}
	if len(store.rows) != 2 {
		t.Fatalf("stored rows = %d, want one per version_sut", len(store.rows))
	}
}

// TestDiscoveryIngestFiltersThirdPartyStaticAndMIME covers the three R-EX-2
// filters: a third-party host, a static extension and a static MIME.
func TestDiscoveryIngestFiltersThirdPartyStaticAndMIME(t *testing.T) {
	store := &fakeDiscoveryStore{}
	ingest := newDiscoveryIngest(store, &fakeDiscoveryHostResolver{hosts: map[int64]map[string]bool{
		7: {"sut.test": true},
	}})

	err := ingest.WriteObservations(context.Background(), []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "cdn.other.test", Path: "/orders/1", MIME: "text/html", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/static/app.js", MIME: "application/javascript", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/assets/logo", MIME: "image/png", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/api/stream", MIME: "text/event-stream", ResourceType: "eventsource", VersionSUTID: 7},
		{Kind: "page", Host: "sut.test", Path: "/dashboard", MIME: "text/html", VersionSUTID: 7},
	})
	if err != nil {
		t.Fatalf("WriteObservations: %v", err)
	}
	if len(store.rows) != 2 {
		t.Fatalf("stored rows = %d, want the eventsource api and the page: %+v", len(store.rows), store.rows)
	}
	if store.rows[0].ResourceTemplate != "/api/stream" || store.rows[0].Kind != "api" {
		t.Fatalf("eventsource row = %+v, want api /api/stream", store.rows[0])
	}
	if store.rows[1].Kind != "page" || store.rows[1].ResourceTemplate != "/dashboard" {
		t.Fatalf("page row = %+v", store.rows[1])
	}
}

// TestDiscoveryIngestNoHostFilterWhenResolverNil: a nil resolver means every
// host is accepted (the composition root may run without a host resolver).
func TestDiscoveryIngestNoHostFilterWhenResolverNil(t *testing.T) {
	store := &fakeDiscoveryStore{}
	ingest := newDiscoveryIngest(store, nil)
	if err := ingest.WriteObservations(context.Background(), []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "cdn.other.test", Path: "/orders/1", MIME: "text/html", VersionSUTID: 7},
	}); err != nil {
		t.Fatalf("WriteObservations: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("stored rows = %d, want the third-party page (nil resolver)", len(store.rows))
	}
}

// TestDiscoveryIngestPropagatesIdentity: wave/case_run/version identities are
// forwarded so the repository can resolve version_id/environment_id.
func TestDiscoveryIngestPropagatesIdentity(t *testing.T) {
	store := &fakeDiscoveryStore{}
	ingest := newDiscoveryIngest(store, nil)
	if err := ingest.WriteObservations(context.Background(), []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "sut.test", Path: "/home", MIME: "text/html",
			VersionSUTID: 7, WaveID: 2001, CaseRunID: 3001},
	}); err != nil {
		t.Fatalf("WriteObservations: %v", err)
	}
	row := store.rows[0]
	if row.VersionSUTID != 7 || row.WaveID != 2001 || row.CaseRunID != 3001 || row.VersionID != 0 {
		t.Fatalf("identity = %+v, want version_sut 7, wave 2001, case_run 3001, version 0", row)
	}
}

// TestDiscoveryIngestNormalizesTrailingSlash: /orders/42 and /orders/42/ are the
// same page template; the root path keeps its slash.
func TestDiscoveryIngestNormalizesTrailingSlash(t *testing.T) {
	store := &fakeDiscoveryStore{}
	ingest := newDiscoveryIngest(store, nil)

	err := ingest.WriteObservations(context.Background(), []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "sut.test", Path: "/orders/42", MIME: "text/html", VersionSUTID: 7},
		{Kind: "page", Host: "sut.test", Path: "/orders/42/", MIME: "text/html", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/api/orders/42/", MIME: "application/json", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "sut.test", Path: "/api/orders/42", MIME: "application/json", VersionSUTID: 7},
		{Kind: "page", Host: "sut.test", Path: "/", MIME: "text/html", VersionSUTID: 8},
	})
	if err != nil {
		t.Fatalf("WriteObservations: %v", err)
	}
	if len(store.rows) != 3 {
		t.Fatalf("stored rows = %d, want page+api+root: %+v", len(store.rows), store.rows)
	}
	if store.rows[0].ResourceTemplate != "/orders/{id}" || store.rows[0].Occurrences != 2 {
		t.Fatalf("page row = %+v, want /orders/{id} occurrences 2", store.rows[0])
	}
	if store.rows[1].ResourceTemplate != "/api/orders/{id}" || store.rows[1].Occurrences != 2 {
		t.Fatalf("api row = %+v, want /api/orders/{id} occurrences 2", store.rows[1])
	}
	if store.rows[2].ResourceTemplate != "/" {
		t.Fatalf("root row = %+v, want /", store.rows[2])
	}
}

// TestDiscoveryIngestAllowsSameRegistrableDomain: an api subdomain of the
// environment host is first-party; an unrelated registrable domain is not.
func TestDiscoveryIngestAllowsSameRegistrableDomain(t *testing.T) {
	store := &fakeDiscoveryStore{}
	ingest := newDiscoveryIngest(store, &fakeDiscoveryHostResolver{hosts: map[int64]map[string]bool{
		7: {"shop.example.com": true},
	}})

	err := ingest.WriteObservations(context.Background(), []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "shop.example.com", Path: "/home", MIME: "text/html", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "api.shop.example.com", Path: "/api/orders/1", MIME: "application/json", VersionSUTID: 7},
		{Kind: "page", Host: "cdn.other.test", Path: "/orders/1", MIME: "text/html", VersionSUTID: 7},
		{Kind: "page", Host: "shop.example.com.evil.test", Path: "/orders/2", MIME: "text/html", VersionSUTID: 7},
	})
	if err != nil {
		t.Fatalf("WriteObservations: %v", err)
	}
	if len(store.rows) != 2 {
		t.Fatalf("stored rows = %d, want the env host and its api subdomain: %+v", len(store.rows), store.rows)
	}
	if store.rows[0].Host != "shop.example.com" || store.rows[1].Host != "api.shop.example.com" {
		t.Fatalf("allowed hosts = %q/%q", store.rows[0].Host, store.rows[1].Host)
	}
}

// TestDiscoveryIngestIPv6Host: an IPv6 literal observation must survive the URL
// rebuild (the host is bracketed before normalization).
func TestDiscoveryIngestIPv6Host(t *testing.T) {
	store := &fakeDiscoveryStore{}
	ingest := newDiscoveryIngest(store, nil)
	if err := ingest.WriteObservations(context.Background(), []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "::1", Path: "/orders/42", MIME: "text/html", VersionSUTID: 7},
		{Kind: "api", Method: "GET", Host: "::1", Path: "/api/orders/42", MIME: "application/json", VersionSUTID: 7},
	}); err != nil {
		t.Fatalf("WriteObservations: %v", err)
	}
	if len(store.rows) != 2 {
		t.Fatalf("stored rows = %d, want the IPv6 page and api: %+v", len(store.rows), store.rows)
	}
	if store.rows[0].Host != "::1" || store.rows[0].ResourceTemplate != "/orders/{id}" {
		t.Fatalf("ipv6 page = %+v", store.rows[0])
	}
	if store.rows[1].ResourceTemplate != "/api/orders/{id}" {
		t.Fatalf("ipv6 api = %+v", store.rows[1])
	}
}

// TestDiscoveryIngestReturnsStoreError: the capture is best effort, but the
// service still reports a store failure to its caller.
func TestDiscoveryIngestReturnsStoreError(t *testing.T) {
	want := errors.New("store down")
	store := &fakeDiscoveryStore{err: want}
	ingest := newDiscoveryIngest(store, nil)
	err := ingest.WriteObservations(context.Background(), []executionapp.DiscoveryObservation{
		{Kind: "page", Host: "sut.test", Path: "/home", MIME: "text/html", VersionSUTID: 7},
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

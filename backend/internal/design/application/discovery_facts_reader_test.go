package designapp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeProvenAssetFacts serves the neutral proven-asset read port from memory.
type fakeProvenAssetFacts struct {
	facts []ProvenAssetFact
	err   error
}

func (f *fakeProvenAssetFacts) ProvenAssetFacts(_ context.Context, _ int64) ([]ProvenAssetFact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]ProvenAssetFact(nil), f.facts...), nil
}

// fakeSavedSessionFacts serves the neutral saved-session read port from memory.
type fakeSavedSessionFacts struct {
	facts []SavedSessionFact
	err   error
}

func (f *fakeSavedSessionFacts) SavedSessionFacts(_ context.Context, _ int64) ([]SavedSessionFact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]SavedSessionFact(nil), f.facts...), nil
}

func producerFixtureSources() DiscoveryFactsSources {
	return DiscoveryFactsSources{
		Assets: &fakeProvenAssetFacts{facts: []ProvenAssetFact{
			{FactID: 11, SUTID: 7, AssetID: 101, AssetKind: "page", Name: "订单页", URL: "http://sut/orders", RouteTemplate: "/orders"},
			{FactID: 12, SUTID: 7, AssetID: 201, AssetKind: "api", Method: "POST", PathTemplate: "/orders", AuthRole: "bearer"},
		}},
		Sessions: &fakeSavedSessionFacts{facts: []SavedSessionFact{
			{FactID: 21, SUTID: 7, EnvironmentID: 3, Role: "admin"},
		}},
	}
}

// TestCaseAuthorFactsHasProducer pins SC-F10 (R-E7): the case_author discovery
// read face has a PRODUCER this version. page/api come from the P17 proven
// asset registrations, login_session from the P18 session fixture library;
// each of the three kinds has a writer (Current returns records when the
// execution facts exist) and the wired read surface renders them. Expectations
// come from the DiscoveryFactsReader/CaseAuthorFacts contract, not observed
// output.
func TestCaseAuthorFactsHasProducer(t *testing.T) {
	producer := NewDiscoveryFactsProducer(producerFixtureSources())
	ctx := context.Background()

	for _, tc := range []struct {
		kind   string
		wantID int64
		key    string
	}{
		{DiscoveryFactKindPage, 11, "page:http://sut/orders"},
		{DiscoveryFactKindAPI, 12, "api:POST /orders"},
		{DiscoveryFactKindLoginSession, 21, "sut:7:role:admin"},
	} {
		records, err := producer.Current(ctx, 1001, tc.kind)
		if err != nil {
			t.Fatalf("Current(%s): %v", tc.kind, err)
		}
		if len(records) != 1 {
			t.Fatalf("Current(%s) = %d records, want 1 (kind must have a writer): %+v", tc.kind, len(records), records)
		}
		if records[0].ID != tc.wantID || records[0].Kind != tc.kind || records[0].Key != tc.key {
			t.Fatalf("Current(%s) = %+v, want id=%d kind=%s key=%s", tc.kind, records[0], tc.wantID, tc.kind, tc.key)
		}
	}

	// The wired read surface (SetDiscoveryFacts) renders the produced facts.
	service := NewGraphService(nil, nil, nil, nil, nil, nil).SetDiscoveryFacts(producer)
	facts, err := service.CaseAuthorFacts(ctx, 1001)
	if err != nil {
		t.Fatalf("CaseAuthorFacts: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("CaseAuthorFacts = %d facts, want 3 (page/api/login_session): %+v", len(facts), facts)
	}
	wantKinds := []string{DiscoveryFactKindPage, DiscoveryFactKindAPI, DiscoveryFactKindLoginSession}
	for index, want := range wantKinds {
		if facts[index].Kind != want {
			t.Fatalf("facts[%d].kind = %q, want %q (kind order is frozen)", index, facts[index].Kind, want)
		}
	}
	byKey := make(map[string]DiscoveryFact, len(facts))
	for _, fact := range facts {
		byKey[fact.Key] = fact
	}
	if got := byKey["page:http://sut/orders"]; got.Summary != "/orders · 订单页" {
		t.Fatalf("page summary = %q, want %q", got.Summary, "/orders · 订单页")
	}
	if got := byKey["api:POST /orders"]; got.Summary != "POST /orders · bearer" {
		t.Fatalf("api summary = %q, want %q", got.Summary, "POST /orders · bearer")
	}
	if got := byKey["sut:7:role:admin"]; got.Summary != "admin" {
		t.Fatalf("login summary = %q, want %q", got.Summary, "admin")
	}
}

// TestGapKindReserved pins the SC-F10 scope narrowing: gap stays reserved with
// due version 0.3 (design §6.6 fact set has no gap; SC-E7 requires F to land the
// producer and promote by 0.3, NOT all four kinds this version). The producer
// must never emit a gap record — fabricating a gap producer is forbidden
// (controller ruling P3/SC-F10) — and the reserved annotation must stay.
func TestGapKindReserved(t *testing.T) {
	sources := producerFixtureSources()
	// Even a source that (wrongly) carries gap/db kinds must not produce them.
	sources.Assets = &fakeProvenAssetFacts{facts: []ProvenAssetFact{
		{FactID: 31, SUTID: 7, AssetID: 301, AssetKind: "gap", Name: "缺口"},
		{FactID: 32, SUTID: 7, AssetID: 302, AssetKind: "db", Name: "库"},
	}}
	producer := NewDiscoveryFactsProducer(sources)
	ctx := context.Background()

	records, err := producer.Current(ctx, 1001, DiscoveryFactKindGap)
	if err != nil {
		t.Fatalf("Current(gap): %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("Current(gap) = %+v, want empty (gap is reserved, due 0.3, no producer this version)", records)
	}
	wide, err := producer.Current(ctx, 1001, "")
	if err != nil {
		t.Fatalf("Current(\"\"): %v", err)
	}
	for _, record := range wide {
		if record.Kind == DiscoveryFactKindGap || record.Kind == "db" {
			t.Fatalf("widened read produced reserved/non-discovery kind: %+v", record)
		}
	}

	// The reserved annotation (due 0.3) must stay in case_author.go.
	source, err := os.ReadFile("case_author.go")
	if err != nil {
		t.Fatalf("read case_author.go: %v", err)
	}
	content := string(source)
	if !strings.Contains(content, "DiscoveryFactKindGap") ||
		!strings.Contains(content, "reserved") || !strings.Contains(content, "0.3") {
		t.Fatal("case_author.go must keep the gap reserved annotation with due version 0.3 (constitution art.3.7)")
	}
}

// TestDiscoveryFactsProducerLatestWins: the fact stores are append-only; a
// re-registered asset / re-saved fixture appends a new fact. The producer
// projects the LATEST fact per knowledge key, never a stale duplicate.
func TestDiscoveryFactsProducerLatestWins(t *testing.T) {
	producer := NewDiscoveryFactsProducer(DiscoveryFactsSources{
		Assets: &fakeProvenAssetFacts{facts: []ProvenAssetFact{
			{FactID: 11, SUTID: 7, AssetID: 101, AssetKind: "page", Name: "旧名", URL: "http://sut/orders", RouteTemplate: "/orders"},
			{FactID: 12, SUTID: 7, AssetID: 101, AssetKind: "page", Name: "新名", URL: "http://sut/orders", RouteTemplate: "/orders"},
		}},
		Sessions: &fakeSavedSessionFacts{facts: []SavedSessionFact{
			{FactID: 21, SUTID: 7, EnvironmentID: 3, Role: "admin"},
			{FactID: 22, SUTID: 7, EnvironmentID: 3, Role: "admin"},
		}},
	})
	ctx := context.Background()

	pages, err := producer.Current(ctx, 1001, DiscoveryFactKindPage)
	if err != nil || len(pages) != 1 {
		t.Fatalf("page records = %+v err = %v, want one (latest wins)", pages, err)
	}
	if pages[0].ID != 12 || pages[0].Payload["title"] != "新名" {
		t.Fatalf("page record = %+v, want the latest fact (id 12, 新名)", pages[0])
	}
	sessions, err := producer.Current(ctx, 1001, DiscoveryFactKindLoginSession)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("session records = %+v err = %v, want one (latest wins)", sessions, err)
	}
	if sessions[0].ID != 22 {
		t.Fatalf("session record = %+v, want the latest fact (id 22)", sessions[0])
	}
}

// TestDiscoveryFactsProducerNoFactsNoFabrication: a version without execution
// facts gets an empty projection — the producer never invents a record or an id
// (C T22 ruling: heuristic id fabrication is forgery). Nil sources (partial
// assembly) are the same safe empty state.
func TestDiscoveryFactsProducerNoFactsNoFabrication(t *testing.T) {
	ctx := context.Background()
	for name, producer := range map[string]DiscoveryFactsReader{
		"empty sources": NewDiscoveryFactsProducer(DiscoveryFactsSources{
			Assets:   &fakeProvenAssetFacts{},
			Sessions: &fakeSavedSessionFacts{},
		}),
		"nil sources": NewDiscoveryFactsProducer(DiscoveryFactsSources{}),
	} {
		for _, kind := range []string{"", DiscoveryFactKindPage, DiscoveryFactKindAPI, DiscoveryFactKindGap, DiscoveryFactKindLoginSession} {
			records, err := producer.Current(ctx, 1001, kind)
			if err != nil {
				t.Fatalf("%s Current(%q): %v", name, kind, err)
			}
			if len(records) != 0 {
				t.Fatalf("%s Current(%q) = %+v, want empty (no facts, no fabrication)", name, kind, records)
			}
		}
	}
}

// TestDiscoveryFactsProducerReadErrorPropagates: an infra read failure is
// returned (the CaseAuthorFacts read face then degrades best effort); it is
// never swallowed into a fake empty success at the producer layer.
func TestDiscoveryFactsProducerReadErrorPropagates(t *testing.T) {
	producer := NewDiscoveryFactsProducer(DiscoveryFactsSources{
		Assets:   &fakeProvenAssetFacts{err: errors.New("db down")},
		Sessions: &fakeSavedSessionFacts{err: errors.New("db down")},
	})
	ctx := context.Background()
	if _, err := producer.Current(ctx, 1001, DiscoveryFactKindPage); err == nil {
		t.Fatal("asset read failure must propagate")
	}
	if _, err := producer.Current(ctx, 1001, DiscoveryFactKindLoginSession); err == nil {
		t.Fatal("session read failure must propagate")
	}
	if _, err := producer.Current(ctx, 0, DiscoveryFactKindPage); err == nil {
		t.Fatal("Current must reject a non-positive version id")
	}
}

// TestDiscoveryFactsProducerCredentialFree: the login_session record carries
// the reuse key and role only — never the storage-state reference or any state
// bytes (the read face is credential-free by contract).
func TestDiscoveryFactsProducerCredentialFree(t *testing.T) {
	producer := NewDiscoveryFactsProducer(producerFixtureSources())
	records, err := producer.Current(context.Background(), 1001, DiscoveryFactKindLoginSession)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v err = %v", records, err)
	}
	for key := range records[0].Payload {
		if key != "account_role" {
			t.Fatalf("login_session payload carries unexpected key %q (credential-free contract)", key)
		}
	}
	if strings.Contains(records[0].Key, "storage") {
		t.Fatalf("login_session key leaks a storage reference: %q", records[0].Key)
	}
}

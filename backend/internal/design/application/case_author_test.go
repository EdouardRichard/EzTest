package designapp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeDiscoveryFacts implements DiscoveryFactsReader for the case-author facts
// test. It records the requested kinds and can fail one kind.
type fakeDiscoveryFacts struct {
	facts map[string][]DiscoveryFactRecord
	errs  map[string]error
	kinds []string
}

func (f *fakeDiscoveryFacts) Current(_ context.Context, _ int64, kind string) ([]DiscoveryFactRecord, error) {
	f.kinds = append(f.kinds, kind)
	if f.errs != nil {
		if err := f.errs[kind]; err != nil {
			return nil, err
		}
	}
	return append([]DiscoveryFactRecord(nil), f.facts[kind]...), nil
}

func TestCaseAuthorFacts(t *testing.T) {
	reader := &fakeDiscoveryFacts{
		facts: map[string][]DiscoveryFactRecord{
			DiscoveryFactKindPage: {
				{ID: 3, Kind: "page", Key: "page:http://sut/app", Payload: map[string]any{
					"route_template": "/app", "page_key": "http://sut/app", "title": "工作台",
					"url": "http://sut/app?token=SECRET", "elements": []any{map[string]any{"selector": "#x", "text": "SECRET-ELEMENT"}},
				}},
				{ID: 1, Kind: "page", Key: "page:http://sut/login", Payload: map[string]any{
					"route_template": "/login", "title": "登录",
				}},
			},
			DiscoveryFactKindAPI: {
				{ID: 2, Kind: "api", Key: "api:POST /orders", Payload: map[string]any{
					"api_key": "POST /orders", "state": "declared_used", "auth": "bearer",
					"params": []any{"token"},
				}},
			},
			DiscoveryFactKindGap: {
				{ID: 4, Kind: "gap", Key: "gap:req:301:acceptance", Payload: map[string]any{
					"dimension": "acceptance", "requirement_id": int64(301), "reason": "缺少验收标准",
				}},
			},
			DiscoveryFactKindLoginSession: {
				{ID: 5, Kind: "login_session", Key: "vs:7:role:admin", Payload: map[string]any{
					"account_role": "admin", "route": "/login", "storage_state_ref": "SECRET-REF",
				}},
			},
		},
	}
	service := NewGraphService(nil, nil, nil, nil, nil, nil).SetDiscoveryFacts(reader)

	facts, err := service.CaseAuthorFacts(context.Background(), 1001)
	if err != nil {
		t.Fatalf("CaseAuthorFacts: %v", err)
	}
	if len(facts) != 5 {
		t.Fatalf("facts = %d, want 5: %+v", len(facts), facts)
	}
	wantKinds := []string{"page", "page", "api", "gap", "login_session"}
	byKey := make(map[string]DiscoveryFact, len(facts))
	for index, want := range wantKinds {
		if facts[index].Kind != want {
			t.Fatalf("facts[%d].kind = %q, want %q (kind order is frozen)", index, facts[index].Kind, want)
		}
		byKey[facts[index].Key] = facts[index]
	}
	// Per-kind ordering is by fact id: page:http://sut/login (id 1) precedes
	// page:http://sut/app (id 3).
	if facts[0].Key != "page:http://sut/login" || facts[1].Key != "page:http://sut/app" {
		t.Fatalf("page facts are not id ordered: %+v", facts[:2])
	}
	if page := byKey["page:http://sut/app"]; page.Summary != "/app · 工作台" {
		t.Fatalf("page summary = %q", page.Summary)
	}
	if got := byKey["api:POST /orders"]; got.Summary != "POST /orders · declared_used · bearer" {
		t.Fatalf("api summary = %q", got.Summary)
	}
	if got := byKey["gap:req:301:acceptance"]; got.Summary != "acceptance · 301 · 缺少验收标准" {
		t.Fatalf("gap summary = %q", got.Summary)
	}
	if got := byKey["vs:7:role:admin"]; got.Summary != "admin · /login" {
		t.Fatalf("login summary = %q", got.Summary)
	}
	for _, fact := range facts {
		for _, forbidden := range []string{"SECRET", "password", "storage_state", "#x"} {
			if strings.Contains(fact.Summary, forbidden) || strings.Contains(fact.Key, forbidden) {
				t.Fatalf("case-author fact leaked %q: %+v", forbidden, fact)
			}
		}
	}

	// A missing reader and a read failure are best effort: the design graph
	// remains readable without exploration facts.
	empty := NewGraphService(nil, nil, nil, nil, nil, nil)
	facts, err = empty.CaseAuthorFacts(context.Background(), 1001)
	if err != nil || len(facts) != 0 {
		t.Fatalf("nil reader facts = %+v err = %v", facts, err)
	}
	failing := NewGraphService(nil, nil, nil, nil, nil, nil)
	failing.SetDiscoveryFacts(&fakeDiscoveryFacts{errs: map[string]error{DiscoveryFactKindAPI: errors.New("db down")}})
	facts, err = failing.CaseAuthorFacts(context.Background(), 1001)
	if err != nil || len(facts) != 0 {
		t.Fatalf("failing reader facts = %+v err = %v", facts, err)
	}

	if _, err := empty.CaseAuthorFacts(context.Background(), 0); err == nil {
		t.Fatal("CaseAuthorFacts must reject a non-positive version id")
	}
}

// TestCaseAuthorFactsBounds keeps one chatty version from flooding the design
// context.
func TestCaseAuthorFactsBounds(t *testing.T) {
	records := make([]DiscoveryFactRecord, 0, maxCaseAuthorFactsPerKind+5)
	for index := 0; index < maxCaseAuthorFactsPerKind+5; index++ {
		records = append(records, DiscoveryFactRecord{
			ID:      int64(index + 1),
			Kind:    DiscoveryFactKindPage,
			Key:     "page:p" + string(rune('a'+index%26)),
			Payload: map[string]any{"route_template": "/p"},
		})
	}
	service := NewGraphService(nil, nil, nil, nil, nil, nil)
	service.SetDiscoveryFacts(&fakeDiscoveryFacts{facts: map[string][]DiscoveryFactRecord{
		DiscoveryFactKindPage: records,
	}})
	facts, err := service.CaseAuthorFacts(context.Background(), 1001)
	if err != nil {
		t.Fatalf("CaseAuthorFacts: %v", err)
	}
	if len(facts) != maxCaseAuthorFactsPerKind {
		t.Fatalf("facts = %d, want per-kind cap %d", len(facts), maxCaseAuthorFactsPerKind)
	}
}

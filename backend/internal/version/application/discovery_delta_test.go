package versionapp_test

// W1-12 (WP-3) RED tests: deterministic five-way discovery delta classification,
// proposed resources, version_fact provenance and one wave-level resource.delta
// event. The fakes below are deliberately thin: the real FactService/FactRepository
// decide fact identity and conflict, so the provenance assertions exercise the
// production append path.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// deltaObservation is the read projection returned by the fake observation port.
type deltaObservation = versionapp.DiscoveryObservation

type fakeDeltaObservations struct {
	rows      []deltaObservation
	listErr   error
	decisions []deltaDecisionCall
}

type deltaDecisionCall struct {
	ObservationID int64
	Decision      string
	ResourceID    int64
	FactID        int64
}

func (f *fakeDeltaObservations) ListForDelta(_ context.Context, _ int64, _ int64) ([]deltaObservation, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]deltaObservation(nil), f.rows...), nil
}

func (f *fakeDeltaObservations) MarkDecision(_ context.Context, observationID int64, decision string, resourceID, factID int64) error {
	f.decisions = append(f.decisions, deltaDecisionCall{
		ObservationID: observationID, Decision: decision,
		ResourceID: resourceID, FactID: factID,
	})
	return nil
}

// fakeDeltaCatalog is an in-memory resource catalog. Its template lookup mirrors
// the production rtrim(('/') route key so the trailing-slash case is covered.
type fakeDeltaCatalog struct {
	pages     []resourceapp.PageView
	apis      []resourceapp.APIView
	nextID    int64
	created   int
	createErr error
}

func deltaTemplateKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	trimmed := strings.TrimRight(value, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

func (f *fakeDeltaCatalog) FindPageByRouteTemplate(_ context.Context, environmentID int64, routeTemplate string) (resourceapp.PageView, bool, error) {
	key := deltaTemplateKey(routeTemplate)
	for _, page := range f.pages {
		if page.EnvironmentID == strconv.FormatInt(environmentID, 10) && deltaTemplateKey(page.RouteTemplate) == key {
			return page, true, nil
		}
	}
	return resourceapp.PageView{}, false, nil
}

func (f *fakeDeltaCatalog) FindAPIByTemplate(_ context.Context, environmentID int64, method, pathTemplate string) (resourceapp.APIView, bool, error) {
	key := deltaTemplateKey(pathTemplate)
	for _, api := range f.apis {
		if api.EnvironmentID == strconv.FormatInt(environmentID, 10) &&
			strings.EqualFold(api.Method, method) && deltaTemplateKey(api.PathTemplate) == key {
			return api, true, nil
		}
	}
	return resourceapp.APIView{}, false, nil
}

func (f *fakeDeltaCatalog) CreateProposedPage(_ context.Context, input resourceapp.ProposedPageInput) (resourceapp.PageView, error) {
	if f.createErr != nil {
		return resourceapp.PageView{}, f.createErr
	}
	f.nextID++
	view := resourceapp.PageView{
		ID: strconv.FormatInt(f.nextID, 10), EnvironmentID: strconv.FormatInt(input.EnvironmentID, 10),
		Name: input.Name, URL: input.URL, RouteTemplate: input.RouteTemplate, PageType: input.PageType,
		Status: "proposed", Source: input.Source,
	}
	f.pages = append(f.pages, view)
	f.created++
	return view, nil
}

func (f *fakeDeltaCatalog) CreateProposedAPI(_ context.Context, input resourceapp.ProposedAPIInput) (resourceapp.APIView, error) {
	if f.createErr != nil {
		return resourceapp.APIView{}, f.createErr
	}
	f.nextID++
	view := resourceapp.APIView{
		ID: strconv.FormatInt(f.nextID, 10), EnvironmentID: strconv.FormatInt(input.EnvironmentID, 10),
		Name: input.Name, Method: input.Method, PathTemplate: input.PathTemplate,
		Status: "proposed", Source: input.Source,
	}
	f.apis = append(f.apis, view)
	f.created++
	return view, nil
}

type deltaFixture struct {
	svc       *versionapp.DiscoveryDelta
	obs       *fakeDeltaObservations
	catalog   *fakeDeltaCatalog
	factRepo  *fakeFactRepo
	factSvc   *versionapp.FactService
	publisher *fakePublisher
}

func newDeltaFixture(observations []deltaObservation) *deltaFixture {
	obs := &fakeDeltaObservations{rows: observations}
	catalog := &fakeDeltaCatalog{nextID: 5000}
	factRepo := newFakeFactRepo()
	publisher := &fakePublisher{}
	factSvc := versionapp.NewFactService(factRepo, &seqIDs{next: 100000}, publisher)
	svc := versionapp.NewDiscoveryDelta(versionapp.DiscoveryDeltaDeps{
		Observations: obs,
		Decisions:    obs,
		Resources:    catalog,
		Facts:        factSvc,
		Events:       publisher,
	})
	return &deltaFixture{svc: svc, obs: obs, catalog: catalog, factRepo: factRepo, factSvc: factSvc, publisher: publisher}
}

func baseDeltaObservations() []deltaObservation {
	return []deltaObservation{
		{ObservationID: 11, VersionID: 7, WaveID: 3, CaseRunID: 3001, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "/orders/{id}", Decision: "new"},
		{ObservationID: 12, VersionID: 7, WaveID: 3, CaseRunID: 3002, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "GET", Host: "shop.example.com", ResourceTemplate: "/api/orders/{id}", Decision: "new"},
		{ObservationID: 13, VersionID: 7, WaveID: 3, CaseRunID: 3003, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "/home", Decision: "new"},
		{ObservationID: 14, VersionID: 7, WaveID: 3, CaseRunID: 3004, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "GET", Host: "shop.example.com", ResourceTemplate: "/api/legacy/{id}", Decision: "new"},
	}
}

func seedRegisteredDeltaResources(catalog *fakeDeltaCatalog) {
	catalog.pages = append(catalog.pages, resourceapp.PageView{
		ID: "101", EnvironmentID: "9001", RouteTemplate: "/home", Status: "approved", Source: "admin",
	})
	catalog.apis = append(catalog.apis, resourceapp.APIView{
		ID: "102", EnvironmentID: "9001", Method: "GET", PathTemplate: "/api/legacy/{id}", Status: "rejected", Source: "code",
	})
}

func countDeltaEvents(p *fakePublisher, eventType string) int {
	count := 0
	for _, event := range p.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func factPayloadProvenance(t *testing.T, fact version.Fact) map[string]any {
	t.Helper()
	provenance, ok := fact.Payload["provenance"].(map[string]any)
	if !ok {
		t.Fatalf("fact %d payload provenance = %#v", fact.ID, fact.Payload["provenance"])
	}
	return provenance
}

// TestDeltaClassifiesAndRecords is the W1-12 acceptance test: one new page, one
// new api, one already registered page and one rejected api classify as
// new=2/out_of_scope=1/rejected=1, create two proposed resources, record facts
// with decimal-string provenance and publish exactly one resource.delta.
func TestDeltaClassifiesAndRecords(t *testing.T) {
	f := newDeltaFixture(baseDeltaObservations())
	seedRegisteredDeltaResources(f.catalog)

	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(result.New) != 2 || len(result.OutOfScope) != 1 || len(result.Rejected) != 1 || len(result.Proposed) != 0 {
		t.Fatalf("delta = new %d proposed %d approved %d rejected %d out_of_scope %d",
			len(result.New), len(result.Proposed), len(result.Approved), len(result.Rejected), len(result.OutOfScope))
	}
	if f.catalog.created != 2 {
		t.Fatalf("created resources = %d, want 2", f.catalog.created)
	}
	home := result.OutOfScope[0]
	if home.ResourceID != 101 || home.ResourceTemplate != "/home" {
		t.Fatalf("out_of_scope item = %+v, want registered page 101", home)
	}
	legacy := result.Rejected[0]
	if legacy.ResourceID != 102 || legacy.Kind != "api" {
		t.Fatalf("rejected item = %+v, want rejected api 102", legacy)
	}

	facts, err := f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want one page and one api fact: %+v", len(facts), facts)
	}
	kinds := map[string]version.Fact{}
	for _, fact := range facts {
		kinds[fact.Kind] = fact
		if fact.Source != "execution" {
			t.Fatalf("fact %d source = %q, want execution", fact.ID, fact.Source)
		}
		provenance := factPayloadProvenance(t, fact)
		for _, key := range []string{"wave_id", "case_run_id", "observation_id"} {
			value, ok := provenance[key].(string)
			if !ok {
				t.Fatalf("provenance[%q] = %#v, want a decimal string", key, provenance[key])
			}
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				t.Fatalf("provenance[%q] = %q, want decimal", key, value)
			}
		}
		if provenance["wave_id"] != "3" {
			t.Fatalf("wave provenance = %#v, want \"3\"", provenance["wave_id"])
		}
	}
	pageFact, ok := kinds["page"]
	if !ok || pageFact.Key != "/orders/{id}" || pageFact.Payload["route_template"] != "/orders/{id}" {
		t.Fatalf("page fact = %+v", pageFact)
	}
	if provenance := factPayloadProvenance(t, pageFact); provenance["case_run_id"] != "3001" || provenance["observation_id"] != "11" {
		t.Fatalf("page provenance = %#v", provenance)
	}
	apiFact, ok := kinds["api"]
	if !ok || apiFact.Key != "/api/orders/{id}" || apiFact.Payload["method"] != "GET" {
		t.Fatalf("api fact = %+v", apiFact)
	}

	if count := countDeltaEvents(f.publisher, "resource.delta"); count != 1 {
		t.Fatalf("resource.delta events = %d, want exactly 1 per wave: %+v", count, f.publisher.events)
	}
	var event eventingapp.Event
	for _, candidate := range f.publisher.events {
		if candidate.Type == "resource.delta" {
			event = candidate
		}
	}
	if event.Channel != "version:7" {
		t.Fatalf("event channel = %q, want version:7", event.Channel)
	}
	if event.Payload["version_id"] != "7" || event.Payload["wave_id"] != "3" {
		t.Fatalf("event payload identity = %#v", event.Payload)
	}
	items, ok := event.Payload["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("event items = %#v, want 2 new resources", event.Payload["items"])
	}
	seenTemplates := map[string]bool{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("event item = %#v", raw)
		}
		for _, key := range []string{"kind", "resource_template", "resource_id", "fact_id", "observation_id"} {
			if item[key] == nil || item[key] == "" {
				t.Fatalf("event item missing %q: %#v", key, item)
			}
		}
		seenTemplates[item["resource_template"].(string)] = true
	}
	if !seenTemplates["/orders/{id}"] || !seenTemplates["/api/orders/{id}"] {
		t.Fatalf("event templates = %v", seenTemplates)
	}

	proposed := 0
	rejected, outOfScope := 0, 0
	for _, decision := range f.obs.decisions {
		switch decision.Decision {
		case "proposed":
			proposed++
			if decision.ResourceID == 0 || decision.FactID == 0 {
				t.Fatalf("proposed decision lost ids: %+v", decision)
			}
		case "rejected":
			rejected++
		case "out_of_scope":
			outOfScope++
		}
	}
	if proposed != 2 || rejected != 1 || outOfScope != 1 {
		t.Fatalf("observation decisions = proposed %d rejected %d out_of_scope %d", proposed, rejected, outOfScope)
	}
	t.Logf("delta: new=%d proposed=%d approved=%d rejected=%d out_of_scope=%d",
		len(result.New), len(result.Proposed), len(result.Approved), len(result.Rejected), len(result.OutOfScope))
	for _, item := range append(append([]versionapp.DeltaItem{}, result.New...), result.OutOfScope...) {
		t.Logf("item: kind=%s method=%s template=%s host=%s env=%d sut=%d observation=%d resource=%d fact=%d -> %s",
			item.Kind, item.Method, item.ResourceTemplate, item.Host, item.EnvironmentID, item.VersionSUTID,
			item.ObservationID, item.ResourceID, item.FactID, item.Decision)
	}
	for _, fact := range facts {
		t.Logf("fact: id=%d kind=%s key=%s source=%s provenance=%v",
			fact.ID, fact.Kind, fact.Key, fact.Source, fact.Payload["provenance"])
	}
	t.Logf("resource.delta: channel=%s payload=%v", event.Channel, event.Payload)
	t.Logf("observation decisions: %+v", f.obs.decisions)
}

// TestDeltaIdempotentPerWave: repeating the same wave creates no second
// resource/fact/event and keeps the classification stable.
func TestDeltaIdempotentPerWave(t *testing.T) {
	f := newDeltaFixture(baseDeltaObservations())
	seedRegisteredDeltaResources(f.catalog)

	first, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}
	second, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if f.catalog.created != 2 {
		t.Fatalf("created resources = %d after a re-run, want 2", f.catalog.created)
	}
	if count := countDeltaEvents(f.publisher, "resource.delta"); count != 1 {
		t.Fatalf("resource.delta events = %d after a re-run, want 1", count)
	}
	facts, err := f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d after a re-run, want 2", len(facts))
	}
	// The first new resource became proposed, so the re-run classifies it as
	// proposed instead of new (no re-proposal and no duplicate event).
	if len(second.New) != 0 || len(second.Proposed) != 2 {
		t.Fatalf("second delta = new %d proposed %d", len(second.New), len(second.Proposed))
	}
	if len(first.Proposed) != 0 {
		t.Fatalf("first delta proposed = %d", len(first.Proposed))
	}
	t.Logf("idempotent rerun: created=%d facts=%d events=%d first(new=%d proposed=%d) second(new=%d proposed=%d)",
		f.catalog.created, len(facts), countDeltaEvents(f.publisher, "resource.delta"),
		len(first.New), len(first.Proposed), len(second.New), len(second.Proposed))
}

// TestDeltaRejectedResourceIsNotReproposed: after the admin rejects a proposed
// resource, a later wave classifies the observation rejected and creates nothing.
func TestDeltaRejectedResourceIsNotReproposed(t *testing.T) {
	f := newDeltaFixture(baseDeltaObservations())
	seedRegisteredDeltaResources(f.catalog)

	if _, err := f.svc.Evaluate(context.Background(), 7, 3); err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}
	// Reject the discovered API (the fake catalog row is the admin decision).
	for index := range f.catalog.apis {
		if f.catalog.apis[index].ID == "5002" {
			f.catalog.apis[index].Status = "rejected"
		}
	}
	createdBefore := f.catalog.created
	result, err := f.svc.Evaluate(context.Background(), 7, 4)
	if err != nil {
		t.Fatalf("Evaluate after reject: %v", err)
	}
	if f.catalog.created != createdBefore {
		t.Fatalf("a rejected resource was re-proposed: created %d -> %d", createdBefore, f.catalog.created)
	}
	if len(result.New) != 0 || len(result.Rejected) != 2 {
		t.Fatalf("delta after reject = new %d rejected %d, want 0/2: %+v", len(result.New), len(result.Rejected), result)
	}
	facts, err := f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts after reject = %d, want 2", len(facts))
	}
	if count := countDeltaEvents(f.publisher, "resource.delta"); count != 1 {
		t.Fatalf("resource.delta events = %d after a rejected re-run, want 1", count)
	}
}

// TestDeltaMultipleSUTsStayIsolated: the same route template on two environments
// is two resources, one per environment; a registered page only suppresses its
// own environment.
func TestDeltaMultipleSUTsStayIsolated(t *testing.T) {
	f := newDeltaFixture([]deltaObservation{
		{ObservationID: 21, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "a.example.com", ResourceTemplate: "/orders/{id}", Decision: "new"},
		{ObservationID: 22, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5002, EnvironmentID: 9002,
			Kind: "page", Host: "b.example.com", ResourceTemplate: "/orders/{id}", Decision: "new"},
	})
	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(result.New) != 2 {
		t.Fatalf("new = %d, want one page per environment: %+v", len(result.New), result.New)
	}
	environments := map[int64]bool{}
	for _, item := range result.New {
		environments[item.EnvironmentID] = true
	}
	if !environments[9001] || !environments[9002] {
		t.Fatalf("new environments = %v, want 9001 and 9002", environments)
	}
	if len(f.catalog.pages) != 2 || f.catalog.pages[0].EnvironmentID == f.catalog.pages[1].EnvironmentID {
		t.Fatalf("catalog pages = %+v, want one per environment", f.catalog.pages)
	}

	// Registering the page in environment 9001 only suppresses that environment.
	f.catalog.pages[0].Status = "approved"
	result, err = f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("Evaluate with one registered: %v", err)
	}
	if len(result.OutOfScope) != 1 || len(result.Proposed) != 1 {
		t.Fatalf("delta = out_of_scope %d proposed %d, want 1/1: %+v", len(result.OutOfScope), len(result.Proposed), result)
	}
	t.Logf("multi-SUT: created=%d pages=%+v second delta out_of_scope=%+v proposed=%+v",
		f.catalog.created, f.catalog.pages, result.OutOfScope, result.Proposed)
}

// TestDeltaDoesNotDuplicateP9Fact: a current P9 page fact of the same
// environment/template is reused (no second fact) while the newly proposed
// resource still publishes one resource.delta carrying the P9 fact id.
func TestDeltaDoesNotDuplicateP9Fact(t *testing.T) {
	f := newDeltaFixture([]deltaObservation{
		{ObservationID: 31, VersionID: 7, WaveID: 3, CaseRunID: 3001, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "/orders/{id}", Decision: "new"},
	})
	p9Payload := map[string]any{
		"page_key":       "https://shop.example.com/orders/{id}",
		"route_template": "/orders/{id}",
		"environment_id": int64(9001),
	}
	hash, err := version.FactContentHash(p9Payload)
	if err != nil {
		t.Fatalf("FactContentHash: %v", err)
	}
	f.factRepo.facts[7001] = version.Fact{
		ID: 7001, VersionID: 7, Kind: "page", Key: "page:env:9001:https://shop.example.com/orders/{id}",
		Payload: p9Payload, Source: "p9", ContentHash: hash,
	}
	f.factRepo.order = append(f.factRepo.order, 7001)

	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(result.New) != 1 || result.New[0].FactID != 7001 {
		t.Fatalf("new item = %+v, want the existing P9 fact 7001", result.New)
	}
	facts, err := f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 1 || facts[0].ID != 7001 {
		t.Fatalf("facts = %+v, want the single P9 fact reused", facts)
	}
	if count := countDeltaEvents(f.publisher, "resource.delta"); count != 1 {
		t.Fatalf("resource.delta events = %d, want 1", count)
	}
	t.Logf("P9 dedupe: facts=%+v new item fact_id=%d events=%d", facts, result.New[0].FactID,
		countDeltaEvents(f.publisher, "resource.delta"))
}

// TestDeltaMatchesTrailingSlashTemplate: the resource catalog lookup uses the
// normalized route key, so /orders/{id}/ does not create a duplicate of
// /orders/{id}.
func TestDeltaMatchesTrailingSlashTemplate(t *testing.T) {
	f := newDeltaFixture([]deltaObservation{
		{ObservationID: 41, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "/orders/{id}/", Decision: "new"},
	})
	f.catalog.pages = append(f.catalog.pages, resourceapp.PageView{
		ID: "101", EnvironmentID: "9001", RouteTemplate: "/orders/{id}", Status: "approved",
	})
	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(result.OutOfScope) != 1 || f.catalog.created != 0 {
		t.Fatalf("delta = %+v created=%d, want the trailing-slash template to match", result, f.catalog.created)
	}
}

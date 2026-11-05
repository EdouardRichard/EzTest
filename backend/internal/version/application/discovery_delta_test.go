package versionapp_test

// W1-12 (WP-3) RED tests: deterministic five-way discovery delta classification,
// proposed resources, version_fact provenance and one wave-level resource.delta
// event. The fakes below are deliberately thin: the real FactService/FactRepository
// decide fact identity and conflict, so the provenance assertions exercise the
// production append path.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
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
	// Mirror the production UPDATE: the decision is visible to a later pass and
	// a zero resource/fact id preserves the previous provenance.
	for index := range f.rows {
		if f.rows[index].ObservationID != observationID {
			continue
		}
		f.rows[index].Decision = decision
		if resourceID > 0 {
			f.rows[index].ResourceID = resourceID
		}
		if factID > 0 {
			f.rows[index].FactID = factID
		}
	}
	return nil
}

// fakeDeltaCatalog is an in-memory resource catalog. Its template lookup mirrors
// the production rtrim(('/') route key so the trailing-slash case is covered.
type fakeDeltaCatalog struct {
	pages      []resourceapp.PageView
	apis       []resourceapp.APIView
	nextID     int64
	created    int
	createErr  error
	createErrs map[string]error
}

// createError returns the injected failure of one proposal template. It models
// a per-observation validation failure without poisoning the whole wave.
func (f *fakeDeltaCatalog) createError(template string) error {
	if err, ok := f.createErrs[template]; ok {
		return err
	}
	return f.createErr
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
	if err := f.createError(input.RouteTemplate); err != nil {
		return resourceapp.PageView{}, err
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
	if err := f.createError(input.PathTemplate); err != nil {
		return resourceapp.APIView{}, err
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
	facts     versionapp.DiscoveryFactRecorder
	publisher *fakePublisher
}

func newDeltaFixture(observations []deltaObservation) *deltaFixture {
	return newDeltaFixtureWith(observations, nil, nil)
}

// newDeltaFixtureWith builds a delta service with an optional lock port and an
// optional fact-recorder wrapper (used to simulate a partial write failure).
func newDeltaFixtureWith(observations []deltaObservation, locker versionapp.DeltaLocker, wrapFacts func(versionapp.DiscoveryFactRecorder) versionapp.DiscoveryFactRecorder) *deltaFixture {
	obs := &fakeDeltaObservations{rows: observations}
	catalog := &fakeDeltaCatalog{nextID: 5000}
	factRepo := newFakeFactRepo()
	publisher := &fakePublisher{}
	factSvc := versionapp.NewFactService(factRepo, &seqIDs{next: 100000}, publisher)
	var facts versionapp.DiscoveryFactRecorder = factSvc
	if wrapFacts != nil {
		facts = wrapFacts(factSvc)
	}
	svc := versionapp.NewDiscoveryDelta(versionapp.DiscoveryDeltaDeps{
		Observations: obs,
		Decisions:    obs,
		Resources:    catalog,
		Facts:        facts,
		Events:       publisher,
		Locker:       locker,
	})
	return &deltaFixture{svc: svc, obs: obs, catalog: catalog, factRepo: factRepo, factSvc: factSvc, facts: facts, publisher: publisher}
}

// fakeDeltaLocker injects lock acquisition outcomes. A nil err models an
// acquired lock whose release function is counted.
type fakeDeltaLocker struct {
	err     error
	locks   int
	unlocks int
}

func (f *fakeDeltaLocker) LockDelta(_ context.Context, _, _ int64) (func(), error) {
	f.locks++
	if f.err != nil {
		return nil, f.err
	}
	return func() { f.unlocks++ }, nil
}

// flakyDeltaFacts fails the first Record calls, then delegates. It models a
// resource proposal that committed while the fact write failed.
type flakyDeltaFacts struct {
	inner    versionapp.DiscoveryFactRecorder
	failNext int
}

func (f *flakyDeltaFacts) Record(ctx context.Context, fact version.Fact) (version.Fact, bool, error) {
	if f.failNext > 0 {
		f.failNext--
		return version.Fact{}, false, errors.New("fact store unavailable")
	}
	return f.inner.Record(ctx, fact)
}

func (f *flakyDeltaFacts) Current(ctx context.Context, versionID int64, kind string) ([]version.Fact, error) {
	return f.inner.Current(ctx, versionID, kind)
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
	if !ok || pageFact.Key != "page:env:9001:/orders/{id}" || pageFact.Payload["route_template"] != "/orders/{id}" {
		t.Fatalf("page fact = %+v", pageFact)
	}
	if provenance := factPayloadProvenance(t, pageFact); provenance["case_run_id"] != "3001" || provenance["observation_id"] != "11" {
		t.Fatalf("page provenance = %#v", provenance)
	}
	apiFact, ok := kinds["api"]
	if !ok || apiFact.Key != "api:env:9001:GET /api/orders/{id}" || apiFact.Payload["method"] != "GET" {
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

// TestDeltaMultipleEnvironmentsStayIsolated: the same route template on two
// environments is two resources and two facts (one per environment); a
// registered page only suppresses its own environment.
func TestDeltaMultipleEnvironmentsStayIsolated(t *testing.T) {
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
	// The environment is part of the fact identity: two environments with the
	// same template must not report a bogus fact conflict.
	facts, err := f.factSvc.Current(context.Background(), 7, "page")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want one per environment: %+v", len(facts), facts)
	}
	keys := map[string]bool{}
	for _, fact := range facts {
		keys[fact.Key] = true
	}
	if !keys["page:env:9001:/orders/{id}"] || !keys["page:env:9002:/orders/{id}"] {
		t.Fatalf("fact keys = %v, want the environment in the key", keys)
	}
	if conflict := countDeltaEvents(f.publisher, "fact.conflict"); conflict != 0 {
		t.Fatalf("fact.conflict events = %d, want none across environments", conflict)
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
	t.Logf("multi-environment: created=%d pages=%+v second delta out_of_scope=%+v proposed=%+v",
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

// TestDeltaFactKeySeparatesEnvironmentAndMethod pins the P9 fact-key convention:
// the environment and the API method are part of fact identity, so two methods
// on one path or two environments on one template never look contradictory.
func TestDeltaFactKeySeparatesEnvironmentAndMethod(t *testing.T) {
	f := newDeltaFixture([]deltaObservation{
		{ObservationID: 71, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "/orders/{id}", Decision: "new"},
		{ObservationID: 72, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5002, EnvironmentID: 9002,
			Kind: "page", Host: "other.example.com", ResourceTemplate: "/orders/{id}", Decision: "new"},
		{ObservationID: 73, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "GET", Host: "shop.example.com", ResourceTemplate: "/api/orders/{id}", Decision: "new"},
		{ObservationID: 74, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "POST", Host: "shop.example.com", ResourceTemplate: "/api/orders/{id}", Decision: "new"},
		{ObservationID: 75, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5002, EnvironmentID: 9002,
			Kind: "api", Method: "GET", Host: "other.example.com", ResourceTemplate: "/api/orders/{id}", Decision: "new"},
	})
	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(result.New) != 5 || len(result.OutOfScope) != 0 || result.Skipped != 0 {
		t.Fatalf("delta = new %d out_of_scope %d skipped %d, want 5 new: %+v",
			len(result.New), len(result.OutOfScope), result.Skipped, result)
	}
	facts, err := f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 5 {
		t.Fatalf("facts = %d, want 5: %+v", len(facts), facts)
	}
	keys := make(map[string]bool, len(facts))
	for _, fact := range facts {
		if fact.SupersededBy != nil {
			t.Fatalf("fact %d was superseded instead of kept current: %+v", fact.ID, fact)
		}
		keys[fact.Key] = true
	}
	for _, want := range []string{
		"page:env:9001:/orders/{id}",
		"page:env:9002:/orders/{id}",
		"api:env:9001:GET /api/orders/{id}",
		"api:env:9001:POST /api/orders/{id}",
		"api:env:9002:GET /api/orders/{id}",
	} {
		if !keys[want] {
			t.Fatalf("fact keys = %v, missing %q", keys, want)
		}
	}
	if conflict := countDeltaEvents(f.publisher, "fact.conflict"); conflict != 0 {
		t.Fatalf("fact.conflict events = %d, want none: %+v", conflict, f.publisher.events)
	}
	t.Logf("fact identity: facts=%d keys=%v conflicts=0", len(facts), keys)
}

// TestDeltaRepairsMissingFactOnRetry pins the partial-failure repair path: the
// proposal committed but the fact write failed, so a re-run must backfill the
// fact id, persist the decision and publish the missing resource.delta.
func TestDeltaRepairsMissingFactOnRetry(t *testing.T) {
	flaky := &flakyDeltaFacts{failNext: 1}
	f := newDeltaFixtureWith([]deltaObservation{
		{ObservationID: 61, VersionID: 7, WaveID: 3, CaseRunID: 3001, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "POST", Host: "shop.example.com", ResourceTemplate: "/api/repair/{id}", Decision: "new"},
	}, nil, func(inner versionapp.DiscoveryFactRecorder) versionapp.DiscoveryFactRecorder {
		flaky.inner = inner
		return flaky
	})

	if _, err := f.svc.Evaluate(context.Background(), 7, 3); err == nil {
		t.Fatal("first Evaluate succeeded, want the injected fact failure")
	}
	if f.catalog.created != 1 {
		t.Fatalf("created resources = %d, want the proposal committed before the fact failure", f.catalog.created)
	}
	if len(f.obs.decisions) != 0 {
		t.Fatalf("decisions after the failed pass = %+v, want none", f.obs.decisions)
	}
	facts, err := f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts after the failed pass: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("facts after the failed pass = %+v, want none", facts)
	}

	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("retry Evaluate: %v", err)
	}
	if f.catalog.created != 1 {
		t.Fatalf("created resources after the retry = %d, want 1 (no second proposal)", f.catalog.created)
	}
	if len(result.New) != 0 || len(result.Proposed) != 1 {
		t.Fatalf("retry delta = new %d proposed %d, want 1 proposed repair: %+v",
			len(result.New), len(result.Proposed), result)
	}
	item := result.Proposed[0]
	if item.FactID == 0 || item.ResourceID == 0 {
		t.Fatalf("repaired item lost ids: %+v", item)
	}
	facts, err = f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts after the retry: %v", err)
	}
	if len(facts) != 1 || facts[0].ID != item.FactID {
		t.Fatalf("facts after the retry = %+v, want the repaired fact %d", facts, item.FactID)
	}
	proposedDecision := false
	for _, decision := range f.obs.decisions {
		if decision.Decision == "proposed" {
			proposedDecision = true
			if decision.ResourceID != item.ResourceID || decision.FactID != item.FactID {
				t.Fatalf("repaired decision = %+v, want resource %d fact %d", decision, item.ResourceID, item.FactID)
			}
		}
	}
	if !proposedDecision {
		t.Fatalf("no proposed decision after the repair: %+v", f.obs.decisions)
	}
	if count := countDeltaEvents(f.publisher, "resource.delta"); count != 1 {
		t.Fatalf("resource.delta events = %d, want the repaired event exactly once", count)
	}

	// A single repaired resource still carries the flattened top-level fields.
	var event eventingapp.Event
	for _, candidate := range f.publisher.events {
		if candidate.Type == "resource.delta" {
			event = candidate
		}
	}
	items, ok := event.Payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("repaired event items = %#v, want exactly one", event.Payload["items"])
	}
	eventItem, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("repaired event item = %#v", items[0])
	}
	for _, key := range []string{"kind", "method", "resource_template", "resource_id", "fact_id", "observation_id"} {
		if eventItem[key] == nil || eventItem[key] == "" {
			t.Fatalf("repaired event item missing %q: %#v", key, eventItem)
		}
	}
	if event.Payload["kind"] != "api" || event.Payload["method"] != "POST" ||
		event.Payload["resource_template"] != "/api/repair/{id}" ||
		event.Payload["resource_id"] != eventItem["resource_id"] ||
		event.Payload["fact_id"] != eventItem["fact_id"] ||
		event.Payload["observation_id"] != "61" {
		t.Fatalf("flat event payload = %#v, item = %#v", event.Payload, eventItem)
	}
	t.Logf("repair: fact=%d resource=%d decisions=%+v event=%v",
		item.FactID, item.ResourceID, f.obs.decisions, event.Payload)
}

// TestDeltaEvaluateLockBusySkipsWave: a concurrent evaluation that cannot take
// the wave lock returns ErrDeltaBusy and produces no event/decision.
func TestDeltaEvaluateLockBusySkipsWave(t *testing.T) {
	locker := &fakeDeltaLocker{}
	f := newDeltaFixtureWith(baseDeltaObservations(), locker, nil)
	seedRegisteredDeltaResources(f.catalog)

	if _, err := f.svc.Evaluate(context.Background(), 7, 3); err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}
	if locker.locks != 1 || locker.unlocks != 1 {
		t.Fatalf("locker calls = locks %d unlocks %d, want 1/1", locker.locks, locker.unlocks)
	}
	eventsBefore := countDeltaEvents(f.publisher, "resource.delta")
	createdBefore := f.catalog.created
	decisionsBefore := len(f.obs.decisions)

	locker.err = versionapp.ErrDeltaBusy
	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if !errors.Is(err, versionapp.ErrDeltaBusy) {
		t.Fatalf("busy Evaluate error = %v, want ErrDeltaBusy", err)
	}
	if len(result.New) != 0 || len(result.Proposed) != 0 || result.Skipped != 0 {
		t.Fatalf("busy Evaluate result = %+v, want an empty result", result)
	}
	if count := countDeltaEvents(f.publisher, "resource.delta"); count != eventsBefore {
		t.Fatalf("resource.delta events = %d after busy, want %d", count, eventsBefore)
	}
	if f.catalog.created != createdBefore {
		t.Fatalf("created resources = %d after busy, want %d", f.catalog.created, createdBefore)
	}
	if len(f.obs.decisions) != decisionsBefore {
		t.Fatalf("decisions = %d after busy, want %d", len(f.obs.decisions), decisionsBefore)
	}
}

// TestDeltaSkipsUnprocessableObservationsAndContinues: a bad observation is
// counted and skipped (no decision, no abort), so the healthy items of the wave
// still become resources/facts/events; the skip is stable on re-runs.
func TestDeltaSkipsUnprocessableObservationsAndContinues(t *testing.T) {
	f := newDeltaFixture([]deltaObservation{
		{ObservationID: 51, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "TRACE", Host: "shop.example.com", ResourceTemplate: "/api/trace", Decision: "new"},
		{ObservationID: 52, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "PROPFIND", Host: "shop.example.com", ResourceTemplate: "/api/propfind", Decision: "new"},
		{ObservationID: 53, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "", Host: "shop.example.com", ResourceTemplate: "/api/no-method", Decision: "new"},
		{ObservationID: 54, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "orders/{id}", Decision: "new"},
		{ObservationID: 55, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 0,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "/no-environment", Decision: "new"},
		{ObservationID: 56, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "POST", Host: "shop.example.com", ResourceTemplate: "/api/broken", Decision: "new"},
		{ObservationID: 57, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "/home", Decision: "new"},
		{ObservationID: 58, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "api", Method: "POST", Host: "shop.example.com", ResourceTemplate: "/api/orders", Decision: "new"},
	})
	f.catalog.createErrs = map[string]error{
		"/api/broken": platform.Invalid("path_template", "rejected by the proposal validator"),
	}

	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Skipped != 6 {
		t.Fatalf("Skipped = %d, want 6: %+v", result.Skipped, result)
	}
	if len(result.New) != 2 || f.catalog.created != 2 {
		t.Fatalf("delta new=%d created=%d, want 2/2: %+v", len(result.New), f.catalog.created, result)
	}
	bad := map[int64]bool{51: true, 52: true, 53: true, 54: true, 55: true, 56: true}
	for _, decision := range f.obs.decisions {
		if bad[decision.ObservationID] {
			t.Fatalf("unprocessable observation %d was decided: %+v", decision.ObservationID, decision)
		}
	}
	facts, err := f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want the 2 healthy items: %+v", len(facts), facts)
	}
	if count := countDeltaEvents(f.publisher, "resource.delta"); count != 1 {
		t.Fatalf("resource.delta events = %d, want 1", count)
	}

	second, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if second.Skipped != 6 {
		t.Fatalf("second Skipped = %d, want the same 6 skips", second.Skipped)
	}
	if len(second.New) != 0 || f.catalog.created != 2 {
		t.Fatalf("second delta new=%d created=%d, want the healthy items idempotent", len(second.New), f.catalog.created)
	}
	if count := countDeltaEvents(f.publisher, "resource.delta"); count != 1 {
		t.Fatalf("resource.delta events after a re-run = %d, want 1", count)
	}
	if facts, err = f.factSvc.Current(context.Background(), 7, ""); err != nil || len(facts) != 2 {
		t.Fatalf("facts after a re-run = %+v err=%v, want 2", facts, err)
	}
	t.Logf("poison isolation: first skipped=%d new=%d, second skipped=%d", result.Skipped, len(result.New), second.Skipped)
}

// TestDeltaApprovedObservationClassification pins the Approved class: an
// observation whose decision is already approved (written by the W1-14 admin
// approval) is classified approved instead of out_of_scope.
func TestDeltaApprovedObservationClassification(t *testing.T) {
	f := newDeltaFixture([]deltaObservation{
		{ObservationID: 81, VersionID: 7, WaveID: 3, CaseRunID: 1, VersionSUTID: 5001, EnvironmentID: 9001,
			Kind: "page", Host: "shop.example.com", ResourceTemplate: "/home", Decision: "approved"},
	})
	f.catalog.pages = append(f.catalog.pages, resourceapp.PageView{
		ID: "101", EnvironmentID: "9001", RouteTemplate: "/home", Status: "approved", Source: "ai",
	})

	result, err := f.svc.Evaluate(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(result.Approved) != 1 || len(result.OutOfScope) != 0 {
		t.Fatalf("delta = approved %d out_of_scope %d, want 1/0: %+v",
			len(result.Approved), len(result.OutOfScope), result)
	}
	if result.Approved[0].ResourceID != 101 || result.Approved[0].Decision != "approved" {
		t.Fatalf("approved item = %+v", result.Approved[0])
	}
	approvedDecision := false
	for _, decision := range f.obs.decisions {
		if decision.ObservationID == 81 && decision.Decision == "approved" {
			approvedDecision = true
		}
	}
	if !approvedDecision {
		t.Fatalf("approved decision was not persisted: %+v", f.obs.decisions)
	}
	facts, err := f.factSvc.Current(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("Current facts: %v", err)
	}
	if len(facts) != 0 || countDeltaEvents(f.publisher, "resource.delta") != 0 {
		t.Fatalf("approved observation wrote facts/events: facts=%+v events=%+v", facts, f.publisher.events)
	}
}

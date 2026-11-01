package searchapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeProvider is a configurable Provider. It records how many times it was
// called and with which arguments, so the aggregation tests can prove that each
// requested type costs exactly one provider call.
type fakeProvider struct {
	items []Item
	err   error

	calls int
	q     string
	limit int
}

func (f *fakeProvider) Search(_ context.Context, q string, limit int) ([]Item, error) {
	f.calls++
	f.q = q
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func item(id, title string) Item {
	return Item{
		ID:           id,
		Title:        title,
		Subtitle:     "版本 " + id,
		VersionID:    "11",
		VersionTitle: "版本甲",
		Status:       "running",
		URL:          "/versions/11?tab=overview",
	}
}

// allProviders wires one fake per search type.
func allProviders(itemsPerType int) (Providers, map[string]*fakeProvider) {
	fakes := make(map[string]*fakeProvider, len(AllTypes))
	build := func(idPrefix string) *fakeProvider {
		items := make([]Item, 0, itemsPerType)
		for index := 0; index < itemsPerType; index++ {
			items = append(items, item(idPrefix, idPrefix+"-"+strings.Repeat("x", index)))
		}
		provider := &fakeProvider{items: items}
		fakes[idPrefix] = provider
		return provider
	}
	return Providers{
		Version:     build(TypeVersion),
		Requirement: build(TypeRequirement),
		Case:        build(TypeCase),
		Issue:       build(TypeIssue),
		Report:      build(TypeReport),
	}, fakes
}

func groupTypes(snapshot Snapshot) []string {
	types := make([]string, 0, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		types = append(types, group.Type)
	}
	return types
}

func assertInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *platform.Error", err)
	}
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("error = %d/%s, want 400 invalid_argument", apiErr.Status, apiErr.Code)
	}
	if got := apiErr.Details["field"]; got != field {
		t.Fatalf("details.field = %v, want %q", got, field)
	}
}

func TestSearchAggregatesFiveGroupsInCanonicalOrder(t *testing.T) {
	providers, fakes := allProviders(1)
	service := NewService(providers)

	snapshot, err := service.Search(context.Background(), "  login  ", nil, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	wantTypes := []string{TypeVersion, TypeRequirement, TypeCase, TypeIssue, TypeReport}
	gotTypes := groupTypes(snapshot)
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("groups = %v, want %v", gotTypes, wantTypes)
	}
	for index, wantType := range wantTypes {
		if gotTypes[index] != wantType {
			t.Fatalf("group %d = %q, want %q", index, gotTypes[index], wantType)
		}
	}
	if snapshot.Total != 5 {
		t.Fatalf("total = %d, want 5", snapshot.Total)
	}
	if snapshot.Truncated || snapshot.Degraded {
		t.Fatalf("flags = truncated:%v degraded:%v, want false/false",
			snapshot.Truncated, snapshot.Degraded)
	}
	for typeName, fake := range fakes {
		if fake.calls != 1 {
			t.Fatalf("%s provider calls = %d, want 1", typeName, fake.calls)
		}
		if fake.q != "login" {
			t.Fatalf("%s provider q = %q, want trimmed %q", typeName, fake.q, "login")
		}
		if fake.limit != 5 {
			t.Fatalf("%s provider limit = %d, want 5", typeName, fake.limit)
		}
	}
}

func TestSearchFiltersRequestedTypesAndDefaultsLimit(t *testing.T) {
	providers, fakes := allProviders(1)
	service := NewService(providers)

	snapshot, err := service.Search(context.Background(), "login",
		[]string{" REPORT ", "case", "case"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	wantTypes := []string{TypeCase, TypeReport}
	gotTypes := groupTypes(snapshot)
	if len(gotTypes) != len(wantTypes) || gotTypes[0] != wantTypes[0] || gotTypes[1] != wantTypes[1] {
		t.Fatalf("groups = %v, want %v", gotTypes, wantTypes)
	}
	for typeName, fake := range fakes {
		wantCalls := 0
		if typeName == TypeCase || typeName == TypeReport {
			wantCalls = 1
		}
		if fake.calls != wantCalls {
			t.Fatalf("%s provider calls = %d, want %d", typeName, fake.calls, wantCalls)
		}
		if wantCalls == 1 && fake.limit != DefaultLimit {
			t.Fatalf("%s provider limit = %d, want default %d",
				typeName, fake.limit, DefaultLimit)
		}
	}
	if snapshot.Total != 2 {
		t.Fatalf("total = %d, want 2", snapshot.Total)
	}
}

func TestSearchDegradesOnlyTheFailingGroup(t *testing.T) {
	providers, fakes := allProviders(1)
	fakes[TypeVersion].err = errors.New("version read failed")
	service := NewService(providers)

	snapshot, err := service.Search(context.Background(), "login", nil, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !snapshot.Degraded {
		t.Fatal("degraded = false, want true when one provider fails")
	}
	if snapshot.Total != 4 {
		t.Fatalf("total = %d, want 4 healthy items", snapshot.Total)
	}
	if len(snapshot.Groups) != 5 {
		t.Fatalf("groups = %d, want all five requested groups", len(snapshot.Groups))
	}
	if len(snapshot.Groups[0].Items) != 0 {
		t.Fatalf("failing version group items = %v, want empty", snapshot.Groups[0].Items)
	}
	if snapshot.Groups[0].Items == nil {
		t.Fatal("failing group items is nil, want []")
	}
	if len(snapshot.Groups[1].Items) != 1 {
		t.Fatalf("healthy requirement group items = %d, want 1", len(snapshot.Groups[1].Items))
	}
}

func TestSearchAllProvidersFailingReturnsEmptySnapshot(t *testing.T) {
	providers, fakes := allProviders(1)
	for _, fake := range fakes {
		fake.err = errors.New("provider unavailable")
	}
	service := NewService(providers)

	snapshot, err := service.Search(context.Background(), "login", nil, 5)
	if err != nil {
		t.Fatalf("Search: %v (all-provider failure must not 5xx)", err)
	}
	if !snapshot.Degraded {
		t.Fatal("degraded = false, want true")
	}
	if snapshot.Total != 0 {
		t.Fatalf("total = %d, want 0", snapshot.Total)
	}
	if len(snapshot.Groups) != 5 {
		t.Fatalf("groups = %d, want the five requested groups", len(snapshot.Groups))
	}
	for _, group := range snapshot.Groups {
		if group.Items == nil {
			t.Fatalf("group %s items is nil, want []", group.Type)
		}
		if len(group.Items) != 0 {
			t.Fatalf("group %s items = %v, want empty", group.Type, group.Items)
		}
	}
}

func TestSearchNilProviderDegrades(t *testing.T) {
	providers, fakes := allProviders(1)
	providers.Report = nil
	service := NewService(providers)

	snapshot, err := service.Search(context.Background(), "login", nil, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !snapshot.Degraded {
		t.Fatal("degraded = false, want true for a nil provider")
	}
	if fakes[TypeReport].calls != 0 {
		t.Fatalf("nil provider still called %d times", fakes[TypeReport].calls)
	}
	if got := snapshot.Total; got != 4 {
		t.Fatalf("total = %d, want 4", got)
	}
}

func TestSearchRejectsInvalidArguments(t *testing.T) {
	providers, fakes := allProviders(1)
	service := NewService(providers)

	t.Run("blank q", func(t *testing.T) {
		_, err := service.Search(context.Background(), "   ", nil, 5)
		assertInvalidField(t, err, "q")
	})

	t.Run("q over 100 runes", func(t *testing.T) {
		_, err := service.Search(context.Background(), strings.Repeat("中", 101), nil, 5)
		assertInvalidField(t, err, "q")
	})

	t.Run("unknown type", func(t *testing.T) {
		_, err := service.Search(context.Background(), "login", []string{"version", "bogus"}, 5)
		assertInvalidField(t, err, "types")
	})

	t.Run("only blank type", func(t *testing.T) {
		_, err := service.Search(context.Background(), "login", []string{"  "}, 5)
		assertInvalidField(t, err, "types")
	})

	t.Run("limit over 20", func(t *testing.T) {
		_, err := service.Search(context.Background(), "login", nil, 21)
		assertInvalidField(t, err, "limit")
	})

	t.Run("negative limit", func(t *testing.T) {
		_, err := service.Search(context.Background(), "login", nil, -1)
		assertInvalidField(t, err, "limit")
	})

	for typeName, fake := range fakes {
		if fake.calls != 0 {
			t.Fatalf("%s provider called %d times on a rejected request", typeName, fake.calls)
		}
	}
}

func TestSearchAcceptsBoundaryQueryLength(t *testing.T) {
	providers, fakes := allProviders(1)
	service := NewService(providers)

	if _, err := service.Search(context.Background(), strings.Repeat("中", 100), nil, 5); err != nil {
		t.Fatalf("Search with a 100-rune query: %v", err)
	}
	if fakes[TypeVersion].calls != 1 {
		t.Fatalf("version provider calls = %d, want 1", fakes[TypeVersion].calls)
	}
}

func TestSearchMarksTruncatedAtPerTypeLimit(t *testing.T) {
	providers, _ := allProviders(1)
	providers.Case = &fakeProvider{items: []Item{
		item("case", "one"), item("case", "two"), item("case", "three"),
	}}
	service := NewService(providers)

	snapshot, err := service.Search(context.Background(), "login", []string{"case"}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !snapshot.Truncated {
		t.Fatal("truncated = false, want true when a provider hits the limit")
	}
	if got := len(snapshot.Groups[0].Items); got != 2 {
		t.Fatalf("case items = %d, want the per-type limit 2", got)
	}
	if snapshot.Total != 2 {
		t.Fatalf("total = %d, want 2", snapshot.Total)
	}
}

func TestSearchUnderLimitIsNotTruncated(t *testing.T) {
	providers, _ := allProviders(1)
	service := NewService(providers)

	snapshot, err := service.Search(context.Background(), "login", nil, 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if snapshot.Truncated {
		t.Fatal("truncated = true for a result below the limit")
	}
}

func TestSearchNilServiceIsUnavailable(t *testing.T) {
	var service *Service
	if _, err := service.Search(context.Background(), "login", nil, 5); err == nil {
		t.Fatal("nil service Search returned nil error")
	}
}

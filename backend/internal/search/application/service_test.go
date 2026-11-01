package searchapp

import (
	"context"
	"errors"
	"reflect"
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

// --- version title decoration -------------------------------------------------

// fakeTitleReader is the narrow version application port used to decorate
// items whose provider cannot read another context's version titles. It records
// every batch so the tests can prove a single deduplicated call.
type fakeTitleReader struct {
	titles map[int64]string
	err    error

	calls int
	ids   [][]int64
}

func (f *fakeTitleReader) VersionTitles(_ context.Context, ids []int64) (map[int64]string, error) {
	f.calls++
	f.ids = append(f.ids, append([]int64(nil), ids...))
	if f.err != nil {
		return nil, f.err
	}
	return f.titles, nil
}

func versionedItem(id, versionID, versionTitle string) Item {
	return Item{
		ID:           id,
		Title:        "结果 " + id,
		VersionID:    versionID,
		VersionTitle: versionTitle,
		Status:       "running",
		URL:          "/versions/" + versionID + "?tab=overview",
	}
}

func groupTitle(snapshot Snapshot, typeName, id string) string {
	for _, group := range snapshot.Groups {
		if group.Type != typeName {
			continue
		}
		for _, it := range group.Items {
			if it.ID == id {
				return it.VersionTitle
			}
		}
	}
	return ""
}

func TestSearchDecoratesMissingVersionTitlesInOneBatch(t *testing.T) {
	providers := Providers{
		Version:     &fakeProvider{items: []Item{versionedItem("1", "10", "版本十")}},
		Requirement: &fakeProvider{items: []Item{versionedItem("2", "10", "")}},
		Case: &fakeProvider{items: []Item{
			versionedItem("3", "11", ""),
			versionedItem("4", "12", ""),
		}},
		Issue:  &fakeProvider{items: []Item{versionedItem("5", "12", "")}},
		Report: &fakeProvider{items: []Item{versionedItem("6", "0", "")}},
	}
	titles := &fakeTitleReader{titles: map[int64]string{
		10: "版本十",
		11: "版本十一",
		12: "版本十二",
	}}

	snapshot, err := NewService(providers).WithVersionTitles(titles).
		Search(context.Background(), "login", nil, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if titles.calls != 1 {
		t.Fatalf("VersionTitles calls = %d, want 1 batch call", titles.calls)
	}
	if want := []int64{10, 11, 12}; !reflect.DeepEqual(titles.ids[0], want) {
		t.Fatalf("VersionTitles ids = %v, want deduplicated %v", titles.ids[0], want)
	}
	if got := groupTitle(snapshot, TypeRequirement, "2"); got != "版本十" {
		t.Fatalf("requirement version_title = %q, want decorated %q", got, "版本十")
	}
	if got := groupTitle(snapshot, TypeCase, "3"); got != "版本十一" {
		t.Fatalf("case version_title = %q, want decorated %q", got, "版本十一")
	}
	if got := groupTitle(snapshot, TypeCase, "4"); got != "版本十二" {
		t.Fatalf("case version_title = %q, want decorated %q", got, "版本十二")
	}
	if got := groupTitle(snapshot, TypeIssue, "5"); got != "版本十二" {
		t.Fatalf("issue version_title = %q, want decorated %q", got, "版本十二")
	}
	if got := groupTitle(snapshot, TypeVersion, "1"); got != "版本十" {
		t.Fatalf("prefilled version_title = %q, want unchanged %q", got, "版本十")
	}
	if got := groupTitle(snapshot, TypeReport, "6"); got != "" {
		t.Fatalf("report with version_id 0 = %q, want empty", got)
	}
	if snapshot.Degraded {
		t.Fatal("degraded = true after a healthy title read")
	}
}

func TestSearchSkipsVersionTitleReadWhenNoTitleMissing(t *testing.T) {
	providers, _ := allProviders(1)
	titles := &fakeTitleReader{titles: map[int64]string{11: "版本甲"}}

	if _, err := NewService(providers).WithVersionTitles(titles).
		Search(context.Background(), "login", nil, 5); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if titles.calls != 0 {
		t.Fatalf("VersionTitles calls = %d, want 0 when every title is present", titles.calls)
	}
}

func TestSearchSkipsEmptyAndNonPositiveVersionIDs(t *testing.T) {
	providers := Providers{Case: &fakeProvider{items: []Item{
		versionedItem("1", "", ""),
		versionedItem("2", "0", ""),
		versionedItem("3", "-7", ""),
		versionedItem("4", "not-a-number", ""),
	}}}
	titles := &fakeTitleReader{titles: map[int64]string{11: "版本十一"}}

	snapshot, err := NewService(providers).WithVersionTitles(titles).
		Search(context.Background(), "login", []string{TypeCase}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if titles.calls != 0 {
		t.Fatalf("VersionTitles calls = %d, want 0 without a usable version ID", titles.calls)
	}
	for _, id := range []string{"1", "2", "3", "4"} {
		if got := groupTitle(snapshot, TypeCase, id); got != "" {
			t.Fatalf("case %s version_title = %q, want empty", id, got)
		}
	}
}

func TestSearchTitleReaderFailureDegradesTheSnapshot(t *testing.T) {
	providers := Providers{
		Version: &fakeProvider{items: []Item{versionedItem("1", "10", "版本十")}},
		Case:    &fakeProvider{items: []Item{versionedItem("2", "11", "")}},
	}
	titles := &fakeTitleReader{err: errors.New("version titles unavailable")}

	snapshot, err := NewService(providers).WithVersionTitles(titles).
		Search(context.Background(), "login", []string{TypeVersion, TypeCase}, 5)
	if err != nil {
		t.Fatalf("Search: %v (title failure must degrade, not fail)", err)
	}
	if !snapshot.Degraded {
		t.Fatal("degraded = false, want true when the title read fails")
	}
	if got := groupTitle(snapshot, TypeCase, "2"); got != "" {
		t.Fatalf("case version_title = %q, want empty on title failure", got)
	}
	if got := groupTitle(snapshot, TypeVersion, "1"); got != "版本十" {
		t.Fatalf("prefilled version_title = %q, want unchanged %q", got, "版本十")
	}
	if snapshot.Total != 2 {
		t.Fatalf("total = %d, want the two provider items", snapshot.Total)
	}
}

func TestSearchWithoutTitleReaderKeepsTitlesEmpty(t *testing.T) {
	providers := Providers{Case: &fakeProvider{items: []Item{versionedItem("1", "11", "")}}}

	snapshot, err := NewService(providers).
		Search(context.Background(), "login", []string{TypeCase}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := groupTitle(snapshot, TypeCase, "1"); got != "" {
		t.Fatalf("case version_title = %q, want empty without a wired reader", got)
	}
	if snapshot.Degraded {
		t.Fatal("degraded = true without a title reader, want an unwired decoration to stay silent")
	}
}

package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	search "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// fakeProvider is the per-context search provider seam (spec §3.3/D55). It
// records the forwarded keyword/limit and serves canned groups.
type fakeProvider struct {
	groups []search.Group
	err    error
	calls  int
	query  string
	limit  int
}

func (f *fakeProvider) Search(_ context.Context, keyword string, limit int) ([]search.Group, error) {
	f.calls++
	f.query = keyword
	f.limit = limit
	return f.groups, f.err
}

// searchItem builds one result item with the fields the service only copies.
func searchItem(id string) search.Item {
	return search.Item{
		ID:          id,
		Title:       "标题 " + id,
		Subtitle:    "副标题",
		VersionID:   "11",
		VersionName: "版本甲",
		Status:      "draft",
		URL:         "/versions/11?tab=overview",
	}
}

// searchGroup builds a group with count generated items.
func searchGroup(typeName string, count int) search.Group {
	items := make([]search.Item, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, searchItem(fmt.Sprintf("%s-%d", typeName, index)))
	}
	return search.Group{Type: typeName, Items: items}
}

// providers is the canonical all-context provider fixture.
func providers() (search.Providers, *fakeProvider, *fakeProvider, *fakeProvider) {
	version := &fakeProvider{groups: []search.Group{
		searchGroup(search.TypeVersion, 1),
		searchGroup(search.TypeRequirement, 1),
	}}
	mindmap := &fakeProvider{groups: []search.Group{searchGroup(search.TypeCase, 1)}}
	delivery := &fakeProvider{groups: []search.Group{searchGroup(search.TypeIssue, 1)}}
	return search.Providers{Version: version, Mindmap: mindmap, Delivery: delivery},
		version, mindmap, delivery
}

// TestSearchRejectsInvalidQuery pins the q length window of spec §6.2: a blank
// (or whitespace-only) query and a query longer than 100 characters are
// invalid_argument{field:"q"} and never reach a provider.
func TestSearchRejectsInvalidQuery(t *testing.T) {
	cases := []string{
		"",
		"   ",
		strings.Repeat("长", search.MaxQueryLength+1),
	}
	for _, text := range cases {
		providerSet, version, mindmap, delivery := providers()
		service := search.NewService(providerSet)
		_, err := service.Search(context.Background(), search.Query{Text: text})
		if err == nil {
			t.Fatalf("q=%q: error = nil, want invalid_argument", text)
		}
		var invalid *search.ErrInvalidArgument
		if !errors.As(err, &invalid) {
			t.Fatalf("q=%q: error = %v, want *ErrInvalidArgument", text, err)
		}
		if invalid.Field != "q" {
			t.Fatalf("q=%q: field = %q, want q", text, invalid.Field)
		}
		if version.calls != 0 || mindmap.calls != 0 || delivery.calls != 0 {
			t.Fatalf("q=%q: providers called %d/%d/%d, want 0",
				text, version.calls, mindmap.calls, delivery.calls)
		}
	}
}

// TestSearchTrimsAndForwardsQuery proves the service strips the surrounding
// whitespace before it queries the providers.
func TestSearchTrimsAndForwardsQuery(t *testing.T) {
	providerSet, version, _, _ := providers()
	service := search.NewService(providerSet)
	if _, err := service.Search(context.Background(), search.Query{Text: "  登录流程  "}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if version.query != "登录流程" {
		t.Fatalf("forwarded query = %q, want the trimmed keyword", version.query)
	}
}

// TestSearchFiltersRequestedTypes proves the `types` filter of spec §6.2: only
// the requested groups are returned, in the canonical type order, and a
// provider whose types are not requested is never queried.
func TestSearchFiltersRequestedTypes(t *testing.T) {
	providerSet, version, mindmap, delivery := providers()
	service := search.NewService(providerSet)
	snapshot, err := service.Search(context.Background(), search.Query{
		Text:  "登录",
		Types: []string{search.TypeIssue, search.TypeCase},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if version.calls != 0 {
		t.Fatalf("version provider calls = %d, want 0 when neither version nor requirement is requested", version.calls)
	}
	if mindmap.calls != 1 || delivery.calls != 1 {
		t.Fatalf("mindmap/delivery calls = %d/%d, want 1/1", mindmap.calls, delivery.calls)
	}
	if len(snapshot.Groups) != 2 {
		t.Fatalf("groups = %+v, want case and issue only", snapshot.Groups)
	}
	if snapshot.Groups[0].Type != search.TypeCase || snapshot.Groups[1].Type != search.TypeIssue {
		t.Fatalf("group order = %q,%q, want case,issue", snapshot.Groups[0].Type, snapshot.Groups[1].Type)
	}
	if len(snapshot.Groups[0].Items) != 1 || len(snapshot.Groups[1].Items) != 1 {
		t.Fatalf("group items = %+v, want one per group", snapshot.Groups)
	}
	if snapshot.Total != 2 {
		t.Fatalf("total = %d, want 2", snapshot.Total)
	}
}

// TestSearchOrdersAllGroupsCanonically proves the response always carries the
// four groups in version → requirement → case → issue order, whatever order a
// provider returns them in.
func TestSearchOrdersAllGroupsCanonically(t *testing.T) {
	version := &fakeProvider{groups: []search.Group{
		searchGroup(search.TypeRequirement, 1),
		searchGroup(search.TypeVersion, 1),
	}}
	mindmap := &fakeProvider{groups: []search.Group{searchGroup(search.TypeCase, 1)}}
	delivery := &fakeProvider{groups: []search.Group{searchGroup(search.TypeIssue, 1)}}
	service := search.NewService(search.Providers{
		Version: version, Mindmap: mindmap, Delivery: delivery,
	})

	snapshot, err := service.Search(context.Background(), search.Query{Text: "登录"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []string{search.TypeVersion, search.TypeRequirement, search.TypeCase, search.TypeIssue}
	if len(snapshot.Groups) != len(want) {
		t.Fatalf("groups = %+v, want %v", snapshot.Groups, want)
	}
	for index, typeName := range want {
		if snapshot.Groups[index].Type != typeName {
			t.Fatalf("group %d = %q, want %q", index, snapshot.Groups[index].Type, typeName)
		}
	}
}

// TestSearchAppliesPerTypeLimit proves `limit` caps every group independently
// and is forwarded to the providers (default 5, max 20).
func TestSearchAppliesPerTypeLimit(t *testing.T) {
	delivery := &fakeProvider{groups: []search.Group{searchGroup(search.TypeIssue, 6)}}
	service := search.NewService(search.Providers{Delivery: delivery})

	snapshot, err := service.Search(context.Background(), search.Query{
		Text: "下单", Types: []string{search.TypeIssue}, Limit: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if delivery.limit != 2 {
		t.Fatalf("forwarded limit = %d, want 2", delivery.limit)
	}
	if len(snapshot.Groups) != 1 || len(snapshot.Groups[0].Items) != 2 {
		t.Fatalf("group = %+v, want two items", snapshot.Groups)
	}
	if snapshot.Total != 2 {
		t.Fatalf("total = %d, want 2", snapshot.Total)
	}
	if !snapshot.Truncated {
		t.Fatal("truncated = false, want true for a capped group")
	}

	defaulted, err := service.Search(context.Background(), search.Query{Text: "下单"})
	if err != nil {
		t.Fatalf("Search(default): %v", err)
	}
	if delivery.limit != search.DefaultLimit {
		t.Fatalf("forwarded default limit = %d, want %d", delivery.limit, search.DefaultLimit)
	}
	var issueGroup *search.Group
	for index := range defaulted.Groups {
		if defaulted.Groups[index].Type == search.TypeIssue {
			issueGroup = &defaulted.Groups[index]
		}
	}
	if issueGroup == nil || len(issueGroup.Items) != search.DefaultLimit {
		t.Fatalf("default issue group = %+v, want %d items", issueGroup, search.DefaultLimit)
	}
	if !defaulted.Truncated {
		t.Fatal("default truncated = false, want true for six results at limit 5")
	}
}

// TestSearchUnderLimitIsNotTruncated proves a group that did not hit its cap is
// not reported as truncated.
func TestSearchUnderLimitIsNotTruncated(t *testing.T) {
	mindmap := &fakeProvider{groups: []search.Group{searchGroup(search.TypeCase, 2)}}
	service := search.NewService(search.Providers{Mindmap: mindmap})
	snapshot, err := service.Search(context.Background(), search.Query{
		Text: "登录", Types: []string{search.TypeCase}, Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if snapshot.Truncated {
		t.Fatal("truncated = true, want false when every group is below the limit")
	}
}

// TestSearchClampsLimitAtServiceBoundary proves a caller that bypasses the HTTP
// validation never asks a provider for more than the documented maximum.
func TestSearchClampsLimitAtServiceBoundary(t *testing.T) {
	delivery := &fakeProvider{groups: []search.Group{searchGroup(search.TypeIssue, 1)}}
	service := search.NewService(search.Providers{Delivery: delivery})
	if _, err := service.Search(context.Background(), search.Query{Text: "问题", Limit: 999}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if delivery.limit != search.MaxLimit {
		t.Fatalf("forwarded limit = %d, want the clamped %d", delivery.limit, search.MaxLimit)
	}
}

// TestSearchDegradesOnProviderError proves spec §6.2's partial-failure rule: one
// failing provider does not fail the whole request; the remaining groups are
// returned and the response is marked degraded.
func TestSearchDegradesOnProviderError(t *testing.T) {
	version := &fakeProvider{groups: []search.Group{
		searchGroup(search.TypeVersion, 1),
		searchGroup(search.TypeRequirement, 1),
	}}
	mindmap := &fakeProvider{err: errors.New("case db unavailable")}
	delivery := &fakeProvider{groups: []search.Group{searchGroup(search.TypeIssue, 1)}}
	service := search.NewService(search.Providers{
		Version: version, Mindmap: mindmap, Delivery: delivery,
	})

	snapshot, err := service.Search(context.Background(), search.Query{Text: "登录"})
	if err != nil {
		t.Fatalf("Search: %v, want a partial result, not an error", err)
	}
	if !snapshot.Degraded {
		t.Fatal("degraded = false, want true after a provider failure")
	}
	if len(snapshot.Groups) != 4 {
		t.Fatalf("groups = %d, want the four requested groups: %+v", len(snapshot.Groups), snapshot.Groups)
	}
	byType := map[string]search.Group{}
	for _, group := range snapshot.Groups {
		byType[group.Type] = group
	}
	if len(byType[search.TypeVersion].Items) != 1 || len(byType[search.TypeRequirement].Items) != 1 {
		t.Fatalf("healthy version/requirement groups = %+v, want their items", byType)
	}
	if len(byType[search.TypeIssue].Items) != 1 {
		t.Fatalf("healthy issue group = %+v, want its item", byType[search.TypeIssue])
	}
	if items, ok := byType[search.TypeCase]; !ok || items.Items == nil || len(items.Items) != 0 {
		t.Fatalf("case group = %+v, want a present empty group", items)
	}
	if snapshot.Total != 3 {
		t.Fatalf("total = %d, want the three healthy items", snapshot.Total)
	}
}

// TestSearchRejectsUnknownType proves an unknown `types` token is a validation
// error (the handler renders it as invalid_argument{field:"types"}).
func TestSearchRejectsUnknownType(t *testing.T) {
	providerSet, version, _, _ := providers()
	service := search.NewService(providerSet)
	_, err := service.Search(context.Background(), search.Query{
		Text: "登录", Types: []string{search.TypeVersion, "bogus"},
	})
	if err == nil {
		t.Fatal("error = nil, want invalid_argument for an unknown type")
	}
	var invalid *search.ErrInvalidArgument
	if !errors.As(err, &invalid) || invalid.Field != "types" {
		t.Fatalf("error = %v, want *ErrInvalidArgument{field:types}", err)
	}
	if version.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 before validation passed", version.calls)
	}
}

// TestSearchEmptySnapshotRendersArrays proves an empty result marshals as
// `groups:[]` (and each group's `items:[]`), never JSON null.
func TestSearchEmptySnapshotRendersArrays(t *testing.T) {
	service := search.NewService(search.Providers{
		Mindmap: &fakeProvider{groups: []search.Group{{Type: search.TypeCase}}},
	})
	snapshot, err := service.Search(context.Background(), search.Query{
		Text: "无结果", Types: []string{search.TypeCase},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"groups":[{"type":"case","items":[]}]`) {
		t.Fatalf("payload = %s, want empty arrays", payload)
	}
	if snapshot.Total != 0 || snapshot.Truncated || snapshot.Degraded {
		t.Fatalf("snapshot = %+v, want an empty non-truncated result", snapshot)
	}
}

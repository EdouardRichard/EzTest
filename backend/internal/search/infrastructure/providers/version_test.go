package providers

import (
	"context"
	"errors"
	"testing"

	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// fakeVersionQueries implements VersionQueries without touching the version
// context internals.
type fakeVersionQueries struct {
	versionHits     []versionapp.SearchHit
	requirementHits []versionapp.SearchHit
	versionErr      error
	requirementErr  error

	versionCalls     int
	requirementCalls int
	versionQ         string
	requirementQ     string
	versionLimit     int
	requirementLimit int
}

func (f *fakeVersionQueries) SearchVersions(_ context.Context, q string, limit int) ([]versionapp.SearchHit, error) {
	f.versionCalls++
	f.versionQ = q
	f.versionLimit = limit
	return f.versionHits, f.versionErr
}

func (f *fakeVersionQueries) SearchRequirements(_ context.Context, q string, limit int) ([]versionapp.SearchHit, error) {
	f.requirementCalls++
	f.requirementQ = q
	f.requirementLimit = limit
	return f.requirementHits, f.requirementErr
}

func TestVersionSearcherMapsOwnedHits(t *testing.T) {
	query := &fakeVersionQueries{versionHits: []versionapp.SearchHit{{
		ID:           11,
		Title:        "登录版本",
		Subtitle:     "",
		VersionID:    11,
		VersionTitle: "登录版本",
		Status:       "running",
		URL:          "/versions/11?tab=overview",
	}}}
	searcher := NewVersionSearcher(query)

	items, err := searcher.Search(context.Background(), "login", 7)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if query.versionCalls != 1 || query.requirementCalls != 0 {
		t.Fatalf("calls = version:%d requirement:%d, want 1/0",
			query.versionCalls, query.requirementCalls)
	}
	if query.versionQ != "login" || query.versionLimit != 7 {
		t.Fatalf("forwarded q/limit = %q/%d, want login/7", query.versionQ, query.versionLimit)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	want := searchapp.Item{
		ID:           "11",
		Title:        "登录版本",
		Subtitle:     "",
		VersionID:    "11",
		VersionTitle: "登录版本",
		Status:       "running",
		URL:          "/versions/11?tab=overview",
	}
	if items[0] != want {
		t.Fatalf("item = %+v, want %+v", items[0], want)
	}
}

func TestRequirementSearcherMapsOwnedHits(t *testing.T) {
	query := &fakeVersionQueries{requirementHits: []versionapp.SearchHit{{
		ID:           21,
		Title:        "登录需求",
		Subtitle:     "",
		VersionID:    11,
		VersionTitle: "版本甲",
		Status:       "clarified",
		URL:          "/versions/11?tab=requirements&requirement=21",
	}}}
	searcher := NewRequirementSearcher(query)

	items, err := searcher.Search(context.Background(), "登录", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if query.versionCalls != 0 || query.requirementCalls != 1 {
		t.Fatalf("calls = version:%d requirement:%d, want 0/1",
			query.versionCalls, query.requirementCalls)
	}
	if len(items) != 1 || items[0].ID != "21" || items[0].VersionID != "11" {
		t.Fatalf("items = %+v, want one requirement hit", items)
	}
	if items[0].URL != "/versions/11?tab=requirements&requirement=21" {
		t.Fatalf("url = %q, want the requirements deep link", items[0].URL)
	}
}

func TestVersionSearcherPropagatesErrors(t *testing.T) {
	cause := errors.New("version query failed")
	searcher := NewVersionSearcher(&fakeVersionQueries{versionErr: cause})

	if _, err := searcher.Search(context.Background(), "login", 5); !errors.Is(err, cause) {
		t.Fatalf("error = %v, want %v", err, cause)
	}
}

func TestVersionSearcherWithoutQueryFails(t *testing.T) {
	searcher := NewVersionSearcher(nil)
	if _, err := searcher.Search(context.Background(), "login", 5); err == nil {
		t.Fatal("Search with a nil query service returned nil error")
	}
}

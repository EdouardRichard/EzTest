package providers

import (
	"context"
	"errors"
	"testing"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// fakeDesignQueries implements DesignQueries without touching the design
// context internals.
type fakeDesignQueries struct {
	hits  []designapp.SearchHit
	err   error
	calls int
	q     string
	limit int
}

func (f *fakeDesignQueries) SearchCases(_ context.Context, q string, limit int) ([]designapp.SearchHit, error) {
	f.calls++
	f.q = q
	f.limit = limit
	return f.hits, f.err
}

func TestCaseSearcherMapsOwnedHits(t *testing.T) {
	query := &fakeDesignQueries{hits: []designapp.SearchHit{{
		ID:           300,
		Title:        "登录流程",
		Subtitle:     "",
		VersionID:    11,
		VersionTitle: "版本甲",
		Status:       "reviewed",
		URL:          "/versions/11?tab=design&case=300",
	}}}
	searcher := NewCaseSearcher(query)

	items, err := searcher.Search(context.Background(), "登录", 4)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if query.calls != 1 || query.q != "登录" || query.limit != 4 {
		t.Fatalf("forwarded q/limit = %q/%d calls=%d, want 登录/4/1",
			query.q, query.limit, query.calls)
	}
	want := searchapp.Item{
		ID:           "300",
		Title:        "登录流程",
		Subtitle:     "",
		VersionID:    "11",
		VersionTitle: "版本甲",
		Status:       "reviewed",
		URL:          "/versions/11?tab=design&case=300",
	}
	if len(items) != 1 || items[0] != want {
		t.Fatalf("items = %+v, want %+v", items, want)
	}
}

func TestCaseSearcherPropagatesErrors(t *testing.T) {
	cause := errors.New("design query failed")
	searcher := NewCaseSearcher(&fakeDesignQueries{err: cause})

	if _, err := searcher.Search(context.Background(), "login", 5); !errors.Is(err, cause) {
		t.Fatalf("error = %v, want %v", err, cause)
	}
}

func TestCaseSearcherWithoutQueryFails(t *testing.T) {
	searcher := NewCaseSearcher(nil)
	if _, err := searcher.Search(context.Background(), "login", 5); err == nil {
		t.Fatal("Search with a nil query service returned nil error")
	}
}

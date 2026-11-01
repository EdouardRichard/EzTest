package providers

import (
	"context"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

func TestNewWiresOneProviderPerSearchType(t *testing.T) {
	versionQuery := &fakeVersionQueries{
		versionHits:     []versionapp.SearchHit{{ID: 1, Title: "v"}},
		requirementHits: []versionapp.SearchHit{{ID: 2, Title: "r"}},
	}
	designQuery := &fakeDesignQueries{
		hits: []designapp.SearchHit{{ID: 3, Title: "c"}},
	}
	deliveryQuery := &fakeDeliveryQueries{
		issueHits:  []deliveryapp.SearchHit{{ID: 4, Title: "i"}},
		reportHits: []deliveryapp.SearchHit{{ID: 5, Title: "p"}},
	}

	wired := New(versionQuery, designQuery, deliveryQuery)

	checks := []struct {
		typeName string
		provider searchapp.Provider
		wantID   string
	}{
		{typeName: "version", provider: wired.Version, wantID: "1"},
		{typeName: "requirement", provider: wired.Requirement, wantID: "2"},
		{typeName: "case", provider: wired.Case, wantID: "3"},
		{typeName: "issue", provider: wired.Issue, wantID: "4"},
		{typeName: "report", provider: wired.Report, wantID: "5"},
	}
	for _, check := range checks {
		if check.provider == nil {
			t.Fatalf("%s provider is nil", check.typeName)
		}
		items, err := check.provider.Search(context.Background(), "q", 5)
		if err != nil {
			t.Fatalf("%s Search: %v", check.typeName, err)
		}
		if len(items) != 1 || items[0].ID != check.wantID {
			t.Fatalf("%s items = %+v, want id %s", check.typeName, items, check.wantID)
		}
	}

	if versionQuery.versionCalls != 1 || versionQuery.requirementCalls != 1 {
		t.Fatalf("version query calls = %d/%d, want 1/1",
			versionQuery.versionCalls, versionQuery.requirementCalls)
	}
	if designQuery.calls != 1 || deliveryQuery.issueCalls != 1 || deliveryQuery.reportCalls != 1 {
		t.Fatalf("design/delivery calls = %d/%d/%d, want 1/1/1",
			designQuery.calls, deliveryQuery.issueCalls, deliveryQuery.reportCalls)
	}
}

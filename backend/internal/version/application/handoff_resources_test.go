package versionapp_test

// H-B1: the handoff resources block is assembled in the APPLICATION service from
// the resource context's port. These tests pin the two halves of that boundary:
// the version-owned scope comes from HandoffReader.ResourceScope, the resource
// catalog comes from resourceapp.HandoffResourceReader, and the mapping onto the
// persisted document is faithful field by field.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

func TestHandoffResourcesReadThroughResourcePort(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	// The version id (7) is NOT the environment id (1001): passing the wrong one
	// to the port would read another environment's catalog.
	reader.scope = versionapp.HandoffResourceScope{EnvironmentID: 1001, AccountIDs: []int64{1301, 1302}}
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	port := reader.resources
	if len(port.environmentCalls) != 1 || port.environmentCalls[0] != 1001 {
		t.Fatalf("resource port environment calls = %v, want exactly [1001] (the bound environment from the version-owned scope)", port.environmentCalls)
	}
	if len(port.accountCalls) != 1 || len(port.accountCalls[0]) != 2 ||
		port.accountCalls[0][0] != 1301 || port.accountCalls[0][1] != 1302 {
		t.Fatalf("resource port account calls = %v, want the enabled association ids [1301 1302]", port.accountCalls)
	}

	resources := document.Resources
	if resources.Environment.ID != 1001 || resources.Environment.Name != "staging" || resources.Environment.Status != "active" {
		t.Fatalf("environment summary = %+v, want the port's bundle projection", resources.Environment)
	}
	if len(resources.SUTs) != 1 {
		t.Fatalf("suts = %+v", resources.SUTs)
	}
	sut := resources.SUTs[0]
	if sut.SUTID != 901 || sut.Name != "订单中心" || sut.BaseURL != "https://orders.example.test" {
		t.Fatalf("sut projection = %+v", sut)
	}
	if len(sut.Pages) != 1 || sut.Pages[0].ID != 1101 || sut.Pages[0].Name != "退款页" ||
		sut.Pages[0].URL != "https://orders.example.test/refund" || sut.Pages[0].RouteTemplate != "/refund" {
		t.Fatalf("page projection = %+v", sut.Pages)
	}
	if len(sut.APIs) != 1 || sut.APIs[0].ID != 1201 || sut.APIs[0].Name != "创建退款" ||
		sut.APIs[0].Method != "POST" || sut.APIs[0].Path != "/api/refunds" {
		t.Fatalf("api projection = %+v", sut.APIs)
	}
	if len(sut.Databases) != 1 || sut.Databases[0].ID != 1401 || sut.Databases[0].SUTID != 901 ||
		sut.Databases[0].Name != "orders" || sut.Databases[0].Kind != "postgres" || !sut.Databases[0].ReadOnly {
		t.Fatalf("database projection = %+v (sut_id/kind/read_only must survive the mapping)", sut.Databases)
	}
	if len(resources.Accounts) != 1 || resources.Accounts[0].ID != 1301 ||
		resources.Accounts[0].Username != "qa-refund" || len(resources.Accounts[0].Roles) != 1 {
		t.Fatalf("account projection = %+v", resources.Accounts)
	}
	if len(resources.Repositories) != 1 || resources.Repositories[0].ID != 1501 ||
		resources.Repositories[0].Name != "order-backend" || resources.Repositories[0].Role != "backend" ||
		resources.Repositories[0].Branch != "main" ||
		resources.Repositories[0].Source != versionapp.HandoffRepositorySourceSUT {
		t.Fatalf("repository projection = %+v (every row is SUT-inherited)", resources.Repositories)
	}
}

// TestHandoffResourcePortFailureIsNotSilentlyEmpty pins 不静默降级: a resource read
// failure fails the generation instead of persisting a handoff with an empty
// resources block (which the Design Host would read as "this version has no
// resources").
func TestHandoffResourcePortFailureIsNotSilentlyEmpty(t *testing.T) {
	service, store, reader, _ := newHandoffTest(t)
	reader.resources.err = errors.New("resource catalog unavailable")
	if _, err := service.Generate(context.Background(), 7); err == nil {
		t.Fatal("Generate succeeded despite a resource port failure")
	}
	if len(store.rows) != 0 {
		t.Fatalf("a failed resource read persisted %d handoff revisions", len(store.rows))
	}
}

// TestHandoffWithoutResourcePortIsUnavailable pins the half-assembly behaviour:
// the service reports a typed 503 rather than generating a resource-less pack.
func TestHandoffWithoutResourcePortIsUnavailable(t *testing.T) {
	_, store, reader, publisher := newHandoffTest(t)
	service := versionapp.NewHandoffService(store, reader, nil, &seqIDs{}, publisher)
	_, err := service.Generate(context.Background(), 7)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("Generate without a resource port = %v, want a typed 503", err)
	}
}

// TestHandoffResourcesBundleProjectionIsPure pins the mapping without any port:
// an empty bundle yields non-nil empty lists (the persisted document must always
// carry the three arrays), and the environment id survives even when the
// resource row is gone.
func TestHandoffResourcesBundleProjectionIsPure(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	reader.scope = versionapp.HandoffResourceScope{EnvironmentID: 4242}
	reader.resources.bundle = resourceapp.HandoffResourceBundle{
		Environment: resourceapp.HandoffEnvironment{ID: 4242},
	}
	reader.resources.accounts = nil
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if document.Resources.Environment.ID != 4242 || document.Resources.Environment.Name != "" {
		t.Fatalf("environment summary = %+v, want the bound id with an empty name", document.Resources.Environment)
	}
	if document.Resources.SUTs == nil || document.Resources.Accounts == nil || document.Resources.Repositories == nil {
		t.Fatalf("resources arrays must be non-nil: %+v", document.Resources)
	}
	if len(document.Resources.SUTs) != 0 || len(document.Resources.Accounts) != 0 || len(document.Resources.Repositories) != 0 {
		t.Fatalf("resources = %+v, want empty", document.Resources)
	}
	if document.Resources.Truncated {
		t.Fatal("an empty scope must not be marked truncated")
	}
}

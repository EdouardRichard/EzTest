package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeChangesService is a fakeVersionService with the optional change-feed
// application slice the HTTP layer type-asserts.
type fakeChangesService struct {
	*fakeVersionService
	page     versionapp.VersionChangePage
	err      error
	lastPage domain.Page
	calls    int
}

// newFakeChangesService returns an empty changes-capable fake.
func newFakeChangesService() *fakeChangesService {
	return &fakeChangesService{fakeVersionService: newFakeVersionService()}
}

// Changes returns the configured page.
func (f *fakeChangesService) Changes(_ context.Context, _ int64, page domain.Page) (versionapp.VersionChangePage, error) {
	f.calls++
	f.lastPage = page
	if f.err != nil {
		return versionapp.VersionChangePage{}, f.err
	}
	return f.page, nil
}

// TestVersionDTOContinueFields pins the P7 additive version fields of spec
// §7.2: continuePhase is null unless the version is continuing, and continuedAt
// is null until the D43 trigger stamps it.
func TestVersionDTOContinueFields(t *testing.T) {
	continued := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)

	t.Run("continuing exposes the phase and the timestamp", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{
			ID: versionDetailID, Name: "Checkout", Status: domain.VersionContinuing,
			ContinuePhase: domain.ContinuePhaseDesigning, ContinuedAt: &continued, Revision: 3,
		})

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "continuePhase", domain.ContinuePhaseDesigning)
		requireStringField(t, data, "continuedAt", continued.Format(time.RFC3339))
	})

	t.Run("completed and draft versions expose null", func(t *testing.T) {
		for _, status := range []string{domain.VersionCompleted, domain.VersionDraft} {
			t.Run(status, func(t *testing.T) {
				handler, deps := newTestServer(t)
				deps.versions.seed(versionapp.Version{
					ID: versionDetailID, Name: "Checkout", Status: status, Revision: 1,
				})

				recorder := doRequest(handler, http.MethodGet, "/api/versions/100", testUserToken, "")
				requireStatus(t, recorder, http.StatusOK)
				data := decodeDataMap(t, recorder)
				requireNullField(t, data, "continuePhase")
				requireNullField(t, data, "continuedAt")
			})
		}
	})
}

// TestVersionChangesRoute pins GET /api/versions/{id}/changes (spec §7.2): the
// derived items, the opaque nextCursor/hasMore block and the error mapping.
func TestVersionChangesRoute(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	t.Run("returns the derived items with ids as strings", func(t *testing.T) {
		service := newFakeChangesService()
		service.page = versionapp.VersionChangePage{
			Items: []domain.VersionChange{
				{
					Kind: domain.VersionChangeRequirementAdded, At: at, RefID: "18446744073709551616",
					Summary: "需求新增：Checkout",
				},
				{
					Kind: domain.VersionChangeBatchFinished, At: at.Add(-time.Minute), RefID: "9",
					Summary: "批次结束：第 1 批（done）",
				},
			},
			NextCursor: "Mg", HasMore: true,
		}
		handler := &VersionHandler{versions: service}
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)

		recorder := doRequest(mux, http.MethodGet, "/api/versions/100/changes?limit=2", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "nextCursor", "Mg")
		requireBoolField(t, data, "hasMore", true)
		items := requireArrayField(t, data, "items")
		if len(items) != 2 {
			t.Fatalf("items = %d, want 2 (body=%s)", len(items), recorder.Body.String())
		}
		first, ok := items[0].(map[string]any)
		if !ok {
			t.Fatalf("item type = %T", items[0])
		}
		requireStringField(t, first, "kind", domain.VersionChangeRequirementAdded)
		requireJSONString(t, first, "refId", "18446744073709551616")
		requireStringField(t, first, "at", at.Format(time.RFC3339))
		requireStringField(t, first, "summary", "需求新增：Checkout")
		second, ok := items[1].(map[string]any)
		if !ok {
			t.Fatalf("item type = %T", items[1])
		}
		requireStringField(t, second, "kind", domain.VersionChangeBatchFinished)
		if service.calls != 1 || service.lastPage.Limit != 2 {
			t.Fatalf("calls/page = %d/%+v", service.calls, service.lastPage)
		}
	})

	t.Run("an empty feed is an empty items array", func(t *testing.T) {
		service := newFakeChangesService()
		handler := &VersionHandler{versions: service}
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)

		recorder := doRequest(mux, http.MethodGet, "/api/versions/100/changes", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireArrayField(t, data, "items")
		requireStringField(t, data, "nextCursor", "")
		requireBoolField(t, data, "hasMore", false)
	})

	t.Run("a missing version is a 404", func(t *testing.T) {
		service := newFakeChangesService()
		service.err = domain.NewNotFound("version", versionDetailID)
		handler := &VersionHandler{versions: service}
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)

		recorder := doRequest(mux, http.MethodGet, "/api/versions/999/changes", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")
	})

	t.Run("a malformed cursor is a 400", func(t *testing.T) {
		service := newFakeChangesService()
		handler := &VersionHandler{versions: service}
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)

		recorder := doRequest(mux, http.MethodGet, "/api/versions/100/changes?cursor=!!!", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_cursor")
		if service.calls != 0 {
			t.Fatalf("malformed cursor reached the service %d times", service.calls)
		}
	})
}

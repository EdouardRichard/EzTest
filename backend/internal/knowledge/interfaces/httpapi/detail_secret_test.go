package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
)

// TestDetailHidesSecrets pins the R91 read-surface rule for the routes that do
// not go through the list/search projection: a legacy secret-bearing item is
// not readable by ID (404, the same shape list/search use for hidden rows), and
// a secret-bearing historical revision is dropped from both the detail payload
// and the revisions endpoint.
func TestDetailHidesSecrets(t *testing.T) {
	t.Run("detail hides a secret-bearing item", func(t *testing.T) {
		item := fixtureItem()
		item.Content = `{"recipe":"password=hunter2"}`
		fake := &fakeService{getItem: item, revisions: []knowledge.Revision{}}
		mux := newKnowledgeMux(fake, "tester")

		recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/9001", "")
		assertErrorCode(t, recorder.Code, body, http.StatusNotFound, "not_found")
		if strings.Contains(recorder.Body.String(), "hunter2") {
			t.Fatalf("detail leaked the credential: %s", recorder.Body.String())
		}
	})

	t.Run("detail drops a secret-bearing historical revision", func(t *testing.T) {
		clean := fixtureRevision()
		secret := clean
		secret.ID = clean.ID + 1
		secret.Revision = clean.Revision + 1
		secret.Content = "Authorization: Bearer [REDACTED_TOKEN]"
		fake := &fakeService{getItem: fixtureItem(), revisions: []knowledge.Revision{clean, secret}}
		mux := newKnowledgeMux(fake, "tester")

		recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/9001", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%v, want 200", recorder.Code, body)
		}
		revisions, ok := body["data"].(map[string]any)["revisions"].([]any)
		if !ok {
			t.Fatalf("detail revisions = %#v", body["data"])
		}
		if len(revisions) != 1 {
			t.Fatalf("detail revisions = %d, want only the clean revision", len(revisions))
		}
		if strings.Contains(recorder.Body.String(), "eyJhbGci") {
			t.Fatalf("detail revisions leaked the credential: %s", recorder.Body.String())
		}
	})

	t.Run("revisions endpoint drops secret-bearing rows", func(t *testing.T) {
		clean := fixtureRevision()
		secret := clean
		secret.ID = clean.ID + 1
		secret.Revision = clean.Revision + 1
		secret.Title = "token=hunter2"
		fake := &fakeService{revisions: []knowledge.Revision{clean, secret}}
		mux := newKnowledgeMux(fake, "tester")

		recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/9001/revisions", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%v, want 200", recorder.Code, body)
		}
		items, ok := body["data"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("revisions = %#v, want only the clean row", body["data"])
		}
		if strings.Contains(recorder.Body.String(), "hunter2") {
			t.Fatalf("revisions leaked the credential: %s", recorder.Body.String())
		}
	})
}

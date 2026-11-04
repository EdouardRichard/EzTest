package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
)

// TestReviewSecretItemHidden pins the R91 rule for the admin review endpoints
// that echo the stored item back through their view: a legacy secret-bearing
// item must be indistinguishable from a missing one (404) and must not be
// mutated, so an admin can never retrieve the body via reject/revoke. A
// non-admin keeps the existing 403 and never reaches the service.
func TestReviewSecretItemHidden(t *testing.T) {
	for _, action := range []string{"reject", "revoke"} {
		t.Run(action+"/admin gets not_found without mutating", func(t *testing.T) {
			secretItem := fixtureItem()
			secretItem.Content = `{"recipe":"password=hunter2"}`
			fake := &fakeService{getItem: secretItem, reviewItem: secretItem}
			mux := newKnowledgeMux(fake, "admin")

			recorder, body := perform(t, mux, http.MethodPost,
				"/api/knowledge/items/9001/"+action, `{"reason":"obsolete"}`)
			assertErrorCode(t, recorder.Code, body, http.StatusNotFound, "not_found")
			if fake.getCalls != 1 {
				t.Fatalf("item reads = %d, want one item read before the mutation", fake.getCalls)
			}
			if fake.reviewCalls != 0 {
				t.Fatalf("%s mutated a hidden item: review calls = %d", action, fake.reviewCalls)
			}
			if strings.Contains(recorder.Body.String(), "hunter2") {
				t.Fatalf("%s leaked the credential: %s", action, recorder.Body.String())
			}
		})

		t.Run(action+"/non-admin is forbidden and never mutates", func(t *testing.T) {
			secretItem := fixtureItem()
			secretItem.Content = `{"recipe":"password=hunter2"}`
			fake := &fakeService{getItem: secretItem, reviewItem: secretItem}
			mux := newKnowledgeMux(fake, "user")

			recorder, body := perform(t, mux, http.MethodPost,
				"/api/knowledge/items/9001/"+action, `{"reason":"obsolete"}`)
			assertErrorCode(t, recorder.Code, body, http.StatusForbidden, "forbidden")
			if fake.reviewCalls != 0 {
				t.Fatalf("%s mutated an item for a non-admin: review calls = %d", action, fake.reviewCalls)
			}
			if strings.Contains(recorder.Body.String(), "hunter2") {
				t.Fatalf("%s leaked the credential: %s", action, recorder.Body.String())
			}
		})
	}
}

// TestRevisionsSecretItemHidden pins the same R91 rule for the append-only
// history route: the revisions of a secret-bearing stored item are not readable
// by ID (404, matching GetItem), instead of the 200 the route used to return.
func TestRevisionsSecretItemHidden(t *testing.T) {
	secretItem := fixtureItem()
	secretItem.Content = `{"recipe":"password=hunter2"}`
	secretRevision := fixtureRevision()
	secretRevision.Content = "Authorization: Bearer [REDACTED_TOKEN]"
	fake := &fakeService{getItem: secretItem, revisions: []knowledge.Revision{secretRevision}}
	mux := newKnowledgeMux(fake, "user")

	recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/9001/revisions", "")
	assertErrorCode(t, recorder.Code, body, http.StatusNotFound, "not_found")
	if fake.getCalls != 1 {
		t.Fatalf("item reads = %d, want one item read before the revision read", fake.getCalls)
	}
	if fake.revisionsCall != 0 {
		t.Fatalf("revisions were read for a hidden item: revision calls = %d", fake.revisionsCall)
	}
	if strings.Contains(recorder.Body.String(), "hunter2") ||
		strings.Contains(recorder.Body.String(), "eyJhbGci") {
		t.Fatalf("revisions leaked the credential: %s", recorder.Body.String())
	}
}

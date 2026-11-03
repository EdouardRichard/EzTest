package resourceapp

import (
	"context"
	"errors"
	"testing"
)

// recordingKnowledgeInvalidator is the resource context's neutral binding-change
// port double. It records exactly what the service asks the knowledge context to
// invalidate.
type recordingKnowledgeInvalidator struct {
	changes []KnowledgeBindingChange
	result  KnowledgeBindingInvalidation
	err     error
}

func (r *recordingKnowledgeInvalidator) MarkBindingsStale(_ context.Context, change KnowledgeBindingChange) (KnowledgeBindingInvalidation, error) {
	r.changes = append(r.changes, change)
	return r.result, r.err
}

// TestResourceChangeMarksStale pins the resource → knowledge invalidation
// wiring: a genuinely changed page route / removed account role is forwarded to
// MarkBindingsStale with only the changed binding values (never the owning
// system/environment IDs, which would over-invalidate the whole SUT), a no-op
// edit never calls the port, and a knowledge failure never blocks the resource
// write.
func TestResourceChangeMarksStale(t *testing.T) {
	ctx := context.Background()

	t.Run("a changed page route invalidates the previous route key", func(t *testing.T) {
		h := newHarness()
		newPageEnv(t, h)
		invalidator := &recordingKnowledgeInvalidator{}
		h.svc.SetKnowledgeInvalidator(invalidator)

		page, err := h.svc.CreatePage(ctx, 1002, PageInput{
			Name: "Order list", URL: "https://app.example.com/orders", RouteTemplate: "/orders",
		}, 42)
		if err != nil {
			t.Fatalf("CreatePage: %v", err)
		}
		route := "/orders/list"
		if _, err := h.svc.UpdatePage(ctx, 1003, PagePatch{RouteTemplate: &route}, 42); err != nil {
			t.Fatalf("UpdatePage: %v", err)
		}
		if len(invalidator.changes) != 1 {
			t.Fatalf("invalidation calls = %d, want exactly one for the changed route", len(invalidator.changes))
		}
		change := invalidator.changes[0]
		if len(change.PageKeys) != 1 || change.PageKeys[0] != "/orders" {
			t.Fatalf("page keys = %#v, want the previous route [/orders]", change.PageKeys)
		}
		if len(change.AccountRoles) != 0 || len(change.SystemIDs) != 0 || len(change.EnvironmentIDs) != 0 {
			t.Fatalf("change over-invalidates unrelated dimensions: %#v", change)
		}
		if change.Reason == "" {
			t.Fatal("change reason is empty")
		}

		// Re-applying the same route is not a change: no second invalidation.
		if _, err := h.svc.UpdatePage(ctx, 1003, PagePatch{RouteTemplate: &route}, 42); err != nil {
			t.Fatalf("UpdatePage same route: %v", err)
		}
		if len(invalidator.changes) != 1 {
			t.Fatalf("a no-op route update invalidated %d times, want 1", len(invalidator.changes))
		}
		_ = page
	})

	t.Run("removed account roles invalidate only those roles", func(t *testing.T) {
		h := newHarness()
		newDataEnv(t, h)
		invalidator := &recordingKnowledgeInvalidator{}
		h.svc.SetKnowledgeInvalidator(invalidator)

		if _, err := h.svc.CreateAccount(ctx, 1001, AccountInput{
			EnvironmentID: 1002, Username: "order_admin", Password: "s3cret",
			Roles: []string{"order_admin", "auditor"},
		}, 42); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		roles := []string{"order_admin"}
		if _, err := h.svc.UpdateAccount(ctx, 1003, AccountPatch{Roles: &roles}, 42); err != nil {
			t.Fatalf("UpdateAccount: %v", err)
		}
		if len(invalidator.changes) != 1 {
			t.Fatalf("invalidation calls = %d, want exactly one", len(invalidator.changes))
		}
		change := invalidator.changes[0]
		if len(change.AccountRoles) != 1 || change.AccountRoles[0] != "auditor" {
			t.Fatalf("account roles = %#v, want the removed role [auditor]", change.AccountRoles)
		}
		if len(change.PageKeys) != 0 || len(change.SystemIDs) != 0 || len(change.EnvironmentIDs) != 0 {
			t.Fatalf("change over-invalidates unrelated dimensions: %#v", change)
		}

		notes := "unchanged roles"
		if _, err := h.svc.UpdateAccount(ctx, 1003, AccountPatch{Notes: &notes}, 42); err != nil {
			t.Fatalf("UpdateAccount notes: %v", err)
		}
		if len(invalidator.changes) != 1 {
			t.Fatalf("a notes-only update invalidated %d times, want 1", len(invalidator.changes))
		}
	})

	t.Run("a knowledge failure never blocks the resource write", func(t *testing.T) {
		h := newHarness()
		newPageEnv(t, h)
		invalidator := &recordingKnowledgeInvalidator{err: errors.New("knowledge unavailable")}
		h.svc.SetKnowledgeInvalidator(invalidator)

		if _, err := h.svc.CreatePage(ctx, 1002, PageInput{
			Name: "Cart", URL: "https://app.example.com/cart", RouteTemplate: "/cart",
		}, 42); err != nil {
			t.Fatalf("CreatePage: %v", err)
		}
		route := "/cart/items"
		updated, err := h.svc.UpdatePage(ctx, 1003, PagePatch{RouteTemplate: &route}, 42)
		if err != nil {
			t.Fatalf("UpdatePage with a failing invalidator: %v", err)
		}
		if updated.RouteTemplate != "/cart/items" {
			t.Fatalf("updated route = %q", updated.RouteTemplate)
		}
		if len(invalidator.changes) != 1 {
			t.Fatalf("invalidation calls = %d, want the best-effort attempt", len(invalidator.changes))
		}

		// Failed CAS writes are logged, never surfaced as a resource error.
		failed := &recordingKnowledgeInvalidator{result: KnowledgeBindingInvalidation{
			Scanned: 3, Matched: 1, Marked: 0, Failed: 1,
		}}
		h.svc.SetKnowledgeInvalidator(failed)
		route = "/cart/broken"
		if _, err := h.svc.UpdatePage(ctx, 1003, PagePatch{RouteTemplate: &route}, 42); err != nil {
			t.Fatalf("UpdatePage with a partially failed invalidation: %v", err)
		}
		if len(failed.changes) != 1 {
			t.Fatalf("failed invalidation calls = %d, want 1", len(failed.changes))
		}
	})
}

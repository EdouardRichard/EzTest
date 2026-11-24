package eventingapi

// v0.2.4-D T8 (SC-D3/R-D4): the run:{id} transcript channel is admin-only.
//
// The gate lives HERE, in the eventing HTTP edge, because the generic event
// endpoints are the two access paths that would otherwise leak a run's model
// transcript to any logged-in user: GET /api/events (persisted history) and
// GET /api/stream (live SSE). Gating only the new Agent Center endpoint would
// leave both open. The third path (the transcript snapshot endpoint) is gated in
// the agent context by its own requireAdminActor.
//
// admin/user is a FUNCTIONAL split (constitution Constraints A.1, Q08=C): the
// management surface reads transcripts, the ordinary surface reads run metadata.
// This mirrors agentapi.requireAdminActor rather than importing it — the eventing
// context must not depend on the agent context's interface layer.

import (
	"net/http"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// requireChannelAdmin admits a channel request. Non-run channels need only the
// authentication the handlers already performed; a run channel additionally
// requires the admin role. It is called AFTER channel validation so a malformed
// channel stays a 400 field error for everyone.
//
// Retained as its own implementation, NOT ratified for cross-context reuse under
// R-H9 (v0.2.4-H T14): it carries run-channel-specific admission policy (the
// IsRunChannel branch), so it is not a pure-vocabulary/pure-function copy of
// agentapi.requireAdminActor and is outside the rule's relaxation. It is recorded
// in the ratification register as "non-reuse, retained"
// (docs/design/architecture-constraints.md §5.1).
func requireChannelAdmin(r *http.Request, channel string) error {
	if !eventingapp.IsRunChannel(channel) {
		return nil
	}
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		return platform.Unauthenticated()
	}
	if !actor.IsAdmin() {
		return platform.Forbidden()
	}
	return nil
}

package execution

// Discovery-observation vocabularies (DB-29, execution_discovery_observation).
//
// T11e-4 (R-H10 full-class closure): these two enums existed only as the DB
// CHECK plus raw literals in the producers, the HTTP handler and the repository
// SQL -- no Go authority anywhere in the repository (T11e-3 §12.1 routed the
// observation-kind half here). Constitution 五.1/五.3: one authority, everything
// else derived. The DB constraint stays the first truth; the schema pin
// TestDiscoveryObservationVocabularyMatchesSchema asserts both directions
// (every constant accepted, a bogus value and each out-of-set constant rejected
// by name) against the live database.

// The ObservationKind* vocabulary is the resource kind of a discovery observation
// (an untyped const group, not a named type). It is
// narrower than AssetKind (asset_registration also registers databases):
// execution-time capture only ever observes navigated pages and HTTP APIs.
// Mirrors chk_execution_discovery_observation_kind
// (execution_0006_v022_wave_observation.sql).
const (
	ObservationKindPage = "page"
	ObservationKindAPI  = "api"
)

// ObservationKinds returns the observation kind vocabulary in declaration order
// so callers derive their lists instead of restating the enum.
func ObservationKinds() []string {
	return []string{ObservationKindPage, ObservationKindAPI}
}

// The ObservationDecision* vocabulary (an untyped const group, not a named type)
// is the classification lifecycle of a discovery
// observation: new (unclassified) -> proposed (a catalog resource was proposed)
// -> approved / rejected / out_of_scope (settled). Mirrors
// chk_execution_discovery_observation_decision and its DEFAULT 'new'.
const (
	ObservationDecisionNew        = "new"
	ObservationDecisionProposed   = "proposed"
	ObservationDecisionApproved   = "approved"
	ObservationDecisionRejected   = "rejected"
	ObservationDecisionOutOfScope = "out_of_scope"
)

// ObservationDecisions returns the decision vocabulary in declaration order.
func ObservationDecisions() []string {
	return []string{
		ObservationDecisionNew, ObservationDecisionProposed, ObservationDecisionApproved,
		ObservationDecisionRejected, ObservationDecisionOutOfScope,
	}
}

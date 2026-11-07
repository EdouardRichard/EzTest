package executionapp

// DiscoveryDeltaObservation is one execution_discovery_observation row read for
// the delta evaluator (W1-12). It is the execution context's read projection:
// the version context owns the delta classification but may not read the
// execution table directly (Agent.md §6.2), so it consumes this via its own
// DiscoveryObservationReader port and an infrastructure adapter.
//
// Decision/ResourceID/FactID are the current classification state persisted on
// the row. ObservationID is the DB-29 primary key and is the provenance id
// carried by the proposal fact and the resource.delta event.
type DiscoveryDeltaObservation struct {
	ObservationID    int64
	VersionID        int64
	WaveID           int64
	CaseRunID        int64
	VersionSUTID     int64
	EnvironmentID    int64
	Kind             string
	Method           string
	Host             string
	ResourceTemplate string
	Decision         string
	ResourceID       int64
	FactID           int64
}

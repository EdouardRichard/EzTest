package agentapp

// H-E2 cap RELATION pin (v0.2.4-H T12).
//
// The producer bound (maxProducedStreamParts) and the message validator bound
// (version.MaxMessageStreamParts) are two independent constants in two bounded
// contexts. Today the produced bound is strictly below the validator bound, and
// that relation — not either number — is what makes an over-limit conversation
// head unreachable from the runner: appendStreamPart refuses parts past the
// producer bound, so the head the terminal seal / recovery sweep writes can
// never exceed what ValidateAssistantMessageParts accepts.
//
// Before this pin nothing recorded the relation. An internal test that derives
// its expectations from maxProducedStreamParts itself stays green when the
// constant changes, and the Host fake bounds by the validator constant without
// ever producing more parts than the producer allows — so raising the producer
// bound past the validator bound (or lowering the validator bound below it)
// would silently re-open the "bad head blocks the whole recovery sweep" defect
// with every test still green.
//
// This pin is therefore a RELATION assertion, never a restated number: it reads
// both authorities. Any change to either constant that breaks the relation
// fails here and forces the orphan-sweep bad-header reachability to be
// re-audited in the same change set (spec §3.5 H-E2, review I8).

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestStreamPartCapRelationPin(t *testing.T) {
	if maxProducedStreamParts >= version.MaxMessageStreamParts {
		t.Fatalf("cap relation changed: produced stream parts %d must stay strictly below the message part limit %d; "+
			"re-audit orphan sweep bad-header reachability (H-E2) before changing either bound",
			maxProducedStreamParts, version.MaxMessageStreamParts)
	}
}

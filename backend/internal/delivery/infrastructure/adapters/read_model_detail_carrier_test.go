package adapters

// H-G4 T8 minor (v0.2.4-H T11): the app DETAIL paths (issue candidates and the
// single-run snapshot) read their own write-side evidence bound and never feed
// the report fingerprint, so the FULL sha carrier read (SHA256sByCaseRun) is
// wasted work there. The carrier feeds ONLY the report path (detail=false,
// EvidenceSHA256s -> report_input digest). This pin keeps the detail paths from
// re-introducing the per-run carrier read; TestProductionCarrierAlwaysNonNil
// keeps the report path's carrier intact.

import (
	"context"
	"testing"
)

type countingEvidence struct {
	EvidenceReader
	carrierCalls int
}

func (c *countingEvidence) SHA256sByCaseRun(ctx context.Context, caseRunID int64) ([]string, error) {
	c.carrierCalls++
	return c.EvidenceReader.SHA256sByCaseRun(ctx, caseRunID)
}

func TestDetailPathDoesNotReadShaCarrier(t *testing.T) {
	deps := newEnrichmentFixture()
	counting := &countingEvidence{EvidenceReader: deps.Evidence}
	deps.Evidence = counting
	reader := NewExecutionReader(deps)
	ctx := context.Background()
	if _, err := reader.ListFailedApplicationCaseRuns(ctx, 7); err != nil {
		t.Fatalf("ListFailedApplicationCaseRuns: %v", err)
	}
	if _, err := reader.CaseRunSnapshot(ctx, 5001); err != nil {
		t.Fatalf("CaseRunSnapshot: %v", err)
	}
	if counting.carrierCalls != 0 {
		t.Fatalf("detail paths read the sha carrier %d times; the carrier feeds only the report fingerprint (detail=false), so these reads are wasted work", counting.carrierCalls)
	}
}

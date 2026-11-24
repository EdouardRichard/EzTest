package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// evidenceProof is the SINGLE server-side evidence gate shared by the P17 asset
// registration (SC-F3) and the R-B4 new-SUT registration (SC-F4). Both gates verify
// the same effective proof, so the rule lives here once (constitution art.5: one
// authority, no second hand-written copy). The model only submits candidate
// content-addressed evidence refs; this gate decides, server-side, whether they
// constitute proof.
//
// For at least one cited ref, effective proof requires:
//  1. the evidence EXISTS in this case_run (looked up by its sha256),
//  2. its sha256 is VALID — the stored content bytes hash to the evidence sha256
//     (content-addressed integrity, constitution art.4.1), and
//  3. the linked assertion PASSED — the assertion_result the evidence is bound to
//     (its step_seq/assertion_seq) carries verdict=passed, the deterministic server
//     verdict, never a model claim.
//
// sutID is the target SUT the proof must belong to WHEN attribution is required:
//   - asset registration passes the resolved SUT id (the asset attaches to it), so
//     evidence attributed to a different SUT does not prove this asset;
//   - new-SUT registration passes nil (the SUT does not exist yet), so attribution
//     is not required — the proof is that the agent reached and validated a resource
//     that matched no existing SUT.
//
// An infra read failure propagates; a ref that simply fails a check is skipped.
type evidenceProof struct {
	evidence AssetProofEvidenceReader
	results  AssetProofResultReader
	content  AssetProofContentReader
}

func (g evidenceProof) prove(ctx context.Context, caseRunID int64, sutID *int64, refs []string) ([]string, error) {
	assertions, err := g.results.ListAssertions(ctx, caseRunID)
	if err != nil {
		return nil, err
	}
	passed := make(map[[2]int]bool, len(assertions))
	for _, a := range assertions {
		if a.Verdict == execution.AssertionPassed {
			passed[[2]int{a.StepSeq, a.AssertionSeq}] = true
		}
	}
	proving := make([]string, 0, len(refs))
	for _, ref := range refs {
		ev, err := g.evidence.EvidenceByCaseRunSHA(ctx, caseRunID, ref)
		if errors.Is(err, execution.ErrNotFound) {
			continue // no evidence for this ref
		}
		if err != nil {
			return nil, err
		}
		// Content-addressed integrity: the stored bytes must hash to the evidence
		// sha256. A mismatch means the cited proof is not the recorded evidence.
		data, err := g.content.Open(ctx, ev.SHA256)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != ev.SHA256 {
			continue // sha256 mismatch
		}
		// When a target SUT is required, the proof must belong to it (when the
		// evidence carries SUT attribution). A new-SUT registration passes nil.
		if sutID != nil && ev.SUTID != nil && *ev.SUTID != *sutID {
			continue
		}
		// The linked assertion must have passed — the deterministic server verdict.
		if ev.StepSeq == nil || ev.AssertionSeq == nil {
			continue
		}
		if !passed[[2]int{*ev.StepSeq, *ev.AssertionSeq}] {
			continue // assertion not passed
		}
		proving = append(proving, ref)
	}
	return proving, nil
}

// proofTime returns the newest created_at among the case_run's evidence rows of
// the given ALREADY-PROVEN refs — the freshness anchor a rejected-asset revival
// is measured against: a rejected asset revives only on proof NEWER than the
// rejection, so replayed old evidence never overturns a human reject (the
// conservative revival rule; the human judgment point is preserved). A zero
// time (no timestamps) is treated as old evidence — fail-closed. An infra read
// failure propagates.
func (g evidenceProof) proofTime(ctx context.Context, caseRunID int64, refs []string) (time.Time, error) {
	var newest time.Time
	for _, ref := range refs {
		ev, err := g.evidence.EvidenceByCaseRunSHA(ctx, caseRunID, ref)
		if err != nil {
			return time.Time{}, err
		}
		if ev.CreatedAt.After(newest) {
			newest = ev.CreatedAt
		}
	}
	return newest, nil
}

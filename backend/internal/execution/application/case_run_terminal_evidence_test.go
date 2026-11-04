package executionapp_test

import (
	"context"
	"encoding/base64"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestFailedCaseRunCapturesTerminalScreenshot pins the P3 §6.4 automatic
// evidence rule: a case_run entering failed/blocked/inconclusive with a live
// browser context captures one screenshot, so a real failure is never a
// zero-evidence row (QA-F1). Best effort: it must not change the verdict.
func TestFailedCaseRunCapturesTerminalScreenshot(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	fixture.sutContext.mu.Lock()
	fixture.sutContext.rows[9101] = execution.CaseRunSUTContext{
		ID: 9101, CaseRunID: 3001, VersionSUTID: 7, AccountRole: "admin",
		BrowserContextID: "ctx_active", Status: execution.SUTContextActive,
	}
	fixture.sutContext.mu.Unlock()

	png := base64.StdEncoding.EncodeToString([]byte("png"))
	fixture.browser.result = executionapp.BrowserActionResult{
		OK: true,
		Artifacts: []executionapp.BrowserArtifact{{
			Kind: "screenshot", Base64: png, Filename: "failure.png", Bytes: 3, MIME: "image/png",
		}},
	}

	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{
		Verdict: execution.CaseRunFailed, FailureClass: execution.FailureClassAutomation,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunFailed {
		t.Fatalf("status = %q, want failed", finished.Status)
	}
	if finished.Summary.EvidenceCount != 1 {
		t.Fatalf("summary evidence count = %d, want the automatic screenshot", finished.Summary.EvidenceCount)
	}
	evidence, err := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if err != nil || len(evidence) != 1 {
		t.Fatalf("evidence = %+v (err %v), want one automatic screenshot", evidence, err)
	}
	if evidence[0].Kind != execution.EvidenceScreenshot {
		t.Fatalf("evidence kind = %q, want screenshot", evidence[0].Kind)
	}
	if evidence[0].VersionSUTID == nil || *evidence[0].VersionSUTID != 7 {
		t.Fatalf("evidence version_sut = %v, want 7", evidence[0].VersionSUTID)
	}
	fixture.browser.mu.Lock()
	defer fixture.browser.mu.Unlock()
	found := false
	for _, action := range fixture.browser.actions {
		if action == "screenshot" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the terminal capture did not call the executor screenshot action: %v", fixture.browser.actions)
	}
}

package executionapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestFailedCaseRunCapturesTerminalScreenshot pins the P3 §6.4 automatic
// evidence rule: a case_run entering failed/blocked/inconclusive with a live
// session captures one screenshot through the official playwright CLI (over the
// general exec channel, T15) and settles it as screenshot evidence, so a real
// failure is never a zero-evidence row (QA-F1). Best effort: it must not change
// the verdict. The retained browser bridge is NOT used for this capture anymore.
func TestFailedCaseRunCapturesTerminalScreenshot(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	fixture.sutContext.mu.Lock()
	fixture.sutContext.rows[9101] = execution.CaseRunSUTContext{
		ID: 9101, CaseRunID: 3001, SUTID: 7, AccountRole: "admin",
		SessionID: "ctx_active", Status: execution.SUTContextActive,
	}
	fixture.sutContext.mu.Unlock()

	// The executor settles the CLI screenshot as a recording-layer artifact; the
	// terminal capture drains the ledger into evidence.
	pngBytes := []byte("png")
	png := base64.StdEncoding.EncodeToString(pngBytes)
	sum := sha256.Sum256(pngBytes)
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{{
			Kind: "screenshot", Base64: png, SHA256: hex.EncodeToString(sum[:]),
			Path: ".playwright-cli/page-terminal.png", Filename: "page-terminal.png",
			Size: int64(len(pngBytes)), MIME: "image/png",
		}},
		NextCursor: 1,
	}}

	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
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
	if evidence[0].SUTID == nil || *evidence[0].SUTID != 7 {
		t.Fatalf("evidence sut id = %v, want 7", evidence[0].SUTID)
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	// The capture ran the CLI screenshot over exec, not the browser bridge.
	foundCmd := false
	for _, command := range fixture.execution.commands {
		if len(command.Argv) >= 2 && command.Argv[0] == "playwright-cli" && command.Argv[1] == "screenshot" {
			foundCmd = true
			break
		}
	}
	if !foundCmd {
		t.Fatalf("the terminal capture did not run the CLI screenshot over exec: %+v", fixture.execution.commands)
	}
}

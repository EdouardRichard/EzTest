package executionapp_test

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestExecSettlesTraceArtifact pins the trace kind's producer: the settlement
// drain asks the executor for its trace file (the trace ledger entry is produced
// on read) and stores the returned trace artifact as trace evidence.
func TestExecSettlesTraceArtifact(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	content := []byte("trace-zip-bytes")
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_1", Outcome: "exited"}
	fixture.execution.traceRef = executionapp.TraceRef{Available: true, Path: "trace.zip"}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{
			{Kind: "command", CommandID: "cmd_1", Filename: "command-cmd_1.json", Base64: "e30="},
			{
				Kind: "trace", Path: "trace.zip", Filename: "trace.zip", MIME: "application/zip",
				Base64: base64.StdEncoding.EncodeToString(content),
			},
		},
		NextCursor: 2,
	}}
	outcome, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(outcome.EvidenceIDs) != 2 {
		t.Fatalf("evidence ids = %v, want the command record and the trace", outcome.EvidenceIDs)
	}
	fixture.execution.mu.Lock()
	probes := fixture.execution.traceProbes
	fixture.execution.mu.Unlock()
	if probes == 0 {
		t.Fatal("the settlement drain never asked the executor to settle its trace")
	}
	rows, err := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if err != nil {
		t.Fatalf("ListByCaseRun: %v", err)
	}
	kinds := map[execution.EvidenceKind]int{}
	for _, row := range rows {
		kinds[row.Kind]++
	}
	if kinds[execution.EvidenceCommand] != 1 || kinds[execution.EvidenceTrace] != 1 {
		t.Fatalf("evidence kinds = %v, want one command and one trace", kinds)
	}
	// The cursor advanced durably, so a second drain settles nothing new.
	row, ok := fixture.sutContext.row(7, "")
	if !ok || row.ArtifactCursor != 2 {
		t.Fatalf("persisted artifact cursor = %+v (found %v), want 2", row, ok)
	}
}

// TestExecRejectsArtifactDigestMismatch: content addressing is verified, not
// trusted — an artifact whose bytes do not match the executor's digest fails.
func TestExecRejectsArtifactDigestMismatch(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_1", Outcome: "exited"}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{{
			Kind: "file", Path: "out.txt", Filename: "out.txt",
			Base64: base64.StdEncoding.EncodeToString([]byte("abc")),
			SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		}},
		NextCursor: 1,
	}}
	if _, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	}); err == nil {
		t.Fatal("a digest mismatch must fail closed, never settle wrong bytes")
	}
}

// TestSessionLeaseRidesTheCaseRunHeartbeat: a command may run far longer than the
// executor session TTL, so the independent heartbeat must renew the session lease
// too — otherwise the executor's orphan sweeper reclaims a working session.
func TestSessionLeaseRidesTheCaseRunHeartbeat(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	runner := executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		Intents:           fixture.intents,
		CaseRuns:          fixture.caseRuns,
		SUTContexts:       fixture.sutContext,
		Results:           fixture.results,
		Evidence:          fixture.evidence,
		Cases:             fixture.cases,
		Bindings:          fixture.bindings,
		Execution:         fixture.execution,
		Content:           fixture.content,
		Settings:          fixture.settings,
		Clock:             fixture.clock,
		IDs:               fixture.ids,
		Events:            fixture.events,
		InstanceID:        "api-1",
		HeartbeatInterval: 5 * time.Millisecond,
	})
	if _, err := runner.Prepare(fixture.runCtx, executionWorkItem()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	fixture.sutContext.mu.Lock()
	fixture.sutContext.rows[9101] = execution.CaseRunSUTContext{
		ID: 9101, CaseRunID: 3001, SUTID: 7, AccountRole: "admin",
		SessionID: "sess_live", Status: execution.SUTContextActive, LeaseEpoch: 3,
	}
	fixture.sutContext.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for {
		fixture.execution.mu.Lock()
		renewed := append([]int64(nil), fixture.execution.renewedEpochs...)
		fixture.execution.mu.Unlock()
		if len(renewed) > 0 {
			for _, epoch := range renewed {
				if epoch != 3 {
					t.Fatalf("renewal epoch = %d, want the dispatch epoch 3", epoch)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the heartbeat never renewed the executor session lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestReusedSessionAdoptsTheNewDispatchEpoch: after a redispatch the existing
// session must be written with the NEW epoch, or the executor fences every write
// of the legitimate new owner with 409 lease_lost.
func TestReusedSessionAdoptsTheNewDispatchEpoch(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	}); err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	// A newer dispatch takes the run over (epoch 4) and reuses the live session.
	fixture.caseRuns.mu.Lock()
	row := fixture.caseRuns.rows[3001]
	row.DispatchEpoch = 4
	fixture.caseRuns.rows[3001] = row
	fixture.caseRuns.mu.Unlock()
	scope := executionScope()
	scope.DispatchEpoch = 4
	if _, err := fixture.runner.Exec(context.Background(), scope, executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	}); err != nil {
		t.Fatalf("Exec after takeover: %v", err)
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if fixture.execution.sessions != 1 {
		t.Fatalf("sessions = %d, want the existing session reused", fixture.execution.sessions)
	}
	last := fixture.execution.leaseEpochs[len(fixture.execution.leaseEpochs)-1]
	if last != 4 {
		t.Fatalf("write epoch after takeover = %d, want 4 (all: %v)", last, fixture.execution.leaseEpochs)
	}
}

// TestExecRequiresArgv: the general execution path validates before it creates a
// session (no empty command can leak a workspace).
func TestExecRequiresArgv(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{}); err == nil {
		t.Fatal("an empty argv must be rejected")
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if fixture.execution.sessions != 0 {
		t.Fatalf("a rejected command created %d sessions", fixture.execution.sessions)
	}
}

// TestExecUsesTheDispatchEpochAsLeaseEpoch pins the fencing contract on the
// application side: the session is created and written with the case_run dispatch
// epoch, so a taken-over dispatch cannot mutate the new owner's session.
func TestExecUsesTheDispatchEpochAsLeaseEpoch(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if fixture.execution.lastSpec.LeaseEpoch != 3 {
		t.Fatalf("session lease epoch = %d, want the dispatch epoch 3", fixture.execution.lastSpec.LeaseEpoch)
	}
	if len(fixture.execution.leaseEpochs) == 0 {
		t.Fatal("no write carried a lease epoch")
	}
	for _, epoch := range fixture.execution.leaseEpochs {
		if epoch != 3 {
			t.Fatalf("a write carried epoch %d, want 3 (all: %v)", epoch, fixture.execution.leaseEpochs)
		}
	}
	if fixture.execution.lastSpec.Workspace == "" {
		t.Fatal("the session spec carries no workspace anchor")
	}
}

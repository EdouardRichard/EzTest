package executionapp_test

// Task 112: analysis.failure_submit must not create a second severity source.
// The event/projection severity is derived from the case priority (P2 → minor),
// the category from the failed assertion kind, and the stored summary drops the
// analyst's contradictory "severity upgraded to major" claim.

import (
	"context"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestFailureSubmitUsesCanonicalSeverityAndSanitizesSummary(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	fixture.cases.snapshot.Priority = "P2"
	fixture.cases.snapshot.Assertions[0].Kind = "api"
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{Seq: 1, Status: "failed"}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "500", Reason: "boom",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunFailed}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	const summary = "定时发布能力整体缺失，重跑无法修复。severity 由 minor 上调为 major：整条 P2 能力完全缺失。"
	if _, err := fixture.runner.FailureSubmit(context.Background(), p3Scope(), tools.FailureSubmitInput{
		CaseRunID: 3001, FailureClass: "application", Category: "ui", Severity: "major", Summary: summary,
	}); err != nil {
		t.Fatalf("FailureSubmit: %v", err)
	}

	run, err := fixture.caseRuns.Get(context.Background(), 3001)
	if err != nil {
		t.Fatalf("Get run: %v", err)
	}
	if strings.Contains(run.Summary.LastError, "上调") || strings.Contains(strings.ToLower(run.Summary.LastError), "severity") {
		t.Fatalf("stored summary kept the severity claim: %q", run.Summary.LastError)
	}
	if !strings.Contains(run.Summary.LastError, "定时发布能力整体缺失") {
		t.Fatalf("stored summary lost the observation: %q", run.Summary.LastError)
	}

	fixture.events.mu.Lock()
	defer fixture.events.mu.Unlock()
	found := false
	for _, event := range fixture.events.events {
		if event.Type != "execution.failure_candidate" {
			continue
		}
		found = true
		if event.Payload["category"] != "api" {
			t.Fatalf("event category = %v, want the assertion kind api", event.Payload["category"])
		}
		if event.Payload["severity"] != "minor" {
			t.Fatalf("event severity = %v, want the P2 canonical minor", event.Payload["severity"])
		}
		if payloadSummary, _ := event.Payload["summary"].(string); strings.Contains(payloadSummary, "上调") {
			t.Fatalf("event summary kept the severity claim: %q", payloadSummary)
		}
	}
	if !found {
		t.Fatal("no execution.failure_candidate event was published")
	}
}

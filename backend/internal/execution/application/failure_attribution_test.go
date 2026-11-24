package executionapp_test

// v0.2.4-C T2: failure attribution relocated from agent/application/explore into
// the execution context — its only consumer chain is attribution_read.go →
// execution.result_read (the analyze.failure result the failure_analyst reads).
// These tests pin the relocated classifier's contract: the deterministic
// four-value classification (application / automation / environment / data),
// retry-vs-human, and the application-only problem draft. Semantics are identical
// to the pre-relocation explore implementation; expectations come from that
// contract, not from observed output.

import (
	"context"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// TestFailureAttributionSeverityUsesCasePriority is migrated verbatim from
// explore/failure_severity_test.go (Task 112): the deterministic attribution
// draft is the value the failure analyst reads. Its severity must come from the
// case priority (the same source as the delivery issue record), not from the
// assertion kind: a P2 API failure is minor, not major, so the model never sees
// a contradictory "upgrade" signal.
func TestFailureAttributionSeverityUsesCasePriority(t *testing.T) {
	attribution := executionapp.NewFailureAttribution()
	cases := []struct {
		priority string
		category string
		want     string
	}{
		{"P0", "ui", "major"},
		{"P1", "api", "major"},
		{"P2", "api", "minor"},
		{"P2", "db", "minor"},
		{"P3", "ui", "trivial"},
	}
	for _, tc := range cases {
		result, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
			CaseRunID:    9001,
			CaseTitle:    "scheduled publish",
			FailureClass: "application",
			Category:     tc.category,
			Priority:     tc.priority,
			Reason:       "missing publish-time control",
		})
		if err != nil {
			t.Fatalf("Classify(%s/%s): %v", tc.priority, tc.category, err)
		}
		if result.Severity != tc.want {
			t.Fatalf("severity for %s/%s = %q, want %q", tc.priority, tc.category, result.Severity, tc.want)
		}
		if result.Problem == nil || result.Problem.Severity != tc.want {
			t.Fatalf("problem draft for %s/%s = %+v, want severity %q", tc.priority, tc.category, result.Problem, tc.want)
		}
	}
}

// TestFailureAttributionClassifyFourValues pins the four-value classification,
// the retry budget rule (only an environment failure within budget retries) and
// the application-only problem draft of the relocated classifier.
func TestFailureAttributionClassifyFourValues(t *testing.T) {
	attribution := executionapp.NewFailureAttribution()
	cases := []struct {
		name           string
		signal         executionapp.FailureSignal
		classification string
		category       string
		severity       string
		retryable      bool
		action         string
		wantProblem    bool
	}{
		{
			name: "application defect drafts a problem",
			signal: executionapp.FailureSignal{
				CaseRunID: 3001, CaseVersionID: 41, CaseTitle: "submit order",
				FailureClass: "application", Priority: "P2",
				Reason:      "expected button text 提交订单 got 提交",
				EvidenceIDs: []int64{9001},
				Attempts:    0, MaxAttempts: 3,
			},
			classification: executionapp.FailureClassificationApplication,
			category:       "application",
			severity:       "minor",
			retryable:      false,
			action:         executionapp.FailureActionHuman,
			wantProblem:    true,
		},
		{
			name: "automation keyword never retries",
			signal: executionapp.FailureSignal{
				CaseRunID: 3002, CaseVersionID: 41,
				FailureClass: "automation", Priority: "P2",
				Reason:   "target_not_found: 提交 button",
				Attempts: 0, MaxAttempts: 3,
			},
			classification: executionapp.FailureClassificationAutomation,
			category:       "ui",
			severity:       "minor",
			retryable:      false,
			action:         executionapp.FailureActionHuman,
		},
		{
			name: "environment failure within budget retries",
			signal: executionapp.FailureSignal{
				CaseRunID: 3003, CaseVersionID: 41,
				FailureClass: "environment", Priority: "P2",
				Reason:   "net::ERR_CONNECTION_TIMED_OUT while opening SUT",
				Attempts: 1, MaxAttempts: 3,
			},
			classification: executionapp.FailureClassificationEnvironment,
			category:       "environment",
			severity:       "major",
			retryable:      true,
			action:         executionapp.FailureActionRetry,
		},
		{
			name: "environment failure with exhausted budget hands off",
			signal: executionapp.FailureSignal{
				CaseRunID: 3004, CaseVersionID: 41,
				FailureClass: "environment", Priority: "P2",
				Reason:   "net::ERR_CONNECTION_TIMED_OUT while opening SUT",
				Attempts: 3, MaxAttempts: 3,
			},
			classification: executionapp.FailureClassificationEnvironment,
			category:       "environment",
			severity:       "major",
			retryable:      false,
			action:         executionapp.FailureActionHuman,
		},
		{
			name: "data keyword needs data repair, not retry",
			signal: executionapp.FailureSignal{
				CaseRunID: 3005, CaseVersionID: 41,
				FailureClass: "session", Priority: "P2",
				Reason:   "account credential missing for role admin",
				Attempts: 0, MaxAttempts: 3,
			},
			classification: executionapp.FailureClassificationData,
			category:       "data",
			severity:       "minor",
			retryable:      false,
			action:         executionapp.FailureActionHuman,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := attribution.Classify(context.Background(), 1001, tc.signal)
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if result.Classification != tc.classification {
				t.Fatalf("classification = %q, want %q", result.Classification, tc.classification)
			}
			if result.Category != tc.category {
				t.Fatalf("category = %q, want %q", result.Category, tc.category)
			}
			if result.Severity != tc.severity {
				t.Fatalf("severity = %q, want %q", result.Severity, tc.severity)
			}
			if result.Retryable != tc.retryable {
				t.Fatalf("retryable = %v, want %v", result.Retryable, tc.retryable)
			}
			if result.Action != tc.action {
				t.Fatalf("action = %q, want %q", result.Action, tc.action)
			}
			if result.Reason == "" {
				t.Fatalf("attribution reason is empty")
			}
			if tc.wantProblem {
				if result.Problem == nil {
					t.Fatalf("application failure has no problem draft: %+v", result)
				}
				if result.Problem.Title == "" || result.Problem.DedupeKey == "" {
					t.Fatalf("problem draft = %+v, want title and dedupe key", result.Problem)
				}
				if len(result.Problem.EvidenceIDs) != len(tc.signal.EvidenceIDs) {
					t.Fatalf("problem draft evidence = %v, want %v", result.Problem.EvidenceIDs, tc.signal.EvidenceIDs)
				}
			} else if result.Problem != nil {
				t.Fatalf("non-application failure produced a problem draft: %+v", result.Problem)
			}
		})
	}
}

// TestFailureAttributionRejectsInvalidVersion pins the failure path: a
// non-positive version id is rejected before any classification.
func TestFailureAttributionRejectsInvalidVersion(t *testing.T) {
	attribution := executionapp.NewFailureAttribution()
	if _, err := attribution.Classify(context.Background(), 0, executionapp.FailureSignal{
		CaseRunID: 3001, FailureClass: "application", Reason: "expected 提交订单 got 提交",
	}); err == nil {
		t.Fatalf("Classify with version 0 succeeded, want error")
	}
}

// TestFailureAttribution is migrated verbatim from explore/analysis_test.go
// (Task 37 P9-T4): the full four-value contract including the deterministic
// dedupe key and the nine-value P3 taxonomy mapping when the failure reason
// carries no keyword evidence of its own.
func TestFailureAttribution(t *testing.T) {
	attribution := executionapp.NewFailureAttribution()

	application, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9001,
		CaseTitle:    "提交订单",
		FailureClass: "failed",
		Category:     "ui",
		Reason:       "expected button text 提交订单 got 提交",
		EvidenceIDs:  []int64{3001, 3002},
		Attempts:     1,
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("application Classify: %v", err)
	}
	if application.Classification != executionapp.FailureClassificationApplication ||
		application.Action != executionapp.FailureActionHuman || application.Retryable {
		t.Fatalf("application attribution = %+v", application)
	}
	if application.Problem == nil {
		t.Fatal("application failure must produce a problem report draft")
	}
	if application.Problem.Title == "" || application.Problem.Summary == "" ||
		len(application.Problem.DedupeKey) != 64 || len(application.Problem.EvidenceIDs) != 2 {
		t.Fatalf("problem draft = %+v", application.Problem)
	}
	again, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9001,
		CaseTitle:    "提交订单",
		FailureClass: "failed",
		Category:     "ui",
		Reason:       "expected button text 提交订单 got 提交",
		EvidenceIDs:  []int64{3001, 3002},
		Attempts:     1,
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("application Classify again: %v", err)
	}
	if again.Problem == nil || again.Problem.DedupeKey != application.Problem.DedupeKey {
		t.Fatalf("dedupe key is not deterministic: %+v vs %+v", application.Problem, again.Problem)
	}

	environment, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9002,
		FailureClass: "blocked",
		Reason:       "net::ERR_CONNECTION_TIMED_OUT while opening http://sut.test",
		Attempts:     1,
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("environment Classify: %v", err)
	}
	if environment.Classification != executionapp.FailureClassificationEnvironment ||
		environment.Action != executionapp.FailureActionRetry || !environment.Retryable {
		t.Fatalf("environment attribution = %+v", environment)
	}
	if environment.Problem != nil {
		t.Fatalf("environment failure must not create a problem draft: %+v", environment.Problem)
	}

	exhausted, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9003,
		FailureClass: "blocked",
		Reason:       "net::ERR_CONNECTION_TIMED_OUT",
		Attempts:     3,
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("exhausted Classify: %v", err)
	}
	if exhausted.Action != executionapp.FailureActionHuman || exhausted.Retryable {
		t.Fatalf("exhausted environment attribution = %+v", exhausted)
	}

	automation, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9004,
		FailureClass: "failed",
		Reason:       "target_not_found: selector #submit",
	})
	if err != nil {
		t.Fatalf("automation Classify: %v", err)
	}
	if automation.Classification != executionapp.FailureClassificationAutomation ||
		automation.Action != executionapp.FailureActionHuman || automation.Retryable {
		t.Fatalf("automation attribution = %+v", automation)
	}
	if automation.Problem != nil {
		t.Fatalf("automation failure must not create a problem draft: %+v", automation.Problem)
	}

	data, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9005,
		FailureClass: "blocked",
		Reason:       "account_secret_unavailable: no bound test account",
	})
	if err != nil {
		t.Fatalf("data Classify: %v", err)
	}
	if data.Classification != executionapp.FailureClassificationData || data.Action != executionapp.FailureActionHuman {
		t.Fatalf("data attribution = %+v", data)
	}

	// The P3 execution taxonomy (nine values, supplied by execution.result_read)
	// is mapped onto the four attribution classes when the failure reason itself
	// carries no keyword evidence.
	environmentClass, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9006,
		FailureClass: string(executionapp.FailureClassificationEnvironment),
	})
	if err != nil {
		t.Fatalf("environment class signal: %v", err)
	}
	if environmentClass.Classification != executionapp.FailureClassificationEnvironment ||
		environmentClass.Action != executionapp.FailureActionRetry {
		t.Fatalf("environment class attribution = %+v", environmentClass)
	}
	automationClass, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9007,
		FailureClass: string(executionapp.FailureClassificationAutomation),
	})
	if err != nil {
		t.Fatalf("automation class signal: %v", err)
	}
	if automationClass.Classification != executionapp.FailureClassificationAutomation {
		t.Fatalf("automation class attribution = %+v", automationClass)
	}
	sessionClass, err := attribution.Classify(context.Background(), 1001, executionapp.FailureSignal{
		CaseRunID:    9008,
		FailureClass: "session",
	})
	if err != nil {
		t.Fatalf("session class signal: %v", err)
	}
	if sessionClass.Classification != executionapp.FailureClassificationData {
		t.Fatalf("session class attribution = %+v", sessionClass)
	}

	if _, err := attribution.Classify(context.Background(), 0, executionapp.FailureSignal{}); err == nil {
		t.Fatal("Classify must reject a non-positive version id")
	}
}

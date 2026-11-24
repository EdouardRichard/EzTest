package designapp

// Whole-branch review Important #4 (constitution VII.6/VII.7): the automatic
// converge path used to return nil for a typed 409 review verdict AND for an
// infrastructure fault alike. A persistent judgment-store outage therefore pinned
// versions in designing with no log, event or metric — an operator could not tell
// "the gate says not yet" from "the gate cannot be read".
//
// The verdict stays quiet (it is the designed outcome of every pass while drafts
// are still reworking); an infrastructure fault must be attributable.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// capturePlannerLogs routes the planner's logger into a buffer.
func capturePlannerLogs(planner *Planner) *bytes.Buffer {
	var buf bytes.Buffer
	planner.SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// newConvergeObservabilityFixture gives the converge path one clarified
// requirement with COMPLETE coverage and one draft case, so the shared gate
// reaches the review-state read (where an infrastructure fault surfaces).
func newConvergeObservabilityFixture() (*Planner, *fakeReviewStates, *fakeDesignConverger, *bytes.Buffer) {
	planner, repo, states, _, converger := newReviewGateFixture()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1"}, nil, [][]string{{"AC-1"}})
	return planner, states, converger, capturePlannerLogs(planner)
}

// TestConvergeReviewVerdictIsQuiet pins the expected-outcome half: a closed gate
// (a draft still pending review) is a typed 409 verdict. It must NOT converge and
// must NOT log a warning — every reconcile pass hits it while the author works.
func TestConvergeReviewVerdictIsQuiet(t *testing.T) {
	planner, states, converger, logs := newConvergeObservabilityFixture()
	states.set(7001, design.ReviewStatePending)

	if err := planner.convergeCompleteDesign(context.Background(), 1001); err != nil {
		t.Fatalf("converge returned %v, want nil (best-effort idempotent pass)", err)
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("closed review gate converged the version: %v", converger.versionIDs)
	}
	if logged := logs.String(); strings.Contains(logged, "level=WARN") {
		t.Fatalf("a 409 review verdict logged a warning:\n%s", logged)
	}
}

// TestConvergeInfraFaultLogsWarning pins the degradation half: an infrastructure
// read failure inside the gate is NOT a verdict. Converge still does not advance
// (and still returns nil so one version cannot abort the whole reconcile pass),
// but the fault must be logged at WARN with the version id and the cause.
func TestConvergeInfraFaultLogsWarning(t *testing.T) {
	planner, states, converger, logs := newConvergeObservabilityFixture()
	states.err = errors.New("judgment store: connection refused")

	if err := planner.convergeCompleteDesign(context.Background(), 1001); err != nil {
		t.Fatalf("converge returned %v, want nil (the pass retries)", err)
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("an unreadable review gate converged the version: %v", converger.versionIDs)
	}
	logged := logs.String()
	for _, want := range []string{"level=WARN", "version_id=1001", "connection refused"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("infra fault log missing %q:\n%s", want, logged)
		}
	}
}

// TestConvergeInfraFaultCoversTypedNonVerdictErrors proves the discrimination is
// by HTTP status, not by "is it a platform.Error": a typed 503 (store unwired /
// unavailable) is an infrastructure fault and must be logged, while a typed 409
// is the verdict and must not.
func TestConvergeInfraFaultCoversTypedNonVerdictErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		wantLog bool
	}{
		{"typed 409 verdict", platform.StateConflict("drafts still reworking"), false},
		{"typed 503 unavailable", platform.Unavailable("the judgment store is not wired"), true},
		{"typed 500 internal", platform.NewError(500, "internal", "read failed"), true},
		{"untyped fault", errors.New("dial tcp: timeout"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			planner, states, converger, logs := newConvergeObservabilityFixture()
			states.err = test.err

			if err := planner.convergeCompleteDesign(context.Background(), 1001); err != nil {
				t.Fatalf("converge returned %v, want nil", err)
			}
			if len(converger.versionIDs) != 0 {
				t.Fatalf("converged with a failing gate: %v", converger.versionIDs)
			}
			if got := strings.Contains(logs.String(), "level=WARN"); got != test.wantLog {
				t.Fatalf("WARN logged = %v, want %v for %v:\n%s", got, test.wantLog, test.err, logs.String())
			}
		})
	}
}

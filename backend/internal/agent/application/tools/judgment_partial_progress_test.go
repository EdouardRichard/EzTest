package tools

// H-F10b #28: design.judgment_submit settles a BATCH one case at a time and is not
// atomic. A mid-batch failure used to return the bare port error, hiding the
// entries that had already been settled as append-only judgment facts — so the
// model would resubmit the whole batch and hit duplicate-fact conflicts on the
// entries that had succeeded. The error now carries the partial progress while
// keeping the typed classification in its chain.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// partialFailureDesignPort fails the Nth SubmitJudgment call and settles the rest.
// It wraps the shared fake so every other DesignPort method keeps working.
type partialFailureDesignPort struct {
	*fakeDesignPort
	failAt int
	err    error
}

func (p *partialFailureDesignPort) SubmitJudgment(ctx context.Context, versionID, requirementID, runID int64, in JudgmentInput) (any, error) {
	result, err := p.fakeDesignPort.SubmitJudgment(ctx, versionID, requirementID, runID, in)
	if p.fakeDesignPort.judgmentCalls == p.failAt {
		return nil, p.err
	}
	return result, err
}

func TestJudgmentSubmitMidBatchFailureReportsPartialProgress(t *testing.T) {
	conflict := platform.StateConflict("a judgment already exists for this case version")
	port := &partialFailureDesignPort{
		fakeDesignPort: &fakeDesignPort{judgmentResult: map[string]any{"judgment_id": "9001"}},
		failAt:         2,
		err:            conflict,
	}
	tool := &designJudgmentSubmit{deps: Deps{Design: port}}

	_, err := tool.Execute(context.Background(), judgmentScope(), json.RawMessage(judgmentSubmitRaw))
	if err == nil {
		t.Fatal("the second case judgment failed but the tool reported success")
	}
	message := err.Error()
	for _, want := range []string{
		"1 of 2 case judgments were already settled",
		"judgments[1]",
		"already-settled case_version_ids=[5]",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("partial-progress report missing %q: %s", want, message)
		}
	}
	// The typed classification survives the wrap: the caller still maps a 409.
	var toolErr *Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("the wrapped error lost its tool classification: %T %v", err, err)
	}
	if toolErr.Code != CodeStateConflict {
		t.Fatalf("tool error code = %q, want %q", toolErr.Code, CodeStateConflict)
	}
}

// TestJudgmentSubmitFirstEntryFailureHasNoPartialProgress is the discriminating
// control: when nothing settled there is no progress to report, and the error must
// stay the plain mapped port error (no misleading "0 of N settled" noise).
func TestJudgmentSubmitFirstEntryFailureHasNoPartialProgress(t *testing.T) {
	port := &partialFailureDesignPort{
		fakeDesignPort: &fakeDesignPort{},
		failAt:         1,
		err:            platform.Invalid("case_version_id", "unknown case version"),
	}
	tool := &designJudgmentSubmit{deps: Deps{Design: port}}
	_, err := tool.Execute(context.Background(), judgmentScope(), json.RawMessage(judgmentSubmitRaw))
	if err == nil {
		t.Fatal("the first case judgment failed but the tool reported success")
	}
	if strings.Contains(err.Error(), "already settled") {
		t.Fatalf("a failure with no settled entries must not claim partial progress: %s", err)
	}
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument {
		t.Fatalf("first-entry failure classification = %v, want invalid_argument", err)
	}
}

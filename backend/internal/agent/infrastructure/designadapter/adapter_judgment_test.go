package designadapter_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/designadapter"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeJudgmentSubmitter struct {
	versionID     int64
	requirementID int64
	runID         int64
	input         designapp.SubmitJudgmentInput
	calls         int
	result        designapp.SubmitJudgmentResult
	err           error
}

func (f *fakeJudgmentSubmitter) SubmitJudgment(_ context.Context, versionID, requirementID, runID int64, in designapp.SubmitJudgmentInput) (designapp.SubmitJudgmentResult, error) {
	f.calls++
	f.versionID = versionID
	f.requirementID = requirementID
	f.runID = runID
	f.input = in
	return f.result, f.err
}

type fakeJudgmentRounds struct {
	latest *design.Judgment
	count  int
	err    error
}

func (f *fakeJudgmentRounds) LatestJudgment(_ context.Context, _ int64) (*design.Judgment, error) {
	return f.latest, f.err
}
func (f *fakeJudgmentRounds) CountReworks(_ context.Context, _ int64) (int, error) {
	return f.count, f.err
}

type fakeJudgeSettings struct {
	maxRounds int64
	err       error
}

func (f *fakeJudgeSettings) Int(_ context.Context, key string) (int64, error) {
	if key != designapp.MaxReworkRoundsKey {
		return 0, errors.New("unexpected settings key: " + key)
	}
	return f.maxRounds, f.err
}

func judgmentInput() tools.JudgmentInput {
	return tools.JudgmentInput{
		CaseID: 7001, CaseVersionID: 6, Verdict: design.VerdictFail,
		Findings: []tools.JudgmentFindingInput{
			{Dimension: "executability", Severity: design.SeverityBlocker, Message: "步骤不可执行", CaseRef: "7002"},
		},
	}
}

// TestSubmitJudgmentMapsInputAndSettles pins the adapter maps the tool input into
// designapp.SubmitJudgmentInput (findings verbatim) and renders the settled fact
// binding as the snake_case decimal-string view.
func TestSubmitJudgmentMapsInputAndSettles(t *testing.T) {
	submitter := &fakeJudgmentSubmitter{result: designapp.SubmitJudgmentResult{
		JudgmentID: 9001, CaseID: 7001, CaseVersionID: 6,
		ContentHash: strings.Repeat("a", 64), Round: 1, Verdict: design.VerdictFail,
	}}
	adapter := designadapter.New(&fakeGraphGateway{}).SetJudgment(submitter, &fakeJudgmentRounds{}, &fakeJudgeSettings{maxRounds: 1})

	result, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput())
	if err != nil {
		t.Fatalf("SubmitJudgment: %v", err)
	}
	if submitter.versionID != 1001 || submitter.requirementID != 10 || submitter.runID != 99 {
		t.Fatalf("submit scope = (%d,%d,%d)", submitter.versionID, submitter.requirementID, submitter.runID)
	}
	in := submitter.input
	if in.CaseID != 7001 || in.CaseVersionID != 6 || in.Verdict != design.VerdictFail {
		t.Fatalf("input = %#v", in)
	}
	if len(in.Findings) != 1 || in.Findings[0].Dimension != "executability" ||
		in.Findings[0].Severity != design.SeverityBlocker || in.Findings[0].CaseRef != "7002" {
		t.Fatalf("finding = %#v", in.Findings)
	}
	payload := result.(map[string]any)
	if payload["judgment_id"] != "9001" || payload["case_id"] != "7001" || payload["case_version_id"] != "6" ||
		payload["content_hash"] != strings.Repeat("a", 64) || payload["round"] != 1 || payload["verdict"] != design.VerdictFail {
		t.Fatalf("payload = %#v", payload)
	}
}

// TestSubmitJudgmentMapsACCoverageMatrix pins the SC-E5 matrix mapping (T13 fix
// round 1): the optional per-case ac_coverage_matrix crosses the adapter into
// designapp.SubmitJudgmentInput.ACCoverageMatrix verbatim (T6 validates/stores
// it); an absent matrix stays nil.
func TestSubmitJudgmentMapsACCoverageMatrix(t *testing.T) {
	submitter := &fakeJudgmentSubmitter{}
	adapter := designadapter.New(&fakeGraphGateway{}).SetJudgment(submitter, &fakeJudgmentRounds{}, &fakeJudgeSettings{maxRounds: 1})
	in := judgmentInput()
	in.ACCoverageMatrix = map[string]any{
		"AC-1": map[string]any{"covered": true, "case_refs": []any{"7001"}},
		"AC-2": map[string]any{"covered": false},
	}
	if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, in); err != nil {
		t.Fatalf("SubmitJudgment: %v", err)
	}
	got := submitter.input.ACCoverageMatrix
	if len(got) != 2 {
		t.Fatalf("matrix = %#v, want 2 AC entries", got)
	}
	ac1, _ := got["AC-1"].(map[string]any)
	if ac1 == nil || ac1["covered"] != true {
		t.Fatalf("AC-1 entry = %#v", got["AC-1"])
	}

	submitter2 := &fakeJudgmentSubmitter{}
	adapter2 := designadapter.New(&fakeGraphGateway{}).SetJudgment(submitter2, &fakeJudgmentRounds{}, &fakeJudgeSettings{maxRounds: 1})
	if _, err := adapter2.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
		t.Fatalf("SubmitJudgment without matrix: %v", err)
	}
	if submitter2.input.ACCoverageMatrix != nil {
		t.Fatalf("absent matrix must stay nil: %#v", submitter2.input.ACCoverageMatrix)
	}
}

// TestSubmitJudgmentRoundDerivation pins the server-supplied round rules: a first
// judgment / host-edit rejudge keeps round 0 (the service derives conservatively),
// a rework rejudge after a fail within budget increments to latest.Round+1, and a
// fail with the budget spent keeps round 0 (no false escalation).
func TestSubmitJudgmentRoundDerivation(t *testing.T) {
	failOlder := &design.Judgment{CaseID: 7001, CaseVersionID: 5, Round: 1, Verdict: design.VerdictFail}
	passOlder := &design.Judgment{CaseID: 7001, CaseVersionID: 5, Round: 1, Verdict: design.VerdictPass}
	cases := []struct {
		name      string
		latest    *design.Judgment
		count     int
		maxRounds int64
		wantRound int
	}{
		{"first judgment", nil, 0, 1, 0},
		{"same version already judged", &design.Judgment{CaseID: 7001, CaseVersionID: 6, Round: 1, Verdict: design.VerdictFail}, 0, 1, 0},
		{"host edit after pass keeps round", passOlder, 0, 1, 0},
		{"rework after fail within budget increments", failOlder, 0, 1, 2},
		{"rework after fail with budget spent keeps round", failOlder, 1, 1, 0},
		{"multi-round rework increments from latest", &design.Judgment{CaseID: 7001, CaseVersionID: 5, Round: 2, Verdict: design.VerdictFail}, 1, 3, 3},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			submitter := &fakeJudgmentSubmitter{}
			adapter := designadapter.New(&fakeGraphGateway{}).SetJudgment(
				submitter,
				&fakeJudgmentRounds{latest: testCase.latest, count: testCase.count},
				&fakeJudgeSettings{maxRounds: testCase.maxRounds},
			)
			if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
				t.Fatalf("SubmitJudgment: %v", err)
			}
			if submitter.input.Round != testCase.wantRound {
				t.Fatalf("round = %d, want %d", submitter.input.Round, testCase.wantRound)
			}
		})
	}
}

// TestSubmitJudgmentNegativeReworkBoundFailsClosed pins H-F10b #30: a NEGATIVE
// configured design.max_rework_rounds is an invalid setting, not "budget spent".
// The design service fails closed on it (JudgmentService.maxReworkRounds ->
// platform.Unavailable); the adapter used to answer round 0, silently judging at
// the wrong round while the service would refuse the submission. Both sides now
// apply ONE rule (constitution VII.6). The zero bound is the discriminating
// control: zero is a legitimate "no rework allowed" budget and must still settle
// at round 0.
func TestSubmitJudgmentNegativeReworkBoundFailsClosed(t *testing.T) {
	failOlder := &design.Judgment{CaseID: 7001, CaseVersionID: 5, Round: 1, Verdict: design.VerdictFail}
	t.Run("negative bound fails closed", func(t *testing.T) {
		submitter := &fakeJudgmentSubmitter{}
		adapter := designadapter.New(&fakeGraphGateway{}).SetJudgment(
			submitter, &fakeJudgmentRounds{latest: failOlder}, &fakeJudgeSettings{maxRounds: -1})
		_, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput())
		if err == nil {
			t.Fatal("a negative rework bound was silently treated as round 0")
		}
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 503 {
			t.Fatalf("negative bound error = %v, want a 503 unavailable platform error", err)
		}
		if submitter.calls != 0 {
			t.Fatalf("the submission reached the service %d times despite the invalid bound", submitter.calls)
		}
	})
	t.Run("zero bound is a legitimate budget", func(t *testing.T) {
		submitter := &fakeJudgmentSubmitter{}
		adapter := designadapter.New(&fakeGraphGateway{}).SetJudgment(
			submitter, &fakeJudgmentRounds{latest: failOlder}, &fakeJudgeSettings{maxRounds: 0})
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
			t.Fatalf("zero bound must remain a valid budget: %v", err)
		}
		if submitter.input.Round != 0 {
			t.Fatalf("round = %d, want 0 (no rework allowed)", submitter.input.Round)
		}
	})
}

// TestSubmitJudgmentNilRoundSourceFallsBack pins that an unwired round source
// passes round 0 (the service derives conservatively) rather than failing.
func TestSubmitJudgmentNilRoundSourceFallsBack(t *testing.T) {
	submitter := &fakeJudgmentSubmitter{}
	adapter := designadapter.New(&fakeGraphGateway{}).SetJudgment(submitter, nil, nil)
	if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
		t.Fatalf("SubmitJudgment: %v", err)
	}
	if submitter.input.Round != 0 {
		t.Fatalf("round = %d, want 0 fallback", submitter.input.Round)
	}
}

// TestSubmitJudgmentNilServiceFailsClosed pins the internal configuration error
// when the judgment service is unwired.
func TestSubmitJudgmentNilServiceFailsClosed(t *testing.T) {
	adapter := designadapter.New(&fakeGraphGateway{})
	if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err == nil {
		t.Fatal("nil judgment service submit succeeded")
	}
}

// TestSubmitJudgmentBusinessErrorStaysPlatformError pins that a service rejection
// crosses the adapter as a platform error the tools layer can map.
func TestSubmitJudgmentBusinessErrorStaysPlatformError(t *testing.T) {
	submitter := &fakeJudgmentSubmitter{err: platform.StateConflict("case_version_id is not the current version")}
	adapter := designadapter.New(&fakeGraphGateway{}).SetJudgment(submitter, &fakeJudgmentRounds{}, &fakeJudgeSettings{maxRounds: 1})
	_, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput())
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a platform error", err)
	}
}

type fakeReworkProvenance struct {
	at               time.Time
	err              error
	calls            int
	gotCaseVersionID int64
}

func (f *fakeReworkProvenance) ReworkRequestedAt(_ context.Context, _, _, caseVersionID int64) (time.Time, error) {
	f.calls++
	f.gotCaseVersionID = caseVersionID
	return f.at, f.err
}

// TestSubmitJudgmentRoundProvenance pins the T14 refinement of judgeRound
// (SC-E4: a Host edit rejudges at the SAME round; only a judge-triggered
// rework increments it). With a provenance reader wired, a latest FAIL bound
// to an older version only increments the round when a design.rework BOUND TO
// THAT FAIL'S VERSION exists (the payload's from-version binding; status
// irrelevant — the request exists at creation and the judge may settle while
// the rework item is still running, fix round 1). A Host edit that lands a new
// draft with no rework responding to this fail keeps round 0 even though the
// rework budget still allows another round. Without a provenance reader
// (nil), the pre-T14 heuristic (any new version after fail within budget
// increments) is preserved unchanged for backward compatibility.
func TestSubmitJudgmentRoundProvenance(t *testing.T) {
	failTime := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	failOlder := &design.Judgment{CaseID: 7001, CaseVersionID: 5, Round: 1, Verdict: design.VerdictFail, CreatedAt: failTime}

	t.Run("host edit with no rework request keeps round", func(t *testing.T) {
		submitter := &fakeJudgmentSubmitter{}
		provenance := &fakeReworkProvenance{at: time.Time{}} // no rework ever requested
		adapter := designadapter.New(&fakeGraphGateway{}).
			SetJudgment(submitter, &fakeJudgmentRounds{latest: failOlder, count: 0}, &fakeJudgeSettings{maxRounds: 1}).
			SetReworkProvenance(provenance)
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
			t.Fatalf("SubmitJudgment: %v", err)
		}
		if submitter.input.Round != 0 {
			t.Fatalf("round = %d, want 0 (Host edit, no rework request, SC-E4)", submitter.input.Round)
		}
		if provenance.calls != 1 {
			t.Fatalf("provenance reader calls = %d, want 1", provenance.calls)
		}
	})
	t.Run("rework requested after the fail increments round", func(t *testing.T) {
		submitter := &fakeJudgmentSubmitter{}
		provenance := &fakeReworkProvenance{at: failTime.Add(time.Minute)}
		adapter := designadapter.New(&fakeGraphGateway{}).
			SetJudgment(submitter, &fakeJudgmentRounds{latest: failOlder, count: 0}, &fakeJudgeSettings{maxRounds: 1}).
			SetReworkProvenance(provenance)
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
			t.Fatalf("SubmitJudgment: %v", err)
		}
		if submitter.input.Round != 2 {
			t.Fatalf("round = %d, want 2 (rework requested after the fail)", submitter.input.Round)
		}
	})
	t.Run("provenance is queried for the latest fail's version", func(t *testing.T) {
		// Fix round 1: the from-version binding — the adapter must ask for the
		// rework responding to THIS fail (failOlder.CaseVersionID=5), never a
		// generic per-case timestamp that a stale rework could satisfy.
		submitter := &fakeJudgmentSubmitter{}
		provenance := &fakeReworkProvenance{at: failTime.Add(time.Minute)}
		adapter := designadapter.New(&fakeGraphGateway{}).
			SetJudgment(submitter, &fakeJudgmentRounds{latest: failOlder, count: 0}, &fakeJudgeSettings{maxRounds: 1}).
			SetReworkProvenance(provenance)
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
			t.Fatalf("SubmitJudgment: %v", err)
		}
		if provenance.gotCaseVersionID != failOlder.CaseVersionID {
			t.Fatalf("provenance queried for version %d, want the latest fail's version %d",
				provenance.gotCaseVersionID, failOlder.CaseVersionID)
		}
	})
	t.Run("rework requested before the fail is stale, keeps round", func(t *testing.T) {
		// A rework from an EARLIER round (before this fail was recorded) must
		// not be mistaken for provenance of the CURRENT new version.
		submitter := &fakeJudgmentSubmitter{}
		provenance := &fakeReworkProvenance{at: failTime.Add(-time.Minute)}
		adapter := designadapter.New(&fakeGraphGateway{}).
			SetJudgment(submitter, &fakeJudgmentRounds{latest: failOlder, count: 0}, &fakeJudgeSettings{maxRounds: 1}).
			SetReworkProvenance(provenance)
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
			t.Fatalf("SubmitJudgment: %v", err)
		}
		if submitter.input.Round != 0 {
			t.Fatalf("round = %d, want 0 (stale rework provenance)", submitter.input.Round)
		}
	})
	t.Run("budget spent keeps round even with fresh rework provenance", func(t *testing.T) {
		submitter := &fakeJudgmentSubmitter{}
		provenance := &fakeReworkProvenance{at: failTime.Add(time.Minute)}
		adapter := designadapter.New(&fakeGraphGateway{}).
			SetJudgment(submitter, &fakeJudgmentRounds{latest: failOlder, count: 1}, &fakeJudgeSettings{maxRounds: 1}).
			SetReworkProvenance(provenance)
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
			t.Fatalf("SubmitJudgment: %v", err)
		}
		if submitter.input.Round != 0 {
			t.Fatalf("round = %d, want 0 (budget spent)", submitter.input.Round)
		}
	})
	t.Run("nil provenance reader preserves the pre-T14 heuristic", func(t *testing.T) {
		submitter := &fakeJudgmentSubmitter{}
		adapter := designadapter.New(&fakeGraphGateway{}).
			SetJudgment(submitter, &fakeJudgmentRounds{latest: failOlder, count: 0}, &fakeJudgeSettings{maxRounds: 1})
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
			t.Fatalf("SubmitJudgment: %v", err)
		}
		if submitter.input.Round != 2 {
			t.Fatalf("round = %d, want 2 (backward-compatible heuristic when provenance is unwired)", submitter.input.Round)
		}
	})
	t.Run("provenance read error propagates", func(t *testing.T) {
		submitter := &fakeJudgmentSubmitter{}
		provenance := &fakeReworkProvenance{err: errors.New("work queue down")}
		adapter := designadapter.New(&fakeGraphGateway{}).
			SetJudgment(submitter, &fakeJudgmentRounds{latest: failOlder, count: 0}, &fakeJudgeSettings{maxRounds: 1}).
			SetReworkProvenance(provenance)
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err == nil {
			t.Fatal("provenance read error must propagate, not silently fall back")
		}
	})
	t.Run("first judgment never consults provenance", func(t *testing.T) {
		submitter := &fakeJudgmentSubmitter{}
		provenance := &fakeReworkProvenance{at: failTime}
		adapter := designadapter.New(&fakeGraphGateway{}).
			SetJudgment(submitter, &fakeJudgmentRounds{latest: nil, count: 0}, &fakeJudgeSettings{maxRounds: 1}).
			SetReworkProvenance(provenance)
		if _, err := adapter.SubmitJudgment(context.Background(), 1001, 10, 99, judgmentInput()); err != nil {
			t.Fatalf("SubmitJudgment: %v", err)
		}
		if provenance.calls != 0 {
			t.Fatalf("provenance must not be consulted for a first judgment, calls = %d", provenance.calls)
		}
	})
}

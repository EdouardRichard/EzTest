package designapi

// v0.2.4-H T10 (H-F10 T16): the judgment DTO used to swallow an AC-coverage
// matrix encoding failure and ship an empty object instead. The failure is now
// surfaced as a typed 500 — an unreachable-but-loud path instead of a silent
// degradation (constitution VII.6).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

func TestJudgmentReportMatrixEncodeFailureIsLoud(t *testing.T) {
	handler, _, _ := newTestHandler()
	judgments := &fakeJudgmentService{
		report: designapp.JudgmentReportView{Cases: []designapp.JudgmentCaseJudgmentView{{
			CaseID: 7001, CurrentVersionID: 7002, ReviewState: design.ReviewStatePass,
			Judgment: &design.Judgment{
				ID: 4001, CaseID: 7001, CaseVersionID: 7002, Round: 1, Verdict: design.VerdictPass,
				// A channel cannot be JSON-encoded. A JSONB-decoded matrix never
				// contains one, which is exactly why the branch must fail loudly
				// rather than quietly emit {} if the invariant is ever broken.
				ACCoverageMatrix: map[string]any{"AC-1": make(chan int)},
			},
		}}},
	}
	handler.SetJudgmentService(judgments)
	recorder := httptest.NewRecorder()
	judgmentTestMux(handler).ServeHTTP(recorder,
		judgmentRequest(http.MethodGet, "/api/versions/1001/design/judgments", "user", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500 (never a silently emptied matrix)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "judgment_matrix_encode_failed") {
		t.Fatalf("error body must name the cause: %s", recorder.Body.String())
	}
}

// TestJudgmentDTOKeepsMatrixContent is the discriminating counterpart: a
// normal matrix is still encoded verbatim (the fix must not drop the payload).
func TestJudgmentDTOKeepsMatrixContent(t *testing.T) {
	dto, err := toJudgmentDTO(design.Judgment{
		ID: 4001, CaseID: 7001, CaseVersionID: 7002, Round: 1, Verdict: design.VerdictPass,
		ACCoverageMatrix: map[string]any{"AC-1": []any{"7001"}},
	})
	if err != nil {
		t.Fatalf("toJudgmentDTO: %v", err)
	}
	if string(dto.ACCoverageMatrix) != `{"AC-1":["7001"]}` {
		t.Fatalf("matrix = %s, want the stored content", dto.ACCoverageMatrix)
	}
	empty, err := toJudgmentDTO(design.Judgment{ID: 4002})
	if err != nil || string(empty.ACCoverageMatrix) != `{}` {
		t.Fatalf("absent matrix = %s (err %v), want {}", empty.ACCoverageMatrix, err)
	}
}

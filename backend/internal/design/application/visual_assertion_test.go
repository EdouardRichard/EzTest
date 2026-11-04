package designapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// fakeVisualAssertionResolver is the design context's view of the visual
// context: the active visual_assertion configurations of one case version.
type fakeVisualAssertionResolver struct {
	refs      map[int64][]VisualAssertionRef
	err       error
	calls     int
	versionID int64
	caseVID   int64
}

func (f *fakeVisualAssertionResolver) ListActiveVisualAssertions(_ context.Context, versionID, caseVersionID int64) ([]VisualAssertionRef, error) {
	f.calls++
	f.versionID = versionID
	f.caseVID = caseVersionID
	if f.err != nil {
		return nil, f.err
	}
	return f.refs[caseVersionID], nil
}

// seedVisualAssertionCase seeds a complete draft case version that carries a
// kind=visual assertion at step 1 assertion 2. The first assertion stays the
// non-visual one created by the shared fixture.
func seedVisualAssertionCase(t *testing.T, repo *fakeRepo, caseID int64) design.CaseVersion {
	t.Helper()
	seedFakeCase(t, repo, caseID, 5001, "视觉断言用例", design.CaseStatusDraft)
	version := repo.cases[caseID].current
	version.Assertions = append(version.Assertions, design.CaseAssertion{
		ID: caseID * 100, CaseVersionID: version.ID,
		StepSeq: 1, AssertionSeq: len(version.Assertions) + 1,
		Kind: design.AssertionVisual, Target: "首页截图", Expected: "页面与基线一致",
	})
	version.AssertionCount = len(version.Assertions)
	return *version
}

func visualAssertionIDOf(version design.CaseVersion) int64 {
	for _, assertion := range version.Assertions {
		if assertion.Kind == design.AssertionVisual {
			return assertion.ID
		}
	}
	return 0
}

// TestVisualAssertionRequiresReference pins Task 115: a case version that
// carries kind=visual assertions may only be confirmed when every visual
// assertion resolves to an active visual_assertion configuration. Retry11's
// FR-025 case was confirmed with a visual assertion that had no
// visual_assertion_id and then executed into inconclusive; confirmation must
// instead return a typed repair block, and a later resolved reference must
// confirm normally.
func TestVisualAssertionRequiresReference(t *testing.T) {
	t.Run("missing configuration blocks confirmation", func(t *testing.T) {
		service, repo, publisher, audit := newConfirmFixture(t)
		version := seedVisualAssertionCase(t, repo, 7001)
		resolver := &fakeVisualAssertionResolver{}
		service.SetVisualAssertionResolver(resolver)

		_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
			Scope: design.ConfirmScopeCase, TargetID: 7001,
		}, 42)
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("Confirm error = %v, want a typed *platform.Error", err)
		}
		if apiErr.Status != http.StatusConflict || apiErr.Code != "visual_assertion_unresolved" {
			t.Fatalf("error = %d/%s, want 409/visual_assertion_unresolved", apiErr.Status, apiErr.Code)
		}
		if !strings.Contains(apiErr.Message, "step 1 assertion 2") {
			t.Fatalf("message = %q, want the unresolved assertion position", apiErr.Message)
		}
		if got, _ := apiErr.Details["case_version_id"].(string); got != platformid.Format(version.ID) {
			t.Fatalf("details case_version_id = %v, want %d", apiErr.Details["case_version_id"], version.ID)
		}
		if next, _ := apiErr.Details["next_action"].(string); next == "" {
			t.Fatalf("details = %#v, want a repair next_action", apiErr.Details)
		}
		if resolver.calls != 1 || resolver.versionID != 1001 || resolver.caseVID != version.ID {
			t.Fatalf("resolver calls=%d version=%d case_version=%d", resolver.calls, resolver.versionID, resolver.caseVID)
		}
		if status := repo.cases[7001].current.Status; status != design.CaseStatusDraft {
			t.Fatalf("unresolved visual assertion confirmed the version: %q", status)
		}
		if len(repo.confirmations) != 0 {
			t.Fatalf("confirmation rows = %d, want 0", len(repo.confirmations))
		}
		if len(publisher.events) != 0 || len(audit.events) != 0 {
			t.Fatalf("blocked confirmation published events=%#v audit=%#v", publisher.events, audit.events)
		}
	})

	t.Run("resolver is required", func(t *testing.T) {
		service, repo, _, _ := newConfirmFixture(t)
		seedVisualAssertionCase(t, repo, 7001)

		_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
			Scope: design.ConfirmScopeCase, TargetID: 7001,
		}, 42)
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("Confirm error = %v, want a typed *platform.Error", err)
		}
		if apiErr.Status != http.StatusConflict || apiErr.Code != "visual_assertion_unresolved" {
			t.Fatalf("error = %d/%s, want 409/visual_assertion_unresolved", apiErr.Status, apiErr.Code)
		}
		if len(repo.confirmations) != 0 {
			t.Fatalf("confirmation rows = %d, want 0", len(repo.confirmations))
		}
	})

	t.Run("resolution failure is typed", func(t *testing.T) {
		service, repo, _, _ := newConfirmFixture(t)
		seedVisualAssertionCase(t, repo, 7001)
		service.SetVisualAssertionResolver(&fakeVisualAssertionResolver{
			err: platform.Unavailable("visual context is unavailable"),
		})

		_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
			Scope: design.ConfirmScopeCase, TargetID: 7001,
		}, 42)
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
			t.Fatalf("Confirm error = %v, want a typed 503", err)
		}
		if status := repo.cases[7001].current.Status; status != design.CaseStatusDraft {
			t.Fatalf("unavailable resolver changed the version status: %q", status)
		}
		if len(repo.confirmations) != 0 {
			t.Fatalf("confirmation rows = %d, want 0", len(repo.confirmations))
		}
	})

	t.Run("resolved reference confirms", func(t *testing.T) {
		service, repo, _, _ := newConfirmFixture(t)
		version := seedVisualAssertionCase(t, repo, 7001)
		service.SetVisualAssertionResolver(&fakeVisualAssertionResolver{
			refs: map[int64][]VisualAssertionRef{
				version.ID: {{ID: 8801, CaseAssertionID: visualAssertionIDOf(version)}},
			},
		})

		result, err := service.Confirm(context.Background(), 1001, ConfirmInput{
			Scope: design.ConfirmScopeCase, TargetID: 7001,
		}, 42)
		if err != nil {
			t.Fatalf("Confirm with resolved visual assertion: %v", err)
		}
		if len(result.Confirmed) != 1 || result.Confirmed[0].CaseVersionID != version.ID {
			t.Fatalf("confirmed = %+v, want case version %d", result.Confirmed, version.ID)
		}
		if status := repo.cases[7001].current.Status; status != design.CaseStatusConfirmed {
			t.Fatalf("version status = %q, want confirmed", status)
		}
		if len(repo.confirmations) != 1 {
			t.Fatalf("confirmation rows = %d, want 1", len(repo.confirmations))
		}
	})
}

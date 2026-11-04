package designapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// VisualAssertionRef is the identity of one active visual assertion
// configuration attached to a design case_assertion. The visual context owns
// the authoritative (case_assertion_id -> visual_assertion_id) mapping; the
// design context only consumes it.
type VisualAssertionRef struct {
	ID              int64
	CaseAssertionID int64
}

// VisualAssertionResolver resolves the active visual assertion configurations of
// one case version. Task 115: a case version carrying kind=visual assertions
// may only be confirmed when every such assertion resolves to an active
// visual_assertion row, so execution can map it to the execution.visual_assert
// tool input instead of ending in inconclusive(visual_assertion_not_run).
type VisualAssertionResolver interface {
	ListActiveVisualAssertions(ctx context.Context, versionID, caseVersionID int64) ([]VisualAssertionRef, error)
}

// visualAssertionPosition is the human-readable position of one design visual
// assertion. case_assertion.order is not persisted, so AssertionSeq is derived
// exactly like the execution read (1-based per step, ascending case_assertion
// ID; P3-D-06).
type visualAssertionPosition struct {
	CaseAssertionID int64
	StepSeq         int
	AssertionSeq    int
}

// caseVersionVisualAssertionPositions returns the visual assertions of one case
// version in execution position order.
func caseVersionVisualAssertionPositions(assertions []design.CaseAssertion) []visualAssertionPosition {
	positions := make([]visualAssertionPosition, 0)
	stepSeq, seq := 0, 0
	for _, assertion := range assertions {
		if assertion.StepSeq != stepSeq {
			stepSeq = assertion.StepSeq
			seq = 0
		}
		seq++
		if assertion.Kind != design.AssertionVisual {
			continue
		}
		assertionSeq := seq
		if assertion.AssertionSeq > 0 {
			assertionSeq = assertion.AssertionSeq
		}
		positions = append(positions, visualAssertionPosition{
			CaseAssertionID: assertion.ID,
			StepSeq:         assertion.StepSeq,
			AssertionSeq:    assertionSeq,
		})
	}
	return positions
}

// validateVisualAssertionReferences enforces the Task 115 rule: every
// kind=visual assertion of the version being confirmed must resolve to an
// active visual_assertion configuration. A missing configuration is a typed
// 409 block/repair response, never a silent inconclusive execution later.
func (s *GraphService) validateVisualAssertionReferences(ctx context.Context, versionID int64, item design.CaseWithCurrent) error {
	if item.Current == nil || item.Current.ID <= 0 {
		return nil
	}
	data, err := s.repo.ReadCase(ctx, versionID, item.Case.ID, &item.Current.ID)
	if err != nil {
		return mapDesignError(err, "case")
	}
	positions := caseVersionVisualAssertionPositions(data.Selected.Assertions)
	if len(positions) == 0 {
		return nil
	}
	version := *item.Current
	if version.CaseID <= 0 {
		version.CaseID = item.Case.ID
	}
	if s.visual == nil {
		return unresolvedVisualAssertionError(version, positions,
			"the visual assertion resolver is not configured")
	}
	resolved, err := s.visual.ListActiveVisualAssertions(ctx, versionID, version.ID)
	if err != nil {
		var apiErr *platform.Error
		if errors.As(err, &apiErr) {
			return apiErr
		}
		return fmt.Errorf("resolve visual assertions of case version %s: %w", platformid.Format(version.ID), err)
	}
	configured := make(map[int64]struct{}, len(resolved))
	for _, ref := range resolved {
		if ref.ID > 0 && ref.CaseAssertionID > 0 {
			configured[ref.CaseAssertionID] = struct{}{}
		}
	}
	unresolved := make([]visualAssertionPosition, 0, len(positions))
	for _, position := range positions {
		if position.CaseAssertionID <= 0 {
			unresolved = append(unresolved, position)
			continue
		}
		if _, ok := configured[position.CaseAssertionID]; !ok {
			unresolved = append(unresolved, position)
		}
	}
	if len(unresolved) == 0 {
		return nil
	}
	return unresolvedVisualAssertionError(version, unresolved, "")
}

// unresolvedVisualAssertionError is the typed 409 block/repair response of a
// case version whose visual assertions have no resolvable visual_assertion_id.
// The message and Details carry the positions and the repair action because the
// design UI renders the API message.
func unresolvedVisualAssertionError(version design.CaseVersion, unresolved []visualAssertionPosition, reason string) *platform.Error {
	positions := make([]string, 0, len(unresolved))
	assertionIDs := make([]string, 0, len(unresolved))
	for _, position := range unresolved {
		positions = append(positions, fmt.Sprintf("step %d assertion %d", position.StepSeq, position.AssertionSeq))
		if position.CaseAssertionID > 0 {
			assertionIDs = append(assertionIDs, platformid.Format(position.CaseAssertionID))
		}
	}
	title := strings.TrimSpace(version.Title)
	if title == "" {
		title = "untitled case"
	}
	message := fmt.Sprintf(
		"case %q version %d has %d visual assertion(s) without a resolvable visual_assertion_id (%s); configure each visual assertion (and approve its baseline so the step can yield pass/fail) before confirming the case.",
		title, version.ID, len(unresolved), strings.Join(positions, ", "),
	)
	if reason != "" {
		message += " " + reason + "."
	}
	details := map[string]any{
		"case_id":               platformid.Format(version.CaseID),
		"case_version_id":       platformid.Format(version.ID),
		"unresolved_assertions": positions,
		"missing":               []string{"visual_assertion_id"},
		"next_action":           "configure the visual assertion for each design visual assertion (and approve its baseline), then confirm the case again",
	}
	if len(assertionIDs) > 0 {
		details["unresolved_case_assertion_ids"] = assertionIDs
	}
	return &platform.Error{
		Status:  http.StatusConflict,
		Code:    "visual_assertion_unresolved",
		Message: message,
		Details: details,
	}
}

package designapp

import (
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// Task 120: design generation defaults to non-visual assertions.
//
// A normal functional acceptance criterion must produce text/ui/api (or db)
// assertions. Only a requirement/AC that explicitly asks for a visual baseline
// comparison may generate kind=visual. Retry11's fresh design turned FR-025's
// "the reading page does not show the preview/draft marker" into a visual
// assertion; S3/S4 do not configure a visual_assertion, so Task 115 now
// rejects that confirmation with 409 visual_assertion_unresolved.
//
// The design prompts tell the model the same rule. This function is the
// deterministic safety net at the only design-generation write entry point: it
// defaults a non-explicit visual assertion to a browser-level UI assertion
// while keeping its concrete target and expected text unchanged. It never
// drops an assertion and never disables an explicit visual request.
const defaultNonVisualAssertionKind = design.AssertionUI

// visualBaselineRequestPhrases are the explicit requirement/AC phrases that
// authorize a visual assertion. Bare words like "视觉" or "基线" are
// deliberately absent: "视觉上不显示某标识" or "基线数据" are ordinary
// functional criteria, and FR-025's marker-absence claim is one of them.
var visualBaselineRequestPhrases = []string{
	"视觉基线", "视觉回归", "视觉比对", "视觉对比", "视觉差异", "视觉断言",
	"像素比对", "像素对比", "像素差异", "像素匹配", "像素一致", "像素级",
	"截图基线", "基线截图", "截图比对", "截图对比", "截图验证", "截图回归",
	"图像比对", "图片比对", "图片对比", "基线图",
	"与基线一致", "与基线截图一致", "ui 基线", "ui基线",
	"visual baseline", "visual regression", "visual comparison", "visual diff",
	"pixel comparison", "pixel diff", "pixel-by-pixel",
	"screenshot baseline", "baseline screenshot", "screenshot comparison", "screenshot diff",
}

// visualBaselineRequested reports whether any supplied requirement/AC text
// explicitly asks for a visual baseline comparison. Matching is
// case-insensitive for the English phrases.
func visualBaselineRequested(texts ...string) bool {
	for _, text := range texts {
		normalized := strings.ToLower(strings.TrimSpace(text))
		if normalized == "" {
			continue
		}
		for _, phrase := range visualBaselineRequestPhrases {
			if strings.Contains(normalized, phrase) {
				return true
			}
		}
	}
	return false
}

// normalizeProposalAssertionKinds applies Task 120 to one case.graph_propose
// input before validation and persistence. It returns the number of assertions
// defaulted away from kind=visual.
//
// Explicit visual evidence is taken from the requirement (title, description,
// acceptance criteria) or from the case's own title/description/preconditions.
// A visual assertion's own target/expected is not evidence: otherwise the
// model could justify a visual assertion that the requirement never asked for.
func normalizeProposalAssertionKinds(in *ProposeInput, requirement design.RequirementRef) int {
	if in == nil {
		return 0
	}
	// ProposeInput is passed by value but its slices are shared with the
	// caller. Copy the case/assertion slices before rewriting kinds so applying
	// the default never mutates the caller's proposal.
	in.Cases = append([]CaseInput(nil), in.Cases...)
	for caseIndex := range in.Cases {
		in.Cases[caseIndex].Assertions = append([]AssertionInput(nil), in.Cases[caseIndex].Assertions...)
	}

	requirementRequestsVisual := visualBaselineRequested(
		requirement.Title, requirement.Description, requirement.AcceptanceCriteria,
	)
	defaulted := 0
	for caseIndex := range in.Cases {
		item := &in.Cases[caseIndex]
		if requirementRequestsVisual ||
			visualBaselineRequested(item.Title, item.Description, item.Preconditions) {
			continue
		}
		for assertionIndex := range item.Assertions {
			assertion := &item.Assertions[assertionIndex]
			if assertion.Kind != design.AssertionVisual {
				continue
			}
			assertion.Kind = defaultNonVisualAssertionKind
			defaulted++
		}
	}
	return defaulted
}

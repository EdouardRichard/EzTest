package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// Judgment submission bounds. H-F10b #27 (thorough fix): the three payload bounds
// are ALIASES of the design DOMAIN authority (design.MaxJudgmentFindings /
// JudgmentDimensionMax / JudgmentMessageMax), not a second copy — the tool rejects
// early so the model gets a precise field error and the design application service
// re-validates the same limits, and both now read ONE constant (constitution
// V.1/V.4). The earlier plain-literal mirror needed a source-scanning pin to stay
// in sync; an alias cannot drift.
//
// The authority sits in design/domain (aliased by design/application under its
// existing exported names), not in design/application: importing the application
// package here would close a test import cycle
// (design/application's internal tests -> version/application -> agent tools).
//
// maxJudgmentSubmitCases is NOT a mirror: the per-call batch cap is this tool
// surface's own authority (the design service settles one case at a time and has
// no batch bound).
const (
	maxJudgmentSubmitCases    = 50
	maxJudgmentSubmitFindings = design.MaxJudgmentFindings
	judgmentDimensionMax      = design.JudgmentDimensionMax
	judgmentMessageMax        = design.JudgmentMessageMax
)

// judgmentSubmitPayload is the decoded design.judgment_submit arguments. The
// json tags are the model-facing contract (six surfaces): they match the schema
// field names and the Invalid field paths exactly. Round is deliberately absent.
type judgmentSubmitPayload struct {
	RequirementID string                `json:"requirement_id"`
	Judgments     []judgmentCasePayload `json:"judgments"`
}

type judgmentCasePayload struct {
	CaseID           string                   `json:"case_id"`
	CaseVersionID    string                   `json:"case_version_id"`
	Verdict          string                   `json:"verdict"`
	Findings         []judgmentFindingPayload `json:"findings"`
	ACCoverageMatrix json.RawMessage          `json:"ac_coverage_matrix"`
}

type judgmentFindingPayload struct {
	Dimension string  `json:"dimension"`
	Severity  string  `json:"severity"`
	Message   string  `json:"message"`
	CaseRef   *string `json:"case_ref"`
}

// judgmentSubmitResult is the settled-batch result. Submitted carries one
// settled fact binding per case (the adapter's snake_case view); Count is the
// number of settled candidates. The json tags are the result surface promised
// to the model.
type judgmentSubmitResult struct {
	Submitted []any `json:"submitted"`
	Count     int   `json:"count"`
}

type designJudgmentSubmit struct{ deps Deps }

func (t *designJudgmentSubmit) Name() string { return ToolDesignJudgmentSubmit }

func (t *designJudgmentSubmit) Description() string {
	return "独立裁判逐用例提交质量判定（design.judgment_submit）：每个用例给 verdict=pass|fail 与 findings。" +
		"存在 blocker/important finding 必须判 fail；仅 minor 可判 pass 并附注记。" +
		"只写裁判事实，不修改用例、不确认用例或需求；判定只是候选，服务端校验后落事实，确认永远由人工完成。" +
		"合法顶层字段：requirement_id、judgments；judgments[].case_id/case_version_id 取自当前脑图的 draft 用例，" +
		"verdict 枚举 pass/fail，findings[].dimension（coverage/executability/system）、severity（blocker/important/minor）、message、case_ref（可选，本需求用例 ID），" +
		"ac_coverage_matrix（可选，AC×用例覆盖矩阵 JSON 对象）。" +
		"round 由服务端供给，不要传；未知字段会被严格拒绝。"
}

func (t *designJudgmentSubmit) Parameters() json.RawMessage {
	findingSchema := schemaObject(map[string]any{
		"dimension": map[string]any{
			"type": "string", "minLength": 1, "maxLength": judgmentDimensionMax,
			"description": "必填。审查维度：coverage（覆盖率）/executability（可执行标准）/system（关联系统）。",
		},
		"severity": map[string]any{
			"type": "string", "enum": []any{design.SeverityBlocker, design.SeverityImportant, design.SeverityMinor},
			"description": "必填。严重度枚举：blocker/important → 该用例必须 fail；仅 minor → 可 pass 带注记。",
		},
		"message": map[string]any{
			"type": "string", "minLength": 1, "maxLength": judgmentMessageMax,
			"description": "必填。可执行、可追溯到 AC 原文或用例内容的发现说明，不编造需求未表达的要求。",
		},
		"case_ref": idStringSchema(),
	}, "dimension", "severity", "message")
	caseSchema := schemaObject(map[string]any{
		"case_id":         idStringSchema(),
		"case_version_id": idStringSchema(),
		"verdict": map[string]any{
			"type": "string", "enum": []any{design.VerdictPass, design.VerdictFail},
			"description": "必填。用例判定枚举：pass/fail。存在 blocker/important finding 时必须 fail。",
		},
		"findings": map[string]any{
			"type": "array", "maxItems": maxJudgmentSubmitFindings, "items": findingSchema,
			"description": "可选。该用例的发现列表；pass 不得携带 blocker/important finding。case_ref 省略为需求级发现。",
		},
		"ac_coverage_matrix": map[string]any{
			"type": "object", "additionalProperties": true,
			"description": "可选。AC×用例覆盖矩阵（自由 JSON 对象，键为 AC id，如 AC-1）：记录每条 AC 是否被本用例语义覆盖及依据，供 case_review 展示。",
		},
	}, "case_id", "case_version_id", "verdict")
	return schemaObject(map[string]any{
		"requirement_id": idStringSchema(),
		"judgments": map[string]any{
			"type": "array", "minItems": 1, "maxItems": maxJudgmentSubmitCases, "items": caseSchema,
			"description": "必填。逐用例判定列表，至少 1 项；case_id/case_version_id 取自当前脑图 draft 用例（design.graph_read）。",
		},
	}, "requirement_id", "judgments")
}

func (t *designJudgmentSubmit) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Design == nil {
		return nil, missingDep("design")
	}
	var payload judgmentSubmitPayload
	if err := decodeParams(raw, &payload); err != nil {
		return nil, err
	}
	if scope.RequirementID <= 0 {
		return nil, Invalid("requirement_id", "design.judgment_submit requires a requirement-scoped run")
	}
	requirementID, err := parseRequiredID("requirement_id", payload.RequirementID)
	if err != nil {
		return nil, err
	}
	if requirementID != scope.RequirementID {
		return nil, Invalid("requirement_id", "must match the injected requirement scope")
	}
	if len(payload.Judgments) == 0 {
		return nil, Invalid("judgments", "must contain at least one case judgment")
	}
	if len(payload.Judgments) > maxJudgmentSubmitCases {
		return nil, Invalid("judgments", fmt.Sprintf("must contain at most %d case judgments", maxJudgmentSubmitCases))
	}

	submitted := make([]any, 0, len(payload.Judgments))
	settledVersions := make([]string, 0, len(payload.Judgments))
	for index, item := range payload.Judgments {
		field := func(name string) string { return fmt.Sprintf("judgments[%d].%s", index, name) }
		caseID, err := parseRequiredID(field("case_id"), item.CaseID)
		if err != nil {
			return nil, err
		}
		caseVersionID, err := parseRequiredID(field("case_version_id"), item.CaseVersionID)
		if err != nil {
			return nil, err
		}
		verdict := strings.TrimSpace(item.Verdict)
		if !design.ValidVerdict(verdict) {
			return nil, InvalidWithAllowed(field("verdict"), "must be one of pass, fail",
				[]string{design.VerdictPass, design.VerdictFail},
				"blocker/important findings mean fail; only minor findings allow a pass verdict")
		}
		if len(item.Findings) > maxJudgmentSubmitFindings {
			return nil, Invalid(field("findings"), fmt.Sprintf("at most %d findings are allowed", maxJudgmentSubmitFindings))
		}
		findings := make([]JudgmentFindingInput, 0, len(item.Findings))
		for findingIndex, finding := range item.Findings {
			findingField := func(name string) string {
				return fmt.Sprintf("judgments[%d].findings[%d].%s", index, findingIndex, name)
			}
			dimension := strings.TrimSpace(finding.Dimension)
			if err := validateText(findingField("dimension"), dimension, 1, judgmentDimensionMax); err != nil {
				return nil, err
			}
			severity := strings.TrimSpace(finding.Severity)
			if !design.ValidSeverity(severity) {
				return nil, InvalidWithAllowed(findingField("severity"), "must be one of blocker, important, minor",
					[]string{design.SeverityBlocker, design.SeverityImportant, design.SeverityMinor},
					"use the exact enum value; blocker/important mean fail")
			}
			message := strings.TrimSpace(finding.Message)
			if err := validateText(findingField("message"), message, 1, judgmentMessageMax); err != nil {
				return nil, err
			}
			caseRef := ""
			if finding.CaseRef != nil && strings.TrimSpace(*finding.CaseRef) != "" {
				if _, err := parsePositiveID(findingField("case_ref"), strings.TrimSpace(*finding.CaseRef)); err != nil {
					return nil, err
				}
				caseRef = strings.TrimSpace(*finding.CaseRef)
			}
			findings = append(findings, JudgmentFindingInput{
				Dimension: dimension, Severity: severity, Message: message, CaseRef: caseRef,
			})
		}
		var matrix map[string]any
		if trimmed := bytes.TrimSpace(item.ACCoverageMatrix); len(trimmed) > 0 && string(trimmed) != "null" {
			if !json.Valid(trimmed) || !bytes.HasPrefix(trimmed, []byte("{")) {
				return nil, Invalid(field("ac_coverage_matrix"), "must be a JSON object keyed by AC id")
			}
			if err := json.Unmarshal(trimmed, &matrix); err != nil {
				return nil, Invalid(field("ac_coverage_matrix"), "must be a JSON object keyed by AC id")
			}
		}
		result, portErr := t.deps.Design.SubmitJudgment(ctx, scope.VersionID, requirementID, scope.AgentRunID, JudgmentInput{
			CaseID: caseID, CaseVersionID: caseVersionID, Verdict: verdict, Findings: findings,
			ACCoverageMatrix: matrix,
		})
		if portErr != nil {
			// H-F10b #28: the batch is NOT atomic — every earlier entry is already
			// settled as an append-only judgment fact. Returning the bare port error
			// hid that progress, so the model would resubmit the whole batch and hit
			// duplicate-fact conflicts on the entries that already succeeded. Report
			// the partial progress on the error itself (the typed classification is
			// preserved through %w) so the retry can skip the settled entries.
			return nil, judgmentPartialProgressError(portErr, index, len(payload.Judgments), settledVersions)
		}
		submitted = append(submitted, result)
		settledVersions = append(settledVersions, item.CaseVersionID)
	}
	return judgmentSubmitResult{Submitted: submitted, Count: len(submitted)}, nil
}

// judgmentPartialProgressError wraps a mid-batch settlement failure with the
// progress already made: how many entries settled, which case versions they
// cover, and which index failed. The underlying error stays in the chain, so the
// caller's typed classification (400 field error vs 409 state conflict) is
// unchanged — this only adds the attribution the non-atomic batch owes
// (constitution VII.6/VII.7). The settled ids come from the submitted INPUT, not
// from the port's opaque result value, so the report cannot depend on the
// adapter's result shape.
func judgmentPartialProgressError(cause error, failedIndex, total int, settledVersions []string) error {
	mapped := mapPortError(cause)
	if len(settledVersions) == 0 {
		return mapped
	}
	return fmt.Errorf("%w (design.judgment_submit is not atomic: %d of %d case judgments were already settled before judgments[%d] failed; "+
		"already-settled case_version_ids=[%s] — resubmit only the remaining entries, a settled case version cannot be judged twice)",
		mapped, len(settledVersions), total, failedIndex, strings.Join(settledVersions, ","))
}

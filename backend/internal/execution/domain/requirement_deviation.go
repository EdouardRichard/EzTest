package execution

import (
	"strings"
	"unicode/utf8"
)

// StepObservedSummary returns the human-readable observation the case executor
// recorded for one executed step. The server stores execution.step_record
// observed as step_result.observed.summary.
func StepObservedSummary(step StepResult) string {
	if len(step.Observed) == 0 {
		return ""
	}
	summary, _ := step.Observed["summary"].(string)
	return strings.TrimSpace(summary)
}

// missingFeatureMarkers are the observation expressions that state the absence
// of a product capability. They are matched inside one clause only, so a marker
// cannot reach across a sentence boundary into an unrelated statement.
var missingFeatureMarkers = []string{
	"无", "没有", "不存在", "缺少", "缺失", "未提供", "未实现", "未找到", "未渲染",
	"missing", "not found", "does not exist", "do not exist", "not implemented", "no such",
}

// missingFeatureNouns are the product control/feature nouns the absence must
// refer to. Generic downstream words (文章/article, 对象/object, 记录/record) are
// deliberately absent: "无预约文章记录" describes a precondition consequence,
// not the missing control itself.
var missingFeatureNouns = []string{
	"控件", "入口", "按钮", "字段", "选项", "功能", "页面", "接口", "端点", "路由",
	"链接", "菜单", "下拉", "输入框", "表单", "计数", "排期", "日历", "提示", "状态",
	"control", "button", "field", "option", "feature", "endpoint", "route", "link",
	"menu", "input", "count", "calendar", "schedule", "status", "page",
}

// missingFeatureWindow bounds how far apart the absence marker and the product
// noun may sit inside one clause (in runes). It keeps "无 like_count/likes 字段"
// (about 20 runes) while rejecting a distant unrelated noun.
const missingFeatureWindow = 24

// Executor capability limitation attribution (v0.2.3 X-02). When the executor
// itself cannot perform an action (native file chooser, hover, download, ...),
// the case executor records the structured marker below on the step observed
// JSON instead of a prose-only observation. The deterministic analysis reads
// the marker before the text heuristics and classifies the run blocked with the
// stable terminal cause executor_capability, so no SUT issue candidate is
// produced. The marker lives in the existing step_result.observed JSONB; there
// is no DB enum or migration.
const (
	// ExecutorLimitationKey is the step_result.observed JSON key carrying the
	// structured executor limitation marker (object, string code or bool).
	ExecutorLimitationKey = "executor_limitation"
	// ExecutorLimitationCodeKey is the optional code inside the marker object.
	ExecutorLimitationCodeKey = "code"

	// TerminalCauseExecutorCapability is the stable case_run.summary.terminal_cause
	// of a run blocked by an executor capability limitation.
	TerminalCauseExecutorCapability = "executor_capability"
	// TerminalCauseExecutorTimeout is the stable cause of an executor request
	// that timed out (not a lost executor).
	TerminalCauseExecutorTimeout = "executor_timeout"
	// TerminalCauseExecutorContextCreateFailed is the stable cause of a failure
	// while creating the lazily-created browser context (the first browser
	// action), which the UAT evidence pointed at.
	TerminalCauseExecutorContextCreateFailed = "executor_context_create_failed"
	// TerminalCauseExecutorUnavailable is the stable cause of a reachable
	// executor that returned a 5xx/protocol failure.
	TerminalCauseExecutorUnavailable = "executor_unavailable"
	// TerminalCauseLeaseExpired is the stable cause of an expired lease on an
	// online executor with no recorded transport failure.
	TerminalCauseLeaseExpired = "lease_expired"
)

// executorLimitationSuffixes are the structured code shapes the executor uses
// for a missing action capability. The transport codes (executor_unavailable /
// executor_timeout) are deliberately excluded: those are not capability limits.
var executorLimitationSuffixes = []string{"_unavailable", "_unsupported", "_not_supported"}

// executorLimitationCodes are the known concrete capability codes. The suffix
// rule keeps future executor codes working without a migration.
var executorLimitationCodes = map[string]struct{}{
	"file_chooser_unavailable": {},
	"hover_unavailable":        {},
	"download_unavailable":     {},
	"upload_unavailable":       {},
	"dialog_unavailable":       {},
	"clipboard_unavailable":    {},
	"drag_unavailable":         {},
	"unsupported_action":       {},
	"action_unsupported":       {},
}

// StepExecutorLimitation reads the structured executor limitation marker of one
// step. ok reports the marker's presence; code is empty when the marker carries
// no concrete code (a legacy bool marker).
func StepExecutorLimitation(step StepResult) (code string, ok bool) {
	if len(step.Observed) == 0 {
		return "", false
	}
	raw, present := step.Observed[ExecutorLimitationKey]
	if !present || raw == nil {
		return "", false
	}
	switch typed := raw.(type) {
	case bool:
		return "", typed
	case string:
		normalized := normalizeLimitationCode(typed)
		if normalized == "" || normalized == "false" {
			return "", false
		}
		return normalized, true
	case map[string]any:
		value, _ := typed[ExecutorLimitationCodeKey].(string)
		return normalizeLimitationCode(value), true
	default:
		return "", true
	}
}

// ExecutorLimitationStep returns the first failed/blocked step carrying the
// structured limitation marker.
func ExecutorLimitationStep(steps []StepResult) (StepResult, string, bool) {
	for _, step := range steps {
		switch step.Status {
		case StepFailed, StepBlocked:
		default:
			continue
		}
		if code, ok := StepExecutorLimitation(step); ok {
			return step, code, true
		}
	}
	return StepResult{}, "", false
}

// ExecutorFailureCause extracts a stable executor failure cause from recorded
// text (case_run.summary.last_error / retry_reason). It returns "" when no
// known executor cause is present.
func ExecutorFailureCause(text string) string {
	lower := strings.ToLower(text)
	if lower == "" {
		return ""
	}
	for _, cause := range []string{
		TerminalCauseExecutorContextCreateFailed,
		TerminalCauseExecutorTimeout,
		TerminalCauseExecutorUnavailable,
		TerminalCauseExecutorCapability,
		string(FailureClassExecutorLost),
		TerminalCauseLeaseExpired,
	} {
		if strings.Contains(lower, cause) {
			return cause
		}
	}
	return ""
}

// ExecutorLimitationCodeFromError scans a tool error text for a structured
// capability code token. It never matches prose keywords: only a lowercase code
// token shaped like the executor's capability codes qualifies. This is the
// producer bridge between the executor action error code and the persisted step
// marker (tools.Error renders as "code: message").
func ExecutorLimitationCodeFromError(text string) (string, bool) {
	lower := strings.ToLower(text)
	for index := 0; index < len(lower); {
		if !isCodeStartByte(lower[index]) {
			index++
			continue
		}
		end := index
		for end < len(lower) && isCodeByte(lower[end]) {
			end++
		}
		if code := lower[index:end]; IsExecutorLimitationCode(code) {
			return code, true
		}
		index = end
	}
	return "", false
}

// IsExecutorLimitationCode reports whether code is a structured executor
// capability limitation code (not a transport/failure-class code).
func IsExecutorLimitationCode(code string) bool {
	normalized := normalizeLimitationCode(code)
	if normalized == "" || !isCodeToken(normalized) {
		return false
	}
	switch normalized {
	case "executor_unavailable", "executor_timeout", "executor_context_create_failed", "executor_lost", "executor_capability":
		// Transport/failure attribution codes are not action capabilities.
		return false
	}
	if _, ok := executorLimitationCodes[normalized]; ok {
		return true
	}
	for _, suffix := range executorLimitationSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func normalizeLimitationCode(code string) string {
	return strings.TrimSpace(strings.ToLower(code))
}

func isCodeToken(value string) bool {
	if value == "" || !isCodeStartByte(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !isCodeByte(value[index]) {
			return false
		}
	}
	return true
}

func isCodeStartByte(value byte) bool { return value >= 'a' && value <= 'z' }

func isCodeByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '_'
}

// FailedStepShowsRequirementDeviation reports whether a failed step carries an
// observed product behavior instead of a tool/automation error.
//
// The case executor records a failed step with an observed summary when the
// product behavior it observed contradicts the step expectation (QA Q1: the SUT
// showed "登录已过期，请重新登录" while the requirement asks for "账号或密码错误，
// 请重试"). Genuine automation failures carry the selector/target/timeout error
// in step_result.error, so they keep their automation/environment class.
//
// A recorded missing-control/missing-feature observation is product evidence
// even when the executor also stored an explanatory error next to it (the
// FR-106-02 trap stored "编辑页无「发布时间/预约发布」入口 ... 期望不可达" in
// step_result.error while the observation recorded the same absent control).
// The tool-error demotion only applies when no such observation exists.
//
// This is the deterministic discriminator that keeps a real requirement
// deviation on the failed(application) path; it never upgrades a step that has
// no observation at all. A step carrying the structured executor limitation
// marker is never product evidence: the executor could not perform the action,
// so it is blocked with terminal_cause=executor_capability (v0.2.3 X-02).
func FailedStepShowsRequirementDeviation(step StepResult) bool {
	if step.Status != StepFailed {
		return false
	}
	if _, limited := StepExecutorLimitation(step); limited {
		return false
	}
	if StepObservesMissingFeature(step) {
		return true
	}
	if strings.TrimSpace(step.Error) != "" {
		return false
	}
	return StepObservedSummary(step) != ""
}

// StepObservesMissingFeature reports whether a failed or blocked step's
// observation states that a required product control or feature is absent.
// A blocked step can carry that observation when the executor could not even
// start the action because the control does not exist; a budget stop, a tool
// error or a missing login/database precondition never does. The structured
// executor limitation marker always wins over the observation text (X-02).
func StepObservesMissingFeature(step StepResult) bool {
	switch step.Status {
	case StepFailed, StepBlocked:
		if _, limited := StepExecutorLimitation(step); limited {
			return false
		}
		return summaryObservesMissingFeature(StepObservedSummary(step))
	default:
		return false
	}
}

// StepShowsApplicationDeviation reports whether the step carries recorded
// application evidence the deterministic fallback must not dismiss as blocked
// or automation: a failed step with an observed requirement deviation (QA Q1)
// or any failed/blocked step observing a required control/feature as absent.
func StepShowsApplicationDeviation(step StepResult) bool {
	return FailedStepShowsRequirementDeviation(step) || StepObservesMissingFeature(step)
}

// summaryObservesMissingFeature scans one observation clause by clause.
func summaryObservesMissingFeature(summary string) bool {
	text := strings.ToLower(strings.TrimSpace(summary))
	if text == "" {
		return false
	}
	for _, clause := range splitObservationClauses(text) {
		if clauseObservesMissingFeature(clause) {
			return true
		}
	}
	return false
}

// splitObservationClauses splits an observation on sentence/clause punctuation
// so an absence marker only applies to its own statement.
func splitObservationClauses(summary string) []string {
	return strings.FieldsFunc(summary, func(r rune) bool {
		switch r {
		case '。', '；', ';', '！', '!', '？', '?', '\n', '，', ',':
			return true
		}
		return false
	})
}

// nonAbsenceWuPrefixes are the common words that start with 无 but do not state
// the absence of a product capability ("无法打开页面" / "无错误提示"). They are
// skipped so a bare 无 cannot match an unrelated control noun in the clause.
var nonAbsenceWuPrefixes = []string{
	"条件", "错误", "异常", "法", "论", "关", "需", "效", "线", "误", "疑",
	"奈", "力", "人", "名", "限", "数", "异", "比", "穷",
}

// clauseObservesMissingFeature reports whether one clause contains an absence
// marker within missingFeatureWindow runes of a product control/feature noun.
func clauseObservesMissingFeature(clause string) bool {
	if strings.TrimSpace(clause) == "" {
		return false
	}
	for _, marker := range missingFeatureMarkers {
		for markerStart := 0; markerStart < len(clause); {
			offset := strings.Index(clause[markerStart:], marker)
			if offset < 0 {
				break
			}
			markerAt := markerStart + offset
			markerEnd := markerAt + len(marker)
			if markerIsAbsence(clause[markerEnd:], marker) &&
				termHasWordBoundary(clause, markerAt, markerEnd) &&
				clauseMarkerHasNoun(clause, marker, markerAt, markerEnd) {
				return true
			}
			markerStart = markerEnd
		}
	}
	return false
}

// markerIsAbsence filters the 无 occurrences that belong to a non-absence word.
func markerIsAbsence(rest, marker string) bool {
	if marker != "无" {
		return true
	}
	for _, prefix := range nonAbsenceWuPrefixes {
		if strings.HasPrefix(rest, prefix) {
			return false
		}
	}
	return true
}

func clauseMarkerHasNoun(clause, marker string, markerAt, markerEnd int) bool {
	for _, noun := range missingFeatureNouns {
		for nounStart := 0; nounStart < len(clause); {
			offset := strings.Index(clause[nounStart:], noun)
			if offset < 0 {
				break
			}
			nounAt := nounStart + offset
			nounEnd := nounAt + len(noun)
			if termHasWordBoundary(clause, nounAt, nounEnd) {
				switch {
				case markerEnd <= nounAt:
					// Marker before noun: "无发布时间控件", "不存在发布日历入口".
					if utf8.RuneCountInString(clause[markerEnd:nounAt]) <= missingFeatureWindow {
						return true
					}
				case nounEnd <= markerAt:
					// Noun before marker: only an adjacent product noun such as
					// "点赞控件不存在" qualifies. A wider gap would let an
					// unrelated subject ("输入框改为不存在的账号") match, and a
					// bare 无 before the noun reads as "页面无错误提示".
					if marker != "无" && nounBeforeMarkerAdjacent(clause[nounEnd:markerAt], noun, marker) {
						return true
					}
				default:
					// Marker and noun overlap (for example "无控件" split across
					// a marker that contains a noun character).
					return true
				}
			}
			nounStart = nounEnd
		}
	}
	return false
}

// nounBeforeMarkerAdjacent reports whether a product noun directly precedes the
// absence marker. Non-ASCII observations allow no filler runes ("点赞控件不存在")
// while ASCII phrases allow the usual space/short adverbial gap
// ("page not found").
func nounBeforeMarkerAdjacent(gap, noun, marker string) bool {
	if gap == "" {
		return true
	}
	if isASCIITerm(noun) && isASCIITerm(marker) {
		return utf8.RuneCountInString(gap) <= 8
	}
	return false
}

// termHasWordBoundary rejects ASCII terms embedded inside a selector, path or
// identifier token (for example the "schedule" inside "selector #scheduled-at"
// or the "missing" inside "missing_visual_baseline"). Chinese terms need no
// boundary check.
func termHasWordBoundary(clause string, at, end int) bool {
	if !isASCIITerm(clause[at:end]) {
		return true
	}
	if at > 0 && isASCIIWordByte(clause[at-1]) {
		return false
	}
	return end >= len(clause) || !isASCIIWordByte(clause[end])
}

func isASCIITerm(term string) bool {
	for index := 0; index < len(term); index++ {
		if term[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func isASCIIWordByte(value byte) bool {
	return value == '_' ||
		(value >= '0' && value <= '9') ||
		(value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z')
}

/**
 * Judge review vocabulary (v0.2.4-E, spec SC-E5). SINGLE frontend source for the
 * review_state / verdict / severity enums.
 *
 * Authority: `backend/internal/design/domain/judgment.go`
 * (`ReviewStatePending/Pass/Reworking/EscalatedFail/JudgeUnavailable`,
 * `VerdictPass/Fail`, `SeverityBlocker/Important/Minor`). These constants mirror
 * that authority so the badge, judgment report grouping and override gating
 * cannot present a state the server does not define.
 *
 * NOTE (constitution 五.1/五.4): the cross-language authority link IS wired —
 * Go `version/domain/enum_authority_test.go` (`TestEnumAuthorityFrontendJudgment`)
 * reads this file and asserts set equality against `design.ReviewStates()` /
 * `Verdicts()` / `Severities()`, so a change to the Go authority that these
 * constants no longer mirror fails CI. The unions below are DERIVED from the
 * const arrays (never a second hand-written union).
 */
import type { BadgeTone } from "@/components/ui/Badge"

export const REVIEW_STATES = [
  "pending",
  "pass",
  "reworking",
  "escalated_fail",
  "judge_unavailable",
] as const

export type ReviewState = (typeof REVIEW_STATES)[number]

export const VERDICTS = ["pass", "fail"] as const

export type Verdict = (typeof VERDICTS)[number]

export const SEVERITIES = ["blocker", "important", "minor"] as const

export type Severity = (typeof SEVERITIES)[number]

const REVIEW_STATE_SET = new Set<string>(REVIEW_STATES)

/** Type guard for a wire review_state; `""`/unknown (degraded) is NOT a state. */
export function isReviewState(value: unknown): value is ReviewState {
  return typeof value === "string" && REVIEW_STATE_SET.has(value)
}

export const REVIEW_STATE_LABEL: Record<ReviewState, string> = {
  pending: "待审查",
  pass: "审查通过",
  reworking: "返工中",
  escalated_fail: "升级失败",
  judge_unavailable: "裁判不可用",
}

/**
 * Distinct tones so a pass is never confused with a second-round failure
 * (owner requirement: 通过的与第二次仍失败的一眼可辨). pass=success(green),
 * escalated_fail=danger(red), judge_unavailable=warning(amber), reworking=info,
 * pending=neutral.
 */
export const REVIEW_STATE_TONE: Record<ReviewState, BadgeTone> = {
  pending: "neutral",
  pass: "success",
  reworking: "info",
  escalated_fail: "danger",
  judge_unavailable: "warning",
}

/** Degraded/empty review_state (T16 unwired gate) reads as unknown, pending-ish. */
export const REVIEW_STATE_UNKNOWN_LABEL = "审查未知"

export function reviewStateLabel(value: string | null | undefined): string {
  return isReviewState(value) ? REVIEW_STATE_LABEL[value] : REVIEW_STATE_UNKNOWN_LABEL
}

export function reviewStateTone(value: string | null | undefined): BadgeTone {
  return isReviewState(value) ? REVIEW_STATE_TONE[value] : "neutral"
}

export const VERDICT_LABEL: Record<Verdict, string> = {
  pass: "通过",
  fail: "不通过",
}

export const SEVERITY_LABEL: Record<Severity, string> = {
  blocker: "阻塞",
  important: "重要",
  minor: "次要",
}

export const SEVERITY_TONE: Record<Severity, BadgeTone> = {
  blocker: "danger",
  important: "warning",
  minor: "neutral",
}

/** Tolerant label/tone lookups for wire values (unknown falls back gracefully). */
export function verdictLabel(value: string): string {
  return value in VERDICT_LABEL ? VERDICT_LABEL[value as Verdict] : value
}

export function severityLabel(value: string): string {
  return value in SEVERITY_LABEL ? SEVERITY_LABEL[value as Severity] : value
}

export function severityTone(value: string): BadgeTone {
  return value in SEVERITY_TONE ? SEVERITY_TONE[value as Severity] : "neutral"
}

/**
 * State gate for the human override (spec SC-E5). Only an escalated fail (rework
 * budget exhausted) or an unavailable judge may be overridden; the admin-role
 * gate is enforced separately by the caller/server. The two eligible values are
 * members of REVIEW_STATES (type-checked), not a second enum copy.
 */
export function canOverrideReviewState(value: string | null | undefined): boolean {
  return value === "escalated_fail" || value === "judge_unavailable"
}

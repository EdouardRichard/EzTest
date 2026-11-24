import { describe, expect, it } from "vitest"

import {
  canOverrideReviewState,
  isReviewState,
  REVIEW_STATE_LABEL,
  REVIEW_STATE_TONE,
  REVIEW_STATES,
  reviewStateLabel,
  reviewStateTone,
  SEVERITIES,
  VERDICTS,
} from "./judgment"

// Frontend pin of the judge-review vocabulary to the Go authority
// backend/internal/design/domain/judgment.go. The reverse (Go -> TS) half is
// version/domain/enum_authority_test.go TestEnumAuthorityFrontendJudgment, which
// reads judgment.ts and asserts set equality against design.ReviewStates() etc.
const REVIEW_STATE_AUTHORITY = [
  "pending",
  "pass",
  "reworking",
  "escalated_fail",
  "judge_unavailable",
]
const VERDICT_AUTHORITY = ["pass", "fail"]
const SEVERITY_AUTHORITY = ["blocker", "important", "minor"]

describe("judgment enum authority", () => {
  it("review_state matches the backend authoritative set exactly", () => {
    expect([...REVIEW_STATES].sort()).toEqual([...REVIEW_STATE_AUTHORITY].sort())
  })

  it("verdict matches the backend authoritative set exactly", () => {
    expect([...VERDICTS].sort()).toEqual([...VERDICT_AUTHORITY].sort())
  })

  it("severity matches the backend authoritative set exactly", () => {
    expect([...SEVERITIES].sort()).toEqual([...SEVERITY_AUTHORITY].sort())
  })

  it("isReviewState accepts every authority member and rejects degraded/unknown", () => {
    for (const state of REVIEW_STATE_AUTHORITY) {
      expect(isReviewState(state)).toBe(true)
    }
    for (const bad of ["", "confirmed", "draft", null, undefined, 42]) {
      expect(isReviewState(bad)).toBe(false)
    }
  })
})

describe("review_state badge rendering", () => {
  it("gives every state a distinct label", () => {
    const labels = REVIEW_STATE_AUTHORITY.map((s) => REVIEW_STATE_LABEL[s as never])
    expect(new Set(labels).size).toBe(REVIEW_STATE_AUTHORITY.length)
  })

  it("separates pass from escalated_fail and judge_unavailable at a glance", () => {
    // Owner requirement: 通过的与第二次仍失败的一眼可辨.
    expect(REVIEW_STATE_TONE.pass).not.toBe(REVIEW_STATE_TONE.escalated_fail)
    expect(REVIEW_STATE_TONE.pass).not.toBe(REVIEW_STATE_TONE.judge_unavailable)
    expect(REVIEW_STATE_TONE.escalated_fail).not.toBe(REVIEW_STATE_TONE.judge_unavailable)
    expect(REVIEW_STATE_TONE.pass).toBe("success")
    expect(REVIEW_STATE_TONE.escalated_fail).toBe("danger")
  })

  it("tolerates the degraded empty review_state as unknown/neutral", () => {
    expect(reviewStateLabel("")).toBe("审查未知")
    expect(reviewStateTone("")).toBe("neutral")
    expect(reviewStateLabel(undefined)).toBe("审查未知")
    expect(reviewStateTone(null)).toBe("neutral")
  })

  it("labels and tones every valid state", () => {
    for (const state of REVIEW_STATE_AUTHORITY) {
      expect(reviewStateLabel(state)).toBe(REVIEW_STATE_LABEL[state as never])
      expect(reviewStateTone(state)).toBe(REVIEW_STATE_TONE[state as never])
    }
  })
})

describe("override state gate", () => {
  it("allows override only for escalated_fail / judge_unavailable", () => {
    expect(canOverrideReviewState("escalated_fail")).toBe(true)
    expect(canOverrideReviewState("judge_unavailable")).toBe(true)
  })

  it("blocks override for pass / pending / reworking / degraded", () => {
    for (const state of ["pass", "pending", "reworking", "", null, undefined]) {
      expect(canOverrideReviewState(state)).toBe(false)
    }
  })
})

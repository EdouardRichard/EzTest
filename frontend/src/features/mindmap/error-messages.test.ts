import { describe, expect, it } from "vitest"

import { ApiError } from "@/lib/api-client"

import { describeMindmapError, MINDMAP_ERROR_FALLBACK } from "./error-messages"
import type { MindmapErrorReason } from "./types"

/** Every stable reason of spec §7.4; the hook/panels must map all of them. */
const REASONS: MindmapErrorReason[] = [
  "version_state_conflict",
  "baseline_frozen",
  "case_revision_conflict",
  "invalid_case_tree",
  "no_cases",
  "unconfirmed_requirements",
  "requirement_not_found",
  "requirement_not_clarified",
  "node_not_found",
  "step_not_found",
  "assertion_not_found",
  "source_not_found",
  "anchor_not_found",
  "run_in_progress",
  "run_not_found",
  "invalid_run_kind",
  "invalid_case_proposal",
  "blocker_findings_open",
  "exploration_requires_environment",
  "environment_not_allowed",
  "code_repo_unavailable",
  "ai_unavailable",
  "export_format_unsupported",
  "export_not_found",
  "export_corrupted",
  "graph_too_large",
  "forbidden",
]

function apiError(details?: Record<string, unknown>): ApiError {
  return new ApiError({
    status: 409,
    code: "conflict",
    message: "request rejected",
    requestId: "req_1",
    retryable: false,
    details,
  })
}

describe("describeMindmapError", () => {
  it("returns a distinct Chinese message for every spec §7.4 reason", () => {
    const messages = new Set<string>()

    for (const reason of REASONS) {
      const message = describeMindmapError(apiError({ reason }))

      expect(message, reason).toMatch(/[\u4e00-\u9fff]/)
      expect(message, reason).not.toBe(MINDMAP_ERROR_FALLBACK)
      expect(messages.has(message), `duplicate message for ${reason}: ${message}`).toBe(false)
      messages.add(message)
    }

    expect(messages.size).toBe(REASONS.length)
  })

  it("falls back for an unknown reason value", () => {
    expect(describeMindmapError(apiError({ reason: "brand_new_reason" }))).toBe(
      MINDMAP_ERROR_FALLBACK,
    )
  })

  it("falls back when the reason is missing or not a string", () => {
    expect(describeMindmapError(apiError())).toBe(MINDMAP_ERROR_FALLBACK)
    expect(describeMindmapError(apiError({ reason: 42 }))).toBe(MINDMAP_ERROR_FALLBACK)
    expect(describeMindmapError(apiError({ reason: null }))).toBe(MINDMAP_ERROR_FALLBACK)
  })

  it("falls back for a non-ApiError failure", () => {
    expect(describeMindmapError(new Error("boom"))).toBe(MINDMAP_ERROR_FALLBACK)
    expect(describeMindmapError("boom")).toBe(MINDMAP_ERROR_FALLBACK)
    expect(describeMindmapError(null)).toBe(MINDMAP_ERROR_FALLBACK)
    expect(describeMindmapError(undefined)).toBe(MINDMAP_ERROR_FALLBACK)
  })
})

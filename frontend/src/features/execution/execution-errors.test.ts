import { describe, expect, it } from "vitest"

import { ApiError } from "@/lib/api-client"

import {
  describeExecutionError,
  EXECUTION_ERROR_FALLBACK,
  executionErrorMessage,
} from "./execution-errors"
import type { ExecutionErrorReason } from "./types"

/** Every stable `details.reason` of spec §6.4; the UI must map all of them. */
const REASONS: ExecutionErrorReason[] = [
  "version_not_executable",
  "baseline_required",
  "baseline_changed",
  "batch_active",
  "no_cases",
  "too_many_cases",
  "invalid_case_ids",
  "environment_required",
  "batch_finished",
  "already_paused",
  "not_paused",
  "case_run_terminal",
  "evidence_missing",
  "executor_unavailable",
  "session_unavailable",
  "unsupported_assertion_kind",
  "export_failed",
  "auth_flow_invalid",
  "takeover_required",
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

describe("executionErrorMessage", () => {
  it("returns a distinct Chinese message for every spec §6.4 reason", () => {
    const messages = new Set<string>()

    for (const reason of REASONS) {
      const message = executionErrorMessage(reason)

      expect(message, reason).toMatch(/[\u4e00-\u9fff]/)
      expect(message, reason).not.toBe(EXECUTION_ERROR_FALLBACK)
      expect(messages.has(message), `duplicate message for ${reason}: ${message}`).toBe(false)
      messages.add(message)
    }

    expect(messages.size).toBe(REASONS.length)
  })

  it("maps batch_active to a readable message", () => {
    expect(executionErrorMessage("batch_active")).toContain("已有执行中的批次")
  })

  it("falls back for an unknown reason value", () => {
    expect(executionErrorMessage("brand_new_reason")).toBe(EXECUTION_ERROR_FALLBACK)
    expect(executionErrorMessage("")).toBe(EXECUTION_ERROR_FALLBACK)
    expect(executionErrorMessage(null)).toBe(EXECUTION_ERROR_FALLBACK)
    expect(executionErrorMessage(undefined)).toBe(EXECUTION_ERROR_FALLBACK)
  })
})

describe("describeExecutionError", () => {
  it("matches the stable details.reason and ignores the raw server message", () => {
    expect(describeExecutionError(apiError({ reason: "baseline_changed" }))).toBe(
      executionErrorMessage("baseline_changed"),
    )
  })

  it("falls back for an unknown reason, a missing reason or a non-ApiError", () => {
    expect(describeExecutionError(apiError({ reason: "brand_new_reason" }))).toBe(
      EXECUTION_ERROR_FALLBACK,
    )
    expect(describeExecutionError(apiError({ reason: 42 }))).toBe(EXECUTION_ERROR_FALLBACK)
    expect(describeExecutionError(apiError())).toBe(EXECUTION_ERROR_FALLBACK)
    expect(describeExecutionError(new Error("boom"))).toBe(EXECUTION_ERROR_FALLBACK)
    expect(describeExecutionError("boom")).toBe(EXECUTION_ERROR_FALLBACK)
    expect(describeExecutionError(null)).toBe(EXECUTION_ERROR_FALLBACK)
  })
})

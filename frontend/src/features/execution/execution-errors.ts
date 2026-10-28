import { ApiError } from "@/lib/api-client"

import type { ExecutionErrorReason } from "./types"

/** Shown when the failure carries no known stable reason (or is not an ApiError). */
export const EXECUTION_ERROR_FALLBACK = "操作失败，请稍后重试。"

/**
 * Every stable `details.reason` of spec §6.4, in spec order. Exported so the
 * UI/tests can assert full coverage instead of drifting from the API.
 */
export const EXECUTION_ERROR_REASONS = [
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
] as const satisfies readonly ExecutionErrorReason[]

/**
 * Chinese copy for every stable reason. The Record type keeps the mapping
 * exhaustive: adding a reason to the union fails typecheck until it gets copy.
 */
const EXECUTION_REASON_MESSAGES: Record<ExecutionErrorReason, string> = {
  version_not_executable: "当前版本状态不可执行，请先进入执行阶段。",
  baseline_required: "当前版本还没有冻结的用例基线，无法执行。",
  baseline_changed: "用例基线已变化，请重新创建批次。",
  batch_active: "已有执行中的批次，请等待其结束或先取消。",
  no_cases: "当前版本没有可执行的用例。",
  too_many_cases: "单批次用例数超过上限，请减少用例后重试。",
  invalid_case_ids: "包含无效的用例 ID，请刷新后重试。",
  environment_required: "当前版本未绑定环境，无法执行。",
  batch_finished: "批次已结束，无法执行该操作。",
  already_paused: "批次已处于暂停状态。",
  not_paused: "批次未暂停，无法恢复。",
  case_run_terminal: "该用例已处于终态，无法重跑。",
  evidence_missing: "证据不存在或已被清理。",
  executor_unavailable: "执行器暂不可用，请稍后重试。",
  session_unavailable: "没有可用的登录会话，请检查账号或访问配置。",
  unsupported_assertion_kind: "不支持该断言类型，无法执行。",
  export_failed: "报告导出失败，请稍后重试。",
  auth_flow_invalid: "登录配方无效，请检查配置。",
  takeover_required: "需要人工接管后才能继续执行。",
}

/**
 * Turns a stable `details.reason` code into user-facing Chinese copy. Unknown
 * codes (and an absent code) fall back to a generic message so raw server text
 * is never surfaced.
 */
export function executionErrorMessage(code: string | null | undefined): string {
  if (typeof code !== "string" || code === "") return EXECUTION_ERROR_FALLBACK
  if (!Object.hasOwn(EXECUTION_REASON_MESSAGES, code)) return EXECUTION_ERROR_FALLBACK
  return EXECUTION_REASON_MESSAGES[code as ExecutionErrorReason]
}

/**
 * Turns an API failure into Chinese copy. Only the stable `details.reason` is
 * matched; non-ApiError failures and missing reasons use the fallback.
 */
export function describeExecutionError(error: unknown): string {
  if (!(error instanceof ApiError)) return EXECUTION_ERROR_FALLBACK
  const reason = error.details.reason
  return executionErrorMessage(typeof reason === "string" ? reason : undefined)
}

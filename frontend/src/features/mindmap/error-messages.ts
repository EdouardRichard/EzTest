import { ApiError } from "@/lib/api-client"

import type { MindmapErrorReason } from "./types"

/** Shown when the failure carries no known stable reason (or is not an ApiError). */
export const MINDMAP_ERROR_FALLBACK = "操作失败，请稍后重试。"

/**
 * Chinese copy for every stable `details.reason` of spec §7.4. The Record type
 * keeps the mapping exhaustive: adding a reason to the union fails typecheck
 * until it gets copy here.
 */
const MINDMAP_REASON_MESSAGES: Record<MindmapErrorReason, string> = {
  version_state_conflict: "当前版本状态不允许此操作，请刷新后重试。",
  baseline_frozen: "用例已绑定基线并冻结，不能再修改。",
  case_revision_conflict: "内容已被他人修改，请刷新后重试。",
  invalid_case_tree: "用例结构不合法，请检查场景层级、步骤与断言。",
  no_cases: "当前版本还没有用例，请先生成或手工新建。",
  unconfirmed_requirements: "存在未澄清的需求，无法生成用例。",
  requirement_not_found: "需求不存在或已被删除。",
  requirement_not_clarified: "需求尚未澄清，不能作为用例来源。",
  node_not_found: "用例节点不存在或已被删除。",
  step_not_found: "用例步骤不存在或已被删除。",
  assertion_not_found: "断言不存在或已被删除。",
  source_not_found: "来源不存在或已被删除。",
  anchor_not_found: "代码锚点不存在或已被删除。",
  run_in_progress: "该版本已有同类运行正在进行，请稍后再试。",
  run_not_found: "运行记录不存在或已被删除。",
  invalid_run_kind: "不支持的运行类型。",
  invalid_case_proposal: "AI 生成的用例内容不合法，已丢弃。",
  blocker_findings_open: "仍存在未处理的 blocker 评审意见。",
  exploration_requires_environment: "应用探索需要先绑定环境。",
  environment_not_allowed: "该环境不在允许访问的域名白名单内。",
  code_repo_unavailable: "代码仓库不可用，无法读取代码信息。",
  ai_unavailable: "AI 服务暂不可用，请稍后重试或手工编辑。",
  export_format_unsupported: "不支持的导出格式。",
  export_not_found: "导出文件不存在或已被删除。",
  export_corrupted: "导出文件校验失败，请重新导出。",
  graph_too_large: "用例图超过 300 个节点，无法继续操作。",
  forbidden: "没有权限执行此操作。",
}

/**
 * Turns an API failure into user-facing Chinese copy. Only the stable
 * `details.reason` is matched; unknown reasons and non-ApiError failures fall
 * back to a generic message so raw server text is never surfaced.
 */
export function describeMindmapError(err: unknown): string {
  if (!(err instanceof ApiError)) return MINDMAP_ERROR_FALLBACK
  const reason = err.details.reason
  if (typeof reason !== "string") return MINDMAP_ERROR_FALLBACK
  if (!Object.prototype.hasOwnProperty.call(MINDMAP_REASON_MESSAGES, reason)) {
    return MINDMAP_ERROR_FALLBACK
  }
  return MINDMAP_REASON_MESSAGES[reason as MindmapErrorReason]
}

import type { StatusBadgeVariant } from "@/components/ui/StatusBadge"

/** Human labels for the delivery vocabularies (spec §5.4, §5.5). */

export const ISSUE_SEVERITY_LABELS: Record<string, string> = {
  blocker: "阻断",
  major: "严重",
  minor: "次要",
  info: "提示",
}

export const ISSUE_STATUS_LABELS: Record<string, string> = {
  open: "待处理",
  confirmed: "已确认",
  dismissed: "已驳回",
}

export const ISSUE_CATEGORY_LABELS: Record<string, string> = {
  defect: "缺陷",
  requirement_gap: "需求缺口",
  other: "其他",
}

export const ISSUE_CONFIDENCE_LABELS: Record<string, string> = {
  high: "高置信",
  medium: "中置信",
  low: "低置信",
}

export const ISSUE_SOURCE_LABELS: Record<string, string> = {
  ai: "AI",
  human: "人工",
}

export const BATCH_KIND_LABELS: Record<string, string> = {
  baseline: "基线",
  incremental: "增量",
  supplement: "补全",
}

export const REPORT_STATUS_LABELS: Record<string, string> = {
  draft: "草稿",
  published: "已发布",
  stale: "已过期",
}

export const BATCH_STATUS_LABELS: Record<string, string> = {
  planned: "待执行",
  running: "执行中",
  done: "已完成",
  failed: "失败",
  cancelled: "已取消",
}

export const FAILURE_CLASS_LABELS: Record<string, string> = {
  application: "应用",
  environment: "环境",
  session: "会话",
  model: "模型",
  case: "用例",
}

export function labelOf(labels: Record<string, string>, value: string): string {
  return labels[value] ?? (value === "" ? "—" : value)
}

export function severityVariant(severity: string): StatusBadgeVariant {
  if (severity === "blocker") return "danger"
  if (severity === "major") return "warning"
  if (severity === "minor") return "info"
  return "neutral"
}

export function issueStatusVariant(status: string): StatusBadgeVariant {
  if (status === "confirmed") return "success"
  if (status === "dismissed") return "neutral"
  return "info"
}

export function reportStatusVariant(status: string): StatusBadgeVariant {
  if (status === "published") return "success"
  if (status === "stale") return "warning"
  return "neutral"
}

export function caseStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    pending: "待执行",
    running: "执行中",
    passed: "通过",
    failed: "失败",
    blocked: "阻塞",
    inconclusive: "不确定",
    skipped: "跳过",
    interrupted: "中断",
  }
  return labelOf(labels, status)
}

import type { BadgeTone } from "@/components/ui/Badge"

import type {
  AssertionKind,
  AssertionVerdict,
  CaseRunStatus,
  EvidenceKind,
  ExecutorStatus,
  FailureClass,
  HumanRequestKind,
  HumanRequestStatus,
  PauseMode,
  StepResultStatus,
  WaveStatus,
  WaveTrigger,
} from "./types"

/** The four terminal verdicts; rerun is only offered for these rows. */
export const CASE_RUN_TERMINAL_STATUSES: CaseRunStatus[] = [
  "passed",
  "failed",
  "blocked",
  "inconclusive",
]

export const CASE_RUN_STATUS_LABEL: Record<CaseRunStatus, string> = {
  pending: "待执行",
  running: "执行中",
  paused: "已暂停",
  passed: "通过",
  failed: "失败",
  blocked: "阻塞",
  inconclusive: "未定",
  skipped: "已跳过",
  interrupted: "已中断",
  superseded: "已取代",
}

/** Status is always encoded as text + color, never color alone (spec §12.2). */
export const CASE_RUN_STATUS_TONE: Record<CaseRunStatus, BadgeTone> = {
  pending: "neutral",
  running: "info",
  paused: "warning",
  passed: "success",
  failed: "danger",
  blocked: "warning",
  inconclusive: "neutral",
  skipped: "neutral",
  interrupted: "danger",
  superseded: "neutral",
}

export type FailureClassKey = Exclude<FailureClass, "">

/** The nine P3 failure classes (spec §4.5); only `application` feeds P4 issues. */
export const FAILURE_CLASS_LABEL: Record<FailureClassKey, string> = {
  application: "应用缺陷",
  environment: "环境异常",
  session: "会话/登录",
  model: "模型异常",
  automation: "自动化异常",
  timeout: "超时",
  executor_lost: "执行器丢失",
  blocked: "无法开始",
  unknown: "未知",
}

/** Renders the empty no-class value as a dash instead of an empty badge. */
export function failureClassLabel(value: FailureClass | string): string {
  if (value === "") return "—"
  if (value in FAILURE_CLASS_LABEL) return FAILURE_CLASS_LABEL[value as FailureClassKey]
  return value
}

export const WAVE_STATUS_LABEL: Record<WaveStatus, string> = {
  planned: "计划中",
  running: "执行中",
  paused: "已暂停",
  done: "已完成",
  failed: "失败",
  cancelled: "已取消",
}

export const WAVE_STATUS_TONE: Record<WaveStatus, BadgeTone> = {
  planned: "neutral",
  running: "info",
  paused: "warning",
  done: "success",
  failed: "danger",
  cancelled: "neutral",
}

export const WAVE_TRIGGER_LABEL: Record<WaveTrigger, string> = {
  auto: "自动",
  manual: "手动",
  retest: "复测",
}

export const PAUSE_MODE_LABEL: Record<PauseMode, string> = {
  graceful: "优雅暂停",
  immediate: "立即暂停",
}

export const HUMAN_REQUEST_KIND_LABEL: Record<HumanRequestKind, string> = {
  login_takeover: "登录接管",
  conflict: "冲突处理",
  other: "其他",
}

export const HUMAN_REQUEST_STATUS_LABEL: Record<HumanRequestStatus, string> = {
  open: "待处理",
  resolved: "已处理",
  cancelled: "已取消",
  expired: "已过期",
}

export const HUMAN_REQUEST_STATUS_TONE: Record<HumanRequestStatus, BadgeTone> = {
  open: "warning",
  resolved: "success",
  cancelled: "neutral",
  expired: "neutral",
}

export const EVIDENCE_KIND_LABEL: Record<EvidenceKind, string> = {
  screenshot: "截图",
  snapshot: "快照",
  console: "控制台",
  network: "网络",
  http: "HTTP",
  db: "数据库",
  download: "下载",
  text: "文本",
}

export const STEP_STATUS_LABEL: Record<StepResultStatus, string> = {
  pending: "待执行",
  running: "执行中",
  passed: "通过",
  failed: "失败",
  blocked: "阻塞",
  skipped: "已跳过",
}

export const ASSERTION_VERDICT_LABEL: Record<AssertionVerdict, string> = {
  passed: "通过",
  failed: "失败",
  inconclusive: "未定",
  skipped: "已跳过",
}

export const ASSERTION_KIND_LABEL: Record<AssertionKind, string> = {
  ui: "UI",
  api: "API",
  db: "数据库",
  text: "文本",
  visual: "视觉",
}

export const EXECUTOR_STATUS_LABEL: Record<ExecutorStatus, string> = {
  active: "在线",
  draining: "排空中",
  offline: "离线",
}

export const EXECUTOR_STATUS_TONE: Record<ExecutorStatus, BadgeTone> = {
  active: "success",
  draining: "warning",
  offline: "danger",
}

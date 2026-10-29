/**
 * The single visual palette of the exploration chain (spec §5.3/§5.4).
 *
 * Every node kind, semantic edge kind, result status, requirement/case status,
 * issue severity and issue status is described here exactly once: lucide icon,
 * Chinese label and Tailwind/CSS colour tokens. Components read these tokens
 * instead of hard-coding colours, and the icon + text label always accompany
 * the colour so meaning never depends on colour alone (spec §5.7).
 *
 * `stroke` is a CSS custom-property expression so SVGs (edges, markers) can use
 * the same semantic colour; the hex fallback lives only in this palette file.
 */

import {
  Activity,
  ArrowRight,
  BadgeCheck,
  Bug,
  CircleCheck,
  CircleDashed,
  CircleDot,
  CircleSlash,
  CircleStop,
  CircleX,
  Clock,
  FileText,
  GitBranch,
  Info,
  Link2,
  ListChecks,
  LoaderCircle,
  type LucideIcon,
  OctagonX,
  ShieldCheck,
  SkipForward,
  Sparkles,
  TriangleAlert,
  Zap,
} from "lucide-react"

import type { StatusBadgeVariant } from "@/components/ui/StatusBadge"
import type { CaseRunStatus } from "../types"
import type { ChainEdgeKindDTO, ChainNodeKindDTO } from "./chain-types"

/** Semantic Tailwind colour names used by the chain palette. */
export type ChainColorName =
  | "blue"
  | "indigo"
  | "violet"
  | "teal"
  | "emerald"
  | "amber"
  | "orange"
  | "red"
  | "sky"
  | "slate"
  | "gray"

/** Tailwind/CSS classes of one semantic colour. */
export type ChainColorStyles = {
  /** CSS colour expression for SVG strokes and markers. */
  stroke: string
  /** Foreground text utility. */
  textClass: string
  /** Soft badge/pill utilities (border + background + text). */
  badgeClass: string
  /** Solid dot/bar utility. */
  dotClass: string
  /** Focus/selection ring utility. */
  ringClass: string
}

/**
 * Chain colours mapped to their utilities. These are the only Tailwind colour
 * names the chain components use; no component carries a raw hex value.
 */
export const CHAIN_COLOR_STYLES: Record<ChainColorName, ChainColorStyles> = {
  blue: {
    stroke: "var(--color-blue-500, #3b82f6)",
    textClass: "text-blue-600 dark:text-blue-400",
    badgeClass: "border-blue-500/25 bg-blue-500/10 text-blue-700 dark:text-blue-300",
    dotClass: "bg-blue-500",
    ringClass: "ring-blue-500/40",
  },
  indigo: {
    stroke: "var(--color-indigo-500, #6366f1)",
    textClass: "text-indigo-600 dark:text-indigo-400",
    badgeClass: "border-indigo-500/25 bg-indigo-500/10 text-indigo-700 dark:text-indigo-300",
    dotClass: "bg-indigo-500",
    ringClass: "ring-indigo-500/40",
  },
  violet: {
    stroke: "var(--color-violet-500, #8b5cf6)",
    textClass: "text-violet-600 dark:text-violet-400",
    badgeClass: "border-violet-500/25 bg-violet-500/10 text-violet-700 dark:text-violet-300",
    dotClass: "bg-violet-500",
    ringClass: "ring-violet-500/40",
  },
  teal: {
    stroke: "var(--color-teal-500, #14b8a6)",
    textClass: "text-teal-600 dark:text-teal-400",
    badgeClass: "border-teal-500/25 bg-teal-500/10 text-teal-700 dark:text-teal-300",
    dotClass: "bg-teal-500",
    ringClass: "ring-teal-500/40",
  },
  emerald: {
    stroke: "var(--color-emerald-500, #10b981)",
    textClass: "text-emerald-600 dark:text-emerald-400",
    badgeClass: "border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
    dotClass: "bg-emerald-500",
    ringClass: "ring-emerald-500/40",
  },
  amber: {
    stroke: "var(--color-amber-500, #f59e0b)",
    textClass: "text-amber-600 dark:text-amber-400",
    badgeClass: "border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-300",
    dotClass: "bg-amber-500",
    ringClass: "ring-amber-500/40",
  },
  orange: {
    stroke: "var(--color-orange-500, #f97316)",
    textClass: "text-orange-600 dark:text-orange-400",
    badgeClass: "border-orange-500/25 bg-orange-500/10 text-orange-700 dark:text-orange-300",
    dotClass: "bg-orange-500",
    ringClass: "ring-orange-500/40",
  },
  red: {
    stroke: "var(--color-red-500, #ef4444)",
    textClass: "text-red-600 dark:text-red-400",
    badgeClass: "border-red-500/25 bg-red-500/10 text-red-700 dark:text-red-300",
    dotClass: "bg-red-500",
    ringClass: "ring-red-500/40",
  },
  sky: {
    stroke: "var(--color-sky-500, #0ea5e9)",
    textClass: "text-sky-600 dark:text-sky-400",
    badgeClass: "border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-300",
    dotClass: "bg-sky-500",
    ringClass: "ring-sky-500/40",
  },
  slate: {
    stroke: "var(--color-slate-500, #64748b)",
    textClass: "text-slate-600 dark:text-slate-400",
    badgeClass: "border-slate-500/25 bg-slate-500/10 text-slate-700 dark:text-slate-300",
    dotClass: "bg-slate-500",
    ringClass: "ring-slate-500/40",
  },
  gray: {
    stroke: "var(--color-gray-400, #9ca3af)",
    textClass: "text-gray-600 dark:text-gray-400",
    badgeClass: "border-gray-400/25 bg-gray-400/10 text-gray-700 dark:text-gray-300",
    dotClass: "bg-gray-400",
    ringClass: "ring-gray-400/40",
  },
}

/** One chain visual token: icon, Chinese label and colour utilities. */
export type ChainToken = {
  icon: LucideIcon
  label: string
  /** Semantic palette name; unique inside each token map. */
  color: ChainColorName
  stroke: string
  textClass: string
  badgeClass: string
  dotClass: string
  ringClass: string
}

/** Status token additionally carries the shared `StatusBadge` variant. */
export type ChainStatusToken = ChainToken & { badgeVariant: StatusBadgeVariant }

function defineToken(icon: LucideIcon, label: string, color: ChainColorName): ChainToken {
  const styles = CHAIN_COLOR_STYLES[color]
  return { icon, label, color, ...styles }
}

function defineStatusToken(
  icon: LucideIcon,
  label: string,
  color: ChainColorName,
  badgeVariant: StatusBadgeVariant,
): ChainStatusToken {
  return { ...defineToken(icon, label, color), badgeVariant }
}

/** Node layer labels/icons/colours (spec §5.4). */
export const NODE_KIND_TOKENS: Record<ChainNodeKindDTO, ChainToken> = {
  requirement: defineToken(FileText, "需求", "blue"),
  case: defineToken(ListChecks, "用例", "indigo"),
  result: defineToken(Activity, "结果", "teal"),
  issue: defineToken(Bug, "问题", "red"),
}

/** Semantic edge labels/icons/colours (spec §5.4). */
export const EDGE_KIND_TOKENS: Record<ChainEdgeKindDTO, ChainToken> = {
  requirement_case: defineToken(GitBranch, "覆盖", "indigo"),
  case_result: defineToken(ArrowRight, "执行", "teal"),
  result_issue: defineToken(TriangleAlert, "发现问题", "red"),
  case_issue: defineToken(Link2, "关联问题", "violet"),
  impact_case: defineToken(Zap, "影响", "amber"),
}

/** Requirement status enum of the mindmap layer. */
export const REQUIREMENT_STATUSES = [
  "draft",
  "clarified",
  "designed",
  "verified",
  "obsolete",
] as const
export type RequirementStatus = (typeof REQUIREMENT_STATUSES)[number]

export const REQUIREMENT_STATUS_TOKENS: Record<RequirementStatus, ChainStatusToken> = {
  draft: defineStatusToken(FileText, "草稿", "gray", "neutral"),
  clarified: defineStatusToken(CircleDot, "已澄清", "sky", "info"),
  designed: defineStatusToken(ListChecks, "已设计", "indigo", "info"),
  verified: defineStatusToken(BadgeCheck, "已验证", "emerald", "success"),
  obsolete: defineStatusToken(CircleSlash, "已废弃", "slate", "neutral"),
}

/** Case status enum of the mindmap layer. */
export const CASE_STATUSES = [
  "draft",
  "reviewed",
  "executed",
  "supplemented",
  "merged",
  "rejected",
  "needs_revalidation",
] as const
export type ChainCaseStatus = (typeof CASE_STATUSES)[number]

export const CASE_STATUS_TOKENS: Record<ChainCaseStatus, ChainStatusToken> = {
  draft: defineStatusToken(FileText, "草稿", "gray", "neutral"),
  reviewed: defineStatusToken(ShieldCheck, "已评审", "sky", "info"),
  executed: defineStatusToken(CircleCheck, "已执行", "emerald", "success"),
  supplemented: defineStatusToken(Sparkles, "已补充", "violet", "info"),
  merged: defineStatusToken(GitBranch, "已合并", "indigo", "info"),
  rejected: defineStatusToken(CircleX, "已驳回", "red", "danger"),
  needs_revalidation: defineStatusToken(TriangleAlert, "需重验", "amber", "warning"),
}

/** Every terminal + in-flight result status of the execution layer. */
export const RESULT_STATUS_TOKENS: Record<CaseRunStatus, ChainStatusToken> = {
  pending: defineStatusToken(Clock, "待执行", "sky", "info"),
  running: defineStatusToken(LoaderCircle, "执行中", "blue", "info"),
  passed: defineStatusToken(CircleCheck, "通过", "emerald", "success"),
  failed: defineStatusToken(CircleX, "失败", "red", "danger"),
  blocked: defineStatusToken(CircleSlash, "阻塞", "slate", "neutral"),
  inconclusive: defineStatusToken(CircleDashed, "不确定", "amber", "warning"),
  skipped: defineStatusToken(SkipForward, "跳过", "gray", "neutral"),
  interrupted: defineStatusToken(CircleStop, "中断", "orange", "danger"),
}

/** Issue severity enum of the delivery layer. */
export const ISSUE_SEVERITIES = ["blocker", "major", "minor", "info"] as const
export type ChainIssueSeverity = (typeof ISSUE_SEVERITIES)[number]

export const ISSUE_SEVERITY_TOKENS: Record<ChainIssueSeverity, ChainStatusToken> = {
  blocker: defineStatusToken(OctagonX, "阻断", "red", "danger"),
  major: defineStatusToken(TriangleAlert, "严重", "orange", "warning"),
  minor: defineStatusToken(Info, "一般", "sky", "info"),
  info: defineStatusToken(CircleDot, "提示", "slate", "neutral"),
}

/** Issue confirmation status enum of the delivery layer. */
export const ISSUE_STATUSES = ["open", "confirmed", "dismissed"] as const
export type ChainIssueStatus = (typeof ISSUE_STATUSES)[number]

export const ISSUE_STATUS_TOKENS: Record<ChainIssueStatus, ChainStatusToken> = {
  open: defineStatusToken(CircleDot, "待确认", "amber", "warning"),
  confirmed: defineStatusToken(BadgeCheck, "已确认", "emerald", "success"),
  dismissed: defineStatusToken(CircleSlash, "已驳回", "slate", "neutral"),
}

/** Chinese labels of the requirement priority enum. */
export const REQUIREMENT_PRIORITY_LABELS: Record<string, string> = {
  must: "必须",
  should: "应该",
  could: "可选",
}

/** Chinese labels of the mindmap case priority enum (P0–P3). */
export const CASE_PRIORITY_LABELS: Record<string, string> = {
  P0: "P0 紧急",
  P1: "P1 高",
  P2: "P2 中",
  P3: "P3 低",
}

/** Chinese labels of the issue category enum. */
export const ISSUE_CATEGORY_LABELS: Record<string, string> = {
  defect: "应用缺陷",
  requirement_gap: "需求缺口",
  other: "其他",
}

/** Chinese labels of the issue confidence enum. */
export const ISSUE_CONFIDENCE_LABELS: Record<string, string> = {
  high: "高",
  medium: "中",
  low: "低",
}

/** Everything the chain canvas needs, in one object. */
export const CHAIN_TOKENS = {
  nodes: NODE_KIND_TOKENS,
  edges: EDGE_KIND_TOKENS,
  requirementStatus: REQUIREMENT_STATUS_TOKENS,
  caseStatus: CASE_STATUS_TOKENS,
  resultStatus: RESULT_STATUS_TOKENS,
  issueSeverity: ISSUE_SEVERITY_TOKENS,
  issueStatus: ISSUE_STATUS_TOKENS,
} as const

/** Neutral fallback used by every unknown-value getter. */
const UNKNOWN_STATUS_TOKEN: ChainStatusToken = defineStatusToken(
  CircleDashed,
  "未知",
  "slate",
  "neutral",
)

function fallbackToken(value: string | undefined | null, emptyLabel: string): ChainStatusToken {
  if (value === undefined || value === null || value.trim() === "") {
    return { ...UNKNOWN_STATUS_TOKEN, label: emptyLabel }
  }
  return { ...UNKNOWN_STATUS_TOKEN, label: value }
}

function labelFor(labels: Record<string, string>, value: string | undefined | null): string {
  if (value === undefined || value === null || value.trim() === "") return "—"
  return labels[value] ?? value
}

export function getNodeKindToken(kind: ChainNodeKindDTO): ChainToken {
  return NODE_KIND_TOKENS[kind]
}

export function getEdgeKindToken(kind: ChainEdgeKindDTO): ChainToken {
  return EDGE_KIND_TOKENS[kind]
}

/** Looks up a result status; unknown/empty values keep a neutral fallback. */
export function getResultStatusToken(status: string | undefined | null): ChainStatusToken {
  if (status === undefined || status === null || status.trim() === "") {
    return { ...UNKNOWN_STATUS_TOKEN }
  }
  return RESULT_STATUS_TOKENS[status as CaseRunStatus] ?? { ...UNKNOWN_STATUS_TOKEN, label: status }
}

export function getRequirementStatusToken(status: string | undefined | null): ChainStatusToken {
  if (status === undefined || status === null || status.trim() === "") {
    return { ...UNKNOWN_STATUS_TOKEN, label: "—" }
  }
  return (
    REQUIREMENT_STATUS_TOKENS[status as RequirementStatus] ?? {
      ...UNKNOWN_STATUS_TOKEN,
      label: status,
    }
  )
}

export function getCaseStatusToken(status: string | undefined | null): ChainStatusToken {
  if (status === undefined || status === null || status.trim() === "") {
    return { ...UNKNOWN_STATUS_TOKEN, label: "—" }
  }
  return CASE_STATUS_TOKENS[status as ChainCaseStatus] ?? { ...UNKNOWN_STATUS_TOKEN, label: status }
}

export function getIssueSeverityToken(severity: string | undefined | null): ChainStatusToken {
  return ISSUE_SEVERITY_TOKENS[severity as ChainIssueSeverity] ?? fallbackToken(severity, "—")
}

export function getIssueStatusToken(status: string | undefined | null): ChainStatusToken {
  return ISSUE_STATUS_TOKENS[status as ChainIssueStatus] ?? fallbackToken(status, "—")
}

export function getRequirementPriorityLabel(priority: string | undefined | null): string {
  return labelFor(REQUIREMENT_PRIORITY_LABELS, priority)
}

export function getCasePriorityLabel(priority: string | undefined | null): string {
  return labelFor(CASE_PRIORITY_LABELS, priority)
}

export function getIssueCategoryLabel(category: string | undefined | null): string {
  return labelFor(ISSUE_CATEGORY_LABELS, category)
}

export function getIssueConfidenceLabel(confidence: string | undefined | null): string {
  return labelFor(ISSUE_CONFIDENCE_LABELS, confidence)
}

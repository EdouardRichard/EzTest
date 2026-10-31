/**
 * Wire types for the P4 delivery issue tab (spec §9.2).
 *
 * Field names and enum values are pinned by the frozen P4 OpenAPI contract:
 * IDs are decimal strings, timestamps are RFC3339 UTC, list endpoints use
 * `{data, next_cursor, has_more}` and detail endpoints use `{data: ...}`.
 */

/* ---------------------------------- enums ---------------------------------- */

/** `IssueView.status` (open → confirmed/dismissed → merged; reopen goes back to open). */
export type IssueStatus = "open" | "confirmed" | "dismissed" | "merged"

export type IssueCategory = "ui" | "api" | "db"

export type IssueSeverity = "major" | "minor" | "trivial"

/** P4 only ingests deterministic failure candidates. */
export type IssueSource = "failure_candidate"

export type IssueRefKind =
  | "case_run"
  | "case_version"
  | "wave"
  | "step_result"
  | "assertion_result"
  | "evidence"
  | "report"
  | "requirement"

export type IssueTriageStatus = "queued" | "running" | "done" | "failed" | "degraded"

export type IssueActorKind = "user" | "agent" | "system"

/* ---------------------------------- views ---------------------------------- */

/** `IssueRefView`: one issue↔entity reference. */
export type IssueRef = {
  kind: IssueRefKind
  ref_id: string
  version_sut_id?: string | null
  role: string
}

/** `IssueTriageView`: latest `issue.triage` analysis-run projection (read-only). */
export type IssueTriage = {
  analysis_run_id?: string | null
  status?: IssueTriageStatus | null
  classification?: string | null
  duplicate_of_issue_id?: string | null
  summary?: string | null
}

/** `IssueView`: list row / action response. */
export type IssueView = {
  id: string
  version_id: string
  title: string
  summary: string
  status: IssueStatus
  category: IssueCategory
  severity: IssueSeverity
  source: IssueSource
  expected_revision: number
  merged_into_id?: string | null
  merged_at?: string | null
  reopen_count: number
  refs: IssueRef[]
  triage?: IssueTriage | null
  created_at: string
  updated_at: string
}

/** `IssueHistoryView`: append-only status history entry. */
export type IssueHistoryView = {
  from_status: IssueStatus | null
  to_status: IssueStatus
  reason: string
  actor_id?: string | null
  actor_kind: IssueActorKind
  created_at: string
}

/** `IssueDetailView`: `IssueView` + history + per-kind related-ref counts. */
export type IssueDetailView = IssueView & {
  history: IssueHistoryView[]
  related_refs: Record<string, number>
}

/* --------------------------------- requests -------------------------------- */

export type IssueListOptions = {
  status?: IssueStatus
  category?: IssueCategory
  severity?: IssueSeverity
  /** `version_sut_id` query parameter (decimal string). */
  versionSutId?: string
  before?: string
  limit?: number
}

/** Confirm / dismiss request; the reason is optional in the frozen contract. */
export type IssueActionInput = {
  reason?: string
  expected_revision?: number
}

/** Reopen request; a non-empty reason is required by the frozen contract. */
export type ReopenIssueInput = {
  reason: string
  expected_revision?: number
}

export type MergeIssueInput = {
  target_issue_id: string
  reason?: string
  expected_revision?: number
}

/* ------------------------------- UI metadata ------------------------------- */

/** Sort priority used by the list: open first, then confirmed/dismissed/merged. */
export const ISSUE_STATUS_ORDER: Record<IssueStatus, number> = {
  open: 0,
  confirmed: 1,
  dismissed: 2,
  merged: 3,
}

export const ISSUE_STATUS_LABELS: Record<IssueStatus, string> = {
  open: "待确认",
  confirmed: "已确认",
  dismissed: "已驳回",
  merged: "已合并",
}

export const ISSUE_CATEGORY_LABELS: Record<IssueCategory, string> = {
  ui: "界面",
  api: "接口",
  db: "数据库",
}

export const ISSUE_SEVERITY_LABELS: Record<IssueSeverity, string> = {
  major: "严重",
  minor: "一般",
  trivial: "轻微",
}

export const ISSUE_REF_LABELS: Record<IssueRefKind, string> = {
  case_run: "用例运行",
  case_version: "用例版本",
  wave: "波次",
  step_result: "步骤结果",
  assertion_result: "断言结果",
  evidence: "证据",
  report: "报告",
  requirement: "需求",
}

export const ISSUE_ACTOR_LABELS: Record<IssueActorKind, string> = {
  user: "用户",
  agent: "代理",
  system: "系统",
}

import { type ApiPage, apiGetPage } from "@/lib/api-client"

import type {
  IssueCategory,
  IssueSeverity,
  IssueStatus,
  IssueView,
  ReportKind,
  ReportStatus,
  ReportView,
} from "./types"

/** `GlobalIssueView`: IssueView + the resolved version title + deep link (spec §4.3.5). */
export type GlobalIssueView = IssueView & {
  version_title: string
  /** `/versions/{vid}?tab=issues&issue={id}` */
  url: string
}

/** `GlobalReportView`: ReportView + version title + deep link; no report content. */
export type GlobalReportView = ReportView & {
  version_title: string
  /** `/versions/{vid}?tab=reports&report={id}` */
  url: string
}

export type GlobalIssueListOptions = {
  versionId?: string
  status?: IssueStatus
  severity?: IssueSeverity
  category?: IssueCategory
  /** Title ILIKE query (1–100 runes on the server). */
  q?: string
  before?: string
  limit?: number
}

export type GlobalReportListOptions = {
  versionId?: string
  status?: ReportStatus
  kind?: ReportKind
  before?: string
  limit?: number
}

/** Builds `?a=1&b=2`, skipping empty values. */
function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === "") continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text.length > 0 ? `?${text}` : ""
}

/**
 * `GET /api/issues` (login-only, read-only): cross-version issues ordered by ID
 * DESC. The server resolves version titles and the workspace deep link.
 */
export function listGlobalIssues(
  options: GlobalIssueListOptions = {},
): Promise<ApiPage<GlobalIssueView>> {
  return apiGetPage<GlobalIssueView>(
    `/api/issues${query({
      version_id: options.versionId,
      status: options.status,
      severity: options.severity,
      category: options.category,
      q: options.q,
      before: options.before,
      limit: options.limit ?? 20,
    })}`,
  )
}

/**
 * `GET /api/reports` (login-only, read-only): cross-version reports ordered by
 * ID DESC with their current revision. No report content or internal path is
 * ever returned by this endpoint.
 */
export function listGlobalReports(
  options: GlobalReportListOptions = {},
): Promise<ApiPage<GlobalReportView>> {
  return apiGetPage<GlobalReportView>(
    `/api/reports${query({
      version_id: options.versionId,
      status: options.status,
      kind: options.kind,
      before: options.before,
      limit: options.limit ?? 20,
    })}`,
  )
}

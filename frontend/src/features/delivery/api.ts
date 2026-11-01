import { type ApiDownload, apiDownload, apiGet, apiGetPage, apiPost } from "@/lib/api-client"

import type {
  IssueActionInput,
  IssueDetailView,
  IssueListOptions,
  IssueView,
  MergeIssueInput,
  RegenerateReportInput,
  RegenerateReportResult,
  ReopenIssueInput,
  ReportDetailView,
  ReportListOptions,
  ReportRevisionView,
  ReportView,
} from "./types"

/** Builds `?a=1&b=2`, skipping empty values (P0/P1 pagination style). */
function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === "") continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text.length > 0 ? `?${text}` : ""
}

function versionPath(versionId: string): string {
  return `/api/versions/${encodeURIComponent(versionId)}`
}

function issuePath(versionId: string, issueId: string): string {
  return `${versionPath(versionId)}/issues/${encodeURIComponent(issueId)}`
}

function reportPath(versionId: string, reportId: string): string {
  return `${versionPath(versionId)}/reports/${encodeURIComponent(reportId)}`
}

/** Builds the optional confirm/dismiss body (spec §4.2). */
function actionBody(input: IssueActionInput): {
  reason?: string
  expected_revision?: number
} {
  const body: { reason?: string; expected_revision?: number } = {}
  if (input.reason !== undefined && input.reason.length > 0) body.reason = input.reason
  if (input.expected_revision !== undefined) body.expected_revision = input.expected_revision
  return body
}

/* ---------------------------------- issues --------------------------------- */

/** `GET /api/versions/{id}/issues` (default `id DESC`, spec §4.2). */
export async function listIssues(
  versionId: string,
  options: IssueListOptions = {},
): Promise<IssueView[]> {
  const page = await apiGetPage<IssueView>(
    `${versionPath(versionId)}/issues${query({
      status: options.status,
      category: options.category,
      severity: options.severity,
      version_sut_id: options.versionSutId,
      before: options.before,
      limit: options.limit ?? 20,
    })}`,
  )
  return page.items
}

/** `GET /api/versions/{id}/issues/{issueId}`: refs/history/triage snapshot. */
export function getIssue(versionId: string, issueId: string): Promise<IssueDetailView> {
  return apiGet<IssueDetailView>(issuePath(versionId, issueId))
}

/** `POST .../confirm`: `open → confirmed` (409 `issue_not_open` otherwise). */
export function confirmIssue(
  versionId: string,
  issueId: string,
  input: IssueActionInput = {},
): Promise<IssueView> {
  return apiPost<IssueView>(`${issuePath(versionId, issueId)}/confirm`, actionBody(input))
}

/** `POST .../dismiss`: `open → dismissed` (409 `issue_not_open` otherwise). */
export function dismissIssue(
  versionId: string,
  issueId: string,
  input: IssueActionInput = {},
): Promise<IssueView> {
  return apiPost<IssueView>(`${issuePath(versionId, issueId)}/dismiss`, actionBody(input))
}

/** `POST .../reopen`: `confirmed/dismissed → open`; the reason is mandatory. */
export function reopenIssue(
  versionId: string,
  issueId: string,
  input: ReopenIssueInput,
): Promise<IssueView> {
  const body: { reason: string; expected_revision?: number } = { reason: input.reason }
  if (input.expected_revision !== undefined) body.expected_revision = input.expected_revision
  return apiPost<IssueView>(`${issuePath(versionId, issueId)}/reopen`, body)
}

/** `POST .../merge`: source becomes `merged`; the server rejects cycles (409). */
export function mergeIssue(
  versionId: string,
  issueId: string,
  input: MergeIssueInput,
): Promise<IssueView> {
  const body: { target_issue_id: string; reason?: string; expected_revision?: number } = {
    target_issue_id: input.target_issue_id,
  }
  if (input.reason !== undefined && input.reason.length > 0) body.reason = input.reason
  if (input.expected_revision !== undefined) body.expected_revision = input.expected_revision
  return apiPost<IssueView>(`${issuePath(versionId, issueId)}/merge`, body)
}

/* ---------------------------------- reports -------------------------------- */

/** `GET /api/versions/{id}/reports` (version summary first, then wave id DESC). */
export async function listReports(
  versionId: string,
  options: ReportListOptions = {},
): Promise<ReportView[]> {
  const page = await apiGetPage<ReportView>(
    `${versionPath(versionId)}/reports${query({
      kind: options.kind,
      before: options.before,
      limit: options.limit ?? 100,
    })}`,
  )
  return page.items
}

/** `GET .../reports/{reportId}`: current revision, all revisions and refs. */
export function getReport(versionId: string, reportId: string): Promise<ReportDetailView> {
  return apiGet<ReportDetailView>(reportPath(versionId, reportId))
}

/** `GET .../reports/{reportId}/revisions/{revision}` (read-only metadata). */
export function getRevision(
  versionId: string,
  reportId: string,
  revision: number,
): Promise<ReportRevisionView> {
  return apiGet<ReportRevisionView>(
    `${reportPath(versionId, reportId)}/revisions/${encodeURIComponent(revision)}`,
  )
}

/** `POST .../regenerate`: ensures the append-only next revision (202). */
export function regenerate(
  versionId: string,
  reportId: string,
  input: RegenerateReportInput = {},
): Promise<RegenerateReportResult> {
  const body: { reason?: string } = {}
  if (input.reason !== undefined && input.reason.length > 0) body.reason = input.reason
  return apiPost<RegenerateReportResult>(`${reportPath(versionId, reportId)}/regenerate`, body)
}

/** `GET .../revisions/{revision}/html`: authenticated report bytes (spec §9.3). */
export function downloadHTML(
  versionId: string,
  reportId: string,
  revision: number,
): Promise<ApiDownload> {
  return apiDownload(
    `${reportPath(versionId, reportId)}/revisions/${encodeURIComponent(revision)}/html`,
  )
}

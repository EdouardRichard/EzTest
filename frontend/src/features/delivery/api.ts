import { type ApiDownload, apiDownload, apiGet, apiGetPage, apiPost } from "@/lib/api-client"

import type {
  CreateManualIssueInput,
  IssueActionInput,
  IssueDetailView,
  IssueListOptions,
  IssueView,
  MergeIssueInput,
  RegenerateReportInput,
  RegenerateReportResult,
  ReopenIssueInput,
  ReportArtifactFormat,
  ReportArtifactView,
  ReportDetailView,
  ReportExportArtifact,
  ReportListOptions,
  ReportRevisionView,
  ReportView,
  RetestIssueResult,
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

/**
 * `POST /api/versions/{id}/issues` (v0.22 W2-4): manual issue creation. The
 * optional `case_run_id` links the source run (and its evidence) to the issue.
 */
export function createManualIssue(
  versionId: string,
  input: CreateManualIssueInput,
): Promise<IssueView> {
  const body: {
    title: string
    summary?: string
    category: string
    severity: string
    case_run_id?: string
    evidence_ids?: string[]
    merge_into_id?: string
    reason?: string
  } = {
    title: input.title,
    category: input.category,
    severity: input.severity,
  }
  if (input.summary !== undefined && input.summary.length > 0) body.summary = input.summary
  if (input.case_run_id !== undefined && input.case_run_id.length > 0) {
    body.case_run_id = input.case_run_id
  }
  if (input.evidence_ids !== undefined && input.evidence_ids.length > 0) {
    body.evidence_ids = input.evidence_ids
  }
  if (input.merge_into_id !== undefined && input.merge_into_id.length > 0) {
    body.merge_into_id = input.merge_into_id
  }
  if (input.reason !== undefined && input.reason.length > 0) body.reason = input.reason
  return apiPost<IssueView>(`${versionPath(versionId)}/issues`, body)
}

/**
 * `POST .../issues/{issueId}/retest` (v0.22 W2-5/API-25). An empty selection
 * retests every case_run linked to the issue. The response `case_run_ids` are
 * the new retest runs, not the request source runs.
 */
export function retestIssue(
  versionId: string,
  issueId: string,
  caseRunIds: string[] = [],
): Promise<RetestIssueResult> {
  const body: { case_run_ids?: string[] } = {}
  if (caseRunIds.length > 0) body.case_run_ids = caseRunIds
  return apiPost<RetestIssueResult>(`${issuePath(versionId, issueId)}/retest`, body)
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

/**
 * `GET .../revisions/{revision}/pdf` (P7): authenticated PDF bytes. The API
 * answers 409 `pdf_not_ready`/`pdf_failed` until the render job is done.
 */
export function downloadPDF(
  versionId: string,
  reportId: string,
  revision: number,
): Promise<ApiDownload> {
  return apiDownload(
    `${reportPath(versionId, reportId)}/revisions/${encodeURIComponent(revision)}/pdf`,
  )
}

/* -------------------------------- artifacts -------------------------------- */

/**
 * `POST .../reports/{reportId}/artifacts` (v0.22 W2-7): renders the current
 * published revision as one `export_artifact` (owner_kind=report). HTML/PDF
 * reuse the verified revision bytes, Markdown is deterministic. PDF answers 409
 * `pdf_not_ready` until the render job is done.
 */
export function createReportArtifact(
  versionId: string,
  reportId: string,
  format: ReportArtifactFormat,
): Promise<ReportArtifactView> {
  return apiPost<ReportArtifactView>(`${reportPath(versionId, reportId)}/artifacts`, { format })
}

/**
 * Lists the version's export artifacts and keeps the report-owned rows
 * (`owner_kind=report`, `owner_id=reportId`). There is no report-scoped list
 * route; the version export list is the existing contract read path (API-28).
 */
export async function listReportArtifacts(
  versionId: string,
  reportId: string,
): Promise<ReportExportArtifact[]> {
  const page = await apiGetPage<ReportExportArtifact>(
    `${versionPath(versionId)}/exports${query({ limit: 100 })}`,
  )
  return page.items.filter(
    (artifact) => artifact.owner_kind === "report" && artifact.owner_id === reportId,
  )
}

/** `GET /api/exports/{exportId}/download`: authenticated artifact bytes. */
export function downloadReportArtifact(exportId: string): Promise<ApiDownload> {
  return apiDownload(`/api/exports/${encodeURIComponent(exportId)}/download`)
}

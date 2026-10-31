import { apiGet, apiGetPage, apiPost } from "@/lib/api-client"

import type {
  IssueActionInput,
  IssueDetailView,
  IssueListOptions,
  IssueView,
  MergeIssueInput,
  ReopenIssueInput,
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

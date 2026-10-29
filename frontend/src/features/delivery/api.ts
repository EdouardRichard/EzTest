import {
  type ApiFetchOptions,
  type ApiPage,
  apiGet,
  apiGetPage,
  apiPatch,
  apiPost,
  apiStreamFetch,
} from "@/lib/api-client"

import type {
  CreateIssueBody,
  GlobalIssue,
  GlobalIssueFilters,
  GlobalReport,
  GlobalReportFilters,
  IssueDetail,
  IssueListFilters,
  IssueSummary,
  PatchIssueBody,
  ReportBatchSummary,
  ReportDetail,
  ReportDiff,
  ReportDownload,
  ReportListFilters,
  ReportSummary,
  VersionOption,
} from "./types"

/** Options forwarded to the shared API client; reads accept an abort signal. */
export type DeliveryRequestOptions = Pick<ApiFetchOptions, "signal" | "idempotencyKey">

const VERSIONS_PATH = "/api/versions"

/** One version's base path; the decimal ID is never re-parsed (D29). */
function versionPath(versionId: string): string {
  return `${VERSIONS_PATH}/${encodeURIComponent(versionId)}`
}

function issuesPath(versionId: string): string {
  return `${versionPath(versionId)}/issues`
}

function issuePath(versionId: string, issueId: string): string {
  return `${issuesPath(versionId)}/${encodeURIComponent(issueId)}`
}

function reportsPath(versionId: string): string {
  return `${versionPath(versionId)}/reports`
}

function reportPath(versionId: string, reportId: string): string {
  return `${reportsPath(versionId)}/${encodeURIComponent(reportId)}`
}

type QueryValue = string | number | undefined

/** Appends only defined, non-empty filters so reads never send placeholders. */
function withQuery(path: string, query: Record<string, QueryValue>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === "") continue
    params.set(key, String(value))
  }
  const suffix = params.toString()
  return suffix === "" ? path : `${path}?${suffix}`
}

/** Keeps request init free of explicit `undefined` fields for stable test calls. */
function requestInit(options: DeliveryRequestOptions): ApiFetchOptions {
  const init: ApiFetchOptions = {}
  if (options.signal !== undefined) init.signal = options.signal
  if (options.idempotencyKey !== undefined) init.idempotencyKey = options.idempotencyKey
  return init
}

/** Reads a RFC 6266 `Content-Disposition` attachment filename. */
function parseAttachmentFilename(header: string | null): string {
  if (!header) return ""

  const extended = /filename\*\s*=\s*([^;]+)/i.exec(header)
  if (extended) {
    const value = extended[1]?.trim() ?? ""
    const encoded = /^[^']*'[^']*'(.*)$/.exec(value)
    if (encoded) {
      try {
        return decodeURIComponent(encoded[1] ?? "")
      } catch {
        return encoded[1] ?? ""
      }
    }
    return value
  }

  const plain = /filename\s*=\s*("(?:[^"\\]|\\.)*"|[^;]+)/i.exec(header)
  if (!plain) return ""
  const value = (plain[1] ?? "").trim()
  if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
    return value.slice(1, -1).replace(/\\(["\\])/g, "$1")
  }
  return value
}

/** GET /api/versions/{id}/issues — one cursor page, severity then id order. */
export function listIssues(
  versionId: string,
  filters: IssueListFilters = {},
  options: DeliveryRequestOptions = {},
): Promise<ApiPage<IssueSummary>> {
  return apiGetPage<IssueSummary>(
    withQuery(issuesPath(versionId), {
      status: filters.status,
      severity: filters.severity,
      category: filters.category,
      batchId: filters.batchId,
      caseRunId: filters.caseRunId,
      caseNodeId: filters.caseNodeId,
      q: filters.q,
      limit: filters.limit,
      cursor: filters.cursor,
    }),
    requestInit(options),
  )
}

/** GET /api/versions/{id}/issues/{issueId} — detail with refs and summaries. */
export function getIssue(
  versionId: string,
  issueId: string,
  options: DeliveryRequestOptions = {},
): Promise<IssueDetail> {
  return apiGet<IssueDetail>(issuePath(versionId, issueId), requestInit(options))
}

/** POST /api/versions/{id}/issues — creates a human-authored open issue. */
export function createIssue(
  versionId: string,
  body: CreateIssueBody,
  options: DeliveryRequestOptions = {},
): Promise<IssueDetail> {
  return apiPost<IssueDetail>(issuesPath(versionId), body, requestInit(options))
}

/** PATCH /api/versions/{id}/issues/{issueId} — confirm/dismiss under revision CAS. */
export function patchIssue(
  versionId: string,
  issueId: string,
  body: PatchIssueBody,
  options: DeliveryRequestOptions = {},
): Promise<IssueDetail> {
  return apiPatch<IssueDetail>(issuePath(versionId, issueId), body, requestInit(options))
}

/** GET /api/issues — the read-only cross-version issue centre. */
export function listGlobalIssues(
  filters: GlobalIssueFilters = {},
  options: DeliveryRequestOptions = {},
): Promise<ApiPage<GlobalIssue>> {
  return apiGetPage<GlobalIssue>(
    withQuery("/api/issues", {
      versionId: filters.versionId,
      status: filters.status,
      severity: filters.severity,
      category: filters.category,
      batchId: filters.batchId,
      caseRunId: filters.caseRunId,
      caseNodeId: filters.caseNodeId,
      q: filters.q,
      limit: filters.limit,
      cursor: filters.cursor,
    }),
    requestInit(options),
  )
}

/** GET /api/versions/{id}/reports — one cursor page, newest first. */
export function listReports(
  versionId: string,
  filters: ReportListFilters = {},
  options: DeliveryRequestOptions = {},
): Promise<ApiPage<ReportSummary>> {
  return apiGetPage<ReportSummary>(
    withQuery(reportsPath(versionId), {
      batchId: filters.batchId,
      status: filters.status,
      limit: filters.limit,
      cursor: filters.cursor,
    }),
    requestInit(options),
  )
}

/** POST /api/versions/{id}/reports — ensures the batch's report exists. */
export function ensureReport(
  versionId: string,
  batchId: string,
  options: DeliveryRequestOptions = {},
): Promise<ReportDetail> {
  return apiPost<ReportDetail>(reportsPath(versionId), { batchId }, requestInit(options))
}

/** GET /api/versions/{id}/reports/{reportId} — detail with the snapshot content. */
export function getReport(
  versionId: string,
  reportId: string,
  options: DeliveryRequestOptions = {},
): Promise<ReportDetail> {
  return apiGet<ReportDetail>(reportPath(versionId, reportId), requestInit(options))
}

/** POST /api/versions/{id}/reports/{reportId}/regenerate — appends a revision. */
export function regenerateReport(
  versionId: string,
  reportId: string,
  options: DeliveryRequestOptions = {},
): Promise<ReportDetail> {
  return apiPost<ReportDetail>(
    `${reportPath(versionId, reportId)}/regenerate`,
    undefined,
    requestInit(options),
  )
}

/** GET /api/versions/{id}/reports/{reportId}/diff[?baseReportId=]. */
export function diffReports(
  versionId: string,
  reportId: string,
  baseReportId?: string,
  options: DeliveryRequestOptions = {},
): Promise<ReportDiff> {
  return apiGet<ReportDiff>(
    withQuery(`${reportPath(versionId, reportId)}/diff`, { baseReportId }),
    requestInit(options),
  )
}

/**
 * GET /api/versions/{id}/reports/{reportId}/export?format=markdown — streams the
 * verified Markdown through `apiStreamFetch`, so bearer/error handling stays in
 * the shared client. The filename comes from `Content-Disposition` and the
 * digest from `X-Content-SHA256`; the filesystem path is never exposed.
 */
export async function exportReport(
  versionId: string,
  reportId: string,
  options: DeliveryRequestOptions = {},
): Promise<ReportDownload> {
  const init: ApiFetchOptions = {
    method: "GET",
    headers: { Accept: "text/markdown" },
  }
  if (options.signal !== undefined) init.signal = options.signal
  const response = await apiStreamFetch(
    `${reportPath(versionId, reportId)}/export?format=markdown`,
    init,
  )
  return {
    blob: await response.blob(),
    filename:
      parseAttachmentFilename(response.headers.get("content-disposition")) ||
      `delivery-report-${reportId}.md`,
    sha256: response.headers.get("x-content-sha256") ?? "",
  }
}

type RawBatch = {
  id: string
  batch_no: number
  status: string
  kind?: string
  counts?: {
    total?: number
    passed?: number
    failed?: number
    blocked?: number
    inconclusive?: number
  }
}

/** GET /api/versions/{id}/batches?limit=100 — options of the report selector. */
export async function listReportBatches(
  versionId: string,
  options: DeliveryRequestOptions = {},
): Promise<ApiPage<ReportBatchSummary>> {
  const page = await apiGetPage<RawBatch>(
    withQuery(`${versionPath(versionId)}/batches`, { limit: 100 }),
    requestInit(options),
  )
  return {
    nextCursor: page.nextCursor,
    hasMore: page.hasMore,
    items: page.items.map((raw) => ({
      id: raw.id,
      batchNo: raw.batch_no,
      status: raw.status,
      kind: raw.kind ?? "baseline",
      counts:
        raw.counts === undefined
          ? undefined
          : {
              total: raw.counts.total ?? 0,
              passed: raw.counts.passed ?? 0,
              failed: raw.counts.failed ?? 0,
              blocked: raw.counts.blocked ?? 0,
              inconclusive: raw.counts.inconclusive,
            },
    })),
  }
}

type RawVersion = {
  id: string
  name?: string
  label?: string
  status?: string
}

/** GET /api/versions?limit=100 — options of the global version filters. */
export async function listVersionOptions(
  options: DeliveryRequestOptions = {},
): Promise<ApiPage<VersionOption>> {
  const page = await apiGetPage<RawVersion>(
    withQuery(VERSIONS_PATH, { limit: 100 }),
    requestInit(options),
  )
  return {
    nextCursor: page.nextCursor,
    hasMore: page.hasMore,
    items: page.items.map((raw) => ({
      id: raw.id,
      name: raw.name ?? `版本 ${raw.id}`,
      label: raw.label ?? "",
      status: raw.status ?? "",
    })),
  }
}

/** GET /api/reports — the read-only cross-version report centre. */
export function listGlobalReports(
  filters: GlobalReportFilters = {},
  options: DeliveryRequestOptions = {},
): Promise<ApiPage<GlobalReport>> {
  return apiGetPage<GlobalReport>(
    withQuery("/api/reports", {
      versionId: filters.versionId,
      status: filters.status,
      limit: filters.limit,
      cursor: filters.cursor,
    }),
    requestInit(options),
  )
}

/** Stable machine-readable reason of an API error (spec §7.11), or "". */
export function deliveryErrorReason(error: unknown): string {
  if (typeof error !== "object" || error === null) return ""
  const details = (error as { details?: unknown }).details
  if (typeof details !== "object" || details === null) return ""
  const reason = (details as { reason?: unknown }).reason
  return typeof reason === "string" ? reason : ""
}

/** Reason→copy map of the delivery-specific API failures (spec §7.11). */
const REASON_MESSAGES: Record<string, string> = {
  issue_state_conflict: "问题已被更新（revision 冲突），请刷新后重试。",
  issue_dismiss_reason_required: "驳回问题必须填写原因。",
  issue_ref_invalid: "问题引用的用例、需求或证据无效，请刷新后重试。",
  report_stale: "报告已过期，请重新生成后再操作。",
  report_not_publishable: "只有草稿报告可以发布。",
  report_export_failed: "报告文件缺失或损坏，请稍后重试导出。",
  forbidden: "无权限执行该操作。",
  not_found: "资源不存在或已被删除。",
  analysis_active: "同类分析正在进行中，请稍后再试。",
  analysis_not_applicable: "当前批次不满足分析条件。",
}

/**
 * Human-readable message for a delivery request failure: maps the stable
 * `details.reason` first, then the HTTP status, and keeps the server message as
 * the last resort. Retryable server failures say so explicitly.
 */
export function describeDeliveryError(error: unknown): string {
  if (typeof error !== "object" || error === null) return "请求失败，请稍后重试。"
  const status =
    typeof (error as { status?: unknown }).status === "number"
      ? (error as { status: number }).status
      : -1
  const message = error instanceof Error ? error.message : "请求失败，请稍后重试。"
  const reason = deliveryErrorReason(error)
  if (reason !== "" && REASON_MESSAGES[reason] !== undefined) return REASON_MESSAGES[reason]

  if (status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (status === 401) return "登录状态已失效，请重新登录。"
  if (status === 403) return "无权限执行该操作。"
  if (status === 404) return "资源不存在或已被删除。"
  if (status >= 500 && (error as { retryable?: unknown }).retryable === true) {
    return `${message}（可稍后重试）`
  }
  return message
}

/** True when the API rejected a write because the issue revision moved (CAS). */
export function isIssueConflict(error: unknown): boolean {
  if (deliveryErrorReason(error) === "issue_state_conflict") return true
  if (typeof error !== "object" || error === null) return false
  return (error as { status?: unknown }).status === 409
}

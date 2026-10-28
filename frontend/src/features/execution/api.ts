import {
  type ApiFetchOptions,
  type ApiPage,
  apiGet,
  apiGetPage,
  apiPost,
  apiStreamFetch,
  resolveApiUrl,
} from "@/lib/api-client"

import type {
  Batch,
  BatchQuery,
  CaseRun,
  CaseRunQuery,
  CreateBatchBody,
  EvidenceMeta,
  ExecutionSession,
  ExecutionSessionQuery,
  PauseMode,
  ReportDownload,
  ReportFormat,
  ReportView,
} from "./types"

/** Version-context prefix of the execution API (spec §6.2). */
const VERSIONS_PATH = "/api/versions"

/** Options forwarded to the shared API client; reads accept an abort signal. */
export type ExecutionRequestOptions = Pick<ApiFetchOptions, "signal" | "idempotencyKey">

/** Options of the streamed report export. */
export type ExportReportOptions = ExecutionRequestOptions & {
  format?: ReportFormat
  /** Filename used when the server omits `Content-Disposition`. */
  fallbackFilename?: string
}

/** One version's base path; the decimal ID is never re-parsed. */
function versionPath(versionId: string): string {
  return `${VERSIONS_PATH}/${encodeURIComponent(versionId)}`
}

/** `{version}/batches` collection path. */
function batchesPath(versionId: string): string {
  return `${versionPath(versionId)}/batches`
}

/** One batch path; both IDs stay decimal strings in the URL. */
function batchPath(versionId: string, batchId: string): string {
  return `${batchesPath(versionId)}/${encodeURIComponent(batchId)}`
}

/** Case-run collection path of one batch. */
function caseRunsPath(versionId: string, batchId: string): string {
  return `${batchPath(versionId, batchId)}/case-runs`
}

/** One case run path. */
function caseRunPath(versionId: string, batchId: string, caseRunId: string): string {
  return `${caseRunsPath(versionId, batchId)}/${encodeURIComponent(caseRunId)}`
}

/** Batch report path. */
function reportPath(versionId: string, batchId: string): string {
  return `${batchPath(versionId, batchId)}/report`
}

/** Evidence byte/meta path. */
function evidencePath(versionId: string, evidenceId: string): string {
  return `${versionPath(versionId)}/evidence/${encodeURIComponent(evidenceId)}`
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
function requestInit(options: ExecutionRequestOptions): ApiFetchOptions {
  const init: ApiFetchOptions = {}
  if (options.signal !== undefined) init.signal = options.signal
  if (options.idempotencyKey !== undefined) init.idempotencyKey = options.idempotencyKey
  return init
}

/**
 * Reads a RFC 6266 `Content-Disposition` attachment filename. The extended
 * `filename*` value wins over the ASCII fallback and is percent-decoded.
 */
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

/** POST /api/versions/{id}/batches — 201 with the created batch. */
export function createBatch(
  versionId: string,
  body: CreateBatchBody = {},
  options: ExecutionRequestOptions = {},
): Promise<Batch> {
  return apiPost<Batch>(batchesPath(versionId), body, requestInit(options))
}

/** GET /api/versions/{id}/batches — one cursor page, newest first. */
export function listBatches(
  versionId: string,
  query: BatchQuery = {},
  options: ExecutionRequestOptions = {},
): Promise<ApiPage<Batch>> {
  return apiGetPage<Batch>(
    withQuery(batchesPath(versionId), {
      status: query.status,
      limit: query.limit,
      cursor: query.cursor,
    }),
    requestInit(options),
  )
}

/** GET /api/versions/{id}/batches/{batchId}. */
export function getBatch(
  versionId: string,
  batchId: string,
  options: ExecutionRequestOptions = {},
): Promise<Batch> {
  return apiGet<Batch>(batchPath(versionId, batchId), requestInit(options))
}

/**
 * POST /api/versions/{id}/batches/{batchId}/pause — graceful by default; the
 * immediate mode interrupts the in-flight step and marks the case run
 * `interrupted` (spec §5.4).
 */
export function pauseBatch(
  versionId: string,
  batchId: string,
  mode: PauseMode = "graceful",
  options: ExecutionRequestOptions = {},
): Promise<Batch> {
  return apiPost<Batch>(`${batchPath(versionId, batchId)}/pause`, { mode }, requestInit(options))
}

/** POST .../resume — only a paused batch can resume; pending runs continue. */
export function resumeBatch(
  versionId: string,
  batchId: string,
  options: ExecutionRequestOptions = {},
): Promise<Batch> {
  return apiPost<Batch>(`${batchPath(versionId, batchId)}/resume`, undefined, requestInit(options))
}

/** POST .../cancel — pending runs become skipped, running ones interrupted. */
export function cancelBatch(
  versionId: string,
  batchId: string,
  options: ExecutionRequestOptions = {},
): Promise<Batch> {
  return apiPost<Batch>(`${batchPath(versionId, batchId)}/cancel`, undefined, requestInit(options))
}

/** GET .../case-runs — one cursor page, optionally filtered by status. */
export function listCaseRuns(
  versionId: string,
  batchId: string,
  query: CaseRunQuery = {},
  options: ExecutionRequestOptions = {},
): Promise<ApiPage<CaseRun>> {
  return apiGetPage<CaseRun>(
    withQuery(caseRunsPath(versionId, batchId), {
      status: query.status,
      limit: query.limit,
      cursor: query.cursor,
    }),
    requestInit(options),
  )
}

/** GET .../case-runs/{caseRunId} — steps, assertions, evidence and takeover. */
export function getCaseRun(
  versionId: string,
  batchId: string,
  caseRunId: string,
  options: ExecutionRequestOptions = {},
): Promise<CaseRun> {
  return apiGet<CaseRun>(caseRunPath(versionId, batchId, caseRunId), requestInit(options))
}

/** POST .../case-runs/{caseRunId}/rerun — starts a new attempt (terminal only). */
export function rerunCase(
  versionId: string,
  batchId: string,
  caseRunId: string,
  options: ExecutionRequestOptions = {},
): Promise<CaseRun> {
  return apiPost<CaseRun>(
    `${caseRunPath(versionId, batchId, caseRunId)}/rerun`,
    undefined,
    requestInit(options),
  )
}

/** GET .../report — the Batch 1 report JSON (spec §6.3). */
export function getReport(
  versionId: string,
  batchId: string,
  options: ExecutionRequestOptions = {},
): Promise<ReportView> {
  return apiGet<ReportView>(reportPath(versionId, batchId), requestInit(options))
}

/**
 * GET .../report/export?format=markdown — streams the server-rendered report
 * through `apiStreamFetch`, keeps the bearer/error behavior of the shared
 * client, and derives the download filename from `Content-Disposition`.
 */
export async function exportReport(
  versionId: string,
  batchId: string,
  options: ExportReportOptions = {},
): Promise<ReportDownload> {
  const format = options.format ?? "markdown"
  const init: ApiFetchOptions = {
    method: "GET",
    headers: { Accept: format === "markdown" ? "text/markdown" : "*/*" },
  }
  if (options.signal !== undefined) init.signal = options.signal

  const response = await apiStreamFetch(
    `${reportPath(versionId, batchId)}/export?format=${encodeURIComponent(format)}`,
    init,
  )
  const filename = parseAttachmentFilename(response.headers.get("content-disposition"))
  const extension = format === "markdown" ? "md" : format
  return {
    blob: await response.blob(),
    filename:
      filename === ""
        ? (options.fallbackFilename ?? `execution-report-${batchId}.${extension}`)
        : filename,
  }
}

/**
 * Absolute URL of one evidence artifact. It is used for links/persistence; the
 * bytes endpoint still requires the bearer, so image rendering should fetch it
 * through the API client and use an object URL.
 */
export function getEvidenceUrl(versionId: string, evidenceId: string): string {
  return resolveApiUrl(evidencePath(versionId, evidenceId))
}

/** GET .../evidence/{evidenceId}/meta — hash/size/metadata, never the path. */
export function getEvidenceMeta(
  versionId: string,
  evidenceId: string,
  options: ExecutionRequestOptions = {},
): Promise<EvidenceMeta> {
  return apiGet<EvidenceMeta>(`${evidencePath(versionId, evidenceId)}/meta`, requestInit(options))
}

/** GET .../execution/sessions — this version's execution Agent sessions. */
export function listExecutionSessions(
  versionId: string,
  query: ExecutionSessionQuery = {},
  options: ExecutionRequestOptions = {},
): Promise<ApiPage<ExecutionSession>> {
  return apiGetPage<ExecutionSession>(
    withQuery(`${versionPath(versionId)}/execution/sessions`, {
      channel: query.channel,
      limit: query.limit,
      cursor: query.cursor,
    }),
    requestInit(options),
  )
}

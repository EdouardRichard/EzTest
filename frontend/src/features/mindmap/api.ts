import {
  type ApiPage,
  apiDelete,
  apiGet,
  apiGetPage,
  apiPatch,
  apiPost,
  apiPut,
  apiStreamFetch,
} from "@/lib/api-client"

import type {
  CaseAssertionView,
  CaseExportView,
  CaseFindingView,
  CaseNodeView,
  CaseReviewView,
  CaseRunView,
  CaseSourceView,
  CaseStepView,
  CodeAnchorView,
  CreateAnchorBody,
  CreateAssertionBody,
  CreateExportBody,
  CreateNodeBody,
  CreateSourceBody,
  CreateStepBody,
  ExportsQuery,
  FindingsQuery,
  MindmapDownload,
  MindmapGraph,
  MindmapInclude,
  MindmapQuery,
  ReorderNodesBody,
  ReorderStepsBody,
  ReviewBody,
  ReviewResult,
  ReviewsQuery,
  RunsQuery,
  StartRunBody,
  StartRunResult,
  SubmitReviewResult,
  UpdateAssertionBody,
  UpdateFindingBody,
  UpdateNodeBody,
  UpdateStepBody,
} from "./types"

/** Version-context prefix. The version id stays a decimal string (spec §7.1). */
const VERSIONS_PATH = "/api/versions"

/** `{versionId}/mindmap` base path; IDs are encoded as single path segments. */
function mindmapPath(versionId: string): string {
  return `${VERSIONS_PATH}/${encodeURIComponent(versionId)}/mindmap`
}

type QueryValue = string | number | null | undefined

/**
 * Appends only the defined, non-empty filters so optional reads never send
 * `cursor=`/`kind=` placeholders. Keys keep the API's camelCase spelling.
 */
function withQuery(path: string, query: Record<string, QueryValue>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "") continue
    params.set(key, String(value))
  }
  const suffix = params.toString()
  return suffix === "" ? path : `${path}?${suffix}`
}

/** Comma-separated `include` value of `GET /mindmap` (spec §7.2 endpoint 1). */
function includeParam(include: MindmapInclude[] | undefined): string | undefined {
  if (!Array.isArray(include) || include.length === 0) return undefined
  return include.join(",")
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

/** GET /mindmap — the whole graph snapshot (spec §7.2 endpoint 1). */
export function getGraph(versionId: string, query: MindmapQuery = {}): Promise<MindmapGraph> {
  return apiGet<MindmapGraph>(
    withQuery(mindmapPath(versionId), { include: includeParam(query.include) }),
  )
}

/** POST /nodes — 201 with the created node (endpoint 2). */
export function createNode(versionId: string, body: CreateNodeBody): Promise<CaseNodeView> {
  return apiPost<CaseNodeView>(`${mindmapPath(versionId)}/nodes`, body)
}

/** PATCH /nodes/{nodeId} — OCC update of one node (endpoint 3). */
export function updateNode(
  versionId: string,
  nodeId: string,
  body: UpdateNodeBody,
): Promise<CaseNodeView> {
  return apiPatch<CaseNodeView>(
    `${mindmapPath(versionId)}/nodes/${encodeURIComponent(nodeId)}`,
    body,
  )
}

/** DELETE /nodes/{nodeId}?revision= — cascade delete of the subtree (endpoint 4). */
export function deleteNode(versionId: string, nodeId: string, revision: number): Promise<void> {
  return apiDelete<void>(
    withQuery(`${mindmapPath(versionId)}/nodes/${encodeURIComponent(nodeId)}`, { revision }),
  )
}

/** PUT /order — reorders the complete child set of one parent (endpoint 5). */
export function reorderNodes(versionId: string, body: ReorderNodesBody): Promise<void> {
  return apiPut<void>(`${mindmapPath(versionId)}/order`, body)
}

/** POST /nodes/{nodeId}/steps — 201 with the created step (endpoint 6). */
export function createStep(
  versionId: string,
  nodeId: string,
  body: CreateStepBody,
): Promise<CaseStepView> {
  return apiPost<CaseStepView>(
    `${mindmapPath(versionId)}/nodes/${encodeURIComponent(nodeId)}/steps`,
    body,
  )
}

/** PATCH /steps/{stepId} — OCC update guarded by the node revision (endpoint 7). */
export function updateStep(
  versionId: string,
  stepId: string,
  body: UpdateStepBody,
): Promise<CaseStepView> {
  return apiPatch<CaseStepView>(
    `${mindmapPath(versionId)}/steps/${encodeURIComponent(stepId)}`,
    body,
  )
}

/** DELETE /steps/{stepId}?nodeRevision= — renumbers the remaining steps (endpoint 8). */
export function deleteStep(versionId: string, stepId: string, nodeRevision: number): Promise<void> {
  return apiDelete<void>(
    withQuery(`${mindmapPath(versionId)}/steps/${encodeURIComponent(stepId)}`, { nodeRevision }),
  )
}

/** PUT /nodes/{nodeId}/steps/order — complete step reorder (endpoint 9). */
export function reorderSteps(
  versionId: string,
  nodeId: string,
  body: ReorderStepsBody,
): Promise<void> {
  return apiPut<void>(
    `${mindmapPath(versionId)}/nodes/${encodeURIComponent(nodeId)}/steps/order`,
    body,
  )
}

/** POST /nodes/{nodeId}/assertions — 201 with the created assertion (endpoint 10). */
export function createAssertion(
  versionId: string,
  nodeId: string,
  body: CreateAssertionBody,
): Promise<CaseAssertionView> {
  return apiPost<CaseAssertionView>(
    `${mindmapPath(versionId)}/nodes/${encodeURIComponent(nodeId)}/assertions`,
    body,
  )
}

/** PATCH /assertions/{assertionId} — OCC update (endpoint 11). */
export function updateAssertion(
  versionId: string,
  assertionId: string,
  body: UpdateAssertionBody,
): Promise<CaseAssertionView> {
  return apiPatch<CaseAssertionView>(
    `${mindmapPath(versionId)}/assertions/${encodeURIComponent(assertionId)}`,
    body,
  )
}

/** DELETE /assertions/{assertionId}?nodeRevision= (endpoint 12). */
export function deleteAssertion(
  versionId: string,
  assertionId: string,
  nodeRevision: number,
): Promise<void> {
  return apiDelete<void>(
    withQuery(`${mindmapPath(versionId)}/assertions/${encodeURIComponent(assertionId)}`, {
      nodeRevision,
    }),
  )
}

/** POST /nodes/{nodeId}/sources — 201 with the created source (endpoint 13). */
export function createSource(
  versionId: string,
  nodeId: string,
  body: CreateSourceBody,
): Promise<CaseSourceView> {
  return apiPost<CaseSourceView>(
    `${mindmapPath(versionId)}/nodes/${encodeURIComponent(nodeId)}/sources`,
    body,
  )
}

/** DELETE /sources/{sourceId} (endpoint 14). */
export function deleteSource(versionId: string, sourceId: string): Promise<void> {
  return apiDelete<void>(`${mindmapPath(versionId)}/sources/${encodeURIComponent(sourceId)}`)
}

/** POST /nodes/{nodeId}/anchors — 201 with the code anchor (endpoint 15). */
export function createAnchor(
  versionId: string,
  nodeId: string,
  body: CreateAnchorBody,
): Promise<CodeAnchorView> {
  return apiPost<CodeAnchorView>(
    `${mindmapPath(versionId)}/nodes/${encodeURIComponent(nodeId)}/anchors`,
    body,
  )
}

/** DELETE /anchors/{anchorId} — admin or creator only (endpoint 16). */
export function deleteAnchor(versionId: string, anchorId: string): Promise<void> {
  return apiDelete<void>(`${mindmapPath(versionId)}/anchors/${encodeURIComponent(anchorId)}`)
}

/** POST /runs — 202 with the accepted run id (endpoint 17). */
export function startRun(versionId: string, body: StartRunBody): Promise<StartRunResult> {
  return apiPost<StartRunResult>(`${mindmapPath(versionId)}/runs`, body)
}

/** GET /runs — one cursor page of runs (endpoint 18). */
export function listRuns(versionId: string, query: RunsQuery = {}): Promise<ApiPage<CaseRunView>> {
  return apiGetPage<CaseRunView>(
    withQuery(`${mindmapPath(versionId)}/runs`, {
      kind: query.kind,
      status: query.status,
      limit: query.limit,
      cursor: query.cursor,
    }),
  )
}

/** GET /runs/{runId} — one run detail without LLM bodies (endpoint 19). */
export function getRun(versionId: string, runId: string): Promise<CaseRunView> {
  return apiGet<CaseRunView>(`${mindmapPath(versionId)}/runs/${encodeURIComponent(runId)}`)
}

/** POST /submit-review — designing → reviewing, returns the version (endpoint 20). */
export function submitReview(versionId: string): Promise<SubmitReviewResult> {
  return apiPost<SubmitReviewResult>(`${mindmapPath(versionId)}/submit-review`, {})
}

/** POST /review — approve (freezes a baseline) or reject (endpoint 21). */
export function decideReview(versionId: string, body: ReviewBody): Promise<ReviewResult> {
  return apiPost<ReviewResult>(`${mindmapPath(versionId)}/review`, body)
}

/** GET /reviews — one cursor page of review rounds (endpoint 22). */
export function listReviews(
  versionId: string,
  query: ReviewsQuery = {},
): Promise<ApiPage<CaseReviewView>> {
  return apiGetPage<CaseReviewView>(
    withQuery(`${mindmapPath(versionId)}/reviews`, {
      limit: query.limit,
      cursor: query.cursor,
    }),
  )
}

/**
 * GET /findings — findings filtered by status/severity/node (endpoint 23).
 *
 * The endpoint returns `{data: FindingView[]}`: one flat, unpaginated array,
 * which `apiGet` unwraps to the array itself. A null `data` means "no rows",
 * but any other non-array payload is a contract violation and throws instead
 * of being silently reported as an empty list (which would hide findings).
 */
export async function listFindings(
  versionId: string,
  query: FindingsQuery = {},
): Promise<CaseFindingView[]> {
  const items = await apiGet<CaseFindingView[] | null>(
    withQuery(`${mindmapPath(versionId)}/findings`, {
      status: query.status,
      severity: query.severity,
      nodeId: query.nodeId,
    }),
  )
  if (items === null || items === undefined) return []
  if (!Array.isArray(items)) {
    throw new Error(
      "mindmap: GET /findings returned an unexpected payload (expected {data: FindingView[]})",
    )
  }
  return items
}

/** PATCH /findings/{findingId} — mark a finding fixed or dismissed (endpoint 24). */
export function updateFinding(
  versionId: string,
  findingId: string,
  body: UpdateFindingBody,
): Promise<CaseFindingView> {
  return apiPatch<CaseFindingView>(
    `${mindmapPath(versionId)}/findings/${encodeURIComponent(findingId)}`,
    body,
  )
}

/** POST /export — 201 (or 200 on reuse) with the export metadata (endpoint 25). */
export function createExport(versionId: string, body: CreateExportBody): Promise<CaseExportView> {
  return apiPost<CaseExportView>(`${mindmapPath(versionId)}/export`, body)
}

/** GET /exports — one cursor page of export history (endpoint 26). */
export function listExports(
  versionId: string,
  query: ExportsQuery = {},
): Promise<ApiPage<CaseExportView>> {
  return apiGetPage<CaseExportView>(
    withQuery(`${mindmapPath(versionId)}/exports`, {
      format: query.format,
      limit: query.limit,
      cursor: query.cursor,
    }),
  )
}

/**
 * GET /exports/{exportId}/download — authenticated binary download (endpoint
 * 27). The filename comes from `Content-Disposition`; the caller-provided name
 * is the fallback when the server omits the header.
 */
export async function downloadExport(
  versionId: string,
  exportId: string,
  fallbackFilename = `mindmap-export-${exportId}`,
): Promise<MindmapDownload> {
  const response = await apiStreamFetch(
    `${mindmapPath(versionId)}/exports/${encodeURIComponent(exportId)}/download`,
    { method: "GET", headers: { Accept: "*/*" } },
  )
  const filename = parseAttachmentFilename(response.headers.get("content-disposition"))
  return { blob: await response.blob(), filename: filename === "" ? fallbackFilename : filename }
}

/** Thin wrapper for all 27 P5 mindmap endpoints (spec §7.2). */
export const mindmapApi = {
  getGraph,
  createNode,
  updateNode,
  deleteNode,
  reorderNodes,
  createStep,
  updateStep,
  deleteStep,
  reorderSteps,
  createAssertion,
  updateAssertion,
  deleteAssertion,
  createSource,
  deleteSource,
  createAnchor,
  deleteAnchor,
  startRun,
  listRuns,
  getRun,
  submitReview,
  decideReview,
  listReviews,
  listFindings,
  updateFinding,
  createExport,
  listExports,
  downloadExport,
} as const

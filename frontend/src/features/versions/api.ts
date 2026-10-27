import { type ApiPage, apiGet, apiGetPage, apiPatch, apiPost, apiPut } from "@/lib/api-client"

import type {
  BindingsBody,
  CreateVersionBody,
  EnterDesignResult,
  UpdateVersionBody,
  Version,
  VersionEvent,
  VersionEventPage,
  VersionQuery,
} from "./types"

/** Version-context base path (spec §7.1). */
const VERSIONS_PATH = "/api/versions"

/** Path for one version. The ID is a decimal string and is never re-parsed. */
function versionPath(id: string): string {
  return `${VERSIONS_PATH}/${id}`
}

function withQuery(path: string, query: URLSearchParams): string {
  const suffix = query.toString()
  return suffix === "" ? path : `${path}?${suffix}`
}

/** GET /api/versions — one cursor page of versions. */
export function listVersions(query: VersionQuery = {}): Promise<ApiPage<Version>> {
  const params = new URLSearchParams()
  if (query.status !== undefined) params.set("status", query.status)
  if (query.limit !== undefined) params.set("limit", String(query.limit))
  if (query.cursor !== undefined && query.cursor !== "") params.set("cursor", query.cursor)
  return apiGetPage<Version>(withQuery(VERSIONS_PATH, params))
}

/** POST /api/versions — 201 with the created draft. */
export function createVersion(body: CreateVersionBody): Promise<Version> {
  return apiPost<Version>(VERSIONS_PATH, body)
}

/** GET /api/versions/{id}. */
export function getVersion(id: string): Promise<Version> {
  return apiGet<Version>(versionPath(id))
}

/** PATCH /api/versions/{id} — name/label/description guarded by `revision`. */
export function updateVersion(id: string, body: UpdateVersionBody): Promise<Version> {
  return apiPatch<Version>(versionPath(id), body)
}

/** PUT /api/versions/{id}/bindings — replaces the whole binding set. */
export function putBindings(id: string, body: BindingsBody): Promise<Version> {
  return apiPut<Version>(`${versionPath(id)}/bindings`, body)
}

/** POST /api/versions/{id}/start-clarification — draft → clarifying. */
export function startClarification(id: string): Promise<Version> {
  return apiPost<Version>(`${versionPath(id)}/start-clarification`)
}

/**
 * POST /api/versions/{id}/enter-design — clarifying → designing. The caller
 * retries with `acknowledge=true` only after the user confirms the warnings
 * returned by the 409 `unclarified_requirements` attempt (spec §7.2).
 */
export async function enterDesign(id: string, acknowledge: boolean): Promise<EnterDesignResult> {
  const result = await apiPost<{ version: Version; warnings?: string[] }>(
    `${versionPath(id)}/enter-design`,
    { acknowledgeUnclarified: acknowledge },
  )
  return {
    version: result.version,
    warnings: Array.isArray(result.warnings) ? result.warnings : [],
  }
}

/**
 * GET /api/versions/{id}/events — the persisted event snapshot plus the
 * channel's current cursor, which the SSE client sends as `Last-Event-ID`.
 */
export function fetchVersionEvents(
  versionId: string,
  params: { limit?: number; beforeSeq?: number } = {},
): Promise<VersionEventPage> {
  const query = new URLSearchParams()
  if (params.beforeSeq !== undefined) query.set("before_seq", String(params.beforeSeq))
  if (params.limit !== undefined) query.set("limit", String(params.limit))
  return apiGetPage<VersionEvent>(withQuery(`${versionPath(versionId)}/events`, query)).then(
    (page) => {
      // snapshot_cursor is a channel sequence number, not a P3 resource ID.
      const cursor = Number(page.nextCursor)
      return {
        events: page.items,
        cursor: Number.isFinite(cursor) ? cursor : 0,
        hasMore: page.hasMore,
      }
    },
  )
}

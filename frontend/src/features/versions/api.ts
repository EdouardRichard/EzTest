import {
  type ApiPage,
  apiDelete,
  apiFetch,
  apiGet,
  apiGetPage,
  apiPatch,
  apiPost,
  apiPut,
  apiStreamFetch,
} from "@/lib/api-client"

import type {
  BindingsBody,
  CreateRelationBody,
  CreateRequirementBody,
  CreateVersionBody,
  EnterDesignResult,
  Requirement,
  RequirementAttachment,
  RequirementQuery,
  RequirementRelation,
  UpdateRelationBody,
  UpdateRequirementBody,
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

/** Requirement collection path for one version (spec §7.3). */
function requirementsPath(versionId: string): string {
  return `${versionPath(versionId)}/requirements`
}

/** Requirement item path; both IDs stay decimal strings in the URL. */
function requirementPath(versionId: string, requirementId: string): string {
  return `${requirementsPath(versionId)}/${requirementId}`
}

/** Attachment collection path for one requirement (spec §7.3). */
function attachmentsPath(versionId: string, requirementId: string): string {
  return `${requirementPath(versionId, requirementId)}/attachments`
}

/** Relation collection path for one version (spec §7.4). */
function relationsPath(versionId: string): string {
  return `${versionPath(versionId)}/relations`
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

// --- Requirements (spec §7.3) ----------------------------------------------

/** GET /api/versions/{id}/requirements — one cursor page, ordered by creation. */
export function listRequirements(
  versionId: string,
  query: RequirementQuery = {},
): Promise<ApiPage<Requirement>> {
  const params = new URLSearchParams()
  if (query.status !== undefined) params.set("status", query.status)
  if (query.limit !== undefined) params.set("limit", String(query.limit))
  if (query.cursor !== undefined && query.cursor !== "") params.set("cursor", query.cursor)
  return apiGetPage<Requirement>(withQuery(requirementsPath(versionId), params))
}

/** POST /api/versions/{id}/requirements — 201 with the created draft. */
export function createRequirement(
  versionId: string,
  body: CreateRequirementBody,
): Promise<Requirement> {
  return apiPost<Requirement>(requirementsPath(versionId), body)
}

/** PATCH /api/versions/{id}/requirements/{rid} — guarded by `revision`. */
export function updateRequirement(
  versionId: string,
  requirementId: string,
  body: UpdateRequirementBody,
): Promise<Requirement> {
  return apiPatch<Requirement>(requirementPath(versionId, requirementId), body)
}

/** DELETE /api/versions/{id}/requirements/{rid} — draft only. */
export function deleteRequirement(versionId: string, requirementId: string): Promise<void> {
  return apiDelete<void>(requirementPath(versionId, requirementId))
}

/** POST .../requirements/{rid}/confirm — draft → clarified (open questions = 0). */
export function confirmRequirement(versionId: string, requirementId: string): Promise<Requirement> {
  return apiPost<Requirement>(`${requirementPath(versionId, requirementId)}/confirm`)
}

/** POST .../requirements/{rid}/reopen — clarified → draft. */
export function reopenRequirement(versionId: string, requirementId: string): Promise<Requirement> {
  return apiPost<Requirement>(`${requirementPath(versionId, requirementId)}/reopen`)
}

// --- Attachments (spec §6.7, §7.3) -----------------------------------------

/**
 * GET .../requirements/{rid}/attachments — attachment metadata only. The
 * backend never exposes `storagePath`.
 */
export async function listAttachments(
  versionId: string,
  requirementId: string,
): Promise<RequirementAttachment[]> {
  const items = await apiGet<RequirementAttachment[]>(attachmentsPath(versionId, requirementId))
  return Array.isArray(items) ? items : []
}

/**
 * POST .../requirements/{rid}/attachments — multipart upload. The body must
 * stay a FormData so the browser sets the multipart boundary; api-client only
 * adds `Content-Type: application/json` for string bodies (spec §9.3).
 */
export function uploadAttachment(
  versionId: string,
  requirementId: string,
  file: File,
): Promise<RequirementAttachment> {
  const body = new FormData()
  body.append("file", file)
  return apiFetch<RequirementAttachment>(attachmentsPath(versionId, requirementId), {
    method: "POST",
    body,
  })
}

/**
 * GET .../attachments/{aid}/content — authenticated download. apiStreamFetch
 * is the raw-response entry point of the shared client (the name reflects its
 * first use, SSE); it applies the same bearer and error mapping as apiFetch.
 */
export async function downloadAttachment(
  versionId: string,
  requirementId: string,
  attachmentId: string,
): Promise<Blob> {
  const response = await apiStreamFetch(
    `${attachmentsPath(versionId, requirementId)}/${attachmentId}/content`,
    { method: "GET" },
  )
  return response.blob()
}

/** DELETE .../attachments/{aid} — metadata delete, draft requirement only. */
export function deleteAttachment(
  versionId: string,
  requirementId: string,
  attachmentId: string,
): Promise<void> {
  return apiDelete<void>(`${attachmentsPath(versionId, requirementId)}/${attachmentId}`)
}

// --- Relations (spec §7.4) -------------------------------------------------

/** GET /api/versions/{id}/relations — every relation (no cursor, max 500). */
export async function listRelations(versionId: string): Promise<RequirementRelation[]> {
  const items = await apiGet<RequirementRelation[]>(relationsPath(versionId))
  return Array.isArray(items) ? items : []
}

/** POST /api/versions/{id}/relations — 201 with the created relation. */
export function createRelation(
  versionId: string,
  body: CreateRelationBody,
): Promise<RequirementRelation> {
  return apiPost<RequirementRelation>(relationsPath(versionId), body)
}

/** PATCH /api/versions/{id}/relations/{relId} — type/description, no revision. */
export function updateRelation(
  versionId: string,
  relationId: string,
  body: UpdateRelationBody,
): Promise<RequirementRelation> {
  return apiPatch<RequirementRelation>(`${relationsPath(versionId)}/${relationId}`, body)
}

/** DELETE /api/versions/{id}/relations/{relId}. */
export function deleteRelation(versionId: string, relationId: string): Promise<void> {
  return apiDelete<void>(`${relationsPath(versionId)}/${relationId}`)
}

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
  ClarificationDecision,
  ClarificationDecisionCounts,
  ClarificationDecisionQuery,
  ClarificationDecisionsPage,
  ClarificationMessage,
  ClarificationMessageQuery,
  ClarificationMessagesPage,
  ClarificationOverview,
  ClarificationQuestion,
  ClarificationQuestionCounts,
  ClarificationQuestionQuery,
  ClarificationQuestionsPage,
  ClarificationRunResult,
  ClarificationSession,
  CreateClarificationQuestionBody,
  CreateDecisionBody,
  CreatedBatch,
  CreateIncrementalBatchBody,
  CreateRelationBody,
  CreateRequirementBody,
  CreateVersionBody,
  EnterDesignResult,
  IncrementalPlan,
  Requirement,
  RequirementAttachment,
  RequirementQuery,
  RequirementRelation,
  SendClarificationMessageBody,
  SendClarificationMessageResult,
  UpdateClarificationQuestionBody,
  UpdateDecisionBody,
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

// --- Clarification (spec §7.5/§7.6) ----------------------------------------

/** Clarification overview path for one version. */
function clarificationPath(versionId: string): string {
  return `${versionPath(versionId)}/clarification`
}

/** Clarification message collection path. */
function clarificationMessagesPath(versionId: string): string {
  return `${clarificationPath(versionId)}/messages`
}

/** Clarification question collection path. */
function clarificationQuestionsPath(versionId: string): string {
  return `${clarificationPath(versionId)}/questions`
}

/** Decision collection path; decisions live beside clarification, not under it. */
function decisionsPath(versionId: string): string {
  return `${versionPath(versionId)}/decisions`
}

/**
 * `GET clarification` — the optional session (absent before the first start)
 * plus the per-status counters. Missing counters are normalized to zero so the
 * UI never renders `undefined` as a count.
 */
export async function getClarification(versionId: string): Promise<ClarificationOverview> {
  type Payload = {
    session?: ClarificationSession | null
    questions?: Partial<ClarificationQuestionCounts>
    decisions?: Partial<ClarificationDecisionCounts>
  }
  const payload = await apiGet<Payload>(clarificationPath(versionId))
  return {
    session: payload.session ?? null,
    questions: {
      open: payload.questions?.open ?? 0,
      answered: payload.questions?.answered ?? 0,
      dismissed: payload.questions?.dismissed ?? 0,
    },
    decisions: {
      proposed: payload.decisions?.proposed ?? 0,
      accepted: payload.decisions?.accepted ?? 0,
      rejected: payload.decisions?.rejected ?? 0,
    },
  }
}

/** POST clarification/sessions — 201 with the started session (409 when active). */
export function startClarificationSession(versionId: string): Promise<ClarificationSession> {
  return apiPost<ClarificationSession>(`${clarificationPath(versionId)}/sessions`)
}

/** POST clarification/sessions/{sid}/complete — active → completed. */
export function completeClarificationSession(versionId: string, sessionId: string): Promise<void> {
  return apiPost<void>(`${clarificationPath(versionId)}/sessions/${sessionId}/complete`)
}

/**
 * GET clarification/messages — the persisted conversation projection, oldest
 * first, with `before_id` (a P1 message ID) for earlier pages.
 */
export async function listClarificationMessages(
  versionId: string,
  query: ClarificationMessageQuery = {},
): Promise<ClarificationMessagesPage> {
  const params = new URLSearchParams()
  if (query.beforeId !== undefined) params.set("before_id", String(query.beforeId))
  if (query.limit !== undefined) params.set("limit", String(query.limit))
  const page = await apiGetPage<ClarificationMessage>(
    withQuery(clarificationMessagesPath(versionId), params),
  )
  // The projection marks reasoning/toolCalls/toolName optional; normalize them
  // so the chat renderer never has to distinguish absent from empty.
  const items = page.items.map((item) => ({
    ...item,
    reasoning: item.reasoning ?? "",
    toolCalls: item.toolCalls ?? null,
    toolName: item.toolName ?? null,
  }))
  return { items, nextCursor: page.nextCursor, hasMore: page.hasMore }
}

/** POST clarification/messages — 202 with the started run (409 busy, 503 AI). */
export function sendClarificationMessage(
  versionId: string,
  message: string,
): Promise<SendClarificationMessageResult> {
  const body: SendClarificationMessageBody = { message }
  return apiPost<SendClarificationMessageResult>(clarificationMessagesPath(versionId), body)
}

/** POST clarification/analyze — one requirement-analysis run (202). */
export function analyzeClarification(versionId: string): Promise<ClarificationRunResult> {
  return apiPost<ClarificationRunResult>(`${clarificationPath(versionId)}/analyze`)
}

/** POST clarification/recon — one context-recon run (202). */
export function reconClarification(versionId: string): Promise<ClarificationRunResult> {
  return apiPost<ClarificationRunResult>(`${clarificationPath(versionId)}/recon`)
}

/** GET clarification/questions — one cursor page of structured questions. */
export async function listClarificationQuestions(
  versionId: string,
  query: ClarificationQuestionQuery = {},
): Promise<ClarificationQuestionsPage> {
  const params = new URLSearchParams()
  if (query.status !== undefined) params.set("status", query.status)
  // Spec §7.6 names this query field `requirementId` (unlike `before_id`).
  if (query.requirementId !== undefined) params.set("requirementId", query.requirementId)
  if (query.limit !== undefined) params.set("limit", String(query.limit))
  if (query.cursor !== undefined && query.cursor !== "") params.set("cursor", query.cursor)
  const page = await apiGetPage<ClarificationQuestion>(
    withQuery(clarificationQuestionsPath(versionId), params),
  )
  const items = page.items.map((item) => ({
    ...item,
    requirementId: item.requirementId ?? null,
    sessionId: item.sessionId ?? null,
  }))
  return { items, nextCursor: page.nextCursor, hasMore: page.hasMore }
}

/** POST clarification/questions — 201 with the created question. */
export function createClarificationQuestion(
  versionId: string,
  body: CreateClarificationQuestionBody,
): Promise<ClarificationQuestion> {
  return apiPost<ClarificationQuestion>(clarificationQuestionsPath(versionId), body)
}

/** PATCH clarification/questions/{qid} — answer/dismiss/reopen/edit. */
export function updateClarificationQuestion(
  versionId: string,
  questionId: string,
  body: UpdateClarificationQuestionBody,
): Promise<ClarificationQuestion> {
  return apiPatch<ClarificationQuestion>(
    `${clarificationQuestionsPath(versionId)}/${questionId}`,
    body,
  )
}

/** GET decisions — one cursor page of proposed/accepted/rejected decisions. */
export async function listDecisions(
  versionId: string,
  query: ClarificationDecisionQuery = {},
): Promise<ClarificationDecisionsPage> {
  const params = new URLSearchParams()
  if (query.status !== undefined) params.set("status", query.status)
  if (query.limit !== undefined) params.set("limit", String(query.limit))
  if (query.cursor !== undefined && query.cursor !== "") params.set("cursor", query.cursor)
  const page = await apiGetPage<ClarificationDecision>(withQuery(decisionsPath(versionId), params))
  const items = page.items.map((item) => ({
    ...item,
    requirementId: item.requirementId ?? null,
    questionId: item.questionId ?? null,
  }))
  return { items, nextCursor: page.nextCursor, hasMore: page.hasMore }
}

/** POST decisions — a manual decision, recorded as accepted/user. */
export function createDecision(
  versionId: string,
  body: CreateDecisionBody,
): Promise<ClarificationDecision> {
  return apiPost<ClarificationDecision>(decisionsPath(versionId), body)
}

/** PATCH decisions/{did} — accept or reject a proposed decision. */
export function updateDecision(
  versionId: string,
  decisionId: string,
  body: UpdateDecisionBody,
): Promise<ClarificationDecision> {
  return apiPatch<ClarificationDecision>(`${decisionsPath(versionId)}/${decisionId}`, body)
}

// --- Continuing incremental plan (spec §7.6/§7.7) --------------------------

/**
 * GET mindmap/incremental-plan — the latest design plan of a continuing
 * version. A version without a finished run returns an empty plan (200), so
 * every collection is normalized to `[]` and the panel never renders
 * `undefined`.
 */
export async function getIncrementalPlan(versionId: string): Promise<IncrementalPlan> {
  type Payload = Partial<IncrementalPlan>
  const payload = await apiGet<Payload>(`${versionPath(versionId)}/mindmap/incremental-plan`)
  return {
    versionId: typeof payload.versionId === "string" ? payload.versionId : versionId,
    phase: typeof payload.phase === "string" ? payload.phase : "",
    parentBatchId: typeof payload.parentBatchId === "string" ? payload.parentBatchId : null,
    run: payload.run ?? null,
    newCases: (payload.newCases ?? []).map((newCase) => ({
      ...newCase,
      requirementIds: Array.isArray(newCase.requirementIds) ? newCase.requirementIds : [],
    })),
    affectedCases: payload.affectedCases ?? [],
    unmappedChanges: payload.unmappedChanges ?? [],
    defaultCaseIds: payload.defaultCaseIds ?? [],
    warnings: payload.warnings ?? [],
    degraded: payload.degraded === true,
  }
}

/**
 * POST batches — creates the incremental batch for the selected plan cases.
 * `kind` is fixed by this helper; the parent batch is required by the
 * incremental gate (spec §5.3).
 */
export function createIncrementalBatch(
  versionId: string,
  body: CreateIncrementalBatchBody,
): Promise<CreatedBatch> {
  return apiPost<CreatedBatch>(`${versionPath(versionId)}/batches`, {
    kind: "incremental",
    caseIds: body.caseIds,
    parentBatchId: body.parentBatchId,
  })
}

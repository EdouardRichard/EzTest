import type { StageView } from "@/features/design/types"
import {
  type ApiDownload,
  type ApiPage,
  apiDelete,
  apiDownload,
  apiGet,
  apiGetPage,
  apiPatch,
  apiPost,
  apiUpload,
} from "@/lib/api-client"

import type {
  BatchConfirmResult,
  ConfirmRequirementResult,
  ContentObject,
  Conversation,
  ConversationMessage,
  ConversationSummary,
  CreateConversationRequest,
  PostMessageRequest,
  Requirement,
  RequirementCreateRequest,
  RequirementDedupeResult,
  RequirementDetail,
  RequirementMergeResult,
  RequirementRelation,
  RequirementStatus,
  RequirementUpdateRequest,
  RollbackRequest,
  TeamRoom,
  TeamRoomKind,
} from "./types"

/** Builds `?a=1&b=2`, skipping empty values (spec §8 pagination). */
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

function conversationPath(versionId: string, conversationId: string): string {
  return `${versionPath(versionId)}/conversations/${encodeURIComponent(conversationId)}`
}

function requirementPath(versionId: string, requirementId: string): string {
  return `${versionPath(versionId)}/requirements/${encodeURIComponent(requirementId)}`
}

/* ------------------------------- conversations ------------------------------ */

/** Lists clarification conversations newest first (summary items). */
export async function listConversations(
  versionId: string,
  options: { limit?: number; before?: string } = {},
): Promise<ConversationSummary[]> {
  const page = await apiGetPage<ConversationSummary>(
    `${versionPath(versionId)}/conversations${query({
      kind: "clarification",
      before: options.before,
      limit: options.limit ?? 100,
    })}`,
  )
  return page.items
}

/** Creates a clarification conversation; the backend ensures the opening turn. */
export function createConversation(
  versionId: string,
  payload: CreateConversationRequest,
): Promise<Conversation> {
  return apiPost<Conversation>(`${versionPath(versionId)}/conversations`, payload)
}

export function getConversation(versionId: string, conversationId: string): Promise<Conversation> {
  return apiGet<Conversation>(conversationPath(versionId, conversationId))
}

/**
 * Loads one page of the conversation thread in ascending id order and keeps the
 * server pagination envelope (`next_cursor`/`has_more`). The history cursor must
 * come from here, never from the last live/optimistic message in the thread.
 */
export function listMessages(
  versionId: string,
  conversationId: string,
  options: { after?: string; limit?: number } = {},
): Promise<ApiPage<ConversationMessage>> {
  return apiGetPage<ConversationMessage>(
    `${conversationPath(versionId, conversationId)}/messages${query({
      after: options.after,
      limit: options.limit ?? 100,
    })}`,
  )
}

/** Appends one user message (text and/or attachments). */
export function postMessage(
  versionId: string,
  conversationId: string,
  payload: PostMessageRequest,
): Promise<ConversationMessage> {
  return apiPost<ConversationMessage>(
    `${conversationPath(versionId, conversationId)}/messages`,
    payload,
  )
}

/* -------------------------------- requirements ------------------------------ */

/** Lists requirements; the sidebar filters client-side, the API filter is optional. */
export async function listRequirements(
  versionId: string,
  options: { status?: RequirementStatus; limit?: number; before?: string } = {},
): Promise<Requirement[]> {
  const page = await apiGetPage<Requirement>(
    `${versionPath(versionId)}/requirements${query({
      status: options.status,
      before: options.before,
      limit: options.limit ?? 100,
    })}`,
  )
  return page.items
}

/** Creates a manual draft requirement. */
export function createRequirement(
  versionId: string,
  payload: RequirementCreateRequest,
): Promise<RequirementDetail> {
  return apiPost<RequirementDetail>(`${versionPath(versionId)}/requirements`, payload)
}

export function getRequirement(
  versionId: string,
  requirementId: string,
): Promise<RequirementDetail> {
  return apiGet<RequirementDetail>(requirementPath(versionId, requirementId))
}

/** Edits a requirement: every edit creates a new immutable draft revision. */
export function updateRequirement(
  versionId: string,
  requirementId: string,
  payload: RequirementUpdateRequest,
): Promise<RequirementDetail> {
  return apiPatch<RequirementDetail>(requirementPath(versionId, requirementId), payload)
}

/** Confirms one requirement; already-confirmed requirements are idempotent. */
export function confirmRequirement(
  versionId: string,
  requirementId: string,
): Promise<ConfirmRequirementResult> {
  return apiPost<ConfirmRequirementResult>(
    `${requirementPath(versionId, requirementId)}/confirmations`,
  )
}

/** Batch confirms requirements; already-confirmed items are skipped. */
export function confirmRequirements(
  versionId: string,
  requirementIds: string[],
): Promise<BatchConfirmResult> {
  return apiPost<BatchConfirmResult>(`${versionPath(versionId)}/requirements/confirmations`, {
    requirement_ids: requirementIds,
  })
}

/**
 * Retires one draft requirement (obsolete, history kept). The server rejects
 * clarified/closed requirements with a typed 409, so confirmed requirements can
 * never be deleted through this path.
 */
export function deleteRequirement(
  versionId: string,
  requirementId: string,
): Promise<RequirementDetail> {
  return apiDelete<RequirementDetail>(requirementPath(versionId, requirementId))
}

/**
 * Collapses duplicate draft requirements of a version. The server keeps the
 * lowest ID per duplicate group and retires the rest; confirmed requirements are
 * never touched.
 */
export function dedupeRequirements(versionId: string): Promise<RequirementDedupeResult> {
  return apiPost<RequirementDedupeResult>(`${versionPath(versionId)}/requirements/dedupe`)
}

/**
 * Folds the selected draft sources into the canonical draft target. The target
 * must still be a draft; clarified/closed targets or sources are a typed 409.
 */
export function mergeRequirements(
  versionId: string,
  targetRequirementId: string,
  sourceRequirementIds: string[],
): Promise<RequirementMergeResult> {
  return apiPost<RequirementMergeResult>(`${versionPath(versionId)}/requirements/merge`, {
    target_requirement_id: targetRequirementId,
    source_requirement_ids: sourceRequirementIds,
  })
}

/**
 * Applies the 0.2.1 requirement hard gate for the selected clarified
 * requirements (`POST /stage/confirm-requirements`, spec §5.5): the version
 * advances requirement_review -> designing and a design.cases entry job is
 * enqueued. This is the "开始设计" action, not a plain confirmation.
 */
export function confirmStageRequirements(
  versionId: string,
  requirementIds: string[],
): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/confirm-requirements`, {
    requirement_ids: requirementIds,
  })
}

/** Rolls a clarified requirement back to a new draft revision. */
export function rollbackRequirement(
  versionId: string,
  requirementId: string,
  payload: RollbackRequest = {},
): Promise<RequirementDetail> {
  return apiPost<RequirementDetail>(
    `${requirementPath(versionId, requirementId)}/rollback`,
    payload,
  )
}

export async function listRequirementRelations(
  versionId: string,
  requirementId: string,
): Promise<RequirementRelation[]> {
  const page = await apiGetPage<RequirementRelation>(
    `${requirementPath(versionId, requirementId)}/relations`,
  )
  return page.items
}

/* --------------------------------- team room -------------------------------- */

/** Reads the read-only team room digest and latest posts (spec §8.3). */
export function getTeamRoom(
  versionId: string,
  team: TeamRoomKind,
  options: { limit?: number } = {},
): Promise<TeamRoom> {
  return apiGet<TeamRoom>(
    `${versionPath(versionId)}/team-rooms/${encodeURIComponent(team)}${query({
      limit: options.limit ?? 20,
    })}`,
  )
}

/* ---------------------------------- content --------------------------------- */

/** Uploads one attachment object through `POST /api/content` (multipart, P0). */
export function uploadContent(file: File): Promise<ContentObject> {
  const form = new FormData()
  form.append("file", file)
  return apiUpload<ContentObject>("/api/content", form)
}

/**
 * Downloads an uploaded image through the authenticated content endpoint.
 * `GET /api/content/{sha256}` requires the bearer token, so the UI loads image
 * parts as an object URL instead of a bare `<img src>`.
 */
export function downloadContent(sha256: string): Promise<ApiDownload> {
  return apiDownload(`/api/content/${encodeURIComponent(sha256)}`)
}

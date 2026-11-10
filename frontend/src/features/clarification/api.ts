import type { StageView } from "@/features/design/types"
import {
  type ApiDownload,
  ApiError,
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
  ReopenRequirementRequest,
  Requirement,
  RequirementCreateRequest,
  RequirementDedupeResult,
  RequirementDetail,
  RequirementExtractionProgress,
  RequirementExtractionStartResult,
  RequirementMergeResult,
  RequirementRelation,
  RequirementStatus,
  RequirementUpdateRequest,
  RollbackRequest,
  TeamRoom,
  TeamRoomKind,
  VersionAttachment,
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
 * server pagination envelope (`next_cursor`/`has_more`).
 *
 * API-8: with no cursor the endpoint returns the **latest** page; pass the
 * oldest loaded id as `before` to walk backwards through history (the design's
 * "首屏最新页" contract). `after` is kept for the legacy oldest-first P1
 * envelope so already-loaded threads can still extend forwards.
 */
export function listMessages(
  versionId: string,
  conversationId: string,
  options: { before?: string; after?: string; limit?: number } = {},
): Promise<ApiPage<ConversationMessage>> {
  return apiGetPage<ConversationMessage>(
    `${conversationPath(versionId, conversationId)}/messages${query({
      before: options.before,
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

/* ----------------------------- structured questions ------------------------ */

/** `human_request.status` of a structured question (v0.22 R-B3-1). */
export type QuestionStatus = "pending" | "answered" | "expired" | "cancelled"

export type QuestionOption = {
  id: string
  label: string
}

/** One question inside a batch (`HumanQuestion.question` payload, R-B3-2). */
export type QuestionItem = {
  id: string
  text: string
  options: QuestionOption[]
  multiple: boolean
  allow_free_text: boolean
  required: boolean
  source_refs: string[]
}

/** One stored answer (`human_request.response.answers[]`). */
export type QuestionAnswer = {
  question_id: string
  selected: string[]
  text: string
}

/**
 * v0.22 version-level decision kinds that share the `human_request` table with
 * structured questions (R-W2-1). Re-exported from `question-kinds` so the pure
 * kind guards survive tests that mock this transport module.
 */
export {
  HUMAN_DECISION_KINDS,
  type HumanDecisionKind,
  isClarificationQuestionBatch,
  isHumanDecisionKind,
} from "./question-kinds"

/**
 * One structured-question batch (`GET /api/versions/{id}/questions`
 * QuestionView). The payload comes from `human_request`, lazily expired by the
 * backend, so a pending card survives a reload without local-only state.
 *
 * W2-2 extends the list with approval/blocked_recovery/conflict decision rows:
 * they carry an empty `questions` array and their recorded decision/reason
 * instead. `kind` separates the two shapes.
 */
export type QuestionBatch = {
  id: string
  version_id?: string
  conversation_id?: string | null
  message_id?: string | null
  kind?: string
  status: QuestionStatus
  batch_id: string
  questions: QuestionItem[]
  expires_at: string
  answers: QuestionAnswer[]
  answered_by?: string | null
  answered_at?: string | null
  skipped?: boolean
  /** Recorded decision of a v0.22 decision row (empty for questions). */
  decision?: string
  /** Recorded decision reason of a v0.22 decision row (empty for questions). */
  reason?: string
  created_at?: string
}

/** One answer typed in the card and sent with `onAnswer` for the echo. */
export type QuestionAnswerInput = {
  questionId: string
  selected: string[]
  text: string
}

/** A question card embedded in the conversation stream. */
export type QuestionPart = {
  type: "question"
  question: QuestionBatch
}

/**
 * The part payload the backend writes with `conversation_message(kind=question)`
 * (`questionMessagePayload`): `{question_id, batch_id, questions, expires_at}`.
 * Task 36/37 expose it through the conversation DTO; Task 24 renders it
 * defensively from either this wire shape or an embedded `question` object.
 */
export type QuestionPartPayload = {
  type: "question"
  question?: QuestionBatch
  question_id?: string
  batch_id?: string
  questions?: QuestionItem[]
  expires_at?: string
  status?: QuestionStatus
  answers?: QuestionAnswer[]
}

/** Lists the version's structured questions (optionally one status). */
export async function listQuestions(
  versionId: string,
  options: { status?: QuestionStatus; limit?: number } = {},
): Promise<QuestionBatch[]> {
  const page = await apiGetPage<QuestionBatch>(
    `${versionPath(versionId)}/questions${query({
      status: options.status,
      limit: options.limit ?? 100,
    })}`,
  )
  return page.items
}

/** Answers every question of one batch (`POST .../questions/{qid}/answer`). */
export function answerQuestion(
  versionId: string,
  questionId: string,
  answers: QuestionAnswer[],
): Promise<QuestionBatch> {
  return apiPost<QuestionBatch>(
    `${versionPath(versionId)}/questions/${encodeURIComponent(questionId)}/answer`,
    { answers },
  )
}

/** Skips a pending batch (`POST .../questions/{qid}/skip` -> cancelled). */
export function skipQuestion(
  versionId: string,
  questionId: string,
  reason?: string,
): Promise<QuestionBatch> {
  return apiPost<QuestionBatch>(
    `${versionPath(versionId)}/questions/${encodeURIComponent(questionId)}/skip`,
    reason === undefined || reason === "" ? {} : { reason },
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

/**
 * Reopens a closed requirement (closed -> draft) with a mandatory reason. This
 * is the explicit recovery path behind a closed-requirement retry 409; it does
 * not confirm or move the version stage.
 */
export function reopenRequirement(
  versionId: string,
  requirementId: string,
  payload: ReopenRequirementRequest,
): Promise<RequirementDetail> {
  return apiPost<RequirementDetail>(`${requirementPath(versionId, requirementId)}/reopen`, payload)
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

/* --------------------------- requirement extraction ------------------------ */

function extractionPath(versionId: string, runId?: string): string {
  const base = `${versionPath(versionId)}/requirements/extractions`
  return runId === undefined ? base : `${base}/${encodeURIComponent(runId)}`
}

/**
 * Reads the latest requirement extraction run of the version (API-5). The
 * backend answers a typed 404 when the version has no run yet; the UI renders
 * its empty state for that case instead of treating it as a load failure.
 */
export async function getRequirementExtraction(
  versionId: string,
  options: { attachmentId?: string } = {},
): Promise<RequirementExtractionProgress | null> {
  try {
    return await apiGet<RequirementExtractionProgress>(
      `${extractionPath(versionId)}${query({ attachment_id: options.attachmentId })}`,
    )
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 404) return null
    throw cause
  }
}

/**
 * Triggers/replays requirement extraction (API-6). Without `attachmentId` the
 * backend starts every ready attachment of the version; replay is idempotent by
 * (attachment_id, input_sha256), so "重新抽取" never duplicates a run.
 */
export function startRequirementExtraction(
  versionId: string,
  attachmentId?: string,
): Promise<RequirementExtractionStartResult> {
  const body =
    attachmentId === undefined || attachmentId === "" ? {} : { attachment_id: attachmentId }
  return apiPost<RequirementExtractionStartResult>(extractionPath(versionId), body)
}

/** Pauses a pending/running extraction run (API-7; typed 409 on wrong state). */
export function pauseRequirementExtraction(
  versionId: string,
  runId: string,
): Promise<RequirementExtractionProgress> {
  return apiPost<RequirementExtractionProgress>(`${extractionPath(versionId, runId)}/pause`)
}

/** Resumes a paused extraction run (API-7). */
export function resumeRequirementExtraction(
  versionId: string,
  runId: string,
): Promise<RequirementExtractionProgress> {
  return apiPost<RequirementExtractionProgress>(`${extractionPath(versionId, runId)}/resume`)
}

/**
 * Retries a failed extraction run (API-7). With `segmentSeq` only that failed
 * segment is reset; without it every failed segment gets a fresh attempt budget.
 */
export function retryRequirementExtraction(
  versionId: string,
  runId: string,
  segmentSeq?: number,
): Promise<RequirementExtractionProgress> {
  return apiPost<RequirementExtractionProgress>(
    `${extractionPath(versionId, runId)}/retry`,
    segmentSeq === undefined ? undefined : { segment_seq: segmentSeq },
  )
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

/* ------------------------- version attachments (C-05) ----------------------- */

/**
 * Lists the version's attachments for the overview panel (C-05).
 *
 * The route is version-scoped, so the panel never guesses a global attachment
 * ID: every row is already owned by the addressed version.
 */
export async function listVersionAttachments(versionId: string): Promise<VersionAttachment[]> {
  const page = await apiGetPage<VersionAttachment>(`${versionPath(versionId)}/attachments`)
  return page.items
}

/* ------------------ attachment_pending progress details (C-04) --------------- */

/** Stage of a still-parsing attachment reported by the typed 409 (Task 10). */
export type AttachmentPendingStage = "queued" | "parsing" | "analyzing_images" | "done" | "failed"

/** Typed progress of the `409 attachment_pending` rejection. */
export type AttachmentPendingProgress = {
  attachmentId: string
  name: string
  /** null when an older server only sends `{attachment_id,name}`. */
  stage: AttachmentPendingStage | null
  imagesTotal: number
  imagesDone: number
  imagesFailed: number
  imagesSkipped: number
  /** 0..100 while `analyzing_images`; null before the image counter is known. */
  percent: number | null
  /** Estimated seconds to finish the image pass; null before the first image. */
  etaSeconds: number | null
  /** Delay the client must wait before the next automatic retry. */
  retryAfterSeconds: number
  error: string
}

const DEFAULT_RETRY_AFTER_SECONDS = 3
const PENDING_STAGES: readonly AttachmentPendingStage[] = [
  "queued",
  "parsing",
  "analyzing_images",
  "done",
  "failed",
]

function readDetailNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

function readPendingStage(value: unknown): AttachmentPendingStage | null {
  return typeof value === "string" && PENDING_STAGES.includes(value as AttachmentPendingStage)
    ? (value as AttachmentPendingStage)
    : null
}

/**
 * Reads the typed `409 attachment_pending` progress details (C-04).
 *
 * Returns null for every other error and for the pre-Task-10 shape without
 * `details.attachment_id`; the composer then keeps the manual retry fallback.
 * A legacy server with only `{attachment_id,name}` still auto-retries with the
 * default delay and no stage/percent to display.
 *
 * Contract note: `done`/`failed` are parsed here (and handled defensively by the
 * composer) but the current backend's `isAttachmentPending` only reports
 * `pending`/`processing`, so those two stages are not emitted today. Do not rely
 * on them without a server contract change.
 */
export function getAttachmentPendingDetails(error: unknown): AttachmentPendingProgress | null {
  if (!(error instanceof ApiError) || error.code !== "attachment_pending") return null
  const rawId = error.details.attachment_id
  const attachmentId =
    typeof rawId === "string" && rawId !== ""
      ? rawId
      : typeof rawId === "number" && Number.isFinite(rawId)
        ? String(rawId)
        : null
  if (attachmentId === null) return null
  const name = typeof error.details.name === "string" ? error.details.name : ""
  const errorText = typeof error.details.error === "string" ? error.details.error : ""
  const retryAfter = readDetailNumber(error.details.retry_after_seconds)
  return {
    attachmentId,
    name,
    stage: readPendingStage(error.details.stage),
    imagesTotal: readDetailNumber(error.details.images_total) ?? 0,
    imagesDone: readDetailNumber(error.details.images_done) ?? 0,
    imagesFailed: readDetailNumber(error.details.images_failed) ?? 0,
    imagesSkipped: readDetailNumber(error.details.images_skipped) ?? 0,
    percent: readDetailNumber(error.details.percent),
    etaSeconds: readDetailNumber(error.details.eta_seconds),
    retryAfterSeconds: retryAfter === null ? DEFAULT_RETRY_AFTER_SECONDS : Math.max(0, retryAfter),
    error: errorText,
  }
}

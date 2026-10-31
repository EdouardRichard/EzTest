/**
 * Wire types for the P1 clarification workspace.
 *
 * Field names are pinned by `docs/version-V0.2/contracts/openapi.yaml`
 * (`x-frozen: P1`, spec §8). IDs are decimal strings, timestamps RFC3339,
 * list endpoints use `{data, next_cursor, has_more}` and detail endpoints use
 * `{data: ...}`.
 */

export type RequirementStatus = "draft" | "clarified" | "closed" | "obsolete"

export type RequirementPriority = "P0" | "P1" | "P2" | "P3"

export type RequirementRelationKind = "depends_on" | "conflicts_with" | "overlaps" | "shares_scope"

export type ConversationKind = "clarification" | "design"

export type ConversationStatus = "active" | "closed"

export type MessageRole = "user" | "assistant"

export type ExtractionStatus = "pending" | "processing" | "done" | "failed" | "skipped"

export type AttachmentErrorCode = "unsupported_attachment_format" | "extraction_failed"

export type TeamRoomKind = "clarification" | "design" | "execution" | "delivery" | "steward"

export type TeamRoomPostKind = "finding" | "decision" | "blocker" | "handoff" | "status"

/** `GET /api/versions/{id}` P1 increments. */
export type RequirementCounts = {
  total: number
  draft: number
  clarified: number
  closed: number
  obsolete: number
}

export type VersionClarificationStats = {
  requirement_counts: RequirementCounts
  conversation_count: number
  pending_design_count: number
}

/** `POST /api/content` response (P0 ContentObject). */
export type ContentObject = {
  sha256: string
  size: number
  mime: string
  url: string
}

/** A file already uploaded to `/api/content`, ready to be attached to a message. */
export type UploadedAttachment = {
  sha256: string
  name: string
  mime: string
  size: number
}

/** `ConversationSummary` — conversation list item. */
export type ConversationSummary = {
  id: string
  title: string
  kind: ConversationKind
  status: ConversationStatus
  message_count: number
  last_message_at: string | null
  created_at: string
}

/** `Conversation` — detail without messages (`summary` is the rolling summary). */
export type Conversation = ConversationSummary & {
  summary: string | null
  updated_at: string
}

export type MessageRef = {
  kind: string
  id: string
}

export type TextPart = {
  type: "text"
  text: string
}

export type AttachmentPart = {
  type: "attachment"
  sha256: string
  name: string
  mime: string
  size: number
}

export type RequirementCardPart = {
  type: "requirement_card"
  requirement_id: string
  title: string
  status: RequirementStatus
  revision: number
}

export type RollbackProposalPart = {
  type: "rollback_proposal"
  requirement_id: string
  reason: string
}

export type MessagePart = TextPart | AttachmentPart | RequirementCardPart | RollbackProposalPart

/** The parts a client may send in a message (P1). */
export type NewMessagePart = TextPart | AttachmentPart

export type ConversationMessage = {
  id: string
  conversation_id: string
  role: MessageRole
  parts: MessagePart[]
  refs: MessageRef[]
  created_at: string
}

export type CreateConversationRequest = {
  kind: ConversationKind
  title?: string
}

export type PostMessageRequest = {
  client_message_id?: string
  parts: NewMessagePart[]
}

/** `RequirementTargets` — decimal IDs of version-bound SUTs/pages/APIs. */
export type RequirementTargets = {
  version_suts: string[]
  pages: string[]
  apis: string[]
}

/** `RequirementSummary` — current revision summary returned by the list endpoint. */
export type Requirement = {
  id: string
  current_version_id: string
  revision: number
  title: string
  status: RequirementStatus
  priority: RequirementPriority
  targets: RequirementTargets
  updated_at: string
  confirmed_at: string | null
}

/** `RequirementVersionView` — immutable requirement version snapshot. */
export type RequirementVersionView = {
  id: string
  requirement_id: string
  revision: number
  title: string
  description: string
  acceptance_criteria: string
  priority: RequirementPriority
  constraints: Record<string, unknown>
  target_version_suts: string[]
  target_pages: string[]
  target_apis: string[]
  content_hash: string
  created_at: string
}

export type RequirementConfirmation = {
  id: string
  requirement_version_id: string
  scope: "requirement" | "batch"
  batch_id: string | null
  actor_id: string
  confirmed_at: string
  affected_requirement_ids: string[]
}

export type RequirementRelation = {
  id: string
  from_requirement_id: string
  to_requirement_id: string
  kind: RequirementRelationKind
  description: string
  created_at: string
}

export type AttachmentView = {
  id: string
  name: string
  mime: string
  size: number
  extraction_status: ExtractionStatus
  extraction_error: { code: AttachmentErrorCode; message?: string } | null
  created_at: string
}

/** `RequirementDetail` — summary + current/history versions + confirmations. */
export type RequirementDetail = Requirement & {
  current_version: RequirementVersionView
  /** Older versions, revision descending (current version excluded). */
  history: RequirementVersionView[]
  confirmations: RequirementConfirmation[]
  relations: RequirementRelation[]
  attachments: AttachmentView[]
}

export type RequirementWriteRequest = {
  title: string
  description: string
  acceptance_criteria: string
  priority: RequirementPriority
  constraints?: Record<string, unknown>
  targets?: RequirementTargets
}

export type RequirementCreateRequest = RequirementWriteRequest

export type RequirementUpdateRequest = RequirementWriteRequest & {
  reason?: string
}

/** `ConfirmRequirementResult` — `confirmed=false` means idempotent replay. */
export type ConfirmRequirementResult = {
  confirmed: boolean
  requirement: RequirementDetail
}

/** `BatchConfirmResult`. */
export type BatchConfirmResult = {
  batch_id: string
  confirmed: string[]
  skipped: string[]
}

export type RollbackRequest = {
  reason?: string
  proposal_message_id?: string
}

export type TeamRoomDigest = {
  summary: string
  pinned_post_ids: string[]
  token_estimate: number
  version: number
  updated_at: string
}

export type TeamRoomPost = {
  id: string
  kind: TeamRoomPostKind
  content: string
  refs: MessageRef[]
  agent_run_id: string | null
  created_at: string
}

/** `TeamRoomView` — read-only digest + recent posts. */
export type TeamRoom = {
  team: TeamRoomKind
  digest: TeamRoomDigest | null
  posts: TeamRoomPost[]
}

/** One event delivered on the `version:{id}` channel (contracts/events.md). */
export type VersionEvent = {
  seq: number
  channel: string
  type: string
  scope: Record<string, unknown>
  payload: Record<string, unknown>
  actor_id: string | null
  request_id?: string
  created_at: string
}

/** Requirement list filter surfaced in the sidebar (closed folds in obsolete). */
export type RequirementFilter = "all" | "draft" | "clarified" | "closed"

/** A selectable target (SUT, page or API) from the version's bindings. */
export type TargetOption = {
  id: string
  label: string
  hint?: string
}

export type TargetOptions = {
  suts: TargetOption[]
  pages: TargetOption[]
  apis: TargetOption[]
}

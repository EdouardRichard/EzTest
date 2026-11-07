/**
 * Wire types for the P6 AI-conversation workspace (0.2.1).
 *
 * Part kinds are pinned by `docs/version-V0.2/contracts/openapi.yaml`
 * (`MessagePart` oneOf and `MessagePartView`). IDs are decimal strings and
 * timestamps are RFC3339. The parts-history endpoint returns the bare
 * `{data: MessagePartView[]}` envelope (no cursor); streaming rows are ordered
 * by `seq` and deduplicated by it on the client.
 */

/** Every part kind the conversation thread can render (OpenAPI `MessagePart`). */
export type MessagePartType =
  | "text"
  | "thinking"
  | "tool_call"
  | "job_card"
  | "attachment"
  | "image"
  | "requirement_card"
  | "case_card"
  | "error"

/**
 * `conversation_message_part.part_type` enum (OpenAPI `MessagePartView`).
 * Image parts live in the `conversation_message.parts` snapshot only; the
 * append-only streaming table CHECK has no `image` value.
 */
export type StreamingPartType = Exclude<MessagePartType, "image">

/** One append-only history row of `GET .../messages/{mid}/parts`. */
export type MessagePartView = {
  /** 1-based position within the message; rows arrive in ascending order. */
  seq: number
  part_type: StreamingPartType
  /** Part-shaped payload, corresponding to the `MessagePart` branch. */
  payload: Record<string, unknown>
  created_at: string
}

export type TextPart = {
  type: "text"
  text: string
}

/** Collapsed by default in the UI ("思考了 Ns"). */
export type ThinkingPart = {
  type: "thinking"
  text: string
  duration_ms: number
}

export type ToolCallStatus = "running" | "done" | "failed"

export type ToolCallPart = {
  type: "tool_call"
  /** Pairs the call with its result across streamed rows (spec §9.1). */
  call_id: string
  name: string
  status: ToolCallStatus
  arguments?: Record<string, unknown>
  result?: Record<string, unknown> | null
  duration_ms?: number
  error?: string
}

/** `work_item` states as projected on a job card (OpenAPI `JobView.state`). */
export type JobStatus =
  | "open"
  | "claimed"
  | "running"
  | "waiting_human"
  | "done"
  | "failed"
  | "cancelled"

export type JobKind =
  | "explore.login"
  | "explore.page"
  | "explore.api"
  | "explore.coverage"
  | "explore.gap"
  | "design.cases"
  | "review.cases"
  | "execute.batch"
  | "analyze.failure"
  | "report.version"
  | "knowledge.propose"
  // Conversation-writing background jobs are projected as job cards too.
  | "chat.turn"
  | "requirement.curate"
  | "design.round"

/** Progress card; `total = 0` means unknown, so no percentage may be derived. */
export type JobCardPart = {
  type: "job_card"
  /** `work_item.id`. */
  job_id: string
  kind: JobKind
  status: JobStatus
  current?: number
  total?: number
  /**
   * Typed failure code and user-facing reason persisted on a terminal failed
   * card (0.2.1 Task 75). Absent on healthy/retrying snapshots.
   */
  code?: string
  reason?: string
}

export type AttachmentPart = {
  type: "attachment"
  sha256: string
  name: string
  mime: string
  size: number
}

/** R39 image payload: content-ledger `data_ref` sent to a vision model. */
export type ImagePart = {
  type: "image"
  mime: string
  data_ref: string
  width: number
  height: number
}

export type RequirementCardStatus = "draft" | "clarified" | "closed" | "obsolete"

export type RequirementCardPart = {
  type: "requirement_card"
  requirement_id: string
  title: string
  status: RequirementCardStatus
  revision: number
}

export type CaseCardStatus = "draft" | "confirmed" | "stale" | "superseded"

export type CaseCardPart = {
  type: "case_card"
  case_id: string
  case_version_id?: string | null
  title: string
  status: CaseCardStatus
}

/** User-visible failure with the three-part message and the next action. */
export type ErrorPart = {
  type: "error"
  code: string
  message: string
  retryable?: boolean
  action?: string
  /**
   * v0.22 E-1 (R-E1-3) field-level correction data: the JSON path of the
   * rejected argument (e.g. `cases[0].assertions[0].database_id`), the legal
   * values, and the next action. Older payloads carry the same keys inside
   * `details`.
   */
  field?: string
  allowed?: string[]
  hint?: string
  details?: Record<string, unknown>
}

/** Discriminated union rendered by the conversation thread (P6-T2 consumes it). */
export type MessagePart =
  | TextPart
  | ThinkingPart
  | ToolCallPart
  | JobCardPart
  | AttachmentPart
  | ImagePart
  | RequirementCardPart
  | CaseCardPart
  | ErrorPart

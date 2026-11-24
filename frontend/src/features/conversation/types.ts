/**
 * Wire types for the P6 AI-conversation workspace (0.2.1).
 *
 * Part kinds are pinned by `docs/openapi/api.yaml`
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
 *
 * ONE runtime authority for the streaming part vocabulary, mirrored from the Go
 * domain `version.StreamingMessagePartTypes()` and pinned in the Go direction by
 * `TestEnumAuthorityFrontendStreamingPartTypes`
 * (backend/internal/version/domain/enum_authority_test.go): a member added or
 * removed on either side fails CI. The type is DERIVED from the list so the
 * runtime set and the compile-time union can never drift apart.
 *
 * It is also the seq-assignment vocabulary: an assistant message's streaming
 * parts carry a 1-based seq in part order (server `publishMessageEvent` /
 * `assistantStreamParts`), which is how an HTTP snapshot is seeded into the live
 * stream projection (v0.2.4-D fix wave, I-1).
 */
export const STREAMING_PART_TYPES = [
  "text",
  "thinking",
  "tool_call",
  "job_card",
  "attachment",
  "requirement_card",
  "case_card",
  "error",
] as const

export type StreamingPartType = (typeof STREAMING_PART_TYPES)[number]

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

/**
 * Lifecycle of an incremental Host reply head (`conversation_message.stream_state`,
 * v0.2.4-D). Full `conversation.message` frames carry it: the Begin placeholder is
 * `streaming`, the terminal frame `complete`/`interrupted`; batch-path messages
 * normalize to `complete`.
 */
export type MessageStreamState = "streaming" | "complete" | "interrupted"

/**
 * `conversation.message.delta` event payload (v0.2.4-D, SC-D2) — the TS mirror of
 * the ONE authoritative Go schema `versionapp.MessageDelta`
 * (`backend/internal/version/application/conversation_stream.go`, json tags).
 * The DB stores the full snapshot per seq; the event carries only the increment.
 * Exactly ONE of two modes is present per delta:
 *
 * - Append mode (text/thinking pure growth): `increment` is the new text suffix
 *   (always non-empty) and `offset` is the UTF-16 code-unit length of the
 *   accumulated text BEFORE the suffix — matching JavaScript `String.length`, so
 *   a replayed frame is deduped by offset. `offset` is omitted when 0.
 * - Replace mode (every other part type, any same-seq type change, and any
 *   text/thinking rewrite or metadata-only change such as thinking duration):
 *   `part` is the full `MessagePart` contract snapshot and replaces the part at
 *   `seq` wholesale; `offset`/`increment` are absent.
 *
 * `seq` is the 1-based `conversation_message_part` seq; `message_id` and
 * `conversation_id` are decimal string IDs. The terminal head state is carried by
 * the full `conversation.message` frame (`stream_state`), not by each delta.
 */
export type MessageDelta = {
  message_id: string
  conversation_id: string
  seq: number
  part_type: StreamingPartType
  /** Append mode only; omitted when 0. UTF-16 code units (== JS String.length). */
  offset?: number
  /** Append mode only; always non-empty. */
  increment?: string
  /** Replace mode only; full MessagePart snapshot, replaces the part at seq. */
  part?: MessagePart
}

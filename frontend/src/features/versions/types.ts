/** Shared DTOs for the P3 version API (spec §6.11, §7.1–§7.2, §9.3). */

/**
 * Version lifecycle status (spec §6.1). P3 only reaches draft → clarifying →
 * designing; later-phase states are declared so badges can render every value
 * the backend may return.
 */
export type VersionStatus =
  | "draft"
  | "clarifying"
  | "designing"
  | "reviewing"
  | "executing"
  | "continuing"
  | "completed"
  | "archived"

/**
 * One resolved binding target. Its ID stays a number: system/environment/code
 * repository belong to P2, unlike every P3-owned ID in this file (spec §7.1).
 */
export type VersionResource = {
  id: number
  name: string
  available: boolean
}

/** The resolved bindings a version detail exposes; unbound entries are null. */
export type VersionBindings = {
  system: VersionResource | null
  environment: VersionResource | null
  codeRepository: VersionResource | null
}

/**
 * `VersionDTO` (spec §7.2). `id` is a 64-bit snowflake serialized as a decimal
 * string and must never be converted with `Number()`.
 */
export type Version = {
  id: string
  name: string
  label: string
  description: string
  status: VersionStatus
  systemId: number | null
  environmentId: number | null
  codeRepositoryId: number | null
  codeBranch: string | null
  codeBaselineCommit: string | null
  bindings: VersionBindings
  requirementCount: number
  clarifiedCount: number
  openQuestionCount: number
  revision: number
  createdBy: number
  updatedBy: number
  createdAt: string
  updatedAt: string
}

/** Filters accepted by `GET /api/versions` (spec §7.2). */
export type VersionQuery = {
  status?: VersionStatus
  limit?: number
  cursor?: string
}

/** `POST /api/versions` body; P2 resource IDs are numbers. */
export type CreateVersionBody = {
  name: string
  label?: string
  description?: string
  systemId?: number | null
  environmentId?: number | null
  codeRepositoryId?: number | null
  codeBranch?: string | null
  codeBaselineCommit?: string | null
}

/** `PATCH /api/versions/{id}` body; `revision` is the optimistic-lock token. */
export type UpdateVersionBody = {
  revision: number
  name?: string
  label?: string
  description?: string
}

/** `PUT /api/versions/{id}/bindings` body; a missing or null field clears it. */
export type BindingsBody = {
  revision: number
  systemId?: number | null
  environmentId?: number | null
  codeRepositoryId?: number | null
  codeBranch?: string | null
  codeBaselineCommit?: string | null
}

/** Result of `POST /api/versions/{id}/enter-design` (spec §7.2). */
export type EnterDesignResult = {
  version: Version
  warnings: string[]
}

/** Frozen V1 refs envelope; absent references are empty arrays. */
export type VersionEventRefs = {
  evidence: string[]
  case: string[]
  requirement: string[]
  code: string[]
}

/**
 * One event from `channel=version:{id}` (spec §6.11, §7.7). `version_id` and
 * every ref are decimal strings; `seq` is the channel-local cursor, and refs
 * point at P3-owned IDs that must stay strings.
 */
export type VersionEvent = {
  seq: number
  channel: string
  type: string
  version_id: string | null
  batch_id: string | null
  case_id: string | null
  step_id: string | null
  agent_role: string | null
  session_id: string | null
  ts: string
  summary: string
  detail: Record<string, unknown>
  refs: VersionEventRefs
}

/** One `GET /api/versions/{id}/events` snapshot page. */
export type VersionEventPage = {
  events: VersionEvent[]
  cursor: number
  hasMore: boolean
}

/**
 * Requirement lifecycle status (spec §6.2). P3 only reaches draft → clarified;
 * later-phase values are declared so badges can render whatever the backend
 * may return.
 */
export type RequirementStatus = "draft" | "clarified" | "designed" | "verified" | "obsolete"

/** Requirement priority (spec §6.2). */
export type RequirementPriority = "must" | "should" | "could"

/** Whether a requirement or relation was authored by a user or the agent. */
export type RequirementSource = "user" | "ai"

/**
 * `RequirementDTO` (spec §7.3). Every P3-owned ID is a decimal string and must
 * never be converted with `Number()`; user IDs stay numbers.
 */
export type Requirement = {
  id: string
  versionId: string
  title: string
  description: string
  acceptanceCriteria: string
  priority: RequirementPriority
  status: RequirementStatus
  source: RequirementSource
  lastUpdateSource: RequirementSource
  revision: number
  confirmedAt: string | null
  confirmedBy: number | null
  openQuestionCount: number
  relationCount: number
  attachmentCount: number
  createdBy: number
  updatedBy: number
  createdAt: string
  updatedAt: string
}

/** Filters accepted by `GET /api/versions/{id}/requirements` (spec §7.3). */
export type RequirementQuery = {
  status?: RequirementStatus
  limit?: number
  cursor?: string
}

/** `POST /api/versions/{id}/requirements` body (spec §7.3). */
export type CreateRequirementBody = {
  title: string
  description?: string
  acceptanceCriteria?: string
  priority?: RequirementPriority
}

/**
 * `PATCH /api/versions/{id}/requirements/{rid}` body. `revision` is the
 * optimistic-lock token and is required on every edit (spec §6.10).
 */
export type UpdateRequirementBody = {
  revision: number
  title?: string
  description?: string
  acceptanceCriteria?: string
  priority?: RequirementPriority
}

/**
 * The four relation kinds (spec §6.4). Only `depends_on` is directed; the
 * other three are undirected even though storage keeps a from/to pair.
 */
export type RelationType = "depends_on" | "conflicts_with" | "overlaps" | "shares_scope"

/** `RelationDTO` (spec §7.4); both endpoint IDs are decimal strings. */
export type RequirementRelation = {
  id: string
  versionId: string
  fromRequirementId: string
  toRequirementId: string
  relationType: RelationType
  description: string
  source: RequirementSource
  createdBy: number
  createdAt: string
  updatedAt: string
}

/** `POST /api/versions/{id}/relations` body (spec §7.4). */
export type CreateRelationBody = {
  fromRequirementId: string
  toRequirementId: string
  relationType: RelationType
  description?: string
}

/** `PATCH /api/versions/{id}/relations/{relId}` body; relations have no revision. */
export type UpdateRelationBody = {
  relationType?: RelationType
  description?: string
}

/** Attachment kind derived from the MIME whitelist (spec §6.7). */
export type AttachmentKind = "text" | "image"

/**
 * Attachment metadata returned by the requirement attachment endpoints
 * (spec §7.3). `storagePath` is deliberately never exposed to the client.
 */
export type RequirementAttachment = {
  id: string
  kind: AttachmentKind
  filename: string
  mimeType: string
  sizeBytes: number
  sha256: string
  createdAt: string
}

// --- Clarification sessions, messages, questions and decisions (spec §7.5/§7.6) ---

/** Clarification session lifecycle (spec §5.2). */
export type ClarificationSessionStatus = "active" | "completed" | "cancelled"

/**
 * One version-level clarification session. `id` is a P3 snowflake string;
 * `agentSessionId` is the P1 agent session and stays a number.
 */
export type ClarificationSession = {
  id: string
  status: ClarificationSessionStatus
  agentSessionId: number | null
  startedAt: string
  endedAt: string | null
  contextSummary: string
  summaryUpdatedAt: string | null
}

/** A clarification question can be open, answered or dismissed (spec §6.6). */
export type ClarificationQuestionStatus = "open" | "answered" | "dismissed"

/** Whether a question/decision was authored by a user or the agent. */
export type ClarificationSource = "user" | "ai"

/** How a question was answered: inline in the UI or transcribed by the agent. */
export type ClarificationAnsweredVia = "ui" | "agent"

/** `QuestionDTO` (spec §7.6). Every P3-owned ID is a decimal string. */
export type ClarificationQuestion = {
  id: string
  versionId: string
  requirementId: string | null
  sessionId: string | null
  question: string
  status: ClarificationQuestionStatus
  answer: string
  source: ClarificationSource
  answeredVia: ClarificationAnsweredVia
  answeredBy: number | null
  answeredAt: string | null
  createdBy: number
  createdAt: string
  updatedAt: string
}

/** Decision lifecycle: proposed by AI, then accepted or rejected by a human. */
export type ClarificationDecisionStatus = "proposed" | "accepted" | "rejected"

/** `DecisionDTO` (spec §7.6). Every P3-owned ID is a decimal string. */
export type ClarificationDecision = {
  id: string
  versionId: string
  requirementId: string | null
  questionId: string | null
  topic: string
  decision: string
  rationale: string
  status: ClarificationDecisionStatus
  source: ClarificationSource
  proposedBy: number
  decidedBy: number | null
  decidedAt: string | null
  createdAt: string
  updatedAt: string
}

/** Per-status question counters returned by `GET clarification` (spec §7.5). */
export type ClarificationQuestionCounts = {
  open: number
  answered: number
  dismissed: number
}

/** Per-status decision counters returned by `GET clarification` (spec §7.5). */
export type ClarificationDecisionCounts = {
  proposed: number
  accepted: number
  rejected: number
}

/** `GET /api/versions/{id}/clarification` (spec §7.5): session + counters. */
export type ClarificationOverview = {
  session: ClarificationSession | null
  questions: ClarificationQuestionCounts
  decisions: ClarificationDecisionCounts
}

/** P1 message roles exposed by the clarification history projection. */
export type ClarificationMessageRole = "user" | "assistant" | "tool"

/** One content part of a persisted agent message. */
export type ClarificationMessagePart = {
  type: string
  text?: string
}

/** A tool call persisted on an assistant message. */
export type ClarificationToolCall = {
  id: string
  name: string
  arguments: string
}

/**
 * One clarification message projected from P1 `agent_messages` (spec §7.5).
 * `id`/`runId` are P1 numbers; the session ID is not part of this projection.
 */
export type ClarificationMessage = {
  id: number
  role: ClarificationMessageRole
  parts: ClarificationMessagePart[]
  reasoning: string
  toolCalls: ClarificationToolCall[] | null
  toolName: string | null
  runId: number | null
  createdAt: string
}

/** One `GET clarification/messages` page; the cursor is a P1 message ID. */
export type ClarificationMessagesPage = {
  items: ClarificationMessage[]
  nextCursor: string
  hasMore: boolean
}

/** One `GET clarification/questions` page. */
export type ClarificationQuestionsPage = {
  items: ClarificationQuestion[]
  nextCursor: string
  hasMore: boolean
}

/** One `GET decisions` page. */
export type ClarificationDecisionsPage = {
  items: ClarificationDecision[]
  nextCursor: string
  hasMore: boolean
}

/** Filters accepted by `GET clarification/messages` (spec §7.5). */
export type ClarificationMessageQuery = {
  beforeId?: number
  limit?: number
}

/** Filters accepted by `GET clarification/questions` (spec §7.6). */
export type ClarificationQuestionQuery = {
  status?: ClarificationQuestionStatus
  requirementId?: string
  limit?: number
  cursor?: string
}

/** Filters accepted by `GET decisions` (spec §7.6). */
export type ClarificationDecisionQuery = {
  status?: ClarificationDecisionStatus
  limit?: number
  cursor?: string
}

/** `POST clarification/messages` body (spec §7.5, 1–5000 characters). */
export type SendClarificationMessageBody = {
  message: string
}

/** 202 result of starting one clarification run. */
export type ClarificationRunResult = {
  agentSessionId: number
  runId: number
}

/** 202 result of posting a clarification message (spec §7.5). */
export type SendClarificationMessageResult = ClarificationRunResult & {
  sessionId: string
  status: string
}

/** `POST clarification/questions` body (spec §7.6); version-level omits it. */
export type CreateClarificationQuestionBody = {
  question: string
  requirementId?: string | null
}

/** `PATCH clarification/questions/{qid}` body (spec §7.6). */
export type UpdateClarificationQuestionBody = {
  status?: ClarificationQuestionStatus
  question?: string
  answer?: string
}

/** `POST decisions` body; the backend records it as accepted/user. */
export type CreateDecisionBody = {
  topic: string
  decision: string
  rationale?: string
  requirementId?: string | null
  questionId?: string | null
}

/** `PATCH decisions/{did}` body: only proposed decisions can transition. */
export type UpdateDecisionBody = {
  status: "accepted" | "rejected"
  rationale?: string
}

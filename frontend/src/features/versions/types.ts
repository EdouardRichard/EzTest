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

import type { StageView, VersionStage } from "@/features/design/types"
import { ApiError, apiGet, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

import type {
  Catalog,
  CreateVersionRequest,
  ReopenVersionInput,
  UpdateVersionRequest,
  Version,
  VersionSummary,
} from "./types"

/** First page of versions; P0 lists up to the API maximum per request. */
export async function listVersions(): Promise<VersionSummary[]> {
  const page = await apiGetPage<VersionSummary>("/api/versions?limit=100")
  return page.items
}

/** One nested version with all SUT bindings and resource summaries. */
export function getVersion(id: string): Promise<Version> {
  return apiGet<Version>(`/api/versions/${encodeURIComponent(id)}`)
}

/** Deterministic stage-machine projection (spec §5.3); drives the stage badge and gates. */
export function getVersionStage(id: string): Promise<StageView> {
  return apiGet<StageView>(`/api/versions/${encodeURIComponent(id)}/stage`)
}

/** Creates a version with the exact contract payload built by the wizard. */
export function createVersion(payload: CreateVersionRequest): Promise<Version> {
  return apiPost<Version>("/api/versions", payload)
}

/** Updates the version's auto-design flag (P2; no new settings keys). */
export function updateVersion(id: string, payload: UpdateVersionRequest): Promise<Version> {
  return apiPatch<Version>(`/api/versions/${encodeURIComponent(id)}`, payload)
}

/** `POST /api/versions/{id}/reopen`: completed → running; reason is required. */
export function reopenVersion(id: string, input: ReopenVersionInput): Promise<Version> {
  return apiPost<Version>(`/api/versions/${encodeURIComponent(id)}/reopen`, {
    reason: input.reason,
  })
}

/** SUT/environment/page/API/database/account/repository catalog for the wizard. */
export function getResourceCatalog(): Promise<Catalog> {
  return apiGet<Catalog>("/api/resources/catalog")
}

/* --------------------------------- stage machine (v0.22 B-4) ---------------------- */

/**
 * Structured version blocker (spec R-B4-4). The backend stores its JSON encoding
 * in `version.blocker` (TEXT), so `StageView.blocker` is an encoded string on the
 * wire; use {@link parseStageBlocker} before rendering it.
 */
export type StageBlocker = {
  kind: string
  reason: string
  action: string
  next: string
}

/** Typed details of a 409 `stage_gate_failed` response (Task 25 APIError shape). */
export type StageGateDetails = {
  gate: string
  reason: string
  action: string
  next: string
}

/** Typed details of a 409 `invalid_stage_transition` response. */
export type StageTransitionDetails = {
  from: string
  to: string
  action: string
}

function versionPath(versionId: string): string {
  return `/api/versions/${encodeURIComponent(versionId)}`
}

/** `POST /api/versions/{id}/stage/request`: user/Host asks the stage machine to advance. */
export function requestStage(versionId: string, to: VersionStage): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/request`, { to })
}

/** `POST /api/versions/{id}/stage/block`: enters blocked with the structured required fields. */
export function blockVersion(versionId: string, blocker: StageBlocker): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/block`, {
    kind: blocker.kind,
    reason: blocker.reason,
    action: blocker.action,
    next: blocker.next,
  })
}

/** `POST /api/versions/{id}/stage/unblock`: blocked → the recorded stage (validated server-side). */
export function unblockVersion(versionId: string): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/unblock`)
}

/** `POST /api/versions/{id}/stage/pause`: minimal graceful pause (R-B4-5). */
export function pauseVersion(versionId: string, reason = "用户暂停"): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/pause`, { reason })
}

/** `POST /api/versions/{id}/stage/resume`: paused → the recorded stage. */
export function resumeVersion(versionId: string): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/resume`)
}

function readDetailString(details: Record<string, unknown>, key: string): string {
  const value = details[key]
  return typeof value === "string" ? value : ""
}

function readDetailId(value: unknown): string | null {
  if (typeof value === "string" && value.length > 0) return value
  if (typeof value === "number" && Number.isFinite(value)) return String(value)
  return null
}

/**
 * Reads the typed `stage_gate_failed` 409 details emitted by
 * `StageGateError.APIError()`. Returns null for every other error, so callers can
 * fall back to the generic {@link ApiError} message.
 */
export function stageGateDetails(error: unknown): StageGateDetails | null {
  if (!(error instanceof ApiError) || error.status !== 409 || error.code !== "stage_gate_failed") {
    return null
  }
  const gate = readDetailString(error.details, "gate")
  if (gate === "") return null
  return {
    gate,
    reason: readDetailString(error.details, "reason"),
    action: readDetailString(error.details, "action"),
    next: readDetailString(error.details, "next"),
  }
}

/** Reads the typed `invalid_stage_transition` 409 details. */
export function stageTransitionDetails(error: unknown): StageTransitionDetails | null {
  if (
    !(error instanceof ApiError) ||
    error.status !== 409 ||
    error.code !== "invalid_stage_transition"
  ) {
    return null
  }
  const from = readDetailString(error.details, "from")
  const to = readDetailString(error.details, "to")
  if (from === "" && to === "") return null
  return { from, to, action: readDetailString(error.details, "action") }
}

/**
 * Decodes a `StageView.blocker` value. A v0.22 row is the JSON encoding of the
 * structured blocker; a pre-v0.22 plain-text blocker (or a paused reason) falls
 * back to a manual blocker whose reason is the whole string. Never throws.
 */
export function parseStageBlocker(raw: unknown): StageBlocker | null {
  if (raw === null || raw === undefined) return null
  if (typeof raw === "string") {
    const trimmed = raw.trim()
    if (trimmed === "") return null
    try {
      const parsed: unknown = JSON.parse(trimmed)
      if (isRecord(parsed)) return normalizeBlocker(parsed)
    } catch {
      // Legacy/plain-text blocker: keep the text as the reason.
    }
    return { kind: "manual", reason: trimmed, action: "", next: "" }
  }
  if (isRecord(raw)) return normalizeBlocker(raw)
  return null
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function normalizeBlocker(record: Record<string, unknown>): StageBlocker {
  return {
    kind: readDetailString(record, "kind") || "manual",
    reason: readDetailString(record, "reason"),
    action: readDetailString(record, "action"),
    next: readDetailString(record, "next"),
  }
}

/* ------------------------------------ plan (API-4/API-34) ----------------------- */

/** One ordered version plan item (read and write DTOs share these fields). */
export type PlanItem = {
  id: string
  version_id: string
  phase: string
  title: string
  status: string
  decision: string
  order: number
  /** Optimistic-lock token: every write must present the revision it read. */
  revision: number
  depends_on: string[]
  work_item_id: string | null
  created_at?: string
  updated_at?: string
}

/** Partial write body of `PATCH /api/versions/{id}/plan-items/{itemId}`. */
export type UpdatePlanItemInput = {
  title?: string
  status?: string
  decision?: string
  order?: number
  revision: number
}

/** Typed details of a stale-revision plan 409 (`code=conflict`). */
export type PlanConflictDetails = {
  itemId: string
  expectedRevision: number | null
  actualRevision: number | null
}

/** `GET /api/versions/{id}/plan-items`: first page of the version plan. */
export async function listPlanItems(versionId: string): Promise<PlanItem[]> {
  const page = await apiGetPage<PlanItem>(`${versionPath(versionId)}/plan-items?limit=100`)
  return page.items
}

/**
 * `PATCH /api/versions/{id}/plan-items/{itemId}`: writes with the revision that
 * was read. A stale revision is a typed 409 conflict (see
 * {@link planConflictDetails}); the caller refreshes the plan and retries with
 * the fresh revision.
 */
export function updatePlanItem(
  versionId: string,
  itemId: string,
  input: UpdatePlanItemInput,
): Promise<PlanItem> {
  return apiPatch<PlanItem>(
    `${versionPath(versionId)}/plan-items/${encodeURIComponent(itemId)}`,
    input,
  )
}

/** Reads the typed stale-revision plan conflict; null for every other error. */
export function planConflictDetails(error: unknown): PlanConflictDetails | null {
  if (!(error instanceof ApiError) || error.status !== 409 || error.code !== "conflict") {
    return null
  }
  const itemId = readDetailId(error.details.item_id)
  if (itemId === null) return null
  const expected = error.details.expected_revision
  const actual = error.details.actual_revision
  return {
    itemId,
    expectedRevision: typeof expected === "number" && Number.isFinite(expected) ? expected : null,
    actualRevision: typeof actual === "number" && Number.isFinite(actual) ? actual : null,
  }
}

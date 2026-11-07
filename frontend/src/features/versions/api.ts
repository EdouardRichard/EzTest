import type { StageView, VersionStage } from "@/features/design/types"
import { ApiError, apiGet, apiGetPage, apiPatch, apiPost, apiPut } from "@/lib/api-client"

import type {
  Catalog,
  CreateSutRepositoryRequest,
  CreateVersionRequest,
  DiscoveryCoverage,
  DiscoveryDecision,
  DiscoveryDeltas,
  DiscoveryKind,
  DiscoveryObservation,
  ReopenVersionInput,
  SutRepository,
  UpdateBindingRequest,
  UpdateVersionRequest,
  Version,
  VersionBindingOverview,
  VersionGap,
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

/** Creates a version with the API-15 environment-only create payload. */
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

/* ------------------------------- binding (API-16/API-17) ---------------------- */

/** `GET /api/versions/{id}/binding`: Task 29 derived SUT/environment overview. */
export function getVersionBinding(versionId: string): Promise<VersionBindingOverview> {
  return apiGet<VersionBindingOverview>(`${versionPath(versionId)}/binding`)
}

/**
 * `PUT /api/versions/{id}/binding`: partial binding update (repository
 * inclusion, account threshold, scope exclusions). The server enforces the
 * stage gate and returns the refreshed overview.
 */
export function updateVersionBinding(
  versionId: string,
  payload: UpdateBindingRequest,
): Promise<VersionBindingOverview> {
  return apiPut<VersionBindingOverview>(`${versionPath(versionId)}/binding`, payload)
}

/** One SUT-level repository association route (mounted by Task 37). */
function sutRepositoriesPath(sutId: string): string {
  return `/api/suts/${encodeURIComponent(sutId)}/repositories`
}

/** `GET /api/suts/{id}/repositories`: inherited multi-repository list (API-17). */
export function listSutRepositories(sutId: string): Promise<SutRepository[]> {
  return apiGet<SutRepository[]>(sutRepositoriesPath(sutId))
}

/** `POST /api/suts/{id}/repositories`: associate a global repository with a SUT. */
export function addSutRepository(
  sutId: string,
  payload: CreateSutRepositoryRequest,
): Promise<SutRepository> {
  return apiPost<SutRepository>(sutRepositoriesPath(sutId), payload)
}

/**
 * Typed details of a binding 400/409 response. A 400 carries the offending
 * `details.field` (mapped back to a wizard step); a 409 means the binding is
 * read-only at the current stage. Returns null for every other error.
 */
export type BindingErrorDetails = {
  status: number
  code: string
  message: string
  field: string
  readOnly: boolean
}

export function bindingErrorDetails(error: unknown): BindingErrorDetails | null {
  if (!(error instanceof ApiError)) return null
  if (error.status !== 400 && error.status !== 409) return null
  return {
    status: error.status,
    code: error.code,
    message: error.message,
    field: readDetailString(error.details, "field"),
    // Only the stage-gate state conflict means the binding is read-only; other
    // 409s (for example a stale-write conflict) are ordinary retryable failures.
    readOnly: error.status === 409 && error.code === "state_conflict",
  }
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

/* --------------------------- version facts (API-12/API-13) -------------------- */

/**
 * One append-only version fact row (`GET /api/versions/{id}/facts`). Facts never
 * carry credentials; `payload` is a reference/summary document and
 * `superseded_by` is non-null once the row lost a conflict or was replaced.
 */
export type VersionFact = {
  id: string
  version_id: string
  kind: string
  key: string
  payload: Record<string, unknown>
  confidence?: number | null
  source: string
  source_ref: string
  evidence_refs: string[]
  content_hash: string
  superseded_by: string | null
  created_at: string
}

export type GetVersionFactsOptions = {
  kind?: string
  /** Includes replaced/conflicting rows so the UI can render arbitration. */
  includeSuperseded?: boolean
  limit?: number
}

/**
 * `GET /api/versions/{id}/facts`: the version fact page. The default projection
 * only returns current facts; arbitration needs the full group, so the panel
 * asks for superseded rows explicitly.
 */
export async function getVersionFacts(
  versionId: string,
  options: GetVersionFactsOptions = {},
): Promise<VersionFact[]> {
  const search = new URLSearchParams()
  if (options.kind !== undefined && options.kind !== "") search.set("kind", options.kind)
  if (options.includeSuperseded === true) search.set("include_superseded", "true")
  search.set("limit", String(options.limit ?? 100))
  const page = await apiGetPage<VersionFact>(`${versionPath(versionId)}/facts?${search.toString()}`)
  return page.items
}

/** One competing fact in an arbitration result or group. */
export type FactArbitrationFact = {
  id: string
  kind: string
  key: string
  confidence?: number | null
  content_hash: string
  superseded_by: string | null
}

export type FactArbitrationDecision = "keep" | "drop"

/** `POST /api/versions/{id}/facts/{fid}/arbitrate` request body. */
export type FactArbitrationInput = {
  decision: FactArbitrationDecision
  reason: string
}

/** `FactArbitrationView`: the applied decision, the winner and the current rows. */
export type FactArbitrationResult = {
  version_id: string
  fact_id: string
  kind: string
  key: string
  decision: FactArbitrationDecision
  reason: string
  keep_id: string
  dropped_ids: string[]
  changed: boolean
  current: FactArbitrationFact[]
}

/**
 * `POST /api/versions/{id}/facts/{fid}/arbitrate`: `keep` preserves this fact
 * and supersedes its competitors; `drop` supersedes this fact instead. A reason
 * is required (1–2000 chars) and is written to the audit trail.
 */
export function arbitrateFact(
  versionId: string,
  factId: string,
  input: FactArbitrationInput,
): Promise<FactArbitrationResult> {
  return apiPost<FactArbitrationResult>(
    `${versionPath(versionId)}/facts/${encodeURIComponent(factId)}/arbitrate`,
    input,
  )
}

/* ------------------------- work items (read projection) ---------------------- */

/** One `work_item` projection (`GET /api/versions/{id}/work-items[/{id}]`). */
export type WorkItem = {
  id: string
  version_id: string
  kind: string
  goal: string
  state: string
  target?: Record<string, unknown>
  inputs?: Record<string, unknown>
  expected?: Record<string, unknown>
  budget?: Record<string, unknown>
  depends_on?: string[] | null
  lease?: Record<string, unknown> | null
  attempt: number
  result?: Record<string, unknown>
  created_by: string
  idempotency_key?: string | null
  created_at: string
  updated_at: string
}

export type ListWorkItemsOptions = {
  kind?: string
  state?: string
  createdBy?: string
  limit?: number
}

/** `GET /api/versions/{id}/work-items`: the version work-item page (id desc). */
export async function listWorkItems(
  versionId: string,
  options: ListWorkItemsOptions = {},
): Promise<WorkItem[]> {
  const search = new URLSearchParams()
  if (options.kind !== undefined && options.kind !== "") search.set("kind", options.kind)
  if (options.state !== undefined && options.state !== "") search.set("state", options.state)
  if (options.createdBy !== undefined && options.createdBy !== "") {
    search.set("created_by", options.createdBy)
  }
  search.set("limit", String(options.limit ?? 100))
  const page = await apiGetPage<WorkItem>(
    `${versionPath(versionId)}/work-items?${search.toString()}`,
  )
  return page.items
}

/** `GET /api/versions/{id}/work-items/{workItemId}`: one work-item projection. */
export function getWorkItem(versionId: string, workItemId: string): Promise<WorkItem> {
  return apiGet<WorkItem>(`${versionPath(versionId)}/work-items/${encodeURIComponent(workItemId)}`)
}

/* ------------------------- hard-gate stage writes ---------------------------- */

/** Target stage of the two hard-gate rejections. */
export type StageRejectTarget = "clarifying" | "designing"

export type RejectStageInput = {
  reason: string
  /** Omitted means "the previous stage of the current hard gate". */
  target_stage?: StageRejectTarget
}

/**
 * `POST /api/versions/{id}/stage/reject`: user-only hard-gate rollback. The
 * reason is required and audited; no requirement/case rows are deleted.
 */
export function rejectStage(versionId: string, input: RejectStageInput): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/reject`, input)
}

/**
 * `POST /api/versions/{id}/stage/advance`: advances to the next forward
 * workflow stage without naming it; the stage machine stays single-writer.
 */
export function advanceStage(versionId: string): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/advance`)
}

/* ---------------------- discovery observations (API-36..40) ------------------ */

/** Optional filters of `GET /api/versions/{id}/discovery/observations`. */
export type GetDiscoveryObservationsOptions = {
  kind?: DiscoveryKind
  /** Empty means every decision; the panel defaults to `proposed`. */
  decision?: DiscoveryDecision
  waveId?: string
  /** Server default is 50; HTTP limits are clamped to 1..200. */
  limit?: number
}

/**
 * `GET /api/versions/{id}/discovery/observations`: the DB-29 observation page
 * (W1-14A/API-36). The list envelope is unwrapped here; every id is already a
 * decimal string on the wire.
 */
export async function getDiscoveryObservations(
  versionId: string,
  options: GetDiscoveryObservationsOptions = {},
): Promise<DiscoveryObservation[]> {
  const search = new URLSearchParams()
  if (options.kind !== undefined) search.set("kind", options.kind)
  if (options.decision !== undefined) search.set("decision", options.decision)
  if (options.waveId !== undefined && options.waveId !== "") {
    search.set("wave_id", options.waveId)
  }
  search.set("limit", String(options.limit ?? 50))
  const payload = await apiGet<{ items: DiscoveryObservation[]; count: number }>(
    `${versionPath(versionId)}/discovery/observations?${search.toString()}`,
  )
  return payload.items
}

/** `GET /api/versions/{id}/discovery/deltas?wave_id=`: one wave/version delta pass. */
export function getDiscoveryDeltas(versionId: string, waveId?: string): Promise<DiscoveryDeltas> {
  const search = new URLSearchParams()
  if (waveId !== undefined && waveId !== "") search.set("wave_id", waveId)
  const query = search.toString()
  return apiGet<DiscoveryDeltas>(
    `${versionPath(versionId)}/discovery/deltas${query === "" ? "" : `?${query}`}`,
  )
}

/** `GET /api/versions/{id}/coverage`: W1 discovery-side coverage matrix. */
export function getDiscoveryCoverage(versionId: string): Promise<DiscoveryCoverage> {
  return apiGet<DiscoveryCoverage>(`${versionPath(versionId)}/coverage`)
}

/** `GET /api/versions/{id}/gaps`: current `version_fact kind='gap'` rows. */
export function getVersionGaps(versionId: string): Promise<VersionGap[]> {
  return apiGet<VersionGap[]>(`${versionPath(versionId)}/gaps`)
}

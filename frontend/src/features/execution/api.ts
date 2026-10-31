import { type ApiDownload, apiDownload, apiGet, apiGetPage, apiPost } from "@/lib/api-client"

import type {
  CaseRun,
  CaseRunDetail,
  CaseRunListOptions,
  CreateIntentInput,
  Evidence,
  EvidenceListOptions,
  ExecutionOverview,
  HumanRequest,
  HumanRequestListOptions,
  HumanRequestResponseInput,
  Intent,
  IntentListOptions,
  PauseCaseRunInput,
  PauseWaveInput,
  RunWave,
  WaveListOptions,
} from "./types"

/** Builds `?a=1&b=2`, skipping empty values (P0/P1 pagination style). */
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

function randomSuffix(): string {
  const bytes = new Uint8Array(4)
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    crypto.getRandomValues(bytes)
  } else {
    for (let i = 0; i < bytes.length; i += 1) bytes[i] = Math.floor(Math.random() * 256)
  }
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")
}

/**
 * Per-write idempotency key (spec §9.1): the server states are naturally
 * idempotent, the key only keeps retried/duplicate submissions correlated.
 */
function newIdempotencyKey(scope: string): string {
  return `${scope}-${Date.now().toString(36)}-${randomSuffix()}`
}

/* --------------------------------- overview -------------------------------- */

/** `GET /api/versions/{id}/execution/overview` (spec §9.2). */
export function getOverview(versionId: string): Promise<ExecutionOverview> {
  return apiGet<ExecutionOverview>(`${versionPath(versionId)}/execution/overview`)
}

/* ----------------------------------- waves --------------------------------- */

/** Lists wave history newest first (`id DESC`, spec §9.2). */
export async function listWaves(
  versionId: string,
  options: WaveListOptions = {},
): Promise<RunWave[]> {
  const page = await apiGetPage<RunWave>(
    `${versionPath(versionId)}/waves${query({
      before: options.before,
      limit: options.limit ?? 20,
    })}`,
  )
  return page.items
}

/** One wave header/counts projection; case runs are a separate paged endpoint. */
export function getWave(versionId: string, waveId: string): Promise<RunWave> {
  return apiGet<RunWave>(`${versionPath(versionId)}/waves/${encodeURIComponent(waveId)}`)
}

/** Pauses a wave; `graceful` is the default mode (spec §5.7). */
export function pauseWave(
  versionId: string,
  waveId: string,
  input: PauseWaveInput,
): Promise<RunWave> {
  const body: { mode: string; reason?: string } = { mode: input.mode }
  if (input.reason !== undefined && input.reason.length > 0) body.reason = input.reason
  return apiPost<RunWave>(
    `${versionPath(versionId)}/waves/${encodeURIComponent(waveId)}/pause`,
    body,
    { idempotencyKey: newIdempotencyKey("wave-pause") },
  )
}

/** Resumes a paused wave (open human requests make the server answer 409). */
export function resumeWave(versionId: string, waveId: string): Promise<RunWave> {
  return apiPost<RunWave>(
    `${versionPath(versionId)}/waves/${encodeURIComponent(waveId)}/resume`,
    undefined,
    { idempotencyKey: newIdempotencyKey("wave-resume") },
  )
}

/** Cancels a wave; non-terminal case runs become `superseded` (spec §5.8). */
export function cancelWave(versionId: string, waveId: string): Promise<RunWave> {
  return apiPost<RunWave>(
    `${versionPath(versionId)}/waves/${encodeURIComponent(waveId)}/cancel`,
    undefined,
    { idempotencyKey: newIdempotencyKey("wave-cancel") },
  )
}

/* --------------------------------- case runs ------------------------------- */

/** Lists the case runs of one wave, newest id first, optional status filter. */
export async function listCaseRuns(
  versionId: string,
  waveId: string,
  options: CaseRunListOptions = {},
): Promise<CaseRun[]> {
  const page = await apiGetPage<CaseRun>(
    `${versionPath(versionId)}/waves/${encodeURIComponent(waveId)}/case-runs${query({
      status: options.status,
      before: options.before,
      limit: options.limit ?? 20,
    })}`,
  )
  return page.items
}

/** One case run with its step/assertion/evidence snapshot (spec §9.3). */
export function getCaseRun(versionId: string, caseRunId: string): Promise<CaseRunDetail> {
  return apiGet<CaseRunDetail>(
    `${versionPath(versionId)}/case-runs/${encodeURIComponent(caseRunId)}`,
  )
}

/** Case-level pause does not change the wave status (spec §5.7). */
export function pauseCaseRun(
  versionId: string,
  caseRunId: string,
  input: PauseCaseRunInput,
): Promise<CaseRun> {
  return apiPost<CaseRun>(
    `${versionPath(versionId)}/case-runs/${encodeURIComponent(caseRunId)}/pause`,
    { mode: input.mode },
    { idempotencyKey: newIdempotencyKey("case-run-pause") },
  )
}

export function resumeCaseRun(versionId: string, caseRunId: string): Promise<CaseRun> {
  return apiPost<CaseRun>(
    `${versionPath(versionId)}/case-runs/${encodeURIComponent(caseRunId)}/resume`,
    undefined,
    { idempotencyKey: newIdempotencyKey("case-run-resume") },
  )
}

/* --------------------------------- evidence -------------------------------- */

/** Lists immutable evidence newest first (spec §4.6). */
export async function listEvidence(
  versionId: string,
  options: EvidenceListOptions = {},
): Promise<Evidence[]> {
  const page = await apiGetPage<Evidence>(
    `${versionPath(versionId)}/evidence${query({
      case_run_id: options.caseRunId,
      version_sut_id: options.versionSutId,
      kind: options.kind,
      before: options.before,
      limit: options.limit ?? 20,
    })}`,
  )
  return page.items
}

/** Evidence metadata only; bytes go through `downloadEvidence`. */
export function getEvidence(versionId: string, evidenceId: string): Promise<Evidence> {
  return apiGet<Evidence>(`${versionPath(versionId)}/evidence/${encodeURIComponent(evidenceId)}`)
}

/** Authenticated evidence bytes; the server re-checks sha256/size (spec §4.6). */
export function downloadEvidence(versionId: string, evidenceId: string): Promise<ApiDownload> {
  return apiDownload(`${versionPath(versionId)}/evidence/${encodeURIComponent(evidenceId)}/content`)
}

/* -------------------------------- human work ------------------------------- */

/** Lists open (or all) human requests paused for a user response. */
export async function listHumanRequests(
  versionId: string,
  options: HumanRequestListOptions = {},
): Promise<HumanRequest[]> {
  const page = await apiGetPage<HumanRequest>(
    `${versionPath(versionId)}/human-requests${query({ status: options.status })}`,
  )
  return page.items
}

/** Resolves one open human request (any logged-in user, spec §4.7). */
export function resolveHumanRequest(
  humanRequestId: string,
  response: HumanRequestResponseInput,
): Promise<HumanRequest> {
  return apiPost<HumanRequest>(
    `/api/human-requests/${encodeURIComponent(humanRequestId)}/resolve`,
    { response },
    { idempotencyKey: newIdempotencyKey("human-resolve") },
  )
}

/** Cancels one open human request; its case run ends `failed(session)`. */
export function cancelHumanRequest(humanRequestId: string): Promise<HumanRequest> {
  return apiPost<HumanRequest>(
    `/api/human-requests/${encodeURIComponent(humanRequestId)}/cancel`,
    undefined,
    { idempotencyKey: newIdempotencyKey("human-cancel") },
  )
}

/* --------------------------------- intents --------------------------------- */

/** Lists execution intents; `retest`/`manual` are created explicitly below. */
export async function listIntents(
  versionId: string,
  options: IntentListOptions = {},
): Promise<Intent[]> {
  const page = await apiGetPage<Intent>(
    `${versionPath(versionId)}/execution/intents${query({
      status: options.status,
      before: options.before,
      limit: options.limit ?? 20,
    })}`,
  )
  return page.items
}

/**
 * Creates a retest/manual intent (spec §5.8); a duplicate active key answers
 * 409 `state_conflict`. Reconciler-owned `initial` intents are never created
 * from the UI (there is no "start execution" button).
 */
export function createIntent(versionId: string, input: CreateIntentInput): Promise<Intent> {
  return apiPost<Intent>(
    `${versionPath(versionId)}/execution/intents`,
    { case_version_id: input.case_version_id, purpose: input.purpose },
    { idempotencyKey: newIdempotencyKey("intent-create") },
  )
}

/** Cancels an intent (idempotent for terminal intents, spec §5.8). */
export function cancelIntent(versionId: string, intentId: string): Promise<Intent> {
  return apiPost<Intent>(
    `${versionPath(versionId)}/execution/intents/${encodeURIComponent(intentId)}/cancel`,
    undefined,
    { idempotencyKey: newIdempotencyKey("intent-cancel") },
  )
}

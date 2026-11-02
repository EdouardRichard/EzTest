/**
 * Wire types for the P3 execution workspace.
 *
 * Field names are pinned by the approved P3 spec §9.3 (`ExecutionOverview`,
 * `RunWaveView`, `CaseRunView`, `CaseRunDetail`, `EvidenceView`,
 * `HumanRequestView`, `IntentView`) and §10.1 (events). IDs are decimal
 * strings, timestamps RFC3339 UTC, list endpoints use
 * `{data, next_cursor, has_more}` and detail endpoints use `{data: ...}`.
 */

/* ---------------------------------- enums ---------------------------------- */

/** Four-state verdicts plus the P3 process states (spec §4.3). */
export type CaseRunStatus =
  | "pending"
  | "running"
  | "paused"
  | "passed"
  | "failed"
  | "blocked"
  | "inconclusive"
  | "skipped"
  | "interrupted"
  | "superseded"

/** The four terminal verdicts (spec §4.3: 四态). */
export type CaseRunVerdict = "passed" | "failed" | "blocked" | "inconclusive"

/** Wave lifecycle (spec §4.2). */
export type WaveStatus = "planned" | "running" | "paused" | "done" | "failed" | "cancelled"

export type WaveTrigger = "auto" | "manual" | "retest"

export type PauseMode = "graceful" | "immediate"

/** Failure classification (spec §4.5) plus the empty no-class value. */
export type FailureClass =
  | ""
  | "application"
  | "environment"
  | "session"
  | "model"
  | "automation"
  | "timeout"
  | "executor_lost"
  | "blocked"
  | "unknown"

export type IntentPurpose = "initial" | "retest" | "manual"

export type IntentStatus =
  | "open"
  | "claimed"
  | "running"
  | "done"
  | "failed"
  | "cancelled"
  | "superseded"

export type HumanRequestKind = "login_takeover" | "conflict" | "other"

export type HumanRequestStatus = "open" | "resolved" | "cancelled" | "expired"

export type EvidenceKind =
  | "screenshot"
  | "snapshot"
  | "console"
  | "network"
  | "http"
  | "db"
  | "download"
  | "text"
  | "visual_actual"
  | "visual_diff"

export type StepResultStatus = "pending" | "running" | "passed" | "failed" | "blocked" | "skipped"

export type AssertionKind = "ui" | "api" | "db" | "text" | "visual"

export type AssertionVerdict = "passed" | "failed" | "inconclusive" | "skipped"

export type ExecutorStatus = "active" | "draining" | "offline"

/* ---------------------------------- counts --------------------------------- */

/** `run_wave.counts` (spec §5.6). */
export type WaveCounts = {
  total: number
  pending: number
  running: number
  paused: number
  passed: number
  failed: number
  blocked: number
  inconclusive: number
  skipped: number
  interrupted: number
  superseded: number
  evidence: number
  /** Per-version-SUT projection keyed by decimal `version_sut_id`. */
  by_version_sut?: Record<string, SutWaveCounts>
}

/** The per-SUT counters the spec §5.6 example pins. */
export type SutWaveCounts = {
  total: number
  passed: number
  failed: number
  blocked: number
  inconclusive: number
}

/** Version-wide case-run projection (no `evidence`/`by_version_sut` keys). */
export type CaseRunCounts = {
  total: number
  pending: number
  running: number
  paused: number
  passed: number
  failed: number
  blocked: number
  inconclusive: number
  skipped: number
  interrupted: number
  superseded: number
}

/* --------------------------------- overview -------------------------------- */

export type ExecutionQuotas = {
  global: number
  per_sut: number
  cross_system: number
}

/** `executor` block of `GET /api/versions/{id}/execution/overview`. */
export type ExecutorStatusView = {
  id?: string | null
  status: ExecutorStatus
  endpoint: string
  capacity: number
  active_contexts: number
  last_heartbeat: string | null
  version: string
}

/** `ExecutionOverview` (spec §9.3). Empty state is valid (no active wave). */
export type ExecutionOverview = {
  version_id: string
  active_wave: RunWave | null
  counts: WaveCounts
  case_run_counts: CaseRunCounts
  open_human_requests: number
  executor: ExecutorStatusView | null
  quotas: ExecutionQuotas
  human_request_timeout_seconds: number
}

/* ----------------------------------- wave ---------------------------------- */

/** `RunWaveView` (spec §9.3). */
export type RunWave = {
  id: string
  version_id: string
  status: WaveStatus
  trigger: WaveTrigger
  counts: WaveCounts
  started_at: string | null
  finished_at: string | null
  paused_at: string | null
  pause_mode: PauseMode | ""
  pause_reason: string
  created_at: string
}

/* --------------------------------- case run -------------------------------- */

/** `case_run.summary` fields the UI reads; unknown keys are ignored. */
export type CaseRunSummary = {
  verdict_reason?: string
  assertion_total?: number
  assertion_failed?: number
  step_total?: number
  step_failed?: number
  evidence_count?: number
  human_request_id?: string | null
  /** P5 visual counters (spec §7.3); `visual_skipped` stays compatible. */
  visual_skipped?: number
  visual_passed?: number
  visual_failed?: number
  visual_inconclusive?: number
}

/** `CaseRunView` (spec §9.3). */
export type CaseRun = {
  id: string
  wave_id: string
  intent_id: string
  case_id: string
  case_version_id: string
  title: string
  status: CaseRunStatus
  failure_class: FailureClass
  attempts: number
  summary: CaseRunSummary
  started_at: string | null
  finished_at: string | null
  version_sut_ids: string[]
  evidence_count: number
}

/** `step_result.observed` (spec §4.4). */
export type StepObserved = {
  summary?: string
  output?: string
  [key: string]: unknown
}

export type StepResult = {
  seq: number
  target_version_sut_id: string | null
  status: StepResultStatus
  observed: StepObserved
  error: string
  started_at: string | null
  finished_at: string | null
}

export type AssertionResult = {
  step_seq: number
  assertion_seq: number
  kind: AssertionKind
  verdict: AssertionVerdict
  expected: string
  actual: string
  reason: string
  target_version_sut_id: string | null
}

/** `EvidenceView` / the evidence summary embedded in `CaseRunDetail`. */
export type Evidence = {
  id: string
  case_run_id: string
  version_sut_id: string | null
  step_seq?: number | null
  assertion_seq?: number | null
  kind: EvidenceKind
  sha256: string
  size_bytes: number
  mime: string
  filename: string
  created_at: string
}

/** `CaseRunDetail` (spec §9.3): run + step/assertion/evidence snapshot. */
export type CaseRunDetail = {
  case_run: CaseRun
  steps: StepResult[]
  assertions: AssertionResult[]
  evidence: Evidence[]
}

/* ------------------------------- human request ----------------------------- */

export type HumanRequestPayload = {
  reason: string
  /** Nullable in the contract when the request is not tied to one SUT. */
  version_sut_id?: string | null
  account_role: string
  page_url: string
  challenge_kind: string
  evidence_id?: string | null
}

/** `ResolveHumanRequestRequest.response` (the code is never echoed back). */
export type HumanRequestResponseInput = {
  code?: string
  note?: string
}

/** `HumanRequestResponseView`: P3 only echoes the note summary (spec §4.7). */
export type HumanRequestResponseView = {
  note?: string
}

/** `HumanRequestView` (spec §9.3). The response never echoes the plaintext code. */
export type HumanRequest = {
  id: string
  version_id: string
  case_run_id: string | null
  kind: HumanRequestKind
  status: HumanRequestStatus
  payload: HumanRequestPayload
  response?: HumanRequestResponseView | null
  expires_at: string
  resolved_by?: string | null
  resolved_at?: string | null
  created_at: string
}

/* ---------------------------------- intent --------------------------------- */

/** `IntentView` (spec §9.3). */
export type Intent = {
  id: string
  version_id: string
  case_version_id: string
  case_id?: string | null
  case_title: string
  version_sut_ids: string[]
  purpose: IntentPurpose
  status: IntentStatus
  attempt_no: number
  created_at: string
}

/* --------------------------------- requests -------------------------------- */

export type PauseWaveInput = {
  mode: PauseMode
  reason?: string
}

/** Case-level pause is immediate-only in the frozen contract (spec §5.7). */
export type PauseCaseRunInput = {
  mode: "immediate"
}

export type CreateIntentInput = {
  case_version_id: string
  purpose: "retest" | "manual"
}

export type WaveListOptions = {
  before?: string
  limit?: number
}

export type CaseRunListOptions = WaveListOptions & {
  status?: CaseRunStatus
}

export type EvidenceListOptions = {
  caseRunId?: string
  versionSutId?: string
  kind?: EvidenceKind
  before?: string
  limit?: number
}

export type IntentListOptions = {
  status?: IntentStatus
  before?: string
  limit?: number
}

export type HumanRequestListOptions = {
  status?: HumanRequestStatus
}

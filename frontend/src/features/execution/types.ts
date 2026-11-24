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

/**
 * v0.22 human_request kinds (DB-16 / spec R-W2-1). Structured question kinds
 * (`clarification_question`/`approval`/`blocked_recovery`) share the table with
 * the P3 login takeover and are version todos, not execution blockers.
 */
export type HumanRequestKind =
  | "clarification_question"
  | "approval"
  | "blocked_recovery"
  | "login_takeover"
  | "conflict"

/** v0.22 H1 human_request state machine (spec R-W2-1, DB-16). */
export type HumanRequestStatus = "pending" | "answered" | "cancelled" | "expired"

export type EvidenceKind =
  | "screenshot"
  | "snapshot"
  | "console"
  | "network"
  | "http"
  | "db"
  | "download"
  | "text"
  | "command"
  | "file"
  | "trace"

export type StepResultStatus = "pending" | "running" | "passed" | "failed" | "blocked" | "skipped"

export type AssertionKind = "ui" | "api" | "db" | "text"

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
  /** Per-SUT projection keyed by decimal `sut_id` (v0.2.4-B rekey). */
  by_sut?: Record<string, SutWaveCounts>
}

/** The per-SUT counters the spec §5.6 example pins. */
export type SutWaveCounts = {
  total: number
  passed: number
  failed: number
  blocked: number
  inconclusive: number
}

/** Version-wide case-run projection (no `evidence`/`by_sut` keys). */
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

/**
 * Single global concurrency projection (v0.2.4-F design P5):
 * `execution.max_concurrent_case_runs`, 0 = unlimited. The retired
 * global/per_sut/cross_system trio is gone from the wire.
 */
export type ExecutionQuotas = {
  max_concurrent_case_runs: number
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
  /**
   * The overview always carries the effective settings, but older/fake
   * projections may omit them; the UI treats a missing block as unavailable
   * rather than crashing (v0.22 audit §2.5-5, Task 37 wiring).
   */
  quotas?: ExecutionQuotas
  human_request_timeout_seconds?: number
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

/** Observed agent-run usage persisted on a terminal case run (QA Q2). */
export type CaseRunUsage = {
  turns: number
  total_tokens: number
  prompt_tokens: number
  completion_tokens: number
}

/** Dispatch-time executor diagnostic persisted in `case_run.summary["dispatch"]` (X-01). */
export type CaseRunDispatch = {
  owner_executor_id: string
  endpoint: string
  executor_status: string
  last_heartbeat: string | null
  version: string
  registry_hit: boolean
  dispatch_at: string
  attempt: number
}

/** `case_run.summary` fields the UI reads; unknown keys are ignored. */
export type CaseRunSummary = {
  verdict_reason?: string
  assertion_total?: number
  assertion_failed?: number
  step_total?: number
  step_failed?: number
  evidence_count?: number
  human_request_id?: string | null
  /**
   * QA Q2: a terminal run explains itself. `terminal_cause` names the concrete
   * stop (budget, agent stop, blocked, assertion failure, ...), `last_error`
   * carries the observable detail and `next_action` is the stable human next
   * step. All three are emitted as empty strings on a clean verdict (Go sends
   * plain strings, never null/absent).
   */
  terminal_cause?: string
  last_error?: string
  next_action?: string
  /** X-01: stable cause of a retryable run error (a retry reason, not a terminal class). */
  retry_reason?: string
  /** X-01: latest dispatch-time executor snapshot; null/absent when no dispatch recorded. */
  dispatch?: CaseRunDispatch | null
  /** Observed agent-run usage; absent on rows settled before usage persisted. */
  usage?: CaseRunUsage | null
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
  sut_ids: string[]
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
  target_sut_id: string | null
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
  target_sut_id: string | null
}

/** `EvidenceView` / the evidence summary embedded in `CaseRunDetail`. */
export type Evidence = {
  id: string
  case_run_id: string
  sut_id: string | null
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
  /**
   * The user-facing title/reason. Login takeovers carry the challenge reason;
   * v0.22 decision requests carry the reason supplied by the producer.
   */
  reason: string
  /** Nullable in the contract when the request is not tied to one SUT. */
  sut_id?: string | null
  /** Login-takeover only fields (absent on v0.22 decision requests). */
  account_role?: string
  page_url?: string
  challenge_kind?: string
  evidence_id?: string | null
}

/** v0.22 decision vocabulary of approval/blocked_recovery/conflict. */
export type HumanDecision = "approved" | "rejected"

/**
 * `ResolveHumanRequestRequest.response` (the code is never echoed back).
 * Login takeovers submit a one-time code/note; decision requests submit a
 * `decision` and (for a rejection) a mandatory `reason`.
 */
export type HumanRequestResponseInput = {
  code?: string
  note?: string
  decision?: HumanDecision
  reason?: string
}

/** `HumanRequestResponseView`: the code is never echoed back (spec §4.7). */
export type HumanRequestResponseView = {
  note?: string
  decision?: HumanDecision | string
  reason?: string
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
  sut_ids: string[]
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
  sutId?: string
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

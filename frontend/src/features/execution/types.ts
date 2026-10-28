/**
 * Shared DTOs for the P6 execution API (spec §6.2, §6.3, §6.4).
 *
 * Every execution-owned identifier is a 64-bit Snowflake serialized as a
 * decimal string. IDs must never be converted with `Number()`: values above
 * `Number.MAX_SAFE_INTEGER` would silently lose precision.
 */

/** Run batch statuses (spec §5.1, DDL `run_batch.status`). */
export type BatchStatus = "planned" | "running" | "done" | "failed" | "cancelled"

/** Pause modes accepted by `POST .../pause` (spec §5.4). */
export type PauseMode = "graceful" | "immediate"

/** Case run statuses (spec §5.8). */
export type CaseRunStatus =
  | "pending"
  | "running"
  | "passed"
  | "failed"
  | "blocked"
  | "inconclusive"
  | "skipped"
  | "interrupted"

/** Failure classes of a non-passed case run (spec §5.8). `flaky` is derived. */
export type FailureClass = "application" | "environment" | "session" | "model" | "case"

/** Step result statuses (spec §5.4). */
export type StepStatus = "pending" | "running" | "passed" | "failed" | "interrupted"

/** Assertion-level verdict; distinct from the case-level four-state verdict. */
export type AssertionVerdict = "passed" | "failed" | "not_run"

/**
 * Stable `details.reason` values of spec §6.4. The union keeps the Chinese
 * message map exhaustive: a new reason fails typecheck until it has copy.
 */
export type ExecutionErrorReason =
  | "version_not_executable"
  | "baseline_required"
  | "baseline_changed"
  | "batch_active"
  | "no_cases"
  | "too_many_cases"
  | "invalid_case_ids"
  | "environment_required"
  | "batch_finished"
  | "already_paused"
  | "not_paused"
  | "case_run_terminal"
  | "evidence_missing"
  | "executor_unavailable"
  | "session_unavailable"
  | "unsupported_assertion_kind"
  | "export_failed"
  | "auth_flow_invalid"
  | "takeover_required"

/** Per-status counters of one batch (spec §6.3). */
export type BatchCounts = {
  total: number
  passed: number
  failed: number
  blocked: number
  inconclusive: number
  skipped: number
  interrupted: number
}

/** `BatchDTO` of spec §6.3. */
export type Batch = {
  id: string
  version_id: string
  batch_no: number
  status: BatchStatus
  paused: boolean
  pause_mode: PauseMode | ""
  pause_reason: string
  baseline_hash: string
  code_baseline_commit: string | null
  head_commit: string | null
  counts: BatchCounts
  created_by: string
  started_at: string | null
  finished_at: string | null
  failure_reason: string
  created_at?: string
  updated_at?: string
}

/** Filters accepted by `GET /api/versions/{id}/batches`. */
export type BatchQuery = {
  status?: BatchStatus
  limit?: number
  cursor?: string
}

/** Body of `POST /api/versions/{id}/batches`; omitted `caseIds` means all cases. */
export type CreateBatchBody = {
  caseIds?: string[]
}

/** The P1 session a case run executed under (credential-free projection). */
export type CaseRunSession = {
  id: string
  account: string
  state: string
}

/** Resume checkpoint of an interrupted/paused case run (spec §5.4). */
export type CaseCheckpoint = {
  completed_seqs: number[]
  cleanup_required: boolean
  interrupted_step_seq: number | null
}

/** One recorded step of a case run (spec §6.3). */
export type StepResultView = {
  id?: string
  seq: number
  action: string
  expected: string
  actual: string
  status: StepStatus
  evidence_ids: string[]
  started_at?: string | null
  finished_at?: string | null
}

/** One recorded assertion outcome (spec §6.3). */
export type AssertionView = {
  id: string
  kind: string
  target: string
  expected: string
  actual: string
  verdict: AssertionVerdict
  evidence_ids: string[]
  step_seq?: number
  failure_reason?: string
}

/** Evidence summary embedded in a case run / report (spec §6.3). */
export type EvidenceView = {
  id: string
  kind: string
  mime: string
  size_bytes: number
  sha256: string
}

/** Evidence metadata returned by `GET .../evidence/{evidenceId}/meta`. */
export type EvidenceMeta = EvidenceView & {
  batch_id?: string
  case_run_id?: string | null
  step_result_id?: string | null
  metadata: Record<string, unknown>
  created_at: string
}

/** Waiting human takeover attached to a case run (spec §5.9, §9.3). */
export type PendingTakeover = {
  id: string
  reason: string
  instructions: Record<string, unknown>
  expires_at: string
  account?: string | null
  system?: string | null
}

/** `CaseRunDTO` detail of spec §6.3. List responses may omit the detail arrays. */
export type CaseRun = {
  id: string
  batch_id: string
  case_node_id: string
  case_revision: number
  attempt: number
  status: CaseRunStatus
  failure_class: FailureClass | null
  failure_reason: string
  summary: string
  duration_ms: number
  title: string
  session: CaseRunSession | null
  checkpoint: CaseCheckpoint
  steps: StepResultView[]
  assertions: AssertionView[]
  evidence: EvidenceView[]
  pending_takeover: PendingTakeover | null
  agent_run_id?: string | null
  agent_session_id?: string | null
  retry_of_case_run_id?: string | null
  started_at?: string | null
  finished_at?: string | null
}

/** Filters accepted by `GET .../case-runs`. */
export type CaseRunQuery = {
  status?: CaseRunStatus
  limit?: number
  cursor?: string
}

/** Batch identity embedded in a report (spec §6.3). */
export type ReportBatch = {
  id: string
  batch_no: number
  status: BatchStatus
  baseline_hash: string
  code_baseline_commit: string | null
}

/** Aggregated report counters (spec §6.3). */
export type ReportSummary = {
  case_count: number
  by_status: Partial<Record<CaseRunStatus, number>>
  by_failure_class: Partial<Record<FailureClass, number>>
  flaky_case_ids: string[]
  duration_ms: number
  evidence_count: number
}

/** One case row of the report (spec §6.3). */
export type ReportCase = {
  case_run_id: string
  case_node_id: string
  title: string
  status: CaseRunStatus
  failure_class: FailureClass | null
  steps: number
  assertions: number
  evidence: number
  duration_ms: number
}

/** Environment identity the batch executed against. */
export type ReportEnvironment = {
  system: string
  environment: string
  base_url: string
}

/** `ReportDTO` of spec §6.3 — the Batch 1 report derived at read time. */
export type ReportView = {
  batch: ReportBatch
  summary: ReportSummary
  cases: ReportCase[]
  environment: ReportEnvironment
  generated_at: string
}

/** Report export formats; P6 only ships server-rendered Markdown (spec §6.2). */
export type ReportFormat = "markdown"

/** A downloaded report plus the filename advertised by `Content-Disposition`. */
export type ReportDownload = {
  blob: Blob
  filename: string
}

/**
 * One execution Agent session of the version channel. The P1 projection may
 * omit the current-case fields, so they stay optional and nullable.
 */
export type ExecutionSession = {
  id: string
  channel: string
  title: string
  status: string
  running: boolean
  agent_role?: string | null
  profile_key?: string
  current_case_run_id?: string | null
  case_node_id?: string | null
  case_title?: string | null
  last_activity_at?: string | null
  created_at?: string
  updated_at?: string
}

/** Filters accepted by `GET .../execution/sessions`. */
export type ExecutionSessionQuery = {
  channel?: string
  limit?: number
  cursor?: string
}

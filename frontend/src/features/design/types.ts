/**
 * Wire types for the P2 case-design workspace.
 *
 * Field names are pinned by the approved P2 spec §7 (`DesignGraphView`,
 * `CaseDetailView`, `CaseConfirmationRequest/Result`, `ExportView`). IDs are
 * decimal strings, timestamps RFC3339, list endpoints use
 * `{data, next_cursor, has_more}` and detail endpoints use `{data: ...}`.
 */

import type { CaseRunVerdict } from "@/features/execution/types"

import type { ReviewState } from "./judgment"

/** Per-requirement projection shown on the requirement node (spec §4.6). */
export type DesignStatus = "pending" | "in_design" | "draft" | "confirmed" | "stale"

export type DesignPriority = "P0" | "P1" | "P2" | "P3"

export type DesignRequirementStatus = "draft" | "clarified" | "closed" | "obsolete"

export type CaseVersionStatus = "draft" | "confirmed" | "stale" | "superseded"

export type DesignStats = {
  requirements: number
  scenarios: number
  cases: number
  draft: number
  confirmed: number
  stale: number
}

/** One current case version summary inside the graph. */
export type DesignCaseSummary = {
  case_id: string
  current_version_id: string
  revision: number
  title: string
  priority: DesignPriority
  status: Exclude<CaseVersionStatus, "superseded">
  sut_description: string
  covered_acs: string[]
  step_count: number
  assertion_count: number
  /**
   * Latest execution verdict of this current case version (spec §9.3);
   * `inconclusive` when the case has never been executed. Additive to the
   * frozen P2 graph contract, so older fixtures may omit it.
   */
  execution_verdict?: CaseRunVerdict
  /**
   * Judge review projection of the current case version (v0.2.4-E, spec SC-E5,
   * T16). `""` when the judgment gate is unwired/unreadable (degraded) — the UI
   * tolerates it as unknown. Additive; older fixtures may omit it.
   */
  review_state?: ReviewState | ""
  created_at: string
}

export type DesignScenario = {
  id: string
  requirement_version_id: string
  name: string
  description: string
  /** API name `sort`; DB column `sort_order` (spec §3.1/§7.2.1). */
  sort: number
  cases: DesignCaseSummary[]
}

export type DesignRequirement = {
  requirement_id: string
  requirement_version_id: string
  revision: number
  title: string
  status: DesignRequirementStatus
  design_status: DesignStatus
  scenarios: DesignScenario[]
}

/** `GET /api/versions/{id}/design/graph` (spec §7.2.1). */
export type DesignGraph = {
  version_id: string
  generated_at: string
  graph_hash: string
  pending_design_count: number
  auto_design_new_requirements: boolean
  stats: DesignStats
  requirements: DesignRequirement[]
}

export type DesignRoundActivation = "manual" | "auto_gap" | "auto_change" | "auto_new"

/** `active_round` from `GET /api/versions/{id}/design/overview` (spec §7.2). */
export type DesignRound = {
  id: string
  round_no: number
  conversation_id: string
  activation: DesignRoundActivation
  requirement_ids: string[]
  created_at: string
}

export type DesignOverview = {
  pending_design_count: number
  auto_design_new_requirements: boolean
  round_count: number
  active_round: DesignRound | null
}

/* ----------------------------------- stage ---------------------------------- */

/**
 * 0.2.1 version stage-machine projection (spec §5.3). Single-source (V-0017):
 * validated against the Go authority version/domain Stages() by
 * backend/internal/version/domain/enum_authority_test.go. Do not add/remove a
 * member here without updating the domain enum.
 */
export type VersionStage =
  | "created"
  | "clarifying"
  | "requirement_review"
  | "designing"
  | "case_review"
  | "executing"
  | "reporting"
  | "completed"
  | "blocked"
  | "paused"

/** `GET/POST /api/versions/{id}/stage` response payload. */
export type StageView = {
  version_id: string
  stage: VersionStage
  stage_entered_at: string
  blocker: string | null
}

/* --------------------------------- case detail ------------------------------ */

export type DesignCaseVersionSummary = {
  case_version_id: string
  revision: number
  status: CaseVersionStatus
  title: string
  created_at: string
  confirmed_at: string | null
}

export type DesignStep = {
  seq: number
  action: string
  expected: string
  target_description: string
  account_role: string
  data: Record<string, unknown>
}

export type DesignAssertionKind = "ui" | "api" | "db" | "text"

export type DesignAssertion = {
  step_seq: number
  kind: DesignAssertionKind
  target: string
  expected: string
  /** v0.2.4-B: a db assertion names the SUT it reads; null for other kinds. */
  sut_id: string | null
  /** P2 `case_assertion` id. */
  case_assertion_id?: string
}

export type DesignSourceKind = "requirement" | "code" | "page" | "api" | "knowledge"

export type DesignSource = {
  kind: DesignSourceKind
  ref_id: string
  path: string
  line: number | null
  sha: string
}

export type DesignCaseVersionDetail = {
  case_version_id: string
  revision: number
  title: string
  description: string
  sut_description: string
  preconditions: string
  covered_acs: string[]
  test_data: Record<string, unknown>
  priority: DesignPriority
  status: CaseVersionStatus
  content_hash: string
  created_at: string
  steps: DesignStep[]
  assertions: DesignAssertion[]
  sources: DesignSource[]
}

/** `GET /api/versions/{id}/design/cases/{caseId}` (spec §7.2.2). */
export type DesignCaseDetail = {
  case_id: string
  status: Exclude<CaseVersionStatus, "superseded">
  current_version_id: string
  requirement_id: string
  requirement_version_id: string
  scenario_id: string
  scenario_name: string
  versions: DesignCaseVersionSummary[]
  selected: DesignCaseVersionDetail
  confirmable: boolean
  /**
   * Judge review projection of the CURRENT case version (T16), independent of
   * which historical revision `selected` shows. `""` degraded is tolerated.
   */
  review_state?: ReviewState | ""
}

/* -------------------------------- confirmation ------------------------------ */

export type CaseConfirmationScope = "case" | "scenario" | "requirement"

/* --------------------------- discovery revisions ---------------------------- */

export type DiscoveryRevisionTrigger = "discovery_delta" | "rollback"

/**
 * One discovery/rollback case revision shown by `DiscoveryRevisionNotice`.
 *
 * The authoritative source is the EV-12 `case.revision.auto_confirmed` SSE
 * payload (`trigger`, `rollback_of`, `confidence`). The design case-detail GET
 * does not expose the revision trigger yet, so a historical notice can only be
 * inferred from the case sources (`path=discovery_delta|discovery_observation`);
 * `inferred` marks that weaker provenance and suppresses the auto-confirm claim.
 */
export type DiscoveryRevisionData = {
  /** Alias accepted by the notice API for a case version id. */
  id?: string
  caseId?: string
  caseVersionId: string
  revision?: number
  status: CaseVersionStatus
  trigger?: DiscoveryRevisionTrigger | string
  autoConfirmedBy?: string | null
  rollbackOf?: string | null
  sourceObservationId?: string | null
  resourceId?: string | null
  confidence?: string | null
  lowConfidenceReason?: string | null
  /** True when derived from case sources rather than an authoritative event. */
  inferred?: boolean
}

/** `POST /api/versions/{id}/case-versions/{caseVersionId}/rollback` (API-41). */
export type CaseRevisionRollbackView = {
  case_id: string
  case_version_id: string
  rolled_back_from: string
  revision: number
  /**
   * R-E2r (release notes E §2): a NEW replacement is `draft` (awaiting judge +
   * human confirm); `confirmed` only on idempotent replay of a legacy/already
   * confirmed replacement. The status is passed through as returned, never
   * assumed.
   */
  status: CaseVersionStatus
  trigger: string
}

export type CaseConfirmationInput = {
  scope: CaseConfirmationScope
  target_id: string
  batch_id?: string
}

export type CaseConfirmationItem = {
  case_id: string
  case_version_id: string
  status?: CaseVersionStatus
}

/** `POST /api/versions/{id}/design/confirmations` (spec §7.3). */
export type CaseConfirmationResult = {
  scope: CaseConfirmationScope
  target_id: string
  batch_id: string
  confirmed: CaseConfirmationItem[]
  skipped: CaseConfirmationItem[]
  confirmed_at: string
}

/* --------------------------- judgment report (T16) -------------------------- */

/** One judge finding against a case version (`judgmentFindingDTO`). */
export type JudgmentFindingView = {
  dimension: string
  /** blocker/important/minor; typed loose for forward tolerance. */
  severity: string
  message: string
  case_ref?: string
}

/** One immutable judge verdict on one case version (`judgmentDTO`). */
export type JudgmentView = {
  id: string
  case_id: string
  case_version_id: string
  content_hash: string
  round: number
  /** pass/fail; typed loose for forward tolerance. */
  verdict: string
  findings: JudgmentFindingView[]
  ac_coverage_matrix: Record<string, unknown>
  created_at: string
}

/** One current case in the judgment report (`judgmentCaseDTO`). */
export type JudgmentCaseView = {
  case_id: string
  current_version_id: string
  /** `""` when the judgment gate is unwired/unreadable (degraded). */
  review_state: ReviewState | ""
  judgment: JudgmentView | null
}

/** `GET /api/versions/{id}/design/judgments` (`judgmentReportDTO`). */
export type JudgmentReportView = {
  cases: JudgmentCaseView[]
}

/** `POST /api/versions/{id}/design/judgments/{caseId}/override` (`judgmentOverrideDTO`). */
export type JudgmentOverrideView = {
  id: string
  case_id: string
  case_version_id: string
  content_hash: string
  judgment_id?: string | null
  condition: string
  actor_id: string
  reason: string
  created_at: string
}

/* ----------------------------------- export --------------------------------- */

export type ExportFormat = "markdown" | "mermaid" | "opml" | "csv"

export const EXPORT_FORMATS: ExportFormat[] = ["markdown", "mermaid", "opml", "csv"]

export type ExportRequest = {
  owner_kind: "case"
  owner_id: string
  format: ExportFormat
}

export type ExportStatus = "ready" | "failed"

/** `ExportView` (spec §7.4): DB `file_format/size_bytes/content_object_id` map to product names. */
export type ExportView = {
  id: string
  version_id: string
  owner_kind: "case" | "report" | "chain"
  owner_id: string
  revision: number
  format: ExportFormat
  size: number
  mime: string
  filename: string
  sha256: string
  status: ExportStatus
  created_at: string
  download_url: string
}

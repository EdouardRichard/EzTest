/**
 * Wire types for the P2 case-design workspace.
 *
 * Field names are pinned by the approved P2 spec §7 (`DesignGraphView`,
 * `CaseDetailView`, `CaseConfirmationRequest/Result`, `ExportView`). IDs are
 * decimal strings, timestamps RFC3339, list endpoints use
 * `{data, next_cursor, has_more}` and detail endpoints use `{data: ...}`.
 */

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
  default_version_sut_id: string
  version_sut_ids: string[]
  page_ids: string[]
  api_ids: string[]
  step_count: number
  assertion_count: number
  created_at: string
}

export type DesignScenario = {
  id: string
  requirement_version_id: string
  name: string
  description: string
  version_sut_ids: string[]
  page_ids: string[]
  api_ids: string[]
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
  target_version_sut_ids: string[]
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
  target_version_sut_id: string | null
  target_page_id: string | null
  target_api_id: string | null
  account_role: string
  data: Record<string, unknown>
}

export type DesignAssertionKind = "ui" | "api" | "db" | "text" | "visual"

export type DesignAssertion = {
  step_seq: number
  kind: DesignAssertionKind
  target: string
  expected: string
  target_version_sut_id: string | null
  target_api_id: string | null
  database_id: string | null
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
  preconditions: string
  test_data: Record<string, unknown>
  priority: DesignPriority
  default_version_sut_id: string
  version_sut_ids: string[]
  page_ids: string[]
  api_ids: string[]
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
}

/* -------------------------------- confirmation ------------------------------ */

export type CaseConfirmationScope = "case" | "scenario" | "requirement"

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

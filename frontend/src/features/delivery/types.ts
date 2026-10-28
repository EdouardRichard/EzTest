/**
 * Shared DTOs for the P7 delivery API (spec §7.3–§7.5, §7.10).
 *
 * Every delivery-owned identifier is a 64-bit Snowflake serialized as a decimal
 * string. IDs must never be converted with `Number()`: values above
 * `Number.MAX_SAFE_INTEGER` would silently lose precision.
 */

import type { VersionEvent } from "@/features/versions/types"
import type { SSEStatus } from "@/lib/sse-client"

/** Issue source: AI analysis or a human author (spec §5.4). */
export type IssueSource = "ai" | "human"

/** Issue category (spec §5.4). */
export type IssueCategory = "defect" | "requirement_gap" | "other"

/** Issue severity, ordered blocker → info by the API (spec §7.3). */
export type IssueSeverity = "blocker" | "major" | "minor" | "info"

/** Issue lifecycle: open is the only non-terminal state (spec §5.4). */
export type IssueStatus = "open" | "confirmed" | "dismissed"

/** AI confidence of an issue (spec §5.4). */
export type IssueConfidence = "high" | "medium" | "low"

/** Failure classes shared with execution (spec §5.8). */
export type IssueFailureClass = "application" | "environment" | "session" | "model" | "case"

/** Durable issue ref kinds (spec §4.2.1). */
export type IssueRefKind =
  | "case_run"
  | "case_node"
  | "step_result"
  | "evidence"
  | "requirement"
  | "batch"
  | "commit"
  | "code_anchor"

/** One durable issue reference; both fields are strings (D29). */
export type IssueRef = {
  kind: IssueRefKind | string
  id: string
}

/** One code anchor of an issue (empty until codeintel fills it). */
export type IssueCodeRef = {
  anchorId: string
  path: string
  line: number
  kind: string
  label: string
}

/** Lightweight case summary of the issue detail. */
export type IssueCaseSummary = {
  caseRunId: string | null
  caseNodeId: string | null
}

/** `IssueSummaryView` (spec §7.3): the list item, without the long bodies. */
export type IssueSummary = {
  id: string
  versionId: string
  batchId: string | null
  caseRunId: string | null
  caseNodeId: string | null
  stepResultId: string | null
  source: IssueSource | string
  category: IssueCategory | string
  severity: IssueSeverity | string
  status: IssueStatus | string
  confidence: IssueConfidence | string
  failureClass: IssueFailureClass | string | null
  title: string
  rerunSuggestion: string
  recurrenceCount: number
  revision: number
  refs: IssueRef[]
  codeRefs: IssueCodeRef[]
  dismissedReason: string
  createdBy: string
  confirmedBy: string | null
  confirmedAt: string | null
  dismissedBy: string | null
  dismissedAt: string | null
  createdAt: string
  updatedAt: string
}

/** `IssueView` detail (spec §7.3), including the long bodies and summaries. */
export type IssueDetail = IssueSummary & {
  description: string
  reproSteps: string
  expected: string
  actual: string
  case: IssueCaseSummary | null
  requirement: { id: string } | null
  batch: { id: string } | null
}

/** Filters accepted by `GET /api/versions/{id}/issues` (spec §7.3). */
export type IssueListFilters = {
  status?: IssueStatus | ""
  severity?: IssueSeverity | ""
  category?: IssueCategory | ""
  batchId?: string
  caseRunId?: string
  caseNodeId?: string
  q?: string
  limit?: number
  cursor?: string
}

/** Body of `POST /api/versions/{id}/issues`; ids are decimal strings. */
export type CreateIssueBody = {
  title: string
  severity: IssueSeverity
  description?: string
  category?: IssueCategory
  confidence?: IssueConfidence
  caseRunId?: string
  caseNodeId?: string
  requirementId?: string
  expected?: string
  actual?: string
  reproSteps?: string
}

/** Body of `PATCH /api/versions/{id}/issues/{issueId}` (spec §7.3). */
export type PatchIssueBody = {
  status: "confirmed" | "dismissed"
  expectedRevision: number
  comment?: string
}

/** Deep link of the global issue centre (spec §7.10). */
export type IssueDeepLink = {
  versionId: string
  tab: string
  issueId: string
}

/** One row of `GET /api/issues` (spec §7.10). */
export type GlobalIssue = IssueSummary & {
  versionName: string
  deepLink: IssueDeepLink
}

/** Filters accepted by `GET /api/issues` (spec §7.10). */
export type GlobalIssueFilters = {
  versionId?: string
  status?: IssueStatus | ""
  severity?: IssueSeverity | ""
  category?: IssueCategory | ""
  batchId?: string
  caseRunId?: string
  caseNodeId?: string
  q?: string
  limit?: number
  cursor?: string
}

/** Report lifecycle (spec §5.5). */
export type ReportStatus = "draft" | "published" | "stale"

/** Markdown artifact summary; the stored filesystem path is never exposed. */
export type ReportMarkdown = {
  sha256: string
  sizeBytes: number
  downloadPath: string
}

/** `ReportSummaryView` (spec §7.4): the list item, without `content`. */
export type ReportSummary = {
  id: string
  versionId: string
  batchId: string
  revision: number
  status: ReportStatus | string
  title: string
  inputFingerprint: string
  markdown: ReportMarkdown
  staleReason: string
  publishedAt: string | null
  createdAt: string
  updatedAt: string
}

/** Version header of a report snapshot. */
export type ReportContentVersion = {
  id: string
  name: string
  label: string
  status: string
  codeBaselineCommit: string
}

/** Batch header of a report snapshot. */
export type ReportContentBatch = {
  id: string
  batchNo: number
  kind: string
  status: string
  baselineHash: string
  graphHash: string
  codeBaseCommit: string
  headCommit: string
  startedAt: string
  finishedAt: string
}

/** Batch tally of a report snapshot. */
export type ReportContentSummary = {
  caseCount: number
  passed: number
  failed: number
  blocked: number
  inconclusive: number
  skipped: number
  interrupted: number
  flakyCount: number
}

/** One latest case run of the report snapshot. */
export type ReportContentCase = {
  caseRunId: string
  caseNodeId: string
  title: string
  source: string
  status: string
  failureClass: string
  attempt: number
  stepCount: number
  assertionCount: number
  evidenceCount: number
}

/** One requirement coverage row of the report snapshot. */
export type ReportContentRequirement = {
  requirementId: string
  title: string
  status: string
  caseCount: number
  passed: number
  failed: number
}

/** One issue row of the report snapshot (classification only). */
export type ReportContentIssue = {
  issueId: string
  title: string
  severity: string
  status: string
  confidence: string
  category: string
  caseNodeId: string
  codeRefs: { path: string; line: number }[]
}

/** Context footer of the report snapshot. */
export type ReportContentAppendix = {
  environment: string
  system: string
  commit: string
  generatedAt: string
}

/** The deterministic `eztest.report.v1` snapshot carried by the detail DTO. */
export type ReportContent = {
  schema: string
  version: ReportContentVersion
  batch: ReportContentBatch
  summary: ReportContentSummary
  cases: ReportContentCase[]
  requirements: ReportContentRequirement[]
  issues: ReportContentIssue[]
  risks: string[]
  degraded: boolean
  appendix: ReportContentAppendix
}

/** `ReportView` detail (spec §7.4). */
export type ReportDetail = ReportSummary & {
  batchNo: number
  batchKind: string
  content: ReportContent
}

/** Filters accepted by `GET /api/versions/{id}/reports` (spec §7.4). */
export type ReportListFilters = {
  batchId?: string
  status?: ReportStatus | ""
  limit?: number
  cursor?: string
}

/** One batch option of the report batch selector (P6 batch DTO subset). */
export type ReportBatchSummary = {
  id: string
  batchNo: number
  status: string
  kind: string
  counts?: {
    total: number
    passed: number
    failed: number
    blocked: number
    inconclusive?: number
  }
}

/** One requirement/option row for the global version filter. */
export type VersionOption = {
  id: string
  name: string
  label: string
  status: string
}

/** Deep link of the global report centre (spec §7.10). */
export type ReportDeepLink = {
  versionId: string
  tab: string
  reportId: string
}

/** One row of `GET /api/reports` (spec §7.10). */
export type GlobalReport = ReportSummary & {
  versionName: string
  batchNo: number
  deepLink: ReportDeepLink
}

/** Filters accepted by `GET /api/reports` (spec §7.10). */
export type GlobalReportFilters = {
  versionId?: string
  status?: ReportStatus | ""
  limit?: number
  cursor?: string
}

/** Counts of the report-to-report diff (spec §7.4). */
export type ReportDiffSummary = {
  addedIssues: number
  fixedIssues: number
  regressed: number
  recovered: number
  newCases: number
  removedCases: number
  changed: number
}

/** One added or fixed issue of the diff. */
export type ReportDiffIssue = {
  issueId: string
  title: string
  severity: string
}

/** One regressed/recovered/changed case of the diff. */
export type ReportDiffCase = {
  caseNodeId: string
  title: string
  from: string
  to: string
}

/** One new or removed case of the diff. */
export type ReportDiffNewCase = {
  caseNodeId: string
  title: string
  source: string
}

/** `ReportDiffView` (spec §7.4). */
export type ReportDiff = {
  baseReportId: string
  targetReportId: string
  baseBatchNo: number
  targetBatchNo: number
  summary: ReportDiffSummary
  addedIssues: ReportDiffIssue[]
  fixedIssues: ReportDiffIssue[]
  regressedCases: ReportDiffCase[]
  recoveredCases: ReportDiffCase[]
  changedCases: ReportDiffCase[]
  newCases: ReportDiffNewCase[]
  removedCases: ReportDiffNewCase[]
}

/** A downloaded report plus the filename and digest advertised by the server. */
export type ReportDownload = {
  blob: Blob
  filename: string
  sha256: string
}

/**
 * A version stream the caller already owns (for example `useVersionStream` in
 * the version detail page). Delivery hooks subscribe to its `events` instead of
 * opening a second connection.
 */
export type DeliveryStream = {
  events: VersionEvent[]
  status?: SSEStatus
}

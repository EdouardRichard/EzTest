/**
 * DTOs for the P4 code-intelligence API (spec §7.2/§7.3).
 *
 * ID rules (spec §7.1): codeintel-owned IDs (analysis/indexRun/changeSet/entity/
 * anchor/candidate) and subject IDs are decimal strings, while `repoId`,
 * `createdBy` and `selectionUpdatedBy` stay numbers. Timestamps are ISO strings.
 */

/** One code evidence range; anchor-sourced evidence only carries `file`. */
export type Evidence = {
  file: string
  startLine?: number
  endLine?: number
  excerptHash?: string
}

/** Human fact-review verdicts of the P8 correction API (spec §7.2). */
export type FactReviewVerdict = "correct" | "incorrect"

/**
 * One human verdict on a code fact at a commit (spec §7.2). `id`/`factId` are
 * decimal strings (D29), `createdBy` stays a number and `reason` is the human
 * note. A fact is reviewed at most once per commit; a new verdict overwrites the
 * previous row.
 */
export type FactReview = {
  id: string
  factId: string
  commit: string
  verdict: FactReviewVerdict
  reason: string
  createdBy: number
  createdAt: string
}

/** Request body of `POST /api/code/repos/{repoId}/facts/{factId}/reviews`. */
export type CreateFactReviewBody = {
  verdict: FactReviewVerdict
  reason?: string
}

/** Evidence-verification projection of the repository quality block. */
export type QualityEvidence = {
  checks: number
  failures: number
  /** (checks-failures)/checks, or null when no evidence was checked. */
  passRate: number | null
}

/** Confidence distribution of the AI facts at the quality head commit. */
export type QualityConfidence = {
  high: number
  medium: number
  low: number
  total: number
}

/** Human review counters of one repository. */
export type QualityReviews = {
  total: number
  correct: number
  incorrect: number
}

/**
 * Newest evaluation run's impact metrics. When the evaluation is unavailable
 * (or has never run) precision/recall are null, `available` is false and
 * `reason` names the degraded source (`no_eval_run` | `no_impact_metric` |
 * `eval_store_unavailable`).
 */
export type QualityImpact = {
  precision: number | null
  recall: number | null
  available: boolean
  reason?: string
  /** Evaluation run id, a decimal string (D29). */
  evalRunId: string | null
  evaluatedAt: string | null
}

/** Newest done analysis that supplied the quality counters. */
export type QualityAnalysisRef = {
  id: string
  headCommit: string
  analyzedAt: string
}

/**
 * `GET /api/code/repos/{repoId}/quality` (spec §9.2): evidence pass rate,
 * invalid facts, confidence distribution, human review counters, the newest
 * evaluation's impact precision/recall and the cache hit rate. Missing sources
 * stay null/zero and are flagged; `analysis` is null for a never-analysed
 * repository.
 */
export type RepoQuality = {
  repoId: number
  generatedAt: string
  evidence: QualityEvidence
  invalidFacts: number
  confidence: QualityConfidence
  reviews: QualityReviews
  impact: QualityImpact
  cacheHitRate: number | null
  analysis: QualityAnalysisRef | null
  indexedCommit: string
}

/** A file-level change inside a persisted change set (spec §7.2). */
export type ChangedFile = {
  path: string
  oldPath: string | null
  status: string
  additions: number
  deletions: number
  binary: boolean
  headBlobHash: string
}

/** Aggregate diff counters for a change set. */
export type ChangeSetStats = {
  files: number
  additions: number
  deletions: number
  truncated: boolean
}

/**
 * One changed entity summary inside a batch snapshot item (spec §4.2: route |
 * api | component).
 */
export type VersionChangeSubject = {
  kind: string
  name: string
}

/**
 * One persisted summary of a changed file inside a version_change snapshot.
 * It never carries code content or a diff; `unmapped` is the P4 INV-8 verdict.
 */
export type VersionChangeItem = {
  file: string
  changeKind: string
  subjects: VersionChangeSubject[]
  /** Case node ids, decimal strings that are never re-parsed (spec §7.1). */
  caseIds: string[]
  unmapped: boolean
}

/**
 * One batchTimeline entry of `GET /api/versions/{id}/code/changes` (P8 D58): the
 * batch identity, its commit range, counters and summary items. `batchId` is a
 * decimal string (D29).
 */
export type VersionChangeBatch = {
  batchId: string
  batchNo: number
  batchStatus: string
  baseCommit: string
  headCommit: string
  historyRewritten: boolean
  changedFiles: number
  impactedCases: number
  unmappedCount: number
  items: VersionChangeItem[]
  computedAt: string
  /**
   * Additive P8 degraded flag: the batch head has no analysis mapping data yet,
   * so the snapshot is computed provisionally on every read and never reflects
   * a durable unmapped verdict. The UI says「映射生成中」and suppresses the
   * pre-analysis unmapped badges. Older backends omit the field (false).
   */
  provisional?: boolean
  /**
   * Additive P8 degraded flag: this batch's deterministic diff could not be
   * computed. The rest of the timeline still renders; the batch says
   *「本批变更暂不可用」instead of pretending it has no changes.
   */
  unavailable?: boolean
}

/**
 * `/api/code/repos/{repoId}/diff` and `/api/versions/{id}/code/changes` share
 * this DTO (spec §7.2). `changeSetId` is null for a version preview that has
 * not persisted a change set yet (spec §7.3). `batchTimeline` is additive in
 * P8: an older backend omits it and the tab renders the unchanged change list.
 */
export type ChangeList = {
  changeSetId: string | null
  baseCommit: string
  headCommit: string
  stats: ChangeSetStats
  files: ChangedFile[]
  nextCursor: string | null
  hasMore: boolean
  batchTimeline?: VersionChangeBatch[]
}

/** Counters produced by one T0 index run. */
export type IndexRunStats = {
  files: number
  truncated: boolean
}

/** A T0 index run; `runId` is a decimal string (spec §7.1). */
export type IndexRun = {
  runId: string
  repoId?: number
  commit: string
  source?: string
  status: string
  stats?: IndexRunStats | null
  errors?: AnalysisError[]
  startedAt: string | null
  finishedAt: string | null
  createdBy?: number
  createdAt?: string
}

/** Accepted (202) body of `POST /api/code/repos/{repoId}/index`. */
export type IndexRunAccepted = Pick<IndexRun, "runId" | "status" | "commit">

/** Repository plus its latest index-run projection (spec §7.2). */
export type RepoSummary = {
  id: number
  key: string
  name: string
  status: string
  fixedBranch: string
  lastCommit: string
  lastIndexedAt: string | null
  index: IndexRun | null
}

/** One entry of a repository tree page (spec §7.2). */
export type TreeEntry = {
  path: string
  type: "blob" | "tree"
  size: number
  gitObjectId: string
  mode: string
  unsupported: boolean
}

/** One cursor page of tree entries. */
export type TreePage = {
  entries: TreeEntry[]
  nextCursor: string
  hasMore: boolean
}

/** A 1-based line window of one file at a commit (spec §7.2). */
export type FileWindow = {
  commit: string
  path: string
  startLine: number
  endLine: number
  totalLines: number
  truncated: boolean
  content: string
}

/** Unified diff of a single file (spec §7.2). */
export type FileDiff = {
  baseCommit: string
  headCommit: string
  path: string
  status: string
  binary: boolean
  truncated: boolean
  patch: string
}

/** A changed file that produced no requirement or app-model candidate. */
export type UnmappedChange = {
  path: string
  status: string
  reason: string
  suggestion: string
}

/** One non-fatal analysis error. */
export type AnalysisError = {
  code: string
  path?: string
  message: string
}

/** The fact version snapshot recorded by an analysis (spec §7.3). */
export type FactVersionRef = {
  factKey: string
  version: number
}

/** Analysis counters reported by the backend. */
export type AnalysisStats = {
  changedFiles: number
  analyzedFiles: number
  cacheHits: number
  facts: number
  edges: number
  contracts: number
  evidenceChecks: number
  evidenceFailures: number
  unmapped: number
  invalidFacts: number
  duplicateFacts: number
  unmatchedContracts: number
  truncated: boolean
  /** True when the server truncated the unmapped list at 200 entries. */
  unmappedTruncated?: boolean
  skipped?: number
}

/** Subject kinds accepted by candidate/impact DTOs (spec §7.3). */
export type CandidateSubjectKind = "requirement" | "app_model"

/** One impact candidate; `subjectId` stays a decimal string (spec §7.1). */
export type Candidate = {
  id: string
  subjectKind: CandidateSubjectKind
  subjectId: string
  reason: string
  confidence: string
  source: string
  evidence: Evidence[]
  selected: boolean
  selectionUpdatedBy?: number | null
  selectionUpdatedAt?: string | null
}

/** Subject kinds accepted by anchors; `case`/`issue` are rejected in P4. */
export type AnchorSubjectKind = "version" | "requirement" | "app_model"

/** A code anchor DTO (spec §7.3). */
export type Anchor = {
  id: string
  versionId: string
  subjectKind: AnchorSubjectKind
  subjectId: string
  filePath: string
  startLine?: number | null
  endLine?: number | null
  commitSha?: string | null
  confidence: string
  createdByKind: string
  createdBy: number
  createdAt: string
}

/** A code-impact analysis with its candidates/unmapped projection (spec §7.3). */
export type Analysis = {
  id: string
  versionId: string
  repoId: number
  baseCommit: string
  headCommit: string
  status: string
  degraded: boolean
  analysisVersion: number
  model: string
  promptVersion: string
  changeSetId: string | null
  changeSummary: string
  stats: AnalysisStats | null
  factVersions: FactVersionRef[]
  candidates: Candidate[]
  unmapped: UnmappedChange[]
  errors: AnalysisError[]
  createdBy?: number
  createdAt: string
  startedAt: string | null
  finishedAt: string | null
}

/** A persisted code entity/fact entry returned by the facts/search reads. */
export type CodeEntity = {
  id: string
  repoId: number
  commit: string
  kind: string
  path: string
  name: string
  startLine?: number | null
  endLine?: number | null
  signature: string
  blobHash: string
  source: string
  confidence: string
  evidence: Evidence[]
  factKey?: string | null
  factVersion?: number | null
  analysisId?: string | null
  /** Additive P8 human verdict at this commit; null/absent when unreviewed. */
  review?: FactReview | null
  createdAt?: string
  updatedAt?: string
}

/** Query for `GET /api/code/repos/{repoId}/tree` (spec §7.2). */
export type TreeQuery = {
  commit?: string
  path?: string
  cursor?: string
  limit?: number
}

/** Query for `GET /api/code/repos/{repoId}/file` (spec §7.2). */
export type FileQuery = {
  commit?: string
  path: string
  startLine?: number
  endLine?: number
}

/** Query for `GET /api/code/repos/{repoId}/diff` (spec §7.2). */
export type DiffQuery = {
  base: string
  head: string
  cursor?: string
  limit?: number
}

/** Query for `GET /api/code/repos/{repoId}/diff/file` (spec §7.2). */
export type FileDiffQuery = {
  base: string
  head: string
  path: string
}

/** Query for `GET /api/code/repos/{repoId}/facts` (spec §7.2). */
export type FactsQuery = {
  commit?: string
  kind?: string
  path?: string
  cursor?: string
  limit?: number
}

/** Query for `GET /api/code/repos/{repoId}/search` (spec §7.2). */
export type SearchQuery = {
  commit?: string
  q: string
  kind?: string
  cursor?: string
  limit?: number
}

/** Query for `GET /api/versions/{id}/code/anchors` (spec §7.3). */
export type AnchorQuery = {
  subjectKind?: AnchorSubjectKind
  subjectId?: string
}

/** Request body of `POST /api/versions/{id}/code/analyses` (spec §7.3). */
export type StartAnalysisBody = {
  headCommit?: string
  force?: boolean
}

/** New-or-reused analysis returned by `POST /api/versions/{id}/code/analyses`. */
export type StartAnalysisResult = {
  id: string
  status: string
  analysisVersion: number
  reused: boolean
}

/** Request body of `POST /api/versions/{id}/code/anchors` (spec §7.3). */
export type CreateAnchorBody = {
  subjectKind: AnchorSubjectKind
  subjectId: string
  filePath: string
  startLine?: number
  endLine?: number
}

/**
 * Wire types for the P5 code & changes tab (spec §4.2, §5, §9.2).
 *
 * Field names and enum values are pinned by the frozen P5 OpenAPI contract
 * (`0.2.0-p5`). IDs are decimal strings everywhere, timestamps are RFC3339 and
 * list endpoints use the `{data, next_cursor, has_more}` envelope. No code body
 * is ever part of these types except the explicitly audited `CodeFileWindowView`.
 */

import type { CodeRepositoryResource } from "@/features/versions/types"

/* ---------------------------------- index ---------------------------------- */

export type CodeIndexStatus = "queued" | "running" | "done" | "failed"

/** `CodeIndexRunView`: T0 indexing run summary. */
export type CodeIndexRunView = {
  id: string
  version_id?: string | null
  repo_id: string
  commit_sha: string
  branch: string
  source: "git"
  status: CodeIndexStatus
  files_total: number
  files_indexed: number
  truncated: boolean
  stats: Record<string, unknown>
  errors: Array<Record<string, unknown>>
  started_at?: string | null
  finished_at?: string | null
  created_by?: string | null
  created_at: string
  updated_at: string
}

/** `CodeRepoStatusView`: bound repository + latest index + resolved baseline. */
export type CodeRepoStatusView = {
  repo_id: string
  repository: CodeRepositoryResource
  latest_index: CodeIndexRunView | null
  baseline_commit: string | null
  index_in_progress: boolean
}

/* ----------------------------------- tree ---------------------------------- */

export type CodeTreeEntryView = {
  path: string
  name: string
  kind: "file" | "directory"
  size_bytes?: number | null
  blob_hash?: string | null
}

/** `CodeFileWindowView`: the only response that carries code text. */
export type CodeFileWindowView = {
  repo_id: string
  commit_sha: string
  path: string
  start_line: number
  end_line: number
  total_lines: number
  truncated: boolean
  binary: boolean
  content: string
  sha256: string
}

/* ---------------------------------- diff ----------------------------------- */

export type CodeChangedFileStatus = "added" | "modified" | "deleted" | "renamed"

/** `CodeChangedFileView`: change-set row without patch text. */
export type CodeChangedFileView = {
  path: string
  old_path?: string | null
  status: CodeChangedFileStatus
  additions: number
  deletions: number
  binary: boolean
  head_blob_hash?: string | null
}

/** `CodeFileDiffView`: one on-demand patch (≤256 KiB) with reduce-content flags. */
export type CodeFileDiffView = {
  path: string
  old_path?: string | null
  status: CodeChangedFileStatus
  additions: number
  deletions: number
  binary: boolean
  large: boolean
  deleted: boolean
  patch?: string | null
}

/** `CodeChangeSetView`: preview-only change set (never persisted by the API). */
export type CodeChangeSetView = {
  repo_id: string
  base_commit_sha: string
  head_commit_sha: string
  files: CodeChangedFileView[]
  stats: Record<string, unknown>
  truncated: boolean
}

/* --------------------------------- entities -------------------------------- */

export type CodeFactKind =
  | "file"
  | "module"
  | "route"
  | "api"
  | "client_call"
  | "component"
  | "page"
  | "symbol"

export type CodeConfidence = "low" | "medium" | "high"

export type CodeSource = "git" | "ai"

/** `CodeFactView`: immutable T1 fact projection used by the facts panel. */
export type CodeFactView = {
  id: string
  repo_id: string
  commit_sha: string
  fact_key: string
  version: number
  kind: CodeFactKind
  path: string
  name?: string
  signature?: string
  start_line?: number | null
  end_line?: number | null
  body?: Record<string, unknown>
  evidence?: Array<Record<string, unknown>>
  source: CodeSource
  confidence: CodeConfidence
  model?: string
  prompt_version?: string
  analysis_id?: string | null
  superseded_at?: string | null
  created_at: string
}

/** `CodeEntityView`: search/tree entity projection. */
export type CodeEntityView = {
  id: string
  repo_id: string
  commit_sha: string
  kind: CodeFactKind
  path: string
  name?: string
  signature?: string
  start_line?: number | null
  end_line?: number | null
  source: CodeSource
  confidence: CodeConfidence
}

/** `CodeCommitView`: commit metadata (never file bodies). */
export type CodeCommitView = {
  sha: string
  branch?: string | null
  message: string
  author_name?: string | null
  authored_at: string
  committed_at: string
  parents: string[]
  is_head: boolean
}

/* --------------------------------- analysis -------------------------------- */

export type CodeAnalysisStatus = "queued" | "running" | "done" | "failed"

/** `CodeAnalysisView`: T1 analysis run summary. */
export type CodeAnalysisView = {
  id: string
  version_id: string
  repo_id: string
  base_commit_sha: string
  head_commit_sha: string
  status: CodeAnalysisStatus
  degraded: boolean
  model?: string
  prompt_version?: string
  analysis_version?: number
  stats: Record<string, unknown>
  changed_files_count?: number
  candidates_count?: number
  selected_count?: number
  unmapped_count?: number
  errors_count?: number
  error?: string | null
  created_at: string
  started_at?: string | null
  finished_at?: string | null
}

export type CodeAnalysisUnmapped = {
  path: string
  reason: string
}

export type CodeImpactSubjectKind =
  | "requirement"
  | "requirement_version"
  | "case_version"
  | "sut_page"
  | "sut_api"

export type CodeImpactSource = "direct" | "contract" | "relation" | "ai"

/** `CodeImpactCandidateView`: deterministic or AI impact candidate. */
export type CodeImpactCandidateView = {
  id: string
  analysis_id: string
  repo_id?: string | null
  version_sut_id?: string | null
  subject_kind: CodeImpactSubjectKind
  subject_id: string
  reason: string
  confidence: CodeConfidence
  source: CodeImpactSource
  evidence?: Array<Record<string, unknown>>
  selected: boolean
  selection_updated_by?: string | null
  selection_updated_at?: string | null
  created_at: string
}

/** `CodeAnalysisDetailView`: analysis plus changed files, unmapped and candidates. */
export type CodeAnalysisDetailView = {
  analysis: CodeAnalysisView
  changed_files: CodeChangedFileView[]
  unmapped: CodeAnalysisUnmapped[]
  candidates: CodeImpactCandidateView[]
  fact_versions?: Record<string, number>
  errors?: Array<Record<string, unknown>>
}

export type CodeImpactAnalysisRef = {
  repo_id: string
  analysis_id: string
  base_commit_sha: string
  head_commit_sha: string
  status: CodeAnalysisStatus
  degraded: boolean
  candidates_count?: number
}

/** `CodeImpactView`: merged multi-repository impact projection. */
export type CodeImpactView = {
  version_id: string
  analyses: CodeImpactAnalysisRef[]
  candidates: CodeImpactCandidateView[]
}

/* ---------------------------------- anchors -------------------------------- */

export type CodeAnchorSubjectKind =
  | "version"
  | "requirement"
  | "requirement_version"
  | "case_version"
  | "issue"

export type CodeAnchorStatus = "valid" | "stale" | "broken"

export type CodeAnchorCreatorKind = "user" | "ai" | "system"

/** `CodeAnchorView`: requirement/case/issue/version → file:line anchor. */
export type CodeAnchorView = {
  id: string
  version_id: string
  repo_id: string
  subject_kind: CodeAnchorSubjectKind
  subject_id: string
  file_path: string
  start_line?: number | null
  end_line?: number | null
  entity_id?: string | null
  commit_sha?: string | null
  excerpt_sha?: string | null
  status: CodeAnchorStatus
  confidence: CodeConfidence
  created_by_kind: CodeAnchorCreatorKind
  created_by?: string | null
  last_validated_at?: string | null
  created_at: string
  updated_at: string
}

/* -------------------------------- fact review ------------------------------ */

export type CodeFactReviewVerdict = "correct" | "incorrect"

/** `CodeFactReviewView`: append-only fact review record. */
export type CodeFactReviewView = {
  id: string
  fact_id: string
  fact_version: number
  repo_id: string
  commit_sha: string
  verdict: CodeFactReviewVerdict
  reason: string
  reviewer_id: string
  created_at: string
}

/* --------------------------------- proposals ------------------------------- */

export type CodeProposalKind = "page" | "api"

/** `CodeProposalView`: proposed page/API generated from code facts. */
export type CodeProposalView = {
  kind: CodeProposalKind
  id: string
  name: string
  url?: string | null
  route_template?: string | null
  method?: string | null
  path_template?: string | null
  status: "proposed"
  source: "ai" | "code"
  evidence?: Array<Record<string, unknown>>
  created_at: string
}

export type CodeProposalCounts = {
  created: number
  skipped: number
  ids: string[]
}

export type CodeProposalResultView = {
  pages: CodeProposalCounts
  apis: CodeProposalCounts
}

/* --------------------------------- requests -------------------------------- */

export type CodeIndexInput = {
  repoId: string
  commit?: string
  branch?: string
}

export type CodeAnalysisInput = {
  repoId: string
  headCommit?: string
  force?: boolean
}

export type CreateCodeAnchorInput = {
  subjectKind: CodeAnchorSubjectKind
  subjectId: string
  repoId: string
  filePath: string
  startLine?: number
  endLine?: number
  excerptSha?: string
}

export type CreateCodeFactReviewInput = {
  verdict: CodeFactReviewVerdict
  reason?: string
}

export type CodeProposalInput = {
  repoId?: string
}

export type CodeTreeQuery = {
  commit: string
  path?: string
  cursor?: string
  limit?: number
}

export type CodeFileQuery = {
  commit: string
  path: string
  startLine?: number
  endLine?: number
}

export type CodeDiffQuery = {
  base: string
  head: string
  cursor?: string
  limit?: number
}

export type CodeFileDiffQuery = {
  base: string
  head: string
  path: string
}

export type CodeFactListQuery = {
  commit: string
  kind?: CodeFactKind
  path?: string
  cursor?: string
  limit?: number
}

export type CodeSearchQuery = {
  commit: string
  q: string
  kind?: CodeFactKind
  cursor?: string
  limit?: number
}

export type CodeChangesQuery = {
  repoId: string
  head?: string
}

export type CodeAnchorFilters = {
  subjectKind?: CodeAnchorSubjectKind
  subjectId?: string
}

/* ------------------------------- UI metadata ------------------------------- */

export const CODE_INDEX_STATUS_LABELS: Record<CodeIndexStatus, string> = {
  queued: "排队中",
  running: "运行中",
  done: "已完成",
  failed: "失败",
}

export const CODE_ANALYSIS_STATUS_LABELS: Record<CodeAnalysisStatus, string> = {
  queued: "排队中",
  running: "运行中",
  done: "已完成",
  failed: "失败",
}

export const CODE_CHANGED_STATUS_LABELS: Record<CodeChangedFileStatus, string> = {
  added: "新增",
  modified: "修改",
  deleted: "删除",
  renamed: "重命名",
}

export const CODE_CHANGED_STATUS_LETTERS: Record<CodeChangedFileStatus, string> = {
  added: "A",
  modified: "M",
  deleted: "D",
  renamed: "R",
}

export const CODE_FACT_KIND_LABELS: Record<CodeFactKind, string> = {
  file: "文件",
  module: "模块",
  route: "路由",
  api: "API",
  client_call: "客户端调用",
  component: "组件",
  page: "页面",
  symbol: "符号",
}

export const CODE_FACT_KIND_ORDER: readonly CodeFactKind[] = [
  "file",
  "route",
  "page",
  "api",
  "client_call",
  "component",
  "symbol",
  "module",
]

/** Low confidence is surfaced as a clue, never as a confirmed fact (spec §5.2). */
export const CODE_CONFIDENCE_LABELS: Record<CodeConfidence, string> = {
  low: "线索",
  medium: "中置信",
  high: "高置信",
}

export const CODE_CONFIDENCE_ORDER: Record<CodeConfidence, number> = {
  high: 0,
  medium: 1,
  low: 2,
}

export const CODE_IMPACT_SUBJECT_KIND_LABELS: Record<CodeImpactSubjectKind, string> = {
  requirement: "需求",
  requirement_version: "需求版本",
  case_version: "用例版本",
  sut_page: "页面",
  sut_api: "API",
}

export const CODE_IMPACT_SUBJECT_KIND_ORDER: readonly CodeImpactSubjectKind[] = [
  "requirement",
  "requirement_version",
  "case_version",
  "sut_page",
  "sut_api",
]

export const CODE_IMPACT_SOURCE_LABELS: Record<CodeImpactSource, string> = {
  direct: "直接",
  contract: "契约",
  relation: "关系",
  ai: "AI 推理",
}

export const CODE_ANCHOR_STATUS_LABELS: Record<CodeAnchorStatus, string> = {
  valid: "有效",
  stale: "内容已变",
  broken: "失效",
}

export const CODE_ANCHOR_SUBJECT_LABELS: Record<CodeAnchorSubjectKind, string> = {
  version: "版本",
  requirement: "需求",
  requirement_version: "需求版本",
  case_version: "用例版本",
  issue: "问题",
}

export const CODE_UNMAPPED_REASON_LABELS: Record<string, string> = {
  deleted_file: "文件已删除",
  unsupported_content: "内容不支持分析",
  no_fact_or_anchor: "无事实或锚点",
  ai_unavailable: "AI 不可用",
  unmatched_contract: "契约未匹配",
}

export const CODE_REVIEW_VERDICT_LABELS: Record<CodeFactReviewVerdict, string> = {
  correct: "正确",
  incorrect: "错误",
}

export const CODE_PROPOSAL_KIND_LABELS: Record<CodeProposalKind, string> = {
  page: "页面",
  api: "API",
}

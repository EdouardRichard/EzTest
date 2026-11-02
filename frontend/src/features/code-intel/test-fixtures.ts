/**
 * Fixtures for the P5 code-intelligence tab tests. Shapes mirror the frozen
 * P5 OpenAPI schemas (decimal-string ids, RFC3339 timestamps, 40-hex commits).
 */

import type { CodeRepositoryResource } from "@/features/versions/types"

import type {
  CodeAnalysisDetailView,
  CodeAnalysisView,
  CodeAnchorView,
  CodeChangedFileView,
  CodeChangeSetView,
  CodeFactReviewView,
  CodeFactView,
  CodeFileDiffView,
  CodeFileWindowView,
  CodeImpactCandidateView,
  CodeIndexRunView,
  CodeProposalView,
  CodeRepoStatusView,
} from "./types"

const CREATED = "2026-11-01T02:00:00Z"
const UPDATED = "2026-11-01T03:00:00Z"

export const BASE_COMMIT = "a".repeat(40)
export const HEAD_COMMIT = "b".repeat(40)
export const INDEXED_COMMIT = "c".repeat(40)

export const repositoryFixture: CodeRepositoryResource = {
  id: "1201",
  name: "order-center-web",
  path: "D:\\fixtures\\p5\\repo",
  branch: "main",
  include: ["src/**"],
  exclude: ["node_modules/**"],
  created_by: "1001",
  status: "active",
  created_at: CREATED,
  updated_at: UPDATED,
}

export function indexRunFixture(overrides: Partial<CodeIndexRunView> = {}): CodeIndexRunView {
  return {
    id: "5001",
    version_id: "2001",
    repo_id: "1201",
    commit_sha: INDEXED_COMMIT,
    branch: "main",
    source: "git",
    status: "done",
    files_total: 2,
    files_indexed: 2,
    truncated: false,
    stats: {},
    errors: [],
    started_at: CREATED,
    finished_at: UPDATED,
    created_by: "1001",
    created_at: CREATED,
    updated_at: UPDATED,
    ...overrides,
  }
}

export function repoStatusFixture(overrides: Partial<CodeRepoStatusView> = {}): CodeRepoStatusView {
  return {
    repo_id: "1201",
    repository: repositoryFixture,
    latest_index: indexRunFixture(),
    baseline_commit: BASE_COMMIT,
    index_in_progress: false,
    ...overrides,
  }
}

export function analysisViewFixture(overrides: Partial<CodeAnalysisView> = {}): CodeAnalysisView {
  return {
    id: "7001",
    version_id: "2001",
    repo_id: "1201",
    base_commit_sha: BASE_COMMIT,
    head_commit_sha: HEAD_COMMIT,
    status: "done",
    degraded: false,
    model: "deepseek-chat",
    prompt_version: "p5.1",
    analysis_version: 1,
    stats: {},
    changed_files_count: 3,
    candidates_count: 2,
    selected_count: 1,
    unmapped_count: 1,
    errors_count: 0,
    error: null,
    created_at: CREATED,
    started_at: CREATED,
    finished_at: UPDATED,
    ...overrides,
  }
}

export const changedFilesFixture: CodeChangedFileView[] = [
  {
    path: "src/pages/RefundPage.tsx",
    old_path: null,
    status: "added",
    additions: 42,
    deletions: 0,
    binary: false,
    head_blob_hash: "d".repeat(40),
  },
  {
    path: "src/api/refund.ts",
    old_path: null,
    status: "modified",
    additions: 8,
    deletions: 3,
    binary: false,
    head_blob_hash: null,
  },
  {
    path: "public/refund.png",
    old_path: null,
    status: "modified",
    additions: 0,
    deletions: 0,
    binary: true,
    head_blob_hash: null,
  },
]

export const candidateRequirementFixture: CodeImpactCandidateView = {
  id: "8101",
  analysis_id: "7001",
  repo_id: "1201",
  version_sut_id: "3001",
  subject_kind: "requirement",
  subject_id: "6001",
  reason: "需求描述了变更的退款流程",
  confidence: "high",
  source: "direct",
  evidence: [{ path: "src/pages/RefundPage.tsx" }],
  selected: false,
  selection_updated_by: null,
  selection_updated_at: null,
  created_at: CREATED,
}

export const candidateCaseFixture: CodeImpactCandidateView = {
  id: "8102",
  analysis_id: "7001",
  repo_id: "1201",
  version_sut_id: "3001",
  subject_kind: "case_version",
  subject_id: "7002",
  reason: "关联需求的已确认用例",
  confidence: "medium",
  source: "relation",
  evidence: [],
  selected: true,
  selection_updated_by: "1001",
  selection_updated_at: UPDATED,
  created_at: CREATED,
}

export const candidatesFixture: CodeImpactCandidateView[] = [
  candidateRequirementFixture,
  candidateCaseFixture,
]

export function analysisDetailFixture(
  overrides: Partial<CodeAnalysisDetailView> = {},
): CodeAnalysisDetailView {
  return {
    analysis: analysisViewFixture(),
    changed_files: changedFilesFixture,
    unmapped: [{ path: "src/legacy.js", reason: "unsupported_content" }],
    candidates: candidatesFixture,
    fact_versions: { "route:/refund": 1 },
    errors: [],
    ...overrides,
  }
}

export const factsFixture: CodeFactView[] = [
  {
    id: "9101",
    repo_id: "1201",
    commit_sha: HEAD_COMMIT,
    fact_key: "route:/refund",
    version: 1,
    kind: "route",
    path: "src/pages/RefundPage.tsx",
    name: "/refund",
    start_line: 12,
    end_line: 16,
    body: { route: "/refund" },
    evidence: [{ file: "src/pages/RefundPage.tsx", startLine: 12, endLine: 16 }],
    source: "ai",
    confidence: "high",
    model: "deepseek-chat",
    prompt_version: "p5.1",
    analysis_id: "7001",
    superseded_at: null,
    created_at: CREATED,
  },
  {
    id: "9102",
    repo_id: "1201",
    commit_sha: HEAD_COMMIT,
    fact_key: "client:POST /api/refunds",
    version: 1,
    kind: "client_call",
    path: "src/api/refund.ts",
    name: "POST /api/refunds",
    start_line: 3,
    end_line: 4,
    body: { meta: { clue: true } },
    evidence: [],
    source: "ai",
    confidence: "low",
    model: "deepseek-chat",
    prompt_version: "p5.1",
    analysis_id: "7001",
    superseded_at: null,
    created_at: CREATED,
  },
]

export const fileWindowFixture: CodeFileWindowView = {
  repo_id: "1201",
  commit_sha: HEAD_COMMIT,
  path: "src/pages/RefundPage.tsx",
  start_line: 10,
  end_line: 14,
  total_lines: 20,
  truncated: false,
  binary: false,
  content: "line ten\nline eleven\nline twelve\nline thirteen\nline fourteen",
  sha256: "f".repeat(64),
}

export function diffFixture(overrides: Partial<CodeFileDiffView> = {}): CodeFileDiffView {
  return {
    path: "src/pages/RefundPage.tsx",
    old_path: null,
    status: "added",
    additions: 42,
    deletions: 0,
    binary: false,
    large: false,
    deleted: false,
    patch: "@@ -0,0 +1,2 @@\n+const a = 1\n+const b = 2",
    ...overrides,
  }
}

export const changeSetFixture: CodeChangeSetView = {
  repo_id: "1201",
  base_commit_sha: BASE_COMMIT,
  head_commit_sha: HEAD_COMMIT,
  files: changedFilesFixture,
  stats: {},
  truncated: false,
}

export const anchorFixture: CodeAnchorView = {
  id: "8201",
  version_id: "2001",
  repo_id: "1201",
  subject_kind: "requirement",
  subject_id: "6001",
  file_path: "src/pages/RefundPage.tsx",
  start_line: 12,
  end_line: 16,
  entity_id: null,
  commit_sha: HEAD_COMMIT,
  excerpt_sha: `sha256:${"e".repeat(64)}`,
  status: "valid",
  confidence: "high",
  created_by_kind: "ai",
  created_by: "1001",
  last_validated_at: UPDATED,
  created_at: CREATED,
  updated_at: UPDATED,
}

export const staleAnchorFixture: CodeAnchorView = {
  ...anchorFixture,
  id: "8202",
  subject_kind: "case_version",
  subject_id: "7002",
  file_path: "src/api/refund.ts",
  start_line: 3,
  end_line: 4,
  status: "stale",
  confidence: "medium",
}

export const brokenAnchorFixture: CodeAnchorView = {
  ...anchorFixture,
  id: "8203",
  subject_kind: "issue",
  subject_id: "9101",
  file_path: "src/pages/RemovedPage.tsx",
  start_line: null,
  end_line: null,
  status: "broken",
}

export const anchorsFixture: CodeAnchorView[] = [
  anchorFixture,
  staleAnchorFixture,
  brokenAnchorFixture,
]

export const reviewFixture: CodeFactReviewView = {
  id: "9201",
  fact_id: "9101",
  fact_version: 1,
  repo_id: "1201",
  commit_sha: HEAD_COMMIT,
  verdict: "correct",
  reason: "行号准确",
  reviewer_id: "1001",
  created_at: UPDATED,
}

export const proposalPageFixture: CodeProposalView = {
  kind: "page",
  id: "3001",
  name: "退款页",
  url: "https://order.test/refund",
  route_template: "/refund",
  method: null,
  path_template: null,
  status: "proposed",
  source: "code",
  evidence: [{ path: "src/pages/RefundPage.tsx" }],
  created_at: CREATED,
}

export const proposalApiFixture: CodeProposalView = {
  kind: "api",
  id: "3101",
  name: "退款申请",
  url: null,
  route_template: null,
  method: "POST",
  path_template: "/api/refunds",
  status: "proposed",
  source: "ai",
  evidence: [{ path: "src/api/refund.ts" }],
  created_at: CREATED,
}

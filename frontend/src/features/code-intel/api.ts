import {
  ApiError,
  type ApiPage,
  apiDelete,
  apiGet,
  apiGetPage,
  apiPatch,
  apiPost,
} from "@/lib/api-client"

import type {
  CodeAnalysisDetailView,
  CodeAnalysisInput,
  CodeAnalysisView,
  CodeAnchorFilters,
  CodeAnchorView,
  CodeChangedFileView,
  CodeChangeSetView,
  CodeChangesQuery,
  CodeCommitView,
  CodeDiffQuery,
  CodeEntityView,
  CodeFactListQuery,
  CodeFactReviewView,
  CodeFactView,
  CodeFileDiffQuery,
  CodeFileDiffView,
  CodeFileQuery,
  CodeFileWindowView,
  CodeImpactCandidateView,
  CodeImpactView,
  CodeIndexInput,
  CodeIndexRunView,
  CodeProposalInput,
  CodeProposalResultView,
  CodeProposalView,
  CodeRepoStatusView,
  CodeSearchQuery,
  CodeTreeEntryView,
  CodeTreeQuery,
  CreateCodeAnchorInput,
  CreateCodeFactReviewInput,
} from "./types"

/** Builds `?a=1&b=2`, skipping empty values (P0/P1 pagination style). */
function query(params: Record<string, string | number | boolean | undefined>): string {
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

function repoPath(repoId: string): string {
  return `/api/code/repos/${encodeURIComponent(repoId)}`
}

function isNotFound(cause: unknown): boolean {
  return cause instanceof ApiError && cause.status === 404
}

/* ------------------------------ repositories ------------------------------- */

/** `GET /api/versions/{id}/code/repos`: bound repos + latest index + baseline. */
export async function listCodeRepos(versionId: string): Promise<CodeRepoStatusView[]> {
  const page = await apiGetPage<CodeRepoStatusView>(`${versionPath(versionId)}/code/repos`)
  return page.items
}

/** `POST /api/versions/{id}/code/index`: 202 queued, 200 reused, 409 in progress. */
export function triggerIndex(versionId: string, input: CodeIndexInput): Promise<CodeIndexRunView> {
  const body: { repoId: string; commit?: string; branch?: string } = { repoId: input.repoId }
  if (input.commit !== undefined && input.commit.length > 0) body.commit = input.commit
  if (input.branch !== undefined && input.branch.length > 0) body.branch = input.branch
  return apiPost<CodeIndexRunView>(`${versionPath(versionId)}/code/index`, body)
}

/** `GET /api/code/repos/{id}/index-runs/latest`; 404 means the repo was never indexed. */
export async function latestIndexRun(repoId: string): Promise<CodeIndexRunView | null> {
  try {
    return await apiGet<CodeIndexRunView>(`${repoPath(repoId)}/index-runs/latest`)
  } catch (cause) {
    if (isNotFound(cause)) return null
    throw cause
  }
}

/** `GET /api/code/repos/{id}/index-runs/{runId}`: one index run by decimal-string id. */
export function getIndexRun(repoId: string, runId: string): Promise<CodeIndexRunView> {
  return apiGet<CodeIndexRunView>(`${repoPath(repoId)}/index-runs/${encodeURIComponent(runId)}`)
}

/* ---------------------------------- tree ----------------------------------- */

/** `GET /api/code/repos/{id}/tree`: cursor page of file/directory entries. */
export function listTree(repoId: string, tree: CodeTreeQuery): Promise<ApiPage<CodeTreeEntryView>> {
  return apiGetPage<CodeTreeEntryView>(
    `${repoPath(repoId)}/tree${query({
      commit: tree.commit,
      path: tree.path,
      cursor: tree.cursor,
      limit: tree.limit,
    })}`,
  )
}

/** `GET /api/code/repos/{id}/file`: the only audited code-text window (≤400 lines). */
export function readFile(repoId: string, file: CodeFileQuery): Promise<CodeFileWindowView> {
  return apiGet<CodeFileWindowView>(
    `${repoPath(repoId)}/file${query({
      commit: file.commit,
      path: file.path,
      startLine: file.startLine,
      endLine: file.endLine,
    })}`,
  )
}

/* ----------------------------------- diff ---------------------------------- */

/** `GET /api/code/repos/{id}/diff`: change-set file page (no patch text). */
export function listDiff(
  repoId: string,
  diff: CodeDiffQuery,
): Promise<ApiPage<CodeChangedFileView>> {
  return apiGetPage<CodeChangedFileView>(
    `${repoPath(repoId)}/diff${query({
      base: diff.base,
      head: diff.head,
      cursor: diff.cursor,
      limit: diff.limit,
    })}`,
  )
}

/** `GET /api/code/repos/{id}/diff/file`: one on-demand patch (binary/large/deleted safe). */
export function readDiffFile(repoId: string, diff: CodeFileDiffQuery): Promise<CodeFileDiffView> {
  return apiGet<CodeFileDiffView>(
    `${repoPath(repoId)}/diff/file${query({
      base: diff.base,
      head: diff.head,
      path: diff.path,
    })}`,
  )
}

/* -------------------------------- facts/search ----------------------------- */

/** `GET /api/code/repos/{id}/facts`: cursor page of T1 facts (≤100). */
export function listFacts(
  repoId: string,
  options: CodeFactListQuery,
): Promise<ApiPage<CodeFactView>> {
  return apiGetPage<CodeFactView>(
    `${repoPath(repoId)}/facts${query({
      commit: options.commit,
      kind: options.kind,
      path: options.path,
      cursor: options.cursor,
      limit: options.limit,
    })}`,
  )
}

/** `GET /api/code/repos/{id}/search`: path/name substring search (≤50). */
export function searchCode(
  repoId: string,
  options: CodeSearchQuery,
): Promise<ApiPage<CodeEntityView>> {
  return apiGetPage<CodeEntityView>(
    `${repoPath(repoId)}/search${query({
      commit: options.commit,
      q: options.q,
      kind: options.kind,
      cursor: options.cursor,
      limit: options.limit,
    })}`,
  )
}

/** `GET /api/code/repos/{id}/commits/{sha}`: commit metadata only. */
export function getCommit(repoId: string, sha: string): Promise<CodeCommitView> {
  return apiGet<CodeCommitView>(`${repoPath(repoId)}/commits/${encodeURIComponent(sha)}`)
}

/* -------------------------------- changes/analysis ------------------------- */

/** `GET /api/versions/{id}/code/changes`: preview only; 422 when no baseline commit. */
export function getCodeChanges(
  versionId: string,
  changes: CodeChangesQuery,
): Promise<CodeChangeSetView> {
  return apiGet<CodeChangeSetView>(
    `${versionPath(versionId)}/code/changes${query({
      repoId: changes.repoId,
      head: changes.head,
    })}`,
  )
}

/** `POST /api/versions/{id}/code/analyses`: 202 queued, 200 reused, 409 in progress. */
export function startAnalysis(
  versionId: string,
  input: CodeAnalysisInput,
): Promise<CodeAnalysisView> {
  const body: { repoId: string; headCommit?: string; force?: boolean } = { repoId: input.repoId }
  if (input.headCommit !== undefined && input.headCommit.length > 0) {
    body.headCommit = input.headCommit
  }
  if (input.force === true) body.force = true
  return apiPost<CodeAnalysisView>(`${versionPath(versionId)}/code/analyses`, body)
}

/** `GET /api/versions/{id}/code/analyses/latest?repoId=`: 404 means not analysed yet. */
export async function latestAnalysis(
  versionId: string,
  repoId: string,
): Promise<CodeAnalysisView | null> {
  try {
    return await apiGet<CodeAnalysisView>(
      `${versionPath(versionId)}/code/analyses/latest${query({ repoId })}`,
    )
  } catch (cause) {
    if (isNotFound(cause)) return null
    throw cause
  }
}

/** `GET /api/versions/{id}/code/analyses/{analysisId}`: detail projection. */
export function getAnalysis(
  versionId: string,
  analysisId: string,
): Promise<CodeAnalysisDetailView> {
  return apiGet<CodeAnalysisDetailView>(
    `${versionPath(versionId)}/code/analyses/${encodeURIComponent(analysisId)}`,
  )
}

/** `PATCH .../candidates/{candidateId}`: persist one selection. */
export function patchCandidate(
  versionId: string,
  analysisId: string,
  candidateId: string,
  selected: boolean,
): Promise<CodeImpactCandidateView> {
  return apiPatch<CodeImpactCandidateView>(
    `${versionPath(versionId)}/code/analyses/${encodeURIComponent(analysisId)}/candidates/${encodeURIComponent(candidateId)}`,
    { selected },
  )
}

/** `GET /api/versions/{id}/code/impact`: merged multi-repo impact view. */
export function getCodeImpact(versionId: string, repoId?: string): Promise<CodeImpactView> {
  return apiGet<CodeImpactView>(`${versionPath(versionId)}/code/impact${query({ repoId })}`)
}

/* --------------------------------- anchors --------------------------------- */

/** `GET /api/versions/{id}/code/anchors`: version anchors, optionally by subject. */
export async function listAnchors(
  versionId: string,
  filters: CodeAnchorFilters = {},
): Promise<CodeAnchorView[]> {
  const page = await apiGetPage<CodeAnchorView>(
    `${versionPath(versionId)}/code/anchors${query({
      subjectKind: filters.subjectKind,
      subjectId: filters.subjectId,
    })}`,
  )
  return page.items
}

/** `POST /api/versions/{id}/code/anchors`: duplicate anchors answer 409. */
export function createAnchor(
  versionId: string,
  input: CreateCodeAnchorInput,
): Promise<CodeAnchorView> {
  const body: {
    subjectKind: string
    subjectId: string
    repoId: string
    filePath: string
    startLine?: number
    endLine?: number
    excerptSha?: string
  } = {
    subjectKind: input.subjectKind,
    subjectId: input.subjectId,
    repoId: input.repoId,
    filePath: input.filePath,
  }
  if (input.startLine !== undefined) body.startLine = input.startLine
  if (input.endLine !== undefined) body.endLine = input.endLine
  if (input.excerptSha !== undefined && input.excerptSha.length > 0) {
    body.excerptSha = input.excerptSha
  }
  return apiPost<CodeAnchorView>(`${versionPath(versionId)}/code/anchors`, body)
}

/** `POST .../anchors/{anchorId}/validate`: recomputes valid/stale/broken. */
export function validateAnchor(versionId: string, anchorId: string): Promise<CodeAnchorView> {
  return apiPost<CodeAnchorView>(
    `${versionPath(versionId)}/code/anchors/${encodeURIComponent(anchorId)}/validate`,
  )
}

/** `DELETE .../anchors/{anchorId}`: 204. */
export function deleteAnchor(versionId: string, anchorId: string): Promise<void> {
  return apiDelete<void>(`${versionPath(versionId)}/code/anchors/${encodeURIComponent(anchorId)}`)
}

/* -------------------------------- fact reviews ----------------------------- */

/** `GET .../facts/{factId}/reviews`: append-only history. */
export async function listReviews(
  versionId: string,
  factId: string,
): Promise<CodeFactReviewView[]> {
  const page = await apiGetPage<CodeFactReviewView>(
    `${versionPath(versionId)}/code/facts/${encodeURIComponent(factId)}/reviews`,
  )
  return page.items
}

/** `POST .../facts/{factId}/reviews`: 201 append-only verdict. */
export function createReview(
  versionId: string,
  factId: string,
  input: CreateCodeFactReviewInput,
): Promise<CodeFactReviewView> {
  const body: { verdict: string; reason?: string } = { verdict: input.verdict }
  if (input.reason !== undefined && input.reason.trim().length > 0) body.reason = input.reason
  return apiPost<CodeFactReviewView>(
    `${versionPath(versionId)}/code/facts/${encodeURIComponent(factId)}/reviews`,
    body,
  )
}

/* --------------------------------- proposals ------------------------------- */

/** `POST .../page-api-proposals`: idempotent proposed-resource generation. */
export function generateProposals(
  versionId: string,
  input: CodeProposalInput = {},
): Promise<CodeProposalResultView> {
  const body: { repoId?: string } = {}
  if (input.repoId !== undefined && input.repoId.length > 0) body.repoId = input.repoId
  return apiPost<CodeProposalResultView>(`${versionPath(versionId)}/code/page-api-proposals`, body)
}

/** `GET .../page-api-proposals`: proposed pages/APIs with evidence. */
export async function listProposals(versionId: string): Promise<CodeProposalView[]> {
  const page = await apiGetPage<CodeProposalView>(
    `${versionPath(versionId)}/code/page-api-proposals`,
  )
  return page.items
}

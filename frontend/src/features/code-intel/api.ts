import { type ApiPage, apiDelete, apiGet, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

import type {
  Analysis,
  Anchor,
  AnchorQuery,
  Candidate,
  ChangeList,
  CodeEntity,
  CreateAnchorBody,
  DiffQuery,
  FactsQuery,
  FileDiff,
  FileDiffQuery,
  FileQuery,
  FileWindow,
  IndexRun,
  IndexRunAccepted,
  RepoSummary,
  SearchQuery,
  StartAnalysisBody,
  StartAnalysisResult,
  TreeEntry,
  TreePage,
  TreeQuery,
} from "./types"

/** Repository-level prefix (spec §7.1). */
const CODE_REPOS_PATH = "/api/code/repos"

type QueryValue = string | number | null | undefined

/**
 * Appends only the defined, non-empty filters so optional reads never send
 * `commit=`/`cursor=` placeholders. Keys keep the API's camelCase spelling.
 */
function withQuery(path: string, query: Record<string, QueryValue>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "") continue
    params.set(key, String(value))
  }
  const suffix = params.toString()
  return suffix === "" ? path : `${path}?${suffix}`
}

/** Repo-scoped path; the repo id is a decimal number in the URL (spec §7.1). */
function repoPath(repoId: number): string {
  return `${CODE_REPOS_PATH}/${repoId}`
}

/**
 * Version-scoped prefix. The version id is a decimal string that is never
 * re-parsed as a number and is encoded as a single path segment (spec §7.1).
 */
function versionCodePath(versionId: string): string {
  return `/api/versions/${encodeURIComponent(versionId)}/code`
}

/** Analysis collection path for one version. */
function analysesPath(versionId: string): string {
  return `${versionCodePath(versionId)}/analyses`
}

/** Anchor collection path for one version. */
function anchorsPath(versionId: string): string {
  return `${versionCodePath(versionId)}/anchors`
}

/** GET /api/code/repos — repositories plus their latest index projection. */
export function getCodeRepos(): Promise<RepoSummary[]> {
  return apiGet<RepoSummary[]>(CODE_REPOS_PATH)
}

/** POST /api/code/repos/{repoId}/index — triggers a T0 index run (admin). */
export function startIndex(repoId: number): Promise<IndexRunAccepted> {
  return apiPost<IndexRunAccepted>(`${repoPath(repoId)}/index`)
}

/** GET /api/code/repos/{repoId}/index-runs/latest — newest index run. */
export function getLatestIndexRun(repoId: number): Promise<IndexRun> {
  return apiGet<IndexRun>(`${repoPath(repoId)}/index-runs/latest`)
}

/** GET /api/code/repos/{repoId}/tree — one cursor page of tree entries. */
export async function getTree(repoId: number, params: TreeQuery = {}): Promise<TreePage> {
  const page = await apiGetPage<TreeEntry>(
    withQuery(`${repoPath(repoId)}/tree`, {
      commit: params.commit,
      path: params.path,
      cursor: params.cursor,
      limit: params.limit,
    }),
  )
  return { entries: page.items, nextCursor: page.nextCursor, hasMore: page.hasMore }
}

/** GET /api/code/repos/{repoId}/file — a 1-based line window (audited read). */
export function getFile(repoId: number, params: FileQuery): Promise<FileWindow> {
  return apiGet<FileWindow>(
    withQuery(`${repoPath(repoId)}/file`, {
      commit: params.commit,
      path: params.path,
      startLine: params.startLine,
      endLine: params.endLine,
    }),
  )
}

/** GET /api/code/repos/{repoId}/diff — file-level change list page. */
export function getDiff(repoId: number, params: DiffQuery): Promise<ChangeList> {
  return apiGet<ChangeList>(
    withQuery(`${repoPath(repoId)}/diff`, {
      base: params.base,
      head: params.head,
      cursor: params.cursor,
      limit: params.limit,
    }),
  )
}

/** GET /api/code/repos/{repoId}/diff/file — one unified diff. */
export function getFileDiff(repoId: number, params: FileDiffQuery): Promise<FileDiff> {
  return apiGet<FileDiff>(
    withQuery(`${repoPath(repoId)}/diff/file`, {
      base: params.base,
      head: params.head,
      path: params.path,
    }),
  )
}

/** GET /api/code/repos/{repoId}/facts — one cursor page of fact entities. */
export function getFacts(repoId: number, params: FactsQuery = {}): Promise<ApiPage<CodeEntity>> {
  return apiGetPage<CodeEntity>(
    withQuery(`${repoPath(repoId)}/facts`, {
      commit: params.commit,
      kind: params.kind,
      path: params.path,
      cursor: params.cursor,
      limit: params.limit,
    }),
  )
}

/** GET /api/code/repos/{repoId}/search — path/name substring search (not full text). */
export function searchFacts(repoId: number, params: SearchQuery): Promise<ApiPage<CodeEntity>> {
  return apiGetPage<CodeEntity>(
    withQuery(`${repoPath(repoId)}/search`, {
      commit: params.commit,
      q: params.q,
      kind: params.kind,
      cursor: params.cursor,
      limit: params.limit,
    }),
  )
}

/** GET /api/versions/{id}/code/changes — read-only version change preview. */
export function getVersionChanges(versionId: string, head?: string): Promise<ChangeList> {
  return apiGet<ChangeList>(withQuery(`${versionCodePath(versionId)}/changes`, { head }))
}

/** POST /api/versions/{id}/code/analyses — triggers or reuses an analysis. */
export function startAnalysis(
  versionId: string,
  body: StartAnalysisBody = {},
): Promise<StartAnalysisResult> {
  return apiPost<StartAnalysisResult>(analysesPath(versionId), body)
}

/** GET /api/versions/{id}/code/analyses/latest — newest analysis for the version. */
export function getLatestAnalysis(versionId: string): Promise<Analysis> {
  return apiGet<Analysis>(`${analysesPath(versionId)}/latest`)
}

/** GET /api/versions/{id}/code/analyses/{analysisId} — one analysis by id. */
export function getAnalysis(versionId: string, analysisId: string): Promise<Analysis> {
  return apiGet<Analysis>(`${analysesPath(versionId)}/${encodeURIComponent(analysisId)}`)
}

/** PATCH .../analyses/{analysisId}/candidates/{candidateId} — manual selection. */
export function patchCandidate(
  versionId: string,
  analysisId: string,
  candidateId: string,
  selected: boolean,
): Promise<Candidate> {
  const analysisPath = `${analysesPath(versionId)}/${encodeURIComponent(analysisId)}`
  return apiPatch<Candidate>(`${analysisPath}/candidates/${encodeURIComponent(candidateId)}`, {
    selected,
  })
}

/** GET /api/versions/{id}/code/anchors — anchors filtered by subject. */
export function listAnchors(versionId: string, params: AnchorQuery = {}): Promise<Anchor[]> {
  return apiGet<Anchor[]>(
    withQuery(anchorsPath(versionId), {
      subjectKind: params.subjectKind,
      subjectId: params.subjectId,
    }),
  )
}

/** POST /api/versions/{id}/code/anchors — creates an anchor for a subject. */
export function createAnchor(versionId: string, body: CreateAnchorBody): Promise<Anchor> {
  return apiPost<Anchor>(anchorsPath(versionId), body)
}

/** DELETE /api/versions/{id}/code/anchors/{anchorId} — 204 on success. */
export function deleteAnchor(versionId: string, anchorId: string): Promise<void> {
  return apiDelete<void>(`${anchorsPath(versionId)}/${encodeURIComponent(anchorId)}`)
}

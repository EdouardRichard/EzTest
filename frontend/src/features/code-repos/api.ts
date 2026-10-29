import { apiDelete, apiGet, apiPatch, apiPost } from "@/lib/api-client"

import type { CodeRepoDTO, CodeRepoPage } from "./types"

/** Resource-context base path (spec §7.1); code repositories live under it. */
const CODE_REPOS_PATH = "/api/resources/code-repos"

/** The backend's maximum page size; the panel has no load-more control. */
const MAX_PAGE_SIZE = 100

/**
 * GET /api/resources/code-repos — one page of repositories.
 *
 * The shared `apiGet` unwraps the top-level `data` array, so the sibling
 * `next_cursor`/`has_more` fields of the list envelope are not observable here.
 * The panel has no pagination UI in P2, so it requests the maximum page size and
 * reports a single complete page. Real cursor paging arrives with the P4 index
 * screens.
 */
export async function listCodeRepos(): Promise<CodeRepoPage> {
  const items = await apiGet<CodeRepoDTO[]>(`${CODE_REPOS_PATH}?limit=${MAX_PAGE_SIZE}`)
  return {
    items: Array.isArray(items) ? items : [],
    nextCursor: null,
    hasMore: false,
  }
}

/** POST /api/resources/code-repos — creates a repository after a Git check. */
export function createCodeRepo(input: Partial<CodeRepoDTO>): Promise<CodeRepoDTO> {
  return apiPost<CodeRepoDTO>(CODE_REPOS_PATH, input)
}

/** PATCH /api/resources/code-repos/{id} — updates everything but the key. */
export function updateCodeRepo(id: number, input: Partial<CodeRepoDTO>): Promise<CodeRepoDTO> {
  return apiPatch<CodeRepoDTO>(`${CODE_REPOS_PATH}/${id}`, input)
}

/** DELETE /api/resources/code-repos/{id} — P2 repositories have no dependents. */
export function deleteCodeRepo(id: number): Promise<void> {
  return apiDelete<void>(`${CODE_REPOS_PATH}/${id}`)
}

/** POST /api/resources/code-repos/{id}/validate — re-runs the Git check. */
export function validateCodeRepo(id: number): Promise<CodeRepoDTO> {
  return apiPost<CodeRepoDTO>(`${CODE_REPOS_PATH}/${id}/validate`)
}

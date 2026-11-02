import { apiDelete, apiGetPage, apiPost } from "@/lib/api-client"

import type {
  CodeAnchor,
  CodeAnchorListOptions,
  CodeRepoOption,
  CreateCodeAnchorInput,
} from "./types"

const PAGE_LIMIT = 100

function query(params: Record<string, string | number | undefined>): string {
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

/** Lists anchors for one subject (subjectKind + subjectId are paired server-side). */
export async function listCodeAnchors(
  versionId: string,
  options: CodeAnchorListOptions,
): Promise<CodeAnchor[]> {
  const page = await apiGetPage<CodeAnchor>(
    `${versionPath(versionId)}/code/anchors${query({
      subjectKind: options.subjectKind,
      subjectId: options.subjectId,
      limit: PAGE_LIMIT,
    })}`,
  )
  return page.items
}

/** Creates one anchor; a duplicate path/line answers 409 `anchor_duplicate`. */
export function createCodeAnchor(
  versionId: string,
  input: CreateCodeAnchorInput,
): Promise<CodeAnchor> {
  return apiPost<CodeAnchor>(`${versionPath(versionId)}/code/anchors`, input)
}

/** Recomputes the anchor status (valid/stale/broken) at the bound commit. */
export function validateCodeAnchor(versionId: string, anchorId: string): Promise<CodeAnchor> {
  return apiPost<CodeAnchor>(
    `${versionPath(versionId)}/code/anchors/${encodeURIComponent(anchorId)}/validate`,
  )
}

export function deleteCodeAnchor(versionId: string, anchorId: string): Promise<void> {
  return apiDelete<void>(`${versionPath(versionId)}/code/anchors/${encodeURIComponent(anchorId)}`)
}

type CodeRepoStatusPayload = {
  repo_id: string
  repository?: { name?: string } | null
}

/** Version-bound repositories, used only to pick a `repoId` for a new anchor. */
export async function listVersionCodeRepos(versionId: string): Promise<CodeRepoOption[]> {
  const page = await apiGetPage<CodeRepoStatusPayload>(
    `${versionPath(versionId)}/code/repos${query({ limit: PAGE_LIMIT })}`,
  )
  return page.items.map((item) => ({
    id: item.repo_id,
    name: item.repository?.name ?? `仓库 ${item.repo_id}`,
  }))
}

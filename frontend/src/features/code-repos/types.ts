/** A local Git repository as returned by the resource API (spec §7.7). */
export type CodeRepoDTO = {
  id: number
  key: string
  name: string
  localPath: string
  fixedBranch: string
  includeGlobs: string[]
  excludeGlobs: string[]
  status: string
  lastCommit: string
  lastValidatedAt: string | null
  lastValidationError: string
  lastIndexedAt: string | null
}

/** One cursor page of code repositories. */
export type CodeRepoPage = {
  items: CodeRepoDTO[]
  nextCursor: string | null
  hasMore: boolean
}

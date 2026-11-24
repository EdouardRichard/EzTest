import { type ApiPage, apiGetPage } from "@/lib/api-client"

/**
 * One saved login-state fixture of the version's session fixture library
 * (admin visibility, SC-F5). Metadata only: the reuse key
 * (version, sut, environment, role), the content-addressed storage-state
 * reference and provenance. The state bytes never leave the content ledger
 * through this API.
 */
export type SessionFixture = {
  id: string
  version_id: string
  sut_id: string
  environment_id: string
  role: string
  storage_state_ref: string
  created_at: string
  created_by: string
}

/**
 * `GET /api/versions/{id}/session-fixtures` (admin only), newest first.
 * Fixture facts are append-only and unbounded, so the listing pages honestly:
 * the full page (items + nextCursor + hasMore) is returned and the caller drives
 * "load more" via `before`. Dropping hasMore here would silently shrink admin
 * audit coverage to the first page (constitution art.7.6).
 */
export async function listSessionFixtures(
  versionId: string,
  options: { limit?: number; before?: string } = {},
): Promise<ApiPage<SessionFixture>> {
  const params = new URLSearchParams()
  if (options.limit !== undefined) params.set("limit", String(options.limit))
  if (options.before) params.set("before", options.before)
  const query = params.toString()
  return apiGetPage<SessionFixture>(
    `/api/versions/${encodeURIComponent(versionId)}/session-fixtures${query ? `?${query}` : ""}`,
  )
}

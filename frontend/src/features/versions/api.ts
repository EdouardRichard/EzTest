import { apiGet, apiGetPage, apiPost } from "@/lib/api-client"

import type { Catalog, CreateVersionRequest, Version, VersionSummary } from "./types"

/** First page of versions; P0 lists up to the API maximum per request. */
export async function listVersions(): Promise<VersionSummary[]> {
  const page = await apiGetPage<VersionSummary>("/api/versions?limit=100")
  return page.items
}

/** One nested version with all SUT bindings and resource summaries. */
export function getVersion(id: string): Promise<Version> {
  return apiGet<Version>(`/api/versions/${encodeURIComponent(id)}`)
}

/** Creates a version with the exact contract payload built by the wizard. */
export function createVersion(payload: CreateVersionRequest): Promise<Version> {
  return apiPost<Version>("/api/versions", payload)
}

/** SUT/environment/page/API/database/account/repository catalog for the wizard. */
export function getResourceCatalog(): Promise<Catalog> {
  return apiGet<Catalog>("/api/resources/catalog")
}

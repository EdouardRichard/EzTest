import type { StageView } from "@/features/design/types"
import { apiGet, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

import type {
  Catalog,
  CreateVersionRequest,
  ReopenVersionInput,
  UpdateVersionRequest,
  Version,
  VersionSummary,
} from "./types"

/** First page of versions; P0 lists up to the API maximum per request. */
export async function listVersions(): Promise<VersionSummary[]> {
  const page = await apiGetPage<VersionSummary>("/api/versions?limit=100")
  return page.items
}

/** One nested version with all SUT bindings and resource summaries. */
export function getVersion(id: string): Promise<Version> {
  return apiGet<Version>(`/api/versions/${encodeURIComponent(id)}`)
}

/** Deterministic stage-machine projection (spec §5.3); drives the stage badge and gates. */
export function getVersionStage(id: string): Promise<StageView> {
  return apiGet<StageView>(`/api/versions/${encodeURIComponent(id)}/stage`)
}

/** Creates a version with the exact contract payload built by the wizard. */
export function createVersion(payload: CreateVersionRequest): Promise<Version> {
  return apiPost<Version>("/api/versions", payload)
}

/** Updates the version's auto-design flag (P2; no new settings keys). */
export function updateVersion(id: string, payload: UpdateVersionRequest): Promise<Version> {
  return apiPatch<Version>(`/api/versions/${encodeURIComponent(id)}`, payload)
}

/** `POST /api/versions/{id}/reopen`: completed → running; reason is required. */
export function reopenVersion(id: string, input: ReopenVersionInput): Promise<Version> {
  return apiPost<Version>(`/api/versions/${encodeURIComponent(id)}/reopen`, {
    reason: input.reason,
  })
}

/** SUT/environment/page/API/database/account/repository catalog for the wizard. */
export function getResourceCatalog(): Promise<Catalog> {
  return apiGet<Catalog>("/api/resources/catalog")
}

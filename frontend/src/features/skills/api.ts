import { apiDelete, apiGet, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

import type { SkillVersionView, SkillView } from "./types"

function skillPath(name: string): string {
  return `/api/skills/${encodeURIComponent(name)}`
}

/** GET /api/skills — admin skill library list (runtime DB is authoritative). */
export async function listSkills(): Promise<SkillView[]> {
  const payload = await apiGet<unknown>("/api/skills")
  return Array.isArray(payload) ? (payload as SkillView[]) : []
}

export type SkillCreateBody = {
  name: string
  description: string
  body: string
  enabled?: boolean
  note?: string
}

/** POST /api/skills — create a skill at version 1. */
export function createSkill(body: SkillCreateBody): Promise<SkillView> {
  return apiPost<SkillView>("/api/skills", body)
}

export type SkillPatchBody = {
  description?: string
  body?: string
  enabled?: boolean
  note?: string
  expected_version?: number
}

/** PATCH /api/skills/{name} — runtime edit; appends one append-only version.
 * The envelope's `changed` sibling is dropped by apiPatch (profile PATCH
 * precedent); the page compares the returned version to detect a no-op. */
export function updateSkill(name: string, patch: SkillPatchBody): Promise<SkillView> {
  return apiPatch<SkillView>(skillPath(name), patch)
}

/** DELETE /api/skills/{name} — soft delete; version history is preserved. */
export function deleteSkill(name: string): Promise<void> {
  return apiDelete<void>(skillPath(name))
}

/** GET /api/skills/{name}/versions — version DESC pages. */
export async function listSkillVersions(
  name: string,
  options: { limit?: number; beforeVersion?: number } = {},
) {
  const search = new URLSearchParams()
  search.set("limit", String(options.limit ?? 20))
  if (options.beforeVersion !== undefined) {
    search.set("before_version", String(options.beforeVersion))
  }
  return apiGetPage<SkillVersionView>(`${skillPath(name)}/versions?${search.toString()}`)
}

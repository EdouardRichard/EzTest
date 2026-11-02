import { apiGet, apiGetPage } from "@/lib/api-client"

import type { EvalRun, QualityOverview } from "./types"

const DEFAULT_EVAL_LIMIT = 20

/** Builds `?a=1&b=2`, skipping empty values (P0/P1 pagination style). */
function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === "") continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text.length > 0 ? `?${text}` : ""
}

/**
 * GET /api/quality/overview — 7 operational metrics + latest eval summary.
 * Logged-in users only; the latest eval summary is already redacted to counts.
 */
export function getQualityOverview(versionId?: string): Promise<QualityOverview> {
  return apiGet<QualityOverview>(`/api/quality/overview${query({ version_id: versionId })}`)
}

/**
 * GET /api/quality/eval-runs — admin-only terminal runs, newest first. The
 * endpoint has no cursor, so the page only asks for the latest `limit` runs.
 */
export async function listEvalRuns(limit: number = DEFAULT_EVAL_LIMIT): Promise<EvalRun[]> {
  const page = await apiGetPage<EvalRun>(`/api/quality/eval-runs${query({ limit })}`)
  return page.items
}

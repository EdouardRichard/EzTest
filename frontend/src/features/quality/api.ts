import { apiGet, apiGetPage, apiPost } from "@/lib/api-client"

import type { EvalRun, QualityOverview, TrendPoint } from "./types"

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

/**
 * POST /api/quality/eval-runs {mode:"snapshot", version_id} — admin trigger of
 * the W2-14 server-derived snapshot eval. The server reads the real version
 * data; no sample or client verdict is sent.
 */
export function triggerSnapshot(versionId: string): Promise<EvalRun> {
  return apiPost<EvalRun>("/api/quality/eval-runs", {
    mode: "snapshot",
    version_id: versionId,
  })
}

/**
 * GET /api/quality/trends?version_id=&metric=&from=&to= — admin-only version
 * metric series ordered oldest first (W2-14). Baseline regressions are carried
 * as flags for a warn-only display.
 */
export function getQualityTrends(params: {
  versionId: string
  metric: string
  from?: string
  to?: string
}): Promise<TrendPoint[]> {
  return apiGet<TrendPoint[]>(
    `/api/quality/trends${query({
      version_id: params.versionId,
      metric: params.metric,
      from: params.from,
      to: params.to,
    })}`,
  )
}

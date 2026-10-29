import { type ApiFetchOptions, apiGet } from "@/lib/api-client"

import type { EvalRun } from "./types"

/** Options forwarded to the shared API client; reads accept an abort signal. */
export type QualityRequestOptions = Pick<ApiFetchOptions, "signal">

/** GET /api/quality/eval-runs — newest deterministic/report runs first. */
export function listEvalRuns(limit = 20, options: QualityRequestOptions = {}): Promise<EvalRun[]> {
  const params = new URLSearchParams({ limit: String(limit) })
  const init: ApiFetchOptions = {}
  if (options.signal !== undefined) init.signal = options.signal
  return apiGet<EvalRun[]>(`/api/quality/eval-runs?${params.toString()}`, init)
}

/** GET /api/quality/eval-runs/{id} — one run, or the shared 404. */
export function getEvalRun(id: string, options: QualityRequestOptions = {}): Promise<EvalRun> {
  const init: ApiFetchOptions = {}
  if (options.signal !== undefined) init.signal = options.signal
  return apiGet<EvalRun>(`/api/quality/eval-runs/${encodeURIComponent(id)}`, init)
}

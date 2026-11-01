import { apiGet, apiGetPage, apiPost, apiPut } from "@/lib/api-client"

import type {
  VisualAssertion,
  VisualAssertionListOptions,
  VisualBaseline,
  VisualConfigInput,
  VisualDiff,
  VisualDiffDetail,
  VisualDiffListOptions,
  VisualMetrics,
  VisualOverview,
} from "./types"

const PAGE_LIMIT = 100

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

function versionPath(versionId: string): string {
  return `/api/versions/${encodeURIComponent(versionId)}`
}

function assertionPath(versionId: string, assertionId: string): string {
  return `${versionPath(versionId)}/visual/assertions/${encodeURIComponent(assertionId)}`
}

/* -------------------------------- assertions ------------------------------- */

/** Lists the version's visual assertion configs (`caseVersionId`/`versionSutId` filters). */
export async function listVisualAssertions(
  versionId: string,
  options: VisualAssertionListOptions = {},
): Promise<VisualAssertion[]> {
  const page = await apiGetPage<VisualAssertion>(
    `${versionPath(versionId)}/visual/assertions${query({
      caseVersionId: options.caseVersionId,
      versionSutId: options.versionSutId,
      limit: PAGE_LIMIT,
    })}`,
  )
  return page.items
}

/** One visual assertion with its active baseline summary. */
export function getVisualAssertion(
  versionId: string,
  assertionId: string,
): Promise<VisualAssertion> {
  return apiGet<VisualAssertion>(assertionPath(versionId, assertionId))
}

/**
 * Upserts one config keyed by the P2 design assertion id. The server bumps
 * `config_revision` and supersedes the active baseline when values change.
 */
export function upsertVisualAssertion(
  versionId: string,
  caseAssertionId: string,
  input: VisualConfigInput,
): Promise<VisualAssertion> {
  return apiPut<VisualAssertion>(assertionPath(versionId, caseAssertionId), input)
}

/* --------------------------------- baselines ------------------------------- */

/** Baseline history (approved + superseded). */
export async function listVisualBaselines(
  versionId: string,
  assertionId: string,
): Promise<VisualBaseline[]> {
  const page = await apiGetPage<VisualBaseline>(
    `${assertionPath(versionId, assertionId)}/baselines${query({ limit: PAGE_LIMIT })}`,
  )
  return page.items
}

/** Admin-only: approves one `visual_actual` evidence as the active baseline. */
export function approveVisualBaseline(
  versionId: string,
  assertionId: string,
  evidenceId: string,
): Promise<VisualBaseline> {
  return apiPost<VisualBaseline>(`${assertionPath(versionId, assertionId)}/baselines`, {
    evidenceId,
  })
}

/* ----------------------------------- diffs --------------------------------- */

/** Lists visual comparisons newest first; `cursor` is the last decimal id. */
export async function listVisualDiffs(
  versionId: string,
  options: VisualDiffListOptions = {},
): Promise<VisualDiff[]> {
  const page = await apiGetPage<VisualDiff>(
    `${versionPath(versionId)}/visual/diffs${query({
      caseRunId: options.caseRunId,
      caseVersionId: options.caseVersionId,
      classification: options.classification,
      cursor: options.cursor,
      limit: options.limit ?? PAGE_LIMIT,
    })}`,
  )
  return page.items
}

/** One comparison with mismatch boxes and environment fingerprint. */
export function getVisualDiff(versionId: string, diffId: string): Promise<VisualDiffDetail> {
  return apiGet<VisualDiffDetail>(
    `${versionPath(versionId)}/visual/diffs/${encodeURIComponent(diffId)}`,
  )
}

/* -------------------------------- overview --------------------------------- */

/** Version visual overview: baseline coverage, four states, latency, failures. */
export function getVisualOverview(versionId: string): Promise<VisualOverview> {
  return apiGet<VisualOverview>(`${versionPath(versionId)}/visual/overview`)
}

/** Global (`versionId` omitted) or per-version visual quality metrics. */
export function getVisualMetrics(versionId?: string): Promise<VisualMetrics> {
  return apiGet<VisualMetrics>(`/api/quality/visual-metrics${query({ versionId })}`)
}

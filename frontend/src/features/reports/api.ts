import { apiPost } from "@/lib/api-client"

import type { WaiveCloseResult, WaiveInput, WaiveRequirementResult } from "./types"

function versionPath(versionId: string): string {
  return `/api/versions/${encodeURIComponent(versionId)}`
}

/**
 * `POST /api/versions/{id}/waive-close` (T6, admin only): overrides the closure
 * completeness checks (still requires execution settled), closes the version as
 * `closure_kind=waived`/`unverified` through the single Close entry and
 * synchronously publishes the unverified report. The server enforces the admin
 * gate (403) and the execution-settled precondition (409).
 */
export function waiveClose(versionId: string, input: WaiveInput): Promise<WaiveCloseResult> {
  return apiPost<WaiveCloseResult>(`${versionPath(versionId)}/waive-close`, {
    reason: input.reason,
  })
}

/**
 * `POST /api/versions/{id}/requirements/{rid}/waive` (T6, admin only): marks one
 * requirement waived so the deterministic reconciler closes it with completeness
 * overridden. The server enforces the admin gate (403) and the state gate (409).
 */
export function waiveRequirement(
  versionId: string,
  requirementId: string,
  input: WaiveInput,
): Promise<WaiveRequirementResult> {
  return apiPost<WaiveRequirementResult>(
    `${versionPath(versionId)}/requirements/${encodeURIComponent(requirementId)}/waive`,
    { reason: input.reason },
  )
}

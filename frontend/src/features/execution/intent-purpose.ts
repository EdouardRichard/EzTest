import type { CaseRun, CreateIntentInput } from "./types"

/**
 * Purpose of a rerun triggered from a terminal result row (spec §5.8).
 *
 * A passed case has no failed result to retest, so the API rejects
 * `purpose=retest` ("retest requires an existing failed result") and the user
 * has to fall back to the API. Rerunning a passed case is a manual execution.
 * Every other verdict keeps `retest`, so the server-side retest gate is not
 * widened or bypassed.
 */
export function rerunPurpose(caseRun: Pick<CaseRun, "status">): CreateIntentInput["purpose"] {
  return caseRun.status === "passed" ? "manual" : "retest"
}

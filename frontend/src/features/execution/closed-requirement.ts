import { ApiError } from "@/lib/api-client"

/**
 * The closed-requirement 409 carries `details.reason=requirement_closed` plus
 * the requirement ID. That is the one retry error the UI can turn into a real
 * recovery action (reopen the requirement) instead of a dead end.
 *
 * Shared by the execution results panel (rerun) and the delivery issue tab /
 * sheet (retest) so both surfaces agree on the detection and the deep link.
 */
export function closedRequirementId(error: unknown): string | null {
  if (!(error instanceof ApiError) || error.details.reason !== "requirement_closed") {
    return null
  }
  const requirementId = error.details.requirement_id
  return typeof requirementId === "string" && requirementId.length > 0 ? requirementId : null
}

export type RequirementClosedKind = "rerun" | "retest"

/**
 * Chinese primary copy for a blocked rerun/retest. The backend keeps its
 * English `message`; the scenario owns the wording so the action is named.
 *
 * Keep in sync with `REQUIREMENT_CLOSED_MESSAGES` in
 * `@/lib/api-error-messages` (the default `InlineError` localizer for the
 * same typed reason).
 */
const REQUIREMENT_CLOSED_MESSAGES: Record<RequirementClosedKind, string> = {
  rerun: "无法重跑：该用例所属需求已闭环，请先在「需求与澄清」中重新打开该需求",
  retest: "无法复测：该用例所属需求已闭环，请先在「需求与澄清」中重新打开该需求",
}

/** Primary copy naming which action a closed requirement blocked. */
export function requirementClosedMessage(kind: RequirementClosedKind): string {
  return REQUIREMENT_CLOSED_MESSAGES[kind]
}

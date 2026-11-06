/**
 * v0.22 human_request kind discrimination.
 *
 * Structured questions and the approval/blocked_recovery/conflict decision
 * requests share one table and one `GET /api/versions/{id}/questions` list
 * (R-W2-1). This module is imported by the renderers so a decision row can
 * never be fed to the clarification QuestionCard (which would render an empty
 * batch and post to `/questions/{id}/answer`).
 *
 * It lives outside `api.ts` because the thread tests mock the API transport
 * module; the pure kind helpers must stay real in those tests.
 */

export const HUMAN_DECISION_KINDS = ["approval", "blocked_recovery", "conflict"] as const

export type HumanDecisionKind = (typeof HUMAN_DECISION_KINDS)[number]

export function isHumanDecisionKind(kind: string | null | undefined): boolean {
  return (
    kind !== null &&
    kind !== undefined &&
    (HUMAN_DECISION_KINDS as readonly string[]).includes(kind)
  )
}

/**
 * Only a `clarification_question` (or a legacy row without a kind) is a
 * question card. Unknown kinds are deliberately excluded so a future
 * human_request kind can never be answered through the question route by
 * accident.
 */
export function isClarificationQuestionBatch(question: { kind?: string | null }): boolean {
  return (
    question.kind === undefined ||
    question.kind === null ||
    question.kind === "" ||
    question.kind === "clarification_question"
  )
}

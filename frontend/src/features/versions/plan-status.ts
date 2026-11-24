/**
 * Single frontend source for the FINISHED plan-item status set (v0.2.4-B).
 *
 * Authority: `backend/internal/version/domain/plan_item.go`
 * `planItemTerminalStatuses` (done / completed / cancelled / canceled / skipped /
 * failed / closed), classified by `PlanItemTerminal` with trim + lower-case
 * normalization. This constant mirrors that set so the plan panel cannot present
 * a finished item as open work; `plan-status.test.ts` pins it to the authority.
 *
 * NOTE (constitution 五.1/五.4): the cross-language authority link IS wired —
 * Go `version/domain/enum_authority_test.go` (`TestEnumAuthorityFrontendPlanStatus`)
 * reads this file the way `VersionStatus` is pinned, so a change to the Go
 * authority that this constant no longer mirrors fails CI. This constant +
 * `plan-status.test.ts` are the frontend half of that link.
 */
export const PLAN_ITEM_TERMINAL_STATUSES = [
  "done",
  "completed",
  "cancelled",
  "canceled",
  "skipped",
  "failed",
  "closed",
] as const

const TERMINAL_SET = new Set<string>(PLAN_ITEM_TERMINAL_STATUSES)

/** Mirrors Go `PlanItemTerminal`: trim + lower-case, then set membership. */
export function planItemTerminal(status: string): boolean {
  return TERMINAL_SET.has(status.trim().toLowerCase())
}

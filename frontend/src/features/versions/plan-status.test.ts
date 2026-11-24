import { describe, expect, it } from "vitest"

import { PLAN_ITEM_TERMINAL_STATUSES, planItemTerminal } from "./plan-status"

// Pins the frontend terminal set to the Go authority
// backend/internal/version/domain/plan_item.go planItemTerminalStatuses. The
// reverse (Go -> frontend) half of the cross-language gate IS wired:
// TestEnumAuthorityFrontendPlanStatus (backend/internal/version/domain/enum_authority_test.go)
// reads plan-status.ts and pins its set equal to the Go authority, so a change on
// either side fails CI until the other is updated.
const AUTHORITY = ["done", "completed", "cancelled", "canceled", "skipped", "failed", "closed"]

describe("plan-item terminal status authority", () => {
  it("matches the backend authoritative set exactly", () => {
    expect([...PLAN_ITEM_TERMINAL_STATUSES].sort()).toEqual([...AUTHORITY].sort())
  })

  it("classifies every terminal status, with trim + lower-case normalization", () => {
    for (const status of AUTHORITY) {
      expect(planItemTerminal(status)).toBe(true)
      expect(planItemTerminal(` ${status.toUpperCase()} `)).toBe(true)
    }
  })

  it("treats open-work statuses as non-terminal", () => {
    for (const status of ["pending", "in_progress", "blocked", ""]) {
      expect(planItemTerminal(status)).toBe(false)
    }
  })
})

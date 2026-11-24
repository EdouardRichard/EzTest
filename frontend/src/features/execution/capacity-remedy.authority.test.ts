import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { describe, expect, it } from "vitest"

import { terminalCauseRemedy } from "./labels"

/**
 * Cross-language behaviour gate for the executor-capacity remedy (v0.2.4-H T17,
 * H-B9).
 *
 * The same remedy (`context_requirement_exceeds_capacity`) is shown through the
 * backend `case_run.blocked` event `action` (blockedAction) and the frontend
 * terminal-cause override (CASE_RUN_TERMINAL_CAUSE_REMEDY). There is no Go->TS
 * codegen pipeline, so the single-source mechanism is the one the enum unions and
 * the requirement precheck use: a SHARED, checked case set. The fixture lives
 * next to the Go authority and is evaluated by BOTH sides —
 * `backend/internal/execution/application/capacity_remedy_gate_test.go` runs the
 * Go blockedAction, this file runs the TypeScript mirror. A wording drift on
 * either side fails its own gate instead of silently diverging.
 *
 * Path resolution mirrors `features/clarification/requirement-precheck.authority.test.ts`:
 * `fileURLToPath(import.meta.url)` (never `new URL(literal, import.meta.url)`,
 * which Vite/vitest rewrite — doc 19 §4 invariant ⑬).
 */
function sharedFixturePath(): string {
  // here = frontend/src/features/execution → four levels up = repo root.
  const here = dirname(fileURLToPath(import.meta.url))
  return resolve(
    here,
    "../../../../backend/internal/execution/application/testdata/capacity_remedy_gate.json",
  )
}

type GateFixture = {
  remedies: Array<{ reason: string; remedy: string }>
}

const fixture = JSON.parse(readFileSync(sharedFixturePath(), "utf8")) as GateFixture

describe("capacity remedy cross-language authority gate (H-B9)", () => {
  it("loads a non-empty shared case set", () => {
    expect(fixture.remedies.length).toBeGreaterThan(0)
  })

  for (const test of fixture.remedies) {
    it(`remedy: ${test.reason}`, () => {
      // Symmetric with the Go gate: each case must carry a non-empty reason and
      // remedy, so an empty fixture entry cannot pass vacuously.
      expect(test.reason.trim().length).toBeGreaterThan(0)
      expect(test.remedy.trim().length).toBeGreaterThan(0)
      expect(terminalCauseRemedy(test.reason)).toBe(test.remedy)
    })
  }
})

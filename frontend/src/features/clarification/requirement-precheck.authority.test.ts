import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { describe, expect, it } from "vitest"

import {
  isPlaceholder,
  normalizePlaceholderTerm,
  precheckConfirmation,
} from "./requirement-precheck"

/**
 * Cross-language behaviour gate for the requirement confirmation precheck
 * (v0.2.4-E, spec SC-E1/SC-E10).
 *
 * SC-E10 forbids a second hand-maintained matching truth in the client. There is
 * no Go->TS codegen pipeline in this worktree, so the single-source mechanism is
 * the one the enum unions already use: a SHARED, checked case set. The fixture
 * lives next to the Go authority and is evaluated by BOTH sides —
 * `backend/internal/version/domain/confirmation_precheck_gate_test.go` runs the
 * Go functions, this file runs the TypeScript mirror. A semantic drift on either
 * side fails its own gate instead of silently diverging.
 *
 * Path resolution mirrors `features/execution/wave-counts.authority.ts`:
 * `fileURLToPath(import.meta.url)` (never `new URL(literal, import.meta.url)`,
 * which Vite/vitest rewrite — doc 19 §4 invariant ⑬).
 */
function sharedFixturePath(): string {
  // here = frontend/src/features/clarification → four levels up = repo root.
  const here = dirname(fileURLToPath(import.meta.url))
  return resolve(
    here,
    "../../../../backend/internal/version/domain/testdata/confirmation_precheck_gate.json",
  )
}

type GateFixture = {
  placeholder_cases: Array<{
    name: string
    value: string
    terms: string[]
    is_placeholder: boolean
  }>
  min_length_cases: Array<{
    name: string
    value: string
    min_chars: number
    too_short: boolean
  }>
}

const fixture = JSON.parse(readFileSync(sharedFixturePath(), "utf8")) as GateFixture

describe("requirement precheck cross-language authority gate", () => {
  it("loads a non-empty shared case set", () => {
    expect(fixture.placeholder_cases.length).toBeGreaterThan(0)
    expect(fixture.min_length_cases.length).toBeGreaterThan(0)
  })

  for (const test of fixture.placeholder_cases) {
    it(`placeholder: ${test.name}`, () => {
      expect(isPlaceholder(test.value, test.terms)).toBe(test.is_placeholder)
    })
  }

  for (const test of fixture.min_length_cases) {
    it(`rune floor: ${test.name}`, () => {
      // Drive the PRODUCTION precheck (not a re-typed expression): only the
      // sut_description floor is exercised, with an empty term list so no
      // placeholder rule can interfere. The floor is a trimmed RUNE count
      // (code points), never UTF-16 units — same as the Go authority.
      const errors = precheckConfirmation(
        {
          title: "手机号登录",
          asA: "访客",
          iWant: "登录",
          soThat: "进入系统",
          sutDescription: test.value,
          criteria: [],
        },
        { placeholderTerms: [], minDescriptionChars: test.min_chars },
      )
      expect(errors.sut_description !== undefined).toBe(test.too_short)
    })
  }

  it("normalizes exactly as Go NormalizePlaceholderTerm (trim + lower-case)", () => {
    expect(normalizePlaceholderTerm("  TbD  ")).toBe("tbd")
    expect(normalizePlaceholderTerm(" 待补充\n")).toBe("待补充")
  })
})

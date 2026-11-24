import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

import { describe, expect, it } from "vitest"

import type { Version } from "../versions/types"
import { CLOSURE_KIND_LABELS, CLOSURE_KINDS, type VersionClosureKind } from "./types"

/**
 * Cross-language authority gate (宪法五.1/五.2): the frontend report view renders
 * the server's byte-frozen template v3 verbatim, so the vocabulary it relies on
 * (five-element card markers, state CSS classes, closure kinds) must stay locked
 * to the Go authority. If the Go template/domain renames a marker or label, this
 * gate fails and the frontend vocabulary/fixture must be updated in the same
 * change set — no silent drift.
 *
 * The AC verification vocabulary (state values + Chinese labels) was deleted as
 * unwired code (H-G4 #31): the report body is verbatim HTML, so there is no
 * React second truth to drift. A regression guard keeps it deleted.
 *
 * Path resolution mirrors wave-counts.authority.ts (cwd-independent, from this
 * module's own location; jsdom keeps import.meta.url a file:// URL).
 */
function repoRoot(): string {
  // here = frontend/src/features/reports → four levels up = repo root.
  return resolve(dirname(fileURLToPath(import.meta.url)), "../../../../")
}

function readRepo(rel: string): string {
  return readFileSync(resolve(repoRoot(), rel), "utf8")
}

type Equal<A, B> =
  (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false
type Assert<T extends true> = T

const TEMPLATE_V3 = "backend/internal/delivery/infrastructure/render/template_v3.go"
const DISPLAY = "backend/internal/delivery/domain/display.go"

describe("report view vocabulary matches the Go report authority", () => {
  it("Version.closure_kind reuses the VersionClosureKind authority (type-level)", () => {
    // Compile-time guard (宪法五.1, H-G4 #32): if `Version.closure_kind` diverges
    // from the single `VersionClosureKind` authority, `Assert<Equal<…>>` is no
    // longer a `true` constraint and this line fails `tsc --noEmit`.
    const authorityCheck: Assert<Equal<NonNullable<Version["closure_kind"]>, VersionClosureKind>> =
      true
    expect(authorityCheck).toBe(true)
  })

  it("the five defect-card element markers are present in the frozen template", () => {
    const template = readRepo(TEMPLATE_V3)
    for (const element of ["requirement", "steps", "ac", "expected", "actual"]) {
      expect(template, `data-element="${element}"`).toContain(`data-element="${element}"`)
    }
  })

  it("the AC state CSS classes are present in the frozen template", () => {
    const template = readRepo(TEMPLATE_V3)
    for (const state of ["verified", "unverified", "failed", "uncovered"]) {
      expect(template, `.state-${state}`).toContain(`.state-${state}`)
    }
  })

  it("the core report section markers are present in the frozen template", () => {
    const template = readRepo(TEMPLATE_V3)
    for (const section of ["summary", "issues", "coverage", "evidence", "traceability", "judge"]) {
      expect(template, `data-section="${section}"`).toContain(`data-section="${section}"`)
    }
  })

  it("closure-kind values match the delivery display authority", () => {
    const display = readRepo(DISPLAY)
    const normal = display.match(/ReportClosureKindNormal\s*=\s*"([a-z]+)"/)
    const waived = display.match(/ReportClosureKindWaived\s*=\s*"([a-z]+)"/)
    expect(normal?.[1]).toBe("normal")
    expect(waived?.[1]).toBe("waived")
    expect([...CLOSURE_KINDS].sort()).toEqual([normal?.[1], waived?.[1]].sort())
    expect(Object.keys(CLOSURE_KIND_LABELS).sort()).toEqual([normal?.[1], waived?.[1]].sort())
  })

  it("the unwired AC verification vocabulary stays deleted (no React second truth)", () => {
    // H-G4 #31: the AC state values/labels/tones had no production consumer; the
    // report body is rendered as verbatim HTML. This guard makes a silent
    // re-introduction fail until the vocabulary is actually wired.
    const src = readRepo("frontend/src/features/reports/types.ts")
    for (const name of ["ACVerificationState", "AC_STATE_LABELS", "AC_STATE_TONES"]) {
      expect(src, `${name} must stay deleted (no production consumer)`).not.toContain(name)
    }
  })

  it("versions/types.ts reuses the report closure-kind authority (no inline second union)", () => {
    // H-G4 #32: `Version.closure_kind` must import the single
    // `VersionClosureKind` authority instead of re-declaring the union inline.
    // Reverting to a divergent inline union makes this test (and `tsc`) fail.
    const src = readRepo("frontend/src/features/versions/types.ts")
    expect(src).toMatch(
      /import type \{[^}]*VersionClosureKind[^}]*\} from "@\/features\/reports\/types"/,
    )
    expect(src).not.toMatch(/closure_kind\?:\s*"normal"\s*\|\s*"waived"/)
  })
})

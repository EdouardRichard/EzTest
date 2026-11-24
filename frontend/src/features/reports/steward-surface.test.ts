import { readdirSync, readFileSync } from "node:fs"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

import { describe, expect, it } from "vitest"

/**
 * StewardSurfaceRemoved (SC-G10): the Version Steward team/profile was deleted
 * backend-side (T5, GD-17). The frontend surface must carry no steward residual
 * either — TeamRoomKind drops "steward" and the Agent Center fixture no longer
 * plays version_steward. This gate scans the frontend source surface (the same
 * whole-class discipline as the Go TestStewardFullyRemoved gate) so a
 * re-introduced steward reference fails here, not just in review.
 */
function frontendSrcRoot(): string {
  // here = frontend/src/features/reports → two levels up = frontend/src.
  return resolve(dirname(fileURLToPath(import.meta.url)), "../../")
}

function walk(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...walk(full))
    } else if (entry.name.endsWith(".ts") || entry.name.endsWith(".tsx")) {
      out.push(full)
    }
  }
  return out
}

describe("StewardSurfaceRemoved", () => {
  it("TeamRoomKind no longer includes the steward team", () => {
    const types = readFileSync(
      resolve(frontendSrcRoot(), "features/clarification/types.ts"),
      "utf8",
    )
    const line = types.split("\n").find((row) => row.includes("export type TeamRoomKind"))
    expect(line, "TeamRoomKind declaration").toBeDefined()
    expect(line).not.toMatch(/steward/i)
    for (const team of ["clarification", "design", "execution", "delivery"]) {
      expect(line).toContain(team)
    }
  })

  it("no frontend source file carries a live steward/version_steward residual", () => {
    const root = frontendSrcRoot()
    const offenders: string[] = []
    for (const file of walk(root)) {
      // This gate necessarily names the retired concept; exclude itself.
      if (file.includes("steward-surface")) continue
      const text = readFileSync(file, "utf8")
      // Scan LIVE code only: retirement notes/comments may legitimately name the
      // removed team (the Go gate likewise exempts its retirement declaration).
      // A full-line comment is not a functional surface; a steward string in
      // executable code (type union, profile key, runtime message) is.
      const live = text
        .split("\n")
        .filter((line) => {
          const trimmed = line.trim()
          return (
            !trimmed.startsWith("//") &&
            !trimmed.startsWith("*") &&
            !trimmed.startsWith("/*") &&
            !trimmed.startsWith("{/*")
          )
        })
        .join("\n")
      if (/steward/i.test(live)) offenders.push(file.slice(root.length + 1))
    }
    expect(offenders, `steward residuals: ${offenders.join(", ")}`).toEqual([])
  })
})

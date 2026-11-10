import { describe, expect, it } from "vitest"

import { AUDIT_ACTIONS } from "./audit-actions"

describe("AUDIT_ACTIONS", () => {
  it("has unique names and aliases that do not shadow canonical names", () => {
    const names = new Set(AUDIT_ACTIONS.map((action) => action.name))
    expect(names.size).toBe(AUDIT_ACTIONS.length)
    const aliases = new Set<string>()
    for (const action of AUDIT_ACTIONS) {
      expect(action.name).toMatch(/^[a-z][a-z0-9_]*\.[a-z][a-z0-9_.]*$/)
      expect(action.label.length).toBeGreaterThan(0)
      for (const alias of action.aliases) {
        expect(alias).not.toBe(action.name)
        expect(names.has(alias)).toBe(false)
        expect(aliases.has(alias)).toBe(false)
        aliases.add(alias)
      }
    }
  })

  it("covers every audit domain exposed by the backend", () => {
    const prefixes = [
      "auth.",
      "user.",
      "settings.",
      "llm.",
      "resource.",
      "version.",
      "agent.",
      "agent_profile.",
      "quality.",
      "case.",
      "execution.",
      "visual.",
      "content.",
    ]
    for (const prefix of prefixes) {
      expect(AUDIT_ACTIONS.some((action) => action.name.startsWith(prefix))).toBe(true)
    }
  })

  it("documents the historical settings alias", () => {
    const updated = AUDIT_ACTIONS.find((action) => action.name === "settings.updated")
    expect(updated?.aliases).toContain("setting.update")
  })
})

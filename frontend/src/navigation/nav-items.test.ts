import { describe, expect, it } from "vitest"

import { isActive, NAV_ITEMS } from "@/navigation/nav-items"

describe("navigation items", () => {
  it("exposes the current P8 routes and keeps the future phase entries", () => {
    const p8 = NAV_ITEMS.filter((item) => item.phase === "P8")
    expect(p8.map((item) => item.href)).toEqual(["/", "/agents", "/settings"])
    expect(NAV_ITEMS.some((item) => item.phase === "P3")).toBe(true)
  })

  it("marks the agent console route as the P1 agent runtime console", () => {
    const agents = NAV_ITEMS.find((item) => item.href === "/agents")
    expect(agents?.phase).toBe("P8")
    expect(agents?.hint).toBe("Agent 运行时控制台（P8 扩展为 Agent 中心）")
  })

  it("points the case-mindmap entry at the version list and drops invalid hashes", () => {
    const mindmap = NAV_ITEMS.find((item) => item.label === "用例脑图")
    expect(mindmap?.href).toBe("/versions")
    expect(NAV_ITEMS.some((item) => item.href.includes("#"))).toBe(false)
  })

  it("marks the issues and reports entries as P7 global delivery destinations", () => {
    const issues = NAV_ITEMS.find((item) => item.href === "/issues")
    const reports = NAV_ITEMS.find((item) => item.href === "/reports")

    expect(issues?.phase).toBe("P7")
    expect(reports?.phase).toBe("P7")
    expect(issues?.hint).toContain("P7")
    expect(reports?.hint).toContain("P7")
    expect(issues?.hint).toContain("全局")
    expect(reports?.hint).toContain("全局")
  })

  it("matches exact and nested routes but never hash anchors", () => {
    expect(isActive("/", "/")).toBe(true)
    expect(isActive("/settings", "/settings")).toBe(true)
    expect(isActive("/settings/users", "/settings")).toBe(true)
    expect(isActive("/versions", "/")).toBe(false)
    expect(isActive("/versions", "/versions#mindmap")).toBe(false)
    expect(isActive("/issues", "/issues")).toBe(true)
    expect(isActive("/reports", "/reports")).toBe(true)
    expect(isActive("/issues", "/reports")).toBe(false)
  })
})

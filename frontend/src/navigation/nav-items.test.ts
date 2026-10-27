import { describe, expect, it } from "vitest"

import { isActive, NAV_ITEMS } from "@/navigation/nav-items"

describe("navigation items", () => {
  it("exposes the two P0 routes and keeps the future phase entries", () => {
    const p0 = NAV_ITEMS.filter((item) => item.phase === "P0")
    expect(p0.map((item) => item.href)).toEqual(["/", "/settings"])
    expect(NAV_ITEMS.some((item) => item.phase === "P3")).toBe(true)
  })

  it("matches exact and nested routes but never hash anchors", () => {
    expect(isActive("/", "/")).toBe(true)
    expect(isActive("/settings", "/settings")).toBe(true)
    expect(isActive("/settings/users", "/settings")).toBe(true)
    expect(isActive("/versions", "/")).toBe(false)
    expect(isActive("/versions", "/versions#mindmap")).toBe(false)
  })
})

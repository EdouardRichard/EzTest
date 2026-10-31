import { describe, expect, it } from "vitest"

import { findNavItem, isNavItemActive, NAV_ITEMS, NAV_SECTIONS, type NavItem } from "./nav-items"

const labels = ["版本", "问题", "报告", "知识", "资源", "质量", "Agent 中心", "设置"]

describe("NAV_ITEMS", () => {
  it("has exactly the eight P0 navigation entries in product order", () => {
    expect(NAV_ITEMS).toHaveLength(8)
    expect(NAV_ITEMS.map((item) => item.label)).toEqual(labels)
  })

  it("uses unique keys, hrefs and icons", () => {
    expect(new Set(NAV_ITEMS.map((item) => item.key)).size).toBe(8)
    expect(new Set(NAV_ITEMS.map((item) => item.href)).size).toBe(8)
    expect(new Set(NAV_ITEMS.map((item) => item.icon)).size).toBe(8)
  })

  it("splits the five workspace entries from the three platform entries", () => {
    expect(NAV_SECTIONS.map((section) => section.label)).toEqual(["工作区", "平台"])
    const workspace = NAV_ITEMS.filter((item) => item.section === "workspace")
    const platform = NAV_ITEMS.filter((item) => item.section === "platform")
    expect(workspace.map((item) => item.label)).toEqual(["版本", "问题", "报告", "知识", "资源"])
    expect(platform.map((item) => item.label)).toEqual(["质量", "Agent 中心", "设置"])
  })

  it("marks only the P1+ global placeholders", () => {
    const placeholders = NAV_ITEMS.filter((item) => item.placeholder)
    expect(placeholders.map((item) => item.label)).toEqual([
      "问题",
      "报告",
      "知识",
      "质量",
      "Agent 中心",
    ])
    const p0 = NAV_ITEMS.filter((item) => !item.placeholder)
    expect(p0.map((item) => item.label)).toEqual(["版本", "资源", "设置"])
  })

  it("points every link at its feature route", () => {
    expect(NAV_ITEMS.map((item) => item.href)).toEqual([
      "/versions",
      "/issues",
      "/reports",
      "/knowledge",
      "/resources",
      "/quality",
      "/agents",
      "/settings",
    ])
  })
})

describe("isNavItemActive", () => {
  const versions = NAV_ITEMS[0] as NavItem

  it("activates on the exact route", () => {
    expect(isNavItemActive("/versions", versions)).toBe(true)
  })

  it("activates for nested routes of the section", () => {
    expect(isNavItemActive("/versions/1024", versions)).toBe(true)
  })

  it("does not activate on a lookalike prefix or an unrelated route", () => {
    expect(isNavItemActive("/versions-archive", versions)).toBe(false)
    expect(isNavItemActive("/resources", versions)).toBe(false)
    expect(isNavItemActive("/", versions)).toBe(false)
  })
})

describe("findNavItem", () => {
  it("finds the entry that owns the current route", () => {
    expect(findNavItem("/versions/42")?.label).toBe("版本")
    expect(findNavItem("/agents")?.label).toBe("Agent 中心")
    expect(findNavItem("/unknown")).toBeUndefined()
  })
})

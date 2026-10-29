import { afterEach, describe, expect, it, vi } from "vitest"

import {
  buildSearchPath,
  MAX_RECENT_SEARCHES,
  normalizeSearchSnapshot,
  RECENT_SEARCHES_KEY,
  readRecentSearches,
  rememberSearch,
  SEARCH_TYPES,
} from "./useGlobalSearch"

afterEach(() => {
  window.localStorage.clear()
  vi.unstubAllGlobals()
})

describe("buildSearchPath", () => {
  it("requests every type with the documented default limit", () => {
    const path = buildSearchPath("登录 流程")
    const url = new URL(path, "http://localhost")

    expect(url.pathname).toBe("/api/search")
    expect(url.searchParams.get("q")).toBe("登录 流程")
    expect(url.searchParams.get("types")).toBe("version,requirement,case,issue")
    expect(url.searchParams.get("limit")).toBe("5")
  })

  it("forwards a custom type filter and limit", () => {
    const url = new URL(buildSearchPath("下单", ["case", "issue"], 12), "http://localhost")

    expect(url.searchParams.get("types")).toBe("case,issue")
    expect(url.searchParams.get("limit")).toBe("12")
  })
})

describe("normalizeSearchSnapshot", () => {
  it("keeps the known groups and item fields", () => {
    const snapshot = normalizeSearchSnapshot({
      groups: [
        {
          type: "case",
          items: [
            {
              id: "300",
              title: "登录流程",
              subtitle: "版本甲",
              versionId: "11",
              versionName: "版本甲",
              status: "reviewed",
              url: "/versions/11?tab=mindmap&case=300",
            },
          ],
        },
      ],
      total: 1,
      truncated: true,
      degraded: true,
    })

    expect(snapshot.groups).toHaveLength(1)
    expect(snapshot.groups[0]?.type).toBe("case")
    expect(snapshot.groups[0]?.items[0]).toEqual({
      id: "300",
      title: "登录流程",
      subtitle: "版本甲",
      versionId: "11",
      versionName: "版本甲",
      status: "reviewed",
      url: "/versions/11?tab=mindmap&case=300",
    })
    expect(snapshot.total).toBe(1)
    expect(snapshot.truncated).toBe(true)
    expect(snapshot.degraded).toBe(true)
  })

  it("drops unknown types, malformed items and derives missing fields", () => {
    const snapshot = normalizeSearchSnapshot({
      groups: [
        { type: "bogus", items: [{ id: "1" }] },
        { type: "issue", items: [{ id: "400", title: "下单失败" }, "not-an-item"] },
      ],
    })

    expect(snapshot.groups).toHaveLength(1)
    expect(snapshot.groups[0]?.type).toBe("issue")
    expect(snapshot.groups[0]?.items).toHaveLength(1)
    expect(snapshot.groups[0]?.items[0]?.subtitle).toBe("")
    expect(snapshot.total).toBe(1)
    expect(snapshot.truncated).toBe(false)
    expect(snapshot.degraded).toBe(false)
  })

  it("returns an empty snapshot for a non-object payload", () => {
    expect(normalizeSearchSnapshot(null).groups).toEqual([])
    expect(normalizeSearchSnapshot("nope").total).toBe(0)
  })
})

describe("recent searches", () => {
  it("reads at most five terms and tolerates corrupt storage", () => {
    window.localStorage.setItem(
      RECENT_SEARCHES_KEY,
      JSON.stringify(["一", "二", "三", "四", "五", "六"]),
    )
    expect(readRecentSearches()).toEqual(["一", "二", "三", "四", "五"])

    window.localStorage.setItem(RECENT_SEARCHES_KEY, "{not-json")
    expect(readRecentSearches()).toEqual([])
  })

  it("puts the newest term first, deduplicates and caps the list", () => {
    const stored = ["一", "二", "三", "四", "五"]
    const next = rememberSearch("  二  ", stored)

    expect(next).toEqual(["二", "一", "三", "四", "五"])
    expect(JSON.parse(window.localStorage.getItem(RECENT_SEARCHES_KEY) ?? "[]")).toEqual(next)
    expect(next).toHaveLength(MAX_RECENT_SEARCHES)
    expect(SEARCH_TYPES).toContain("case")
  })

  it("ignores a blank term", () => {
    const stored = ["一"]
    expect(rememberSearch("   ", stored)).toEqual(stored)
  })
})

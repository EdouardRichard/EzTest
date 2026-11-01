import { beforeEach, describe, expect, it, vi } from "vitest"

const client = vi.hoisted(() => ({ apiGetPage: vi.fn() }))
vi.mock("@/lib/api-client", () => client)

import { listGlobalIssues, listGlobalReports } from "./global-api"

function lastPath(): URL {
  const call = client.apiGetPage.mock.calls.at(-1)
  if (call === undefined) throw new Error("apiGetPage was not called")
  return new URL(String(call[0]), "http://localhost")
}

describe("global-api", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    client.apiGetPage.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
  })

  it("builds the filtered global issue list path", async () => {
    await listGlobalIssues({
      versionId: "42",
      status: "open",
      severity: "major",
      category: "api",
      q: "结算",
      before: "900",
      limit: 10,
    })

    const url = lastPath()
    expect(url.pathname).toBe("/api/issues")
    expect(url.searchParams.get("version_id")).toBe("42")
    expect(url.searchParams.get("status")).toBe("open")
    expect(url.searchParams.get("severity")).toBe("major")
    expect(url.searchParams.get("category")).toBe("api")
    expect(url.searchParams.get("q")).toBe("结算")
    expect(url.searchParams.get("before")).toBe("900")
    expect(url.searchParams.get("limit")).toBe("10")
  })

  it("defaults the global issue page size to 20 and omits empty filters", async () => {
    await listGlobalIssues()

    const url = lastPath()
    expect(url.pathname).toBe("/api/issues")
    expect(url.searchParams.get("limit")).toBe("20")
    expect(url.searchParams.has("status")).toBe(false)
    expect(url.searchParams.has("version_id")).toBe(false)
    expect(url.searchParams.has("q")).toBe(false)
  })

  it("builds the filtered global report list path", async () => {
    await listGlobalReports({
      versionId: "43",
      status: "stale",
      kind: "wave",
      before: "800",
      limit: 5,
    })

    const url = lastPath()
    expect(url.pathname).toBe("/api/reports")
    expect(url.searchParams.get("version_id")).toBe("43")
    expect(url.searchParams.get("status")).toBe("stale")
    expect(url.searchParams.get("kind")).toBe("wave")
    expect(url.searchParams.get("before")).toBe("800")
    expect(url.searchParams.get("limit")).toBe("5")
  })
})

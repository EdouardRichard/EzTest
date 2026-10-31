import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { createVersion, getResourceCatalog, getVersion, listVersions } from "./api"
import { catalogFixture, versionFixture, versionSummaryFixture } from "./test-fixtures"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

describe("versions API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("lists versions through the pagination envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [versionSummaryFixture],
        next_cursor: null,
        has_more: false,
      }),
    )

    await expect(listVersions()).resolves.toEqual([versionSummaryFixture])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions?limit=100")
    expect(init?.method).toBe("GET")
  })

  it("loads the resource catalog for the binding wizard", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: catalogFixture }))

    await expect(getResourceCatalog()).resolves.toEqual(catalogFixture)

    const [url] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/resources/catalog")
  })

  it("loads one version by its string id", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: versionFixture }))

    await expect(getVersion("2001")).resolves.toEqual(versionFixture)

    const [url] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001")
  })

  it("creates a version with a POST body", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: versionFixture }, 201))
    const payload = {
      title: "订单中心 · 2026 Q4 迭代",
      bindings: [
        {
          sut_id: "1001",
          environment_id: "1101",
          page_ids: ["1301"],
          api_ids: [],
          database_ids: [],
          accounts: [],
        },
      ],
    }

    await expect(createVersion(payload)).resolves.toEqual(versionFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions")
    expect(init?.method).toBe("POST")
    expect(JSON.parse(String(init?.body))).toEqual(payload)
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import {
  approveVisualBaseline,
  getVisualAssertion,
  getVisualDiff,
  getVisualMetrics,
  getVisualOverview,
  listVisualAssertions,
  listVisualBaselines,
  listVisualDiffs,
  upsertVisualAssertion,
} from "../api"
import {
  visualAssertionFixture,
  visualBaselineFixture,
  visualDiffDetailFixture,
  visualDiffFailedFixture,
  visualMetricsFixture,
  visualOverviewFixture,
} from "../test-fixtures"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

describe("visual API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
  })

  it("lists assertions with the case-version filter through the cursor envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [visualAssertionFixture],
        next_cursor: null,
        has_more: false,
      }),
    )

    await expect(
      listVisualAssertions("2001", { caseVersionId: "7002", versionSutId: "1002" }),
    ).resolves.toEqual([visualAssertionFixture])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(
      "http://localhost:13333/api/versions/2001/visual/assertions?caseVersionId=7002&versionSutId=1002&limit=100",
    )
    expect(init?.method).toBe("GET")
  })

  it("reads one assertion by id", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: visualAssertionFixture }))

    await expect(getVisualAssertion("2001", "7101")).resolves.toEqual(visualAssertionFixture)

    const [url] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/visual/assertions/7101")
  })

  it("upserts a config with the camelCase request body", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: visualAssertionFixture }, 200))

    await expect(
      upsertVisualAssertion("2001", "6101", {
        versionSutId: "1002",
        selector: "#order-total",
        threshold: 0.005,
        viewport: { width: 1280, height: 720 },
        deviceScaleFactor: 1,
      }),
    ).resolves.toEqual(visualAssertionFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/visual/assertions/6101")
    expect(init?.method).toBe("PUT")
    expect(JSON.parse(String(init?.body))).toEqual({
      versionSutId: "1002",
      selector: "#order-total",
      threshold: 0.005,
      viewport: { width: 1280, height: 720 },
      deviceScaleFactor: 1,
    })
  })

  it("lists baselines and approves one from actual evidence", async () => {
    fetchMock
      .mockResolvedValueOnce(
        jsonResponse({ data: [visualBaselineFixture], next_cursor: null, has_more: false }),
      )
      .mockResolvedValueOnce(jsonResponse({ data: visualBaselineFixture }, 201))

    await expect(listVisualBaselines("2001", "7101")).resolves.toEqual([visualBaselineFixture])
    await expect(approveVisualBaseline("2001", "7101", "9401")).resolves.toEqual(
      visualBaselineFixture,
    )

    const [listUrl] = fetchMock.mock.calls[0]
    expect(listUrl).toBe(
      "http://localhost:13333/api/versions/2001/visual/assertions/7101/baselines?limit=100",
    )
    const [approveUrl, approveInit] = fetchMock.mock.calls[1]
    expect(approveUrl).toBe(
      "http://localhost:13333/api/versions/2001/visual/assertions/7101/baselines",
    )
    expect(approveInit?.method).toBe("POST")
    expect(JSON.parse(String(approveInit?.body))).toEqual({ evidenceId: "9401" })
  })

  it("lists diffs with cursor filters and loads one detail", async () => {
    fetchMock
      .mockResolvedValueOnce(
        jsonResponse({
          data: [visualDiffFailedFixture],
          next_cursor: "7299",
          has_more: true,
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ data: visualDiffDetailFixture }))

    await expect(
      listVisualDiffs("2001", {
        caseRunId: "9301",
        classification: "failed",
        cursor: "7300",
        limit: 20,
      }),
    ).resolves.toEqual([visualDiffFailedFixture])
    await expect(getVisualDiff("2001", "7302")).resolves.toEqual(visualDiffDetailFixture)

    const [url] = fetchMock.mock.calls[0]
    expect(url).toBe(
      "http://localhost:13333/api/versions/2001/visual/diffs?caseRunId=9301&classification=failed&cursor=7300&limit=20",
    )
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/visual/diffs/7302",
    )
  })

  it("reads the version overview and the global/version quality metrics", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: visualOverviewFixture }))
      .mockResolvedValueOnce(jsonResponse({ data: visualMetricsFixture }))
      .mockResolvedValueOnce(jsonResponse({ data: visualMetricsFixture }))

    await expect(getVisualOverview("2001")).resolves.toEqual(visualOverviewFixture)
    await expect(getVisualMetrics("2001")).resolves.toEqual(visualMetricsFixture)
    await expect(getVisualMetrics()).resolves.toEqual(visualMetricsFixture)

    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/visual/overview",
    )
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/quality/visual-metrics?versionId=2001",
    )
    expect(fetchMock.mock.calls[2][0]).toBe("http://localhost:13333/api/quality/visual-metrics")
  })

  it("maps the frozen 403 envelope for non-admin baseline approval", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "forbidden",
            message: "admin only",
            request_id: "req_visual",
            retryable: false,
            details: {},
          },
        }),
        { status: 403, headers: { "Content-Type": "application/json" } },
      ),
    )

    const failure = approveVisualBaseline("2001", "7101", "9401")
    await expect(failure).rejects.toBeInstanceOf(ApiError)
    await expect(failure).rejects.toMatchObject({ status: 403, code: "forbidden" })
  })
})

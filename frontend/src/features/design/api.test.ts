import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError, apiDownload } from "@/lib/api-client"
import { getToken } from "@/lib/auth-storage"

import {
  confirmCases,
  createDesignConversation,
  createExport,
  downloadExport,
  getDesignCase,
  getDesignGraph,
  getDesignOverview,
  listDesignConversations,
  listExports,
} from "./api"
import {
  designCaseDetailFixture,
  designGraphFixture,
  designOverviewFixture,
  exportViewFixture,
} from "./test-fixtures"
import type { CaseConfirmationResult } from "./types"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function errorResponse(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({
      error: { code, message, request_id: "req_design", retryable: false, details: {} },
    }),
    { status, headers: { "Content-Type": "application/json" } },
  )
}

function binaryResponse(
  body: string,
  headers: Record<string, string> = {},
  status = 200,
): Response {
  return new Response(body, {
    status,
    headers: { "Content-Type": "application/octet-stream", ...headers },
  })
}

function readBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string") return undefined
  return JSON.parse(init.body) as unknown
}

const fetchMock = vi.fn<typeof fetch>()

describe("design api", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
    window.sessionStorage.setItem("eztest.token", "ezt_token")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("lists design conversations with kind=design", async () => {
    const conversation = {
      id: "4009",
      title: "用例设计 2026-10-31 12:00",
      kind: "design",
      status: "active",
      message_count: 0,
      last_message_at: null,
      created_at: "2026-10-31T12:00:00Z",
    }
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [conversation], next_cursor: "", has_more: false }),
    )

    await expect(listDesignConversations("2001")).resolves.toEqual([conversation])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/conversations?kind=design&limit=100")
    expect(init?.method).toBe("GET")
  })

  it("creates a design conversation without an explicit title", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: { id: "4009" } }, 201))

    await expect(createDesignConversation("2001")).resolves.toEqual({ id: "4009" })

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/conversations")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({ kind: "design" })
  })

  it("loads the whole-version design graph", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: designGraphFixture }))

    await expect(getDesignGraph("2001")).resolves.toEqual(designGraphFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/design/graph")
    expect(init?.method).toBe("GET")
  })

  it("loads the design overview with the active round", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: designOverviewFixture }))

    await expect(getDesignOverview("2001")).resolves.toEqual(designOverviewFixture)
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/design/overview",
    )
  })

  it("loads one case detail and forwards the selected case version", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: designCaseDetailFixture }))

    await expect(getDesignCase("2001", "7001", "7002")).resolves.toEqual(designCaseDetailFixture)

    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/design/cases/7001?version_id=7002",
    )

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: designCaseDetailFixture }))
    await getDesignCase("2001", "7001")
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/design/cases/7001",
    )
  })

  it("confirms a case/scenario/requirement scope and returns the batch result", async () => {
    const result: CaseConfirmationResult = {
      scope: "scenario",
      target_id: "5001",
      batch_id: "batch_ab12",
      confirmed: [{ case_id: "7001", case_version_id: "7002" }],
      skipped: [{ case_id: "7003", case_version_id: "7004", status: "confirmed" }],
      confirmed_at: "2026-10-31T12:05:00Z",
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: result }))

    await expect(confirmCases("2001", { scope: "scenario", target_id: "5001" })).resolves.toEqual(
      result,
    )

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/design/confirmations")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({ scope: "scenario", target_id: "5001" })
  })

  it("creates a whole-version export and lists previous exports", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: exportViewFixture }, 201))

    await expect(
      createExport("2001", {
        owner_kind: "case",
        owner_id: "2001",
        format: "markdown",
      }),
    ).resolves.toEqual(exportViewFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/exports")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({
      owner_kind: "case",
      owner_id: "2001",
      format: "markdown",
    })

    fetchMock.mockResolvedValueOnce(
      jsonResponse({ data: [exportViewFixture], next_cursor: "", has_more: false }),
    )
    await expect(
      listExports("2001", { format: "markdown", limit: 20, before: "9000" }),
    ).resolves.toEqual([exportViewFixture])
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/exports?format=markdown&before=9000&limit=20",
    )
  })

  it("downloads an export through the shared api-client with the bearer token", async () => {
    fetchMock.mockResolvedValue(
      binaryResponse("# 用例设计", {
        "Content-Disposition": 'attachment; filename="case-export-9001.md"',
      }),
    )

    const download = await downloadExport("9001")
    expect(download.filename).toBe("case-export-9001.md")
    expect(await download.blob.text()).toBe("# 用例设计")

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/exports/9001/download")
    expect(init?.method).toBe("GET")
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer ezt_token")
  })

  it("parses RFC 5987 filenames and defaults when the header is absent", async () => {
    fetchMock.mockResolvedValueOnce(
      binaryResponse("md", {
        "Content-Disposition": "attachment; filename*=UTF-8''case-%E7%94%A8%E4%BE%8B.md",
      }),
    )
    await expect(apiDownload("/api/exports/9002/download")).resolves.toMatchObject({
      filename: "case-用例.md",
    })

    fetchMock.mockResolvedValueOnce(binaryResponse("csv"))
    const fallback = await apiDownload("/api/exports/9003/download")
    expect(fallback.filename).toBe("")
    expect(fallback.blob.size).toBeGreaterThan(0)
  })

  it("clears the local session when a download is unauthenticated", async () => {
    expect(getToken()).toBe("ezt_token")
    fetchMock.mockResolvedValue(errorResponse(401, "unauthenticated", "登录已失效"))

    await expect(downloadExport("9001")).rejects.toBeInstanceOf(ApiError)
    expect(getToken()).toBeNull()
  })

  it("propagates the API error envelope for design calls", async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(409, "state_conflict", "该用例状态不可确认"))
    await expect(confirmCases("2001", { scope: "case", target_id: "7001" })).rejects.toMatchObject({
      code: "state_conflict",
      status: 409,
      message: "该用例状态不可确认",
      requestId: "req_design",
    })

    fetchMock.mockResolvedValueOnce(errorResponse(404, "not_found", "设计图不存在"))
    await expect(getDesignGraph("9999")).rejects.toMatchObject({ code: "not_found", status: 404 })
  })
})

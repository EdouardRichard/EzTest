import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import {
  createCodeAnchor,
  deleteCodeAnchor,
  listCodeAnchors,
  listVersionCodeRepos,
  validateCodeAnchor,
} from "../api"
import { anchorFixture, brokenAnchorFixture, staleAnchorFixture } from "../test-fixtures"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

describe("anchors API", () => {
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

  it("lists anchors for one subject through the cursor envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [anchorFixture, staleAnchorFixture, brokenAnchorFixture],
        next_cursor: null,
        has_more: false,
      }),
    )

    await expect(
      listCodeAnchors("2001", { subjectKind: "case_version", subjectId: "7002" }),
    ).resolves.toEqual([anchorFixture, staleAnchorFixture, brokenAnchorFixture])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(
      "http://localhost:13333/api/versions/2001/code/anchors?subjectKind=case_version&subjectId=7002&limit=100",
    )
    expect(init?.method).toBe("GET")
  })

  it("creates an anchor with the camelCase contract body", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: anchorFixture }, 201))

    await expect(
      createCodeAnchor("2001", {
        subjectKind: "case_version",
        subjectId: "7002",
        repoId: "5001",
        filePath: "src/pages/OrdersPage.tsx",
        startLine: 10,
        endLine: 24,
      }),
    ).resolves.toEqual(anchorFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/code/anchors")
    expect(init?.method).toBe("POST")
    expect(JSON.parse(String(init?.body))).toEqual({
      subjectKind: "case_version",
      subjectId: "7002",
      repoId: "5001",
      filePath: "src/pages/OrdersPage.tsx",
      startLine: 10,
      endLine: 24,
    })
  })

  it("validates and deletes one anchor", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: staleAnchorFixture }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await expect(validateCodeAnchor("2001", "8102")).resolves.toEqual(staleAnchorFixture)
    await expect(deleteCodeAnchor("2001", "8102")).resolves.toBeUndefined()

    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/anchors/8102/validate",
    )
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/anchors/8102",
    )
    expect(fetchMock.mock.calls[1][1]?.method).toBe("DELETE")
  })

  it("maps the version code repositories to selectable options", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [
          {
            repo_id: "5001",
            repository: { id: "5001", name: "order-center-web" },
            latest_index: null,
            baseline_commit: null,
            index_in_progress: false,
          },
        ],
        next_cursor: null,
        has_more: false,
      }),
    )

    await expect(listVersionCodeRepos("2001")).resolves.toEqual([
      { id: "5001", name: "order-center-web" },
    ])
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/repos?limit=100",
    )
  })

  it("maps the duplicate-anchor 409 envelope", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "anchor_duplicate",
            message: "anchor already exists",
            request_id: "req_anchor",
            retryable: false,
            details: {},
          },
        }),
        { status: 409, headers: { "Content-Type": "application/json" } },
      ),
    )

    const failure = createCodeAnchor("2001", {
      subjectKind: "issue",
      subjectId: "9201",
      repoId: "5001",
      filePath: "src/a.ts",
    })
    await expect(failure).rejects.toBeInstanceOf(ApiError)
    await expect(failure).rejects.toMatchObject({ status: 409, code: "anchor_duplicate" })
  })
})

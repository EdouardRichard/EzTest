import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { ApiError } from "@/lib/api-client"

import {
  createAnchor,
  createReview,
  deleteAnchor,
  generateProposals,
  getAnalysis,
  getCodeChanges,
  getCodeImpact,
  getCommit,
  getIndexRun,
  latestAnalysis,
  latestIndexRun,
  listAnchors,
  listCodeRepos,
  listDiff,
  listFacts,
  listProposals,
  listReviews,
  listTree,
  patchCandidate,
  readDiffFile,
  readFile,
  searchCode,
  startAnalysis,
  triggerIndex,
  validateAnchor,
} from "../api"
import {
  analysisDetailFixture,
  analysisViewFixture,
  anchorFixture,
  BASE_COMMIT,
  candidatesFixture,
  changedFilesFixture,
  changeSetFixture,
  diffFixture,
  factsFixture,
  fileWindowFixture,
  HEAD_COMMIT,
  indexRunFixture,
  proposalApiFixture,
  proposalPageFixture,
  repoStatusFixture,
  reviewFixture,
} from "../test-fixtures"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function errorResponse(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({
      error: { code, message, request_id: "req_code", retryable: false, details: {} },
    }),
    { status, headers: { "Content-Type": "application/json" } },
  )
}

function listResponse(data: unknown[]): Response {
  return jsonResponse({ data, next_cursor: "", has_more: false })
}

function pageResponse(data: unknown[], nextCursor = "", hasMore = false): Response {
  return jsonResponse({ data, next_cursor: nextCursor, has_more: hasMore })
}

function readBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string") return undefined
  return JSON.parse(init.body) as unknown
}

function headersOf(init: RequestInit | undefined): Headers {
  return new Headers(init?.headers)
}

const fetchMock = vi.fn<typeof fetch>()

describe("code-intel api", () => {
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

  it("lists the version's bound repositories with their index status", async () => {
    fetchMock.mockResolvedValue(listResponse([repoStatusFixture()]))

    await expect(listCodeRepos("2001")).resolves.toEqual([repoStatusFixture()])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/code/repos")
    expect(init?.method).toBe("GET")
    expect(headersOf(init).get("Authorization")).toBe("Bearer ezt_token")
  })

  it("starts a T0 index run with decimal-string ids and an optional commit", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: indexRunFixture({ status: "queued" }) }, 202))

    await expect(triggerIndex("2001", { repoId: "1201", commit: HEAD_COMMIT })).resolves.toEqual(
      indexRunFixture({ status: "queued" }),
    )

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/code/index")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({ repoId: "1201", commit: HEAD_COMMIT })
  })

  it("keeps index request ids as strings even above Number.MAX_SAFE_INTEGER", async () => {
    const hugeId = "9007199254740993"
    fetchMock.mockResolvedValue(jsonResponse({ data: indexRunFixture() }, 202))

    await triggerIndex("2001", { repoId: hugeId })

    expect(fetchMock.mock.calls[0][0]).toBe("http://localhost:13333/api/versions/2001/code/index")
    expect(readBody(fetchMock.mock.calls[0][1])).toEqual({ repoId: hugeId })
  })

  it("maps the 409 index_in_progress envelope to ApiError", async () => {
    fetchMock.mockResolvedValue(errorResponse(409, "index_in_progress", "索引运行中"))

    await expect(triggerIndex("2001", { repoId: "1201" })).rejects.toMatchObject({
      status: 409,
      code: "index_in_progress",
    } satisfies Partial<ApiError>)
    expect(fetchMock.mock.calls[0][0]).toBe("http://localhost:13333/api/versions/2001/code/index")
  })

  it("loads the latest index run and treats 404 as absent", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: indexRunFixture() }))
    await expect(latestIndexRun("1201")).resolves.toEqual(indexRunFixture())
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/code/repos/1201/index-runs/latest",
    )

    fetchMock.mockResolvedValueOnce(errorResponse(404, "not_found", "索引运行不存在"))
    await expect(latestIndexRun("1201")).resolves.toBeNull()
  })

  it("loads one index run by its decimal-string id", async () => {
    const hugeId = "9007199254740993"
    fetchMock.mockResolvedValue(jsonResponse({ data: indexRunFixture({ id: hugeId }) }))

    await expect(getIndexRun("1201", hugeId)).resolves.toEqual(indexRunFixture({ id: hugeId }))
    expect(fetchMock.mock.calls[0][0]).toBe(
      `http://localhost:13333/api/code/repos/1201/index-runs/${hugeId}`,
    )
  })

  it("lists the code tree with commit/path/cursor/limit query parameters", async () => {
    fetchMock.mockResolvedValue(
      pageResponse([{ path: "src/pages/RefundPage.tsx", name: "RefundPage.tsx", kind: "file" }]),
    )

    await expect(
      listTree("1201", {
        commit: HEAD_COMMIT,
        path: "src",
        cursor: "src/a.ts",
        limit: 200,
      }),
    ).resolves.toEqual({
      items: [{ path: "src/pages/RefundPage.tsx", name: "RefundPage.tsx", kind: "file" }],
      nextCursor: "",
      hasMore: false,
    })

    expect(fetchMock.mock.calls[0][0]).toBe(
      `http://localhost:13333/api/code/repos/1201/tree?commit=${HEAD_COMMIT}&path=src&cursor=src%2Fa.ts&limit=200`,
    )
  })

  it("reads an audited file window with optional 1-based line bounds", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: fileWindowFixture }))

    await expect(
      readFile("1201", {
        commit: HEAD_COMMIT,
        path: "src/pages/RefundPage.tsx",
        startLine: 12,
        endLine: 16,
      }),
    ).resolves.toEqual(fileWindowFixture)

    expect(fetchMock.mock.calls[0][0]).toBe(
      `http://localhost:13333/api/code/repos/1201/file?commit=${HEAD_COMMIT}&path=src%2Fpages%2FRefundPage.tsx&startLine=12&endLine=16`,
    )
  })

  it("pages changed files and fetches one on-demand file patch", async () => {
    fetchMock.mockResolvedValueOnce(pageResponse(changedFilesFixture, "src/api/refund.ts", true))
    await expect(
      listDiff("1201", { base: BASE_COMMIT, head: HEAD_COMMIT, limit: 200 }),
    ).resolves.toEqual({
      items: changedFilesFixture,
      nextCursor: "src/api/refund.ts",
      hasMore: true,
    })
    expect(fetchMock.mock.calls[0][0]).toBe(
      `http://localhost:13333/api/code/repos/1201/diff?base=${BASE_COMMIT}&head=${HEAD_COMMIT}&limit=200`,
    )

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: diffFixture() }))
    await expect(
      readDiffFile("1201", {
        base: BASE_COMMIT,
        head: HEAD_COMMIT,
        path: "src/pages/RefundPage.tsx",
      }),
    ).resolves.toEqual(diffFixture())
    expect(fetchMock.mock.calls[1][0]).toBe(
      `http://localhost:13333/api/code/repos/1201/diff/file?base=${BASE_COMMIT}&head=${HEAD_COMMIT}&path=src%2Fpages%2FRefundPage.tsx`,
    )
  })

  it("pages facts and searches entities with the pinned query names", async () => {
    fetchMock.mockResolvedValueOnce(pageResponse(factsFixture, "9102", true))
    await expect(
      listFacts("1201", {
        commit: HEAD_COMMIT,
        kind: "route",
        path: "src/pages/RefundPage.tsx",
        limit: 100,
      }),
    ).resolves.toEqual({ items: factsFixture, nextCursor: "9102", hasMore: true })
    expect(fetchMock.mock.calls[0][0]).toBe(
      `http://localhost:13333/api/code/repos/1201/facts?commit=${HEAD_COMMIT}&kind=route&path=src%2Fpages%2FRefundPage.tsx&limit=100`,
    )

    fetchMock.mockResolvedValueOnce(listResponse([]))
    await expect(
      searchCode("1201", { commit: HEAD_COMMIT, q: "refund", limit: 50 }),
    ).resolves.toEqual({ items: [], nextCursor: "", hasMore: false })
    expect(fetchMock.mock.calls[1][0]).toBe(
      `http://localhost:13333/api/code/repos/1201/search?commit=${HEAD_COMMIT}&q=refund&limit=50`,
    )
  })

  it("loads commit metadata with the sha kept as a string", async () => {
    const commit = {
      sha: HEAD_COMMIT,
      branch: "main",
      message: "feat: refund page",
      author_name: "Dev",
      authored_at: "2026-11-01T02:00:00Z",
      committed_at: "2026-11-01T02:00:00Z",
      parents: [BASE_COMMIT],
      is_head: true,
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: commit }))

    await expect(getCommit("1201", HEAD_COMMIT)).resolves.toEqual(commit)
    expect(fetchMock.mock.calls[0][0]).toBe(
      `http://localhost:13333/api/code/repos/1201/commits/${HEAD_COMMIT}`,
    )
  })

  it("previews the version change set from the SUT baseline to head", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: changeSetFixture }))

    await expect(getCodeChanges("2001", { repoId: "1201", head: HEAD_COMMIT })).resolves.toEqual(
      changeSetFixture,
    )

    expect(fetchMock.mock.calls[0][0]).toBe(
      `http://localhost:13333/api/versions/2001/code/changes?repoId=1201&head=${HEAD_COMMIT}`,
    )
  })

  it("starts an analysis with the camelCase contract body and maps 422", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: analysisViewFixture({ status: "queued" }) }, 202),
    )

    await expect(
      startAnalysis("2001", { repoId: "1201", headCommit: HEAD_COMMIT, force: true }),
    ).resolves.toEqual(analysisViewFixture({ status: "queued" }))
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/analyses",
    )
    expect(readBody(fetchMock.mock.calls[0][1])).toEqual({
      repoId: "1201",
      headCommit: HEAD_COMMIT,
      force: true,
    })

    fetchMock.mockResolvedValueOnce(
      errorResponse(422, "baseline_commit_missing", "版本尚未解析代码基线"),
    )
    await expect(startAnalysis("2001", { repoId: "1201" })).rejects.toMatchObject({
      status: 422,
      code: "baseline_commit_missing",
    })
  })

  it("loads the latest analysis and treats 404 as absent", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: analysisViewFixture() }))
    await expect(latestAnalysis("2001", "1201")).resolves.toEqual(analysisViewFixture())
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/analyses/latest?repoId=1201",
    )

    fetchMock.mockResolvedValueOnce(errorResponse(404, "not_found", "分析不存在"))
    await expect(latestAnalysis("2001", "1201")).resolves.toBeNull()
  })

  it("loads an analysis detail by its decimal-string id", async () => {
    const hugeId = "9007199254740993"
    fetchMock.mockResolvedValue(jsonResponse({ data: analysisDetailFixture() }))

    await expect(getAnalysis("2001", hugeId)).resolves.toEqual(analysisDetailFixture())
    expect(fetchMock.mock.calls[0][0]).toBe(
      `http://localhost:13333/api/versions/2001/code/analyses/${hugeId}`,
    )
  })

  it("patches one candidate selection", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: { ...candidatesFixture[0], selected: true } }))

    await expect(patchCandidate("2001", "7001", "8101", true)).resolves.toEqual({
      ...candidatesFixture[0],
      selected: true,
    })
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/analyses/7001/candidates/8101",
    )
    expect(fetchMock.mock.calls[0][1]?.method).toBe("PATCH")
    expect(readBody(fetchMock.mock.calls[0][1])).toEqual({ selected: true })
  })

  it("loads the merged impact view with and without a repo filter", async () => {
    const impact = {
      version_id: "2001",
      analyses: [
        {
          repo_id: "1201",
          analysis_id: "7001",
          base_commit_sha: BASE_COMMIT,
          head_commit_sha: HEAD_COMMIT,
          status: "done",
          degraded: false,
          candidates_count: 2,
        },
      ],
      candidates: candidatesFixture,
    }
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: impact }))

    await expect(getCodeImpact("2001")).resolves.toEqual(impact)
    expect(fetchMock.mock.calls[0][0]).toBe("http://localhost:13333/api/versions/2001/code/impact")

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: impact }))
    await getCodeImpact("2001", "1201")
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/impact?repoId=1201",
    )
  })

  it("lists, creates and validates anchors and deletes with 204", async () => {
    fetchMock.mockResolvedValueOnce(listResponse([anchorFixture]))
    await expect(
      listAnchors("2001", { subjectKind: "requirement", subjectId: "6001" }),
    ).resolves.toEqual([anchorFixture])
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/anchors?subjectKind=requirement&subjectId=6001",
    )

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: anchorFixture }, 201))
    await expect(
      createAnchor("2001", {
        subjectKind: "requirement",
        subjectId: "6001",
        repoId: "1201",
        filePath: "src/pages/RefundPage.tsx",
        startLine: 12,
        endLine: 16,
      }),
    ).resolves.toEqual(anchorFixture)
    expect(fetchMock.mock.calls[1][1]?.method).toBe("POST")
    expect(readBody(fetchMock.mock.calls[1][1])).toEqual({
      subjectKind: "requirement",
      subjectId: "6001",
      repoId: "1201",
      filePath: "src/pages/RefundPage.tsx",
      startLine: 12,
      endLine: 16,
    })

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: { ...anchorFixture, status: "stale" } }))
    await expect(validateAnchor("2001", "8201")).resolves.toEqual({
      ...anchorFixture,
      status: "stale",
    })
    expect(fetchMock.mock.calls[2][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/anchors/8201/validate",
    )
    expect(fetchMock.mock.calls[2][1]?.method).toBe("POST")

    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(deleteAnchor("2001", "8201")).resolves.toBeUndefined()
    expect(fetchMock.mock.calls[3][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/anchors/8201",
    )
    expect(fetchMock.mock.calls[3][1]?.method).toBe("DELETE")
  })

  it("lists append-only fact reviews and creates one verdict", async () => {
    fetchMock.mockResolvedValueOnce(listResponse([reviewFixture]))
    await expect(listReviews("2001", "9101")).resolves.toEqual([reviewFixture])
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/facts/9101/reviews",
    )

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: reviewFixture }, 201))
    await expect(
      createReview("2001", "9101", { verdict: "correct", reason: "行号准确" }),
    ).resolves.toEqual(reviewFixture)
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/facts/9101/reviews",
    )
    expect(readBody(fetchMock.mock.calls[1][1])).toEqual({
      verdict: "correct",
      reason: "行号准确",
    })
  })

  it("generates page/API proposals and lists the proposed resources", async () => {
    const result = {
      pages: { created: 1, skipped: 0, ids: ["3001"] },
      apis: { created: 1, skipped: 0, ids: ["3101"] },
    }
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: result }))
    await expect(generateProposals("2001", { repoId: "1201" })).resolves.toEqual(result)
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/page-api-proposals",
    )
    expect(readBody(fetchMock.mock.calls[0][1])).toEqual({ repoId: "1201" })

    fetchMock.mockResolvedValueOnce(listResponse([proposalPageFixture, proposalApiFixture]))
    await expect(listProposals("2001")).resolves.toEqual([proposalPageFixture, proposalApiFixture])
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/page-api-proposals",
    )
  })
})

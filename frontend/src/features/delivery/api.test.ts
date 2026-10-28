import { beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetPage: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiStreamFetch: vi.fn(),
}))

vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return {
    ...actual,
    apiGet: mocks.apiGet,
    apiGetPage: mocks.apiGetPage,
    apiPost: mocks.apiPost,
    apiPatch: mocks.apiPatch,
    apiStreamFetch: mocks.apiStreamFetch,
  }
})

import { ApiError } from "@/lib/api-client"

import {
  createIssue,
  describeDeliveryError,
  diffReports,
  ensureReport,
  exportReport,
  getIssue,
  getReport,
  listGlobalIssues,
  listGlobalReports,
  listIssues,
  listReportBatches,
  listReports,
  listVersionOptions,
  patchIssue,
  regenerateReport,
} from "./api"

/** Snowflake IDs stay decimal strings end-to-end (D29): never Number(). */
const VERSION_ID = "1844674407370955161"
const OTHER_VERSION_ID = "1844674407370955169"
const ISSUE_ID = "1844674407370955162"
const REPORT_ID = "1844674407370955163"
const BASE_REPORT_ID = "1844674407370955164"
const BATCH_ID = "1844674407370955165"

const BASE = `/api/versions/${VERSION_ID}`

function emptyPage<T>() {
  return { items: [] as T[], nextCursor: "", hasMore: false }
}

function markdownResponse(): Response {
  const headers = new Headers({
    "content-type": "text/markdown; charset=utf-8",
    "content-disposition": 'attachment; filename="report-3-r2.md"',
    "x-content-sha256": "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
  })
  return new Response("# report", { status: 200, headers })
}

describe("delivery api", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset()
    mocks.apiGetPage.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiPatch.mockReset()
    mocks.apiStreamFetch.mockReset()
  })

  it("lists issues with every filter, keeping IDs as decimal strings", async () => {
    mocks.apiGetPage.mockResolvedValueOnce(emptyPage())

    await listIssues(
      VERSION_ID,
      {
        status: "open",
        severity: "major",
        category: "defect",
        batchId: BATCH_ID,
        q: "login",
        limit: 20,
        cursor: "c1",
      },
      { signal: undefined },
    )

    expect(mocks.apiGetPage).toHaveBeenCalledWith(
      `${BASE}/issues?status=open&severity=major&category=defect&batchId=${BATCH_ID}` +
        "&q=login&limit=20&cursor=c1",
      {},
    )

    await listIssues(VERSION_ID)
    expect(mocks.apiGetPage).toHaveBeenLastCalledWith(`${BASE}/issues`, {})
  })

  it("reads, creates and patches an issue without re-parsing IDs", async () => {
    mocks.apiGet.mockResolvedValueOnce({ id: ISSUE_ID })
    mocks.apiPost.mockResolvedValueOnce({ id: ISSUE_ID })
    mocks.apiPatch.mockResolvedValueOnce({ id: ISSUE_ID, status: "dismissed" })

    await getIssue(VERSION_ID, ISSUE_ID)
    expect(mocks.apiGet).toHaveBeenCalledWith(`${BASE}/issues/${ISSUE_ID}`, {})

    await createIssue(VERSION_ID, {
      title: "登录失败",
      severity: "major",
      caseNodeId: "1844674407370955170",
      requirementId: "1844674407370955171",
    })
    expect(mocks.apiPost).toHaveBeenCalledWith(
      `${BASE}/issues`,
      {
        title: "登录失败",
        severity: "major",
        caseNodeId: "1844674407370955170",
        requirementId: "1844674407370955171",
      },
      {},
    )

    await patchIssue(VERSION_ID, ISSUE_ID, {
      status: "dismissed",
      expectedRevision: 3,
      comment: "误报",
    })
    expect(mocks.apiPatch).toHaveBeenCalledWith(
      `${BASE}/issues/${ISSUE_ID}`,
      { status: "dismissed", expectedRevision: 3, comment: "误报" },
      {},
    )
  })

  it("lists global issues with the optional version filter", async () => {
    mocks.apiGetPage.mockResolvedValueOnce(emptyPage())

    await listGlobalIssues({ versionId: OTHER_VERSION_ID, status: "open", limit: 50 })
    expect(mocks.apiGetPage).toHaveBeenCalledWith(
      `/api/issues?versionId=${OTHER_VERSION_ID}&status=open&limit=50`,
      {},
    )

    await listGlobalIssues()
    expect(mocks.apiGetPage).toHaveBeenLastCalledWith("/api/issues", {})
  })

  it("lists reports of a batch and reads one revision", async () => {
    mocks.apiGetPage.mockResolvedValueOnce(emptyPage())
    mocks.apiGet.mockResolvedValueOnce({ id: REPORT_ID })

    await listReports(VERSION_ID, { batchId: BATCH_ID, status: "published", limit: 10 })
    expect(mocks.apiGetPage).toHaveBeenCalledWith(
      `${BASE}/reports?batchId=${BATCH_ID}&status=published&limit=10`,
      {},
    )

    await getReport(VERSION_ID, REPORT_ID)
    expect(mocks.apiGet).toHaveBeenCalledWith(`${BASE}/reports/${REPORT_ID}`, {})
  })

  it("ensures and regenerates a report", async () => {
    mocks.apiPost.mockResolvedValueOnce({ id: REPORT_ID })
    mocks.apiPost.mockResolvedValueOnce({ id: REPORT_ID, revision: 2 })

    await ensureReport(VERSION_ID, BATCH_ID)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/reports`, { batchId: BATCH_ID }, {})

    await regenerateReport(VERSION_ID, REPORT_ID)
    expect(mocks.apiPost).toHaveBeenLastCalledWith(
      `${BASE}/reports/${REPORT_ID}/regenerate`,
      undefined,
      {},
    )
  })

  it("diffs against the default base or an explicit base report", async () => {
    mocks.apiGet.mockResolvedValueOnce({ targetReportId: REPORT_ID })

    await diffReports(VERSION_ID, REPORT_ID)
    expect(mocks.apiGet).toHaveBeenCalledWith(`${BASE}/reports/${REPORT_ID}/diff`, {})

    await diffReports(VERSION_ID, REPORT_ID, BASE_REPORT_ID)
    expect(mocks.apiGet).toHaveBeenLastCalledWith(
      `${BASE}/reports/${REPORT_ID}/diff?baseReportId=${BASE_REPORT_ID}`,
      {},
    )
  })

  it("exports the Markdown revision with its filename and SHA256 header", async () => {
    mocks.apiStreamFetch.mockResolvedValueOnce(markdownResponse())

    const download = await exportReport(VERSION_ID, REPORT_ID)

    expect(mocks.apiStreamFetch).toHaveBeenCalledWith(
      `${BASE}/reports/${REPORT_ID}/export?format=markdown`,
      { method: "GET", headers: { Accept: "text/markdown" } },
    )
    expect(download.filename).toBe("report-3-r2.md")
    expect(download.sha256).toHaveLength(64)
    await expect(download.blob.text()).resolves.toBe("# report")
  })

  it("lists report batches and version options for the selectors", async () => {
    mocks.apiGetPage.mockResolvedValueOnce(emptyPage())
    mocks.apiGetPage.mockResolvedValueOnce(emptyPage())

    await listReportBatches(VERSION_ID)
    expect(mocks.apiGetPage).toHaveBeenCalledWith(`${BASE}/batches?limit=100`, {})

    await listVersionOptions()
    expect(mocks.apiGetPage).toHaveBeenLastCalledWith("/api/versions?limit=100", {})
  })

  it("lists global reports with the optional version filter", async () => {
    mocks.apiGetPage.mockResolvedValueOnce(emptyPage())

    await listGlobalReports({
      versionId: OTHER_VERSION_ID,
      status: "stale",
      limit: 20,
      cursor: "c",
    })
    expect(mocks.apiGetPage).toHaveBeenCalledWith(
      `/api/reports?versionId=${OTHER_VERSION_ID}&status=stale&limit=20&cursor=c`,
      {},
    )
  })

  it("maps stable reason codes to readable messages", () => {
    expect(
      describeDeliveryError(
        new ApiError({
          status: 409,
          code: "conflict",
          message: "revision mismatch",
          details: { reason: "issue_state_conflict" },
        }),
      ),
    ).toContain("已被更新")

    expect(
      describeDeliveryError(
        new ApiError({
          status: 409,
          code: "conflict",
          message: "stale",
          details: { reason: "report_stale" },
        }),
      ),
    ).toContain("过期")

    expect(
      describeDeliveryError(new ApiError({ status: 403, code: "forbidden", message: "forbidden" })),
    ).toContain("无权限")

    expect(
      describeDeliveryError(
        new ApiError({ status: 500, code: "internal", message: "export failed", retryable: true }),
      ),
    ).toContain("重试")

    expect(describeDeliveryError(new Error("boom"))).toBe("boom")
  })
})

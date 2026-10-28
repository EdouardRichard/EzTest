import { beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetPage: vi.fn(),
  apiPost: vi.fn(),
  apiStreamFetch: vi.fn(),
}))

vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return {
    ...actual,
    apiGet: mocks.apiGet,
    apiGetPage: mocks.apiGetPage,
    apiPost: mocks.apiPost,
    apiStreamFetch: mocks.apiStreamFetch,
  }
})

import { resolveApiUrl } from "@/lib/api-client"

import {
  cancelBatch,
  createBatch,
  exportReport,
  getBatch,
  getCaseRun,
  getEvidenceMeta,
  getEvidenceUrl,
  getReport,
  listBatches,
  listCaseRuns,
  listExecutionSessions,
  pauseBatch,
  rerunCase,
  resumeBatch,
} from "./api"

const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"
const CASE_RUN_ID = "1844674407370955163"
const EVIDENCE_ID = "1844674407370955164"
const CASE_ID = "1844674407370955165"

const BASE = `/api/versions/${VERSION_ID}`
const BATCH = `${BASE}/batches/${BATCH_ID}`

function responseWith(disposition?: string): Response {
  const headers = new Headers({ "content-type": "text/markdown" })
  if (disposition !== undefined) headers.set("content-disposition", disposition)
  return new Response("report-bytes", { status: 200, headers })
}

describe("execution api", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset()
    mocks.apiGetPage.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiStreamFetch.mockReset()
  })

  it("creates and lists batches without re-parsing IDs", async () => {
    mocks.apiPost.mockResolvedValueOnce({ id: BATCH_ID })
    mocks.apiGetPage.mockResolvedValueOnce({ items: [], nextCursor: "", hasMore: false })

    await expect(createBatch(VERSION_ID, { caseIds: [CASE_ID] })).resolves.toEqual({
      id: BATCH_ID,
    })
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/batches`, { caseIds: [CASE_ID] }, {})

    await listBatches(VERSION_ID)
    expect(mocks.apiGetPage).toHaveBeenCalledWith(`${BASE}/batches`, {})

    await listBatches(VERSION_ID, { status: "running", limit: 20, cursor: "c1" })
    expect(mocks.apiGetPage).toHaveBeenLastCalledWith(
      `${BASE}/batches?status=running&limit=20&cursor=c1`,
      {},
    )
  })

  it("covers batch detail, pause, resume and cancel", async () => {
    mocks.apiGet.mockResolvedValueOnce({ id: BATCH_ID })

    await getBatch(VERSION_ID, BATCH_ID)
    expect(mocks.apiGet).toHaveBeenCalledWith(BATCH, {})

    await pauseBatch(VERSION_ID, BATCH_ID)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BATCH}/pause`, { mode: "graceful" }, {})

    await pauseBatch(VERSION_ID, BATCH_ID, "immediate")
    expect(mocks.apiPost).toHaveBeenLastCalledWith(`${BATCH}/pause`, { mode: "immediate" }, {})

    await resumeBatch(VERSION_ID, BATCH_ID)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BATCH}/resume`, undefined, {})

    await cancelBatch(VERSION_ID, BATCH_ID)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BATCH}/cancel`, undefined, {})
  })

  it("lists and reads case runs, then reruns one", async () => {
    mocks.apiGetPage.mockResolvedValueOnce({ items: [], nextCursor: "", hasMore: false })
    mocks.apiGet.mockResolvedValueOnce({ id: CASE_RUN_ID })
    mocks.apiPost.mockResolvedValueOnce({ id: "1844674407370955166" })

    await listCaseRuns(VERSION_ID, BATCH_ID)
    expect(mocks.apiGetPage).toHaveBeenCalledWith(`${BATCH}/case-runs`, {})

    await listCaseRuns(VERSION_ID, BATCH_ID, { status: "failed", limit: 10 })
    expect(mocks.apiGetPage).toHaveBeenLastCalledWith(
      `${BATCH}/case-runs?status=failed&limit=10`,
      {},
    )

    await getCaseRun(VERSION_ID, BATCH_ID, CASE_RUN_ID)
    expect(mocks.apiGet).toHaveBeenCalledWith(`${BATCH}/case-runs/${CASE_RUN_ID}`, {})

    await rerunCase(VERSION_ID, BATCH_ID, CASE_RUN_ID)
    expect(mocks.apiPost).toHaveBeenCalledWith(
      `${BATCH}/case-runs/${CASE_RUN_ID}/rerun`,
      undefined,
      {},
    )
  })

  it("loads the batch report JSON", async () => {
    mocks.apiGet.mockResolvedValueOnce({ generated_at: "2026-10-28T00:00:00Z" })

    await getReport(VERSION_ID, BATCH_ID)
    expect(mocks.apiGet).toHaveBeenCalledWith(`${BATCH}/report`, {})
  })

  it("exports the report as a streamed download with the header filename", async () => {
    mocks.apiStreamFetch.mockResolvedValueOnce(
      responseWith('attachment; filename="batch-1-report.md"'),
    )

    const download = await exportReport(VERSION_ID, BATCH_ID)

    expect(mocks.apiStreamFetch).toHaveBeenCalledWith(`${BATCH}/report/export?format=markdown`, {
      method: "GET",
      headers: { Accept: "text/markdown" },
    })
    expect(download.filename).toBe("batch-1-report.md")
    await expect(download.blob.text()).resolves.toBe("report-bytes")
  })

  it("prefers the RFC 5987 export filename and falls back without a header", async () => {
    mocks.apiStreamFetch.mockResolvedValueOnce(
      responseWith(
        "attachment; filename=\"fallback.md\"; filename*=UTF-8''%E6%89%A7%E8%A1%8C%E6%8A%A5%E5%91%8A.md",
      ),
    )
    await expect(exportReport(VERSION_ID, BATCH_ID)).resolves.toMatchObject({
      filename: "执行报告.md",
    })

    mocks.apiStreamFetch.mockResolvedValueOnce(responseWith())
    await expect(exportReport(VERSION_ID, BATCH_ID)).resolves.toMatchObject({
      filename: `execution-report-${BATCH_ID}.md`,
    })
  })

  it("builds the authenticated evidence URL and loads evidence metadata", async () => {
    mocks.apiGet.mockResolvedValueOnce({ id: EVIDENCE_ID })

    expect(getEvidenceUrl(VERSION_ID, EVIDENCE_ID)).toBe(
      resolveApiUrl(`${BASE}/evidence/${EVIDENCE_ID}`),
    )

    await getEvidenceMeta(VERSION_ID, EVIDENCE_ID)
    expect(mocks.apiGet).toHaveBeenCalledWith(`${BASE}/evidence/${EVIDENCE_ID}/meta`, {})
  })

  it("lists execution sessions for the version channel", async () => {
    mocks.apiGetPage.mockResolvedValueOnce({ items: [], nextCursor: "", hasMore: false })

    await listExecutionSessions(VERSION_ID)
    expect(mocks.apiGetPage).toHaveBeenCalledWith(`${BASE}/execution/sessions`, {})

    await listExecutionSessions(VERSION_ID, { channel: `version:${VERSION_ID}`, limit: 20 })
    expect(mocks.apiGetPage).toHaveBeenLastCalledWith(
      `${BASE}/execution/sessions?channel=version%3A${VERSION_ID}&limit=20`,
      {},
    )
  })
})

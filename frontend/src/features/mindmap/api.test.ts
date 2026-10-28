import { beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetPage: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiPut: vi.fn(),
  apiDelete: vi.fn(),
  apiStreamFetch: vi.fn(),
}))

vi.mock("@/lib/api-client", () => ({
  apiGet: mocks.apiGet,
  apiGetPage: mocks.apiGetPage,
  apiPost: mocks.apiPost,
  apiPatch: mocks.apiPatch,
  apiPut: mocks.apiPut,
  apiDelete: mocks.apiDelete,
  apiStreamFetch: mocks.apiStreamFetch,
}))

import { mindmapApi } from "./api"

const VERSION_ID = "1844674407370955161"
const BASE = `/api/versions/${VERSION_ID}/mindmap`
const NODE_ID = "1844674407370955162"
const STEP_ID = "1844674407370955163"
const ASSERTION_ID = "1844674407370955164"
const SOURCE_ID = "1844674407370955165"
const ANCHOR_ID = "1844674407370955166"
const RUN_ID = "1844674407370955167"
const FINDING_ID = "1844674407370955169"
const EXPORT_ID = "1844674407370955170"
const REQUIREMENT_ID = "1844674407370955171"

function responseWith(disposition?: string): Response {
  const headers = new Headers()
  if (disposition !== undefined) headers.set("content-disposition", disposition)
  return new Response("file-bytes", { status: 200, headers })
}

describe("mindmap api", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset()
    mocks.apiGetPage.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiPatch.mockReset()
    mocks.apiPut.mockReset()
    mocks.apiDelete.mockReset()
    mocks.apiStreamFetch.mockReset()
  })

  it("loads the graph, optionally narrowing the includes", async () => {
    mocks.apiGet.mockResolvedValueOnce({ versionId: VERSION_ID })

    await expect(mindmapApi.getGraph(VERSION_ID)).resolves.toEqual({ versionId: VERSION_ID })
    expect(mocks.apiGet).toHaveBeenCalledWith(BASE)

    await mindmapApi.getGraph(VERSION_ID, { include: ["anchors", "findings"] })
    expect(mocks.apiGet).toHaveBeenLastCalledWith(`${BASE}?include=anchors%2Cfindings`)
  })

  it("covers the node write endpoints without numeric ID conversion", async () => {
    const createBody = {
      kind: "case" as const,
      title: "下单成功",
      requirementIds: [REQUIREMENT_ID],
      testData: { orderNo: "1" },
    }
    const updateBody = { title: "改名", revision: 3 }
    const orderBody = { parentId: NODE_ID, nodeIds: [NODE_ID, STEP_ID] }

    await mindmapApi.createNode(VERSION_ID, createBody)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/nodes`, createBody)

    await mindmapApi.updateNode(VERSION_ID, NODE_ID, updateBody)
    expect(mocks.apiPatch).toHaveBeenCalledWith(`${BASE}/nodes/${NODE_ID}`, updateBody)

    await mindmapApi.deleteNode(VERSION_ID, NODE_ID, 3)
    expect(mocks.apiDelete).toHaveBeenCalledWith(`${BASE}/nodes/${NODE_ID}?revision=3`)

    await mindmapApi.reorderNodes(VERSION_ID, orderBody)
    expect(mocks.apiPut).toHaveBeenCalledWith(`${BASE}/order`, orderBody)
  })

  it("covers the step endpoints", async () => {
    const stepBody = { action: "点击下单", expected: "生成订单", afterSeq: 1 }
    const updateBody = { action: "再次点击", nodeRevision: 2 }
    const orderBody = { stepIds: [STEP_ID] }

    await mindmapApi.createStep(VERSION_ID, NODE_ID, stepBody)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/nodes/${NODE_ID}/steps`, stepBody)

    await mindmapApi.updateStep(VERSION_ID, STEP_ID, updateBody)
    expect(mocks.apiPatch).toHaveBeenCalledWith(`${BASE}/steps/${STEP_ID}`, updateBody)

    await mindmapApi.deleteStep(VERSION_ID, STEP_ID, 2)
    expect(mocks.apiDelete).toHaveBeenCalledWith(`${BASE}/steps/${STEP_ID}?nodeRevision=2`)

    await mindmapApi.reorderSteps(VERSION_ID, NODE_ID, orderBody)
    expect(mocks.apiPut).toHaveBeenCalledWith(`${BASE}/nodes/${NODE_ID}/steps/order`, orderBody)
  })

  it("covers the assertion endpoints", async () => {
    const assertionBody = { kind: "ui" as const, expression: "订单号非空", stepId: STEP_ID }
    const updateBody = { expression: "订单号存在", nodeRevision: 2 }

    await mindmapApi.createAssertion(VERSION_ID, NODE_ID, assertionBody)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/nodes/${NODE_ID}/assertions`, assertionBody)

    await mindmapApi.updateAssertion(VERSION_ID, ASSERTION_ID, updateBody)
    expect(mocks.apiPatch).toHaveBeenCalledWith(`${BASE}/assertions/${ASSERTION_ID}`, updateBody)

    await mindmapApi.deleteAssertion(VERSION_ID, ASSERTION_ID, 2)
    expect(mocks.apiDelete).toHaveBeenCalledWith(
      `${BASE}/assertions/${ASSERTION_ID}?nodeRevision=2`,
    )
  })

  it("covers the source and anchor endpoints", async () => {
    const sourceBody = { refKind: "requirement" as const, refId: REQUIREMENT_ID }
    const anchorBody = {
      filePath: "src/order/submit.ts",
      startLine: 120,
      confidence: "high" as const,
    }

    await mindmapApi.createSource(VERSION_ID, NODE_ID, sourceBody)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/nodes/${NODE_ID}/sources`, sourceBody)

    await mindmapApi.deleteSource(VERSION_ID, SOURCE_ID)
    expect(mocks.apiDelete).toHaveBeenCalledWith(`${BASE}/sources/${SOURCE_ID}`)

    await mindmapApi.createAnchor(VERSION_ID, NODE_ID, anchorBody)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/nodes/${NODE_ID}/anchors`, anchorBody)

    await mindmapApi.deleteAnchor(VERSION_ID, ANCHOR_ID)
    expect(mocks.apiDelete).toHaveBeenCalledWith(`${BASE}/anchors/${ANCHOR_ID}`)
  })

  it("covers the run endpoints", async () => {
    const runBody = { kind: "design" as const, mode: "replace" as const }
    const page = { items: [], nextCursor: "c1", hasMore: true }
    mocks.apiGetPage.mockResolvedValueOnce(page)

    await mindmapApi.startRun(VERSION_ID, runBody)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/runs`, runBody)

    await expect(
      mindmapApi.listRuns(VERSION_ID, { kind: "design", status: "done", limit: 20, cursor: "c1" }),
    ).resolves.toBe(page)
    expect(mocks.apiGetPage).toHaveBeenCalledWith(
      `${BASE}/runs?kind=design&status=done&limit=20&cursor=c1`,
    )

    await mindmapApi.getRun(VERSION_ID, RUN_ID)
    expect(mocks.apiGet).toHaveBeenCalledWith(`${BASE}/runs/${RUN_ID}`)
  })

  it("covers the review and finding endpoints", async () => {
    const reviewBody = { decision: "approve" as const, acknowledgeFindings: true }
    const reviews = { items: [], nextCursor: "", hasMore: false }
    mocks.apiGetPage.mockResolvedValueOnce(reviews)

    await mindmapApi.submitReview(VERSION_ID)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/submit-review`, {})

    await mindmapApi.decideReview(VERSION_ID, reviewBody)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/review`, reviewBody)

    await expect(mindmapApi.listReviews(VERSION_ID, { limit: 10, cursor: "r1" })).resolves.toBe(
      reviews,
    )
    expect(mocks.apiGetPage).toHaveBeenCalledWith(`${BASE}/reviews?limit=10&cursor=r1`)

    await mindmapApi.listFindings(VERSION_ID, {
      status: "open",
      severity: "blocker",
      nodeId: NODE_ID,
    })
    expect(mocks.apiGet).toHaveBeenCalledWith(
      `${BASE}/findings?status=open&severity=blocker&nodeId=${NODE_ID}`,
    )

    await mindmapApi.updateFinding(VERSION_ID, FINDING_ID, { status: "fixed" })
    expect(mocks.apiPatch).toHaveBeenCalledWith(`${BASE}/findings/${FINDING_ID}`, {
      status: "fixed",
    })
  })

  it("returns the flat findings array and rejects an unexpected payload shape", async () => {
    const findings = [{ id: FINDING_ID }]
    mocks.apiGet.mockResolvedValueOnce(findings)

    await expect(mindmapApi.listFindings(VERSION_ID)).resolves.toBe(findings)
    expect(mocks.apiGet).toHaveBeenLastCalledWith(`${BASE}/findings`)

    // `data: null` is the only empty shape; it resolves to [].
    mocks.apiGet.mockResolvedValueOnce(null)
    await expect(mindmapApi.listFindings(VERSION_ID)).resolves.toEqual([])

    // Anything else is a contract violation: never hide findings as "none".
    mocks.apiGet.mockResolvedValueOnce({ items: [] })
    await expect(mindmapApi.listFindings(VERSION_ID)).rejects.toThrow(
      /GET \/findings returned an unexpected payload/,
    )
  })

  it("covers the export endpoints", async () => {
    const exportBody = { format: "markdown" as const }
    const exportsPage = { items: [], nextCursor: "", hasMore: false }
    mocks.apiGetPage.mockResolvedValueOnce(exportsPage)

    await mindmapApi.createExport(VERSION_ID, exportBody)
    expect(mocks.apiPost).toHaveBeenCalledWith(`${BASE}/export`, exportBody)

    await expect(
      mindmapApi.listExports(VERSION_ID, { format: "markdown", limit: 10 }),
    ).resolves.toBe(exportsPage)
    expect(mocks.apiGetPage).toHaveBeenCalledWith(`${BASE}/exports?format=markdown&limit=10`)
  })

  it("downloads an export and reads the attachment filename", async () => {
    mocks.apiStreamFetch.mockResolvedValueOnce(
      responseWith('attachment; filename="case-mindmap.md"'),
    )

    const downloaded = await mindmapApi.downloadExport(VERSION_ID, EXPORT_ID)

    expect(mocks.apiStreamFetch).toHaveBeenCalledWith(`${BASE}/exports/${EXPORT_ID}/download`, {
      method: "GET",
      headers: { Accept: "*/*" },
    })
    expect(downloaded.filename).toBe("case-mindmap.md")
    await expect(downloaded.blob.text()).resolves.toBe("file-bytes")
  })

  it("prefers the RFC 5987 filename* value and decodes it", async () => {
    mocks.apiStreamFetch.mockResolvedValueOnce(
      responseWith(
        "attachment; filename=\"fallback.md\"; filename*=UTF-8''%E7%94%A8%E4%BE%8B%E8%84%91%E5%9B%BE.md",
      ),
    )

    const downloaded = await mindmapApi.downloadExport(VERSION_ID, EXPORT_ID)

    expect(downloaded.filename).toBe("用例脑图.md")
  })

  it("falls back to the caller-provided name when the header is absent", async () => {
    mocks.apiStreamFetch.mockResolvedValueOnce(responseWith())

    const downloaded = await mindmapApi.downloadExport(VERSION_ID, EXPORT_ID, "mindmap.opml")

    expect(downloaded.filename).toBe("mindmap.opml")
  })

  it("falls back to an export-specific name without a header or explicit name", async () => {
    mocks.apiStreamFetch.mockResolvedValueOnce(responseWith("attachment"))

    const downloaded = await mindmapApi.downloadExport(VERSION_ID, EXPORT_ID)

    expect(downloaded.filename).toBe(`mindmap-export-${EXPORT_ID}`)
  })
})

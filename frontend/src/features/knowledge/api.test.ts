import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  createKnowledge,
  listGlobalKnowledge,
  listVersionKnowledge,
  normalizeKnowledgeEntries,
  reviewKnowledge,
  updateKnowledge,
} from "./api"

function jsonResponse(body: unknown, status = 200): Response {
  const text = JSON.stringify(body)
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers({ "content-type": "application/json" }),
    text: async () => text,
    json: async () => body,
  } as unknown as Response
}

const ENTRY = {
  id: "1844674407370955161",
  versionId: "100",
  versionName: "订单系统",
  kind: "pitfall",
  title: "支付超时必须用假时钟",
  body: "沙箱网关 30s 超时，测试请使用假时钟。",
  tags: ["payment"],
  sourceRefs: [{ kind: "requirement", id: "200" }],
  status: "approved",
  confidence: "high",
  createdBy: 42,
  reviewedBy: 43,
  reviewNote: "符合沙箱行为",
  revision: 2,
  createdAt: "2026-10-29T10:00:00Z",
  updatedAt: "2026-10-29T11:00:00Z",
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("knowledge api", () => {
  it("reads one version's knowledge page and normalizes the payload", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [ENTRY, { ...ENTRY, id: "2", tags: null, sourceRefs: null, reviewedBy: null }],
        next_cursor: "opaque",
        has_more: true,
      }),
    )

    const page = await listVersionKnowledge("100", {
      status: "approved",
      kind: "pitfall",
      limit: 5,
    })

    const url = String(fetchMock.mock.calls[0]?.[0])
    expect(url).toContain("/api/versions/100/knowledge")
    expect(url).toContain("status=approved")
    expect(url).toContain("kind=pitfall")
    expect(url).toContain("limit=5")
    expect(page.hasMore).toBe(true)
    expect(page.nextCursor).toBe("opaque")
    expect(page.items).toHaveLength(2)
    expect(page.items[0]?.sourceRefs).toEqual([{ kind: "requirement", id: "200" }])
    expect(page.items[1]?.tags).toEqual([])
    expect(page.items[1]?.sourceRefs).toEqual([])
  })

  it("defaults the global read to approved and maps the version filter", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [ENTRY], has_more: false }))

    await listGlobalKnowledge({ versionId: "100", kind: "decision", limit: 20 })

    const url = String(fetchMock.mock.calls[0]?.[0])
    expect(url).toContain("/api/knowledge")
    expect(url).toContain("status=approved")
    expect(url).toContain("versionId=100")
    expect(url).toContain("kind=decision")

    await listGlobalKnowledge({ status: "draft", limit: 50 })
    const draftUrl = String(fetchMock.mock.calls[1]?.[0])
    expect(draftUrl).toContain("status=draft")
  })

  it("creates, updates and reviews entries with decimal string ids in the URL", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: ENTRY }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: ENTRY }))
      .mockResolvedValueOnce(jsonResponse({ data: { ...ENTRY, status: "rejected" } }))

    await createKnowledge("100", {
      kind: "pitfall",
      title: "支付超时",
      body: "使用假时钟",
      confidence: "high",
      tags: ["payment"],
      sourceRefs: [{ kind: "requirement", id: "200" }],
    })
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/versions/100/knowledge")
    const createInit = fetchMock.mock.calls[0]?.[1] as RequestInit
    expect(createInit.method).toBe("POST")
    expect(JSON.parse(String(createInit.body))).toMatchObject({
      kind: "pitfall",
      title: "支付超时",
    })

    await updateKnowledge("100", "9007199254740994", { revision: 2, title: "新标题" })
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain(
      "/api/versions/100/knowledge/9007199254740994",
    )
    const updateInit = fetchMock.mock.calls[1]?.[1] as RequestInit
    expect(updateInit.method).toBe("PATCH")
    expect(JSON.parse(String(updateInit.body))).toEqual({ revision: 2, title: "新标题" })

    await reviewKnowledge("100", "9007199254740994", {
      revision: 2,
      status: "rejected",
      reason: "重复条目",
    })
    expect(String(fetchMock.mock.calls[2]?.[0])).toContain(
      "/api/versions/100/knowledge/9007199254740994/review",
    )
    const reviewInit = fetchMock.mock.calls[2]?.[1] as RequestInit
    expect(reviewInit.method).toBe("POST")
    expect(JSON.parse(String(reviewInit.body))).toEqual({
      revision: 2,
      status: "rejected",
      reason: "重复条目",
    })
  })

  it("normalizes a partial or malformed entry list without inventing fields", () => {
    const items = normalizeKnowledgeEntries([
      null,
      { id: 12, title: "short shape" },
      { ...ENTRY, status: "unknown-status", kind: "unknown-kind" },
    ])
    expect(items).toHaveLength(2)
    expect(items[0]?.id).toBe("")
    expect(items[0]?.title).toBe("short shape")
    expect(items[0]?.status).toBe("draft")
    expect(items[0]?.confidence).toBe("medium")
    expect(items[1]?.status).toBe("draft")
    expect(items[1]?.kind).toBe("pattern")
  })
})

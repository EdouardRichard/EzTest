import { beforeEach, describe, expect, it, vi } from "vitest"

const client = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetPage: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
}))
vi.mock("@/lib/api-client", () => client)

import {
  approveKnowledge,
  createKnowledge,
  getKnowledge,
  listKnowledge,
  listKnowledgeRevisions,
  rejectKnowledge,
  revokeKnowledge,
  updateKnowledge,
} from "./api"

function lastPath(): URL {
  const call = [
    ...client.apiGet.mock.calls,
    ...client.apiGetPage.mock.calls,
    ...client.apiPost.mock.calls,
    ...client.apiPatch.mock.calls,
  ].at(-1)
  if (call === undefined) throw new Error("the api client was not called")
  return new URL(String(call[0]), "http://localhost")
}

describe("knowledge api", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    client.apiGet.mockResolvedValue({})
    client.apiGetPage.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    client.apiPost.mockResolvedValue({})
    client.apiPatch.mockResolvedValue({})
  })

  it("builds the filtered knowledge list path with the default page size", async () => {
    await listKnowledge({
      scope: "system",
      status: "approved",
      systemKey: "order-center",
      tag: "结算",
      q: "校验",
      limit: 10,
    })

    const url = lastPath()
    expect(url.pathname).toBe("/api/knowledge/items")
    expect(url.searchParams.get("scope")).toBe("system")
    expect(url.searchParams.get("status")).toBe("approved")
    expect(url.searchParams.get("system_key")).toBe("order-center")
    expect(url.searchParams.get("tag")).toBe("结算")
    expect(url.searchParams.get("q")).toBe("校验")
    expect(url.searchParams.get("limit")).toBe("10")
    expect(url.searchParams.has("cursor")).toBe(false)
  })

  it("defaults the page size to 20, omits empty filters and appends the cursor", async () => {
    await listKnowledge(undefined, "7000")

    const url = lastPath()
    expect(url.pathname).toBe("/api/knowledge/items")
    expect(url.searchParams.get("limit")).toBe("20")
    expect(url.searchParams.get("cursor")).toBe("7000")
    expect(url.searchParams.has("scope")).toBe(false)
    expect(url.searchParams.has("status")).toBe(false)
    expect(url.searchParams.has("system_key")).toBe(false)
    expect(url.searchParams.has("tag")).toBe(false)
    expect(url.searchParams.has("q")).toBe(false)
  })

  it("targets one item detail and its append-only revisions", async () => {
    await getKnowledge("7001")
    expect(lastPath().pathname).toBe("/api/knowledge/items/7001")

    client.apiGetPage.mockResolvedValue({
      items: [{ id: "1", knowledge_id: "7001", revision: 1 }],
      nextCursor: "",
      hasMore: false,
    })
    const revisions = await listKnowledgeRevisions("7001")
    expect(lastPath().pathname).toBe("/api/knowledge/items/7001/revisions")
    expect(revisions).toHaveLength(1)
  })

  it("creates a draft through POST /api/knowledge/items", async () => {
    await createKnowledge({
      title: "结算校验",
      content: "提交前必须校验支付方式。",
      scope: "global",
      system_key: null,
      tags: ["结算"],
    })

    const url = lastPath()
    expect(url.pathname).toBe("/api/knowledge/items")
    const [path, body] = client.apiPost.mock.calls.at(-1) ?? []
    expect(String(path)).toBe("/api/knowledge/items")
    expect(body).toEqual({
      title: "结算校验",
      content: "提交前必须校验支付方式。",
      scope: "global",
      system_key: null,
      tags: ["结算"],
    })
  })

  it("edits an item through PATCH", async () => {
    await updateKnowledge("7001", { title: "新标题" })

    expect(lastPath().pathname).toBe("/api/knowledge/items/7001")
    expect(client.apiPatch.mock.calls.at(-1)).toEqual([
      "/api/knowledge/items/7001",
      { title: "新标题" },
    ])
  })

  it("posts the review transitions to their admin paths", async () => {
    await approveKnowledge("7001")
    expect(lastPath().pathname).toBe("/api/knowledge/items/7001/approve")

    await rejectKnowledge("7001", "标题不清晰")
    expect(lastPath().pathname).toBe("/api/knowledge/items/7001/reject")
    expect(client.apiPost.mock.calls.at(-1)).toEqual([
      "/api/knowledge/items/7001/reject",
      { reason: "标题不清晰" },
    ])

    await revokeKnowledge("7001", "来源版本已废弃")
    expect(lastPath().pathname).toBe("/api/knowledge/items/7001/revoke")
    expect(client.apiPost.mock.calls.at(-1)).toEqual([
      "/api/knowledge/items/7001/revoke",
      { reason: "来源版本已废弃" },
    ])
  })
})

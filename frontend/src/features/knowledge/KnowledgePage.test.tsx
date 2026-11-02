import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { KnowledgeDetailView, KnowledgeItemView } from "./types"

const knowledgeApi = vi.hoisted(() => ({
  listKnowledge: vi.fn(),
  getKnowledge: vi.fn(),
  createKnowledge: vi.fn(),
  updateKnowledge: vi.fn(),
  approveKnowledge: vi.fn(),
  rejectKnowledge: vi.fn(),
  revokeKnowledge: vi.fn(),
  listKnowledgeRevisions: vi.fn(),
}))
vi.mock("./api", () => knowledgeApi)

const auth = vi.hoisted(() => ({ isAdmin: false, userId: "9002" }))
vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => auth.isAdmin }))
vi.mock("@/lib/auth-storage", () => ({
  getStoredUser: () => ({
    id: auth.userId,
    username: "tester",
    display_name: "tester",
    role: auth.isAdmin ? "admin" : "user",
    status: "active",
  }),
}))

import { KnowledgePage } from "./KnowledgePage"

/* --------------------------------- fixtures -------------------------------- */

const createdAt = "2026-11-01T02:00:00Z"
const updatedAt = "2026-11-01T06:00:00Z"

const draftItem: KnowledgeItemView = {
  id: "7001",
  scope: "system",
  system_key: "order-center",
  title: "结算页提交前校验",
  content: "提交前必须校验支付方式，否则提示用户补充。",
  tags: ["结算", "支付"],
  status: "draft",
  confidence: null,
  source_version_id: "2001",
  promoted_from_version_id: "2001",
  revision: 2,
  review_reason: null,
  reviewed_by: null,
  reviewed_at: null,
  revoked_at: null,
  source_stale: false,
  created_by: "9002",
  created_at: createdAt,
  updated_at: updatedAt,
}

const approvedItem: KnowledgeItemView = {
  ...draftItem,
  id: "7002",
  title: "全局：所有接口必须带 request id",
  scope: "global",
  system_key: null,
  status: "approved",
  tags: ["接口"],
  source_version_id: null,
  promoted_from_version_id: null,
  revision: 1,
  reviewed_by: "9001",
  reviewed_at: updatedAt,
  source_stale: false,
}

const draftDetail: KnowledgeDetailView = {
  ...draftItem,
  revisions: [
    {
      id: "8002",
      knowledge_id: "7001",
      revision: 2,
      title: draftItem.title,
      content: draftItem.content,
      tags: draftItem.tags,
      changed_by: "9002",
      created_at: updatedAt,
    },
    {
      id: "8001",
      knowledge_id: "7001",
      revision: 1,
      title: "结算页校验",
      content: "旧内容",
      tags: ["结算"],
      changed_by: "9002",
      created_at: createdAt,
    },
  ],
}

function page(items: KnowledgeItemView[], nextCursor = "", hasMore = false) {
  return { items, nextCursor, hasMore }
}

/* ---------------------------------- setup ---------------------------------- */

beforeEach(() => {
  vi.clearAllMocks()
  auth.isAdmin = false
  auth.userId = "9002"
  knowledgeApi.listKnowledge.mockResolvedValue(page([draftItem, approvedItem]))
  knowledgeApi.getKnowledge.mockResolvedValue(draftDetail)
})

afterEach(cleanup)

/* ---------------------------------- tests ---------------------------------- */

describe("KnowledgePage", () => {
  it("renders the knowledge list with scope, status, revision and tags", async () => {
    render(<KnowledgePage />)

    expect(screen.getByTestId("knowledge-page")).toBeInTheDocument()
    expect(await screen.findByText(draftItem.title)).toBeInTheDocument()
    expect(screen.getByText(approvedItem.title)).toBeInTheDocument()
    expect(screen.getByTestId("knowledge-scope-7001")).toHaveTextContent("系统 · order-center")
    expect(screen.getByTestId("knowledge-status-7001")).toHaveTextContent("草稿")
    expect(screen.getByTestId("knowledge-status-7002")).toHaveTextContent("已通过")
    expect(screen.getByTestId("knowledge-revision-7001")).toHaveTextContent("r2")
    expect(screen.getByTestId("knowledge-tags-7001")).toHaveTextContent("结算、支付")
    expect(knowledgeApi.listKnowledge).toHaveBeenCalledWith(expect.objectContaining({ limit: 20 }))
  })

  it("forwards scope, status, tag, full-text and system_key filters", async () => {
    render(<KnowledgePage />)
    await screen.findByText(draftItem.title)

    fireEvent.change(screen.getByLabelText("范围"), { target: { value: "system" } })
    await waitFor(() => {
      expect(knowledgeApi.listKnowledge).toHaveBeenLastCalledWith(
        expect.objectContaining({ scope: "system" }),
      )
    })

    fireEvent.change(screen.getByLabelText("状态"), { target: { value: "approved" } })
    fireEvent.change(screen.getByLabelText("系统标识"), { target: { value: " order-center " } })
    fireEvent.change(screen.getByLabelText("标签"), { target: { value: " 结算 " } })
    fireEvent.change(screen.getByLabelText("搜索"), { target: { value: " 校验 " } })
    fireEvent.click(screen.getByTestId("knowledge-search"))

    await waitFor(() => {
      expect(knowledgeApi.listKnowledge).toHaveBeenLastCalledWith(
        expect.objectContaining({
          scope: "system",
          status: "approved",
          systemKey: "order-center",
          tag: "结算",
          q: "校验",
        }),
      )
    })
  })

  it("clears the system_key filter when the scope returns to global", async () => {
    render(<KnowledgePage />)
    await screen.findByText(draftItem.title)

    fireEvent.change(screen.getByLabelText("范围"), { target: { value: "system" } })
    fireEvent.change(screen.getByLabelText("系统标识"), { target: { value: "order-center" } })
    fireEvent.click(screen.getByTestId("knowledge-search"))
    await waitFor(() => {
      expect(knowledgeApi.listKnowledge).toHaveBeenLastCalledWith(
        expect.objectContaining({ systemKey: "order-center" }),
      )
    })

    fireEvent.change(screen.getByLabelText("范围"), { target: { value: "global" } })
    await waitFor(() => {
      expect(knowledgeApi.listKnowledge).toHaveBeenLastCalledWith(
        expect.not.objectContaining({ systemKey: expect.anything() }),
      )
    })
    expect(screen.queryByLabelText("系统标识")).not.toBeInTheDocument()
  })

  it("appends the next page through the cursor with 加载更多", async () => {
    knowledgeApi.listKnowledge
      .mockResolvedValueOnce(page([draftItem], "7000", true))
      .mockResolvedValueOnce(page([approvedItem], "", false))
    render(<KnowledgePage />)

    await screen.findByText(draftItem.title)
    fireEvent.click(screen.getByTestId("knowledge-load-more"))

    await waitFor(() => {
      expect(knowledgeApi.listKnowledge).toHaveBeenLastCalledWith(expect.anything(), "7000")
    })
    expect(await screen.findByText(approvedItem.title)).toBeInTheDocument()
    expect(screen.getByText(draftItem.title)).toBeInTheDocument()
    expect(screen.queryByTestId("knowledge-load-more")).not.toBeInTheDocument()
  })

  it("shows the empty state", async () => {
    knowledgeApi.listKnowledge.mockResolvedValue(page([]))
    render(<KnowledgePage />)

    expect(await screen.findByTestId("knowledge-empty")).toHaveTextContent("暂无知识")
  })

  it("shows the error state and retries the list", async () => {
    knowledgeApi.listKnowledge
      .mockRejectedValueOnce(new Error("knowledge unavailable"))
      .mockResolvedValueOnce(page([draftItem]))
    render(<KnowledgePage />)

    expect(await screen.findByText("知识列表加载失败")).toBeInTheDocument()
    expect(screen.getByText("knowledge unavailable")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText(draftItem.title)).toBeInTheDocument()
  })

  it("opens the detail dialog for a row", async () => {
    render(<KnowledgePage />)

    fireEvent.click(await screen.findByTestId("knowledge-item-7001"))

    expect(await screen.findByTestId("knowledge-detail")).toBeInTheDocument()
    expect(knowledgeApi.getKnowledge).toHaveBeenCalledWith("7001")
    expect(screen.getByTestId("knowledge-content")).toHaveTextContent(draftItem.content)
    expect(screen.getByTestId("knowledge-revisions")).toHaveTextContent("r1")
  })

  it("refreshes both the detail and the list after an admin approves a draft", async () => {
    auth.isAdmin = true
    auth.userId = "9001"
    knowledgeApi.approveKnowledge.mockResolvedValue({
      ...draftItem,
      status: "approved",
      reviewed_by: "9001",
      reviewed_at: updatedAt,
    })
    render(<KnowledgePage />)

    fireEvent.click(await screen.findByTestId("knowledge-item-7001"))
    await screen.findByTestId("knowledge-detail")
    expect(knowledgeApi.listKnowledge).toHaveBeenCalledTimes(1)

    fireEvent.click(await screen.findByTestId("knowledge-review-approve"))

    await waitFor(() => expect(knowledgeApi.listKnowledge).toHaveBeenCalledTimes(2))
    expect(await screen.findByTestId("knowledge-status")).toHaveTextContent("已通过")
  })
})

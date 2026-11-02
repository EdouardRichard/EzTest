import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
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

import { KnowledgeDetail } from "./KnowledgeDetail"

/* --------------------------------- fixtures -------------------------------- */

const createdAt = "2026-11-01T02:00:00Z"
const updatedAt = "2026-11-01T06:00:00Z"
const longContent =
  "提交前必须校验支付方式，否则提示用户补充。这是一段用于验证详情内容渲染的较长说明。"

const baseItem: KnowledgeItemView = {
  id: "7001",
  scope: "system",
  system_key: "order-center",
  title: "结算页提交前校验",
  content: longContent,
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

const revisions = [
  {
    id: "8002",
    knowledge_id: "7001",
    revision: 2,
    title: baseItem.title,
    content: baseItem.content,
    tags: baseItem.tags,
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
]

function detail(overrides: Partial<KnowledgeDetailView> = {}): KnowledgeDetailView {
  return { ...baseItem, revisions, ...overrides }
}

function props(overrides: Partial<Parameters<typeof KnowledgeDetail>[0]> = {}) {
  return {
    knowledgeId: "7001",
    isAdmin: false,
    currentUserId: "9002",
    refreshToken: 0,
    onClose: vi.fn(),
    onEdit: vi.fn(),
    onChanged: vi.fn(),
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  knowledgeApi.getKnowledge.mockResolvedValue(detail())
})

afterEach(cleanup)

/* ---------------------------------- tests ---------------------------------- */

describe("KnowledgeDetail", () => {
  it("renders the item fields, source version, review info and revisions", async () => {
    knowledgeApi.getKnowledge.mockResolvedValue(
      detail({
        status: "approved",
        source_stale: true,
        review_reason: "评审通过",
        reviewed_by: "9001",
        reviewed_at: updatedAt,
      }),
    )
    render(<KnowledgeDetail {...props()} />)

    const root = await screen.findByTestId("knowledge-detail")
    expect(within(root).getByText(baseItem.title)).toBeInTheDocument()
    expect(screen.getByTestId("knowledge-content")).toHaveTextContent("提交前必须校验支付方式")
    expect(screen.getByTestId("knowledge-status")).toHaveTextContent("已通过")
    expect(screen.getByTestId("knowledge-scope")).toHaveTextContent("系统 · order-center")
    expect(screen.getByTestId("knowledge-revision")).toHaveTextContent("r2")
    expect(screen.getByTestId("knowledge-source-version")).toHaveTextContent("2001")
    expect(screen.getByTestId("knowledge-source-stale")).toHaveTextContent("来源版本已失效")
    expect(screen.getByTestId("knowledge-tags")).toHaveTextContent("结算")
    expect(screen.getByTestId("knowledge-review-info")).toHaveTextContent("评审通过")
    expect(screen.getByTestId("knowledge-review-info")).toHaveTextContent("9001")

    const history = screen.getByTestId("knowledge-revisions")
    expect(within(history).getByTestId("knowledge-revision-2")).toHaveTextContent("提交前必须校验")
    expect(within(history).getByTestId("knowledge-revision-1")).toHaveTextContent("旧内容")
    expect(knowledgeApi.getKnowledge).toHaveBeenCalledWith("7001")
  })

  it("shows no edit affordance for an approved item", async () => {
    knowledgeApi.getKnowledge.mockResolvedValue(detail({ status: "approved" }))
    render(<KnowledgeDetail {...props({ isAdmin: true })} />)

    expect(await screen.findByTestId("knowledge-detail")).toBeInTheDocument()
    expect(screen.queryByTestId("knowledge-edit")).not.toBeInTheDocument()
  })

  it("shows edit only to the author of a draft", async () => {
    render(<KnowledgeDetail {...props({ currentUserId: "9999" })} />)
    expect(await screen.findByTestId("knowledge-detail")).toBeInTheDocument()
    expect(screen.queryByTestId("knowledge-edit")).not.toBeInTheDocument()

    cleanup()
    const onEdit = vi.fn()
    render(<KnowledgeDetail {...props({ currentUserId: "9002", onEdit })} />)
    fireEvent.click(await screen.findByTestId("knowledge-edit"))
    expect(onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: "7001", status: "draft" }))
  })

  it("lets an admin edit a rejected item belonging to someone else", async () => {
    knowledgeApi.getKnowledge.mockResolvedValue(
      detail({ status: "rejected", created_by: "9999", review_reason: "不清晰" }),
    )
    render(<KnowledgeDetail {...props({ isAdmin: true, currentUserId: "9001" })} />)

    expect(await screen.findByTestId("knowledge-edit")).toBeInTheDocument()
    fireEvent.click(screen.getByTestId("knowledge-edit"))
  })

  it("hides every review action from a normal user", async () => {
    render(<KnowledgeDetail {...props({ isAdmin: false })} />)

    expect(await screen.findByTestId("knowledge-detail")).toBeInTheDocument()
    expect(screen.queryByTestId("knowledge-review-actions")).not.toBeInTheDocument()
  })

  it("refreshes the detail and notifies the parent after approve", async () => {
    const onChanged = vi.fn()
    knowledgeApi.approveKnowledge.mockResolvedValue({
      ...baseItem,
      status: "approved",
      reviewed_by: "9001",
      reviewed_at: updatedAt,
    })
    render(<KnowledgeDetail {...props({ isAdmin: true, currentUserId: "9001", onChanged })} />)

    fireEvent.click(await screen.findByTestId("knowledge-review-approve"))

    await waitFor(() => expect(knowledgeApi.approveKnowledge).toHaveBeenCalledWith("7001"))
    await waitFor(() => expect(screen.getByTestId("knowledge-status")).toHaveTextContent("已通过"))
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it("shows the loading and error states with retry", async () => {
    knowledgeApi.getKnowledge.mockRejectedValueOnce(new Error("detail unavailable"))
    render(<KnowledgeDetail {...props()} />)

    expect(await screen.findByText("知识详情加载失败")).toBeInTheDocument()
    expect(screen.getByText("detail unavailable")).toBeInTheDocument()

    knowledgeApi.getKnowledge.mockResolvedValueOnce(detail())
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("knowledge-detail")).toHaveTextContent(baseItem.title)
  })
})

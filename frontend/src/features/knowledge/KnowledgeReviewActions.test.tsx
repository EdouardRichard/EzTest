import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { KnowledgeItemView } from "./types"

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

import { KnowledgeReviewActions } from "./KnowledgeReviewActions"

const baseItem: KnowledgeItemView = {
  id: "7001",
  scope: "system",
  system_key: "order-center",
  title: "结算页提交前校验",
  content: "提交前必须校验支付方式。",
  tags: ["结算"],
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
  created_at: "2026-11-01T02:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
}

beforeEach(() => {
  vi.clearAllMocks()
  knowledgeApi.approveKnowledge.mockResolvedValue({
    ...baseItem,
    status: "approved",
    reviewed_by: "9001",
    reviewed_at: "2026-11-01T06:00:00Z",
  })
  knowledgeApi.rejectKnowledge.mockResolvedValue({
    ...baseItem,
    status: "rejected",
    review_reason: "标题不清晰",
    reviewed_by: "9001",
    reviewed_at: "2026-11-01T06:00:00Z",
  })
  knowledgeApi.revokeKnowledge.mockResolvedValue({
    ...baseItem,
    status: "revoked",
    review_reason: "来源版本废弃",
    revoked_at: "2026-11-01T06:00:00Z",
  })
})

afterEach(cleanup)

describe("KnowledgeReviewActions", () => {
  it("renders nothing for a normal user", () => {
    render(<KnowledgeReviewActions item={baseItem} isAdmin={false} onUpdated={vi.fn()} />)
    expect(screen.queryByTestId("knowledge-review-actions")).not.toBeInTheDocument()
  })

  it("approves a draft without asking for a reason", async () => {
    const onUpdated = vi.fn()
    render(<KnowledgeReviewActions item={baseItem} isAdmin onUpdated={onUpdated} />)

    fireEvent.click(screen.getByTestId("knowledge-review-approve"))

    await waitFor(() => expect(knowledgeApi.approveKnowledge).toHaveBeenCalledWith("7001"))
    expect(onUpdated).toHaveBeenCalledWith(expect.objectContaining({ status: "approved" }))
    expect(knowledgeApi.rejectKnowledge).not.toHaveBeenCalled()
  })

  it("requires a reason to reject and posts the trimmed reason", async () => {
    const onUpdated = vi.fn()
    render(<KnowledgeReviewActions item={baseItem} isAdmin onUpdated={onUpdated} />)

    fireEvent.click(screen.getByTestId("knowledge-review-reject"))
    const confirm = screen.getByTestId("knowledge-review-confirm")
    expect(confirm).toBeDisabled()

    fireEvent.change(screen.getByTestId("knowledge-review-reason"), {
      target: { value: "  标题不清晰  " },
    })
    expect(confirm).toBeEnabled()
    fireEvent.click(confirm)

    await waitFor(() =>
      expect(knowledgeApi.rejectKnowledge).toHaveBeenCalledWith("7001", "标题不清晰"),
    )
    expect(onUpdated).toHaveBeenCalledWith(expect.objectContaining({ status: "rejected" }))
  })

  it("requires a reason to revoke an approved item", async () => {
    const onUpdated = vi.fn()
    render(
      <KnowledgeReviewActions
        item={{ ...baseItem, status: "approved" }}
        isAdmin
        onUpdated={onUpdated}
      />,
    )

    fireEvent.click(screen.getByTestId("knowledge-review-revoke"))
    expect(screen.getByTestId("knowledge-review-confirm")).toBeDisabled()
    fireEvent.change(screen.getByTestId("knowledge-review-reason"), {
      target: { value: "来源版本废弃" },
    })
    fireEvent.click(screen.getByTestId("knowledge-review-confirm"))

    await waitFor(() =>
      expect(knowledgeApi.revokeKnowledge).toHaveBeenCalledWith("7001", "来源版本废弃"),
    )
    expect(onUpdated).toHaveBeenCalledWith(expect.objectContaining({ status: "revoked" }))
  })

  it("re-approves a revoked item", async () => {
    const onUpdated = vi.fn()
    render(
      <KnowledgeReviewActions
        item={{ ...baseItem, status: "revoked" }}
        isAdmin
        onUpdated={onUpdated}
      />,
    )

    const approve = screen.getByTestId("knowledge-review-approve")
    expect(approve).toHaveTextContent("重新批准")
    fireEvent.click(approve)

    await waitFor(() => expect(knowledgeApi.approveKnowledge).toHaveBeenCalledWith("7001"))
    expect(onUpdated).toHaveBeenCalledWith(expect.objectContaining({ status: "approved" }))
  })

  it("offers no review transition for a rejected item", () => {
    render(
      <KnowledgeReviewActions
        item={{ ...baseItem, status: "rejected" }}
        isAdmin
        onUpdated={vi.fn()}
      />,
    )
    expect(screen.queryByTestId("knowledge-review-actions")).not.toBeInTheDocument()
  })

  it("shows the API error inside the reason dialog", async () => {
    knowledgeApi.rejectKnowledge.mockRejectedValueOnce(new Error("state_conflict"))
    render(<KnowledgeReviewActions item={baseItem} isAdmin onUpdated={vi.fn()} />)

    fireEvent.click(screen.getByTestId("knowledge-review-reject"))
    fireEvent.change(screen.getByTestId("knowledge-review-reason"), {
      target: { value: "不清晰" },
    })
    fireEvent.click(screen.getByTestId("knowledge-review-confirm"))

    expect(await screen.findByText("state_conflict")).toBeInTheDocument()
    expect(screen.getByTestId("knowledge-review-dialog")).toBeInTheDocument()
  })
})

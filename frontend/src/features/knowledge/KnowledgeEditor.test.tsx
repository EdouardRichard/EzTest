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

import { KnowledgeEditor } from "./KnowledgeEditor"

const item: KnowledgeItemView = {
  id: "7001",
  scope: "system",
  system_key: "order-center",
  title: "结算页提交前校验",
  content: "提交前必须校验支付方式。",
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
  created_at: "2026-11-01T02:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
}

beforeEach(() => {
  vi.clearAllMocks()
  knowledgeApi.createKnowledge.mockResolvedValue({ ...item, id: "7003", title: "新知识" })
  knowledgeApi.updateKnowledge.mockResolvedValue({ ...item, title: "更新后的标题" })
})

afterEach(cleanup)

describe("KnowledgeEditor", () => {
  it("rejects an empty create form before calling the API", () => {
    render(<KnowledgeEditor mode="create" onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.click(screen.getByTestId("knowledge-editor-submit"))

    expect(screen.getByText("标题长度需在 1–200 字之间")).toBeInTheDocument()
    expect(screen.getByText("内容长度需在 1–20000 字之间")).toBeInTheDocument()
    expect(knowledgeApi.createKnowledge).not.toHaveBeenCalled()
  })

  it("requires a system key for the system scope", () => {
    render(<KnowledgeEditor mode="create" onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.change(screen.getByLabelText("标题"), { target: { value: "新知识" } })
    fireEvent.change(screen.getByLabelText("内容"), { target: { value: "内容说明" } })
    fireEvent.change(screen.getByLabelText("范围"), { target: { value: "system" } })
    fireEvent.click(screen.getByTestId("knowledge-editor-submit"))

    expect(screen.getByText("系统范围必须填写系统标识（system_key）")).toBeInTheDocument()
    expect(knowledgeApi.createKnowledge).not.toHaveBeenCalled()
  })

  it("creates a draft with trimmed fields and deduplicated tags", async () => {
    const onSaved = vi.fn()
    render(<KnowledgeEditor mode="create" onClose={vi.fn()} onSaved={onSaved} />)

    fireEvent.change(screen.getByLabelText("标题"), { target: { value: "  新知识  " } })
    fireEvent.change(screen.getByLabelText("内容"), { target: { value: "  内容说明  " } })
    fireEvent.change(screen.getByLabelText("范围"), { target: { value: "system" } })
    fireEvent.change(screen.getByLabelText(/系统标识/), { target: { value: " order-center " } })
    fireEvent.change(screen.getByLabelText("标签"), { target: { value: " 结算, 支付 ,结算" } })
    fireEvent.change(screen.getByLabelText("来源版本 ID（可选）"), { target: { value: "2001" } })
    fireEvent.click(screen.getByTestId("knowledge-editor-submit"))

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
    expect(knowledgeApi.createKnowledge).toHaveBeenCalledWith({
      title: "新知识",
      content: "内容说明",
      scope: "system",
      system_key: "order-center",
      tags: ["结算", "支付"],
      source_version_id: "2001",
    })
    expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ id: "7003" }))
  })

  it("edits an existing draft with the update call", async () => {
    const onSaved = vi.fn()
    render(<KnowledgeEditor mode="edit" item={item} onClose={vi.fn()} onSaved={onSaved} />)

    expect(screen.getByLabelText("标题")).toHaveValue(item.title)
    expect(screen.getByLabelText(/系统标识/)).toHaveValue("order-center")

    fireEvent.change(screen.getByLabelText("标题"), { target: { value: "更新后的标题" } })
    fireEvent.click(screen.getByTestId("knowledge-editor-submit"))

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
    expect(knowledgeApi.updateKnowledge).toHaveBeenCalledWith("7001", {
      title: "更新后的标题",
      content: item.content,
      scope: "system",
      system_key: "order-center",
      tags: ["结算", "支付"],
    })
  })

  it("shows the API error and keeps the editor open", async () => {
    knowledgeApi.createKnowledge.mockRejectedValueOnce(new Error("title already exists"))
    const onSaved = vi.fn()
    render(<KnowledgeEditor mode="create" onClose={vi.fn()} onSaved={onSaved} />)

    fireEvent.change(screen.getByLabelText("标题"), { target: { value: "重复标题" } })
    fireEvent.change(screen.getByLabelText("内容"), { target: { value: "内容说明" } })
    fireEvent.click(screen.getByTestId("knowledge-editor-submit"))

    expect(await screen.findByText("title already exists")).toBeInTheDocument()
    expect(onSaved).not.toHaveBeenCalled()
    expect(screen.getByTestId("knowledge-editor")).toBeInTheDocument()
  })
})

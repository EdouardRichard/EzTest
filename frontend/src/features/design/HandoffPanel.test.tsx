import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({ getVersionHandoff: vi.fn() }))
vi.mock("./api", () => api)

import type { HandoffView } from "./api"
import { HandoffPanel } from "./HandoffPanel"

const handoff: HandoffView = {
  version_id: "2001",
  revision: 3,
  generated_at: "2026-11-01T08:00:00Z",
  requirements: [
    {
      id: "6001",
      current_version_id: "6101",
      revision: 2,
      title: "下单流程支持优惠券",
      description: "结算时可用优惠券抵扣",
      acceptance_criteria: "优惠券金额正确抵扣",
      priority: "P1",
      status: "confirmed",
      source_quote: "用户希望下单可用券",
      confidence: 0.92,
      confirmed_at: "2026-11-01T07:30:00Z",
    },
  ],
  qa: [
    {
      question_id: "7001",
      question: "优惠券是否可叠加？",
      options: [
        { id: "yes", label: "可叠加" },
        { id: "no", label: "不可叠加" },
      ],
      source_refs: [],
      requirement_refs: ["6001"],
      status: "answered",
      answer_selected: ["no"],
      answer_text: "不可叠加",
    },
  ],
  decisions: [
    {
      fact_id: "31",
      kind: "api",
      key: "api:env:1:POST /orders",
      summary: "下单接口返回优惠金额",
      source: "explore",
    },
  ],
  open: [
    {
      kind: "question",
      summary: "退款优惠券是否返还？",
      status: "pending",
      source: "clarification",
    },
  ],
  plan: {
    stage: "designing",
    current: { id: "41", phase: "P2", title: "生成用例", status: "running", decision: "" },
    next: null,
  },
  resources: {
    suts: [{ id: "1" }],
    pages: [{ id: "2" }, { id: "3" }],
    apis: [{ id: "4" }],
    accounts: [],
    databases: [],
    repositories: [{ id: "5" }],
  },
}

describe("HandoffPanel", () => {
  beforeEach(() => {
    api.getVersionHandoff.mockReset()
  })

  afterEach(cleanup)

  it("renders the six handoff blocks from the read API", async () => {
    api.getVersionHandoff.mockResolvedValue(handoff)

    render(<HandoffPanel versionId="2001" />)

    expect(await screen.findByText("下单流程支持优惠券")).toBeInTheDocument()
    expect(screen.getByText("优惠券是否可叠加？")).toBeInTheDocument()
    expect(screen.getByText("下单接口返回优惠金额")).toBeInTheDocument()
    expect(screen.getByText("退款优惠券是否返还？")).toBeInTheDocument()
    expect(screen.getByText(/生成用例/)).toBeInTheDocument()
    expect(screen.getByText(/修订 #3/)).toBeInTheDocument()
    expect(api.getVersionHandoff).toHaveBeenCalledWith("2001", undefined)
  })

  it("shows explicit empty states for the list blocks", async () => {
    api.getVersionHandoff.mockResolvedValue({
      ...handoff,
      requirements: [],
      qa: [],
      decisions: [],
      open: [],
      plan: { stage: "clarifying", current: null, next: null },
    })

    render(<HandoffPanel versionId="2001" />)

    expect(await screen.findByText("暂无已确认需求")).toBeInTheDocument()
    expect(screen.getByText("暂无问答记录")).toBeInTheDocument()
    expect(screen.getByText("暂无设计决策")).toBeInTheDocument()
    expect(screen.getByText("暂无待决事项")).toBeInTheDocument()
    expect(screen.getByText("暂无当前计划项")).toBeInTheDocument()
  })

  it("reads a selected revision through the same API", async () => {
    api.getVersionHandoff.mockResolvedValue(handoff)

    render(<HandoffPanel versionId="2001" />)
    await screen.findByText("下单流程支持优惠券")

    fireEvent.change(screen.getByLabelText("修订号"), { target: { value: "2" } })
    fireEvent.click(screen.getByRole("button", { name: "读取" }))

    await waitFor(() => expect(api.getVersionHandoff).toHaveBeenLastCalledWith("2001", "2"))
  })

  it("shows the read error and retries", async () => {
    api.getVersionHandoff
      .mockRejectedValueOnce(
        new ApiError({
          status: 404,
          code: "not_found",
          message: "交接文档不存在",
          requestId: "req_handoff",
        }),
      )
      .mockResolvedValueOnce(handoff)

    render(<HandoffPanel versionId="2001" />)

    expect(await screen.findByText("交接文档不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("下单流程支持优惠券")).toBeInTheDocument()
  })
})

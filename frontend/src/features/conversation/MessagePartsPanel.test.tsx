import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  getMessageParts: vi.fn(),
}))
vi.mock("./api", () => api)

import { MessagePartsPanel } from "./MessagePartsPanel"
import type { MessagePartView } from "./types"

const createdAt = "2026-11-01T10:00:00Z"

function part(overrides: Partial<MessagePartView>): MessagePartView {
  return {
    seq: 1,
    part_type: "text",
    payload: { text: "已读取退款规则。" },
    created_at: createdAt,
    ...overrides,
  }
}

describe("MessagePartsPanel", () => {
  beforeEach(() => {
    api.getMessageParts.mockReset()
  })

  afterEach(cleanup)

  it("renders every persisted part by its part_type", async () => {
    api.getMessageParts.mockResolvedValue([
      part({ seq: 1, part_type: "text", payload: { text: "已读取退款规则。" } }),
      part({
        seq: 2,
        part_type: "thinking",
        payload: { text: "核对退款时效。", duration_ms: 2_000 },
      }),
      part({
        seq: 3,
        part_type: "tool_call",
        payload: {
          call_id: "call_1",
          name: "knowledge.search",
          status: "done",
          duration_ms: 1_250,
          arguments: { query: "退款" },
          result: { hits: 2 },
        },
      }),
      part({
        seq: 4,
        part_type: "job_card",
        payload: { job_id: "501", kind: "design.cases", status: "running", current: 12, total: 27 },
      }),
      part({
        seq: 5,
        part_type: "error",
        payload: {
          code: "model_error",
          message: "模型调用失败，请重试。",
          retryable: true,
          action: "重试",
        },
      }),
    ])

    render(<MessagePartsPanel versionId="2001" conversationId="4001" messageId="5001" />)

    expect(await screen.findByText("已读取退款规则。")).toBeInTheDocument()
    expect(screen.getByText("思考了 2s")).toBeInTheDocument()
    expect(screen.getByText("knowledge.search")).toBeInTheDocument()
    expect(screen.getByText("12/27")).toBeInTheDocument()
    expect(screen.getByText("模型调用失败，请重试。")).toBeInTheDocument()
    expect(screen.getByTestId("message-part-type-1")).toHaveTextContent("text")
    expect(screen.getByTestId("message-part-type-4")).toHaveTextContent("job_card")
    expect(api.getMessageParts).toHaveBeenCalledWith("2001", "4001", "5001")
  })

  it("shows an explicit empty state when the message has no persisted parts", async () => {
    api.getMessageParts.mockResolvedValue([])

    render(<MessagePartsPanel versionId="2001" conversationId="4001" messageId="5001" />)

    expect(await screen.findByText("该消息暂无消息片段")).toBeInTheDocument()
  })

  it("shows the load error and retries the parts history", async () => {
    api.getMessageParts
      .mockRejectedValueOnce(new Error("片段读取失败"))
      .mockResolvedValueOnce([part({})])

    render(<MessagePartsPanel versionId="2001" conversationId="4001" messageId="5001" />)

    expect(await screen.findByText("片段读取失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("已读取退款规则。")).toBeInTheDocument()
    expect(api.getMessageParts).toHaveBeenCalledTimes(2)
  })
})

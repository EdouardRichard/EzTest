import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type { NewMessagePart } from "./types"

const api = vi.hoisted(() => ({ uploadContent: vi.fn() }))
vi.mock("./api", () => api)

import { Composer } from "./Composer"

const CONTENT = {
  sha256: "a".repeat(64),
  size: 12,
  mime: "text/plain",
  url: `/api/content/${"a".repeat(64)}`,
}

function ErrorHarness(props: {
  onSend: (parts: NewMessagePart[]) => Promise<boolean>
  error: unknown
}) {
  return <Composer onSend={props.onSend} error={props.error} />
}

describe("Composer", () => {
  beforeEach(() => {
    api.uploadContent.mockReset()
  })

  afterEach(cleanup)

  it("sends on Enter and clears the textarea on success", async () => {
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "退款 T+1 到账" } })
    fireEvent.keyDown(textarea, { key: "Enter", shiftKey: false })

    await waitFor(() =>
      expect(onSend).toHaveBeenCalledWith([{ type: "text", text: "退款 T+1 到账" }]),
    )
    await waitFor(() => expect(textarea).toHaveValue(""))
  })

  it("keeps a newline on Shift+Enter without sending", () => {
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "第一行" } })
    fireEvent.keyDown(textarea, { key: "Enter", shiftKey: true })

    expect(onSend).not.toHaveBeenCalled()
    expect(textarea).toHaveValue("第一行")
  })

  it("does not send an empty composer", () => {
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.keyDown(textarea, { key: "Enter" })
    fireEvent.click(screen.getByRole("button", { name: "发送" }))

    expect(onSend).not.toHaveBeenCalled()
  })

  it("disables the send button while a message is in flight", async () => {
    let resolveSend: (value: boolean) => void = () => {}
    const onSend = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          resolveSend = resolve
        }),
    )
    render(<Composer onSend={onSend} />)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "退款 T+1 到账" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))
    expect(screen.getByRole("button", { name: "发送中…" })).toBeDisabled()
    fireEvent.keyDown(textarea, { key: "Enter" })
    expect(onSend).toHaveBeenCalledTimes(1)

    resolveSend(true)
    await waitFor(() => expect(screen.getByRole("button", { name: "发送" })).toBeInTheDocument())
    fireEvent.change(textarea, { target: { value: "下一条" } })
    await waitFor(() => expect(screen.getByRole("button", { name: "发送" })).toBeEnabled())
  })

  it("sends uploaded attachments together with the text", async () => {
    api.uploadContent.mockResolvedValue(CONTENT)
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    fireEvent.change(screen.getByTestId("attachment-input"), {
      target: { files: [new File(["hello 退款"], "退款规则V2.pdf", { type: "application/pdf" })] },
    })
    await screen.findByText("退款规则V2.pdf")

    fireEvent.change(screen.getByTestId("composer-textarea"), {
      target: { value: "请参考附件" },
    })
    fireEvent.keyDown(screen.getByTestId("composer-textarea"), { key: "Enter" })

    await waitFor(() => expect(onSend).toHaveBeenCalled())
    const parts = onSend.mock.calls[0][0] as NewMessagePart[]
    expect(parts[0]).toEqual({ type: "text", text: "请参考附件" })
    expect(parts[1]).toEqual({
      type: "attachment",
      sha256: CONTENT.sha256,
      name: "退款规则V2.pdf",
      mime: "text/plain",
      size: 12,
    })
    await waitFor(() => expect(screen.queryByText("退款规则V2.pdf")).not.toBeInTheDocument())
  })

  it("shows the send error inline and preserves the draft on failure", async () => {
    const onSend = vi.fn().mockResolvedValue(false)
    const error = new ApiError({ status: 503, code: "ai_unavailable", message: "AI 暂不可用" })
    render(<ErrorHarness onSend={onSend} error={error} />)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "退款 T+1 到账" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await waitFor(() => expect(onSend).toHaveBeenCalled())
    expect(screen.getByText("AI 暂不可用")).toBeInTheDocument()
    expect(textarea).toHaveValue("退款 T+1 到账")
  })
})

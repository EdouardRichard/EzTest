import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useState } from "react"
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

function PendingRetryHarness(props: {
  onSend: (parts: NewMessagePart[]) => Promise<boolean>
  pendingError: ApiError
}) {
  const [error, setError] = useState<unknown>(undefined)
  return (
    <Composer
      error={error}
      onSend={async (parts) => {
        const sent = await props.onSend(parts)
        setError(sent ? undefined : props.pendingError)
        return sent
      }}
    />
  )
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

  it("uploads pasted files through the same attachment pipeline", async () => {
    api.uploadContent.mockResolvedValue({ ...CONTENT, mime: "image/png" })
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    const file = new File(["png-bytes"], "登录截图.png", { type: "image/png" })
    fireEvent.paste(screen.getByTestId("composer-textarea"), {
      clipboardData: { files: [file] },
    })

    await screen.findByText("登录截图.png")
    expect(api.uploadContent).toHaveBeenCalledWith(file)

    fireEvent.change(screen.getByTestId("composer-textarea"), { target: { value: "请看截图" } })
    fireEvent.keyDown(screen.getByTestId("composer-textarea"), { key: "Enter" })

    await waitFor(() => expect(onSend).toHaveBeenCalled())
    const parts = onSend.mock.calls[0][0] as NewMessagePart[]
    expect(parts[0]).toEqual({ type: "text", text: "请看截图" })
    expect(parts[1]).toEqual({
      type: "attachment",
      sha256: CONTENT.sha256,
      name: "登录截图.png",
      mime: "image/png",
      size: 12,
    })
  })

  it("uploads files dropped onto the composer through the same attachment pipeline", async () => {
    api.uploadContent.mockResolvedValue({ ...CONTENT, mime: "application/pdf" })
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    const file = new File(["pdf-bytes"], "需求说明.pdf", { type: "application/pdf" })
    fireEvent.drop(screen.getByTestId("composer-textarea"), {
      dataTransfer: { files: [file], types: ["Files"] },
    })

    await screen.findByText("需求说明.pdf")
    expect(api.uploadContent).toHaveBeenCalledWith(file)
  })

  it("blocks sending while an attachment is still pending", async () => {
    let resolveUpload: (value: typeof CONTENT) => void = () => {}
    api.uploadContent.mockImplementation(
      () =>
        new Promise<typeof CONTENT>((resolve) => {
          resolveUpload = resolve
        }),
    )
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.paste(textarea, {
      clipboardData: { files: [new File(["png-bytes"], "登录截图.png", { type: "image/png" })] },
    })

    const pending = await screen.findByTestId("attachment-pending")
    expect(pending).toHaveTextContent("登录截图.png")
    expect(pending).toHaveTextContent("解析中")
    expect(screen.getByRole("button", { name: "解析中…" })).toBeDisabled()

    fireEvent.change(textarea, { target: { value: "请看截图" } })
    fireEvent.keyDown(textarea, { key: "Enter" })
    expect(onSend).not.toHaveBeenCalled()

    resolveUpload({ ...CONTENT, mime: "image/png" })
    await waitFor(() => expect(screen.queryByTestId("attachment-pending")).not.toBeInTheDocument())
    await waitFor(() => expect(screen.getByRole("button", { name: "发送" })).toBeEnabled())

    fireEvent.keyDown(textarea, { key: "Enter" })
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))
    const parts = onSend.mock.calls[0][0] as NewMessagePart[]
    expect(parts[1]).toEqual({
      type: "attachment",
      sha256: CONTENT.sha256,
      name: "登录截图.png",
      mime: "image/png",
      size: 12,
    })
  })

  it("serializes concurrent paste and drop uploads without losing attachments", async () => {
    const resolvers: Array<(value: typeof CONTENT) => void> = []
    api.uploadContent.mockImplementation(
      () =>
        new Promise<typeof CONTENT>((resolve) => {
          resolvers.push(resolve)
        }),
    )
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.paste(textarea, {
      clipboardData: { files: [new File(["png"], "a.png", { type: "image/png" })] },
    })
    fireEvent.drop(textarea, {
      dataTransfer: {
        files: [new File(["pdf"], "b.pdf", { type: "application/pdf" })],
        types: ["Files"],
      },
    })

    // Uploads are serialized: the second file waits for the first request.
    await waitFor(() => expect(api.uploadContent).toHaveBeenCalledTimes(1))
    resolvers[0]({ ...CONTENT, sha256: "a".repeat(64), mime: "image/png" })
    await waitFor(() => expect(api.uploadContent).toHaveBeenCalledTimes(2))
    resolvers[1]({ ...CONTENT, sha256: "b".repeat(64), mime: "application/pdf" })
    await screen.findByText("b.pdf")

    fireEvent.change(textarea, { target: { value: "两个附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))
    const parts = onSend.mock.calls[0][0] as NewMessagePart[]
    expect(parts).toEqual([
      { type: "text", text: "两个附件" },
      { type: "attachment", sha256: "a".repeat(64), name: "a.png", mime: "image/png", size: 12 },
      {
        type: "attachment",
        sha256: "b".repeat(64),
        name: "b.pdf",
        mime: "application/pdf",
        size: 12,
      },
    ])
  })

  it("keeps the message and retries after a 409 attachment_pending", async () => {
    api.uploadContent.mockResolvedValue({ ...CONTENT, mime: "text/markdown" })
    const pendingError = new ApiError({
      status: 409,
      code: "attachment_pending",
      message: "附件解析中，可重试",
      retryable: true,
    })
    const onSend = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true)

    render(<PendingRetryHarness onSend={onSend} pendingError={pendingError} />)
    fireEvent.change(screen.getByTestId("attachment-input"), {
      target: { files: [new File(["# spec"], "spec.md", { type: "text/markdown" })] },
    })
    await screen.findByText("spec.md")

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))
    expect(await screen.findByText("附件解析中，可重试")).toBeInTheDocument()
    // The draft and the uploaded attachment survive the rejected send.
    expect(textarea).toHaveValue("请读附件")
    expect(screen.getByText("spec.md")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试发送" }))
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2))
    expect(onSend.mock.calls[1][0]).toEqual(onSend.mock.calls[0][0])
    await waitFor(() => expect(textarea).toHaveValue(""))
    await waitFor(() => expect(screen.queryByText("spec.md")).not.toBeInTheDocument())
  })
})

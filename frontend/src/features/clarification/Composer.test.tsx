import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useRef, useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type { NewMessagePart } from "./types"

const api = vi.hoisted(() => ({ uploadContent: vi.fn() }))
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return { ...actual, ...api }
})

import { AUTO_RETRY_MAX_MS, autoRetryExpired, Composer } from "./Composer"

const CONTENT = {
  sha256: "a".repeat(64),
  size: 12,
  mime: "text/plain",
  url: `/api/content/${"a".repeat(64)}`,
}

/** A C-04 typed 409 with the progress details Task 10 sends. */
function attachmentPending(details: Record<string, unknown> = {}): ApiError {
  return new ApiError({
    status: 409,
    code: "attachment_pending",
    message: "附件解析中，可重试",
    retryable: true,
    details: {
      attachment_id: "55",
      name: "spec.md",
      retry_after_seconds: 2,
      ...details,
    },
  })
}

function ErrorHarness(props: {
  onSend: (parts: NewMessagePart[]) => Promise<boolean>
  error: unknown
}) {
  return <Composer onSend={props.onSend} error={props.error} />
}

function PendingRetryHarness(props: {
  onSend: (parts: NewMessagePart[], options?: { clientMessageId?: string }) => Promise<boolean>
  pendingError: ApiError
}) {
  const [error, setError] = useState<unknown>(undefined)
  return (
    <Composer
      error={error}
      onSend={async (parts, options) => {
        const sent = await props.onSend(parts, options)
        setError(sent ? undefined : props.pendingError)
        return sent
      }}
    />
  )
}

/**
 * Simulates the page transport: each failed send publishes the next typed 409,
 * and a successful send clears the error, exactly like `useConversation.send`
 * + `ChatPanel` do in production.
 */
function AutoRetryHarness(props: {
  onSend: (parts: NewMessagePart[], options?: { clientMessageId?: string }) => Promise<boolean>
  errors: ApiError[]
}) {
  const [error, setError] = useState<unknown>(undefined)
  const index = useRef(0)
  return (
    <Composer
      error={error}
      onSend={async (parts, options) => {
        const sent = await props.onSend(parts, options)
        if (sent) {
          setError(undefined)
        } else {
          const next = props.errors[Math.min(index.current, props.errors.length - 1)]
          index.current += 1
          setError(next)
        }
        return sent
      }}
    />
  )
}

describe("Composer", () => {
  beforeEach(() => {
    api.uploadContent.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it("sends on Enter and clears the textarea on success", async () => {
    const onSend = vi.fn().mockResolvedValue(true)
    render(<Composer onSend={onSend} />)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "退款 T+1 到账" } })
    fireEvent.keyDown(textarea, { key: "Enter", shiftKey: false })

    await waitFor(() =>
      expect(onSend).toHaveBeenCalledWith(
        [{ type: "text", text: "退款 T+1 到账" }],
        expect.objectContaining({ clientMessageId: expect.any(String) }),
      ),
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
    // C-04: the manual retry reuses the message id of the rejected attempt.
    const firstId = onSend.mock.calls[0][1]?.clientMessageId
    expect(firstId).toEqual(expect.any(String))
    expect(firstId?.length).toBeGreaterThan(0)
    expect(onSend.mock.calls[1][1]?.clientMessageId).toBe(firstId)
    await waitFor(() => expect(textarea).toHaveValue(""))
    await waitFor(() => expect(screen.queryByText("spec.md")).not.toBeInTheDocument())
  })

  it("shows the parse progress of a 409 attachment_pending and a retry countdown", async () => {
    const onSend = vi.fn().mockResolvedValue(false)
    const pending = attachmentPending({
      stage: "analyzing_images",
      images_total: 12,
      images_done: 3,
      images_failed: 0,
      images_skipped: 0,
      percent: 25,
      eta_seconds: 60,
      retry_after_seconds: 30,
    })

    render(<AutoRetryHarness onSend={onSend} errors={[pending]} />)
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))
    const progress = await screen.findByTestId("composer-retry-progress")
    expect(progress).toHaveTextContent("图片 3/12")
    expect(progress).toHaveTextContent("25%")
    expect(progress).toHaveTextContent("预计剩余 60 秒")
    expect(progress).toHaveTextContent("30 秒后自动重试")
    const cancel = screen.getByRole("button", { name: "取消自动重试" })
    // The Cancel action is a control, not an announcement: it must not sit
    // inside the aria-live status region (screen readers would re-read it).
    for (const status of screen.getAllByRole("status")) {
      expect(status).not.toContainElement(cancel)
    }
    // The draft is preserved while the attachment finishes parsing.
    expect(textarea).toHaveValue("请读附件")
  })

  it("auto-sends exactly once with a stable client_message_id when parsing is done", async () => {
    const onSend = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    const pending = attachmentPending({ stage: "done", images_total: 0 })

    render(<AutoRetryHarness onSend={onSend} errors={[pending]} />)
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2))
    expect(onSend.mock.calls[1][0]).toEqual(onSend.mock.calls[0][0])
    // Stable idempotency id: the retried POST can never create a second message.
    expect(onSend.mock.calls[1][1]?.clientMessageId).toBe(onSend.mock.calls[0][1]?.clientMessageId)
    await waitFor(() => expect(textarea).toHaveValue(""))

    // No third automatic send after the success cleared the error.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(onSend).toHaveBeenCalledTimes(2)
  })

  it("keeps text typed while the attachment is still parsing", async () => {
    let resolveFirst: (value: boolean) => void = () => {}
    const onSend = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<boolean>((resolve) => {
            resolveFirst = resolve
          }),
      )
      .mockResolvedValueOnce(true)
    const pending = attachmentPending({ stage: "done", images_total: 0 })

    render(<AutoRetryHarness onSend={onSend} errors={[pending]} />)
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))

    // The textarea stays editable during the wait; a new draft typed now must
    // not be wiped when the delayed snapshot is finally delivered.
    fireEvent.change(textarea, { target: { value: "等待期间写的新消息" } })
    resolveFirst(false)

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2))
    // The auto-send posts the snapshot taken at chain creation, not the new draft.
    expect(onSend.mock.calls[1][0]).toEqual([{ type: "text", text: "请读附件" }])
    expect(await screen.findByTestId("composer-sent-note")).toHaveTextContent("已发送")
    expect(textarea).toHaveValue("等待期间写的新消息")
  })

  it("keeps text typed while the queued retry timer is pending", async () => {
    vi.useFakeTimers()
    // Production path: the backend's `isAttachmentPending` only emits
    // pending/processing stages, so the retry runs from the timer scheduled by
    // the `error` effect, never from the (unreachable) `done` branch.
    const onSend = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    const queued = attachmentPending({ stage: "queued", retry_after_seconds: 2 })

    render(<AutoRetryHarness onSend={onSend} errors={[queued]} />)
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(onSend).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId("composer-retry-progress")).toHaveTextContent("2 秒后自动重试")

    // The textarea stays editable while the retry timer waits; this render must
    // not reschedule the timer (the effect only depends on `error`).
    fireEvent.change(textarea, { target: { value: "等待期间写的新消息" } })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })

    expect(onSend).toHaveBeenCalledTimes(2)
    // The retry still posts the chain snapshot captured at chain creation.
    expect(onSend.mock.calls[1][0]).toEqual([{ type: "text", text: "请读附件" }])
    // The timer callback runs from the render that scheduled it; the comparison
    // must use the live draft, not that stale closure, or this text is wiped.
    expect(textarea).toHaveValue("等待期间写的新消息")
    expect(screen.getByTestId("composer-sent-note")).toHaveTextContent("已发送")
  })

  it("stops auto-retry on a failed parse and shows the reason", async () => {
    const onSend = vi.fn().mockResolvedValue(false)
    const failed = attachmentPending({
      stage: "failed",
      images_total: 12,
      images_done: 4,
      images_failed: 1,
      error: "图片分析失败",
    })

    render(<AutoRetryHarness onSend={onSend} errors={[failed]} />)
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    const progress = await screen.findByTestId("composer-retry-progress")
    expect(progress).toHaveTextContent("解析失败")
    expect(progress).toHaveTextContent("图片分析失败")
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(onSend).toHaveBeenCalledTimes(1)
    expect(screen.getByRole("button", { name: "重试发送" })).toBeInTheDocument()
  })

  it("cancels the automatic retry and falls back to the manual retry", async () => {
    const onSend = vi.fn().mockResolvedValue(false)
    const queued = attachmentPending({ stage: "queued", retry_after_seconds: 1 })

    render(<AutoRetryHarness onSend={onSend} errors={[queued]} />)
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await screen.findByTestId("composer-retry-progress")
    fireEvent.click(screen.getByRole("button", { name: "取消自动重试" }))
    expect(screen.queryByRole("button", { name: "取消自动重试" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "重试发送" })).toBeInTheDocument()

    // The cancelled timer never fires another send.
    await new Promise((resolve) => setTimeout(resolve, 1200))
    expect(onSend).toHaveBeenCalledTimes(1)
  })

  it("caps the automatic retry window at five minutes", () => {
    expect(autoRetryExpired(0, AUTO_RETRY_MAX_MS - 1)).toBe(false)
    expect(autoRetryExpired(0, AUTO_RETRY_MAX_MS)).toBe(true)
  })

  it("stops the timer-driven retry at the five minute cap", async () => {
    vi.useFakeTimers()
    const onSend = vi.fn().mockResolvedValue(false)
    // A single 5-minute poll window: the next timer fires exactly at the cap.
    const queued = attachmentPending({ stage: "queued", retry_after_seconds: 300 })

    render(<AutoRetryHarness onSend={onSend} errors={[queued]} />)
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(0)
    expect(onSend).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(AUTO_RETRY_MAX_MS)
    expect(onSend).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId("composer-retry-progress")).toHaveTextContent("5 分钟上限")
  })

  it("clears the pending automatic retry on unmount", async () => {
    vi.useFakeTimers()
    const onSend = vi.fn().mockResolvedValue(false)
    const queued = attachmentPending({ stage: "queued", retry_after_seconds: 1 })

    const { unmount } = render(<AutoRetryHarness onSend={onSend} errors={[queued]} />)
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请读附件" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(0)
    expect(onSend).toHaveBeenCalledTimes(1)

    unmount()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(onSend).toHaveBeenCalledTimes(1)
  })
})

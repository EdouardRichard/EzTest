import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type { UploadedAttachment } from "./types"

const api = vi.hoisted(() => ({ uploadContent: vi.fn() }))
vi.mock("./api", () => api)

import { AttachmentUploader } from "./AttachmentUploader"

const CONTENT = {
  sha256: "a".repeat(64),
  size: 12,
  mime: "text/plain",
  url: `/api/content/${"a".repeat(64)}`,
}

function Harness(props: {
  maxBytes?: number
  onUploadingChange?: (uploading: boolean) => void
  onChange?: (items: UploadedAttachment[]) => void
}) {
  const [items, setItems] = useState<UploadedAttachment[]>([])
  return (
    <AttachmentUploader
      items={items}
      onChange={(next) => {
        setItems(next)
        props.onChange?.(next)
      }}
      maxBytes={props.maxBytes}
      onUploadingChange={props.onUploadingChange}
    />
  )
}

function RemovableHarness(props: { onChange: (items: UploadedAttachment[]) => void }) {
  const [items, setItems] = useState<UploadedAttachment[]>([
    { sha256: CONTENT.sha256, name: "退款规则V2.pdf", mime: "application/pdf", size: 12 },
  ])
  return (
    <AttachmentUploader
      items={items}
      onChange={(next) => {
        setItems(next)
        props.onChange(next)
      }}
    />
  )
}

describe("AttachmentUploader", () => {
  beforeEach(() => {
    api.uploadContent.mockReset()
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:test") }))
  })

  afterEach(cleanup)

  it("uploads a selected file and emits the content object as an attachment", async () => {
    api.uploadContent.mockResolvedValue(CONTENT)
    const onChange = vi.fn()
    const onUploadingChange = vi.fn()
    render(<Harness onChange={onChange} onUploadingChange={onUploadingChange} />)

    const file = new File(["hello 退款"], "退款.txt", { type: "text/plain" })
    fireEvent.change(screen.getByTestId("attachment-input"), { target: { files: [file] } })

    await waitFor(() => expect(onChange).toHaveBeenCalled())
    expect(api.uploadContent).toHaveBeenCalledWith(file)
    expect(onChange).toHaveBeenCalledWith([
      { sha256: CONTENT.sha256, name: "退款.txt", mime: "text/plain", size: 12 },
    ])
    expect(onUploadingChange).toHaveBeenCalledWith(true)
    expect(onUploadingChange).toHaveBeenLastCalledWith(false)
    expect(await screen.findByText("退款.txt")).toBeInTheDocument()
  })

  it("shows an inline error and keeps the list unchanged when upload fails", async () => {
    api.uploadContent.mockRejectedValue(
      new ApiError({ status: 413, code: "payload_too_large", message: "附件超过 10 MiB" }),
    )
    const onChange = vi.fn()
    render(<Harness onChange={onChange} />)

    fireEvent.change(screen.getByTestId("attachment-input"), {
      target: { files: [new File(["x"], "big.pdf")] },
    })

    expect(await screen.findByText(/附件超过 10 MiB/)).toBeInTheDocument()
    expect(onChange).not.toHaveBeenCalled()
  })

  it("rejects oversized files locally before uploading", async () => {
    const onChange = vi.fn()
    render(<Harness maxBytes={10} onChange={onChange} />)

    fireEvent.change(screen.getByTestId("attachment-input"), {
      target: { files: [new File(["0123456789X"], "too-big.txt")] },
    })

    expect(await screen.findByText(/不能超过/)).toBeInTheDocument()
    expect(api.uploadContent).not.toHaveBeenCalled()
    expect(onChange).not.toHaveBeenCalled()
  })

  it("removes an uploaded attachment", async () => {
    const onChange = vi.fn()
    render(<RemovableHarness onChange={onChange} />)

    fireEvent.click(screen.getByRole("button", { name: "移除附件 退款规则V2.pdf" }))

    expect(onChange).toHaveBeenCalledWith([])
    expect(screen.queryByText("退款规则V2.pdf")).not.toBeInTheDocument()
  })
})

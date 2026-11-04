import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { MessageList } from "./MessageList"
import type { ConversationMessage, MessagePart } from "./types"

const api = vi.hoisted(() => ({ downloadContent: vi.fn() }))
vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  downloadContent: api.downloadContent,
}))

const SHA = "e".repeat(64)

function userMessage(parts: MessagePart[]): ConversationMessage {
  return {
    id: "1",
    conversation_id: "2",
    role: "user",
    parts,
    refs: [],
    created_at: "2026-11-01T00:00:00Z",
  }
}

describe("MessageList", () => {
  beforeEach(() => {
    api.downloadContent.mockReset()
    vi.stubGlobal(
      "URL",
      Object.assign(URL, {
        createObjectURL: vi.fn(() => "blob:image"),
        revokeObjectURL: vi.fn(),
      }),
    )
  })

  afterEach(cleanup)

  it("renders an image part through the authenticated content download", async () => {
    api.downloadContent.mockResolvedValue({
      blob: new Blob(["png"], { type: "image/png" }),
      filename: "",
      contentType: "image/png",
    })

    render(
      <MessageList
        versionId="2001"
        messages={[
          userMessage([
            { type: "image", mime: "image/png", data_ref: SHA, width: 800, height: 600 },
          ]),
        ]}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )

    const image = await screen.findByTestId("message-image")
    expect(api.downloadContent).toHaveBeenCalledWith(SHA)
    expect(image).toHaveAttribute("src", "blob:image")
    expect(image).toHaveAttribute("width", "800")
    expect(image).toHaveAttribute("height", "600")
  })

  it("shows a readable error when the image content cannot be loaded", async () => {
    api.downloadContent.mockRejectedValue(new Error("content unavailable"))

    render(
      <MessageList
        versionId="2001"
        messages={[
          userMessage([
            { type: "image", mime: "image/png", data_ref: SHA, width: 800, height: 600 },
          ]),
        ]}
        activity={null}
        onRequirementSelect={vi.fn()}
      />,
    )

    expect(await screen.findByText("图片加载失败")).toBeInTheDocument()
  })
})

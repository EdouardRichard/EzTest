import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { conversationFixture, messageFixture } from "@/features/clarification/test-fixtures"
import type { ConversationSummary } from "@/features/clarification/types"
import type { PublicModel } from "@/features/conversation/api"
import { DesignChat, type DesignChatProps } from "@/features/design/DesignChat"

const clarificationApi = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  getTeamRoom: vi.fn(),
  rollbackRequirement: vi.fn(),
  uploadContent: vi.fn(),
  getAttachmentPendingDetails: vi.fn(),
}))
vi.mock("@/features/clarification/api", () => clarificationApi)

const conversationApi = vi.hoisted(() => ({
  listPublicModels: vi.fn(),
  updateConversationModel: vi.fn(),
}))
vi.mock("@/features/conversation/api", () => conversationApi)

import { ChatPanel } from "./ChatPanel"

const VISION_WARNING = "当前草稿含图片，请选择支持视觉的模型"
const IMAGE_NAME = "登录截图.png"

function publicModel(
  overrides: Partial<Omit<PublicModel, "capabilities">> & {
    capabilities?: Partial<PublicModel["capabilities"]>
  },
): PublicModel {
  const base: PublicModel = {
    id: "8101",
    config_id: "7101",
    config_name: "Primary",
    model: "gpt-4o",
    display_name: "GPT-4o",
    context_window: 128000,
    capabilities: {
      supports_tools: true,
      supports_vision: true,
      supports_reasoning: true,
      context_window: 128000,
      supports_prompt_cache: true,
    },
    default: false,
    deprecated: false,
  }
  return {
    ...base,
    ...overrides,
    capabilities: { ...base.capabilities, ...overrides.capabilities },
  }
}

const visionDefault = publicModel({ model: "gpt-4o", display_name: "GPT-4o", default: true })
const visionOther = publicModel({ id: "8102", model: "vision-plus", display_name: "Vision Plus" })
const textOnly = publicModel({
  id: "8103",
  model: "text-only",
  display_name: "Text Only",
  capabilities: { supports_vision: false },
})

const IMAGE_CONTENT = {
  sha256: "c".repeat(64),
  size: 12,
  mime: "image/png",
  url: `/api/content/${"c".repeat(64)}`,
}

function renderChatPanel(overrides: Partial<Parameters<typeof ChatPanel>[0]> = {}) {
  return render(
    <ChatPanel
      versionId="2001"
      conversationId="4001"
      conversations={[conversationFixture]}
      onSelectConversation={vi.fn()}
      onCreateConversation={vi.fn()}
      onRequirementSelect={vi.fn()}
      liveMessages={[]}
      activity={null}
      {...overrides}
    />,
  )
}

const designConversation: ConversationSummary = {
  ...conversationFixture,
  kind: "design",
  title: "用例设计 2026-10-31 10:00",
}

function renderDesignChat(overrides: Partial<DesignChatProps> = {}) {
  const props: DesignChatProps = {
    versionId: "2001",
    conversations: [designConversation],
    activeConversationId: "4001",
    onSelectConversation: vi.fn(),
    onCreateConversation: vi.fn(),
    liveMessages: [],
    ...overrides,
  }
  render(<DesignChat {...props} />)
  return props
}

/** Uploads one image through the Composer's real attachment pipeline. */
async function uploadDraftImage(name: string = IMAGE_NAME): Promise<void> {
  fireEvent.change(screen.getByTestId("attachment-input"), {
    target: { files: [new File(["png-bytes"], name, { type: "image/png" })] },
  })
  await screen.findByText(name)
  // The uploader commits the attachment asynchronously; wait until the chip is
  // no longer pending before asserting on the resulting gating.
  await waitFor(() => expect(screen.queryByTestId("attachment-pending")).not.toBeInTheDocument())
}

describe("ChatPanel vision gating", () => {
  beforeEach(() => {
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    for (const fn of Object.values(conversationApi)) fn.mockReset()
    clarificationApi.listMessages.mockResolvedValue([messageFixture])
    clarificationApi.uploadContent.mockResolvedValue(IMAGE_CONTENT)
    conversationApi.listPublicModels.mockResolvedValue([visionOther, visionDefault, textOnly])
  })

  afterEach(cleanup)

  it("disables non-vision models while the draft has an image and re-enables after removal", async () => {
    renderChatPanel()

    const textOnlyOption = await screen.findByRole("option", { name: /Text Only/ })
    expect(textOnlyOption).not.toBeDisabled()

    await uploadDraftImage()

    await waitFor(() => expect(textOnlyOption).toBeDisabled())
    expect(textOnlyOption).toHaveTextContent("不支持图片")
    expect(screen.getByRole("option", { name: /Vision Plus/ })).not.toBeDisabled()

    fireEvent.click(screen.getByRole("button", { name: `移除附件 ${IMAGE_NAME}` }))

    await waitFor(() => expect(textOnlyOption).not.toBeDisabled())
    expect(textOnlyOption).not.toHaveTextContent("不支持图片")
  })

  it("keeps a selected non-vision model and warns instead of silently switching", async () => {
    renderChatPanel({
      conversations: [{ ...conversationFixture, model_override: "text-only" }],
    })

    const select = await screen.findByTestId("model-selector")
    expect(select).toHaveValue("text-only")

    await uploadDraftImage()

    await waitFor(() => expect(screen.getByRole("option", { name: /Text Only/ })).toBeDisabled())
    // No silent switch: the persisted override stays selected and the user is
    // told to pick a vision-capable model deliberately (D24).
    expect(select).toHaveValue("text-only")
    expect(screen.getByText(VISION_WARNING)).toBeInTheDocument()
    expect(conversationApi.updateConversationModel).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: `移除附件 ${IMAGE_NAME}` }))

    await waitFor(() => expect(screen.queryByText(VISION_WARNING)).not.toBeInTheDocument())
    expect(select).toHaveValue("text-only")
  })

  it("blocks send when the image draft is pinned to a non-vision model", async () => {
    renderChatPanel({
      conversations: [{ ...conversationFixture, model_override: "text-only" }],
    })

    const textarea = await screen.findByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请看这张截图" } })
    const send = screen.getByRole("button", { name: "发送" })
    expect(send).not.toBeDisabled()

    await uploadDraftImage()

    // The advisory warning is not enough (admin B1): the send itself must be
    // blocked, with the reason, so no 201 ghost message and no doomed job.
    await waitFor(() => expect(send).toBeDisabled())
    expect(screen.getByTestId("composer-send-blocked")).toHaveTextContent(/不支持图片/)
    fireEvent.click(send)
    expect(clarificationApi.postMessage).not.toHaveBeenCalled()

    // Removing the image unblocks the same draft.
    fireEvent.click(screen.getByRole("button", { name: `移除附件 ${IMAGE_NAME}` }))
    await waitFor(() => expect(send).not.toBeDisabled())
    expect(screen.queryByTestId("composer-send-blocked")).not.toBeInTheDocument()
  })

  it("does not gate on a text attachment", async () => {
    clarificationApi.uploadContent.mockResolvedValue({
      ...IMAGE_CONTENT,
      mime: "application/pdf",
    })
    renderChatPanel()

    const textOnlyOption = await screen.findByRole("option", { name: /Text Only/ })
    fireEvent.change(screen.getByTestId("attachment-input"), {
      target: { files: [new File(["pdf-bytes"], "需求说明.pdf", { type: "application/pdf" })] },
    })
    await screen.findByText("需求说明.pdf")

    await waitFor(() => expect(textOnlyOption).not.toBeDisabled())
    expect(screen.queryByText(VISION_WARNING)).not.toBeInTheDocument()
  })
})

describe("DesignChat vision gating", () => {
  beforeEach(() => {
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    for (const fn of Object.values(conversationApi)) fn.mockReset()
    clarificationApi.listMessages.mockResolvedValue([messageFixture])
    clarificationApi.uploadContent.mockResolvedValue(IMAGE_CONTENT)
    conversationApi.listPublicModels.mockResolvedValue([visionOther, visionDefault, textOnly])
  })

  afterEach(cleanup)

  it("disables non-vision models while the design draft has an image and re-enables after removal", async () => {
    renderDesignChat()

    const textOnlyOption = await screen.findByRole("option", { name: /Text Only/ })
    expect(textOnlyOption).not.toBeDisabled()

    await uploadDraftImage()

    await waitFor(() => expect(textOnlyOption).toBeDisabled())
    expect(screen.getByRole("option", { name: /Vision Plus/ })).not.toBeDisabled()

    fireEvent.click(screen.getByRole("button", { name: `移除附件 ${IMAGE_NAME}` }))

    await waitFor(() => expect(textOnlyOption).not.toBeDisabled())
  })

  it("blocks send for a collapsed image draft pinned to a non-vision model", async () => {
    // The collapsed design chat hides the model selector; the send gate must
    // still be enforced from the persisted conversation model.
    renderDesignChat({
      collapsed: true,
      conversations: [{ ...designConversation, model_override: "text-only" }],
    })
    expect(screen.queryByTestId("model-selector")).not.toBeInTheDocument()

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "请看这张设计截图" } })
    const send = screen.getByRole("button", { name: "发送" })
    expect(send).not.toBeDisabled()

    await uploadDraftImage()

    await waitFor(() => expect(send).toBeDisabled())
    expect(screen.getByTestId("composer-send-blocked")).toBeInTheDocument()
    fireEvent.click(send)
    expect(clarificationApi.postMessage).not.toHaveBeenCalled()
  })
})

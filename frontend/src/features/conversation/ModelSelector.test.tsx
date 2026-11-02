import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { ConversationMessage } from "@/features/clarification/types"
import { ApiError } from "@/lib/api-client"

import type { PublicModel, PublicModelCapabilities } from "./api"

const api = vi.hoisted(() => ({
  listPublicModels: vi.fn(),
  updateConversationModel: vi.fn(),
}))
vi.mock("./api", () => api)

import { MessageList } from "@/features/clarification/MessageList"

import { ModelSelector } from "./ModelSelector"

const TIMESTAMP = "2026-11-01T10:00:00Z"

function publicModel(
  overrides: Partial<Omit<PublicModel, "capabilities">> & {
    capabilities?: Partial<PublicModelCapabilities>
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

const defaultModel = publicModel({
  id: "8101",
  model: "gpt-4o",
  display_name: "GPT-4o",
  default: true,
})
const visionModel = publicModel({
  id: "8102",
  model: "vision-plus",
  display_name: "Vision Plus",
  default: false,
})
const textOnlyModel = publicModel({
  id: "8103",
  model: "text-only",
  display_name: "Text Only",
  default: false,
  capabilities: { supports_vision: false },
})
const deprecatedModel = publicModel({
  id: "8104",
  model: "retired-llm",
  display_name: "Retired",
  deprecated: true,
})

function renderSelector(
  overrides: {
    conversationId?: string | null
    modelOverride?: string | null
    draftHasImages?: boolean
  } = {},
) {
  return render(
    <ModelSelector
      versionId="2001"
      conversationId={overrides.conversationId ?? "4001"}
      modelOverride={overrides.modelOverride ?? null}
      draftHasImages={overrides.draftHasImages ?? false}
    />,
  )
}

beforeEach(() => {
  api.listPublicModels.mockReset()
  api.updateConversationModel.mockReset()
  api.listPublicModels.mockResolvedValue([
    defaultModel,
    visionModel,
    textOnlyModel,
    deprecatedModel,
  ])
})

afterEach(cleanup)

describe("model selector", () => {
  it("selects the LLM config default model by default", async () => {
    renderSelector()

    const select = await screen.findByTestId("model-selector")
    expect(select).toHaveValue("gpt-4o")
    expect(select).not.toBeDisabled()
    // The default is the config's default_model_id, never models[0].
    expect((screen.getByRole("option", { name: /GPT-4o/ }) as HTMLOptionElement).selected).toBe(
      true,
    )
  })

  it("uses the conversation override instead of the configured default", async () => {
    renderSelector({ modelOverride: "text-only" })

    const select = await screen.findByTestId("model-selector")
    expect(select).toHaveValue("text-only")
  })

  it("sends the override when the user switches model", async () => {
    api.updateConversationModel.mockResolvedValue({ id: "4001" })
    renderSelector()

    const select = await screen.findByTestId("model-selector")
    fireEvent.change(select, { target: { value: "vision-plus" } })

    await waitFor(() =>
      expect(api.updateConversationModel).toHaveBeenCalledWith("2001", "4001", "vision-plus"),
    )
    expect(select).toHaveValue("vision-plus")
  })

  it("reverts and reports the failure instead of silently switching", async () => {
    api.updateConversationModel.mockRejectedValue(
      new ApiError({ status: 400, code: "invalid_argument", message: "模型不可用" }),
    )
    renderSelector()

    const select = await screen.findByTestId("model-selector")
    fireEvent.change(select, { target: { value: "text-only" } })

    expect(await screen.findByRole("alert")).toHaveTextContent("模型不可用")
    await waitFor(() => expect(select).toHaveValue("gpt-4o"))
  })

  it("greys out non-vision models while the draft has images", async () => {
    renderSelector({ draftHasImages: true })

    await screen.findByTestId("model-selector")
    expect(screen.getByRole("option", { name: /Text Only/ })).toBeDisabled()
    expect(screen.getByRole("option", { name: /GPT-4o/ })).not.toBeDisabled()
  })

  it("shows the actual model on assistant messages only", async () => {
    const assistant: ConversationMessage = {
      id: "5001",
      conversation_id: "4001",
      role: "assistant",
      parts: [{ type: "text", text: "我已读取退款规则。" }],
      refs: [],
      created_at: TIMESTAMP,
      actual_model: "gpt-4o",
    }
    const user: ConversationMessage = {
      id: "5002",
      conversation_id: "4001",
      role: "user",
      parts: [{ type: "text", text: "请总结退款规则。" }],
      refs: [],
      created_at: TIMESTAMP,
      actual_model: null,
    }

    render(
      <MessageList
        versionId="2001"
        messages={[user, assistant]}
        activity={null}
        onRequirementSelect={() => {}}
      />,
    )

    const badges = screen.getAllByTestId("actual-model-badge")
    expect(badges).toHaveLength(1)
    expect(badges[0]).toHaveTextContent("gpt-4o")
  })
})

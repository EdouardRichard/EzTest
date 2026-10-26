import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiStreamFetch: vi.fn(),
}))

vi.mock("@/lib/api-client", () => {
  class ApiError extends Error {
    readonly status: number
    constructor(status: number, message: string) {
      super(message)
      this.name = "ApiError"
      this.status = status
    }
  }
  return {
    ApiError,
    apiGet: mocks.apiGet,
    apiPost: mocks.apiPost,
    apiPatch: mocks.apiPatch,
    apiStreamFetch: mocks.apiStreamFetch,
    resolveApiUrl: (path: string) => `http://localhost:13333${path}`,
  }
})

import { LLMPanel } from "./LLMPanel"

const CONFIG = {
  id: 1,
  name: "deepseek",
  protocol: "chat_completions",
  base_url: "https://api.deepseek.com",
  model: "deepseek-chat",
  has_api_key: true,
  key_version: 2,
  header_names: ["X-Tenant"],
  retry_count: 2,
  retry_interval_ms: 500,
  timeout_ms: 60000,
  stream_enabled: true,
  multimodal_enabled: true,
  is_default: true,
  model_catalog: [
    {
      id: "deepseek-chat",
      context_window: 1048576,
      max_output_tokens: 8192,
      input_modalities: ["text"],
    },
  ],
  catalog_fetched_at: "2026-10-27T00:00:00Z",
  // Hostile/legacy payload field: the UI must never render secret material.
  api_key: "sk-should-never-render",
}

describe("LLMPanel", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiPatch.mockReset()
    mocks.apiStreamFetch.mockReset()
    mocks.apiGet.mockResolvedValue([CONFIG])
    mocks.apiPatch.mockResolvedValue(CONFIG)
  })

  afterEach(() => {
    cleanup()
  })

  it("renders masked config metadata and keeps secrets when the edit fields stay blank", async () => {
    render(<LLMPanel />)

    await waitFor(() => expect(screen.getByText("deepseek")).toBeTruthy())
    expect(mocks.apiGet).toHaveBeenCalledWith("/api/admin/llm/configs")
    expect(screen.getByText("X-Tenant")).toBeTruthy()
    expect(screen.getByText(/1,048,576/)).toBeTruthy()
    expect(screen.getByText(/版本 2/)).toBeTruthy()
    expect(document.body.textContent).not.toContain("sk-should-never-render")

    fireEvent.click(screen.getByRole("button", { name: "编辑" }))
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))

    await waitFor(() => expect(mocks.apiPatch).toHaveBeenCalledTimes(1))
    const [path, payload] = mocks.apiPatch.mock.calls[0] as [string, Record<string, unknown>]
    expect(path).toBe("/api/admin/llm/configs/1")
    expect(payload).not.toHaveProperty("api_key")
    expect(payload).not.toHaveProperty("headers")
    expect(payload).toMatchObject({
      name: "deepseek",
      protocol: "chat_completions",
      model: "deepseek-chat",
      is_default: true,
    })
  })
})

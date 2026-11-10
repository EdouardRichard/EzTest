import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { LLMModel } from "./api"
import { ModelEditDialog } from "./ModelEditDialog"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

/** Catalog-owned provenance shared by the capability fixtures. */
const catalogCapabilities: NonNullable<LLMModel["capabilities"]> = {
  supports_tools: true,
  supports_vision: false,
  supports_reasoning: false,
  supports_prompt_cache: false,
  context_window: 64000,
  supports_tools_source: "catalog",
  supports_vision_source: "catalog",
  supports_reasoning_source: "catalog",
  supports_prompt_cache_source: "catalog",
}

/**
 * All four capabilities are catalog-owned, so a single checkbox edit must not
 * silently turn the untouched three into manual overrides (R2-S-01).
 */
const catalogOwnedModel: LLMModel = {
  id: "8010",
  config_id: "7001",
  model: "catalog-model",
  display_name: "catalog-model",
  context_window: 64000,
  max_output: 4096,
  max_input: 59904,
  supports_tools: true,
  supports_vision: false,
  supports_tools_source: "catalog",
  supports_vision_source: "catalog",
  supports_reasoning_source: "catalog",
  supports_prompt_cache_source: "catalog",
  capabilities: catalogCapabilities,
  deprecated: false,
  source: "catalog",
  window_source: "catalog",
  output_source: "catalog",
  status: "active",
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
}

type RouteHandler = (url: URL, init: RequestInit) => Response | Promise<Response>

function installFetch(handler: RouteHandler): void {
  fetchMock.mockImplementation(async (input, init) => {
    const url = new URL(String(input))
    return handler(url, init ?? {})
  })
}

function method(init: RequestInit): string {
  return init.method ?? "GET"
}

function readBody(init: RequestInit): unknown {
  return typeof init.body === "string" ? (JSON.parse(init.body) as unknown) : undefined
}

function renderDialog(model: LLMModel): {
  onClose: ReturnType<typeof vi.fn>
  onModelChange: ReturnType<typeof vi.fn>
} {
  const onClose = vi.fn()
  const onModelChange = vi.fn()
  render(<ModelEditDialog model={model} onClose={onClose} onModelChange={onModelChange} />)
  return { onClose, onModelChange }
}

/** The last PATCH request body, parsed from the mocked fetch calls. */
function lastPatchBody(): unknown {
  const patches = fetchMock.mock.calls.filter((call) => method(call[1] ?? {}) === "PATCH")
  expect(patches).toHaveLength(1)
  return readBody(patches[0]?.[1] ?? {})
}

async function save(onClose: ReturnType<typeof vi.fn>): Promise<void> {
  fireEvent.click(screen.getByRole("button", { name: "保存" }))
  await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  expect(fetchMock.mock.calls.some((call) => method(call[1] ?? {}) === "PATCH")).toBe(true)
}

describe("ModelEditDialog capability diffing", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("patches only the capability that changed", async () => {
    installFetch((_url, init) => {
      if (method(init) === "PATCH") {
        return jsonResponse({ data: { ...catalogOwnedModel, supports_vision: true } })
      }
      return jsonResponse({ data: [] })
    })
    const { onClose } = renderDialog(catalogOwnedModel)

    fireEvent.click(screen.getByLabelText("支持视觉"))
    await save(onClose)

    expect(lastPatchBody()).toEqual({ supports_vision: true, deprecated: false })
  })

  it("patches every edited capability but leaves catalog-owned capabilities out", async () => {
    installFetch((_url, init) => {
      if (method(init) === "PATCH") {
        return jsonResponse({
          data: { ...catalogOwnedModel, supports_vision: true, supports_reasoning: true },
        })
      }
      return jsonResponse({ data: [] })
    })
    const { onClose } = renderDialog(catalogOwnedModel)

    fireEvent.click(screen.getByLabelText("支持视觉"))
    fireEvent.click(screen.getByLabelText("支持推理"))
    await save(onClose)

    expect(lastPatchBody()).toEqual({
      supports_vision: true,
      supports_reasoning: true,
      deprecated: false,
    })
  })

  it("keeps unchanged numeric parameters out while still patching an edited window", async () => {
    installFetch((_url, init) => {
      if (method(init) === "PATCH") {
        return jsonResponse({ data: { ...catalogOwnedModel, context_window: 128000 } })
      }
      return jsonResponse({ data: [] })
    })
    const { onClose } = renderDialog(catalogOwnedModel)

    fireEvent.click(screen.getByLabelText("支持视觉"))
    fireEvent.change(screen.getByLabelText("上下文窗口 (K)"), { target: { value: "128" } })
    await save(onClose)

    expect(lastPatchBody()).toEqual({
      supports_vision: true,
      context_window: 128000,
      deprecated: false,
    })
  })

  it("diffs against the restored baseline after restoring upstream values", async () => {
    // Manual numeric overrides keep 恢复上游值 enabled; the restored row then
    // becomes the save baseline for the capability diff.
    const manualParamsModel: LLMModel = {
      ...catalogOwnedModel,
      window_source: "manual",
      output_source: "manual",
    }
    const restored: LLMModel = {
      ...manualParamsModel,
      window_source: "catalog",
      output_source: "catalog",
      supports_vision: true,
      capabilities: { ...catalogCapabilities, supports_vision: true },
    }
    installFetch((url, init) => {
      if (url.pathname.endsWith("/reset-params") && method(init) === "POST") {
        return jsonResponse({ data: restored })
      }
      if (method(init) === "PATCH") {
        return jsonResponse({ data: { ...restored, supports_vision: false } })
      }
      return jsonResponse({ data: [] })
    })
    const { onClose } = renderDialog(manualParamsModel)

    fireEvent.click(screen.getByRole("button", { name: "恢复上游值" }))
    await waitFor(() => expect(screen.getByLabelText("支持视觉")).toBeChecked())
    // The restored row owns vision now, so turning it off is unique to the diff
    // baseline `current`: comparing against the original `model` prop would drop
    // this field from the patch.
    fireEvent.click(screen.getByLabelText("支持视觉"))
    await save(onClose)

    expect(lastPatchBody()).toEqual({ supports_vision: false, deprecated: false })
  })
})

import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
}))

vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return { ...actual, apiGet: mocks.apiGet }
})

import { BasicConfigPanel } from "./BasicConfigPanel"

const HEALTH = {
  status: "ok",
  version: "1.2.3",
  commit: "abcdef1234567890",
  db: "ok",
  llm_configured: true,
  rate_limit: { rps: 25, burst: 50, sse_max: 4 },
  gc: { enabled: true, retention_days: 12, batch_keep: 5 },
}

describe("BasicConfigPanel", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset().mockResolvedValue(HEALTH)
  })

  afterEach(() => {
    cleanup()
  })

  it("shows the read-only runtime projection including the build version", async () => {
    render(<BasicConfigPanel />)

    expect(screen.getByRole("heading", { name: "基础配置" })).toBeTruthy()
    expect(screen.getByText("运行环境")).toBeTruthy()
    expect(screen.getByText("限流与保留")).toBeTruthy()
    expect(screen.getByText("SLO 阈值")).toBeTruthy()
    expect(screen.getByText("存储与配置")).toBeTruthy()

    await waitFor(() => expect(screen.getByText("1.2.3")).toBeTruthy())
    expect(screen.getByText(/abcdef1/)).toBeTruthy()
    // The panel projects the effective health values, not hard-coded defaults.
    expect(screen.getByText("25 次/秒 · 突发 50")).toBeTruthy()
    expect(screen.getByText("4 / 用户")).toBeTruthy()
    expect(screen.getByText("12 天")).toBeTruthy()
    expect(screen.getByText("5 批")).toBeTruthy()
    expect(screen.getByText("允许非 dry-run")).toBeTruthy()
    expect(screen.getByText("≤ 2.5s")).toBeTruthy()
    expect(screen.getByText(/EZTEST_EVIDENCE_DIR/)).toBeTruthy()
    expect(screen.getByText(/修改环境变量后需重启后端服务/)).toBeTruthy()
  })

  it("renders no form control and no secret material", async () => {
    const { container } = render(<BasicConfigPanel />)
    await waitFor(() => expect(screen.getByText("1.2.3")).toBeTruthy())

    // Read-only config projection: no inputs, selects or textareas exist.
    expect(container.querySelectorAll("input, textarea, select")).toHaveLength(0)

    const text = (document.body.textContent ?? "").toLowerCase()
    for (const banned of [
      "api_key",
      "apikey",
      "password",
      "secret",
      "token",
      "authorization",
      "bearer",
      "数据库连接串",
      "eztest_secret_keys",
    ]) {
      expect(text).not.toContain(banned)
    }
    // The panel reports LLM configuration presence as a boolean only.
    expect(screen.getByText("已配置")).toBeTruthy()
  })

  it("keeps the static projection visible when the health read fails", async () => {
    mocks.apiGet.mockRejectedValue(new Error("network down"))
    render(<BasicConfigPanel />)

    expect(await screen.findByText("无法读取运行信息")).toBeTruthy()
    expect(screen.getByText("限流与保留")).toBeTruthy()
    expect(screen.getByText("30 天")).toBeTruthy()
  })
})

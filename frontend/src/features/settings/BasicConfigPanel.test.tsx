import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
}))

vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return { ...actual, apiGet: mocks.apiGet, apiPost: mocks.apiPost }
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

const DRY_RUN_REPORT = {
  dryRun: true,
  candidates: 2,
  protected: 7,
  bytes: 4096,
  deleted: 0,
  failed: 0,
  skipped: 0,
  retentionDays: 30,
  batchKeep: 3,
  candidateLimit: 10000,
}

describe("BasicConfigPanel", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset().mockResolvedValue(HEALTH)
    mocks.apiPost.mockReset()
  })

  afterEach(() => {
    cleanup()
  })

  it("shows the read-only runtime projection including the build version", async () => {
    render(<BasicConfigPanel />)

    expect(screen.getByRole("heading", { name: "基础配置" })).toBeTruthy()
    expect(screen.getByText("配置只读")).toBeTruthy()
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

  it("previews GC candidates and purges only after the second confirmation", async () => {
    mocks.apiPost.mockResolvedValueOnce(DRY_RUN_REPORT)
    render(<BasicConfigPanel />)
    await waitFor(() => expect(screen.getByText("1.2.3")).toBeTruthy())

    fireEvent.click(screen.getByRole("button", { name: "GC dry-run 预览" }))
    await waitFor(() =>
      expect(mocks.apiPost).toHaveBeenCalledWith("/api/admin/gc", { dryRun: true, reason: "settings panel" }),
    )
    expect(await screen.findByText(/候选 2 个/)).toBeTruthy()
    expect(screen.getByText(/受保护 7 个/)).toBeTruthy()
    expect(screen.getByText(/4\.0 KiB/)).toBeTruthy()
    expect(screen.getByText(/单次最多处理 10000 个候选/)).toBeTruthy()

    // The execute button only opens the confirmation; it must not purge yet.
    fireEvent.click(screen.getByRole("button", { name: "执行 GC" }))
    expect(mocks.apiPost).toHaveBeenCalledTimes(1)
    expect(screen.getByRole("button", { name: "确认执行" })).toBeTruthy()

    // Alertdialog focus moves to the least destructive action, and Escape
    // closes it without a request; focus returns to the execute button.
    const cancel = screen.getByRole("button", { name: "取消" })
    await waitFor(() => expect(document.activeElement).toBe(cancel))
    fireEvent.keyDown(cancel, { key: "Escape" })
    expect(screen.queryByRole("button", { name: "确认执行" })).toBeNull()
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "执行 GC" })),
    )
    expect(mocks.apiPost).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole("button", { name: "执行 GC" }))
    mocks.apiPost.mockResolvedValueOnce({
      ...DRY_RUN_REPORT,
      dryRun: false,
      deleted: 2,
      candidates: 2,
    })
    fireEvent.click(screen.getByRole("button", { name: "确认执行" }))
    await waitFor(() =>
      expect(mocks.apiPost).toHaveBeenLastCalledWith("/api/admin/gc", {
        dryRun: false,
        reason: "settings panel",
      }),
    )
    expect(await screen.findByText(/已删除 2 个/)).toBeTruthy()
    // After a real purge the destructive action requires a new dry-run.
    expect((screen.getByRole("button", { name: "执行 GC" }) as HTMLButtonElement).disabled).toBe(true)
  })

  it("cancels the second confirmation and shows the disabled error from the API", async () => {
    mocks.apiPost
      .mockResolvedValueOnce(DRY_RUN_REPORT)
      .mockRejectedValueOnce(
        Object.assign(new Error("evidence GC execution is disabled"), {
          status: 409,
          code: "conflict",
        }),
      )
    render(<BasicConfigPanel />)

    fireEvent.click(await screen.findByRole("button", { name: "GC dry-run 预览" }))
    expect(await screen.findByText(/候选 2 个/)).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "执行 GC" }))
    fireEvent.click(screen.getByRole("button", { name: "取消" }))
    expect(screen.queryByRole("button", { name: "确认执行" })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "执行 GC" }))
    fireEvent.click(screen.getByRole("button", { name: "确认执行" }))
    expect(
      await screen.findByText(
        "evidence GC execution is disabled（409：非 dry-run 需要 EZTEST_GC_ENABLED=true）",
      ),
    ).toBeTruthy()
    expect(mocks.apiPost).toHaveBeenCalledWith("/api/admin/gc", {
      dryRun: false,
      reason: "settings panel",
    })
  })

  it("labels a 403 as an admin-permission failure instead of a disabled switch", async () => {
    mocks.apiPost
      .mockResolvedValueOnce(DRY_RUN_REPORT)
      .mockRejectedValueOnce(
        Object.assign(new Error("forbidden"), { status: 403, code: "forbidden" }),
      )
    render(<BasicConfigPanel />)

    fireEvent.click(await screen.findByRole("button", { name: "GC dry-run 预览" }))
    expect(await screen.findByText(/候选 2 个/)).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "执行 GC" }))
    fireEvent.click(screen.getByRole("button", { name: "确认执行" }))
    expect(
      await screen.findByText(/仅管理员可执行 GC（403）：forbidden/),
    ).toBeTruthy()
    expect(screen.queryByText(/（409：/)).toBeNull()
  })
})

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  validateSession: vi.fn(),
  confirmSession: vi.fn(),
  invalidateSession: vi.fn(),
  revokeSession: vi.fn(),
  requestTakeover: vi.fn(),
  completeTakeover: vi.fn(),
  cancelTakeover: vi.fn(),
}))

vi.mock("./api", () => ({
  validateSession: mocks.validateSession,
  confirmSession: mocks.confirmSession,
  invalidateSession: mocks.invalidateSession,
  revokeSession: mocks.revokeSession,
  requestTakeover: mocks.requestTakeover,
  completeTakeover: mocks.completeTakeover,
  cancelTakeover: mocks.cancelTakeover,
}))

import { ApiError } from "@/lib/api-client"

import { SessionDetail } from "./SessionDetail"
import type { SessionDTO, SnapshotDTO } from "./types"

function makeSnapshot(overrides: Partial<SnapshotDTO> = {}): SnapshotDTO {
  return {
    id: 7,
    sessionId: 1,
    kind: "login_check",
    items: {},
    summary: { total: 1, allowed: 0, denied: 1, error: 0, degraded: true },
    permissionHash: "permission-hash",
    degraded: true,
    capturedAt: "2026-10-27T09:00:00Z",
    ...overrides,
  }
}

function makeSession(overrides: Partial<SessionDTO> = {}): SessionDTO {
  return {
    id: 1,
    accountId: 3,
    systemId: 1,
    environmentId: 5,
    accessId: 11,
    state: "valid",
    acquisition: "import",
    storageStateKind: "storage_state",
    cookieCount: 2,
    originCount: 1,
    storageStateHash: "state-hash",
    expiresAt: "2026-10-28T12:00:00Z",
    lastValidatedAt: "2026-10-27T09:00:00Z",
    lastUsedAt: null,
    failureReason: "",
    invalidatedReason: "",
    expiring: false,
    latestSnapshot: makeSnapshot({ degraded: false, summary: { total: 1, allowed: 1 } }),
    activeLease: null,
    latestTakeover: null,
    ...overrides,
  }
}

function renderDetail(
  session: SessionDTO,
  canWrite = true,
  onChanged = vi.fn().mockResolvedValue(undefined),
) {
  render(
    <SessionDetail
      session={session}
      accountName="QA 01"
      systemName="Shop"
      environmentName="本地"
      canWrite={canWrite}
      onChanged={onChanged}
    />,
  )
  return onChanged
}

const WAITING = makeSession({
  state: "needs_manual",
  expiring: true,
  failureReason: "login_failed",
  latestSnapshot: makeSnapshot(),
  activeLease: { holder: "worker-1", expiresAt: "2026-10-27T10:00:00Z" },
  latestTakeover: { id: 9, status: "waiting", reason: "mfa" },
})

describe("SessionDetail", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.validateSession.mockResolvedValue(makeSession({ state: "valid" }))
    mocks.confirmSession.mockResolvedValue(makeSession({ state: "valid" }))
    mocks.invalidateSession.mockResolvedValue(makeSession({ state: "invalid" }))
    mocks.revokeSession.mockResolvedValue(makeSession({ state: "revoked" }))
    mocks.cancelTakeover.mockResolvedValue({ id: 9, status: "cancelled" })
  })

  afterEach(cleanup)

  it("shows snapshot summary, degraded flag, expiry, lease, takeover and failure reason", () => {
    renderDetail(WAITING)

    expect(screen.getByText("会话 #1")).toBeTruthy()
    expect(screen.getByText("能力未验证")).toBeTruthy()
    expect(screen.getByText("允许 0 / 拒绝 1 / 错误 0")).toBeTruthy()
    expect(screen.getByText("即将过期")).toBeTruthy()
    expect(screen.getByText("worker-1")).toBeTruthy()
    expect(screen.getByText("waiting")).toBeTruthy()
    expect(screen.getByText("mfa")).toBeTruthy()
    expect(screen.getByText(/login_failed/)).toBeTruthy()
    expect(screen.getByText("QA 01")).toBeTruthy()
    expect(screen.getByText("Shop")).toBeTruthy()
  })

  it("shows the login_check observation captured in the snapshot items", () => {
    renderDetail(
      makeSession({
        latestSnapshot: makeSnapshot({
          items: { login: { kind: "page", observed: "allowed", status: 200 } },
          degraded: false,
          summary: { total: 1, allowed: 1, denied: 0, error: 0, degraded: false },
        }),
      }),
    )

    expect(screen.getByText("登录检查")).toBeTruthy()
    expect(screen.getByText("allowed")).toBeTruthy()
    expect(screen.getByText("200")).toBeTruthy()
  })

  it("hides every action from readonly users", () => {
    renderDetail(WAITING, false)

    for (const name of ["校验", "确认", "失效", "撤销", "发起接管", "完成接管", "取消接管"]) {
      expect(screen.queryByRole("button", { name })).toBeNull()
    }
    expect(screen.getByText("能力未验证")).toBeTruthy()
  })

  it("reports a state conflict from validate as an alert", async () => {
    const onChanged = renderDetail(makeSession())
    mocks.validateSession.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "session state changed",
        details: { reason: "session_state_conflict" },
      }),
    )

    fireEvent.click(screen.getByRole("button", { name: "校验" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("session_state_conflict")
    expect(alert.textContent).toContain("当前状态不允许")
    expect(onChanged).not.toHaveBeenCalled()
  })

  it("invalidates with the typed reason and reloads", async () => {
    const onChanged = renderDetail(makeSession())

    fireEvent.change(screen.getByLabelText("操作原因"), {
      target: { value: "账号异常" },
    })
    fireEvent.click(screen.getByRole("button", { name: "失效" }))

    await waitFor(() => expect(mocks.invalidateSession).toHaveBeenCalledWith(1, "账号异常"))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect((await screen.findByRole("status")).textContent).toContain("invalid")
  })

  it("revokes with a default reason when none is typed", async () => {
    renderDetail(makeSession())

    fireEvent.click(screen.getByRole("button", { name: "撤销" }))

    await waitFor(() => expect(mocks.revokeSession).toHaveBeenCalledWith(1, "管理员手动撤销"))
  })

  it("requests a takeover through the dialog", async () => {
    const onChanged = renderDetail(makeSession())
    mocks.requestTakeover.mockResolvedValue({
      takeover: { id: 9, status: "waiting" },
      session: makeSession({ state: "needs_manual" }),
    })

    fireEvent.click(screen.getByRole("button", { name: "发起接管" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText("接管原因"), {
      target: { value: "需要验证码" },
    })
    fireEvent.change(within(dialog).getByLabelText("操作指引"), {
      target: { value: "5 分钟内完成" },
    })
    fireEvent.change(within(dialog).getByLabelText("有效期（秒）"), { target: { value: "900" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "确认发起" }))

    await waitFor(() =>
      expect(mocks.requestTakeover).toHaveBeenCalledWith(1, {
        reason: "需要验证码",
        instructions: { text: "5 分钟内完成" },
        expiresInSeconds: 900,
      }),
    )
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it("completes a waiting takeover with a new storage state", async () => {
    const onChanged = renderDetail(WAITING)
    mocks.completeTakeover.mockResolvedValue({
      takeover: { id: 9, status: "completed" },
      session: makeSession({ state: "valid" }),
    })

    fireEvent.click(screen.getByRole("button", { name: "完成接管" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText("JSON 内容"), {
      target: { value: JSON.stringify({ cookies: [], origins: [] }) },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "上传并完成" }))

    await waitFor(() =>
      expect(mocks.completeTakeover).toHaveBeenCalledWith(9, {
        kind: "storage_state",
        storageState: { cookies: [], origins: [] },
      }),
    )
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it("cancels a waiting takeover", async () => {
    const onChanged = renderDetail(WAITING)

    fireEvent.click(screen.getByRole("button", { name: "取消接管" }))

    await waitFor(() => expect(mocks.cancelTakeover).toHaveBeenCalledWith(9))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect((await screen.findByRole("status")).textContent).toContain("取消")
  })
})

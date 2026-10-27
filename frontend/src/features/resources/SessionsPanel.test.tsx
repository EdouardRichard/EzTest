import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listSessions: vi.fn(),
  getSession: vi.fn(),
  listAccounts: vi.fn(),
  listSystems: vi.fn(),
  listEnvironments: vi.fn(),
  importSession: vi.fn(),
  validateSession: vi.fn(),
  confirmSession: vi.fn(),
  invalidateSession: vi.fn(),
  revokeSession: vi.fn(),
  requestTakeover: vi.fn(),
  completeTakeover: vi.fn(),
  cancelTakeover: vi.fn(),
}))

vi.mock("./api", () => ({
  listSessions: mocks.listSessions,
  getSession: mocks.getSession,
  listAccounts: mocks.listAccounts,
  listSystems: mocks.listSystems,
  listEnvironments: mocks.listEnvironments,
  importSession: mocks.importSession,
  validateSession: mocks.validateSession,
  confirmSession: mocks.confirmSession,
  invalidateSession: mocks.invalidateSession,
  revokeSession: mocks.revokeSession,
  requestTakeover: mocks.requestTakeover,
  completeTakeover: mocks.completeTakeover,
  cancelTakeover: mocks.cancelTakeover,
}))

import { SessionsPanel } from "./SessionsPanel"
import type { AccountDTO, EnvironmentDTO, Page, SessionDTO, SnapshotDTO, SystemDTO } from "./types"

const SYSTEM: SystemDTO = {
  id: 1,
  key: "shop",
  name: "Shop",
  kind: "web",
  baseUrl: "http://localhost:13335",
  defaultSessionTtlSeconds: 3600,
  notes: "",
  status: "active",
  createdAt: "2026-10-27T00:00:00Z",
  updatedAt: "2026-10-27T00:00:00Z",
}

const ACCOUNT: AccountDTO = {
  id: 3,
  key: "qa01",
  label: "QA 01",
  username: "qa01",
  kind: "human",
  status: "active",
  notes: "",
}

const ENVIRONMENT: EnvironmentDTO = {
  id: 5,
  systemId: 1,
  key: "local",
  name: "本地",
  baseUrl: "http://localhost:13335",
  kind: "staging",
  isProd: false,
  allowedHosts: [],
  loginCheck: null,
  allowAutoLogin: false,
  requireApproval: false,
}

function makeSnapshot(overrides: Partial<SnapshotDTO> = {}): SnapshotDTO {
  return {
    id: 7,
    sessionId: 1,
    kind: "login_check",
    items: {},
    summary: { total: 1, allowed: 1, denied: 0, error: 0, degraded: false },
    permissionHash: "permission-hash",
    degraded: false,
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
    latestSnapshot: makeSnapshot(),
    activeLease: null,
    latestTakeover: null,
    ...overrides,
  }
}

const SESSION = makeSession()

function page<T>(items: T[]) {
  return { items, nextCursor: null, hasMore: false }
}

describe("SessionsPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listSessions.mockResolvedValue(page([SESSION]))
    mocks.getSession.mockResolvedValue(SESSION)
    mocks.listAccounts.mockResolvedValue(page([ACCOUNT]))
    mocks.listSystems.mockResolvedValue(page([SYSTEM]))
    mocks.listEnvironments.mockResolvedValue([ENVIRONMENT])
    mocks.validateSession.mockResolvedValue(SESSION)
  })

  afterEach(cleanup)

  it("lists sessions with state badges and hides actions from readonly users", async () => {
    render(<SessionsPanel canWrite={false} initialSessionId={1} />)

    await screen.findAllByText("valid")
    const table = await screen.findByRole("table")
    expect(within(table).getByText("QA 01")).toBeTruthy()
    expect(within(table).getByText("Shop")).toBeTruthy()
    expect(within(table).getByText("本地")).toBeTruthy()
    expect(within(table).getByRole("button", { name: "查看会话 #1" })).toBeTruthy()
    expect(mocks.listSessions).toHaveBeenCalledWith({ limit: 100 })
    expect(mocks.getSession).toHaveBeenCalledWith(1)

    expect(screen.queryByRole("button", { name: "校验" })).toBeNull()
    expect(screen.queryByRole("button", { name: "确认" })).toBeNull()
    expect(screen.queryByRole("button", { name: "失效" })).toBeNull()
    expect(screen.queryByRole("button", { name: "撤销" })).toBeNull()
    expect(screen.queryByRole("button", { name: "发起接管" })).toBeNull()
    expect(screen.queryByRole("button", { name: "导入会话" })).toBeNull()
  })

  it("auto-selects the first session and shows admin actions in the detail", async () => {
    render(<SessionsPanel canWrite />)

    const detail = await screen.findByRole("region", { name: "会话详情" })
    expect(within(detail).getByText("会话 #1")).toBeTruthy()
    expect(within(detail).getByRole("button", { name: "校验" })).toBeTruthy()
    expect(within(detail).getByRole("button", { name: "发起接管" })).toBeTruthy()
    expect(mocks.getSession).toHaveBeenCalledWith(1)
    expect(screen.getByRole("button", { name: "导入会话" })).toBeTruthy()

    // `valid` allows validate/revoke but not confirm/invalidate.
    expect(
      (within(detail).getByRole("button", { name: "确认" }) as HTMLButtonElement).disabled,
    ).toBe(true)
  })

  it("validates the selected session and refetches list and detail", async () => {
    mocks.validateSession.mockResolvedValue(
      makeSession({ state: "needs_manual", failureReason: "login_failed" }),
    )

    render(<SessionsPanel canWrite initialSessionId={1} />)
    const detail = await screen.findByRole("region", { name: "会话详情" })

    fireEvent.click(within(detail).getByRole("button", { name: "校验" }))

    await waitFor(() => expect(mocks.validateSession).toHaveBeenCalledWith(1))
    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("needs_manual")
    await waitFor(() => expect(mocks.listSessions).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(mocks.getSession).toHaveBeenCalledTimes(2))
  })

  it("passes account, system and state filters with the maximum page size", async () => {
    render(<SessionsPanel canWrite={false} />)
    await screen.findByRole("table")

    fireEvent.change(screen.getByLabelText("按状态筛选"), {
      target: { value: "needs_manual" },
    })
    await waitFor(() =>
      expect(mocks.listSessions).toHaveBeenLastCalledWith({ limit: 100, state: "needs_manual" }),
    )

    fireEvent.change(screen.getByLabelText("按账号筛选"), { target: { value: "3" } })
    await waitFor(() =>
      expect(mocks.listSessions).toHaveBeenLastCalledWith({
        limit: 100,
        state: "needs_manual",
        accountId: 3,
      }),
    )

    fireEvent.change(screen.getByLabelText("按系统筛选"), { target: { value: "1" } })
    await waitFor(() =>
      expect(mocks.listSessions).toHaveBeenLastCalledWith({
        limit: 100,
        state: "needs_manual",
        accountId: 3,
        systemId: 1,
      }),
    )
  })

  it("ignores a late session list response after the filters change", async () => {
    const stale = makeSession({ id: 3, state: "needs_manual" })
    const fresh = makeSession({ id: 4, state: "invalid" })
    mocks.listSessions.mockResolvedValueOnce(page([]))
    mocks.getSession.mockImplementation((id: number) =>
      Promise.resolve(id === 3 ? stale : id === 4 ? fresh : SESSION),
    )
    const pending: Array<(value: Page<SessionDTO>) => void> = []

    render(<SessionsPanel canWrite={false} />)
    await screen.findByText("暂无会话")

    // Hold both filter loads open so the older response can land last.
    mocks.listSessions.mockImplementation(
      () => new Promise<Page<SessionDTO>>((resolve) => pending.push(resolve)),
    )
    fireEvent.change(screen.getByLabelText("按状态筛选"), {
      target: { value: "needs_manual" },
    })
    await waitFor(() => expect(pending).toHaveLength(1))
    fireEvent.change(screen.getByLabelText("按状态筛选"), { target: { value: "invalid" } })
    await waitFor(() => expect(pending).toHaveLength(2))

    await act(async () => {
      pending[1](page([fresh]))
    })
    const table = await screen.findByRole("table")
    expect(within(table).getByText("#4")).toBeTruthy()

    await act(async () => {
      pending[0](page([stale]))
    })

    expect(within(screen.getByRole("table")).getByText("#4")).toBeTruthy()
    expect(within(screen.getByRole("table")).queryByText("#3")).toBeNull()
  })

  it("selects a row, records the deep link and renders the fetched detail", async () => {
    const second = makeSession({ id: 2, state: "needs_manual" })
    mocks.listSessions.mockResolvedValue(page([SESSION, second]))
    mocks.getSession.mockImplementation((id: number) =>
      Promise.resolve(id === 2 ? second : SESSION),
    )
    const onSelectSession = vi.fn()

    render(<SessionsPanel canWrite={false} onSelectSession={onSelectSession} />)
    const table = await screen.findByRole("table")

    fireEvent.click(within(table).getByRole("button", { name: "查看会话 #2" }))

    await waitFor(() => expect(mocks.getSession).toHaveBeenLastCalledWith(2))
    expect(onSelectSession).toHaveBeenCalledWith(2)
    const detail = await screen.findByRole("region", { name: "会话详情" })
    expect(within(detail).getByText("会话 #2")).toBeTruthy()
  })

  it("opens the import dialog for admins and closes it again", async () => {
    render(<SessionsPanel canWrite />)
    await screen.findByRole("table")

    fireEvent.click(screen.getByRole("button", { name: "导入会话" }))
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText("导入会话")).toBeTruthy()
    await waitFor(() => expect(mocks.listAccounts).toHaveBeenCalledWith({ limit: 100 }))

    fireEvent.click(within(dialog).getByRole("button", { name: "关闭" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })
})

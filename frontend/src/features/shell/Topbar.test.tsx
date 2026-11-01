import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const nav = vi.hoisted(() => ({ replace: vi.fn(), pathname: "/issues" }))
vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
  useRouter: () => ({
    push: vi.fn(),
    replace: nav.replace,
    refresh: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
  }),
}))

const auth = vi.hoisted(() => ({
  user: {
    id: "9001",
    username: "admin",
    display_name: "管理员",
    role: "admin" as const,
    status: "active" as const,
  },
  token: "ezt_token",
  loading: false,
  login: vi.fn(),
  logout: vi.fn(),
  changePassword: vi.fn(),
}))
vi.mock("@/features/auth/AuthProvider", () => ({ useAuth: () => auth }))

type SSECall = {
  url: string
  token: string
  onEvent: (event: unknown) => void
  onStatus: (status: string) => void
  onReset?: (data: unknown) => void
}

const sse = vi.hoisted(() => ({ calls: [] as SSECall[], cleanup: vi.fn() }))
vi.mock("@/lib/sse-client", () => ({
  connectSSE: (options: SSECall) => {
    sse.calls.push(options)
    return sse.cleanup
  },
}))

const api = vi.hoisted(() => ({ apiGet: vi.fn() }))
vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return { ...actual, apiGet: api.apiGet }
})

const versionsApi = vi.hoisted(() => ({ getVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

const emptyTodoCounts = {
  requirement_confirmation: 0,
  case_confirmation: 0,
  issue_confirmation: 0,
  design_pending: 0,
  human_request: 0,
  dead_work_item: 0,
}

const versionFixture = {
  id: "2001",
  title: "订单中心 · 2026 Q4 迭代",
  status: "running",
  auto_design_new_requirements: false,
  bindings: [],
  pending_design_count: 0,
  created_by: "9001",
  created_at: "2026-10-31T10:00:00Z",
  updated_at: "2026-10-31T10:00:00Z",
}

import { Topbar } from "./Topbar"

describe("Topbar", () => {
  beforeEach(() => {
    sse.calls = []
    sse.cleanup.mockReset()
    auth.logout.mockReset()
    auth.logout.mockResolvedValue(undefined)
    auth.changePassword.mockReset()
    nav.replace.mockReset()
    nav.pathname = "/issues"
    api.apiGet.mockReset()
    api.apiGet.mockResolvedValue({ items: [], counts: emptyTodoCounts })
    versionsApi.getVersion.mockReset()
    versionsApi.getVersion.mockResolvedValue(versionFixture)
  })

  afterEach(cleanup)

  it("shows the current page title from the navigation map", () => {
    render(<Topbar />)

    expect(screen.getByTestId("topbar-title")).toHaveTextContent("问题")
  })

  it("connects the system SSE channel and reflects its status", () => {
    render(<Topbar />)

    expect(sse.calls).toHaveLength(1)
    expect(sse.calls[0].url).toBe("http://localhost:13333/api/stream?channel=system")
    expect(sse.calls[0].token).toBe("ezt_token")

    expect(screen.getByTestId("sse-status")).toHaveAttribute("data-state", "closed")
    act(() => sse.calls[0].onStatus("connecting"))
    expect(screen.getByTestId("sse-status")).toHaveAttribute("data-state", "connecting")
    act(() => sse.calls[0].onStatus("live"))
    expect(screen.getByTestId("sse-status")).toHaveAttribute("data-state", "live")
    expect(screen.getByText("实时")).toBeInTheDocument()
    act(() => sse.calls[0].onStatus("reconnecting"))
    expect(screen.getByText("重连中")).toBeInTheDocument()
  })

  it("opens the user menu with change-password and logout", async () => {
    render(<Topbar />)

    fireEvent.click(screen.getByRole("button", { name: "打开用户菜单" }))

    expect(await screen.findByRole("menuitem", { name: "修改密码" })).toBeInTheDocument()
    expect(screen.getByRole("menuitem", { name: "退出登录" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "打开用户菜单" })).toHaveTextContent("管理员")
  })

  it("opens the change-password dialog from the user menu", async () => {
    render(<Topbar />)
    fireEvent.click(screen.getByRole("button", { name: "打开用户菜单" }))
    fireEvent.click(await screen.findByRole("menuitem", { name: "修改密码" }))

    expect(await screen.findByRole("dialog", { name: "修改密码" })).toBeInTheDocument()
    expect(screen.getByLabelText("旧密码")).toBeInTheDocument()
  })

  it("logs out through the auth provider and returns to /login", async () => {
    render(<Topbar />)
    fireEvent.click(screen.getByRole("button", { name: "打开用户菜单" }))
    fireEvent.click(await screen.findByRole("menuitem", { name: "退出登录" }))

    await waitFor(() => expect(auth.logout).toHaveBeenCalledTimes(1))
    expect(nav.replace).toHaveBeenCalledWith("/login")
  })

  it("shows the global todo entry and opens the drawer", async () => {
    nav.pathname = "/versions/2001"
    api.apiGet.mockResolvedValue({
      items: [],
      counts: { ...emptyTodoCounts, human_request: 1 },
    })

    render(<Topbar />)

    const trigger = await screen.findByTestId("todo-drawer-trigger")
    await waitFor(() => expect(trigger).toHaveTextContent("1"))
    expect(api.apiGet).toHaveBeenCalledWith("/api/todos?limit=50")

    fireEvent.click(trigger)
    expect(await screen.findByRole("dialog", { name: "待办" })).toBeInTheDocument()
  })
})

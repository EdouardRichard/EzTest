import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { type AuthUser, clearSession, setSession } from "@/lib/auth-storage"

const nav = vi.hoisted(() => ({ pathname: "/issues" }))
vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
}))

const client = vi.hoisted(() => ({ apiGet: vi.fn() }))
vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return { ...actual, apiGet: client.apiGet }
})

import { ApiError } from "@/lib/api-client"

import { TodoDrawer, TodoDrawerButton } from "./TodoDrawer"

const ADMIN: AuthUser = {
  id: "9001",
  username: "admin",
  display_name: "管理员",
  role: "admin",
  status: "active",
}

const REGULAR: AuthUser = {
  id: "9002",
  username: "qa01",
  display_name: "QA 01",
  role: "user",
  status: "active",
}

const counts = {
  requirement_confirmation: 7,
  case_confirmation: 2,
  issue_confirmation: 3,
  design_pending: 4,
  human_request: 5,
  dead_work_item: 6,
}

const items = [
  {
    kind: "human_request",
    title: "登录接管请求",
    version_id: "2001",
    version_title: "订单中心",
    severity: "warning",
    created_at: "2026-11-01T07:00:00Z",
    expires_at: "2026-11-01T08:00:00Z",
    url: "/versions/2001?tab=execution",
  },
  {
    kind: "design_pending",
    title: "4 条待设计",
    version_id: "2001",
    version_title: "订单中心",
    severity: "warning",
    created_at: "2026-11-01T07:00:00Z",
    expires_at: null,
    url: "/versions/2001?tab=design",
  },
  {
    kind: "issue_confirmation",
    title: "支付回调偶发超时",
    version_id: "2001",
    version_title: "订单中心",
    severity: "major",
    created_at: "2026-11-01T07:00:00Z",
    expires_at: null,
    url: "/versions/2001?tab=issues&issue=5001",
  },
  {
    kind: "requirement_confirmation",
    title: "支持部分退款",
    version_id: "2001",
    version_title: "订单中心",
    severity: "warning",
    created_at: "2026-11-01T07:00:00Z",
    expires_at: null,
    url: "/versions/2001?tab=requirements&requirement=4001",
  },
  {
    kind: "case_confirmation",
    title: "退款 T+1 到账",
    version_id: "2001",
    version_title: "订单中心",
    severity: "warning",
    created_at: "2026-11-01T07:00:00Z",
    expires_at: null,
    url: "/versions/2001?tab=design&case=4101",
  },
  {
    kind: "dead_work_item",
    title: "执行死任务",
    version_id: "2001",
    version_title: "订单中心",
    severity: "major",
    created_at: "2026-11-01T07:00:00Z",
    expires_at: null,
    url: "/versions/2001?tab=execution",
  },
]

const snapshot = { items, counts }

const groupOrder = [
  "todo-group-human_request",
  "todo-group-design_pending",
  "todo-group-issue_confirmation",
  "todo-group-requirement_confirmation",
  "todo-group-case_confirmation",
  "todo-group-dead_work_item",
]

describe("TodoDrawer", () => {
  beforeEach(() => {
    client.apiGet.mockReset()
    client.apiGet.mockResolvedValue(snapshot)
    nav.pathname = "/issues"
    setSession("ezt_test_token", ADMIN)
  })

  afterEach(() => {
    cleanup()
    clearSession()
    vi.useRealTimers()
  })

  it("renders the six global groups in contract order with pre-limit counts and deep links", async () => {
    const onClose = vi.fn()
    render(<TodoDrawer open onClose={onClose} />)

    const drawer = await screen.findByRole("dialog", { name: "待办" })
    expect(client.apiGet).toHaveBeenCalledWith("/api/todos?limit=50")

    const headers = drawer.querySelectorAll<HTMLElement>('[data-testid^="todo-group-"]')
    expect([...headers].map((header) => header.getAttribute("data-testid"))).toEqual(groupOrder)

    expect(within(drawer).getByTestId("todo-count-human_request")).toHaveTextContent("5")
    expect(within(drawer).getByTestId("todo-count-design_pending")).toHaveTextContent("4")
    expect(within(drawer).getByTestId("todo-count-issue_confirmation")).toHaveTextContent("3")
    expect(within(drawer).getByTestId("todo-count-requirement_confirmation")).toHaveTextContent("7")
    expect(within(drawer).getByTestId("todo-count-case_confirmation")).toHaveTextContent("2")
    expect(within(drawer).getByTestId("todo-count-dead_work_item")).toHaveTextContent("6")

    // Counts are pre-limit: seven requirements are reported but only one item
    // is present in this snapshot.
    expect(within(drawer).getAllByTestId(/^todo-item-requirement_confirmation-/)).toHaveLength(1)

    const requirement = within(drawer).getByTestId("todo-item-requirement_confirmation-0")
    expect(requirement).toHaveTextContent("支持部分退款")
    expect(requirement).toHaveTextContent("订单中心")
    expect(within(requirement).getByRole("link", { name: "去处理" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=requirements&requirement=4001",
    )
    const issue = within(drawer).getByTestId("todo-item-issue_confirmation-0")
    expect(within(issue).getByText("严重")).toBeInTheDocument()
    const human = within(drawer).getByTestId("todo-item-human_request-0")
    expect(within(human).getByText(/过期时间/)).toBeInTheDocument()

    // Read-only: the drawer never offers resolve/cancel actions.
    expect(within(drawer).queryByRole("button", { name: "完成" })).not.toBeInTheDocument()
    expect(within(drawer).queryByRole("button", { name: "取消请求" })).not.toBeInTheDocument()

    fireEvent.click(within(drawer).getByRole("button", { name: "关闭待办" }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it("opens the global entry and endpoint without a version context", async () => {
    render(<TodoDrawerButton versionId={null} />)

    const trigger = await screen.findByTestId("todo-drawer-trigger")
    expect(trigger).toHaveTextContent("27")
    fireEvent.click(trigger)

    const drawer = await screen.findByRole("dialog", { name: "待办" })
    expect(within(drawer).getByTestId("todo-group-human_request")).toBeInTheDocument()
    expect(client.apiGet).toHaveBeenCalledTimes(1)
    expect(client.apiGet).toHaveBeenCalledWith("/api/todos?limit=50")
  })

  it("collapses and expands a group", async () => {
    render(<TodoDrawer open onClose={vi.fn()} />)

    const drawer = await screen.findByRole("dialog", { name: "待办" })
    const header = within(drawer).getByTestId("todo-group-human_request")
    expect(header).toHaveAttribute("aria-expanded", "true")
    expect(within(drawer).getByTestId("todo-item-human_request-0")).toBeVisible()

    fireEvent.click(header)
    expect(header).toHaveAttribute("aria-expanded", "false")
    expect(within(drawer).queryByTestId("todo-item-human_request-0")).not.toBeInTheDocument()

    fireEvent.click(header)
    expect(header).toHaveAttribute("aria-expanded", "true")
    expect(within(drawer).getByTestId("todo-item-human_request-0")).toBeVisible()
  })

  it("hides the admin-only dead work item group from regular users", async () => {
    setSession("ezt_test_token", REGULAR)
    render(<TodoDrawer open onClose={vi.fn()} />)

    const drawer = await screen.findByRole("dialog", { name: "待办" })
    expect(within(drawer).getByTestId("todo-group-human_request")).toBeInTheDocument()
    expect(within(drawer).queryByTestId("todo-group-dead_work_item")).not.toBeInTheDocument()
    expect(within(drawer).queryByTestId("todo-count-dead_work_item")).not.toBeInTheDocument()
  })

  it("shows the empty state when every count is zero", async () => {
    client.apiGet.mockResolvedValue({
      items: [],
      counts: {
        requirement_confirmation: 0,
        case_confirmation: 0,
        issue_confirmation: 0,
        design_pending: 0,
        human_request: 0,
        dead_work_item: 0,
      },
    })

    render(<TodoDrawer open onClose={vi.fn()} />)

    expect(await screen.findByText("暂无待办")).toBeInTheDocument()
    expect(screen.queryAllByTestId(/^todo-group-/)).toHaveLength(0)
  })

  it("shows an error and retries the global request", async () => {
    client.apiGet.mockRejectedValueOnce(
      new ApiError({
        status: 503,
        code: "unavailable",
        message: "待办服务暂不可用",
        retryable: true,
      }),
    )

    render(<TodoDrawer open onClose={vi.fn()} />)

    expect(await screen.findByText("待办加载失败")).toBeInTheDocument()
    expect(screen.getByText("待办服务暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByTestId("todo-group-human_request")).toBeInTheDocument()
    expect(client.apiGet).toHaveBeenCalledTimes(2)
  })

  it("polls the global snapshot every 30 seconds while open", async () => {
    vi.useFakeTimers()
    render(<TodoDrawer open onClose={vi.fn()} />)

    await vi.waitFor(() => expect(client.apiGet).toHaveBeenCalledTimes(1))
    await vi.advanceTimersByTimeAsync(30_000)
    expect(client.apiGet).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(client.apiGet).toHaveBeenCalledTimes(3)
  })

  it("refreshes when the route changes", async () => {
    const view = render(<TodoDrawer open onClose={vi.fn()} />)

    await screen.findByTestId("todo-group-human_request")
    expect(client.apiGet).toHaveBeenCalledTimes(1)

    nav.pathname = "/versions/2001"
    view.rerender(<TodoDrawer open onClose={vi.fn()} />)

    await waitFor(() => expect(client.apiGet).toHaveBeenCalledTimes(2))
  })

  it("does not request while closed", () => {
    render(<TodoDrawer open={false} onClose={vi.fn()} />)

    expect(screen.queryByRole("dialog", { name: "待办" })).not.toBeInTheDocument()
    expect(client.apiGet).not.toHaveBeenCalled()
  })
})

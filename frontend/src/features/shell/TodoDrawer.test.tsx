import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { HumanRequest } from "@/features/execution/types"
import { versionFixture } from "@/features/versions/test-fixtures"

const executionApi = vi.hoisted(() => ({ listHumanRequests: vi.fn() }))
vi.mock("@/features/execution/api", () => executionApi)

const versionsApi = vi.hoisted(() => ({ getVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

import { TodoDrawer, TodoDrawerButton } from "./TodoDrawer"

const humanRequest: HumanRequest = {
  id: "9501",
  version_id: "2001",
  case_run_id: "9301",
  kind: "login_takeover",
  status: "open",
  payload: {
    reason: "需要输入短信验证码",
    version_sut_id: "3001",
    account_role: "buyer",
    page_url: "https://order.test/login",
    challenge_kind: "sms",
  },
  expires_at: "2026-11-01T08:00:00Z",
  created_at: "2026-11-01T07:00:00Z",
}

const versionWithTodos = {
  ...versionFixture,
  pending_design_count: 2,
}

const versionWithoutTodos = {
  ...versionFixture,
  pending_design_count: 0,
}

describe("TodoDrawer", () => {
  beforeEach(() => {
    executionApi.listHumanRequests.mockReset()
    versionsApi.getVersion.mockReset()
    executionApi.listHumanRequests.mockResolvedValue([humanRequest])
    versionsApi.getVersion.mockResolvedValue(versionWithTodos)
  })

  afterEach(cleanup)

  it("lists open human requests and the pending-design CTA read-only", async () => {
    const onClose = vi.fn()
    render(<TodoDrawer versionId="2001" open onClose={onClose} />)

    const drawer = await screen.findByRole("dialog", { name: "待办" })
    expect(executionApi.listHumanRequests).toHaveBeenCalledWith("2001", { status: "open" })
    expect(versionsApi.getVersion).toHaveBeenCalledWith("2001")

    const request = await within(drawer).findByTestId("todo-human-request-9501")
    expect(request).toHaveTextContent("登录接管")
    expect(request).toHaveTextContent("需要输入短信验证码")
    expect(within(request).getByRole("link", { name: "去处理" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=execution&case_run=9301",
    )

    const pending = within(drawer).getByTestId("pending-design-cta")
    expect(pending).toHaveTextContent("2 条待设计")
    expect(within(pending).getByRole("link", { name: "前往设计" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=design",
    )

    // Read-only: the drawer never resolves or cancels takeover requests itself.
    expect(within(drawer).queryByRole("button", { name: "完成" })).not.toBeInTheDocument()
    expect(within(drawer).queryByRole("button", { name: "取消请求" })).not.toBeInTheDocument()

    fireEvent.click(within(drawer).getByRole("button", { name: "关闭待办" }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it("shows an empty state when there are no open requests and no pending design", async () => {
    executionApi.listHumanRequests.mockResolvedValue([])
    versionsApi.getVersion.mockResolvedValue(versionWithoutTodos)

    render(<TodoDrawer versionId="2001" open onClose={vi.fn()} />)

    expect(await screen.findByText("暂无待办")).toBeInTheDocument()
    expect(screen.getByText("暂无待处理的人工请求。")).toBeInTheDocument()
  })

  it("shows the no-version empty state and does not call the APIs", async () => {
    render(<TodoDrawer versionId={null} open onClose={vi.fn()} />)

    expect(await screen.findByText("未选择版本")).toBeInTheDocument()
    expect(executionApi.listHumanRequests).not.toHaveBeenCalled()
    expect(versionsApi.getVersion).not.toHaveBeenCalled()
  })

  it("drops resolved requests after a refresh", async () => {
    executionApi.listHumanRequests.mockResolvedValueOnce([humanRequest]).mockResolvedValueOnce([])
    versionsApi.getVersion.mockResolvedValue(versionWithoutTodos)

    render(<TodoDrawer versionId="2001" open onClose={vi.fn()} />)

    expect(await screen.findByTestId("todo-human-request-9501")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "刷新待办" }))

    await waitFor(() => {
      expect(screen.queryByTestId("todo-human-request-9501")).not.toBeInTheDocument()
    })
    expect(await screen.findByText("暂无待处理的人工请求。")).toBeInTheDocument()
  })

  it("shows the open count on the topbar trigger and opens the drawer", async () => {
    render(<TodoDrawerButton versionId="2001" />)

    const trigger = await screen.findByTestId("todo-drawer-trigger")
    await waitFor(() => expect(trigger).toHaveTextContent("1"))
    fireEvent.click(trigger)

    expect(await screen.findByRole("dialog", { name: "待办" })).toBeInTheDocument()
  })

  it("hides the topbar trigger when there is no current version", () => {
    render(<TodoDrawerButton versionId={null} />)

    expect(screen.queryByTestId("todo-drawer-trigger")).not.toBeInTheDocument()
    expect(executionApi.listHumanRequests).not.toHaveBeenCalled()
  })
})

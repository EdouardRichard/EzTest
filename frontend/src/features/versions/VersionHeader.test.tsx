import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { HumanRequest } from "@/features/execution/types"
import { versionFixture } from "@/features/versions/test-fixtures"
import { ApiError } from "@/lib/api-client"

import type { Version } from "./types"

const api = vi.hoisted(() => ({ reopenVersion: vi.fn() }))
vi.mock("./api", () => api)

const executionApi = vi.hoisted(() => ({ listHumanRequests: vi.fn() }))
vi.mock("@/features/execution/api", () => executionApi)

import { VersionHeader } from "./VersionHeader"

const openRequest: HumanRequest = {
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

const runningVersion: Version = {
  ...versionFixture,
  pending_design_count: 2,
}

const completedVersion: Version = {
  ...versionFixture,
  status: "completed",
  closed_at: "2026-11-01T03:00:00Z",
  close_reason: "需求全部闭环，报告已发布",
}

const reopenedVersion: Version = {
  ...completedVersion,
  status: "running",
  closed_at: null,
  close_reason: null,
}

describe("VersionHeader", () => {
  beforeEach(() => {
    api.reopenVersion.mockReset()
    executionApi.listHumanRequests.mockReset()
    executionApi.listHumanRequests.mockResolvedValue([openRequest])
  })

  afterEach(cleanup)

  it("renders the status badge, SUT chips, chain link, todo count and pending-design entry", async () => {
    render(<VersionHeader version={runningVersion} onOpenTodo={vi.fn()} onReopened={vi.fn()} />)

    expect(screen.getByRole("heading", { name: runningVersion.title })).toBeInTheDocument()
    expect(screen.getByText("运行中")).toBeInTheDocument()
    expect(screen.getByText("订单中心")).toBeInTheDocument()
    expect(screen.getByText("支付网关")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "查看探索链路" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=chain",
    )

    const todo = screen.getByTestId("version-todo-entry")
    await waitFor(() => expect(todo).toHaveTextContent("1"))
    expect(executionApi.listHumanRequests).toHaveBeenCalledWith("2001", { status: "open" })

    const pending = screen.getByTestId("version-pending-design")
    expect(pending).toHaveTextContent("2 条待设计")
    expect(pending).toHaveAttribute("href", "/versions/2001?tab=design")
    expect(screen.queryByRole("button", { name: "重新打开" })).not.toBeInTheDocument()
  })

  it("opens the todo drawer through the header entry", async () => {
    const onOpenTodo = vi.fn()
    render(<VersionHeader version={runningVersion} onOpenTodo={onOpenTodo} />)

    fireEvent.click(screen.getByTestId("version-todo-entry"))

    expect(onOpenTodo).toHaveBeenCalledTimes(1)
  })

  it("shows initial status without closed information", async () => {
    render(<VersionHeader version={{ ...versionFixture, status: "initial" }} />)

    expect(screen.getByText("初始")).toBeInTheDocument()
    expect(screen.queryByTestId("version-closed-info")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "重新打开" })).not.toBeInTheDocument()
  })

  it("shows closed metadata and reopens a completed version with a required reason", async () => {
    api.reopenVersion.mockResolvedValue(reopenedVersion)
    const onReopened = vi.fn()

    render(<VersionHeader version={completedVersion} onReopened={onReopened} />)

    expect(screen.getByText("已完成")).toBeInTheDocument()
    const closed = screen.getByTestId("version-closed-info")
    expect(closed).toHaveTextContent("2026-11-01")
    expect(closed).toHaveTextContent(completedVersion.close_reason ?? "")

    fireEvent.click(screen.getByRole("button", { name: "重新打开" }))
    const dialog = await screen.findByRole("dialog", { name: "重新打开版本" })
    const submit = within(dialog).getByRole("button", { name: "重新打开" })
    expect(submit).toBeDisabled()

    fireEvent.change(within(dialog).getByLabelText("重开原因"), {
      target: { value: "需要补充一个用例" },
    })
    expect(submit).not.toBeDisabled()
    fireEvent.click(submit)

    await waitFor(() =>
      expect(api.reopenVersion).toHaveBeenCalledWith("2001", { reason: "需要补充一个用例" }),
    )
    await waitFor(() => expect(onReopened).toHaveBeenCalledWith(reopenedVersion))
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "重新打开版本" })).not.toBeInTheDocument()
    })
  })

  it("keeps the reopen dialog open and shows the API error", async () => {
    api.reopenVersion.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "版本状态已变化",
        requestId: "req_reopen",
      }),
    )

    render(<VersionHeader version={completedVersion} />)
    fireEvent.click(screen.getByRole("button", { name: "重新打开" }))
    const dialog = await screen.findByRole("dialog", { name: "重新打开版本" })
    fireEvent.change(within(dialog).getByLabelText("重开原因"), {
      target: { value: "再次打开" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "重新打开" }))

    expect(await within(dialog).findByText("版本状态已变化")).toBeInTheDocument()
    expect(screen.getByRole("dialog", { name: "重新打开版本" })).toBeInTheDocument()
  })
})

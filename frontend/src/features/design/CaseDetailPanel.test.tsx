import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  getDesignCase: vi.fn(),
}))

vi.mock("./api", () => api)

import { CaseDetailPanel } from "./CaseDetailPanel"
import { designCaseDetailFixture } from "./test-fixtures"

function renderPanel(overrides: Partial<Parameters<typeof CaseDetailPanel>[0]> = {}) {
  const props = {
    versionId: "2001",
    caseId: "7001",
    onConfirm: vi.fn(),
    onRequestChange: vi.fn(),
    ...overrides,
  }
  render(<CaseDetailPanel {...props} />)
  return props
}

describe("CaseDetailPanel", () => {
  beforeEach(() => {
    api.getDesignCase.mockReset()
    api.getDesignCase.mockResolvedValue(designCaseDetailFixture)
  })

  afterEach(cleanup)

  it("shows a hint when no case is selected", () => {
    renderPanel({ caseId: null })

    expect(screen.getByTestId("case-detail")).toHaveTextContent("选择用例查看详情")
    expect(api.getDesignCase).not.toHaveBeenCalled()
    expect(screen.queryByText("查看执行结果")).not.toBeInTheDocument()
  })

  it("renders metadata, steps, assertions, sources and version history", async () => {
    renderPanel()

    expect(await screen.findByText("退款 T+1 到账")).toBeInTheDocument()
    expect(screen.getByText(/审核通过后 T\+1 个工作日原路退回/)).toBeInTheDocument()
    expect(screen.getByText("订单已支付")).toBeInTheDocument()
    expect(screen.getByText("P1")).toBeInTheDocument()

    expect(screen.getByText("打开退款详情页")).toBeInTheDocument()
    expect(screen.getByText("显示退款按钮")).toBeInTheDocument()
    expect(screen.getByText("点击确认退款")).toBeInTheDocument()
    expect(screen.getByText(/SUT 1002/)).toBeInTheDocument()
    expect(screen.getByText(/页面 2301/)).toBeInTheDocument()

    expect(screen.getByText(/UI · 退款按钮/)).toBeInTheDocument()
    expect(screen.getByText(/可见可点击/)).toBeInTheDocument()
    expect(screen.getByText(/requirement · 6101/)).toBeInTheDocument()

    expect(screen.getByText(/v2 · draft/)).toBeInTheDocument()
    expect(screen.getByText(/v1 · stale/)).toBeInTheDocument()
    expect(screen.queryByText("查看执行结果")).not.toBeInTheDocument()
    expect(api.getDesignCase).toHaveBeenCalledWith("2001", "7001", undefined)
  })

  it("confirms a draft case and requests a change without sending", async () => {
    const props = renderPanel()

    const confirm = await screen.findByTestId("btn-confirm-detail-7001")
    expect(confirm).toBeEnabled()
    fireEvent.click(confirm)
    expect(props.onConfirm).toHaveBeenCalledWith("7001")

    fireEvent.click(screen.getByRole("button", { name: "请求修改" }))
    expect(props.onRequestChange).toHaveBeenCalledWith("请修改：退款 T+1 到账")
  })

  it("loads a historical version when the history entry is clicked", async () => {
    renderPanel()

    await screen.findByText("退款 T+1 到账")
    fireEvent.click(screen.getByRole("button", { name: "查看版本 v1" }))

    expect(api.getDesignCase).toHaveBeenLastCalledWith("2001", "7001", "7000")
  })

  it("hides the confirm action for a non-draft selected version", async () => {
    api.getDesignCase.mockResolvedValue({
      ...designCaseDetailFixture,
      status: "confirmed",
      confirmable: false,
      selected: { ...designCaseDetailFixture.selected, status: "confirmed" },
      versions: designCaseDetailFixture.versions.map((version) =>
        version.case_version_id === "7002" ? { ...version, status: "confirmed" } : version,
      ),
    })
    renderPanel()

    await screen.findByText("退款 T+1 到账")
    expect(screen.getByText(/v2 · confirmed/)).toBeInTheDocument()
    expect(screen.queryByTestId("btn-confirm-detail-7001")).not.toBeInTheDocument()
  })

  it("shows a retryable error when the case cannot be loaded", async () => {
    api.getDesignCase
      .mockRejectedValueOnce(
        new ApiError({
          status: 404,
          code: "not_found",
          message: "用例不存在",
          requestId: "req_case",
        }),
      )
      .mockResolvedValueOnce(designCaseDetailFixture)

    renderPanel()

    expect(await screen.findByText("用例不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("退款 T+1 到账")).toBeInTheDocument()
    expect(api.getDesignCase).toHaveBeenCalledTimes(2)
  })
})

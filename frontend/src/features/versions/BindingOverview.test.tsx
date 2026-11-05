import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { BindingOverview } from "./BindingOverview"
import { bindingOverviewFixture } from "./test-fixtures"
import type { VersionBindingOverview } from "./types"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

function respondWith(overview: VersionBindingOverview): void {
  fetchMock.mockResolvedValue(jsonResponse({ data: overview }))
  vi.stubGlobal("fetch", fetchMock)
}

describe("BindingOverview", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("renders the derived SUT, repository roles/overrides, enabled accounts and scope exclusion chips", async () => {
    respondWith(bindingOverviewFixture)
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText("SUT 绑定概览")).toBeInTheDocument()
    expect(screen.getByText("SUT：订单中心")).toBeInTheDocument()
    expect(screen.getByText(/环境：测试环境/)).toBeInTheDocument()
    expect(screen.getByText("order-center-web")).toBeInTheDocument()
    expect(screen.getByText("web")).toBeInTheDocument()
    expect(screen.getByText("版本覆盖")).toBeInTheDocument()
    expect(screen.getByText("已排除")).toBeInTheDocument()
    expect(screen.getByText("payment-infra")).toBeInTheDocument()

    expect(screen.getByText(/buyer/)).toBeInTheDocument()
    expect(screen.getByText("仅显示启用账号")).toBeInTheDocument()

    const scope = screen.getByRole("region", { name: "范围排除" })
    expect(scope).toHaveTextContent("订单详情")
    expect(scope).toHaveTextContent("order_db")

    expect(screen.getByRole("button", { name: "编辑绑定" })).toBeEnabled()
  })

  it("disables binding editing for a completed version and explains the stage gate", async () => {
    respondWith({ ...bindingOverviewFixture, stage: "completed", editable: false })
    render(<BindingOverview versionId="2001" />)

    const edit = await screen.findByRole("button", { name: "编辑绑定" })
    expect(edit).toBeDisabled()
    expect(screen.getByText(/已完成版本只读/)).toBeInTheDocument()
    expect(screen.getByText("已完成")).toBeInTheDocument()
  })

  it("shows a loading state while the binding read is pending", () => {
    fetchMock.mockImplementation(() => new Promise<Response>(() => undefined))
    vi.stubGlobal("fetch", fetchMock)
    render(<BindingOverview versionId="2001" />)
    expect(screen.getByText("正在加载绑定概览…")).toBeInTheDocument()
  })

  it("shows the error state and retries the binding read", async () => {
    fetchMock
      .mockResolvedValueOnce(
        jsonResponse({ error: { code: "unavailable", message: "绑定读取失败" } }, 503),
      )
      .mockResolvedValueOnce(jsonResponse({ data: bindingOverviewFixture }))
    vi.stubGlobal("fetch", fetchMock)

    render(<BindingOverview versionId="2001" />)
    expect(await screen.findByText("绑定概览加载失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("SUT 绑定概览")).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("renders explicit empty states for repositories, accounts and exclusions", async () => {
    respondWith({
      ...bindingOverviewFixture,
      scope_exclude: { pages: [], apis: [], databases: [] },
      counts: {
        pages: { approved: 2, proposed: 0, excluded: 0, included: 2 },
        apis: { approved: 1, proposed: 0, excluded: 0, included: 1 },
        databases: { approved: 1, proposed: 0, excluded: 0, included: 1 },
      },
      repositories: [],
      accounts: [],
    })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText("该 SUT 未关联代码仓")).toBeInTheDocument()
    expect(screen.getByText(/未关联测试账号/)).toBeInTheDocument()
    expect(screen.getByText(/未设置排除项/)).toBeInTheDocument()
  })
})

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
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

/** The one binding update request the inline editor is allowed to send. */
function updateRequest(): { url: string; body: unknown } {
  const call = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")
  if (call === undefined) throw new Error("expected one binding update request")
  return { url: String(call[0]), body: JSON.parse(String(call[1]?.body)) }
}

const unexcludedOverview: VersionBindingOverview = {
  ...bindingOverviewFixture,
  scope_exclude: { pages: [], apis: [], databases: [] },
  pages: bindingOverviewFixture.environment.pages,
  counts: {
    ...bindingOverviewFixture.counts,
    pages: { approved: 2, proposed: 0, excluded: 0, included: 2 },
    databases: { approved: 1, proposed: 0, excluded: 0, included: 1 },
  },
}

const excludedAfterSave: VersionBindingOverview = {
  ...unexcludedOverview,
  scope_exclude: { pages: ["1302"], apis: [], databases: [] },
  pages: bindingOverviewFixture.environment.pages.slice(0, 1),
  counts: bindingOverviewFixture.counts,
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

    const edit = screen.getByRole("button", { name: "编辑绑定" })
    expect(edit).toBeEnabled()
    expect(screen.queryByTestId("binding-edit-deferred")).not.toBeInTheDocument()
  })

  it("invokes the optional onEdit callback when inline editing starts", async () => {
    respondWith(bindingOverviewFixture)
    const onEdit = vi.fn()
    render(<BindingOverview versionId="2001" onEdit={onEdit} />)

    const edit = await screen.findByRole("button", { name: "编辑绑定" })
    expect(edit).toBeEnabled()
    // Inline editing is available without a host callback; when one is provided
    // it observes the edit start instead of replacing the inline editor.
    fireEvent.click(edit)
    expect(onEdit).toHaveBeenCalledTimes(1)
    expect(onEdit.mock.calls[0][0]).toMatchObject({ stage: bindingOverviewFixture.stage })
    expect(screen.getByRole("button", { name: "保存绑定" })).toBeInTheDocument()
  })

  it("opens an inline editor with repository, account and exclusion checkboxes", async () => {
    respondWith(bindingOverviewFixture)
    render(<BindingOverview versionId="2001" />)

    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))

    expect(screen.getByRole("checkbox", { name: "包含代码仓 order-center-web" })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: "包含代码仓 payment-infra" })).not.toBeChecked()
    expect(screen.getByRole("checkbox", { name: "启用账号 buyer" })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: "排除页面 订单详情" })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: "排除页面 订单列表" })).not.toBeChecked()
    expect(screen.getByRole("checkbox", { name: "排除目标库 order_db" })).toBeChecked()

    // Nothing changed yet, so saving is not offered.
    expect(screen.getByRole("button", { name: "保存绑定" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "取消编辑" })).toBeEnabled()
  })

  it("saves only the changed scope_exclude document", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: unexcludedOverview }))
      .mockResolvedValueOnce(jsonResponse({ data: excludedAfterSave }))
    vi.stubGlobal("fetch", fetchMock)
    const onUpdated = vi.fn()

    render(<BindingOverview versionId="2001" onUpdated={onUpdated} />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "排除页面 订单详情" }))
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1))
    expect(updateRequest()).toEqual({
      url: "http://localhost:13333/api/versions/2001/binding",
      body: { scope_exclude: { pages: ["1302"], apis: [], databases: [] } },
    })
    expect(onUpdated.mock.calls[0][0]).toMatchObject({
      scope_exclude: { pages: ["1302"], apis: [], databases: [] },
    })
    expect(screen.queryByRole("button", { name: "保存绑定" })).not.toBeInTheDocument()
    expect(
      within(screen.getByRole("region", { name: "范围排除" })).getByText("订单详情"),
    ).toBeInTheDocument()
  })

  it("sends repository_ids without a repository whose checkbox was cleared", async () => {
    const after: VersionBindingOverview = {
      ...bindingOverviewFixture,
      repositories: bindingOverviewFixture.repositories.map((row) =>
        row.repository.id === "1202" ? { ...row, included: false } : row,
      ),
    }
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: bindingOverviewFixture }))
      .mockResolvedValueOnce(jsonResponse({ data: after }))
    vi.stubGlobal("fetch", fetchMock)

    render(<BindingOverview versionId="2001" />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "包含代码仓 payment-gateway" }))
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() => expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull())
    expect(updateRequest().body).toEqual({ repository_ids: ["1201"] })
  })

  it("sends account_ids without an account whose enable checkbox was cleared", async () => {
    const twoAccounts: VersionBindingOverview = {
      ...bindingOverviewFixture,
      accounts: bindingOverviewFixture.environment.accounts,
    }
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: twoAccounts }))
      .mockResolvedValueOnce(
        jsonResponse({ data: { ...twoAccounts, accounts: twoAccounts.accounts.slice(0, 1) } }),
      )
    vi.stubGlobal("fetch", fetchMock)

    render(<BindingOverview versionId="2001" />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "启用账号 operator" }))
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() => expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull())
    expect(updateRequest().body).toEqual({ account_ids: ["1601"] })
  })

  it("restores the original selection and sends no request when editing is cancelled", async () => {
    respondWith(bindingOverviewFixture)
    render(<BindingOverview versionId="2001" />)

    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "包含代码仓 order-center-web" }))
    fireEvent.click(screen.getByRole("button", { name: "取消编辑" }))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(
      screen.queryByRole("checkbox", { name: "包含代码仓 order-center-web" }),
    ).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "编辑绑定" }))
    expect(screen.getByRole("checkbox", { name: "包含代码仓 order-center-web" })).toBeChecked()
  })

  it("keeps a read-only binding view without an edit button", async () => {
    respondWith({ ...bindingOverviewFixture, stage: "executing", editable: false })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText(/当前阶段为「执行中」/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑绑定" })).not.toBeInTheDocument()
  })

  it("shows the stage-gate notice and stays read-only when the save is rejected with 409", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: bindingOverviewFixture }))
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: {
              code: "state_conflict",
              message: "version binding is read-only at stage executing",
            },
          },
          409,
        ),
      )
    vi.stubGlobal("fetch", fetchMock)

    render(<BindingOverview versionId="2001" />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "排除页面 订单列表" }))
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    expect(await screen.findByText("当前阶段绑定只读")).toBeInTheDocument()
    expect(screen.getByText(/version binding is read-only at stage executing/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑绑定" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "保存绑定" })).not.toBeInTheDocument()
  })

  it("disables binding editing for a completed version and explains the stage gate", async () => {
    respondWith({ ...bindingOverviewFixture, stage: "completed", editable: false })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText(/已完成版本只读/)).toBeInTheDocument()
    expect(screen.getByText("已完成")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑绑定" })).not.toBeInTheDocument()
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

    const scope = screen.getByRole("region", { name: "范围排除" })
    expect(within(scope).getByText("页面")).toBeInTheDocument()
    expect(within(scope).getByText("API")).toBeInTheDocument()
    expect(within(scope).getByText("目标库")).toBeInTheDocument()
    expect(within(scope).getAllByText("未排除")).toHaveLength(3)
  })

  it("explains the read-only binding while the version is paused", async () => {
    respondWith({ ...bindingOverviewFixture, stage: "paused", editable: false })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText(/版本已暂停/)).toBeInTheDocument()
    expect(screen.getByText("已暂停")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑绑定" })).not.toBeInTheDocument()
  })

  it("explains the read-only binding while the version is blocked", async () => {
    respondWith({ ...bindingOverviewFixture, stage: "blocked", editable: false })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText(/版本已阻塞/)).toBeInTheDocument()
    expect(screen.getByText("已阻塞")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑绑定" })).not.toBeInTheDocument()
  })
})

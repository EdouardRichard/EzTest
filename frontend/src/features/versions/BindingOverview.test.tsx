import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { BindingOverview } from "./BindingOverview"
import { bindingOverviewFixture, catalogFixture } from "./test-fixtures"
import type {
  AccountResource,
  PageResource,
  UpdateBindingRequest,
  VersionBindingOverview,
} from "./types"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

type FetchRoute = () => Response

type FetchRoutes = {
  /** `GET /api/versions/2001/binding` */
  binding: FetchRoute
  /** `PUT /api/versions/2001/binding`; receives the raw request body. */
  update?: (body: string) => Response
  /** `GET /api/resources/catalog`; defaults to the shared catalog fixture. */
  catalog?: FetchRoute
}

/**
 * Routes the binding read/write and the account-pool catalog by URL. `GET
 * /binding` carries only the bound account subset, so the editor reads the
 * selectable pool from `/api/resources/catalog` (same source as the wizard).
 */
function routeFetch(routes: FetchRoutes): void {
  fetchMock.mockImplementation(async (input, init) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    if (url.endsWith("/api/resources/catalog")) {
      return (routes.catalog ?? (() => jsonResponse({ data: catalogFixture })))()
    }
    if (url.endsWith("/api/versions/2001/binding")) {
      if (method === "PUT") {
        return (routes.update ?? (() => routes.binding()))(String(init?.body ?? "{}"))
      }
      return routes.binding()
    }
    return jsonResponse(
      { error: { code: "not_found", message: `unexpected ${method} ${url}` } },
      404,
    )
  })
  vi.stubGlobal("fetch", fetchMock)
}

function putBodies(): UpdateBindingRequest[] {
  return fetchMock.mock.calls
    .filter(([, init]) => init?.method === "PUT")
    .map(([, init]) => JSON.parse(String(init?.body)) as UpdateBindingRequest)
}

/** The one binding update request the inline editor is allowed to send. */
function updateRequest(): { url: string; body: unknown } {
  const call = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")
  if (call === undefined) throw new Error("expected one binding update request")
  return { url: String(call[0]), body: JSON.parse(String(call[1]?.body)) }
}

function bindingReads(): number {
  return fetchMock.mock.calls.filter(
    ([input, init]) =>
      (init?.method ?? "GET").toUpperCase() === "GET" &&
      String(input).endsWith("/api/versions/2001/binding"),
  ).length
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

const buyer = bindingOverviewFixture.accounts[0] as AccountResource

/** A page that was excluded while approved and has since become proposed. */
const proposedPage: PageResource = {
  ...(bindingOverviewFixture.environment.pages[0] as PageResource),
  id: "1303",
  name: "订单草稿",
  status: "proposed",
}

const overviewWithUnavailableExclusion: VersionBindingOverview = {
  ...bindingOverviewFixture,
  environment: {
    ...bindingOverviewFixture.environment,
    pages: [...bindingOverviewFixture.environment.pages, proposedPage],
  },
  scope_exclude: { pages: ["1302", "1303"], apis: [], databases: [] },
  counts: {
    ...bindingOverviewFixture.counts,
    pages: { approved: 2, proposed: 1, excluded: 1, included: 1 },
  },
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
    routeFetch({ binding: () => jsonResponse({ data: bindingOverviewFixture }) })
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
    routeFetch({ binding: () => jsonResponse({ data: bindingOverviewFixture }) })
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

  it("opens an inline editor with repository, account-pool and exclusion checkboxes", async () => {
    routeFetch({ binding: () => jsonResponse({ data: bindingOverviewFixture }) })
    render(<BindingOverview versionId="2001" />)

    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))

    expect(screen.getByRole("checkbox", { name: "包含代码仓 order-center-web" })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: "包含代码仓 payment-infra" })).not.toBeChecked()
    // The pool comes from the catalog, so a currently unbound account is
    // selectable again even though `GET /binding` only returns the bound subset.
    expect(screen.getByText(/全局测试账号池/)).toBeInTheDocument()
    expect(screen.getByRole("checkbox", { name: "启用账号 buyer" })).toBeChecked()
    expect(await screen.findByRole("checkbox", { name: "启用账号 operator" })).not.toBeChecked()
    expect(screen.getByRole("checkbox", { name: "排除页面 订单详情" })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: "排除页面 订单列表" })).not.toBeChecked()
    expect(screen.getByRole("checkbox", { name: "排除目标库 order_db" })).toBeChecked()

    // Nothing changed yet, so saving is not offered.
    expect(screen.getByRole("button", { name: "保存绑定" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "取消编辑" })).toBeEnabled()
  })

  it("saves only the changed scope_exclude document", async () => {
    routeFetch({
      binding: () => jsonResponse({ data: unexcludedOverview }),
      update: () => jsonResponse({ data: excludedAfterSave }),
    })
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
    routeFetch({
      binding: () => jsonResponse({ data: bindingOverviewFixture }),
      update: () => jsonResponse({ data: after }),
    })

    render(<BindingOverview versionId="2001" />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "包含代码仓 payment-gateway" }))
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() => expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull())
    expect(updateRequest().body).toEqual({ repository_ids: ["1201"] })
  })

  it("sends account_ids without a bound account whose checkbox was cleared", async () => {
    routeFetch({
      binding: () => jsonResponse({ data: bindingOverviewFixture }),
      update: () => jsonResponse({ data: { ...bindingOverviewFixture, accounts: [] } }),
    })

    render(<BindingOverview versionId="2001" />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    // operator is in the pool but not bound, so it is selectable (not checked).
    expect(await screen.findByRole("checkbox", { name: "启用账号 operator" })).not.toBeChecked()
    fireEvent.click(screen.getByRole("checkbox", { name: "启用账号 buyer" }))
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() => expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull())
    expect(updateRequest().body).toEqual({ account_ids: [] })
  })

  it("restores a removed account from the pool and sends it again", async () => {
    let bound: AccountResource[] = [buyer]
    routeFetch({
      binding: () => jsonResponse({ data: { ...bindingOverviewFixture, accounts: bound } }),
      update: (body) => {
        const payload = JSON.parse(body) as UpdateBindingRequest
        const ids = payload.account_ids ?? bound.map((account) => account.id)
        bound = [buyer].filter((account) => ids.includes(account.id))
        return jsonResponse({ data: { ...bindingOverviewFixture, accounts: bound } })
      },
    })

    render(<BindingOverview versionId="2001" />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "启用账号 buyer" }))
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))
    await waitFor(() => expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull())

    fireEvent.click(screen.getByRole("button", { name: "编辑绑定" }))
    const restored = await screen.findByRole("checkbox", { name: "启用账号 buyer" })
    expect(restored).not.toBeChecked()
    fireEvent.click(restored)
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))
    await waitFor(() => expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull())

    expect(putBodies()).toEqual([{ account_ids: [] }, { account_ids: [buyer.id] }])
  })

  it("keeps the account pool selectable when the version binds no account", async () => {
    const overview: VersionBindingOverview = { ...bindingOverviewFixture, accounts: [] }
    routeFetch({
      binding: () => jsonResponse({ data: overview }),
      update: (body) => {
        const payload = JSON.parse(body) as UpdateBindingRequest
        const accounts = payload.account_ids?.includes(buyer.id) ? [buyer] : []
        return jsonResponse({ data: { ...overview, accounts } })
      },
    })

    render(<BindingOverview versionId="2001" />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))

    // The empty-state hint stays, but it must not replace the selector.
    expect(screen.getByText(/未关联测试账号/)).toBeInTheDocument()
    const accountsRegion = screen.getByRole("region", { name: "测试账号" })
    expect(within(accountsRegion).getByText(/全局测试账号池/)).toBeInTheDocument()
    const selectableBuyer = await within(accountsRegion).findByRole("checkbox", {
      name: "启用账号 buyer",
    })
    expect(selectableBuyer).not.toBeChecked()
    fireEvent.click(selectableBuyer)
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() => expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull())
    expect(putBodies()).toEqual([{ account_ids: [buyer.id] }])
  })

  it("keeps an excluded resource that is no longer approved removable", async () => {
    routeFetch({
      binding: () => jsonResponse({ data: overviewWithUnavailableExclusion }),
      update: (body) => {
        const payload = JSON.parse(body) as UpdateBindingRequest
        return jsonResponse({
          data: {
            ...overviewWithUnavailableExclusion,
            scope_exclude: payload.scope_exclude ?? overviewWithUnavailableExclusion.scope_exclude,
            counts: {
              ...overviewWithUnavailableExclusion.counts,
              pages: { approved: 2, proposed: 1, excluded: 1, included: 1 },
            },
          },
        })
      },
    })

    render(<BindingOverview versionId="2001" />)
    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))

    const scope = screen.getByRole("region", { name: "范围排除" })
    expect(within(scope).getByText("已排除（不可用/未批准）")).toBeInTheDocument()
    const unavailable = screen.getByRole("checkbox", {
      name: "排除页面 订单草稿（不可用/未批准）",
    })
    expect(unavailable).toBeChecked()
    fireEvent.click(unavailable)
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() => expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull())
    expect(updateRequest().body).toEqual({
      scope_exclude: { pages: ["1302"], apis: [], databases: [] },
    })
  })

  it("restores the original selection and sends no request when editing is cancelled", async () => {
    routeFetch({ binding: () => jsonResponse({ data: bindingOverviewFixture }) })
    render(<BindingOverview versionId="2001" />)

    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "包含代码仓 order-center-web" }))
    fireEvent.click(screen.getByRole("button", { name: "取消编辑" }))

    expect(putBodies()).toHaveLength(0)
    expect(
      screen.queryByRole("checkbox", { name: "包含代码仓 order-center-web" }),
    ).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "编辑绑定" }))
    expect(screen.getByRole("checkbox", { name: "包含代码仓 order-center-web" })).toBeChecked()
  })

  it("keeps a read-only binding view without an edit button", async () => {
    routeFetch({
      binding: () =>
        jsonResponse({ data: { ...bindingOverviewFixture, stage: "executing", editable: false } }),
    })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText(/当前阶段为「执行中」/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑绑定" })).not.toBeInTheDocument()
  })

  it("shows the stage-gate notice and stays read-only when the save is rejected with 409", async () => {
    routeFetch({
      binding: () => jsonResponse({ data: bindingOverviewFixture }),
      update: () =>
        jsonResponse(
          {
            error: {
              code: "state_conflict",
              message: "version binding is read-only at stage executing",
            },
          },
          409,
        ),
    })

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
    routeFetch({
      binding: () =>
        jsonResponse({ data: { ...bindingOverviewFixture, stage: "completed", editable: false } }),
    })
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
    let reads = 0
    routeFetch({
      binding: () => {
        reads += 1
        return reads === 1
          ? jsonResponse({ error: { code: "unavailable", message: "绑定读取失败" } }, 503)
          : jsonResponse({ data: bindingOverviewFixture })
      },
    })

    render(<BindingOverview versionId="2001" />)
    expect(await screen.findByText("绑定概览加载失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("SUT 绑定概览")).toBeInTheDocument()
    expect(bindingReads()).toBe(2)
  })

  it("renders explicit empty states for repositories, accounts and exclusions", async () => {
    routeFetch({
      binding: () =>
        jsonResponse({
          data: {
            ...bindingOverviewFixture,
            scope_exclude: { pages: [], apis: [], databases: [] },
            counts: {
              pages: { approved: 2, proposed: 0, excluded: 0, included: 2 },
              apis: { approved: 1, proposed: 0, excluded: 0, included: 1 },
              databases: { approved: 1, proposed: 0, excluded: 0, included: 1 },
            },
            repositories: [],
            accounts: [],
          },
        }),
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
    routeFetch({
      binding: () =>
        jsonResponse({ data: { ...bindingOverviewFixture, stage: "paused", editable: false } }),
    })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText(/版本已暂停/)).toBeInTheDocument()
    expect(screen.getByText("已暂停")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑绑定" })).not.toBeInTheDocument()
  })

  it("explains the read-only binding while the version is blocked", async () => {
    routeFetch({
      binding: () =>
        jsonResponse({ data: { ...bindingOverviewFixture, stage: "blocked", editable: false } }),
    })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText(/版本已阻塞/)).toBeInTheDocument()
    expect(screen.getByText("已阻塞")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑绑定" })).not.toBeInTheDocument()
  })
})

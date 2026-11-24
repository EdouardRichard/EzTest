// v0.2.4-B: the version binds an environment directly; only account_ids is
// writable. Scope-exclusion and version-level repository-override tests were
// deleted with the version_sut chain (see task-20-report.md). The inline editor
// now toggles the global account pool only.
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { BindingOverview } from "./BindingOverview"
import { bindingOverviewFixture, catalogFixture } from "./test-fixtures"
import type { AccountResource, UpdateBindingRequest } from "./types"

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

const buyer = bindingOverviewFixture.accounts[0] as AccountResource
// `GET /binding` returns only the bound subset; bind buyer alone so operator is a
// selectable-but-unbound pool account in the editor tests.
const buyerBound = { ...bindingOverviewFixture, accounts: [buyer] }

describe("BindingOverview", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("renders the environment, its SUTs, derived repositories and enabled accounts", async () => {
    routeFetch({ binding: () => jsonResponse({ data: bindingOverviewFixture }) })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText("SUT 绑定概览")).toBeInTheDocument()
    expect(screen.getByText(/环境：测试环境/)).toBeInTheDocument()
    expect(screen.getByText(/SUT：订单中心/)).toBeInTheDocument()
    expect(screen.getByText(/SUT：支付网关/)).toBeInTheDocument()
    expect(screen.getByText("https://order.test")).toBeInTheDocument()

    // Derived environment code pool, read-only (no version-level override).
    const repos = screen.getByRole("region", { name: "代码仓" })
    expect(within(repos).getByText("order-center-web")).toBeInTheDocument()
    expect(within(repos).getByText("payment-gateway")).toBeInTheDocument()
    expect(within(repos).getAllByText("SUT 继承").length).toBeGreaterThan(0)
    expect(screen.queryByText("版本覆盖")).not.toBeInTheDocument()
    expect(screen.queryByText("已排除")).not.toBeInTheDocument()

    expect(screen.getByText(/buyer/)).toBeInTheDocument()
    expect(screen.getByText("仅显示启用账号")).toBeInTheDocument()

    // No scope-exclusion UI remains (deleted with the version_sut chain).
    expect(screen.queryByRole("region", { name: "范围排除" })).not.toBeInTheDocument()

    const edit = screen.getByRole("button", { name: "编辑绑定" })
    expect(edit).toBeEnabled()
  })

  it("invokes the optional onEdit callback when inline editing starts", async () => {
    routeFetch({ binding: () => jsonResponse({ data: bindingOverviewFixture }) })
    const onEdit = vi.fn()
    render(<BindingOverview versionId="2001" onEdit={onEdit} />)

    const edit = await screen.findByRole("button", { name: "编辑绑定" })
    expect(edit).toBeEnabled()
    fireEvent.click(edit)
    expect(onEdit).toHaveBeenCalledTimes(1)
    expect(onEdit.mock.calls[0][0]).toMatchObject({ stage: bindingOverviewFixture.stage })
    expect(screen.getByRole("button", { name: "保存绑定" })).toBeInTheDocument()
  })

  it("opens an inline editor with only account-pool checkboxes", async () => {
    routeFetch({ binding: () => jsonResponse({ data: buyerBound }) })
    render(<BindingOverview versionId="2001" />)

    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))

    // The pool comes from the catalog, so a currently unbound account is
    // selectable again even though `GET /binding` only returns the bound subset.
    expect(screen.getByText(/全局测试账号池/)).toBeInTheDocument()
    expect(screen.getByRole("checkbox", { name: "启用账号 buyer" })).toBeChecked()
    expect(await screen.findByRole("checkbox", { name: "启用账号 operator" })).not.toBeChecked()

    // Repository overrides and scope exclusion were deleted: no such checkboxes.
    expect(screen.queryByRole("checkbox", { name: /包含代码仓/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("checkbox", { name: /排除/ })).not.toBeInTheDocument()

    // Nothing changed yet, so saving is not offered.
    expect(screen.getByRole("button", { name: "保存绑定" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "取消编辑" })).toBeEnabled()
  })

  it("sends account_ids without a bound account whose checkbox was cleared", async () => {
    routeFetch({
      binding: () => jsonResponse({ data: buyerBound }),
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
    const overview = { ...bindingOverviewFixture, accounts: [] }
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

  it("restores the original selection and sends no request when editing is cancelled", async () => {
    routeFetch({ binding: () => jsonResponse({ data: buyerBound }) })
    render(<BindingOverview versionId="2001" />)

    fireEvent.click(await screen.findByRole("button", { name: "编辑绑定" }))
    fireEvent.click(await screen.findByRole("checkbox", { name: "启用账号 operator" }))
    fireEvent.click(screen.getByRole("button", { name: "取消编辑" }))

    expect(putBodies()).toHaveLength(0)
    expect(screen.queryByRole("checkbox", { name: "启用账号 operator" })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "编辑绑定" }))
    expect(await screen.findByRole("checkbox", { name: "启用账号 operator" })).not.toBeChecked()
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
      binding: () => jsonResponse({ data: buyerBound }),
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
    fireEvent.click(await screen.findByRole("checkbox", { name: "启用账号 operator" }))
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

  it("renders explicit empty states for repositories and accounts", async () => {
    routeFetch({
      binding: () =>
        jsonResponse({
          data: {
            ...bindingOverviewFixture,
            counts: {
              pages: { approved: 2, proposed: 0 },
              apis: { approved: 1, proposed: 0 },
              databases: { approved: 1, proposed: 0 },
            },
            repositories: [],
            accounts: [],
          },
        }),
    })
    render(<BindingOverview versionId="2001" />)

    expect(await screen.findByText("该环境未关联代码仓")).toBeInTheDocument()
    expect(screen.getByText(/未关联测试账号/)).toBeInTheDocument()
    // The scope-exclusion section was deleted with the version_sut chain.
    expect(screen.queryByRole("region", { name: "范围排除" })).not.toBeInTheDocument()
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

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { BindingWizard } from "./BindingWizard"
import { bindingOverviewFixture, catalogFixture, orderSutRepositories } from "./test-fixtures"
import type {
  AccountResource,
  ApiResource,
  Catalog,
  DatabaseResource,
  EnvironmentResource,
  PageResource,
  SutResource,
  Version,
} from "./types"

const TIMESTAMP = "2026-10-31T10:00:00Z"

function page(id: string, name: string, status: PageResource["status"]): PageResource {
  return {
    id,
    sut_id: "1001",
    environment_id: "1101",
    name,
    url: `https://order.test/${id}`,
    route_template: `/${id}`,
    page_type: "page",
    auth_role: "",
    module: "",
    status,
    source: "admin",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  }
}

function api(id: string, name: string, status: ApiResource["status"] = "approved"): ApiResource {
  return {
    id,
    sut_id: "1001",
    environment_id: "1101",
    method: "GET",
    path_template: `/api/${id}`,
    name,
    auth_role: "",
    source: "manual",
    status,
    schema_ref: "",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  }
}

function database(id: string, name: string): DatabaseResource {
  return {
    id,
    sut_id: "1001",
    environment_id: "1101",
    name,
    dsn: "postgres://order-db:5432/order",
    username: "order",
    password_set: true,
    read_only: true,
    status: "active",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  }
}

function account(id: string, username: string, roles: string[]): AccountResource {
  return {
    id,
    // v0.22: the global account pool carries empty SUT/environment ids.
    sut_id: "",
    environment_id: "",
    username,
    roles,
    notes: "",
    password_set: true,
    status: "active",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  }
}

const environment: EnvironmentResource = {
  id: "1101",
  sut_id: "1001",
  name: "测试环境",
  base_url: "https://order.test",
  is_default: true,
  status: "active",
  pages: [page("1301", "登录页", "approved"), page("1302", "订单列表", "approved")],
  apis: [api("1401", "查询订单")],
  databases: [database("1501", "order_db")],
  accounts: [account("1601", "buyer", ["buyer"]), account("1602", "operator", ["operator"])],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const sut: SutResource = {
  id: "1001",
  name: "订单中心",
  intro: "订单",
  environments: [environment],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const catalog: Catalog = {
  suts: [sut],
  repositories: catalogFixture.repositories,
}

const createdVersion: Version = {
  id: "2001",
  title: "v22",
  status: "initial",
  auto_design_new_requirements: false,
  bindings: [],
  created_by: "9001",
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

type MockRoute = {
  method: string
  path: string
  response: () => Response
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function errorResponse(
  status: number,
  code: string,
  message: string,
  details: unknown = {},
): Response {
  return jsonResponse({ error: { code, message, details } }, status)
}

const fetchMock = vi.fn<typeof fetch>()

function routeFetch(routes: MockRoute[]): void {
  fetchMock.mockImplementation(async (input, init) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    const route = routes.find((item) => item.method === method && url.endsWith(item.path))
    if (route === undefined) {
      return errorResponse(404, "not_found", `unexpected request ${method} ${url}`)
    }
    return route.response()
  })
  vi.stubGlobal("fetch", fetchMock)
}

function standardRoutes(
  overrides: Partial<Record<"put" | "post" | "repositories" | "binding", MockRoute>> = {},
): MockRoute[] {
  return [
    {
      method: "GET",
      path: "/api/resources/catalog",
      response: () => jsonResponse({ data: catalog }),
    },
    overrides.repositories ?? {
      method: "GET",
      path: "/api/suts/1001/repositories",
      response: () => jsonResponse({ data: orderSutRepositories }),
    },
    overrides.post ?? {
      method: "POST",
      path: "/api/versions",
      response: () => jsonResponse({ data: createdVersion }, 201),
    },
    overrides.binding ?? {
      method: "GET",
      path: "/api/versions/2001/binding",
      response: () => jsonResponse({ data: bindingOverviewFixture }),
    },
    overrides.put ?? {
      method: "PUT",
      path: "/api/versions/2001/binding",
      response: () => jsonResponse({ data: {} }),
    },
  ]
}

function renderWizard() {
  const onCreated = vi.fn()
  const onCancel = vi.fn()
  render(<BindingWizard onCreated={onCreated} onCancel={onCancel} />)
  return { onCreated, onCancel }
}

async function selectEnvironment(): Promise<void> {
  await screen.findByLabelText("版本名称")
  fireEvent.change(screen.getByLabelText("版本名称"), { target: { value: "v22" } })
  fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })
}

async function walkToStep3(): Promise<void> {
  fireEvent.click(screen.getByRole("button", { name: "下一步" }))
  await screen.findByRole("group", { name: /代码仓/ })
  fireEvent.click(screen.getByRole("button", { name: "下一步" }))
  await screen.findByRole("group", { name: /页面/ })
}

async function walkToConfirm(): Promise<void> {
  await selectEnvironment()
  await walkToStep3()
  fireEvent.click(screen.getByRole("button", { name: "下一步" }))
  await screen.findByText("确认创建")
}

function callsFor(method: string, suffix: string) {
  return fetchMock.mock.calls.filter(
    ([input, init]) =>
      String(input).endsWith(suffix) && (init?.method ?? "GET").toUpperCase() === method,
  )
}

describe("BindingWizard (v0.22 environment-only create)", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("TestBindingWizardEnvironmentOnlyCreate: step 1 has no repo/account inputs, shows the derived SUT and submits only the environment", async () => {
    routeFetch(standardRoutes())
    const { onCreated } = renderWizard()

    await screen.findByLabelText("版本名称")
    expect(screen.queryByRole("group", { name: /代码仓/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("group", { name: /测试账号/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "添加代码仓" })).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("版本名称"), { target: { value: "  v22  " } })
    fireEvent.change(screen.getByLabelText("版本描述"), {
      target: { value: "订单迭代" },
    })
    fireEvent.change(screen.getByLabelText("版本目标"), { target: { value: "验证下单" } })
    fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })

    expect(screen.getByText("SUT：订单中心（由环境推导）")).toBeInTheDocument()
    expect(screen.getByText(/页面 2 · API 1 · 目标库 1 · 测试账号 2/)).toBeInTheDocument()

    await walkToStep3()
    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByText("确认创建")
    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))

    await waitFor(() => expect(callsFor("POST", "/api/versions")).toHaveLength(1))
    const [, postInit] = callsFor("POST", "/api/versions")[0] as [string, RequestInit]
    expect(JSON.parse(String(postInit.body))).toEqual({
      title: "v22",
      description: "订单迭代",
      goal: "验证下单",
      environment_id: "1101",
    })
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(createdVersion))
  })

  it("persists multi-repo and account inclusion in the binding PUT body", async () => {
    routeFetch(standardRoutes())
    const { onCreated } = renderWizard()
    await selectEnvironment()

    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    const repos = await screen.findByRole("group", { name: /代码仓/ })
    const backend = await within(repos).findByRole("checkbox", { name: /order-center-web/ })
    const frontend = within(repos).getByRole("checkbox", { name: /payment-gateway/ })
    expect(backend).toBeChecked()
    expect(frontend).toBeChecked()
    fireEvent.click(frontend)

    const accounts = screen.getByRole("group", { name: /测试账号/ })
    const buyer = within(accounts).getByRole("checkbox", { name: /buyer/ })
    const operator = within(accounts).getByRole("checkbox", { name: /operator/ })
    expect(buyer).toBeChecked()
    fireEvent.click(operator)

    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByText("确认创建")

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    await waitFor(() => expect(callsFor("PUT", "/api/versions/2001/binding")).toHaveLength(1))

    const [, putInit] = callsFor("PUT", "/api/versions/2001/binding")[0] as [string, RequestInit]
    expect(JSON.parse(String(putInit.body))).toEqual({
      repository_ids: ["1201"],
      account_ids: ["1601"],
      scope_exclude: { pages: [], apis: [], databases: [] },
    })
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(createdVersion))
  })

  it("sends scope_exclude for unchecked pages and databases and keeps proposed resources out", async () => {
    const withProposed: EnvironmentResource = {
      ...environment,
      pages: [...environment.pages, page("1303", "购物车", "proposed")],
    }
    routeFetch([
      {
        method: "GET",
        path: "/api/resources/catalog",
        response: () =>
          jsonResponse({ data: { ...catalog, suts: [{ ...sut, environments: [withProposed] }] } }),
      },
      ...standardRoutes().filter((route) => route.path !== "/api/resources/catalog"),
    ])
    renderWizard()
    await selectEnvironment()
    await walkToStep3()

    const pagesGroup = screen.getByRole("group", { name: /页面/ })
    expect(within(pagesGroup).queryByRole("checkbox", { name: /购物车/ })).not.toBeInTheDocument()
    fireEvent.click(within(pagesGroup).getByRole("checkbox", { name: /订单列表/ }))

    const databasesGroup = screen.getByRole("group", { name: /目标库/ })
    fireEvent.click(within(databasesGroup).getByRole("checkbox", { name: /order_db/ }))

    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByText("确认创建")
    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    await waitFor(() => expect(callsFor("PUT", "/api/versions/2001/binding")).toHaveLength(1))

    const [, putInit] = callsFor("PUT", "/api/versions/2001/binding")[0] as [string, RequestInit]
    expect(JSON.parse(String(putInit.body))).toMatchObject({
      scope_exclude: { pages: ["1302"], apis: [], databases: ["1501"] },
    })
  })

  it("shows a typed 409 binding conflict inline and retries without re-creating the version", async () => {
    routeFetch(
      standardRoutes({
        put: {
          method: "PUT",
          path: "/api/versions/2001/binding",
          response: () =>
            errorResponse(409, "state_conflict", "version binding is read-only at stage executing"),
        },
      }),
    )
    renderWizard()
    await walkToConfirm()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("version binding is read-only at stage executing")
    expect(alert).toHaveTextContent("步骤 1 的版本信息修改不会随重试提交")
    expect(screen.getByRole("button", { name: "刷新绑定" })).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试保存绑定" }))
    await waitFor(() => expect(callsFor("PUT", "/api/versions/2001/binding")).toHaveLength(2))
    expect(callsFor("POST", "/api/versions")).toHaveLength(1)
  })

  it("maps a typed 400 field error back to the scope step", async () => {
    routeFetch(
      standardRoutes({
        put: {
          method: "PUT",
          path: "/api/versions/2001/binding",
          response: () =>
            errorResponse(
              400,
              "invalid_argument",
              "page 1302 does not belong to the bound SUT environment",
              { field: "scope_exclude.pages" },
            ),
        },
      }),
    )
    renderWizard()
    await walkToConfirm()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("scope_exclude.pages")
    fireEvent.click(screen.getByRole("button", { name: "返回步骤 3 修正" }))
    expect(screen.getByText("步骤 3 / 4")).toBeInTheDocument()
  })

  it("validates the version name and environment before leaving step 1", async () => {
    routeFetch(standardRoutes())
    renderWizard()
    await screen.findByLabelText("版本名称")

    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    expect(await screen.findByText("请输入版本名称")).toBeInTheDocument()
    expect(screen.getByText("请选择环境")).toBeInTheDocument()
    expect(screen.queryByRole("group", { name: /代码仓/ })).not.toBeInTheDocument()
  })

  it("updates the live scope counts on step 3 and the step 4 review when a page is unchecked", async () => {
    routeFetch(standardRoutes())
    renderWizard()
    await selectEnvironment()
    await walkToStep3()

    expect(screen.getByText("页面 已确认 2 · 生效 2 · 排除 0 · 待确认 0")).toBeInTheDocument()
    const pages = screen.getByRole("group", { name: /页面/ })
    fireEvent.click(within(pages).getByRole("checkbox", { name: /订单列表/ }))

    expect(screen.getByText("页面 已确认 2 · 生效 1 · 排除 1 · 待确认 0")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByText("确认创建")
    expect(screen.getByText("已确认 2 · 生效 1 · 排除 1 · 待确认 0")).toBeInTheDocument()
  })

  it("shows proposed API counts even when no page is proposed", async () => {
    const withProposedApi: EnvironmentResource = {
      ...environment,
      apis: [...environment.apis, api("1402", "退款接口", "proposed")],
    }
    routeFetch([
      {
        method: "GET",
        path: "/api/resources/catalog",
        response: () =>
          jsonResponse({
            data: { ...catalog, suts: [{ ...sut, environments: [withProposedApi] }] },
          }),
      },
      ...standardRoutes().filter((route) => route.path !== "/api/resources/catalog"),
    ])
    renderWizard()
    await screen.findByLabelText("版本名称")
    fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })

    expect(await screen.findByText(/探索待确认：页面 0 · API 1/)).toBeInTheDocument()
  })

  it("adds an unassociated catalog repository as a version-level override without POSTing to the SUT", async () => {
    routeFetch(
      standardRoutes({
        repositories: {
          method: "GET",
          path: "/api/suts/1001/repositories",
          response: () => jsonResponse({ data: orderSutRepositories.slice(0, 1) }),
        },
      }),
    )
    renderWizard()
    await selectEnvironment()
    fireEvent.click(screen.getByRole("button", { name: "下一步" }))

    const repositories = await screen.findByRole("group", { name: /代码仓/ })
    const payment = await within(repositories).findByRole("checkbox", { name: /payment-gateway/ })
    expect(payment).not.toBeChecked()
    fireEvent.click(payment)

    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByRole("group", { name: /页面/ })
    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByText("确认创建")
    expect(screen.getByText(/payment-gateway（版本覆盖）/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    await waitFor(() => expect(callsFor("PUT", "/api/versions/2001/binding")).toHaveLength(1))

    const [, putInit] = callsFor("PUT", "/api/versions/2001/binding")[0] as [string, RequestInit]
    expect(JSON.parse(String(putInit.body))).toMatchObject({
      repository_ids: ["1201", "1202"],
    })
    expect(callsFor("POST", "/api/suts/1001/repositories")).toHaveLength(0)
  })

  it("omits repository_ids and explains the fallback when the SUT repository read fails", async () => {
    routeFetch(
      standardRoutes({
        repositories: {
          method: "GET",
          path: "/api/suts/1001/repositories",
          response: () => errorResponse(503, "unavailable", "代码仓暂不可用"),
        },
      }),
    )
    renderWizard()
    await selectEnvironment()
    fireEvent.click(screen.getByRole("button", { name: "下一步" }))

    expect(await screen.findByText("SUT 代码仓读取失败")).toBeInTheDocument()
    expect(await screen.findByText("代码仓暂不可用")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByRole("group", { name: /页面/ })
    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByText("确认创建")
    expect(
      screen.getByText(/继承 SUT 的全部代码仓（代码仓接口未就绪，未覆盖）/),
    ).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    await waitFor(() => expect(callsFor("PUT", "/api/versions/2001/binding")).toHaveLength(1))

    const [, putInit] = callsFor("PUT", "/api/versions/2001/binding")[0] as [string, RequestInit]
    const body = JSON.parse(String(putInit.body)) as Record<string, unknown>
    expect(body).not.toHaveProperty("repository_ids")
    expect(body.account_ids).toEqual(["1601", "1602"])
  })

  it("refreshes the created binding after a 409 and renders the refreshed overview data", async () => {
    routeFetch(
      standardRoutes({
        put: {
          method: "PUT",
          path: "/api/versions/2001/binding",
          response: () =>
            errorResponse(409, "state_conflict", "version binding is read-only at stage executing"),
        },
        binding: {
          method: "GET",
          path: "/api/versions/2001/binding",
          response: () =>
            jsonResponse({
              data: { ...bindingOverviewFixture, stage: "executing", editable: false },
            }),
        },
      }),
    )
    renderWizard()
    await walkToConfirm()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    const alert = await screen.findByRole("alert")
    fireEvent.click(screen.getByRole("button", { name: "刷新绑定" }))

    await waitFor(() => expect(callsFor("GET", "/api/versions/2001/binding")).toHaveLength(1))
    expect(await screen.findByText(/已读取最新绑定：阶段 执行中/)).toBeInTheDocument()
    expect(within(alert).getByText("order-center-web")).toBeInTheDocument()
    expect(within(alert).getByText("版本覆盖")).toBeInTheDocument()
  })

  it("shows the binding refresh error when re-reading the binding fails", async () => {
    routeFetch(
      standardRoutes({
        put: {
          method: "PUT",
          path: "/api/versions/2001/binding",
          response: () =>
            errorResponse(409, "state_conflict", "version binding is read-only at stage executing"),
        },
        binding: {
          method: "GET",
          path: "/api/versions/2001/binding",
          response: () => errorResponse(503, "unavailable", "绑定读取失败"),
        },
      }),
    )
    renderWizard()
    await walkToConfirm()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    await screen.findByRole("alert")
    fireEvent.click(screen.getByRole("button", { name: "刷新绑定" }))

    expect(await screen.findByText(/刷新绑定失败：绑定读取失败/)).toBeInTheDocument()
  })

  it("renders the created-version success panel when no navigation callback is supplied", async () => {
    routeFetch(standardRoutes())
    render(<BindingWizard onCancel={vi.fn()} />)
    await selectEnvironment()
    await walkToStep3()
    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    await screen.findByText("确认创建")

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))

    expect(await screen.findByText("版本 #2001 已创建，绑定已保存。")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "完成" })).toBeInTheDocument()
  })

  it("labels a create failure differently from a binding failure", async () => {
    routeFetch(
      standardRoutes({
        post: {
          method: "POST",
          path: "/api/versions",
          response: () => errorResponse(503, "unavailable", "版本创建接口不可用"),
        },
      }),
    )
    renderWizard()
    await walkToConfirm()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("创建版本失败")
    expect(alert).toHaveTextContent("版本创建接口不可用")
    expect(alert).not.toHaveTextContent("保存绑定失败")
    expect(callsFor("PUT", "/api/versions/2001/binding")).toHaveLength(0)
  })
})

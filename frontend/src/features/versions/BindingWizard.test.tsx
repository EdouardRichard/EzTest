// T19b: fixtures adapted to v0.2.4-B shapes; assertions on removed scope-exclusion/repository-override behaviour stay red pending T20.
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
    // v0.2.4-B: the global account pool carries an optional SUT affinity hint.
    sut_id: "",
    username,
    roles,
    notes: "",
    password_set: true,
    status: "active",
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  }
}

const sut: SutResource = {
  id: "1001",
  environment_id: "1101",
  name: "订单中心",
  intro: "订单",
  base_url: "https://order.test",
  pages: [page("1301", "登录页", "approved"), page("1302", "订单列表", "approved")],
  apis: [api("1401", "查询订单")],
  databases: [database("1501", "order_db")],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const environment: EnvironmentResource = {
  id: "1101",
  name: "测试环境",
  description: "",
  status: "active",
  suts: [sut],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const accounts: AccountResource[] = [
  account("1601", "buyer", ["buyer"]),
  account("1602", "operator", ["operator"]),
]

const catalog: Catalog = {
  environments: [environment],
  repositories: catalogFixture.repositories,
  accounts,
}

const createdVersion: Version = {
  id: "2001",
  title: "v22",
  status: "initial",
  auto_design_new_requirements: false,
  environment_id: "1101",
  binding: {
    environment,
    pages: sut.pages,
    apis: sut.apis,
    databases: sut.databases,
    accounts,
    repositories: [],
    code_baseline_commit: null,
  },
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

  it("maps a typed 400 field error back to the account step", async () => {
    routeFetch(
      standardRoutes({
        put: {
          method: "PUT",
          path: "/api/versions/2001/binding",
          response: () =>
            errorResponse(400, "invalid_argument", "account 1601 is not in the global pool", {
              field: "account_ids[0]",
            }),
        },
      }),
    )
    renderWizard()
    await walkToConfirm()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("account_ids[0]")
    fireEvent.click(screen.getByRole("button", { name: "返回步骤 2 修正" }))
    expect(screen.getByText("步骤 2 / 4")).toBeInTheDocument()
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

  it("shows proposed API counts even when no page is proposed", async () => {
    const withProposedApi: EnvironmentResource = {
      ...environment,
      suts: [{ ...sut, apis: [...sut.apis, api("1402", "退款接口", "proposed")] }],
    }
    routeFetch([
      {
        method: "GET",
        path: "/api/resources/catalog",
        response: () =>
          jsonResponse({
            data: { ...catalog, environments: [withProposedApi] },
          }),
      },
      ...standardRoutes().filter((route) => route.path !== "/api/resources/catalog"),
    ])
    renderWizard()
    await screen.findByLabelText("版本名称")
    fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })

    expect(await screen.findByText(/探索待确认：页面 0 · API 1/)).toBeInTheDocument()
  })

  it("previews the code pool aggregated across every SUT of the environment", async () => {
    // A 2-SUT environment (the canonical fixture shape): each SUT inherits a
    // different repository. The preview must show both, not just the first SUT's.
    const secondSut: SutResource = { ...sut, id: "1002", name: "支付网关" }
    const twoSutCatalog: Catalog = {
      ...catalog,
      environments: [{ ...environment, suts: [sut, secondSut] }],
    }
    routeFetch([
      {
        method: "GET",
        path: "/api/resources/catalog",
        response: () => jsonResponse({ data: twoSutCatalog }),
      },
      {
        method: "GET",
        path: "/api/suts/1001/repositories",
        response: () => jsonResponse({ data: [orderSutRepositories[0]] }),
      },
      {
        method: "GET",
        path: "/api/suts/1002/repositories",
        response: () => jsonResponse({ data: [{ ...orderSutRepositories[1], sut_id: "1002" }] }),
      },
      {
        method: "POST",
        path: "/api/versions",
        response: () => jsonResponse({ data: createdVersion }, 201),
      },
      {
        method: "GET",
        path: "/api/versions/2001/binding",
        response: () => jsonResponse({ data: bindingOverviewFixture }),
      },
      {
        method: "PUT",
        path: "/api/versions/2001/binding",
        response: () => jsonResponse({ data: {} }),
      },
    ])
    renderWizard()
    await selectEnvironment()
    fireEvent.click(screen.getByRole("button", { name: "下一步" }))

    const repos = await screen.findByRole("group", { name: /代码仓/ })
    expect(await within(repos).findByText("order-center-web")).toBeInTheDocument()
    expect(within(repos).getByText("payment-gateway")).toBeInTheDocument()
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
    expect(within(alert).getAllByText("SUT 继承").length).toBeGreaterThan(0)
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

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  createAccount,
  createApi,
  createDatabase,
  createEnvironment,
  createPage,
  createRepository,
  createSut,
  createSutWithDefaultEnvironment,
  DefaultEnvironmentError,
  deleteAccount,
  deleteApi,
  deleteEnvironment,
  deletePage,
  deleteRepository,
  deleteSut,
  getSut,
  importApis,
  importPages,
  listRepositories,
  listSuts,
  updateAccount,
  updateApi,
  updateDatabase,
  updateEnvironment,
  updatePage,
  updateRepository,
  updateSut,
} from "./api"
import {
  accountFixture,
  apiFixture,
  databaseFixture,
  environmentFixture,
  pageFixture,
  repositoryFixture,
  sutFixture,
  sutListItemFixture,
} from "./test-fixtures"
import type {
  AccountFormValues,
  ApiFormValues,
  DatabaseFormValues,
  EnvironmentFormValues,
  PageFormValues,
  RepositoryFormValues,
} from "./types"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function errorResponse(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({
      error: { code, message, request_id: "req_resources", retryable: false, details: {} },
    }),
    { status, headers: { "Content-Type": "application/json" } },
  )
}

function readBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string") return undefined
  return JSON.parse(init.body) as unknown
}

const fetchMock = vi.fn<typeof fetch>()

const pageValues: PageFormValues = {
  name: "订单列表",
  url: "https://order.test/orders",
  routeTemplate: "/orders",
  pageType: "page",
  authRole: "",
  module: "订单",
}

const apiValues: ApiFormValues = {
  name: "查询订单",
  method: "GET",
  pathTemplate: "/api/orders",
  authRole: "",
  schemaRef: "",
}

const databaseValues: DatabaseFormValues = {
  environmentId: "1101",
  name: "order_db",
  dsn: "postgres://order-db:5432/order",
  username: "order",
  password: "secret",
  readOnly: true,
}

const accountValues: AccountFormValues = {
  environmentId: "1101",
  username: "buyer",
  password: "secret",
  roles: "buyer, order_admin",
  notes: "",
}

const repositoryValues: RepositoryFormValues = {
  name: "order-center-web",
  path: "D:/code/order-center-web",
  branch: "main",
  include: "src/**, tests/**",
  exclude: "node_modules/**",
}

const environmentValues: EnvironmentFormValues = {
  name: "测试环境",
  baseUrl: "https://order.test",
  isDefault: true,
  status: "active",
}

describe("resources API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
  })

  it("lists SUTs through the cursor pagination envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [sutListItemFixture], next_cursor: null, has_more: false }),
    )

    await expect(listSuts()).resolves.toEqual([sutListItemFixture])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/resources/suts?limit=100")
    expect(init?.method).toBe("GET")
  })

  it("reads a SUT aggregate by its path id", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: sutFixture }))

    await expect(getSut("1001")).resolves.toEqual(sutFixture)

    const [url] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/resources/suts/1001")
  })

  it("creates and updates a SUT with the contract body", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: sutListItemFixture }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: sutFixture }))

    await createSut({ name: "订单中心", intro: "订单主链路" })
    await updateSut("1001", { name: "订单中心 v2", intro: "新简介" })

    const [createUrl, createInit] = fetchMock.mock.calls[0]
    expect(createUrl).toBe("http://localhost:13333/api/resources/suts")
    expect(createInit?.method).toBe("POST")
    expect(readBody(createInit)).toEqual({ name: "订单中心", intro: "订单主链路" })

    const [updateUrl, updateInit] = fetchMock.mock.calls[1]
    expect(updateUrl).toBe("http://localhost:13333/api/resources/suts/1001")
    expect(updateInit?.method).toBe("PATCH")
    expect(readBody(updateInit)).toEqual({ name: "订单中心 v2", intro: "新简介" })
  })

  it("includes the optional SUT family in create and update bodies", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: sutListItemFixture }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: sutFixture }))

    await createSut({ name: "订单中心", intro: "订单主链路", family: "payments" })
    await updateSut("1001", { name: "订单中心", intro: "订单主链路", family: "order-center" })

    expect(readBody(fetchMock.mock.calls[0][1])).toEqual({
      name: "订单中心",
      intro: "订单主链路",
      family: "payments",
    })
    expect(readBody(fetchMock.mock.calls[1][1])).toEqual({
      name: "订单中心",
      intro: "订单主链路",
      family: "order-center",
    })
  })

  it("deletes a SUT with DELETE", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))

    await deleteSut("1001")

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/resources/suts/1001")
    expect(init?.method).toBe("DELETE")
  })

  it("creates the SUT and then its default environment in one flow", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: sutListItemFixture }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: environmentFixture }, 201))

    const result = await createSutWithDefaultEnvironment({
      sut: { name: "订单中心", intro: "订单主链路" },
      environment: { name: "测试环境", base_url: "https://order.test" },
    })

    expect(result).toEqual({ sut: sutListItemFixture, environment: environmentFixture })
    expect(fetchMock).toHaveBeenCalledTimes(2)

    const [sutUrl, sutInit] = fetchMock.mock.calls[0]
    expect(sutUrl).toBe("http://localhost:13333/api/resources/suts")
    expect(readBody(sutInit)).toEqual({ name: "订单中心", intro: "订单主链路" })

    const [environmentUrl, environmentInit] = fetchMock.mock.calls[1]
    expect(environmentUrl).toBe("http://localhost:13333/api/resources/suts/1001/environments")
    expect(readBody(environmentInit)).toEqual({
      name: "测试环境",
      base_url: "https://order.test",
      is_default: true,
      status: "active",
    })
  })

  it("keeps the created SUT when the default environment fails", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: sutListItemFixture }, 201))
      .mockResolvedValueOnce(errorResponse(409, "conflict", "environment name already exists"))

    const caught = await createSutWithDefaultEnvironment({
      sut: { name: "订单中心", intro: "订单主链路" },
      environment: { name: "测试环境", base_url: "https://order.test" },
    }).catch((error: unknown) => error)

    expect(caught).toBeInstanceOf(DefaultEnvironmentError)
    if (caught instanceof DefaultEnvironmentError) {
      expect(caught.sut.id).toBe("1001")
      expect(caught.message).toContain("默认环境创建失败")
      expect(caught.message).toContain("environment name already exists")
    }
  })

  it("maps environment create/update/delete to the environment routes", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: environmentFixture }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: environmentFixture }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await createEnvironment("1001", environmentValues)
    await updateEnvironment("1101", { ...environmentValues, name: "预发环境" })
    await deleteEnvironment("1101")

    const [createUrl, createInit] = fetchMock.mock.calls[0]
    expect(createUrl).toBe("http://localhost:13333/api/resources/suts/1001/environments")
    expect(readBody(createInit)).toEqual({
      name: "测试环境",
      base_url: "https://order.test",
      is_default: true,
      status: "active",
    })

    const [updateUrl, updateInit] = fetchMock.mock.calls[1]
    expect(updateUrl).toBe("http://localhost:13333/api/resources/environments/1101")
    expect(updateInit?.method).toBe("PATCH")
    expect(readBody(updateInit)).toEqual({
      name: "预发环境",
      base_url: "https://order.test",
      is_default: true,
      status: "active",
    })

    const [deleteUrl, deleteInit] = fetchMock.mock.calls[2]
    expect(deleteUrl).toBe("http://localhost:13333/api/resources/environments/1101")
    expect(deleteInit?.method).toBe("DELETE")
  })

  it("maps page CRUD and import to the page routes", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: pageFixture }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: pageFixture }))
      .mockResolvedValueOnce(
        jsonResponse({ data: { created: 1, skipped: 0, items: [pageFixture] } }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await createPage("1101", pageValues)
    await updatePage("1301", { ...pageValues, name: "订单列表 v2" })
    const imported = await importPages("1101", [pageValues])
    await deletePage("1301")

    expect(imported).toEqual({ created: 1, skipped: 0, items: [pageFixture] })

    const [createUrl, createInit] = fetchMock.mock.calls[0]
    expect(createUrl).toBe("http://localhost:13333/api/resources/environments/1101/pages")
    expect(readBody(createInit)).toEqual({
      name: "订单列表",
      url: "https://order.test/orders",
      route_template: "/orders",
      page_type: "page",
      auth_role: "",
      module: "订单",
    })

    const [updateUrl, updateInit] = fetchMock.mock.calls[1]
    expect(updateUrl).toBe("http://localhost:13333/api/resources/pages/1301")
    expect(updateInit?.method).toBe("PATCH")

    const [importUrl, importInit] = fetchMock.mock.calls[2]
    expect(importUrl).toBe("http://localhost:13333/api/resources/environments/1101/pages/import")
    expect(readBody(importInit)).toEqual({
      items: [
        {
          name: "订单列表",
          url: "https://order.test/orders",
          route_template: "/orders",
          page_type: "page",
          auth_role: "",
          module: "订单",
        },
      ],
    })

    const [deleteUrl, deleteInit] = fetchMock.mock.calls[3]
    expect(deleteUrl).toBe("http://localhost:13333/api/resources/pages/1301")
    expect(deleteInit?.method).toBe("DELETE")
  })

  it("maps API CRUD and import to the API routes", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: apiFixture }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: apiFixture }))
      .mockResolvedValueOnce(
        jsonResponse({ data: { created: 1, skipped: 0, items: [apiFixture] } }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await createApi("1101", apiValues)
    await updateApi("1401", { ...apiValues, name: "查询订单 v2" })
    const imported = await importApis("1101", [apiValues])
    await deleteApi("1401")

    expect(imported).toEqual({ created: 1, skipped: 0, items: [apiFixture] })

    const [createUrl, createInit] = fetchMock.mock.calls[0]
    expect(createUrl).toBe("http://localhost:13333/api/resources/environments/1101/apis")
    expect(readBody(createInit)).toEqual({
      method: "GET",
      path_template: "/api/orders",
      name: "查询订单",
      auth_role: "",
      schema_ref: "",
    })

    const [updateUrl, updateInit] = fetchMock.mock.calls[1]
    expect(updateUrl).toBe("http://localhost:13333/api/resources/apis/1401")
    expect(updateInit?.method).toBe("PATCH")

    const [importUrl, importInit] = fetchMock.mock.calls[2]
    expect(importUrl).toBe("http://localhost:13333/api/resources/environments/1101/apis/import")
    expect(readBody(importInit)).toEqual({
      items: [
        {
          method: "GET",
          path_template: "/api/orders",
          name: "查询订单",
          auth_role: "",
          schema_ref: "",
        },
      ],
    })

    const [deleteUrl, deleteInit] = fetchMock.mock.calls[3]
    expect(deleteUrl).toBe("http://localhost:13333/api/resources/apis/1401")
    expect(deleteInit?.method).toBe("DELETE")
  })

  it("creates a target database under the SUT with environment_id", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: databaseFixture }, 201))

    await createDatabase("1001", databaseValues)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/resources/suts/1001/databases")
    expect(readBody(init)).toEqual({
      environment_id: "1101",
      name: "order_db",
      dsn: "postgres://order-db:5432/order",
      username: "order",
      password: "secret",
      read_only: true,
    })
  })

  it("omits an unchanged database password and sends an empty string to clear it", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: databaseFixture }))
      .mockResolvedValueOnce(jsonResponse({ data: databaseFixture }))

    const { password: _password, ...unchanged } = databaseValues
    await updateDatabase("1501", unchanged)
    await updateDatabase("1501", { ...unchanged, password: "" })

    const [keepUrl, keepInit] = fetchMock.mock.calls[0]
    expect(keepUrl).toBe("http://localhost:13333/api/resources/databases/1501")
    expect(readBody(keepInit)).toEqual({
      name: "order_db",
      dsn: "postgres://order-db:5432/order",
      username: "order",
      read_only: true,
    })

    const [clearUrl, clearInit] = fetchMock.mock.calls[1]
    expect(clearUrl).toBe("http://localhost:13333/api/resources/databases/1501")
    expect(readBody(clearInit)).toMatchObject({ password: "" })
  })

  it("creates and updates accounts with roles arrays and optional password clearing", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: accountFixture }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: accountFixture }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await createAccount("1001", accountValues)
    await updateAccount("1601", {
      username: "buyer",
      roles: ["buyer"],
      notes: "",
      password: "",
    })
    await deleteAccount("1601")

    const [createUrl, createInit] = fetchMock.mock.calls[0]
    expect(createUrl).toBe("http://localhost:13333/api/resources/suts/1001/accounts")
    expect(readBody(createInit)).toEqual({
      environment_id: "1101",
      username: "buyer",
      password: "secret",
      roles: ["buyer", "order_admin"],
      notes: "",
    })

    const [updateUrl, updateInit] = fetchMock.mock.calls[1]
    expect(updateUrl).toBe("http://localhost:13333/api/resources/accounts/1601")
    expect(readBody(updateInit)).toEqual({
      username: "buyer",
      roles: ["buyer"],
      notes: "",
      password: "",
    })

    const [deleteUrl, deleteInit] = fetchMock.mock.calls[2]
    expect(deleteUrl).toBe("http://localhost:13333/api/resources/accounts/1601")
    expect(deleteInit?.method).toBe("DELETE")
  })

  it("lists, creates, updates and deletes code repositories", async () => {
    fetchMock
      .mockResolvedValueOnce(
        jsonResponse({ data: [repositoryFixture], next_cursor: null, has_more: false }),
      )
      .mockResolvedValueOnce(jsonResponse({ data: repositoryFixture }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: repositoryFixture }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await expect(listRepositories()).resolves.toEqual([repositoryFixture])
    await createRepository(repositoryValues)
    await updateRepository("1201", repositoryValues)
    await deleteRepository("1201")

    const [listUrl] = fetchMock.mock.calls[0]
    expect(listUrl).toBe("http://localhost:13333/api/resources/code-repos?limit=100")

    const [createUrl, createInit] = fetchMock.mock.calls[1]
    expect(createUrl).toBe("http://localhost:13333/api/resources/code-repos")
    expect(readBody(createInit)).toEqual({
      name: "order-center-web",
      path: "D:/code/order-center-web",
      branch: "main",
      include: ["src/**", "tests/**"],
      exclude: ["node_modules/**"],
    })

    const [updateUrl, updateInit] = fetchMock.mock.calls[2]
    expect(updateUrl).toBe("http://localhost:13333/api/resources/code-repos/1201")
    expect(updateInit?.method).toBe("PATCH")

    const [deleteUrl, deleteInit] = fetchMock.mock.calls[3]
    expect(deleteUrl).toBe("http://localhost:13333/api/resources/code-repos/1201")
    expect(deleteInit?.method).toBe("DELETE")
  })
})

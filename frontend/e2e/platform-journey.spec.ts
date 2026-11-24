import fs from "node:fs"
import path from "node:path"

import { expect, test } from "@playwright/test"

import { ADMIN_PASSWORD, ADMIN_USERNAME } from "./credentials"

const API = process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:13333"
const suffix = `${Date.now()}-${Math.floor(Math.random() * 1000)}`
const sutName = `E2E 订单中心 ${suffix}`
const versionTitle = `E2E 验证版本 ${suffix}`

type Setup = {
  sutID: string
  environmentID: string
  pageID: string
  apiID: string
  databaseID: string
  accountID: string
  repositoryID: string
  versionID: string
}

let setup: Setup
let token = ""

async function api<T>(
  request: import("@playwright/test").APIRequestContext,
  method: "POST" | "GET" | "PATCH",
  pathName: string,
  body?: unknown,
  expected = 200,
): Promise<T> {
  const response = await request.fetch(API + pathName, {
    method,
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    data: body,
  })
  if (response.status() !== expected) {
    throw new Error(`${method} ${pathName} -> ${response.status()}: ${await response.text()}`)
  }
  return (await response.json()) as T
}

test.beforeAll(async ({ request }) => {
  const login = await api<{ data: { token: string } }>(request, "POST", "/api/auth/login", {
    username: ADMIN_USERNAME,
    password: ADMIN_PASSWORD,
  })
  token = login.data.token

  // v0.2.4-B inverted hierarchy: environment -> SUT -> assets. The environment
  // is a top-level resource ({name, description, status}); base_url lives on the
  // SUT; is_default is deleted.
  const environment = await api<{ data: { id: string } }>(
    request,
    "POST",
    "/api/resources/environments",
    { name: `e2e-${suffix}`, description: "P0 E2E", status: "active" },
    201,
  )
  setup = { environmentID: environment.data.id } as Setup

  const sut = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/environments/${setup.environmentID}/suts`,
    { name: sutName, intro: "P0 E2E", base_url: "http://localhost:8080" },
    201,
  )
  setup.sutID = sut.data.id

  const page = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${setup.sutID}/pages`,
    { name: "E2E 首页", url: "http://localhost:8080/home", route_template: `/e2e-${suffix}` },
    201,
  )
  setup.pageID = page.data.id

  const apiEndpoint = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${setup.sutID}/apis`,
    { name: "E2E Orders", method: "GET", path_template: `/api/e2e-${suffix}` },
    201,
  )
  setup.apiID = apiEndpoint.data.id

  const database = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${setup.sutID}/databases`,
    {
      name: `e2e_db_${suffix}`,
      dsn: "postgres://localhost/e2e",
      username: "readonly",
      password: "secret",
      read_only: true,
    },
    201,
  )
  setup.databaseID = database.data.id

  // Accounts are a global pool; sut_id is an optional affinity hint (no
  // environment_id, no per-SUT account route).
  const account = await api<{ data: { id: string } }>(
    request,
    "POST",
    "/api/resources/accounts",
    {
      sut_id: setup.sutID,
      username: `e2e_buyer_${suffix}`,
      password: "secret",
      roles: ["buyer"],
    },
    201,
  )
  setup.accountID = account.data.id

  const repository = await api<{ data: { id: string } }>(
    request,
    "POST",
    "/api/resources/code-repos",
    { name: `e2e-repo-${suffix}`, path: "D:/e2e", branch: "main" },
    201,
  )
  setup.repositoryID = repository.data.id

  // The version's code pool is derived from the SUT's repository associations
  // (replaces the old per-binding code_repository_id).
  await api(
    request,
    "POST",
    `/api/suts/${setup.sutID}/repositories`,
    { repository_id: setup.repositoryID, role: "backend" },
    201,
  )

  // A version binds one environment directly; the account subset is selected
  // through the binding update (the old bindings[] array is deleted).
  const version = await api<{ data: { id: string } }>(
    request,
    "POST",
    "/api/versions",
    { title: versionTitle, environment_id: setup.environmentID },
    201,
  )
  setup.versionID = version.data.id

  await api(
    request,
    "PATCH",
    `/api/versions/${setup.versionID}/binding`,
    { account_ids: [setup.accountID] },
    200,
  )
})

test("P0 admin journey: login, versions, detail, resources, settings", async ({ page }) => {
  const pageErrors: string[] = []
  page.on("pageerror", (error) => pageErrors.push(String(error)))

  await page.goto("/login")
  await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible()
  await expect(page.getByText("初始管理员密码")).toHaveCount(0)
  await expect(page.getByText(/EZTEST_INITIAL_ADMIN_PASSWORD/)).toHaveCount(0)
  await expect(page.getByText("admin123")).toHaveCount(0)

  await page.fill("#username", ADMIN_USERNAME)
  await page.fill("#password", ADMIN_PASSWORD)
  await page.getByRole("button", { name: "登录" }).click()
  await page.waitForURL("**/versions")

  for (const label of ["版本", "问题", "报告", "知识", "资源", "质量", "Agent 中心", "设置"]) {
    await expect(page.getByRole("link", { name: new RegExp(label) }).first()).toBeVisible()
  }

  await expect(page.getByText(versionTitle).first()).toBeVisible()
  await expect(page.getByText("1 个 SUT").first()).toBeVisible()

  const screenshotDir = path.resolve(process.cwd(), "..", ".tmp", "p0-acceptance", "e2e")
  fs.mkdirSync(screenshotDir, { recursive: true })
  await page.screenshot({ path: path.join(screenshotDir, "versions.png"), fullPage: true })

  await page.getByText(versionTitle).first().click()
  await page.waitForURL(new RegExp(`/versions/${setup.versionID}`))
  await expect(page.getByRole("heading", { name: versionTitle })).toBeVisible()
  for (const label of [
    "总览",
    "需求与澄清",
    "用例设计",
    "执行",
    "探索链路",
    "问题",
    "报告",
    "代码与变更",
  ]) {
    await expect(
      page
        .getByRole("tab", { name: new RegExp(label) })
        .or(page.getByText(label).first())
        .first(),
    ).toBeVisible()
  }
  await expect(page.getByText(sutName).first()).toBeVisible()
  await page.screenshot({ path: path.join(screenshotDir, "version-detail.png"), fullPage: true })

  await page.goto("/resources?tab=suts")
  await expect(page.getByText(sutName).first()).toBeVisible()
  await expect(page.getByText("SUT 与环境").first()).toBeVisible()
  await page.screenshot({ path: path.join(screenshotDir, "resources.png"), fullPage: true })

  await page.goto("/settings?tab=general")
  await expect(page.getByText("常规设置").first()).toBeVisible()
  await expect(page.getByText("execution.max_concurrent_case_runs").first()).toBeVisible()
  await page.screenshot({ path: path.join(screenshotDir, "settings.png"), fullPage: true })

  expect(pageErrors).toEqual([])
})

test("serves the app icon on a fresh context without a favicon 404", async ({ page }) => {
  const missing: string[] = []
  page.on("response", (response) => {
    if (response.status() === 404) missing.push(new URL(response.url()).pathname)
  })

  await page.goto("/login")
  await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible()
  await page.waitForLoadState("networkidle")

  expect(missing).not.toContain("/favicon.ico")

  const icon = await page.request.get("/icon.svg")
  expect(icon.status()).toBe(200)
  expect(icon.headers()["content-type"] ?? "").toContain("image/svg+xml")
})

test("renders the Chinese product 404 with the application shell", async ({ page }) => {
  const response = await page.goto(`/__eztest_missing_route_${suffix}`)

  expect(response?.status()).toBe(404)
  await expect(page.getByRole("heading", { name: "页面不存在" })).toBeVisible()
  await expect(page.getByText("404")).toBeVisible()
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible()
  await expect(page.getByRole("link", { name: /返回版本列表/ })).toHaveAttribute(
    "href",
    "/versions",
  )
})

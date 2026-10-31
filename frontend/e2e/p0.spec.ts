import fs from "node:fs"
import path from "node:path"

import { expect, test } from "@playwright/test"

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
    username: "admin",
    password: "admin123",
  })
  token = login.data.token

  const sut = await api<{ data: { id: string } }>(request, "POST", "/api/resources/suts", {
    name: sutName,
    intro: "P0 E2E",
  }, 201)
  setup = { sutID: sut.data.id } as Setup

  const environment = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${setup.sutID}/environments`,
    { name: `e2e-${suffix}`, base_url: "http://localhost:8080", is_default: true },
    201,
  )
  setup.environmentID = environment.data.id

  const page = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/environments/${setup.environmentID}/pages`,
    { name: "E2E 首页", url: "http://localhost:8080/home", route_template: `/e2e-${suffix}` },
    201,
  )
  setup.pageID = page.data.id

  const apiEndpoint = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/environments/${setup.environmentID}/apis`,
    { name: "E2E Orders", method: "GET", path_template: `/api/e2e-${suffix}` },
    201,
  )
  setup.apiID = apiEndpoint.data.id

  const database = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${setup.sutID}/databases`,
    {
      environment_id: setup.environmentID,
      name: `e2e_db_${suffix}`,
      dsn: "postgres://localhost/e2e",
      username: "readonly",
      password: "secret",
      read_only: true,
    },
    201,
  )
  setup.databaseID = database.data.id

  const account = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${setup.sutID}/accounts`,
    {
      environment_id: setup.environmentID,
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

  const version = await api<{ data: { id: string } }>(request, "POST", "/api/versions", {
    title: versionTitle,
    bindings: [
      {
        sut_id: setup.sutID,
        environment_id: setup.environmentID,
        code_repository_id: setup.repositoryID,
        page_ids: [setup.pageID],
        api_ids: [setup.apiID],
        database_ids: [setup.databaseID],
        accounts: [{ account_id: setup.accountID, role: "buyer" }],
      },
    ],
  }, 201)
  setup.versionID = version.data.id
})

test("P0 admin journey: login, versions, detail, resources, settings", async ({ page }) => {
  const pageErrors: string[] = []
  page.on("pageerror", (error) => pageErrors.push(String(error)))

  await page.goto("/login")
  await expect(page.getByText("首次启动默认 admin / admin123")).toBeVisible()

  await page.fill("#username", "admin")
  await page.fill("#password", "admin123")
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
  for (const label of ["总览", "需求与澄清", "用例设计", "执行", "探索链路", "问题", "报告", "代码与变更"]) {
    await expect(page.getByRole("tab", { name: new RegExp(label) }).or(page.getByText(label).first()).first()).toBeVisible()
  }
  await expect(page.getByText(sutName).first()).toBeVisible()
  await page.screenshot({ path: path.join(screenshotDir, "version-detail.png"), fullPage: true })

  await page.goto("/resources?tab=suts")
  await expect(page.getByText(sutName).first()).toBeVisible()
  await expect(page.getByText("SUT 与环境").first()).toBeVisible()
  await page.screenshot({ path: path.join(screenshotDir, "resources.png"), fullPage: true })

  await page.goto("/settings?tab=general")
  await expect(page.getByText("常规设置").first()).toBeVisible()
  await expect(page.getByText("execution.global_concurrency").first()).toBeVisible()
  await page.screenshot({ path: path.join(screenshotDir, "settings.png"), fullPage: true })

  expect(pageErrors).toEqual([])
})

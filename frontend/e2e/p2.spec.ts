import fs from "node:fs"
import path from "node:path"

import { type APIRequestContext, expect, test } from "@playwright/test"

const API = process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:14333"
const FAKE_LLM = process.env.EZTEST_E2E_FAKE_LLM_URL?.trim() || "http://127.0.0.1:13335/v1"
const LLM_BASE = process.env.EZTEST_E2E_LLM_BASE_URL?.trim() || FAKE_LLM
const LLM_KEY = process.env.EZTEST_E2E_LLM_KEY?.trim() || "fake-llm-key"
const LLM_TIMEOUT_SECONDS = Number(process.env.EZTEST_E2E_LLM_TIMEOUT_SECONDS || "30")
const CASE_TIMEOUT_MS = Number(process.env.EZTEST_E2E_CASE_TIMEOUT_MS || "90000")
const suffix = `${Date.now()}-${Math.floor(Math.random() * 1000)}`

type Setup = {
  versionID: string
  requirementIDs: string[]
}

let setup: Setup
let token = ""

async function api<T>(
  request: APIRequestContext,
  method: "GET" | "POST" | "PATCH",
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

  const config = await api<{ data: { id: string } }>(request, "POST", "/api/admin/llm/configs", {
    name: `p2-e2e-llm-${suffix}`,
    provider: "openai",
    protocol: "openai",
    base_url: LLM_BASE,
    api_key: LLM_KEY,
    timeout_seconds: LLM_TIMEOUT_SECONDS,
    max_retries: 1,
    is_default: true,
  })
  await api(
    request,
    "POST",
    `/api/admin/llm/configs/${config.data.id}/refresh-models`,
    undefined,
    200,
  )

  const sut = await api<{ data: { id: string } }>(
    request,
    "POST",
    "/api/resources/suts",
    { name: `P2 E2E SUT ${suffix}`, intro: "P2 design E2E" },
    201,
  )
  const environment = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${sut.data.id}/environments`,
    { name: `e2e-${suffix}`, base_url: "https://e2e.example.test", is_default: true },
    201,
  )
  const page = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/environments/${environment.data.id}/pages`,
    {
      name: "登录页",
      url: "https://e2e.example.test/login",
      route_template: `/login-${suffix}`,
      page_type: "page",
      auth_role: "guest",
      module: "auth",
    },
    201,
  )
  const apiEndpoint = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/environments/${environment.data.id}/apis`,
    { name: "登录 API", method: "POST", path_template: `/api/login-${suffix}`, auth_role: "guest" },
    201,
  )
  const version = await api<{ data: { id: string; bindings: Array<{ id: string }> } }>(
    request,
    "POST",
    "/api/versions",
    {
      title: `P2 E2E 版本 ${suffix}`,
      bindings: [
        {
          sut_id: sut.data.id,
          environment_id: environment.data.id,
          page_ids: [page.data.id],
          api_ids: [apiEndpoint.data.id],
          database_ids: [],
          accounts: [],
        },
      ],
    },
    201,
  )
  const versionSUTID = version.data.bindings[0]?.id
  if (!versionSUTID) throw new Error("version has no binding id")

  const requirementIDs: string[] = []
  for (const [index, title] of ["账号密码登录", "登录失败提示"].entries()) {
    const requirement = await api<{ data: { id: string } }>(
      request,
      "POST",
      `/api/versions/${version.data.id}/requirements`,
      {
        title: `${title} ${suffix}`,
        description: `${title} 的验收描述`,
        acceptance_criteria: `${title} 可验收`,
        priority: index === 0 ? "P1" : "P2",
        constraints: {},
        targets: {
          version_suts: [versionSUTID],
          pages: [page.data.id],
          apis: [apiEndpoint.data.id],
        },
      },
      201,
    )
    await api(
      request,
      "POST",
      `/api/versions/${version.data.id}/requirements/${requirement.data.id}/confirmations`,
      undefined,
      200,
    )
    requirementIDs.push(requirement.data.id)
  }

  setup = { versionID: version.data.id, requirementIDs }
})

test("P2 design workspace: conversation, live mindmap, case detail, confirm and export", async ({
  page,
}) => {
  test.setTimeout(240_000)
  const pageErrors: string[] = []
  page.on("pageerror", (error) => pageErrors.push(String(error)))

  await page.goto("/login")
  await page.fill("#username", "admin")
  await page.fill("#password", "admin123")
  await page.getByRole("button", { name: "登录" }).click()
  await page.waitForURL("**/versions")

  await page.goto(`/versions/${setup.versionID}?tab=design`)
  await expect(page.getByTestId("design-tab")).toBeVisible()
  await expect(page.getByTestId("design-mindmap")).toBeVisible()
  await expect(page.getByTestId("design-sse-status")).toBeVisible()

  // New design conversation -> planner/author tasks -> live graph.
  await page.getByTestId("design-new-conversation").click()
  const caseCards = page.locator('[data-testid^="case-card-"]')
  await expect(caseCards.first()).toBeVisible({ timeout: CASE_TIMEOUT_MS })
  const caseCount = await caseCards.count()
  expect(caseCount).toBeGreaterThanOrEqual(1)

  // Select a case, open detail, confirm it.
  const firstCase = caseCards.first()
  const caseID = (await firstCase.getAttribute("data-testid"))?.replace("case-card-", "") ?? ""
  expect(caseID).not.toBe("")
  await page.getByTestId(`case-select-${caseID}`).click()
  await expect(page.getByTestId("case-detail")).toBeVisible()
  await page.getByTestId(`btn-confirm-detail-${caseID}`).click()
  await expect(page.getByTestId("design-confirm-notice")).toBeVisible({ timeout: 30_000 })

  // Export the whole design as Markdown, authenticated download.
  await page.getByTestId("export-cases").click()
  const downloadPromise = page.waitForEvent("download", { timeout: 60_000 })
  await page.getByTestId("export-format-markdown").click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toMatch(/\.md$/)
  await expect(page.getByTestId("export-status")).toBeVisible({ timeout: 30_000 })

  // Evidence screenshots.
  const screenshotDir =
    process.env.EZTEST_E2E_SCREENSHOT_DIR?.trim() ||
    path.resolve(process.cwd(), "..", ".tmp", "p2-acceptance", "screenshots")
  fs.mkdirSync(screenshotDir, { recursive: true })
  await page.screenshot({ path: path.join(screenshotDir, "design-workspace.png"), fullPage: true })
  await page.getByTestId("case-detail").screenshot({
    path: path.join(screenshotDir, "case-detail.png"),
  })

  expect(pageErrors).toEqual([])
})

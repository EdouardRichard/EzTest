import { mkdirSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import { type APIRequestContext, type Browser, expect, type Page, test } from "@playwright/test"

const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""
const USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"
const API_URL = process.env.EZTEST_E2E_API_URL?.trim() || "http://localhost:13333"

/** The deterministic fixture target started by scripts/e2e.ps1 (spec §9.2). */
const FIXTURE_URL = "http://127.0.0.1:13335"
const FIXTURE_USERNAME = "fixture"
const FIXTURE_PASSWORD = "fixture-pass"

/** P2 acceptance screenshots live in the gitignored repository `.tmp` (spec §9.2). */
const P2_ACCEPTANCE_DIR = fileURLToPath(new URL("../../.tmp/p2-acceptance", import.meta.url))

function requireAdminPassword(): string {
  if (!ADMIN_PASSWORD) {
    throw new Error("EZTEST_E2E_ADMIN_PASSWORD must be set; run via scripts/e2e.ps1")
  }
  return ADMIN_PASSWORD
}

/** Logs in through the real login form and waits for the protected shell. */
async function login(page: Page, username: string, password: string): Promise<void> {
  await page.goto("/login")
  await page.getByLabel("用户名").fill(username)
  await page.getByLabel("密码").fill(password)
  await page.getByRole("button", { name: "登录", exact: true }).click()
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 })
}

/** Signs out through the topbar user menu. */
async function logout(page: Page): Promise<void> {
  await page.getByRole("button", { name: "打开用户菜单" }).click()
  await page.getByRole("menuitem", { name: "退出登录" }).click()
  await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible({ timeout: 15_000 })
}

/** Returns one unique, key-safe suffix for this run. */
function uniqueSuffix(): string {
  return `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
}

/** Logs into the fixture target in a separate context and exports storageState. */
async function fixtureStorageState(browser: Browser): Promise<unknown> {
  const context = await browser.newContext()
  try {
    const page = await context.newPage()
    await page.goto(`${FIXTURE_URL}/login`)
    await page.getByLabel("Username").fill(FIXTURE_USERNAME)
    await page.getByLabel("Password").fill(FIXTURE_PASSWORD)
    await page.getByRole("button", { name: "Sign in" }).click()
    await expect(page.getByRole("heading", { name: "Fixture Account" })).toBeVisible()
    return await context.storageState()
  } finally {
    await context.close()
  }
}

/** Returns a stable screenshot path under `.tmp/p2-acceptance/`. */
function screenshotPath(name: string): string {
  mkdirSync(P2_ACCEPTANCE_DIR, { recursive: true })
  return join(P2_ACCEPTANCE_DIR, name)
}

/** Logs in through the real admin API and returns the bearer token. */
async function adminApiToken(request: APIRequestContext): Promise<string> {
  const response = await request.post(`${API_URL}/api/auth/login`, {
    data: { username: ADMIN_USERNAME, password: requireAdminPassword() },
  })
  expect(response.ok()).toBeTruthy()
  const body = (await response.json()) as { data?: { token?: string } }
  const token = body.data?.token
  expect(token).toBeTruthy()
  return token ?? ""
}

test("admin configures resources, imports a fixture session, and the normal user is read-only", async ({
  page,
  browser,
  request,
}) => {
  // The flow creates four resources through the real forms, exports a real
  // fixture storageState and imports it, then re-logs in as a normal user.
  test.setTimeout(180_000)
  const adminPassword = requireAdminPassword()
  const suffix = uniqueSuffix()
  const systemKey = `fixture-web-${suffix}`
  const systemName = `Fixture Web ${suffix}`
  const environmentName = `本地环境 ${suffix}`
  const accountKey = `fixture-account-${suffix}`
  const accountLabel = `Fixture 账号 ${suffix}`
  const normalUsername = `qa_res_${suffix}`

  await login(page, ADMIN_USERNAME, adminPassword)
  await page.goto("/resources")
  await expect(page.getByRole("heading", { name: "公共资源" })).toBeVisible()
  await expect(page.getByText("只读", { exact: true })).toHaveCount(0)

  // 1. System: kind=web, baseUrl on the running fixture target.
  await page.getByRole("button", { name: "新建系统" }).click()
  await page.getByLabel("系统标识 key").fill(systemKey)
  await page.getByLabel("名称", { exact: true }).fill(systemName)
  await page.getByLabel("类型", { exact: true }).selectOption({ label: "Web 站点" })
  await page.getByLabel("Base URL").fill(FIXTURE_URL)
  await page.getByRole("button", { name: "保存系统" }).click()
  await expect(
    page.getByRole("status").filter({ hasText: `已创建系统 ${systemName}。` }),
  ).toBeVisible()
  await expect(page.getByRole("button", { name: systemName, exact: true })).toBeVisible()

  // 2. Environment: the fixture login_check of spec §9.2. The URL stays relative
  // and the backend joins it with the environment Base URL (spec §6.3).
  await page.getByRole("button", { name: "新建环境" }).click()
  await page.getByLabel("环境标识 key").fill("local")
  await page.getByLabel("名称", { exact: true }).fill(environmentName)
  await page.getByLabel("Base URL").fill(FIXTURE_URL)
  await page.getByLabel("登录检查 URL").fill("/account")
  await page.getByLabel("期望状态码（逗号分隔）").fill("200")
  await page.getByLabel("响应体包含").fill("Fixture Account")
  await page.getByRole("button", { name: "保存环境" }).click()
  await expect(
    page.getByRole("status").filter({ hasText: `已创建环境 ${environmentName}。` }),
  ).toBeVisible()
  const environmentRow = page.getByRole("row").filter({ hasText: environmentName })
  await expect(environmentRow).toContainText("已配置")
  await expect(environmentRow).toContainText(FIXTURE_URL)

  // 3. Account + manual Access for the fixture identity.
  await page.getByRole("tab", { name: "账号与访问" }).click()
  await expect(page.getByRole("heading", { name: "账号与访问" })).toBeVisible()
  await page.getByRole("button", { name: "新建账号" }).click()
  await page.getByLabel("账号标识 key").fill(accountKey)
  await page.getByLabel("标签", { exact: true }).fill(accountLabel)
  await page.getByLabel("用户名", { exact: true }).fill(FIXTURE_USERNAME)
  await page.getByRole("button", { name: "保存账号" }).click()
  await expect(
    page.getByRole("status").filter({ hasText: `已创建账号 ${accountLabel}。` }),
  ).toBeVisible()
  await expect(page.getByRole("button", { name: "新建 Access" })).toBeVisible()

  await page.getByRole("button", { name: "新建 Access" }).click()
  const systemOption = page.locator("#access-system option").filter({ hasText: systemName })
  await expect(systemOption).toHaveCount(1)
  await page.locator("#access-system").selectOption({ label: `${systemName}（${systemKey}）` })
  await page.getByLabel("认证类型").selectOption({ label: "手工" })
  await page.getByRole("button", { name: "保存", exact: true }).click()
  await expect(
    page.getByRole("status").filter({ hasText: `已为 ${systemName} 创建 Access。` }),
  ).toBeVisible()
  const accessRow = page
    .getByRole("row")
    .filter({ hasText: systemName })
    .filter({ hasText: "手工" })
  await expect(accessRow).toContainText("未配置")

  // 4. Real fixture login in a separate browser context; close it after export.
  const storageState = await fixtureStorageState(browser)

  // 5. Import the exported storageState through the real dialog.
  await page.getByRole("tab", { name: "会话" }).click()
  await expect(page.getByRole("heading", { name: "会话", exact: true })).toBeVisible()
  await page.getByRole("button", { name: "导入会话" }).click()
  const dialog = page.getByRole("dialog", { name: "导入会话" })
  await expect(dialog).toBeVisible()

  await expect(
    dialog.locator("#session-import-account option").filter({ hasText: accountLabel }),
  ).toHaveCount(1)
  await dialog
    .locator("#session-import-account")
    .selectOption({ label: `${accountLabel}（${FIXTURE_USERNAME}）` })
  await expect(
    dialog.locator("#session-import-access option").filter({ hasText: "manual" }),
  ).toHaveCount(1)
  await dialog.locator("#session-import-access").selectOption({ label: `${systemName} · manual` })
  await expect(
    dialog.locator("#session-import-environment option").filter({ hasText: environmentName }),
  ).toHaveCount(1)
  await dialog
    .locator("#session-import-environment")
    .selectOption({ label: `${environmentName}（${FIXTURE_URL}）` })
  await dialog.locator("#session-import-file").setInputFiles({
    name: "fixture-storage-state.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify(storageState)),
  })
  await expect(dialog.locator("#session-import-json")).not.toHaveValue("")
  await dialog.getByRole("button", { name: "导入", exact: true }).click()
  const importResult = dialog.getByRole("status")
  await expect(importResult).toContainText(/已导入会话 #\d+：状态 valid。/)
  await expect(importResult).toContainText("能力校验通过。")
  await expect(importResult).toContainText("快照：允许 1 / 拒绝 0 / 错误 0。")
  const importText = (await importResult.textContent()) ?? ""
  const importedId = Number(/已导入会话 #(\d+)/.exec(importText)?.[1] ?? 0)
  expect(importedId).toBeGreaterThan(0)
  await dialog.getByRole("button", { name: "关闭" }).click()
  await expect(dialog).toHaveCount(0)

  // 6. The imported session is valid and its login snapshot reports allowed.
  const detail = page.getByRole("region", { name: "会话详情" })
  await expect(detail.getByRole("heading", { name: `会话 #${importedId}` })).toBeVisible()
  await expect(detail.getByText("valid", { exact: true })).toBeVisible()
  await expect(detail.getByText("允许 1 / 拒绝 0 / 错误 0", { exact: true })).toBeVisible()
  await expect(detail.getByText("allowed", { exact: true })).toBeVisible()
  await expect(detail.getByText("200", { exact: true })).toBeVisible()
  await expect(detail.getByText("能力未验证", { exact: true })).toHaveCount(0)
  await page.screenshot({ path: screenshotPath("resources-session-valid.png"), fullPage: true })

  // 7. A normal user sees the readonly badge/data and gets 403 from the write API.
  const adminToken = await adminApiToken(request)
  const created = await request.post(`${API_URL}/api/users`, {
    headers: { Authorization: `Bearer ${adminToken}` },
    data: { username: normalUsername, password: USER_PASSWORD },
  })
  expect(created.status()).toBe(201)

  await logout(page)
  await login(page, normalUsername, USER_PASSWORD)
  await page.goto("/resources")
  await expect(page.getByRole("heading", { name: "公共资源" })).toBeVisible()
  await expect(page.getByText("只读", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "新建系统" })).toHaveCount(0)
  await expect(page.getByRole("button", { name: `编辑 ${systemName}` })).toHaveCount(0)
  await expect(page.getByRole("button", { name: systemName, exact: true })).toBeVisible()

  await page.getByRole("tab", { name: "账号与访问" }).click()
  await expect(page.getByRole("button", { name: "新建账号" })).toHaveCount(0)
  await expect(page.getByRole("button", { name: `编辑账号 ${accountLabel}` })).toHaveCount(0)

  await page.getByRole("tab", { name: "会话" }).click()
  await expect(page.getByRole("button", { name: "导入会话" })).toHaveCount(0)
  await expect(page.getByRole("button", { name: "校验", exact: true })).toHaveCount(0)

  // Read-only is enforced by the API, not only by hiding UI controls.
  const statuses = await page.evaluate(
    async ({ apiUrl }) => {
      const token = sessionStorage.getItem("eztest.token") ?? ""
      const response = await fetch(`${apiUrl}/api/resources/systems`, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          key: "forbidden-e2e",
          name: "forbidden",
          kind: "web",
          baseUrl: "http://127.0.0.1:13335",
        }),
      })
      const body = (await response.json()) as { error?: { code?: string } }
      return { status: response.status, code: body.error?.code ?? "" }
    },
    { apiUrl: API_URL },
  )
  expect(statuses).toEqual({ status: 403, code: "forbidden" })
  await page.screenshot({ path: screenshotPath("resources-readonly.png"), fullPage: true })
})

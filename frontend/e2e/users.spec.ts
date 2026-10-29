import { expect, type Page, test } from "@playwright/test"

const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""
const USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"
const API_URL = process.env.EZTEST_E2E_API_URL?.trim() || "http://localhost:13333"

function requireAdminPassword(): string {
  if (!ADMIN_PASSWORD) {
    throw new Error("EZTEST_E2E_ADMIN_PASSWORD must be set; run via scripts/e2e.ps1")
  }
  return ADMIN_PASSWORD
}

async function login(page: Page, username: string, password: string): Promise<void> {
  await page.goto("/login")
  await page.getByLabel("用户名").fill(username)
  await page.getByLabel("密码").fill(password)
  await page.getByRole("button", { name: "登录", exact: true }).click()
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 })
}

async function logout(page: Page): Promise<void> {
  await page.getByRole("button", { name: "打开用户菜单" }).click()
  await page.getByRole("menuitem", { name: "退出登录" }).click()
  await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible({ timeout: 15_000 })
}

test("admin creates a normal user and that user is read-only", async ({ page }) => {
  const adminPassword = requireAdminPassword()
  // Unique per run and per retry; the e2e database itself is reset by
  // scripts/e2e.ps1 before the suite starts.
  const username = `qa_e2e_${Date.now().toString(36)}`

  await login(page, ADMIN_USERNAME, adminPassword)
  await page.goto("/settings")
  await page.getByRole("tab", { name: "用户" }).click()

  await page.getByLabel("用户名", { exact: true }).fill(username)
  await page.getByLabel("初始密码").fill(USER_PASSWORD)
  await page.getByRole("button", { name: "创建用户" }).click()

  await expect(page.getByText(`已创建用户 ${username}。`)).toBeVisible()
  const row = page.getByRole("row", { name: new RegExp(username) })
  await expect(row).toBeVisible()
  await expect(row).toContainText("普通用户")
  await expect(row).toContainText("活跃")

  await logout(page)
  await login(page, username, USER_PASSWORD)

  // The normal-user settings page is the account + theme card only; the
  // admin-only tabs (and their write actions) are not rendered.
  await page.goto("/settings")
  await expect(page.getByRole("heading", { name: "外观" })).toBeVisible()
  await expect(page.getByRole("tab", { name: "用户" })).toHaveCount(0)
  await expect(page.getByRole("tab", { name: "LLM" })).toHaveCount(0)
  await expect(page.getByRole("tab", { name: "SSE 测试" })).toHaveCount(0)
  await expect(page.getByRole("button", { name: "创建用户" })).toHaveCount(0)

  // Read-only is enforced by the API, not only by hiding UI: a real browser
  // request with the normal user's bearer gets 403 for list and create.
  const statuses = await page.evaluate(
    async ({ apiUrl }) => {
      const token = sessionStorage.getItem("eztest.token") ?? ""
      const headers = {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      }
      const list = await fetch(`${apiUrl}/api/users?limit=100`, { headers })
      const create = await fetch(`${apiUrl}/api/users`, {
        method: "POST",
        headers,
        body: JSON.stringify({ username: "qa_forbidden", password: "qa-forbidden-pass" }),
      })
      const body = (await list.json()) as { error?: { code?: string } }
      return { list: list.status, create: create.status, code: body.error?.code ?? "" }
    },
    { apiUrl: API_URL },
  )

  expect(statuses.list).toBe(403)
  expect(statuses.create).toBe(403)
  expect(statuses.code).toBe("forbidden")
})

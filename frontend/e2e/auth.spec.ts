import { expect, type Page, test } from "@playwright/test"

const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""

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

test.describe("admin authentication", () => {
  test("admin logs in, sees the overview, and logs out", async ({ page }) => {
    const password = requireAdminPassword()

    await login(page, ADMIN_USERNAME, password)

    await expect(page).toHaveURL(/\/$/)
    await expect(page.getByRole("button", { name: "打开用户菜单" })).toContainText(ADMIN_USERNAME)

    await logout(page)
    await expect(page).toHaveURL(/\/login/)
  })

  test("wrong credentials show a generic error and keep the session signed out", async ({
    page,
  }) => {
    requireAdminPassword()

    await page.goto("/login")
    await page.getByLabel("用户名").fill(ADMIN_USERNAME)
    await page.getByLabel("密码").fill("definitely-not-the-password")
    await page.getByRole("button", { name: "登录", exact: true }).click()

    await expect(page.getByRole("alert").filter({ hasText: "用户名或密码错误。" })).toBeVisible()
    await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible()
    await expect(page).toHaveURL(/\/login/)
  })
})

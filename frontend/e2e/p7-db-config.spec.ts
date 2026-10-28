import { expect, test } from "@playwright/test"

import { ADMIN_PASSWORD, ADMIN_USERNAME, API_URL, login, uniqueSuffix } from "./p3-helpers"
import { authHeaders } from "./p4-helpers"
import { seedMindmapVersion } from "./p5-helpers"
import {
  captureConsoleErrors,
  dbTargetFixture,
  expectNoConsoleErrors,
  expectNoSecretInPage,
  getP7DbTarget,
  getP7Environment,
  getP7System,
  p7ScreenshotPath,
  skipP7SpecWhenHarnessMissing,
} from "./p7-helpers"

/**
 * P7 DB target E2E (design spec §10.7/§12.4): an administrator opens the
 * environment "目标数据库" section, fills the real local PostgreSQL target,
 * tests the connection (read-only transaction + write-privilege warning), saves
 * it without echoing the password, and an ordinary user can neither see the
 * section nor read the admin-only endpoint (403).
 */

const SPEC_TIMEOUT_MS = 300_000
const USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"

test.describe.configure({ mode: "serial" })

test("P7 DB 目标：管理员配置/测试/只读告警；普通用户隐藏且 403", async ({ page, request }) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireDbTarget: true, requireFakeLlm: false })) {
    return
  }
  const fixture = dbTargetFixture()
  if (fixture === null) throw new Error("EZTEST_E2E_DB_TARGET_* is not set")

  const monitor = captureConsoleErrors(page)
  try {
    const seed = await test.step("seed a visible system/environment", async () => {
      const suffix = uniqueSuffix()
      const seeded = await seedMindmapVersion(request, { suffix })
      const [system, environment] = await Promise.all([
        getP7System(request, seeded.token, seeded.systemId),
        getP7Environment(request, seeded.token, seeded.environmentId),
      ])
      await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
      return { seeded, system, environment }
    })

    await test.step("admin: configure and test the DB target connection", async () => {
      await page.goto("/resources")
      await expect(page.getByRole("heading", { name: "公共资源" })).toBeVisible({ timeout: 60_000 })
      await page.getByRole("button", { name: seed.system.name, exact: true }).click()
      await expect(
        page.getByRole("cell", { name: seed.environment.name, exact: true }),
      ).toBeVisible({ timeout: 60_000 })

      await page
        .getByRole("button", { name: `目标数据库 ${seed.environment.name}`, exact: true })
        .click()
      const section = page.getByTestId("db-target-section")
      await expect(section).toBeVisible({ timeout: 60_000 })
      const form = page.getByTestId("db-target-form")
      await expect(form).toBeVisible()

      await form.getByLabel("主机", { exact: true }).fill(fixture.host)
      await form.getByLabel("端口", { exact: true }).fill(String(fixture.port))
      await form.getByLabel("数据库", { exact: true }).fill(fixture.database)
      await form.getByLabel("用户名", { exact: true }).fill(fixture.username)
      await form.getByLabel("密码", { exact: true }).fill(fixture.password)

      await page.getByTestId("db-target-test-btn").click()
      const result = page.getByTestId("db-target-result")
      await expect(result).toBeVisible({ timeout: 60_000 })
      await expect(result).toContainText("连接成功")
      await expect(result).toContainText("只读事务")
      const warning = page.getByTestId("db-target-readonly-warning")
      await expect(warning).toBeVisible({ timeout: 60_000 })
      await expect(warning).toContainText("写权限")
      await page.screenshot({ path: p7ScreenshotPath("p7-db-target-01-test.png"), fullPage: true })

      await page.getByRole("button", { name: "保存目标数据库", exact: true }).click()
      await expect(page.getByText("已保存目标数据库。")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("db-target-password-state")).toHaveText("密码已设置")
      await expect(form.getByLabel("密码", { exact: true })).toHaveValue("")

      const stored = await getP7DbTarget(request, seed.seeded.token, seed.seeded.environmentId)
      expect(stored?.host).toBe(fixture.host)
      expect(stored?.database).toBe(fixture.database)
      expect(stored?.credentialSet).toBe(true)
      expect(stored?.enabled).toBe(true)
      await expectNoSecretInPage(page, fixture.password, "DB 目标配置")
      await page.screenshot({ path: p7ScreenshotPath("p7-db-target-02-saved.png"), fullPage: true })
    })

    await test.step("ordinary user: no section and 403 from the API", async () => {
      const adminToken = seed.seeded.token
      const username = `qa_p7_${uniqueSuffix()}`
      const created = await request.post(`${API_URL}/api/users`, {
        headers: authHeaders(adminToken),
        data: { username, password: USER_PASSWORD },
      })
      expect(created.status(), "create normal user").toBe(201)

      const normalLogin = await request.post(`${API_URL}/api/auth/login`, {
        data: { username, password: USER_PASSWORD },
      })
      expect(normalLogin.status(), "normal user login").toBe(200)
      const loginBody = (await normalLogin.json()) as { data: { token?: string } }
      const normalToken = loginBody.data.token ?? ""
      expect(normalToken).not.toBe("")

      const forbiddenTarget = await request.get(
        `${API_URL}/api/resources/environments/${seed.seeded.environmentId}/db-target`,
        { headers: authHeaders(normalToken) },
      )
      expect(forbiddenTarget.status()).toBe(403)
      const forbiddenBody = (await forbiddenTarget.json()) as {
        error?: { code?: string }
      }
      expect(forbiddenBody.error?.code).toBe("forbidden")

      // UI: the section (and its action) is hidden for non-admins.
      await page.getByRole("button", { name: "打开用户菜单" }).click()
      await page.getByRole("menuitem", { name: "退出登录" }).click()
      await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible({
        timeout: 15_000,
      })
      await login(page, username, USER_PASSWORD)
      await page.goto("/resources")
      await expect(page.getByRole("heading", { name: "公共资源" })).toBeVisible({ timeout: 60_000 })
      await page.getByRole("button", { name: seed.system.name, exact: true }).click()
      await expect(
        page.getByRole("cell", { name: seed.environment.name, exact: true }),
      ).toBeVisible({ timeout: 60_000 })
      await expect(
        page.getByRole("button", { name: `目标数据库 ${seed.environment.name}`, exact: true }),
      ).toHaveCount(0)
      await expect(page.getByTestId("db-target-section")).toHaveCount(0)
      await page.screenshot({
        path: p7ScreenshotPath("p7-db-target-03-readonly.png"),
        fullPage: true,
      })
    })

    await expectNoConsoleErrors(monitor, page, "P7 DB 目标")
  } finally {
    monitor.stop()
  }
})

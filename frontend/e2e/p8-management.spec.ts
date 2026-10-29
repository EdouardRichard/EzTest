import { execFileSync } from "node:child_process"
import { mkdirSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import { type APIRequestContext, type APIResponse, expect, type Page, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  API_URL,
  createVersion,
  login,
  uniqueSuffix,
} from "./p3-helpers"
import { authHeaders } from "./p4-helpers"
import {
  captureConsoleErrors,
  expectNoConsoleErrors,
  expectNoSecretInPage,
  skipP7SpecWhenHarnessMissing,
} from "./p7-helpers"

/**
 * P8 Task 34: management surface E2E (design spec §8.1/§8.2).
 *
 * - Agent centre: administrators get the 13-role catalogue, the console/config
 *   tab and per-role edit entry; ordinary users get the read-only banner, no
 *   write controls, a readable catalogue and a 403 for a profile write.
 * - Settings: the admin audit viewer filters real audit records by action and
 *   actor, the read-only basic-config panel has no editable control and the GC
 *   dry-run/confirmation path works; ordinary users keep only the theme panel.
 *
 * The fixtures (a normal user and one empty version) are created through the
 * real APIs; the version row is removed in a `finally` so other specs do not
 * inherit an audit-only version.
 */

const USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"

/** Marker of the console error the phase-boundary regression step injects. */
const INJECTED_CONSOLE_ERROR = "p8-e2e-injected-early-console-error"

/** Directory of the committed P8 cross-wave acceptance evidence. */
const ACCEPTANCE_DIR = fileURLToPath(new URL("../../docs/acceptance/p8/e2e", import.meta.url))

function evidencePath(name: string): string {
  mkdirSync(ACCEPTANCE_DIR, { recursive: true })
  return join(ACCEPTANCE_DIR, name)
}

type DataEnvelope<T> = { data: T }

async function readData<T>(response: APIResponse, expected: number, label: string): Promise<T> {
  expect(response.status(), `${label}: unexpected HTTP ${response.status()}`).toBe(expected)
  const body = (await response.json()) as DataEnvelope<T>
  return body.data
}

/** Runs one SQL statement through the psql client exported by e2e.ps1. */
function runPsql(sql: string): string {
  const psql = process.env.EZTEST_E2E_PSQL?.trim()
  const databaseUrl = process.env.EZTEST_E2E_DATABASE_URL?.trim()
  if (!psql || !databaseUrl) {
    throw new Error("EZTEST_E2E_PSQL/EZTEST_E2E_DATABASE_URL are not set; run via scripts/e2e.ps1")
  }
  let parsed: URL
  try {
    parsed = new URL(databaseUrl)
  } catch {
    throw new Error("EZTEST_E2E_DATABASE_URL is not a valid URL")
  }
  const database = decodeURIComponent(parsed.pathname.replace(/^\//, ""))
  if (database === "") throw new Error("EZTEST_E2E_DATABASE_URL has no database name")
  return execFileSync(
    psql,
    [
      "-X",
      "-h",
      parsed.hostname,
      "-p",
      parsed.port || "5432",
      "-U",
      decodeURIComponent(parsed.username),
      "-d",
      database,
      "-v",
      "ON_ERROR_STOP=1",
      "-tA",
      "-c",
      sql,
    ],
    {
      encoding: "utf8",
      env: {
        ...process.env,
        PGPASSWORD: decodeURIComponent(parsed.password),
        PGSSLMODE: parsed.searchParams.get("sslmode") ?? "disable",
      },
    },
  ).trim()
}

async function loginAdmin(page: Page): Promise<void> {
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
}

/** Logs out through the real user menu. */
async function logout(page: Page): Promise<void> {
  await page.getByRole("button", { name: "打开用户菜单" }).click()
  await page.getByRole("menuitem", { name: "退出登录" }).click()
  await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible({ timeout: 15_000 })
}

/**
 * Switches accounts through the real user menu. The outgoing phase is asserted
 * before the monitor is stopped: stopping first (then only checking the final
 * monitor) would silently drop every console error raised before the last
 * account switch. The logout redirect can race in-flight shell reads (todos)
 * whose token was just cleared, which Chromium reports as a 401 resource console
 * error; that transition happens while no monitor is active, so no exception
 * rule is needed.
 */
async function switchAccount(
  page: Page,
  monitor: ReturnType<typeof captureConsoleErrors>,
  username: string,
  password: string,
): Promise<ReturnType<typeof captureConsoleErrors>> {
  await expectNoConsoleErrors(monitor, page, `P8 管理面（切换到 ${username} 前）`)
  await logout(page)
  await login(page, username, password)
  return captureConsoleErrors(page)
}

/** Creates one normal user through the admin API. */
async function createNormalUser(
  request: APIRequestContext,
  token: string,
  suffix: string,
): Promise<{ id: number; username: string; password: string }> {
  const username = `qa_p8_mgmt_${suffix}`
  const created = await request.post(`${API_URL}/api/users`, {
    headers: authHeaders(token),
    data: { username, password: USER_PASSWORD },
  })
  const user = await readData<{ id: number; username: string }>(created, 201, "create normal user")
  return { id: user.id, username, password: USER_PASSWORD }
}

// ---------------------------------------------------------------------------

test("P8 管理面：Agent 中心只读/可配、审计筛选、基础配置与 GC 确认", async ({ page, request }) => {
  test.setTimeout(300_000)
  if (await skipP7SpecWhenHarnessMissing({ requireFakeLlm: false })) return

  let monitor = captureConsoleErrors(page)
  const suffix = uniqueSuffix()
  let auditVersionId = ""
  try {
    const loginResponse = await request.post(`${API_URL}/api/auth/login`, {
      data: { username: ADMIN_USERNAME, password: ADMIN_PASSWORD },
    })
    const token = (await readData<{ token: string }>(loginResponse, 200, "admin login")).token
    const me = await request.get(`${API_URL}/api/auth/me`, { headers: authHeaders(token) })
    const admin = await readData<{ id: number; role: string }>(me, 200, "read admin identity")
    expect(admin.role).toBe("admin")
    const normal = await createNormalUser(request, token, suffix)

    // A real requirement/version write produces the audit record the viewer filters.
    const version = await createVersion(request, token, {
      name: `P8 审计版本 ${suffix}`,
      label: `p8-audit-${suffix}`,
    })
    auditVersionId = version.id

    const profilesResponse = await request.get(`${API_URL}/api/agents/profiles?limit=100`, {
      headers: authHeaders(token),
    })
    expect(profilesResponse.status(), "admin lists agent profiles").toBe(200)

    await test.step("admin Agent centre: 13 roles, configurable console", async () => {
      await loginAdmin(page)
      await page.goto("/agents")
      await expect(page.getByTestId("agent-center-page")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("agent-role-row")).toHaveCount(13, { timeout: 60_000 })
      await expect(page.getByTestId("agent-center-quality")).toBeVisible()
      const consoleTab = page.getByRole("tab", { name: "配置与控制台" })
      await expect(consoleTab).toBeVisible()
      await expect(page.getByRole("button", { name: /编辑 .* 配置/ }).first()).toBeVisible()
      await page.screenshot({
        path: evidencePath("p8-management-01-agents-admin.png"),
        fullPage: true,
      })

      await page
        .getByRole("button", { name: /编辑 .* 配置/ })
        .first()
        .click()
      await expect(page.getByRole("heading", { name: "Agent 运行时控制台" })).toBeVisible({
        timeout: 60_000,
      })
      await expect(page.getByRole("tab", { name: "配置", exact: true })).toBeVisible()
    })

    await test.step("an early-phase console error is caught at the account switch", async () => {
      // Regression guard for the monitor hand-off: stopping the monitor before
      // asserting the outgoing phase used to drop every early error. Inject one
      // and require switchAccount to reject before logging out.
      await page.evaluate((message) => console.error(message), INJECTED_CONSOLE_ERROR)
      await expect.poll(() => monitor.errors.join("\n")).toContain(INJECTED_CONSOLE_ERROR)

      let rejected: unknown = null
      try {
        monitor = await switchAccount(page, monitor, normal.username, normal.password)
      } catch (caught) {
        rejected = caught
      }
      expect(rejected, "switchAccount must assert the outgoing phase before logout").not.toBeNull()
      expect(monitor.errors.join("\n")).toContain(INJECTED_CONSOLE_ERROR)
      // The rejected switch aborted before logout, so the session is still the
      // admin one; restart the monitor and let the next step switch for real.
      await expect(page.getByRole("button", { name: "打开用户菜单" })).toBeVisible()
      monitor = captureConsoleErrors(page)
    })

    await test.step("ordinary user Agent centre: read-only catalogue, no write controls, 403 write", async () => {
      monitor = await switchAccount(page, monitor, normal.username, normal.password)
      await page.goto("/agents")
      await expect(page.getByTestId("agent-center-page")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("readonly-banner")).toBeVisible()
      await expect(page.getByTestId("agent-role-row")).toHaveCount(13, { timeout: 60_000 })
      await expect(page.getByRole("tab", { name: "配置与控制台" })).toHaveCount(0)
      await expect(page.getByRole("button", { name: /编辑 .* 配置/ })).toHaveCount(0)
      await page.screenshot({
        path: evidencePath("p8-management-02-agents-readonly.png"),
        fullPage: true,
      })

      const normalLogin = await request.post(`${API_URL}/api/auth/login`, {
        data: { username: normal.username, password: normal.password },
      })
      const normalToken = (await readData<{ token: string }>(normalLogin, 200, "normal login"))
        .token
      const catalog = await request.get(`${API_URL}/api/agent-center/catalog`, {
        headers: authHeaders(normalToken),
      })
      expect(catalog.status(), "ordinary user reads the catalogue").toBe(200)
      const write = await request.patch(`${API_URL}/api/agents/profiles/${admin.id}`, {
        headers: authHeaders(normalToken),
        data: { enabled: false },
      })
      expect(write.status(), "ordinary user cannot write an agent profile").toBe(403)
      const writeBody = (await write.json()) as { error?: { code?: string } }
      expect(writeBody.error?.code).toBe("forbidden")
    })

    await test.step("admin audit viewer filters real records by action and actor", async () => {
      monitor = await switchAccount(page, monitor, ADMIN_USERNAME, ADMIN_PASSWORD)
      await page.goto("/settings")
      await page.getByRole("tab", { name: "审计日志" }).click()
      await expect(page.getByRole("heading", { name: "审计日志" })).toBeVisible({ timeout: 60_000 })

      const actionInput = page.getByLabel("操作", { exact: true })
      await page.getByLabel("操作者 ID").fill(String(admin.id))
      await actionInput.fill("version.created")
      await page.getByRole("button", { name: "筛选", exact: true }).click()
      await expect(page.getByRole("cell", { name: "version.created" }).first()).toBeVisible({
        timeout: 60_000,
      })
      await expect(page.getByText(`version #${auditVersionId}`).first()).toBeVisible()
      await page.screenshot({
        path: evidencePath("p8-management-03-audit-filter.png"),
        fullPage: true,
      })

      // The actor filter must actually filter: the freshly created normal user
      // never created a version, so the same action filtered by their id keeps
      // the explicit empty state (a decorative actor filter would still show the
      // admin's record).
      await page.getByLabel("操作者 ID").fill(String(normal.id))
      await page.getByRole("button", { name: "筛选", exact: true }).click()
      await expect(page.getByText("没有符合条件的审计记录")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByText(`version #${auditVersionId}`)).toHaveCount(0)

      // Restore the admin actor so the detail flow opens the target record.
      await page.getByLabel("操作者 ID").fill(String(admin.id))
      await page.getByRole("button", { name: "筛选", exact: true }).click()
      await expect(page.getByText(`version #${auditVersionId}`).first()).toBeVisible({
        timeout: 60_000,
      })

      // Opening one row shows the read-only metadata detail.
      await page
        .getByRole("button", { name: /查看记录/ })
        .first()
        .click()
      await expect(page.getByRole("heading", { name: "记录详情" })).toBeVisible({
        timeout: 60_000,
      })

      // A filter with no match keeps the explicit empty state instead of an error.
      await actionInput.fill("p8.e2e.no_such_action")
      await page.getByRole("button", { name: "筛选", exact: true }).click()
      await expect(page.getByText("没有符合条件的审计记录")).toBeVisible({ timeout: 60_000 })
      await page.getByRole("button", { name: "重置", exact: true }).click()
      await expect(page.getByRole("cell", { name: "version.created" }).first()).toBeVisible({
        timeout: 60_000,
      })
    })

    await test.step("admin basic config is read-only and the GC path asks for confirmation", async () => {
      await page.getByRole("tab", { name: "基础配置" }).click()
      const basicHeading = page.getByRole("heading", { name: "基础配置", exact: true })
      await expect(basicHeading).toBeVisible({ timeout: 60_000 })
      const panel = page.locator("section").filter({ has: basicHeading }).first()
      await expect(panel).toContainText("配置只读")
      await expect(panel).toContainText("运行环境")
      await expect(panel).toContainText("限流与保留")
      await expect(panel).toContainText("SLO 阈值")
      await expect(panel).toContainText("存储与配置")
      await expect(panel).toContainText("证据 GC")
      await expect(panel.locator("input, select, textarea")).toHaveCount(0)

      await panel.getByRole("button", { name: "GC dry-run 预览" }).click()
      await expect(panel).toContainText("dry-run（未删除）", { timeout: 60_000 })
      await expect(panel).toContainText("候选")
      const execute = panel.getByRole("button", { name: "执行 GC", exact: true })
      if (await execute.isEnabled().catch(() => false)) {
        await execute.click()
        const dialog = panel.getByRole("alertdialog")
        await expect(dialog).toBeVisible()
        await expect(dialog).toContainText("确认执行 GC")
        await page.screenshot({
          path: evidencePath("p8-management-04-gc-confirm.png"),
          fullPage: true,
        })
        await dialog.getByRole("button", { name: "取消", exact: true }).click()
        await expect(dialog).toHaveCount(0)
      } else {
        await expect(execute).toBeDisabled()
      }
      await page.screenshot({
        path: evidencePath("p8-management-05-basic-config.png"),
        fullPage: true,
      })
    })

    await test.step("ordinary user settings keep only the theme panel", async () => {
      monitor = await switchAccount(page, monitor, normal.username, normal.password)
      await page.goto("/settings")
      await expect(page.getByRole("heading", { name: "外观" })).toBeVisible({ timeout: 60_000 })
      await expect(page.getByRole("tab", { name: "审计日志" })).toHaveCount(0)
      await expect(page.getByRole("tab", { name: "基础配置" })).toHaveCount(0)
      await expect(page.getByRole("tab", { name: "用户" })).toHaveCount(0)
    })

    await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 管理面")
    await expectNoSecretInPage(page, normal.password, "P8 管理面")
    await expectNoConsoleErrors(monitor, page, "P8 管理面")
  } finally {
    monitor.stop()
    try {
      if (auditVersionId !== "") {
        runPsql(`DELETE FROM version WHERE id = ${auditVersionId}::bigint;`)
      }
    } catch {
      // Raw Playwright runs without the e2e env skip the DB cleanup.
    }
  }
})

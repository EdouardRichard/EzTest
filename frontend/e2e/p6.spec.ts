import fs from "node:fs"
import path from "node:path"

import { expect, type Page, test } from "@playwright/test"

/**
 * P6 browser E2E against the real API/DB cloned from the P4 acceptance dataset.
 * No fake LLM or fake executor is used: every page reads the real read models,
 * and the prompt admin flow was exercised through the real model in acceptance.
 *
 * The version id is discovered through the real API after an admin login instead
 * of hardcoding a dataset-specific snowflake; `EZTEST_E2E_P6_VERSION_ID` stays
 * available as an explicit override. The admin and normal-user credentials are
 * configurable too (defaults keep the documented acceptance accounts).
 */
const API_BASE_URL = (process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:13333").replace(
  /\/+$/,
  "",
)
const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD?.trim() || "admin123"
const USER_USERNAME = process.env.EZTEST_E2E_P6_USER_USERNAME?.trim() || "p6user"
const USER_PASSWORD = process.env.EZTEST_E2E_P6_USER_PASSWORD?.trim() || "p6user12345"
const SCREENSHOT_DIR =
  process.env.EZTEST_E2E_P6_SCREENSHOT_DIR?.trim() ||
  path.resolve(process.cwd(), "..", ".tmp", "p6-acceptance", "screenshots")

let VERSION_ID = process.env.EZTEST_E2E_P6_VERSION_ID?.trim() || ""

type P6VersionSummary = {
  id: string
  title: string
  status: "initial" | "running" | "completed"
  updated_at: string
}

type P6VersionDetail = P6VersionSummary & {
  bindings: unknown[]
}

/** Logs in through the real API and returns the bearer token (never printed). */
async function apiLogin(): Promise<string> {
  const response = await fetch(`${API_BASE_URL}/api/auth/login`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ username: ADMIN_USERNAME, password: ADMIN_PASSWORD }),
  })
  if (!response.ok) {
    throw new Error(`P6 seed discovery: API login failed (HTTP ${response.status})`)
  }
  const payload = (await response.json()) as { data?: { token?: string } }
  const token = payload.data?.token
  if (typeof token !== "string" || token.length === 0) {
    throw new Error("P6 seed discovery: login response did not include a token")
  }
  return token
}

async function apiGet<T>(token: string, pathname: string): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${pathname}`, {
    headers: { Accept: "application/json", Authorization: `Bearer ${token}` },
  })
  if (!response.ok) {
    throw new Error(`P6 seed discovery: GET ${pathname} failed (HTTP ${response.status})`)
  }
  const payload = (await response.json()) as { data: T }
  return payload.data
}

/**
 * Resolves the version used by the chain/search/todo assertions. Prefers the
 * most recently updated version with at least one SUT binding; the P4 acceptance
 * dataset always has one, and a missing version fails with an actionable error
 * instead of silently skipping the scenario.
 */
test.beforeAll(async () => {
  if (VERSION_ID.length > 0) return
  const token = await apiLogin()
  const versions = await apiGet<P6VersionSummary[]>(token, "/api/versions?limit=100")
  if (versions.length === 0) {
    throw new Error(
      "P6 seed discovery: no versions returned by GET /api/versions; run scripts/p6-acceptance.ps1 first",
    )
  }
  const ordered = [...versions].sort((left, right) =>
    right.updated_at.localeCompare(left.updated_at),
  )
  for (const version of ordered) {
    const detail = await apiGet<P6VersionDetail>(
      token,
      `/api/versions/${encodeURIComponent(version.id)}`,
    )
    if (detail.bindings.length > 0) {
      VERSION_ID = version.id
      return
    }
  }
  VERSION_ID = ordered[0].id
})

let shot = 0
function nextShot(name: string): string {
  shot += 1
  return path.join(SCREENSHOT_DIR, `p6-${String(shot).padStart(2, "0")}-${name}.png`)
}

async function snap(page: Page, name: string): Promise<void> {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
  await page.screenshot({ path: nextShot(name), fullPage: true })
}

async function loginUI(page: Page, username: string, password: string): Promise<void> {
  await page.goto("/login")
  await page.getByLabel("用户名").fill(username)
  await page.getByLabel("密码").fill(password)
  await page.getByRole("button", { name: "登录" }).click()
  await page.waitForURL((url) => !url.pathname.startsWith("/login"), { timeout: 30_000 })
}

test.describe.configure({ mode: "serial" })

test("admin sees quality, agent centre, global centres, chain, search and todos", async ({
  page,
}) => {
  await loginUI(page, ADMIN_USERNAME, ADMIN_PASSWORD)

  await page.goto("/quality")
  await expect(page.getByTestId("quality-page")).toBeVisible()
  await expect(page.getByTestId("quality-eval-board")).toBeVisible()
  await snap(page, "quality-admin")

  await page.goto("/agents")
  await expect(page.getByTestId("agent-center-page")).toBeVisible()
  await expect(page.getByTestId("agent-center-catalog")).toBeVisible()
  await page.getByRole("tab", { name: "配置" }).click()
  await expect(page.getByTestId("agent-center-config")).toBeVisible()
  await snap(page, "agents-admin")

  await page.goto("/issues")
  await expect(page.getByTestId("global-issues-page")).toBeVisible()
  await snap(page, "issues-admin")

  await page.goto("/reports")
  await expect(page.getByTestId("global-reports-page")).toBeVisible()
  await snap(page, "reports-admin")

  await page.goto(`/versions/${VERSION_ID}?tab=chain`)
  await expect(page.getByRole("tab", { name: "探索链路" })).toHaveAttribute("aria-selected", "true")
  await expect(page.getByTestId("chain-tab")).toBeVisible()
  await snap(page, "chain-admin")

  await page.keyboard.press("Control+K")
  await expect(page.getByTestId("global-search-dialog")).toBeVisible()
  await page.getByTestId("global-search-input").fill("P4")
  await expect(page.locator('[data-testid^="global-search-item-"]').first()).toBeVisible({
    timeout: 20_000,
  })
  await snap(page, "search-results")
  await page.keyboard.press("Escape")

  await page.getByTestId("todo-drawer-trigger").click()
  await expect(page.getByRole("dialog", { name: "待办" })).toBeVisible()
  await snap(page, "todos-admin")
})

test("normal user gets read-only quality/agent views without prompts", async ({ page }) => {
  await loginUI(page, USER_USERNAME, USER_PASSWORD)

  await page.goto("/quality")
  await expect(page.getByTestId("quality-page")).toBeVisible()
  await expect(page.getByTestId("quality-eval-admin-only")).toBeVisible()
  await snap(page, "quality-user")

  await page.goto("/agents")
  await expect(page.getByTestId("agent-center-page")).toBeVisible()
  await expect(page.getByRole("tab", { name: "配置" })).toHaveCount(0)
  await expect(page.getByText("[profile:clarification_host]")).toHaveCount(0)
  await snap(page, "agents-user")
})

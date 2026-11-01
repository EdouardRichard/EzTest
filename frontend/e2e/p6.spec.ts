import fs from "node:fs"
import path from "node:path"

import { expect, type Page, test } from "@playwright/test"

/**
 * P6 browser E2E against the real API/DB cloned from the P4 acceptance dataset.
 * No fake LLM or fake executor is used: every page reads the real read models,
 * and the prompt admin flow was exercised through the real model in acceptance.
 */
const API = process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:14345"
const VERSION_ID = process.env.EZTEST_E2E_P6_VERSION_ID?.trim() || "375072010541207552"
const SCREENSHOT_DIR =
  process.env.EZTEST_E2E_P6_SCREENSHOT_DIR?.trim() ||
  path.resolve(process.cwd(), "..", ".tmp", "p6-acceptance", "screenshots")

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

test("admin sees quality, agent centre, global centres, chain, search and todos", async ({ page }) => {
  await loginUI(page, "admin", "admin123")

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
  await loginUI(page, "p6user", "p6user12345")

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

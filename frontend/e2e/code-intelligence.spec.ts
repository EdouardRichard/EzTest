import fs from "node:fs"
import path from "node:path"

import { type APIRequestContext, expect, type Page, test } from "@playwright/test"

import { ADMIN_PASSWORD, ADMIN_USERNAME } from "./credentials"

/**
 * P5 browser E2E (real API/DB/executor/fixture, real LLM + real Chrome).
 *
 * `scripts/code-intelligence-e2e.ps1` boots an isolated stack and prepares:
 *   - a code repository with real T0 index A/B and a real T1 analysis A..B
 *   - a code anchor (requirement) and page/API proposals from code facts
 *
 * Phase A (admin) walks the Code tab, the quality page and the resource page
 * proposals. Phase B (normal user) asserts the admin-only proposal decisions
 * are hidden.
 *
 * No fake LLM and no fake executor are ever used.
 */

const API = process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:14345"
const VERSION_ID = process.env.EZTEST_E2E_P5_VERSION_ID?.trim() || ""
const REPO_ID = process.env.EZTEST_E2E_P5_REPO_ID?.trim() || ""
const ANCHOR_ID = process.env.EZTEST_E2E_P5_ANCHOR_ID?.trim() || ""
const HEAD_COMMIT = process.env.EZTEST_E2E_P5_HEAD_COMMIT?.trim() || ""
const BASELINE_COMMIT = process.env.EZTEST_E2E_P5_BASELINE_COMMIT?.trim() || ""
const TEST_TIMEOUT_MS = Number(process.env.EZTEST_E2E_P5_TIMEOUT_MS || "900000")
const SCREENSHOT_DIR =
  process.env.EZTEST_E2E_P5_SCREENSHOT_DIR?.trim() ||
  path.resolve(process.cwd(), "..", ".tmp", "p5-acceptance", "screenshots")

type Fact = { id: string; fact_key: string }
type Proposal = { id: string; kind: string; status: string }

let adminToken = ""
let userToken = ""
let screenshotIndex = 0

async function api<T>(
  request: APIRequestContext,
  method: "GET" | "POST" | "PATCH" | "PUT" | "DELETE",
  pathName: string,
  body?: unknown,
  expected = 200,
  token = adminToken,
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

async function loginApi(
  request: APIRequestContext,
  username: string,
  password: string,
): Promise<string> {
  const response = await api<{ data: { token: string } }>(
    request,
    "POST",
    "/api/auth/login",
    { username, password },
    200,
    "",
  )
  return response.data.token
}

function screenshotPath(name: string): string {
  screenshotIndex += 1
  return path.join(SCREENSHOT_DIR, `p5-${String(screenshotIndex).padStart(2, "0")}-${name}.png`)
}

async function loginUI(
  page: Page,
  username = ADMIN_USERNAME,
  password = ADMIN_PASSWORD,
): Promise<void> {
  await page.goto("/login")
  await page.fill("#username", username)
  await page.fill("#password", password)
  await page.getByRole("button", { name: "登录" }).click()
  await page.waitForURL("**/versions")
}

function trackPageErrors(page: Page): string[] {
  const pageErrors: string[] = []
  page.on("pageerror", (error) => pageErrors.push(String(error)))
  return pageErrors
}

function trackConsoleErrors(page: Page): string[] {
  const consoleErrors: string[] = []
  page.on("console", (message) => {
    if (message.type() !== "error") return
    const text = message.text()
    // The app intentionally probes latest analysis rows; the APIs answer
    // 404 for "not found yet" and the UI handles it. Those are not app errors.
    if (text.includes("Failed to load resource") && text.includes("404")) return
    consoleErrors.push(text)
  })
  return consoleErrors
}

test.beforeAll(async ({ request }) => {
  for (const [name, value] of Object.entries({
    EZTEST_E2E_P5_VERSION_ID: VERSION_ID,
    EZTEST_E2E_P5_REPO_ID: REPO_ID,
    EZTEST_E2E_P5_HEAD_COMMIT: HEAD_COMMIT,
  })) {
    if (value === "")
      throw new Error(`${name} is unset; run scripts/code-intelligence-e2e.ps1 (real stack)`)
  }
  adminToken = await loginApi(request, ADMIN_USERNAME, ADMIN_PASSWORD)
  userToken = await loginApi(request, "p5user", "p5-user-pass-123")
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
})

test("phase A: code tab, quality page, resource proposals", async ({ page, request }) => {
  test.setTimeout(TEST_TIMEOUT_MS)
  const pageErrors = trackPageErrors(page)
  const consoleErrors = trackConsoleErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await loginUI(page)

  // ----------------------------------------------------------- code tab --
  await page.goto(`/versions/${VERSION_ID}?tab=code`)
  const repoSection = page.locator('section[aria-label="代码仓库状态"]')
  await expect(repoSection).toBeVisible({ timeout: 60_000 })
  await expect(page.locator("#code-repo")).toHaveValue(REPO_ID)
  await expect(repoSection).toContainText(BASELINE_COMMIT.slice(0, 7))

  const changedSection = page.locator('section[aria-label="变更文件"]')
  await expect(changedSection).toBeVisible()
  await expect(changedSection).toContainText("src/api/refund.ts")
  await expect(changedSection).toContainText("src/pages/RefundPage.tsx")
  await expect
    .poll(() => changedSection.locator("li").count(), { timeout: 30_000 })
    .toBeGreaterThanOrEqual(6)

  await changedSection.getByRole("button", { name: /src\/api\/refund\.ts/ }).click()
  const diffDialog = page.getByRole("dialog")
  await expect(diffDialog).toBeVisible({ timeout: 30_000 })
  await expect(diffDialog).toContainText("src/api/refund.ts")
  await expect(diffDialog.locator('section[aria-label="文件 patch"]')).toBeVisible({
    timeout: 30_000,
  })
  await page.screenshot({ path: screenshotPath("code-tab-diff"), fullPage: true })
  await diffDialog.getByRole("button", { name: "关闭", exact: true }).click()

  const factsSection = page.locator('section[aria-label="代码事实"]')
  await expect(factsSection).toBeVisible()
  await expect(factsSection).toContainText(/refund/, { timeout: 30_000 })
  const anchorsSection = page.locator('section[aria-label="代码锚点"]')
  await expect(anchorsSection).toBeVisible()
  await expect(page.locator(`[aria-label="锚点 ${ANCHOR_ID}"]`)).toBeVisible({ timeout: 30_000 })

  const impactSection = page.locator('section[aria-label="影响候选"]')
  await expect(impactSection).toBeVisible()
  const impactCardCount = await impactSection.locator("li").count()
  test.info().annotations.push({
    type: "note",
    description: `impact panel candidate rows in UI: ${impactCardCount} (analysis-detail candidates are a known P5 API gap)`,
  })

  const proposalSection = page.locator('section[aria-label="页面/API 提议"]')
  await expect(proposalSection).toBeVisible()
  await proposalSection.getByRole("button", { name: "从代码生成提议" }).click()
  await expect(proposalSection.getByRole("status")).toContainText("本次生成", { timeout: 60_000 })
  await expect(proposalSection).toContainText("待确认", { timeout: 30_000 })
  await page.screenshot({ path: screenshotPath("code-tab"), fullPage: true })

  // Deep link: fact focus + anchor focus through the URL.
  const facts = (
    await api<{ data: Fact[] }>(
      request,
      "GET",
      `/api/code/repos/${REPO_ID}/facts?commit=${HEAD_COMMIT}&limit=100`,
    )
  ).data
  const focusFact = facts.find((fact) => fact.fact_key === "route:/orders") ?? facts[0]
  if (!focusFact) throw new Error("analysis produced no facts to deep link")
  await page.goto(
    `/versions/${VERSION_ID}?tab=code&repo=${REPO_ID}&analysis=${process.env.EZTEST_E2E_P5_ANALYSIS_ID ?? ""}&fact=${focusFact.id}&anchor=${ANCHOR_ID}`,
  )
  await expect(page.locator('section[aria-label="代码事实"]')).toBeVisible({ timeout: 60_000 })
  await expect(page.locator('section[aria-label="代码锚点"] [aria-current="true"]')).toBeVisible({
    timeout: 30_000,
  })
  await page.screenshot({ path: screenshotPath("code-tab-deeplink"), fullPage: true })

  // ----------------------------------------------------- quality page --
  await page.goto(`/quality?version=${VERSION_ID}`)
  await expect(page.getByTestId("quality-operational-metrics")).toBeVisible({ timeout: 60_000 })
  const qualityText = (await page.getByTestId("quality-operational-metrics").textContent()) ?? ""
  for (const label of ["需求闭环率", "用例确认率", "执行通过率"]) {
    expect(qualityText).toContain(label)
  }
  await page.screenshot({ path: screenshotPath("quality-page"), fullPage: true })

  // ----------------------------------------- resource page proposals --
  await page.goto("/resources?tab=pages")
  const proposalBlock = page.locator('section[aria-label="代码提议"]')
  await expect(proposalBlock).toBeVisible({ timeout: 60_000 })
  await proposalBlock.getByRole("button", { name: "展开代码提议" }).click()
  await expect(proposalBlock).toContainText("待确认", { timeout: 60_000 })
  const confirmButtons = proposalBlock.locator('[aria-label^="确认提议"]')
  await expect(confirmButtons.first()).toBeVisible({ timeout: 60_000 })
  const proposedBefore = (
    await api<{ data: Proposal[] }>(
      request,
      "GET",
      `/api/versions/${VERSION_ID}/code/page-api-proposals`,
    )
  ).data
  const proposedCountBefore = proposedBefore.filter(
    (proposal) => proposal.status === "proposed",
  ).length
  if (proposedCountBefore < 1) throw new Error("no proposed resource to confirm in the UI")
  await confirmButtons.first().click()
  await expect
    .poll(
      async () => {
        const list = (
          await api<{ data: Proposal[] }>(
            request,
            "GET",
            `/api/versions/${VERSION_ID}/code/page-api-proposals`,
          )
        ).data
        return list.filter((proposal) => proposal.status === "proposed").length
      },
      { timeout: 30_000 },
    )
    .toBe(proposedCountBefore - 1)
  await page.screenshot({ path: screenshotPath("resources-proposals"), fullPage: true })

  expect(pageErrors, `page errors: ${pageErrors.join(" | ")}`).toEqual([])
  expect(consoleErrors, `console errors: ${consoleErrors.join(" | ")}`).toEqual([])
})

test("phase B: non-admin permissions hide proposal decisions", async ({ page, request }) => {
  test.setTimeout(TEST_TIMEOUT_MS)
  const pageErrors = trackPageErrors(page)
  const consoleErrors = trackConsoleErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await loginUI(page, "p5user", "p5-user-pass-123")

  await page.goto("/resources?tab=pages")
  const proposalBlock = page.locator('section[aria-label="代码提议"]')
  await expect(proposalBlock).toBeVisible({ timeout: 60_000 })
  await proposalBlock.getByRole("button", { name: "展开代码提议" }).click()
  await expect(proposalBlock).toContainText("待确认", { timeout: 60_000 })
  await expect(proposalBlock.locator('[aria-label^="确认提议"]')).toHaveCount(0)
  await expect(proposalBlock.locator('[aria-label^="驳回提议"]')).toHaveCount(0)

  const proposals = (
    await api<{ data: Proposal[] }>(
      request,
      "GET",
      `/api/versions/${VERSION_ID}/code/page-api-proposals`,
    )
  ).data
  const remaining = proposals.find((proposal) => proposal.status === "proposed")
  if (remaining) {
    const pathName =
      remaining.kind === "page"
        ? `/api/resources/pages/${remaining.id}/confirm`
        : `/api/resources/apis/${remaining.id}/confirm`
    const forbidden = await request.fetch(API + pathName, {
      method: "POST",
      headers: { Authorization: `Bearer ${userToken}` },
    })
    expect(forbidden.status()).toBe(403)
  }
  await page.screenshot({ path: screenshotPath("resources-non-admin"), fullPage: true })

  expect(pageErrors, `page errors: ${pageErrors.join(" | ")}`).toEqual([])
  expect(consoleErrors, `console errors: ${consoleErrors.join(" | ")}`).toEqual([])
})

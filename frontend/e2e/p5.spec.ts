import fs from "node:fs"
import path from "node:path"

import { type APIRequestContext, expect, type Page, test } from "@playwright/test"

/**
 * P5 browser E2E (real API/DB/executor/fixture, real LLM + real Chrome).
 *
 * `scripts/p5-e2e.ps1` boots an isolated stack and prepares:
 *   - a code repository with real T0 index A/B and a real T1 analysis A..B
 *   - a code anchor (requirement) and page/API proposals from code facts
 *   - one visual case with an active visual assertion; the first real run is
 *     inconclusive(missing_visual_baseline) with a stored visual_actual
 *
 * Phase A (admin) walks the Code tab, the execution `?view=visual` sub-view
 * (approve baseline -> retest -> passed diff + actual/diff viewer), the quality
 * page and the resource page proposals. Phase B (normal user) asserts the
 * admin-only actions are hidden (baseline approval and proposal decisions).
 *
 * No fake LLM and no fake executor are ever used.
 */

const API = process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:14345"
const VERSION_ID = process.env.EZTEST_E2E_P5_VERSION_ID?.trim() || ""
const REPO_ID = process.env.EZTEST_E2E_P5_REPO_ID?.trim() || ""
const REQUIREMENT_ID = process.env.EZTEST_E2E_P5_REQUIREMENT_ID?.trim() || ""
const VISUAL_CASE_VERSION_ID = process.env.EZTEST_E2E_P5_VISUAL_CASE_VERSION_ID?.trim() || ""
const VISUAL_ASSERTION_ID = process.env.EZTEST_E2E_P5_VISUAL_ASSERTION_ID?.trim() || ""
const VISUAL_CASE_RUN_ID = process.env.EZTEST_E2E_P5_VISUAL_CASE_RUN_ID?.trim() || ""
const WAVE1_ID = process.env.EZTEST_E2E_P5_WAVE1_ID?.trim() || ""
const ANCHOR_ID = process.env.EZTEST_E2E_P5_ANCHOR_ID?.trim() || ""
const HEAD_COMMIT = process.env.EZTEST_E2E_P5_HEAD_COMMIT?.trim() || ""
const BASELINE_COMMIT = process.env.EZTEST_E2E_P5_BASELINE_COMMIT?.trim() || ""
const TEST_TIMEOUT_MS = Number(process.env.EZTEST_E2E_P5_TIMEOUT_MS || "900000")
const SCREENSHOT_DIR =
  process.env.EZTEST_E2E_P5_SCREENSHOT_DIR?.trim() ||
  path.resolve(process.cwd(), "..", ".tmp", "p5-acceptance", "screenshots")

type Wave = { id: string; status: string }
type CaseRun = { id: string; case_version_id: string; status: string }
type VisualDiff = {
  id: string
  visual_assertion_id: string
  case_run_id: string
  classification: string
  reason: string
  diff_ratio: number
  actual_evidence_id: string
  diff_evidence_id: string | null
}
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

async function sleep(milliseconds: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, milliseconds))
}

async function waitFor<T>(
  probe: () => Promise<T | null | undefined>,
  timeoutMs: number,
  label: string,
  intervalMs = 2_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown = null
  while (Date.now() < deadline) {
    try {
      const value = await probe()
      if (value !== null && value !== undefined && value !== false) return value
      lastError = null
    } catch (error) {
      lastError = error
    }
    await sleep(intervalMs)
  }
  const detail = lastError === null ? "" : ` (last error: ${String(lastError)})`
  throw new Error(`timeout after ${timeoutMs}ms waiting for ${label}${detail}`)
}

function screenshotPath(name: string): string {
  screenshotIndex += 1
  return path.join(SCREENSHOT_DIR, `p5-${String(screenshotIndex).padStart(2, "0")}-${name}.png`)
}

async function loginUI(page: Page, username = "admin", password = "admin123"): Promise<void> {
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
    // The app intentionally probes latest analysis/visual rows; the APIs answer
    // 404 for "not found yet" and the UI handles it. Those are not app errors.
    if (text.includes("Failed to load resource") && text.includes("404")) return
    consoleErrors.push(text)
  })
  return consoleErrors
}

async function getWaves(request: APIRequestContext): Promise<Wave[]> {
  return (
    await api<{ data: Wave[] }>(request, "GET", `/api/versions/${VERSION_ID}/waves?limit=100`)
  ).data
}

async function waitWaveTerminal(request: APIRequestContext, waveID: string): Promise<Wave> {
  return waitFor(
    async () => {
      const waves = await getWaves(request)
      const wave = waves.find((item) => item.id === waveID)
      return wave && ["done", "failed", "cancelled"].includes(wave.status) ? wave : null
    },
    TEST_TIMEOUT_MS,
    `wave ${waveID} terminal`,
  )
}

async function reopenRequirement(request: APIRequestContext): Promise<void> {
  const response = await request.fetch(
    `${API}/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}/reopen`,
    {
      method: "POST",
      headers: { Authorization: `Bearer ${adminToken}` },
      data: { reason: "P5 browser E2E retest" },
    },
  )
  if (response.status() === 200 || response.status() === 409) return
  throw new Error(`requirement reopen -> ${response.status()}: ${await response.text()}`)
}

async function getVisualDiffsForRun(
  request: APIRequestContext,
  caseRunID: string,
): Promise<VisualDiff[]> {
  return (
    await api<{ data: VisualDiff[] }>(
      request,
      "GET",
      `/api/versions/${VERSION_ID}/visual/diffs?caseRunId=${caseRunID}&limit=100`,
    )
  ).data
}

test.beforeAll(async ({ request }) => {
  for (const [name, value] of Object.entries({
    EZTEST_E2E_P5_VERSION_ID: VERSION_ID,
    EZTEST_E2E_P5_REPO_ID: REPO_ID,
    EZTEST_E2E_P5_VISUAL_ASSERTION_ID: VISUAL_ASSERTION_ID,
    EZTEST_E2E_P5_VISUAL_CASE_RUN_ID: VISUAL_CASE_RUN_ID,
    EZTEST_E2E_P5_HEAD_COMMIT: HEAD_COMMIT,
  })) {
    if (value === "") throw new Error(`${name} is unset; run scripts/p5-e2e.ps1 (real stack)`)
  }
  adminToken = await loginApi(request, "admin", "admin123")
  userToken = await loginApi(request, "p5user", "p5-user-pass-123")
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
})

test("phase A: code tab, visual approve/retest/diff, quality page, resource proposals", async ({
  page,
  request,
}) => {
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

  // ------------------------------------------------- execution visual --
  await reopenRequirement(request)
  await page.goto(`/versions/${VERSION_ID}?tab=execution&view=visual`)
  await expect(page.getByTestId("visual-view")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId("visual-overview-strip")).toBeVisible()
  await expect(page.getByTestId("visual-assertion-list")).toBeVisible({ timeout: 60_000 })
  const visualCard = page.getByTestId(`visual-case-run-${VISUAL_CASE_RUN_ID}`)
  await expect(visualCard).toBeVisible({ timeout: 60_000 })
  await expect(visualCard).toContainText("缺失基线")
  await expect(visualCard.getByRole("button", { name: "批准基线" })).toBeVisible()

  await visualCard.getByRole("button", { name: "查看差异" }).first().click()
  const diffViewer = page.getByRole("dialog")
  await expect(diffViewer).toBeVisible({ timeout: 30_000 })
  await expect(diffViewer.getByTestId("visual-actual-image")).toBeVisible({ timeout: 60_000 })
  await expect(diffViewer).toContainText("未生成差异图")
  await page.screenshot({ path: screenshotPath("visual-missing-baseline"), fullPage: true })
  await diffViewer.getByRole("button", { name: "关闭", exact: true }).click()

  // Admin approves the first-run actual as the baseline.
  await visualCard.getByRole("button", { name: "批准基线" }).click()
  const approvalDialog = page.getByRole("dialog", { name: "批准视觉基线" })
  await expect(approvalDialog).toBeVisible()
  await expect(approvalDialog.getByRole("radio", { name: /证据 #/ })).toBeChecked()
  await page.screenshot({ path: screenshotPath("visual-approve-dialog") })
  await approvalDialog.getByRole("button", { name: "确认批准", exact: true }).click()
  await expect(visualCard.getByText("基线已批准，复测后生效")).toBeVisible({ timeout: 30_000 })
  await expect(visualCard.getByText("基线已批准", { exact: true })).toBeVisible({ timeout: 30_000 })
  await page.screenshot({ path: screenshotPath("visual-baseline-approved"), fullPage: true })

  // Retest through the UI, then wait for the real wave and open the diff viewer.
  const beforeWaves = await getWaves(request)
  await visualCard.getByRole("button", { name: "复测" }).click()
  await expect(page.getByText("已创建复测意图")).toBeVisible({ timeout: 30_000 })
  const wave2 = await waitFor(
    async () => {
      const waves = await getWaves(request)
      return waves.find((wave) => !beforeWaves.some((item) => item.id === wave.id)) ?? null
    },
    120_000,
    "retest wave created",
  )
  await waitWaveTerminal(request, wave2.id)
  const wave2Runs = (
    await api<{ data: CaseRun[] }>(
      request,
      "GET",
      `/api/versions/${VERSION_ID}/waves/${wave2.id}/case-runs?limit=100`,
    )
  ).data
  const wave2VisualRun = wave2Runs.find((run) => run.case_version_id === VISUAL_CASE_VERSION_ID)
  if (!wave2VisualRun) throw new Error("retest wave did not contain the visual case run")
  const wave2Diffs = await getVisualDiffsForRun(request, wave2VisualRun.id)
  const passedDiff = wave2Diffs.find((diff) => diff.classification === "passed")
  if (!passedDiff) throw new Error("retest produced no passed visual diff")

  await page.reload()
  await expect(page.getByTestId("visual-view")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByLabel("波次").locator(`option[value="${wave2.id}"]`)).toHaveCount(1, {
    timeout: 60_000,
  })
  await page.getByLabel("波次").selectOption(wave2.id)
  const retestCard = page.getByTestId(`visual-case-run-${wave2VisualRun.id}`)
  await expect(retestCard).toBeVisible({ timeout: 60_000 })
  await expect(retestCard).toContainText("通过")
  await expect(retestCard.getByTestId(`visual-diff-${passedDiff.id}`)).toBeVisible()
  await retestCard.getByRole("button", { name: "查看差异" }).first().click()
  const passedViewer = page.getByRole("dialog")
  await expect(passedViewer.getByTestId("visual-actual-image")).toBeVisible({ timeout: 60_000 })
  await expect(passedViewer.getByTestId("visual-diff-image")).toBeVisible({ timeout: 60_000 })
  await page.screenshot({ path: screenshotPath("visual-passed-diff"), fullPage: true })
  await passedViewer.getByRole("button", { name: "关闭", exact: true }).click()

  // ----------------------------------------------------- quality page --
  await page.goto(`/quality?version=${VERSION_ID}`)
  await expect(page.getByTestId("visual-quality-page")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId("quality-metrics-panel")).toBeVisible({ timeout: 60_000 })
  const qualityText = await page.getByTestId("quality-metrics-panel").innerText()
  for (const label of ["断言总数", "已批准基线", "基线覆盖率", "通过率", "未定率", "视觉比对"]) {
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

test("phase B: non-admin permissions hide baseline approval and proposal decisions", async ({
  page,
  request,
}) => {
  test.setTimeout(TEST_TIMEOUT_MS)
  const pageErrors = trackPageErrors(page)
  const consoleErrors = trackConsoleErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await loginUI(page, "p5user", "p5-user-pass-123")

  await page.goto(`/versions/${VERSION_ID}?tab=execution&view=visual`)
  await expect(page.getByTestId("visual-view")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByLabel("波次").locator(`option[value="${WAVE1_ID}"]`)).toHaveCount(1, {
    timeout: 60_000,
  })
  await page.getByLabel("波次").selectOption(WAVE1_ID)
  const visualCard = page.getByTestId(`visual-case-run-${VISUAL_CASE_RUN_ID}`)
  await expect(visualCard).toBeVisible({ timeout: 60_000 })
  await expect(visualCard.getByRole("button", { name: "批准基线" })).toHaveCount(0)
  await page.screenshot({ path: screenshotPath("visual-non-admin"), fullPage: true })

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

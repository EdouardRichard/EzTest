import fs from "node:fs"
import path from "node:path"

import { type APIRequestContext, expect, test } from "@playwright/test"

/**
 * P4 delivery-workspace browser E2E (real LLM; spec §12 S10 / scripts/p4-e2e.ps1).
 *
 * `scripts/p4-e2e.ps1` boots the isolated stack, prepares one version with a
 * passing case, a failing case (auto issue) and a 2FA takeover case, waits until
 * the human request is open and the failing issue exists, and keeps `version.close`
 * blocked with a not-yet-available SQL work item so phase A can exercise the
 * running-version views (including report stale/regenerate) deterministically.
 * It then removes the blocker, waits for the real Steward to complete the
 * version and runs phase B for the completed/reopen views.
 *
 * No fake LLM is ever configured.
 */

const API = process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:14344"
const VERSION_ID = process.env.EZTEST_E2E_P4_VERSION_ID?.trim() || ""
const CASE_C_VERSION_ID = process.env.EZTEST_E2E_P4_CASE_C_VERSION_ID?.trim() || ""
const TEST_TIMEOUT_MS = Number(process.env.EZTEST_E2E_P4_TIMEOUT_MS || "1200000")
const SCREENSHOT_DIR =
  process.env.EZTEST_E2E_SCREENSHOT_DIR?.trim() ||
  path.resolve(process.cwd(), "..", ".tmp", "p4-acceptance", "screenshots")

type VersionView = {
  id: string
  status: "initial" | "running" | "completed"
  closed_at?: string | null
  close_reason?: string | null
}
type Wave = { id: string; status: string; trigger?: string }
type ExecutionOverview = { active_wave: Wave | null }
type IssueView = {
  id: string
  title: string
  status: string
  refs: Array<{ kind: string; ref_id: string }>
  reopen_count: number
}
type ReportView = {
  id: string
  kind: string
  status: string
  wave_id?: string | null
  current_revision?: { revision: number; status: string; as_of: string } | null
}
type ReportDetailView = ReportView & {
  revisions: Array<{ revision: number; status: string; render_status: string; as_of: string }>
}
type HumanRequest = { id: string; kind: string; status: string; case_run_id?: string | null }

let token = ""
let versionID = ""
let firstWaveID = ""
let failingIssueID = ""
let screenshotIndex = 0

async function api<T>(
  request: APIRequestContext,
  method: "GET" | "POST" | "PATCH" | "DELETE",
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

async function login(request: APIRequestContext): Promise<string> {
  const response = await api<{ data: { token: string } }>(request, "POST", "/api/auth/login", {
    username: "admin",
    password: "admin123",
  })
  return response.data.token
}

async function sleep(milliseconds: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, milliseconds))
}

/** Polls `probe` until it returns a truthy value or the timeout expires. */
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

async function getVersion(request: APIRequestContext): Promise<VersionView> {
  return (await api<{ data: VersionView }>(request, "GET", `/api/versions/${versionID}`)).data
}

async function getOverview(request: APIRequestContext): Promise<ExecutionOverview> {
  return (
    await api<{ data: ExecutionOverview }>(
      request,
      "GET",
      `/api/versions/${versionID}/execution/overview`,
    )
  ).data
}

async function getWaves(request: APIRequestContext): Promise<Wave[]> {
  return (await api<{ data: Wave[] }>(request, "GET", `/api/versions/${versionID}/waves?limit=100`))
    .data
}

async function getIssues(request: APIRequestContext): Promise<IssueView[]> {
  return (
    await api<{ data: IssueView[] }>(request, "GET", `/api/versions/${versionID}/issues?limit=100`)
  ).data
}

async function getReports(request: APIRequestContext, kind?: string): Promise<ReportView[]> {
  const suffix = kind ? `&kind=${kind}` : ""
  return (
    await api<{ data: ReportView[] }>(
      request,
      "GET",
      `/api/versions/${versionID}/reports?limit=100${suffix}`,
    )
  ).data
}

async function getReportDetail(
  request: APIRequestContext,
  reportID: string,
): Promise<ReportDetailView> {
  return (
    await api<{ data: ReportDetailView }>(
      request,
      "GET",
      `/api/versions/${versionID}/reports/${reportID}`,
    )
  ).data
}

async function getOpenHumanRequests(request: APIRequestContext): Promise<HumanRequest[]> {
  return (
    await api<{ data: HumanRequest[] }>(
      request,
      "GET",
      `/api/versions/${versionID}/human-requests?status=open&limit=10`,
    )
  ).data
}

function screenshotPath(name: string): string {
  screenshotIndex += 1
  return path.join(SCREENSHOT_DIR, `${String(screenshotIndex).padStart(2, "0")}-${name}.png`)
}

async function expectNoHorizontalOverflow(page: import("@playwright/test").Page): Promise<void> {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  )
  expect(overflow).toBeLessThanOrEqual(1)
}

async function loginUI(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/login")
  await page.fill("#username", process.env.EZTEST_E2E_USERNAME?.trim() || "admin")
  await page.fill("#password", process.env.EZTEST_E2E_PASSWORD?.trim() || "admin123")
  await page.getByRole("button", { name: "登录" }).click()
  await page.waitForURL("**/versions")
}

function trackPageErrors(page: import("@playwright/test").Page): string[] {
  const pageErrors: string[] = []
  page.on("pageerror", (error) => pageErrors.push(String(error)))
  return pageErrors
}

test.beforeAll(async ({ request }) => {
  if (VERSION_ID === "") {
    throw new Error("EZTEST_E2E_P4_VERSION_ID is unset; run scripts/p4-e2e.ps1 (real LLM stack)")
  }
  versionID = VERSION_ID
  token = await login(request)
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
})

test("phase A: running version tabs, issues, chain and report stale/regenerate", async ({
  page,
  request,
}) => {
  test.setTimeout(TEST_TIMEOUT_MS)
  const pageErrors = trackPageErrors(page)
  await page.setViewportSize({ width: 1280, height: 900 })
  await loginUI(page)

  const overview = await getOverview(request)
  if (overview.active_wave === null) {
    throw new Error("the prepared version has no active wave; the E2E preconditions are stale")
  }
  firstWaveID = overview.active_wave.id

  // Queue the retest before any slow UI work: once wave 1 becomes terminal the
  // requirements may close, and a closed requirement rejects new intents. The
  // queued retest intent also keeps the requirement close blocked until wave 2.
  if (CASE_C_VERSION_ID === "") throw new Error("EZTEST_E2E_P4_CASE_C_VERSION_ID is unset")
  await api(
    request,
    "POST",
    `/api/versions/${versionID}/execution/intents`,
    { case_version_id: CASE_C_VERSION_ID, purpose: "retest" },
    201,
  )

  // ------------------------------------------------- running version header --
  await page.goto(`/versions/${versionID}`)
  await expect(page.getByTestId("version-header")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId("version-header")).toContainText("运行中")
  await expectNoHorizontalOverflow(page)
  await page.screenshot({ path: screenshotPath("version-running"), fullPage: true })

  // ------------------------------------------------------- todo drawer (2FA) --
  await page.getByTestId("version-todo-entry").click()
  await expect(page.getByTestId("todo-drawer")).toBeVisible()
  const todoHumanItems = page.locator('[data-testid^="todo-human-request-"]')
  await expect(todoHumanItems.first()).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId("todo-drawer")).toContainText("人工请求")
  await page.screenshot({ path: screenshotPath("todo-drawer-human-request") })
  await page.getByRole("button", { name: "关闭待办", exact: true }).click()
  await expect(page.getByTestId("todo-drawer")).toHaveCount(0)

  // ------------------------------------------------------------- issue tab --
  const issues = await getIssues(request)
  const failingIssue = issues.find((issue) => issue.status === "open") ?? issues[0]
  if (!failingIssue) throw new Error("the prepared version has no issue to exercise")
  failingIssueID = failingIssue.id
  await page.goto(`/versions/${versionID}?tab=issues`)
  await expect(page.getByTestId("issues-tab")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId(`issue-${failingIssueID}`)).toBeVisible({ timeout: 120_000 })
  await page.screenshot({ path: screenshotPath("issues-tab"), fullPage: true })
  await page.getByTestId(`issue-${failingIssueID}`).click()
  const issueSheet = page.getByTestId("issue-detail-sheet")
  await expect(issueSheet).toBeVisible({ timeout: 30_000 })
  await expect(issueSheet.getByTestId("issue-refs")).toBeVisible()
  await expect(issueSheet.getByTestId("issue-history")).toBeVisible()
  if (await issueSheet.getByRole("button", { name: "确认", exact: true }).isVisible()) {
    await issueSheet.getByRole("button", { name: "确认", exact: true }).click()
    const confirmDialog = page.getByRole("dialog", { name: "确认问题" })
    await expect(confirmDialog).toBeVisible()
    await confirmDialog.getByRole("button", { name: "确认问题", exact: true }).click()
  }
  await expect(issueSheet.getByText("已确认").first()).toBeVisible({ timeout: 30_000 })
  await page.screenshot({ path: screenshotPath("issue-confirmed") })
  await page.getByRole("button", { name: "关闭详情", exact: true }).click()

  // ------------------------------------------------------------- chain tab --
  await page.goto(`/versions/${versionID}?tab=chain`)
  await expect(page.getByTestId("chain-tab")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId("chain-toolbar")).toBeVisible()
  await expect(page.getByTestId("chain-filters")).toBeVisible()
  await expect(page.getByTestId("chain-stats")).toBeVisible()
  await expect(page.getByTestId("chain-event-timeline")).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await page.screenshot({ path: screenshotPath("chain-live"), fullPage: true })

  await page.locator("#chain-filter-result").selectOption("failed")
  await expect(page.locator("#chain-filter-result")).toHaveValue("failed")
  await page.waitForTimeout(1_500)
  await page.screenshot({ path: screenshotPath("chain-filter-failed"), fullPage: true })
  await page.locator("#chain-filter-result").selectOption("all")

  // The E2E script starts the API with chain.max_nodes=5 (the chain service
  // pins its budget at construction; hot reload is a reported P4 gap), so the
  // truncated graph and its alert are visible without a settings race.
  await expect(page.getByText("链路已按预算截断")).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText(/节点上限 5/)).toBeVisible()
  await page.screenshot({ path: screenshotPath("chain-truncated"), fullPage: true })
  await page.getByRole("button", { name: "刷新链路", exact: true }).click()
  await expect(page.getByText("链路已按预算截断")).toBeVisible({ timeout: 30_000 })

  // Deep link into the issue node.
  await page.goto(`/versions/${versionID}?tab=chain&issue=${failingIssueID}`)
  await expect(page.getByTestId("chain-detail-sheet")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId("chain-detail-sheet")).toContainText(`#${failingIssueID}`)
  await page.screenshot({ path: screenshotPath("chain-deep-link") })
  await page.getByRole("button", { name: "关闭链路详情", exact: true }).click()

  // ------------------------------------ resolve 2FA, wait for wave 1 terminal --
  const humanRequest = await waitFor(
    async () => {
      const requests = await getOpenHumanRequests(request)
      return requests.find((item) => item.kind === "login_takeover") ?? null
    },
    120_000,
    "open login_takeover human request",
  )
  await api(request, "POST", `/api/human-requests/${humanRequest.id}/resolve`, {
    response: { code: "246810", note: "P4 E2E 2FA code" },
  })
  await waitFor(
    async () => {
      const requests = await getOpenHumanRequests(request)
      return requests.length === 0 ? true : null
    },
    30_000,
    "human request resolved",
  )
  const wave1 = await waitFor(
    async () => {
      const waves = await getWaves(request)
      const wave = waves.find((item) => item.id === firstWaveID)
      return wave && ["done", "failed", "cancelled"].includes(wave.status) ? wave : null
    },
    10 * 60_000,
    "wave 1 terminal",
  )
  if (!["done", "failed"].includes(wave1.status)) {
    throw new Error(`wave 1 ended ${wave1.status}`)
  }
  await waitFor(
    async () => {
      const reports = await getReports(request, "wave")
      return reports.find(
        (report) => report.wave_id === firstWaveID && report.status === "published",
      )
    },
    10 * 60_000,
    "published wave-1 report",
  )

  // ------------------------------------- wait for the queued retest wave 2 --
  await waitFor(
    async () => {
      const current = await getOverview(request)
      if (current.active_wave !== null) return null
      const waves = await getWaves(request)
      return waves.length >= 2 ? waves : null
    },
    10 * 60_000,
    "wave 2 terminal (>=2 waves)",
  )

  // The E2E script keeps version.close blocked; the version report must still be
  // published after the requirements close, which lets phase A test stale.
  await waitFor(
    async () => {
      const reports = await getReports(request, "version")
      return reports.find((report) => report.status === "published") ?? null
    },
    10 * 60_000,
    "published version report while close is blocked",
  )

  // -------------------------------------------------- chain history replay --
  await page.goto(`/versions/${versionID}?tab=chain`)
  await expect(page.getByTestId("chain-tab")).toBeVisible({ timeout: 60_000 })
  const waveOptions = page.locator("#chain-history-wave option")
  await expect.poll(() => waveOptions.count(), { timeout: 30_000 }).toBeGreaterThanOrEqual(3) // realtime + >=2 waves
  const sortedWaves = [...(await getWaves(request))].sort((left, right) =>
    BigInt(left.id) < BigInt(right.id) ? -1 : 1,
  )
  await page.locator("#chain-history-wave").selectOption(sortedWaves[0].id)
  await expect(page.getByTestId("chain-replay-badge")).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId("chain-replay-badge")).toContainText("历史回看")
  await page.screenshot({ path: screenshotPath("chain-history"), fullPage: true })
  if (await page.getByTestId("chain-detail-sheet").isVisible()) {
    await page.getByRole("button", { name: "关闭链路详情", exact: true }).click()
  }

  // --------------------------------------------------- wave report + HTML --
  const publishedWaveReports = await getReports(request, "wave")
  const publishedWave = publishedWaveReports.find(
    (report) =>
      (report.status === "published" || report.status === "stale") &&
      report.current_revision?.status === "published",
  )
  if (!publishedWave) throw new Error("no readable wave report to open")
  await page.goto(`/versions/${versionID}?tab=reports`)
  await expect(page.getByTestId("reports-tab")).toBeVisible({ timeout: 60_000 })
  await page.getByTestId("reports-kind-wave").click()
  await expect(page.getByTestId("report-list")).toContainText("波次报告")
  await page.getByTestId(`report-${publishedWave.id}`).click()
  await expect(page.getByTestId("report-detail")).toBeVisible({ timeout: 30_000 })
  await expect(page.locator("#report-revision")).toBeVisible()
  await expect(page.getByTestId("report-detail")).toContainText("修订")
  const downloadPromise = page.waitForEvent("download", { timeout: 60_000 })
  await page.getByRole("button", { name: "下载 HTML", exact: true }).click()
  const download = await downloadPromise
  await download.saveAs(path.join(SCREENSHOT_DIR, "wave-report.html"))
  await page.screenshot({ path: screenshotPath("report-wave"), fullPage: true })

  // ------------------------------------- issue reopen -> stale -> regenerate --
  await page.goto(`/versions/${versionID}?tab=issues`)
  await expect(page.getByTestId(`issue-${failingIssueID}`)).toBeVisible({ timeout: 60_000 })
  await page.getByTestId(`issue-${failingIssueID}`).click()
  const reopenedSheet = page.getByTestId("issue-detail-sheet")
  await expect(reopenedSheet).toBeVisible({ timeout: 30_000 })
  await reopenedSheet.getByRole("button", { name: "重开", exact: true }).click()
  const reopenDialog = page.getByRole("dialog", { name: "重开问题" })
  await expect(reopenDialog).toBeVisible()
  await reopenDialog.locator("#issue-reopen-reason").fill("P4 E2E stale report input change")
  await reopenDialog.getByRole("button", { name: "重开问题", exact: true }).click()
  await expect(reopenedSheet.getByText("待确认").first()).toBeVisible({ timeout: 30_000 })
  await page.getByRole("button", { name: "关闭详情", exact: true }).click()

  const staleReport = await waitFor(
    async () => {
      const reports = await getReports(request, "version")
      return reports.find((report) => report.status === "stale") ?? null
    },
    120_000,
    "version report stale after issue change",
  )
  await page.goto(`/versions/${versionID}?tab=reports`)
  await expect(page.getByTestId("reports-tab")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText("输入已变化，建议重新生成。")).toBeVisible({ timeout: 30_000 })
  await page.screenshot({ path: screenshotPath("report-stale"), fullPage: true })

  await page.getByRole("button", { name: "重新生成", exact: true }).click()
  const regenerateDialog = page.getByRole("dialog", { name: "重新生成报告" })
  await expect(regenerateDialog).toBeVisible()
  await regenerateDialog.getByRole("button", { name: "重新生成", exact: true }).click()
  await expect(page.getByTestId("report-notice")).toBeVisible({ timeout: 30_000 })
  await waitFor(
    async () => {
      const detail = await getReportDetail(request, staleReport.id)
      return (
        detail.revisions.find(
          (revision) => revision.revision >= 2 && revision.status === "published",
        ) ?? null
      )
    },
    10 * 60_000,
    "version report revision 2 published",
  )
  await page.goto(`/versions/${versionID}?tab=reports&report=${staleReport.id}&revision=1`)
  await expect(page.getByTestId("reports-tab")).toBeVisible({ timeout: 60_000 })
  await expect(page.locator("#report-revision option[value='2']")).toHaveCount(1, {
    timeout: 30_000,
  })
  const finalDetail = await getReportDetail(request, staleReport.id)
  expect(finalDetail.current_revision?.revision ?? 0).toBeGreaterThanOrEqual(2)
  await page.locator("#report-revision").selectOption("1")
  await expect(page.getByTestId("report-detail")).toContainText("v1")
  await expect(page.getByTestId("report-detail").getByText("当前修订")).toHaveCount(0)
  await expect(page.getByRole("button", { name: "下载 HTML", exact: true })).toBeEnabled()
  await page.screenshot({ path: screenshotPath("report-old-revision"), fullPage: true })

  expect(pageErrors).toEqual([])
})

test("phase B: completed version, version report, manual reopen and resolved todo", async ({
  page,
  request,
}) => {
  test.setTimeout(TEST_TIMEOUT_MS)
  const pageErrors = trackPageErrors(page)
  await page.setViewportSize({ width: 1280, height: 900 })
  await loginUI(page)

  const completed = await getVersion(request)
  expect(completed.status).toBe("completed")
  expect(completed.closed_at).toBeTruthy()
  expect(completed.close_reason).toBeTruthy()

  await page.goto(`/versions/${versionID}`)
  await expect(page.getByTestId("version-header")).toContainText("已完成", { timeout: 30_000 })
  await expect(page.getByTestId("version-closed-info")).toBeVisible()
  await expect(page.getByTestId("version-reopen")).toBeVisible()
  await page.screenshot({ path: screenshotPath("version-completed"), fullPage: true })

  // ------------------------------------- version report at revision 2 --
  const versionReports = await getReports(request, "version")
  const versionReport = versionReports[0]
  if (!versionReport) throw new Error("the completed version has no summary report")
  await page.goto(`/versions/${versionID}?tab=reports`)
  await expect(page.getByTestId("reports-tab")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId("reports-kind-version")).toHaveAttribute("aria-pressed", "true")
  await expect(page.getByTestId(`report-${versionReport.id}`)).toBeVisible({ timeout: 30_000 })
  await page.getByTestId(`report-${versionReport.id}`).click()
  await expect(page.getByTestId("report-detail")).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId("report-detail")).toContainText("当前修订")
  const versionDownload = page.waitForEvent("download", { timeout: 60_000 })
  await page.getByRole("button", { name: "下载 HTML", exact: true }).click()
  await (await versionDownload).saveAs(path.join(SCREENSHOT_DIR, "version-report.html"))
  await page.screenshot({ path: screenshotPath("report-version"), fullPage: true })

  // ------------------------------------- todo drawer has no open requests --
  await page.goto(`/versions/${versionID}`)
  await expect(page.getByTestId("version-todo-entry")).toBeVisible({ timeout: 60_000 })
  await page.getByTestId("version-todo-entry").click()
  await expect(page.getByTestId("todo-drawer")).toBeVisible()
  await expect(page.locator('[data-testid^="todo-human-request-"]')).toHaveCount(0)
  await page.screenshot({ path: screenshotPath("todo-drawer-resolved") })
  await page.getByRole("button", { name: "关闭待办", exact: true }).click()

  // ------------------------------------- completed -> manual reopen --
  await expect(page.getByTestId("version-closed-info")).toBeVisible({ timeout: 30_000 })
  await page.getByTestId("version-reopen").click()
  const versionReopenDialog = page.getByRole("dialog", { name: "重新打开版本" })
  await expect(versionReopenDialog).toBeVisible()
  await versionReopenDialog.locator("#version-reopen-reason").fill("P4 E2E manual reopen")
  await versionReopenDialog.getByRole("button", { name: "重新打开", exact: true }).click()
  await expect(page.getByTestId("version-header")).toContainText("运行中", { timeout: 30_000 })
  await expect(page.getByTestId("version-closed-info")).toHaveCount(0)
  await page.screenshot({ path: screenshotPath("version-reopened"), fullPage: true })

  // ------------------------------------- 1600px width / no overflow smoke --
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto(`/versions/${versionID}?tab=chain`)
  await expect(page.getByTestId("chain-tab")).toBeVisible({ timeout: 60_000 })
  await expectNoHorizontalOverflow(page)
  await page.screenshot({ path: screenshotPath("chain-1600"), fullPage: true })

  expect(pageErrors).toEqual([])
})

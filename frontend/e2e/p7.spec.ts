import fs from "node:fs"
import path from "node:path"
import { pathToFileURL } from "node:url"

import { expect, type Locator, type Page, test } from "@playwright/test"

/**
 * P7 browser E2E against the real API/DB/Chrome, using the clone created by the
 * P7 API/DB acceptance run (`eztest_v02_p7`). No fake LLM and no fake executor:
 * the knowledge lifecycle, report PDFs, GC dry-run, instances, issue
 * recurrence/merge chain and the read-only global reports centre all read and
 * write through the live API.
 *
 * The P7 acceptance dataset re-mints its snowflakes on every reset, so the
 * version/report/knowledge/issue ids are discovered through the real API after
 * an admin login in `test.beforeAll`. Every `EZTEST_E2E_P7_*` id override is
 * still honoured when it is set; discovery then only fills the missing ids.
 * Assertions are unchanged: the discovered data must satisfy the same
 * preconditions the hardcoded ids used to pin (v2 appendix HTML + PDF, approved
 * knowledge items, a merged issue chain, recurrence_count >= 2).
 */
const ADMIN_USER = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD?.trim() || "admin123"
const API_BASE_URL = (process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:13333").replace(
  /\/+$/,
  "",
)

function envValue(name: string): string {
  return process.env[name]?.trim() ?? ""
}

let VERSION_ID = envValue("EZTEST_E2E_P7_VERSION_ID")
let REPORT_VERSION_ID = envValue("EZTEST_E2E_P7_REPORT_VERSION_ID")
let REPORT_ID = envValue("EZTEST_E2E_P7_REPORT_ID")
let APPENDIX_REVISION = Number.parseInt(envValue("EZTEST_E2E_P7_APPENDIX_REVISION"), 10) || 0
let APPENDIX_TEMPLATE_VERSION = "2"
let KNOWLEDGE_ID = envValue("EZTEST_E2E_P7_KNOWLEDGE_ID")
let ORDERS_KNOWLEDGE_ID = envValue("EZTEST_E2E_P7_ORDERS_KNOWLEDGE_ID")
let PAYMENTS_KNOWLEDGE_ID = envValue("EZTEST_E2E_P7_PAYMENTS_KNOWLEDGE_ID")
let SSE_KNOWLEDGE_ID = envValue("EZTEST_E2E_P7_SSE_KNOWLEDGE_ID")
let RECURRENCE_ISSUE_ID = envValue("EZTEST_E2E_P7_RECURRENCE_ISSUE_ID")
let MERGED_ISSUE_ID = envValue("EZTEST_E2E_P7_MERGED_ISSUE_ID")
let MERGED_VIA_ID = envValue("EZTEST_E2E_P7_MERGED_VIA_ID")
let MERGED_CHAIN: string[] = []
let RECURRENCE_COUNT = 0

const SCREENSHOT_DIR =
  process.env.EZTEST_E2E_P7_SCREENSHOT_DIR?.trim() ||
  path.resolve(process.cwd(), "..", ".tmp", "p7-e2e", "screenshots")

let apiToken = ""

type P7VersionSummary = {
  id: string
  title: string
  status: "initial" | "running" | "completed"
  updated_at: string
}

type P7ReportRevision = {
  revision: number
  template_version: string
  status: string
  html_available: boolean
  pdf_available: boolean
}

type P7ReportSummary = {
  id: string
  version_id: string
  kind: "version" | "wave"
  status: string
  current_revision?: P7ReportRevision | null
}

type P7ReportDetail = P7ReportSummary & { revisions: P7ReportRevision[] }

type P7Issue = {
  id: string
  title: string
  status: "open" | "confirmed" | "dismissed" | "merged"
  recurrence_count?: number
  merged_into_id?: string | null
  merged_chain?: string[] | null
}

type P7KnowledgeItem = {
  id: string
  scope: "global" | "system"
  system_key: string | null
  title: string
  content: string
  tags: string[]
  status: string
}

/** Logs in through the real API and returns the bearer token (never printed). */
async function apiLogin(): Promise<string> {
  const response = await fetch(`${API_BASE_URL}/api/auth/login`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASSWORD }),
  })
  if (!response.ok) {
    throw new Error(`P7 seed discovery: API login failed (HTTP ${response.status})`)
  }
  const payload = (await response.json()) as { data?: { token?: string } }
  const token = payload.data?.token
  if (typeof token !== "string" || token.length === 0) {
    throw new Error("P7 seed discovery: login response did not include a token")
  }
  return token
}

async function apiGet<T>(pathname: string): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${pathname}`, {
    headers: { Accept: "application/json", Authorization: `Bearer ${apiToken}` },
  })
  if (!response.ok) {
    throw new Error(`P7 seed discovery: GET ${pathname} failed (HTTP ${response.status})`)
  }
  const payload = (await response.json()) as { data: T }
  return payload.data
}

async function apiText(pathname: string): Promise<string> {
  const response = await fetch(`${API_BASE_URL}${pathname}`, {
    headers: { Accept: "text/html", Authorization: `Bearer ${apiToken}` },
  })
  if (!response.ok) {
    throw new Error(`P7 seed discovery: GET ${pathname} failed (HTTP ${response.status})`)
  }
  return response.text()
}

function versionOrder(left: P7VersionSummary, right: P7VersionSummary): number {
  const rank = (status: P7VersionSummary["status"]): number => {
    if (status === "running") return 0
    if (status === "initial") return 1
    return 2
  }
  if (rank(left.status) !== rank(right.status)) return rank(left.status) - rank(right.status)
  return right.updated_at.localeCompare(left.updated_at)
}

/**
 * Finds the report + appendix revision the PDF/HTML scenario needs:
 * - the report's current revision has generated PDF bytes;
 * - some revision with template v2 both has HTML+PDF and its HTML contains the
 *   enabled appendix (`data-template-version="2"` + `id="appendix"`).
 */
async function discoverReport(versions: P7VersionSummary[]): Promise<void> {
  if (REPORT_ID.length > 0 && APPENDIX_REVISION > 0) {
    if (REPORT_VERSION_ID.length === 0) {
      throw new Error(
        "P7 seed discovery: EZTEST_E2E_P7_REPORT_ID requires EZTEST_E2E_P7_REPORT_VERSION_ID",
      )
    }
    const html = await apiText(
      `/api/versions/${encodeURIComponent(REPORT_VERSION_ID)}/reports/${encodeURIComponent(
        REPORT_ID,
      )}/revisions/${APPENDIX_REVISION}/html`,
    )
    if (!html.includes('data-template-version="2"') || !html.includes('id="appendix"')) {
      throw new Error(
        "P7 seed discovery: the overridden appendix revision HTML has no template-v2 appendix",
      )
    }
    return
  }

  const candidates = REPORT_VERSION_ID
    ? versions.filter((version) => version.id === REPORT_VERSION_ID)
    : [...versions].sort(versionOrder)
  for (const version of candidates) {
    let reports: P7ReportSummary[]
    try {
      reports = await apiGet<P7ReportSummary[]>(
        `/api/versions/${encodeURIComponent(version.id)}/reports?limit=100`,
      )
    } catch {
      continue
    }
    if (REPORT_ID.length > 0) reports = reports.filter((report) => report.id === REPORT_ID)
    reports = [...reports].sort((left, right) =>
      left.kind === right.kind ? 0 : left.kind === "version" ? -1 : 1,
    )
    for (const report of reports) {
      let detail: P7ReportDetail
      try {
        detail = await apiGet<P7ReportDetail>(
          `/api/versions/${encodeURIComponent(version.id)}/reports/${encodeURIComponent(report.id)}`,
        )
      } catch {
        continue
      }
      const current = detail.current_revision
      if (
        current === null ||
        current === undefined ||
        !current.pdf_available ||
        Number.parseInt(current.template_version, 10) < 2
      ) {
        continue
      }
      const revisions = detail.revisions.filter(
        (revision) =>
          revision.pdf_available &&
          revision.html_available &&
          Number.parseInt(revision.template_version, 10) >= 2 &&
          (APPENDIX_REVISION <= 0 || revision.revision === APPENDIX_REVISION),
      )
      for (const revision of revisions) {
        let html = ""
        try {
          html = await apiText(
            `/api/versions/${encodeURIComponent(version.id)}/reports/${encodeURIComponent(
              report.id,
            )}/revisions/${revision.revision}/html`,
          )
        } catch {
          continue
        }
        if (html.includes('data-template-version="2"') && html.includes('id="appendix"')) {
          REPORT_VERSION_ID = version.id
          REPORT_ID = report.id
          APPENDIX_REVISION = revision.revision
          APPENDIX_TEMPLATE_VERSION = revision.template_version
          return
        }
      }
    }
  }
  throw new Error(
    "P7 seed discovery: no report with a generated PDF and a template-v2 appendix HTML revision was found; run scripts/p7-acceptance.ps1 (or set EZTEST_E2E_P7_REPORT_* overrides)",
  )
}

/**
 * Finds the issue pair the scenario needs: a merged issue whose chain reaches an
 * issue with recurrence_count >= 2 (the P7 recurrence redirect target).
 */
async function discoverIssues(versions: P7VersionSummary[]): Promise<void> {
  const candidates = VERSION_ID
    ? versions.filter((version) => version.id === VERSION_ID)
    : [...versions].sort(versionOrder)
  for (const version of candidates) {
    let issues: P7Issue[]
    try {
      issues = await apiGet<P7Issue[]>(
        `/api/versions/${encodeURIComponent(version.id)}/issues?limit=100`,
      )
    } catch {
      continue
    }
    const mergedCandidate = issues.find(
      (issue) =>
        issue.status === "merged" && (MERGED_ISSUE_ID.length === 0 || issue.id === MERGED_ISSUE_ID),
    )
    if (mergedCandidate === undefined) continue
    let merged: P7Issue
    try {
      merged = await apiGet<P7Issue>(
        `/api/versions/${encodeURIComponent(version.id)}/issues/${encodeURIComponent(
          mergedCandidate.id,
        )}`,
      )
    } catch {
      continue
    }
    const chain =
      merged.merged_chain !== null &&
      merged.merged_chain !== undefined &&
      merged.merged_chain.length > 0
        ? merged.merged_chain
        : merged.merged_into_id !== null && merged.merged_into_id !== undefined
          ? [merged.merged_into_id]
          : []
    const targetId = chain[chain.length - 1]
    if (targetId === undefined) continue
    let recurrence = issues.find((issue) => issue.id === targetId)
    if (recurrence === undefined || recurrence.recurrence_count === undefined) {
      try {
        recurrence = await apiGet<P7Issue>(
          `/api/versions/${encodeURIComponent(version.id)}/issues/${encodeURIComponent(targetId)}`,
        )
      } catch {
        continue
      }
    }
    if (
      recurrence === undefined ||
      (recurrence.recurrence_count ?? 0) < 2 ||
      (RECURRENCE_ISSUE_ID.length > 0 && recurrence.id !== RECURRENCE_ISSUE_ID)
    ) {
      continue
    }
    VERSION_ID = version.id
    MERGED_ISSUE_ID = merged.id
    MERGED_CHAIN = chain
    MERGED_VIA_ID = chain[0]
    RECURRENCE_ISSUE_ID = recurrence.id
    RECURRENCE_COUNT = recurrence.recurrence_count ?? 0
    return
  }
  throw new Error(
    "P7 seed discovery: no version with a merged issue chain into a recurrence_count>=2 issue was found; run scripts/p7-acceptance.ps1 (or set the EZTEST_E2E_P7_*_ISSUE_ID overrides)",
  )
}

async function listKnowledge(query: string): Promise<P7KnowledgeItem[]> {
  return apiGet<P7KnowledgeItem[]>(`/api/knowledge/items?limit=100&${query}`)
}

/** Resolves the four approved knowledge items the knowledge scenario asserts. */
async function discoverKnowledge(): Promise<void> {
  if (KNOWLEDGE_ID.length === 0) {
    const items = await listKnowledge("status=approved&tag=p7-acceptance")
    const global = items.find(
      (item) => item.scope === "global" && item.content.includes("P7K-GLOBAL"),
    )
    if (global === undefined) {
      throw new Error(
        "P7 seed discovery: no approved global knowledge tagged p7-acceptance with token P7K-GLOBAL; run scripts/p7-acceptance.ps1",
      )
    }
    KNOWLEDGE_ID = global.id
  }
  if (ORDERS_KNOWLEDGE_ID.length === 0) {
    const items = await listKnowledge("status=approved&scope=system&system_key=p4-orders")
    const orders = items.find((item) => item.content.includes("P7K-SYS-ORDERS"))
    if (orders === undefined) {
      throw new Error(
        "P7 seed discovery: no approved p4-orders system knowledge with token P7K-SYS-ORDERS",
      )
    }
    ORDERS_KNOWLEDGE_ID = orders.id
  }
  if (PAYMENTS_KNOWLEDGE_ID.length === 0) {
    const items = await listKnowledge("status=approved&scope=system&system_key=p4-payments")
    const payments = items.find((item) => item.content.includes("P7K-SYS-PAYMENTS"))
    if (payments === undefined) {
      throw new Error(
        "P7 seed discovery: no approved p4-payments system knowledge with token P7K-SYS-PAYMENTS",
      )
    }
    PAYMENTS_KNOWLEDGE_ID = payments.id
  }
  if (SSE_KNOWLEDGE_ID.length === 0) {
    const items = await listKnowledge("status=approved&q=SSE")
    const candidates = items.filter(
      (item) =>
        item.scope === "global" && `${item.title} ${item.content}`.toUpperCase().includes("SSE"),
    )
    // The tag-filter assertion expects this item to be untagged.
    const sse = candidates.find((item) => !item.tags.includes("p7-acceptance")) ?? candidates[0]
    if (sse === undefined) {
      throw new Error(
        "P7 seed discovery: no approved global knowledge matching q=SSE; run scripts/p7-acceptance.ps1",
      )
    }
    SSE_KNOWLEDGE_ID = sse.id
  }
}

test.beforeAll(async () => {
  apiToken = await apiLogin()
  const versions = await apiGet<P7VersionSummary[]>("/api/versions?limit=100")
  if (versions.length === 0) {
    throw new Error(
      "P7 seed discovery: no versions returned by GET /api/versions; run scripts/p7-acceptance.ps1 first",
    )
  }
  await discoverReport(versions)
  await discoverIssues(versions)
  await discoverKnowledge()
})

let shot = 0

function nextShot(name: string): string {
  shot += 1
  return path.join(SCREENSHOT_DIR, `p7-e2e-${String(shot).padStart(2, "0")}-${name}.png`)
}

/** Full-page screenshot for ordinary pages. */
async function snap(page: Page, name: string): Promise<void> {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
  await page.screenshot({ path: nextShot(name), fullPage: true })
}

/** Viewport screenshot for modal/sheet overlays and long settings pages. */
async function snapViewport(page: Page, name: string): Promise<void> {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
  await page.screenshot({ path: nextShot(name), fullPage: false })
}

/** Element screenshot for a specific panel. */
async function snapElement(locator: Locator, name: string): Promise<void> {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
  await locator.screenshot({ path: nextShot(name) })
}

async function loginUI(page: Page): Promise<void> {
  await page.goto("/login")
  await page.getByLabel("用户名").fill(ADMIN_USER)
  await page.getByLabel("密码").fill(ADMIN_PASSWORD)
  await page.getByRole("button", { name: "登录" }).click()
  await page.waitForURL((url) => !url.pathname.startsWith("/login"), { timeout: 30_000 })
}

async function expectPdfDownload(page: Page): Promise<{ filename: string; bytes: Buffer }> {
  const downloadPromise = page.waitForEvent("download", { timeout: 60_000 })
  await page.getByRole("button", { name: "下载 PDF" }).click()
  const download = await downloadPromise
  const filename = download.suggestedFilename()
  const filePath = await download.path()
  expect(filePath).not.toBeNull()
  const bytes = fs.readFileSync(filePath as string)
  return { filename, bytes }
}

test.describe.configure({ mode: "serial" })

test("knowledge list filters and the admin review lifecycle", async ({ page }) => {
  await loginUI(page)

  await page.goto("/knowledge")
  await expect(page.getByTestId("knowledge-page")).toBeVisible()
  await expect(page.getByTestId("knowledge-list")).toBeVisible()
  await expect(page.locator('[data-testid^="knowledge-item-"]').first()).toBeVisible({
    timeout: 30_000,
  })
  await snap(page, "knowledge-list")

  // Scope=system + system_key narrows the list to the matching SUT family.
  await page.locator("#knowledge-filter-scope").selectOption("system")
  await expect(page.locator("#knowledge-filter-system-key")).toBeVisible()
  await page.locator("#knowledge-filter-system-key").fill("p4-orders")
  await page.getByTestId("knowledge-search").click()
  await expect(page.getByTestId(`knowledge-item-${PAYMENTS_KNOWLEDGE_ID}`)).toHaveCount(0)
  await expect(page.getByTestId(`knowledge-item-${KNOWLEDGE_ID}`)).toHaveCount(0)
  await expect(page.getByTestId(`knowledge-item-${ORDERS_KNOWLEDGE_ID}`)).toBeVisible()
  await snap(page, "knowledge-filter-system")

  // Reset restores the unfiltered list.
  await page.getByTestId("knowledge-reset-filters").click()
  await expect(page.getByTestId(`knowledge-item-${KNOWLEDGE_ID}`)).toBeVisible()

  // Full-text search only returns the matching item.
  await page.locator("#knowledge-filter-q").fill("SSE")
  await page.getByTestId("knowledge-search").click()
  await expect(page.getByTestId(`knowledge-item-${KNOWLEDGE_ID}`)).toHaveCount(0)
  await expect(page.getByTestId(`knowledge-item-${SSE_KNOWLEDGE_ID}`)).toBeVisible()
  await snap(page, "knowledge-filter-search")

  // Tag filter only returns the acceptance-tagged items.
  await page.getByTestId("knowledge-reset-filters").click()
  await page.locator("#knowledge-filter-tag").fill("p7-acceptance")
  await page.getByTestId("knowledge-search").click()
  await expect(page.getByTestId(`knowledge-item-${SSE_KNOWLEDGE_ID}`)).toHaveCount(0)
  await expect(page.getByTestId(`knowledge-item-${KNOWLEDGE_ID}`)).toBeVisible()

  // Open the seeded approved item and inspect the frozen detail fields.
  await page.getByTestId(`knowledge-item-${KNOWLEDGE_ID}`).click()
  await expect(page.getByTestId("knowledge-detail")).toBeVisible()
  await expect(page.getByTestId("knowledge-status")).toHaveText(/已通过/)
  await expect(page.getByTestId("knowledge-content")).toContainText("P7K-GLOBAL")
  await expect(page.getByTestId("knowledge-revisions")).toBeVisible()
  await snapViewport(page, "knowledge-detail-approved")
  await page
    .getByRole("dialog", { name: "知识详情" })
    .getByRole("button", { name: "关闭", exact: true })
    .click()
  await expect(page.getByTestId("knowledge-detail")).toHaveCount(0)

  // Create a draft through the UI, approve it, revoke it and re-approve it.
  await page.getByTestId("knowledge-reset-filters").click()
  const stamp = Date.now()
  const approvedTitle = `P7 E2E knowledge ${stamp}`
  const rejectedTitle = `P7 E2E rejected ${stamp}`

  await page.getByTestId("knowledge-create").click()
  await expect(page.getByTestId("knowledge-editor")).toBeVisible()
  await page.getByTestId("knowledge-editor-title").fill(approvedTitle)
  await page
    .getByTestId("knowledge-editor-content")
    .fill("P7 E2E review lifecycle content. Token P7-E2E-APPROVE.")
  await page.getByTestId("knowledge-editor-tags").fill("p7-e2e, lifecycle")
  await snapViewport(page, "knowledge-editor-create")
  await page.getByTestId("knowledge-editor-submit").click()
  await expect(page.getByTestId("knowledge-editor")).toHaveCount(0)
  await expect(page.getByTestId("knowledge-detail")).toBeVisible()
  await expect(page.getByTestId("knowledge-status")).toHaveText(/草稿/)
  await snapViewport(page, "knowledge-draft-created")

  await page.getByTestId("knowledge-review-approve").click()
  await expect(page.getByTestId("knowledge-status")).toHaveText(/已通过/)
  await snapViewport(page, "knowledge-approved")

  await page.getByTestId("knowledge-review-revoke").click()
  const revokeDialog = page.getByRole("dialog", { name: "撤回知识" })
  await expect(revokeDialog).toBeVisible()
  await revokeDialog.getByTestId("knowledge-review-reason").fill("P7 E2E revoke check")
  await revokeDialog.getByTestId("knowledge-review-confirm").click()
  await expect(page.getByTestId("knowledge-status")).toHaveText(/已撤回/)
  await snapViewport(page, "knowledge-revoked")

  await expect(page.getByTestId("knowledge-review-approve")).toHaveText(/重新批准/)
  await page.getByTestId("knowledge-review-approve").click()
  await expect(page.getByTestId("knowledge-status")).toHaveText(/已通过/)
  await page
    .getByRole("dialog", { name: "知识详情" })
    .getByRole("button", { name: "关闭", exact: true })
    .click()
  await expect(page.getByTestId("knowledge-detail")).toHaveCount(0)

  // A second fresh draft exercises the reject transition (reason required).
  await page.getByTestId("knowledge-create").click()
  await expect(page.getByTestId("knowledge-editor")).toBeVisible()
  await page.getByTestId("knowledge-editor-title").fill(rejectedTitle)
  await page
    .getByTestId("knowledge-editor-content")
    .fill("P7 E2E rejected draft content. Token P7-E2E-REJECT.")
  await page.getByTestId("knowledge-editor-tags").fill("p7-e2e, rejected")
  await page.getByTestId("knowledge-editor-submit").click()
  await expect(page.getByTestId("knowledge-detail")).toBeVisible()
  await expect(page.getByTestId("knowledge-status")).toHaveText(/草稿/)

  await page.getByTestId("knowledge-review-reject").click()
  const rejectDialog = page.getByRole("dialog", { name: "驳回知识" })
  await expect(rejectDialog).toBeVisible()
  await expect(rejectDialog.getByTestId("knowledge-review-confirm")).toBeDisabled()
  await rejectDialog.getByTestId("knowledge-review-reason").fill("P7 E2E reject check")
  await expect(rejectDialog.getByTestId("knowledge-review-confirm")).toBeEnabled()
  await rejectDialog.getByTestId("knowledge-review-confirm").click()
  await expect(page.getByTestId("knowledge-status")).toHaveText(/已驳回/)
  await snapViewport(page, "knowledge-rejected")
  await page
    .getByRole("dialog", { name: "知识详情" })
    .getByRole("button", { name: "关闭", exact: true })
    .click()
  await expect(page.getByTestId("knowledge-detail")).toHaveCount(0)

  // The rejected status filter finds the item and excludes approved ones.
  await page.locator("#knowledge-filter-status").selectOption("rejected")
  await expect(page.getByTestId(`knowledge-item-${KNOWLEDGE_ID}`)).toHaveCount(0)
  await expect(page.getByRole("button", { name: rejectedTitle })).toBeVisible()
  await snap(page, "knowledge-filter-rejected")
})

test("report detail shows the done PDF, downloads it and renders the v2 appendix", async ({
  page,
  context,
}) => {
  await loginUI(page)

  await page.goto(`/versions/${REPORT_VERSION_ID}?tab=reports`)
  await expect(page.getByTestId("reports-tab")).toBeVisible()
  await expect(page.getByTestId("report-list")).toBeVisible()
  await page.getByTestId(`report-${REPORT_ID}`).click()
  await expect(page.getByTestId("report-detail")).toBeVisible()
  await expect(page.getByTestId("report-pdf-status")).toHaveText(/PDF 已生成/)
  await expect(
    page.locator("dt", { hasText: "模板版本" }).locator("xpath=following-sibling::dd[1]"),
  ).toHaveText("2")
  await snap(page, "report-detail-pdf-done")

  // The P7 PDF download is a real authenticated blob download.
  const pdf = await expectPdfDownload(page)
  expect(pdf.filename).toMatch(/\.pdf$/i)
  expect(pdf.bytes.length).toBeGreaterThan(1_000)
  expect(pdf.bytes.subarray(0, 5).toString("latin1")).toBe("%PDF-")
  await snap(page, "report-pdf-downloaded")

  // The discovered revision is template v2 with the appendix section enabled.
  await page.locator("#report-revision").selectOption(String(APPENDIX_REVISION))
  await expect(page.getByTestId("report-pdf-status")).toHaveText(/PDF 已生成/)
  await expect(
    page.locator("dt", { hasText: "模板版本" }).locator("xpath=following-sibling::dd[1]"),
  ).toHaveText(APPENDIX_TEMPLATE_VERSION)
  await snap(page, "report-revision-appendix-on")

  const htmlDownloadPromise = page.waitForEvent("download", { timeout: 60_000 })
  await page.getByRole("button", { name: "下载 HTML" }).click()
  const htmlDownload = await htmlDownloadPromise
  const downloadDir = path.join(SCREENSHOT_DIR, "..", "downloads")
  fs.mkdirSync(downloadDir, { recursive: true })
  const htmlPath = path.join(downloadDir, `report-${REPORT_ID}-r${APPENDIX_REVISION}.html`)
  await htmlDownload.saveAs(htmlPath)
  const html = fs.readFileSync(htmlPath, "utf8")
  expect(html).toContain('data-template-version="2"')
  expect(html).toContain('id="appendix"')
  expect(html).toContain("附录")

  // Render the downloaded revision and verify the appendix is actually visible.
  const htmlPage = await context.newPage()
  await htmlPage.goto(pathToFileURL(htmlPath).href)
  await expect(htmlPage.locator('main[data-template-version="2"]')).toBeVisible()
  await expect(htmlPage.locator("#appendix")).toBeVisible()
  await expect(htmlPage.getByRole("heading", { name: "附录" })).toBeVisible()
  await snap(htmlPage, "report-html-appendix")
  await htmlPage.close()
})

test("settings GC dry-run, apply confirmation gate and instance roster", async ({ page }) => {
  await loginUI(page)

  await page.goto("/settings?tab=general")
  await expect(page.getByRole("heading", { name: "设置中心" })).toBeVisible()
  const gcPanel = page.getByTestId("gc-panel")
  await expect(gcPanel).toBeVisible({ timeout: 30_000 })
  await gcPanel.scrollIntoViewIfNeeded()
  await expect(page.getByRole("table", { name: "GC 运行历史" })).toBeVisible()

  const latestHeading = gcPanel.locator('section[aria-label="最新运行"] h3')
  await expect(latestHeading).toContainText("#")
  const beforeRun = ((await latestHeading.textContent()) ?? "").trim()

  await page.getByRole("button", { name: "运行演练" }).click()
  await expect
    .poll(async () => ((await latestHeading.textContent()) ?? "").trim(), { timeout: 60_000 })
    .not.toBe(beforeRun)

  const latestSection = gcPanel.locator('section[aria-label="最新运行"]')
  await expect(latestSection.getByText("演练", { exact: true })).toBeVisible()
  await expect(latestSection.getByText("已完成", { exact: true })).toBeVisible()
  const reportTable = page.getByRole("table", { name: "GC 最新运行报告" })
  await expect(reportTable).toBeVisible()
  await expect(reportTable).toContainText("无引用对象")
  await expect(reportTable).toContainText("临时文件")
  await expect(reportTable).toContainText("被替代导出")
  await expect(reportTable).toContainText("孤儿文件")
  await expect(gcPanel.locator('[data-testid="gc-tombstones"]')).toBeVisible()
  await snapElement(gcPanel, "settings-gc-dry-run")

  // APPLY is gated behind typing the exact confirmation text; never submit it.
  await page.getByRole("button", { name: "执行回收" }).click()
  const applyDialog = page.getByRole("dialog", { name: "确认执行 GC" })
  await expect(applyDialog).toBeVisible()
  const applyConfirm = applyDialog.getByRole("button", { name: "确认执行" })
  await expect(applyConfirm).toBeDisabled()
  await applyDialog.locator("#gc-apply-confirm").fill("APPL")
  await expect(applyConfirm).toBeDisabled()
  await applyDialog.locator("#gc-apply-confirm").fill("APPLY")
  await expect(applyConfirm).toBeEnabled()
  await snapViewport(page, "settings-gc-apply-gate")
  await applyDialog.getByRole("button", { name: "取消" }).click()
  await expect(applyDialog).toHaveCount(0)

  // The instance roster is admin-only and lists at least the running API instance.
  const instancePanel = page.getByTestId("instance-panel")
  await instancePanel.scrollIntoViewIfNeeded()
  await expect(instancePanel).toBeVisible()
  const instanceTable = page.getByRole("table", { name: "API 实例列表" })
  await expect(instanceTable).toBeVisible()
  await expect(instanceTable.locator("tbody tr").first()).toBeVisible()
  await expect(instancePanel.getByText("运行中", { exact: true }).first()).toBeVisible()
  await snapElement(instancePanel, "settings-instances")
})

test("version issues show recurrence, the merged chain and the reopen dialog", async ({ page }) => {
  await loginUI(page)

  await page.goto(`/versions/${VERSION_ID}?tab=issues`)
  await expect(page.getByTestId("issues-tab")).toBeVisible()
  await expect(page.getByRole("table", { name: "问题列表" })).toBeVisible()

  // Deep links open the sheet even when the issue is outside the first table
  // page (the tab lists 20 rows); no fixed seed id is involved.
  await page.goto(
    `/versions/${VERSION_ID}?tab=issues&issue=${encodeURIComponent(RECURRENCE_ISSUE_ID)}`,
  )
  const sheet = page.getByTestId("issue-detail-sheet")
  await expect(sheet).toBeVisible()

  // Normalize the status to open first: a previous interrupted run may have left
  // the issue confirmed/dismissed, and the scenario below asserts the open state
  // before confirming and reopening it again.
  const resetReopen = sheet.getByRole("button", { name: "重开", exact: true })
  if ((await resetReopen.count()) > 0) {
    await resetReopen.click()
    const resetDialog = page.getByRole("dialog", { name: "重开问题" })
    await expect(resetDialog).toBeVisible()
    await resetDialog.locator("#issue-reopen-reason").fill("P7 E2E reset to open for rerun")
    await resetDialog.getByRole("button", { name: "重开问题" }).click()
    await expect(resetDialog).toHaveCount(0)
  }

  await expect(sheet.getByTestId("issue-recurrence")).toContainText(`复发 ${RECURRENCE_COUNT} 次`)
  await expect(sheet.getByText("待确认", { exact: true }).first()).toBeVisible()
  await snapViewport(page, "issues-recurrence")

  // The merged issue exposes the discovered merge chain source -> target.
  await sheet.getByRole("button", { name: "关闭详情" }).click()
  await expect(sheet).toHaveCount(0)
  await page.goto(`/versions/${VERSION_ID}?tab=issues&issue=${encodeURIComponent(MERGED_ISSUE_ID)}`)
  await expect(sheet).toBeVisible()
  const chain = sheet.getByTestId("issue-merged-chain")
  await expect(chain).toContainText("该问题已合并")
  await expect(sheet).toContainText(`#${MERGED_ISSUE_ID}`)
  for (const chainIssueId of MERGED_CHAIN) {
    await expect(chain).toContainText(chainIssueId)
  }
  await expect(chain).toContainText(MERGED_VIA_ID)
  await expect(chain).toContainText(RECURRENCE_ISSUE_ID)
  await expect(sheet.getByText("已合并", { exact: true }).first()).toBeVisible()
  await snapViewport(page, "issues-merged-chain")

  // Confirm (if still open), open the mandatory-reason reopen dialog and
  // restore the open state so reruns against the same clone stay deterministic.
  await sheet.getByRole("button", { name: "关闭详情" }).click()
  await expect(sheet).toHaveCount(0)
  await page.goto(
    `/versions/${VERSION_ID}?tab=issues&issue=${encodeURIComponent(RECURRENCE_ISSUE_ID)}`,
  )
  await expect(sheet).toBeVisible()
  const confirmButton = sheet.getByRole("button", { name: "确认", exact: true })
  if ((await confirmButton.count()) > 0) {
    await confirmButton.click()
    const confirmDialog = page.getByRole("dialog", { name: "确认问题" })
    await expect(confirmDialog).toBeVisible()
    await confirmDialog.getByRole("button", { name: "确认问题" }).click()
    await expect(confirmDialog).toHaveCount(0)
  }
  await expect(sheet.getByText("已确认", { exact: true }).first()).toBeVisible()

  await sheet.getByRole("button", { name: "重开", exact: true }).click()
  const reopenDialog = page.getByRole("dialog", { name: "重开问题" })
  await expect(reopenDialog).toBeVisible()
  const reopenReason = reopenDialog.locator("#issue-reopen-reason")
  await expect(reopenReason).toBeVisible()
  const reopenSubmit = reopenDialog.getByRole("button", { name: "重开问题" })
  await expect(reopenSubmit).toBeDisabled()
  await reopenReason.fill("P7 E2E reopen round-trip")
  await expect(reopenSubmit).toBeEnabled()
  await snapViewport(page, "issues-reopen-dialog")
  await reopenSubmit.click()
  await expect(reopenDialog).toHaveCount(0)
  await expect(sheet.getByText("待确认", { exact: true }).first()).toBeVisible()
  await expect(sheet.getByTestId("issue-recurrence")).toContainText(`复发 ${RECURRENCE_COUNT} 次`)
})

test("global reports centre surfaces PDF availability read-only", async ({ page }) => {
  await loginUI(page)

  await page.goto("/reports")
  await expect(page.getByTestId("global-reports-page")).toBeVisible()
  await expect(page.getByRole("table", { name: "全局报告列表" })).toBeVisible()
  const row = page.getByTestId(`global-report-${REPORT_ID}`)
  await expect(row).toBeVisible()
  await row.click()
  const detail = page.getByTestId("report-detail")
  await expect(detail).toBeVisible()
  await expect(detail.getByTestId("report-pdf-status")).toHaveText(/PDF 已生成/)
  await expect(detail.getByText("PDF 生成时间")).toBeVisible()
  // Read-only: the global centre must not offer the mutation/download actions.
  await expect(detail.getByRole("button", { name: "下载 PDF" })).toHaveCount(0)
  await snap(page, "global-reports-pdf")
})

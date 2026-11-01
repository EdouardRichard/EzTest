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
 * Seeded data (see docs/version-V0.2/acceptance/p7/acceptance.md):
 * - version 375072010541207552 (P4 acceptance 2026-11-01 08:02:15)
 * - version 375072612025372672 has a report whose revision 3 is template v2
 *   with the appendix section and a rendered PDF (revision 4 disables it)
 * - knowledge 375317489480892416 is the approved global acceptance item
 * - issue 990200 is the merge target with recurrence_count=2
 * - issue 375072083727613952 is merged through 375072108880855040 -> 990200
 */
const ADMIN_USER = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD?.trim() || "admin123"

const VERSION_ID = process.env.EZTEST_E2E_P7_VERSION_ID?.trim() || "375072010541207552"
const REPORT_VERSION_ID =
  process.env.EZTEST_E2E_P7_REPORT_VERSION_ID?.trim() || "375072612025372672"
const REPORT_ID = process.env.EZTEST_E2E_P7_REPORT_ID?.trim() || "375072738047426560"
const KNOWLEDGE_ID = process.env.EZTEST_E2E_P7_KNOWLEDGE_ID?.trim() || "375317489480892416"
const ORDERS_KNOWLEDGE_ID =
  process.env.EZTEST_E2E_P7_ORDERS_KNOWLEDGE_ID?.trim() || "375317509823266816"
const PAYMENTS_KNOWLEDGE_ID =
  process.env.EZTEST_E2E_P7_PAYMENTS_KNOWLEDGE_ID?.trim() || "375317509861015552"
const SSE_KNOWLEDGE_ID = process.env.EZTEST_E2E_P7_SSE_KNOWLEDGE_ID?.trim() || "375317682695700480"
const RECURRENCE_ISSUE_ID = process.env.EZTEST_E2E_P7_RECURRENCE_ISSUE_ID?.trim() || "990200"
const MERGED_ISSUE_ID = process.env.EZTEST_E2E_P7_MERGED_ISSUE_ID?.trim() || "375072083727613952"
const MERGED_VIA_ID = process.env.EZTEST_E2E_P7_MERGED_VIA_ID?.trim() || "375072108880855040"

const SCREENSHOT_DIR =
  process.env.EZTEST_E2E_P7_SCREENSHOT_DIR?.trim() ||
  path.resolve(process.cwd(), "..", ".tmp", "p7-e2e", "screenshots")

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

  // Revision 3 is template v2 with the appendix section enabled.
  await page.locator("#report-revision").selectOption("3")
  await expect(page.getByTestId("report-pdf-status")).toHaveText(/PDF 已生成/)
  await snap(page, "report-revision-appendix-on")

  const htmlDownloadPromise = page.waitForEvent("download", { timeout: 60_000 })
  await page.getByRole("button", { name: "下载 HTML" }).click()
  const htmlDownload = await htmlDownloadPromise
  const downloadDir = path.join(SCREENSHOT_DIR, "..", "downloads")
  fs.mkdirSync(downloadDir, { recursive: true })
  const htmlPath = path.join(downloadDir, `report-${REPORT_ID}-r3.html`)
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

  const sheet = page.getByTestId("issue-detail-sheet")
  await page.getByTestId(`issue-${RECURRENCE_ISSUE_ID}`).click()
  await expect(sheet).toBeVisible()
  await expect(sheet.getByTestId("issue-recurrence")).toContainText("复发 2 次")
  await expect(sheet.getByText("待确认", { exact: true }).first()).toBeVisible()
  await snapViewport(page, "issues-recurrence")

  // The merged issue exposes the merge chain source -> target.
  await sheet.getByRole("button", { name: "关闭详情" }).click()
  await expect(sheet).toHaveCount(0)
  await page.getByTestId(`issue-${MERGED_ISSUE_ID}`).click()
  await expect(sheet).toBeVisible()
  const chain = sheet.getByTestId("issue-merged-chain")
  await expect(chain).toContainText("该问题已合并")
  await expect(chain).toContainText(MERGED_VIA_ID)
  await expect(chain).toContainText(RECURRENCE_ISSUE_ID)
  await expect(sheet.getByText("已合并", { exact: true }).first()).toBeVisible()
  await snapViewport(page, "issues-merged-chain")

  // Confirm (if still open), open the mandatory-reason reopen dialog and
  // restore the open state so reruns against the same clone stay deterministic.
  await sheet.getByRole("button", { name: "关闭详情" }).click()
  await expect(sheet).toHaveCount(0)
  await page.getByTestId(`issue-${RECURRENCE_ISSUE_ID}`).click()
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
  await expect(sheet.getByTestId("issue-recurrence")).toContainText("复发 2 次")
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

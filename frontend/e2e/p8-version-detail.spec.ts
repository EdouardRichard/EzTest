import { execFileSync } from "node:child_process"
import { mkdirSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import { type APIRequestContext, type APIResponse, expect, type Page, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  API_URL,
  createRequirement,
  login,
  startVersionClarification,
  type VersionRecord,
} from "./p3-helpers"
import {
  authHeaders,
  createP4Anchor,
  p4Fixture,
  startP4Analysis,
  triggerAndWaitForIndex,
  waitForAnalysis,
} from "./p4-helpers"
import { approveMindmap, seedMindmapVersion, submitMindmapReview } from "./p5-helpers"
import { createBatch, listCaseRuns, setP6Mode, waitBatch } from "./p6-helpers"
import {
  captureConsoleErrors,
  createHumanIssue,
  expectNoConsoleErrors,
  expectNoSecretInPage,
  seedP7ExecutionVersion,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitP7BatchTerminal,
  waitP7ReportForBatch,
  waitVersionCompleted,
} from "./p7-helpers"

/**
 * P8 Task 34: version-detail E2E (design spec §7.1/§7.2/§7.3).
 *
 * - the embedded `?tab=mindmap&case=` view and the full-screen
 *   `/versions/{id}/mindmap?case=` route render the same selected case, with the
 *   full-screen header/back link present only on the standalone route;
 * - the code/changes tab renders the real `batchTimeline`: commit pair, changed
 *   file count, affected cases and the file → case mapping produced by the P4
 *   analysis and a `subjectKind=case` anchor;
 * - the report tab shows the stale marker after a human issue changed the input
 *   fingerprint, regenerates revision 2 and lets the revision selector switch
 *   back to the stale revision 1.
 *
 * Everything is real API/UI data; the fake LLM only drives the deterministic
 * agent turns. The executing fixtures are intentionally left in the throwaway
 * e2e database (it is recreated by scripts/e2e.ps1 before each run).
 */

const SPEC_TIMEOUT_MS = 400_000

/** Directory of the committed P8 cross-wave acceptance evidence. */
const ACCEPTANCE_DIR = fileURLToPath(new URL("../../docs/acceptance/p8/e2e", import.meta.url))

function evidencePath(name: string): string {
  mkdirSync(ACCEPTANCE_DIR, { recursive: true })
  return join(ACCEPTANCE_DIR, name)
}

/** Runs one SQL statement through the psql client exported by e2e.ps1. */
function runPsql(sql: string): string {
  const psql = process.env.EZTEST_E2E_PSQL?.trim()
  const databaseUrl = process.env.EZTEST_E2E_DATABASE_URL?.trim()
  if (!psql || !databaseUrl) {
    throw new Error("EZTEST_E2E_PSQL/EZTEST_E2E_DATABASE_URL are not set; run via scripts/e2e.ps1")
  }
  const parsed = new URL(databaseUrl)
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

type DataEnvelope<T> = { data: T }

async function readData<T>(response: APIResponse, expected: number, label: string): Promise<T> {
  expect(response.status(), `${label}: unexpected HTTP ${response.status()}`).toBe(expected)
  const body = (await response.json()) as DataEnvelope<T>
  return body.data
}

type MindmapNodeRecord = { id: string; kind: string; title: string; revision: number }
type MindmapStepRecord = { id: string; seq: number; action: string; expected: string }
type MindmapAssertionRecord = { id: string; stepId: string | null; kind: string }

/** POST /api/versions/{id}/mindmap/nodes. */
async function postMindmapNode(
  request: APIRequestContext,
  token: string,
  versionId: string,
  body: Record<string, unknown>,
): Promise<MindmapNodeRecord> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/mindmap/nodes`, {
    headers: authHeaders(token),
    data: body,
  })
  return readData<MindmapNodeRecord>(response, 201, "create mindmap node")
}

/** POST .../mindmap/nodes/{nodeId}/steps. */
async function postMindmapStep(
  request: APIRequestContext,
  token: string,
  versionId: string,
  nodeId: string,
  body: { action: string; expected: string },
): Promise<MindmapStepRecord> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/mindmap/nodes/${nodeId}/steps`,
    { headers: authHeaders(token), data: body },
  )
  return readData<MindmapStepRecord>(response, 201, "create mindmap step")
}

/** POST .../mindmap/nodes/{nodeId}/assertions. */
async function postMindmapAssertion(
  request: APIRequestContext,
  token: string,
  versionId: string,
  nodeId: string,
  body: { stepId?: string; kind: string; target?: string; expression: string; expected: string },
): Promise<MindmapAssertionRecord> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/mindmap/nodes/${nodeId}/assertions`,
    { headers: authHeaders(token), data: body },
  )
  return readData<MindmapAssertionRecord>(response, 201, "create mindmap assertion")
}

/** POST /api/versions/{id}/requirements/{rid}/confirm. */
async function confirmRequirement(
  request: APIRequestContext,
  token: string,
  versionId: string,
  requirementId: string,
): Promise<void> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/requirements/${requirementId}/confirm`,
    { headers: authHeaders(token) },
  )
  await readData<unknown>(response, 200, "confirm requirement")
}

/** POST /api/versions/{id}/enter-design. */
async function enterDesign(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<void> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/enter-design`, {
    headers: authHeaders(token),
    data: { acknowledgeUnclarified: true },
  })
  const data = await readData<{ version: { status: string } }>(response, 200, "enter design")
  expect(data.version.status).toBe("designing")
}

/** One `version_change` timeline entry of `GET /api/versions/{id}/code/changes`. */
type BatchTimelineEntry = {
  batchId: string
  batchNo: number
  batchStatus: string
  baseCommit: string
  headCommit: string
  changedFiles: number
  impactedCases: number
  unmappedCount: number
  provisional: boolean
  unavailable: boolean
  historyRewritten: boolean
  items: Array<{ file: string; changeKind: string; caseIds: string[]; unmapped: boolean }>
}

type CodeChangesView = {
  batchTimeline?: BatchTimelineEntry[]
}

async function loginAdmin(page: Page): Promise<void> {
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
}

// ---------------------------------------------------------------------------

test("P8 版本详情：脑图 Tab 与全屏路由一致", async ({ page, request }) => {
  test.setTimeout(180_000)
  if (await skipP7SpecWhenHarnessMissing({ requireFakeLlm: false })) return

  const monitor = captureConsoleErrors(page)
  const suffix = `mindmap${Date.now().toString(36)}`
  try {
    const seeded = await seedMindmapVersion(request, {
      suffix,
      requirementTitles: [`P8 脑图需求 ${suffix}`, `P8 脑图需求二 ${suffix}`],
    })
    const token = seeded.token
    const versionId = seeded.version.id
    const scenario = await postMindmapNode(request, token, versionId, {
      kind: "scenario",
      title: `P8 脑图场景 ${suffix}`,
      description: "P8 version-detail mindmap fixture",
    })
    const caseNode = await postMindmapNode(request, token, versionId, {
      kind: "case",
      parentId: scenario.id,
      title: `P8 脑图用例 ${suffix}`,
      priority: "P0",
      requirementIds: [seeded.requirements[0]?.id].filter((id): id is string => id !== undefined),
    })
    const step = await postMindmapStep(request, token, versionId, caseNode.id, {
      action: "打开订单页",
      expected: "订单页可见",
    })
    await postMindmapAssertion(request, token, versionId, caseNode.id, {
      stepId: step.id,
      kind: "ui",
      target: "#orders",
      expression: "text contains Orders",
      expected: "Orders",
    })

    await loginAdmin(page)

    await test.step("embedded tab selects the ?case= node without the full-screen chrome", async () => {
      await page.goto(
        `/versions/${encodeURIComponent(versionId)}?tab=mindmap&case=${encodeURIComponent(caseNode.id)}`,
      )
      await expect(page.getByTestId("version-tab-mindmap")).toBeVisible({ timeout: 60_000 })
      const mindmapPage = page.getByTestId("mindmap-page")
      await expect(mindmapPage).toBeVisible({ timeout: 60_000 })
      await expect(mindmapPage).toHaveAttribute("data-embedded", "true")
      await expect(page.getByTestId("back-to-version")).toHaveCount(0)
      await expect(page.getByTestId("mindmap-toolbar")).toHaveCount(0)
      await expect(page.getByTestId("case-editor")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByLabel("用例标题")).toHaveValue(`P8 脑图用例 ${suffix}`)
      await expect(page).toHaveURL(new RegExp(`case=${caseNode.id}`))
      await page.screenshot({
        path: evidencePath("p8-version-detail-01-mindmap-tab.png"),
        fullPage: true,
      })
    })

    await test.step("the full-screen route selects the same case and keeps the back link", async () => {
      await page.goto(
        `/versions/${encodeURIComponent(versionId)}/mindmap?case=${encodeURIComponent(caseNode.id)}`,
      )
      const mindmapPage = page.getByTestId("mindmap-page")
      await expect(mindmapPage).toBeVisible({ timeout: 60_000 })
      await expect(mindmapPage).not.toHaveAttribute("data-embedded", "true")
      await expect(page.getByTestId("back-to-version")).toBeVisible()
      await expect(page.getByTestId("mindmap-version-name")).toContainText(`P5 Version ${suffix}`)
      await expect(page.getByTestId("case-editor")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByLabel("用例标题")).toHaveValue(`P8 脑图用例 ${suffix}`)
      await page.screenshot({
        path: evidencePath("p8-version-detail-02-mindmap-fullscreen.png"),
        fullPage: true,
      })
    })

    await test.step("an illegal ?case= deep link shows a notice instead of crashing", async () => {
      await page.goto(
        `/versions/${encodeURIComponent(versionId)}/mindmap?case=18446744073709551615`,
      )
      await expect(page.getByTestId("mindmap-case-not-found")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("editor-placeholder")).toBeVisible()
    })

    await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 脑图 Tab")
    await expectNoConsoleErrors(monitor, page, "P8 脑图 Tab")
  } finally {
    monitor.stop()
  }
})

test("P8 版本详情：代码与变更批次时间线/受影响用例", async ({ page, request }) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireFakeLlm: false })) return

  const monitor = captureConsoleErrors(page)
  const suffix = `code${Date.now().toString(36)}`
  const fixture = p4Fixture()
  try {
    await setP7Mode(request, "noop")
    const seeded = await seedMindmapVersion(request, {
      suffix,
      requirementTitles: [`P8 代码需求 ${suffix}`, `P8 代码需求二 ${suffix}`],
    })
    const token = seeded.token

    // A version bound to the fixture's base→head range (the P5 seed pins HEAD).
    const versionResponse = await request.post(`${API_URL}/api/versions`, {
      headers: authHeaders(token),
      data: {
        name: `P8 代码与变更版本 ${suffix}`,
        label: `p8-code-changes-${suffix}`,
        description: "P8 version-detail code timeline fixture",
        systemId: seeded.systemId,
        environmentId: seeded.environmentId,
        codeRepositoryId: seeded.codeRepositoryId,
        codeBranch: fixture.branch,
        codeBaselineCommit: fixture.baseCommit,
      },
    })
    const version = await readData<VersionRecord>(versionResponse, 201, "create code version")
    const versionId = version.id

    const requirement = await createRequirement(request, token, versionId, {
      title: `P8 代码需求 ${suffix}`,
      description: "P8 code timeline requirement",
      acceptanceCriteria: "The code tab shows the change snapshot.",
    })
    await startVersionClarification(request, token, versionId)
    await confirmRequirement(request, token, versionId, requirement.id)
    await enterDesign(request, token, versionId)

    const scenario = await postMindmapNode(request, token, versionId, {
      kind: "scenario",
      title: `P8 代码场景 ${suffix}`,
      description: "P8 code timeline scenario",
    })
    const caseNode = await postMindmapNode(request, token, versionId, {
      kind: "case",
      parentId: scenario.id,
      title: `P8 代码用例 ${suffix}`,
      priority: "P0",
      requirementIds: [requirement.id],
    })
    const step = await postMindmapStep(request, token, versionId, caseNode.id, {
      action: "打开订单页",
      expected: "订单页可见",
    })
    await postMindmapAssertion(request, token, versionId, caseNode.id, {
      stepId: step.id,
      kind: "ui",
      target: "#orders",
      expression: "text contains Orders",
      expected: "Orders",
    })
    const anchor = await createP4Anchor(request, token, versionId, {
      subjectKind: "case",
      subjectId: caseNode.id,
      filePath: "src/api/refund.ts",
    })
    expect(anchor.versionId).toBe(versionId)

    // Real T0 index + P4 analysis on the fixture head, so the version_change
    // snapshot is final instead of provisional.
    const index = await triggerAndWaitForIndex(request, token, seeded.codeRepositoryId)
    expect(index.run.status).toBe("done")
    expect(index.run.commit).toBe(fixture.headCommit)
    await startP4Analysis(request, token, versionId)
    const analysis = await waitForAnalysis(request, token, versionId)
    expect(analysis.status).toBe("done")
    expect(analysis.headCommit).toBe(fixture.headCommit)

    await submitMindmapReview(request, token, versionId)
    const approved = await approveMindmap(request, token, versionId)
    expect(approved.version.status).toBe("executing")

    const batch = await createBatch(request, token, versionId)
    expect(batch.code_baseline_commit).toBe(fixture.baseCommit)
    expect(batch.head_commit).toBe(fixture.headCommit)
    await waitBatch(request, token, versionId, batch.id, { timeoutMs: 300_000 })

    const changesResponse = await request.get(`${API_URL}/api/versions/${versionId}/code/changes`, {
      headers: authHeaders(token),
    })
    const changes = await readData<CodeChangesView>(changesResponse, 200, "read code changes")
    const timeline = changes.batchTimeline ?? []
    expect(timeline).toHaveLength(1)
    const entry = timeline[0]
    expect(entry?.batchId).toBe(batch.id)
    expect(entry?.baseCommit).toBe(fixture.baseCommit)
    expect(entry?.headCommit).toBe(fixture.headCommit)
    expect(entry?.changedFiles).toBe(7)
    expect(entry?.impactedCases).toBe(1)
    const anchoredItem = entry?.items.find((item) => item.file === "src/api/refund.ts")
    expect(anchoredItem, "the changed anchor file must be in the snapshot").toBeDefined()
    expect(anchoredItem?.caseIds).toContain(caseNode.id)

    await loginAdmin(page)
    await page.goto(`/versions/${encodeURIComponent(versionId)}?tab=code`)
    const codeTimeline = page.getByTestId("code-batch-timeline")
    await expect(codeTimeline).toBeVisible({ timeout: 60_000 })
    const batchEntry = page.getByTestId(`batch-timeline-${batch.id}`)
    await expect(batchEntry).toBeVisible({ timeout: 60_000 })
    await expect(batchEntry).toContainText("Batch 1")
    await expect(batchEntry).toContainText("变更文件 7")
    await expect(batchEntry).toContainText("影响用例 1")
    await expect(batchEntry).toContainText(`用例 ${caseNode.id}`)
    await expect(batchEntry).toContainText(fixture.baseCommit.slice(0, 8))
    await expect(batchEntry).toContainText(fixture.headCommit.slice(0, 8))
    await expect(
      page.getByTestId("batch-change-item").filter({ hasText: "src/api/refund.ts" }).first(),
    ).toBeVisible()
    await page.screenshot({
      path: evidencePath("p8-version-detail-03-code-timeline.png"),
      fullPage: true,
    })

    await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 代码与变更")
    await expectNoConsoleErrors(monitor, page, "P8 代码与变更")
  } finally {
    monitor.stop()
    await setP7Mode(request, "normal").catch(() => undefined)
  }
})

test("P8 版本详情：报告 revision 切换与 stale 标记", async ({ page, request }) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true })) return

  const monitor = captureConsoleErrors(page)
  const suffix = `report${Date.now().toString(36)}`
  let reportVersionId = ""
  try {
    await setP6Mode(request, "normal")
    await setP7Mode(request, "noop")
    const seeded = await seedP7ExecutionVersion(request, {
      caseTitle: `P8 报告用例 ${suffix}`,
    })
    const token = seeded.token
    const versionId = seeded.version.id
    reportVersionId = versionId
    await loginAdmin(page)

    const batch = await createBatch(request, token, versionId)
    const done = await waitP7BatchTerminal(request, token, versionId, batch.id, 300_000)
    expect(done.status).toBe("done")
    await waitVersionCompleted(request, token, versionId, 300_000)
    const report = await waitP7ReportForBatch(request, token, versionId, batch.id)
    expect(report.revision).toBe(1)
    expect(report.status).toBe("published")

    // A human issue linked to the batch's case run changes the report input
    // fingerprint; the next read must mark the published revision stale.
    const runs = await listCaseRuns(request, token, versionId, done.id)
    expect(runs).toHaveLength(1)
    const issue = await createHumanIssue(request, token, versionId, {
      title: `P8 报告 stale 问题 ${suffix}`,
      severity: "minor",
      description: "P8 report stale linkage fixture",
      caseRunId: runs[0]?.id,
      caseNodeId: seeded.caseNodeId,
    })
    expect(issue.id).toMatch(/^\d+$/)

    await page.goto(
      `/versions/${encodeURIComponent(versionId)}?tab=report&report=${encodeURIComponent(report.id)}`,
    )
    await expect(page.getByTestId("report-tab")).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 1")
    const staleBanner = page.getByTestId("report-stale-banner")
    await expect(staleBanner).toBeVisible({ timeout: 60_000 })
    await expect(staleBanner).toContainText("报告已过期")
    await page.screenshot({
      path: evidencePath("p8-version-detail-04-report-stale.png"),
      fullPage: true,
    })

    await page.getByTestId("report-regenerate-btn").click()
    await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 2", {
      timeout: 60_000,
    })
    await expect(staleBanner).toHaveCount(0)
    const selector = page.getByTestId("report-revision-selector")
    await expect(selector.locator("option")).toHaveCount(2)
    await expect(page).toHaveURL(/report=/)

    // Switching back to revision 1 shows that historical revision's stale badge.
    await selector.selectOption(report.id)
    await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 1", {
      timeout: 60_000,
    })
    await expect(staleBanner).toBeVisible({ timeout: 60_000 })

    const latestOption = await selector
      .locator("option")
      .filter({ hasNotText: "revision 1" })
      .getAttribute("value")
    expect(latestOption).toMatch(/^\d+$/)
    await selector.selectOption(latestOption ?? "")
    await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 2", {
      timeout: 60_000,
    })
    await expect(staleBanner).toHaveCount(0)
    await page.screenshot({
      path: evidencePath("p8-version-detail-05-report-revision.png"),
      fullPage: true,
    })

    await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 报告 revision")
    await expectNoConsoleErrors(monitor, page, "P8 报告 revision")
  } finally {
    monitor.stop()
    await setP7Mode(request, "normal").catch(() => undefined)
    try {
      if (reportVersionId !== "") {
        // The report fixture must not leak into the global reports list of any
        // later spec; report_ref rows cascade with the report.
        runPsql(`DELETE FROM report WHERE version_id = ${reportVersionId}::bigint;`)
      }
    } catch {
      // Raw Playwright runs without the psql env skip the DB cleanup.
    }
  }
})

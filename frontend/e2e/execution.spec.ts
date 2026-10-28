import { createHash } from "node:crypto"
import { readFileSync } from "node:fs"

import { type APIRequestContext, expect, type Page, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  API_URL,
  createVersion,
  login,
  uniqueSuffix,
  type VersionRecord,
} from "./p3-helpers"
import {
  type CaseRun,
  createBatch,
  type ExecutionSeed,
  fakeLlmUrl,
  getBatch,
  getCaseRun,
  getEvidence,
  getReport,
  listCaseRuns,
  readVersionEvents,
  seedExecutionVersion,
  setP6Mode,
  waitBatch,
  waitCaseRun,
} from "./p6-helpers"

/**
 * P6 execution E2E (design spec §10.5, eight steps). One serial test drives the
 * real API, the real Playwright executor on :13336 and the deterministic P6
 * fixture/fake-LLM through the browser UI:
 *
 * 1. login + API seed (system/environment/account/access/auth-flow/version with
 *    a confirmed requirement set and an approved mindmap baseline → executing);
 * 2. start a batch, watch the live sessions tab and the broadcast timeline;
 * 3. results rows, step/assertion details and the evidence viewer;
 * 4. graceful pause → checkpoint → resume without re-running recorded steps;
 * 5. immediate pause → interrupted + cleanup flag → resume to completion;
 * 6. assertion failure → failed(application) and environment block →
 *    blocked(environment);
 * 7. report tab + Markdown export content;
 * 8. negatives: non-executing batch create 409, executor down 503.
 *
 * Every page assertion also checks that neither the admin nor the fixture
 * password leaks into the rendered text (Review Focus #2).
 *
 * The user-visible evidence viewer currently receives screenshot artifacts from
 * the deterministic P6 normal scenario; HTTP-evidence rendering is covered by
 * the T22 component tests because the fake upstream emits browser tools only.
 */

const FIXTURE_PASSWORD = "fixture-pass"
const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])

/** Design spec §10.5: one serial test with a large budget (multiple batches). */
const EXECUTION_SPEC_TIMEOUT_MS = 600_000

function executorUrl(): string {
  const configured = process.env.EZTEST_E2E_EXECUTOR_URL?.trim()
  if (configured !== undefined && configured !== "") return configured.replace(/\/+$/, "")
  return "http://127.0.0.1:13336"
}

function executorPid(): number {
  const raw = process.env.EZTEST_E2E_EXECUTOR_PID?.trim() ?? ""
  const pid = Number(raw)
  return Number.isInteger(pid) && pid > 0 ? pid : 0
}

/**
 * Skips the spec when the dedicated harness is absent (admin credentials, fake
 * LLM, real executor). Playwright E2E must never silently reach the real
 * provider or a missing executor.
 */
async function executionHarnessSkipReason(): Promise<string> {
  if (!ADMIN_PASSWORD) {
    return "EZTEST_E2E_ADMIN_PASSWORD is not set; run via scripts/e2e.ps1"
  }
  try {
    const response = await fetch(`${fakeLlmUrl()}/v1/models`)
    if (!response.ok) {
      return `fake LLM at ${fakeLlmUrl()} answered ${response.status}; run via scripts/e2e.ps1`
    }
  } catch {
    return `fake LLM is unavailable at ${fakeLlmUrl()}; run via scripts/e2e.ps1`
  }
  try {
    const response = await fetch(`${executorUrl()}/healthz`)
    if (!response.ok) {
      return `executor at ${executorUrl()} answered ${response.status}; run via scripts/e2e.ps1`
    }
  } catch {
    return `executor is unavailable at ${executorUrl()}; run via scripts/e2e.ps1`
  }
  return ""
}

/** Asserts the execution UI never renders a credential value (Review Focus #2). */
async function expectNoPasswordLeak(page: Page, label: string): Promise<void> {
  const text = await page.locator("body").textContent()
  for (const secret of [ADMIN_PASSWORD, FIXTURE_PASSWORD]) {
    if (secret === "") continue
    expect(text?.includes(secret), `${label}: the page text must not contain a credential`).toBe(
      false,
    )
  }
}

/** Reads exactly one case run of a one-case batch; polled while the batch starts. */
async function onlyCaseRunId(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
): Promise<string> {
  let caseRunId = ""
  await expect
    .poll(
      async () => {
        const runs = await listCaseRuns(request, token, versionId, batchId)
        caseRunId = runs[0]?.id ?? ""
        return runs.length
      },
      { message: "the batch must create exactly one case run", timeout: 30_000 },
    )
    .toBe(1)
  expect(caseRunId).toMatch(/^\d+$/)
  return caseRunId
}

/** Polls the case-run status without treating an intermediate state as an error. */
async function expectCaseRunStatus(
  request: APIRequestContext,
  token: string,
  seed: ExecutionSeed,
  batchId: string,
  caseRunId: string,
  status: CaseRun["status"],
  timeout: number,
): Promise<void> {
  await expect
    .poll(
      async () => (await getCaseRun(request, token, seed.version.id, batchId, caseRunId)).status,
      { message: `case run ${caseRunId} must become ${status}`, timeout },
    )
    .toBe(status)
}

/** Reads the persisted event list until one type exists (SSE/persist ordering). */
async function expectVersionEvent(
  request: APIRequestContext,
  token: string,
  versionId: string,
  type: string,
  timeout = 20_000,
): Promise<void> {
  await expect
    .poll(
      async () =>
        (await readVersionEvents(request, token, versionId, { maxPages: 3 })).some(
          (e) => e.type === type,
        ),
      { message: `version ${versionId} must persist a ${type} event`, timeout },
    )
    .toBe(true)
}

/**
 * Expands the broadcast history until the row of one persisted event exists.
 * The tab starts from the newest 50 events (its history cursor), so the
 * batch/run start rows need two 「加载更早事件」 pages on a 26-step run.
 */
async function expectBroadcastRow(page: Page, seq: number): Promise<void> {
  const row = page.getByTestId(`broadcast-row-${seq}`)
  const loadOlder = page.getByTestId("broadcast-load-older")
  const rows = page.locator('[data-testid^="broadcast-row-"]')
  for (let pageIndex = 0; pageIndex < 6; pageIndex += 1) {
    if (await row.isVisible()) return
    if ((await loadOlder.count()) === 0) break
    const before = await rows.count()
    await loadOlder.click()
    await expect
      .poll(async () => rows.count(), { message: "broadcast history page", timeout: 15_000 })
      .toBeGreaterThan(before)
  }
  await expect(row).toBeVisible({ timeout: 30_000 })
}

/** Sorted, de-duplicated step sequence list used by the checkpoint assertions. */
function stepSeqs(caseRun: CaseRun): number[] {
  return caseRun.steps.map((step) => step.seq).sort((left, right) => left - right)
}

/** Waits until the version detail shell for one version finished loading. */
async function openVersionPage(page: Page, version: VersionRecord): Promise<void> {
  await page.goto(`/versions/${version.id}`)
  await expect(page.getByRole("heading", { name: version.name })).toBeVisible({ timeout: 60_000 })
}

test.describe.configure({ mode: "serial" })

test("P6 执行端到端：seed → 批次 → 暂停/恢复 → 结果/证据 → 报告 → 负例", async ({
  page,
  request,
}) => {
  test.setTimeout(EXECUTION_SPEC_TIMEOUT_MS)

  const skipReason = await executionHarnessSkipReason()
  if (skipReason !== "") {
    test.skip(true, skipReason)
    return
  }

  /** Seeds + logs in (spec §10.5 step 1) and checks the executing baseline UI. */
  const normalSeed = await test.step("Step 1 登录并 seed 已批准基线的版本", async () => {
    // The extra steps widen the deterministic pause window: the fake run records
    // them one per turn, so the graceful pause can observe a non-empty checkpoint
    // before the run would otherwise finish.
    const seed = await seedExecutionVersion(request, { extraSteps: 24 })
    expect(seed.version.status, "the approved mindmap must move the version to executing").toBe(
      "executing",
    )
    expect(seed.sessionId, "the fixture session must be imported").toBeGreaterThan(0)

    await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
    await openVersionPage(page, seed.version)
    await expect(page.getByTestId("exec-start-btn")).toBeVisible()
    await expectNoPasswordLeak(page, "版本详情")
    return seed
  })

  /** Steps 2-4: live views, graceful pause with a checkpoint, resume to done. */
  const graceful =
    await test.step("Step 2-4 批次 running → 实时会话/播报 → 优雅暂停 → 从检查点恢复", async () => {
      const token = normalSeed.token
      const versionId = normalSeed.version.id
      await setP6Mode(request, "normal")

      const selector = page.getByTestId("exec-batch-selector")
      await page.getByTestId("exec-start-btn").click()
      await expect(page.getByTestId("exec-pause-btn")).toBeVisible({ timeout: 60_000 })
      await expect(selector).toBeVisible()
      const batchId = await selector.inputValue()
      expect(batchId, "the batch selector must expose the created batch id").toMatch(/^\d+$/)

      const created = await getBatch(request, token, versionId, batchId)
      expect(created.status).toBe("running")
      expect(created.paused).toBe(false)
      expect(created.counts.total).toBe(1)

      const caseRunId = await onlyCaseRunId(request, token, versionId, batchId)
      await expectCaseRunStatus(request, token, normalSeed, batchId, caseRunId, "running", 60_000)

      // A graceful pause must park the run at a step boundary with the finished
      // steps preserved. Open the dialog up front (it only sends the pause on
      // confirm) so the confirmation is the only action left in the critical
      // window; the normal fake records its 26 steps in ~150ms, so the step
      // poll must not use the default 100/250/500/1000ms backoff or it misses
      // the first step entirely and the run hits max_turns unanswered.
      await page.getByTestId("exec-pause-btn").click()
      await expect(page.getByTestId("exec-pause-dialog")).toBeVisible()
      await expect(page.getByTestId("exec-pause-mode-graceful")).toHaveAttribute(
        "aria-pressed",
        "true",
      )
      await expect
        .poll(
          async () =>
            (await getCaseRun(request, token, versionId, batchId, caseRunId)).steps.length,
          { message: "the normal fake run must record a step", timeout: 120_000, intervals: [25] },
        )
        .toBeGreaterThan(0)
      // Switch to the slow `budget` planner only after the first step exists:
      // its 8s navigations would otherwise pre-empt the normal step recording.
      await setP6Mode(request, "budget")
      await page.getByTestId("exec-pause-confirm").click()

      const banner = page.getByTestId("paused-banner")
      await expect(banner).toBeVisible({ timeout: 30_000 })
      await expect(banner).toHaveAttribute("data-pause-mode", "graceful")
      await expect(page.getByTestId("exec-resume-btn")).toBeVisible()

      await expect
        .poll(async () => (await getBatch(request, token, versionId, batchId)).paused, {
          message: "the graceful pause must flag the batch",
          timeout: 30_000,
        })
        .toBe(true)
      const parked = await waitCaseRun(request, token, versionId, batchId, caseRunId, {
        until: ["pending"],
        timeoutMs: 30_000,
      })
      expect(parked.status).toBe("pending")
      expect(parked.checkpoint.cleanup_required).toBe(false)
      expect(parked.checkpoint.completed_seqs.length).toBeGreaterThan(0)
      expect(parked.checkpoint.completed_seqs.length).toBeLessThan(normalSeed.stepIds.length)

      // Persisted events must document the pause (batch + run).
      await expectVersionEvent(request, token, versionId, "batch.paused")
      await expectVersionEvent(request, token, versionId, "run.paused")
      const pausedEvents = await readVersionEvents(request, token, versionId)
      const runPaused = pausedEvents.find((event) => event.type === "run.paused")
      expect(runPaused?.detail?.mode).toBe("graceful")

      // Step 2 live views: sessions tab + broadcast timeline while the batch is
      // still active (paused batches keep the running status).
      await page.getByRole("tab", { name: "执行会话" }).click()
      const sessionsTab = page.getByTestId("sessions-tab")
      await expect(sessionsTab).toBeVisible()
      await expect(sessionsTab.getByTestId("stream-status")).toContainText("实时", {
        timeout: 30_000,
      })
      await expect(page.getByTestId("session-filter-agent")).toBeVisible()
      await expect(page.getByTestId("session-filter-case")).toBeVisible()
      await expect(page.getByTestId("session-filter-kind")).toBeVisible()
      await expect(page.getByTestId("session-group-by")).toBeVisible()
      await expect(page.getByTestId("session-search")).toBeVisible()
      await expect(page.getByTestId("session-filter-errors")).toBeVisible()
      await expect(page.locator('[data-testid^="session-row-"]').first()).toBeVisible({
        timeout: 30_000,
      })
      await expect(page.getByTestId("execution-transcript").first()).toBeVisible({
        timeout: 30_000,
      })

      await page.getByRole("tab", { name: "实时播报" }).click()
      const broadcastTab = page.getByTestId("broadcast-tab")
      await expect(broadcastTab).toBeVisible()
      await expect(broadcastTab.getByTestId("stream-status")).toHaveAttribute(
        "data-status",
        "live",
        { timeout: 30_000 },
      )
      await expect(page.getByTestId("broadcast-filter-type")).toBeVisible()
      await expect(page.getByTestId("broadcast-filter-agent")).toBeVisible()
      await expect(page.getByTestId("broadcast-filter-severity")).toBeVisible()
      await expect(page.getByTestId("broadcast-search")).toBeVisible()
      await expect(page.locator('[data-testid^="broadcast-row-"]').first()).toBeVisible({
        timeout: 30_000,
      })

      // The run emits more than one 100-event page, so read the history back to
      // the batch start instead of assuming it is still on the newest page.
      const events = await readVersionEvents(request, token, versionId, { maxPages: 3 })
      for (const type of ["batch.started", "run.started", "step.finished"]) {
        const persisted = events.find((event) => event.type === type)
        expect(persisted, `the version stream must persist a ${type} event`).toBeTruthy()
        if (persisted === undefined) throw new Error(`missing ${type} event`)
        await expectBroadcastRow(page, persisted.seq)
      }
      await expectNoPasswordLeak(page, "实时播报")

      // Resume from the checkpoint: the fake returns to `normal`, so the same
      // attempt records only the remaining steps and finishes passed.
      await setP6Mode(request, "normal")
      await page.getByTestId("exec-resume-btn").click()
      await expect(banner).toBeHidden({ timeout: 30_000 })

      const finished = await waitCaseRun(request, token, versionId, batchId, caseRunId, {
        timeoutMs: 240_000,
      })
      expect(finished.status).toBe("passed")
      expect(finished.checkpoint.cleanup_required).toBe(false)
      expect(finished.checkpoint.interrupted_step_seq).toBeNull()
      expect(stepSeqs(finished)).toEqual(
        Array.from({ length: normalSeed.stepIds.length }, (_, index) => index + 1),
      )
      expect(new Set(stepSeqs(finished)).size, "resume must not repeat a step").toBe(
        normalSeed.stepIds.length,
      )
      expect(finished.assertions).toHaveLength(1)
      expect(finished.assertions[0]?.verdict).toBe("passed")
      expect(finished.evidence.length).toBeGreaterThan(0)

      const done = await waitBatch(request, token, versionId, batchId, { timeoutMs: 240_000 })
      expect(done.status).toBe("done")
      expect(done.counts.passed).toBe(1)
      expect(done.paused).toBe(false)
      return { batchId, caseRunId, finished, done }
    })

  /** Step 3: results rows, expanded steps/assertions and the evidence viewer. */
  await test.step("Step 3 结果与证据：行/步骤/断言/证据查看器（截图）", async () => {
    const token = normalSeed.token
    const versionId = normalSeed.version.id
    await openVersionPage(page, normalSeed.version)
    await page.getByRole("tab", { name: "结果与证据" }).click()
    await expect(page.getByTestId("results-tab")).toBeVisible()

    const row = page.getByTestId(`case-run-row-${graceful.caseRunId}`)
    await expect(row).toBeVisible({ timeout: 60_000 })
    await expect(row).toHaveAttribute("data-status", "passed")
    await row.getByRole("button", { expanded: false }).click()

    const detail = page.getByTestId(`case-run-detail-${graceful.caseRunId}`)
    await expect(detail).toBeVisible({ timeout: 60_000 })
    await expect(detail.locator('[data-testid^="step-row-"]')).toHaveCount(
      normalSeed.stepIds.length,
    )
    for (const seq of [1, normalSeed.stepIds.length]) {
      await expect(detail.getByTestId(`step-row-${seq}`)).toHaveAttribute("data-status", "passed")
    }
    const assertionRow = detail.locator('[data-testid^="assertion-row-"]').first()
    await expect(assertionRow).toHaveAttribute("data-verdict", "passed")

    // Evidence viewer: open the screenshot artifact through its results thumbnail.
    const shot = graceful.finished.evidence.find((item) => item.kind === "screenshot")
    expect(shot, "the normal run must persist screenshot evidence").toBeTruthy()
    if (shot === undefined) throw new Error("no screenshot evidence")
    const download = await getEvidence(request, token, versionId, shot.id)
    expect(download.contentType).toContain("image/png")
    expect(download.sha256).toBe(shot.sha256)
    expect(createHash("sha256").update(download.body).digest("hex")).toBe(shot.sha256)
    expect(download.body.subarray(0, PNG_SIGNATURE.length).equals(PNG_SIGNATURE)).toBe(true)

    await detail.getByTestId(`evidence-thumb-${shot.id}`).first().click()
    const viewer = page.getByTestId("evidence-viewer")
    await expect(viewer).toBeVisible({ timeout: 30_000 })
    await expect(viewer).toHaveAttribute("data-evidence-kind", "screenshot")
    await expect(viewer.getByTestId("evidence-screenshot")).toBeVisible({ timeout: 30_000 })
    await expect(viewer).toContainText("image/png")
    await expect(viewer).toContainText("sha256")
    await page.keyboard.press("Escape")
    await expect(viewer).toBeHidden({ timeout: 10_000 })
    await expectNoPasswordLeak(page, "结果与证据")
  })

  /** Step 7: report tab and the server-rendered Markdown export. */
  await test.step("Step 7 报告 Tab 与 Markdown 导出内容", async () => {
    const token = normalSeed.token
    const versionId = normalSeed.version.id
    const report = await getReport(request, token, versionId, graceful.batchId)
    expect(report.batch.id).toBe(graceful.batchId)
    expect(report.batch.batch_no).toBe(graceful.done.batch_no)
    // Restore the P6 report identity on the kept API view: the delivery report
    // header/fingerprint does not carry the baseline hash, so assert it against
    // the executed batch and keep the resolved environment context here too.
    expect(report.batch.baseline_hash, "the report must keep the executed baseline").toBe(
      graceful.done.baseline_hash,
    )
    expect(report.environment.base_url, "the report must carry the fixture base URL").toContain(
      "127.0.0.1",
    )
    const environmentName = report.environment.environment
    const systemName = report.environment.system
    expect(environmentName, "the report must name the bound environment").not.toBe("")
    expect(systemName, "the report must name the bound system").not.toBe("")
    expect(environmentName, "the two context names must be distinguishable").not.toBe(systemName)
    expect(report.summary.case_count).toBe(1)
    expect(report.summary.by_status.passed).toBe(1)
    expect(report.cases).toHaveLength(1)
    expect(report.cases[0]?.status).toBe("passed")
    expect(report.summary.evidence_count).toBeGreaterThan(0)

    // The autonomy worker normally creates revision 1 asynchronously; ensure the
    // report through the same API the UI uses first, so the UI wait never
    // depends on a worker tick alone. The revision badge below stays the real
    // UI assertion.
    const ensured = await request.post(`${API_URL}/api/versions/${versionId}/reports`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { batchId: graceful.batchId },
    })
    expect([200, 201], `ensure the batch report: unexpected HTTP ${ensured.status()}`).toContain(
      ensured.status(),
    )

    await page.getByRole("tab", { name: "报告" }).click()
    // T18 replaced the P6 execution report surface with the delivery report
    // tab: batch selector, immutable revision header and the stored snapshot.
    await expect(page.getByTestId("report-tab")).toBeVisible()
    const reportRevision = page.getByTestId("report-revision-badge")
    await expect(reportRevision).toHaveText("revision 1", { timeout: 60_000 })
    await expect(page.getByText(`Batch #${graceful.done.batch_no}`, { exact: true })).toBeVisible()

    const reportBatchSelector = page.getByTestId("report-batch-selector")
    await expect(reportBatchSelector).toBeVisible()
    await expect(reportBatchSelector).toHaveValue(graceful.batchId)
    await expect(reportBatchSelector.locator(`option[value="${graceful.batchId}"]`)).toContainText(
      `Batch #${graceful.done.batch_no}`,
    )

    await expect(page.getByTestId("report-summary")).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId("report-summary-case-count")).toHaveText("1")
    await expect(page.getByTestId("report-summary-passed")).toHaveText("1")
    await expect(page.getByTestId("report-cases")).toBeVisible()
    const reportCaseRow = page.getByTestId(`report-case-row-${graceful.caseRunId}`)
    await expect(reportCaseRow).toBeVisible()
    await expect(reportCaseRow.getByTestId("report-case-source")).toHaveAttribute(
      "data-source",
      "baseline",
    )

    // The appendix keeps the P6 report context (environment/system). Scope each
    // assertion to the <dd> following its own <dt> so a swapped pair fails; the
    // expected names come from the P6 report API, which resolves the same
    // version bindings as the delivery appendix.
    const reportAppendix = page.getByTestId("report-appendix")
    const appendixValue = (label: string) =>
      reportAppendix
        .locator("dt", { hasText: new RegExp(`^${label}$`) })
        .locator("xpath=following-sibling::dd[1]")
    await expect(appendixValue("环境"), "环境 must show the bound environment").toHaveText(
      environmentName,
    )
    await expect(appendixValue("系统"), "系统 must show the bound system").toHaveText(systemName)

    const [download] = await Promise.all([
      page.waitForEvent("download"),
      page.getByTestId("report-export-btn").click(),
    ])
    expect(download.suggestedFilename()).toMatch(/\.md$/)
    const downloadPath = await download.path()
    expect(downloadPath, "the Markdown export must produce a readable file").not.toBeNull()
    if (downloadPath === null) throw new Error("Markdown export path is unavailable")
    const markdown = readFileSync(downloadPath, "utf8")
    expect(markdown).toContain("## 1. 执行摘要")
    expect(markdown).toContain("## 3. 用例结果")
    expect(markdown).toContain(`Batch ${graceful.done.batch_no}`)
    expect(markdown).toContain("P6 登录后创建订单")
    expect(markdown.includes(ADMIN_PASSWORD), "the report must not leak the admin password").toBe(
      false,
    )
    expect(
      markdown.includes(FIXTURE_PASSWORD),
      "the report must not leak the fixture password",
    ).toBe(false)
    await expect(page.getByTestId("report-export-notice")).toContainText("已导出", {
      timeout: 30_000,
    })
    await expectNoPasswordLeak(page, "报告")
  })

  /** Step 5: immediate pause during the slow-action window → cleanup → resume. */
  await test.step("Step 5 立即暂停 → interrupted(cleanup) → 恢复后继续并通过", async () => {
    const seed = await seedExecutionVersion(request, { suffix: uniqueSuffix() })
    const token = seed.token
    const versionId = seed.version.id

    // `budget` starts with two 8s slow navigations, so a run is reliably
    // in-flight while the immediate pause dialog is confirmed.
    await setP6Mode(request, "budget")
    const batch = await createBatch(request, token, versionId)
    const batchId = batch.id
    const caseRunId = await onlyCaseRunId(request, token, versionId, batchId)
    await expectCaseRunStatus(request, token, seed, batchId, caseRunId, "running", 60_000)

    await openVersionPage(page, seed.version)
    await expect(page.getByTestId("exec-pause-btn")).toBeVisible({ timeout: 60_000 })
    await page.getByTestId("exec-pause-btn").click()
    await expect(page.getByTestId("exec-pause-dialog")).toBeVisible()
    await page.getByTestId("exec-pause-mode-immediate").click()
    await expect(page.getByTestId("exec-pause-confirm")).toHaveText("确认立即暂停")
    await expect(page.getByText("该用例将标记 interrupted")).toBeVisible()
    await page.getByTestId("exec-pause-confirm").click()

    const banner = page.getByTestId("paused-banner")
    await expect(banner).toBeVisible({ timeout: 30_000 })
    await expect(banner).toHaveAttribute("data-pause-mode", "immediate")
    await expect
      .poll(async () => (await getBatch(request, token, versionId, batchId)).paused, {
        message: "the immediate pause must flag the batch",
        timeout: 30_000,
      })
      .toBe(true)

    const interrupted = await waitCaseRun(request, token, versionId, batchId, caseRunId, {
      until: ["interrupted"],
      timeoutMs: 30_000,
    })
    expect(interrupted.status).toBe("interrupted")
    expect(interrupted.checkpoint.cleanup_required).toBe(true)
    expect(interrupted.checkpoint.interrupted_step_seq).toBe(1)
    await expectVersionEvent(request, token, versionId, "run.paused")
    const pausedEvents = await readVersionEvents(request, token, versionId)
    const runPaused = pausedEvents.find((event) => event.type === "run.paused")
    expect(runPaused?.detail?.mode).toBe("immediate")
    await expectNoPasswordLeak(page, "立即暂停")

    // Resume with the normal planner: the same attempt must run cleanup, record
    // the two remaining steps once and finish passed. The resume is dispatched
    // asynchronously, so the run stays `interrupted` until the scheduler picks
    // it up again: `interrupted` must not end this wait as a terminal status.
    await setP6Mode(request, "normal")
    await page.getByTestId("exec-resume-btn").click()
    await expect(banner).toBeHidden({ timeout: 30_000 })
    const finished = await waitCaseRun(request, token, versionId, batchId, caseRunId, {
      until: ["passed", "failed", "blocked", "inconclusive", "skipped"],
      timeoutMs: 240_000,
    })
    expect(finished.status).toBe("passed")
    expect(finished.checkpoint.cleanup_required).toBe(false)
    expect(stepSeqs(finished)).toEqual([1, 2])
    const done = await waitBatch(request, token, versionId, batchId, { timeoutMs: 240_000 })
    expect(done.status).toBe("done")
  })

  /** Step 6a: failed(application) with evidence and the results classification. */
  const failedSeed = await test.step("Step 6a 断言失败 → failed(application) + 证据", async () => {
    const seed = await seedExecutionVersion(request, { suffix: uniqueSuffix() })
    const token = seed.token
    const versionId = seed.version.id
    await setP6Mode(request, "fail")
    const batch = await createBatch(request, token, versionId)
    const caseRunId = await onlyCaseRunId(request, token, versionId, batch.id)
    const done = await waitBatch(request, token, versionId, batch.id, { timeoutMs: 240_000 })
    expect(done.status).toBe("done")

    const run = await getCaseRun(request, token, versionId, batch.id, caseRunId)
    expect(run.status).toBe("failed")
    expect(run.failure_class).toBe("application")
    expect(run.evidence.length).toBeGreaterThan(0)
    expect(run.assertions[0]?.verdict).toBe("failed")

    await openVersionPage(page, seed.version)
    await page.getByRole("tab", { name: "结果与证据" }).click()
    const row = page.getByTestId(`case-run-row-${caseRunId}`)
    await expect(row).toBeVisible({ timeout: 60_000 })
    await expect(row).toHaveAttribute("data-status", "failed")
    await expect(row.locator('[data-failure-class="application"]')).toHaveText("应用缺陷")
    await row.getByRole("button", { expanded: false }).click()
    const detail = page.getByTestId(`case-run-detail-${caseRunId}`)
    await expect(detail).toBeVisible({ timeout: 60_000 })
    await expect(detail.locator('[data-testid^="assertion-row-"]').first()).toHaveAttribute(
      "data-verdict",
      "failed",
    )
    await expectNoPasswordLeak(page, "失败结果")
    return seed
  })

  /** Step 6b: blocked(environment) for the inaccessible-target scenario. */
  await test.step("Step 6b 环境不可达 → blocked(environment)", async () => {
    const seed = await seedExecutionVersion(request, { suffix: uniqueSuffix() })
    const token = seed.token
    const versionId = seed.version.id
    await setP6Mode(request, "blocked")
    const batch = await createBatch(request, token, versionId)
    const caseRunId = await onlyCaseRunId(request, token, versionId, batch.id)
    const done = await waitBatch(request, token, versionId, batch.id, { timeoutMs: 240_000 })
    expect(done.status).toBe("done")

    const run = await getCaseRun(request, token, versionId, batch.id, caseRunId)
    expect(run.status).toBe("blocked")
    expect(run.failure_class).toBe("environment")

    await openVersionPage(page, seed.version)
    await page.getByRole("tab", { name: "结果与证据" }).click()
    const row = page.getByTestId(`case-run-row-${caseRunId}`)
    await expect(row).toBeVisible({ timeout: 60_000 })
    await expect(row).toHaveAttribute("data-status", "blocked")
    await expect(row.locator('[data-failure-class="environment"]')).toHaveText("环境问题")
    await expectNoPasswordLeak(page, "阻塞结果")
  })

  /** Step 8: negatives — non-executing 409 and executor-down 503. */
  await test.step("Step 8 负例：非 executing 版本建批 409、执行器不可用 503", async () => {
    // Step 8a runs before the executor is stopped: the health gate passes and
    // the version state gate owns the 409.
    const token = failedSeed.token
    const suffix = uniqueSuffix()
    const draft = await createVersion(request, token, {
      name: `P6 草稿版本 ${suffix}`,
      label: `p6-draft-${suffix}`,
    })
    expect(draft.status).toBe("draft")

    const conflict = await request.post(`${API_URL}/api/versions/${draft.id}/batches`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {},
    })
    expect(conflict.status()).toBe(409)
    const conflictBody = (await conflict.json()) as {
      error?: { details?: { reason?: string } }
    }
    expect(conflictBody.error?.details?.reason).toBe("version_not_executable")

    const empty = await request.get(`${API_URL}/api/versions/${draft.id}/batches`, {
      headers: { Authorization: `Bearer ${token}` },
    })
    expect(empty.status()).toBe(200)
    const emptyBody = (await empty.json()) as { data?: unknown[] }
    expect(emptyBody.data ?? []).toHaveLength(0)

    await openVersionPage(page, draft)
    await expect(page.getByTestId("exec-start-btn")).toHaveCount(0)
    await expectNoPasswordLeak(page, "草稿版本")

    // Step 8b: stop the real executor and prove the batch-create health gate
    // answers 503 executor_unavailable without persisting a batch. The
    // executor is intentionally left stopped; e2e.ps1 owns the final cleanup.
    const pid = executorPid()
    if (pid === 0) {
      test.info().annotations.push({
        type: "note",
        description: "EZTEST_E2E_EXECUTOR_PID is not set; executor-down 503 negative skipped",
      })
      return
    }
    process.kill(pid, "SIGTERM")
    await expect
      .poll(
        async () => {
          try {
            const response = await fetch(`${executorUrl()}/healthz`)
            return response.ok ? "up" : "down"
          } catch {
            return "down"
          }
        },
        { message: "the executor must stop answering health probes", timeout: 60_000 },
      )
      .toBe("down")

    // The HTTP-layer executor health gate runs before every batch-level gate,
    // so the already-completed fail version cannot surface as a 409 here: a
    // stopped executor must answer 503 executor_unavailable.
    const unavailable = await request.post(
      `${API_URL}/api/versions/${failedSeed.version.id}/batches`,
      { headers: { Authorization: `Bearer ${token}` }, data: {} },
    )
    expect(unavailable.status()).toBe(503)
    const unavailableBody = (await unavailable.json()) as {
      error?: { code?: string; details?: { reason?: string } }
    }
    expect(unavailableBody.error?.code).toBe("unavailable")
    expect(unavailableBody.error?.details?.reason).toBe("executor_unavailable")

    const untouched = await request.get(
      `${API_URL}/api/versions/${failedSeed.version.id}/batches`,
      { headers: { Authorization: `Bearer ${token}` } },
    )
    const untouchedBody = (await untouched.json()) as {
      data?: Array<{ kind?: string }>
    }
    // P7 autonomy may append a supplement batch after the failed baseline; the
    // P6 intent here is only that the 409/503 gate did not create a second
    // baseline for this untouched version.
    const baselines = (untouchedBody.data ?? []).filter(
      (batch) => batch.kind === "baseline",
    )
    expect(baselines).toHaveLength(1)
  })
})

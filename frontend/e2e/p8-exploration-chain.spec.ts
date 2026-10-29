import { execFileSync } from "node:child_process"
import { mkdirSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import {
  type APIRequestContext,
  type ConsoleMessage,
  expect,
  type Page,
  test,
} from "@playwright/test"

import { ADMIN_PASSWORD, ADMIN_USERNAME, API_URL, createVersion, login } from "./p3-helpers"
import { authHeaders } from "./p4-helpers"
import { setP5Mode } from "./p5-helpers"
import { createBatch, listCaseRuns, setP6Mode } from "./p6-helpers"
import {
  expectNoSecretInPage,
  seedP7ExecutionVersion,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitP7AnalysisSettled,
  waitP7BatchKind,
  waitP7BatchTerminal,
  waitP7CaseRun,
  waitP7Issue,
  waitVersionCompleted,
} from "./p7-helpers"

/**
 * P8 Task 13: exploration-chain E2E (design spec §5.5/§5.9).
 *
 * One serial test drives the real API, the real Playwright executor, the
 * deterministic fake LLM and the real installed Chrome:
 *
 * 1. seed an executing version, run a failing baseline batch and let the P7
 *    autonomy chain materialize the AI issue plus its passing supplement cases;
 * 2. open `?tab=exploration`, prove all four node layers render without an
 *    error/degraded banner, and read the same snapshot from the API to assert
 *    the UI counts are the real read-model counts;
 * 3. open the issue node Sheet, load its evidence and follow the issue deep link;
 * 4. collapse/expand the first requirement and assert the visible/hidden counts;
 * 5. enable 「仅异常」 and assert the passing supplement nodes disappear while
 *    the failed baseline result stays;
 * 6. abort the SSE transport and assert the chain still renders with 重连中;
 * 7. seed >100 requirements through psql and assert the `chain-truncated`
 *    banner on the second version;
 * 8. assert no unexpected console error and no credential is rendered.
 *
 * Nothing is mocked. The truncation fixture inserts reserved high ids through
 * the psql client exported by scripts/e2e.ps1 because creating 101 requirements
 * one HTTP call at a time would only slow the suite down.
 */

const SPEC_TIMEOUT_MS = 900_000
const FIXTURE_PASSWORD = "fixture-pass"
/** Reserved psql id space of the >100 requirement fixture (int64-safe). */
const TRUNCATED_REQUIREMENT_BASE_PREFIX = "8900000000"

/** Directory of the committed wave-1 acceptance evidence. */
const WAVE1_DIR = fileURLToPath(new URL("../../docs/acceptance/p8/wave1", import.meta.url))

function wave1Screenshot(name: string): string {
  mkdirSync(WAVE1_DIR, { recursive: true })
  return join(WAVE1_DIR, name)
}

// --- read-model DTOs (hand-written mirrors of the API contract) -------------

type ChainNodeKind = "requirement" | "case" | "result" | "issue"

type ChainNode = {
  id: string
  kind: ChainNodeKind
  title: string
  status: string
  source?: string
  caseRunId?: string
  caseNodeId?: string
  evidenceCount?: number
}

type ChainSnapshot = {
  versionId: string
  nodes: ChainNode[]
  edges: Array<{ id: string; source: string; target: string; kind: string }>
  counts: {
    requirements: number
    cases: number
    results: number
    issues: number
  }
  truncated: { requirements: boolean; cases: boolean; results: boolean; issues: boolean }
}

function domainIdOf(nodeId: string): string {
  const separator = nodeId.indexOf(":")
  return separator === -1 ? nodeId : nodeId.slice(separator + 1)
}

/** The canvas wrapper test id is `chain-node-<domain id>` (no kind prefix). */
function chainNodeTestId(nodeId: string): string {
  return `chain-node-${domainIdOf(nodeId)}`
}

async function readChain(
  request: APIRequestContext,
  token: string,
  versionId: string,
  query: Record<string, string> = {},
): Promise<ChainSnapshot> {
  const params = new URLSearchParams({
    limit: "300",
    requirementLimit: "100",
    ...query,
  })
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/exploration-chain?${params.toString()}`,
    { headers: authHeaders(token) },
  )
  expect(response.status(), "exploration-chain read").toBe(200)
  const body = (await response.json()) as { data: ChainSnapshot }
  return body.data
}

// --- psql fixture -----------------------------------------------------------

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
  // Passing host/port/user/database (and the password through PGPASSWORD)
  // instead of the connection URL keeps credentials out of a failed command's
  // error text.
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

/**
 * Inserts 101 requirements under one throwaway version. The reserved high id
 * range is derived from the clock so a repeated local run against the same
 * database can never collide with the previous fixture. The title is ASCII:
 * psql.exe on Windows rejects UTF-8 argv that the console code page cannot
 * decode, and the requirement title is only fixture data here.
 */
function seedTruncatedRequirements(versionId: string): void {
  expect(versionId).toMatch(/^\d+$/)
  const base = `${TRUNCATED_REQUIREMENT_BASE_PREFIX}${String(Date.now()).slice(-9)}`
  const insert = `
    INSERT INTO requirement (id, version_id, title, description, acceptance_criteria,
                             priority, status, source, last_update_source, revision,
                             created_by, updated_by)
    SELECT (${base}::bigint + g), ${versionId}::bigint,
           'P8 truncated requirement ' || g, '', '', 'must', 'clarified', 'user', 'user', 1,
           admin.id, admin.id
    FROM generate_series(1, 101) AS g
    CROSS JOIN (SELECT id FROM user_account WHERE role = 'admin' ORDER BY id ASC LIMIT 1)
      AS admin;
  `
  runPsql(insert)
  const count = runPsql(`SELECT count(*) FROM requirement WHERE version_id = ${versionId}::bigint;`)
  expect(count, "the truncation fixture must insert 101 requirements").toBe("101")
}

// --- browser helpers --------------------------------------------------------

type BrowserError = { text: string; url: string }

/**
 * Collects browser console errors and page errors with their request URL, so
 * the deliberate SSE abort can be distinguished from a real page error.
 * Next.js dev-mode favicon/404 noise is filtered like the other P8 specs do.
 */
function captureBrowserErrors(page: Page): { errors: BrowserError[]; stop: () => void } {
  const errors: BrowserError[] = []
  const onConsole = (message: ConsoleMessage): void => {
    if (message.type() !== "error") return
    const text = message.text()
    if (/favicon\.ico/i.test(text)) return
    if (/Failed to load resource: the server responded with a status of 404/i.test(text)) return
    if (/Download the React DevTools/i.test(text)) return
    errors.push({ text, url: message.location().url })
  }
  const onPageError = (error: Error): void => {
    errors.push({ text: `pageerror: ${error.message}`, url: "" })
  }
  page.on("console", onConsole)
  page.on("pageerror", onPageError)
  return {
    errors,
    stop: () => {
      page.off("console", onConsole)
      page.off("pageerror", onPageError)
    },
  }
}

/** Reads the visible-node counter (`可见 N 个节点[，折叠 M 个]`). */
async function visibleCount(page: Page): Promise<number> {
  const text = (await page.getByTestId("exploration-visible-count").textContent()) ?? ""
  const match = /可见 (\d+) 个节点/.exec(text)
  if (match === null) throw new Error(`unexpected visible-count text: ${text}`)
  return Number(match[1])
}

async function openExploration(page: Page, versionId: string): Promise<void> {
  await page.goto(`/versions/${versionId}?tab=exploration`)
  await expect(page.getByTestId("exploration-tab")).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId("exploration-error")).toHaveCount(0)
  await expect(page.getByTestId("exploration-degraded")).toHaveCount(0)
  await expect(page.getByTestId("chain-issues-degraded")).toHaveCount(0)
}

/**
 * Switches between the canvas and the list-view fallback. The floating filter
 * toolbar overlays the top of the canvas, so on a 1280x720 viewport the first
 * node row can start underneath it; the Sheet is opened from the list view so
 * the flow does not depend on the viewport scroll position.
 */
async function selectListView(page: Page): Promise<void> {
  await page.getByTestId("chain-toolbar-list").click()
  await expect(page.getByTestId("chain-list-view")).toBeVisible({ timeout: 30_000 })
}

async function selectCanvasView(page: Page): Promise<void> {
  await page.getByTestId("chain-toolbar-list").click()
  await expect(page.getByTestId("chain-canvas")).toBeVisible({ timeout: 30_000 })
}

/** Switches the chain to the version-wide batch scope. */
async function selectAllBatches(page: Page): Promise<void> {
  const filter = page.getByTestId("chain-batch-filter")
  await expect(filter).toBeVisible({ timeout: 60_000 })
  await filter.selectOption("all")
}

test.describe.configure({ mode: "serial" })

test("P8 探索链路：四类节点 / 问题 Sheet / 筛选 / 折叠 / 截断 / 重连", async ({
  page,
  request,
}) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true })) {
    return
  }

  const monitor = captureBrowserErrors(page)
  try {
    // The seed uses the P5 fake design/review scenario; keep every fake-LLM
    // profile deterministic for this spec even after a previous degraded run.
    await setP5Mode(request, "normal")
    await setP7Mode(request, "normal")
    await setP6Mode(request, "normal")

    const seeded = await test.step("seed an executing version and a failing batch", async () => {
      const seed = await seedP7ExecutionVersion(request, {
        caseTitle: "P8 探索链路失败用例",
      })
      await setP6Mode(request, "fail")
      const batch = await createBatch(request, seed.token, seed.version.id)
      const done = await waitP7BatchTerminal(
        request,
        seed.token,
        seed.version.id,
        batch.id,
        300_000,
      )
      expect(done.status).toBe("done")
      expect(done.counts.failed).toBe(1)
      const runs = await listCaseRuns(request, seed.token, seed.version.id, done.id)
      expect(runs).toHaveLength(1)
      const caseRun = await waitP7CaseRun(request, seed.token, seed.version.id, done.id, runs[0].id)
      expect(caseRun.status).toBe("failed")
      // The supplement child batch executes with the normal scenario.
      await setP6Mode(request, "normal")
      return { seed, batchId: done.id, caseRun }
    })

    const token = seeded.seed.token
    const versionId = seeded.seed.version.id
    expect(versionId).toMatch(/^\d+$/)

    const issue = await test.step("P7 analysis materializes the AI issue", async () => {
      const analyses = await waitP7AnalysisSettled(request, token, versionId, seeded.batchId, [
        "failure",
        "issue_triage",
      ])
      for (const kind of ["failure", "issue_triage"]) {
        expect(analyses.find((run) => run.kind === kind)?.status).toBe("done")
      }
      const found = await waitP7Issue(
        request,
        token,
        versionId,
        (candidate) => candidate.source === "ai" && candidate.category === "defect",
      )
      expect(found.caseRunId).toBe(seeded.caseRun.id)
      const evidenceIds = found.refs.filter((ref) => ref.kind === "evidence").map((ref) => ref.id)
      expect(evidenceIds.length).toBeGreaterThan(0)
      return { issue: found, evidenceIds }
    })

    await test.step("the autonomous chain settles with passing supplement cases", async () => {
      const child = await waitP7BatchKind(request, token, versionId, "supplement", {
        parentBatchId: seeded.batchId,
        timeoutMs: 300_000,
      })
      const childDone = await waitP7BatchTerminal(request, token, versionId, child.id, 300_000)
      expect(childDone.status).toBe("done")
      await waitVersionCompleted(request, token, versionId)
    })

    const snapshotAll = await readChain(request, token, versionId, { batchId: "all" })
    expect(snapshotAll.truncated).toEqual({
      requirements: false,
      cases: false,
      results: false,
      issues: false,
    })
    // The all-batch snapshot must contain the failing baseline plus the passing
    // supplement cases, otherwise the abnormal filter cannot be proven.
    expect(
      snapshotAll.nodes.some((node) => node.kind === "case" && node.source === "ai_supplement"),
    ).toBe(true)
    expect(
      snapshotAll.nodes.some((node) => node.kind === "result" && node.status === "passed"),
    ).toBe(true)
    expect(
      snapshotAll.nodes.some((node) => node.kind === "result" && node.status === "failed"),
    ).toBe(true)
    expect(snapshotAll.nodes.some((node) => node.id === `issue:${issue.issue.id}`)).toBe(true)

    await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
    await openExploration(page, versionId)

    await test.step("four node layers render the real read-model snapshot", async () => {
      await selectAllBatches(page)
      // Wait for the all-batch snapshot: the AI issue is baseline-only.
      await expect(page.getByTestId(chainNodeTestId(`issue:${issue.issue.id}`))).toBeVisible({
        timeout: 60_000,
      })
      await expect
        .poll(() => visibleCount(page), { timeout: 60_000 })
        .toBe(snapshotAll.nodes.length)
      for (const kind of ["requirement", "case", "result", "issue"] as const) {
        const node = snapshotAll.nodes.find((candidate) => candidate.kind === kind)
        expect(node, `the API snapshot must contain a ${kind} node`).toBeDefined()
        if (node === undefined) continue
        await expect(page.getByTestId(chainNodeTestId(node.id))).toBeVisible({ timeout: 60_000 })
        await expect(page.getByTestId(`chain-node-${kind}`).first()).toBeVisible({
          timeout: 60_000,
        })
      }
      await expect(page.getByTestId("chain-issue-count")).toContainText(
        `${snapshotAll.counts.issues} 个问题`,
      )
      await expect(page.getByTestId("chain-issue-count")).toHaveAttribute("data-state", "ready")
      await page.screenshot({ path: wave1Screenshot("p8-chain-01-overview.png"), fullPage: true })
    })

    await test.step("issue node Sheet shows evidence and the issue deep link", async () => {
      // The floating toolbar overlays the top of the canvas at 1280x720, so a
      // pointer reaches the baseline issue through the list-view fallback; the
      // Sheet itself is the same component the canvas opens.
      await selectListView(page)
      const issueRow = page.getByTestId(`chain-list-open-${issue.issue.id}`)
      await expect(issueRow).toBeVisible({ timeout: 30_000 })
      await issueRow.click()
      const sheet = page.getByTestId("chain-sheet")
      await expect(sheet).toBeVisible({ timeout: 30_000 })
      await expect(sheet).toHaveAttribute("data-node-id", `issue:${issue.issue.id}`)
      await expect(page.getByTestId("chain-sheet-evidence")).toBeVisible()
      await expect(page.getByTestId("chain-sheet-open-issue")).toBeVisible()
      const loadEvidence = page.getByTestId("chain-sheet-load-evidence")
      await expect(loadEvidence).toContainText(`加载证据（${issue.evidenceIds.length}）`)
      await loadEvidence.click()
      await expect(page.getByTestId(`chain-sheet-evidence-${issue.evidenceIds[0]}`)).toBeVisible({
        timeout: 30_000,
      })
      await page.screenshot({
        path: wave1Screenshot("p8-chain-02-issue-sheet.png"),
        fullPage: true,
      })

      await page.getByTestId("chain-sheet-open-issue").click()
      await expect(page.getByTestId("issues-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("issue-detail")).toHaveAttribute(
        "data-issue-id",
        issue.issue.id,
        { timeout: 60_000 },
      )
    })

    await test.step("collapse and expand the first requirement group", async () => {
      await openExploration(page, versionId)
      await selectAllBatches(page)
      await expect(page.getByTestId(chainNodeTestId(`issue:${issue.issue.id}`))).toBeVisible({
        timeout: 60_000,
      })
      await expect
        .poll(() => visibleCount(page), { timeout: 60_000 })
        .toBe(snapshotAll.nodes.length)
      // Use the list-view fallback so the collapse control is never behind the
      // floating toolbar; the Sheet control is the same state the canvas toggle
      // drives.
      await selectListView(page)
      await page.getByTestId(`chain-list-open-${seeded.seed.requirements[0].id}`).click()
      const sheet = page.getByTestId("chain-sheet")
      await expect(sheet).toBeVisible({ timeout: 30_000 })
      await expect(sheet).toHaveAttribute(
        "data-node-id",
        `requirement:${seeded.seed.requirements[0].id}`,
      )
      const toggle = page.getByTestId("chain-sheet-toggle-collapse")
      await expect(toggle).toBeVisible()
      const before = await visibleCount(page)
      await toggle.click()
      await expect(toggle).toHaveAttribute("aria-expanded", "false")
      await expect.poll(() => visibleCount(page), { timeout: 30_000 }).toBeLessThan(before)
      const collapsed = await visibleCount(page)
      expect(collapsed).toBeLessThan(before)
      const collapsedText =
        (await page.getByTestId("exploration-visible-count").textContent()) ?? ""
      expect(collapsedText).toContain(`，折叠 ${before - collapsed} 个`)
      await page.screenshot({
        path: wave1Screenshot("p8-chain-03-collapsed.png"),
        fullPage: true,
      })

      await toggle.click()
      await expect(toggle).toHaveAttribute("aria-expanded", "true")
      await expect.poll(() => visibleCount(page), { timeout: 30_000 }).toBe(before)
      await page.getByTestId("chain-sheet-close").click()
      await expect(sheet).toHaveCount(0, { timeout: 30_000 })
      // Return to the canvas for the remaining visual checks.
      await selectCanvasView(page)
    })

    await test.step("仅异常 keeps only the abnormal baseline result", async () => {
      const filtered = await readChain(request, token, versionId, {
        batchId: "all",
        abnormalOnly: "true",
      })
      expect(filtered.nodes.length).toBeLessThan(snapshotAll.nodes.length)
      const failedResult = filtered.nodes.find(
        (node) => node.kind === "result" && node.status === "failed",
      )
      expect(failedResult).toBeDefined()
      const passedResult = snapshotAll.nodes.find(
        (node) => node.kind === "result" && node.status === "passed",
      )
      expect(passedResult).toBeDefined()
      if (failedResult === undefined || passedResult === undefined) return

      await page.getByTestId("exploration-filter-abnormal").click()
      await expect(page).toHaveURL(/[?&]abnormal=1(?:&|$)/, { timeout: 30_000 })
      await expect.poll(() => visibleCount(page), { timeout: 60_000 }).toBe(filtered.nodes.length)
      await expect(page.getByTestId(chainNodeTestId(failedResult.id))).toBeVisible()
      await expect(page.getByTestId(chainNodeTestId(passedResult.id))).toHaveCount(0)
      await expect(page.getByTestId("exploration-filtered-empty")).toHaveCount(0)
      await expect(page.getByTestId("chain-issue-count")).toContainText(
        `${filtered.counts.issues} 个问题`,
      )
      await page.screenshot({
        path: wave1Screenshot("p8-chain-04-abnormal-filter.png"),
        fullPage: true,
      })
    })

    await test.step("a dropped SSE transport keeps the chain and shows 重连中", async () => {
      const streamRoute = "**/api/versions/*/stream*"
      await page.route(streamRoute, (route) => route.abort("failed"))
      await openExploration(page, versionId)
      const chainStatus = page.getByTestId("chain-stream-status")
      await expect(chainStatus).toContainText("重连中", { timeout: 30_000 })
      await expect
        .poll(() => chainStatus.getAttribute("data-state"), { timeout: 30_000 })
        .toMatch(/^(connecting|reconnecting)$/)
      await expect(page.getByTestId("version-stream-status")).toContainText("重连中", {
        timeout: 30_000,
      })
      // The read model is a separate REST call: the canvas must still render.
      await expect(
        page.getByTestId(chainNodeTestId(`requirement:${seeded.seed.requirements[0].id}`)),
      ).toBeVisible({ timeout: 60_000 })
      await page.screenshot({
        path: wave1Screenshot("p8-chain-05-reconnecting.png"),
        fullPage: true,
      })
      await page.unroute(streamRoute)
    })

    await test.step("more than 100 requirements renders the truncation banner", async () => {
      const truncatedVersion = await createVersion(request, token, {
        name: `P8 截断验证 ${seeded.seed.suffix}`,
        label: `p8-truncated-${seeded.seed.suffix}`,
      })
      seedTruncatedRequirements(truncatedVersion.id)

      await openExploration(page, truncatedVersion.id)
      const banner = page.getByTestId("chain-truncated")
      await expect(banner).toBeVisible({ timeout: 60_000 })
      await expect(banner).toContainText("已显示前 100 条（共 101 条）")
      await expect(banner).toContainText("需求层已截断")
      await expect(page.getByTestId("exploration-visible-count")).toHaveText("可见 100 个节点", {
        timeout: 60_000,
      })
      await page.screenshot({
        path: wave1Screenshot("p8-chain-06-truncated.png"),
        fullPage: true,
      })
    })

    await test.step("no credential leaks and no unexpected console error", async () => {
      monitor.stop()
      const body = (await page.locator("body").textContent()) ?? ""
      expect(
        /Application error|Unhandled Runtime Error/i.test(body),
        "the page must not render a Next.js error overlay",
      ).toBe(false)
      await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 探索链路")
      await expectNoSecretInPage(page, FIXTURE_PASSWORD, "P8 探索链路")
      const unexpected = monitor.errors.filter(
        (error) =>
          !(
            error.url.includes("/api/versions/") &&
            error.url.includes("/stream") &&
            /net::ERR_/i.test(error.text)
          ),
      )
      expect(unexpected, "unexpected browser console/page errors").toEqual([])
    })
  } finally {
    monitor.stop()
    await setP6Mode(request, "normal").catch(() => undefined)
    await setP7Mode(request, "normal").catch(() => undefined)
    await setP5Mode(request, "normal").catch(() => undefined)
  }
})

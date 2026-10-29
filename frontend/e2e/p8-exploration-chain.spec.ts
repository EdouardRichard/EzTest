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
 * 3. click the issue card on the real React Flow canvas (real pointer, hit-test
 *    asserted against the filter toolbar) and follow its deep link;
 * 4. collapse/expand the first requirement and assert the visible/hidden counts;
 * 5. enable 「仅异常」 and assert the passing supplement nodes disappear while
 *    the failed baseline result stays;
 * 6. abort the SSE transport and assert the chain still renders with 重连中;
 * 7. seed >100 requirements through psql and assert the `chain-truncated`
 *    banner on the second version, then remove that throwaway fixture;
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

/**
 * Removes the throwaway truncation version and its 101 requirements. The E2E
 * script does reset `eztest_e2e` before each run, but the spec still owns what
 * it creates: leaving the fixture behind would make a local dev DB (and any
 * later assertion that lists versions) see an orphan 101-requirement version.
 */
function cleanupTruncatedRequirements(versionId: string): void {
  expect(versionId).toMatch(/^\d+$/)
  runPsql(`
    DELETE FROM requirement WHERE version_id = ${versionId}::bigint;
    DELETE FROM version WHERE id = ${versionId}::bigint;
  `)
  const remaining = runPsql(
    `SELECT (SELECT count(*) FROM requirement WHERE version_id = ${versionId}::bigint)::text
            || '|' ||
            (SELECT count(*) FROM version WHERE id = ${versionId}::bigint)::text;`,
  )
  expect(remaining, "the truncation fixture must be removed after the assertion").toBe("0|0")
}

// --- browser helpers --------------------------------------------------------

type BrowserError = { text: string; url: string }

/**
 * Known dev-server noise that is not an application failure. Deliberately tiny:
 * a 404 for an API call, a Next.js chunk or any other page resource must still
 * fail the spec instead of being swallowed by a generic "404" filter.
 */
const IGNORED_CONSOLE_ERRORS: readonly RegExp[] = [/favicon\.ico/i, /Download the React DevTools/i]

/**
 * Collects browser console errors and page errors with their request URL, so
 * the deliberate SSE abort can be distinguished from a real page error. Only
 * the favicon 404 and the React DevTools hint are filtered; every other 404
 * (API included) stays in the list.
 */
function captureBrowserErrors(page: Page): { errors: BrowserError[]; stop: () => void } {
  const errors: BrowserError[] = []
  const onConsole = (message: ConsoleMessage): void => {
    if (message.type() !== "error") return
    const text = message.text()
    const url = message.location().url
    if (IGNORED_CONSOLE_ERRORS.some((pattern) => pattern.test(text))) return
    // The browser reports a failed resource fetch as a generic 404 console
    // message; only the favicon request may be ignored. An API 404 keeps its
    // entry (the URL is the fetch target, not the page) and fails the spec.
    if (
      /Failed to load resource: the server responded with a status of 404/i.test(text) &&
      /\/favicon\.ico(?:\?|$)/i.test(url)
    ) {
      return
    }
    errors.push({ text, url })
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
 * Switches between the canvas and the list-view fallback. The issue Sheet is
 * opened from the canvas with a real pointer click; the list view stays the
 * deterministic path for the requirement collapse control (a requirement card
 * can still sit under the legend overlay at the canvas' left edge).
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

    await test.step("issue node opens its Sheet from a real canvas click", async () => {
      // No list view and no programmatic selection: the real React Flow card is
      // clicked with the pointer. The toolbar owns its own row above the canvas
      // (chain-workspace-toolbar), so the top node row must be clickable at the
      // default 1280x720 viewport without panning first.
      await expect(page.getByTestId("chain-list-view")).toHaveCount(0)
      const issueNode = page.getByTestId(chainNodeTestId(`issue:${issue.issue.id}`))
      await expect(issueNode).toBeVisible({ timeout: 60_000 })
      await issueNode.scrollIntoViewIfNeeded()
      type HitProbe = {
        x: number
        y: number
        viewportWidth: number
        viewportHeight: number
        hitTestId: string
        hitInsideToolbar: boolean
        issueReceivesHit: boolean
      }
      const hit = await issueNode.evaluate((element): HitProbe => {
        const rect = element.getBoundingClientRect()
        const x = rect.left + rect.width / 2
        const y = rect.top + rect.height / 2
        const target = document.elementFromPoint(x, y)
        const toolbar = document.querySelector<HTMLElement>('[data-testid="chain-toolbar"]')
        return {
          x,
          y,
          viewportWidth: window.innerWidth,
          viewportHeight: window.innerHeight,
          hitTestId: target?.getAttribute("data-testid") ?? target?.tagName ?? "none",
          hitInsideToolbar: target !== null && toolbar?.contains(target) === true,
          issueReceivesHit: target !== null && element.contains(target),
        }
      })
      expect(
        hit.x >= 0 && hit.y >= 0 && hit.x < hit.viewportWidth && hit.y < hit.viewportHeight,
        `the issue node center (${hit.x}, ${hit.y}) must be inside the viewport`,
      ).toBe(true)
      expect(
        hit.hitInsideToolbar,
        `the issue node center must not be covered by the filter toolbar (hit ${hit.hitTestId})`,
      ).toBe(false)
      expect(
        hit.issueReceivesHit,
        `the issue node center must receive the pointer, not ${hit.hitTestId}`,
      ).toBe(true)

      await issueNode.click()
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
      // The list view stays the deterministic path for the requirement control:
      // a requirement card can still sit under the legend overlay at the
      // canvas' left edge. The Sheet control drives the same collapse state as
      // the canvas double click.
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
      try {
        seedTruncatedRequirements(truncatedVersion.id)

        await openExploration(page, truncatedVersion.id)
        const banner = page.getByTestId("chain-truncated")
        await expect(banner).toBeVisible({ timeout: 60_000 })
        await expect(banner).toContainText("已显示前 100 条（共 101 条）")
        await expect(banner).toContainText("需求层已截断")
        await expect(page.getByTestId("exploration-visible-count")).toHaveText("可见 100 个节点", {
          timeout: 60_000,
        })
        // The count label is layout-derived; wait for the rendered node layer
        // itself so the evidence screenshot never captures an empty canvas.
        await expect(page.locator('[data-testid^="chain-node-"]').first()).toBeVisible({
          timeout: 60_000,
        })
        await page.screenshot({
          path: wave1Screenshot("p8-chain-06-truncated.png"),
          fullPage: true,
        })
      } finally {
        // The spec removes the 101 requirements and the throwaway version it
        // created, so a local dev DB does not accumulate orphan fixtures.
        cleanupTruncatedRequirements(truncatedVersion.id)
      }
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

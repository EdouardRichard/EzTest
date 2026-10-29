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
  createVersion,
  login,
  startVersionClarification,
  uniqueSuffix,
} from "./p3-helpers"
import { authHeaders } from "./p4-helpers"
import { submitMindmapReview } from "./p5-helpers"
import { createBatch, waitBatch } from "./p6-helpers"
import {
  captureConsoleErrors,
  createHumanIssue,
  expectNoConsoleErrors,
  expectNoSecretInPage,
  seedP7ExecutionVersion,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitVersionStatus,
} from "./p7-helpers"

/**
 * P8 Task 34: global shell E2E (design spec §6.1/§6.2/§6.3/§8.3).
 *
 * - version switcher: current version + status, local search over the loaded
 *   versions, recent list, and preservation of the active `tab`/`batch` query
 *   across a switch;
 * - todo centre: the admin review group mixes reviewing versions and draft
 *   knowledge, and both deep links navigate to the real destination;
 * - global search: Ctrl+K palette, grouped real results (version/requirement/
 *   case/issue), Enter follows the version deep link, recent searches, Escape;
 * - phone (375×667) and tablet (768×1024) navigation: hamburger, drawer and the
 *   Escape/overlay/close controls, without horizontal overflow.
 *
 * All data is created through the real APIs with unique names; the search/todo
 * fixtures are removed in a `finally` so other specs never see them.
 */

const USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"

/** Directory of the committed P8 cross-wave acceptance evidence. */
const ACCEPTANCE_DIR = fileURLToPath(new URL("../../docs/acceptance/p8/e2e", import.meta.url))

function evidencePath(name: string): string {
  mkdirSync(ACCEPTANCE_DIR, { recursive: true })
  return join(ACCEPTANCE_DIR, name)
}

type DataEnvelope<T> = { data: T }

async function readData<T>(response: APIResponse, expected: number, label: string): Promise<T> {
  expect(response.status(), `${label}: unexpected HTTP ${response.status()}`).toBe(expected)
  const body = (await response.json()) as DataEnvelope<T>
  return body.data
}

type MindmapNodeRecord = { id: string; kind: string; title: string; revision: number }

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

/** POST /api/versions/{id}/knowledge — one draft (admin todo review item). */
async function createKnowledgeDraft(
  request: APIRequestContext,
  token: string,
  versionId: string,
  input: { kind: string; title: string; body: string; confidence: string },
): Promise<{ id: string }> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/knowledge`, {
    headers: authHeaders(token),
    data: input,
  })
  return readData<{ id: string }>(response, 201, "create knowledge draft")
}

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

async function loginAdmin(page: Page): Promise<void> {
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
}

/** Fails when the shell is wider than the viewport. */
async function expectNoHorizontalOverflow(page: Page): Promise<void> {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  )
  expect(overflow).toBeLessThanOrEqual(1)
}

// ---------------------------------------------------------------------------

test("P8 壳：版本切换器保留 Tab/批次，待办分组深链", async ({ page, request }) => {
  test.setTimeout(600_000)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true })) return

  const monitor = captureConsoleErrors(page)
  const suffix = uniqueSuffix()
  const keyword = `p8shell${suffix}`
  const reviewName = `P8 ${keyword} 评审版本`
  const knowledgeTitle = `P8 ${keyword} 知识草稿`
  let plainVersionId = ""
  let reviewVersionId = ""
  let knowledgeId = ""
  try {
    // The shell fixture batch is not the subject of this spec: keep the P7
    // agents from generating supplement cases in the background.
    await setP7Mode(request, "noop")
    const loginResponse = await request.post(`${API_URL}/api/auth/login`, {
      data: { username: ADMIN_USERNAME, password: ADMIN_PASSWORD },
    })
    const token = (await readData<{ token: string }>(loginResponse, 200, "admin login")).token

    const executionSeed = await seedP7ExecutionVersion(request, {
      caseTitle: `P8 壳切换用例 ${suffix}`,
    })
    const executionVersion = executionSeed.version
    const batch = await createBatch(request, token, executionVersion.id)
    const plainVersion = await createVersion(request, token, {
      name: `P8 ${keyword} 切换目标`,
      label: `p8-shell-${suffix}`,
    })
    plainVersionId = plainVersion.id

    await test.step("version switcher keeps the tab and batch query", async () => {
      await loginAdmin(page)
      await page.goto(
        `/versions/${encodeURIComponent(executionVersion.id)}?tab=exploration&batch=${encodeURIComponent(batch.id)}`,
      )
      const switcher = page.getByRole("button", { name: "切换版本" })
      await expect(switcher).toBeVisible({ timeout: 60_000 })
      await expect(switcher).toContainText(executionVersion.name)
      await expect(switcher).toContainText("执行中")

      await switcher.click()
      const dialog = page.getByRole("dialog", { name: "版本切换" })
      await expect(dialog).toBeVisible()
      await dialog.getByLabel("搜索版本").fill(`P8 ${keyword} 切换目标`)
      const option = page.getByTestId(`version-option-${plainVersion.id}`)
      await expect(option).toBeVisible({ timeout: 30_000 })
      await option.click()

      await expect(page).toHaveURL(
        new RegExp(`/versions/${plainVersion.id}\\?tab=exploration&batch=${batch.id}(?:&|$)`),
      )
      await expect(switcher).toContainText(`P8 ${keyword} 切换目标`)
      await expect(switcher).toContainText("草稿")
      await expect(page.getByRole("tab", { name: "探索链路" })).toHaveAttribute(
        "aria-selected",
        "true",
      )
      const recent = await page.evaluate(() => {
        const raw = window.localStorage.getItem("eztest.recentVersions") ?? "[]"
        return JSON.parse(raw) as Array<{ id: string; name: string }>
      })
      expect(recent.map((item) => item.id)).toEqual([plainVersion.id])

      // Re-opening the dropdown now shows the localStorage recent list.
      await switcher.click()
      await expect(page.getByTestId("recent-version").first()).toContainText(
        `P8 ${keyword} 切换目标`,
      )
      await page.screenshot({
        path: evidencePath("p8-shell-01-version-switcher.png"),
        fullPage: true,
      })

      // Switching back records the execution version too and keeps the same
      // tab/batch query in the other direction.
      await dialog.getByLabel("搜索版本").fill(executionVersion.name)
      const back = page.getByTestId(`version-option-${executionVersion.id}`)
      await expect(back).toBeVisible({ timeout: 30_000 })
      await back.click()
      await expect(page).toHaveURL(
        new RegExp(`/versions/${executionVersion.id}\\?tab=exploration&batch=${batch.id}(?:&|$)`),
      )
      await expect(switcher).toContainText(executionVersion.name)
      const recentAfterBack = await page.evaluate(() => {
        const raw = window.localStorage.getItem("eztest.recentVersions") ?? "[]"
        return JSON.parse(raw) as Array<{ id: string; name: string }>
      })
      expect(recentAfterBack.map((item) => item.id)).toEqual([executionVersion.id, plainVersion.id])
      await page.keyboard.press("Escape")

      // The background batch is left to settle so no executor slot leaks into
      // the next spec (workers=1, single-case concurrency).
      const settled = await waitBatch(request, token, executionVersion.id, batch.id, {
        timeoutMs: 300_000,
      })
      expect(["done", "failed"]).toContain(settled.status)
    })

    await test.step("seed one reviewing version with a case, a human issue and a knowledge draft", async () => {
      const version = await createVersion(request, token, {
        name: reviewName,
        label: `p8-shell-review-${suffix}`,
      })
      const requirement = await createRequirement(request, token, version.id, {
        title: `P8 ${keyword} 需求`,
        description: "P8 shell E2E todo fixture requirement",
        acceptanceCriteria: "The fixture is visible to the todo and search flows.",
      })
      await startVersionClarification(request, token, version.id)
      await confirmRequirement(request, token, version.id, requirement.id)
      await enterDesign(request, token, version.id)
      const scenario = await postMindmapNode(request, token, version.id, {
        kind: "scenario",
        title: `P8 ${keyword} 场景`,
        description: "P8 shell E2E scenario",
      })
      const caseNode = await postMindmapNode(request, token, version.id, {
        kind: "case",
        parentId: scenario.id,
        title: `P8 ${keyword} 用例`,
        priority: "P1",
        requirementIds: [requirement.id],
      })
      // submit-review requires every case to carry at least one step and one
      // assertion (domain.ValidateGraph), even though this fixture only needs
      // the reviewing status.
      const step = await request.post(
        `${API_URL}/api/versions/${version.id}/mindmap/nodes/${caseNode.id}/steps`,
        {
          headers: authHeaders(token),
          data: { action: "打开概览页", expected: "概览页可见" },
        },
      )
      const stepBody = await readData<{ id: string }>(step, 201, "create review fixture step")
      const assertion = await request.post(
        `${API_URL}/api/versions/${version.id}/mindmap/nodes/${caseNode.id}/assertions`,
        {
          headers: authHeaders(token),
          data: {
            stepId: stepBody.id,
            kind: "ui",
            target: "#main-content",
            expression: "text contains EzTest",
            expected: "EzTest",
          },
        },
      )
      await readData<{ id: string }>(assertion, 201, "create review fixture assertion")
      await submitMindmapReview(request, token, version.id)
      await waitVersionStatus(request, token, version.id, "reviewing", { timeoutMs: 60_000 })

      const issue = await createHumanIssue(request, token, version.id, {
        title: `P8 ${keyword} 问题`,
        severity: "minor",
        description: "P8 shell E2E search fixture issue",
      })
      const knowledge = await createKnowledgeDraft(request, token, version.id, {
        kind: "pattern",
        title: knowledgeTitle,
        body: `P8 shell E2E knowledge draft ${suffix}`,
        confidence: "medium",
      })
      reviewVersionId = version.id
      knowledgeId = knowledge.id
      expect(issue.id).toMatch(/^\d+$/)
      return { version, requirement, issue, knowledge }
    })

    await test.step("todo centre groups the review items and follows both deep links", async () => {
      await page.goto("/")
      await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 })
      await page.getByRole("button", { name: "待办中心" }).click()
      const panel = page.getByTestId("todo-panel")
      await expect(panel).toBeVisible({ timeout: 60_000 })
      const reviewGroup = panel.locator('[data-testid="todo-group"][data-kind="review"]')
      await expect(reviewGroup).toBeVisible({ timeout: 60_000 })
      const reviewCount = Number(
        (await panel.getByTestId("todo-count-review").textContent())?.trim() ?? "0",
      )
      expect(reviewCount).toBeGreaterThanOrEqual(2)
      await expect(panel.locator('[data-testid="todo-group"][data-kind="takeover"]')).toHaveCount(0)
      await expect(panel.locator('[data-testid="todo-group"][data-kind="gate"]')).toHaveCount(0)

      const versionLink = panel.locator(`a[href="/versions/${reviewVersionId}?tab=mindmap"]`)
      await expect(versionLink).toBeVisible()
      await expect(versionLink).toContainText(reviewName)
      await page.screenshot({
        path: evidencePath("p8-shell-02-todo-review.png"),
        fullPage: true,
      })
      await versionLink.click()
      await expect(panel).toHaveCount(0)
      await expect(page).toHaveURL(new RegExp(`/versions/${reviewVersionId}\\?tab=mindmap`))
      await expect(page.getByTestId("version-tab-mindmap")).toBeVisible({ timeout: 60_000 })

      await page.getByRole("button", { name: "待办中心" }).click()
      await expect(panel).toBeVisible()
      const knowledgeLink = panel.locator(`a[href="/knowledge?review=${knowledgeId}"]`)
      await expect(knowledgeLink).toBeVisible()
      await expect(knowledgeLink).toContainText(knowledgeTitle)
      await knowledgeLink.click()
      await expect(page).toHaveURL(new RegExp(`/knowledge\\?review=${knowledgeId}`))
      const drawer = page.getByTestId("knowledge-review-drawer")
      await expect(drawer).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId(`knowledge-review-item-${knowledgeId}`)).toHaveAttribute(
        "aria-current",
        "true",
      )
      await drawer.getByRole("button", { name: "关闭审核抽屉" }).click()
    })

    await test.step("global search groups real results, opens with Enter and remembers the query", async () => {
      await page.goto("/")
      await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 })

      await page.keyboard.press("Control+k")
      const palette = page.getByRole("dialog", { name: "全局搜索" })
      await expect(palette).toBeVisible({ timeout: 30_000 })
      const input = palette.getByLabel("搜索关键词")
      await input.fill(keyword)
      await expect(palette.getByTestId("search-group")).toHaveCount(4, { timeout: 30_000 })
      for (const kind of ["version", "requirement", "case", "issue"]) {
        await expect(
          palette.locator(`[data-testid="search-group"][data-kind="${kind}"]`),
        ).toBeVisible()
      }
      await page.screenshot({
        path: evidencePath("p8-shell-03-global-search.png"),
        fullPage: true,
      })

      // Enter follows the first (version) deep link.
      await input.press("Enter")
      await expect(palette).toHaveCount(0)
      await expect(page).toHaveURL(new RegExp(`/versions/${reviewVersionId}\\?tab=overview(?:&|$)`))
      await expect(page.getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true")

      // The selected query is remembered; re-opening shows the recent search.
      await page.keyboard.press("Control+k")
      await expect(palette).toBeVisible()
      await expect(palette.getByTestId("recent-searches")).toBeVisible({ timeout: 30_000 })
      await expect(palette.getByTestId("recent-search").first()).toContainText(keyword)
      await page.keyboard.press("Escape")
      await expect(palette).toHaveCount(0)

      // `/` is the second documented shortcut.
      await page.keyboard.press("/")
      await expect(palette).toBeVisible({ timeout: 30_000 })
      await page.keyboard.press("Escape")
      await expect(palette).toHaveCount(0)
    })

    await test.step("375px and 768px navigation open, close and navigate the drawer", async () => {
      await page.setViewportSize({ width: 375, height: 667 })
      const hamburger = page.getByRole("button", { name: "导航菜单", exact: true })
      const drawer = page.getByRole("dialog", { name: "主导航菜单" })
      await expect(page.locator("aside")).toBeHidden()
      await expect(hamburger).toBeVisible()
      await expect(hamburger).toHaveAttribute("aria-expanded", "false")
      await expect(hamburger).toHaveAttribute("aria-controls", "mobile-nav-drawer")
      await expectNoHorizontalOverflow(page)

      await hamburger.click()
      await expect(drawer).toBeVisible()
      await expect(hamburger).toHaveAttribute("aria-expanded", "true")
      await page.screenshot({
        path: evidencePath("p8-shell-04-mobile-nav-375.png"),
        fullPage: true,
      })
      await page.keyboard.press("Escape")
      await expect(drawer).toBeHidden()
      await expect(hamburger).toHaveAttribute("aria-expanded", "false")

      // Overlay close (the drawer owns the left 288px; click the exposed right).
      await hamburger.click()
      await page.getByTestId("mobile-nav-overlay").click({ position: { x: 350, y: 320 } })
      await expect(drawer).toBeHidden()

      // Explicit close control.
      await hamburger.click()
      await expect(drawer).toBeVisible()
      await drawer.getByRole("button", { name: "关闭导航菜单" }).click()
      await expect(drawer).toBeHidden()

      // Route navigation closes the drawer after following a destination.
      await hamburger.click()
      await drawer.getByRole("link", { name: /^版本/ }).click()
      await expect(page).toHaveURL(/\/versions/)
      await expect(drawer).toBeHidden()
      await expectNoHorizontalOverflow(page)

      await page.setViewportSize({ width: 768, height: 1024 })
      await expect(page.locator("aside")).toBeHidden()
      await expect(hamburger).toBeVisible()
      await hamburger.click()
      await expect(drawer).toBeVisible()
      await expect(drawer.getByRole("link", { name: /^概览/ })).toBeVisible()
      await expectNoHorizontalOverflow(page)
      await page.screenshot({
        path: evidencePath("p8-shell-05-tablet-nav-768.png"),
        fullPage: true,
      })
      await page.keyboard.press("Escape")
      await expect(drawer).toBeHidden()
    })

    await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 壳")
    await expectNoSecretInPage(page, USER_PASSWORD, "P8 壳")
    await expectNoConsoleErrors(monitor, page, "P8 壳")
  } finally {
    monitor.stop()
    await setP7Mode(request, "normal").catch(() => undefined)
    try {
      if (knowledgeId !== "") {
        runPsql(`DELETE FROM knowledge_entry WHERE id = ${knowledgeId}::bigint;`)
      }
      if (reviewVersionId !== "") {
        runPsql(`
          DELETE FROM knowledge_entry WHERE version_id = ${reviewVersionId}::bigint;
          DELETE FROM issue WHERE version_id = ${reviewVersionId}::bigint;
          DELETE FROM case_node WHERE version_id = ${reviewVersionId}::bigint;
          DELETE FROM requirement WHERE version_id = ${reviewVersionId}::bigint;
          DELETE FROM version WHERE id = ${reviewVersionId}::bigint;
        `)
      }
      if (plainVersionId !== "") {
        runPsql(`DELETE FROM version WHERE id = ${plainVersionId}::bigint;`)
      }
    } catch {
      // Raw Playwright runs without the e2e env skip the DB cleanup.
    }
  }
})

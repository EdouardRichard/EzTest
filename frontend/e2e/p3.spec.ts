import fs from "node:fs"
import path from "node:path"

import { type APIRequestContext, expect, test } from "@playwright/test"

/**
 * P3 execution-workspace browser E2E (real LLM; spec 13.2 / scripts/p3-e2e.ps1).
 *
 * The acceptance script prepares one confirmed slow-page case through SQL + the
 * P2 design confirmation API and exports `EZTEST_E2E_P3_VERSION_ID`. Without
 * that variable the suite falls back to the P2-style API setup: create the LLM
 * config/SUT/version/requirement, run the design conversation (real LLM) and
 * confirm the generated case -- it never injects a fake execution path.
 */

const API = process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:14333"
const VERSION_ID_ENV = process.env.EZTEST_E2E_P3_VERSION_ID?.trim() || ""
const LLM_BASE =
  process.env.EZTEST_E2E_LLM_BASE_URL?.trim() || process.env.EZTEST_E2E_FAKE_LLM_URL?.trim() || ""
const LLM_KEY = process.env.EZTEST_E2E_LLM_KEY?.trim() || ""
const LLM_MODEL = process.env.EZTEST_E2E_LLM_MODEL?.trim() || "deepseek-flash"
const CASE_TIMEOUT_MS = Number(process.env.EZTEST_E2E_CASE_TIMEOUT_MS || "780000")
const TEST_TIMEOUT_MS = Number(process.env.EZTEST_E2E_P3_TIMEOUT_MS || "900000")

type Setup = { versionID: string }

let setup: Setup
let token = ""

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

/**
 * P2-style fallback setup: build a version, clarify one requirement, let the
 * real-LLM design conversation author a case, confirm it, and return the
 * version id. `scripts/p3-e2e.ps1` normally sets EZTEST_E2E_P3_VERSION_ID so
 * this path only runs for a manual `playwright test` against a prepared stack.
 */
async function createVersionThroughDesign(request: APIRequestContext): Promise<string> {
  if (LLM_BASE === "" || LLM_KEY === "") {
    throw new Error(
      "EZTEST_E2E_P3_VERSION_ID is unset and EZTEST_E2E_LLM_BASE_URL/EZTEST_E2E_LLM_KEY are missing; cannot create a real-LLM design case",
    )
  }
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 1000)}`
  const config = await api<{ data: { id: string } }>(request, "POST", "/api/admin/llm/configs", {
    name: `p3-e2e-llm-${suffix}`,
    provider: "openai",
    protocol: "openai",
    base_url: LLM_BASE,
    api_key: LLM_KEY,
    timeout_seconds: 120,
    max_retries: 1,
    is_default: true,
  })
  await api(
    request,
    "POST",
    `/api/admin/llm/configs/${config.data.id}/refresh-models`,
    undefined,
    200,
  )
  // The runner resolves the first catalog model of the default config when the
  // profile has no model override; make `llm_model_name` that first model.
  let catalog = await api<{ data: Array<{ id: string; model: string }> }>(
    request,
    "GET",
    "/api/admin/llm/models",
  )
  if (!catalog.data.some((item) => item.model === LLM_MODEL)) {
    await api(request, "POST", `/api/admin/llm/configs/${config.data.id}/models`, {
      model: LLM_MODEL,
      context_window: 128000,
      max_output: 8192,
      supports_tools: true,
      supports_vision: false,
    })
    catalog = await api<{ data: Array<{ id: string; model: string }> }>(
      request,
      "GET",
      "/api/admin/llm/models",
    )
  }
  for (const item of catalog.data) {
    if (item.model === LLM_MODEL) break
    await api(
      request,
      "DELETE",
      `/api/admin/llm/configs/${config.data.id}/models/${item.id}`,
      undefined,
      200,
    )
  }

  const sut = await api<{ data: { id: string } }>(
    request,
    "POST",
    "/api/resources/suts",
    { name: `P3 E2E SUT ${suffix}`, intro: "P3 execution E2E fallback" },
    201,
  )
  const environment = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${sut.data.id}/environments`,
    { name: `e2e-${suffix}`, base_url: "http://127.0.0.1:13335", is_default: true },
    201,
  )
  const page = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/environments/${environment.data.id}/pages`,
    {
      name: "P3 slow page",
      url: "http://127.0.0.1:13335/p3/slow",
      route_template: `/p3/slow-${suffix}`,
      page_type: "page",
      auth_role: "guest",
      module: "p3",
    },
    201,
  )
  const account = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${sut.data.id}/accounts`,
    {
      environment_id: environment.data.id,
      username: "fixture",
      password: "fixture-pass",
      roles: ["user"],
      status: "active",
    },
    201,
  )
  const version = await api<{ data: { id: string; bindings: Array<{ id: string }> } }>(
    request,
    "POST",
    "/api/versions",
    {
      title: `P3 E2E fallback ${suffix}`,
      bindings: [
        {
          sut_id: sut.data.id,
          environment_id: environment.data.id,
          page_ids: [page.data.id],
          api_ids: [],
          database_ids: [],
          accounts: [{ account_id: account.data.id, role: "user" }],
        },
      ],
    },
    201,
  )
  const versionSUTID = version.data.bindings[0]?.id
  if (!versionSUTID) throw new Error("version has no binding id")
  const requirement = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/versions/${version.data.id}/requirements`,
    {
      title: `P3 E2E slow page ${suffix}`,
      description: "The slow page executes and produces evidence",
      acceptance_criteria: "The slow page result is visible",
      priority: "P1",
      constraints: {},
      targets: { version_suts: [versionSUTID], pages: [page.data.id], apis: [] },
    },
    201,
  )
  await api(
    request,
    "POST",
    `/api/versions/${version.data.id}/requirements/${requirement.data.id}/confirmations`,
    undefined,
    200,
  )
  await api(
    request,
    "POST",
    `/api/versions/${version.data.id}/conversations`,
    { kind: "design", title: `P3 E2E design ${suffix}` },
    201,
  )

  const deadline = Date.now() + 300_000
  let cases: Array<{ case_id: string; status: string }> = []
  while (Date.now() < deadline) {
    const graph = await api<{
      data: {
        requirements?: Array<{
          scenarios?: Array<{ cases?: Array<{ case_id: string; status: string }> }>
        }>
      }
    }>(request, "GET", `/api/versions/${version.data.id}/design/graph`)
    cases = (graph.data.requirements ?? []).flatMap((item) =>
      (item.scenarios ?? []).flatMap((scenario) => scenario.cases ?? []),
    )
    if (cases.some((item) => item.status === "draft")) break
    await new Promise((resolve) => setTimeout(resolve, 2000))
  }
  const draftCases = cases.filter((item) => item.status === "draft")
  if (draftCases.length === 0) throw new Error("design conversation authored no draft case")
  for (const item of draftCases.slice(0, 2)) {
    await api(request, "POST", `/api/versions/${version.data.id}/design/confirmations`, {
      scope: "case",
      target_id: item.case_id,
    })
  }
  return version.data.id
}

test.beforeAll(async ({ request }) => {
  token = await login(request)
  const versionID =
    VERSION_ID_ENV !== "" ? VERSION_ID_ENV : await createVersionThroughDesign(request)
  setup = { versionID }
})

test("P3 execution workspace: auto wave, live results, evidence, pause/resume and retest", async ({
  page,
}) => {
  test.setTimeout(TEST_TIMEOUT_MS)
  const pageErrors: string[] = []
  page.on("pageerror", (error) => pageErrors.push(String(error)))

  await page.goto("/login")
  await page.fill("#username", process.env.EZTEST_E2E_USERNAME?.trim() || "admin")
  await page.fill("#password", process.env.EZTEST_E2E_PASSWORD?.trim() || "admin123")
  await page.getByRole("button", { name: "登录" }).click()
  await page.waitForURL("**/versions")

  await page.goto(`/versions/${setup.versionID}?tab=execution`)
  await expect(page.getByTestId("execution-tab")).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId("execution-toolbar")).toBeVisible()
  await expect(page.getByTestId("wave-list")).toBeVisible()
  await expect(page.getByTestId("live-trace")).toBeVisible()
  await expect(page.getByTestId("results-panel")).toBeVisible()

  // Execution is intent-driven: no "start execution" action may exist.
  await expect(page.getByRole("button", { name: /开始执行/ })).toHaveCount(0)

  // A confirmed case generates an intent and a wave without user action.
  const caseRunRows = page.locator('[data-testid^="case-run-row-"]')
  await expect(caseRunRows.first()).toBeVisible({ timeout: CASE_TIMEOUT_MS })

  // Pause/resume the active wave while the case is in flight. Best-effort:
  // the wave may already be finished, in which case no pause button is shown.
  const pauseButton = page.getByTestId("execution-toolbar").getByRole("button", { name: "暂停" })
  if ((await pauseButton.count()) > 0) {
    await pauseButton.click()
    const dialog = page.getByRole("dialog", { name: "暂停执行" })
    await expect(dialog).toBeVisible()
    await dialog.getByRole("radio", { name: /立即暂停/ }).click()
    await dialog.getByRole("checkbox", { name: /我了解立即暂停/ }).click()
    await dialog.getByRole("button", { name: "暂停", exact: true }).click()
    await expect(page.getByText("执行已暂停").first()).toBeVisible({ timeout: 30_000 })
    await page.getByRole("button", { name: "恢复" }).first().click()
    await expect(page.getByText("执行已暂停")).toHaveCount(0, { timeout: 60_000 })
  }

  // The deterministic failing case must reach failed/blocked: retest (§5.8) is
  // only allowed for a case whose latest result did not pass.
  const failedRow = caseRunRows.filter({ hasText: /失败|阻塞|未定/ }).first()
  await expect(failedRow).toBeVisible({ timeout: CASE_TIMEOUT_MS })

  // Open the case-run detail and the immutable evidence viewer. Prefer the
  // failing case, fall back to the passing one when evidence has not landed yet.
  const passedRow = caseRunRows.filter({ hasText: /通过/ }).first()
  let detailRow = failedRow
  await detailRow.getByRole("button").first().click()
  await expect(page.getByTestId("case-run-detail")).toBeVisible()
  let evidenceTrigger = page
    .getByTestId("case-run-detail")
    .locator("button", { hasText: /截图|快照|文本|HTTP|数据库|下载/ })
  if ((await evidenceTrigger.count()) === 0 && (await passedRow.count()) > 0) {
    detailRow = passedRow
    await detailRow.getByRole("button").first().click()
    await expect(page.getByTestId("case-run-detail")).toBeVisible()
    evidenceTrigger = page
      .getByTestId("case-run-detail")
      .locator("button", { hasText: /截图|快照|文本|HTTP|数据库|下载/ })
  }
  if ((await evidenceTrigger.count()) > 0) {
    await evidenceTrigger.first().click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText("不可变证据")).toBeVisible()
    const image = page.getByTestId("evidence-image")
    const text = page.getByTestId("evidence-text")
    await expect(image.or(text).first()).toBeVisible({ timeout: 30_000 })
    await page.getByRole("button", { name: "关闭" }).click()
  } else {
    test.info().annotations.push({
      type: "note",
      description: "no evidence trigger was rendered for the prepared cases",
    })
  }

  // Retest the failing case: the inline confirmation creates a purpose=retest
  // intent; once the active wave finishes the scheduler starts a new wave.
  await failedRow.getByRole("button", { name: "重跑" }).click()
  await failedRow.getByRole("button", { name: "确认重跑" }).click()
  await expect(page.getByTestId("wave-list")).toContainText(/2 个波次|[3-9] 个波次/, {
    timeout: CASE_TIMEOUT_MS,
  })

  // Human takeover cards are only present when the LLM asked for a human; the
  // acceptance script asserts that scenario for the dedicated 2FA case.
  const humanBanner = page.getByTestId("human-request-banner")
  if ((await humanBanner.count()) > 0) {
    await expect(humanBanner).toBeVisible()
  }

  const screenshotDir =
    process.env.EZTEST_E2E_SCREENSHOT_DIR?.trim() ||
    path.resolve(process.cwd(), "..", ".tmp", "p3-acceptance", "screenshots")
  fs.mkdirSync(screenshotDir, { recursive: true })
  await page.screenshot({
    path: path.join(screenshotDir, "execution-workspace.png"),
    fullPage: true,
  })
  if ((await page.getByTestId("case-run-detail").count()) > 0) {
    await page.getByTestId("case-run-detail").screenshot({
      path: path.join(screenshotDir, "case-run-detail.png"),
    })
  }

  expect(pageErrors).toEqual([])
})

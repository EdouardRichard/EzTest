import { execFileSync } from "node:child_process"
import { createHash } from "node:crypto"
import { mkdirSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import {
  type APIRequestContext,
  type APIResponse,
  expect,
  type Locator,
  type Page,
  test,
} from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  API_URL,
  createRequirement,
  createVersion,
  login,
  uniqueSuffix,
} from "./p3-helpers"
import { authHeaders } from "./p4-helpers"
import { type MindmapGraph, seedMindmapVersion, submitMindmapReview } from "./p5-helpers"
import {
  type Batch,
  type CaseRun,
  fixtureTargetUrl,
  getEvidence,
  listCaseRuns,
  sleep,
  waitBatch,
} from "./p6-helpers"
import {
  captureConsoleErrors,
  expectNoConsoleErrors,
  expectNoSecretInPage,
  getP7Version,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitP7BatchKind,
  waitP7BatchTerminal,
  waitVersionCompleted,
  waitVersionStatus,
} from "./p7-helpers"

/**
 * P8 Task 34: cross-wave quality platform E2E (design spec §9.3/§9.4/§9.1).
 *
 * Two real-UI flows and one permission check:
 *
 * 1. knowledge: an ordinary user submits a draft in the version overview, an
 *    administrator approves it in the `/knowledge` review drawer, and the
 *    approved entry becomes visible to both accounts in the global read;
 * 2. visual assertion: a real baseline batch executes the visual assertion
 *    without a baseline (inconclusive(case, missing_visual_baseline)), the
 *    administrator promotes that run's real screenshot evidence to the approved
 *    baseline, a continuing incremental batch re-executes the same case, and the
 *    run stores `visual_actual` + `visual_diff` evidence classified as passed;
 * 3. `/quality`: the administrator sees the recorded KPI dashboard while a
 *    normal user sees the denial page and the API answers 403.
 *
 * Nothing is mocked: the visual comparison runs in the real Playwright executor
 * against the fixture target, the batch is started from the version detail UI,
 * and the baseline is promoted from a screenshot the executor actually
 * captured. The fake LLM only drives the deterministic execution-agent turns.
 */

const SPEC_TIMEOUT_MS = 600_000
const USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"
const VISUAL_VIEWPORT = { width: 1280, height: 720 }
const VISUAL_DPR = 1
const VISUAL_THRESHOLD = 0.05

/** Directory of the committed P8 cross-wave acceptance evidence. */
const ACCEPTANCE_DIR = fileURLToPath(new URL("../../docs/acceptance/p8/e2e", import.meta.url))

function evidencePath(name: string): string {
  mkdirSync(ACCEPTANCE_DIR, { recursive: true })
  return join(ACCEPTANCE_DIR, name)
}

// --- API envelope -----------------------------------------------------------

type DataEnvelope<T> = { data: T }

async function readData<T>(response: APIResponse, expected: number, label: string): Promise<T> {
  expect(response.status(), `${label}: unexpected HTTP ${response.status()}`).toBe(expected)
  const body = (await response.json()) as DataEnvelope<T>
  return body.data
}

// --- fixture definitions (mirror of the P5/P6/P7 seed primitives) ------------

type VisualSeed = {
  token: string
  suffix: string
  versionId: string
  caseNodeId: string
  visualAssertionId: string
  uiAssertionId: string
  requirementId: string
  fixtureUrl: string
  environmentId: number
  accessId: number
  sessionId: number
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
  body: {
    stepId?: string
    kind: string
    target?: string
    expression: string
    expected: string
  },
): Promise<MindmapAssertionRecord> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/mindmap/nodes/${nodeId}/assertions`,
    { headers: authHeaders(token), data: body },
  )
  return readData<MindmapAssertionRecord>(response, 201, "create mindmap assertion")
}

/** One Set-Cookie value from a fetch Response (Node 24). */
function setCookieValue(response: Response, name: string): string {
  const values =
    typeof response.headers.getSetCookie === "function" ? response.headers.getSetCookie() : []
  const fallback = response.headers.get("set-cookie")
  const candidates = values.length > 0 ? values : fallback === null ? [] : [fallback]
  for (const value of candidates) {
    for (const part of value.split(",")) {
      const pair = part.split(";")[0]?.trim() ?? ""
      const separator = pair.indexOf("=")
      if (separator > 0 && pair.slice(0, separator) === name) {
        return pair.slice(separator + 1)
      }
    }
  }
  return ""
}

/** Imports one valid fixture session for the seeded access (P6 seed helper). */
async function importFixtureSession(
  request: APIRequestContext,
  token: string,
  input: { fixtureUrl: string; environmentId: number; accessId: number },
): Promise<number> {
  const response = await fetch(`${input.fixtureUrl}/p6/login`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ username: "fixture", password: "fixture-pass" }).toString(),
    redirect: "manual",
  })
  expect(response.status, "fixture P6 login").toBe(302)
  const cookie = setCookieValue(response, "p6_session")
  expect(cookie, "fixture P6 login must set p6_session").not.toBe("")
  const fixture = new URL(input.fixtureUrl)
  const imported = await request.post(
    `${API_URL}/api/resources/accesses/${input.accessId}/sessions`,
    {
      headers: authHeaders(token),
      data: {
        environmentId: input.environmentId,
        kind: "storage_state",
        storageState: {
          cookies: [
            {
              name: "p6_session",
              value: cookie,
              domain: fixture.hostname,
              path: "/",
              expires: -1,
              httpOnly: true,
              secure: false,
              sameSite: "Lax",
            },
          ],
          origins: [{ origin: fixture.origin, localStorage: [], sessionStorage: [] }],
        },
        note: "P8 visual assertion E2E imported fixture session",
      },
    },
  )
  const session = await readData<{ id: number; state: string }>(
    imported,
    201,
    "import fixture session",
  )
  expect(session.state).toBe("valid")
  return session.id
}

/**
 * Seeds one executing version whose case carries a UI assertion and a visual
 * assertion configured for the full viewport (1280x720, DPR 1). The visual
 * baseline is intentionally unapproved until the first real run produced a
 * screenshot: that is the documented baseline-candidate flow of spec §9.3.
 */
async function seedVisualVersion(
  request: APIRequestContext,
  token: string,
  suffix: string,
): Promise<VisualSeed> {
  const base = await seedMindmapVersion(request, {
    suffix,
    requirementTitles: [`P8 视觉需求 ${suffix}`, `P8 视觉需求二 ${suffix}`],
  })
  const versionId = base.version.id
  const fixtureUrl = fixtureTargetUrl()

  const envResponse = await request.patch(
    `${API_URL}/api/resources/environments/${base.environmentId}`,
    {
      headers: authHeaders(token),
      data: {
        allowAutoLogin: true,
        loginCheck: { url: "/p6/home", expectStatus: [200], bodyContains: "P6 Home" },
      },
    },
  )
  await readData<unknown>(envResponse, 200, "patch visual environment")

  const loginUrl = `${fixtureUrl}/p6/login`
  const authFlowResponse = await request.post(`${API_URL}/api/resources/auth-flows`, {
    headers: authHeaders(token),
    data: {
      system_id: String(base.systemId),
      name: `P8 visual login ${suffix}`,
      kind: "form",
      definition: {
        login_url: loginUrl,
        username_selector: "#username",
        password_selector: "#password",
        submit_selector: "button[type=submit]",
        success: { url_contains: "/p6/home" },
        failure: { invalid_selector: ".login-error", mfa_selector: "#mfa" },
        timeout_ms: 10_000,
      },
      enabled: true,
    },
  })
  const authFlow = await readData<{ id: string }>(authFlowResponse, 201, "create visual auth flow")

  const accountResponse = await request.post(`${API_URL}/api/resources/accounts`, {
    headers: authHeaders(token),
    data: {
      key: `p8-visual-account-${suffix}`,
      label: `P8 Visual Account ${suffix}`,
      username: "fixture",
      kind: "human",
    },
  })
  const account = await readData<{ id: number }>(accountResponse, 201, "create visual account")

  const accessResponse = await request.post(
    `${API_URL}/api/resources/accounts/${account.id}/accesses`,
    {
      headers: authHeaders(token),
      data: {
        systemId: base.systemId,
        authType: "password",
        concurrencyLimit: 1,
        credential: "fixture-pass",
        usernameOverride: "fixture",
        loginUrl,
        login_flow_id: authFlow.id,
        enabled: true,
      },
    },
  )
  const access = await readData<{ id: number }>(accessResponse, 201, "create visual access")

  const scenario = await postMindmapNode(request, token, versionId, {
    kind: "scenario",
    title: `P8 视觉场景 ${suffix}`,
    description: "P8 visual assertion E2E scenario",
  })
  const caseNode = await postMindmapNode(request, token, versionId, {
    kind: "case",
    parentId: scenario.id,
    title: `P8 视觉下单用例 ${suffix}`,
    priority: "P0",
    preconditions: "已通过 fixture 会话登录",
    requirementIds: [base.requirements[0]?.id].filter((id): id is string => id !== undefined),
  })
  const firstStep = await postMindmapStep(request, token, versionId, caseNode.id, {
    action: "打开 /p6/orders/new 并填写订单号",
    expected: "新建订单表单可见",
  })
  const secondStep = await postMindmapStep(request, token, versionId, caseNode.id, {
    action: "提交新建订单并确认创建成功",
    expected: "订单详情显示新订单号",
  })
  const uiAssertion = await postMindmapAssertion(request, token, versionId, caseNode.id, {
    stepId: secondStep.id,
    kind: "ui",
    target: "#order-number",
    expression: "text contains P6-E2E-ORDER",
    expected: "P6-E2E-ORDER",
  })
  const visualAssertion = await postMindmapAssertion(request, token, versionId, caseNode.id, {
    stepId: secondStep.id,
    kind: "visual",
    target: "订单详情全页",
    expression: "pixelmatch",
    expected: "视觉一致",
  })
  expect(firstStep.id).toMatch(/^\d+$/)

  const graphResponse = await request.get(`${API_URL}/api/versions/${versionId}/mindmap`, {
    headers: authHeaders(token),
  })
  const graph = await readData<MindmapGraph>(graphResponse, 200, "read visual graph")
  const graphNode = graph.nodes.find((node) => node.id === caseNode.id)
  expect(graphNode, "the visual case must be in the graph").toBeDefined()
  const visualResponse = await request.put(
    `${API_URL}/api/versions/${versionId}/mindmap/assertions/${visualAssertion.id}/visual`,
    {
      headers: authHeaders(token),
      data: {
        nodeRevision: graphNode?.revision ?? 1,
        selector: "",
        threshold: VISUAL_THRESHOLD,
        viewport: VISUAL_VIEWPORT,
        deviceScaleFactor: VISUAL_DPR,
      },
    },
  )
  const visual = await readData<{
    threshold: number
    viewportWidth: number
    viewportHeight: number
  }>(visualResponse, 200, "configure visual assertion")
  expect(visual.threshold).toBe(VISUAL_THRESHOLD)
  expect([visual.viewportWidth, visual.viewportHeight]).toEqual([
    VISUAL_VIEWPORT.width,
    VISUAL_VIEWPORT.height,
  ])

  await submitMindmapReview(request, token, versionId)
  const approvedResponse = await request.post(
    `${API_URL}/api/versions/${versionId}/mindmap/review`,
    {
      headers: authHeaders(token),
      data: { decision: "approve", acknowledgeFindings: true },
    },
  )
  const approved = await readData<{ version: { status: string } }>(
    approvedResponse,
    200,
    "approve visual mindmap",
  )
  expect(approved.version.status).toBe("executing")

  const sessionId = await importFixtureSession(request, token, {
    fixtureUrl,
    environmentId: base.environmentId,
    accessId: access.id,
  })

  return {
    token,
    suffix,
    versionId,
    caseNodeId: caseNode.id,
    visualAssertionId: visualAssertion.id,
    uiAssertionId: uiAssertion.id,
    requirementId: base.requirements[0]?.id ?? "",
    fixtureUrl,
    environmentId: base.environmentId,
    accessId: access.id,
    sessionId,
  }
}

// --- batch / evidence helpers ----------------------------------------------

/** POST /api/versions/{id}/batches with an explicit kind and case selection. */
async function createBatchWithKind(
  request: APIRequestContext,
  token: string,
  versionId: string,
  body: { kind?: string; caseIds?: string[]; parentBatchId?: string },
): Promise<Batch> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/batches`, {
    headers: authHeaders(token),
    data: body,
  })
  return readData<Batch>(response, 201, "create batch")
}

/** Polls one case run until it reaches a terminal status. */
async function waitCaseRunTerminal(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  caseRunId: string,
  timeoutMs = 300_000,
): Promise<CaseRun> {
  const deadline = Date.now() + timeoutMs
  let last: CaseRun | null = null
  while (Date.now() < deadline) {
    const response = await request.get(
      `${API_URL}/api/versions/${versionId}/batches/${batchId}/case-runs/${caseRunId}`,
      { headers: authHeaders(token) },
    )
    last = await readData<CaseRun>(response, 200, "get case run")
    if (
      ["passed", "failed", "blocked", "inconclusive", "skipped", "interrupted"].includes(
        last.status,
      )
    ) {
      return last
    }
    await sleep(250)
  }
  throw new Error(
    `case run ${caseRunId} did not reach a terminal status within ${timeoutMs}ms ` +
      `(last: ${last?.status ?? "none"})`,
  )
}

type VisualActual = {
  assertionId?: string
  diffRatio?: number
  threshold?: number
  environmentMatch?: boolean
  width?: number
  height?: number
  baselineEvidenceId?: string
  actualEvidenceId?: string
  diffEvidenceId?: string
  error?: string
}

/** Parses the compact visual assertion `actual` document. */
function parseVisualActual(actual: string): VisualActual {
  try {
    const parsed = JSON.parse(actual) as VisualActual
    return typeof parsed === "object" && parsed !== null ? parsed : {}
  } catch {
    return {}
  }
}

/** Opens the results tab for one batch and expands its single case run. */
async function openResultsCaseRun(
  page: Page,
  versionId: string,
  batchId: string,
  caseRunId: string,
): Promise<Locator> {
  await page.goto(
    `/versions/${encodeURIComponent(versionId)}?tab=results&batch=${encodeURIComponent(batchId)}`,
  )
  await expect(page.getByTestId("results-tab")).toBeVisible({ timeout: 60_000 })
  const row = page.getByTestId(`case-run-row-${caseRunId}`)
  await expect(row).toBeVisible({ timeout: 60_000 })
  await row.getByRole("button", { name: /展开/ }).click()
  const detail = page.getByTestId(`case-run-detail-${caseRunId}`)
  await expect(detail).toBeVisible({ timeout: 60_000 })
  return detail
}

// --- fixture cleanup --------------------------------------------------------

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

// --- shared browser helpers -------------------------------------------------

/** Logs in as the admin account through the real login form. */
async function loginAdmin(page: Page): Promise<void> {
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
}

/** Logs out through the real user menu. */
async function logout(page: Page): Promise<void> {
  await page.getByRole("button", { name: "打开用户菜单" }).click()
  await page.getByRole("menuitem", { name: "退出登录" }).click()
  await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible({ timeout: 15_000 })
}

/**
 * Switches accounts through the real user menu. The outgoing phase is asserted
 * before the monitor is stopped: stopping first (then only checking the final
 * monitor) would silently drop every console error raised before the last
 * account switch. The logout redirect can race in-flight shell reads whose token
 * was just cleared, which Chromium reports as a 401 resource console error; that
 * transition happens while no monitor is active, so no exception rule is needed.
 */
async function switchAccount(
  page: Page,
  monitor: ReturnType<typeof captureConsoleErrors>,
  username: string,
  password: string,
): Promise<ReturnType<typeof captureConsoleErrors>> {
  await expectNoConsoleErrors(monitor, page, `P8 质量（切换到 ${username} 前）`)
  await logout(page)
  await login(page, username, password)
  return captureConsoleErrors(page)
}

/** Creates one normal user through the admin API and returns its credentials. */
async function createNormalUser(
  request: APIRequestContext,
  token: string,
  suffix: string,
): Promise<{ username: string; password: string }> {
  const username = `qa_p8_quality_${suffix}`
  const created = await request.post(`${API_URL}/api/users`, {
    headers: authHeaders(token),
    data: { username, password: USER_PASSWORD },
  })
  expect(created.status(), "create normal user").toBe(201)
  return { username, password: USER_PASSWORD }
}

/** Confirms one requirement through the real API. */
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

// ---------------------------------------------------------------------------

test("P8 知识沉淀：普通用户草稿 → admin 审批 → /knowledge 可见", async ({ page, request }) => {
  test.setTimeout(180_000)
  if (await skipP7SpecWhenHarnessMissing({ requireFakeLlm: false })) return

  let monitor = captureConsoleErrors(page)
  const suffix = uniqueSuffix()
  const title = `P8 知识草稿 ${suffix}`
  const body = `P8 E2E 知识正文 ${suffix}：登录后先确认订单状态，再提交表单。`
  let versionId = ""
  let knowledgeId = ""
  try {
    const loginResponse = await request.post(`${API_URL}/api/auth/login`, {
      data: { username: ADMIN_USERNAME, password: ADMIN_PASSWORD },
    })
    const token = (await readData<{ token: string }>(loginResponse, 200, "admin login")).token
    const version = await createVersion(request, token, {
      name: `P8 知识版本 ${suffix}`,
      label: `p8-knowledge-${suffix}`,
    })
    versionId = version.id
    const normal = await createNormalUser(request, token, suffix)

    await test.step("ordinary user submits a knowledge draft in the version overview", async () => {
      await login(page, normal.username, normal.password)
      await page.goto(`/versions/${encodeURIComponent(versionId)}?tab=overview`)
      const panel = page.getByTestId("knowledge-panel")
      await expect(panel).toBeVisible({ timeout: 60_000 })
      await expect(panel).toContainText("本版本暂无已批准知识。")

      await page.getByTestId("knowledge-create-toggle").click()
      const form = page.getByTestId("knowledge-create-form")
      await expect(form).toBeVisible()
      await form.getByTestId("knowledge-kind").selectOption("pattern")
      await form.getByTestId("knowledge-title").fill(title)
      await form.getByTestId("knowledge-body").fill(body)
      await form.getByTestId("knowledge-submit").click()
      await expect(page.getByTestId("knowledge-submit-success")).toContainText("等待管理员审核")
      await page.screenshot({
        path: evidencePath("p8-quality-01-knowledge-draft.png"),
        fullPage: true,
      })
    })

    await test.step("the draft is real API data with status=draft", async () => {
      const response = await request.get(
        `${API_URL}/api/versions/${versionId}/knowledge?status=draft&limit=50`,
        { headers: authHeaders(token) },
      )
      const items = await readData<Array<{ id: string; title: string; status: string }>>(
        response,
        200,
        "list draft knowledge",
      )
      const created = items.find((item) => item.title === title)
      expect(created, "the UI draft must be readable through the API").toBeDefined()
      expect(created?.status).toBe("draft")
      knowledgeId = created?.id ?? ""
      expect(knowledgeId).toMatch(/^\d+$/)
    })

    await test.step("admin approves the draft in the /knowledge review drawer", async () => {
      monitor = await switchAccount(page, monitor, ADMIN_USERNAME, ADMIN_PASSWORD)
      await page.goto(
        `/knowledge?version=${encodeURIComponent(versionId)}&review=${encodeURIComponent(knowledgeId)}`,
      )
      const drawer = page.getByTestId("knowledge-review-drawer")
      await expect(drawer).toBeVisible({ timeout: 60_000 })
      const item = page.getByTestId(`knowledge-review-item-${knowledgeId}`)
      await expect(item).toBeVisible({ timeout: 60_000 })
      await expect(item).toHaveAttribute("aria-current", "true")
      await expect(drawer).toContainText(title)
      await page.getByTestId("knowledge-review-approve").click()
      // The success paragraph is only rendered while a selection exists; approving
      // the only draft empties the queue, so the durable proof is the API state.
      await expect
        .poll(
          async () => {
            const response = await request.get(
              `${API_URL}/api/versions/${versionId}/knowledge?status=approved&limit=50`,
              { headers: authHeaders(token) },
            )
            const items = await readData<Array<{ id: string; status: string }>>(
              response,
              200,
              "list approved knowledge",
            )
            return items.find((entry) => entry.id === knowledgeId)?.status ?? ""
          },
          { timeout: 30_000 },
        )
        .toBe("approved")
      await expect(drawer).toContainText("暂无待审核知识", { timeout: 30_000 })
      await page.screenshot({
        path: evidencePath("p8-quality-02-knowledge-approved.png"),
        fullPage: true,
      })
      await drawer.getByRole("button", { name: "关闭审核抽屉" }).click()
      await expect(drawer).toHaveCount(0)

      // The page list reloads after the review and now contains the approved entry.
      await page.goto(`/knowledge?version=${encodeURIComponent(versionId)}`)
      await expect(page.getByTestId(`knowledge-list-item-${knowledgeId}`)).toBeVisible({
        timeout: 60_000,
      })
      await expect(page.getByTestId("knowledge-detail")).toContainText(title)
    })

    await test.step("an ordinary user reads the approved knowledge too", async () => {
      monitor = await switchAccount(page, monitor, normal.username, normal.password)
      await page.goto(`/knowledge?version=${encodeURIComponent(versionId)}`)
      await expect(page.getByTestId(`knowledge-list-item-${knowledgeId}`)).toBeVisible({
        timeout: 60_000,
      })
      await expect(page.getByTestId("knowledge-status-filter")).toHaveCount(0)
      await expect(page.getByTestId("knowledge-open-review")).toHaveCount(0)
      await expect(page.getByTestId("knowledge-detail")).toContainText(title)
      await page.screenshot({
        path: evidencePath("p8-quality-03-knowledge-visible.png"),
        fullPage: true,
      })
    })

    await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 知识沉淀")
    await expectNoConsoleErrors(monitor, page, "P8 知识沉淀")
  } finally {
    monitor.stop()
    try {
      if (knowledgeId !== "")
        runPsql(`DELETE FROM knowledge_entry WHERE id = ${knowledgeId}::bigint;`)
      if (versionId !== "") runPsql(`DELETE FROM version WHERE id = ${versionId}::bigint;`)
    } catch {
      // The harness may run without psql (raw Playwright); the e2e database is
      // reset by scripts/e2e.ps1, so cleanup is best effort outside it.
    }
  }
})

test("P8 视觉断言：基线审批 → 真实执行 → visual_actual/visual_diff 证据", async ({
  page,
  request,
}) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true })) return

  const monitor = captureConsoleErrors(page)
  const suffix = uniqueSuffix()
  try {
    await setP7Mode(request, "noop")
    await loginAdmin(page)
    const loginResponse = await request.post(`${API_URL}/api/auth/login`, {
      data: { username: ADMIN_USERNAME, password: ADMIN_PASSWORD },
    })
    const token = (await readData<{ token: string }>(loginResponse, 200, "admin login")).token

    const visualSeed =
      await test.step("seed an executing version with a configured visual assertion", async () => {
        const seeded = await seedVisualVersion(request, token, suffix)
        expect(seeded.sessionId, "the fixture session must be imported").toBeGreaterThan(0)
        return seeded
      })
    expect(visualSeed.versionId).toMatch(/^\d+$/)
    const versionId = visualSeed.versionId
    const caseNodeId = visualSeed.caseNodeId

    const baselineRun = await test.step("real baseline batch runs without a baseline", async () => {
      await page.goto(`/versions/${encodeURIComponent(versionId)}`)
      await expect(
        page.getByRole("heading", { name: `P5 Version ${suffix}`, level: 1 }),
      ).toBeVisible({ timeout: 60_000 })
      await page.getByTestId("exec-start-btn").click()
      const batch = await waitP7BatchKind(request, token, versionId, "baseline", {
        timeoutMs: 120_000,
      })
      expect(batch.kind).toBe("baseline")
      const done = await waitP7BatchTerminal(request, token, versionId, batch.id, 300_000)
      expect(done.status).toBe("done")
      expect(done.counts.inconclusive).toBe(1)
      expect(done.counts.failed).toBe(0)
      const runs = await listCaseRuns(request, token, versionId, done.id)
      expect(runs).toHaveLength(1)
      const run = await waitCaseRunTerminal(request, token, versionId, done.id, runs[0].id)
      expect(run.status).toBe("inconclusive")
      expect(run.failure_class).toBe("case")
      const visual = run.assertions.find((assertion) => assertion.kind === "visual")
      expect(visual, "the visual assertion must be recorded server-side").toBeDefined()
      expect(visual?.verdict).toBe("not_run")
      const actual = parseVisualActual(visual?.actual ?? "")
      expect(actual.error).toBe("missing_visual_baseline")
      expect(run.evidence.some((item) => item.kind === "screenshot")).toBe(true)
      expect(run.evidence.some((item) => item.kind === "visual_actual")).toBe(false)
      const screenshot = run.evidence.find((item) => item.kind === "screenshot")
      expect(screenshot, "the baseline candidate must be a real executor screenshot").toBeDefined()
      return { batch: done, run, screenshotId: screenshot?.id ?? "" }
    })

    await test.step("admin promotes the run screenshot to the approved visual baseline", async () => {
      const response = await request.post(
        `${API_URL}/api/versions/${versionId}/mindmap/assertions/${visualSeed.visualAssertionId}/visual/baseline`,
        { headers: authHeaders(token), data: { evidenceId: baselineRun.screenshotId } },
      )
      const approved = await readData<{
        baselineEvidenceId: string
        approvedBy: string | null
        approvedAt: string | null
      }>(response, 200, "approve visual baseline")
      expect(approved.baselineEvidenceId).toBe(baselineRun.screenshotId)
      expect(approved.approvedBy).not.toBeNull()
      expect(approved.approvedAt).not.toBeNull()

      const graphResponse = await request.get(`${API_URL}/api/versions/${versionId}/mindmap`, {
        headers: authHeaders(token),
      })
      const graph = await readData<MindmapGraph>(graphResponse, 200, "read approved graph")
      const node = graph.nodes.find((item) => item.id === caseNodeId)
      const assertion = (
        node as unknown as {
          assertions?: Array<{
            id: string
            kind: string
            visual?: { baselineEvidenceId?: string | null }
          }>
        }
      )?.assertions?.find((item) => item.id === visualSeed.visualAssertionId)
      expect(assertion?.visual?.baselineEvidenceId ?? "").toBe(baselineRun.screenshotId)
    })

    await test
      .step("the completed version continues and re-executes the same case", async () => {
        await waitVersionCompleted(request, token, versionId, 300_000)
        const requirement = await createRequirement(request, token, versionId, {
          title: `P8 视觉继续需求 ${suffix}`,
          description: "继续中场景：验证视觉基线在增量批次里生效。",
          acceptanceCriteria: "同一视觉用例在增量批次里通过。",
        })
        await waitVersionStatus(request, token, versionId, "continuing", { timeoutMs: 120_000 })
        await confirmRequirement(request, token, versionId, requirement.id)
        await expect
          .poll(async () => (await getP7Version(request, token, versionId)).continuePhase, {
            timeout: 120_000,
          })
          .toBe("designing")

        const incremental = await createBatchWithKind(request, token, versionId, {
          kind: "incremental",
          caseIds: [caseNodeId],
          parentBatchId: baselineRun.batch.id,
        })
        const done = await waitBatch(request, token, versionId, incremental.id, {
          timeoutMs: 300_000,
        })
        expect(done.status).toBe("done")
        expect(done.counts.passed).toBe(1)
        const runs = await listCaseRuns(request, token, versionId, done.id)
        expect(runs).toHaveLength(1)
        const run = await waitCaseRunTerminal(request, token, versionId, done.id, runs[0].id)
        expect(run.status).toBe("passed")

        const visual = run.assertions.find((assertion) => assertion.kind === "visual")
        expect(visual, "the incremental run must record the visual assertion").toBeDefined()
        expect(visual?.verdict).toBe("passed")
        const actual = parseVisualActual(visual?.actual ?? "")
        expect(actual.environmentMatch).toBe(true)
        expect(actual.diffRatio ?? 1).toBeLessThanOrEqual(actual.threshold ?? VISUAL_THRESHOLD)
        expect(actual.actualEvidenceId, "visual_actual evidence id").toMatch(/^\d+$/)
        expect(actual.diffEvidenceId, "visual_diff evidence id").toMatch(/^\d+$/)
        const actualEvidence = run.evidence.find((item) => item.kind === "visual_actual")
        const diffEvidence = run.evidence.find((item) => item.kind === "visual_diff")
        expect(actualEvidence, "visual_actual evidence row").toBeDefined()
        expect(diffEvidence, "visual_diff evidence row").toBeDefined()
        expect(actualEvidence?.id).toBe(actual.actualEvidenceId)
        expect(diffEvidence?.id).toBe(actual.diffEvidenceId)

        // The evidence bytes are real PNG artifacts served by the evidence API.
        for (const evidenceID of [actual.actualEvidenceId, actual.diffEvidenceId]) {
          const evidence = await getEvidence(request, token, versionId, evidenceID ?? "")
          expect(evidence.contentType).toContain("image/png")
          expect(evidence.body.subarray(0, 8)).toEqual(
            Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
          )
          const sha = createHash("sha256").update(evidence.body).digest("hex")
          expect(sha).toBe(evidence.sha256)
        }

        return {
          batch: done,
          run,
          visualResultId: visual?.id ?? "",
          actualEvidenceId: actual.actualEvidenceId ?? "",
          diffEvidenceId: actual.diffEvidenceId ?? "",
        }
      })
      .then(async (incrementalRun) => {
        await test.step("results tab renders the visual assertion and both evidence artifacts", async () => {
          const detail = await openResultsCaseRun(
            page,
            versionId,
            incrementalRun.batch.id,
            incrementalRun.run.id,
          )
          // The row test id is the assertion_result id (not the snapshot assertion).
          const assertionRow = detail.locator(
            `[data-testid="assertion-row-${incrementalRun.visualResultId}"]`,
          )
          await expect(assertionRow).toHaveAttribute("data-kind", "visual")
          await expect(assertionRow).toHaveAttribute("data-verdict", "passed")
          await page.screenshot({
            path: evidencePath("p8-quality-04-visual-results.png"),
            fullPage: true,
          })

          await page
            .getByTestId(`evidence-thumb-${incrementalRun.actualEvidenceId}`)
            .first()
            .click()
          const viewer = page.getByTestId("evidence-viewer")
          await expect(viewer).toBeVisible({ timeout: 30_000 })
          await expect(viewer).toHaveAttribute("data-evidence-kind", "visual_actual")
          const image = page.getByTestId("evidence-visual-image")
          await expect(image).toHaveAttribute("data-visual-kind", "visual_actual")
          await page.screenshot({
            path: evidencePath("p8-quality-05-visual-actual.png"),
            fullPage: true,
          })

          await page.keyboard.press("Escape")
          await expect(viewer).toHaveCount(0)
          await page.getByTestId(`evidence-thumb-${incrementalRun.diffEvidenceId}`).first().click()
          await expect(viewer).toBeVisible({ timeout: 30_000 })
          await expect(viewer).toHaveAttribute("data-evidence-kind", "visual_diff")
          await expect(page.getByTestId("evidence-visual-diff")).toBeVisible({ timeout: 30_000 })
          await page.screenshot({
            path: evidencePath("p8-quality-06-visual-diff.png"),
            fullPage: true,
          })
        })
      })

    await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 视觉断言")
    await expectNoConsoleErrors(monitor, page, "P8 视觉断言")
  } finally {
    monitor.stop()
    await setP7Mode(request, "normal").catch(() => undefined)
    // The visual fixture version is intentionally left behind: it owns executor
    // evidence/analysis rows whose cleanup is covered by the e2e database reset.
  }
})

test("P8 质量中心：admin 指标可见，普通用户 403", async ({ page, request }) => {
  test.setTimeout(180_000)
  if (await skipP7SpecWhenHarnessMissing({ requireFakeLlm: false })) return

  let monitor = captureConsoleErrors(page)
  const suffix = uniqueSuffix()
  const corpusVersion = `p8-e2e-${suffix}`
  try {
    const loginResponse = await request.post(`${API_URL}/api/auth/login`, {
      data: { username: ADMIN_USERNAME, password: ADMIN_PASSWORD },
    })
    const token = (await readData<{ token: string }>(loginResponse, 200, "admin login")).token
    const normal = await createNormalUser(request, token, suffix)

    const recorded =
      await test.step("record one seeded deterministic fixture evaluation run", async () => {
        const response = await request.post(`${API_URL}/api/quality/eval-runs`, {
          headers: authHeaders(token),
          data: {
            // This is a seeded, deterministic fixture — never claim a real-model
            // report in the acceptance evidence. The deterministic track is the
            // reproducible CI gate (design spec §9.1/D61); the committed baseline
            // equals the observed sample so the run exercises the pass path.
            mode: "deterministic",
            corpusVersion,
            model: "deterministic-fixture",
            baseline: {
              design_coverage: 1,
              impact_recall: 1,
              impact_precision: 0.8,
              classification_accuracy: 1,
              issue_false_positive_rate: 0,
              supplement_hit_rate: 1,
            },
            sample: {
              confirmedRequirements: 10,
              coveredRequirements: 10,
              impactExpected: 10,
              impactHit: 10,
              impactSelected: 12,
              injectedFailures: 5,
              correctClassifications: 5,
              aiIssues: 10,
              truePositiveIssues: 10,
              supplementCases: 5,
              supplementHits: 5,
            },
            summary: { source: "p8-e2e-deterministic-fixture" },
          },
        })
        const run = await readData<{
          id: string
          status: string
          metrics: Array<{ name: string; passed: boolean }>
        }>(response, 201, "record eval run")
        expect(run.id).toMatch(/^\d+$/)
        expect(run.metrics).toHaveLength(6)
        expect(run.metrics.every((metric) => metric.passed)).toBe(true)
        return run
      })

    await test.step("admin sees the KPI dashboard", async () => {
      await loginAdmin(page)
      await page.goto("/quality")
      await expect(page.getByTestId("quality-page")).toBeVisible({ timeout: 60_000 })
      const summary = page.getByTestId("quality-summary")
      await expect(summary).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("quality-run-id")).toContainText(`#${recorded.id}`)
      await expect(page.getByTestId("quality-verdict")).toContainText("全部指标达标")
      // The dashboard must label the seeded run as the deterministic track, not
      // as a real-LLM report.
      await expect(summary).toContainText("确定性门禁")
      await expect(summary).not.toContainText("真实 LLM 报告")
      await expect(page.locator('[data-testid^="quality-metric-value-"]')).toHaveCount(6)
      await expect(page.getByTestId(`quality-history-row-${recorded.id}`)).toContainText(
        "确定性门禁",
      )
      await page.screenshot({
        path: evidencePath("p8-quality-07-quality-admin.png"),
        fullPage: true,
      })
    })

    await test.step("normal user is denied by the page and the API", async () => {
      monitor = await switchAccount(page, monitor, normal.username, normal.password)
      await page.goto("/quality")
      await expect(page.getByTestId("quality-page-denied")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("quality-page")).toHaveCount(0)
      await expect(page.getByTestId("quality-summary")).toHaveCount(0)
      await page.screenshot({
        path: evidencePath("p8-quality-08-quality-denied.png"),
        fullPage: true,
      })

      const normalLogin = await request.post(`${API_URL}/api/auth/login`, {
        data: { username: normal.username, password: normal.password },
      })
      const normalToken = (await readData<{ token: string }>(normalLogin, 200, "normal login"))
        .token
      const forbidden = await request.get(`${API_URL}/api/quality/eval-runs`, {
        headers: authHeaders(normalToken),
      })
      expect(forbidden.status()).toBe(403)
      const forbiddenBody = (await forbidden.json()) as { error?: { code?: string } }
      expect(forbiddenBody.error?.code).toBe("forbidden")
    })

    await expectNoSecretInPage(page, ADMIN_PASSWORD, "P8 质量中心")
    await expectNoConsoleErrors(monitor, page, "P8 质量中心")
  } finally {
    monitor.stop()
    try {
      runPsql(
        `DELETE FROM eval_metric WHERE run_id IN (SELECT id FROM eval_run WHERE corpus_version = '${corpusVersion}');
         DELETE FROM eval_run WHERE corpus_version = '${corpusVersion}';`,
      )
    } catch {
      // Raw Playwright runs without the e2e env skip the DB cleanup.
    }
  }
})

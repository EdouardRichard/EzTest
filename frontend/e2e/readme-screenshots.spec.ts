import { execFileSync } from "node:child_process"
import { mkdirSync, rmSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import {
  type APIRequestContext,
  type APIResponse,
  expect,
  type Page,
  test,
} from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  API_URL,
  adminApiToken,
  createRequirement,
  createVersion,
  login,
  startVersionClarification,
  type RequirementRecord,
} from "./p3-helpers"
import { authHeaders } from "./p4-helpers"
import { seedMindmapVersion, setP5Mode } from "./p5-helpers"
import { createBatch, listCaseRuns, setP6Mode } from "./p6-helpers"
import {
  getP7Version,
  seedP7ExecutionVersion,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitP7AnalysisSettled,
  waitP7BatchKind,
  waitP7BatchTerminal,
  waitP7Issue,
  waitP7ReportForBatch,
  waitVersionCompleted,
  type P7ExecutionSeed,
} from "./p7-helpers"

/**
 * README screenshot capture (temporary tool, not part of the committed suite).
 *
 * Seeds a product-looking Chinese demo dataset through the real APIs + the
 * deterministic fake LLM, drives the real Playwright executor against the
 * fixture target, and captures stable 1440x900 @2x PNGs into
 * `docs/assets/screenshots/`.
 *
 * The captured pages are the current HEAD UI. Three presentation-only passes
 * are applied so a public README never shows internal phase labels:
 *  - resource/version names are renamed through SQL to release-quality labels;
 *  - fake-LLM fixture prefixes (`P7 补全：`, `P7 失败分析：`, `P5 ` ...) are
 *    stripped from the snapshot/content rows the UI renders;
 *  - internal phase chips (P0..P8) and the dev status card are hidden with
 *    injected CSS/JS before each capture (no functional UI is altered).
 *
 * Run with: powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 readme-screenshots
 */

test.describe.configure({ mode: "serial" })

// The README expects 1440x900 CSS pixels at deviceScaleFactor 2 (2880x1800 PNG).
test.use({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 })

const SHOTS_DIR = fileURLToPath(new URL("../../docs/assets/screenshots", import.meta.url))

const DEMO = {
  versionA: { name: "订单中心 · 2026 Q4 迭代", label: "order-center-web 2.4.0" },
  versionB: { name: "结算与退款流程 · 回归验证", label: "settlement-refund 2.4.0" },
  systemA: "订单中心",
  environmentA: "预发布环境",
  repositoryA: "order-center-web",
  systemB: "结算中心",
  environmentB: "测试环境",
  repositoryB: "settlement-service",
  caseTitle: "登录后创建订单并校验金额",
} as const

function shotPath(name: string): string {
  mkdirSync(SHOTS_DIR, { recursive: true })
  return join(SHOTS_DIR, name)
}

type DataEnvelope<T> = { data: T }

async function readData<T>(
  response: APIResponse,
  status: number,
  label: string,
): Promise<T> {
  expect(response.status(), `${label}: unexpected HTTP ${response.status()}`).toBe(status)
  const body = (await response.json()) as DataEnvelope<T>
  return body.data
}

// --- SQL polish (throwaway eztest_e2e database) -----------------------------

/**
 * Runs one or more SQL statements through the psql client exported by e2e.ps1.
 *
 * The SQL is written to a UTF-8 file and passed with `-f`: psql.exe on Windows
 * rejects non-ASCII UTF-8 in the `-c` argv (the console code page cannot decode
 * it), which is why the committed P8 spec keeps psql fixtures ASCII-only.
 */
function runSql(sql: string): string {
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
  const sqlDir = join(fileURLToPath(new URL("../../.tmp", import.meta.url)), "readme-sql")
  mkdirSync(sqlDir, { recursive: true })
  const sqlPath = join(sqlDir, `polish-${Date.now()}-${Math.random().toString(36).slice(2, 8)}.sql`)
  writeFileSync(sqlPath, `${sql}\n`, { encoding: "utf8" })
  try {
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
        "-f",
        sqlPath,
      ],
      {
        encoding: "utf8",
        env: {
          ...process.env,
          PGPASSWORD: decodeURIComponent(parsed.password),
          PGCLIENTENCODING: "UTF8",
          PGSSLMODE: parsed.searchParams.get("sslmode") ?? "disable",
        },
      },
    ).trim()
  } finally {
    rmSync(sqlPath, { force: true })
  }
}

/** Renames the seeded P5/P7 fixture resources to release-quality demo labels. */
function renameDemoResources(input: {
  versionAId: string
  systemAId: number
  environmentAId: number
  repositoryAId: number
  versionBId: string
  systemBId: number
  environmentBId: number
}): void {
  const versionB = input.versionBId
  runSql(
    [
      `UPDATE version SET name = '${DEMO.versionA.name}', label = '${DEMO.versionA.label}', description = '订单中心结算与退款能力的迭代验收版本，含 AI 澄清与用例设计。' WHERE id = ${input.versionAId}::bigint;`,
      `UPDATE system SET name = '${DEMO.systemA}', key = 'order-center' WHERE id = ${input.systemAId};`,
      `UPDATE environment SET name = '${DEMO.environmentA}', key = 'order-center-preprod' WHERE id = ${input.environmentAId};`,
      `UPDATE code_repository SET name = '${DEMO.repositoryA}', key = 'order-center-web' WHERE id = ${input.repositoryAId};`,
      `UPDATE version SET name = '${DEMO.versionB.name}', label = '${DEMO.versionB.label}', description = '结算与退款流程回归验证，覆盖基线失败、AI 分析、补全用例与报告。' WHERE id = ${versionB}::bigint;`,
      `UPDATE system SET name = '${DEMO.systemB}', key = 'settlement-center' WHERE id = ${input.systemBId};`,
      `UPDATE environment SET name = '${DEMO.environmentB}', key = 'settlement-center-test' WHERE id = ${input.environmentBId};`,
      `UPDATE code_repository SET name = '${DEMO.repositoryB}', key = 'settlement-service' WHERE id = (SELECT code_repository_id FROM version WHERE id = ${versionB}::bigint);`,
    ].join("\n"),
  )
}

/**
 * Strips the deterministic fake-LLM fixture prefixes and test-suite labels from
 * every row the demo screenshots render. Content only: no domain state changes.
 */
function polishFixtureText(versionAId: string, versionBId: string): void {
  const versions = `${versionAId}::bigint, ${versionBId}::bigint`
  const replacements: Array<[string, string]> = [
    ["P7 补全：", ""],
    ["P7 失败分析：", ""],
    ["P7 失败候选：", ""],
    ["P7 补全", "补全"],
    ["P7 失败分析", "失败分析"],
    ["P7 问题整理", "问题整理"],
    ["P7 契约测试", "契约测试"],
    ["P7 ", ""],
    ["P6 ", ""],
    ["P5 ", ""],
    ["P8 ", ""],
    ["P4 ", ""],
    ["P3 ", ""],
    // The deterministic fixture target mounts its pages under `/p6/*`; the
    // step texts are demo content, so present the product-relative paths.
    ["/p6/orders/new", "/orders/new"],
    ["/p6/orders", "/orders"],
    ["/p6/home", "/home"],
    ["/p6", ""],
  ]
  const strip = (expression: string): string => {
    let next = expression
    for (const [from, to] of replacements) {
      next = `replace(${next}, '${from}', '${to}')`
    }
    return next
  }
  runSql(
    [
      `UPDATE requirement SET title = ${strip("title")}, description = ${strip("description")}, acceptance_criteria = ${strip("acceptance_criteria")} WHERE version_id IN (${versions});`,
      `UPDATE case_node SET title = ${strip("title")} WHERE version_id IN (${versions});`,
      `UPDATE case_run SET case_snapshot = ${strip("case_snapshot::text")}::jsonb WHERE version_id IN (${versions});`,
      `UPDATE step_result SET action = ${strip("action")}, expected = ${strip("expected")}, actual = ${strip("actual")} WHERE batch_id IN (SELECT id FROM run_batch WHERE version_id = ${versionBId}::bigint);`,
      `UPDATE assertion_result SET target = ${strip("target")}, expected = ${strip("expected")}, actual = ${strip("actual")}, failure_reason = ${strip("failure_reason")} WHERE batch_id IN (SELECT id FROM run_batch WHERE version_id = ${versionBId}::bigint);`,
      `UPDATE issue SET title = ${strip("title")}, description = ${strip("description")}, repro_steps = ${strip("repro_steps")}, expected = ${strip("expected")}, actual = ${strip("actual")}, rerun_suggestion = ${strip("rerun_suggestion")} WHERE version_id IN (${versions});`,
      `UPDATE report SET title = ${strip("title")}, content = ${strip("content::text")}::jsonb WHERE version_id IN (${versions});`,
      `UPDATE agent_session SET title = ${strip("title")} WHERE title LIKE '%P7%' OR title LIKE '%P6%' OR title LIKE '%P5%';`,
      `UPDATE event SET summary = ${strip("summary")} WHERE summary LIKE '%P7%' OR summary LIKE '%P6%' OR summary LIKE '%P5%';`,
      `UPDATE account SET label = '结算中心测试账号' WHERE label LIKE 'P7 Account%';`,
      `UPDATE auth_flow SET name = '结算中心登录流程' WHERE name LIKE 'P7 login%';`,
    ].join("\n"),
  )
}

// --- browser helpers --------------------------------------------------------

/**
 * Hides internal delivery-phase chrome (P0..P8 chips, the dev status card and
 * the login phase eyebrow) so a public README shows product UI only. Layout and
 * every functional control stay untouched.
 */
async function maskDevChrome(page: Page): Promise<void> {
  await page.evaluate(() => {
    // The Next.js dev-tools indicator is a <nextjs-portal> host element; hiding
    // it removes the floating "N" bubble from every screenshot.
    if (document.getElementById("readme-devtools-mask") === null) {
      const style = document.createElement("style")
      style.id = "readme-devtools-mask"
      style.textContent = "nextjs-portal{display:none!important}"
      document.head.appendChild(style)
    }
    // Badges keep their layout box (visibility) so the page composition does
    // not shift when the internal phase labels are hidden.
    const hide = (element: Element | null): void => {
      if (!(element instanceof HTMLElement)) return
      element.style.visibility = "hidden"
      element.style.opacity = "0"
    }
    // Sidebar footer dev card ("P8 · 体验硬化" wave status).
    const aside = document.querySelector("aside")
    if (aside !== null && aside.lastElementChild instanceof HTMLElement) {
      aside.lastElementChild.style.display = "none"
    }
    // Phase badges (span/p with a short "P<n>" or "P<n> · wave" label). Only
    // leaf-like elements are hidden: a block <p> in the login hero spans the
    // whole column, so its box width is no useful filter.
    for (const element of Array.from(document.querySelectorAll("span, p"))) {
      if (!(element instanceof HTMLElement)) continue
      const text = (element.textContent ?? "").trim()
      if (!/^P[0-9](?: · .+)?$/.test(text) && text !== "EzTest · P8") continue
      if (element.childElementCount > 2) continue
      hide(element)
    }
  })
}

/** Waits for fonts/animations, masks dev chrome and takes one PNG. */
async function capture(
  page: Page,
  name: string,
  options: { fullPage?: boolean; settleMs?: number } = {},
): Promise<void> {
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(options.settleMs ?? 500)
  await maskDevChrome(page)
  await page.waitForTimeout(120)
  await page.screenshot({
    path: shotPath(name),
    fullPage: options.fullPage ?? false,
    animations: "disabled",
  })
  console.log(`captured docs/assets/screenshots/${name}`)
}

/** Fails when a Next.js dev/error overlay is rendered on the captured page. */
async function expectNoDevOverlay(page: Page): Promise<void> {
  const body = (await page.locator("body").textContent()) ?? ""
  expect(
    /Application error|Unhandled Runtime Error/i.test(body),
    "the captured page must not render a Next.js error overlay",
  ).toBe(false)
}

// --- demo data helpers ------------------------------------------------------

type MindmapNode = { id: string; kind: string; title: string; revision: number }
type MindmapStep = { id: string; seq: number; action: string; expected: string }
type MindmapAssertion = { id: string; kind: string; expression: string }

async function postJson<T>(
  request: APIRequestContext,
  token: string,
  path: string,
  data: Record<string, unknown>,
  status = 201,
  label = path,
): Promise<T> {
  const response = await request.post(`${API_URL}${path}`, { headers: authHeaders(token), data })
  return readData<T>(response, status, label)
}

async function patchJson<T>(
  request: APIRequestContext,
  token: string,
  path: string,
  data: Record<string, unknown>,
  status = 200,
  label = path,
): Promise<T> {
  const response = await request.patch(`${API_URL}${path}`, { headers: authHeaders(token), data })
  return readData<T>(response, status, label)
}

async function createMindmapNode(
  request: APIRequestContext,
  token: string,
  versionId: string,
  body: Record<string, unknown>,
): Promise<MindmapNode> {
  return postJson<MindmapNode>(
    request,
    token,
    `/api/versions/${versionId}/mindmap/nodes`,
    body,
    201,
    "create mindmap node",
  )
}

async function createMindmapStep(
  request: APIRequestContext,
  token: string,
  versionId: string,
  nodeId: string,
  body: { action: string; expected: string },
): Promise<MindmapStep> {
  return postJson<MindmapStep>(
    request,
    token,
    `/api/versions/${versionId}/mindmap/nodes/${nodeId}/steps`,
    body,
    201,
    "create mindmap step",
  )
}

async function createMindmapAssertion(
  request: APIRequestContext,
  token: string,
  versionId: string,
  nodeId: string,
  body: { stepId?: string; kind: string; target?: string; expression: string; expected: string },
): Promise<MindmapAssertion> {
  return postJson<MindmapAssertion>(
    request,
    token,
    `/api/versions/${versionId}/mindmap/nodes/${nodeId}/assertions`,
    body,
    201,
    "create mindmap assertion",
  )
}

/** Seeds a plausible ordering/refund case design under two scenarios. */
async function seedCaseDesign(
  request: APIRequestContext,
  token: string,
  versionId: string,
  requirements: RequirementRecord[],
): Promise<void> {
  const firstRequirement = requirements[0]?.id
  const secondRequirement = requirements[1]?.id
  const scenarioMain = await createMindmapNode(request, token, versionId, {
    kind: "scenario",
    title: "订单结算主流程",
    description: "用户从购物车进入结算并完成支付的端到端主路径。",
  })
  const payCase = await createMindmapNode(request, token, versionId, {
    kind: "case",
    parentId: scenarioMain.id,
    title: "提交订单并完成支付",
    priority: "P0",
    preconditions: "已登录且购物车中存在可售商品",
    requirementIds: firstRequirement === undefined ? [] : [firstRequirement],
  })
  const payStep = await createMindmapStep(request, token, versionId, payCase.id, {
    action: "打开购物车并点击「去结算」",
    expected: "结算页展示商品小计、优惠与应付金额",
  })
  await createMindmapStep(request, token, versionId, payCase.id, {
    action: "选择收货地址并点击「提交订单」",
    expected: "订单创建成功并跳转到收银台",
  })
  const payAssertionStep = await createMindmapStep(request, token, versionId, payCase.id, {
    action: "使用余额完成支付",
    expected: "支付成功，订单状态更新为「已支付」",
  })
  await createMindmapAssertion(request, token, versionId, payCase.id, {
    stepId: payStep.id,
    kind: "ui",
    target: "#checkout-total",
    expression: "text contains 应付",
    expected: "结算页应付金额可见",
  })
  await createMindmapAssertion(request, token, versionId, payCase.id, {
    stepId: payAssertionStep.id,
    kind: "ui",
    target: "#order-status",
    expression: "text contains 已支付",
    expected: "支付成功后订单状态为已支付",
  })
  await createMindmapStep(request, token, versionId, payCase.id, {
    action: "刷新订单详情页",
    expected: "订单状态保持「已支付」且金额不变",
  })

  const stockCase = await createMindmapNode(request, token, versionId, {
    kind: "case",
    parentId: scenarioMain.id,
    title: "库存不足时阻止下单",
    priority: "P1",
    preconditions: "商品库存被调整为 0",
    requirementIds: firstRequirement === undefined ? [] : [firstRequirement],
  })
  const stockStep = await createMindmapStep(request, token, versionId, stockCase.id, {
    action: "将商品库存改为 0 后提交订单",
    expected: "提示「库存不足」并停留在结算页",
  })
  await createMindmapAssertion(request, token, versionId, stockCase.id, {
    stepId: stockStep.id,
    kind: "text",
    expression: "text contains 库存不足",
    expected: "页面出现库存不足提示",
  })

  const scenarioRefund = await createMindmapNode(request, token, versionId, {
    kind: "scenario",
    title: "退款申请流程",
    description: "退款申请、审核与原路退回的端到端路径。",
  })
  const refundCase = await createMindmapNode(request, token, versionId, {
    kind: "case",
    parentId: scenarioRefund.id,
    title: "退款申请原路退回",
    priority: "P0",
    preconditions: "存在已支付且未超过 7 天的订单",
    requirementIds: secondRequirement === undefined ? [] : [secondRequirement],
  })
  const refundStep = await createMindmapStep(request, token, versionId, refundCase.id, {
    action: "进入订单详情并点击「申请退款」",
    expected: "退款表单展示可退金额",
  })
  await createMindmapStep(request, token, versionId, refundCase.id, {
    action: "填写退款原因并提交",
    expected: "生成退款单，状态为「审核中」",
  })
  await createMindmapAssertion(request, token, versionId, refundCase.id, {
    stepId: refundStep.id,
    kind: "ui",
    target: "#refund-amount",
    expression: "text contains 可退金额",
    expected: "退款金额与订单实付一致",
  })
  const expireCase = await createMindmapNode(request, token, versionId, {
    kind: "case",
    parentId: scenarioRefund.id,
    title: "超过 7 天不再受理退款",
    priority: "P2",
    preconditions: "订单支付时间超过 7 天",
    requirementIds: secondRequirement === undefined ? [] : [secondRequirement],
  })
  await createMindmapStep(request, token, versionId, expireCase.id, {
    action: "打开超过 7 天的订单详情",
    expected: "「申请退款」入口不可用并展示规则说明",
  })
}

/** Records one seeded deterministic evaluation run for the quality centre. */
async function seedEvalRun(request: APIRequestContext, token: string): Promise<void> {
  const response = await request.post(`${API_URL}/api/quality/eval-runs`, {
    headers: authHeaders(token),
    data: {
      mode: "deterministic",
      corpusVersion: "demo-corpus-2026-q4",
      model: "deepseek-flash",
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
      summary: { source: "demo-seed" },
    },
  })
  await readData(response, 201, "record demo eval run")
}

test("capture the README screenshots from the current HEAD UI", async ({ page, request }) => {
  test.setTimeout(1_800_000)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true })) return

  // The demo dataset must be deterministic regardless of a previous spec run.
  await setP5Mode(request, "normal")
  await setP6Mode(request, "normal")
  await setP7Mode(request, "normal")

  const token = await adminApiToken(request)

  // --- 1. login screen ------------------------------------------------------
  await page.goto("/login")
  await expect(page.getByRole("button", { name: "登录", exact: true })).toBeVisible({
    timeout: 60_000,
  })
  await expectNoDevOverlay(page)
  await capture(page, "01-login.png")

  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)

  // --- 2. seed the design-side demo version (workbench + mindmap) -----------
  const seedA = await seedMindmapVersion(request, {
    requirementTitles: ["订单创建与库存扣减", "退款原路退回"],
  })
  const versionA = seedA.version
  // Give the demo environment the same realistic login-check configuration the
  // execution version uses (shown on the resources screenshot).
  await patchJson(
    request,
    token,
    `/api/resources/environments/${seedA.environmentId}`,
    {
      allowAutoLogin: true,
      loginCheck: {
        url: "/p6/home",
        expectStatus: [200],
        bodyContains: "P6 Home",
      },
    },
  )
  const extraRequirement = await createRequirement(request, token, versionA.id, {
    title: "购物车结算金额校验",
    description: "结算页金额需要与购物车明细、优惠券、运费保持一致，避免重复计算。",
    acceptanceCriteria: "结算页应付金额 = 商品小计 - 优惠 + 运费；金额变化实时刷新。",
    priority: "should",
  })

  // Relations + human clarification trail for the workbench screenshot.
  const requirements = [...seedA.requirements, extraRequirement]
  await postJson(
    request,
    token,
    `/api/versions/${versionA.id}/relations`,
    {
      fromRequirementId: requirements[1]?.id ?? "",
      toRequirementId: requirements[0]?.id ?? "",
      relationType: "depends_on",
      description: "退款需要依赖订单创建时的库存回滚能力。",
    },
    201,
    "create relation",
  )
  await postJson(
    request,
    token,
    `/api/versions/${versionA.id}/relations`,
    {
      fromRequirementId: requirements[2]?.id ?? "",
      toRequirementId: requirements[0]?.id ?? "",
      relationType: "overlaps",
      description: "结算金额校验与订单创建共用同一套金额计算服务。",
    },
    201,
    "create relation",
  )
  const question = await postJson<{ id: string }>(
    request,
    token,
    `/api/versions/${versionA.id}/clarification/questions`,
    {
      question: "订单应付金额达到多少需要进入人工审核？",
      requirementId: requirements[0]?.id ?? "",
    },
    201,
    "create clarification question",
  )
  const secondQuestion = await postJson<{ id: string }>(
    request,
    token,
    `/api/versions/${versionA.id}/clarification/questions`,
    {
      question: "退款审核通过后，到账时效按几个工作日对外承诺？",
      requirementId: requirements[1]?.id ?? "",
    },
    201,
    "create clarification question",
  )
  await patchJson(
    request,
    token,
    `/api/versions/${versionA.id}/clarification/questions/${question.id}`,
    {
      status: "answered",
      answer: "订单应付金额超过 5000 元时进入人工审核队列，审核通过后再创建订单。",
    },
  )
  await postJson(
    request,
    token,
    `/api/versions/${versionA.id}/clarification/sessions`,
    {},
    201,
    "start clarification session",
  )
  await postJson(
    request,
    token,
    `/api/versions/${versionA.id}/decisions`,
    {
      topic: "退款到账时效",
      decision: "退款审核通过后 T+1 个工作日原路退回，节假日顺延。",
      rationale: "与支付渠道的结算周期保持一致，避免用户侧预期偏差。",
      requirementId: requirements[1]?.id ?? "",
      questionId: secondQuestion.id,
    },
    201,
    "create decision",
  )

  // Case-design mindmap (scenarios + cases + steps + assertions).
  await seedCaseDesign(request, token, versionA.id, requirements)

  // Extra versions for the versions-list status variety.
  const draftVersion = await createVersion(request, token, {
    name: "会员权益中心 · 灰度验证",
    label: "member-center 1.8.0",
    description: "会员权益叠加规则灰度验证，覆盖积分、等级与优惠券互斥。",
  })
  const draftVersion2 = await createVersion(request, token, {
    name: "购物车重构 · 技术方案验证",
    label: "cart-web 3.0.0-rc1",
    description: "购物车领域重构的技术方案验证版本。",
  })
  const clarifyingVersion = await createVersion(request, token, {
    name: "售后工单 · 需求澄清",
    label: "after-sales-ticket 0.9.0",
    description: "售后工单流转与超时升级需求澄清。",
  })
  await createRequirement(request, token, clarifyingVersion.id, {
    title: "工单超时自动升级",
    description: "工单超过 SLA 未处理时自动升级到二级支持。",
    acceptanceCriteria: "超时后负责人变更且发送通知。",
    priority: "must",
  })
  await createRequirement(request, token, clarifyingVersion.id, {
    title: "工单转派记录可追溯",
    description: "每次转派记录操作人、时间与原因。",
    acceptanceCriteria: "转派历史按时间倒序展示。",
    priority: "should",
  })
  await startVersionClarification(request, token, clarifyingVersion.id)

  // --- 3. seed the execution-side demo version (executor + fake LLM) --------
  const seedB: P7ExecutionSeed = await seedP7ExecutionVersion(request, {
    caseTitle: DEMO.caseTitle,
  })
  const versionB = seedB.version

  // Version/system/environment/repository labels are presentation metadata; the
  // SQL rename cannot invalidate the frozen case baseline.
  renameDemoResources({
    versionAId: versionA.id,
    systemAId: seedA.systemId,
    environmentAId: seedA.environmentId,
    repositoryAId: seedA.codeRepositoryId,
    versionBId: versionB.id,
    systemBId: seedB.systemId,
    environmentBId: seedB.environmentId,
  })

  // Management-side demo data (quality centre + knowledge base).
  await seedEvalRun(request, token)
  const knowledgeSeeds = [
    {
      kind: "pattern",
      title: "订单中心测试数据准备规范",
      body: "订单用例执行前统一准备测试数据：固定订单号前缀、固定金额档位（100/5000/10001 元）与三种库存状态，保证断言可重复。",
      confidence: "high",
      tags: ["订单", "测试数据", "fixture"],
    },
    {
      kind: "pitfall",
      title: "退款流程的幂等风险",
      body: "退款接口在网络抖动下可能重复提交，历史缺陷均由重复点击触发。用例设计需覆盖连续两次提交退款、刷新后重试两个场景。",
      confidence: "medium",
      tags: ["退款", "幂等", "风险"],
    },
    {
      kind: "pattern",
      title: "金额类断言的通用写法",
      body: "金额断言不要比较格式化后的字符串，统一比较两位小数数值，并对千分位分隔符做归一化，避免因文案格式变化造成误报。",
      confidence: "high",
      tags: ["断言", "金额", "模式"],
    },
  ] as const
  for (const entry of knowledgeSeeds) {
    const created = await postJson<{ id: string; revision: number }>(
      request,
      token,
      `/api/versions/${versionA.id}/knowledge`,
      {
        kind: entry.kind,
        title: entry.title,
        body: entry.body,
        confidence: entry.confidence,
        tags: [...entry.tags],
      },
      201,
      "create knowledge entry",
    )
    await postJson(
      request,
      token,
      `/api/versions/${versionA.id}/knowledge/${created.id}/review`,
      { status: "approved", revision: created.revision },
      200,
      "approve knowledge entry",
    )
  }

  // --- 4. run one failing baseline batch; the autonomy chain follows --------
  await setP6Mode(request, "fail")
  const baselineBatch = await createBatch(request, seedB.token, versionB.id)
  const failedBatch = await waitP7BatchTerminal(
    request,
    seedB.token,
    versionB.id,
    baselineBatch.id,
    300_000,
  )
  expect(failedBatch.status).toBe("done")
  expect(failedBatch.counts.failed).toBe(1)
  // The auto-created supplement child batch must run with the normal planner.
  await setP6Mode(request, "normal")

  // --- 5. design-side screenshots while the supplement batch runs -----------
  await page.goto(`/versions/${versionA.id}?tab=requirements`)
  await expect(page.getByRole("list", { name: "需求条目" }).getByRole("button")).toHaveCount(3, {
    timeout: 90_000,
  })
  await expect(page.getByRole("region", { name: "澄清问题" })).toContainText("人工审核", {
    timeout: 90_000,
  })
  await expect(page.getByRole("region", { name: "澄清决策" })).toContainText("T+1", {
    timeout: 90_000,
  })
  await expect(page.getByRole("heading", { name: DEMO.versionA.name, level: 1 })).toBeVisible()
  await capture(page, "03-version-requirements.png")

  // A second workbench frame scrolled to the clarification panels
  // (questions/decisions next to the chat + relation graph) so the README can
  // show the human-in-the-loop part that sits below the fold at 1440x900.
  const clarificationChat = page.getByRole("region", { name: "澄清对话" })
  await clarificationChat.getByLabel("消息内容").scrollIntoViewIfNeeded()
  await page
    .getByRole("region", { name: "澄清问题" })
    .evaluate((element) => element.scrollIntoView({ block: "center" }))
  await expect(clarificationChat.getByLabel("消息内容")).toBeEnabled({ timeout: 60_000 })
  await page.waitForTimeout(500)
  await capture(page, "04-version-clarification.png")

  await page.goto(`/versions/${versionA.id}?tab=mindmap`)
  await expect(page.getByTestId("mindmap-canvas")).toBeVisible({ timeout: 90_000 })
  const caseNode = page.locator('[data-testid^="case-node-"]').first()
  await expect(caseNode).toBeVisible({ timeout: 90_000 })
  await caseNode.click()
  await expect(page.getByTestId("case-editor")).toBeVisible({ timeout: 90_000 })
  await page.getByTestId("mindmap-fit").click()
  await page.waitForTimeout(700)
  await capture(page, "05-case-mindmap.png")

  // --- 6. let the autonomous chain settle, then polish the fixture text -----
  const supplementBatch = await waitP7BatchKind(
    request,
    seedB.token,
    versionB.id,
    "supplement",
    { parentBatchId: failedBatch.id, timeoutMs: 300_000 },
  )
  const supplementDone = await waitP7BatchTerminal(
    request,
    seedB.token,
    versionB.id,
    supplementBatch.id,
    300_000,
  )
  expect(supplementDone.status).toBe("done")
  await waitVersionCompleted(request, seedB.token, versionB.id, 300_000)
  await waitP7AnalysisSettled(request, seedB.token, versionB.id, failedBatch.id, [
    "failure",
    "issue_triage",
  ])
  const issue = await waitP7Issue(
    request,
    seedB.token,
    versionB.id,
    (candidate) => candidate.source === "ai",
  )
  expect(issue.id).toMatch(/^\d+$/)

  // Both the failing baseline batch and the passing supplement chain must have
  // their delivery report before the fixtures are polished for the screenshots.
  const baselineReport = await waitP7ReportForBatch(
    request,
    seedB.token,
    versionB.id,
    failedBatch.id,
    180_000,
  )
  expect(baselineReport.id).toMatch(/^\d+$/)
  await waitP7ReportForBatch(request, seedB.token, versionB.id, supplementDone.id, 180_000).catch(
    () => null,
  )

  polishFixtureText(versionA.id, versionB.id)

  const baselineRuns = await listCaseRuns(request, seedB.token, versionB.id, failedBatch.id)
  const failedCaseRunId = baselineRuns[0]?.id ?? ""
  expect(failedCaseRunId).toMatch(/^\d+$/)

  // --- 7. execution-side screenshots ---------------------------------------
  await page.goto(`/versions/${versionB.id}?tab=results`)
  await expect(page.getByTestId("results-tab")).toBeVisible({ timeout: 120_000 })
  // The version header batch selector defaults to the newest (supplement) batch;
  // the README shot shows the failing baseline batch with its evidence chain.
  const batchSelector = page.getByTestId("exec-batch-selector")
  if (await batchSelector.isVisible().catch(() => false)) {
    await batchSelector.selectOption(failedBatch.id).catch(() => undefined)
  }
  const resultsRow = page.getByTestId(`case-run-row-${failedCaseRunId}`)
  await expect(resultsRow).toBeVisible({ timeout: 120_000 })
  await resultsRow.getByRole("button", { expanded: false }).click()
  const caseDetail = page.getByTestId(`case-run-detail-${failedCaseRunId}`)
  await expect(caseDetail).toBeVisible({ timeout: 90_000 })
  await expect(caseDetail.locator('[data-testid^="step-row-"]').first()).toBeVisible({
    timeout: 60_000,
  })
  await capture(page, "06-execution-results.png")

  // The passing supplement batch's evidence shows a successful order page: a
  // better README frame than the deliberate failure page.
  let evidenceCaptured = false
  if (await batchSelector.isVisible().catch(() => false)) {
    await batchSelector.selectOption(supplementDone.id).catch(() => undefined)
    const supplementRuns = await listCaseRuns(
      request,
      seedB.token,
      versionB.id,
      supplementDone.id,
    )
    const passingRunId = supplementRuns[0]?.id ?? ""
    if (passingRunId !== "") {
      const passingRow = page.getByTestId(`case-run-row-${passingRunId}`)
      await expect(passingRow).toBeVisible({ timeout: 120_000 })
      await passingRow.getByRole("button", { expanded: false }).click()
      const passingDetail = page.getByTestId(`case-run-detail-${passingRunId}`)
      await expect(passingDetail).toBeVisible({ timeout: 90_000 })
      const passingThumb = passingDetail.locator('[data-testid^="evidence-thumb-"]').first()
      await expect(passingThumb).toBeVisible({ timeout: 90_000 })
      await passingThumb.click()
      const passingViewer = page.getByTestId("evidence-viewer")
      await expect(passingViewer).toBeVisible({ timeout: 90_000 })
      await expect(page.getByTestId("evidence-screenshot")).toBeVisible({ timeout: 90_000 })
      await capture(page, "07-evidence-viewer.png")
      await page.keyboard.press("Escape")
      await expect(passingViewer).toHaveCount(0, { timeout: 30_000 })
      evidenceCaptured = true
    }
  }
  if (!evidenceCaptured) {
    // Fallback: the failing baseline run always persists a screenshot evidence.
    await batchSelector.selectOption(failedBatch.id).catch(() => undefined)
    const failedRow = page.getByTestId(`case-run-row-${failedCaseRunId}`)
    await expect(failedRow).toBeVisible({ timeout: 120_000 })
    await failedRow.getByRole("button", { expanded: false }).click().catch(() => undefined)
    const detail = page.getByTestId(`case-run-detail-${failedCaseRunId}`)
    const thumb = detail.locator('[data-testid^="evidence-thumb-"]').first()
    await expect(thumb).toBeVisible({ timeout: 90_000 })
    await thumb.click()
    await expect(page.getByTestId("evidence-viewer")).toBeVisible({ timeout: 90_000 })
    await expect(page.getByTestId("evidence-screenshot")).toBeVisible({ timeout: 90_000 })
    await capture(page, "07-evidence-viewer.png")
    await page.keyboard.press("Escape")
  }

  await page.goto(`/versions/${versionB.id}?tab=report`)
  await expect(page.getByTestId("report-summary")).toBeVisible({ timeout: 120_000 })
  const reportBatchSelector = page.getByTestId("report-batch-selector")
  if (await reportBatchSelector.isVisible().catch(() => false)) {
    await reportBatchSelector.selectOption(failedBatch.id).catch(() => undefined)
    await page.waitForTimeout(1200)
  }
  await capture(page, "08-report.png")

  await page.goto(`/versions/${versionB.id}?tab=exploration`)
  await expect(page.getByTestId("exploration-tab")).toBeVisible({ timeout: 120_000 })
  const batchFilter = page.getByTestId("chain-batch-filter")
  if (await batchFilter.isVisible().catch(() => false)) {
    await batchFilter.selectOption("all").catch(() => undefined)
  }
  await expect(page.locator('[data-testid^="chain-node-"]').first()).toBeVisible({
    timeout: 120_000,
  })
  await page.waitForTimeout(900)
  await capture(page, "09-exploration-chain.png")

  // AI-materialized issue with its evidence/code refs (deep link ?issue=).
  await page.goto(`/versions/${versionB.id}?tab=issues&issue=${issue.id}`)
  await expect(page.getByTestId("issues-tab")).toBeVisible({ timeout: 120_000 })
  await expect(page.getByTestId("issue-detail")).toBeVisible({ timeout: 120_000 })
  await page.waitForTimeout(600)
  await capture(page, "15-version-issues.png")

  await page.goto(`/versions/${versionB.id}?tab=overview`)
  await expect(page.getByText("批次时间线").first()).toBeVisible({ timeout: 120_000 })
  await capture(page, "10-version-overview.png")

  // --- 8. management-side screenshots --------------------------------------
  await page.goto("/agents")
  await expect(page.getByTestId("agent-center-page")).toBeVisible({ timeout: 120_000 })
  await expect(page.getByTestId("agent-role-row")).toHaveCount(13, { timeout: 120_000 })
  await capture(page, "11-agent-console.png")

  await page.goto(`/resources?tab=systems&system=${String(seedA.systemId)}`)
  await expect(page.getByRole("heading", { name: "公共资源", exact: true })).toBeVisible({
    timeout: 90_000,
  })
  await expect(page.getByText(DEMO.systemA, { exact: false }).first()).toBeVisible({
    timeout: 90_000,
  })
  await page.waitForTimeout(500)
  await capture(page, "12-resources.png")

  await page.goto("/knowledge")
  await expect(page.getByRole("heading", { name: "知识库", exact: true })).toBeVisible({
    timeout: 90_000,
  })
  await expect(page.getByText("订单中心测试数据准备规范").first()).toBeVisible({
    timeout: 120_000,
  })
  await capture(page, "13-knowledge.png")

  await page.goto("/quality")
  await expect(page.getByTestId("quality-page")).toBeVisible({ timeout: 120_000 })
  await expect(page.getByTestId("quality-summary")).toBeVisible({ timeout: 120_000 })
  await capture(page, "14-quality-center.png")

  // --- 9. final versions list with the completed execution version ----------
  await page.goto("/versions")
  await expect(page.getByRole("heading", { name: "版本", exact: true })).toBeVisible({
    timeout: 90_000,
  })
  await expect(
    page.getByRole("row").filter({ hasText: DEMO.versionB.name }).first(),
  ).toBeVisible({ timeout: 120_000 })
  const completedVersion = await getP7Version(request, seedB.token, versionB.id)
  expect(completedVersion.status).toBe("completed")
  await page.waitForTimeout(500)
  await capture(page, "02-versions.png")

  // Sanity: the captured versions exist with their demo names.
  const versionsResponse = await request.get(`${API_URL}/api/versions?limit=20`, {
    headers: authHeaders(token),
  })
  const listed = await readData<Array<{ name: string }>>(versionsResponse, 200, "list versions")
  expect(listed.map((item) => item.name)).toContain(DEMO.versionA.name)
  expect(listed.map((item) => item.name)).toContain(DEMO.versionB.name)
  // The extra versions keep the list screenshot status variety.
  expect(listed.map((item) => item.name)).toContain(draftVersion.name)
  expect(listed.map((item) => item.name)).toContain(draftVersion2.name)
  expect(listed.map((item) => item.name)).toContain(clarifyingVersion.name)
  console.log("README screenshots captured into docs/assets/screenshots")
})

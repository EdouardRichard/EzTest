import { mkdirSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import {
  type APIRequestContext,
  type APIResponse,
  type ConsoleMessage,
  expect,
  type Page,
  test,
} from "@playwright/test"

import {
  ADMIN_PASSWORD,
  API_URL,
  type RequirementRecord,
  uniqueSuffix,
  type VersionRecord,
} from "./p3-helpers"
import { authHeaders } from "./p4-helpers"
import { approveMindmap, seedMindmapVersion, submitMindmapReview } from "./p5-helpers"
import {
  type Batch,
  type CaseRun,
  fakeLlmUrl,
  fixtureTargetUrl,
  listCaseRuns,
  readVersionEvents,
  sleep,
  type VersionEvent,
  waitBatch,
  waitCaseRun,
} from "./p6-helpers"

/**
 * Shared P7 autonomous-loop E2E helpers (design spec §10.9/§12.4/§12.5).
 *
 * Every helper talks to the real API, the real Playwright executor and the
 * deterministic fake LLM; nothing is mocked. P7 ids stay decimal strings
 * end-to-end (D29) and are never passed through Number().
 */

/** Fixture target base URL (the P6 pages live under `/p6/*`). */
export function p7FixtureUrl(): string {
  return fixtureTargetUrl()
}

/** Real Playwright executor base URL started by scripts/e2e.ps1. */
export function p7ExecutorUrl(): string {
  const configured = process.env.EZTEST_E2E_EXECUTOR_URL?.trim()
  if (configured !== undefined && configured !== "") return configured.replace(/\/+$/, "")
  return "http://127.0.0.1:13336"
}

/** Accepted (or interactive) P7 fake-LLM agent modes. */
export type P7Mode = "normal" | "noop" | "degraded" | "invalid"

export type P7HarnessOptions = {
  /** True for specs that execute batches through the real executor. */
  requireExecutor?: boolean
  /** True for specs that test a real DB target connection. */
  requireDbTarget?: boolean
  /** False for specs that do not call the P7 agents (defaults to true). */
  requireFakeLlm?: boolean
}

/** Reads the DB target fixture exported by scripts/e2e.ps1 from .env. */
export function dbTargetFixture(): {
  host: string
  port: number
  database: string
  username: string
  password: string
  sslmode: string
} | null {
  const host = process.env.EZTEST_E2E_DB_TARGET_HOST?.trim() ?? ""
  const port = Number(process.env.EZTEST_E2E_DB_TARGET_PORT?.trim() ?? "")
  const database = process.env.EZTEST_E2E_DB_TARGET_DATABASE?.trim() ?? ""
  const username = process.env.EZTEST_E2E_DB_TARGET_USERNAME?.trim() ?? ""
  const password = process.env.EZTEST_E2E_DB_TARGET_PASSWORD ?? ""
  if (host === "" || database === "" || username === "" || password === "") return null
  if (!Number.isInteger(port) || port <= 0) return null
  const sslmode = process.env.EZTEST_E2E_DB_TARGET_SSLMODE?.trim() || "disable"
  return { host, port, database, username, password, sslmode }
}

/**
 * Skips the calling spec when the deterministic harness (admin credentials,
 * fake LLM, executor, DB target fixture) is absent, so a local browser run never
 * talks to a real provider. A missing P7 API route is not skipped: that is a
 * wiring blocker the spec must surface.
 */
export async function p7HarnessSkipReason(options: P7HarnessOptions = {}): Promise<string> {
  if (!ADMIN_PASSWORD) {
    return "EZTEST_E2E_ADMIN_PASSWORD is not set; run via scripts/e2e.ps1"
  }
  if (options.requireFakeLlm !== false) {
    try {
      const response = await fetch(`${fakeLlmUrl()}/v1/models`)
      if (!response.ok) {
        return `fake LLM at ${fakeLlmUrl()} answered ${response.status}; run via scripts/e2e.ps1`
      }
    } catch {
      return `fake LLM is unavailable at ${fakeLlmUrl()}; run via scripts/e2e.ps1`
    }
  }
  if (options.requireExecutor === true) {
    try {
      const response = await fetch(`${p7ExecutorUrl()}/healthz`)
      if (!response.ok) {
        return `executor at ${p7ExecutorUrl()} answered ${response.status}; run via scripts/e2e.ps1`
      }
    } catch {
      return `executor is unavailable at ${p7ExecutorUrl()}; run via scripts/e2e.ps1`
    }
  }
  if (options.requireDbTarget === true && dbTargetFixture() === null) {
    return "EZTEST_E2E_DB_TARGET_* is not set; run via scripts/e2e.ps1"
  }
  return ""
}

/** Marks the whole spec skipped with a reason when the harness is absent. */
export async function skipP7SpecWhenHarnessMissing(
  options: P7HarnessOptions = {},
): Promise<boolean> {
  const reason = await p7HarnessSkipReason(options)
  if (reason === "") return false
  test.skip(true, reason)
  return true
}

// --- envelopes / ids --------------------------------------------------------

async function readData<T>(
  response: APIResponse,
  expected: number | number[],
  label: string,
): Promise<T> {
  const allowed = Array.isArray(expected) ? expected : [expected]
  expect(allowed, `${label}: unexpected HTTP ${response.status()}`).toContain(response.status())
  const body = (await response.json()) as { data: T }
  return body.data
}

async function readPage<T>(response: APIResponse, label: string): Promise<T[]> {
  expect(response.status(), `${label}: unexpected HTTP ${response.status()}`).toBe(200)
  const body = (await response.json()) as { data?: T[] }
  return Array.isArray(body.data) ? body.data : []
}

// --- P7 DTOs (hand-written mirrors of the OpenAPI contract) -----------------

export type P7Version = VersionRecord & {
  paused?: boolean
  pause_mode?: string
  batch_no?: number
  continuePhase?: string | null
  continuedAt?: string | null
}

export type P7Batch = Batch & {
  kind: "baseline" | "incremental" | "supplement"
  parentBatchId: string | null
  graphHash: string
  baseFallbackReason: string
}

export type P7CaseRun = CaseRun & {
  source: "baseline" | "ai_supplement"
}

export type IssueRef = { kind: string; id: string }

export type IssueCodeRef = {
  anchorId: string
  path: string
  line: number
  kind: string
  label: string
}

export type P7Issue = {
  id: string
  versionId: string
  batchId: string | null
  caseRunId: string | null
  caseNodeId: string | null
  source: string
  category: string
  severity: string
  status: string
  confidence: string
  failureClass: string | null
  title: string
  description?: string
  reproSteps?: string
  expected?: string
  actual?: string
  rerunSuggestion: string
  recurrenceCount: number
  revision: number
  refs: IssueRef[]
  codeRefs: IssueCodeRef[]
  dismissedReason: string
  createdAt: string
  updatedAt: string
}

export type P7ReportCase = {
  caseRunId: string
  caseNodeId: string
  title: string
  source: string
  status: string
  failureClass: string
  stepCount: number
  assertionCount: number
  evidenceCount: number
}

export type P7ReportContent = {
  schema: string
  degraded: boolean
  summary: {
    caseCount: number
    passed: number
    failed: number
    blocked: number
    inconclusive: number
    skipped: number
    interrupted: number
    flakyCount: number
  }
  cases: P7ReportCase[]
  issues: Array<{ issueId: string; title: string; severity: string; status: string }>
  requirements: Array<{
    requirementId: string
    title: string
    caseCount: number
    passed: number
    failed: number
  }>
  risks: string[]
  appendix: { environment: string; system: string; generatedAt: string }
}

export type P7ReportSummary = {
  id: string
  versionId: string
  batchId: string
  revision: number
  status: string
  title: string
  inputFingerprint: string
  markdown: { sha256: string; sizeBytes: number; downloadPath: string }
  staleReason: string
  publishedAt: string | null
  createdAt: string
  updatedAt: string
}

export type P7ReportDetail = P7ReportSummary & {
  batchNo: number
  batchKind: string
  content: P7ReportContent
}

export type P7ReportDiff = {
  baseReportId: string
  targetReportId: string
  baseBatchNo: number
  targetBatchNo: number
  summary: {
    addedIssues: number
    fixedIssues: number
    regressed: number
    recovered: number
    newCases: number
    removedCases: number
    changed: number
  }
  addedIssues: Array<{ issueId: string; title: string; severity: string }>
  fixedIssues: Array<{ issueId: string; title: string; severity: string }>
  newCases: Array<{ caseNodeId: string; title: string; source: string }>
}

export type P7Analysis = {
  id: string
  versionId: string
  batchId: string
  kind: string
  status: string
  model: string
  promptVersion: string
  result?: unknown
  createdAt: string
}

export type P7IncrementalPlan = {
  versionId: string
  phase: string
  parentBatchId: string | null
  run: { id: string; kind: string; status: string; result: unknown } | null
  newCases: Array<{
    nodeId: string
    title: string
    priority: string
    confidence: string
    requirementIds: string[]
    status: string
  }>
  affectedCases: Array<{
    nodeId: string
    title: string
    confidence: string
    reason: string
    source: string
    selected: boolean
  }>
  unmappedChanges: Array<{ path: string; summary: string }>
  defaultCaseIds: string[]
  warnings: Array<{ code: string; message: string }>
  degraded: boolean
}

export type P7DbTarget = {
  id: string
  environmentId: string
  host: string
  port: number
  database: string
  username: string
  credentialSet: boolean
  sslmode: string
  enabled: boolean
  lastTestStatus: string
}

// --- harness primitives -----------------------------------------------------

/**
 * Copies one fixture session (the same recipe as the P6 seed): log in to the
 * fixture /p6/login endpoint, then import the resulting cookie as a
 * storage-state session so the real executor can run the seeded case.
 */
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

async function importFixtureSession(
  request: APIRequestContext,
  token: string,
  input: { fixtureUrl: string; environmentId: number; accessId: number },
): Promise<{ sessionId: number; cookie: string }> {
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
  const importResponse = await request.post(
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
        note: "P7 E2E imported fixture session",
      },
    },
  )
  const session = await readData<{ id: number; state: string }>(
    importResponse,
    201,
    "import fixture session",
  )
  expect(session.state).toBe("valid")
  return { sessionId: session.id, cookie }
}

type MindmapNodeRecord = { id: string; title: string }
type MindmapStepRecord = { id: string; seq: number }
type MindmapAssertionRecord = { id: string; kind: string }

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

export type P7SeedOptions = {
  /** Unique suffix for seeded keys; defaults to `uniqueSuffix()`. */
  suffix?: string
  /** Title of the seeded case (the fake analysis derives its issue title). */
  caseTitle?: string
  /** Adds one SQL `SELECT 1` db assertion (requires a configured db target). */
  withDbAssertion?: boolean
  /** Import one valid fixture session; defaults to true. */
  seedSession?: boolean
}

/** Everything the P7 specs need from one seeded executing version. */
export type P7ExecutionSeed = {
  token: string
  suffix: string
  fixtureUrl: string
  systemId: number
  environmentId: number
  environmentName: string
  version: P7Version
  requirements: RequirementRecord[]
  scenarioNodeId: string
  caseNodeId: string
  stepIds: string[]
  assertionIds: string[]
  dbAssertionId: string
  authFlowId: string
  accountId: number
  accessId: number
  sessionId: number
  sessionCookie: string
}

/**
 * Seeds an executing version through the real APIs, mirroring the P6 execution
 * seed but adding an optional `kind=db` assertion before the baseline freeze.
 * The version ends in `executing` with an approved baseline, an imported fixture
 * session and (when requested) one db assertion the fake executor drives through
 * `execution.db_assert`.
 */
export async function seedP7ExecutionVersion(
  request: APIRequestContext,
  options: P7SeedOptions = {},
): Promise<P7ExecutionSeed> {
  const suffix = options.suffix ?? uniqueSuffix()
  const fixtureUrl = p7FixtureUrl()
  const withDbAssertion = options.withDbAssertion === true
  const seedSession = options.seedSession !== false

  // System + environment + code repository + version with two confirmed
  // requirements (P5 seed, ending in `designing`).
  const base = await seedMindmapVersion(request, { suffix, fixtureUrl })
  const token = base.token

  // Auto-login recipe + login check the fixture session import validates.
  const envResponse = await request.patch(
    `${API_URL}/api/resources/environments/${base.environmentId}`,
    {
      headers: authHeaders(token),
      data: {
        allowAutoLogin: true,
        loginCheck: {
          url: "/p6/home",
          expectStatus: [200],
          bodyContains: "P6 Home",
        },
      },
    },
  )
  await readData(envResponse, 200, "patch P7 environment")

  const loginUrl = `${fixtureUrl}/p6/login`
  const authFlowResponse = await request.post(`${API_URL}/api/resources/auth-flows`, {
    headers: authHeaders(token),
    data: {
      system_id: String(base.systemId),
      name: `P7 login ${suffix}`,
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
  const authFlow = await readData<{ id: string }>(authFlowResponse, 201, "create P7 auth flow")

  const accountResponse = await request.post(`${API_URL}/api/resources/accounts`, {
    headers: authHeaders(token),
    data: {
      key: `p7-account-${suffix}`,
      label: `P7 Account ${suffix}`,
      username: "fixture",
      kind: "human",
    },
  })
  const account = await readData<{ id: number }>(accountResponse, 201, "create P7 account")

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
  const access = await readData<{ id: number }>(accessResponse, 201, "create P7 access")

  const scenario = await postMindmapNode(request, token, base.version.id, {
    kind: "scenario",
    title: `${options.caseTitle ?? "P7 下单流程"}场景`,
    description: "P7 E2E 自主链执行场景",
  })
  const caseNode = await postMindmapNode(request, token, base.version.id, {
    kind: "case",
    parentId: scenario.id,
    title: options.caseTitle ?? "P7 登录后创建订单",
    priority: "P0",
    preconditions: "已通过 fixture 会话登录",
    requirementIds: [base.requirements[0]?.id].filter((id): id is string => id !== undefined),
  })
  const firstStep = await postMindmapStep(request, token, base.version.id, caseNode.id, {
    action: "打开 /p6/orders/new 并填写订单号",
    expected: "新建订单表单可见",
  })
  const secondStep = await postMindmapStep(request, token, base.version.id, caseNode.id, {
    action: "提交新建订单并确认创建成功",
    expected: "订单详情显示新订单号",
  })
  const uiAssertion = await postMindmapAssertion(request, token, base.version.id, caseNode.id, {
    stepId: secondStep.id,
    kind: "ui",
    target: "#result",
    expression: "text contains 订单创建成功",
    expected: "订单创建成功",
  })

  let dbAssertionId = ""
  if (withDbAssertion) {
    const dbAssertion = await postMindmapAssertion(request, token, base.version.id, caseNode.id, {
      stepId: secondStep.id,
      kind: "db",
      target: "p7-e2e-target",
      expression: "SELECT 1",
      expected: JSON.stringify({ rows: [["1"]], ordered: true }),
    })
    dbAssertionId = dbAssertion.id
  }

  await submitMindmapReview(request, token, base.version.id)
  const approved = await approveMindmap(request, token, base.version.id)
  expect(approved.version.status, "approval must move the version to executing").toBe("executing")
  expect(approved.baseline, "approval must freeze a baseline").not.toBeNull()

  let sessionId = 0
  let sessionCookie = ""
  if (seedSession) {
    const imported = await importFixtureSession(request, token, {
      fixtureUrl,
      environmentId: base.environmentId,
      accessId: access.id,
    })
    sessionId = imported.sessionId
    sessionCookie = imported.cookie
  }

  const environmentResponse = await request.get(
    `${API_URL}/api/resources/environments/${base.environmentId}`,
    { headers: authHeaders(token) },
  )
  const environment = await readData<{ id: number; name: string }>(
    environmentResponse,
    200,
    "get P7 environment",
  )

  return {
    token,
    suffix,
    fixtureUrl,
    systemId: base.systemId,
    environmentId: base.environmentId,
    environmentName: environment.name,
    version: approved.version as P7Version,
    requirements: base.requirements,
    scenarioNodeId: scenario.id,
    caseNodeId: caseNode.id,
    stepIds: [firstStep.id, secondStep.id],
    assertionIds: withDbAssertion
      ? [uiAssertion.id, dbAssertionId].filter((id) => id !== "")
      : [uiAssertion.id],
    dbAssertionId,
    authFlowId: authFlow.id,
    accountId: account.id,
    accessId: access.id,
    sessionId,
    sessionCookie,
  }
}

// --- version / batches ------------------------------------------------------

/** GET /api/versions/{id} (P7 additive fields included). */
export async function getP7Version(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<P7Version> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}`, {
    headers: authHeaders(token),
  })
  return readData<P7Version>(response, 200, "get version")
}

/** Polls the version snapshot until it reaches one of the statuses. */
export async function waitVersionStatus(
  request: APIRequestContext,
  token: string,
  versionId: string,
  status: string | string[],
  options: { timeoutMs?: number } = {},
): Promise<P7Version> {
  const allowed = Array.isArray(status) ? status : [status]
  await expect
    .poll(async () => (await getP7Version(request, token, versionId)).status, {
      message: `version ${versionId} must become ${allowed.join("/")}`,
      timeout: options.timeoutMs ?? 300_000,
    })
    .toMatch(new RegExp(`^(${allowed.join("|")})$`))
  return getP7Version(request, token, versionId)
}

/** Waits until the autonomous chain finalized the version to `completed`. */
export async function waitVersionCompleted(
  request: APIRequestContext,
  token: string,
  versionId: string,
  timeoutMs = 300_000,
): Promise<P7Version> {
  const version = await waitVersionStatus(request, token, versionId, "completed", { timeoutMs })
  expect(version.continuePhase, "a completed version clears continuePhase").toBeNull()
  return version
}

/** GET /api/versions/{id}/batches (newest first, one page). */
export async function listP7Batches(
  request: APIRequestContext,
  token: string,
  versionId: string,
  filter: { kind?: string; status?: string } = {},
): Promise<P7Batch[]> {
  const params = new URLSearchParams({ limit: "100" })
  if (filter.kind !== undefined) params.set("kind", filter.kind)
  if (filter.status !== undefined) params.set("status", filter.status)
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/batches?${params.toString()}`,
    { headers: authHeaders(token) },
  )
  return readPage<P7Batch>(response, "list batches")
}

/** Finds the newest batch of one kind (optionally of one parent). */
export async function findP7Batch(
  request: APIRequestContext,
  token: string,
  versionId: string,
  kind: P7Batch["kind"],
  parentBatchId?: string,
): Promise<P7Batch | null> {
  const batches = await listP7Batches(request, token, versionId, { kind })
  return (
    batches.find((batch) => parentBatchId === undefined || batch.parentBatchId === parentBatchId) ??
    null
  )
}

/** Polls the batch list until a batch of the kind (and parent) exists. */
export async function waitP7BatchKind(
  request: APIRequestContext,
  token: string,
  versionId: string,
  kind: P7Batch["kind"],
  options: { parentBatchId?: string; timeoutMs?: number } = {},
): Promise<P7Batch> {
  const timeout = options.timeoutMs ?? 240_000
  const deadline = Date.now() + timeout
  let seen: string[] = []
  while (Date.now() < deadline) {
    const batches = await listP7Batches(request, token, versionId)
    seen = batches.map((batch) => `${batch.kind}:${batch.id}:${batch.status}`)
    const found = batches.find(
      (batch) =>
        batch.kind === kind &&
        (options.parentBatchId === undefined || batch.parentBatchId === options.parentBatchId),
    )
    if (found !== undefined) return found
    await sleep(300)
  }
  throw new Error(
    `no ${kind} batch appeared within ${timeout}ms (batches: ${seen.join(", ") || "none"})`,
  )
}

/** Waits for a batch id to reach a terminal status. */
export async function waitP7BatchTerminal(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  timeoutMs = 300_000,
): Promise<P7Batch> {
  const batch = await waitBatch(request, token, versionId, batchId, { timeoutMs })
  return batch as P7Batch
}

/** One page of case runs with their P7 source. */
export async function listP7CaseRuns(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
): Promise<P7CaseRun[]> {
  const runs = await listCaseRuns(request, token, versionId, batchId, { limit: 100 })
  return runs as P7CaseRun[]
}

/** Polls one case run like `waitCaseRun`, returning the P7 source projection. */
export async function waitP7CaseRun(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  caseRunId: string,
  timeoutMs = 300_000,
): Promise<P7CaseRun> {
  const run = await waitCaseRun(request, token, versionId, batchId, caseRunId, { timeoutMs })
  return run as P7CaseRun
}

// --- mindmap supplement nodes ----------------------------------------------

export type P7SupplementNode = {
  id: string
  title: string
  source: string
  status: string
}

/** GET /api/versions/{id}/mindmap, reduced to the ai_supplement nodes. */
export async function listP7SupplementNodes(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<P7SupplementNode[]> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/mindmap`, {
    headers: authHeaders(token),
  })
  const graph = await readData<{
    nodes?: Array<{ id: string; title: string; source: string; status: string }>
  }>(response, 200, "get mindmap")
  return (graph.nodes ?? [])
    .filter((node) => node.source === "ai_supplement")
    .map((node) => ({ id: node.id, title: node.title, source: node.source, status: node.status }))
}

// --- reports ----------------------------------------------------------------

/** GET /api/versions/{id}/reports (newest first). */
export async function listP7Reports(
  request: APIRequestContext,
  token: string,
  versionId: string,
  filter: { batchId?: string; status?: string; limit?: number } = {},
): Promise<P7ReportSummary[]> {
  const params = new URLSearchParams({ limit: String(Math.min(filter.limit ?? 100, 100)) })
  if (filter.batchId !== undefined) params.set("batchId", filter.batchId)
  if (filter.status !== undefined) params.set("status", filter.status)
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/reports?${params.toString()}`,
    { headers: authHeaders(token) },
  )
  return readPage<P7ReportSummary>(response, "list reports")
}

/** GET /api/versions/{id}/reports/{reportId} — the read also refreshes stale. */
export async function getP7Report(
  request: APIRequestContext,
  token: string,
  versionId: string,
  reportId: string,
): Promise<P7ReportDetail> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/reports/${reportId}`, {
    headers: authHeaders(token),
  })
  return readData<P7ReportDetail>(response, 200, "get report")
}

/** Polls a batch report list until the batch has one, then reads its detail. */
export async function waitP7ReportForBatch(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  timeoutMs = 180_000,
): Promise<P7ReportDetail> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const reports = await listP7Reports(request, token, versionId, { batchId })
    const first = reports[0]
    if (first !== undefined) return getP7Report(request, token, versionId, first.id)
    await sleep(300)
  }
  throw new Error(`batch ${batchId} has no report after ${timeoutMs}ms`)
}

/** POST .../reports/{reportId}/regenerate — appends revision+1. */
export async function regenerateP7Report(
  request: APIRequestContext,
  token: string,
  versionId: string,
  reportId: string,
): Promise<P7ReportDetail> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/reports/${reportId}/regenerate`,
    { headers: authHeaders(token), data: {} },
  )
  return readData<P7ReportDetail>(response, [200, 201], "regenerate report")
}

/** GET .../reports/{reportId}/diff?baseReportId=... */
export async function diffP7Reports(
  request: APIRequestContext,
  token: string,
  versionId: string,
  reportId: string,
  baseReportId?: string,
): Promise<P7ReportDiff> {
  const query =
    baseReportId === undefined ? "" : `?baseReportId=${encodeURIComponent(baseReportId)}`
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/reports/${reportId}/diff${query}`,
    { headers: authHeaders(token) },
  )
  return readData<P7ReportDiff>(response, 200, "diff reports")
}

/** GET .../reports/{reportId}/export?format=markdown — verified bytes. */
export async function exportP7Report(
  request: APIRequestContext,
  token: string,
  versionId: string,
  reportId: string,
): Promise<{ body: Buffer; sha256: string; contentType: string }> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/reports/${reportId}/export?format=markdown`,
    { headers: authHeaders(token) },
  )
  expect(response.status(), "export report").toBe(200)
  const headers = response.headers()
  return {
    body: await response.body(),
    sha256: headers["x-content-sha256"] ?? "",
    contentType: headers["content-type"] ?? "",
  }
}

// --- issues -----------------------------------------------------------------

/** GET /api/versions/{id}/issues (page envelope). */
export async function listP7Issues(
  request: APIRequestContext,
  token: string,
  versionId: string,
  filter: { status?: string; batchId?: string; limit?: number } = {},
): Promise<P7Issue[]> {
  const params = new URLSearchParams({ limit: String(Math.min(filter.limit ?? 100, 100)) })
  if (filter.status !== undefined) params.set("status", filter.status)
  if (filter.batchId !== undefined) params.set("batchId", filter.batchId)
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/issues?${params.toString()}`,
    { headers: authHeaders(token) },
  )
  return readPage<P7Issue>(response, "list issues")
}

/** GET /api/versions/{id}/issues/{issueId}. */
export async function getP7Issue(
  request: APIRequestContext,
  token: string,
  versionId: string,
  issueId: string,
): Promise<P7Issue> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/issues/${issueId}`, {
    headers: authHeaders(token),
  })
  return readData<P7Issue>(response, 200, "get issue")
}

/** Polls the issue list until `predicate` matches an item. */
export async function waitP7Issue(
  request: APIRequestContext,
  token: string,
  versionId: string,
  predicate: (issue: P7Issue) => boolean,
  timeoutMs = 240_000,
): Promise<P7Issue> {
  const deadline = Date.now() + timeoutMs
  let last: P7Issue[] = []
  while (Date.now() < deadline) {
    last = await listP7Issues(request, token, versionId)
    const found = last.find(predicate)
    if (found !== undefined) return found
    await sleep(500)
  }
  throw new Error(
    `no issue matched within ${timeoutMs}ms (seen: ${last.map((issue) => `${issue.id}:${issue.title}`).join(", ") || "none"})`,
  )
}

/** POST /api/versions/{id}/issues — a human issue (used for dismiss). */
export async function createHumanIssue(
  request: APIRequestContext,
  token: string,
  versionId: string,
  input: {
    title: string
    severity: string
    description?: string
    reproSteps?: string
    caseRunId?: string
    caseNodeId?: string
    requirementId?: string
  },
): Promise<P7Issue> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/issues`, {
    headers: authHeaders(token),
    data: {
      category: "other",
      confidence: "medium",
      ...input,
    },
  })
  return readData<P7Issue>(response, 201, "create human issue")
}

/** PATCH /api/versions/{id}/issues/{issueId} under the revision CAS. */
export async function patchP7Issue(
  request: APIRequestContext,
  token: string,
  versionId: string,
  issueId: string,
  input: { status: "confirmed" | "dismissed"; expectedRevision: number; comment?: string },
): Promise<P7Issue> {
  const response = await request.patch(`${API_URL}/api/versions/${versionId}/issues/${issueId}`, {
    headers: authHeaders(token),
    data: input,
  })
  return readData<P7Issue>(response, 200, "patch issue")
}

// --- analysis ---------------------------------------------------------------

/** GET /api/versions/{id}/batches/{batchId}/analysis. */
export async function listP7Analyses(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
): Promise<P7Analysis[]> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/batches/${batchId}/analysis`,
    { headers: authHeaders(token) },
  )
  return readData<P7Analysis[]>(response, 200, "list analyses")
}

/** Polls the analysis list until the expected kinds are present and settled. */
export async function waitP7AnalysisSettled(
  request: APIRequestContext,
  token: string,
  versionId: string,
  batchId: string,
  kinds: string[],
  timeoutMs = 240_000,
): Promise<P7Analysis[]> {
  const deadline = Date.now() + timeoutMs
  let last: P7Analysis[] = []
  while (Date.now() < deadline) {
    last = await listP7Analyses(request, token, versionId, batchId)
    const settled = kinds.every((kind) => {
      const run = last.find((candidate) => candidate.kind === kind)
      return run !== undefined && ["done", "failed", "degraded"].includes(run.status)
    })
    if (settled) return last
    await sleep(500)
  }
  throw new Error(
    `analyses ${kinds.join("/")} did not settle within ${timeoutMs}ms ` +
      `(last: ${last.map((run) => `${run.kind}:${run.status}`).join(", ") || "none"})`,
  )
}

// --- incremental plan -------------------------------------------------------

/** GET /api/versions/{id}/mindmap/incremental-plan. */
export async function getP7IncrementalPlan(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<P7IncrementalPlan> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/mindmap/incremental-plan`,
    { headers: authHeaders(token) },
  )
  return readData<P7IncrementalPlan>(response, 200, "get incremental plan")
}

/**
 * Polls the plan until the incremental_design run settled and produced
 * new/affected cases (the autonomy worker starts the run asynchronously).
 */
export async function waitP7IncrementalPlan(
  request: APIRequestContext,
  token: string,
  versionId: string,
  timeoutMs = 240_000,
): Promise<P7IncrementalPlan> {
  const deadline = Date.now() + timeoutMs
  let last: P7IncrementalPlan | null = null
  while (Date.now() < deadline) {
    last = await getP7IncrementalPlan(request, token, versionId)
    const run = last.run
    const settled = run !== null && ["done", "degraded", "failed"].includes(run.status)
    if (settled && last.newCases.length + last.affectedCases.length > 0) {
      if (run?.status === "failed") {
        throw new Error("incremental design run failed; the plan is unusable")
      }
      return last
    }
    await sleep(500)
  }
  throw new Error(
    `incremental plan was not ready within ${timeoutMs}ms ` +
      `(run: ${last?.run?.status ?? "none"}, new: ${last?.newCases.length ?? 0}, ` +
      `affected: ${last?.affectedCases.length ?? 0})`,
  )
}

// --- DB target --------------------------------------------------------------

/** GET /api/resources/systems/{id} (P2 catalog read). */
export async function getP7System(
  request: APIRequestContext,
  token: string,
  systemId: number,
): Promise<{ id: number; name: string }> {
  const response = await request.get(`${API_URL}/api/resources/systems/${systemId}`, {
    headers: authHeaders(token),
  })
  return readData<{ id: number; name: string }>(response, 200, "get system")
}

/** GET /api/resources/environments/{id} (P2 catalog read). */
export async function getP7Environment(
  request: APIRequestContext,
  token: string,
  environmentId: number,
): Promise<{ id: number; name: string }> {
  const response = await request.get(`${API_URL}/api/resources/environments/${environmentId}`, {
    headers: authHeaders(token),
  })
  return readData<{ id: number; name: string }>(response, 200, "get environment")
}

export type DbTargetInput = {
  host: string
  port: number
  database: string
  username: string
  password?: string
  sslmode?: string
  connectTimeoutMs?: number
  statementTimeoutMs?: number
  maxRows?: number
  enabled?: boolean
}

/** GET /api/resources/environments/{id}/db-target (admin only). */
export async function getP7DbTarget(
  request: APIRequestContext,
  token: string,
  environmentId: number,
): Promise<P7DbTarget | null> {
  const response = await request.get(
    `${API_URL}/api/resources/environments/${environmentId}/db-target`,
    { headers: authHeaders(token) },
  )
  if (response.status() === 403) {
    throw new Error(`db target read is forbidden for this token (environment ${environmentId})`)
  }
  expect(response.status(), "get db target").toBe(200)
  const body = (await response.json()) as { data: P7DbTarget | null }
  return body.data
}

/** PUT /api/resources/environments/{id}/db-target (admin only). */
export async function upsertP7DbTarget(
  request: APIRequestContext,
  token: string,
  environmentId: number,
  input: DbTargetInput,
): Promise<P7DbTarget> {
  const response = await request.put(
    `${API_URL}/api/resources/environments/${environmentId}/db-target`,
    {
      headers: authHeaders(token),
      data: {
        sslmode: input.sslmode ?? "disable",
        connectTimeoutMs: input.connectTimeoutMs ?? 3000,
        statementTimeoutMs: input.statementTimeoutMs ?? 5000,
        maxRows: input.maxRows ?? 100,
        enabled: input.enabled ?? true,
        ...input,
      },
    },
  )
  return readData<P7DbTarget>(response, [200, 201], "upsert db target")
}

/** Configures the seeded environment as the e2e DB target fixture. */
export async function configureP7DbTarget(
  request: APIRequestContext,
  token: string,
  environmentId: number,
): Promise<P7DbTarget> {
  const fixture = dbTargetFixture()
  if (fixture === null) {
    throw new Error("EZTEST_E2E_DB_TARGET_* is not set; run via scripts/e2e.ps1")
  }
  return upsertP7DbTarget(request, token, environmentId, fixture)
}

// --- fake LLM P7 mode -------------------------------------------------------

/** Reads the active fake LLM P7 mode through `GET /__p7-mode`. */
export async function getP7Mode(request: APIRequestContext): Promise<string> {
  const response = await request.get(`${fakeLlmUrl()}/__p7-mode`)
  expect(response.status(), "GET fake P7 mode").toBe(200)
  const body = (await response.json()) as { mode?: string }
  return body.mode ?? ""
}

/**
 * Switches the fake LLM's three P7 profiles through `POST /__p7-mode`. The mode
 * is global to the fake process, so specs that change it must reset it to
 * `normal` in a finally/afterEach path.
 */
export async function setP7Mode(request: APIRequestContext, mode: P7Mode): Promise<void> {
  const response = await request.post(`${fakeLlmUrl()}/__p7-mode`, { data: { mode } })
  expect(response.status(), `set fake P7 mode ${mode}`).toBe(200)
  const body = (await response.json()) as { mode?: string }
  expect(body.mode).toBe(mode)
}

// --- events -----------------------------------------------------------------

/**
 * Polls the persisted event snapshot until one event of each requested type
 * (optionally matching the predicate) exists. P7 chains emit well over one
 * 100-event page, so the helper pages backwards like the P6 specs do.
 */
export async function waitP7Events(
  request: APIRequestContext,
  token: string,
  versionId: string,
  expectations: Array<{ type: string; match?: (event: VersionEvent) => boolean }>,
  timeoutMs = 120_000,
): Promise<VersionEvent[]> {
  const deadline = Date.now() + timeoutMs
  let events: VersionEvent[] = []
  while (Date.now() < deadline) {
    events = await readVersionEvents(request, token, versionId, { maxPages: 5 })
    const missing = expectations.filter(
      (expected) =>
        !events.some(
          (event) =>
            event.type === expected.type && (expected.match === undefined || expected.match(event)),
        ),
    )
    if (missing.length === 0) return events
    await sleep(500)
  }
  throw new Error(
    `missing P7 events: ${expectations
      .filter((expected) => !events.some((event) => event.type === expected.type))
      .map((expected) => expected.type)
      .join(", ")}`,
  )
}

// --- browser helpers --------------------------------------------------------

/** Directory of the gitignored P7 acceptance screenshots. */
export const P7_ACCEPTANCE_DIR = fileURLToPath(new URL("../../.tmp/p7-acceptance", import.meta.url))

/** Returns a stable screenshot path under `.tmp/p7-acceptance/`. */
export function p7ScreenshotPath(name: string): string {
  mkdirSync(P7_ACCEPTANCE_DIR, { recursive: true })
  return join(P7_ACCEPTANCE_DIR, name)
}

/**
 * Collects browser console errors and page errors. Next.js dev-mode favicon /
 * missing-resource 404 noise is filtered; everything else fails the spec at the
 * end so a crashed P7 page is never silently accepted.
 */
export type ConsoleMonitor = {
  errors: string[]
  stop: () => void
}

const IGNORED_CONSOLE_ERRORS = [
  /favicon\.ico/i,
  /Failed to load resource: the server responded with a status of 404/i,
  /Download the React DevTools/i,
]

export function captureConsoleErrors(page: Page): ConsoleMonitor {
  const errors: string[] = []
  const onConsole = (message: ConsoleMessage): void => {
    if (message.type() !== "error") return
    const text = message.text()
    if (IGNORED_CONSOLE_ERRORS.some((pattern) => pattern.test(text))) return
    errors.push(text)
  }
  const onPageError = (error: Error): void => {
    errors.push(`pageerror: ${error.message}`)
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

/** Stops collecting, rejects a Next.js error overlay and asserts no errors. */
export async function expectNoConsoleErrors(
  monitor: ConsoleMonitor,
  page: Page,
  label: string,
): Promise<void> {
  monitor.stop()
  const body = (await page.locator("body").textContent()) ?? ""
  expect(
    /Application error|Unhandled Runtime Error/i.test(body),
    `${label}: the page must not render a Next.js error overlay`,
  ).toBe(false)
  expect(monitor.errors, `${label}: browser console errors`).toEqual([])
}

/** Opens the version detail shell and waits for its heading. */
export async function openP7VersionPage(page: Page, version: VersionRecord): Promise<void> {
  await page.goto(`/versions/${version.id}`)
  await expect(page.getByRole("heading", { name: version.name })).toBeVisible({ timeout: 60_000 })
}

/** Asserts a secret value is not rendered as page text. */
export async function expectNoSecretInPage(
  page: Page,
  secret: string,
  label: string,
): Promise<void> {
  if (secret === "") return
  const body = (await page.locator("body").textContent()) ?? ""
  expect(body.includes(secret), `${label}: the page must not render a credential`).toBe(false)
}

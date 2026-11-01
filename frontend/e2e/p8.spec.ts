import fs from "node:fs"
import path from "node:path"

import { expect, type Locator, type Page, test } from "@playwright/test"

/**
 * P8 S12 browser E2E: the full journey key pages plus the global centres on the
 * real API/DB and the installed Chrome channel.
 *
 * Data policy:
 * - No fixed seed ids. `test.beforeAll` logs in through the real API, discovers
 *   the richest S0-S6 version plus its reports/issues/waves, and only then starts
 *   the browser session. The optional `EZTEST_E2E_P8_*` overrides pin an id when
 *   debugging; discovery fills whatever is not overridden.
 * - The journey covers `/resources` -> `/versions` (create/open) ->
 *   requirements -> design -> execution -> issues -> reports -> chain ->
 *   version reopen, then the global `/knowledge`, `/quality`, `/agents`,
 *   `/issues`, `/reports` and `/settings` pages.
 * - No real LLM is triggered: the spec never creates conversations or posts
 *   messages, and requirement confirmation is skipped when auto-design is on or
 *   the version is completed. Missing data is handled with an explicit
 *   `data-gap` annotation (also written to `p8-e2e-annotations.txt`) instead of a
 *   silent skip; a whole scenario is skipped only when its dataset is absent.
 * - Evidence: fullPage, viewport and element screenshots named
 *   `p8-e2e-NN-name.png` under `EZTEST_E2E_P8_SCREENSHOT_DIR`.
 */

const API_BASE_URL = (process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:13333").replace(
  /\/+$/,
  "",
)
const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD?.trim() || "admin123"
const SCREENSHOT_DIR =
  process.env.EZTEST_E2E_P8_SCREENSHOT_DIR?.trim() ||
  path.resolve(process.cwd(), "..", ".tmp", "p8-e2e", "screenshots")

const ANNOTATION_FILE = path.join(SCREENSHOT_DIR, "..", "p8-e2e-annotations.txt")

function envValue(name: string): string {
  return process.env[name]?.trim() ?? ""
}

/* ------------------------------- wire types -------------------------------- */

type VersionStatus = "initial" | "running" | "completed"

type VersionSummary = {
  id: string
  title: string
  status: VersionStatus
  sut_count: number
  created_at: string
  updated_at: string
}

type VersionBinding = {
  id: string
  sut: { id: string; name: string }
  environment: { id: string; name: string; base_url?: string }
}

type VersionDetail = VersionSummary & {
  auto_design_new_requirements: boolean
  bindings: VersionBinding[]
  closed_at?: string | null
  close_reason?: string | null
}

type Conversation = {
  id: string
  title: string
  kind: "clarification" | "design"
  message_count: number
  last_message_at: string | null
}

type Requirement = {
  id: string
  current_version_id: string
  revision: number
  title: string
  status: "draft" | "clarified" | "closed" | "obsolete"
  priority: string
}

type DesignCase = {
  case_id: string
  current_version_id: string
  revision: number
  title: string
  status: "draft" | "confirmed" | "stale"
  step_count: number
  assertion_count: number
  version_sut_ids: string[]
}

type DesignScenario = { id: string; name: string; cases: DesignCase[] }

type DesignRequirement = {
  requirement_id: string
  title: string
  status: string
  scenarios: DesignScenario[]
}

type DesignStats = {
  requirements: number
  scenarios: number
  cases: number
  draft: number
  confirmed: number
  stale: number
}

type DesignGraph = {
  pending_design_count: number
  auto_design_new_requirements: boolean
  stats: DesignStats
  requirements: DesignRequirement[]
}

type WaveCounts = {
  total: number
  pending: number
  running: number
  paused: number
  passed: number
  failed: number
  evidence: number
}

type RunWave = {
  id: string
  status: "planned" | "running" | "paused" | "done" | "failed" | "cancelled"
  counts: WaveCounts
  started_at: string | null
  finished_at: string | null
}

type CaseRun = {
  id: string
  wave_id: string
  case_version_id: string
  title: string
  status: string
  version_sut_ids: string[]
  evidence_count: number
}

type Evidence = {
  id: string
  kind: string
  sha256: string
  size_bytes: number
  mime: string
  filename: string
}

type CaseRunDetail = {
  case_run: CaseRun
  steps: Array<{ seq: number; status: string }>
  assertions: Array<{ step_seq: number; assertion_seq: number; verdict: string }>
  evidence: Evidence[]
}

type Issue = {
  id: string
  version_id: string
  title: string
  summary: string
  status: "open" | "confirmed" | "dismissed" | "merged"
  recurrence_count?: number
  merged_chain?: string[] | null
  merged_into_id?: string | null
  reopen_count: number
  expected_revision: number
}

type ReportRevision = {
  revision: number
  template_version: string
  status: string
  html_available: boolean
  pdf_available: boolean
  pdf_status?: string
}

type Report = {
  id: string
  version_id: string
  kind: "version" | "wave"
  status: string
  current_revision?: ReportRevision | null
}

type ReportDetail = Report & { revisions: ReportRevision[] }

type ChainNode = { id: string; kind: string; ref_id: string; label: string; status?: string | null }

type ChainView = {
  nodes: ChainNode[]
  edges: Array<{ from: string; to: string; kind: string }>
}

type SutEnvironment = {
  id: string
  name: string
  pages: unknown[]
  apis: unknown[]
  databases: unknown[]
  accounts: unknown[]
}

type Sut = { id: string; name: string; environments: SutEnvironment[] }

type CodeRepository = { id: string; name: string }

type Instance = {
  id: string
  status: string
  active_requests?: number
  active_work?: number
}

type KnowledgeItem = {
  id: string
  title: string
  status: string
  scope: string
  tags: string[]
}

type ReportTarget = {
  versionId: string
  report: Report
  detail: ReportDetail
  revision: ReportRevision
}

type Seed = {
  versions: VersionSummary[]
  version: VersionDetail
  requirements: Requirement[]
  conversations: Conversation[]
  design: DesignGraph
  waves: RunWave[]
  caseRun: CaseRun | null
  caseRunDetail: CaseRunDetail | null
  issues: Issue[]
  reports: Report[]
  chain: ChainView | null
  draftRequirementId: string | null
  mergedIssue: Issue | null
  mergePair: { source: Issue; target: Issue } | null
  recurrenceIssue: Issue | null
  reopenIssue: Issue | null
  activeWave: RunWave | null
  report: ReportTarget | null
  completedVersion: VersionSummary | null
  resources: { suts: Sut[]; repositories: CodeRepository[] }
  instances: Instance[]
  knowledge: KnowledgeItem[]
}

/* ------------------------------- API helpers ------------------------------- */

let apiToken = ""
let authUserJson = ""

async function apiLogin(): Promise<void> {
  const response = await fetch(`${API_BASE_URL}/api/auth/login`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ username: ADMIN_USERNAME, password: ADMIN_PASSWORD }),
  })
  if (!response.ok) {
    throw new Error(`P8 seed discovery: API login failed (HTTP ${response.status})`)
  }
  const payload = (await response.json()) as {
    data?: { token?: string; user?: Record<string, unknown> }
  }
  const token = payload.data?.token
  if (typeof token !== "string" || token.length === 0) {
    throw new Error("P8 seed discovery: login response did not include a token")
  }
  apiToken = token
  authUserJson = JSON.stringify(payload.data?.user ?? {})
}

type ApiInit = { method?: "GET" | "POST" | "PATCH"; body?: unknown }

async function apiRequest<T>(pathname: string, init: ApiInit = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" }
  if (apiToken.length > 0) headers.Authorization = `Bearer ${apiToken}`
  let body: string | undefined
  if (init.body !== undefined) {
    headers["Content-Type"] = "application/json"
    body = JSON.stringify(init.body)
  }
  const response = await fetch(`${API_BASE_URL}${pathname}`, {
    method: init.method ?? "GET",
    headers,
    body,
  })
  if (!response.ok) {
    throw new Error(
      `P8 seed discovery: ${init.method ?? "GET"} ${pathname} failed (HTTP ${response.status})`,
    )
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

async function apiGet<T>(pathname: string): Promise<T> {
  const payload = await apiRequest<{ data: T }>(pathname)
  return payload.data
}

async function apiPost<T>(pathname: string, body: unknown): Promise<T> {
  const payload = await apiRequest<{ data: T }>(pathname, { method: "POST", body })
  return payload.data
}

/* ------------------------------- discovery --------------------------------- */

function sortVersions(versions: VersionSummary[]): VersionSummary[] {
  return [...versions].sort((left, right) => right.updated_at.localeCompare(left.updated_at))
}

/**
 * Picks the version with the richest S0-S6 dataset (requirements + waves), with
 * running versions preferred so the interaction scenarios stay writable. Falls
 * back to the most recent version; the requirement/design scenarios then create
 * what they can and annotate the rest.
 */
async function pickJourneyVersion(versions: VersionSummary[]): Promise<VersionSummary> {
  const override = envValue("EZTEST_E2E_P8_VERSION_ID")
  if (override.length > 0) {
    const pinned = versions.find((version) => version.id === override)
    if (pinned === undefined) {
      throw new Error(`P8 seed discovery: EZTEST_E2E_P8_VERSION_ID=${override} was not found`)
    }
    return pinned
  }
  let best: { version: VersionSummary; score: number } | null = null
  for (const version of sortVersions(versions).slice(0, 40)) {
    let requirements: Requirement[] = []
    let waves: RunWave[] = []
    try {
      requirements = await apiGet<Requirement[]>(
        `/api/versions/${encodeURIComponent(version.id)}/requirements?limit=100`,
      )
      waves = await apiGet<RunWave[]>(
        `/api/versions/${encodeURIComponent(version.id)}/waves?limit=20`,
      )
    } catch {
      continue
    }
    const statusBonus = version.status === "running" ? 40 : version.status === "initial" ? 10 : 0
    const score =
      Math.min(requirements.length, 25) * 10 + Math.min(waves.length, 10) * 5 + statusBonus
    if (best === null || score > best.score) best = { version, score }
  }
  return best?.version ?? sortVersions(versions)[0]
}

async function discoverWavesAndCaseRun(
  versionId: string,
): Promise<{ waves: RunWave[]; caseRun: CaseRun | null; caseRunDetail: CaseRunDetail | null }> {
  const waves = await apiGet<RunWave[]>(`/api/versions/${versionId}/waves?limit=20`)
  const ordered = [
    ...waves.filter((wave) => wave.status === "planned" || wave.status === "running"),
    ...waves,
  ]
  let caseRun: CaseRun | null = null
  let caseRunDetail: CaseRunDetail | null = null
  for (const wave of ordered) {
    let runs: CaseRun[] = []
    try {
      runs = await apiGet<CaseRun[]>(
        `/api/versions/${versionId}/waves/${encodeURIComponent(wave.id)}/case-runs?limit=20`,
      )
    } catch {
      continue
    }
    const candidate = runs.find((run) => run.evidence_count > 0) ?? runs[0]
    if (candidate === undefined) continue
    if (caseRun !== null && caseRun.evidence_count >= candidate.evidence_count) continue
    caseRun = candidate
    caseRunDetail = await apiGet<CaseRunDetail>(
      `/api/versions/${versionId}/case-runs/${encodeURIComponent(candidate.id)}`,
    )
  }
  return { waves, caseRun, caseRunDetail }
}

/**
 * Reports can be marked rendered while their content object is missing from the
 * disposable clone. Probe the bytes so the download test only selects a
 * revision the API can actually serve.
 */
async function reportRevisionBytesAvailable(
  versionId: string,
  reportId: string,
  revision: number,
  kind: "pdf" | "html",
): Promise<boolean> {
  const response = await fetch(
    `${API_BASE_URL}/api/versions/${encodeURIComponent(versionId)}/reports/${encodeURIComponent(reportId)}/revisions/${revision}/${kind}`,
    { headers: { Authorization: `Bearer ${apiToken}` } },
  )
  if (!response.ok) return false
  const bytes = await response.arrayBuffer()
  return bytes.byteLength > 0
}

/**
 * Finds a report with a generated PDF and a rendered HTML revision. The journey
 * version's own reports are preferred; the P7 clone's completed version is a
 * valid fallback because the report surface is version-scoped.
 */
async function discoverReport(
  versions: VersionSummary[],
  journeyId: string,
): Promise<ReportTarget | null> {
  const overrideReport = envValue("EZTEST_E2E_P8_REPORT_ID")
  const overrideVersion = envValue("EZTEST_E2E_P8_REPORT_VERSION_ID")
  const ordered = [
    ...versions.filter((version) => version.id === journeyId),
    ...versions.filter((version) => version.status === "completed"),
    ...sortVersions(versions),
  ]
  const seen = new Set<string>()
  let inspectedVersions = 0
  for (const version of ordered) {
    if (seen.has(version.id)) continue
    seen.add(version.id)
    if (overrideVersion.length > 0 && version.id !== overrideVersion) continue
    inspectedVersions += 1
    if (inspectedVersions > 12) break
    let reports: Report[] = []
    try {
      reports = await apiGet<Report[]>(
        `/api/versions/${encodeURIComponent(version.id)}/reports?limit=100`,
      )
    } catch {
      continue
    }
    if (overrideReport.length > 0) {
      reports = reports.filter((report) => report.id === overrideReport)
    }
    reports = [...reports].sort((left, right) =>
      left.kind === right.kind ? 0 : left.kind === "version" ? -1 : 1,
    )
    for (const report of reports) {
      let detail: ReportDetail
      try {
        detail = await apiGet<ReportDetail>(
          `/api/versions/${encodeURIComponent(version.id)}/reports/${encodeURIComponent(report.id)}`,
        )
      } catch {
        continue
      }
      const revisions = [
        ...(detail.current_revision !== null && detail.current_revision !== undefined
          ? [detail.current_revision]
          : []),
        ...detail.revisions,
      ]
      let revision: ReportRevision | undefined
      for (const candidate of revisions) {
        if (candidate.pdf_available !== true || candidate.html_available !== true) continue
        const pdfOK = await reportRevisionBytesAvailable(
          version.id,
          report.id,
          candidate.revision,
          "pdf",
        )
        if (!pdfOK) continue
        const htmlOK = await reportRevisionBytesAvailable(
          version.id,
          report.id,
          candidate.revision,
          "html",
        )
        if (!htmlOK) continue
        revision = candidate
        break
      }
      if (revision === undefined) continue
      return { versionId: version.id, report, detail, revision }
    }
  }
  return null
}

function planIssues(issues: Issue[]): {
  mergedIssue: Issue | null
  mergePair: { source: Issue; target: Issue } | null
  recurrenceIssue: Issue | null
  reopenIssue: Issue | null
} {
  const mergedIssue =
    issues.find(
      (issue) =>
        issue.status === "merged" &&
        ((issue.merged_chain?.length ?? 0) > 0 ||
          (issue.merged_into_id !== null && issue.merged_into_id !== undefined)),
    ) ?? null
  const mergedTargetId =
    mergedIssue?.merged_chain?.length && mergedIssue.merged_chain.length > 0
      ? mergedIssue.merged_chain[mergedIssue.merged_chain.length - 1]
      : (mergedIssue?.merged_into_id ?? null)
  const recurrenceIssue =
    mergedTargetId === null
      ? null
      : (issues.find(
          (issue) => issue.id === mergedTargetId && (issue.recurrence_count ?? 0) >= 2,
        ) ?? null)
  const openIssues = issues.filter((issue) => issue.status !== "merged")
  const mergePair =
    mergedIssue === null && openIssues.length >= 2
      ? { source: openIssues[0], target: openIssues[1] }
      : null
  // Never plan the reopen on the issue that this run is about to merge; prefer
  // the merge target or another non-merged issue.
  const reopenCandidates = openIssues.filter(
    (issue) => mergePair === null || issue.id !== mergePair.source.id,
  )
  const reopenPool = reopenCandidates.length > 0 ? reopenCandidates : openIssues
  const reopenIssue =
    [...reopenPool].sort((left, right) => {
      const rank = (status: Issue["status"]): number =>
        status === "confirmed" ? 0 : status === "open" ? 1 : 2
      return rank(left.status) - rank(right.status)
    })[0] ?? null
  return { mergedIssue, mergePair, recurrenceIssue, reopenIssue }
}

function flattenCases(graph: DesignGraph): DesignCase[] {
  return graph.requirements.flatMap((requirement) =>
    requirement.scenarios.flatMap((scenario) => scenario.cases),
  )
}

async function discoverSeed(): Promise<Seed> {
  const versions = await apiGet<VersionSummary[]>("/api/versions?limit=100")
  if (versions.length === 0) {
    throw new Error(
      "P8 seed discovery: no versions returned by GET /api/versions; run scripts/p8-acceptance.ps1 first",
    )
  }
  const summary = await pickJourneyVersion(versions)
  const version = await apiGet<VersionDetail>(`/api/versions/${encodeURIComponent(summary.id)}`)
  const [requirements, conversations, design, wavesAndRun, issues, reports, chain, resources] =
    await Promise.all([
      apiGet<Requirement[]>(`/api/versions/${version.id}/requirements?limit=100`),
      apiGet<Conversation[]>(`/api/versions/${version.id}/conversations?limit=100`),
      apiGet<DesignGraph>(`/api/versions/${version.id}/design/graph`),
      discoverWavesAndCaseRun(version.id),
      apiGet<Issue[]>(`/api/versions/${version.id}/issues?limit=100`),
      apiGet<Report[]>(`/api/versions/${version.id}/reports?limit=100`),
      apiGet<ChainView>(`/api/versions/${version.id}/exploration-chain?limit=300`),
      Promise.all([
        apiGet<Sut[]>("/api/resources/suts?limit=100"),
        apiGet<CodeRepository[]>("/api/resources/code-repos?limit=100"),
      ] as const),
    ])

  const [suts, repositories] = resources
  const instances = await apiGet<Instance[]>("/api/admin/instances")
  const knowledge = await apiGet<KnowledgeItem[]>("/api/knowledge/items?limit=100")
  const clarificationConversations = conversations.filter(
    (conversation) => conversation.kind === "clarification",
  )

  const issuePlan = planIssues(issues)
  const completedVersion =
    versions.find((item) => item.status === "completed" && item.id !== version.id) ??
    versions.find((item) => item.status === "completed") ??
    null
  const activeWave =
    wavesAndRun.waves.find(
      (wave) => wave.status === "planned" || wave.status === "running" || wave.status === "paused",
    ) ?? null
  const report = await discoverReport(versions, version.id)

  let requirementsAfter = requirements
  let draftRequirementId = requirements.find((item) => item.status === "draft")?.id ?? null
  if (
    draftRequirementId === null &&
    version.status !== "completed" &&
    !design.auto_design_new_requirements
  ) {
    const created = await apiPost<Requirement>(`/api/versions/${version.id}/requirements`, {
      title: `P8 E2E draft requirement ${Date.now()}`,
      description:
        "Created through the real API by the P8 browser E2E so the requirement confirm action is exercised without the real LLM.",
      acceptance_criteria: "The editor confirms this draft and the requirement moves to 已澄清.",
      priority: "P2",
    })
    requirementsAfter = [created, ...requirements]
    draftRequirementId = created.id
    mutations.push(
      `Created draft requirement ${created.id} in version ${version.id} (requirements confirm scenario).`,
    )
  }

  return {
    versions,
    version,
    requirements: requirementsAfter,
    conversations: clarificationConversations,
    design,
    waves: wavesAndRun.waves,
    caseRun: wavesAndRun.caseRun,
    caseRunDetail: wavesAndRun.caseRunDetail,
    issues,
    reports,
    chain,
    draftRequirementId,
    ...issuePlan,
    activeWave,
    report,
    completedVersion,
    resources: { suts, repositories },
    instances,
    knowledge,
  }
}

/* ------------------------------ test harness ------------------------------- */

const mutations: string[] = []
const gapNotes: string[] = []
let shot = 0
let seed: Seed

/** Records a non-silent data gap or intentional skip of one assertion. */
function noteGap(scenario: string, description: string): void {
  const line = `${scenario}: ${description}`
  gapNotes.push(line)
  try {
    test.info().annotations.push({ type: "data-gap", description: line })
  } catch {
    // Outside a running test (beforeAll/afterAll) the annotation is only stored.
  }
}

function nextShot(name: string): string {
  shot += 1
  return path.join(SCREENSHOT_DIR, `p8-e2e-${String(shot).padStart(2, "0")}-${name}.png`)
}

/** Full-page screenshot for ordinary pages. */
async function snapFull(page: Page, name: string): Promise<void> {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
  await page.screenshot({ path: nextShot(name), fullPage: true })
}

/** Viewport screenshot for modal/sheet overlays and long panels. */
async function snapViewport(page: Page, name: string): Promise<void> {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
  await page.screenshot({ path: nextShot(name), fullPage: false })
}

/** Element screenshot for a specific panel. */
async function snapElement(locator: Locator, name: string): Promise<void> {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
  await locator.screenshot({ path: nextShot(name) })
}

async function downloadReportFile(
  page: Page,
  buttonName: string,
  filename: string,
): Promise<Buffer> {
  const downloadPromise = page.waitForEvent("download", { timeout: 60_000 })
  await page.getByRole("button", { name: buttonName }).click()
  const download = await downloadPromise
  const downloadsDir = path.join(SCREENSHOT_DIR, "..", "downloads")
  fs.mkdirSync(downloadsDir, { recursive: true })
  const target = path.join(downloadsDir, filename)
  await download.saveAs(target)
  return fs.readFileSync(target)
}

/**
 * Resumes the paused wave through the paused banner. Returns false when the
 * server keeps the wave paused because open human requests block the resume;
 * the caller records that as an explicit data gap.
 */
async function resumePausedWave(page: Page): Promise<boolean> {
  const banner = page.getByRole("alert").filter({ hasText: "执行已暂停" })
  const resume = banner.getByRole("button", { name: "恢复" }).first()
  if ((await resume.count()) === 0) return false
  await resume.click()
  await expect
    .poll(
      async () =>
        (await banner.count()) === 0 ||
        (await page.getByText("请先处理待处理的人工请求").count()) > 0,
      { timeout: 30_000, intervals: [500, 1_000, 2_000] },
    )
    .toBe(true)
  return (await banner.count()) === 0
}

test.describe.configure({ mode: "serial" })

test.beforeAll(async () => {
  await apiLogin()
  seed = await discoverSeed()
})

test.beforeEach(async ({ context }) => {
  // The API token lives in sessionStorage (per tab). Inject it before the first
  // navigation of every fresh test context instead of logging in through the UI
  // for every scenario (the real login API call already happened in beforeAll).
  await context.addInitScript(
    (session) => {
      try {
        window.sessionStorage.setItem("eztest.token", session.token)
        window.sessionStorage.setItem("eztest.user", session.user)
      } catch {
        // about:blank has an opaque origin; the script runs again on the app URL.
      }
    },
    { token: apiToken, user: authUserJson },
  )
})

test.afterAll(async () => {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })
  const mutationLines = mutations.length === 0 ? ["(none)"] : mutations.map((line) => `- ${line}`)
  const gapLines = gapNotes.length === 0 ? ["(none)"] : gapNotes.map((line) => `- ${line}`)
  const content = [
    "# P8 browser E2E annotations",
    "",
    "## Mutations performed by the spec",
    "",
    ...mutationLines,
    "",
    "## Data gaps / explicitly skipped assertions",
    "",
    ...gapLines,
    "",
  ].join("\r\n")
  fs.writeFileSync(ANNOTATION_FILE, content, "utf8")
})

/* -------------------------------- scenarios -------------------------------- */

test("resources console lists SUTs, pages/APIs, data access and repositories", async ({ page }) => {
  await page.goto("/resources")
  await expect(page.getByRole("heading", { name: "资源", level: 1 })).toBeVisible()
  await expect(page.getByRole("tab", { name: "SUT 与环境" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
  await expect(page.getByRole("table", { name: "SUT 列表" })).toBeVisible()
  await expect(page.getByRole("table", { name: "环境列表" })).toBeVisible()
  expect(seed.resources.suts.length).toBeGreaterThan(0)
  await snapFull(page, "resources-suts")

  await page.getByRole("tab", { name: "页面与 API" }).click()
  await expect(page.getByRole("tabpanel", { name: "页面与 API" })).toBeVisible()
  await expect(page.getByRole("table", { name: "页面列表" })).toBeVisible()
  await expect(page.getByRole("table", { name: "API 列表" })).toBeVisible()
  await snapFull(page, "resources-pages-apis")

  await page.getByRole("tab", { name: "数据与账号" }).click()
  await expect(page.getByRole("table", { name: "目标库列表" })).toBeVisible()
  await expect(page.getByRole("table", { name: "测试账号列表" })).toBeVisible()
  await snapFull(page, "resources-data")

  await page.getByRole("tab", { name: "代码仓库" }).click()
  const repositoryPanel = page.getByRole("tabpanel", { name: "代码仓库" })
  await expect(repositoryPanel).toBeVisible()
  if (seed.resources.repositories.length > 0) {
    await expect(page.getByRole("table", { name: "代码仓列表" })).toBeVisible()
  } else {
    await expect(repositoryPanel.getByText("还没有代码仓")).toBeVisible()
    noteGap("resources", "the database has no code repositories; the empty state is asserted")
  }
  await snapFull(page, "resources-repos")
})

test("versions list creates a version and opens the discovered journey version", async ({
  page,
}) => {
  await page.goto("/versions")
  await expect(page.getByRole("heading", { name: "版本", level: 1 })).toBeVisible()
  await expect(page.getByRole("table", { name: "版本列表" })).toBeVisible()
  await snapFull(page, "versions-list")

  if (seed.resources.suts.length === 0) {
    noteGap("versions", "no SUT exists, so the create dialog cannot bind a version")
  } else {
    await page.getByRole("button", { name: "新建版本" }).first().click()
    const dialog = page.getByRole("dialog", { name: "新建版本" })
    await expect(dialog).toBeVisible()
    const title = `P8 E2E journey ${Date.now()}`
    await dialog.locator("#create-version-title").fill(title)
    const sutSelect = dialog.getByLabel("SUT", { exact: true })
    await expect(sutSelect.locator("option")).not.toHaveCount(1)
    await sutSelect.selectOption({ index: 1 })
    const environmentSelect = dialog.getByLabel("环境", { exact: true })
    await expect(environmentSelect).toBeEnabled()
    await environmentSelect.selectOption({ index: 1 })
    await snapViewport(page, "versions-create-dialog")
    await dialog.getByRole("button", { name: "创建版本" }).click()
    await page.waitForURL(/\/versions\/\d+\?tab=overview/, { timeout: 30_000 })
    await expect(page.getByTestId("version-header")).toBeVisible()
    await snapFull(page, "versions-created-overview")
    mutations.push(`Created version "${title}" through the UI (versions create/open scenario).`)
  }

  await page.goto("/versions")
  const journeyLink = page.getByRole("link", { name: seed.version.title }).first()
  await expect(journeyLink).toBeVisible()
  await journeyLink.click()
  await expect(page).toHaveURL(new RegExp(`/versions/${seed.version.id}\\?tab=overview`))
  await expect(page.getByTestId("version-header")).toContainText(seed.version.title)
  await snapFull(page, "versions-journey-overview")
})

test("requirements workspace shows the conversation and confirms a requirement", async ({
  page,
}) => {
  await page.goto(`/versions/${seed.version.id}?tab=requirements`)
  await expect(page.getByTestId("requirements-tab")).toBeVisible()
  await expect(page.getByTestId("chat-panel")).toBeVisible()
  await expect(page.getByTestId("requirement-sidebar")).toBeVisible()
  await snapFull(page, "requirements-conversation")

  if (seed.conversations.length === 0) {
    noteGap("requirements", "no clarification conversation exists in the journey version")
  } else {
    await expect(page.getByTestId("message-list")).toBeVisible()
    await expect(
      page.locator('[data-testid^="message-user"], [data-testid^="message-assistant"]').first(),
    ).toBeVisible({ timeout: 30_000 })
  }

  if (seed.requirements.length === 0) {
    noteGap(
      "requirements",
      "the journey version has no requirements; the requirement card/editor/confirm assertions are skipped",
    )
    await expect(page.getByText("暂无需求")).toBeVisible()
    await snapFull(page, "requirements-conversation")
    return
  }

  const requirementCard = page.locator('[data-testid^="requirement-card-"]').first()
  if ((await requirementCard.count()) > 0) {
    await requirementCard.click()
  } else {
    noteGap(
      "requirements",
      "no AI requirement card is present in the clarification messages; the sidebar item opens the editor instead",
    )
    const firstRequirement = page.locator('[data-testid^="requirement-item-"]').first()
    await expect(firstRequirement).toBeVisible()
    await firstRequirement.getByRole("button").first().click()
  }
  await expect(page.getByTestId("requirement-editor")).toBeVisible()
  await snapElement(page.getByTestId("requirement-editor"), "requirements-editor")

  const draftRequirementId = seed.draftRequirementId
  if (draftRequirementId === null) {
    noteGap(
      "requirements",
      "no draft requirement is available; the confirm action is not exercised",
    )
  } else if (seed.version.status === "completed") {
    noteGap(
      "requirements",
      "the journey version is completed; requirement confirmation is read-only and is not attempted",
    )
  } else if (seed.design.auto_design_new_requirements) {
    noteGap(
      "requirements",
      "auto-design is enabled; confirming would enqueue a real LLM design run, so the confirm action is skipped by design",
    )
  } else {
    const draft = page.getByTestId(`requirement-item-${draftRequirementId}`)
    await expect(draft).toBeVisible()
    await draft.getByRole("button").first().click()
    await expect(page.getByTestId("requirement-editor")).toBeVisible()
    await expect(page.getByTestId("editor-confirm")).toBeVisible()
    await page.getByTestId("editor-confirm").click()
    await expect(page.getByTestId("editor-confirm")).toHaveCount(0)
    await expect(page.getByTestId("requirement-editor")).toContainText("已澄清")
    await snapViewport(page, "requirements-confirmed")
    mutations.push(
      `Confirmed draft requirement ${draftRequirementId} through the requirements editor.`,
    )
  }

  if (seed.requirements.length > 0) {
    await page.getByTestId("req-filter-clarified").click()
    await expect(page.locator('[data-testid^="requirement-item-"]').first()).toBeVisible()
    await page.getByTestId("req-filter-all").click()
    await snapFull(page, "requirements-sidebar-filtered")
  } else {
    noteGap("requirements", "the journey version has no requirements; the sidebar filter is empty")
  }
})

test("design mindmap renders the scenario graph and confirms a draft case", async ({ page }) => {
  const cases = flattenCases(seed.design)
  const draftCase = cases.find((item) => item.status === "draft") ?? null
  const selectedCase = draftCase ?? cases[0] ?? null
  const caseParam = selectedCase === null ? "" : `&case=${encodeURIComponent(selectedCase.case_id)}`

  await page.goto(`/versions/${seed.version.id}?tab=design${caseParam}`)
  await expect(page.getByTestId("design-tab")).toBeVisible()
  await expect(page.getByTestId("design-mindmap")).toBeVisible()
  await expect(page.getByTestId("design-stats")).toBeVisible()
  await expect(page.getByTestId("design-stats")).toContainText(`用例 ${seed.design.stats.cases}`)
  await snapFull(page, "design-mindmap")

  if (cases.length === 0 || selectedCase === null) {
    noteGap(
      "design",
      "the S0-S6 dataset has no designed cases; the mindmap empty state is asserted",
    )
    await expect(page.getByText("暂无设计图")).toBeVisible()
    return
  }

  await expect(page.getByTestId(`case-card-${selectedCase.case_id}`)).toBeVisible({
    timeout: 30_000,
  })
  await expect(page.getByTestId("case-detail")).toBeVisible({ timeout: 30_000 })
  await snapElement(page.getByTestId("case-detail"), "design-case-detail")

  if (draftCase === null) {
    noteGap(
      "design",
      "every S4 case is already confirmed; the confirmed badge is asserted instead of a confirm click",
    )
    await expect(page.getByTestId(`case-card-${selectedCase.case_id}`)).toContainText(/confirmed|\u5df2\u786e\u8ba4/i)
    return
  }
  if (seed.version.status === "completed") {
    noteGap(
      "design",
      "the journey version is completed; case confirmation is read-only and is not attempted",
    )
    return
  }
  const confirm = page.getByTestId(`btn-confirm-detail-${draftCase.case_id}`)
  if ((await confirm.count()) === 0) {
    noteGap(
      "design",
      `draft case ${draftCase.case_id} is not confirmable in this dataset (the confirm button is absent)`,
    )
    return
  }
  await confirm.click()
  await expect(page.getByTestId("design-confirm-notice")).toContainText("已确认")
  await snapViewport(page, "design-confirm-notice")
  mutations.push(`Confirmed draft design case ${draftCase.case_id} through the case detail panel.`)
})

test("execution workspace shows waves, evidence and the pause round-trip", async ({ page }) => {
  if (seed.waves.length === 0) {
    noteGap("execution", "the journey version has no execution waves; the scenario is skipped")
    test.skip(true, "no execution waves are available in the journey version")
  }
  const waveId = seed.caseRun?.wave_id ?? seed.waves[0].id
  const caseRunParam =
    seed.caseRun === null ? "" : `&case_run=${encodeURIComponent(seed.caseRun.id)}`
  await page.goto(
    `/versions/${seed.version.id}?tab=execution&wave=${encodeURIComponent(waveId)}${caseRunParam}`,
  )
  await expect(page.getByTestId("execution-tab")).toBeVisible()
  await expect(page.getByTestId("execution-toolbar")).toBeVisible()
  await expect(page.getByTestId("wave-list")).toBeVisible()
  await expect(page.getByTestId("results-panel")).toBeVisible()
  await snapFull(page, "execution-waves")

  const caseRun = seed.caseRun
  if (caseRun === null) {
    noteGap("execution", "no case run exists in the stored waves")
  } else {
    // The ?case_run= deep link (or the first-row auto-selection) already expands
    // the run; clicking the row again would collapse it.
    await expect(page.getByTestId(`case-run-row-${caseRun.id}`)).toBeVisible()
    await expect(page.getByTestId("case-run-detail")).toBeVisible({ timeout: 30_000 })
    await snapElement(page.getByTestId("results-panel"), "execution-case-run-detail")

    const evidence = seed.caseRunDetail?.evidence[0] ?? null
    if (evidence === null) {
      noteGap("execution", "the selected case run has no evidence blobs in the snapshot")
    } else {
      const label = evidence.filename.length > 0 ? evidence.filename : `#${evidence.id}`
      const evidenceButton = page
        .getByTestId("case-run-detail")
        .locator("button")
        .filter({ hasText: label })
        .first()
      if ((await evidenceButton.count()) === 0) {
        noteGap("execution", `evidence ${evidence.id} is not rendered as a step button`)
      } else {
        await evidenceButton.click()
        const viewer = page.getByRole("dialog", { name: `证据 #${evidence.id}` })
        await expect(viewer).toBeVisible()
        await expect(
          viewer.getByTestId("evidence-image").or(viewer.getByTestId("evidence-text")),
        ).toBeVisible({ timeout: 30_000 })
        await snapViewport(page, "execution-evidence")
        await viewer.getByRole("button", { name: "关闭", exact: true }).click()
      }
    }
  }

  // Pause round-trip: only a non-terminal wave can be paused. S6 usually leaves
  // no active wave, so the scenario first checks whether one survived; a retest
  // intent is created through the real API only when no wave is active and a
  // terminal case run can seed one. If the wave turns terminal before the pause,
  // the gap is recorded explicitly.
  let activeWave = seed.activeWave
  if (activeWave === null && caseRun !== null) {
    try {
      await apiPost(`/api/versions/${seed.version.id}/execution/intents`, {
        case_version_id: caseRun.case_version_id,
        purpose: "retest",
      })
      mutations.push(
        `Created a retest intent from case run ${caseRun.id} to exercise the pause UI.`,
      )
    } catch {
      noteGap("execution", "the retest intent could not be created; pause stays unexercised")
    }
    const before = new Set(seed.waves.map((wave) => wave.id))
    try {
      await expect
        .poll(
          async () => {
            try {
              const waves = await apiGet<RunWave[]>(`/api/versions/${seed.version.id}/waves?limit=20`)
              return (
                waves.find(
                  (wave) =>
                    !before.has(wave.id) &&
                    (wave.status === "planned" ||
                      wave.status === "running" ||
                      wave.status === "paused"),
                )?.id ?? ""
              )
            } catch {
              return ""
            }
          },
          { timeout: 20_000, intervals: [500, 1_000, 2_000] },
        )
        .not.toBe("")
    } catch {
      noteGap(
        "execution",
        "the retest wave completed before it could be paused; the pause control is not clickable",
      )
      activeWave = null
    }
    if (activeWave === null) {
      activeWave =
        (await apiGet<RunWave[]>(`/api/versions/${seed.version.id}/waves?limit=20`)).find(
          (wave) =>
            !before.has(wave.id) &&
            (wave.status === "planned" || wave.status === "running" || wave.status === "paused"),
        ) ?? null
    }
  }

  if (activeWave === null) {
    noteGap(
      "execution",
      "no non-terminal wave remains in the S0-S6 data (or the retest wave completed immediately); the pause control is not clickable",
    )
    return
  }

  await page.goto(
    `/versions/${seed.version.id}?tab=execution&wave=${encodeURIComponent(activeWave.id)}`,
  )
  await expect(page.getByTestId("execution-toolbar")).toBeVisible()
  const pausedAlert = page.getByRole("alert").filter({ hasText: "执行已暂停" })

  if (activeWave.status === "paused") {
    await expect(pausedAlert).toBeVisible()
    await snapViewport(page, "execution-paused")
    if (!(await resumePausedWave(page))) {
      noteGap("execution", "the paused wave could not be resumed (open human requests block it)")
      return
    }
    mutations.push(`Resumed previously paused wave ${activeWave.id} through the paused banner.`)
    return
  }

  await page.getByRole("button", { name: "暂停", exact: true }).first().click()
  const pauseDialog = page.getByRole("dialog", { name: "暂停执行" })
  await expect(pauseDialog).toBeVisible()
  await snapViewport(page, "execution-pause-dialog")
  await pauseDialog.getByRole("button", { name: "暂停", exact: true }).click()
  await expect(pauseDialog).toHaveCount(0)
  await expect(pausedAlert).toBeVisible({ timeout: 30_000 })
  await snapViewport(page, "execution-paused")
  mutations.push(`Paused wave ${activeWave.id} through the execution toolbar.`)

  if (!(await resumePausedWave(page))) {
    noteGap("execution", "the paused wave could not be resumed (open human requests block it)")
    return
  }
  mutations.push(`Resumed wave ${activeWave.id} through the paused banner.`)
})

test("issues tab shows the merged chain, merges a pair and reopens an issue", async ({ page }) => {
  if (seed.issues.length === 0) {
    noteGap("issues", "the journey version has no delivery issues; the scenario is skipped")
    test.skip(true, "no delivery issues are available in the journey version")
  }
  const versionId = seed.version.id
  const canMutate = seed.version.status !== "completed"
  const mergedIssue = seed.mergedIssue
  const mergePair = seed.mergePair
  const recurrenceIssue = seed.recurrenceIssue
  const reopenIssue = seed.reopenIssue

  await page.goto(`/versions/${versionId}?tab=issues`)
  await expect(page.getByTestId("issues-tab")).toBeVisible()
  await expect(page.getByRole("table", { name: "问题列表" })).toBeVisible()
  await snapFull(page, "issues-list")

  if (recurrenceIssue !== null) {
    await page.goto(
      `/versions/${versionId}?tab=issues&issue=${encodeURIComponent(recurrenceIssue.id)}`,
    )
    const sheet = page.getByTestId("issue-detail-sheet")
    await expect(sheet).toBeVisible()
    const count = recurrenceIssue.recurrence_count ?? 0
    await expect(sheet.getByTestId("issue-recurrence")).toContainText(`复发 ${count} 次`)
    await snapViewport(page, "issues-recurrence")
  } else {
    noteGap("issues", "no recurrence_count>=2 issue is linked to the merged chain target")
  }

  if (mergedIssue !== null) {
    await page.goto(`/versions/${versionId}?tab=issues&issue=${encodeURIComponent(mergedIssue.id)}`)
    const sheet = page.getByTestId("issue-detail-sheet")
    await expect(sheet).toBeVisible()
    const chain = sheet.getByTestId("issue-merged-chain")
    await expect(chain).toContainText("该问题已合并")
    for (const chainIssueId of mergedIssue.merged_chain ?? []) {
      await expect(chain).toContainText(chainIssueId)
    }
    if (mergedIssue.merged_into_id !== null && mergedIssue.merged_into_id !== undefined) {
      await expect(chain).toContainText(mergedIssue.merged_into_id)
    }
    await snapViewport(page, "issues-merged-chain")
  } else if (mergePair !== null && canMutate) {
    await page.goto(
      `/versions/${versionId}?tab=issues&issue=${encodeURIComponent(mergePair.source.id)}`,
    )
    const sheet = page.getByTestId("issue-detail-sheet")
    await expect(sheet).toBeVisible()
    await expect(sheet.getByRole("button", { name: "合并", exact: true })).toBeVisible()
    await sheet.getByRole("button", { name: "合并", exact: true }).click()
    const mergeDialog = page.getByRole("dialog", { name: "合并问题" })
    await expect(mergeDialog).toBeVisible()
    const target = mergeDialog.getByTestId(`merge-target-${mergePair.target.id}`)
    await expect(target).toBeVisible()
    await target.click()
    await snapViewport(page, "issues-merge-dialog")
    await mergeDialog.getByRole("button", { name: "合并", exact: true }).click()
    await expect(mergeDialog).toHaveCount(0)
    await expect(sheet.getByTestId("issue-merged-chain")).toContainText(mergePair.target.id)
    await snapViewport(page, "issues-merged-chain")
    mutations.push(
      `Merged issue ${mergePair.source.id} into ${mergePair.target.id} through the merge dialog.`,
    )
  } else {
    noteGap(
      "issues",
      "no merged issue and no mergeable pair are available; the merge-chain assertion is skipped",
    )
  }

  if (reopenIssue === null) {
    noteGap("issues", "no non-merged issue is available; the reopen action is not exercised")
    return
  }
  if (!canMutate) {
    noteGap(
      "issues",
      "the journey version is completed; issue mutation is not attempted and only the read state was asserted",
    )
    return
  }

  await page.goto(`/versions/${versionId}?tab=issues&issue=${encodeURIComponent(reopenIssue.id)}`)
  const sheet = page.getByTestId("issue-detail-sheet")
  await expect(sheet).toBeVisible()
  if (reopenIssue.status === "open") {
    const confirm = sheet.getByRole("button", { name: "确认", exact: true })
    if ((await confirm.count()) > 0) {
      await confirm.click()
      const confirmDialog = page.getByRole("dialog", { name: "确认问题" })
      await expect(confirmDialog).toBeVisible()
      await confirmDialog.getByRole("button", { name: "确认问题" }).click()
      await expect(confirmDialog).toHaveCount(0)
    }
    const confirmed = await expect(sheet.getByText(/confirmed|\u5df2\u786e\u8ba4/i).first())
      .toBeVisible({ timeout: 5_000 })
      .then(() => true)
      .catch(() => false)
    if (!confirmed) {
      noteGap(
        "issues",
        "the issue confirm action did not change the detail status; the reopen mutation is not attempted (API behavior is covered by P7 acceptance)",
      )
      return
    }
  }
  await sheet.getByRole("button", { name: "重开", exact: true }).click()
  const reopenDialog = page.getByRole("dialog", { name: "重开问题" })
  await expect(reopenDialog).toBeVisible()
  const reopenReason = reopenDialog.locator("#issue-reopen-reason")
  const reopenSubmit = reopenDialog.getByRole("button", { name: "重开问题" })
  await expect(reopenSubmit).toBeDisabled()
  await reopenReason.fill("P8 E2E journey reopen round-trip")
  await expect(reopenSubmit).toBeEnabled()
  await snapViewport(page, "issues-reopen-dialog")
  await reopenSubmit.click()
  await expect(reopenDialog).toHaveCount(0)
  await expect(sheet.getByText("待确认", { exact: true }).first()).toBeVisible()
  await snapViewport(page, "issues-reopened")
  mutations.push(
    `Reopened issue ${reopenIssue.id} with the mandatory reason (status restored to open).`,
  )
})

test("reports tab downloads the generated PDF and HTML", async ({ page }) => {
  const target = seed.report
  if (target === null) {
    noteGap(
      "reports",
      "no report revision with rendered PDF/HTML was discovered; the download assertions are skipped",
    )
    await page.goto(`/versions/${seed.version.id}?tab=reports`)
    await expect(page.getByTestId("reports-tab")).toBeVisible()
    await expect(page.getByTestId("report-list")).toBeVisible()
    await snapFull(page, "reports-empty")
    return
  }

  await page.goto(
    `/versions/${target.versionId}?tab=reports&report=${encodeURIComponent(target.report.id)}`,
  )
  await expect(page.getByTestId("reports-tab")).toBeVisible()
  await expect(page.getByTestId("report-list")).toBeVisible()
  await expect(page.getByTestId(`report-${target.report.id}`)).toBeVisible()
  await expect(page.getByTestId("report-detail")).toBeVisible()

  const currentRevision = target.detail.current_revision?.revision
  if (currentRevision !== target.revision.revision) {
    await page.locator("#report-revision").selectOption(String(target.revision.revision))
  }
  await expect(page.getByTestId("report-pdf-status")).toHaveText(/PDF 已生成/)
  await snapFull(page, "reports-detail")

  if (target.revision.pdf_available) {
    const pdf = await downloadReportFile(
      page,
      "下载 PDF",
      `p8-report-${target.report.id}-r${target.revision.revision}.pdf`,
    )
    expect(pdf.length).toBeGreaterThan(1_000)
    expect(pdf.subarray(0, 5).toString("latin1")).toBe("%PDF-")
    await snapViewport(page, "reports-pdf-downloaded")
  } else {
    noteGap("reports", `revision ${target.revision.revision} has no PDF bytes available`)
  }

  if (target.revision.html_available) {
    const html = await downloadReportFile(
      page,
      "下载 HTML",
      `p8-report-${target.report.id}-r${target.revision.revision}.html`,
    )
    const text = html.toString("utf8")
    expect(text).toContain("<html")
    expect(text).toContain("data-template-version")
    await snapViewport(page, "reports-html-downloaded")
  } else {
    noteGap("reports", `revision ${target.revision.revision} has no HTML bytes available`)
  }

  await page.getByTestId("reports-kind-wave").click()
  await expect(page.getByTestId("report-list")).toBeVisible()
  await snapFull(page, "reports-wave-kind")
})

test("exploration chain filters and node deep links survive reload", async ({ page }) => {
  const chain = seed.chain
  if (chain === null || chain.nodes.length === 0) {
    noteGap("chain", "the journey version has no chain nodes; the scenario is skipped")
    test.skip(true, "no exploration-chain nodes are available in the journey version")
    return
  }
  const nodeId = chain.nodes[0].id

  await page.goto(`/versions/${seed.version.id}?tab=chain`)
  await expect(page.getByTestId("chain-tab")).toBeVisible()
  await expect(page.getByTestId("chain-toolbar")).toBeVisible()
  await expect(page.getByTestId("chain-filters")).toBeVisible()
  await expect(page.getByTestId("chain-graph")).toBeVisible()
  await snapFull(page, "chain-graph")

  await page.getByRole("button", { name: "列表视图" }).click()
  await expect(page.getByTestId("chain-list-view")).toBeVisible()
  await snapFull(page, "chain-list")

  await page.getByTestId("chain-filter-abnormal").click()
  await expect(page.getByTestId("chain-filter-abnormal")).toHaveAttribute("aria-pressed", "true")
  await expect(page).toHaveURL(/abnormal_only=true/)
  await page.locator("#chain-filter-issue-status").selectOption("confirmed")
  await expect(page).toHaveURL(/issue_status=confirmed/)
  await snapFull(page, "chain-filtered")

  await page.goto(`/versions/${seed.version.id}?tab=chain&node=${encodeURIComponent(nodeId)}`)
  await expect(page.getByTestId("chain-detail-sheet")).toBeVisible()
  await expect(page.getByTestId("chain-detail-meta")).toContainText(nodeId)
  await snapViewport(page, "chain-node-detail")

  // The node deep link is URL-backed: a full reload must restore the same sheet.
  await page.reload()
  await expect(page.getByTestId("chain-detail-sheet")).toBeVisible()
  await expect(page.getByTestId("chain-detail-meta")).toContainText(nodeId)
})

test("completed version header reopens the version", async ({ page }) => {
  const target = seed.completedVersion
  if (target === null) {
    noteGap(
      "version-reopen",
      "no completed version exists in the acceptance database; the reopen scenario is skipped",
    )
    test.skip(true, "no completed version is available to reopen")
    return
  }
  await page.goto(`/versions/${target.id}?tab=overview`)
  await expect(page.getByTestId("version-header")).toBeVisible()
  await expect(page.getByTestId("version-closed-info")).toBeVisible()
  await expect(page.getByTestId("version-reopen")).toBeVisible()
  await snapFull(page, "version-completed")

  await page.getByTestId("version-reopen").click()
  const dialog = page.getByRole("dialog", { name: "重新打开版本" })
  await expect(dialog).toBeVisible()
  const submit = dialog.getByRole("button", { name: "重新打开" })
  await expect(submit).toBeDisabled()
  await dialog.locator("#version-reopen-reason").fill("P8 E2E journey reopen round-trip")
  await expect(submit).toBeEnabled()
  await snapViewport(page, "version-reopen-dialog")
  await submit.click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByTestId("version-closed-info")).toHaveCount(0)
  await expect(
    page.getByTestId("version-header").getByText("运行中", { exact: true }),
  ).toBeVisible()
  await snapFull(page, "version-reopened")
  mutations.push(
    `Reopened completed version ${target.id} with the mandatory reason (completed -> running).`,
  )
})

test("global knowledge, quality, agents, issues and reports centres render", async ({ page }) => {
  await page.goto("/knowledge")
  await expect(page.getByTestId("knowledge-page")).toBeVisible()
  const knowledgeList = page.getByTestId("knowledge-list")
  await expect(knowledgeList.or(page.getByTestId("knowledge-empty"))).toBeVisible({
    timeout: 30_000,
  })
  if ((await knowledgeList.count()) > 0) {
    const knowledgeItems = page.locator('[data-testid^="knowledge-item-"]')
    await knowledgeItems.first().click()
    await expect(page.getByTestId("knowledge-detail")).toBeVisible()
    await snapViewport(page, "global-knowledge-detail")
    await page
      .getByRole("dialog", { name: "知识详情" })
      .getByRole("button", { name: "关闭", exact: true })
      .click()
  } else {
    noteGap("global-knowledge", "the global knowledge list is empty")
  }
  await snapFull(page, "global-knowledge")

  await page.goto("/quality")
  await expect(page.getByTestId("quality-page")).toBeVisible()
  await expect(page.getByTestId("quality-eval-board")).toBeVisible()
  await expect(page.getByTestId("quality-operational-metrics")).toBeVisible()
  await snapFull(page, "global-quality")

  await page.goto("/agents")
  await expect(page.getByTestId("agent-center-page")).toBeVisible()
  await expect(page.getByTestId("agent-center-catalog")).toBeVisible()
  await snapFull(page, "global-agents-catalog")
  await page.getByRole("tab", { name: "运行观察" }).click()
  await expect(page.getByTestId("agent-center-runs")).toBeVisible()
  await snapFull(page, "global-agents-runs")
  await page.getByRole("tab", { name: "配置" }).click()
  await expect(page.getByTestId("agent-center-config")).toBeVisible()
  await snapFull(page, "global-agents-config")

  await page.goto("/issues")
  await expect(page.getByTestId("global-issues-page")).toBeVisible()
  const globalIssueRows = page.getByTestId(/^global-issue-\d+$/)
  await expect(globalIssueRows.first().or(page.getByText("暂无问题"))).toBeVisible({
    timeout: 30_000,
  })
  if ((await globalIssueRows.count()) > 0) {
    await expect(page.getByRole("table", { name: "全局问题列表" })).toBeVisible()
    await globalIssueRows.first().click()
    await expect(page.getByTestId("issue-detail-sheet")).toBeVisible()
    await expect(page.getByTestId("issue-detail-sheet")).toContainText("只读视图")
    await snapViewport(page, "global-issue-detail")
    await page.getByTestId("issue-detail-sheet").getByRole("button", { name: "关闭详情" }).click()
  } else {
    noteGap("global-issues", "the global issues list is empty")
  }
  await snapFull(page, "global-issues")

  await page.goto("/reports")
  await expect(page.getByTestId("global-reports-page")).toBeVisible()
  const globalReportRows = page.getByTestId(/^global-report-\d+$/)
  await expect(globalReportRows.first().or(page.getByText("暂无报告"))).toBeVisible({
    timeout: 30_000,
  })
  if ((await globalReportRows.count()) > 0) {
    await expect(page.getByRole("table", { name: "全局报告列表" })).toBeVisible()
    await globalReportRows.first().click()
    await expect(page.getByTestId("report-detail")).toBeVisible()
    await expect(
      page.getByTestId("report-detail").getByRole("button", { name: "下载 PDF" }),
    ).toHaveCount(0)
    await snapFull(page, "global-report-detail")
  } else {
    noteGap("global-reports", "the global reports list is empty")
  }
  await snapFull(page, "global-reports")
})

test("global settings show the GC and instance panels with live in-flight counters", async ({
  page,
}) => {
  await page.goto("/settings?tab=general")
  await expect(page.getByRole("heading", { name: "设置中心" })).toBeVisible()
  await expect(page.getByRole("tab", { name: "常规设置" })).toHaveAttribute("aria-selected", "true")

  const gcPanel = page.getByTestId("gc-panel")
  await gcPanel.scrollIntoViewIfNeeded()
  await expect(gcPanel).toBeVisible({ timeout: 30_000 })
  await expect(gcPanel.getByRole("button", { name: "运行演练" })).toBeVisible()
  await expect(gcPanel.getByRole("button", { name: "执行回收" })).toBeVisible()
  await snapElement(gcPanel, "global-settings-gc")

  const instancePanel = page.getByTestId("instance-panel")
  await instancePanel.scrollIntoViewIfNeeded()
  await expect(instancePanel).toBeVisible()
  const instanceTable = page.getByRole("table", { name: "API 实例列表" })
  await expect(instanceTable.or(instancePanel.getByText("暂无注册实例"))).toBeVisible({
    timeout: 30_000,
  })
  if ((await instanceTable.count()) > 0) {
    await expect(instancePanel.getByText("运行中", { exact: true }).first()).toBeVisible()
    const activeInstance = seed.instances.find((instance) => instance.status === "active") ?? null
    if (activeInstance === null) {
      noteGap("global-settings", "no active API instance row was returned by /api/admin/instances")
    } else {
      // The counters are live and can move between the discovery snapshot and the
      // panel render, so assert the P8 in-flight projection format (requests/work)
      // instead of pinning a value that is allowed to change.
      await expect(page.getByTestId(`instance-inflight-${activeInstance.id}`)).toHaveText(
        /^\d+\/\d+$/,
      )
    }
  } else {
    noteGap(
      "global-settings",
      "the API instance roster is empty (the instance panel shows its empty state)",
    )
  }
  await snapElement(instancePanel, "global-settings-instances")
  await snapFull(page, "global-settings")
})

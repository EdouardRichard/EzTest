import { execFileSync } from "node:child_process"
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import { type APIRequestContext, type APIResponse, expect, type Page, test } from "@playwright/test"

export const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
export const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""
export const API_URL = process.env.EZTEST_E2E_API_URL?.trim() || "http://localhost:13333"
export const FAKE_LLM_URL = process.env.EZTEST_E2E_FAKE_LLM_URL?.trim() || "http://127.0.0.1:13334"

/**
 * The fake upstream appends every chat-completions request body to this log
 * (`scripts/fake-llm.cjs`). Playwright runs from `frontend/`, so the log lives
 * two levels above the spec files in the repository `.tmp` directory.
 */
export const FAKE_REQUEST_LOG =
  process.env.EZTEST_E2E_FAKE_LLM_LOG?.trim() ||
  fileURLToPath(new URL("../../.tmp/fake-llm-requests.jsonl", import.meta.url))

/** P3 acceptance screenshots live in the gitignored repository `.tmp`. */
export const P3_ACCEPTANCE_DIR = fileURLToPath(new URL("../../.tmp/p3-acceptance", import.meta.url))

/** Throwaway Git repository used by the version/code-repository E2E fixtures. */
export const P3_E2E_REPO_DIR = fileURLToPath(new URL("../../.tmp/p3-e2e-repo", import.meta.url))

/** The tiny PNG fixture reused from the LLM testdata (design spec §10.2). */
const PIXEL_PNG_PATH = fileURLToPath(
  new URL("../../backend/internal/llm/interfaces/httpapi/testdata/pixel.png", import.meta.url),
)

type DataEnvelope<T> = { data: T }

export type FakeContentPart = {
  type?: string
  text?: string
  image_url?: { url?: string }
}

export type FakeMessage = {
  role?: string
  content?: string | FakeContentPart[]
  tool_calls?: Array<{ id?: string; function?: { name?: string; arguments?: string } }>
  tool_call_id?: string
}

export type FakeChatRequest = {
  model?: string
  messages?: FakeMessage[]
  tools?: Array<{ function?: { name?: string } }>
}

type SystemRecord = { id: number; name: string }
type EnvironmentRecord = { id: number; name: string }
type CodeRepoRecord = { id: number; name: string }
type AttachmentRecord = { id: string; filename: string; kind: string; sizeBytes: number }

export type VersionRecord = {
  id: string
  name: string
  label: string
  status: string
  revision: number
  requirementCount: number
  clarifiedCount: number
  openQuestionCount: number
  codeBranch: string | null
  codeBaselineCommit: string | null
  bindings: {
    system: { id: number; name: string; available: boolean } | null
    environment: { id: number; name: string; available: boolean } | null
    codeRepository: { id: number; name: string; available: boolean } | null
  }
}

export type RequirementRecord = {
  id: string
  versionId: string
  title: string
  status: string
  revision: number
  openQuestionCount: number
  description: string
}

export type ClarificationSessionRecord = {
  id: string
  status: string
  agentSessionId: number | null
  contextSummary?: string
}

export type ClarificationOverviewRecord = {
  session: ClarificationSessionRecord | null
  questions: { open: number; answered: number; dismissed: number }
  decisions: { proposed: number; accepted: number; rejected: number }
}

export type QuestionRecord = {
  id: string
  requirementId: string | null
  question: string
  status: string
  answer: string
}

export type DecisionRecord = {
  id: string
  requirementId: string | null
  questionId: string | null
  topic: string
  decision: string
  status: string
  source: string
}

export type VersionMessageRecord = {
  id: number
  role: string
  parts: Array<{ type: string; text?: string }>
  reasoning?: string
}

export type AgentMessageImageStats = { dataUrlRows: number; imageRefRows: number } | null

/** Logs in through the real login form and waits for the protected shell. */
export async function login(page: Page, username: string, password: string): Promise<void> {
  await page.goto("/login")
  await page.getByLabel("用户名").fill(username)
  await page.getByLabel("密码").fill(password)
  await page.getByRole("button", { name: "登录", exact: true }).click()
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 })
}

/** Returns one unique, key-safe suffix for this run. */
export function uniqueSuffix(): string {
  return `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
}

/** Returns a stable screenshot path under `.tmp/p3-acceptance/`. */
export function screenshotPath(name: string): string {
  mkdirSync(P3_ACCEPTANCE_DIR, { recursive: true })
  return join(P3_ACCEPTANCE_DIR, name)
}

/**
 * Skips the calling spec when the deterministic fake LLM or the seeded admin
 * credentials are unavailable, so a P3 spec never silently talks to a real
 * provider and never fails on a deliberately partial local environment.
 */
export async function p3HarnessSkipReason(): Promise<string> {
  if (!ADMIN_PASSWORD) {
    return "EZTEST_E2E_ADMIN_PASSWORD is not set; run via scripts/e2e.ps1"
  }
  try {
    const response = await fetch(`${FAKE_LLM_URL}/v1/models`)
    if (!response.ok) {
      return `fake LLM at ${FAKE_LLM_URL} answered ${response.status}; run via scripts/e2e.ps1`
    }
  } catch {
    return `fake LLM is unavailable at ${FAKE_LLM_URL}; run via scripts/e2e.ps1`
  }
  return ""
}

/** Marks the whole spec file skipped with a reason when the harness is absent. */
export async function skipP3SpecWhenHarnessMissing(): Promise<boolean> {
  const reason = await p3HarnessSkipReason()
  if (reason === "") return false
  test.skip(true, reason)
  return true
}

function authHeaders(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

async function expectData<T>(response: APIResponse, status: number): Promise<T> {
  expect(response.status()).toBe(status)
  const body = (await response.json()) as DataEnvelope<T>
  return body.data
}

/** Logs in through the real API and returns the bearer token. */
export async function adminApiToken(request: APIRequestContext): Promise<string> {
  const response = await request.post(`${API_URL}/api/auth/login`, {
    data: { username: ADMIN_USERNAME, password: ADMIN_PASSWORD },
  })
  const data = await expectData<{ token: string }>(response, 200)
  expect(data.token).toBeTruthy()
  return data.token
}

/** Creates a P2 system through the real admin API. */
export async function createSystem(
  request: APIRequestContext,
  token: string,
  input: { key: string; name: string; baseUrl: string },
): Promise<SystemRecord> {
  const response = await request.post(`${API_URL}/api/resources/systems`, {
    headers: authHeaders(token),
    data: { ...input, kind: "web" },
  })
  return expectData<SystemRecord>(response, 201)
}

/** Creates a P2 environment under one system through the real admin API. */
export async function createEnvironment(
  request: APIRequestContext,
  token: string,
  systemId: number,
  input: { key: string; name: string; baseUrl: string },
): Promise<EnvironmentRecord> {
  const response = await request.post(`${API_URL}/api/resources/systems/${systemId}/environments`, {
    headers: authHeaders(token),
    data: { ...input, kind: "dev" },
  })
  return expectData<EnvironmentRecord>(response, 201)
}

/** Creates a P2 code repository bound to a real local Git repository. */
export async function createCodeRepo(
  request: APIRequestContext,
  token: string,
  input: { key: string; name: string; localPath: string; fixedBranch: string },
): Promise<CodeRepoRecord> {
  const response = await request.post(`${API_URL}/api/resources/code-repos`, {
    headers: authHeaders(token),
    data: input,
  })
  return expectData<CodeRepoRecord>(response, 201)
}

/** Runs one git command against the throwaway E2E repository. */
function git(...args: string[]): string {
  return execFileSync("git", ["-C", P3_E2E_REPO_DIR, ...args], { encoding: "utf8" }).trim()
}

/** Recreates `.tmp/p3-e2e-repo` as a real Git repository and returns its HEAD. */
export function prepareGitRepo(): { path: string; branch: string; commit: string } {
  rmSync(P3_E2E_REPO_DIR, { recursive: true, force: true })
  mkdirSync(P3_E2E_REPO_DIR, { recursive: true })
  writeFileSync(join(P3_E2E_REPO_DIR, "README.md"), "# EzTest P3 E2E repository\n")
  writeFileSync(join(P3_E2E_REPO_DIR, "app.ts"), "export const version = 'p3-e2e'\n")

  git("init")
  const branch = git("symbolic-ref", "--short", "HEAD")
  expect(branch).toMatch(/^(main|master)$/)
  git("-c", "core.autocrlf=false", "add", "-A")
  git(
    "-c",
    "user.name=EzTest E2E",
    "-c",
    "user.email=e2e@eztest.invalid",
    "commit",
    "-m",
    "seed p3 e2e repository",
  )
  const commit = git("rev-parse", "HEAD")
  expect(commit).toMatch(/^[0-9a-f]{40}$/)
  return { path: P3_E2E_REPO_DIR.replaceAll("\\", "/"), branch, commit }
}

/** Creates a draft version through the real API. */
export async function createVersion(
  request: APIRequestContext,
  token: string,
  input: {
    name: string
    label?: string
    description?: string
    systemId?: number
    environmentId?: number
    codeRepositoryId?: number
  },
): Promise<VersionRecord> {
  const response = await request.post(`${API_URL}/api/versions`, {
    headers: authHeaders(token),
    data: input,
  })
  return expectData<VersionRecord>(response, 201)
}

/** Lists one version's requirements through the real API. */
export async function listRequirements(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<RequirementRecord[]> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/requirements?limit=100`,
    { headers: authHeaders(token) },
  )
  expect(response.status()).toBe(200)
  const body = (await response.json()) as DataEnvelope<RequirementRecord[]>
  return body.data
}

/** Creates one draft requirement through the real API. */
export async function createRequirement(
  request: APIRequestContext,
  token: string,
  versionId: string,
  input: { title: string; description?: string; acceptanceCriteria?: string; priority?: string },
): Promise<RequirementRecord> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/requirements`, {
    headers: authHeaders(token),
    data: { priority: "should", ...input },
  })
  return expectData<RequirementRecord>(response, 201)
}

/** Uploads one multipart attachment through the real API. */
export async function uploadAttachment(
  request: APIRequestContext,
  token: string,
  versionId: string,
  requirementId: string,
  file: { name: string; mimeType: string; buffer: Buffer },
): Promise<AttachmentRecord> {
  const response = await request.post(
    `${API_URL}/api/versions/${versionId}/requirements/${requirementId}/attachments`,
    {
      headers: authHeaders(token),
      multipart: { file: { name: file.name, mimeType: file.mimeType, buffer: file.buffer } },
    },
  )
  return expectData<AttachmentRecord>(response, 201)
}

/** Lists one requirement's attachment metadata through the real API. */
export async function listAttachments(
  request: APIRequestContext,
  token: string,
  versionId: string,
  requirementId: string,
): Promise<AttachmentRecord[]> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/requirements/${requirementId}/attachments`,
    { headers: authHeaders(token) },
  )
  return expectData<AttachmentRecord[]>(response, 200)
}

/** Starts the version's clarification state through the real API. */
export async function startVersionClarification(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<VersionRecord> {
  const response = await request.post(`${API_URL}/api/versions/${versionId}/start-clarification`, {
    headers: authHeaders(token),
  })
  return expectData<VersionRecord>(response, 200)
}

/** Reads the clarification overview (session + counters) through the API. */
export async function getClarification(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<ClarificationOverviewRecord> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/clarification`, {
    headers: authHeaders(token),
  })
  return expectData<ClarificationOverviewRecord>(response, 200)
}

/** Lists clarification questions through the API. */
export async function listQuestions(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<QuestionRecord[]> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/clarification/questions?limit=100`,
    { headers: authHeaders(token) },
  )
  expect(response.status()).toBe(200)
  const body = (await response.json()) as DataEnvelope<QuestionRecord[]>
  return body.data
}

/** Lists decisions through the API. */
export async function listDecisions(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<DecisionRecord[]> {
  const response = await request.get(`${API_URL}/api/versions/${versionId}/decisions?limit=100`, {
    headers: authHeaders(token),
  })
  expect(response.status()).toBe(200)
  const body = (await response.json()) as DataEnvelope<DecisionRecord[]>
  return body.data
}

/** Reads the persisted clarification transcript through the API. */
export async function listVersionMessages(
  request: APIRequestContext,
  token: string,
  versionId: string,
): Promise<VersionMessageRecord[]> {
  const response = await request.get(
    `${API_URL}/api/versions/${versionId}/clarification/messages?limit=50`,
    { headers: authHeaders(token) },
  )
  expect(response.status()).toBe(200)
  const body = (await response.json()) as DataEnvelope<VersionMessageRecord[]>
  return body.data
}

/** Reads the tiny valid PNG reused for the multimodal fixture. */
export function readPixelPng(): Buffer {
  return readFileSync(PIXEL_PNG_PATH)
}

/**
 * Builds an image larger than the 2 MiB recon injection cap while keeping the
 * PNG signature that the attachment MIME sniff accepts (the upload limit is
 * 10 MiB, so it is still a legal attachment).
 */
export function buildOversizedPng(): Buffer {
  return Buffer.concat([readPixelPng(), Buffer.alloc(2 * 1024 * 1024 + 1, 0)])
}

/** Reads the fake upstream request log; an absent log reads as empty. */
export function readFakeRequests(): FakeChatRequest[] {
  try {
    return readFileSync(FAKE_REQUEST_LOG, "utf8")
      .split("\n")
      .filter((line) => line.trim() !== "")
      .map((line) => JSON.parse(line) as FakeChatRequest)
  } catch {
    return []
  }
}

/** Extracts one fake message's text (string or typed text parts). */
export function fakeMessageText(content: unknown): string {
  if (typeof content === "string") return content
  if (!Array.isArray(content)) return ""
  return content
    .map((part) => {
      if (typeof part === "string") return part
      if (typeof part === "object" && part !== null && "text" in part) {
        return typeof part.text === "string" ? part.text : ""
      }
      return ""
    })
    .join("")
}

/** Every text part of one fake request's messages, joined with newlines. */
export function fakeRequestText(request: FakeChatRequest): string {
  return (request.messages ?? []).map((message) => fakeMessageText(message.content)).join("\n")
}

/** Counts the resolved image_url parts of one fake request. */
export function countImageParts(request: FakeChatRequest): number {
  let count = 0
  for (const message of request.messages ?? []) {
    if (!Array.isArray(message.content)) continue
    for (const part of message.content) {
      if (part.type === "image_url") count += 1
    }
  }
  return count
}

/** Returns the data URLs carried by one fake request. */
export function imageUrls(request: FakeChatRequest): string[] {
  const urls: string[] = []
  for (const message of request.messages ?? []) {
    if (!Array.isArray(message.content)) continue
    for (const part of message.content) {
      const url = part.image_url?.url
      if (typeof url === "string" && url !== "") urls.push(url)
    }
  }
  return urls
}

/** Waits until the fake request log contains one matching request. */
export async function waitForFakeRequest(
  match: (request: FakeChatRequest) => boolean,
  timeout = 30_000,
): Promise<FakeChatRequest> {
  await expect.poll(() => readFakeRequests().some(match), { timeout }).toBe(true)
  const found = readFakeRequests().find(match)
  if (found === undefined) throw new Error("matching fake LLM request was not found")
  return found
}

/** Reads the default LLM configuration through the real admin API. */
export async function getDefaultLlmConfig(
  request: APIRequestContext,
  token: string,
): Promise<{ id: number; protocol: string }> {
  const response = await request.get(`${API_URL}/api/admin/llm/configs`, {
    headers: authHeaders(token),
  })
  const items = await expectData<Array<{ id: number; protocol: string; is_default: boolean }>>(
    response,
    200,
  )
  const found = items.find((item) => item.is_default)
  if (found === undefined) throw new Error("no default LLM configuration is seeded")
  return { id: found.id, protocol: found.protocol }
}

/**
 * Switches the default LLM protocol to make `Gateway.Prepare` fail (the P3
 * endpoints then return the spec §8.6 503 ai_unavailable). The degraded spec
 * restores the original protocol in its finally/afterAll paths.
 */
export async function setDefaultLlmProtocol(
  request: APIRequestContext,
  token: string,
  id: number,
  protocol: string,
): Promise<void> {
  const response = await request.patch(`${API_URL}/api/admin/llm/configs/${id}`, {
    headers: authHeaders(token),
    data: { protocol },
  })
  await expectData(response, 200)
}

/** Reads the bearer token the browser stored after the real login form. */
export async function pageToken(page: Page): Promise<string> {
  const token = await page.evaluate(() => sessionStorage.getItem("eztest.token"))
  if (!token) throw new Error("the browser has no EzTest session token")
  return token
}

/**
 * Counts persisted `agent_messages` rows that contain a data URL versus an image
 * reference. This is the design spec §10.2/D30 assertion that the transient
 * base64 never reaches the database. Returns null when the e2e script did not
 * export the database access used by this check, so a locally started browser
 * run still works without psql.
 */
export function readAgentMessageImageStats(): AgentMessageImageStats {
  const psql = process.env.EZTEST_E2E_PSQL?.trim()
  const databaseUrl = process.env.EZTEST_E2E_DATABASE_URL?.trim()
  if (!psql || !databaseUrl) return null
  const sql =
    "SELECT count(*) FILTER (WHERE content::text LIKE '%data:image%'), " +
    "count(*) FILTER (WHERE content::text LIKE '%image_ref%') FROM agent_messages;"
  let output = ""
  try {
    output = execFileSync(psql, ["-X", "-d", databaseUrl, "-tA", "-c", sql], {
      encoding: "utf8",
    }).trim()
  } catch {
    return null
  }
  const [dataUrlRows, imageRefRows] = output.split("|").map((value) => Number(value.trim()))
  if (!Number.isFinite(dataUrlRows) || !Number.isFinite(imageRefRows)) return null
  return { dataUrlRows, imageRefRows }
}

/** Waits until at least one persisted image reference exists (recon finished). */
export async function waitForPersistedImageRef(timeout = 30_000): Promise<AgentMessageImageStats> {
  let stats = readAgentMessageImageStats()
  if (stats === null) return null
  await expect
    .poll(
      () => {
        stats = readAgentMessageImageStats()
        return stats === null ? true : stats.imageRefRows > 0
      },
      { timeout },
    )
    .toBe(true)
  return stats
}

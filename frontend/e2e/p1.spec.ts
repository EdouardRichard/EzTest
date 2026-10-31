import fs from "node:fs"
import path from "node:path"

import { expect, test, type APIRequestContext } from "@playwright/test"

const API = process.env.EZTEST_E2E_API_URL?.trim() || "http://127.0.0.1:14333"
const FAKE_LLM = process.env.EZTEST_E2E_FAKE_LLM_URL?.trim() || "http://127.0.0.1:13335/v1"
const suffix = `${Date.now()}-${Math.floor(Math.random() * 1000)}`

type Setup = {
  versionID: string
  sutID: string
  environmentID: string
  pageID: string
  apiID: string
  conversationID: string
}

let setup: Setup
let token = ""

async function api<T>(
  request: APIRequestContext,
  method: "GET" | "POST" | "PATCH",
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

async function latestAssistantText(request: APIRequestContext, conversationID: string): Promise<string> {
  const response = await api<{ data: Array<{ role: string; parts: Array<{ type: string; text?: string }> }> }>(
    request,
    "GET",
    `/api/versions/${setup.versionID}/conversations/${conversationID}/messages?limit=100`,
  )
  const assistant = response.data.filter((message) => message.role === "assistant").at(-1)
  return (assistant?.parts ?? []).map((part) => part.text ?? "").join("")
}

test.beforeAll(async ({ request }) => {
  const login = await api<{ data: { token: string } }>(request, "POST", "/api/auth/login", {
    username: "admin",
    password: "admin123",
  })
  token = login.data.token

  const config = await api<{ data: { id: string } }>(request, "POST", "/api/admin/llm/configs", {
    name: `p1-e2e-llm-${suffix}`,
    provider: "openai",
    protocol: "openai",
    base_url: FAKE_LLM,
    api_key: "fake-llm-key",
    timeout_seconds: 30,
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

  const sut = await api<{ data: { id: string } }>(request, "POST", "/api/resources/suts", {
    name: `P1 E2E SUT ${suffix}`,
    intro: "P1 clarification E2E",
  }, 201)
  const environment = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/suts/${sut.data.id}/environments`,
    { name: `e2e-${suffix}`, base_url: "https://e2e.example.test", is_default: true },
    201,
  )
  const page = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/environments/${environment.data.id}/pages`,
    {
      name: "登录页",
      url: "https://e2e.example.test/login",
      route_template: `/login-${suffix}`,
      page_type: "page",
      auth_role: "guest",
      module: "auth",
    },
    201,
  )
  const apiEndpoint = await api<{ data: { id: string } }>(
    request,
    "POST",
    `/api/resources/environments/${environment.data.id}/apis`,
    { name: "登录 API", method: "POST", path_template: `/api/login-${suffix}`, auth_role: "guest" },
    201,
  )
  const version = await api<{ data: { id: string } }>(request, "POST", "/api/versions", {
    title: `P1 E2E 版本 ${suffix}`,
    bindings: [
      {
        sut_id: sut.data.id,
        environment_id: environment.data.id,
        page_ids: [page.data.id],
        api_ids: [apiEndpoint.data.id],
        database_ids: [],
        accounts: [],
      },
    ],
  }, 201)
  setup = {
    versionID: version.data.id,
    sutID: sut.data.id,
    environmentID: environment.data.id,
    pageID: page.data.id,
    apiID: apiEndpoint.data.id,
    conversationID: "",
  }
})

test("P1 clarification workspace: conversation, AI draft, confirm, batch, team room", async ({
  page,
  request,
}) => {
  test.setTimeout(180_000)
  const pageErrors: string[] = []
  page.on("pageerror", (error) => pageErrors.push(String(error)))

  await page.goto("/login")
  await page.fill("#username", "admin")
  await page.fill("#password", "admin123")
  await page.getByRole("button", { name: "登录" }).click()
  await page.waitForURL("**/versions")

  await page.goto(`/versions/${setup.versionID}?tab=requirements`)
  await expect(page.getByTestId("requirements-tab")).toBeVisible()
  await expect(page.getByTestId("chat-panel").getByTestId("sse-status")).toBeVisible()

  // New conversation -> Host opening + Curator draft.
  await page.getByTestId("chat-panel").getByRole("button", { name: /新增对话/ }).first().click()
  await expect(page.getByTestId("chat-panel")).toBeVisible()
  await expect(page.getByTestId("message-assistant").first()).toBeVisible({ timeout: 30_000 })

  // Attachment + user message; Scout produces a team-room finding.
  await page.getByTestId("attachment-input").setInputFiles({
    name: "login-spec.md",
    mimeType: "text/markdown",
    buffer: Buffer.from(
      "# Login clarification\n\n- Password and SMS login\n- Remember-me is optional\n- Errors must not reveal account existence\n",
    ),
  })
  await expect(page.getByTestId("composer-textarea")).toBeEnabled()
  await page.getByTestId("composer-textarea").fill(
    "请根据附件澄清登录需求，说明登录方式、记住我和错误提示的要求。",
  )
  await page.getByRole("button", { name: "发送" }).click()

  const requirementItems = page.locator('[data-testid^="requirement-item-"]')
  await expect(requirementItems.first()).toBeVisible({ timeout: 30_000 })
  await requirementItems.first().getByRole("button").click()
  await expect(page.getByTestId("requirement-editor")).toBeVisible()

  // Edit the AI draft and save (creates a new immutable revision).
  const editedTitle = `登录需求（UI 编辑）${suffix}`
  await page.getByTestId("editor-title").fill(editedTitle)
  await page.getByTestId("editor-save").click()
  await expect(page.getByTestId("requirement-editor")).toContainText(/v[2-9]/)

  // Single confirmation from the editor.
  await page.getByTestId("editor-confirm").click()
  await expect(page.getByTestId("editor-rollback-hint")).toBeVisible({ timeout: 20_000 })

  // Two manual drafts -> select both -> batch confirm.
  const firstDraft = await api<{ data: { id: string } }>(request, "POST", `/api/versions/${setup.versionID}/requirements`, {
    title: `批量需求 A ${suffix}`,
    description: "批量确认 A",
    acceptance_criteria: "A 可确认",
    priority: "P1",
    targets: { version_suts: [], pages: [], apis: [] },
  }, 201)
  const secondDraft = await api<{ data: { id: string } }>(request, "POST", `/api/versions/${setup.versionID}/requirements`, {
    title: `批量需求 B ${suffix}`,
    description: "批量确认 B",
    acceptance_criteria: "B 可确认",
    priority: "P2",
    targets: { version_suts: [], pages: [], apis: [] },
  }, 201)

  await page.reload()
  await expect(page.getByTestId("requirements-tab")).toBeVisible()
  await expect(page.getByTestId(`requirement-item-${firstDraft.data.id}`)).toBeVisible()
  await expect(page.getByTestId(`requirement-item-${secondDraft.data.id}`)).toBeVisible()
  await page.getByTestId(`requirement-item-${firstDraft.data.id}`).getByRole("checkbox").check()
  await page.getByTestId(`requirement-item-${secondDraft.data.id}`).getByRole("checkbox").check()
  await expect(page.getByTestId("bulk-confirm-bar")).toBeVisible()
  await page.getByRole("button", { name: "批量确认已澄清" }).click()
  await expect(page.getByTestId("bulk-confirm-notice")).toBeVisible({ timeout: 20_000 })
  await expect(page.getByTestId("design-cta")).toBeVisible()

  // Team room is readable and contains the Scout finding.
  await page.getByTestId("team-room-toggle").click()
  await expect(page.getByTestId("team-room-panel")).toBeVisible()
  await expect(page.getByTestId("team-room-posts")).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId("team-room-posts").locator("li").first()).toBeVisible()

  // Evidence screenshots.
  const screenshotDir = path.resolve(process.cwd(), "..", ".tmp", "p1-acceptance", "screenshots")
  fs.mkdirSync(screenshotDir, { recursive: true })
  await page.screenshot({ path: path.join(screenshotDir, "requirements-workspace.png"), fullPage: true })
  await page.getByTestId("team-room-panel").screenshot({
    path: path.join(screenshotDir, "team-room.png"),
  })

  expect(pageErrors).toEqual([])
})

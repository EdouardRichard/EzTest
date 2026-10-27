import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

import { expect, type Locator, type Page, test } from "@playwright/test"

const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""
const USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"
const API_URL = process.env.EZTEST_E2E_API_URL?.trim() || "http://localhost:13333"

/**
 * The fake upstream appends every chat-completions request body to this log
 * (design spec §12.2). scripts/e2e.ps1 runs Playwright from `frontend/`, so the
 * log lives two levels above the spec file in the repository `.tmp` directory.
 */
const FAKE_REQUEST_LOG = fileURLToPath(
  new URL("../../.tmp/fake-llm-requests.jsonl", import.meta.url),
)

type FakeChatMessage = { role?: string; content?: unknown }
type FakeChatRequest = { messages?: FakeChatMessage[] }

function requireAdminPassword(): string {
  if (!ADMIN_PASSWORD) {
    throw new Error("EZTEST_E2E_ADMIN_PASSWORD must be set; run via scripts/e2e.ps1")
  }
  return ADMIN_PASSWORD
}

/** Logs in through the real login form and waits for the protected shell. */
async function login(page: Page, username: string, password: string): Promise<void> {
  await page.goto("/login")
  await page.getByLabel("用户名").fill(username)
  await page.getByLabel("密码").fill(password)
  await page.getByRole("button", { name: "登录", exact: true }).click()
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 })
}

/** The right-hand session pane; every transcript assertion is scoped to it. */
function sessionPane(page: Page): Locator {
  return page.getByRole("region", { name: "会话内容" })
}

/**
 * One transcript run group. `Transcript` labels each run section with
 * `aria-label="运行 {run_id}"`, so the count is the visible run count.
 */
function runGroups(pane: Locator): Locator {
  return pane.getByRole("region", { name: /^运行 \d+$/ })
}

/** Types and sends one message once the composer is enabled again. */
async function sendMessage(pane: Locator, message: string): Promise<void> {
  const composer = pane.getByLabel("发送消息")
  await expect(composer).toBeEnabled()
  await composer.fill(message)
  await pane.getByRole("button", { name: "发送", exact: true }).click()
}

/** Creates a session and returns its transcript pane. */
async function startSession(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "新建会话" }).click()
  const pane = sessionPane(page)
  await expect(pane.getByRole("log", { name: "转录内容" })).toBeVisible()
  await expect(page).toHaveURL(/[?&]session=\d+/)
  return pane
}

/** Extracts the text of one message content (string or text parts). */
function messageText(content: unknown): string {
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

/** Reads the fake upstream request log; an absent log reads as empty. */
function readFakeRequests(): FakeChatRequest[] {
  try {
    return readFileSync(FAKE_REQUEST_LOG, "utf8")
      .split("\n")
      .filter((line) => line.trim() !== "")
      .map((line) => JSON.parse(line) as FakeChatRequest)
  } catch {
    return []
  }
}

test.describe("agent console", () => {
  // The three scenarios share the dedicated e2e database and the built-in
  // profile; the prompt edit must apply to the run created afterwards.
  test.describe.configure({ mode: "serial" })

  test("admin runs the validation agent end to end", async ({ page }) => {
    const adminPassword = requireAdminPassword()

    await login(page, ADMIN_USERNAME, adminPassword)
    await page.goto("/agents")
    await expect(page.getByRole("heading", { name: "Agent 运行时控制台" })).toBeVisible({
      timeout: 30_000,
    })

    const pane = await startSession(page)
    const runs = runGroups(pane)

    // First run: a live run_started (the run heading), reasoning, the
    // file.read tool call + result, usage and the completed status.
    await sendMessage(pane, "检查运行时")
    await expect(runs).toHaveCount(1, { timeout: 30_000 })
    const firstRun = runs.first()
    await expect(firstRun).toContainText(/运行 #\d+/)

    const thinking = firstRun.getByRole("button", { name: "思考" })
    const tool = firstRun.getByRole("button", { name: "工具 file.read" })
    await expect(thinking).toBeVisible()
    await expect(tool).toBeVisible()
    await expect(firstRun.getByRole("button", { name: "用量" })).toBeVisible()
    await expect(firstRun.getByText(/诊断完成/)).toBeVisible({ timeout: 30_000 })
    await expect(pane.getByText("已完成", { exact: true })).toBeVisible({ timeout: 30_000 })
    await expect(tool).toContainText("成功")

    // Expanding the thinking block lazily loads the persisted reasoning.
    await thinking.click()
    await expect(firstRun.getByText("需要读取 README")).toBeVisible()

    // The tool block pairs tool_use/tool_result by call_id and lazily loads the
    // full arguments (README.md) and the README content itself.
    await tool.click()
    await expect(firstRun.getByText(/"path":"README\.md"/)).toBeVisible()
    // The expanded result carries the README's stable title heading rather than
    // mutable introductory prose.
    await expect(firstRun.getByText(/# EzTest/)).toBeVisible()

    // The usage row aggregates both model turns of the run (152 + 272 tokens).
    await firstRun.getByRole("button", { name: "用量" }).click()
    await expect(firstRun.getByText("共 424 tokens")).toBeVisible()

    // Second message in the same session starts a second run, live.
    await sendMessage(pane, "再总结一下")
    await expect(runs).toHaveCount(2, { timeout: 30_000 })
    const secondRun = runs.nth(1)
    await expect(secondRun.getByRole("button", { name: "思考" })).toBeVisible()
    await expect(secondRun.getByRole("button", { name: "工具 file.read" })).toBeVisible()
    await expect(secondRun.getByText(/诊断完成/)).toBeVisible({ timeout: 30_000 })
    await expect(pane.getByText("已完成", { exact: true })).toBeVisible()

    // The fake request log proves the second run's context contains the first
    // user turn (design spec §12.3 item 4).
    await expect
      .poll(
        () => {
          const requests = readFakeRequests()
          const last = requests[requests.length - 1]
          if (!last) return false
          return (last.messages ?? []).some(
            (message) =>
              message.role === "user" && messageText(message.content).includes("检查运行时"),
          )
        },
        { timeout: 20_000 },
      )
      .toBe(true)

    // SLOW streams for about six seconds: assert the live running status and
    // abort it. The rendered run outcome carries the terminal reason and the
    // user_abort code (design spec §11.4).
    await sendMessage(pane, "SLOW")
    await expect(runs).toHaveCount(3, { timeout: 30_000 })
    await expect(pane.getByText("运行中", { exact: true })).toBeVisible()
    const stop = pane.getByRole("button", { name: "停止" })
    await expect(stop).toBeVisible()
    await stop.click()
    await expect(pane.getByText("已中止", { exact: true })).toBeVisible({ timeout: 30_000 })
    const abortedRun = runs.nth(2)
    await expect(abortedRun.getByText(/aborted_streaming/)).toBeVisible()
    await expect(abortedRun.getByText(/user_abort/)).toBeVisible()

    // The service must still accept new messages after an abort (design spec
    // §12.3 item 5): a normal message starts a fresh run that completes with
    // its own text and tool evidence instead of staying wedged.
    await sendMessage(pane, "继续检查")
    await expect(runs).toHaveCount(4, { timeout: 30_000 })
    const postAbortRun = runs.nth(3)
    const postAbortTool = postAbortRun.getByRole("button", { name: "工具 file.read" })
    await expect(postAbortRun.getByText("继续检查")).toBeVisible()
    await expect(postAbortRun.getByRole("button", { name: "思考" })).toBeVisible()
    await expect(postAbortTool).toBeVisible()
    await expect(postAbortRun.getByRole("button", { name: "用量" })).toBeVisible()
    await expect(postAbortRun.getByText(/诊断完成/)).toBeVisible({ timeout: 30_000 })
    await expect(postAbortTool).toContainText("成功")
    await expect(pane.getByText("已完成", { exact: true })).toBeVisible()

    // A reload replays the complete transcript, not only its text (design spec
    // §12.3 item 6): all four run groups (three diagnostics plus the aborted
    // run) and their thinking, file.read and usage activities must come back
    // from the event snapshot.
    await page.reload()
    await expect(page.getByRole("heading", { name: "Agent 运行时控制台" })).toBeVisible({
      timeout: 30_000,
    })
    await expect(page).toHaveURL(/[?&]session=\d+/)
    const replayPane = sessionPane(page)
    const replayRuns = runGroups(replayPane)
    await expect(replayRuns).toHaveCount(4, { timeout: 30_000 })
    await expect(replayPane.getByText(/诊断完成/)).toHaveCount(3)

    const replayTools = replayRuns.getByRole("button", { name: "工具 file.read" })
    await expect(replayRuns.getByRole("button", { name: "思考" })).toHaveCount(3)
    await expect(replayTools).toHaveCount(3)
    await expect(replayTools.filter({ hasText: "成功" })).toHaveCount(3)
    await expect(replayRuns.getByRole("button", { name: "用量" })).toHaveCount(3)

    const replayedAbortedRun = replayRuns.nth(2)
    await expect(replayedAbortedRun.getByText(/aborted_streaming/)).toBeVisible()
    await expect(replayedAbortedRun.getByText(/user_abort/)).toBeVisible()
    await expect(replayRuns.nth(3).getByText(/诊断完成/)).toBeVisible()
  })

  test("prompt edit applies to the next run", async ({ page }) => {
    const adminPassword = requireAdminPassword()
    const marker = `E2E-MARKER-${Date.now()}`

    await login(page, ADMIN_USERNAME, adminPassword)
    await page.goto("/agents")
    await expect(page.getByRole("heading", { name: "Agent 运行时控制台" })).toBeVisible({
      timeout: 30_000,
    })

    // The fake ECHO_SYSTEM behavior echoes only the first 80 characters of the
    // system prompt, so the marker must be inserted at the very start.
    await page.getByRole("tab", { name: "配置" }).click()
    const prompt = page.getByLabel("系统提示词")
    await expect(prompt).toBeVisible()
    const originalPrompt = await prompt.inputValue()
    const updatedPrompt = `${marker}\n${originalPrompt}`
    await prompt.fill(updatedPrompt)
    await page.getByRole("button", { name: "保存配置" }).click()
    await expect(page.getByRole("status").filter({ hasText: "下次运行生效" })).toBeVisible()

    await page.getByRole("tab", { name: "会话" }).click()
    const pane = await startSession(page)
    await sendMessage(pane, "ECHO_SYSTEM")
    await expect(pane.getByText(`system=${marker}`)).toBeVisible({ timeout: 30_000 })
    await expect(pane.getByText("已完成", { exact: true })).toBeVisible()
  })

  test("regular user sees the admin-only state and gets 403", async ({ page, request }) => {
    const adminPassword = requireAdminPassword()
    const username = `qa_agent_${Date.now().toString(36)}`

    // Create the normal user through the real admin API (no role field: P0
    // only creates regular users).
    const adminLogin = await request.post(`${API_URL}/api/auth/login`, {
      data: { username: ADMIN_USERNAME, password: adminPassword },
    })
    expect(adminLogin.ok()).toBeTruthy()
    const adminBody = (await adminLogin.json()) as { data?: { token?: string } }
    const adminToken = adminBody.data?.token
    expect(adminToken).toBeTruthy()
    const created = await request.post(`${API_URL}/api/users`, {
      headers: { Authorization: `Bearer ${adminToken}` },
      data: { username, password: USER_PASSWORD },
    })
    expect(created.status()).toBe(201)

    await login(page, username, USER_PASSWORD)
    await page.goto("/agents")
    await expect(page.getByRole("heading", { name: "仅管理员可用" })).toBeVisible({
      timeout: 30_000,
    })
    await expect(page.getByRole("button", { name: "新建会话" })).toHaveCount(0)
    await expect(page.getByLabel("发送消息")).toHaveCount(0)

    // Admin-only is enforced by the API, not only hidden in the UI.
    const token = await page.evaluate(() => sessionStorage.getItem("eztest.token"))
    expect(token).toBeTruthy()
    const response = await request.get(`${API_URL}/api/agents/sessions`, {
      headers: { Authorization: `Bearer ${token ?? ""}` },
    })
    expect(response.status()).toBe(403)
    const body = (await response.json()) as { error?: { code?: string } }
    expect(body.error?.code).toBe("forbidden")
  })
})

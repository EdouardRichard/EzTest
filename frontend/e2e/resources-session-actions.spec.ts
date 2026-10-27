import { mkdirSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import { type APIRequestContext, type Browser, expect, type Page, test } from "@playwright/test"

const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""
const API_URL = process.env.EZTEST_E2E_API_URL?.trim() || "http://localhost:13333"

/** The deterministic fixture target started by scripts/e2e.ps1 (spec §9.2). */
const FIXTURE_URL = "http://127.0.0.1:13335"
const FIXTURE_USERNAME = "fixture"
const FIXTURE_PASSWORD = "fixture-pass"

/** P2 acceptance screenshots live in the gitignored repository `.tmp` (spec §9.2). */
const P2_ACCEPTANCE_DIR = fileURLToPath(new URL("../../.tmp/p2-acceptance", import.meta.url))

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

/** Returns one unique, key-safe suffix for this run. */
function uniqueSuffix(): string {
  return `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
}

/** Returns a stable screenshot path under `.tmp/p2-acceptance/`. */
function screenshotPath(name: string): string {
  mkdirSync(P2_ACCEPTANCE_DIR, { recursive: true })
  return join(P2_ACCEPTANCE_DIR, name)
}

/** Logs in through the real admin API and returns the bearer token. */
async function adminApiToken(request: APIRequestContext): Promise<string> {
  const response = await request.post(`${API_URL}/api/auth/login`, {
    data: { username: ADMIN_USERNAME, password: requireAdminPassword() },
  })
  expect(response.ok()).toBeTruthy()
  const body = (await response.json()) as { data?: { token?: string } }
  const token = body.data?.token
  expect(token).toBeTruthy()
  return token ?? ""
}

/** Logs into the fixture target in a separate context and exports storageState. */
async function fixtureStorageState(browser: Browser): Promise<unknown> {
  const context = await browser.newContext()
  try {
    const page = await context.newPage()
    await page.goto(`${FIXTURE_URL}/login`)
    await page.getByLabel("Username").fill(FIXTURE_USERNAME)
    await page.getByLabel("Password").fill(FIXTURE_PASSWORD)
    await page.getByRole("button", { name: "Sign in" }).click()
    await expect(page.getByRole("heading", { name: "Fixture Account" })).toBeVisible()
    return await context.storageState()
  } finally {
    await context.close()
  }
}

type StorageStateDocument = {
  cookies: Record<string, unknown>[]
  origins: unknown[]
}

/**
 * Adds one marker cookie to a freshly exported fixture state so the takeover
 * completion provably replaces the stored state (the detail's Cookie count grows).
 */
function withMarkerCookie(state: unknown): StorageStateDocument {
  const document = state as StorageStateDocument
  return {
    ...document,
    cookies: [
      ...document.cookies,
      {
        name: "e2e_takeover",
        value: new Date().toISOString(),
        domain: "127.0.0.1",
        path: "/",
        expires: -1,
        httpOnly: false,
        secure: false,
        sameSite: "Lax",
      },
    ],
  }
}

type FixtureResources = {
  systemName: string
  environmentName: string
  accountLabel: string
  accountId: number
  accessId: number
  environmentId: number
}

type LoginCheckConfig = { url: string; expectStatus: number[]; bodyContains: string }

/**
 * Creates the account/access/system/environment chain for one test through the
 * real admin API, so each scenario owns fresh resources. Only the session flows
 * themselves are driven through the UI.
 */
async function createResources(
  request: APIRequestContext,
  options: {
    tag: string
    withLoginCheck: boolean
    /** Defaults to a web system; the token scenario uses an API system (spec §9.2 item 6). */
    systemKind?: "web" | "api"
    /** Overrides the login_check; `undefined` derives it from `withLoginCheck`. */
    loginCheck?: LoginCheckConfig | null
    authType?: "manual" | "token"
  },
): Promise<FixtureResources> {
  const token = await adminApiToken(request)
  const headers = { Authorization: `Bearer ${token}` }
  const suffix = uniqueSuffix()
  const systemName = `P2 ${options.tag} ${suffix}`
  const environmentName = options.withLoginCheck ? `检查环境 ${suffix}` : `无检查环境 ${suffix}`
  const accountLabel = `Fixture 账号 ${suffix}`
  const loginCheck: LoginCheckConfig | null =
    options.loginCheck !== undefined
      ? options.loginCheck
      : options.withLoginCheck
        ? {
            url: `${FIXTURE_URL}/account`,
            expectStatus: [200],
            bodyContains: "Fixture Account",
          }
        : null

  const systemResponse = await request.post(`${API_URL}/api/resources/systems`, {
    headers,
    data: {
      key: `pw-${options.tag}-${suffix}`,
      name: systemName,
      kind: options.systemKind ?? "web",
      baseUrl: FIXTURE_URL,
      defaultSessionTtlSeconds: 3600,
      notes: "E2E session actions",
    },
  })
  expect(systemResponse.status()).toBe(201)
  const systemBody = (await systemResponse.json()) as { data: { id: number } }
  const systemId = systemBody.data.id

  const environmentResponse = await request.post(
    `${API_URL}/api/resources/systems/${systemId}/environments`,
    {
      headers,
      data: {
        key: options.withLoginCheck ? "check" : "nocheck",
        name: environmentName,
        kind: "staging",
        baseUrl: FIXTURE_URL,
        isProd: false,
        allowedHosts: [],
        loginCheck,
        allowAutoLogin: false,
        requireApproval: false,
      },
    },
  )
  expect(environmentResponse.status()).toBe(201)
  const environmentBody = (await environmentResponse.json()) as { data: { id: number } }

  const accountResponse = await request.post(`${API_URL}/api/resources/accounts`, {
    headers,
    data: {
      key: `fixture-${options.tag}-${suffix}`,
      label: accountLabel,
      username: FIXTURE_USERNAME,
      kind: "human",
      notes: "",
    },
  })
  expect(accountResponse.status()).toBe(201)
  const accountBody = (await accountResponse.json()) as { data: { id: number } }

  const accessResponse = await request.post(
    `${API_URL}/api/resources/accounts/${accountBody.data.id}/accesses`,
    {
      headers,
      data: {
        systemId,
        authType: options.authType ?? "manual",
        usernameOverride: "",
        loginUrl: "",
        enabled: true,
        concurrencyLimit: 1,
        sessionTtlSeconds: null,
        permissionNote: "E2E read-only account",
      },
    },
  )
  expect(accessResponse.status()).toBe(201)
  const accessBody = (await accessResponse.json()) as { data: { id: number } }

  return {
    systemName,
    environmentName,
    accountLabel,
    accountId: accountBody.data.id,
    accessId: accessBody.data.id,
    environmentId: environmentBody.data.id,
  }
}

type ImportedSession = { sessionId: number; resultText: string }

/** Imports one session document for resources and returns the new session. */
async function importSession(
  page: Page,
  resources: FixtureResources,
  document: unknown,
  options: { kind?: "storage_state" | "token"; accessAuthType?: string } = {},
): Promise<ImportedSession> {
  const kind = options.kind ?? "storage_state"
  const accessAuthType = options.accessAuthType ?? "manual"
  await page.getByRole("button", { name: "导入会话" }).click()
  const dialog = page.getByRole("dialog", { name: "导入会话" })
  await expect(dialog).toBeVisible()

  await expect(
    dialog.locator("#session-import-account option").filter({ hasText: resources.accountLabel }),
  ).toHaveCount(1)
  await dialog.locator("#session-import-account").selectOption(String(resources.accountId))

  await expect(
    dialog.locator("#session-import-access option").filter({ hasText: accessAuthType }),
  ).toHaveCount(1)
  await dialog.locator("#session-import-access").selectOption(String(resources.accessId))

  await expect(
    dialog
      .locator("#session-import-environment option")
      .filter({ hasText: resources.environmentName }),
  ).toHaveCount(1)
  await dialog.locator("#session-import-environment").selectOption(String(resources.environmentId))

  if (kind === "token") {
    // Token JSON mode: select the kind and paste the document (spec §6.2).
    await dialog.locator("#session-import-kind").selectOption("token")
    await dialog.locator("#session-import-json").fill(JSON.stringify(document))
  } else {
    await dialog.locator("#session-import-file").setInputFiles({
      name: "fixture-storage-state.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(document)),
    })
    await expect(dialog.locator("#session-import-json")).not.toHaveValue("")
  }
  await dialog.getByRole("button", { name: "导入", exact: true }).click()

  const result = dialog.getByRole("status")
  await expect(result).toContainText(/已导入会话 #\d+：状态 (valid|needs_manual)。/)
  const resultText = (await result.textContent()) ?? ""
  const sessionId = Number(/已导入会话 #(\d+)/.exec(resultText)?.[1] ?? 0)
  expect(sessionId).toBeGreaterThan(0)

  await dialog.getByRole("button", { name: "关闭" }).click()
  await expect(dialog).toHaveCount(0)
  return { sessionId, resultText }
}

test.describe("session actions", () => {
  test.describe.configure({ timeout: 180_000 })

  test("validate, invalidate and revoke a login-checked session", async ({
    page,
    browser,
    request,
  }) => {
    const adminPassword = requireAdminPassword()
    const resources = await createResources(request, { tag: "validate", withLoginCheck: true })
    const storageState = await fixtureStorageState(browser)

    await login(page, ADMIN_USERNAME, adminPassword)
    await page.goto("/resources?tab=sessions")
    await expect(page.getByRole("heading", { name: "会话", exact: true })).toBeVisible()
    const { sessionId } = await importSession(page, resources, storageState)

    const detail = page.getByRole("region", { name: "会话详情" })
    await expect(detail.getByRole("heading", { name: `会话 #${sessionId}` })).toBeVisible()
    await expect(detail.getByText("valid", { exact: true })).toBeVisible()

    const validate = detail.getByRole("button", { name: "校验", exact: true })
    const confirm = detail.getByRole("button", { name: "确认", exact: true })
    const invalidate = detail.getByRole("button", { name: "失效", exact: true })
    const revoke = detail.getByRole("button", { name: "撤销", exact: true })
    const requestTakeover = detail.getByRole("button", { name: "发起接管" })

    // A valid session can be re-validated; confirm is only legal from
    // pending/needs_manual (spec §7.5 allowed-state table).
    await expect(validate).toBeEnabled()
    await expect(confirm).toBeDisabled()
    await expect(invalidate).toBeEnabled()
    await expect(revoke).toBeEnabled()
    await validate.click()
    await expect(detail.getByRole("status")).toContainText("校验完成：状态 valid。")
    await expect(detail.getByText("允许 1 / 拒绝 0 / 错误 0", { exact: true })).toBeVisible()

    await invalidate.click()
    await expect(detail.getByRole("status")).toContainText("已失效会话：状态 invalid。")
    await expect(detail.getByText("invalid", { exact: true })).toBeVisible()
    await expect(detail.getByText(/失效\/撤销原因：管理员手动失效/)).toBeVisible()
    await expect(confirm).toBeDisabled()
    await expect(invalidate).toBeDisabled()
    await expect(validate).toBeEnabled()
    await expect(revoke).toBeEnabled()
    await page.screenshot({
      path: screenshotPath("session-actions-invalidated.png"),
      fullPage: true,
    })

    await revoke.click()
    await expect(detail.getByRole("status")).toContainText("已撤销会话：状态 revoked。")
    await expect(detail.getByText("revoked", { exact: true })).toBeVisible()
    await expect(detail.getByText(/失效\/撤销原因：管理员手动撤销/)).toBeVisible()
    for (const button of [validate, confirm, invalidate, revoke, requestTakeover]) {
      await expect(button).toBeDisabled()
    }
  })

  test("validate, confirm and revoke a session that has no login check", async ({
    page,
    browser,
    request,
  }) => {
    const adminPassword = requireAdminPassword()
    const resources = await createResources(request, { tag: "confirm", withLoginCheck: false })
    const storageState = await fixtureStorageState(browser)

    await login(page, ADMIN_USERNAME, adminPassword)
    await page.goto("/resources?tab=sessions")
    await expect(page.getByRole("heading", { name: "会话", exact: true })).toBeVisible()
    const imported = await importSession(page, resources, storageState)
    expect(imported.resultText).toContain("能力未验证（degraded）")

    const detail = page.getByRole("region", { name: "会话详情" })
    await expect(detail.getByRole("heading", { name: `会话 #${imported.sessionId}` })).toBeVisible()
    await expect(detail.getByText("needs_manual", { exact: true })).toBeVisible()
    await expect(detail.getByText("能力未验证", { exact: true })).toBeVisible()

    // Same session: a re-check without a configured login_check stays
    // needs_manual, then manual confirmation moves it to valid.
    const validate = detail.getByRole("button", { name: "校验", exact: true })
    await expect(validate).toBeEnabled()
    await validate.click()
    await expect(detail.getByRole("status")).toContainText("校验完成：状态 needs_manual。")
    await expect(detail.getByText("needs_manual", { exact: true })).toBeVisible()

    const confirm = detail.getByRole("button", { name: "确认", exact: true })
    await expect(confirm).toBeEnabled()
    await confirm.click()
    await expect(detail.getByRole("status")).toContainText("已确认会话：状态 valid。")
    await expect(detail.getByText("valid", { exact: true })).toBeVisible()
    // Manual confirmation records a degraded snapshot, so the capability badge
    // stays visible even though the session state is valid (spec §6.3).
    await expect(detail.getByText("能力未验证", { exact: true })).toBeVisible()
    await expect(confirm).toBeDisabled()

    await detail.getByRole("button", { name: "撤销", exact: true }).click()
    await expect(detail.getByRole("status")).toContainText("已撤销会话：状态 revoked。")
    await expect(detail.getByText("revoked", { exact: true })).toBeVisible()
  })

  test("request and complete a manual takeover with a new storage state", async ({
    page,
    browser,
    request,
  }) => {
    const adminPassword = requireAdminPassword()
    const resources = await createResources(request, { tag: "takeover", withLoginCheck: true })
    const storageState = await fixtureStorageState(browser)

    await login(page, ADMIN_USERNAME, adminPassword)
    await page.goto("/resources?tab=sessions")
    await expect(page.getByRole("heading", { name: "会话", exact: true })).toBeVisible()
    const { sessionId } = await importSession(page, resources, storageState)

    const detail = page.getByRole("region", { name: "会话详情" })
    await expect(detail.getByRole("heading", { name: `会话 #${sessionId}` })).toBeVisible()
    await expect(detail.getByText("valid", { exact: true })).toBeVisible()

    // Request: the backend's takeover reason is the closed enum of spec §7.6,
    // so the form is filled with a value of that enum ("other").
    await detail.getByRole("button", { name: "发起接管" }).click()
    const requestDialog = page.getByRole("dialog", { name: "发起人工接管" })
    await expect(requestDialog).toBeVisible()
    await requestDialog.getByLabel("接管原因").selectOption("other")
    await requestDialog.getByLabel("操作指引").fill("E2E 人工接管：上传新的 fixture 状态")
    await requestDialog.getByRole("button", { name: "确认发起" }).click()
    await expect(detail.getByRole("status")).toContainText("已发起接管，等待人工完成。")
    await expect(detail.getByText("needs_manual", { exact: true })).toBeVisible()
    await expect(detail.getByText("waiting", { exact: true })).toBeVisible()
    await expect(detail.getByText("等待人工处理", { exact: true })).toBeVisible()
    await expect(detail.getByRole("button", { name: "完成接管" })).toBeEnabled()
    await expect(detail.getByRole("button", { name: "取消接管" })).toBeEnabled()
    await expect(detail.getByRole("button", { name: "发起接管" })).toBeDisabled()

    // Complete with a freshly exported state plus one marker cookie: the
    // replacement is visible as a larger Cookie count on the refreshed detail.
    const freshState = await fixtureStorageState(browser)
    const replacedState = withMarkerCookie(freshState)
    await detail.getByRole("button", { name: "完成接管" }).click()
    const completeDialog = page.getByRole("dialog", { name: "完成人工接管" })
    await expect(completeDialog).toBeVisible()
    await completeDialog.getByLabel("JSON 内容").fill(JSON.stringify(replacedState))
    await completeDialog.getByRole("button", { name: "上传并完成" }).click()

    await expect(detail.getByRole("status")).toContainText("已完成接管，系统已重新校验会话。")
    await expect(detail.getByText("valid", { exact: true })).toBeVisible()
    await expect(detail.getByText("completed", { exact: true })).toBeVisible()
    await expect(detail.getByText("已完成", { exact: true })).toBeVisible()
    await expect(detail.getByText("允许 1 / 拒绝 0 / 错误 0", { exact: true })).toBeVisible()
    await expect(detail.getByText(/^2 \/ \d+$/)).toBeVisible()
    await expect(detail.getByRole("button", { name: "完成接管" })).toHaveCount(0)
    await expect(detail.getByRole("button", { name: "发起接管" })).toBeEnabled()
    await page.screenshot({
      path: screenshotPath("session-actions-takeover.png"),
      fullPage: true,
    })
  })

  test("imports a token session against an API system login check", async ({ page, request }) => {
    // Spec §9.2 item 6: a token session on an API system is validated by the
    // fixture target's bearer-protected /api/profile login_check.
    const resources = await createResources(request, {
      tag: "token",
      withLoginCheck: true,
      systemKind: "api",
      authType: "token",
      loginCheck: { url: "/api/profile", expectStatus: [200], bodyContains: "fixture" },
    })

    await login(page, ADMIN_USERNAME, requireAdminPassword())
    await page.goto("/resources?tab=sessions")
    await expect(page.getByRole("heading", { name: "会话", exact: true })).toBeVisible()

    const imported = await importSession(
      page,
      resources,
      { kind: "token", headers: { Authorization: "Bearer fixture-token" } },
      { kind: "token", accessAuthType: "token" },
    )
    expect(imported.resultText).toContain("状态 valid。")
    expect(imported.resultText).toContain("能力校验通过。")
    expect(imported.resultText).toContain("快照：允许 1 / 拒绝 0 / 错误 0。")

    const detail = page.getByRole("region", { name: "会话详情" })
    await expect(detail.getByRole("heading", { name: `会话 #${imported.sessionId}` })).toBeVisible()
    await expect(detail.getByText("valid", { exact: true })).toBeVisible()
    await expect(detail.getByText("token", { exact: true })).toBeVisible()
    await expect(detail.getByText("允许 1 / 拒绝 0 / 错误 0", { exact: true })).toBeVisible()
    await expect(detail.getByText("allowed", { exact: true })).toBeVisible()
    await expect(detail.getByText("200", { exact: true })).toBeVisible()
    await expect(detail.getByText("能力未验证", { exact: true })).toHaveCount(0)
    await page.screenshot({
      path: screenshotPath("session-actions-token-import.png"),
      fullPage: true,
    })
  })
})

import { expect, type Page, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  API_URL,
  adminApiToken,
  createCodeRepo,
  login,
  uniqueSuffix,
} from "./p3-helpers"
import {
  getLatestIndexRun,
  p4Fixture,
  skipP4SpecWhenHarnessMissing,
  triggerAndWaitForIndex,
} from "./p4-helpers"

/**
 * Design spec §10.5 item 4 (repository indexing): the admin sees the index
 * state go 未索引 → 排队中/索引中 → 已索引 with the commit and time, while a
 * regular user cannot trigger an index from the UI and gets the backend 403 as
 * the real guard. The P2 configuration actions stay available to the admin.
 */

const USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"

let fixture = { repo: "", baseCommit: "", headCommit: "", branch: "" }
let repoId = 0
let repoName = ""
let normalUsername = ""

test.describe.configure({ mode: "serial" })

test.beforeAll(async ({ request }) => {
  if (await skipP4SpecWhenHarnessMissing()) return

  fixture = p4Fixture()
  const suffix = uniqueSuffix()
  repoName = `P4 索引代码仓 ${suffix}`
  normalUsername = `p4_reader_${suffix}`
  const token = await adminApiToken(request)

  const repo = await createCodeRepo(request, token, {
    key: `p4-index-repo-${suffix}`,
    name: repoName,
    localPath: fixture.repo,
    fixedBranch: fixture.branch,
  })
  repoId = repo.id

  const createdUser = await request.post(`${API_URL}/api/users`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { username: normalUsername, password: USER_PASSWORD },
  })
  expect(createdUser.status()).toBe(201)
})

/** Logs the current user out through the real user menu. */
async function logout(page: Page): Promise<void> {
  await page.getByRole("button", { name: "打开用户菜单" }).click()
  await page.getByRole("menuitem", { name: "退出登录" }).click()
  await expect(page.getByRole("heading", { name: "登录 EzTest" })).toBeVisible({ timeout: 15_000 })
}

test("admin drives the index state and a normal user cannot trigger it", async ({
  page,
  request,
}) => {
  test.setTimeout(240_000)
  const token = await adminApiToken(request)

  // 1. The admin sees the unindexed repository and its P2 configuration actions.
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
  await page.goto("/code-repos")
  const row = page.getByRole("row").filter({ hasText: repoName })
  await expect(row).toBeVisible()
  await expect(row).toContainText("未索引")
  await expect(row).toContainText("尚未建立 T0 索引。")
  await expect(row).toContainText(fixture.branch)
  await expect(row.getByRole("button", { name: "编辑" })).toBeVisible()
  await expect(row.getByRole("button", { name: "重新校验" })).toBeVisible()

  // 2. Triggering the index shows the queued/running state, then done with the
  // indexed commit and time.
  await row.getByRole("button", { name: "刷新索引" }).click()
  await expect(row.getByText(/排队中|索引中/).first()).toBeVisible({ timeout: 15_000 })
  const indexCell = row.locator("td").nth(5)
  await expect(indexCell).toContainText("已索引", { timeout: 60_000 })
  await expect(indexCell).toContainText(fixture.headCommit.slice(0, 8))
  await expect(indexCell).toContainText(/\d{4}\/\d{1,2}\/\d{1,2}/)

  const indexRun = await getLatestIndexRun(request, token, repoId)
  expect(indexRun.status).toBe("done")
  expect(indexRun.commit).toBe(fixture.headCommit)
  expect(indexRun.startedAt).not.toBeNull()
  expect(indexRun.finishedAt).not.toBeNull()
  expect(indexRun.stats?.files).toBeGreaterThan(0)
  // Repeat-trigger path used by the admin button returns a fresh queued run.
  const repeated = await triggerAndWaitForIndex(request, token, repoId)
  expect(repeated.accepted.status).toBe("queued")
  expect(repeated.run.status).toBe("done")
  expect(repeated.statuses[0]).toBe("queued")
  expect(repeated.statuses[repeated.statuses.length - 1]).toBe("done")

  // 3. A regular user only reads: no write affordances, and the backend rejects
  // the index trigger with 403.
  await logout(page)
  await login(page, normalUsername, USER_PASSWORD)
  await page.goto("/code-repos")
  await expect(page.getByRole("heading", { name: "代码仓", exact: true })).toBeVisible()
  await expect(page.getByText("只读", { exact: true })).toBeVisible()
  const readonlyRow = page.getByRole("row").filter({ hasText: repoName })
  await expect(readonlyRow).toBeVisible()
  await expect(readonlyRow).toContainText("已索引")
  await expect(page.getByRole("button", { name: "新建代码仓" })).toHaveCount(0)
  await expect(page.getByRole("button", { name: "刷新索引" })).toHaveCount(0)
  await expect(readonlyRow.getByRole("button", { name: "编辑" })).toHaveCount(0)

  const statuses = await page.evaluate(
    async ({ apiUrl, id }) => {
      const token = sessionStorage.getItem("eztest.token") ?? ""
      const headers = { Authorization: `Bearer ${token}` }
      const list = await fetch(`${apiUrl}/api/code/repos`, { headers })
      const index = await fetch(`${apiUrl}/api/code/repos/${id}/index`, {
        method: "POST",
        headers,
      })
      return { list: list.status, index: index.status }
    },
    { apiUrl: API_URL, id: repoId },
  )
  expect(statuses.list).toBe(200)
  expect(statuses.index).toBe(403)
})

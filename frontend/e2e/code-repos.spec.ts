import { execFileSync } from "node:child_process"
import { mkdirSync, rmSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

import { expect, type Page, test } from "@playwright/test"

const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""

/** P2 acceptance screenshots live in the gitignored repository `.tmp` (spec §9.2). */
const P2_ACCEPTANCE_DIR = fileURLToPath(new URL("../../.tmp/p2-acceptance", import.meta.url))
const E2E_REPO_DIR = fileURLToPath(new URL("../../.tmp/e2e-repo", import.meta.url))

let repoBranch = ""
let repoCommit = ""

function requireAdminPassword(): string {
  if (!ADMIN_PASSWORD) {
    throw new Error("EZTEST_E2E_ADMIN_PASSWORD must be set; run via scripts/e2e.ps1")
  }
  return ADMIN_PASSWORD
}

/** Runs one git command against the throwaway E2E repository. */
function git(...args: string[]): string {
  return execFileSync("git", ["-C", E2E_REPO_DIR, ...args], { encoding: "utf8" }).trim()
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

// Spec §9.2 step 7: the E2E script prepares a real throwaway Git repository
// (init + commit) so the UI create/revalidate path talks to real Git.
test.beforeAll(() => {
  rmSync(E2E_REPO_DIR, { recursive: true, force: true })
  mkdirSync(E2E_REPO_DIR, { recursive: true })
  writeFileSync(join(E2E_REPO_DIR, "README.md"), "# EzTest E2E repository\n")
  writeFileSync(join(E2E_REPO_DIR, "app.ts"), "export const name = 'e2e-repo'\n")

  git("init")
  // Use the repository's actual default branch instead of assuming main/master.
  repoBranch = git("symbolic-ref", "--short", "HEAD")
  expect(repoBranch).toMatch(/^(main|master)$/)

  // The host may set core.autocrlf=true; keeping it off for the throwaway repo
  // avoids Git's LF→CRLF warning on stderr while the UI assertions only read Git.
  git("-c", "core.autocrlf=false", "add", "-A")
  git(
    "-c",
    "user.name=EzTest E2E",
    "-c",
    "user.email=e2e@eztest.invalid",
    "commit",
    "-m",
    "seed e2e repository",
  )
  repoCommit = git("rev-parse", "HEAD")
  expect(repoCommit).toMatch(/^[0-9a-f]{40}$/)
})

test("admin creates a code repository, revalidates it, and sees a 422 for a missing path", async ({
  page,
}) => {
  const adminPassword = requireAdminPassword()
  const suffix = uniqueSuffix()
  const repoKey = `e2e-repo-${suffix}`
  const repoName = `E2E 代码仓 ${suffix}`
  // The API validates the local path on the same host; forward slashes keep the
  // UI input and the assertions independent of Windows escaping.
  const repoPath = E2E_REPO_DIR.replaceAll("\\", "/")
  const missingPath = `${repoPath}-missing`

  await login(page, ADMIN_USERNAME, adminPassword)
  await page.goto("/code-repos")
  await expect(page.getByRole("heading", { name: "代码仓", exact: true })).toBeVisible()
  await expect(page.getByText("只读", { exact: true })).toHaveCount(0)

  await page.getByRole("button", { name: "新建代码仓" }).click()
  await expect(page.getByRole("heading", { name: "新建代码仓", exact: true })).toBeVisible()
  await page.getByLabel("标识", { exact: true }).fill(repoKey)
  await page.getByLabel("名称", { exact: true }).fill(repoName)
  await page.getByLabel("本地路径").fill(repoPath)
  await page.getByLabel("固定分支").fill(repoBranch)
  await page.getByRole("button", { name: "保存", exact: true }).click()
  await expect(
    page.getByRole("status").filter({ hasText: `已创建代码仓 ${repoName}。` }),
  ).toBeVisible()

  // The stored commit is the real 40-char HEAD of the throwaway repository and
  // the index column stays a P4 placeholder.
  const row = page.getByRole("row").filter({ hasText: repoName })
  await expect(row).toBeVisible()
  await expect(row.getByText(repoCommit, { exact: true })).toBeVisible()
  await expect(row).toContainText("未索引（P4）")
  await expect(row).toContainText(repoPath)
  await expect(row).toContainText(repoBranch)
  await expect(row).toContainText("启用")

  // Revalidation re-reads the same HEAD through real Git.
  await row.getByRole("button", { name: "重新校验" }).click()
  const validateNotice = page.getByRole("status").filter({ hasText: `已重新校验 ${repoName}` })
  await expect(validateNotice).toContainText(repoCommit)
  await expect(row.getByText(repoCommit, { exact: true })).toBeVisible()
  await page.screenshot({ path: screenshotPath("code-repos-created.png"), fullPage: true })

  // Editing to a directory that does not exist is rejected by the real Git
  // validation with the 422 `path_not_found` reason (spec §6.7/§8.2).
  await row.getByRole("button", { name: "编辑", exact: true }).click()
  await expect(page.getByRole("heading", { name: `编辑代码仓：${repoName}` })).toBeVisible()
  await page.locator("#code-repo-path").fill(missingPath)
  await page.getByRole("button", { name: "保存", exact: true }).click()
  const alert = page.getByRole("alert").filter({ hasText: "本地路径不存在（path_not_found）" })
  await expect(alert).toBeVisible()
  // The failed update persists nothing: the form stays open on the invalid
  // value while the stored row keeps its original path and commit.
  await expect(page.getByRole("heading", { name: `编辑代码仓：${repoName}` })).toBeVisible()
  await expect(page.getByRole("row").filter({ hasText: repoName })).toContainText(repoPath)
  await expect(page.getByRole("row").filter({ hasText: repoName })).toContainText(repoCommit)
  await page.screenshot({ path: screenshotPath("code-repos-invalid-path.png"), fullPage: true })

  await page.getByRole("button", { name: "取消", exact: true }).click()
  await expect(page.getByRole("heading", { name: `编辑代码仓：${repoName}` })).toHaveCount(0)
})

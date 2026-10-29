#!/usr/bin/env node
// Throwaway Playwright spike for the P6 risk probe. It is not product code and
// must never be imported by the agent runtime.
//
// It verifies:
//   1. Playwright can drive the real EzTest login form with an installed Chrome.
//   2. context.storageState() can be saved and injected into a fresh context.
//   3. Whether the injected state alone restores the EzTest session.
//
// Playwright storageState covers cookies and localStorage, not sessionStorage.
// EzTest keeps its bearer token in sessionStorage, so the script records the
// storageState-only outcome, then re-seeds the captured sessionStorage as the
// complement P6 would need. Credentials and tokens are never printed.
//
// The brief names "../.tmp/..." relative to frontend/; resolving from the
// script location targets the same repo .tmp directory without depending on
// the caller's working directory.

import { mkdirSync, statSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

import { chromium } from "@playwright/test"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(scriptDir, "..", "..")
const tmpDir = path.join(repoRoot, ".tmp")
const storageStatePath = path.join(tmpDir, "spike-storage-state.json")
const screenshotPath = path.join(tmpDir, "spike-shell.png")

const baseURL = (process.env.EZTEST_E2E_BASE_URL?.trim() || "http://localhost:6868").replace(
  /\/+$/,
  "",
)
const channel = process.env.EZTEST_E2E_CHANNEL?.trim() || "chrome"
const username = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() ?? ""
const password = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""

const startedAt = Date.now()
const timings = {}
const pages = []
let agentsResult = "not-run"
let storageStateOnlyAuthenticated = false
let sessionStorageSeeded = false
let sessionStorageKeyCount = 0
let storageStateLocalStorageKeyCount = 0
let screenshotBytes = 0

function log(message) {
  console.log(`SPIKE-PW ${message}`)
}

function warn(message) {
  console.warn(`SPIKE-PW ${message}`)
}

function displayPath(filePath) {
  return path.relative(repoRoot, filePath).split(path.sep).join("/")
}

/** Removes credential substrings from any error text before it is printed. */
function redact(text) {
  let result = text
  if (username.length > 0) result = result.split(username).join("[redacted]")
  if (password.length > 0) result = result.split(password).join("[redacted]")
  return result
}

async function step(name, action) {
  const stepStartedAt = Date.now()
  try {
    return await action()
  } finally {
    timings[name] = Date.now() - stepStartedAt
    log(`step=${name} ms=${timings[name]}`)
  }
}

function requireCredentials() {
  if (username.length === 0 || password.length === 0) {
    throw new Error(
      "EZTEST_E2E_ADMIN_USERNAME and EZTEST_E2E_ADMIN_PASSWORD must both be set " +
        "(import them from .env; the script never prints them)",
    )
  }
}

/** Reads the current page's sessionStorage without logging its values. */
function readSessionStorage(page) {
  return page.evaluate(() => {
    const entries = []
    for (let index = 0; index < sessionStorage.length; index += 1) {
      const key = sessionStorage.key(index)
      if (key !== null) entries.push([key, sessionStorage.getItem(key) ?? ""])
    }
    return entries
  })
}

async function main() {
  requireCredentials()
  mkdirSync(tmpDir, { recursive: true })

  log(`start base_url=${baseURL} channel=${channel} headless=true`)

  let browser = null
  try {
    browser = await step("launch", () => chromium.launch({ channel, headless: true }))

    // 1. Real login through the UI, then snapshot the authenticated state.
    let sessionStorageEntries = []
    const loginContext = await browser.newContext()
    try {
      const loginPage = await loginContext.newPage()
      await step("login", async () => {
        await loginPage.goto(`${baseURL}/login`)
        await loginPage.getByLabel("用户名").fill(username)
        await loginPage.getByLabel("密码").fill(password)
        await loginPage.getByRole("button", { name: "登录", exact: true }).click()
        await loginPage
          .getByRole("heading", { name: "概览" })
          .waitFor({ state: "visible", timeout: 30_000 })
      })

      sessionStorageEntries = await step("capture-session-storage", () =>
        readSessionStorage(loginPage),
      )
      sessionStorageKeyCount = sessionStorageEntries.length

      const state = await step("save-storage-state", async () => {
        const saved = await loginContext.storageState({ path: storageStatePath })
        if (statSync(storageStatePath).size === 0) {
          throw new Error(`storageState file is empty: ${storageStatePath}`)
        }
        return saved
      })
      storageStateLocalStorageKeyCount = state.origins.reduce(
        (total, origin) =>
          total + (Array.isArray(origin.localStorage) ? origin.localStorage.length : 0),
        0,
      )
      log(
        `storage-state path=${displayPath(storageStatePath)} ` +
          `bytes=${statSync(storageStatePath).size} cookies=${state.cookies.length} ` +
          `origins=${state.origins.length} local_storage_keys=${storageStateLocalStorageKeyCount}`,
      )
    } finally {
      await loginContext.close()
    }

    // 2. storageState-only injection: record whether it is sufficient on its own.
    const storageStateOnlyContext = await browser.newContext({ storageState: storageStatePath })
    try {
      const storageStateOnlyPage = await storageStateOnlyContext.newPage()
      await step("storage-state-only-visit", async () => {
        await storageStateOnlyPage.goto(`${baseURL}/`)
        const menu = storageStateOnlyPage.getByRole("button", { name: "打开用户菜单" })
        storageStateOnlyAuthenticated = await menu
          .waitFor({ state: "visible", timeout: 6_000 })
          .then(() => true)
          .catch(() => false)
        const tokenRestored = await storageStateOnlyPage
          .evaluate(() => sessionStorage.getItem("eztest.token") !== null)
          .catch(() => false)
        const finalPath = new URL(storageStateOnlyPage.url()).pathname
        log(
          `storage-state-only authenticated=${storageStateOnlyAuthenticated} ` +
            `final_path=${finalPath} session_token_restored=${tokenRestored}`,
        )
      })
    } finally {
      await storageStateOnlyContext.close()
    }

    // 3. storageState + sessionStorage re-seed: the viable P6 session handoff.
    const injectedContext = await browser.newContext({ storageState: storageStatePath })
    try {
      await injectedContext.addInitScript((entries) => {
        try {
          for (const [key, value] of entries) window.sessionStorage.setItem(key, value)
        } catch {
          // The initial about:blank document can have an opaque origin; the
          // init script runs again for the real navigation.
        }
      }, sessionStorageEntries)
      sessionStorageSeeded = sessionStorageEntries.length > 0

      const page = await injectedContext.newPage()
      await step("injected-shell", async () => {
        await page.goto(`${baseURL}/`)
        await page.getByRole("heading", { name: "概览" }).waitFor({
          state: "visible",
          timeout: 30_000,
        })
        await page.getByRole("button", { name: "打开用户菜单" }).waitFor({
          state: "visible",
          timeout: 30_000,
        })
      })
      pages.push("/")

      await step("screenshot", async () => {
        await page.screenshot({ path: screenshotPath, fullPage: true })
        screenshotBytes = statSync(screenshotPath).size
        if (screenshotBytes === 0) throw new Error(`screenshot is empty: ${screenshotPath}`)
      })

      await step("agents-page", async () => {
        const response = await page.goto(`${baseURL}/agents`)
        const httpStatus = response?.status() ?? 0
        const menu = page.getByRole("button", { name: "打开用户菜单" })
        const rendered = await menu
          .waitFor({ state: "visible", timeout: 30_000 })
          .then(() => true)
          .catch(() => false)
        if (rendered) {
          agentsResult = "verified"
          pages.push("/agents")
          log(`agents verified http_status=${httpStatus}`)
          return
        }

        const finalPath = new URL(page.url()).pathname
        const bodyText = await page
          .locator("body")
          .textContent({ timeout: 5_000 })
          .catch(() => "")
        const compileFailure =
          /Failed to compile|Module not found|Build Error|Unhandled Runtime Error/.test(bodyText)
        if (compileFailure || httpStatus >= 500) {
          agentsResult = "skipped-compile-error"
          warn(`agents skipped=compile-error http_status=${httpStatus} final_path=${finalPath}`)
          return
        }

        agentsResult = "failed"
        throw new Error(
          `/agents did not render the authenticated shell ` +
            `(http_status=${httpStatus}, final_path=${finalPath})`,
        )
      })
    } finally {
      await injectedContext.close()
    }

    const totalMs = Date.now() - startedAt
    log(
      `summary steps=${JSON.stringify(timings)} session_storage_keys=${sessionStorageKeyCount} ` +
        `storage_state_local_storage_keys=${storageStateLocalStorageKeyCount}`,
    )
    log(
      `OK total_ms=${totalMs} storage_state=${displayPath(storageStatePath)} ` +
        `screenshot=${displayPath(screenshotPath)} screenshot_bytes=${screenshotBytes} ` +
        `storage_state_only_authenticated=${storageStateOnlyAuthenticated} ` +
        `session_storage_seeded=${sessionStorageSeeded} pages=${pages.join("|")} ` +
        `agents=${agentsResult}`,
    )
  } finally {
    if (browser) {
      try {
        await browser.close()
      } catch {
        // Best-effort cleanup; the process exits either way.
      }
    }
  }
}

main().catch((error) => {
  const message = error instanceof Error ? error.message : String(error)
  log(
    `FAIL total_ms=${Date.now() - startedAt} error=${JSON.stringify(redact(message).slice(0, 2000))}`,
  )
  process.exitCode = 1
})

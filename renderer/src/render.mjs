/**
 * Headless PDF rendering for the report renderer service (P7 D59).
 *
 * One Chromium instance is shared by all requests; every request gets its own
 * browser context (and is therefore isolated from every other render). The
 * browser is launched lazily and resolved with the same fallback chain as the
 * P3 executor (`executor/src/contexts.mjs`): the bundled Playwright Chromium
 * first, then a locally cached Chromium build, then the installed Chrome
 * channel and finally the installed Edge channel. The official renderer image
 * ships the pinned Chromium, so the fallbacks only matter on dev machines.
 */

import fs from "node:fs"
import os from "node:os"
import path from "node:path"

import { chromium } from "playwright"

// DEFAULT_CHANNEL is the system browser fallback used when the bundled
// Playwright Chromium is not installed (EZTEST_RENDER_CHANNEL overrides it).
export const DEFAULT_CHANNEL = "chrome"
// DEFAULT_TIMEOUT_SECONDS is the per-request render deadline
// (EZTEST_RENDER_TIMEOUT_SECONDS overrides it).
export const DEFAULT_TIMEOUT_SECONDS = 60
// READINESS_TTL_MS caches a failed browser probe so /healthz does not retry a
// failing launch on every call, while still re-probing a recovered browser.
const READINESS_TTL_MS = 10_000

/** Looks for a locally cached Chromium when the pinned revision is absent. */
function resolveCachedChromium() {
  if (process.env.EZTEST_RENDER_CHROMIUM_PATH) {
    return process.env.EZTEST_RENDER_CHROMIUM_PATH
  }
  const roots = [
    process.env.PLAYWRIGHT_BROWSERS_PATH,
    path.join(os.homedir(), "AppData", "Local", "ms-playwright"),
    path.join(os.homedir(), "Library", "Caches", "ms-playwright"),
    path.join(os.homedir(), ".cache", "ms-playwright"),
  ].filter((root) => typeof root === "string" && root.length > 0)
  const candidates = [
    path.join("chrome-win64", "chrome.exe"),
    path.join("chrome-linux", "chrome"),
    path.join("chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium"),
  ]
  for (const root of roots) {
    let entries
    try {
      entries = fs.readdirSync(root, { withFileTypes: true })
    } catch {
      continue
    }
    const revisions = entries
      .filter((entry) => entry.isDirectory() && /^chromium-\d+$/.test(entry.name))
      .map((entry) => entry.name)
      .sort()
      .reverse()
    for (const revision of revisions) {
      for (const suffix of candidates) {
        const executable = path.join(root, revision, suffix)
        if (fs.existsSync(executable)) {
          return executable
        }
      }
    }
  }
  return null
}

function isMissingBrowser(error) {
  const message = error instanceof Error ? error.message : String(error)
  return /Executable doesn't exist|executable doesn't exist|is not found at/i.test(message)
}

/**
 * browserLaunchAttempts returns the ordered launch options: bundled Chromium,
 * an optional cached Chromium path, then the configured channel and msedge.
 */
export function browserLaunchAttempts(channel = DEFAULT_CHANNEL) {
  const attempts = [{ headless: true }]
  const cachedPath = resolveCachedChromium()
  if (cachedPath) {
    attempts.push({ headless: true, executablePath: cachedPath })
  }
  const channels = []
  if (typeof channel === "string" && channel.length > 0) {
    channels.push(channel)
  }
  if (!channels.includes("msedge")) {
    channels.push("msedge")
  }
  for (const value of channels) {
    attempts.push({ headless: true, channel: value })
  }
  return attempts
}

/**
 * launchBrowser tries every fallback in order. It only falls back when the
 * failure means "that browser is not installed"; any other launch error is
 * surfaced so a broken installation is visible instead of silently retried.
 */
export async function launchBrowser({
  channel = DEFAULT_CHANNEL,
  launch = (options) => chromium.launch(options),
} = {}) {
  const attempts = browserLaunchAttempts(channel)
  let lastError
  for (const options of attempts) {
    try {
      return await launch(options)
    } catch (error) {
      lastError = error
      if (!isMissingBrowser(error)) {
        throw error
      }
    }
  }
  throw lastError
}

function timeoutError() {
  const error = new Error("PDF rendering exceeded its timeout")
  error.code = "render_timeout"
  return error
}

/** Races a promise against an AbortSignal; an abort rejects with render_timeout. */
function abortable(promise, signal) {
  if (!signal) {
    return promise
  }
  if (signal.aborted) {
    return Promise.reject(timeoutError())
  }
  return new Promise((resolve, reject) => {
    const onAbort = () => reject(timeoutError())
    signal.addEventListener("abort", onAbort, { once: true })
    promise.then(
      (value) => {
        signal.removeEventListener("abort", onAbort)
        resolve(value)
      },
      (error) => {
        signal.removeEventListener("abort", onAbort)
        reject(error)
      },
    )
  })
}

/**
 * PDFRenderer owns the shared browser. It implements the renderer surface of
 * the HTTP layer: render(html, {signal}) -> PDF bytes, health() and close().
 */
export class PDFRenderer {
  #browser = null
  #browserPromise = null
  #launch
  #logger
  #closed = false
  #readiness = null

  constructor({ launch, channel = DEFAULT_CHANNEL, logger = null } = {}) {
    this.#launch =
      typeof launch === "function" ? launch : () => launchBrowser({ channel })
    this.#logger = logger
  }

  async #getBrowser() {
    if (this.#closed) {
      throw new Error("renderer is shutting down")
    }
    if (this.#browser) {
      if (this.#browser.isConnected?.() === false) {
        this.#browser = null
        this.#browserPromise = null
      } else {
        return this.#browser
      }
    }
    if (!this.#browserPromise) {
      this.#browserPromise = this.#launch().then(
        (browser) => {
          this.#browser = browser
          return browser
        },
        (error) => {
          this.#browserPromise = null
          throw error
        },
      )
    }
    return this.#browserPromise
  }

  async browserVersion() {
    const browser = await this.#getBrowser()
    return browser.version()
  }

  /**
   * health is the /healthz probe: a browser that cannot launch makes the
   * service degraded (503). The result is cached briefly so repeated probes do
   * not hammer a failing launch; a recovered browser is re-probed.
   */
  async health() {
    if (this.#closed) {
      return { status: "degraded", error: "renderer is shutting down" }
    }
    if (this.#readiness && Date.now() - this.#readiness.at < READINESS_TTL_MS) {
      const cached = this.#readiness.result
      const browserUsable = this.#browser !== null && this.#browser.isConnected?.() === true
      if (cached.status !== "ok" || browserUsable) {
        return { ...cached }
      }
    }
    try {
      const browser = await this.#getBrowser()
      if (browser.isConnected?.() === false) {
        throw new Error("browser is not connected")
      }
      const result = { status: "ok", browserVersion: browser.version() }
      this.#readiness = { at: Date.now(), result }
      return { ...result }
    } catch (error) {
      const result = {
        status: "degraded",
        error: error instanceof Error ? error.message : String(error),
      }
      this.#readiness = { at: Date.now(), result }
      return { ...result }
    }
  }

  /**
   * render prints one standalone HTML document to an A4 PDF. Every call uses
   * its own browser context; the context is always closed, including on error
   * or abort. An aborted request (the HTTP deadline) closes the context as soon
   * as the signal fires so a wedged render cannot keep a slot forever.
   */
  async render(html, { signal } = {}) {
    if (signal?.aborted) {
      throw timeoutError()
    }
    const browser = await this.#getBrowser()
    if (signal?.aborted) {
      throw timeoutError()
    }
    const context = await browser.newContext()
    let aborted = false
    let contextClosed = false
    const closeContext = async () => {
      if (contextClosed) {
        return
      }
      contextClosed = true
      await context.close().catch(() => {})
    }
    const onAbort = () => {
      aborted = true
      void closeContext()
    }
    signal?.addEventListener("abort", onAbort, { once: true })
    try {
      const work = (async () => {
        const page = await context.newPage()
        if (aborted) {
          throw timeoutError()
        }
        await page.setContent(html, { waitUntil: "load" })
        if (aborted) {
          throw timeoutError()
        }
        const pdf = await page.pdf({ format: "A4", printBackground: true })
        if (aborted) {
          throw timeoutError()
        }
        return pdf
      })()
      return await abortable(work, signal)
    } finally {
      signal?.removeEventListener("abort", onAbort)
      await closeContext()
    }
  }

  async close() {
    if (this.#closed) {
      return
    }
    this.#closed = true
    if (this.#browser) {
      await this.#browser.close().catch(() => {})
      this.#browser = null
      this.#browserPromise = null
    }
    this.#logger?.info?.("[renderer] browser closed")
  }
}

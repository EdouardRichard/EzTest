/**
 * Explorer action family (0.2.1 spec §11, executor protocol v1.3).
 *
 * The explorer performs read-mostly page exploration through a deliberately
 * small action set: open / snapshot / click / fill / extract / screenshot.
 * Nothing here can start a shell or run an arbitrary command: the action name
 * set is closed and every input is a page/selector/index value.
 *
 * Each `page_id` owns an isolated Playwright browser context, so the context's
 * allow list, cookie jar and console/network rings stay scoped to that page.
 * Reusing the same `page_id` keeps the browser session (cookies, storage) while
 * navigating; `open` without `page_id` creates a fresh isolated page.
 *
 * Failures are wrapped in the protocol envelope; `fill(secret=true)` values are
 * registered before the interaction and scrubbed from every returned string
 * (console, network, DOM snapshot, extract and error messages).
 */

import crypto from "node:crypto"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"

import { chromium } from "playwright"

import { withAbort } from "../actions.mjs"
import { isWebSocketAllowed, parseAllowedHosts } from "../limits.mjs"
import {
  DEFAULT_ACTION_TIMEOUT_MS,
  ExecutorError,
  GOTO_TIMEOUT_MS,
  MAX_SNAPSHOT_CHARS,
  ensureArtifactSize,
  hostFromUrl,
  invalidArguments,
  isAllowedHost,
  isAllowedUrl,
  normalizeAllowedHosts,
  pushRing,
  registerSecret,
  resolveTimeout,
} from "../security.mjs"
import { captureSnapshot, stripSecretValues } from "../snapshot.mjs"

/** Closed action set; anything else is rejected as invalid_arguments (400). */
export const EXPLORE_ACTIONS = Object.freeze([
  "explore.open",
  "explore.snapshot",
  "explore.click",
  "explore.fill",
  "explore.extract",
  "explore.screenshot",
])

const EXPLORE_ACTION_SET = new Set(EXPLORE_ACTIONS)
const MAX_EXPLORE_PAGES = 8
const MAX_ELEMENTS = 200
const MAX_OUTPUT_ITEMS = 50
const MAX_TEXT_CHARS = 200
// Small settle window so console/network events emitted by the interaction are
// queued before the result envelope is built.
const SETTLE_MS = 25
const HARD_DEADLINE_SLACK_MS = 250
const DEFAULT_EXPLORE_HOSTS = Object.freeze(["127.0.0.1", "localhost", "::1"])
const WAIT_UNTIL = new Set(["load", "domcontentloaded", "networkidle", "commit"])

function requireString(value, name, { allowEmpty = false } = {}) {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0)) {
    throw invalidArguments(`${name} must be a ${allowEmpty ? "" : "non-empty "}string`)
  }
  return value
}

function parseIndex(value) {
  const numeric = Number(value)
  if (!Number.isInteger(numeric) || numeric < 0) {
    throw invalidArguments("index must be a non-negative integer")
  }
  return numeric
}

function parseAllowedHostsArg(input) {
  const hosts = normalizeAllowedHosts(input)
  if (hosts.length === 0) {
    throw invalidArguments("allowed_hosts must contain at least one host")
  }
  return hosts
}

function normalizeViewport(input) {
  if (input === undefined) {
    return undefined
  }
  if (!input || typeof input !== "object" || Array.isArray(input)) {
    throw invalidArguments("viewport must be an object with width and height")
  }
  const width = Number(input.width)
  const height = Number(input.height)
  if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0) {
    throw invalidArguments("viewport width and height must be positive integers")
  }
  return { width, height }
}

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

function artifact(kind, mime, buffer, extra = {}) {
  return {
    kind,
    mime,
    base64: buffer.toString("base64"),
    sha256: sha256(buffer),
    bytes: buffer.length,
    ...extra,
  }
}

function scrubText(text, entry) {
  return stripSecretValues(text, [...(entry?.secrets ?? [])])
}

/**
 * Recursively scrubs secret values out of a JSON-serializable result. Every
 * explore result passes through this before it leaves the executor, so a secret
 * typed into the page can never appear in a console/network/DOM output.
 */
function scrubValue(value, secrets) {
  if (typeof value === "string") {
    return stripSecretValues(value, secrets)
  }
  if (Array.isArray(value)) {
    return value.map((item) => scrubValue(item, secrets))
  }
  if (value && typeof value === "object") {
    const output = {}
    for (const [key, item] of Object.entries(value)) {
      output[key] = scrubValue(item, secrets)
    }
    return output
  }
  return value
}

/** Path + query of the current page, used as the exploration `route`. */
function routeOf(page) {
  try {
    const parsed = new URL(page.url())
    return `${parsed.pathname}${parsed.search}`
  } catch {
    return ""
  }
}

function classifyUrl(entry, rawUrl) {
  try {
    const parsed = new URL(String(rawUrl))
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return { allowed: false, blocked: { url: rawUrl, scheme: parsed.protocol } }
    }
    const host = parsed.hostname
    if (isAllowedHost(host, entry.allowedHosts)) {
      return { allowed: true }
    }
    return { allowed: false, blocked: { url: rawUrl, host } }
  } catch {
    return { allowed: false, blocked: { url: rawUrl } }
  }
}

function envelopeError(error) {
  if (error instanceof ExecutorError) {
    return { ok: false, error: { code: error.code, message: error.message } }
  }
  return {
    ok: false,
    error: {
      code: "internal",
      message: error instanceof Error ? error.message : String(error),
    },
  }
}

/**
 * Maps Playwright/runtime errors onto the protocol error codes. A browser-side
 * timeout (`Timeout 50ms exceeded`) is a normal action failure (`timeout`); a
 * hard deadline enforced by withAbort keeps `executor_timeout` so the caller can
 * distinguish a wedged browser operation.
 */
function mapExploreError(error, entry) {
  if (error instanceof ExecutorError) {
    error.message = scrubText(error.message, entry)
    return error
  }
  const message = scrubText(error instanceof Error ? error.message : String(error), entry)
  let code = "internal"
  if (error?.name === "TimeoutError" || /Timeout .* exceeded|timed out/i.test(message)) {
    code = "timeout"
  } else if (/ERR_NAME_NOT_RESOLVED|ENOTFOUND|EAI_AGAIN/i.test(message)) {
    code = "dns_failed"
  } else if (
    /ERR_CONNECTION_(REFUSED|RESET|CLOSED|ABORTED|FAILED)|ERR_UNSAFE_PORT|ECONNREFUSED|ECONNRESET/i.test(
      message,
    )
  ) {
    code = "connection_failed"
  }
  return new ExecutorError(code, message)
}

function settle() {
  return new Promise((resolve) => setTimeout(resolve, SETTLE_MS))
}

/** Resolves a cached Playwright Chromium when the pinned revision is absent. */
function resolveCachedChromium() {
  if (process.env.EZTEST_EXECUTOR_CHROMIUM_PATH) {
    return process.env.EZTEST_EXECUTOR_CHROMIUM_PATH
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

/** Launches the exploration browser, falling back to cached/system Chrome. */
async function launchExploreBrowser() {
  const attempts = [{ headless: true }]
  const cachedPath = resolveCachedChromium()
  if (cachedPath) {
    attempts.push({ headless: true, executablePath: cachedPath })
  }
  attempts.push({ headless: true, channel: "chrome" }, { headless: true, channel: "msedge" })
  let lastError
  for (const options of attempts) {
    try {
      return await chromium.launch(options)
    } catch (error) {
      lastError = error
      if (!isMissingBrowser(error)) {
        throw error
      }
    }
  }
  throw lastError
}

/** Blocks page-created workers, which would bypass the request/WS guards. */
function blockWorkers() {
  const block = (name) => {
    const blocked = function () {
      throw new DOMException(
        `${name} creation is blocked by the EzTest executor (worker_blocked)`,
        "NotAllowedError",
      )
    }
    Object.defineProperty(blocked, "name", { value: name, configurable: true })
    try {
      Object.defineProperty(globalThis, name, {
        value: blocked,
        configurable: false,
        enumerable: true,
        writable: false,
      })
    } catch {
      try {
        globalThis[name] = blocked
      } catch {
        // A frozen global leaves no usable constructor either.
      }
    }
  }
  block("Worker")
  block("SharedWorker")
}

async function installRouteGuard(entry) {
  await entry.context.route("**/*", (route) => {
    const request = route.request()
    const verdict = classifyUrl(entry, request.url())
    if (!verdict.allowed) {
      entry.blocked = verdict.blocked
      return route.abort("blockedbyclient").catch(() => {})
    }
    return route.continue().catch(() => {})
  })
}

async function installWebSocketGuard(entry, executorAllowedHosts) {
  if (typeof entry.context.routeWebSocket !== "function") {
    throw new ExecutorError(
      "internal",
      "the explore browser context cannot intercept WebSocket connections",
    )
  }
  await entry.context.routeWebSocket(/.*/, (socket) => {
    const url = typeof socket.url === "function" ? socket.url() : ""
    if (isWebSocketAllowed(url, entry.allowedHosts, executorAllowedHosts)) {
      socket.connectToServer()
      return
    }
    const host = hostFromUrl(url)
    entry.blocked = { url, host }
    try {
      socket.close({ code: 1008, reason: "host_not_allowed" })
    } catch {
      socket.close()
    }
  })
}

/**
 * The per-page CDP Fetch guard also sees redirect hops, which Playwright's
 * context route does not observe in Chromium. It is the authoritative allow
 * list check for navigation and subresources of one explore page.
 */
async function installCdpGuard(entry, page) {
  const session = await entry.context.newCDPSession(page)
  entry.cdpSession = session
  await session.send("Fetch.enable", {
    patterns: [{ urlPattern: "*", requestStage: "Request" }],
  })
  session.on("Fetch.requestPaused", (event) => {
    const verdict = classifyUrl(entry, event.request.url)
    if (!verdict.allowed) {
      entry.blocked = verdict.blocked
    }
    const command = verdict.allowed ? "Fetch.continueRequest" : "Fetch.failRequest"
    const params = verdict.allowed
      ? { requestId: event.requestId }
      : { requestId: event.requestId, errorReason: "BlockedByClient" }
    session.send(command, params).catch(() => {})
  })
}

function attachTelemetry(entry) {
  const page = entry.page
  page.on("console", (message) => {
    pushRing(entry.consoleItems, {
      type: message.type(),
      text: scrubText(message.text(), entry),
      ts: Date.now(),
    })
  })
  page.on("request", (request) => {
    entry.pendingRequests.set(request, {
      method: request.method(),
      url: scrubText(request.url(), entry),
      start: Date.now(),
    })
  })
  const record = async (request, failed, failureText) => {
    const pending = entry.pendingRequests.get(request)
    entry.pendingRequests.delete(request)
    let status = null
    let mime = null
    try {
      const response = await request.response()
      if (response) {
        status = response.status()
        const contentType = response.headers()["content-type"]
        mime = contentType === undefined ? null : contentType
      }
    } catch {
      // The response may already be gone after a failed request.
    }
    pushRing(entry.networkItems, {
      method: pending?.method ?? request.method(),
      url: scrubText(request.url(), entry),
      status,
      duration_ms: Date.now() - (pending?.start ?? Date.now()),
      mime,
      ...(failed ? { error: scrubText(failureText ?? "", entry) } : {}),
    })
  }
  page.on("requestfinished", (request) => {
    void record(request, false, null)
  })
  page.on("requestfailed", (request) => {
    void record(request, true, request.failure()?.errorText ?? null)
  })
}

/** Guarantees the main navigation is visible in `requests` even if events lag. */
function ensureNavigationRecorded(entry, response) {
  if (!response) {
    return
  }
  let url
  try {
    url = scrubText(entry.page.url(), entry)
  } catch {
    return
  }
  const status = response.status()
  if (entry.networkItems.some((item) => item.url === url && item.status === status)) {
    return
  }
  let mime = null
  try {
    const contentType = response.headers()["content-type"]
    mime = contentType === undefined ? null : contentType
  } catch {
    mime = null
  }
  let method = "GET"
  try {
    method = response.request().method()
  } catch {
    method = "GET"
  }
  pushRing(entry.networkItems, { method, url, status, duration_ms: 0, mime })
}

/**
 * Returns interactive elements of the page (or of `selector`). Runs inside the
 * page: it only reads DOM attributes/text, never executes page-provided code.
 */
async function readElements(page, selector = null) {
  return page.evaluate(
    ({ selector, max, maxText }) => {
      const INTERACTIVE =
        'a[href], button, input, select, textarea, [role="button"], [role="link"], ' +
        '[role="tab"], [role="checkbox"], [role="radio"], [role="switch"], ' +
        '[role="textbox"], [contenteditable="true"], [onclick], [data-testid]'
      const nodes = (
        selector
          ? Array.from(document.querySelectorAll(selector))
          : Array.from(document.querySelectorAll(INTERACTIVE))
      ).slice(0, max)
      const textOf = (node) => {
        let text = node.innerText ?? node.textContent ?? ""
        if (typeof text !== "string") {
          return ""
        }
        text = text.replace(/\s+/g, " ").trim()
        return text.length > maxText ? text.slice(0, maxText) : text
      }
      const selectorOf = (node) => {
        if (node.id) {
          return `#${CSS.escape(node.id)}`
        }
        const testId = node.getAttribute("data-testid")
        if (testId) {
          return `[data-testid="${CSS.escape(testId)}"]`
        }
        const name = node.getAttribute("name")
        const tag = node.tagName.toLowerCase()
        if (name && tag === "input") {
          return `input[name="${CSS.escape(name)}"]`
        }
        const parts = []
        let current = node
        let depth = 0
        while (current && current.nodeType === 1 && current !== document.body && depth < 4) {
          const currentTag = current.tagName.toLowerCase()
          const parent = current.parentElement
          if (!parent) {
            parts.unshift(currentTag)
            break
          }
          const siblings = Array.from(parent.children).filter(
            (sibling) => sibling.tagName === current.tagName,
          )
          const position = siblings.indexOf(current) + 1
          parts.unshift(
            siblings.length > 1 ? `${currentTag}:nth-of-type(${position})` : currentTag,
          )
          current = parent
          depth += 1
        }
        return parts.length > 0 ? `body > ${parts.join(" > ")}` : tag
      }
      const implicitRole = (tag, type) => {
        if (tag === "button") {
          return "button"
        }
        if (tag === "a") {
          return "link"
        }
        if (tag === "select") {
          return "combobox"
        }
        if (tag === "textarea") {
          return "textbox"
        }
        if (tag === "input") {
          if (type === "checkbox" || type === "radio") {
            return type
          }
          if (type === "button" || type === "submit" || type === "reset") {
            return "button"
          }
          return "textbox"
        }
        return null
      }
      return nodes.map((node, index) => {
        const tag = node.tagName.toLowerCase()
        const type = tag === "input" ? (node.getAttribute("type") ?? "text").toLowerCase() : null
        const isPassword = type === "password"
        let value = null
        if (tag === "input" || tag === "textarea" || tag === "select") {
          const raw = typeof node.value === "string" ? node.value : ""
          if (isPassword) {
            value = raw.length > 0 ? "***" : ""
          } else {
            value = raw.length > maxText ? raw.slice(0, maxText) : raw
          }
        }
        const text = textOf(node)
        const label =
          node.getAttribute("aria-label") ??
          node.getAttribute("title") ??
          node.getAttribute("placeholder") ??
          (text.length > 0 ? text : node.getAttribute("alt")) ??
          ""
        const href = tag === "a" ? node.getAttribute("href") : null
        return {
          index,
          tag,
          selector: selectorOf(node),
          role: node.getAttribute("role") ?? implicitRole(tag, type),
          name: typeof label === "string" ? label.slice(0, maxText) : "",
          type,
          text,
          value,
          href: href === null ? null : href.slice(0, 500),
          disabled: Boolean(node.disabled),
          checked:
            typeof node.checked === "boolean" ? node.checked : null,
        }
      })
    },
    { selector, max: MAX_ELEMENTS, maxText: MAX_TEXT_CHARS },
  )
}

/** Polls for a visible locator and throws a protocol-friendly target error. */
async function requireLocator(page, selector, index, timeoutMs) {
  const locator = page.locator(selector)
  let count = await locator.count()
  if (count === 0) {
    // Wait once so an async-rendered target is not reported as missing while
    // the page is still settling. A genuine miss surfaces as `timeout`.
    await locator.first().waitFor({ state: "visible", timeout: timeoutMs })
    count = await locator.count()
    if (count === 0) {
      throw new ExecutorError("target_not_found", `no element matched selector ${JSON.stringify(selector)}`)
    }
  }
  if (index >= count) {
    throw new ExecutorError(
      "target_not_found",
      `index ${index} is out of range for ${count} match(es) of ${JSON.stringify(selector)}`,
    )
  }
  const target = locator.nth(index)
  await target.waitFor({ state: "visible", timeout: timeoutMs })
  return target
}

export class ExploreRuntime {
  #browser = null
  #browserPromise = null
  #pages = new Map()
  #nextPageId = 1
  #launcher
  #logger
  #maxPages
  #allowedHosts
  #closed = false
  #controllers = new Set()
  #current = null
  #tail = Promise.resolve()

  constructor({
    launch,
    logger = null,
    maxPages = MAX_EXPLORE_PAGES,
    allowedHosts = parseAllowedHosts(process.env.EZTEST_EXEC_ALLOWED_HOSTS),
  } = {}) {
    this.#launcher = typeof launch === "function" ? launch : launchExploreBrowser
    this.#logger = logger
    const parsed = Number(maxPages)
    this.#maxPages = Number.isInteger(parsed) && parsed > 0 ? parsed : MAX_EXPLORE_PAGES
    // Executor-wide WebSocket allow list (EZTEST_EXEC_ALLOWED_HOSTS); a page's
    // own allowed_hosts must also contain the host.
    this.#allowedHosts = parseAllowedHosts(
      Array.isArray(allowedHosts) ? allowedHosts.join(",") : allowedHosts,
    )
  }

  get size() {
    return this.#pages.size
  }

  async #ensureBrowser() {
    if (this.#closed) {
      throw new ExecutorError("internal", "explore runtime is shutting down")
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
      this.#browserPromise = this.#launcher().then(
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

  async #createPage(allowedHosts, viewport, operation) {
    const browser = await this.#ensureBrowser()
    if (this.#pages.size >= this.#maxPages) {
      throw new ExecutorError(
        "page_limit",
        `maximum of ${this.#maxPages} concurrent explore pages reached`,
      )
    }
    const context = await browser.newContext({
      acceptDownloads: false,
      serviceWorkers: "block",
      ...(viewport ? { viewport } : {}),
    })
    const pageId = `page_${this.#nextPageId++}`
    const entry = {
      pageId,
      context,
      page: null,
      allowedHosts: [...allowedHosts],
      consoleItems: [],
      networkItems: [],
      pendingRequests: new Map(),
      secrets: new Set(),
      blocked: null,
      cdpSession: null,
      closed: false,
    }
    try {
      await context.addInitScript(blockWorkers)
      await installRouteGuard(entry)
      await installWebSocketGuard(entry, this.#allowedHosts)
      const page = await context.newPage()
      entry.page = page
      context.on("page", (other) => {
        if (other !== entry.page) {
          void other.close().catch(() => {})
        }
      })
      attachTelemetry(entry)
      await installCdpGuard(entry, page)
      this.#pages.set(pageId, entry)
      if (operation) {
        operation.entry = entry
      }
      return entry
    } catch (error) {
      await context.close().catch(() => {})
      throw error
    }
  }

  #requirePage(pageId, operation) {
    if (typeof pageId !== "string" || pageId.length === 0) {
      throw invalidArguments("page_id must be a non-empty string")
    }
    const entry = this.#pages.get(pageId)
    if (!entry || entry.closed) {
      throw new ExecutorError("page_not_found", `unknown explore page: ${pageId}`)
    }
    if (operation) {
      operation.entry = entry
    }
    return entry
  }

  /** Resolves a page and clears the previous action's blocked-host marker. */
  #enterPage(pageId, operation) {
    const entry = this.#requirePage(pageId, operation)
    entry.blocked = null
    return entry
  }

  /** Consumes a blocked-host marker recorded during the action, if any. */
  #takeBlocked(entry) {
    if (!entry.blocked) {
      return null
    }
    const blocked = entry.blocked
    entry.blocked = null
    return new ExecutorError(
      "host_not_allowed",
      `blocked request to ${blocked.host ?? blocked.url}`,
    )
  }

  #requests(entry) {
    return entry.networkItems.slice(-MAX_OUTPUT_ITEMS).map((item) => ({
      ...item,
      url: scrubText(item.url, entry),
      ...(item.error ? { error: scrubText(item.error, entry) } : {}),
    }))
  }

  #console(entry) {
    return entry.consoleItems
      .slice(-MAX_OUTPUT_ITEMS)
      .map((item) => ({ ...item, text: scrubText(item.text, entry) }))
  }

  async #domSnapshot(entry) {
    // captureSnapshot scrubs secrets and password input values from the ARIA
    // text; readElements adds the actionable element inventory.
    const snapshot = await captureSnapshot(entry.page, {
      maxChars: MAX_SNAPSHOT_CHARS,
      secrets: [...entry.secrets],
    })
    const elements = await readElements(entry.page)
    return {
      ...snapshot,
      route: scrubText(routeOf(entry.page), entry),
      elements,
      element_count: elements.length,
    }
  }

  async #open(args, timeoutMs, operation) {
    const url = requireString(args.url, "url")
    let waitUntil = "load"
    if (args.wait_until !== undefined) {
      waitUntil = requireString(args.wait_until, "wait_until")
      if (!WAIT_UNTIL.has(waitUntil)) {
        throw invalidArguments(`wait_until must be one of ${[...WAIT_UNTIL].join(", ")}`)
      }
    }
    let entry
    if (args.page_id !== undefined) {
      entry = this.#enterPage(args.page_id, operation)
      if (args.allowed_hosts !== undefined) {
        entry.allowedHosts = parseAllowedHostsArg(args.allowed_hosts)
      }
    } else {
      const hosts =
        args.allowed_hosts === undefined
          ? [...DEFAULT_EXPLORE_HOSTS]
          : parseAllowedHostsArg(args.allowed_hosts)
      const viewport = normalizeViewport(args.viewport)
      if (!isAllowedUrl(url, { allowedHosts: hosts })) {
        throw new ExecutorError(
          "host_not_allowed",
          `url host is not allowed: ${hostFromUrl(url) ?? url}`,
        )
      }
      entry = await this.#createPage(hosts, viewport, operation)
    }
    if (!isAllowedUrl(url, { allowedHosts: entry.allowedHosts })) {
      throw new ExecutorError(
        "host_not_allowed",
        `url host is not allowed: ${hostFromUrl(url) ?? url}`,
      )
    }
    let response
    try {
      response = await entry.page.goto(url, { waitUntil, timeout: timeoutMs })
    } catch (error) {
      const blocked = this.#takeBlocked(entry)
      if (blocked) {
        throw blocked
      }
      throw error
    }
    await settle()
    const blocked = this.#takeBlocked(entry)
    if (blocked) {
      throw blocked
    }
    ensureNavigationRecorded(entry, response)
    return {
      result: {
        page_id: entry.pageId,
        route: scrubText(routeOf(entry.page), entry),
        url: scrubText(entry.page.url(), entry),
        status: response ? response.status() : null,
        dom_snapshot: await this.#domSnapshot(entry),
        requests: this.#requests(entry),
        console: this.#console(entry),
      },
    }
  }

  async #snapshot(args, operation) {
    const entry = this.#enterPage(args.page_id, operation)
    return {
      result: {
        page_id: entry.pageId,
        route: scrubText(routeOf(entry.page), entry),
        dom_snapshot: await this.#domSnapshot(entry),
        requests: this.#requests(entry),
        console: this.#console(entry),
      },
    }
  }

  async #click(args, timeoutMs, operation) {
    const entry = this.#enterPage(args.page_id, operation)
    const selector = requireString(args.selector, "selector")
    const index = args.index === undefined ? 0 : parseIndex(args.index)
    const target = await requireLocator(entry.page, selector, index, timeoutMs)
    const tag = await target.evaluate((element) => element.tagName.toLowerCase())
    await target.click({ timeout: timeoutMs })
    await settle()
    const blocked = this.#takeBlocked(entry)
    if (blocked) {
      throw blocked
    }
    return {
      result: {
        page_id: entry.pageId,
        route: scrubText(routeOf(entry.page), entry),
        selector,
        index,
        tag,
        requests: this.#requests(entry),
        console: this.#console(entry),
      },
    }
  }

  async #fill(args, timeoutMs, operation) {
    const entry = this.#enterPage(args.page_id, operation)
    const selector = requireString(args.selector, "selector")
    const index = args.index === undefined ? 0 : parseIndex(args.index)
    const secret = args.secret === undefined ? false : args.secret
    if (typeof secret !== "boolean") {
      throw invalidArguments("secret must be a boolean")
    }
    const value = requireString(args.value, "value", { allowEmpty: true })
    if (secret) {
      // Registered before the interaction so any failure echoing the value is
      // scrubbed as well; bounded like the execution context's secret set.
      registerSecret(entry.secrets, value)
    }
    const target = await requireLocator(entry.page, selector, index, timeoutMs)
    await target.click({ timeout: timeoutMs })
    await target.fill(value, { timeout: timeoutMs })
    await settle()
    const blocked = this.#takeBlocked(entry)
    if (blocked) {
      throw blocked
    }
    return {
      result: {
        page_id: entry.pageId,
        route: scrubText(routeOf(entry.page), entry),
        selector,
        index,
        value_set: true,
        secret,
        requests: this.#requests(entry),
        console: this.#console(entry),
      },
    }
  }

  async #extract(args, operation) {
    const entry = this.#enterPage(args.page_id, operation)
    const selector = args.selector === undefined ? null : requireString(args.selector, "selector")
    let index = null
    if (args.index !== undefined) {
      index = parseIndex(args.index)
      if (selector === null) {
        throw invalidArguments("index requires selector")
      }
    }
    let items
    if (selector === null) {
      items = await readElements(entry.page)
    } else {
      items = await readElements(entry.page, selector)
      if (items.length === 0) {
        throw new ExecutorError(
          "target_not_found",
          `no element matched selector ${JSON.stringify(selector)}`,
        )
      }
      if (index !== null) {
        if (index >= items.length) {
          throw new ExecutorError(
            "target_not_found",
            `index ${index} is out of range for ${items.length} match(es) of ${JSON.stringify(selector)}`,
          )
        }
        items = [items[index]]
      }
    }
    return {
      result: {
        page_id: entry.pageId,
        route: scrubText(routeOf(entry.page), entry),
        items,
        dom_snapshot: await this.#domSnapshot(entry),
        requests: this.#requests(entry),
        console: this.#console(entry),
      },
    }
  }

  async #screenshot(args, timeoutMs, operation) {
    const entry = this.#enterPage(args.page_id, operation)
    const selector = args.selector === undefined ? null : requireString(args.selector, "selector")
    const index = args.index === undefined ? 0 : parseIndex(args.index)
    if (selector === null && args.index !== undefined) {
      throw invalidArguments("index requires selector")
    }
    const fullPage = args.full_page === true
    let buffer
    let target
    if (selector === null) {
      buffer = await entry.page.screenshot({ type: "png", fullPage, timeout: timeoutMs })
    } else {
      const locator = await requireLocator(entry.page, selector, index, timeoutMs)
      buffer = await locator.screenshot({ type: "png", timeout: timeoutMs })
      target = { selector, index }
    }
    ensureArtifactSize(buffer)
    const digest = sha256(buffer)
    const screenshotRef = {
      kind: "screenshot",
      mime: "image/png",
      sha256: digest,
      bytes: buffer.length,
      ...(target ? { target } : {}),
    }
    return {
      result: {
        page_id: entry.pageId,
        route: scrubText(routeOf(entry.page), entry),
        screenshot_ref: screenshotRef,
        requests: this.#requests(entry),
        console: this.#console(entry),
      },
      artifacts: [artifact("screenshot", "image/png", buffer)],
    }
  }

  #dispatch(action, args, timeoutMs, operation) {
    switch (action) {
      case "explore.open":
        return this.#open(args, timeoutMs, operation)
      case "explore.snapshot":
        return this.#snapshot(args, operation)
      case "explore.click":
        return this.#click(args, timeoutMs, operation)
      case "explore.fill":
        return this.#fill(args, timeoutMs, operation)
      case "explore.extract":
        return this.#extract(args, operation)
      case "explore.screenshot":
        return this.#screenshot(args, timeoutMs, operation)
      default:
        return Promise.reject(invalidArguments(`unknown action: ${action}`))
    }
  }

  /**
   * Runs one explore action on the serialized action queue. Unknown actions are
   * rejected before the queue; actions get a Playwright timeout plus a hard
   * deadline and can be canceled through `cancel()`.
   */
  async runAction(request) {
    let action
    let args
    let timeoutMs
    try {
      if (request === null || typeof request !== "object" || Array.isArray(request)) {
        throw invalidArguments("request must be an object with an action")
      }
      action = request.action
      if (typeof action !== "string" || action.trim().length === 0) {
        throw invalidArguments("action is required")
      }
      if (!EXPLORE_ACTION_SET.has(action)) {
        throw invalidArguments(`unknown action: ${action}`)
      }
      args = request.args === undefined ? {} : request.args
      if (args === null || typeof args !== "object" || Array.isArray(args)) {
        throw invalidArguments("args must be an object")
      }
      const defaultTimeout = action === "explore.open" ? GOTO_TIMEOUT_MS : DEFAULT_ACTION_TIMEOUT_MS
      timeoutMs = resolveTimeout(args.timeout_ms, defaultTimeout)
    } catch (error) {
      return envelopeError(error)
    }

    const controller = new AbortController()
    const operation = { controller, entry: null }
    this.#controllers.add(controller)
    const execute = async () => {
      if (this.#closed) {
        throw new ExecutorError("internal", "explore runtime is shutting down")
      }
      if (controller.signal.aborted) {
        throw new ExecutorError("canceled", "action was canceled before it started")
      }
      this.#current = operation
      try {
        const outcome = await withAbort(this.#dispatch(action, args, timeoutMs, operation), {
          timeoutMs: timeoutMs + HARD_DEADLINE_SLACK_MS,
          signal: controller.signal,
          label: action,
        })
        return {
          ...outcome,
          result: scrubValue(outcome.result, [...(operation.entry?.secrets ?? [])]),
        }
      } catch (error) {
        const mapped = mapExploreError(error, operation.entry)
        if (
          controller.signal.aborted ||
          (error instanceof ExecutorError && error.code === "executor_timeout")
        ) {
          // Aborting the promise does not stop Playwright; dropping the page is
          // what actually terminates the browser-side operation.
          await this.#dropPage(operation.entry)
        }
        throw mapped
      } finally {
        if (this.#current === operation) {
          this.#current = null
        }
      }
    }
    const queued = this.#tail.then(execute, execute)
    this.#tail = queued.then(
      () => undefined,
      () => undefined,
    )
    try {
      const outcome = await queued
      return outcome.artifacts && outcome.artifacts.length > 0
        ? { ok: true, result: outcome.result, artifacts: outcome.artifacts }
        : { ok: true, result: outcome.result }
    } catch (error) {
      return envelopeError(
        error instanceof ExecutorError ? error : mapExploreError(error, operation.entry),
      )
    } finally {
      this.#controllers.delete(controller)
    }
  }

  /** Drops a page so a canceled/timed-out browser operation cannot wedge it. */
  async #dropPage(entry) {
    if (!entry || entry.closed) {
      return
    }
    entry.closed = true
    this.#pages.delete(entry.pageId)
    await withAbort(entry.context.close(), {
      timeoutMs: 5_000,
      label: `close of ${entry.pageId}`,
    }).catch(() => {})
    this.#logger?.info?.(`[executor] explore page ${entry.pageId} dropped`)
  }

  /** Aborts every in-flight and queued explore action; drops the live page. */
  async cancel() {
    let canceled = false
    for (const controller of this.#controllers) {
      if (!controller.signal.aborted) {
        controller.abort()
        canceled = true
      }
    }
    const current = this.#current
    if (canceled && current?.entry) {
      // Drop the page now so the canceled browser operation is terminated before
      // the endpoint returns; the action's own cleanup call is then a no-op.
      await this.#dropPage(current.entry)
    }
    return { canceled }
  }

  /**
   * Closes every explore page without shutting the runtime down, so a new
   * explore.open can be used again. In-flight actions are canceled first.
   */
  async closePages() {
    for (const controller of this.#controllers) {
      if (!controller.signal.aborted) {
        controller.abort()
      }
    }
    this.#controllers.clear()
    const entries = [...this.#pages.values()]
    this.#pages.clear()
    for (const entry of entries) {
      entry.closed = true
      await entry.context.close().catch(() => {})
    }
    return { closed: true }
  }

  /** Closes all explore pages and the browser. Idempotent. */
  async close() {
    if (this.#closed) {
      return { closed: true }
    }
    this.#closed = true
    await this.closePages()
    if (this.#browser) {
      await this.#browser.close().catch(() => {})
      this.#browser = null
      this.#browserPromise = null
    }
    return { closed: true }
  }
}

export function createExploreRuntime(options = {}) {
  return new ExploreRuntime(options)
}

/**
 * Browser / context lifecycle for the executor (spec §5.6 / §8).
 *
 * A single Chromium instance is shared by all contexts. Every context owns one
 * page, a serialized action queue, an AbortController per in-flight action and
 * ring buffers for console messages and network requests.
 */

import fs from "node:fs"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { chromium } from "playwright"

import { executeAction } from "./actions.mjs"
import {
  DEFAULT_ALLOWED_HOSTS,
  isWebSocketAllowed,
  parseAllowedHosts,
} from "./limits.mjs"
import {
  DEFAULT_MAX_CONTEXTS,
  ExecutorError,
  hostFromUrl,
  invalidArguments,
  isAllowedHost,
  normalizeAllowedHosts,
  pushRing,
} from "./security.mjs"
import { stripSecretValues } from "./snapshot.mjs"

const READINESS_TTL_MS = 10_000

function scrub(text, secrets) {
  return stripSecretValues(text, [...secrets])
}

/** Looks for a locally cached Chromium when the pinned revision is absent. */
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

/**
 * Launches the pinned Playwright Chromium. Local development machines may only
 * have an older cached revision or a system Chrome/Edge, so a missing pinned
 * browser falls back to those instead of requiring a browser download. The
 * official Docker image ships the pinned revision, so this is a dev-only path.
 */
async function launchChromium() {
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

function normalizeSessionStorage(input) {
  const result = {}
  if (input === undefined || input === null) {
    return result
  }
  const add = (origin, items) => {
    if (typeof origin !== "string" || origin.length === 0) {
      throw invalidArguments("sessionStorage origin must be a non-empty string")
    }
    let list
    if (Array.isArray(items)) {
      list = items
    } else if (items && typeof items === "object") {
      list = Object.entries(items).map(([name, value]) => ({ name, value }))
    } else {
      throw invalidArguments("sessionStorage entries must be an array or an object")
    }
    result[origin] = list.map((item) => {
      if (!item || typeof item !== "object" || typeof item.name !== "string") {
        throw invalidArguments("sessionStorage entries need a string name")
      }
      return { name: item.name, value: String(item.value ?? "") }
    })
  }
  if (Array.isArray(input)) {
    for (const entry of input) {
      if (!entry || typeof entry !== "object") {
        throw invalidArguments("sessionStorage array entries must be objects")
      }
      add(entry.origin, entry.items ?? entry.sessionStorage ?? entry.values)
    }
    return result
  }
  if (typeof input !== "object") {
    throw invalidArguments("sessionStorage must be an object or an array")
  }
  for (const [origin, items] of Object.entries(input)) {
    add(origin, items)
  }
  return result
}

function splitStorageState(input) {
  if (input === undefined || input === null) {
    return { storageState: undefined, sessionStorage: {} }
  }
  if (typeof input !== "object" || Array.isArray(input)) {
    throw invalidArguments("storageState must be an object")
  }
  const cookies = input.cookies ?? []
  if (!Array.isArray(cookies)) {
    throw invalidArguments("storageState.cookies must be an array")
  }
  const origins = []
  const sessionStorage = {}
  for (const origin of input.origins ?? []) {
    if (!origin || typeof origin !== "object" || typeof origin.origin !== "string") {
      throw invalidArguments("storageState.origins entries need a string origin")
    }
    origins.push({
      origin: origin.origin,
      localStorage: Array.isArray(origin.localStorage) ? origin.localStorage : [],
    })
    if (Array.isArray(origin.sessionStorage) && origin.sessionStorage.length > 0) {
      sessionStorage[origin.origin] = origin.sessionStorage
    }
  }
  const hasState = Array.isArray(input.cookies) || Array.isArray(input.origins)
  return { storageState: hasState ? { cookies, origins } : undefined, sessionStorage }
}

// Device scale factor bounds of design spec §9.3. Playwright fixes the DPR at
// context creation, so a visual assertion's configured value must be applied
// here (a later visual.compare only verifies the live fingerprint).
const MIN_DEVICE_SCALE_FACTOR = 0.5
const MAX_DEVICE_SCALE_FACTOR = 4

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

function normalizeDeviceScaleFactor(input) {
  if (input === undefined) {
    return undefined
  }
  const value = Number(input)
  if (
    !Number.isFinite(value) ||
    value < MIN_DEVICE_SCALE_FACTOR ||
    value > MAX_DEVICE_SCALE_FACTOR
  ) {
    throw invalidArguments(
      `deviceScaleFactor must be between ${MIN_DEVICE_SCALE_FACTOR} and ${MAX_DEVICE_SCALE_FACTOR}`,
    )
  }
  return value
}

function parseBaseUrl(input) {
  if (input === undefined) {
    return undefined
  }
  if (typeof input !== "string" || input.length === 0) {
    throw invalidArguments("baseUrl must be a non-empty string")
  }
  let parsed
  try {
    parsed = new URL(input)
  } catch {
    throw invalidArguments("baseUrl must be an absolute URL")
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw invalidArguments("baseUrl must use http or https")
  }
  return input
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

/** Classifies a URL against the context allow list and records blocked URLs. */
function classifyUrl(entry, url) {
  if (url.startsWith("data:")) {
    if (url.startsWith("data:text/html") && entry.allowDataPages) {
      return { allowed: true }
    }
    return { allowed: false, blocked: { url, scheme: "data:" } }
  }
  try {
    const parsed = new URL(url)
    if (parsed.protocol === "http:" || parsed.protocol === "https:") {
      if (isAllowedHost(parsed.hostname, entry.allowedHosts)) {
        return { allowed: true }
      }
      return { allowed: false, blocked: { url, host: parsed.hostname } }
    }
    return { allowed: false, blocked: { url, scheme: parsed.protocol } }
  } catch {
    return { allowed: false, blocked: { url } }
  }
}

/**
 * Installs the WebSocket allow-list guard of one browser context (design spec
 * §10.5). Every WebSocket opened by a page of the context must pass both the
 * context's allowedHosts and the executor-wide allow list
 * (EZTEST_EXEC_ALLOWED_HOSTS). A rejected connection is recorded in the context
 * network log, closed with 1008 (policy violation) and surfaced to the next
 * action as host_not_allowed.
 *
 * Playwright's HTTP guards (CDP Fetch and context.route) never see WebSocket
 * handshakes, so this dedicated guard is the only enforcement point.
 */
async function installWebSocketGuard(entry, browserContext, executorAllowedHosts, logger = null) {
  if (typeof browserContext.routeWebSocket !== "function") {
    // Fail closed: an executor that cannot intercept WebSockets must not run
    // contexts whose WebSocket traffic would bypass the allow list.
    throw new ExecutorError("internal", "the browser context cannot intercept WebSocket connections")
  }
  await browserContext.routeWebSocket(/.*/, (webSocket) => {
    const url = typeof webSocket.url === "function" ? webSocket.url() : ""
    if (isWebSocketAllowed(url, entry.allowedHosts, executorAllowedHosts)) {
      webSocket.connectToServer()
      return
    }
    let host = ""
    try {
      host = new URL(url).hostname
    } catch {
      host = ""
    }
    const recorded = {
      url: scrub(url, entry.secrets),
      host: host === "" ? null : host,
    }
    entry.blockedWebSocket = recorded
    pushRing(entry.networkItems, {
      method: "WS",
      url: recorded.url,
      status: null,
      duration_ms: 0,
      mime: null,
      error: "host_not_allowed",
    })
    logger?.warn?.(
      scrub(
        `[executor] blocked WebSocket connection of ${entry.contextId} to ${host || url}`,
        entry.secrets,
      ),
    )
    try {
      webSocket.close({ code: 1008, reason: "host_not_allowed" })
    } catch {
      webSocket.close()
    }
  })
}

/**
 * Enforces the host allow list for every request of `page`, including redirect
 * hops and subresources.
 *
 * Playwright's `context.route` does not observe redirect targets in Chromium
 * (the browser follows them below the route layer), so the guard uses the
 * Chromium DevTools Protocol `Fetch` domain, which pauses initial requests and
 * redirect hops alike. Disallowed requests are failed with BlockedByClient and
 * surfaced to actions as `host_not_allowed`.
 */
async function installRequestGuard(entry, page) {
  const session = await entry.context.newCDPSession(page)
  entry.cdpSessions.add(session)
  page.once("close", () => entry.cdpSessions.delete(session))
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

/**
 * Context-level route used for requests that the CDP guard cannot see yet.
 *
 * Two cases need it:
 * - a page exists but its CDP session is still being attached (the request is
 *   held until the guard is ready, so a redirect cannot outrun it);
 * - Chromium reports a popup's first navigation without a frame and before the
 *   `page` event exists, so there is no page to guard. Such navigations are
 *   refused: the protocol has no action that can operate on a popup, and
 *   allowing the request would let an allowed host redirect to a blocked one
 *   before any guard exists.
 */
async function installEarlyRouteGuard(entry, browserContext) {
  await browserContext.route("**/*", async (route) => {
    const request = route.request()
    const verdict = classifyUrl(entry, request.url())
    if (!verdict.allowed) {
      entry.blocked = verdict.blocked
      return route.abort("blockedbyclient").catch(() => {})
    }
    let page = null
    try {
      page = request.frame().page()
    } catch {
      page = null
    }
    if (typeof entry.guardPage !== "function") {
      return route.continue().catch(() => {})
    }
    if (page) {
      if (!entry.readyPages.has(page)) {
        // Hold the popup's first request until its CDP guard is active, so a
        // redirect from an allowed host cannot outrun the guard.
        try {
          await entry.guardPage(page)
        } catch {
          return route.abort("blockedbyclient").catch(() => {})
        }
      }
      return route.fallback().catch(() => {})
    }
    // A frame-less navigation is a popup that is about to load its first
    // document before its page/guard exists. Refuse it rather than letting an
    // allowed host redirect to a blocked one.
    if (request.isNavigationRequest()) {
      entry.blocked = { url: request.url() }
      return route.abort("blockedbyclient").catch(() => {})
    }
    return route.continue().catch(() => {})
  })
}

/**
 * Installs the allow-list guard and telemetry for one page. Every page that
 * Chromium exposes through the context's `page` event (the initial page and
 * any popup that survives its first navigation) goes through this function.
 * Popups that navigate immediately have no page/frame yet when their first
 * request is routed, so the early route guard refuses that navigation instead.
 */
async function installPageGuard(entry, page) {
  attachPageTelemetry(entry, page)
  await installRequestGuard(entry, page)
  entry.readyPages.add(page)
}

function attachPageTelemetry(entry, page) {
  page.on("console", (message) => {
    pushRing(entry.consoleItems, {
      type: message.type(),
      text: scrub(message.text(), entry.secrets),
      ts: Date.now(),
    })
  })
  page.on("request", (request) => {
    entry.pendingRequests.set(request, {
      method: request.method(),
      url: scrub(request.url(), entry.secrets),
      start: Date.now(),
    })
  })
  const recordNetwork = async (request, failed, failureText) => {
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
      // The response may be gone after a failure; keep the request record.
    }
    pushRing(entry.networkItems, {
      method: pending?.method ?? request.method(),
      url: scrub(request.url(), entry.secrets),
      status,
      duration_ms: Date.now() - (pending?.start ?? Date.now()),
      mime,
      ...(failed ? { error: scrub(failureText ?? "", entry.secrets) } : {}),
    })
  }
  page.on("requestfinished", (request) => {
    void recordNetwork(request, false, null)
  })
  page.on("requestfailed", (request) => {
    void recordNetwork(request, true, request.failure()?.errorText ?? null)
  })
}

export class ContextManager {
  #browser = null
  #browserPromise = null
  #contexts = new Map()
  #nextId = 1
  #maxContexts
  #launcher
  #logger
  #closed = false
  #creating = 0
  #allowDataPages
  #readiness = null
  // Executor-wide WebSocket allow list (EZTEST_EXEC_ALLOWED_HOSTS). It is an
  // additional restriction on top of every context's own allowedHosts.
  #allowedHosts

  constructor({
    maxContexts = DEFAULT_MAX_CONTEXTS,
    launch,
    logger = null,
    allowedHosts = parseAllowedHosts(process.env.EZTEST_EXEC_ALLOWED_HOSTS),
    allowDataPages = process.env.EZTEST_EXECUTOR_ALLOW_DATA_PAGES === "1",
  } = {}) {
    const parsed = Number(maxContexts)
    this.#maxContexts = Number.isInteger(parsed) && parsed > 0 ? parsed : DEFAULT_MAX_CONTEXTS
    this.#launcher = typeof launch === "function" ? launch : launchChromium
    this.#logger = logger
    this.#allowDataPages = allowDataPages === true
    this.#allowedHosts = normalizeAllowedHosts(
      Array.isArray(allowedHosts) && allowedHosts.length > 0 ? allowedHosts : [...DEFAULT_ALLOWED_HOSTS],
    )
  }

  get size() {
    return this.#contexts.size
  }

  get maxContexts() {
    return this.#maxContexts
  }

  // allowedHosts is the executor-wide WebSocket allow list.
  get allowedHosts() {
    return [...this.#allowedHosts]
  }

  async #getBrowser() {
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

  async browserVersion() {
    const browser = await this.#getBrowser()
    return browser.version()
  }

  /**
   * Cached readiness probe for /healthz. A browser that cannot launch makes the
   * service degraded; the result is cached briefly so repeated probes do not
   * retry a failing launch, but a recovered browser (or a crash) is re-probed.
   */
  async health() {
    if (this.#closed) {
      return { status: "degraded", error: "executor is shutting down" }
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

  async createContext(input = {}) {
    if (this.#closed) {
      throw new ExecutorError("internal", "executor is shutting down")
    }
    if (!input || typeof input !== "object" || Array.isArray(input)) {
      throw invalidArguments("context payload must be an object")
    }
    if (this.#contexts.size + this.#creating >= this.#maxContexts) {
      throw new ExecutorError(
        "context_limit",
        `maximum of ${this.#maxContexts} concurrent contexts reached`,
      )
    }
    const allowedHosts = normalizeAllowedHosts(input.allowedHosts)
    const baseUrl = parseBaseUrl(input.baseUrl)
    const baseHost = baseUrl ? hostFromUrl(baseUrl) : null
    if (baseHost && !allowedHosts.includes(baseHost)) {
      allowedHosts.push(baseHost)
    }
    if (allowedHosts.length === 0) {
      throw invalidArguments("allowedHosts or baseUrl is required")
    }
    const viewport = normalizeViewport(input.viewport)
    const deviceScaleFactor = normalizeDeviceScaleFactor(input.deviceScaleFactor)
    const { storageState, sessionStorage: stateSessionStorage } = splitStorageState(input.storageState)
    const sessionStorage = {
      ...stateSessionStorage,
      ...normalizeSessionStorage(input.sessionStorage),
    }
    if (input.key !== undefined && typeof input.key !== "string") {
      throw invalidArguments("key must be a string")
    }

    this.#creating += 1
    try {
      const browser = await this.#getBrowser()
      const browserContext = await browser.newContext({
        acceptDownloads: true,
        // Service workers issue requests outside the page's CDP session, so the
        // page-level Fetch guard cannot see them. Blocking them is the only
        // reliable way to keep the allow list authoritative.
        serviceWorkers: "block",
        ...(viewport ? { viewport } : {}),
        ...(deviceScaleFactor !== undefined ? { deviceScaleFactor } : {}),
        ...(storageState ? { storageState } : {}),
      })
      const contextId = `ctx_${this.#nextId++}`
      const entry = {
        contextId,
        key: typeof input.key === "string" ? input.key : null,
        context: browserContext,
        page: null,
        allowedHosts,
        baseUrl,
        allowDataPages: this.#allowDataPages,
        consoleItems: [],
        networkItems: [],
        pendingRequests: new Map(),
        blocked: null,
        blockedWebSocket: null,
        secrets: new Set(),
        tmpDir: null,
        closed: false,
        tail: Promise.resolve(),
        controllers: new Set(),
        cdpSessions: new Set(),
        pageGuards: new WeakMap(),
        readyPages: new WeakSet(),
        guardPage: null,
        newPage: null,
        currentToken: null,
      }
      if (Object.keys(sessionStorage).length > 0) {
        await browserContext.addInitScript((entries) => {
          try {
            const items = entries[window.location.origin]
            if (!items) {
              return
            }
            for (const item of items) {
              window.sessionStorage.setItem(item.name, item.value)
            }
          } catch {
            // Opaque origins (about:blank, sandboxed frames) cannot store data.
          }
        }, sessionStorage)
      }
      await installEarlyRouteGuard(entry, browserContext)
      await installWebSocketGuard(entry, browserContext, this.#allowedHosts, this.#logger)
      entry.guardPage = (page) => {
        let pending = entry.pageGuards.get(page)
        if (!pending) {
          pending = installPageGuard(entry, page)
          entry.pageGuards.set(page, pending)
        }
        return pending
      }
      entry.newPage = async () => {
        const page = await browserContext.newPage()
        await entry.guardPage(page)
        return page
      }
      // Every page in the context (window.open, target=_blank, ...) must get the
      // guard before it can load content. A guard that cannot be installed
      // closes the page instead of leaving it unguarded.
      browserContext.on("page", (page) => {
        entry.guardPage(page).catch((error) => {
          this.#logger?.warn?.(
            scrub(
              `[executor] could not guard a ${contextId} page: ${error instanceof Error ? error.message : error}`,
              entry.secrets,
            ),
          )
          void page.close().catch(() => {})
        })
      })

      const page = await entry.newPage()
      entry.page = page

      this.#contexts.set(contextId, entry)
      this.#logger?.info?.(`[executor] context ${contextId} created`)
      return { contextId, browserVersion: browser.version() }
    } finally {
      this.#creating -= 1
    }
  }

  #require(contextId) {
    const entry = this.#contexts.get(contextId)
    if (!entry || entry.closed) {
      throw new ExecutorError("context_not_found", `unknown context: ${contextId}`)
    }
    return entry
  }

  async runAction(contextId, request) {
    let entry
    try {
      entry = this.#require(contextId)
    } catch (error) {
      return envelopeError(error)
    }
    if (request?.action === "close") {
      await this.deleteContext(contextId)
      return { ok: true, result: { closed: true } }
    }
    const controller = new AbortController()
    const operation = { controller, page: null, recovery: null }
    entry.controllers.add(controller)
    const execute = async () => {
      if (entry.closed) {
        throw new ExecutorError("context_not_found", `context ${contextId} is closed`)
      }
      if (controller.signal.aborted) {
        throw new ExecutorError("canceled", "action was canceled before it started")
      }
      entry.blocked = null
      const takeBlockedWebSocket = () => {
        if (!entry.blockedWebSocket) {
          return null
        }
        const blocked = entry.blockedWebSocket
        entry.blockedWebSocket = null
        return new ExecutorError(
          "host_not_allowed",
          `WebSocket connection is not allowed: ${blocked.host ?? blocked.url}`,
        )
      }
      const blockedBefore = takeBlockedWebSocket()
      if (blockedBefore) {
        throw blockedBefore
      }
      if (!entry.page) {
        entry.page = await entry.newPage()
      }
      operation.page = entry.page
      entry.currentToken = operation
      try {
        const { result, artifacts } = await executeAction(entry, request, controller.signal)
        // A cross-host WebSocket opened while the action ran (for example a
        // page script that connects during goto) fails that action too: it is
        // recorded in the network log and must not be silently ignored.
        const blockedDuring = takeBlockedWebSocket()
        if (blockedDuring) {
          throw blockedDuring
        }
        return artifacts && artifacts.length > 0
          ? { ok: true, result, artifacts }
          : { ok: true, result }
      } catch (error) {
        if (controller.signal.aborted) {
          // The AbortController only rejects the caller's promise; the browser
          // operation keeps running unless the page is torn down. Recover before
          // this action leaves the queue so the next action gets a clean page.
          await this.#recoverCanceledPage(entry, operation.page, operation)
        } else if (error instanceof ExecutorError && error.code === "executor_timeout") {
          // The hard deadline won over a background browser task that ignored
          // its own timeout (design spec §10.5). Cancel the operation and
          // replace the page so it cannot keep the context wedged.
          controller.abort()
          await this.#recoverCanceledPage(entry, operation.page, operation)
          this.#logger?.warn?.(
            `[executor] ${entry.contextId} action exceeded its hard deadline; page recovered`,
          )
        }
        if (error instanceof ExecutorError) {
          error.message = scrub(error.message, entry.secrets)
        }
        throw error
      } finally {
        if (entry.currentToken === operation) {
          entry.currentToken = null
        }
      }
    }
    const queued = entry.tail.then(execute, execute)
    entry.tail = queued.then(
      () => undefined,
      () => undefined,
    )
    try {
      return await queued
    } catch (error) {
      return envelopeError(error)
    } finally {
      entry.controllers.delete(controller)
    }
  }

  /**
   * Terminates the page that hosted a canceled action and replaces it. Closing
   * the page is what actually aborts Playwright's in-flight navigation/wait;
   * `AbortController` alone leaves the browser-side operation running.
   */
  async #recoverCanceledPage(entry, page, operation) {
    if (operation.recovery) {
      return operation.recovery
    }
    operation.recovery = (async () => {
      if (page) {
        await page.close().catch(() => {})
      }
      if (entry.closed || entry.page !== page) {
        return
      }
      try {
        entry.page = await entry.newPage()
        this.#logger?.info?.(`[executor] context ${entry.contextId} page recreated after cancel`)
      } catch (error) {
        // Leave entry.page empty so the next queued action retries creation.
        entry.page = null
        this.#logger?.warn?.(
          `[executor] could not recreate page for ${entry.contextId}: ${error instanceof Error ? error.message : error}`,
        )
      }
    })()
    return operation.recovery
  }

  async cancel(contextId) {
    const entry = this.#require(contextId)
    let canceled = false
    for (const controller of entry.controllers) {
      if (!controller.signal.aborted) {
        controller.abort()
        canceled = true
      }
    }
    if (!canceled) {
      return { canceled: false }
    }
    // Start tearing down the page immediately; the in-flight action awaits the
    // same recovery promise before it releases the queue.
    const operation = entry.currentToken
    if (operation?.page) {
      await this.#recoverCanceledPage(entry, operation.page, operation)
    }
    return { canceled }
  }

  async deleteContext(contextId) {
    const entry = this.#require(contextId)
    this.#contexts.delete(contextId)
    entry.closed = true
    for (const controller of entry.controllers) {
      controller.abort()
    }
    entry.controllers.clear()
    await entry.context.close().catch(() => {})
    if (entry.tmpDir) {
      await fsp.rm(entry.tmpDir, { recursive: true, force: true }).catch(() => {})
    }
    this.#logger?.info?.(`[executor] context ${contextId} closed`)
    return { closed: true }
  }

  async close() {
    if (this.#closed) {
      return
    }
    this.#closed = true
    const ids = [...this.#contexts.keys()]
    for (const contextId of ids) {
      await this.deleteContext(contextId).catch(() => {})
    }
    if (this.#browser) {
      await this.#browser.close().catch(() => {})
      this.#browser = null
      this.#browserPromise = null
    }
  }
}

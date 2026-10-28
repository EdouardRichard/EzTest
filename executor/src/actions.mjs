/**
 * Executor actions (spec §5.6 / §8.2).
 *
 * Every action receives a context entry created by contexts.mjs and returns
 * `{ result, artifacts }`. Failures are thrown as ExecutorError so the context
 * manager / HTTP server can produce the protocol error envelope.
 */

import crypto from "node:crypto"
import fs from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import {
  DEFAULT_ACTION_TIMEOUT_MS,
  ExecutorError,
  GOTO_TIMEOUT_MS,
  ensureArtifactFileSize,
  ensureArtifactSize,
  hostFromUrl,
  invalidArguments,
  isAllowedUrl,
  registerSecret,
  resolveTimeout,
} from "./security.mjs"
import { captureSnapshot, stripSecretValues } from "./snapshot.mjs"

const WAIT_STATES = new Set(["attached", "detached", "visible", "hidden"])
const MOUSE_BUTTONS = new Set(["left", "right", "middle"])
const WAIT_UNTIL = new Set(["load", "domcontentloaded", "networkidle", "commit"])

function requireString(value, name, { allowEmpty = false } = {}) {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0)) {
    throw invalidArguments(`${name} must be a ${allowEmpty ? "" : "non-empty "}string`)
  }
  return value
}

/** Parses the target descriptor shape from spec §5.6. */
export function parseTarget(target) {
  if (target === null || typeof target !== "object" || Array.isArray(target)) {
    throw invalidArguments("target must be an object")
  }
  const groups = new Set()
  if (target.role !== undefined || target.name !== undefined || target.exact !== undefined) {
    groups.add("role")
  }
  if (target.text !== undefined) {
    groups.add("text")
  }
  if (target.test_id !== undefined) {
    groups.add("test_id")
  }
  if (target.selector !== undefined) {
    groups.add("selector")
  }
  if (groups.size === 0) {
    throw invalidArguments("target must contain one of role, text, test_id or selector")
  }
  if (groups.size > 1) {
    throw invalidArguments("target must contain exactly one of role, text, test_id or selector")
  }
  const kind = [...groups][0]
  if (kind === "role") {
    const role = requireString(target.role, "target.role")
    let name
    if (target.name !== undefined) {
      name = requireString(target.name, "target.name")
    }
    let exact = false
    if (target.exact !== undefined) {
      if (typeof target.exact !== "boolean") {
        throw invalidArguments("target.exact must be a boolean")
      }
      exact = target.exact
    }
    return name === undefined ? { kind: "role", role, exact } : { kind: "role", role, name, exact }
  }
  if (kind === "text") {
    return { kind: "text", text: requireString(target.text, "target.text") }
  }
  if (kind === "test_id") {
    return { kind: "test_id", testId: requireString(target.test_id, "target.test_id") }
  }
  return { kind: "selector", selector: requireString(target.selector, "target.selector") }
}

function describeTarget(spec) {
  switch (spec.kind) {
    case "role":
      return spec.name === undefined
        ? `role=${spec.role}`
        : `role=${spec.role} name=${JSON.stringify(spec.name)} exact=${spec.exact}`
    case "text":
      return `text=${JSON.stringify(spec.text)}`
    case "test_id":
      return `test_id=${JSON.stringify(spec.testId)}`
    default:
      return `selector=${JSON.stringify(spec.selector)}`
  }
}

function buildLocator(page, spec) {
  switch (spec.kind) {
    case "role":
      return spec.name === undefined
        ? page.getByRole(spec.role)
        : page.getByRole(spec.role, { name: spec.name, exact: spec.exact })
    case "text":
      return page.getByText(spec.text)
    case "test_id":
      return page.getByTestId(spec.testId)
    default:
      return page.locator(spec.selector)
  }
}

async function resolveOne(page, spec, timeoutMs) {
  const locator = buildLocator(page, spec)
  const count = await locator.count()
  if (count === 0) {
    throw new ExecutorError("target_not_found", `no element matched ${describeTarget(spec)}`)
  }
  if (count > 1) {
    throw new ExecutorError(
      "target_ambiguous",
      `${count} elements matched ${describeTarget(spec)}; refine the target`,
    )
  }
  const single = locator.first()
  await single.waitFor({ state: "visible", timeout: timeoutMs })
  return single
}

function clampTail(value, fallback = 50, max = 200) {
  const numeric = Number(value)
  if (!Number.isFinite(numeric) || numeric <= 0) {
    return fallback
  }
  return Math.min(Math.max(Math.trunc(numeric), 1), max)
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

/** Runs a promise with a hard deadline and cancellation signal. */
export function withAbort(promise, { timeoutMs, signal, label }) {
  return new Promise((resolve, reject) => {
    let settled = false
    const finish = (callback, value) => {
      if (settled) {
        return
      }
      settled = true
      clearTimeout(timer)
      signal?.removeEventListener("abort", onAbort)
      callback(value)
    }
    const timer = setTimeout(
      () => finish(reject, new ExecutorError("timeout", `${label} timed out after ${timeoutMs}ms`)),
      timeoutMs,
    )
    const onAbort = () => finish(reject, new ExecutorError("canceled", `${label} was canceled`))
    if (signal?.aborted) {
      onAbort()
      return
    }
    signal?.addEventListener("abort", onAbort, { once: true })
    promise.then(
      (value) => finish(resolve, value),
      (error) => finish(reject, error),
    )
  })
}

function knownSecrets(entry, args) {
  const values = [...(entry?.secrets ?? [])]
  if (args?.secret === true && typeof args?.value === "string" && args.value.length > 0) {
    values.push(args.value)
  }
  return values
}

function scrubSecrets(text, entry, args) {
  return stripSecretValues(text, knownSecrets(entry, args))
}

function mapActionError(error, action, args, entry) {
  if (error instanceof ExecutorError) {
    error.message = scrubSecrets(error.message, entry, args)
    return error
  }
  const message = scrubSecrets(error instanceof Error ? error.message : String(error), entry, args)
  let code = "internal"
  if (error?.name === "TimeoutError" || /Timeout .* exceeded|timed out/i.test(message)) {
    code = "timeout"
  } else if (/ERR_NAME_NOT_RESOLVED|ENOTFOUND|EAI_AGAIN/i.test(message)) {
    code = "dns_failed"
  } else if (/ERR_CONNECTION_(REFUSED|RESET|CLOSED|ABORTED|FAILED)|ERR_UNSAFE_PORT|ECONNREFUSED|ECONNRESET/i.test(message)) {
    code = "connection_failed"
  } else if (/net::ERR/i.test(message)) {
    code = action === "goto" ? "navigation_failed" : "internal"
  }
  return new ExecutorError(code, message)
}

function sessionStorageFromPage(page) {
  return page.evaluate(() => {
    const items = []
    for (let index = 0; index < window.sessionStorage.length; index += 1) {
      const name = window.sessionStorage.key(index)
      if (name !== null) {
        items.push({ name, value: window.sessionStorage.getItem(name) ?? "" })
      }
    }
    return items
  })
}

async function readSessionStorageForOrigin(entry, origin, timeoutMs) {
  let page
  try {
    page = await entry.newPage()
    await page.goto(origin, { waitUntil: "domcontentloaded", timeout: Math.min(timeoutMs, 5_000) })
    return await sessionStorageFromPage(page)
  } catch {
    return []
  } finally {
    if (page) {
      await page.close().catch(() => {})
    }
  }
}

const HANDLERS = {
  async goto(entry, args, timeoutMs) {
    const url = requireString(args.url, "url")
    if (!isAllowedUrl(url, { allowedHosts: entry.allowedHosts, allowData: entry.allowDataPages })) {
      throw new ExecutorError(
        "host_not_allowed",
        `url host is not allowed: ${hostFromUrl(url) ?? url}`,
      )
    }
    let waitUntil = "load"
    if (args.wait_until !== undefined) {
      waitUntil = requireString(args.wait_until, "wait_until")
      if (!WAIT_UNTIL.has(waitUntil)) {
        throw invalidArguments(`wait_until must be one of ${[...WAIT_UNTIL].join(", ")}`)
      }
    }
    let response
    try {
      response = await entry.page.goto(url, { waitUntil, timeout: timeoutMs })
    } catch (error) {
      if (entry.blocked) {
        throw new ExecutorError(
          "host_not_allowed",
          `blocked request to ${entry.blocked.host ?? entry.blocked.url}`,
        )
      }
      throw error
    }
    const blocked = entry.blocked
    const result = {
      url: entry.page.url(),
      status: response ? response.status() : null,
    }
    if (blocked) {
      result.blocked_hosts = [scrubSecrets(blocked.url, entry, args)]
    }
    return { result }
  },

  async snapshot(entry, args) {
    const maxChars = args.max_chars === undefined ? undefined : Number(args.max_chars)
    const result = await captureSnapshot(entry.page, {
      ...(Number.isFinite(maxChars) ? { maxChars } : {}),
      secrets: [...entry.secrets],
    })
    return { result }
  },

  async click(entry, args, timeoutMs) {
    let button = "left"
    if (args.button !== undefined) {
      button = requireString(args.button, "button")
      if (!MOUSE_BUTTONS.has(button)) {
        throw invalidArguments("button must be left, right or middle")
      }
    }
    let clickCount = 1
    if (args.click_count !== undefined) {
      clickCount = Number(args.click_count)
      if (!Number.isInteger(clickCount) || clickCount < 1) {
        throw invalidArguments("click_count must be a positive integer")
      }
    }
    const locator = await resolveOne(entry.page, parseTarget(args.target), timeoutMs)
    await locator.click({ button, clickCount, timeout: timeoutMs })
    return { result: { ok: true } }
  },

  async fill(entry, args, timeoutMs) {
    const secret = args.secret === undefined ? false : args.secret
    if (typeof secret !== "boolean") {
      throw invalidArguments("secret must be a boolean")
    }
    const value = requireString(args.value, "value", { allowEmpty: true })
    if (secret) {
      // Register before the interaction so a Playwright failure that echoes the
      // value is scrubbed; the set itself is bounded (oldest entries evicted,
      // oversized values rejected).
      registerSecret(entry.secrets, value)
    }
    const locator = await resolveOne(entry.page, parseTarget(args.target), timeoutMs)
    await locator.click({ timeout: timeoutMs })
    await locator.fill(value, { timeout: timeoutMs })
    return { result: { ok: true, secret } }
  },

  async press(entry, args, timeoutMs) {
    const key = requireString(args.key, "key")
    if (args.target !== undefined) {
      const locator = await resolveOne(entry.page, parseTarget(args.target), timeoutMs)
      await locator.press(key, { timeout: timeoutMs })
    } else {
      await entry.page.keyboard.press(key)
    }
    return { result: { ok: true } }
  },

  async wait_for(entry, args, timeoutMs) {
    const state = args.state === undefined ? "visible" : requireString(args.state, "state")
    if (!WAIT_STATES.has(state)) {
      throw invalidArguments(`state must be one of ${[...WAIT_STATES].join(", ")}`)
    }
    const waits = []
    if (args.target !== undefined) {
      const spec = parseTarget(args.target)
      waits.push(buildLocator(entry.page, spec).first().waitFor({ state, timeout: timeoutMs }))
    }
    if (args.text !== undefined) {
      const text = requireString(args.text, "text")
      waits.push(entry.page.getByText(text).first().waitFor({ state, timeout: timeoutMs }))
    }
    if (args.url_contains !== undefined) {
      const needle = requireString(args.url_contains, "url_contains")
      waits.push(
        entry.page.waitForURL((url) => url.toString().includes(needle), { timeout: timeoutMs }),
      )
    }
    if (waits.length === 0) {
      throw invalidArguments("wait_for requires at least one of target, text or url_contains")
    }
    try {
      await Promise.any(waits)
    } catch {
      throw new ExecutorError("timeout", `wait_for timed out after ${timeoutMs}ms`)
    }
    return { result: { ok: true } }
  },

  async screenshot(entry, args, timeoutMs) {
    const fullPage = args.full_page === true
    let buffer
    if (args.target !== undefined) {
      const locator = await resolveOne(entry.page, parseTarget(args.target), timeoutMs)
      buffer = await locator.screenshot({ type: "png", timeout: timeoutMs })
    } else {
      buffer = await entry.page.screenshot({ type: "png", fullPage, timeout: timeoutMs })
    }
    // Playwright returns the PNG as a Buffer, so the size can only be checked
    // after capture. Oversized screenshots are rejected here without being
    // added to the response envelope.
    ensureArtifactSize(buffer)
    return {
      result: { full_page: fullPage, bytes: buffer.length },
      artifacts: [artifact("screenshot", "image/png", buffer)],
    }
  },

  async console(entry, args) {
    const tail = clampTail(args.tail, 50, 200)
    const secrets = knownSecrets(entry, args)
    return {
      result: {
        items: entry.consoleItems
          .slice(-tail)
          .map((item) => ({ ...item, text: stripSecretValues(item.text ?? "", secrets) })),
      },
    }
  },

  async network(entry, args) {
    const tail = clampTail(args.tail, 50, 200)
    const secrets = knownSecrets(entry, args)
    return {
      result: {
        items: entry.networkItems.slice(-tail).map((item) => ({
          ...item,
          url: stripSecretValues(item.url ?? "", secrets),
          ...(item.error ? { error: stripSecretValues(item.error, secrets) } : {}),
        })),
      },
    }
  },

  async download(entry, args, timeoutMs) {
    const locator = await resolveOne(entry.page, parseTarget(args.target), timeoutMs)
    const [download] = await Promise.all([
      entry.page.waitForEvent("download", { timeout: timeoutMs }),
      locator.click({ timeout: timeoutMs }),
    ])
    const suggested = download.suggestedFilename()
    entry.tmpDir ??= await fs.mkdtemp(path.join(os.tmpdir(), `eztest-executor-${entry.contextId}-`))
    const safeName = path.basename(suggested).replace(/[^\w.-]/g, "_") || "download.bin"
    const targetPath = path.join(entry.tmpDir, `${Date.now()}-${safeName}`)
    try {
      await download.saveAs(targetPath)
      // Reject oversized downloads from the file size before buffering them.
      await ensureArtifactFileSize(targetPath)
      const buffer = await fs.readFile(targetPath)
      ensureArtifactSize(buffer)
      return {
        result: { filename: suggested, bytes: buffer.length },
        artifacts: [
          artifact("download", "application/octet-stream", buffer, { filename: suggested }),
        ],
      }
    } finally {
      await fs.rm(targetPath, { force: true }).catch(() => {})
    }
  },

  async storage_state(entry, args, timeoutMs) {
    const includeSessionStorage = args.include_session_storage !== false
    const state = await entry.context.storageState()
    const origins = new Map()
    for (const origin of state.origins ?? []) {
      origins.set(origin.origin, {
        origin: origin.origin,
        localStorage: origin.localStorage ?? [],
        sessionStorage: [],
      })
    }
    if (includeSessionStorage) {
      let pageOrigin = null
      try {
        pageOrigin = new URL(entry.page.url()).origin
      } catch {
        pageOrigin = null
      }
      const candidates = new Set(origins.keys())
      if (pageOrigin) {
        candidates.add(pageOrigin)
      }
      for (const origin of candidates) {
        const record = origins.get(origin) ?? { origin, localStorage: [], sessionStorage: [] }
        record.sessionStorage =
          pageOrigin === origin
            ? await sessionStorageFromPage(entry.page).catch(() => [])
            : await readSessionStorageForOrigin(entry, origin, timeoutMs)
        origins.set(origin, record)
      }
    }
    const result = { cookies: state.cookies ?? [], origins: [] }
    for (const record of origins.values()) {
      result.origins.push(
        includeSessionStorage
          ? record
          : { origin: record.origin, localStorage: record.localStorage },
      )
    }
    return { result }
  },
}

export const KNOWN_ACTIONS = new Set([...Object.keys(HANDLERS), "close"])

export async function executeAction(entry, request, signal) {
  if (request === null || typeof request !== "object" || Array.isArray(request)) {
    throw invalidArguments("request must be an object with an action")
  }
  const action = request.action
  if (typeof action !== "string" || action.trim().length === 0) {
    throw invalidArguments("action is required")
  }
  const handler = HANDLERS[action]
  if (!handler) {
    throw invalidArguments(`unknown action: ${action}`)
  }
  let args = request.args
  if (args === undefined) {
    args = {}
  }
  if (args === null || typeof args !== "object" || Array.isArray(args)) {
    throw invalidArguments("args must be an object")
  }
  const defaultTimeout = action === "goto" ? GOTO_TIMEOUT_MS : DEFAULT_ACTION_TIMEOUT_MS
  const timeoutMs = resolveTimeout(args.timeout_ms, defaultTimeout)
  try {
    return await withAbort(handler(entry, args, timeoutMs), {
      timeoutMs: timeoutMs + 250,
      signal,
      label: action,
    })
  } catch (error) {
    throw mapActionError(error, action, args, entry)
  }
}

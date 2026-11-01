/**
 * Optional executor → API push registration/heartbeat (executor-protocol §12.4).
 *
 * Pull healthz (P3) remains the primary registration path. When EZTEST_API_URL
 * is set the executor also sends one POST /api/internal/executors/register at
 * startup and then a heartbeat every EZTEST_EXECUTOR_HEARTBEAT_SECONDS
 * (default 10, 1-60). Push failures are logged and retried with exponential
 * backoff; they never block or crash case execution.
 *
 * The Bearer token is EZTEST_EXECUTOR_TOKEN and is never logged.
 */

import os from "node:os"

const DEFAULT_HEARTBEAT_SECONDS = 10
const MIN_HEARTBEAT_SECONDS = 1
const MAX_HEARTBEAT_SECONDS = 60
const DEFAULT_BACKOFF_MS = 1000
const MAX_BACKOFF_MS = 30_000
const DEFAULT_REQUEST_TIMEOUT_MS = 5000

export const REGISTER_PATH = "/api/internal/executors/register"
export const HEARTBEAT_PATH = "/api/internal/executors/heartbeat"

function trimToBlank(value) {
  return String(value ?? "").trim()
}

function normalizeBaseUrl(value) {
  return trimToBlank(value).replace(/\/+$/, "")
}

/** Parses EZTEST_EXECUTOR_HEARTBEAT_SECONDS into the contract range 1-60. */
export function parseHeartbeatSeconds(value) {
  const numeric = Math.trunc(Number(value))
  if (!Number.isFinite(numeric) || numeric < MIN_HEARTBEAT_SECONDS) {
    return DEFAULT_HEARTBEAT_SECONDS
  }
  return Math.min(numeric, MAX_HEARTBEAT_SECONDS)
}

/**
 * Derives the absolute push endpoint from EZTEST_EXECUTOR_ADDR. Wildcard bind
 * addresses are advertised as loopback so the URL stays absolute and valid.
 */
export function executorEndpointFromAddress(address) {
  const raw = trimToBlank(address)
  if (raw.length === 0) {
    return null
  }
  if (/^https?:\/\//i.test(raw)) {
    try {
      const parsed = new URL(raw)
      if (!parsed.hostname || !parsed.port) {
        return null
      }
      return `${parsed.protocol}//${parsed.host}`
    } catch {
      return null
    }
  }
  const index = raw.lastIndexOf(":")
  if (index <= 0 || index === raw.length - 1) {
    return null
  }
  let host = raw.slice(0, index)
  if (host.startsWith("[") && host.endsWith("]")) {
    host = host.slice(1, -1)
  }
  const port = Number(raw.slice(index + 1))
  if (!Number.isInteger(port) || port <= 0 || port > 65535) {
    return null
  }
  if (host === "0.0.0.0" || host === "::" || host === "[::]") {
    host = "127.0.0.1"
  }
  return `http://${host}:${port}`
}

/**
 * Reads the optional push configuration. Returns null when EZTEST_API_URL is
 * empty (push disabled) or no token is available.
 */
export function parsePushConfig(env = process.env) {
  const apiUrl = normalizeBaseUrl(env.EZTEST_API_URL)
  if (apiUrl.length === 0) {
    return null
  }
  const token = trimToBlank(env.EZTEST_EXECUTOR_TOKEN)
  if (token.length === 0) {
    return null
  }
  return {
    apiUrl,
    token,
    heartbeatSeconds: parseHeartbeatSeconds(env.EZTEST_EXECUTOR_HEARTBEAT_SECONDS),
  }
}

function defaultSnapshot({ activeContexts = 0, capacity = 1, status = "ok" } = {}) {
  return { activeContexts, capacity, status }
}

/**
 * Creates the push client. All I/O is injectable so tests can use a local HTTP
 * server without opening real timers/network dependencies.
 */
export function createPushClient({
  apiUrl,
  token,
  endpoint,
  name = "",
  version = "0.0.0",
  heartbeatSeconds = DEFAULT_HEARTBEAT_SECONDS,
  snapshot = null,
  activeContexts = 0,
  capacity = 1,
  status = "ok",
  logger = null,
  fetchImpl = globalThis.fetch,
  requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
  backoffMs = DEFAULT_BACKOFF_MS,
  maxBackoffMs = MAX_BACKOFF_MS,
} = {}) {
  if (typeof apiUrl !== "string" || normalizeBaseUrl(apiUrl).length === 0) {
    throw new Error("push client requires an API URL")
  }
  if (typeof token !== "string" || token.length === 0) {
    throw new Error("push client requires a token")
  }
  if (typeof fetchImpl !== "function") {
    throw new Error("push client requires fetch")
  }
  const baseUrl = normalizeBaseUrl(apiUrl)
  const intervalMs = Math.max(1, Number(heartbeatSeconds) * 1000)
  let timer = null
  let running = false
  let retryDelay = Math.max(1, Number(backoffMs))
  const maxDelay = Math.max(retryDelay, Number(maxBackoffMs))
  const snap = typeof snapshot === "function" ? snapshot : () => defaultSnapshot({ activeContexts, capacity, status })

  async function readSnapshot() {
    try {
      const value = await snap()
      if (value && typeof value === "object") {
        return {
          activeContexts: Number.isInteger(value.activeContexts) && value.activeContexts >= 0 ? value.activeContexts : 0,
          capacity: Number.isInteger(value.capacity) && value.capacity > 0 ? value.capacity : 1,
          status: value.status === "degraded" ? "degraded" : "ok",
        }
      }
    } catch (error) {
      logger?.warn?.(`[executor] push snapshot failed: ${errorMessage(error)}`)
    }
    return defaultSnapshot({ activeContexts, capacity, status })
  }

  async function post(path, body) {
    const options = {
      method: "POST",
      headers: {
        authorization: `Bearer ${token}`,
        "content-type": "application/json",
      },
      body: JSON.stringify(body),
    }
    if (Number.isInteger(requestTimeoutMs) && requestTimeoutMs > 0 && typeof AbortSignal?.timeout === "function") {
      options.signal = AbortSignal.timeout(requestTimeoutMs)
    }
    const response = await fetchImpl(`${baseUrl}${path}`, options)
    // Drain the body so the socket can be reused; the payload is not consumed.
    try {
      await response.text()
    } catch {
      // Best effort only.
    }
    if (!response.ok) {
      throw new Error(`${path} returned HTTP ${response.status}`)
    }
  }

  function schedule(callback, delayMs) {
    if (!running) {
      return
    }
    timer = setTimeout(() => {
      timer = null
      void callback()
    }, Math.max(1, delayMs))
    timer.unref?.()
  }

  function nextRetryDelay() {
    const current = retryDelay
    retryDelay = Math.min(maxDelay, retryDelay * 2)
    return current
  }

  function resetRetryDelay() {
    retryDelay = Math.max(1, Number(backoffMs))
  }

  async function sendRegister() {
    if (!running) {
      return
    }
    const state = await readSnapshot()
    try {
      await post(REGISTER_PATH, {
        endpoint,
        name: trimToBlank(name) || endpoint,
        capacity: state.capacity,
        active_contexts: state.activeContexts,
        version,
      })
      resetRetryDelay()
      logger?.info?.(`[executor] registered with ${baseUrl}`)
      schedule(sendHeartbeat, intervalMs)
    } catch (error) {
      const delayMs = nextRetryDelay()
      logger?.warn?.(`[executor] push register failed: ${errorMessage(error)} (retrying in ${delayMs}ms)`)
      schedule(sendRegister, delayMs)
    }
  }

  async function sendHeartbeat() {
    if (!running) {
      return
    }
    const state = await readSnapshot()
    try {
      await post(HEARTBEAT_PATH, {
        endpoint,
        active_contexts: state.activeContexts,
        capacity: state.capacity,
        version,
        status: state.status === "degraded" ? "degraded" : "active",
      })
      resetRetryDelay()
      schedule(sendHeartbeat, intervalMs)
    } catch (error) {
      const delayMs = nextRetryDelay()
      logger?.warn?.(`[executor] push heartbeat failed: ${errorMessage(error)} (retrying in ${delayMs}ms)`)
      schedule(sendHeartbeat, delayMs)
    }
  }

  return {
    start() {
      if (running) {
        return
      }
      running = true
      retryDelay = Math.max(1, Number(backoffMs))
      void sendRegister()
    },
    stop() {
      running = false
      if (timer !== null) {
        clearTimeout(timer)
        timer = null
      }
    },
    get running() {
      return running
    },
  }
}

function errorMessage(error) {
  return error instanceof Error ? error.message : String(error)
}

/**
 * Wires the push client from the executor process environment and starts it.
 * Returns null when push is disabled, when the token is missing or when the
 * listen address cannot be converted into an absolute endpoint.
 */
export function startPushFromEnv({ env = process.env, manager = null, version = "0.0.0", logger = null, fetchImpl = globalThis.fetch } = {}) {
  const config = parsePushConfig(env)
  if (config === null) {
    return null
  }
  const address = trimToBlank(env.EZTEST_EXECUTOR_ADDR) || "127.0.0.1:13336"
  const endpoint = executorEndpointFromAddress(address)
  if (endpoint === null) {
    logger?.warn?.(`[executor] cannot derive a push endpoint from EZTEST_EXECUTOR_ADDR=${address}; push disabled`)
    return null
  }
  const port = endpoint.slice(endpoint.lastIndexOf(":") + 1)
  const name = trimToBlank(env.EZTEST_EXECUTOR_NAME) || `${os.hostname()}:${port}`
  const snapshot = async () => {
    const health = manager && typeof manager.health === "function" ? await manager.health() : { status: "ok" }
    return {
      activeContexts: manager?.size ?? 0,
      capacity: manager?.maxContexts ?? 1,
      status: health?.status === "degraded" ? "degraded" : "ok",
    }
  }
  const client = createPushClient({
    ...config,
    endpoint,
    name,
    version,
    snapshot,
    logger,
    fetchImpl,
  })
  client.start()
  return client
}

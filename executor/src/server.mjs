/**
 * HTTP entry point for the executor protocol (spec §5.6).
 *
 * Transport: JSON over HTTP on 127.0.0.1:13336 by default, authenticated with
 * `Authorization: Bearer $EZTEST_EXECUTOR_TOKEN`. The token is only accepted in
 * the header and is never logged; request bodies are capped at 1MB.
 */

import crypto from "node:crypto"
import fs from "node:fs"
import http from "node:http"
import { pathToFileURL } from "node:url"

import { ContextManager } from "./contexts.mjs"
import { parseAllowedHosts } from "./limits.mjs"
import { startPushFromEnv } from "./push.mjs"
import {
  DEFAULT_MAX_CONTEXTS,
  ExecutorError,
  MAX_ACTION_REQUEST_BYTES,
  MAX_REQUEST_BYTES,
  visualActionRequestLimit,
} from "./security.mjs"

const PACKAGE_VERSION = (() => {
  try {
    const pkg = JSON.parse(fs.readFileSync(new URL("../package.json", import.meta.url), "utf8"))
    return typeof pkg.version === "string" ? pkg.version : "0.0.0"
  } catch {
    return "0.0.0"
  }
})()

function sendJson(res, status, payload) {
  const body = JSON.stringify(payload)
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "content-length": Buffer.byteLength(body),
  })
  res.end(body)
}

function sendError(res, status, code, message) {
  sendJson(res, status, { ok: false, error: { code, message } })
}

function statusForError(code) {
  switch (code) {
    case "unauthorized":
      return 401
    case "invalid_request":
    case "invalid_arguments":
      return 400
    case "context_not_found":
      return 404
    case "context_limit":
      return 409
    case "too_large":
      return 413
    default:
      return 500
  }
}

const ACTION_ERROR_STATUS = {
  invalid_arguments: 400,
  context_not_found: 404,
  too_large: 413,
}

function isAuthorized(req, token) {
  const header = req.headers.authorization
  if (typeof header !== "string" || !header.startsWith("Bearer ")) {
    return false
  }
  const provided = Buffer.from(header.slice("Bearer ".length))
  const expected = Buffer.from(token)
  return provided.length === expected.length && crypto.timingSafeEqual(provided, expected)
}

export function readBody(req, limit = MAX_REQUEST_BYTES) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let total = 0
    let settled = false
    const finish = (callback, value) => {
      if (settled) {
        return
      }
      settled = true
      callback(value)
    }
    req.on("data", (chunk) => {
      if (settled) {
        return
      }
      total += chunk.length
      if (total > limit) {
        finish(
          reject,
          new ExecutorError("too_large", `request body exceeds the ${limit} byte limit`),
        )
        req.resume()
        return
      }
      chunks.push(chunk)
    })
    req.on("end", () => finish(resolve, Buffer.concat(chunks)))
    req.on("error", () =>
      finish(reject, new ExecutorError("invalid_request", "request body could not be read")),
    )
    req.on("aborted", () =>
      finish(reject, new ExecutorError("invalid_request", "client aborted the request")),
    )
    req.on("close", () => {
      // IncomingMessage also emits "close" after a complete body; only an
      // incomplete body means the client went away before the request ended.
      if (!req.complete) {
        finish(reject, new ExecutorError("invalid_request", "client aborted the request"))
      }
    })
  })
}

function parseJsonObject(buffer) {
  if (buffer.length === 0) {
    return {}
  }
  let value
  try {
    value = JSON.parse(buffer.toString("utf8"))
  } catch {
    throw new ExecutorError("invalid_request", "request body must be valid JSON")
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new ExecutorError("invalid_request", "request body must be a JSON object")
  }
  return value
}

async function readJsonBody(req, limit = MAX_REQUEST_BYTES) {
  return parseJsonObject(await readBody(req, limit))
}

function decodeId(raw) {
  try {
    return decodeURIComponent(raw)
  } catch {
    throw new ExecutorError("invalid_arguments", "context id is not valid URL encoding")
  }
}

export function createExecutorServer({ token, manager, version = PACKAGE_VERSION, logger = null } = {}) {
  if (typeof token !== "string" || token.length === 0) {
    throw new Error("EZTEST_EXECUTOR_TOKEN is required")
  }
  const executor = manager ?? new ContextManager({ logger })
  const server = http.createServer((req, res) => {
    handle(req, res).catch((error) => {
      if (res.destroyed || res.writableEnded) {
        return
      }
      if (res.headersSent) {
        res.destroy()
        return
      }
      const code = error instanceof ExecutorError ? error.code : "internal"
      sendJson(res, statusForError(code), {
        ok: false,
        error: {
          code,
          message: error instanceof Error ? error.message : "internal executor error",
        },
      })
    })
  })

  async function handle(req, res) {
    const startedAt = Date.now()
    let pathname = "/"
    try {
      pathname = new URL(req.url ?? "/", "http://127.0.0.1").pathname
    } catch {
      // Keep the default path; the request will 404 below.
    }
    res.once("finish", () => {
      logger?.info?.(`[executor] ${req.method} ${pathname} ${res.statusCode} ${Date.now() - startedAt}ms`)
    })

    if (pathname === "/healthz") {
      if (req.method !== "GET") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const health =
        typeof executor.health === "function" ? await executor.health() : { status: "ok" }
      const ready = health.status === "ok"
      return sendJson(res, ready ? 200 : 503, {
        status: health.status,
        version,
        contexts: executor.size ?? 0,
        maxContexts: executor.maxContexts ?? DEFAULT_MAX_CONTEXTS,
        ...(health.browserVersion ? { browserVersion: health.browserVersion } : {}),
        ...(health.error ? { error: health.error } : {}),
      })
    }

    if (!isAuthorized(req, token)) {
      return sendError(res, 401, "unauthorized", "missing or invalid bearer token")
    }

    if (pathname === "/v1/contexts") {
      if (req.method === "GET") {
        // P3 extension (execution design §6.1): context inventory used by the
        // API for orphan reconciliation and executor capacity accounting.
        const contexts = typeof executor.list === "function" ? executor.list() : []
        return sendJson(res, 200, { contexts })
      }
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const body = await readJsonBody(req)
      const created = await executor.createContext(body)
      return sendJson(res, 200, created)
    }

    const actionMatch = /^\/v1\/contexts\/([^/]+)\/actions$/.exec(pathname)
    if (actionMatch) {
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const contextId = decodeId(actionMatch[1])
      // Only visual.compare may carry the ~6.7 MiB base64 baseline copy; every
      // other action keeps the normal 1 MiB body cap (executor protocol §11.1).
      const rawBody = await readBody(req, MAX_ACTION_REQUEST_BYTES)
      const body = parseJsonObject(rawBody)
      const limit = body.action === "visual.compare" ? visualActionRequestLimit() : MAX_REQUEST_BYTES
      if (rawBody.length > limit) {
        throw new ExecutorError("too_large", `request body exceeds the ${limit} byte limit`)
      }
      const envelope = await executor.runAction(contextId, body)
      const status = envelope.ok ? 200 : ACTION_ERROR_STATUS[envelope.error.code] ?? 200
      return sendJson(res, status, envelope)
    }

    const cancelMatch = /^\/v1\/contexts\/([^/]+)\/cancel$/.exec(pathname)
    if (cancelMatch) {
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const contextId = decodeId(cancelMatch[1])
      const result = await executor.cancel(contextId)
      return sendJson(res, 200, { ok: true, result })
    }

    const contextMatch = /^\/v1\/contexts\/([^/]+)$/.exec(pathname)
    if (contextMatch) {
      if (req.method !== "DELETE") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const contextId = decodeId(contextMatch[1])
      const result = await executor.deleteContext(contextId)
      return sendJson(res, 200, { ok: true, result })
    }

    return sendError(res, 404, "invalid_request", "not found")
  }

  return server
}

export function parseAddress(address = "127.0.0.1:13336") {
  const raw = String(address).trim()
  const index = raw.lastIndexOf(":")
  if (index <= 0 || index === raw.length - 1) {
    throw new Error(`invalid EZTEST_EXECUTOR_ADDR: ${address} (expected host:port)`)
  }
  let host = raw.slice(0, index)
  if (host.startsWith("[") && host.endsWith("]")) {
    host = host.slice(1, -1)
  }
  const port = Number(raw.slice(index + 1))
  if (!Number.isInteger(port) || port <= 0 || port > 65535) {
    throw new Error(`invalid EZTEST_EXECUTOR_ADDR port: ${address}`)
  }
  return { host, port }
}

export function parseMaxContexts(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric > 0) {
    return numeric
  }
  return DEFAULT_MAX_CONTEXTS
}

export async function startFromEnv(env = process.env, { manager: injectedManager = null } = {}) {
  const token = String(env.EZTEST_EXECUTOR_TOKEN ?? "").trim()
  if (token.length === 0) {
    console.error("[executor] EZTEST_EXECUTOR_TOKEN is required")
    process.exitCode = 1
    return null
  }
  const { host, port } = parseAddress(env.EZTEST_EXECUTOR_ADDR ?? "127.0.0.1:13336")
  const maxContexts = parseMaxContexts(env.EZTEST_EXECUTOR_MAX_CONTEXTS)
  // P8 hardening (design spec §10.5): the executor-wide WebSocket allow list.
  // It defaults to loopback; remote targets must be listed explicitly.
  const allowedHosts = parseAllowedHosts(env.EZTEST_EXEC_ALLOWED_HOSTS)
  const logger = console
  const manager =
    injectedManager ?? new ContextManager({ maxContexts, logger, allowedHosts })
  const version = env.EZTEST_EXECUTOR_VERSION ?? PACKAGE_VERSION
  const server = createExecutorServer({
    token,
    manager,
    logger,
    version,
  })
  await new Promise((resolve, reject) => {
    server.once("error", reject)
    server.listen(port, host, resolve)
  })
  console.log(`[executor] listening on http://${host}:${port} (max contexts ${maxContexts})`)

  // Optional push registration/heartbeat (executor-protocol §12.4). A missing
  // EZTEST_API_URL keeps the P3 pull-only behavior.
  const push = startPushFromEnv({ env, manager, version, logger })
  if (push) {
    console.log("[executor] push registration enabled (EZTEST_API_URL is set)")
  }

  const shutdown = async (signal) => {
    console.log(`[executor] received ${signal}, shutting down`)
    push?.stop()
    await new Promise((resolve) => server.close(resolve))
    await manager.close()
    process.exit(0)
  }
  process.once("SIGINT", () => {
    void shutdown("SIGINT")
  })
  process.once("SIGTERM", () => {
    void shutdown("SIGTERM")
  })
  return { server, manager, push }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  startFromEnv().catch((error) => {
    console.error(`[executor] failed to start: ${error instanceof Error ? error.message : error}`)
    process.exit(1)
  })
}

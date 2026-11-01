/**
 * HTTP entry point of the EzTest PDF renderer service (P7 D59).
 *
 * The service is intentionally small: `GET /healthz` (public readiness) and
 * `POST /v1/pdf` (Bearer token), which renders one standalone HTML document to
 * PDF bytes with Playwright. The API never installs Chromium: it calls this
 * process and stores the returned bytes in the content ledger.
 *
 * Configuration (environment):
 * - EZTEST_RENDERER_ADDR: listen address (default 127.0.0.1:13337)
 * - EZTEST_RENDER_TOKEN: required Bearer token
 * - EZTEST_RENDER_TIMEOUT_SECONDS: per-request render deadline (default 60)
 * - EZTEST_RENDER_CHANNEL: fallback browser channel (default chrome)
 */

import crypto from "node:crypto"
import fs from "node:fs"
import http from "node:http"
import { pathToFileURL } from "node:url"

import { DEFAULT_CHANNEL, DEFAULT_TIMEOUT_SECONDS, PDFRenderer } from "./render.mjs"

/** MAX_HTML_BYTES is the hard cap of one report document (2 MiB). */
export const MAX_HTML_BYTES = 2 * 1024 * 1024
// MAX_REQUEST_BYTES bounds the JSON envelope (escaped HTML can be several times
// larger than the decoded document); the decoded html is capped separately.
const MAX_REQUEST_BYTES = 16 * 1024 * 1024
const DEFAULT_MAX_CONCURRENCY = 2
const MAX_ERROR_SUMMARY = 2000

const PACKAGE_VERSION = (() => {
  try {
    const pkg = JSON.parse(fs.readFileSync(new URL("../package.json", import.meta.url), "utf8"))
    return typeof pkg.version === "string" ? pkg.version : "0.0.0"
  } catch {
    return "0.0.0"
  }
})()

function sendJson(response, status, payload) {
  const body = JSON.stringify(payload)
  response.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "content-length": Buffer.byteLength(body),
    "cache-control": "no-store",
  })
  response.end(body)
}

function sendError(response, status, code, message) {
  sendJson(response, status, { ok: false, error: { code, message } })
}

function isAuthorized(request, token) {
  const header = request.headers.authorization
  if (typeof header !== "string" || !header.startsWith("Bearer ")) {
    return false
  }
  const provided = Buffer.from(header.slice("Bearer ".length))
  const expected = Buffer.from(token)
  return provided.length === expected.length && crypto.timingSafeEqual(provided, expected)
}

function readBody(request, limit = MAX_REQUEST_BYTES) {
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
    request.on("data", (chunk) => {
      if (settled) {
        return
      }
      total += chunk.length
      if (total > limit) {
        finish(reject, Object.assign(new Error(`request body exceeds ${limit} bytes`), { code: "invalid_request" }))
        request.resume()
        return
      }
      chunks.push(chunk)
    })
    request.on("end", () => finish(resolve, Buffer.concat(chunks)))
    request.on("error", () =>
      finish(reject, Object.assign(new Error("request body could not be read"), { code: "invalid_request" })),
    )
    request.on("aborted", () =>
      finish(reject, Object.assign(new Error("client aborted the request"), { code: "invalid_request" })),
    )
  })
}

async function readJsonBody(request) {
  const buffer = await readBody(request)
  if (buffer.length === 0) {
    return {}
  }
  let value
  try {
    value = JSON.parse(buffer.toString("utf8"))
  } catch {
    throw Object.assign(new Error("request body must be valid JSON"), { code: "invalid_request" })
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw Object.assign(new Error("request body must be a JSON object"), { code: "invalid_request" })
  }
  return value
}

function errorMessage(error) {
  const message = error instanceof Error ? error.message : String(error)
  return message.length > MAX_ERROR_SUMMARY ? message.slice(0, MAX_ERROR_SUMMARY) : message
}

/**
 * createRendererServer builds the HTTP server. renderer defaults to the real
 * Playwright PDFRenderer; tests inject a fake with the same surface.
 */
export function createRendererServer({
  token,
  renderer,
  version = PACKAGE_VERSION,
  logger = null,
  timeoutMs = DEFAULT_TIMEOUT_SECONDS * 1000,
  maxConcurrency = DEFAULT_MAX_CONCURRENCY,
} = {}) {
  if (typeof token !== "string" || token.trim().length === 0) {
    throw new Error("EZTEST_RENDER_TOKEN is required")
  }
  const engine = renderer ?? new PDFRenderer({ logger })
  const deadline = Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : DEFAULT_TIMEOUT_SECONDS * 1000
  const limit = Number.isInteger(maxConcurrency) && maxConcurrency > 0 ? maxConcurrency : DEFAULT_MAX_CONCURRENCY
  let active = 0

  async function handle(request, response) {
    const startedAt = Date.now()
    let pathname = "/"
    try {
      pathname = new URL(request.url ?? "/", "http://127.0.0.1").pathname
    } catch {
      // Keep the default path; the request 404s below.
    }
    response.once("finish", () => {
      logger?.info?.(`[renderer] ${request.method} ${pathname} ${response.statusCode} ${Date.now() - startedAt}ms`)
    })

    if (pathname === "/healthz") {
      if (request.method !== "GET") {
        return sendError(response, 405, "invalid_request", "method not allowed")
      }
      let health = { status: "ok" }
      try {
        health = typeof engine.health === "function" ? await engine.health() : { status: "ok" }
      } catch (error) {
        health = { status: "degraded", error: errorMessage(error) }
      }
      const ready = health.status === "ok"
      return sendJson(response, ready ? 200 : 503, {
        status: health.status,
        version,
        ...(health.browserVersion ? { browser: health.browserVersion } : {}),
        ...(health.error ? { error: health.error } : {}),
      })
    }

    if (pathname !== "/v1/pdf") {
      return sendError(response, 404, "not_found", "not found")
    }
    if (!isAuthorized(request, token)) {
      return sendError(response, 401, "unauthorized", "missing or invalid bearer token")
    }
    if (request.method !== "POST") {
      return sendError(response, 405, "invalid_request", "method not allowed")
    }
    if (active >= limit) {
      return sendError(response, 429, "busy", `at most ${limit} concurrent renders are allowed`)
    }

    let body
    try {
      body = await readJsonBody(request)
    } catch (error) {
      return sendError(response, 400, error.code ?? "invalid_request", errorMessage(error))
    }
    const html = body.html
    if (typeof html !== "string" || html.trim().length === 0) {
      return sendError(response, 400, "html_required", "html must be a non-empty string")
    }
    if (Buffer.byteLength(html, "utf8") > MAX_HTML_BYTES) {
      return sendError(response, 400, "html_too_large", `html must be at most ${MAX_HTML_BYTES} bytes`)
    }

    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), deadline)
    active += 1
    try {
      const pdf = await engine.render(html, { signal: controller.signal })
      if (!Buffer.isBuffer(pdf) || pdf.length === 0) {
        throw new Error("renderer produced no PDF bytes")
      }
      response.writeHead(200, {
        "content-type": "application/pdf",
        "content-length": pdf.length,
        "cache-control": "no-store",
      })
      response.end(pdf)
    } catch (error) {
      if (error?.code === "render_timeout" || controller.signal.aborted) {
        return sendError(response, 504, "render_timeout", `PDF rendering exceeded ${deadline}ms`)
      }
      logger?.warn?.(`[renderer] render failed: ${errorMessage(error)}`)
      return sendError(response, 422, "render_failed", errorMessage(error))
    } finally {
      clearTimeout(timer)
      active -= 1
    }
  }

  const server = http.createServer((request, response) => {
    handle(request, response).catch((error) => {
      if (response.destroyed || response.writableEnded) {
        return
      }
      if (response.headersSent) {
        response.destroy()
        return
      }
      sendError(response, 500, "internal", errorMessage(error))
    })
  })
  // Test/ops introspection: the number of renders currently in flight.
  server.activeRenders = () => active
  return server
}

export function parseAddress(address = "127.0.0.1:13337") {
  const raw = String(address).trim()
  const index = raw.lastIndexOf(":")
  if (index <= 0 || index === raw.length - 1) {
    throw new Error(`invalid EZTEST_RENDERER_ADDR: ${address} (expected host:port)`)
  }
  let host = raw.slice(0, index)
  if (host.startsWith("[") && host.endsWith("]")) {
    host = host.slice(1, -1)
  }
  const port = Number(raw.slice(index + 1))
  if (!Number.isInteger(port) || port <= 0 || port > 65535) {
    throw new Error(`invalid EZTEST_RENDERER_ADDR port: ${address}`)
  }
  return { host, port }
}

export function parseTimeoutSeconds(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric > 0) {
    return numeric
  }
  return DEFAULT_TIMEOUT_SECONDS
}

/** startFromEnv reads the frozen environment contract and starts the server. */
export async function startFromEnv(env = process.env) {
  const token = String(env.EZTEST_RENDER_TOKEN ?? "").trim()
  if (token.length === 0) {
    console.error("[renderer] EZTEST_RENDER_TOKEN is required")
    process.exitCode = 1
    return null
  }
  const { host, port } = parseAddress(env.EZTEST_RENDERER_ADDR ?? "127.0.0.1:13337")
  const timeoutSeconds = parseTimeoutSeconds(env.EZTEST_RENDER_TIMEOUT_SECONDS)
  const channel = String(env.EZTEST_RENDER_CHANNEL ?? DEFAULT_CHANNEL).trim() || DEFAULT_CHANNEL
  const logger = console
  const renderer = new PDFRenderer({ channel, logger })
  const server = createRendererServer({
    token,
    renderer,
    version: env.EZTEST_RENDER_VERSION ?? PACKAGE_VERSION,
    logger,
    timeoutMs: timeoutSeconds * 1000,
  })
  await new Promise((resolve, reject) => {
    server.once("error", reject)
    server.listen(port, host, resolve)
  })
  console.log(
    `[renderer] listening on http://${host}:${port} (timeout ${timeoutSeconds}s, channel ${channel})`,
  )

  const shutdown = async (signal) => {
    console.log(`[renderer] received ${signal}, shutting down`)
    await new Promise((resolve) => server.close(resolve))
    await renderer.close()
    process.exit(0)
  }
  process.once("SIGINT", () => {
    void shutdown("SIGINT")
  })
  process.once("SIGTERM", () => {
    void shutdown("SIGTERM")
  })
  return { server, renderer }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  startFromEnv().catch((error) => {
    console.error(`[renderer] failed to start: ${error instanceof Error ? error.message : error}`)
    process.exit(1)
  })
}

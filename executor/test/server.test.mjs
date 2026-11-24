// Executor protocol v2 HTTP surface: transport contract (bearer pipe, body
// limits, abort handling, request-id correlation, typed errors). The v1 context
// family is deleted (R-C2) and the transitional browser bridge is removed (T16);
// their absence is pinned in protocol_v2.test.mjs.
import test, { after, before } from "node:test"
import assert from "node:assert/strict"
import { EventEmitter } from "node:events"
import http from "node:http"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import {
  createExecutorServer,
  readBody,
  startFromEnv,
} from "../src/server.mjs"
import { SessionManager } from "../src/sessions.mjs"
import { ExecutorError, MAX_REQUEST_BYTES } from "../src/protocol.mjs"
import { httpClient, startLocalPage } from "./helpers.mjs"

const TOKEN = "test-executor-token"

let pageServer
let sessions
let server
let request
let workspaceRoot

before(async () => {
  pageServer = await startLocalPage()
  workspaceRoot = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t10-server-"))
  sessions = new SessionManager({ logger: null, reclaimIntervalMs: 0 })
  server = createExecutorServer({ token: TOKEN, sessions, logger: null, version: "test" })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  request = httpClient(`http://127.0.0.1:${port}`, TOKEN)
})

after(async () => {
  await sessions.close()
  await new Promise((resolve) => server.close(resolve))
  await pageServer.close()
  await fsp.rm(workspaceRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
})

let workspaceSeq = 0

async function createSession(overrides = {}) {
  workspaceSeq += 1
  const response = await request("/v1/sessions", {
    method: "POST",
    body: {
      key: "run:42:sut:7:role:admin",
      workspace: path.join(workspaceRoot, `ws-${workspaceSeq}`),
      base_url: pageServer.url,
      ...overrides,
    },
  })
  assert.equal(response.status, 200, response.text)
  return response.json
}

test("GET /healthz is reachable without a token and reports the session surface", async () => {
  const response = await request("/healthz", { token: undefined })
  assert.equal(response.status, 200)
  assert.equal(response.json.status, "ok")
  assert.equal(response.json.version, "test")
  assert.equal(response.json.maxSessions, 8)
  assert.equal(typeof response.json.sessions, "number")
  assert.equal(Object.hasOwn(response.json, "browserContexts"), false)
})

test("readBody rejects when the client aborts the request", async () => {
  const req = new EventEmitter()
  const body = readBody(req)
  req.emit("data", Buffer.from('{"action":'))
  req.emit("aborted")
  const outcome = await Promise.race([
    body.then(
      () => "resolved",
      (error) => error,
    ),
    new Promise((resolve) => setTimeout(() => resolve("timeout"), 500)),
  ])
  assert.ok(outcome instanceof ExecutorError, `expected an error, got ${String(outcome)}`)
  assert.equal(outcome.code, "invalid_request")
})

test("an aborted request body does not wedge or crash the server", async () => {
  const { port } = server.address()
  await new Promise((resolve) => {
    const req = http.request({
      host: "127.0.0.1",
      port,
      path: "/v1/sessions",
      method: "POST",
      headers: {
        authorization: `Bearer ${TOKEN}`,
        "content-type": "application/json",
        "content-length": "1000",
      },
    })
    req.on("error", () => resolve())
    req.write('{"key":')
    setTimeout(() => {
      req.destroy()
      resolve()
    }, 50)
  })
  await new Promise((resolve) => setTimeout(resolve, 100))
  const health = await request("/healthz", { token: undefined })
  assert.equal(health.status, 200)
})

test("v2 endpoints reject missing or wrong bearer tokens", async () => {
  const missing = await request("/v1/sessions", { method: "POST", token: undefined, body: {} })
  assert.equal(missing.status, 401)
  assert.equal(missing.json.error.code, "unauthorized")

  const wrong = await request("/v1/sessions", { method: "POST", token: "nope", body: {} })
  assert.equal(wrong.status, 401)
  assert.equal(wrong.json.error.code, "unauthorized")
})

test("POST /v1/sessions creates a session and GET lists it", async () => {
  const metadata = { case_run_id: "42", sut_id: "7", account_role: "admin" }
  const created = await createSession({ metadata })
  assert.ok(created.sessionId)
  try {
    const listed = await request("/v1/sessions")
    assert.equal(listed.status, 200)
    const entry = listed.json.sessions.find((session) => session.sessionId === created.sessionId)
    assert.ok(entry, `session ${created.sessionId} is missing from GET /v1/sessions`)
    assert.equal(entry.key, "run:42:sut:7:role:admin")
    assert.deepEqual(entry.metadata, metadata)
  } finally {
    await request(`/v1/sessions/${created.sessionId}`, { method: "DELETE" })
  }
})

test("GET /v1/sessions requires the bearer token", async () => {
  const missing = await request("/v1/sessions", { token: undefined })
  assert.equal(missing.status, 401)
})

test("POST /v1/sessions rejects metadata over 2KB and non-object metadata", async () => {
  const oversized = await request("/v1/sessions", {
    method: "POST",
    body: {
      workspace: path.join(workspaceRoot, "meta-big"),
      metadata: { blob: "x".repeat(3000) },
    },
  })
  assert.equal(oversized.status, 400)
  assert.equal(oversized.json.error.code, "invalid_arguments")

  const notObject = await request("/v1/sessions", {
    method: "POST",
    body: { workspace: path.join(workspaceRoot, "meta-bad"), metadata: ["nope"] },
  })
  assert.equal(notObject.status, 400)
  assert.equal(notObject.json.error.code, "invalid_arguments")
})

test("unknown sessions return 404 session_not_found", async () => {
  const network = await request("/v1/sessions/sess_missing/network")
  assert.equal(network.status, 404)
  assert.equal(network.json.error.code, "session_not_found")

  const cancelled = await request("/v1/sessions/sess_missing/cancel", { method: "POST" })
  assert.equal(cancelled.status, 404)

  const deleted = await request("/v1/sessions/sess_missing", { method: "DELETE" })
  assert.equal(deleted.status, 404)
})

test("malformed JSON is rejected with 400 invalid_request", async () => {
  const response = await request("/v1/sessions", { method: "POST", raw: "{not-json" })
  assert.equal(response.status, 400)
  assert.equal(response.json.error.code, "invalid_request")
})

test("request bodies over 1MB are rejected with 413 too_large", async () => {
  const oversized = JSON.stringify({ workspace: "/tmp/ws", metadata: { blob: "x".repeat(MAX_REQUEST_BYTES) } })
  const response = await request("/v1/sessions", { method: "POST", raw: oversized })
  assert.equal(response.status, 413)
  assert.equal(response.json.error.code, "too_large")
})

test("DELETE closes the session; cancel reports no in-flight work", async () => {
  const created = await createSession()

  const cancelled = await request(`/v1/sessions/${created.sessionId}/cancel`, { method: "POST" })
  assert.equal(cancelled.status, 200)
  assert.deepEqual(cancelled.json.result.cancelled, [])

  const deleted = await request(`/v1/sessions/${created.sessionId}`, { method: "DELETE" })
  assert.equal(deleted.status, 200)

  // The session (and its traffic read surface) is gone after delete.
  const after = await request(`/v1/sessions/${created.sessionId}/network`)
  assert.equal(after.status, 404)
})

test("unknown routes and methods are rejected", async () => {
  const missing = await request("/v1/nope")
  assert.equal(missing.status, 404)
  const wrongMethod = await request("/v1/sessions", { method: "PUT" })
  assert.equal(wrongMethod.status, 405)
})

test("startFromEnv ignores the legacy EZTEST_EXEC_ALLOWED_HOSTS variable (GD-01)", async () => {
  // Reserve an ephemeral port, then let startFromEnv bind it.
  const probe = http.createServer()
  probe.listen(0, "127.0.0.1")
  await new Promise((resolve) => probe.once("listening", resolve))
  const { port } = probe.address()
  await new Promise((resolve) => probe.close(resolve))

  const started = await startFromEnv({
    EZTEST_EXECUTOR_TOKEN: "env-test-token",
    EZTEST_EXECUTOR_ADDR: `127.0.0.1:${port}`,
    EZTEST_EXEC_ALLOWED_HOSTS: "app.example.test, *.internal.test",
    EZTEST_EXECUTOR_STATE_DIR: path.join(workspaceRoot, "state"),
    EZTEST_EXECUTOR_RECLAIM_INTERVAL_MS: "0",
  })
  assert.ok(started, "startFromEnv returned null for a valid environment")
  try {
    // The executor-wide allow list is gone; the env var has no effect. Session
    // capacity has its own knob: the v1 max-contexts fallback is gone.
    assert.equal(started.sessions.maxSessions, 8)
  } finally {
    await new Promise((resolve) => started.server.close(resolve))
    await started.sessions.close()
  }
})

async function startProtocolServer(logger = null) {
  const protocolSessions = new SessionManager({ logger: null, reclaimIntervalMs: 0 })
  const protocolServer = createExecutorServer({
    token: TOKEN,
    sessions: protocolSessions,
    logger,
    version: "test",
  })
  protocolServer.listen(0, "127.0.0.1")
  await new Promise((resolve) => protocolServer.once("listening", resolve))
  const { port } = protocolServer.address()
  return {
    sessions: protocolSessions,
    request: httpClient(`http://127.0.0.1:${port}`, TOKEN),
    async close() {
      await protocolSessions.close()
      await new Promise((resolve) => protocolServer.close(resolve))
    },
  }
}

// v0.2.3 X-01: every executor request is logged at start and finish with the
// caller's x-ezt-test-request-id, so an API-side timeout can be matched to the
// executor's own log line.
test("request start and finish logs carry the x-ezt-test-request-id", async () => {
  const requestId = "req_0123456789abcdef"
  const lines = []
  let resolveFinish
  const finished = new Promise((resolve) => {
    resolveFinish = resolve
  })
  const record = (line) => {
    const text = String(line)
    lines.push(text)
    if (text.includes("request finish") && text.includes(requestId)) {
      resolveFinish()
    }
  }
  const logger = { info: record, warn: record, error: record }
  const protocol = await startProtocolServer(logger)
  try {
    const response = await protocol.request("/healthz", {
      token: undefined,
      headers: { "x-ezt-test-request-id": requestId },
    })
    assert.equal(response.status, 200, response.text)
    // The finish line is emitted when the response is fully flushed; awaiting
    // it removes the previous sleep without racing the logger.
    await finished
    assert.ok(
      lines.some((line) => line.includes("request start") && line.includes(requestId)),
      `missing start log with the request id:\n${lines.join("\n")}`,
    )
    assert.ok(
      lines.some((line) => line.includes("request finish") && line.includes(requestId)),
      `missing finish log with the request id:\n${lines.join("\n")}`,
    )
  } finally {
    await protocol.close()
  }
})
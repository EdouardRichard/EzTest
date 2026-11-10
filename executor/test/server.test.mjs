import test, { after, before } from "node:test"
import assert from "node:assert/strict"
import { EventEmitter } from "node:events"
import http from "node:http"

import { ContextManager } from "../src/contexts.mjs"
import {
  createExecutorServer,
  parseContextCreateTimeout,
  readBody,
  startFromEnv,
} from "../src/server.mjs"
import { ExecutorError, MAX_REQUEST_BYTES } from "../src/security.mjs"
import { httpClient, startLocalPage } from "./helpers.mjs"

const TOKEN = "test-executor-token"

let pageServer
let manager
let server
let request

before(async () => {
  pageServer = await startLocalPage()
  manager = new ContextManager({ maxContexts: 1, logger: null })
  server = createExecutorServer({ token: TOKEN, manager, logger: null, version: "test" })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  request = httpClient(`http://127.0.0.1:${port}`, TOKEN)
})

after(async () => {
  await manager.close()
  await new Promise((resolve) => server.close(resolve))
  await pageServer.close()
})

async function createContext(overrides = {}) {
  const response = await request("/v1/contexts", {
    method: "POST",
    body: {
      key: "server:test",
      allowedHosts: ["127.0.0.1"],
      baseUrl: pageServer.url,
      viewport: { width: 1280, height: 720 },
      ...overrides,
    },
  })
  assert.equal(response.status, 200, response.text)
  return response.json
}

test("GET /healthz is reachable without a token", async () => {
  const response = await request("/healthz", { token: undefined })
  assert.equal(response.status, 200)
  assert.equal(response.json.status, "ok")
  assert.equal(response.json.version, "test")
  assert.equal(response.json.maxContexts, 1)
  assert.equal(typeof response.json.contexts, "number")
})

test("healthz reports degraded 503 when the browser cannot launch", async () => {
  let launchAttempts = 0
  const failing = new ContextManager({
    maxContexts: 1,
    logger: null,
    launch: async () => {
      launchAttempts += 1
      throw new Error("browser unavailable")
    },
  })
  const degradedServer = createExecutorServer({
    token: TOKEN,
    manager: failing,
    logger: null,
    version: "test",
  })
  degradedServer.listen(0, "127.0.0.1")
  await new Promise((resolve) => degradedServer.once("listening", resolve))
  const { port } = degradedServer.address()
  const degraded = httpClient(`http://127.0.0.1:${port}`, TOKEN)
  try {
    const first = await degraded("/healthz", { token: undefined })
    assert.equal(first.status, 503)
    assert.equal(first.json.status, "degraded")
    // The readiness probe result is cached, so repeated probes do not retry.
    const second = await degraded("/healthz", { token: undefined })
    assert.equal(second.status, 503)
    assert.equal(launchAttempts, 1)
  } finally {
    await new Promise((resolve) => degradedServer.close(resolve))
    await failing.close()
  }
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
      path: "/v1/contexts",
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

test("v1 endpoints reject missing or wrong bearer tokens", async () => {
  const missing = await request("/v1/contexts", { method: "POST", token: undefined, body: {} })
  assert.equal(missing.status, 401)
  assert.equal(missing.json.error.code, "unauthorized")

  const wrong = await request("/v1/contexts", { method: "POST", token: "nope", body: {} })
  assert.equal(wrong.status, 401)
  assert.equal(wrong.json.error.code, "unauthorized")
})

test("POST /v1/contexts creates a context", async () => {
  const created = await createContext()
  assert.match(created.contextId, /^ctx_/)
  assert.equal(typeof created.browserVersion, "string")
  assert.ok(created.browserVersion.length > 0)
  await request(`/v1/contexts/${created.contextId}`, { method: "DELETE" })
})

test("actions run through the HTTP envelope", async () => {
  const created = await createContext()
  try {
    const goto = await request(`/v1/contexts/${created.contextId}/actions`, {
      method: "POST",
      body: { action: "goto", args: { url: `${pageServer.url}/home` } },
    })
    assert.equal(goto.status, 200)
    assert.equal(goto.json.ok, true)
    assert.equal(goto.json.result.status, 200)

    const screenshot = await request(`/v1/contexts/${created.contextId}/actions`, {
      method: "POST",
      body: { action: "screenshot", args: { full_page: false } },
    })
    assert.equal(screenshot.status, 200)
    assert.equal(screenshot.json.ok, true)
    assert.equal(screenshot.json.artifacts[0].mime, "image/png")
    assert.match(screenshot.json.artifacts[0].sha256, /^[0-9a-f]{64}$/)
  } finally {
    await request(`/v1/contexts/${created.contextId}`, { method: "DELETE" })
  }
})

test("unknown actions return 400 invalid_arguments", async () => {
  const created = await createContext()
  try {
    const response = await request(`/v1/contexts/${created.contextId}/actions`, {
      method: "POST",
      body: { action: "teleport", args: {} },
    })
    assert.equal(response.status, 400)
    assert.equal(response.json.ok, false)
    assert.equal(response.json.error.code, "invalid_arguments")
  } finally {
    await request(`/v1/contexts/${created.contextId}`, { method: "DELETE" })
  }
})

test("unknown contexts return 404 context_not_found", async () => {
  const action = await request("/v1/contexts/ctx_missing/actions", {
    method: "POST",
    body: { action: "snapshot", args: {} },
  })
  assert.equal(action.status, 404)
  assert.equal(action.json.error.code, "context_not_found")

  const canceled = await request("/v1/contexts/ctx_missing/cancel", { method: "POST" })
  assert.equal(canceled.status, 404)

  const deleted = await request("/v1/contexts/ctx_missing", { method: "DELETE" })
  assert.equal(deleted.status, 404)
})

test("malformed JSON is rejected with 400 invalid_request", async () => {
  const response = await request("/v1/contexts", { method: "POST", raw: "{not-json" })
  assert.equal(response.status, 400)
  assert.equal(response.json.error.code, "invalid_request")
})

test("request bodies over 1MB are rejected with 413 too_large", async () => {
  const oversized = "x".repeat(MAX_REQUEST_BYTES + 1024)
  const response = await request("/v1/contexts", { method: "POST", raw: oversized })
  assert.equal(response.status, 413)
  assert.equal(response.json.error.code, "too_large")
})

test("action bodies over 1MB are rejected unless the action is visual.compare", async () => {
  const created = await createContext()
  try {
    const oversized = {
      action: "snapshot",
      args: { pad: "x".repeat(MAX_REQUEST_BYTES + 1024) },
    }
    const rejected = await request(`/v1/contexts/${created.contextId}/actions`, {
      method: "POST",
      body: oversized,
    })
    assert.equal(rejected.status, 413)
    assert.equal(rejected.json.error.code, "too_large")
  } finally {
    await request(`/v1/contexts/${created.contextId}`, { method: "DELETE" })
  }
})

test("visual.compare keeps the larger visual action body limit", async () => {
  let received = null
  const fakeManager = {
    runAction: async (_contextId, body) => {
      received = body
      return { ok: true, result: {} }
    },
  }
  const visualServer = createExecutorServer({
    token: TOKEN,
    manager: fakeManager,
    logger: null,
    version: "test",
  })
  visualServer.listen(0, "127.0.0.1")
  await new Promise((resolve) => visualServer.once("listening", resolve))
  const { port } = visualServer.address()
  const call = httpClient(`http://127.0.0.1:${port}`, TOKEN)
  try {
    const body = {
      action: "visual.compare",
      args: { pad: "x".repeat(MAX_REQUEST_BYTES + 1024) },
    }
    const response = await call("/v1/contexts/ctx_visual/actions", {
      method: "POST",
      body,
    })
    assert.equal(response.status, 200, response.text)
    assert.equal(received.action, "visual.compare")
  } finally {
    await new Promise((resolve) => visualServer.close(resolve))
  }
})

test("context limit rejects additional contexts", async () => {
  const created = await createContext()
  try {
    const overLimit = await request("/v1/contexts", {
      method: "POST",
      body: {
        key: "server:test:second",
        allowedHosts: ["127.0.0.1"],
        baseUrl: pageServer.url,
      },
    })
    assert.equal(overLimit.status, 409)
    assert.equal(overLimit.json.error.code, "context_limit")
  } finally {
    await request(`/v1/contexts/${created.contextId}`, { method: "DELETE" })
  }
})

test("DELETE closes the context and cancel reports no in-flight action", async () => {
  const created = await createContext()
  await request(`/v1/contexts/${created.contextId}/actions`, {
    method: "POST",
    body: { action: "goto", args: { url: `${pageServer.url}/home` } },
  })
  const canceled = await request(`/v1/contexts/${created.contextId}/cancel`, { method: "POST" })
  assert.equal(canceled.status, 200)
  assert.equal(canceled.json.result.canceled, false)

  const deleted = await request(`/v1/contexts/${created.contextId}`, { method: "DELETE" })
  assert.equal(deleted.status, 200)
  assert.equal(deleted.json.ok, true)

  const after = await request(`/v1/contexts/${created.contextId}/actions`, {
    method: "POST",
    body: { action: "snapshot", args: {} },
  })
  assert.equal(after.status, 404)
})

test("unknown routes and methods are rejected", async () => {
  const missing = await request("/nope")
  assert.equal(missing.status, 404)

  const wrongMethod = await request("/v1/contexts", { method: "PUT" })
  assert.equal(wrongMethod.status, 405)
})

test("POST /v1/contexts saves metadata and GET /v1/contexts lists contexts", async () => {
  const metadata = {
    case_run_id: 42,
    version_sut_id: 7,
    account_role: "admin",
    dispatch_epoch: 3,
    dispatch_token: "dispatch-token",
  }
  const created = await createContext({ key: "run:42:vs:7:role:admin", metadata })
  try {
    const listed = await request("/v1/contexts")
    assert.equal(listed.status, 200, listed.text)
    assert.equal(Array.isArray(listed.json.contexts), true)
    const entry = listed.json.contexts.find((context) => context.id === created.contextId)
    assert.ok(entry, `context ${created.contextId} is missing from GET /v1/contexts`)
    assert.equal(entry.key, "run:42:vs:7:role:admin")
    assert.deepEqual(entry.metadata, metadata)
    assert.equal(typeof entry.createdAt, "string")
    assert.ok(Number.isFinite(Date.parse(entry.createdAt)))
  } finally {
    await request(`/v1/contexts/${created.contextId}`, { method: "DELETE" })
  }
})

test("GET /v1/contexts requires the bearer token", async () => {
  const missing = await request("/v1/contexts", { token: undefined })
  assert.equal(missing.status, 401)
  assert.equal(missing.json.error.code, "unauthorized")
})

test("POST /v1/contexts rejects metadata over 2KB and non-object metadata", async () => {
  const oversized = await request("/v1/contexts", {
    method: "POST",
    body: {
      key: "server:metadata",
      allowedHosts: ["127.0.0.1"],
      baseUrl: pageServer.url,
      metadata: { blob: "x".repeat(3000) },
    },
  })
  assert.equal(oversized.status, 400)
  assert.equal(oversized.json.error.code, "invalid_arguments")

  const notObject = await request("/v1/contexts", {
    method: "POST",
    body: {
      key: "server:metadata",
      allowedHosts: ["127.0.0.1"],
      baseUrl: pageServer.url,
      metadata: ["not", "an", "object"],
    },
  })
  assert.equal(notObject.status, 400)
  assert.equal(notObject.json.error.code, "invalid_arguments")
})

test("startFromEnv wires EZTEST_EXEC_ALLOWED_HOSTS into the context manager", async () => {
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
  })
  assert.ok(started, "startFromEnv returned null for a valid environment")
  try {
    assert.deepEqual(started.manager.allowedHosts, ["app.example.test", "*.internal.test"])
  } finally {
    await new Promise((resolve) => started.server.close(resolve))
    await started.manager.close()
  }
})

// A fake manager keeps these protocol-level tests off a real browser.
function fakeManager(overrides = {}) {
  return {
    size: 0,
    maxContexts: 8,
    list: () => [],
    async health() {
      return { status: "ok" }
    },
    async close() {},
    ...overrides,
  }
}

async function startProtocolServer(manager, logger = null) {
  const protocolServer = createExecutorServer({
    token: TOKEN,
    manager,
    logger,
    version: "test",
  })
  protocolServer.listen(0, "127.0.0.1")
  await new Promise((resolve) => protocolServer.once("listening", resolve))
  const { port } = protocolServer.address()
  return {
    request: httpClient(`http://127.0.0.1:${port}`, TOKEN),
    close: () => new Promise((resolve) => protocolServer.close(resolve)),
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
  const protocol = await startProtocolServer(fakeManager(), logger)
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

test("healthz exposes creating and lastContextCreateError", async () => {
  const lastContextCreateError = "executor_timeout: context creation exceeded its hard deadline of 30000ms"
  const protocol = await startProtocolServer(
    fakeManager({
      size: 1,
      async health() {
        return { status: "ok", browserVersion: "fake-2", creating: 1, lastContextCreateError }
      },
    }),
  )
  try {
    const response = await protocol.request("/healthz", { token: undefined })
    assert.equal(response.status, 200, response.text)
    assert.equal(response.json.creating, 1)
    assert.equal(response.json.lastContextCreateError, lastContextCreateError)
  } finally {
    await protocol.close()
  }
})

// /healthz is reachable without a bearer token, so a create-failure diagnostic
// must never expose URL credentials, bearer tokens or secret query parameters.
test("healthz scrubs credentials from lastContextCreateError", async () => {
  const raw =
    "context create failed: https://alice:s3cret@example.test/app?token=tok_live_123&inert=1 with Bearer abc.def.ghi"
  const protocol = await startProtocolServer(
    fakeManager({
      async health() {
        return { status: "ok", creating: 0, lastContextCreateError: raw }
      },
    }),
  )
  try {
    const response = await protocol.request("/healthz", { token: undefined })
    assert.equal(response.status, 200, response.text)
    const exposed = String(response.json.lastContextCreateError ?? "")
    assert.doesNotMatch(exposed, /s3cret|tok_live_123|abc\.def\.ghi|alice:/)
    assert.match(exposed, /https:\/\/\*\*\*@example\.test/)
    assert.match(exposed, /inert=1/)
  } finally {
    await protocol.close()
  }
})

test("POST /v1/contexts propagates a typed executor_timeout", async () => {
  const protocol = await startProtocolServer(
    fakeManager({
      async createContext() {
        throw new ExecutorError(
          "executor_timeout",
          "context creation exceeded its hard deadline of 30000ms",
        )
      },
    }),
  )
  try {
    const response = await protocol.request("/v1/contexts", {
      method: "POST",
      body: { allowedHosts: ["127.0.0.1"] },
    })
    assert.equal(response.status, 504, response.text)
    assert.equal(response.json.error.code, "executor_timeout")
  } finally {
    await protocol.close()
  }
})

test("parseContextCreateTimeout applies the 30s default", () => {
  assert.equal(parseContextCreateTimeout(undefined), 30_000)
  assert.equal(parseContextCreateTimeout("1500"), 1500)
  assert.equal(parseContextCreateTimeout("nope"), 30_000)
  assert.equal(parseContextCreateTimeout("-5"), 30_000)
})

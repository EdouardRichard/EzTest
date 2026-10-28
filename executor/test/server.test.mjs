import test, { after, before } from "node:test"
import assert from "node:assert/strict"
import { EventEmitter } from "node:events"
import http from "node:http"

import { ContextManager } from "../src/contexts.mjs"
import { createExecutorServer, readBody } from "../src/server.mjs"
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

  const wrongMethod = await request("/v1/contexts", { method: "GET" })
  assert.equal(wrongMethod.status, 405)
})

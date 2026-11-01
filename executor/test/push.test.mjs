import test from "node:test"
import assert from "node:assert/strict"
import http from "node:http"
import { once } from "node:events"

import {
  createPushClient,
  executorEndpointFromAddress,
  parseHeartbeatSeconds,
  parsePushConfig,
  startPushFromEnv,
} from "../src/push.mjs"
import { startFromEnv } from "../src/server.mjs"

const TOKEN = "executor-push-token"

function readRequestBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = []
    req.on("data", (chunk) => chunks.push(chunk))
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")))
    req.on("error", reject)
  })
}

/**
 * Local EzTest API stand-in. `handler(request, response, index)` may override
 * the default 200 JSON response.
 */
async function startApi(handler = null) {
  const requests = []
  const server = http.createServer(async (req, res) => {
    const body = await readRequestBody(req)
    const record = {
      method: req.method,
      path: req.url,
      authorization: req.headers.authorization ?? "",
      body: body ? JSON.parse(body) : null,
    }
    requests.push(record)
    if (handler) {
      const handled = await handler(req, res, record, requests.length)
      if (handled !== false) {
        return
      }
    }
    res.writeHead(200, { "content-type": "application/json" })
    res.end(JSON.stringify({ data: { ok: true } }))
  })
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  const { port } = server.address()
  return {
    url: `http://127.0.0.1:${port}`,
    requests,
    async close() {
      server.closeAllConnections?.()
      await new Promise((resolve) => server.close(resolve))
    },
  }
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function waitFor(predicate, { timeoutMs = 3000, intervalMs = 10, label = "condition" } = {}) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) {
      return
    }
    await delay(intervalMs)
  }
  throw new Error(`timed out waiting for ${label}`)
}

function requestsFor(requests, suffix) {
  return requests.filter((request) => request.path === suffix)
}

test("push client registers then heartbeats capacity and active contexts", async () => {
  const api = await startApi()
  const logs = []
  let state = { activeContexts: 2, capacity: 8, status: "ok" }
  const client = createPushClient({
    apiUrl: api.url,
    token: TOKEN,
    endpoint: "http://127.0.0.1:13336",
    name: "executor-test",
    version: "0.2.0-p7",
    snapshot: () => state,
    heartbeatSeconds: 0.05,
    logger: { info: (line) => logs.push(line), warn: (line) => logs.push(line) },
  })
  try {
    client.start()
    await waitFor(
      () =>
        requestsFor(api.requests, "/api/internal/executors/register").length === 1 &&
        requestsFor(api.requests, "/api/internal/executors/heartbeat").length >= 1,
      { label: "register + heartbeat" },
    )
    const register = requestsFor(api.requests, "/api/internal/executors/register")[0]
    assert.equal(register.method, "POST")
    assert.equal(register.authorization, `Bearer ${TOKEN}`)
    assert.deepEqual(register.body, {
      endpoint: "http://127.0.0.1:13336",
      name: "executor-test",
      capacity: 8,
      active_contexts: 2,
      version: "0.2.0-p7",
    })

    const heartbeat = requestsFor(api.requests, "/api/internal/executors/heartbeat")[0]
    assert.equal(heartbeat.authorization, `Bearer ${TOKEN}`)
    assert.deepEqual(heartbeat.body, {
      endpoint: "http://127.0.0.1:13336",
      active_contexts: 2,
      capacity: 8,
      version: "0.2.0-p7",
      status: "active",
    })

    // The snapshot is read again for each heartbeat.
    state = { activeContexts: 5, capacity: 8, status: "ok" }
    await waitFor(
      () => requestsFor(api.requests, "/api/internal/executors/heartbeat").some((request) => request.body.active_contexts === 5),
      { label: "updated heartbeat" },
    )
  } finally {
    client.stop()
    const countAtStop = api.requests.length
    await delay(150)
    assert.equal(api.requests.length, countAtStop, "stop must cancel the heartbeat timer")
    await api.close()
  }
  assert.ok(!logs.join("\n").includes(TOKEN), "logs must never contain the token")
})

test("push client sends degraded status and never crashes on 5xx", async () => {
  const api = await startApi((req, res, request, index) => {
    if (request.path.endsWith("/register") && index <= 2) {
      res.writeHead(500, { "content-type": "text/plain" })
      res.end("boom")
      return true
    }
    return false
  })
  const logs = []
  const client = createPushClient({
    apiUrl: api.url,
    token: TOKEN,
    endpoint: "http://127.0.0.1:13336",
    name: "executor-test",
    version: "0.2.0-p7",
    snapshot: () => ({ activeContexts: 0, capacity: 4, status: "degraded" }),
    heartbeatSeconds: 0.02,
    backoffMs: 10,
    maxBackoffMs: 40,
    logger: { info: (line) => logs.push(line), warn: (line) => logs.push(line) },
  })
  try {
    client.start()
    await waitFor(() => requestsFor(api.requests, "/api/internal/executors/register").length >= 3, {
      label: "register retries",
    })
    await waitFor(
      () => requestsFor(api.requests, "/api/internal/executors/heartbeat").length >= 1,
      { label: "heartbeat after recovery" },
    )
    const heartbeat = requestsFor(api.requests, "/api/internal/executors/heartbeat")[0]
    assert.equal(heartbeat.body.status, "degraded")
    assert.ok(logs.some((line) => /register failed/i.test(line)), "the failed register must be logged")
  } finally {
    client.stop()
    await api.close()
  }
  assert.ok(!logs.join("\n").includes(TOKEN), "logs must never contain the token")
})

test("push configuration is disabled without EZTEST_API_URL", () => {
  assert.equal(parsePushConfig({}), null)
  assert.equal(parsePushConfig({ EZTEST_API_URL: "   ", EZTEST_EXECUTOR_TOKEN: TOKEN }), null)
  assert.equal(parsePushConfig({ EZTEST_API_URL: "http://127.0.0.1:13333" }), null, "token is required")

  assert.deepEqual(
    parsePushConfig({
      EZTEST_API_URL: "http://127.0.0.1:13333/",
      EZTEST_EXECUTOR_TOKEN: TOKEN,
      EZTEST_EXECUTOR_HEARTBEAT_SECONDS: "3",
    }),
    { apiUrl: "http://127.0.0.1:13333", token: TOKEN, heartbeatSeconds: 3 },
  )
  assert.equal(parseHeartbeatSeconds("0"), 10)
  assert.equal(parseHeartbeatSeconds("abc"), 10)
  assert.equal(parseHeartbeatSeconds("120"), 60)
  assert.equal(parseHeartbeatSeconds("2.9"), 2)
})

test("executor endpoint is derived from the listen address", () => {
  assert.equal(executorEndpointFromAddress("127.0.0.1:13336"), "http://127.0.0.1:13336")
  assert.equal(executorEndpointFromAddress("0.0.0.0:13336"), "http://127.0.0.1:13336")
  assert.equal(executorEndpointFromAddress("[::]:13336"), "http://127.0.0.1:13336")
  assert.equal(executorEndpointFromAddress("executor:13336"), "http://executor:13336")
  assert.equal(executorEndpointFromAddress("https://executor:13336"), "https://executor:13336")
  assert.equal(executorEndpointFromAddress("not-an-address"), null)
})

test("startPushFromEnv reads manager contexts and health", async () => {
  const api = await startApi()
  const manager = {
    size: 1,
    maxContexts: 4,
    health: async () => ({ status: "degraded", error: "browser unavailable" }),
  }
  const client = startPushFromEnv({
    env: {
      EZTEST_API_URL: api.url,
      EZTEST_EXECUTOR_TOKEN: TOKEN,
      EZTEST_EXECUTOR_ADDR: "127.0.0.1:13337",
      EZTEST_EXECUTOR_HEARTBEAT_SECONDS: "1",
    },
    manager,
    version: "0.2.0-p7",
    logger: null,
  })
  assert.ok(client, "startPushFromEnv returned null with a valid config")
  try {
    await waitFor(() => requestsFor(api.requests, "/api/internal/executors/register").length === 1, {
      label: "env register",
    })
    const register = requestsFor(api.requests, "/api/internal/executors/register")[0]
    assert.equal(register.body.endpoint, "http://127.0.0.1:13337")
    assert.equal(register.body.capacity, 4)
    assert.equal(register.body.active_contexts, 1)
  } finally {
    client.stop()
    await api.close()
  }

  assert.equal(
    startPushFromEnv({
      env: {},
      manager,
      version: "0.2.0-p7",
      logger: null,
    }),
    null,
  )
})

test("startFromEnv wires the optional push client", async () => {
  const api = await startApi()
  const probe = http.createServer()
  probe.listen(0, "127.0.0.1")
  await once(probe, "listening")
  const { port } = probe.address()
  await new Promise((resolve) => probe.close(resolve))

  const manager = {
    size: 0,
    maxContexts: 3,
    health: async () => ({ status: "ok" }),
    close: async () => {},
  }
  const started = await startFromEnv(
    {
      EZTEST_EXECUTOR_TOKEN: TOKEN,
      EZTEST_EXECUTOR_ADDR: `127.0.0.1:${port}`,
      EZTEST_API_URL: api.url,
    },
    { manager },
  )
  assert.ok(started, "startFromEnv returned null for a valid environment")
  try {
    await waitFor(() => requestsFor(api.requests, "/api/internal/executors/register").length === 1, {
      label: "server-wired register",
    })
    const register = requestsFor(api.requests, "/api/internal/executors/register")[0]
    assert.equal(register.authorization, `Bearer ${TOKEN}`)
    assert.equal(register.body.endpoint, `http://127.0.0.1:${port}`)
    assert.equal(register.body.capacity, 3)
  } finally {
    started.push?.stop()
    await new Promise((resolve) => started.server.close(resolve))
    await started.manager.close()
    await api.close()
  }
})

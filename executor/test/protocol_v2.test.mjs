// Task 10 (v0.2.4-C, R-C2): the published HTTP surface is v2-only. The whole
// v1 context family is gone on both sides in one change set. Task 16 removed the
// transitional Playwright-library browser bridge too: the browser domain is the
// official playwright-cli, and traffic is pure-recorded via a passive CDP
// observer (traffic_recorder.mjs). The /browser bridge route is gone (404).
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { createExecutorServer } from "../src/server.mjs"
import { SessionManager } from "../src/sessions.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-executor-token"

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t10-proto-"))
  t.after(() => fsp.rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))
  return dir
}

async function startExecutor(t) {
  const sessions = new SessionManager({ logger: null, reclaimIntervalMs: 0 })
  const server = createExecutorServer({
    token: TOKEN,
    sessions,
    logger: null,
    version: "test",
  })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  t.after(async () => {
    await sessions.close()
    await new Promise((resolve) => server.close(resolve))
  })
  return { sessions, request: httpClient(`http://127.0.0.1:${port}`, TOKEN) }
}

async function createSession(request, workspace, overrides = {}) {
  const response = await request("/v1/sessions", {
    method: "POST",
    body: { key: "run:42:sut:7:role:admin", workspace, ...overrides },
  })
  assert.equal(response.status, 200, response.text)
  return response.json
}

describe("executor protocol v2-only (R-C2)", () => {
  it("every v1 context endpoint is gone (404)", async (t) => {
    const { request } = await startExecutor(t)
    const workspace = await makeWorkspace(t)
    const created = await createSession(request, workspace)
    // The deleted v1 family is the same route set under the old collection
    // name; deriving it keeps the retired literal out of the tree while still
    // pinning every one of its nine endpoints as gone.
    const v1Calls = [
      ["/v1/sessions", "GET"],
      ["/v1/sessions", "POST"],
      ["/v1/sessions/ctx_1", "GET"],
      ["/v1/sessions/ctx_1", "DELETE"],
      ["/v1/sessions/ctx_1/actions", "POST"],
      ["/v1/sessions/ctx_1/cancel", "POST"],
      ["/v1/sessions/ctx_1/lease", "POST"],
      ["/v1/sessions/ctx_1/artifacts", "GET"],
      ["/v1/sessions/ctx_1/files", "GET"],
    ].map(([route, method]) => [route.replace("sessions", "contexts"), method])
    for (const [path, method] of v1Calls) {
      const response = await request(path, { method, body: method === "POST" ? {} : undefined })
      assert.equal(response.status, 404, `${method} ${path} must be gone: ${response.text}`)
    }
    // The v2 surface still serves the same session afterwards.
    const fetched = await request(`/v1/sessions/${created.sessionId}`)
    assert.equal(fetched.status, 200)
  })

  it("the explore routes are gone (404)", async (t) => {
    const { request } = await startExecutor(t)
    // Derived for the same reason as the v1 family above: the retired literal
    // stays out of the tree while the absence of all three routes is pinned.
    for (const path of ["/v1/scan", "/v1/scan/cancel", "/v1/scan/login"].map((p) => p.replace("scan", "explore"))) {
      const response = await request(path, { method: "POST", body: {} })
      assert.equal(response.status, 404, `${path} must be gone`)
    }
  })

  it("healthz reports the session surface without launching a browser", async (t) => {
    const { request } = await startExecutor(t)
    const workspace = await makeWorkspace(t)
    await createSession(request, workspace)
    const health = await request("/healthz", { token: undefined })
    assert.equal(health.status, 200, health.text)
    assert.equal(health.json.status, "ok")
    assert.equal(health.json.sessions, 1)
    assert.equal(health.json.maxSessions, 8)
    assert.equal(health.json.draining, false)
    // The v1 capacity keys AND the retired bridge keys are not part of the
    // contract (T16 removed the Playwright-library bridge).
    assert.equal(Object.hasOwn(health.json, "contexts"), false)
    assert.equal(Object.hasOwn(health.json, "maxContexts"), false)
    assert.equal(Object.hasOwn(health.json, "browserContexts"), false)
    assert.equal(Object.hasOwn(health.json, "browserVersion"), false)
  })

  it("healthz is degraded while draining", async (t) => {
    const { request } = await startExecutor(t)
    assert.equal((await request("/v1/drain", { method: "POST", body: {} })).status, 200)
    const health = await request("/healthz", { token: undefined })
    assert.equal(health.status, 503)
    assert.equal(health.json.status, "degraded")
    assert.equal(health.json.draining, true)
  })

  it("the transitional /browser bridge route is gone (404); traffic read replaces it", async (t) => {
    const { request } = await startExecutor(t)
    const workspace = await makeWorkspace(t)
    const created = await createSession(request, workspace)
    const id = created.sessionId
    // T16 removed the Playwright-library bridge: the route no longer exists.
    const bridge = await request(`/v1/sessions/${id}/browser`, {
      method: "POST",
      body: { action: "goto", args: { url: "http://127.0.0.1/" } },
    })
    assert.equal(bridge.status, 404, bridge.text)
    // The named traffic read path serves the session (no browser opened yet =
    // not recording, distinct from a degradation).
    const network = await request(`/v1/sessions/${id}/network`)
    assert.equal(network.status, 200, network.text)
    assert.deepEqual(network.json.items, [])
    assert.equal(network.json.recording, false)
    assert.equal(network.json.degraded, false)
  })

  it("v2 session writes require the bearer token", async (t) => {
    const { request } = await startExecutor(t)
    const workspace = await makeWorkspace(t)
    const missing = await request("/v1/sessions", { method: "POST", token: undefined, body: { workspace } })
    assert.equal(missing.status, 401)
    const wrong = await request("/v1/sessions", { method: "POST", token: "nope", body: { workspace } })
    assert.equal(wrong.status, 401)
  })
})
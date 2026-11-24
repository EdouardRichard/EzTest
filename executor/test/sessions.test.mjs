// Task 7 (v0.2.4-C): SessionManager core CRUD + workspace anchor + lease metadata.
// Lifecycle refinements (capacity/429/drain/fencing) belong to Task 9.
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { SessionManager } from "../src/sessions.mjs"
import { ExecutorError } from "../src/protocol.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-executor-token"

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t7-sess-"))
  t.after(() => fsp.rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))
  return dir
}

function makeManager(t) {
  const manager = new SessionManager({ logger: null })
  t.after(() => manager.close())
  return manager
}

async function waitFor(fn, { timeoutMs = 15000, intervalMs = 50 } = {}) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const value = await fn()
    if (value) {
      return value
    }
    if (Date.now() > deadline) {
      throw new Error("waitFor timed out")
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs))
  }
}

function pidAlive(pid) {
  try {
    process.kill(pid, 0)
    return true
  } catch (error) {
    return error.code === "EPERM"
  }
}

describe("SessionManager core", () => {
  it("creates a session with a workspace anchor and returns sessionId", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t)
    const workspace = path.join(root, "nested", "case-run-1")
    const created = await manager.create({
      key: "case_run:42",
      workspace,
      metadata: { case_run_id: 42 },
      lease: { lease_epoch: 7, ttl_ms: 300000 },
    })
    assert.equal(typeof created.sessionId, "string")
    assert.ok(created.sessionId.length > 0)
    assert.equal(path.resolve(created.workspace), path.resolve(workspace))
    // The workspace directory is created (anchor), including missing parents.
    const stat = await fsp.stat(workspace)
    assert.ok(stat.isDirectory())
    // Key/metadata/lease are stored verbatim (fencing enforcement is Task 9).
    const fetched = manager.get(created.sessionId)
    assert.equal(fetched.key, "case_run:42")
    assert.deepEqual(fetched.metadata, { case_run_id: 42 })
    assert.deepEqual(fetched.lease, { lease_epoch: 7, ttl_ms: 300000 })
  })

  it("lists sessions and deletes them", async (t) => {
    const workspace = await makeWorkspace(t)
    const manager = makeManager(t)
    const a = await manager.create({ workspace: path.join(workspace, "a") })
    const b = await manager.create({ workspace: path.join(workspace, "b") })
    const listed = manager.list()
    assert.equal(listed.length, 2)
    assert.ok(listed.some((s) => s.sessionId === a.sessionId))
    assert.ok(listed.some((s) => s.sessionId === b.sessionId))
    const deleted = await manager.delete(a.sessionId)
    assert.equal(deleted.sessionId, a.sessionId)
    assert.equal(manager.list().length, 1)
    assert.throws(
      () => manager.get(a.sessionId),
      (error) => error instanceof ExecutorError && error.code === "session_not_found",
    )
  })

  it("rejects invalid create parameters with typed invalid_arguments", async (t) => {
    const workspace = await makeWorkspace(t)
    const manager = makeManager(t)
    for (const params of [
      {},
      { workspace: "" },
      { workspace: 123 },
      { workspace, key: 5 },
      { workspace, metadata: "not-an-object" },
      { workspace, lease: "not-an-object" },
    ]) {
      await assert.rejects(
        () => manager.create(params),
        (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
        `expected invalid_arguments for ${JSON.stringify(params)}`,
      )
    }
  })

  it("get/delete on unknown sessions raise session_not_found", async (t) => {
    const manager = makeManager(t)
    assert.throws(
      () => manager.get("sess_missing"),
      (error) => error instanceof ExecutorError && error.code === "session_not_found",
    )
    await assert.rejects(
      () => manager.delete("sess_missing"),
      (error) => error instanceof ExecutorError && error.code === "session_not_found",
    )
  })

  it("deleting a session cancels its running commands", async (t) => {
    const workspace = await makeWorkspace(t)
    const manager = makeManager(t)
    const created = await manager.create({ workspace })
    const result = await manager.exec(created.sessionId, {
      argv: [process.execPath, "-e", "setInterval(() => {}, 1000)"],
      background: true,
    })
    assert.ok(pidAlive(result.pid))
    const deleted = await manager.delete(created.sessionId)
    assert.equal(deleted.cancelled.length, 1)
    await waitFor(() => !pidAlive(result.pid))
  })
})

describe("sessions HTTP routes (v2)", () => {
  async function startServer(t) {
    const sessions = new SessionManager({ logger: null })
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

  it("POST /v1/sessions creates a session; GET lists and fetches it", async (t) => {
    const { request } = await startServer(t)
    const workspace = await makeWorkspace(t)
    const created = await request("/v1/sessions", {
      method: "POST",
      body: {
        key: "t7:http",
        workspace,
        metadata: { case_run_id: 7 },
        lease: { lease_epoch: 1 },
      },
    })
    assert.equal(created.status, 200, created.text)
    const sessionId = created.json.sessionId
    assert.equal(typeof sessionId, "string")

    const listed = await request("/v1/sessions")
    assert.equal(listed.status, 200)
    assert.ok(listed.json.sessions.some((s) => s.sessionId === sessionId))

    const fetched = await request(`/v1/sessions/${sessionId}`)
    assert.equal(fetched.status, 200)
    assert.equal(fetched.json.sessionId, sessionId)
    assert.equal(fetched.json.key, "t7:http")
    assert.deepEqual(fetched.json.metadata, { case_run_id: 7 })
    assert.deepEqual(fetched.json.lease, { lease_epoch: 1 })
    assert.equal(path.resolve(fetched.json.workspace), path.resolve(workspace))
  })

  it("DELETE removes the session; subsequent GET/exec are 404", async (t) => {
    const { request } = await startServer(t)
    const workspace = await makeWorkspace(t)
    const created = await request("/v1/sessions", { method: "POST", body: { workspace } })
    const sessionId = created.json.sessionId
    const deleted = await request(`/v1/sessions/${sessionId}`, { method: "DELETE" })
    assert.equal(deleted.status, 200, deleted.text)
    assert.equal(deleted.json.ok, true)
    const fetched = await request(`/v1/sessions/${sessionId}`)
    assert.equal(fetched.status, 404)
    assert.equal(fetched.json.error.code, "session_not_found")
    const exec = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"] },
    })
    assert.equal(exec.status, 404)
  })

  it("create without workspace is 400 invalid_arguments", async (t) => {
    const { request } = await startServer(t)
    const response = await request("/v1/sessions", { method: "POST", body: { key: "x" } })
    assert.equal(response.status, 400)
    assert.equal(response.json.error.code, "invalid_arguments")
  })

  it("v2 session routes require the Bearer token", async (t) => {
    const { request } = await startServer(t)
    const workspace = await makeWorkspace(t)
    const noToken = await request("/v1/sessions", {
      method: "POST",
      body: { workspace },
      token: undefined,
    })
    assert.equal(noToken.status, 401)
    const listNoToken = await request("/v1/sessions", { token: undefined })
    assert.equal(listNoToken.status, 401)
    const wrongToken = await request("/v1/sessions", { token: "wrong-token" })
    assert.equal(wrongToken.status, 401)
  })
})

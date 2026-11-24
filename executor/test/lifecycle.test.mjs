// Task 9 (v0.2.4-C): executor v2 lifecycle — capacity 429 backpressure, drain,
// lease TTL + epoch fencing, orphan reclaim, bounded artifact/shell buffers and
// workspace anchor integrity. Operational lifecycle controls (design §4.1),
// NOT GD-01 security gates: defaults are open/configurable and reversible.
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import { spawn } from "node:child_process"
import fs from "node:fs"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { SessionManager } from "../src/sessions.mjs"
import { ShellEngine } from "../src/pty.mjs"
import { ExecutorError } from "../src/protocol.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-executor-token"

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t9-life-"))
  t.after(() => fsp.rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))
  return dir
}

function makeManager(t, options = {}) {
  const manager = new SessionManager({ logger: null, reclaimIntervalMs: 0, ...options })
  t.after(() => manager.close())
  return manager
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function waitFor(fn, { timeoutMs = 15000, intervalMs = 40, label = "condition" } = {}) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const value = await fn()
    if (value) {
      return value
    }
    if (Date.now() > deadline) {
      throw new Error(`waitFor timed out: ${label}`)
    }
    await delay(intervalMs)
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

/** A pid that is guaranteed to have exited (for crash-orphan simulation). */
async function deadPid() {
  const child = spawn(process.execPath, ["-e", "0"], { stdio: "ignore", windowsHide: true })
  const pid = child.pid
  await new Promise((resolve) => child.once("close", resolve))
  return pid
}

/** A live sleeper process independent of this test runner (orphan stand-in). */
function spawnSleeper() {
  const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
    stdio: "ignore",
    windowsHide: true,
    detached: process.platform !== "win32",
  })
  child.unref()
  return child.pid
}

async function startServer(t, { sessions, ...rest } = {}) {
  const sessionManager = sessions ?? new SessionManager({ logger: null, reclaimIntervalMs: 0 })
  const server = createExecutorServer({
    token: TOKEN,
    sessions: sessionManager,
    logger: null,
    version: "test",
    ...rest,
  })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  t.after(async () => {
    await sessionManager.close()
    await new Promise((resolve) => server.close(resolve))
  })
  return { sessions: sessionManager, request: httpClient(`http://127.0.0.1:${port}`, TOKEN) }
}

describe("capacity gate (429 + Retry-After)", () => {
  it("default capacity is 8: the 9th session is 429 with Retry-After, not 409", async (t) => {
    const root = await makeWorkspace(t)
    const { request } = await startServer(t)
    for (let i = 0; i < 8; i += 1) {
      const created = await request("/v1/sessions", {
        method: "POST",
        body: { workspace: path.join(root, `s${i}`), lease: { ttl_ms: 60000 } },
      })
      assert.equal(created.status, 200, created.text)
    }
    const ninth = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: path.join(root, "s8") },
    })
    assert.equal(ninth.status, 429, ninth.text)
    assert.equal(ninth.json.error.code, "session_limit")
    const retryAfter = Number(ninth.headers.get("retry-after"))
    assert.ok(Number.isInteger(retryAfter) && retryAfter >= 1, `Retry-After: ${retryAfter}`)
    // Retry-After reflects the earliest lease expiry (60s leases here).
    assert.ok(retryAfter <= 60, `Retry-After should hint the earliest lease expiry: ${retryAfter}`)
    // Freeing a slot makes creation work again (backpressure, not a hard wall).
    const listed = await request("/v1/sessions")
    const first = listed.json.sessions[0].sessionId
    assert.equal((await request(`/v1/sessions/${first}`, { method: "DELETE" })).status, 200)
    const retry = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: path.join(root, "s8") },
    })
    assert.equal(retry.status, 200, retry.text)
  })

  it("capacity is configurable; the typed error carries retry_after_seconds", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t, { maxSessions: 1 })
    await manager.create({ workspace: path.join(root, "a"), lease: { ttl_ms: 2000 } })
    await assert.rejects(
      () => manager.create({ workspace: path.join(root, "b") }),
      (error) => {
        assert.ok(error instanceof ExecutorError)
        assert.equal(error.code, "session_limit")
        assert.ok(Number.isInteger(error.details?.retry_after_seconds))
        assert.ok(error.details.retry_after_seconds >= 1)
        assert.ok(error.details.retry_after_seconds <= 2)
        return true
      },
    )
  })
})

describe("drain", () => {
  it("drain rejects new sessions (503 draining) while existing ones keep working and renewing", async (t) => {
    const root = await makeWorkspace(t)
    const { request } = await startServer(t)
    const created = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: path.join(root, "live"), lease: { lease_epoch: 1, ttl_ms: 60000 } },
    })
    const sessionId = created.json.sessionId

    const drained = await request("/v1/drain", { method: "POST", body: {} })
    assert.equal(drained.status, 200, drained.text)
    assert.equal(drained.json.draining, true)
    assert.equal((await request("/v1/drain")).json.draining, true)
    assert.equal((await request("/healthz", { token: undefined })).json.draining, true)

    const rejected = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: path.join(root, "new") },
    })
    assert.equal(rejected.status, 503, rejected.text)
    assert.equal(rejected.json.error.code, "draining")

    // Existing session: exec with its epoch still works and renews the lease.
    const exec = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"], lease_epoch: 1 },
    })
    assert.equal(exec.status, 200, exec.text)
    const renewed = await request(`/v1/sessions/${sessionId}/lease`, {
      method: "POST",
      body: { lease_epoch: 1 },
    })
    assert.equal(renewed.status, 200, renewed.text)
    assert.equal(renewed.json.lease_epoch, 1)

    // Drain is reversible (default-open operational control).
    const undrained = await request("/v1/drain", { method: "POST", body: { enabled: false } })
    assert.equal(undrained.json.draining, false)
    const after = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: path.join(root, "new") },
    })
    assert.equal(after.status, 200, after.text)
  })

  it("drained executor empties naturally: expired leases are reclaimed", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t, { reclaimIntervalMs: 25 })
    await manager.create({ workspace: path.join(root, "a"), lease: { ttl_ms: 30 } })
    manager.drain(true)
    // Wait on the reclaim FACT, not on size: #reclaim removes the session before
    // it records the reclaim, so polling size alone can observe the window
    // between the two (load-sensitive flake). Waiting for the counter is the
    // stronger condition and implies the session is gone.
    await waitFor(() => manager.reclaimedCount === 1, { label: "drained executor empties" })
    assert.equal(manager.size, 0)
    assert.equal(manager.reclaimedCount, 1)
  })
})

describe("lease TTL + epoch fencing", () => {
  it("stale epochs are fenced (409 lease_lost); higher epochs take over", async (t) => {
    const root = await makeWorkspace(t)
    const { request } = await startServer(t)
    const created = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: root, lease: { lease_epoch: 5, ttl_ms: 60000 } },
    })
    const sessionId = created.json.sessionId
    assert.equal(created.json.lease_epoch, 5)
    assert.equal(typeof created.json.lease_expires_at, "string")

    const stale = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"], lease_epoch: 4 },
    })
    assert.equal(stale.status, 409, stale.text)
    assert.equal(stale.json.error.code, "lease_lost")

    const current = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"], lease_epoch: 5 },
    })
    assert.equal(current.status, 200, current.text)

    // Takeover: a newer dispatch epoch adopts the session (aligns dispatch fencing).
    const takeover = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"], lease_epoch: 6 },
    })
    assert.equal(takeover.status, 200, takeover.text)
    assert.equal((await request(`/v1/sessions/${sessionId}`)).json.lease_epoch, 6)

    const oldAgain = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"], lease_epoch: 5 },
    })
    assert.equal(oldAgain.status, 409)
    assert.equal(oldAgain.json.error.code, "lease_lost")

    // Files upload and shell create/write are fenced the same way.
    const upload = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "fenced.txt", base64: Buffer.from("x").toString("base64"), lease_epoch: 1 },
    })
    assert.equal(upload.status, 409)
    assert.equal(upload.json.error.code, "lease_lost")
    const shell = await request(`/v1/sessions/${sessionId}/shell`, {
      method: "POST",
      body: { lease_epoch: 1 },
    })
    assert.equal(shell.status, 409)
    assert.equal(shell.json.error.code, "lease_lost")
  })

  it("expired leases reject writes (409 lease_expired); reads, cancel and delete stay open", async (t) => {
    const root = await makeWorkspace(t)
    const { request } = await startServer(t)
    const created = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: root, lease: { lease_epoch: 2, ttl_ms: 20 } },
    })
    const sessionId = created.json.sessionId
    await delay(60)

    const exec = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"], lease_epoch: 2 },
    })
    assert.equal(exec.status, 409, exec.text)
    assert.equal(exec.json.error.code, "lease_expired")

    const upload = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "late.txt", base64: Buffer.from("x").toString("base64"), lease_epoch: 2 },
    })
    assert.equal(upload.status, 409)
    assert.equal(upload.json.error.code, "lease_expired")

    // Reads and cleanup are never fenced.
    assert.equal((await request(`/v1/sessions/${sessionId}`)).status, 200)
    assert.equal((await request(`/v1/sessions/${sessionId}/artifacts`)).status, 200)
    assert.equal((await request(`/v1/sessions/${sessionId}/cancel`, { method: "POST" })).status, 200)

    // Same-epoch renewal after expiry is refused; a higher epoch may take over.
    const sameEpoch = await request(`/v1/sessions/${sessionId}/lease`, {
      method: "POST",
      body: { lease_epoch: 2 },
    })
    assert.equal(sameEpoch.status, 409)
    assert.equal(sameEpoch.json.error.code, "lease_expired")
    const takeover = await request(`/v1/sessions/${sessionId}/lease`, {
      method: "POST",
      body: { lease_epoch: 3 },
    })
    assert.equal(takeover.status, 200, takeover.text)
    const revived = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"], lease_epoch: 3 },
    })
    assert.equal(revived.status, 200, revived.text)

    assert.equal((await request(`/v1/sessions/${sessionId}`, { method: "DELETE" })).status, 200)
  })

  it("write activity renews the lease (default TTL aligns the 300s backend semantic)", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t)
    const created = await manager.create({ workspace: root, lease: { lease_epoch: 1, ttl_ms: 300 } })
    // Default TTL is 300s (backend execution.executor_context_ttl_seconds=300).
    const defaults = await manager.create({ workspace: path.join(root, "d") })
    const expiresAt = Date.parse(defaults.lease_expires_at)
    assert.ok(expiresAt - Date.now() > 290_000 && expiresAt - Date.now() <= 300_000)

    await delay(200)
    await manager.exec(created.sessionId, { argv: [process.execPath, "-e", "0"], lease_epoch: 1 })
    await delay(200)
    // 400ms > 300ms TTL: only the activity renewal keeps this write alive.
    const result = await manager.exec(created.sessionId, {
      argv: [process.execPath, "-e", "0"],
      lease_epoch: 1,
    })
    assert.equal(result.exit, 0)
  })

  it("invalid lease fields are typed invalid_arguments", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t)
    for (const lease of [{ lease_epoch: -1 }, { lease_epoch: 1.5 }, { ttl_ms: 0 }, { ttl_ms: "x" }]) {
      await assert.rejects(
        () => manager.create({ workspace: root, lease }),
        (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
        `expected invalid_arguments for ${JSON.stringify(lease)}`,
      )
    }
    const created = await manager.create({ workspace: root })
    await assert.rejects(
      () => manager.exec(created.sessionId, { argv: [process.execPath, "-e", "0"], lease_epoch: -2 }),
      (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
    )
  })

  it("a leased session rejects writes that omit lease_epoch; a lease-less session accepts omission", async (t) => {
    const root = await makeWorkspace(t)
    const { request } = await startServer(t)
    // Explicit lease (even epoch 0) => every write must carry lease_epoch.
    const leased = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: path.join(root, "leased"), lease: { lease_epoch: 0, ttl_ms: 60000 } },
    })
    const leasedId = leased.json.sessionId
    const omitted = await request(`/v1/sessions/${leasedId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"] },
    })
    assert.equal(omitted.status, 409, omitted.text)
    assert.equal(omitted.json.error.code, "lease_epoch_required")
    // The omission must NOT have renewed the lease (no zombie keep-alive).
    const omittedUpload = await request(`/v1/sessions/${leasedId}/files`, {
      method: "POST",
      body: { path: "x.txt", base64: Buffer.from("x").toString("base64") },
    })
    assert.equal(omittedUpload.status, 409)
    assert.equal(omittedUpload.json.error.code, "lease_epoch_required")
    const omittedShell = await request(`/v1/sessions/${leasedId}/shell`, { method: "POST", body: {} })
    assert.equal(omittedShell.status, 409)
    assert.equal(omittedShell.json.error.code, "lease_epoch_required")
    // Carrying the epoch works.
    const withEpoch = await request(`/v1/sessions/${leasedId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"], lease_epoch: 0 },
    })
    assert.equal(withEpoch.status, 200, withEpoch.text)

    // Lease-less legacy session: omission stays allowed (backward compat).
    const legacy = await request("/v1/sessions", {
      method: "POST",
      body: { workspace: path.join(root, "legacy") },
    })
    const legacyExec = await request(`/v1/sessions/${legacy.json.sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"] },
    })
    assert.equal(legacyExec.status, 200, legacyExec.text)
  })
})

describe("orphan reclaim", () => {
  it("expired sessions are swept: processes killed, workspace retained by default", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t)
    const workspace = path.join(root, "orphan")
    const created = await manager.create({ workspace, lease: { ttl_ms: 30 } })
    await fsp.writeFile(path.join(workspace, "evidence.txt"), "keep me")
    const bg = await manager.exec(created.sessionId, {
      argv: [process.execPath, "-e", "setInterval(() => {}, 1000)"],
      background: true,
      lease_epoch: 0,
    })
    t.after(() => {
      try {
        process.kill(bg.pid)
      } catch {
        // already reclaimed
      }
    })
    assert.ok(pidAlive(bg.pid))
    await delay(60)
    const swept = await manager.sweep()
    assert.equal(swept.length, 1)
    assert.equal(swept[0].session_id, created.sessionId)
    assert.equal(swept[0].workspace_retained, true)
    await waitFor(() => !pidAlive(bg.pid), { label: "orphan process killed" })
    assert.throws(
      () => manager.get(created.sessionId),
      (error) => error instanceof ExecutorError && error.code === "session_not_found",
    )
    // Retention default: the workspace (evidence spill root) survives reclaim.
    assert.equal(await fsp.readFile(path.join(workspace, "evidence.txt"), "utf8"), "keep me")
    assert.equal(manager.reclaimedCount, 1)
  })

  it("retention policy delete removes the workspace on reclaim (opt-in)", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t, { workspaceRetention: "delete" })
    const workspace = path.join(root, "gone")
    const created = await manager.create({ workspace, lease: { ttl_ms: 20 } })
    await fsp.writeFile(path.join(workspace, "tmp.txt"), "x")
    await delay(50)
    const swept = await manager.sweep()
    assert.equal(swept.length, 1)
    assert.equal(swept[0].workspace_retained, false)
    assert.equal(fs.existsSync(workspace), false)
    assert.equal(manager.size, 0)
  })

  it("startup scan reclaims crash orphans: dead-owner registry entries, processes killed", async (t) => {
    const root = await makeWorkspace(t)
    const stateDir = path.join(root, "state")
    const ownerPid = await deadPid()
    const orphanPid = spawnSleeper()
    t.after(() => {
      try {
        process.kill(orphanPid)
      } catch {
        // already dead
      }
    })
    const workspace = path.join(root, "crashed-workspace")
    await fsp.mkdir(workspace, { recursive: true })
    await fsp.writeFile(path.join(workspace, "spill.log"), "evidence")
    const entryDir = path.join(stateDir, String(ownerPid))
    await fsp.mkdir(entryDir, { recursive: true })
    await fsp.writeFile(
      path.join(entryDir, "sess_crashed.json"),
      JSON.stringify({
        session_id: "sess_crashed",
        workspace,
        lease_epoch: 1,
        lease_expires_at: new Date(Date.now() - 1000).toISOString(),
        ttl_ms: 300000,
        executor_pid: ownerPid,
        pids: [orphanPid],
      }),
    )
    // A live owner's entries must NOT be touched (another executor process).
    const liveDir = path.join(stateDir, String(process.pid))
    await fsp.mkdir(liveDir, { recursive: true })
    await fsp.writeFile(
      path.join(liveDir, "sess_live.json"),
      JSON.stringify({ session_id: "sess_live", workspace, executor_pid: process.pid, pids: [] }),
    )

    const manager = makeManager(t, { stateDir })
    const report = await manager.recoverOrphans()
    assert.equal(report.reclaimed.length, 1)
    assert.equal(report.reclaimed[0].session_id, "sess_crashed")
    await waitFor(() => !pidAlive(orphanPid), { label: "crash-orphan process killed" })
    // Workspace retained by default; registry entry removed; live owner untouched.
    assert.equal(await fsp.readFile(path.join(workspace, "spill.log"), "utf8"), "evidence")
    assert.equal(fs.existsSync(path.join(entryDir, "sess_crashed.json")), false)
    assert.equal(fs.existsSync(path.join(liveDir, "sess_live.json")), true)
    assert.equal(manager.reclaimedCount, 1)
  })

  it("a session re-claimed during the reclaim window survives and stays usable", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t)
    // Hold the reclaim cleanup open so a takeover can land mid-window.
    let releaseGate
    const gate = new Promise((resolve) => {
      releaseGate = resolve
    })
    let entered = false
    manager.onReclaim = async () => {
      entered = true
      await gate
    }
    const created = await manager.create({ workspace: root, lease: { lease_epoch: 1, ttl_ms: 20 } })
    await delay(60) // lease expired

    const sweepPromise = manager.sweep()
    await waitFor(() => entered, { label: "reclaim cleanup entered" })
    // Higher-epoch re-claim during the window: accepted, renews the lease.
    const takeover = manager.renew(created.sessionId, { lease_epoch: 2 })
    assert.equal(takeover.lease_epoch, 2)
    releaseGate()
    const reclaimed = await sweepPromise
    // The re-claimed session must NOT be destroyed.
    assert.deepEqual(reclaimed, [])
    assert.equal(manager.reclaimedCount, 0)
    const fetched = manager.get(created.sessionId)
    assert.equal(fetched.lease_epoch, 2)
    // ...and remains usable for fenced writes.
    const result = await manager.exec(created.sessionId, {
      argv: [process.execPath, "-e", "0"],
      lease_epoch: 2,
    })
    assert.equal(result.exit, 0)
  })
})

describe("bounded memory retention", () => {
  it("artifact buffer is capped per session; evicted artifacts spill and the cursor stays monotonic", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t, { artifactBufferLimit: 3 })
    const created = await manager.create({ workspace: root })
    for (let i = 0; i < 5; i += 1) {
      await manager.exec(created.sessionId, { argv: [process.execPath, "-e", "0"] })
    }
    const session = manager.sessions.get(created.sessionId)
    assert.ok(session.artifacts.length <= 3, `artifacts=${session.artifacts.length}`)
    assert.equal(session.artifactsEvicted, 2)
    assert.equal(session.artifactBase, 2)
    // Evicted evidence is spilled to the workspace, never silently lost.
    const spill0 = JSON.parse(
      await fsp.readFile(path.join(root, ".eztest", "artifacts", "0.json"), "utf8"),
    )
    assert.equal(spill0.kind, "command")

    const { request } = await startServer(t, { sessions: manager })
    const all = await request(`/v1/sessions/${created.sessionId}/artifacts?since=0`)
    assert.equal(all.status, 200, all.text)
    assert.equal(all.json.base_cursor, 2)
    assert.equal(all.json.evicted, 2)
    assert.equal(all.json.gap, true)
    assert.equal(all.json.missed, 2)
    assert.equal(all.json.artifacts.length, 3)
    assert.equal(all.json.next_cursor, 5)
    assert.equal(all.json.total, 5)

    // Pulled-cursor semantics keep working: an up-to-date client sees no gap.
    const delta = await request(`/v1/sessions/${created.sessionId}/artifacts?since=3`)
    assert.equal(delta.json.gap, undefined)
    assert.equal(delta.json.artifacts.length, 2)
    assert.equal(delta.json.next_cursor, 5)
    const empty = await request(`/v1/sessions/${created.sessionId}/artifacts?since=5`)
    assert.deepEqual(empty.json.artifacts, [])
    assert.equal(empty.json.next_cursor, 5)
  })

  it("shell output memory is bounded; spilled bytes stay readable through the byte cursor", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t)
    const created = await manager.create({ workspace: root })
    const session = manager.sessions.get(created.sessionId)
    const engine = new ShellEngine({
      logger: null,
      shellBufferLimitBytes: 256,
      resolveSession: (id) => manager.sessions.get(id) ?? null,
    })
    t.after(() => engine.close())
    const shell = engine.create(session, {
      argv: [process.execPath, "-e", "process.stdout.write('x'.repeat(8192)); setTimeout(() => {}, 300)"],
    })
    t.after(() => {
      // ConPTY reports pid 0 until the agent connects; only kill a real pid.
      const pid = engine.shells.get(shell.shell_id)?.pty?.pid
      if (Number.isInteger(pid) && pid > 0) {
        try {
          process.kill(pid)
        } catch {
          // already exited
        }
      }
    })
    await waitFor(() => engine.get(created.sessionId, shell.shell_id).total_bytes >= 8192, {
      label: "shell output produced",
    })
    const entry = engine.shells.get(shell.shell_id)
    assert.ok(entry.tailBytes <= 256, `in-memory tail must stay bounded, got ${entry.tailBytes}`)
    assert.ok(entry.spilledBytes > 0, "overflow must spill to the workspace")
    assert.ok(fs.existsSync(path.join(root, ".eztest", "shells", `${shell.shell_id}.log`)))

    // Let the shell exit so the byte stream is stable, then page the WHOLE
    // stream through the cursor: spilled prefix + tail, no byte lost. (ConPTY
    // adds line-wrap bytes, so the total is >= 8192, not exactly 8192.)
    const result = await engine.wait(created.sessionId, shell.shell_id, 10000)
    assert.equal(result.exit, 0)
    let cursor = 0
    let collected = ""
    let pages = 0
    for (;;) {
      const page = engine.read(created.sessionId, shell.shell_id, cursor)
      collected += page.data
      cursor = page.next_cursor
      pages += 1
      assert.ok(pages < 1000, "paging must terminate")
      if (!page.has_more) {
        break
      }
    }
    // Paging reconstructs the whole stream (spilled prefix + tail), no loss.
    assert.equal(cursor, collected.length)
    assert.ok(collected.length >= 8192, `stream must include the 8192 x's, got ${collected.length}`)
    assert.ok(collected.includes("x".repeat(100)))

    // Settlement still records the FULL output (spill + tail), not just the tail.
    const artifact = session.artifacts.find((a) => a.command_id === shell.shell_id)
    assert.ok(artifact.stdout_bytes >= 8192, `artifact must record full output, got ${artifact.stdout_bytes}`)
    await engine.cancelSession(created.sessionId)
  })
})

describe("workspace anchor integrity", () => {
  it("rejects a symlink/junction anchor at session creation", async (t) => {
    const root = await makeWorkspace(t)
    const manager = makeManager(t)
    const real = path.join(root, "real")
    await fsp.mkdir(real)
    const link = path.join(root, "link")
    await fsp.symlink(real, link, process.platform === "win32" ? "junction" : "dir")
    await assert.rejects(
      () => manager.create({ workspace: link }),
      (error) =>
        error instanceof ExecutorError &&
        error.code === "invalid_arguments" &&
        /symlink|junction|real directory/i.test(error.message),
    )
    // A real directory anchor still works (the guard does not over-reject).
    const ok = await manager.create({ workspace: path.join(root, "real") })
    assert.equal(typeof ok.sessionId, "string")
  })
})
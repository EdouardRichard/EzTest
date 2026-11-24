// Task 8 (v0.2.4-C): workspace file-change auto-recording (file artifacts),
// the artifacts pull endpoint (command/file/trace/screenshot/download) and the
// trace file-ref endpoint. fs.watch events + settlement-time scan fallback.
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import crypto from "node:crypto"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { ExecEngine } from "../src/exec.mjs"
import { WorkspaceFiles } from "../src/files.mjs"
import { ARTIFACT_KINDS, assertArtifactKind, CommandRecorder } from "../src/recording.mjs"
import { ExecutorError } from "../src/protocol.mjs"
import { SessionManager } from "../src/sessions.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { WorkspaceWatcher } from "../src/workspace_watch.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-executor-token"

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t8-watch-"))
  t.after(() => fsp.rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))
  return dir
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

async function startServer(t, { files, sessions } = {}) {
  const sessionManager = sessions ?? new SessionManager({ logger: null })
  const server = createExecutorServer({
    token: TOKEN,
    sessions: sessionManager,
    files,
    logger: null,
    version: "test",
  })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  const request = httpClient(`http://127.0.0.1:${port}`, TOKEN)
  t.after(async () => {
    // Delete sessions through the protocol first: stops their watchers and
    // shells deterministically before workspace directories are removed.
    for (const session of sessionManager.list()) {
      await request(`/v1/sessions/${session.sessionId}`, { method: "DELETE" })
    }
    await sessionManager.close()
    await new Promise((resolve) => server.close(resolve))
  })
  return { sessions: sessionManager, request }
}

async function createSession(request, t) {
  const workspace = await makeWorkspace(t)
  const created = await request("/v1/sessions", { method: "POST", body: { workspace } })
  assert.equal(created.status, 200, created.text)
  return { sessionId: created.json.sessionId, workspace }
}

function nodeWrite(file, content) {
  return [
    process.execPath,
    "-e",
    `require('fs').writeFileSync(${JSON.stringify(file)}, ${JSON.stringify(content)})`,
  ]
}

describe("artifact kind registry (fail-closed)", () => {
  it("declares command/file/trace/screenshot/snapshot/console/network/download and rejects undeclared kinds", () => {
    // snapshot + console are the browser CLI channel kinds (T15, Q06 producer);
    // network is the passive traffic recorder kind (T16, design §4.3); the
    // registry stays the single authority (constitution art.4.5).
    assert.deepEqual([...ARTIFACT_KINDS].sort(), [
      "command",
      "console",
      "download",
      "file",
      "network",
      "screenshot",
      "snapshot",
      "trace",
    ])
    for (const kind of ARTIFACT_KINDS) {
      assert.doesNotThrow(() => assertArtifactKind(kind))
    }
    assert.throws(
      () => assertArtifactKind("bogus"),
      (error) => error instanceof ExecutorError && /bogus/.test(error.message),
    )
  })
})

describe("workspace file-change recording", () => {
  it("exec writing a file settles a file artifact (sha256/size/mime/path/command_id)", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const content = "hello t8"
    const exec = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: nodeWrite("out.txt", content) },
    })
    assert.equal(exec.status, 200, exec.text)
    const artifacts = await request(`/v1/sessions/${sessionId}/artifacts`)
    assert.equal(artifacts.status, 200, artifacts.text)
    const kinds = artifacts.json.artifacts.map((a) => a.kind)
    assert.ok(kinds.includes("command"))
    const file = artifacts.json.artifacts.find((a) => a.kind === "file")
    assert.ok(file, "file artifact must be recorded")
    assert.equal(file.path, "out.txt")
    assert.equal(file.sha256, sha256(Buffer.from(content)))
    assert.equal(file.size, Buffer.byteLength(content))
    assert.equal(file.mime, "text/plain")
    assert.equal(file.command_id, exec.json.command_id)
    // Settle-ready content payload (backend stores the bytes content-addressed).
    assert.equal(Buffer.from(file.base64, "base64").toString("utf8"), content)
    assert.equal(file.filename, "out.txt")
    assert.equal(artifacts.json.next_cursor, artifacts.json.total)
  })

  it("modified files re-ledger; unchanged files do not", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: nodeWrite("out.txt", "version one") },
    })
    await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: nodeWrite("out.txt", "version two, changed") },
    })
    let pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
    const fileArtifacts = pulled.json.artifacts.filter(
      (a) => a.kind === "file" && a.path === "out.txt",
    )
    assert.equal(fileArtifacts.length, 2)
    assert.notEqual(fileArtifacts[0].sha256, fileArtifacts[1].sha256)
    assert.equal(fileArtifacts[1].sha256, sha256(Buffer.from("version two, changed")))
    // A command that changes nothing must not re-ledger the same content.
    await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"] },
    })
    pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
    assert.equal(
      pulled.json.artifacts.filter((a) => a.kind === "file" && a.path === "out.txt").length,
      2,
    )
  })

  it("recording spill files under .eztest are not ledgered as workspace changes", async (t) => {
    const sessions = new SessionManager({ logger: null, stdioInlineLimitBytes: 16 })
    const { request } = await startServer(t, { sessions })
    const { sessionId, workspace } = await createSession(request, t)
    const exec = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "process.stdout.write('x'.repeat(200))"] },
    })
    assert.equal(exec.status, 200, exec.text)
    assert.equal(exec.json.stdout_truncated, true)
    assert.ok(exec.json.stdout_ref.startsWith(".eztest/commands/"))
    // The spill file exists on disk but is recording output, not a workspace change.
    await fsp.stat(path.join(workspace, ...exec.json.stdout_ref.split("/")))
    const pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
    assert.ok(
      pulled.json.artifacts.every(
        (a) => a.kind !== "file" || !a.path.startsWith(".eztest"),
      ),
      "no file artifact may point into .eztest",
    )
  })

  it("fs.watch records external workspace changes without an explicit settle", async (t) => {
    const workspace = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t8-watch-fs-"))
    const manager = new SessionManager({ logger: null })
    const created = await manager.create({ workspace })
    const session = manager.sessions.get(created.sessionId)
    const watcher = new WorkspaceWatcher(session, { debounceMs: 30, logger: null })
    watcher.start()
    // One ordered cleanup: stop watching before removing the watched dir.
    t.after(async () => {
      await watcher.close()
      await manager.close()
      await fsp.rm(workspace, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
    })
    await fsp.writeFile(path.join(workspace, "watched.txt"), "via fs, no exec")
    // No settle() call: the fs.watch event path alone must ledger the change.
    const artifact = await waitFor(() =>
      session.artifacts.find((a) => a.kind === "file" && a.path === "watched.txt"),
    )
    assert.equal(artifact.sha256, sha256(Buffer.from("via fs, no exec")))
    assert.equal(artifact.command_id, null)
  })

  it("files above the artifact limit ledger metadata with explicit content_omitted", async (t) => {
    const files = new WorkspaceFiles({ artifactLimitBytes: 32 })
    const { request } = await startServer(t, { files })
    const { sessionId } = await createSession(request, t)
    const content = "y".repeat(100)
    await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: nodeWrite("big.txt", content) },
    })
    const pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
    const file = pulled.json.artifacts.find((a) => a.kind === "file" && a.path === "big.txt")
    assert.ok(file)
    assert.equal(file.size, 100)
    assert.equal(file.sha256, sha256(Buffer.from(content)))
    // Explicit, never silent: the content is omitted with a stated reason.
    assert.equal(file.content_omitted, true)
    assert.equal(typeof file.content_reason, "string")
    assert.equal(file.base64, null)
  })

  it("uploads are ledgered as file artifacts with command_id null", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const uploaded = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "up.txt", base64: Buffer.from("uploaded body").toString("base64") },
    })
    assert.equal(uploaded.status, 200, uploaded.text)
    const pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
    const file = pulled.json.artifacts.find((a) => a.kind === "file" && a.path === "up.txt")
    assert.ok(file)
    assert.equal(file.command_id, null)
    assert.equal(file.sha256, sha256(Buffer.from("uploaded body")))
  })
})
describe("artifacts pull endpoint", () => {
  it("since cursor returns only new artifacts (command + file)", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: nodeWrite("first.txt", "one") },
    })
    const first = await request(`/v1/sessions/${sessionId}/artifacts?since=0`)
    assert.equal(first.status, 200, first.text)
    assert.ok(first.json.total >= 2)
    const cursor = first.json.next_cursor
    const empty = await request(`/v1/sessions/${sessionId}/artifacts?since=${cursor}`)
    assert.equal(empty.status, 200)
    assert.deepEqual(empty.json.artifacts, [])
    assert.equal(empty.json.next_cursor, cursor)
    await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: nodeWrite("second.txt", "two") },
    })
    const delta = await request(`/v1/sessions/${sessionId}/artifacts?since=${cursor}`)
    assert.equal(delta.status, 200)
    const kinds = delta.json.artifacts.map((a) => a.kind)
    assert.ok(kinds.includes("command"))
    assert.ok(delta.json.artifacts.some((a) => a.kind === "file" && a.path === "second.txt"))
    assert.ok(!delta.json.artifacts.some((a) => a.kind === "file" && a.path === "first.txt"))
    assert.ok(delta.json.next_cursor > cursor)
    const badSince = await request(`/v1/sessions/${sessionId}/artifacts?since=-1`)
    assert.equal(badSince.status, 400)
    assert.equal(badSince.json.error.code, "invalid_arguments")
  })

  it("background command completion is observable via the pull endpoint", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const exec = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: nodeWrite("bg.txt", "bg-body"), background: true },
    })
    assert.equal(exec.status, 200, exec.text)
    assert.equal(exec.json.background, true)
    assert.deepEqual(exec.json.artifacts, [])
    // The settled command artifact AND the file it wrote both become pullable.
    const found = await waitFor(async () => {
      const pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
      const command = pulled.json.artifacts.find(
        (a) => a.kind === "command" && a.command_id === exec.json.command_id,
      )
      const file = pulled.json.artifacts.find((a) => a.kind === "file" && a.path === "bg.txt")
      return command && file ? { command, file } : null
    })
    assert.equal(found.command.exit, 0)
    assert.equal(found.command.outcome, "exited")
    assert.equal(found.file.sha256, sha256(Buffer.from("bg-body")))
  })

  it("background spawn failures are observable via the pull endpoint", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const exec = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: ["eztest-no-such-cmd-xyz"], background: true },
    })
    assert.equal(exec.status, 200, exec.text)
    const artifact = await waitFor(async () => {
      const pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
      return pulled.json.artifacts.find(
        (a) => a.kind === "command" && a.command_id === exec.json.command_id,
      )
    })
    assert.equal(artifact.outcome, "spawn_error")
    assert.ok(artifact.spawn_error)
  })
})
describe("recording failure observability", () => {
  it("recording failures surface as explicit recording_failed markers (no silent loss)", async (t) => {
    // Genuine failure path: `.eztest` exists as a FILE, so the stdio spill
    // mkdir fails and CommandRecorder.settle throws.
    let manager
    const recorder = new CommandRecorder({
      inlineLimitBytes: 16,
      resolveSession: (id) => manager.sessions.get(id) ?? null,
    })
    manager = new SessionManager({
      logger: null,
      engine: new ExecEngine({ logger: null, recorder }),
    })
    const { request } = await startServer(t, { sessions: manager })
    const { sessionId, workspace } = await createSession(request, t)
    await fsp.writeFile(path.join(workspace, ".eztest"), "not a directory")
    const exec = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, "-e", "process.stdout.write('x'.repeat(200))"] },
    })
    assert.equal(exec.status, 500, exec.text)
    assert.equal(exec.json.error.code, "internal")
    const pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
    assert.equal(pulled.status, 200, pulled.text)
    const marker = pulled.json.artifacts.find((a) => a.recording_failed === true)
    assert.ok(marker, "recording failure must be observable, not log-only")
    assert.equal(marker.kind, "command")
    assert.equal(marker.outcome, "recording_failed")
    assert.equal(typeof marker.error, "string")
  })

  it("undeclared artifact kinds fail closed on pull (constitution art. 4.5)", async (t) => {
    const { sessions, request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    sessions.sessions.get(sessionId).artifacts.push({ kind: "bogus" })
    const pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
    assert.equal(pulled.status, 500)
    assert.equal(pulled.json.error.code, "internal")
    assert.ok(pulled.json.error.message.includes("bogus"))
  })

  it("artifacts routes require the token and a known session", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const noToken = await request(`/v1/sessions/${sessionId}/artifacts`, { token: undefined })
    assert.equal(noToken.status, 401)
    const unknown = await request("/v1/sessions/sess_missing/artifacts")
    assert.equal(unknown.status, 404)
    assert.equal(unknown.json.error.code, "session_not_found")
  })
})

describe("trace endpoint (file ref; CLI tracing lands with Task 15)", () => {
  it("reports available:false when no trace file exists yet", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const trace = await request(`/v1/sessions/${sessionId}/trace`)
    assert.equal(trace.status, 200, trace.text)
    assert.equal(trace.json.available, false)
    assert.equal(trace.json.path, null)
  })

  it("returns the trace file ref and settles a trace artifact once per content", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const content = Buffer.from("PK-fake-playwright-trace-bytes")
    const uploaded = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "trace.zip", base64: content.toString("base64") },
    })
    assert.equal(uploaded.status, 200, uploaded.text)
    const trace = await request(`/v1/sessions/${sessionId}/trace`)
    assert.equal(trace.status, 200, trace.text)
    assert.equal(trace.json.available, true)
    assert.equal(trace.json.path, "trace.zip")
    assert.equal(trace.json.bytes, content.length)
    assert.equal(trace.json.sha256, sha256(content))
    assert.equal(trace.json.mime, "application/zip")
    const pulled = await request(`/v1/sessions/${sessionId}/artifacts`)
    const traces = pulled.json.artifacts.filter((a) => a.kind === "trace")
    assert.equal(traces.length, 1)
    assert.equal(traces[0].path, "trace.zip")
    assert.deepEqual(Buffer.from(traces[0].base64, "base64"), content)
    // Same content again: ref returned, no duplicate trace artifact.
    const again = await request(`/v1/sessions/${sessionId}/trace`)
    assert.equal(again.status, 200)
    assert.equal(again.json.sha256, sha256(content))
    const pulled2 = await request(`/v1/sessions/${sessionId}/artifacts`)
    assert.equal(pulled2.json.artifacts.filter((a) => a.kind === "trace").length, 1)
  })
})

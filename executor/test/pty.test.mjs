// Task 8 (v0.2.4-C): PTY/shell channel — create/write/read/close/wait.
// Interactive forms (ssh/psql/mysql/REPL) are exercised with a local node REPL
// and the platform shell; no real remote endpoints are needed.
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import {
  DEFAULT_PTY_COLS,
  DEFAULT_PTY_ROWS,
  DEFAULT_SHELL_WAIT_TIMEOUT_MS,
  ShellEngine,
} from "../src/pty.mjs"
import { ExecutorError } from "../src/protocol.mjs"
import { SessionManager } from "../src/sessions.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-executor-token"

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

// One ordered cleanup hook per test: shells die first (their cwd locks the
// workspace on Windows), then the manager, then the directory removal.
async function setup(t, engineOpts = {}) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t8-pty-"))
  const manager = new SessionManager({ logger: null })
  const engine = new ShellEngine({
    logger: null,
    resolveSession: (id) => manager.sessions.get(id) ?? null,
    ...engineOpts,
  })
  t.after(async () => {
    await engine.close()
    await manager.close()
    await fsp.rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
  })
  return { dir, manager, engine }
}

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t8-pty-http-"))
  t.after(() => fsp.rm(dir, { recursive: true, force: true, maxRetries: 8, retryDelay: 150 }))
  return dir
}

/** Reads the whole shell output from cursor 0 (paging through the read limit). */
async function readAll(engine, sessionId, shellId) {
  let cursor = 0
  let text = ""
  for (;;) {
    const chunk = engine.read(sessionId, shellId, cursor)
    text += chunk.data
    cursor = chunk.next_cursor
    if (!chunk.has_more) {
      return { text, cursor, last: chunk }
    }
  }
}

// ConPTY renders the screen with VT cursor-positioning sequences instead of
// plain newlines; strip them to assert the LOGICAL terminal content.
function stripVt(text) {
  return text
    .replace(/\x1b\][^\x07]*(\x07|\x1b\\)/g, "")
    .replace(/\x1b\[[0-9;?]*[a-zA-Z]/g, "")
    .replace(/\x1b[=>]/g, "")
}

describe("ShellEngine (PTY) core", () => {
  it("creates a PTY, writes an echo command, reads the output, closes", async (t) => {
    const { dir, manager, engine } = await setup(t)
    const created = await manager.create({ workspace: dir })
    const session = manager.sessions.get(created.sessionId)

    const shell = engine.create(session, {})
    assert.equal(typeof shell.shell_id, "string")
    assert.ok(shell.shell_id.length > 0)
    assert.equal(shell.status, "running")
    assert.equal(shell.cols, DEFAULT_PTY_COLS)
    assert.equal(shell.rows, DEFAULT_PTY_ROWS)

    engine.write(created.sessionId, shell.shell_id, "echo PTY_HELLO\r")
    // The echoed keystrokes contain "echo PTY_HELLO"; a PTY_HELLO at the start
    // of a rendered line proves the shell actually executed the command.
    const { text } = await waitFor(async () => {
      const out = await readAll(engine, created.sessionId, shell.shell_id)
      return /(^|\r?\n)PTY_HELLO/.test(stripVt(out.text)) ? out : null
    })
    assert.ok(stripVt(text).includes("PTY_HELLO"))

    const closed = await engine.closeShell(created.sessionId, shell.shell_id)
    assert.equal(closed.status, "exited")
    assert.equal(closed.outcome, "cancelled")
    // A killed shell has no exit status of its own (same semantics as exec).
    assert.equal(closed.exit, null)
  })

  it("runs an interactive REPL: write expression, read result, .exit, wait", async (t) => {
    const { dir, manager, engine } = await setup(t)
    const created = await manager.create({ workspace: dir })
    const session = manager.sessions.get(created.sessionId)

    // node with no args inside a PTY starts the interactive REPL — the same
    // shape as ssh/psql/mysql sessions (prompt, line input, streamed output).
    const shell = engine.create(session, { argv: [process.execPath], cols: 100, rows: 30 })
    assert.equal(shell.cols, 100)
    assert.equal(shell.rows, 30)
    engine.write(created.sessionId, shell.shell_id, "console.log('PTY_MATH_' + (6*7))\r")
    await waitFor(async () => {
      const { text } = await readAll(engine, created.sessionId, shell.shell_id)
      return text.includes("PTY_MATH_42")
    })
    engine.write(created.sessionId, shell.shell_id, ".exit\r")
    const waited = await engine.wait(created.sessionId, shell.shell_id, 15000)
    assert.equal(waited.status, "exited")
    assert.equal(waited.exit, 0)
    assert.equal(waited.outcome, "exited")
    assert.ok(waited.duration >= 0)
  })

  it("read is cursor-based: pages never overlap or drop bytes", async (t) => {
    // Tiny read window forces pagination.
    const { dir, manager, engine } = await setup(t, { readLimitBytes: 8 })
    const created = await manager.create({ workspace: dir })
    const session = manager.sessions.get(created.sessionId)
    const shell = engine.create(session, {})
    engine.write(created.sessionId, shell.shell_id, "echo PTY_PAGED\r")
    const full = await waitFor(async () => {
      const out = await readAll(engine, created.sessionId, shell.shell_id)
      return /(^|\r?\n)PTY_PAGED/.test(stripVt(out.text)) ? out : null
    })
    // Paging with a tiny window must have advanced one window at a time.
    // The cursor is a BYTE offset; a window may split a UTF-8 code point
    // (decoded as replacement chars), so assert on string length, not bytes.
    const first = engine.read(created.sessionId, shell.shell_id, 0)
    assert.ok(first.data.length <= 8)
    assert.equal(first.since, 0)
    assert.ok(first.next_cursor <= 8)
    assert.equal(first.has_more, true)
    const second = engine.read(created.sessionId, shell.shell_id, first.next_cursor)
    assert.equal(second.since, first.next_cursor)
    assert.ok(full.text.startsWith(first.data + second.data))
    assert.equal(full.cursor, full.last.total_bytes)
    await engine.closeShell(created.sessionId, shell.shell_id)
  })

  it("wait times out with typed executor_timeout while the shell keeps running", async (t) => {
    const { dir, manager, engine } = await setup(t)
    const created = await manager.create({ workspace: dir })
    const session = manager.sessions.get(created.sessionId)
    const shell = engine.create(session, { argv: [process.execPath] })
    await assert.rejects(
      () => engine.wait(created.sessionId, shell.shell_id, 300),
      (error) =>
        error instanceof ExecutorError &&
        error.code === "executor_timeout" &&
        error.details?.shell_id === shell.shell_id &&
        error.details?.status === "running",
    )
    // The shell survived the wait timeout and can still be closed.
    const closed = await engine.closeShell(created.sessionId, shell.shell_id)
    assert.equal(closed.status, "exited")
    assert.equal(DEFAULT_SHELL_WAIT_TIMEOUT_MS, 30000)
  })

  it("settles a command artifact on exit with full plaintext env (GD-01)", async (t) => {
    const { dir, manager, engine } = await setup(t)
    const created = await manager.create({ workspace: dir })
    const session = manager.sessions.get(created.sessionId)
    const secret = "pty-plain-9876"
    const inherited = "pty-inherited-2468"
    // A controlled inherited variable proves the artifact carries the process
    // environment, without coupling the assertion to the host's PATH value.
    process.env.EZTEST_T8_PTY_INHERITED = inherited
    t.after(() => {
      delete process.env.EZTEST_T8_PTY_INHERITED
    })
    const shell = engine.create(session, {
      argv: [process.execPath],
      env: { EZTEST_T8_PTY_SECRET: secret },
    })
    engine.write(created.sessionId, shell.shell_id, "console.log(process.env.EZTEST_T8_PTY_SECRET)\r")
    await waitFor(async () => {
      const { text } = await readAll(engine, created.sessionId, shell.shell_id)
      return text.includes(secret)
    })
    engine.write(created.sessionId, shell.shell_id, ".exit\r")
    await engine.wait(created.sessionId, shell.shell_id, 15000)
    const artifact = await waitFor(() =>
      session.artifacts.find((a) => a.kind === "command" && a.command_id === shell.shell_id),
    )
    assert.equal(artifact.argv[0], process.execPath)
    assert.equal(artifact.cwd, session.workspace)
    assert.equal(artifact.exit, 0)
    assert.equal(artifact.outcome, "exited")
    // FULL environment verbatim, no scrub (GD-01 / R-C3): the controlled
    // inherited variable and the explicit override both reach the artifact.
    assert.equal(artifact.env.EZTEST_T8_PTY_SECRET, secret)
    assert.equal(artifact.env.EZTEST_T8_PTY_INHERITED, inherited)
    assert.ok(artifact.stdout.includes(secret))
    assert.equal(artifact.stdout_sha256.length, 64)
    assert.equal(typeof artifact.base64, "string")
  })

  it("rejects invalid parameters and unknown ids with typed errors", async (t) => {
    const { dir, manager, engine } = await setup(t)
    const created = await manager.create({ workspace: dir })
    const session = manager.sessions.get(created.sessionId)
    for (const params of [
      { argv: [] },
      { argv: "cmd.exe" },
      { argv: [1] },
      { cols: 0 },
      { rows: -5 },
      { env: { A: 1 } },
    ]) {
      assert.throws(
        () => engine.create(session, params),
        (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
        `expected invalid_arguments for ${JSON.stringify(params)}`,
      )
    }
    assert.throws(
      () => engine.read(created.sessionId, "shell_missing", 0),
      (error) => error instanceof ExecutorError && error.code === "shell_not_found",
    )
    assert.throws(
      () => engine.read("sess_missing", "shell_missing", 0),
      (error) => error instanceof ExecutorError && error.code === "session_not_found",
    )
    const shell = engine.create(session, {})
    assert.throws(
      () => engine.write(created.sessionId, shell.shell_id, 42),
      (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
    )
    await engine.closeShell(created.sessionId, shell.shell_id)
  })

  it("spawn failure is typed and still settles an artifact", async (t) => {
    const { dir, manager, engine } = await setup(t)
    const created = await manager.create({ workspace: dir })
    const session = manager.sessions.get(created.sessionId)
    assert.throws(
      () => engine.create(session, { argv: ["eztest-no-such-shell-xyz"] }),
      (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
    )
    const artifact = await waitFor(() =>
      session.artifacts.find((a) => a.outcome === "spawn_error"),
    )
    assert.equal(artifact.kind, "command")
  })
})
describe("shell HTTP routes (v2)", () => {
  async function startServer(t) {
    const sessions = new SessionManager({ logger: null })
    const server = createExecutorServer({ token: TOKEN, sessions, logger: null, version: "test" })
    server.listen(0, "127.0.0.1")
    await new Promise((resolve) => server.once("listening", resolve))
    const { port } = server.address()
    const request = httpClient(`http://127.0.0.1:${port}`, TOKEN)
    t.after(async () => {
      // Delete sessions through the protocol first: that kills their shells
      // deterministically before the workspace directories are removed.
      for (const session of sessions.list()) {
        await request(`/v1/sessions/${session.sessionId}`, { method: "DELETE" })
      }
      await sessions.close()
      await new Promise((resolve) => server.close(resolve))
    })
    return { sessions, request }
  }

  async function createSession(request, t) {
    const workspace = await makeWorkspace(t)
    const created = await request("/v1/sessions", { method: "POST", body: { workspace } })
    assert.equal(created.status, 200, created.text)
    return created.json.sessionId
  }

  it("POST shell creates; write/read/close/wait round-trip over HTTP", async (t) => {
    const { request } = await startServer(t)
    const sessionId = await createSession(request, t)
    const created = await request(`/v1/sessions/${sessionId}/shell`, {
      method: "POST",
      body: { argv: [process.execPath] },
    })
    assert.equal(created.status, 200, created.text)
    const shellId = created.json.shell_id
    assert.equal(created.json.status, "running")

    const listed = await request(`/v1/sessions/${sessionId}/shell`)
    assert.equal(listed.status, 200)
    assert.ok(listed.json.shells.some((s) => s.shell_id === shellId))

    const wrote = await request(`/v1/sessions/${sessionId}/shell/${shellId}/write`, {
      method: "POST",
      body: { data: "console.log('HTTP_PTY_' + 4321)\r" },
    })
    assert.equal(wrote.status, 200, wrote.text)
    assert.equal(wrote.json.ok, true)

    const output = await waitFor(async () => {
      let cursor = 0
      let text = ""
      for (;;) {
        const read = await request(`/v1/sessions/${sessionId}/shell/${shellId}/read?since=${cursor}`)
        assert.equal(read.status, 200, read.text)
        text += read.json.data
        cursor = read.json.next_cursor
        if (!read.json.has_more) {
          break
        }
      }
      return text.includes("HTTP_PTY_4321") ? text : null
    })
    assert.ok(output.includes("HTTP_PTY_4321"))

    await request(`/v1/sessions/${sessionId}/shell/${shellId}/write`, {
      method: "POST",
      body: { data: ".exit\r" },
    })
    const waited = await request(`/v1/sessions/${sessionId}/shell/${shellId}/wait`, {
      method: "POST",
      body: { timeout_ms: 15000 },
    })
    assert.equal(waited.status, 200, waited.text)
    assert.equal(waited.json.exit, 0)
    assert.equal(waited.json.status, "exited")

    const fetched = await request(`/v1/sessions/${sessionId}/shell/${shellId}`)
    assert.equal(fetched.status, 200)
    assert.equal(fetched.json.status, "exited")
    assert.equal(fetched.json.exit, 0)

    const closed = await request(`/v1/sessions/${sessionId}/shell/${shellId}/close`, {
      method: "POST",
    })
    assert.equal(closed.status, 200, closed.text)
    assert.equal(closed.json.ok, true)
  })

  it("wait timeout maps to 504 executor_timeout with running details", async (t) => {
    const { request } = await startServer(t)
    const sessionId = await createSession(request, t)
    const created = await request(`/v1/sessions/${sessionId}/shell`, {
      method: "POST",
      body: { argv: [process.execPath] },
    })
    const shellId = created.json.shell_id
    const waited = await request(`/v1/sessions/${sessionId}/shell/${shellId}/wait`, {
      method: "POST",
      body: { timeout_ms: 300 },
    })
    assert.equal(waited.status, 504, waited.text)
    assert.equal(waited.json.error.code, "executor_timeout")
    assert.equal(waited.json.error.details.shell_id, shellId)
    assert.equal(waited.json.error.details.status, "running")
    const closed = await request(`/v1/sessions/${sessionId}/shell/${shellId}/close`, {
      method: "POST",
    })
    assert.equal(closed.status, 200)
  })

  it("unknown shell/session are 404; invalid bodies are 400; token required", async (t) => {
    const { request } = await startServer(t)
    const sessionId = await createSession(request, t)
    const missingShell = await request(`/v1/sessions/${sessionId}/shell/shell_missing/read`)
    assert.equal(missingShell.status, 404)
    assert.equal(missingShell.json.error.code, "shell_not_found")
    const missingSession = await request("/v1/sessions/sess_missing/shell", {
      method: "POST",
      body: {},
    })
    assert.equal(missingSession.status, 404)
    assert.equal(missingSession.json.error.code, "session_not_found")
    const badCols = await request(`/v1/sessions/${sessionId}/shell`, {
      method: "POST",
      body: { cols: 0 },
    })
    assert.equal(badCols.status, 400)
    assert.equal(badCols.json.error.code, "invalid_arguments")
    const noToken = await request(`/v1/sessions/${sessionId}/shell`, {
      method: "POST",
      body: {},
      token: undefined,
    })
    assert.equal(noToken.status, 401)
  })

  it("DELETE session closes its running shells", async (t) => {
    const { request } = await startServer(t)
    const sessionId = await createSession(request, t)
    const created = await request(`/v1/sessions/${sessionId}/shell`, {
      method: "POST",
      body: { argv: [process.execPath] },
    })
    assert.equal(created.status, 200, created.text)
    const shellId = created.json.shell_id
    const deleted = await request(`/v1/sessions/${sessionId}`, { method: "DELETE" })
    assert.equal(deleted.status, 200, deleted.text)
    const read = await request(`/v1/sessions/${sessionId}/shell/${shellId}/read`)
    assert.equal(read.status, 404)
    assert.equal(read.json.error.code, "session_not_found")
  })
})

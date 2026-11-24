// Task 7 (v0.2.4-C): exec engine lifecycle tests.
// Real child processes only (no browser): argv/shell execution, stdin, env,
// background PID, cancel kills the process group, typed command timeout.
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import crypto from "node:crypto"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { ExecEngine, DEFAULT_COMMAND_TIMEOUT_MS } from "../src/exec.mjs"
import { SessionManager } from "../src/sessions.mjs"
import { ExecutorError } from "../src/protocol.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-executor-token"

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t7-exec-"))
  t.after(() => fsp.rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))
  return dir
}

function makeEngine(t, options = {}) {
  const engine = new ExecEngine({ logger: null, ...options })
  t.after(() => engine.close())
  return engine
}

function makeSession(workspace) {
  return { id: `sess_${crypto.randomUUID()}`, workspace, artifacts: [] }
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

// A parent process that spawns a child (same process tree/group) and writes the
// child's pid to a file, then both loop forever. Used to prove cancel kills the
// whole group, not just the direct child.
const TREE_PARENT_SCRIPT = `
const { spawn } = require("node:child_process")
const fs = require("node:fs")
const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore" })
fs.writeFileSync(process.argv[2], String(child.pid))
setInterval(() => {}, 1000)
`

async function writeTreeParent(workspace) {
  const scriptPath = path.join(workspace, "tree-parent.cjs")
  const pidPath = path.join(workspace, "child-pid.txt")
  await fsp.writeFile(scriptPath, TREE_PARENT_SCRIPT)
  return { scriptPath, pidPath }
}

describe("exec engine (argv/shell lifecycle)", () => {
  it("runs argv[] and reports exit/stdout/stderr/duration", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const result = await engine.run(session, {
      argv: [
        process.execPath,
        "-e",
        "console.log('hello-out'); console.error('hello-err'); process.exit(3)",
      ],
    })
    assert.equal(result.exit, 3)
    assert.match(result.stdout, /hello-out/)
    assert.match(result.stderr, /hello-err/)
    assert.equal(typeof result.duration, "number")
    assert.ok(result.duration >= 0)
    assert.equal(typeof result.command_id, "string")
    assert.ok(result.command_id.length > 0)
    assert.equal(result.background, false)
    assert.equal(result.cancelled, false)
    assert.equal(result.timed_out, false)
  })

  it("feeds stdin to the command", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const result = await engine.run(session, {
      argv: [
        process.execPath,
        "-e",
        "let d='';process.stdin.on('data',c=>d+=c).on('end',()=>console.log('got:'+d))",
      ],
      stdin: "ping-stdin",
    })
    assert.equal(result.exit, 0)
    assert.match(result.stdout, /got:ping-stdin/)
  })

  it("shell=true runs the command line through a shell", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const result = await engine.run(session, {
      argv: ["echo shell-marker && echo second-line"],
      shell: true,
    })
    assert.equal(result.exit, 0)
    assert.match(result.stdout, /shell-marker/)
    assert.match(result.stdout, /second-line/)
  })

  it("passes extra env vars to the command", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const result = await engine.run(session, {
      argv: [process.execPath, "-e", "console.log('probe=' + process.env.EZTEST_T7_PROBE)"],
      env: { EZTEST_T7_PROBE: "probe-value-42" },
    })
    assert.equal(result.exit, 0)
    assert.match(result.stdout, /probe=probe-value-42/)
  })

  it("runs commands with cwd anchored to the session workspace", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const result = await engine.run(session, {
      argv: [process.execPath, "-e", "console.log(process.cwd())"],
    })
    assert.equal(result.exit, 0)
    assert.equal(
      path.resolve(result.stdout.trim()),
      path.resolve(workspace),
    )
  })

  it("rejects invalid exec parameters with typed invalid_arguments", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    for (const params of [
      {},
      { argv: [] },
      { argv: ["echo", 5] },
      { argv: "echo hi" },
      { argv: [process.execPath, "-e", "0"], timeout_ms: -5 },
      { argv: [process.execPath, "-e", "0"], stdin: 123 },
      { argv: [process.execPath, "-e", "0"], env: { A: 5 } },
    ]) {
      await assert.rejects(
        () => engine.run(session, params),
        (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
        `expected invalid_arguments for ${JSON.stringify(params)}`,
      )
    }
  })

  it("reports spawn failure as a typed error and still records the command", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    await assert.rejects(
      () => engine.run(session, { argv: [path.join(workspace, "no-such-binary")] }),
      (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
    )
    assert.equal(session.artifacts.length, 1)
    assert.equal(session.artifacts[0].outcome, "spawn_error")
  })
})

describe("exec engine timeout", () => {
  it("defaults the command timeout to 15 minutes", () => {
    assert.equal(DEFAULT_COMMAND_TIMEOUT_MS, 15 * 60 * 1000)
  })

  it("kills the command at timeout_ms and raises typed executor_timeout", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    await assert.rejects(
      () =>
        engine.run(session, {
          argv: [process.execPath, "-e", "setTimeout(() => {}, 60000)"],
          timeout_ms: 300,
        }),
      (error) => {
        assert.ok(error instanceof ExecutorError)
        assert.equal(error.code, "executor_timeout")
        assert.equal(typeof error.details.command_id, "string")
        assert.equal(error.details.timed_out, true)
        assert.equal(error.details.artifacts.length, 1)
        assert.equal(error.details.artifacts[0].outcome, "timeout")
        return true
      },
    )
    assert.equal(session.artifacts.length, 1)
    assert.equal(session.artifacts[0].kind, "command")
    assert.equal(session.artifacts[0].outcome, "timeout")
  })

  it("honors a configurable engine-level default timeout", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t, { defaultTimeoutMs: 250 })
    const session = makeSession(workspace)
    await assert.rejects(
      () =>
        engine.run(session, {
          argv: [process.execPath, "-e", "setTimeout(() => {}, 60000)"],
        }),
      (error) => error instanceof ExecutorError && error.code === "executor_timeout",
    )
  })
})

describe("exec engine background + cancel", () => {
  it("background=true returns immediately with a PID and settles an artifact on exit", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const result = await engine.run(session, {
      argv: [process.execPath, "-e", "setTimeout(() => console.log('bg-done'), 150)"],
      background: true,
    })
    assert.equal(result.background, true)
    assert.equal(typeof result.pid, "number")
    assert.ok(result.pid > 0)
    assert.equal(result.exit, null)
    assert.equal(typeof result.command_id, "string")
    const artifact = await waitFor(() =>
      session.artifacts.find((a) => a.command_id === result.command_id),
    )
    assert.equal(artifact.kind, "command")
    assert.equal(artifact.exit, 0)
    assert.equal(artifact.outcome, "exited")
    assert.match(artifact.stdout, /bg-done/)
  })

  it("cancel kills the whole process group (grandchild dies too)", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const { scriptPath, pidPath } = await writeTreeParent(workspace)
    const running = engine.run(session, {
      argv: [process.execPath, scriptPath, pidPath],
    })
    const childPidText = await waitFor(async () => {
      try {
        return await fsp.readFile(pidPath, "utf8")
      } catch {
        return null
      }
    })
    const childPid = Number(childPidText.trim())
    assert.ok(Number.isInteger(childPid) && childPid > 0)
    assert.ok(pidAlive(childPid), "grandchild must be alive before cancel")

    const cancelled = await engine.cancelSession(session.id)
    assert.equal(cancelled.cancelled.length, 1)

    const result = await running
    assert.equal(result.cancelled, true)
    assert.equal(result.exit, null)
    await waitFor(() => !pidAlive(childPid))
    assert.equal(session.artifacts.length, 1)
    assert.equal(session.artifacts[0].outcome, "cancelled")
  })

  it("cancel also kills background process trees", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const { scriptPath, pidPath } = await writeTreeParent(workspace)
    const result = await engine.run(session, {
      argv: [process.execPath, scriptPath, pidPath],
      background: true,
    })
    const childPidText = await waitFor(async () => {
      try {
        return await fsp.readFile(pidPath, "utf8")
      } catch {
        return null
      }
    })
    const childPid = Number(childPidText.trim())
    assert.ok(pidAlive(childPid))
    await engine.cancelSession(session.id)
    await waitFor(() => !pidAlive(childPid))
    assert.ok(!pidAlive(result.pid), "direct child must be dead after cancel")
    const artifact = await waitFor(() =>
      session.artifacts.find((a) => a.command_id === result.command_id),
    )
    assert.equal(artifact.outcome, "cancelled")
  })

  it("cancel with no running commands is a no-op", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const cancelled = await engine.cancelSession(session.id)
    assert.deepEqual(cancelled.cancelled, [])
  })
})

describe("exec HTTP routes (v2 sessions)", () => {
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

  async function createSession(t, request) {
    const workspace = await makeWorkspace(t)
    const response = await request("/v1/sessions", {
      method: "POST",
      body: { key: "t7:exec", workspace },
    })
    assert.equal(response.status, 200, response.text)
    return { workspace, sessionId: response.json.sessionId }
  }

  it("POST exec returns exit/stdout/stderr/duration/command_id/artifacts", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(t, request)
    const response = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: {
        argv: [
          process.execPath,
          "-e",
          "console.log('http-out'); console.error('http-err'); process.exit(2)",
        ],
      },
    })
    assert.equal(response.status, 200, response.text)
    assert.equal(response.json.exit, 2)
    assert.match(response.json.stdout, /http-out/)
    assert.match(response.json.stderr, /http-err/)
    assert.equal(typeof response.json.duration, "number")
    assert.equal(typeof response.json.command_id, "string")
    assert.equal(response.json.artifacts.length, 1)
    assert.equal(response.json.artifacts[0].kind, "command")
  })

  it("exec on an unknown session is 404 session_not_found", async (t) => {
    const { request } = await startServer(t)
    const response = await request("/v1/sessions/sess_missing/exec", {
      method: "POST",
      body: { argv: [process.execPath, "-e", "0"] },
    })
    assert.equal(response.status, 404)
    assert.equal(response.json.error.code, "session_not_found")
  })

  it("command timeout surfaces as 504 executor_timeout with recording details", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(t, request)
    const response = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: {
        argv: [process.execPath, "-e", "setTimeout(() => {}, 60000)"],
        timeout_ms: 300,
      },
    })
    assert.equal(response.status, 504, response.text)
    assert.equal(response.json.error.code, "executor_timeout")
    assert.equal(typeof response.json.error.details.command_id, "string")
    assert.equal(response.json.error.details.artifacts[0].outcome, "timeout")
  })

  it("POST cancel stops a running background command", async (t) => {
    const { request } = await startServer(t)
    const { workspace, sessionId } = await createSession(t, request)
    const { scriptPath, pidPath } = await writeTreeParent(workspace)
    const execResponse = await request(`/v1/sessions/${sessionId}/exec`, {
      method: "POST",
      body: { argv: [process.execPath, scriptPath, pidPath], background: true },
    })
    assert.equal(execResponse.status, 200, execResponse.text)
    const childPidText = await waitFor(async () => {
      try {
        return await fsp.readFile(pidPath, "utf8")
      } catch {
        return null
      }
    })
    const childPid = Number(childPidText.trim())
    assert.ok(pidAlive(childPid))
    const cancelResponse = await request(`/v1/sessions/${sessionId}/cancel`, {
      method: "POST",
    })
    assert.equal(cancelResponse.status, 200, cancelResponse.text)
    assert.equal(cancelResponse.json.ok, true)
    assert.equal(cancelResponse.json.result.cancelled.length, 1)
    await waitFor(() => !pidAlive(childPid))
    assert.ok(!pidAlive(execResponse.json.pid))
  })
})

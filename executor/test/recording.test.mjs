// Task 7 (v0.2.4-C): command evidence recording tests.
// Every command yields a `command` artifact: argv/cwd/env FULL plaintext
// (GD-01, R-C3 — no scrub), exit, stdio refs, duration, command_id.
// Large stdio spills to a workspace file with an explicit truncated flag.
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import crypto from "node:crypto"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { ExecEngine } from "../src/exec.mjs"
import { CommandRecorder, DEFAULT_STDIO_INLINE_LIMIT_BYTES } from "../src/recording.mjs"

function sha256(data) {
  return crypto.createHash("sha256").update(data).digest("hex")
}

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t7-rec-"))
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

describe("command recording", () => {
  it("every command yields a command artifact with argv/cwd/env/exit/stdio/duration", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const argv = [process.execPath, "-e", "console.log('rec-out'); console.error('rec-err')"]
    const result = await engine.run(session, { argv })
    assert.equal(session.artifacts.length, 1)
    const artifact = session.artifacts[0]
    assert.equal(artifact.kind, "command")
    assert.equal(artifact.command_id, result.command_id)
    assert.deepEqual(artifact.argv, argv)
    assert.equal(artifact.cwd, workspace)
    assert.equal(artifact.exit, 0)
    assert.equal(artifact.outcome, "exited")
    assert.equal(typeof artifact.duration, "number")
    assert.ok(artifact.duration >= 0)
    assert.equal(typeof artifact.env, "object")
    assert.match(artifact.stdout, /rec-out/)
    assert.match(artifact.stderr, /rec-err/)
    assert.equal(artifact.stdout_sha256, sha256(Buffer.from(result.stdout, "utf8")))
    assert.equal(artifact.stderr_sha256, sha256(Buffer.from(result.stderr, "utf8")))
    // The artifact carries a settle-ready payload (backend stores base64 bytes).
    assert.equal(artifact.filename, `command-${result.command_id}.json`)
    const record = JSON.parse(Buffer.from(artifact.base64, "base64").toString("utf8"))
    assert.equal(record.command_id, result.command_id)
    assert.deepEqual(record.argv, argv)
    assert.equal(record.exit, 0)
    assert.equal(record.cwd, workspace)
  })

  it("records env FULL and plaintext — secret values appear verbatim (GD-01, R-C3)", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const secret = "sk-super-secret-PLAINTEXT-9876"
    const result = await engine.run(session, {
      argv: [process.execPath, "-e", "console.log(process.env.EZTEST_T7_SECRET)"],
      env: { EZTEST_T7_SECRET: secret },
    })
    assert.equal(result.exit, 0)
    // Output is not scrubbed either.
    assert.match(result.stdout, /sk-super-secret-PLAINTEXT-9876/)
    const artifact = session.artifacts[0]
    // The override appears verbatim...
    assert.equal(artifact.env.EZTEST_T7_SECRET, secret)
    // ...and the FULL inherited environment is recorded, not just overrides.
    for (const key of Object.keys(process.env)) {
      assert.ok(
        Object.prototype.hasOwnProperty.call(artifact.env, key),
        `recorded env must include inherited key ${key}`,
      )
      assert.equal(artifact.env[key], process.env[key])
    }
    // The settle payload contains the secret verbatim (no masking anywhere).
    const recordText = Buffer.from(artifact.base64, "base64").toString("utf8")
    assert.ok(recordText.includes(secret))
    assert.ok(JSON.stringify(artifact).includes(secret))
  })

  it("command_ids are unique and artifacts accumulate in execution order", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const first = await engine.run(session, {
      argv: [process.execPath, "-e", "console.log('one')"],
    })
    const second = await engine.run(session, {
      argv: [process.execPath, "-e", "console.log('two')"],
    })
    assert.notEqual(first.command_id, second.command_id)
    assert.equal(session.artifacts.length, 2)
    assert.equal(session.artifacts[0].command_id, first.command_id)
    assert.equal(session.artifacts[1].command_id, second.command_id)
    assert.match(session.artifacts[0].stdout, /one/)
    assert.match(session.artifacts[1].stdout, /two/)
  })

  it("small stdio stays inline with truncated=false and no spill ref", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t)
    const session = makeSession(workspace)
    const result = await engine.run(session, {
      argv: [process.execPath, "-e", "console.log('small')"],
    })
    assert.equal(result.stdout_truncated, false)
    assert.equal(result.stderr_truncated, false)
    assert.equal(result.stdout_ref, null)
    assert.equal(result.stderr_ref, null)
    const artifact = session.artifacts[0]
    assert.equal(artifact.stdout_truncated, false)
    assert.equal(artifact.stdout_ref, null)
  })
})

describe("large stdio spill", () => {
  it("spills oversized stdout/stderr to workspace files with explicit truncated flags", async (t) => {
    const workspace = await makeWorkspace(t)
    const engine = makeEngine(t, { stdioInlineLimitBytes: 1024 })
    const session = makeSession(workspace)
    const result = await engine.run(session, {
      argv: [
        process.execPath,
        "-e",
        "process.stdout.write('a'.repeat(8192)); process.stderr.write('b'.repeat(8192))",
      ],
    })
    assert.equal(result.exit, 0)
    // Explicit truncation — never silent.
    assert.equal(result.stdout_truncated, true)
    assert.equal(result.stderr_truncated, true)
    assert.equal(result.stdout.length, 1024)
    assert.equal(result.stderr.length, 1024)
    assert.equal(result.stdout_bytes, 8192)
    assert.equal(result.stderr_bytes, 8192)
    assert.equal(typeof result.stdout_ref, "string")
    assert.equal(typeof result.stderr_ref, "string")

    // The spill files hold the FULL output and are content-addressable.
    const stdoutPath = path.join(workspace, result.stdout_ref)
    const stderrPath = path.join(workspace, result.stderr_ref)
    const stdoutFull = await fsp.readFile(stdoutPath)
    const stderrFull = await fsp.readFile(stderrPath)
    assert.equal(stdoutFull.length, 8192)
    assert.equal(stderrFull.length, 8192)
    assert.equal(stdoutFull.toString("utf8"), "a".repeat(8192))
    assert.equal(stderrFull.toString("utf8"), "b".repeat(8192))
    assert.equal(result.stdout_sha256, sha256(stdoutFull))
    assert.equal(result.stderr_sha256, sha256(stderrFull))

    const artifact = session.artifacts[0]
    assert.equal(artifact.stdout_truncated, true)
    assert.equal(artifact.stderr_truncated, true)
    assert.equal(artifact.stdout_ref, result.stdout_ref)
    assert.equal(artifact.stderr_ref, result.stderr_ref)
    assert.equal(artifact.stdout_bytes, 8192)
    assert.equal(artifact.stdout_sha256, sha256(stdoutFull))
    const record = JSON.parse(Buffer.from(artifact.base64, "base64").toString("utf8"))
    assert.equal(record.stdout_truncated, true)
    assert.equal(record.stdout_ref, result.stdout_ref)
  })

  it("the inline spill limit is configurable and defaults to a sane bound", async (t) => {
    assert.equal(typeof DEFAULT_STDIO_INLINE_LIMIT_BYTES, "number")
    assert.ok(DEFAULT_STDIO_INLINE_LIMIT_BYTES > 0)
    const workspace = await makeWorkspace(t)
    const recorder = new CommandRecorder({ inlineLimitBytes: 16 })
    const artifact = await recorder.settle({
      command_id: "cmd_direct",
      session_id: "sess_direct",
      argv: ["echo", "hi"],
      shell: false,
      cwd: workspace,
      env: { ONLY: "value" },
      pid: null,
      exit: 0,
      signal: null,
      outcome: "exited",
      duration: 5,
      started_at: new Date().toISOString(),
      stdout: Buffer.from("x".repeat(64)),
      stderr: Buffer.alloc(0),
    })
    assert.equal(artifact.stdout_truncated, true)
    assert.equal(artifact.stdout.length, 16)
    assert.equal(artifact.stdout_bytes, 64)
    const full = await fsp.readFile(path.join(workspace, artifact.stdout_ref))
    assert.equal(full.toString("utf8"), "x".repeat(64))
  })
})

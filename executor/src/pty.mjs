/**
 * Interactive PTY shells (executor v2, general-execution domain).
 *
 * Design §4.1 mandates PTY for interactive forms (ssh/psql/mysql/REPL).
 * Dependency: @homebridge/node-pty-prebuilt-multiarch (MIT) — the node-pty
 * API with published multi-arch prebuilds (win32-x64 ConPTY included), so no
 * node-gyp toolchain is required on the host. Native execution, no sandbox /
 * approval gate (GD-01); env is inherited + overrides, recorded verbatim.
 *
 * Every shell settles ONE `command` artifact on exit (argv/cwd/env full,
 * stdout = the full terminal output with spill, exit/outcome/duration) — the
 * shell is a long-running command and uses the already-declared command
 * evidence kind. Output reads are cursor-based (byte offsets); a read window
 * never drops bytes: the client pages with next_cursor until has_more=false.
 */

import crypto from "node:crypto"
import fs from "node:fs"
import path from "node:path"

import pty from "@homebridge/node-pty-prebuilt-multiarch"

import { appendArtifact, CommandRecorder } from "./recording.mjs"
import { ExecutorError, invalidArguments } from "./protocol.mjs"

export const DEFAULT_PTY_COLS = 80
export const DEFAULT_PTY_ROWS = 24
export const DEFAULT_SHELL_WAIT_TIMEOUT_MS = 30_000
// Operational read window (configurable); paging is explicit, never lossy.
export const DEFAULT_PTY_READ_LIMIT_BYTES = 64 * 1024
// Operational in-memory shell output bound (Task 9 lifecycle). Above this many
// bytes the OLDEST output spills to `<workspace>/.eztest/shells/<id>.log` and
// only a tail stays in memory; the byte cursor reads spilled bytes back from
// the file, so no output is lost. 0 = unlimited (default-open, configurable).
export const DEFAULT_SHELL_BUFFER_LIMIT_BYTES = 1024 * 1024

const DEFAULT_SHELL =
  process.platform === "win32" ? "cmd.exe" : process.env.SHELL || "bash"

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

/**
 * Releases the native ConPTY handles of a terminal. Upstream
 * @homebridge/node-pty-prebuilt-multiarch 0.15.0 kills the pty but never
 * destroys the input pipe socket (_cleanUpProcess only destroys _outSocket),
 * which keeps the executor event loop alive after every shell exit. Destroy
 * both pipe sockets explicitly — a version-pinned workaround for that leak
 * (see task-8 report); guarded so a future upstream fix changes nothing.
 */
function releaseHandles(term) {
  if (!term) {
    return
  }
  try {
    term.kill()
  } catch {
    // already released
  }
  const agent = term._agent
  try {
    agent?._inSocket?.destroy()
  } catch {
    // already destroyed
  }
  try {
    agent?._outSocket?.destroy()
  } catch {
    // already destroyed
  }
}

function validateParams(params) {
  if (!isPlainObject(params)) {
    throw invalidArguments("shell parameters must be a JSON object")
  }
  const { argv, cols = DEFAULT_PTY_COLS, rows = DEFAULT_PTY_ROWS, env } = params
  if (argv !== undefined) {
    if (!Array.isArray(argv) || argv.length === 0 || argv.some((a) => typeof a !== "string")) {
      throw invalidArguments("argv must be a non-empty array of strings")
    }
    if (argv[0].length === 0) {
      throw invalidArguments("argv[0] must not be empty")
    }
  }
  if (!Number.isInteger(cols) || cols <= 0) {
    throw invalidArguments("cols must be a positive integer")
  }
  if (!Number.isInteger(rows) || rows <= 0) {
    throw invalidArguments("rows must be a positive integer")
  }
  if (env !== undefined) {
    if (!isPlainObject(env) || Object.values(env).some((v) => typeof v !== "string")) {
      throw invalidArguments("env must be an object with string values")
    }
  }
  return { argv: argv ?? [DEFAULT_SHELL], cols, rows, env: env ?? {} }
}

export class ShellEngine {
  constructor({
    logger = null,
    recorder = null,
    waitTimeoutMs = DEFAULT_SHELL_WAIT_TIMEOUT_MS,
    readLimitBytes = DEFAULT_PTY_READ_LIMIT_BYTES,
    shellBufferLimitBytes = DEFAULT_SHELL_BUFFER_LIMIT_BYTES,
    resolveSession = null,
  } = {}) {
    this.logger = logger
    this.recorder = recorder ?? new CommandRecorder()
    // Session existence lookup (the SessionManager in server wiring). Without
    // it the engine only knows sessions it has created shells for.
    this.resolveSession = typeof resolveSession === "function" ? resolveSession : null
    this.waitTimeoutMs =
      Number.isInteger(waitTimeoutMs) && waitTimeoutMs > 0
        ? waitTimeoutMs
        : DEFAULT_SHELL_WAIT_TIMEOUT_MS
    this.readLimitBytes =
      Number.isInteger(readLimitBytes) && readLimitBytes > 0
        ? readLimitBytes
        : DEFAULT_PTY_READ_LIMIT_BYTES
    this.shellBufferLimitBytes =
      Number.isInteger(shellBufferLimitBytes) && shellBufferLimitBytes >= 0
        ? shellBufferLimitBytes
        : DEFAULT_SHELL_BUFFER_LIMIT_BYTES
    /** shell_id -> entry (exited shells stay readable until session close) */
    this.shells = new Map()
    /** sessions this engine has seen (session existence for typed errors) */
    this.knownSessions = new Set()
  }

  create(session, rawParams = {}) {
    const { argv, cols, rows, env: overrides } = validateParams(rawParams)
    // FULL environment: inherited + overrides, recorded verbatim (GD-01).
    const env = { ...process.env, ...overrides }
    const shellId = `shell_${crypto.randomUUID()}`
    const entry = {
      id: shellId,
      sessionId: session.id,
      session,
      argv,
      env,
      cols,
      rows,
      startedAt: Date.now(),
      chunks: [],
      totalBytes: 0,
      // Bounded-memory spill state (Task 9): oldest output above the buffer
      // limit is appended to spillPath; only the tail (chunks) stays in memory.
      spillPath: path.join(session.workspace, ".eztest", "shells", `${shellId}.log`),
      spillFd: null,
      spilledBytes: 0,
      tailBytes: 0,
      spillFailed: false,
      status: "running",
      exit: null,
      signal: null,
      outcome: null,
      settled: false,
      result: null,
      pty: null,
      done: null,
      resolveDone: null,
      rejectDone: null,
    }
    entry.done = new Promise((resolve, reject) => {
      entry.resolveDone = resolve
      entry.rejectDone = reject
    })
    // Nobody may be awaiting done (e.g. spawn error, session shutdown).
    entry.done.catch(() => {})
    this.knownSessions.add(session.id)

    let term
    try {
      term = pty.spawn(argv[0], argv.slice(1), {
        name: "xterm-256color",
        cols,
        rows,
        cwd: session.workspace,
        env,
      })
    } catch (error) {
      entry.spawnError = error instanceof Error ? error.message : String(error)
      void this.#settle(entry, "spawn_error", null, null)
      const typed = invalidArguments(`shell could not be spawned: ${entry.spawnError}`)
      throw typed
    }
    entry.pty = term
    term.onData((data) => {
      const chunk = Buffer.from(data, "utf8")
      entry.chunks.push(chunk)
      entry.totalBytes += chunk.length
      entry.tailBytes += chunk.length
      this.#trimBuffer(entry)
    })
    term.onExit(({ exitCode, signal }) => {
      void this.#settle(entry, entry.outcome ?? "exited", exitCode, signal ?? null)
    })
    this.shells.set(entry.id, entry)
    this.logger?.info?.(`[executor] shell created ${entry.id} session=${session.id}`)
    return this.#summary(entry)
  }

  list(sessionId) {
    this.#requireSession(sessionId)
    return [...this.shells.values()]
      .filter((entry) => entry.sessionId === sessionId)
      .map((entry) => this.#summary(entry))
  }

  get(sessionId, shellId) {
    return this.#summary(this.#requireShell(sessionId, shellId))
  }

  write(sessionId, shellId, data) {
    const entry = this.#requireShell(sessionId, shellId)
    if (typeof data !== "string") {
      throw invalidArguments("data must be a string")
    }
    if (entry.settled) {
      throw invalidArguments("shell has exited; write is no longer possible")
    }
    entry.pty.write(data)
    return { shell_id: shellId, bytes: Buffer.byteLength(data, "utf8") }
  }

  read(sessionId, shellId, since = 0) {
    const entry = this.#requireShell(sessionId, shellId)
    if (!Number.isInteger(since) || since < 0) {
      throw invalidArguments("since must be a non-negative integer byte cursor")
    }
    // The full stream is [spilled prefix on disk] + [in-memory tail]. The byte
    // cursor is absolute over the whole stream, so spilled bytes stay readable.
    const start = Math.min(since, entry.totalBytes)
    const length = Math.min(this.readLimitBytes, entry.totalBytes - start)
    const out = Buffer.alloc(length)
    let filled = 0
    if (start < entry.spilledBytes && entry.spillFd !== null) {
      const fromDisk = Math.min(length, entry.spilledBytes - start)
      let read = 0
      while (read < fromDisk) {
        const n = fs.readSync(entry.spillFd, out, read, fromDisk - read, start + read)
        if (n === 0) {
          break
        }
        read += n
      }
      filled = read
    }
    if (filled < length) {
      const tail = Buffer.concat(entry.chunks)
      tail.copy(out, filled, start + filled - entry.spilledBytes)
    }
    const nextCursor = start + length
    return {
      shell_id: shellId,
      data: out.toString("utf8"),
      since: start,
      next_cursor: nextCursor,
      total_bytes: entry.totalBytes,
      has_more: nextCursor < entry.totalBytes,
      status: entry.status,
      exit: entry.exit,
    }
  }

  /**
   * Bounds the in-memory tail: while it exceeds the buffer limit, the oldest
   * chunk is appended to the spill file (sync, so reads never race writes) and
   * dropped from memory. A spill failure keeps the chunk in memory and is
   * surfaced explicitly — output is never silently lost (constitution art. 7.6).
   */
  #trimBuffer(entry) {
    const limit = this.shellBufferLimitBytes
    if (!(limit > 0)) {
      return
    }
    while (entry.tailBytes > limit && !entry.spillFailed) {
      const head = entry.chunks[0]
      const excess = entry.tailBytes - limit
      // Spill whole chunks when possible; otherwise split the head so even a
      // single oversized chunk is bounded (the kept tail is copied to release
      // the original buffer).
      const prefix = head.length <= excess ? head : head.subarray(0, excess)
      try {
        if (entry.spillFd === null) {
          fs.mkdirSync(path.dirname(entry.spillPath), { recursive: true })
          entry.spillFd = fs.openSync(entry.spillPath, "a+")
        }
        fs.writeSync(entry.spillFd, prefix)
      } catch (error) {
        entry.spillFailed = true
        this.logger?.error?.(
          `[executor] shell output spill failed (kept in memory): ${error?.message ?? error}`,
        )
        return
      }
      if (head.length <= excess) {
        entry.chunks.shift()
      } else {
        entry.chunks[0] = Buffer.from(head.subarray(excess))
      }
      entry.spilledBytes += prefix.length
      entry.tailBytes -= prefix.length
    }
  }

  async closeShell(sessionId, shellId) {
    const entry = this.#requireShell(sessionId, shellId)
    if (!entry.settled) {
      entry.outcome = "cancelled"
    }
    // Also releases the native handles of an already-exited shell.
    releaseHandles(entry.pty)
    return entry.done
  }

  async wait(sessionId, shellId, timeoutMs = this.waitTimeoutMs) {
    const entry = this.#requireShell(sessionId, shellId)
    if (!Number.isInteger(timeoutMs) || timeoutMs <= 0) {
      throw invalidArguments("timeout_ms must be a positive integer")
    }
    if (entry.settled) {
      return entry.done
    }
    let timer
    const timeout = new Promise((_resolve, reject) => {
      timer = setTimeout(() => {
        reject(
          new ExecutorError("executor_timeout", `shell still running after ${timeoutMs}ms`, {
            shell_id: shellId,
            status: "running",
            total_bytes: entry.totalBytes,
            next_cursor: entry.totalBytes,
          }),
        )
      }, timeoutMs)
    })
    try {
      return await Promise.race([entry.done, timeout])
    } finally {
      clearTimeout(timer)
    }
  }

  /** Kills every running shell of a session and forgets it (session delete). */
  async cancelSession(sessionId) {
    const entries = [...this.shells.values()].filter((entry) => entry.sessionId === sessionId)
    const cancelled = []
    const completions = []
    for (const entry of entries) {
      if (!entry.settled) {
        entry.outcome = "cancelled"
        cancelled.push(entry.id)
      }
      // Kill running shells AND release handles of already-exited ones
      // (natural exit leaves the ConPTY pipe sockets referenced otherwise).
      releaseHandles(entry.pty)
      completions.push(entry.done)
    }
    await Promise.allSettled(completions)
    for (const entry of entries) {
      if (entry.spillFd !== null) {
        try {
          fs.closeSync(entry.spillFd)
        } catch {
          // already closed
        }
        entry.spillFd = null
      }
      this.shells.delete(entry.id)
    }
    this.knownSessions.delete(sessionId)
    return { cancelled }
  }

  async close() {
    for (const sessionId of [...this.knownSessions]) {
      await this.cancelSession(sessionId)
    }
    this.shells.clear()
    this.knownSessions.clear()
  }

  async #settle(entry, outcome, code, signal) {
    if (entry.settled) {
      return
    }
    entry.settled = true
    entry.status = "exited"
    entry.outcome = outcome
    // Windows ConPTY keeps pipe handles referenced after a NATURAL exit,
    // which would keep the executor event loop alive forever. Release them
    // shortly after exit (small grace so pending output can still drain);
    // cancelSession/closeShell release them deterministically.
    const release = setTimeout(() => releaseHandles(entry.pty), 100)
    release.unref?.()
    // A killed shell has no exit status of its own (same semantics as exec).
    entry.exit = outcome === "cancelled" ? null : (code ?? null)
    entry.signal = signal ?? null
    const duration = Date.now() - entry.startedAt
    // Settlement records the FULL output: spilled prefix + in-memory tail. The
    // prefix is read back transiently here (the running buffer stayed bounded).
    const stdout =
      entry.spilledBytes > 0
        ? Buffer.concat([fs.readFileSync(entry.spillPath), ...entry.chunks])
        : Buffer.concat(entry.chunks)
    const result = {
      shell_id: entry.id,
      exit: entry.exit,
      signal: entry.signal,
      outcome,
      duration,
      status: "exited",
    }
    entry.result = result
    try {
      const artifact = await this.recorder.settle({
        command_id: entry.id,
        session_id: entry.sessionId,
        pid: entry.pty?.pid ?? null,
        argv: entry.argv,
        shell: false,
        cwd: entry.session.workspace,
        env: entry.env,
        exit: entry.exit,
        signal: entry.signal,
        outcome,
        duration,
        started_at: new Date(entry.startedAt).toISOString(),
        stdout,
        stderr: Buffer.alloc(0),
        ...(entry.spawnError ? { spawn_error: entry.spawnError } : {}),
      })
      if (Array.isArray(entry.session.artifacts)) {
        await appendArtifact(entry.session, artifact, this.logger)
      }
    } catch (error) {
      // Recording failure must not fake success; leave an observable marker.
      this.logger?.error?.(`[executor] shell recording failed: ${error?.message ?? error}`)
      if (Array.isArray(entry.session.artifacts)) {
        await appendArtifact(entry.session, {
          kind: "command",
          command_id: entry.id,
          session_id: entry.sessionId,
          outcome: "recording_failed",
          recording_failed: true,
          error: error instanceof Error ? error.message : String(error),
          recorded_at: new Date().toISOString(),
        }, this.logger)
      }
      entry.rejectDone(
        new ExecutorError("internal", `shell recording failed: ${error?.message ?? error}`, result),
      )
      return
    }
    entry.resolveDone(result)
  }

  #requireSession(sessionId) {
    const known = this.resolveSession
      ? this.resolveSession(sessionId) != null
      : this.knownSessions.has(sessionId)
    if (!known) {
      throw new ExecutorError("session_not_found", `session ${sessionId} does not exist`)
    }
  }

  #requireShell(sessionId, shellId) {
    this.#requireSession(sessionId)
    const entry = this.shells.get(shellId)
    if (!entry || entry.sessionId !== sessionId) {
      throw new ExecutorError("shell_not_found", `shell ${shellId} does not exist`)
    }
    return entry
  }

  #summary(entry) {
    return {
      shell_id: entry.id,
      // ConPTY reports pid 0 until the agent connection lands, then the real
      // inner pid; read live so summaries converge.
      pid: entry.pty?.pid ?? 0,
      cols: entry.cols,
      rows: entry.rows,
      status: entry.status,
      exit: entry.exit,
      total_bytes: entry.totalBytes,
      started_at: new Date(entry.startedAt).toISOString(),
    }
  }
}

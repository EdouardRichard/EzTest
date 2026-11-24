/**
 * General command execution engine (executor v2, general-execution domain).
 *
 * Native execution, no sandbox / allow-list / approval gate (GD-01): argv[]
 * by default, shell=true explicitly, stdin, background processes with PID,
 * per-command timeout (default 15min, operational parameter) and cancel that
 * kills the whole PROCESS GROUP / tree:
 *   - Windows: `taskkill /pid <pid> /T /F` (tree kill)
 *   - POSIX: spawn detached (own process group) + `kill(-pid, SIGKILL)`
 *
 * Every command settles a `command` artifact through the recording layer
 * (recording.mjs) — including timed-out, cancelled and failed spawns.
 */

import { spawn } from "node:child_process"
import crypto from "node:crypto"

import { appendArtifact, CommandRecorder, DEFAULT_STDIO_INLINE_LIMIT_BYTES } from "./recording.mjs"
import { ExecutorError, invalidArguments } from "./protocol.mjs"

// Operational command deadline (configurable), not a capability restriction.
export const DEFAULT_COMMAND_TIMEOUT_MS = 15 * 60 * 1000

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function validateParams(params) {
  if (!isPlainObject(params)) {
    throw invalidArguments("exec parameters must be a JSON object")
  }
  const { argv, shell = false, stdin = null, timeout_ms: timeoutMs, background = false, env } = params
  if (!Array.isArray(argv) || argv.length === 0 || argv.some((a) => typeof a !== "string")) {
    throw invalidArguments("argv must be a non-empty array of strings")
  }
  if (argv[0].length === 0) {
    throw invalidArguments("argv[0] must not be empty")
  }
  if (typeof shell !== "boolean") {
    throw invalidArguments("shell must be a boolean")
  }
  if (stdin !== null && typeof stdin !== "string") {
    throw invalidArguments("stdin must be a string")
  }
  if (timeoutMs !== undefined && (!Number.isInteger(timeoutMs) || timeoutMs <= 0)) {
    throw invalidArguments("timeout_ms must be a positive integer")
  }
  if (typeof background !== "boolean") {
    throw invalidArguments("background must be a boolean")
  }
  if (env !== undefined) {
    if (!isPlainObject(env) || Object.values(env).some((v) => typeof v !== "string")) {
      throw invalidArguments("env must be an object with string values")
    }
  }
  return { argv, shell, stdin, timeoutMs, background, env: env ?? {} }
}

/** Kills the process tree/group rooted at `pid` (see file header). Exported so
 * lifecycle orphan recovery can kill recorded pids of a crashed executor. */
export function killPidTree(pid) {
  if (pid == null) {
    return Promise.resolve()
  }
  if (process.platform === "win32") {
    return new Promise((resolve) => {
      let settled = false
      const finish = () => {
        if (!settled) {
          settled = true
          resolve()
        }
      }
      let killer
      try {
        killer = spawn("taskkill", ["/pid", String(pid), "/T", "/F"], {
          stdio: "ignore",
          windowsHide: true,
        })
      } catch {
        finish()
        return
      }
      killer.on("error", () => finish())
      killer.on("close", finish)
    })
  }
  // POSIX: children were spawned detached, so pid is the process-group id.
  try {
    process.kill(-pid, "SIGKILL")
  } catch {
    try {
      process.kill(pid, "SIGKILL")
    } catch {
      // already dead
    }
  }
  return Promise.resolve()
}

/** Kills the process tree/group rooted at `child`. */
function killTree(child) {
  if (!child || child.pid == null) {
    return Promise.resolve()
  }
  if (process.platform === "win32") {
    return new Promise((resolve) => {
      let settled = false
      const finish = () => {
        if (!settled) {
          settled = true
          resolve()
        }
      }
      let killer
      try {
        killer = spawn("taskkill", ["/pid", String(child.pid), "/T", "/F"], {
          stdio: "ignore",
          windowsHide: true,
        })
      } catch {
        try {
          child.kill("SIGKILL")
        } catch {
          // already dead
        }
        finish()
        return
      }
      killer.on("error", () => {
        try {
          child.kill("SIGKILL")
        } catch {
          // already dead
        }
        finish()
      })
      killer.on("close", finish)
    })
  }
  // POSIX: children were spawned detached, so pid is the process-group id.
  try {
    process.kill(-child.pid, "SIGKILL")
  } catch {
    try {
      child.kill("SIGKILL")
    } catch {
      // already dead
    }
  }
  return Promise.resolve()
}

export class ExecEngine {
  constructor({
    logger = null,
    defaultTimeoutMs = DEFAULT_COMMAND_TIMEOUT_MS,
    stdioInlineLimitBytes = DEFAULT_STDIO_INLINE_LIMIT_BYTES,
    recorder = null,
  } = {}) {
    this.logger = logger
    this.defaultTimeoutMs =
      Number.isInteger(defaultTimeoutMs) && defaultTimeoutMs > 0
        ? defaultTimeoutMs
        : DEFAULT_COMMAND_TIMEOUT_MS
    this.recorder = recorder ?? new CommandRecorder({ inlineLimitBytes: stdioInlineLimitBytes })
    /** command_id -> live command entry */
    this.commands = new Map()
  }

  runningFor(sessionId) {
    return [...this.commands.values()].filter(
      (entry) => entry.sessionId === sessionId && !entry.settled,
    )
  }

  /**
   * Runs one command in the session workspace. Foreground: resolves with the
   * full result (exit/stdout/stderr/duration/command_id/artifacts). Background:
   * resolves immediately with {command_id, pid}; the artifact settles into
   * session.artifacts when the process ends. Timeout rejects with a typed
   * executor_timeout whose details carry the recorded result.
   */
  async run(session, rawParams = {}) {
    const { argv, shell, stdin, timeoutMs, background, env: overrides } = validateParams(rawParams)
    const commandId = `cmd_${crypto.randomUUID()}`
    const cwd = session.workspace
    // FULL environment: inherited + overrides, recorded verbatim (GD-01).
    const env = { ...process.env, ...overrides }
    const startedAt = Date.now()
    const entry = {
      id: commandId,
      sessionId: session.id,
      child: null,
      pid: null,
      outcome: null,
      spawnError: null,
      settled: false,
      done: null,
    }
    this.commands.set(commandId, entry)

    let resolveRun
    let rejectRun
    const settlePromise = new Promise((resolve, reject) => {
      resolveRun = resolve
      rejectRun = reject
    })
    entry.done = settlePromise.catch(() => {})

    const stdoutChunks = []
    const stderrChunks = []
    let timer = null

    const finalize = async (outcome, code, signal) => {
      if (entry.settled) {
        return
      }
      entry.settled = true
      this.commands.delete(commandId)
      if (timer) {
        clearTimeout(timer)
      }
      const stdout = Buffer.concat(stdoutChunks)
      const stderr = Buffer.concat(stderrChunks)
      const duration = Date.now() - startedAt
      // A killed process has no exit status of its own (Windows taskkill /F
      // surfaces code 1); report exit=null for cancelled/timed-out commands.
      const exitCode = outcome === "cancelled" || outcome === "timeout" ? null : code
      let artifact = null
      try {
        artifact = await this.recorder.settle({
          command_id: commandId,
          session_id: session.id,
          pid: entry.pid,
          argv,
          shell,
          cwd,
          env,
          exit: exitCode,
          signal,
          outcome,
          duration,
          started_at: new Date(startedAt).toISOString(),
          stdout,
          stderr,
          ...(entry.spawnError ? { spawn_error: entry.spawnError } : {}),
        })
        if (Array.isArray(session.artifacts)) {
          await appendArtifact(session, artifact, this.logger)
        }
      } catch (error) {
        // Recording failure must not fake success: surface it as an error.
        this.logger?.error?.(`[executor] command recording failed: ${error?.message ?? error}`)
        rejectRun(
          new ExecutorError("internal", `command recording failed: ${error?.message ?? error}`),
        )
        return
      }
      const result = {
        command_id: commandId,
        pid: entry.pid,
        exit: exitCode,
        signal,
        outcome,
        duration,
        stdout: artifact.stdout,
        stderr: artifact.stderr,
        stdout_truncated: artifact.stdout_truncated,
        stderr_truncated: artifact.stderr_truncated,
        stdout_ref: artifact.stdout_ref,
        stderr_ref: artifact.stderr_ref,
        stdout_bytes: artifact.stdout_bytes,
        stderr_bytes: artifact.stderr_bytes,
        stdout_sha256: artifact.stdout_sha256,
        stderr_sha256: artifact.stderr_sha256,
        background,
        timed_out: outcome === "timeout",
        cancelled: outcome === "cancelled",
        artifacts: [artifact],
        ...(entry.spawnError ? { spawn_error: entry.spawnError } : {}),
      }
      if (outcome === "timeout") {
        rejectRun(
          new ExecutorError(
            "executor_timeout",
            `command timed out after ${timeoutMs ?? this.defaultTimeoutMs}ms`,
            result,
          ),
        )
        return
      }
      if (outcome === "spawn_error") {
        const error = invalidArguments(`command could not be spawned: ${entry.spawnError}`)
        error.details = result
        rejectRun(error)
        return
      }
      resolveRun(result)
    }

    let child
    try {
      const options = {
        cwd,
        env,
        shell,
        windowsHide: true,
        // POSIX: own process group so cancel can kill(-pid). Windows kills the
        // tree via taskkill /T instead (detached would open a new console).
        detached: process.platform !== "win32",
        stdio: ["pipe", "pipe", "pipe"],
      }
      child = shell
        ? spawn(argv.join(" "), [], options)
        : spawn(argv[0], argv.slice(1), options)
    } catch (error) {
      entry.spawnError = error instanceof Error ? error.message : String(error)
      await finalize("spawn_error", null, null)
      return settlePromise
    }
    entry.child = child
    entry.pid = child.pid ?? null
    child.stdout.on("data", (chunk) => stdoutChunks.push(chunk))
    child.stderr.on("data", (chunk) => stderrChunks.push(chunk))
    child.on("error", (error) => {
      entry.spawnError = error instanceof Error ? error.message : String(error)
      void finalize("spawn_error", null, null)
    })
    child.on("close", (code, signal) => {
      void finalize(entry.outcome ?? (entry.spawnError ? "spawn_error" : "exited"), code, signal)
    })
    if (child.stdin) {
      child.stdin.on("error", () => {
        // EPIPE when the command exits before reading stdin — not a failure.
      })
      if (stdin) {
        child.stdin.write(stdin)
      }
      child.stdin.end()
    }

    // The deadline bounds a foreground wait. Background processes run until
    // cancel/session delete (a dev server must not die at the command timeout).
    if (!background) {
      const effectiveTimeout = timeoutMs ?? this.defaultTimeoutMs
      timer = setTimeout(() => {
        if (!entry.settled && entry.outcome == null) {
          entry.outcome = "timeout"
          void killTree(child)
        }
      }, effectiveTimeout)
    }

    if (background) {
      return {
        command_id: commandId,
        pid: entry.pid,
        background: true,
        status: "running",
        exit: null,
        signal: null,
        duration: 0,
        stdout: "",
        stderr: "",
        artifacts: [],
      }
    }
    return settlePromise
  }

  /**
   * Cancels every running command of a session (process-group kill). Returns
   * the cancelled command ids plus a `completion` promise that settles when
   * the killed commands have been recorded.
   */
  async cancelSession(sessionId) {
    const running = this.runningFor(sessionId)
    const cancelled = []
    const completions = []
    for (const entry of running) {
      if (entry.outcome == null) {
        entry.outcome = "cancelled"
      }
      cancelled.push(entry.id)
      completions.push(entry.done)
      void killTree(entry.child)
    }
    return { cancelled, completion: Promise.allSettled(completions) }
  }

  /** Cancels everything still running (process shutdown). */
  async close() {
    const sessionIds = new Set([...this.commands.values()].map((entry) => entry.sessionId))
    const completions = []
    for (const sessionId of sessionIds) {
      const { completion } = await this.cancelSession(sessionId)
      completions.push(completion)
    }
    await Promise.allSettled(completions)
    this.commands.clear()
  }
}

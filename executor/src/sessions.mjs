/**
 * Session management (executor v2): CRUD + per-session workspace anchor +
 * lifecycle (Task 9). A session is the unit the backend creates per case_run;
 * `workspace` is passed in at creation and becomes the cwd anchor for every
 * command and the spill root for recordings. No read-only root / host
 * isolation (GD-01) — the workspace may be any path the caller names.
 *
 * Lifecycle controls are OPERATIONAL resource management (design §4.1), not
 * GD-01 security gates: capacity backpressure (429 + Retry-After), drain
 * (reject new, let existing finish), lease TTL + epoch fencing (aligned with
 * the backend dispatch epoch) and crash-orphan reclaim. Defaults are open and
 * configurable; drain is reversible; workspace retention defaults to keep.
 */

import crypto from "node:crypto"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { DEFAULT_COMMAND_TIMEOUT_MS, ExecEngine, killPidTree } from "./exec.mjs"
import { DEFAULT_ARTIFACT_BUFFER_LIMIT, DEFAULT_STDIO_INLINE_LIMIT_BYTES } from "./recording.mjs"
import { ExecutorError, invalidArguments } from "./protocol.mjs"

// Operational bound for the small correlation object (same scale as v1
// context metadata); not a capability restriction.
export const MAX_SESSION_METADATA_BYTES = 2048

// Operational lifecycle defaults (configurable). Capacity 8 mirrors the v1
// context default; lease TTL 300s aligns the backend
// execution.executor_context_ttl_seconds=300 semantic (design §4.1).
export const DEFAULT_MAX_SESSIONS = 8
export const DEFAULT_LEASE_TTL_MS = 300_000
export const DEFAULT_RECLAIM_INTERVAL_MS = 10_000
export const DEFAULT_STATE_DIR = path.join(os.tmpdir(), "eztest-executor-sessions")
const RECLAIM_LOG_LIMIT = 100

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function positiveInt(value, fallback) {
  return Number.isInteger(value) && value > 0 ? value : fallback
}

export class SessionManager {
  constructor({
    logger = null,
    engine = null,
    commandTimeoutMs = DEFAULT_COMMAND_TIMEOUT_MS,
    stdioInlineLimitBytes = DEFAULT_STDIO_INLINE_LIMIT_BYTES,
    maxSessions = DEFAULT_MAX_SESSIONS,
    leaseTtlMs = DEFAULT_LEASE_TTL_MS,
    reclaimIntervalMs = DEFAULT_RECLAIM_INTERVAL_MS,
    artifactBufferLimit = DEFAULT_ARTIFACT_BUFFER_LIMIT,
    workspaceRetention = "keep",
    stateDir = DEFAULT_STATE_DIR,
  } = {}) {
    this.logger = logger
    this.engine =
      engine ??
      new ExecEngine({
        logger,
        defaultTimeoutMs: commandTimeoutMs,
        stdioInlineLimitBytes,
      })
    this.maxSessions = positiveInt(maxSessions, DEFAULT_MAX_SESSIONS)
    this.leaseTtlMs = positiveInt(leaseTtlMs, DEFAULT_LEASE_TTL_MS)
    this.reclaimIntervalMs =
      Number.isInteger(reclaimIntervalMs) && reclaimIntervalMs >= 0
        ? reclaimIntervalMs
        : DEFAULT_RECLAIM_INTERVAL_MS
    // 0 = unlimited buffer (default-open); a positive cap bounds memory.
    this.artifactBufferLimit =
      Number.isInteger(artifactBufferLimit) && artifactBufferLimit >= 0
        ? artifactBufferLimit
        : DEFAULT_ARTIFACT_BUFFER_LIMIT
    this.workspaceRetention = workspaceRetention === "delete" ? "delete" : "keep"
    this.stateDir = stateDir
    /** sessionId -> live session record (with artifacts array) */
    this.sessions = new Map()
    this.draining = false
    this.reclaimedCount = 0
    /** bounded ring of reclaim facts (observable, never silent) */
    this.reclaimed = []
    // Optional hooks wired by the HTTP server: cleanup of shells/watchers on
    // reclaim, and live shell pids for the crash-recovery registry.
    this.onReclaim = null
    // onClose runs for EVERY session teardown (explicit delete, reclaim, and
    // shutdown close) before the record is removed — the server uses it to close
    // the session's browser CLI sessions so no detached daemon is orphaned.
    this.onClose = null
    this.externalProcesses = null
    this.registryTail = Promise.resolve()
    this.sweepTimer = null
    if (this.reclaimIntervalMs > 0) {
      this.sweepTimer = setInterval(() => {
        this.sweep().catch((error) =>
          this.logger?.error?.(`[executor] session sweep failed: ${error?.message ?? error}`),
        )
      }, this.reclaimIntervalMs)
      this.sweepTimer.unref?.()
    }
  }

  get size() {
    return this.sessions.size
  }

  async create(params = {}) {
    if (!isPlainObject(params)) {
      throw invalidArguments("session parameters must be a JSON object")
    }
    // Drain rejects NEW sessions; existing ones keep running until close/expiry.
    if (this.draining) {
      throw new ExecutorError("draining", "executor is draining; new sessions are rejected")
    }
    // Capacity backpressure: 429 + Retry-After (NOT 409), hinting the earliest
    // lease expiry so the caller knows when a slot is likely to free up.
    if (this.sessions.size >= this.maxSessions) {
      const earliest = Math.min(...[...this.sessions.values()].map((s) => s.leaseExpiresAt))
      const retryAfter = Math.max(1, Math.ceil((earliest - Date.now()) / 1000))
      throw new ExecutorError(
        "session_limit",
        `session capacity ${this.maxSessions} reached`,
        { retry_after_seconds: retryAfter },
      )
    }
    const { key, workspace, metadata, lease, base_url: baseUrl } = params
    if (typeof workspace !== "string" || workspace.trim().length === 0) {
      throw invalidArguments("workspace must be a non-empty string path")
    }
    if (key !== undefined && typeof key !== "string") {
      throw invalidArguments("key must be a string")
    }
    // base_url is an informational hint for the retained browser bridge (it
    // resolves relative navigation against it). It is not a capability gate.
    if (baseUrl !== undefined && typeof baseUrl !== "string") {
      throw invalidArguments("base_url must be a string")
    }
    if (metadata !== undefined && !isPlainObject(metadata)) {
      throw invalidArguments("metadata must be a JSON object")
    }
    if (metadata !== undefined && Buffer.byteLength(JSON.stringify(metadata), "utf8") > MAX_SESSION_METADATA_BYTES) {
      throw invalidArguments(`metadata exceeds the ${MAX_SESSION_METADATA_BYTES} byte limit`)
    }
    if (lease !== undefined && !isPlainObject(lease)) {
      throw invalidArguments("lease must be a JSON object")
    }
    const leaseObj = lease ?? {}
    const leaseEpoch = leaseObj.lease_epoch ?? 0
    const leaseTtlMs = leaseObj.ttl_ms ?? this.leaseTtlMs
    if (!Number.isInteger(leaseEpoch) || leaseEpoch < 0) {
      throw invalidArguments("lease.lease_epoch must be a non-negative integer")
    }
    if (!Number.isInteger(leaseTtlMs) || leaseTtlMs <= 0) {
      throw invalidArguments("lease.ttl_ms must be a positive integer")
    }
    const anchor = path.resolve(workspace)
    // Anchor integrity (T8 re-review): the confinement anchor must be a REAL
    // directory. A symlink/junction anchor would let the files channel resolve
    // outside the intended workspace, so reject it before creating anything.
    try {
      const stat = await fsp.lstat(anchor)
      if (stat.isSymbolicLink()) {
        throw invalidArguments("workspace anchor must be a real directory, not a symlink/junction")
      }
      if (!stat.isDirectory()) {
        throw invalidArguments("workspace anchor must be a directory")
      }
    } catch (error) {
      if (error instanceof ExecutorError) {
        throw error
      }
      if (error?.code !== "ENOENT") {
        throw invalidArguments(
          `workspace could not be inspected: ${error instanceof Error ? error.message : error}`,
        )
      }
    }
    try {
      await fsp.mkdir(anchor, { recursive: true })
    } catch (error) {
      throw invalidArguments(
        `workspace could not be created: ${error instanceof Error ? error.message : error}`,
      )
    }
    const session = {
      id: `sess_${crypto.randomUUID()}`,
      key: key ?? null,
      workspace: anchor,
      baseUrl: baseUrl ?? null,
      metadata: metadata ?? {},
      // Stored verbatim for the caller; enforcement uses the typed fields below.
      lease: lease ?? null,
      // A session created with an explicit lease requires lease_epoch on every
      // write (omission would bypass fencing). Lease-less legacy sessions may
      // omit it (Task 9 review finding 1).
      leaseExplicit: lease !== undefined,
      leaseEpoch,
      leaseTtlMs,
      leaseExpiresAt: Date.now() + leaseTtlMs,
      // Set synchronously at the reclaim commit point so a late takeover cannot
      // race the delete (Task 9 review finding 2).
      reclaiming: false,
      createdAt: new Date().toISOString(),
      artifacts: [],
      artifactBase: 0,
      artifactsEvicted: 0,
      artifactEvictionFailed: 0,
      artifactBufferLimit: this.artifactBufferLimit,
      commandCount: 0,
      // Browser CLI channel alignment (browser_cli.mjs): the CLI named sessions
      // this executor session opened (1:1 by default), their daemon pids for
      // crash recovery, the CLI output ledger/claims, and explicit output targets.
      cliSessions: new Set(),
      cliDaemons: new Map(),
      cliLedger: new Map(),
      cliArtifactClaims: new Map(),
      cliOutputs: [],
    }
    this.sessions.set(session.id, session)
    await this.#queueRegistry(session)
    this.logger?.info?.(`[executor] session created ${session.id} workspace=${anchor}`)
    return this.#summary(session)
  }

  list() {
    return [...this.sessions.values()].map((session) => this.#summary(session))
  }

  get(id) {
    return this.#summary(this.#require(id))
  }

  async exec(id, params) {
    const session = this.#require(id)
    const epoch = isPlainObject(params) ? params.lease_epoch : undefined
    this.#assertLease(session, epoch)
    session.commandCount += 1
    const runPromise = this.engine.run(session, params)
    // Capture the spawned pid(s) in the registry for crash-orphan recovery.
    void this.#queueRegistry(session)
    try {
      return await runPromise
    } finally {
      void this.#queueRegistry(session)
    }
  }

  /**
   * Fences and renews a write operation (shell/files routes call this before
   * mutating). Returns the live session record. Reads/cancel/delete never fence.
   */
  touch(id, epoch) {
    return this.#assertLease(this.#require(id), epoch)
  }

  /** Explicit lease renewal (POST .../lease). A higher epoch may take over an
   * expired lease before the sweeper reclaims it (re-claim semantics). */
  renew(id, params = {}) {
    const session = this.#require(id)
    const epoch = isPlainObject(params) ? params.lease_epoch : undefined
    this.#assertLease(session, epoch, { allowExpiredTakeover: true })
    return {
      sessionId: session.id,
      lease_epoch: session.leaseEpoch,
      lease_expires_at: new Date(session.leaseExpiresAt).toISOString(),
      lease_ttl_ms: session.leaseTtlMs,
    }
  }

  drain(enabled = true) {
    this.draining = enabled !== false
    this.logger?.info?.(`[executor] drain ${this.draining ? "enabled" : "disabled"}`)
    return { draining: this.draining }
  }

  async cancel(id) {
    this.#require(id)
    const { cancelled } = await this.engine.cancelSession(id)
    return { cancelled }
  }

  async delete(id) {
    const session = this.#require(id)
    const { cancelled, completion } = await this.engine.cancelSession(id)
    await completion
    // Close the session's browser CLI sessions (graceful daemon shutdown) before
    // the record goes away. Best effort: a failure is logged, never blocks delete.
    try {
      await this.onClose?.(session)
    } catch (error) {
      this.logger?.error?.(`[executor] session close hook failed for ${id}: ${error?.message ?? error}`)
    }
    this.sessions.delete(id)
    await this.#removeRegistry(id)
    // The workspace directory is intentionally kept on explicit delete:
    // retention/reclaim is a lifecycle decision, never an implicit side effect.
    this.logger?.info?.(`[executor] session deleted ${id} cancelled=${cancelled.length}`)
    return { sessionId: id, cancelled }
  }

  /** Reclaims every lease-expired session (periodic tick + manual trigger). */
  async sweep() {
    const now = Date.now()
    const expired = [...this.sessions.values()].filter((session) => session.leaseExpiresAt <= now)
    const reclaimed = []
    for (const session of expired) {
      const fact = await this.#reclaim(session, "lease_expired")
      // A null fact means the session was re-claimed during the reclaim window
      // and survives (Task 9 review finding 2).
      if (fact) {
        reclaimed.push(fact)
      }
    }
    // Refresh the registry pids of survivors so a later crash recovers them.
    for (const session of this.sessions.values()) {
      void this.#queueRegistry(session)
    }
    return reclaimed
  }

  /**
   * Startup scan: reclaims sessions left by a crashed executor. The registry is
   * per-owner-pid; an entry whose owner pid is dead is an orphan (this process
   * just started, so live-owner entries belong to another running executor and
   * are left untouched). Recorded process trees are killed; the workspace is
   * retained or deleted per policy.
   */
  async recoverOrphans() {
    const reclaimed = []
    let owners
    try {
      owners = await fsp.readdir(this.stateDir, { withFileTypes: true })
    } catch (error) {
      if (error?.code === "ENOENT") {
        return { reclaimed }
      }
      throw error
    }
    for (const owner of owners) {
      if (!owner.isDirectory()) {
        continue
      }
      const ownerPid = Number(owner.name)
      if (!Number.isInteger(ownerPid) || ownerPid <= 0 || ownerPid === process.pid) {
        continue
      }
      if (pidAlive(ownerPid)) {
        continue
      }
      const ownerDir = path.join(this.stateDir, owner.name)
      let files
      try {
        files = await fsp.readdir(ownerDir)
      } catch {
        continue
      }
      for (const file of files) {
        if (!file.endsWith(".json")) {
          continue
        }
        const fullPath = path.join(ownerDir, file)
        let record
        try {
          record = JSON.parse(await fsp.readFile(fullPath, "utf8"))
        } catch {
          continue
        }
        for (const pid of Array.isArray(record.pids) ? record.pids : []) {
          await killPidTree(pid)
        }
        const workspaceRetained = await this.#applyRetention(record.workspace)
        await fsp.rm(fullPath, { force: true })
        reclaimed.push(
          this.#recordReclaim({
            session_id: record.session_id ?? file.replace(/\.json$/, ""),
            workspace: record.workspace ?? null,
            reason: "startup_orphan",
            workspace_retained: workspaceRetained,
          }),
        )
      }
      try {
        await fsp.rmdir(ownerDir)
      } catch {
        // not empty (a live entry remained) — leave it
      }
    }
    return { reclaimed }
  }

  async close() {
    if (this.sweepTimer) {
      clearInterval(this.sweepTimer)
      this.sweepTimer = null
    }
    for (const id of [...this.sessions.keys()]) {
      await this.delete(id)
    }
    await this.engine.close()
    await this.registryTail.catch(() => {})
    try {
      await fsp.rmdir(this.#registryDir())
    } catch {
      // best effort: own registry dir removed when empty
    }
  }

  #require(id) {
    const session = this.sessions.get(id)
    if (!session) {
      throw new ExecutorError("session_not_found", `session ${id} does not exist`)
    }
    return session
  }

  /**
   * Lease fencing + activity renewal. An expired lease rejects writes
   * (lease_expired); a stale epoch is fenced (lease_lost); a higher epoch takes
   * over (aligns the backend dispatch epoch). Any accepted write renew the TTL.
   *
   * A session created with an explicit lease REQUIRES lease_epoch on every write
   * — omission would bypass the stale check and renew a zombie lease (review
   * finding 1). Only lease-less legacy sessions may omit it. A session already
   * committed to reclaim rejects takeover (review finding 2).
   */
  #assertLease(session, epoch, { allowExpiredTakeover = false } = {}) {
    if (epoch !== undefined && (!Number.isInteger(epoch) || epoch < 0)) {
      throw invalidArguments("lease_epoch must be a non-negative integer")
    }
    if (session.reclaiming) {
      throw new ExecutorError(
        "session_reclaiming",
        `session ${session.id} is being reclaimed and can no longer be taken over`,
        { lease_epoch: session.leaseEpoch },
      )
    }
    if (epoch === undefined && (session.leaseExplicit || session.leaseEpoch > 0)) {
      throw new ExecutorError(
        "lease_epoch_required",
        `session ${session.id} has a lease; writes must carry lease_epoch`,
        { lease_epoch: session.leaseEpoch },
      )
    }
    const now = Date.now()
    const expired = now >= session.leaseExpiresAt
    const takeover = epoch !== undefined && epoch > session.leaseEpoch
    if (expired && !(allowExpiredTakeover && takeover)) {
      throw new ExecutorError(
        "lease_expired",
        `session ${session.id} lease expired at ${new Date(session.leaseExpiresAt).toISOString()}`,
        { lease_epoch: session.leaseEpoch, lease_expires_at: new Date(session.leaseExpiresAt).toISOString() },
      )
    }
    if (epoch !== undefined && epoch < session.leaseEpoch) {
      throw new ExecutorError(
        "lease_lost",
        `lease epoch ${epoch} is stale (current ${session.leaseEpoch})`,
        { lease_epoch: session.leaseEpoch, provided_epoch: epoch },
      )
    }
    if (takeover) {
      session.leaseEpoch = epoch
    }
    session.leaseExpiresAt = now + session.leaseTtlMs
    void this.#queueRegistry(session)
    return session
  }

  async #reclaim(session, reason) {
    const id = session.id
    const workspace = session.workspace
    // Abort if the session was re-claimed before cleanup starts (review finding
    // 2): a higher-epoch takeover renewed the lease, so it legitimately survives.
    if (session.leaseExpiresAt > Date.now()) {
      return null
    }
    try {
      await this.onReclaim?.(id)
    } catch (error) {
      this.logger?.error?.(`[executor] reclaim cleanup failed for ${id}: ${error?.message ?? error}`)
    }
    // Re-check AFTER the (potentially slow) cleanup: a takeover during the
    // window re-claimed the session; abort so it is not destroyed. This check
    // and the reclaiming mark below are synchronous (no await between), so no
    // takeover can slip between the decision and the commit.
    if (session.leaseExpiresAt > Date.now()) {
      return null
    }
    session.reclaiming = true
    await this.delete(id)
    const workspaceRetained = await this.#applyRetention(workspace)
    return this.#recordReclaim({ session_id: id, workspace, reason, workspace_retained: workspaceRetained })
  }

  async #applyRetention(workspace) {
    if (this.workspaceRetention !== "delete" || typeof workspace !== "string") {
      return true
    }
    try {
      await fsp.rm(workspace, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 })
      return false
    } catch (error) {
      this.logger?.error?.(`[executor] workspace reclaim delete failed: ${error?.message ?? error}`)
      return true
    }
  }

  #recordReclaim(entry) {
    const fact = { ...entry, reclaimed_at: new Date().toISOString() }
    this.reclaimedCount += 1
    this.reclaimed.push(fact)
    if (this.reclaimed.length > RECLAIM_LOG_LIMIT) {
      this.reclaimed.splice(0, this.reclaimed.length - RECLAIM_LOG_LIMIT)
    }
    this.logger?.info?.(
      `[executor] session reclaimed ${fact.session_id} reason=${fact.reason} workspace_retained=${fact.workspace_retained}`,
    )
    return fact
  }

  #registryDir() {
    return path.join(this.stateDir, String(process.pid))
  }

  #processesFor(session) {
    const pids = this.engine.runningFor(session.id).map((entry) => entry.pid)
    let external = []
    try {
      external = this.externalProcesses?.(session.id) ?? []
    } catch {
      external = []
    }
    return [...pids, ...external].filter((pid) => Number.isInteger(pid) && pid > 0)
  }

  #queueRegistry(session) {
    const run = this.registryTail.then(() => this.#writeRegistry(session))
    this.registryTail = run.catch(() => {})
    return run
  }

  async #writeRegistry(session) {
    try {
      const dir = this.#registryDir()
      await fsp.mkdir(dir, { recursive: true })
      const record = {
        session_id: session.id,
        workspace: session.workspace,
        key: session.key,
        lease_epoch: session.leaseEpoch,
        lease_expires_at: new Date(session.leaseExpiresAt).toISOString(),
        ttl_ms: session.leaseTtlMs,
        executor_pid: process.pid,
        pids: this.#processesFor(session),
        updated_at: new Date().toISOString(),
      }
      await fsp.writeFile(path.join(dir, `${session.id}.json`), JSON.stringify(record), "utf8")
    } catch (error) {
      // Registry is a crash-recovery aid; a write failure degrades recovery but
      // must not fail the session. Surface it explicitly (no silent degradation).
      this.logger?.warn?.(`[executor] session registry write failed: ${error?.message ?? error}`)
    }
  }

  async #removeRegistry(id) {
    try {
      await fsp.rm(path.join(this.#registryDir(), `${id}.json`), { force: true })
    } catch {
      // best effort
    }
  }

  #summary(session) {
    return {
      sessionId: session.id,
      key: session.key,
      workspace: session.workspace,
      metadata: session.metadata,
      lease: session.lease,
      lease_epoch: session.leaseEpoch,
      lease_expires_at: new Date(session.leaseExpiresAt).toISOString(),
      lease_ttl_ms: session.leaseTtlMs,
      createdAt: session.createdAt,
      commandCount: session.commandCount,
      runningCommands: this.engine.runningFor(session.id).length,
      artifactCount: session.artifacts.length,
      artifact_base: session.artifactBase,
      artifacts_evicted: session.artifactsEvicted,
    }
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
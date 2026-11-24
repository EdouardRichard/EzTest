/**
 * Workspace file-change auto-recording (recording layer, executor v2).
 *
 * New/modified workspace files become immutable `file` artifacts
 * (sha256/size/mime/path + settle-ready content). Two complementary paths:
 *   - fs.watch (recursive) with a debounce → settles changes while commands
 *     are still running (background processes, uploads, PTY output files);
 *   - settlement-time full scan → the authoritative fallback that catches
 *     missed/coalesced watch events (fs.watch is best-effort on every OS).
 *
 * A per-path sha256 ledger dedupes: unchanged content never re-ledgers;
 * modified content re-ledgers as a NEW artifact (append-only evidence).
 * The recording layer's own spill directory `.eztest/` is excluded — it is
 * recording output, not a workspace change. Files above the artifact limit
 * ledger metadata with an explicit content_omitted flag (never silent).
 */

import crypto from "node:crypto"
import fs from "node:fs"
import fsp from "node:fs/promises"
import path from "node:path"

import { DEFAULT_ARTIFACT_LIMIT_BYTES } from "./files.mjs"
import { appendArtifact, settleFileArtifact } from "./recording.mjs"

// The recording layer's own directory (command stdio spill, traces, ...).
const RECORDING_DIR = ".eztest"
// The browser CLI channel's output directory (snapshots/screenshots/console/
// traces). browser_cli.mjs owns its settlement with typed evidence kinds, so
// the generic file watcher excludes it to avoid double-ledgering the same bytes.
const CLI_OUTPUT_DIR = ".playwright-cli"
const DEFAULT_DEBOUNCE_MS = 150

/** Streaming sha256 for files too large to buffer. */
export function hashFile(absolutePath) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash("sha256")
    const stream = fs.createReadStream(absolutePath)
    stream.on("data", (chunk) => hash.update(chunk))
    stream.on("error", reject)
    stream.on("end", () => resolve(hash.digest("hex")))
  })
}

function isRecordingPath(relative) {
  return (
    relative === RECORDING_DIR ||
    relative.startsWith(`${RECORDING_DIR}/`) ||
    relative === CLI_OUTPUT_DIR ||
    relative.startsWith(`${CLI_OUTPUT_DIR}/`)
  )
}

export class WorkspaceWatcher {
  constructor(
    session,
    { artifactLimitBytes = DEFAULT_ARTIFACT_LIMIT_BYTES, debounceMs = DEFAULT_DEBOUNCE_MS, logger = null } = {},
  ) {
    this.session = session
    this.artifactLimitBytes =
      Number.isInteger(artifactLimitBytes) && artifactLimitBytes > 0
        ? artifactLimitBytes
        : DEFAULT_ARTIFACT_LIMIT_BYTES
    this.debounceMs = Number.isInteger(debounceMs) && debounceMs >= 0 ? debounceMs : DEFAULT_DEBOUNCE_MS
    this.logger = logger
    /** relative path -> {sha256, size, mtimeMs} */
    this.ledger = new Map()
    this.watcher = null
    this.timer = null
    this.closed = false
    // Settles are serialized so concurrent watch events and explicit
    // settlement scans never double-ledger the same change.
    this.tail = Promise.resolve()
  }

  start() {
    if (this.watcher || this.closed) {
      return
    }
    try {
      this.watcher = fs.watch(this.session.workspace, { recursive: true }, (_event, filename) => {
        this.#onEvent(filename)
      })
      this.watcher.on("error", (error) => {
        // Watch is best-effort; the settlement scan is the fallback. Surface
        // the degradation in the log instead of failing the session.
        this.logger?.error?.(`[executor] workspace watch error: ${error?.message ?? error}`)
      })
    } catch (error) {
      this.logger?.error?.(`[executor] workspace watch unavailable: ${error?.message ?? error}`)
    }
  }

  #onEvent(filename) {
    if (this.closed) {
      return
    }
    if (typeof filename === "string") {
      const relative = filename.split(path.sep).join("/")
      if (isRecordingPath(relative)) {
        return
      }
    }
    if (this.timer) {
      clearTimeout(this.timer)
    }
    this.timer = setTimeout(() => {
      this.timer = null
      this.settle().catch((error) => {
        this.logger?.error?.(`[executor] workspace watch settle failed: ${error?.message ?? error}`)
      })
    }, this.debounceMs)
    // The debounce must not keep the process alive on its own.
    this.timer.unref?.()
  }

  /**
   * Scans the workspace and ledgers every new/modified file. `command_id`
   * attributes the change to the command whose settlement triggered the scan
   * (null for watch-driven/upload changes). Returns the produced artifacts.
   */
  settle({ command_id = null } = {}) {
    const run = this.tail.then(() => this.#scan(command_id))
    this.tail = run.catch(() => {})
    return run
  }

  async #scan(commandId) {
    if (this.closed) {
      return []
    }
    const produced = []
    const seen = new Set()
    const stack = [this.session.workspace]
    while (stack.length > 0) {
      const dir = stack.pop()
      let entries
      try {
        entries = await fsp.readdir(dir, { withFileTypes: true })
      } catch (error) {
        if (error?.code === "ENOENT") {
          continue
        }
        throw error
      }
      for (const entry of entries) {
        const absolute = path.join(dir, entry.name)
        const relative = path
          .relative(this.session.workspace, absolute)
          .split(path.sep)
          .join("/")
        if (isRecordingPath(relative) || entry.isSymbolicLink()) {
          continue
        }
        if (entry.isDirectory()) {
          stack.push(absolute)
          continue
        }
        if (!entry.isFile()) {
          continue
        }
        let stat
        try {
          stat = await fsp.stat(absolute)
        } catch (error) {
          if (error?.code === "ENOENT") {
            continue
          }
          throw error
        }
        seen.add(relative)
        const known = this.ledger.get(relative)
        if (known && known.size === stat.size && known.mtimeMs === stat.mtimeMs) {
          continue
        }
        // Small files: read once, hash the exact bytes that get ledgered.
        // Large files: stream the hash, omit content explicitly.
        let content = null
        let sha
        if (stat.size <= this.artifactLimitBytes) {
          try {
            content = await fsp.readFile(absolute)
          } catch (error) {
            if (error?.code === "ENOENT") {
              continue
            }
            throw error
          }
          sha = crypto.createHash("sha256").update(content).digest("hex")
        } else {
          sha = await hashFile(absolute)
        }
        if (known && known.sha256 === sha) {
          // Touch without content change: refresh the stat shortcut only.
          this.ledger.set(relative, { sha256: sha, size: stat.size, mtimeMs: stat.mtimeMs })
          continue
        }
        // The browser CLI channel already settled this exact content as a typed
        // artifact (snapshot/screenshot/...); do not double-ledger it as a file.
        if (this.session.cliArtifactClaims?.get(relative) === sha) {
          this.ledger.set(relative, { sha256: sha, size: stat.size, mtimeMs: stat.mtimeMs })
          continue
        }
        const artifact = settleFileArtifact({
          session_id: this.session.id,
          path: relative,
          sha256: sha,
          size: stat.size,
          content,
          command_id: commandId,
        })
        if (Array.isArray(this.session.artifacts)) {
          await appendArtifact(this.session, artifact, this.logger)
        }
        produced.push(artifact)
        this.ledger.set(relative, { sha256: sha, size: stat.size, mtimeMs: stat.mtimeMs })
      }
    }
    // Deleted files leave the ledger so a later re-creation re-ledgers.
    for (const key of this.ledger.keys()) {
      if (!seen.has(key)) {
        this.ledger.delete(key)
      }
    }
    return produced
  }

  async close() {
    this.closed = true
    if (this.timer) {
      clearTimeout(this.timer)
      this.timer = null
    }
    this.watcher?.close()
    this.watcher = null
    await this.tail
  }
}

/** Per-session watcher registry owned by the HTTP server wiring. */
export class WorkspaceWatchers {
  constructor({ artifactLimitBytes = DEFAULT_ARTIFACT_LIMIT_BYTES, logger = null } = {}) {
    this.artifactLimitBytes = artifactLimitBytes
    this.logger = logger
    this.watchers = new Map()
  }

  forSession(session) {
    let watcher = this.watchers.get(session.id)
    if (!watcher) {
      watcher = new WorkspaceWatcher(session, {
        artifactLimitBytes: this.artifactLimitBytes,
        logger: this.logger,
      })
      watcher.start()
      this.watchers.set(session.id, watcher)
    }
    return watcher
  }

  settleFor(session, options = {}) {
    return this.forSession(session).settle(options)
  }

  async closeFor(sessionId) {
    const watcher = this.watchers.get(sessionId)
    if (watcher) {
      this.watchers.delete(sessionId)
      await watcher.close()
    }
  }

  async closeAll() {
    for (const sessionId of [...this.watchers.keys()]) {
      await this.closeFor(sessionId)
    }
  }
}

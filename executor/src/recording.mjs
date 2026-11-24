/**
 * Evidence recording (recording layer, executor v2).
 *
 * Every executed command settles a `command` artifact: argv/cwd/env FULL
 * plaintext (GD-01, R-C3 — credentials are plaintext by design, no scrub),
 * exit/signal/outcome, stdio with sha256, duration and command_id. Workspace
 * file changes settle `file` artifacts and playwright trace files settle
 * `trace` artifacts (settleFileArtifact/settleTraceArtifact below); all kinds
 * are declared in the single ARTIFACT_KINDS registry — undeclared kinds fail
 * closed (constitution art. 4.5).
 *
 * Large stdio spills to a workspace file: the inline value keeps a head slice,
 * `*_truncated` is set explicitly and `*_ref` points at the full content
 * (workspace-relative). Nothing is silently dropped (constitution art. 7.6);
 * a failed settle leaves an observable recording_failed marker when the
 * session is resolvable.
 *
 * Artifacts carry settle-ready `base64` payloads so the backend can store
 * them content-addressed as evidence without a second serialization.
 */

import crypto from "node:crypto"
import fsp from "node:fs/promises"
import path from "node:path"

import { ExecutorError } from "./protocol.mjs"

// Operational spill bound (not a capability restriction): stdio above this
// size is written to a workspace file and referenced, never discarded.
export const DEFAULT_STDIO_INLINE_LIMIT_BYTES = 64 * 1024

// Operational per-session in-memory artifact retention (Task 9 lifecycle). A
// long session must not grow its artifact array without bound; above this count
// the OLDEST artifacts are evicted from memory. Eviction is NOT a silent drop:
// each evicted artifact is spilled to `<workspace>/.eztest/artifacts/<seq>.json`
// (recoverable through the files channel) and counted (`artifactsEvicted`), and
// the pull cursor exposes an explicit gap so the backend can degrade honestly
// (constitution art. 7.6). 0 = unlimited (default-open, configurable).
export const DEFAULT_ARTIFACT_BUFFER_LIMIT = 512

/**
 * Appends an artifact to a session's in-memory buffer, enforcing the bounded
 * retention. The absolute sequence of an artifact is `artifactBase + index`;
 * eviction advances `artifactBase` so the `?since=` cursor stays monotonic.
 * Tolerates sessions without buffer fields (unit-test fixtures): no limit set
 * means unlimited, matching the pre-lifecycle behavior.
 */
export async function appendArtifact(session, artifact, logger = null) {
  if (!Array.isArray(session.artifacts)) {
    return artifact
  }
  session.artifacts.push(artifact)
  const limit = session.artifactBufferLimit
  if (!Number.isInteger(limit) || limit <= 0) {
    return artifact
  }
  while (session.artifacts.length > limit) {
    const seq = session.artifactBase ?? 0
    const oldest = session.artifacts[0]
    try {
      const dir = path.join(session.workspace, ".eztest", "artifacts")
      await fsp.mkdir(dir, { recursive: true })
      await fsp.writeFile(path.join(dir, `${seq}.json`), JSON.stringify(oldest), "utf8")
    } catch (error) {
      // Spill failed: keep the artifact in memory (no evidence loss) and surface
      // the degradation explicitly instead of dropping it silently.
      session.artifactEvictionFailed = (session.artifactEvictionFailed ?? 0) + 1
      logger?.error?.(
        `[executor] artifact eviction spill failed (kept in memory): ${error?.message ?? error}`,
      )
      break
    }
    session.artifacts.shift()
    session.artifactBase = seq + 1
    session.artifactsEvicted = (session.artifactsEvicted ?? 0) + 1
  }
  return artifact
}

// Single registry of artifact kinds this executor may emit (constitution
// art. 4.5: an undeclared kind fails closed, never silently passes). The
// backend evidence registry declares the matching evidence kinds. The browser
// channel (official playwright CLI, browser_cli.mjs) produces snapshot /
// screenshot / console / trace / download / file artifacts from the CLI's
// output files; snapshot is the Q06 producer the evidence enum lacked. The
// browser traffic recorder (traffic_recorder.mjs, T16) produces `network`
// artifacts from passive CDP capture (pure recording, no interception).
export const ARTIFACT_KINDS = Object.freeze([
  "command",
  "file",
  "trace",
  "screenshot",
  "snapshot",
  "console",
  "network",
  "download",
])

export function assertArtifactKind(kind) {
  if (!ARTIFACT_KINDS.includes(kind)) {
    throw new ExecutorError("internal", `undeclared artifact kind: ${kind}`)
  }
  return kind
}

// Extension-based MIME mapping for workspace files (no external dependency).
const MIME_BY_EXTENSION = new Map([
  [".txt", "text/plain"],
  [".log", "text/plain"],
  [".md", "text/plain"],
  [".sql", "text/plain"],
  [".json", "application/json"],
  [".html", "text/html"],
  [".htm", "text/html"],
  [".css", "text/css"],
  [".js", "text/javascript"],
  [".mjs", "text/javascript"],
  [".csv", "text/csv"],
  [".xml", "application/xml"],
  [".png", "image/png"],
  [".jpg", "image/jpeg"],
  [".jpeg", "image/jpeg"],
  [".gif", "image/gif"],
  [".svg", "image/svg+xml"],
  [".webp", "image/webp"],
  [".pdf", "application/pdf"],
  [".zip", "application/zip"],
  [".mp4", "video/mp4"],
  [".wasm", "application/wasm"],
])

export function mimeForPath(filePath) {
  return MIME_BY_EXTENSION.get(path.extname(String(filePath)).toLowerCase()) ??
    "application/octet-stream"
}

/**
 * Settles one workspace file change into a `file` artifact. `content` is the
 * full file Buffer, or null when the file exceeds the artifact transfer limit
 * — in that case the metadata (sha256/size/mime/path) is still ledgered with
 * an EXPLICIT content_omitted flag + reason (never a silent drop).
 */
export function settleFileArtifact({
  session_id,
  path: relativePath,
  sha256,
  size,
  content,
  command_id = null,
  content_reason = "exceeds_artifact_limit",
}) {
  assertArtifactKind("file")
  const artifact = {
    kind: "file",
    session_id,
    path: relativePath,
    sha256,
    size,
    mime: mimeForPath(relativePath),
    command_id: command_id ?? null,
    filename: String(relativePath).split("/").pop(),
    base64: Buffer.isBuffer(content) ? content.toString("base64") : null,
  }
  if (!Buffer.isBuffer(content)) {
    artifact.content_omitted = true
    artifact.content_reason = content_reason
  }
  return artifact
}

/** Settles a playwright trace file into a `trace` artifact (same shape). */
export function settleTraceArtifact({ session_id, path: relativePath, sha256, size, content }) {
  assertArtifactKind("trace")
  const artifact = {
    kind: "trace",
    session_id,
    path: relativePath,
    sha256,
    size,
    mime: mimeForPath(relativePath),
    command_id: null,
    filename: String(relativePath).split("/").pop(),
    base64: Buffer.isBuffer(content) ? content.toString("base64") : null,
  }
  if (!Buffer.isBuffer(content)) {
    artifact.content_omitted = true
    artifact.content_reason = "exceeds_artifact_limit"
  }
  return artifact
}

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

export class CommandRecorder {
  constructor({ inlineLimitBytes = DEFAULT_STDIO_INLINE_LIMIT_BYTES, resolveSession = null } = {}) {
    this.inlineLimitBytes =
      Number.isInteger(inlineLimitBytes) && inlineLimitBytes > 0
        ? inlineLimitBytes
        : DEFAULT_STDIO_INLINE_LIMIT_BYTES
    // Optional session lookup so a FAILED settle still leaves an observable
    // recording_failed marker in session.artifacts (background commands have
    // no HTTP response to carry the error — constitution art. 7.6).
    this.resolveSession = typeof resolveSession === "function" ? resolveSession : null
  }

  /**
   * Settles one command record into a `command` artifact. `stdout`/`stderr`
   * are Buffers holding the FULL output; `cwd` is the session workspace anchor
   * (spill files land under `<cwd>/.eztest/commands/`). On failure a
   * recording_failed marker is ledgered (when the session is resolvable) and
   * the error is rethrown — recording failure never fakes success.
   */
  async settle(record) {
    assertArtifactKind("command")
    try {
      return await this.#build(record)
    } catch (error) {
      const session = this.resolveSession?.(record.session_id)
      if (session && Array.isArray(session.artifacts)) {
        await appendArtifact(session, {
          kind: "command",
          command_id: record.command_id,
          session_id: record.session_id,
          outcome: "recording_failed",
          recording_failed: true,
          error: error instanceof Error ? error.message : String(error),
          recorded_at: new Date().toISOString(),
        })
      }
      throw error
    }
  }

  async #build(record) {
    const stdout = await this.#settleStdio(record, "stdout", record.stdout)
    const stderr = await this.#settleStdio(record, "stderr", record.stderr)
    const artifact = {
      kind: "command",
      command_id: record.command_id,
      session_id: record.session_id,
      pid: record.pid ?? null,
      argv: record.argv,
      shell: record.shell === true,
      cwd: record.cwd,
      // FULL inherited + override environment, verbatim (GD-01: no scrub).
      env: record.env,
      exit: record.exit ?? null,
      signal: record.signal ?? null,
      outcome: record.outcome,
      duration: record.duration,
      started_at: record.started_at,
      ...stdout,
      ...stderr,
      ...(record.spawn_error ? { spawn_error: record.spawn_error } : {}),
    }
    artifact.filename = `command-${record.command_id}.json`
    artifact.base64 = Buffer.from(JSON.stringify(artifact), "utf8").toString("base64")
    return artifact
  }

  async #settleStdio(record, stream, buffer) {
    const data = Buffer.isBuffer(buffer) ? buffer : Buffer.alloc(0)
    const fields = {
      [stream]: data.subarray(0, this.inlineLimitBytes).toString("utf8"),
      [`${stream}_truncated`]: data.length > this.inlineLimitBytes,
      [`${stream}_ref`]: null,
      [`${stream}_bytes`]: data.length,
      [`${stream}_sha256`]: sha256(data),
    }
    if (data.length > this.inlineLimitBytes) {
      const relative = `.eztest/commands/${record.command_id}.${stream}.log`
      const target = path.join(record.cwd, ...relative.split("/"))
      await fsp.mkdir(path.dirname(target), { recursive: true })
      await fsp.writeFile(target, data)
      fields[`${stream}_ref`] = relative
    }
    return fields
  }
}

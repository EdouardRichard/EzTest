/**
 * HTTP entry point for the executor protocol v2 (sessions + exec/shell/PTY/
 * files/artifacts/trace).
 *
 * Transport: JSON over HTTP on 127.0.0.1:13336 by default, authenticated with
 * `Authorization: Bearer $EZTEST_EXECUTOR_TOKEN`. The token is only accepted in
 * the header and is never logged; request bodies are capped at 1MB.
 *
 * v2-only (v0.2.4-C R-C2): the whole v1 context family was deleted on both sides
 * in one change set. The transitional Playwright-library browser bridge was
 * removed in T16: the browser capability domain runs the official playwright-cli
 * (T15), and browser traffic is PURE-RECORDED through a passive CDP observer
 * (traffic_recorder.mjs, design §4.3) — no interception, no Playwright library.
 */

import crypto from "node:crypto"
import fs from "node:fs"
import fsp from "node:fs/promises"
import http from "node:http"
import path from "node:path"
import { pathToFileURL } from "node:url"

import {
  browserCliSubcommand,
  cliDaemonPids,
  cliVersionInfo,
  closeBrowserCliSessions,
  isBrowserCliArgv,
  prepareBrowserCliInvocation,
  recordCliDaemonPid,
  settleBrowserCliArtifacts,
} from "./browser_cli.mjs"
import {
  injectTrafficRecording,
  readSessionNetworkLog,
  settleNetworkCapture,
  startTrafficRecorder,
  stopTrafficRecorder,
} from "./traffic_recorder.mjs"
import { DEFAULT_COMMAND_TIMEOUT_MS, ExecEngine } from "./exec.mjs"
import {
  DEFAULT_ARTIFACT_LIMIT_BYTES,
  DEFAULT_JSON_BODY_LIMIT_BYTES,
  WorkspaceFiles,
} from "./files.mjs"
import { ShellEngine, DEFAULT_SHELL_BUFFER_LIMIT_BYTES } from "./pty.mjs"
import { startPushFromEnv } from "./push.mjs"
import {
  appendArtifact,
  assertArtifactKind,
  CommandRecorder,
  DEFAULT_ARTIFACT_BUFFER_LIMIT,
  DEFAULT_STDIO_INLINE_LIMIT_BYTES,
  mimeForPath,
  settleTraceArtifact,
} from "./recording.mjs"
import {
  ExecutorError,
  MAX_REQUEST_BYTES,
} from "./protocol.mjs"
import {
  DEFAULT_LEASE_TTL_MS,
  DEFAULT_MAX_SESSIONS,
  DEFAULT_RECLAIM_INTERVAL_MS,
  DEFAULT_STATE_DIR,
  SessionManager,
} from "./sessions.mjs"
import { hashFile, WorkspaceWatchers } from "./workspace_watch.mjs"

const PACKAGE_VERSION = (() => {
  try {
    const pkg = JSON.parse(fs.readFileSync(new URL("../package.json", import.meta.url), "utf8"))
    return typeof pkg.version === "string" ? pkg.version : "0.0.0"
  } catch {
    return "0.0.0"
  }
})()

function sendJson(res, status, payload, extraHeaders = {}) {
  const body = JSON.stringify(payload)
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "content-length": Buffer.byteLength(body),
    ...extraHeaders,
  })
  res.end(body)
}

function sendError(res, status, code, message) {
  sendJson(res, status, { ok: false, error: { code, message } })
}

function statusForError(code) {
  switch (code) {
    case "unauthorized":
      return 401
    case "invalid_request":
    case "invalid_arguments":
      return 400
    case "context_not_found":
    case "session_not_found":
    case "shell_not_found":
    case "file_not_found":
      return 404
    case "context_limit":
    case "lease_expired":
    case "lease_lost":
    case "lease_epoch_required":
    case "session_reclaiming":
      return 409
    case "too_large":
      return 413
    case "session_limit":
      // Capacity backpressure: 429 + Retry-After (NOT 409), design §4.1.
      return 429
    case "draining":
      return 503
    case "browser_cli_unavailable":
      // The official playwright CLI runtime is not installed in this image.
      return 503
    case "executor_timeout":
      // The API maps this back to its typed executor_timeout code.
      return 504
    default:
      return 500
  }
}

// Request-id header the API injects into every executor call (v0.2.3 X-01).
const REQUEST_ID_HEADER = "x-ezt-test-request-id"

function isAuthorized(req, token) {
  const header = req.headers.authorization
  if (typeof header !== "string" || !header.startsWith("Bearer ")) {
    return false
  }
  const provided = Buffer.from(header.slice("Bearer ".length))
  const expected = Buffer.from(token)
  return provided.length === expected.length && crypto.timingSafeEqual(provided, expected)
}

export function readBody(req, limit = MAX_REQUEST_BYTES) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let total = 0
    let settled = false
    const finish = (callback, value) => {
      if (settled) {
        return
      }
      settled = true
      callback(value)
    }
    req.on("data", (chunk) => {
      if (settled) {
        return
      }
      total += chunk.length
      if (total > limit) {
        finish(
          reject,
          new ExecutorError("too_large", `request body exceeds the ${limit} byte limit`),
        )
        req.resume()
        return
      }
      chunks.push(chunk)
    })
    req.on("end", () => finish(resolve, Buffer.concat(chunks)))
    req.on("error", () =>
      finish(reject, new ExecutorError("invalid_request", "request body could not be read")),
    )
    req.on("aborted", () =>
      finish(reject, new ExecutorError("invalid_request", "client aborted the request")),
    )
    req.on("close", () => {
      // IncomingMessage also emits "close" after a complete body; only an
      // incomplete body means the client went away before the request ended.
      if (!req.complete) {
        finish(reject, new ExecutorError("invalid_request", "client aborted the request"))
      }
    })
  })
}

function parseJsonObject(buffer) {
  if (buffer.length === 0) {
    return {}
  }
  let value
  try {
    value = JSON.parse(buffer.toString("utf8"))
  } catch {
    throw new ExecutorError("invalid_request", "request body must be valid JSON")
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new ExecutorError("invalid_request", "request body must be a JSON object")
  }
  return value
}

async function readJsonBody(req, limit = MAX_REQUEST_BYTES) {
  return parseJsonObject(await readBody(req, limit))
}

function decodeId(raw) {
  try {
    return decodeURIComponent(raw)
  } catch {
    throw new ExecutorError("invalid_arguments", "id is not valid URL encoding")
  }
}

/**
 * Builds the v2 session manager with the recording failure ledger wired: a
 * failed CommandRecorder.settle leaves an observable recording_failed marker
 * in session.artifacts (background commands have no response to carry it).
 */
export function makeSessionManager({
  logger = null,
  commandTimeoutMs = DEFAULT_COMMAND_TIMEOUT_MS,
  stdioInlineLimitBytes = DEFAULT_STDIO_INLINE_LIMIT_BYTES,
  maxSessions = DEFAULT_MAX_SESSIONS,
  leaseTtlMs = DEFAULT_LEASE_TTL_MS,
  reclaimIntervalMs = DEFAULT_RECLAIM_INTERVAL_MS,
  artifactBufferLimit = DEFAULT_ARTIFACT_BUFFER_LIMIT,
  workspaceRetention = "keep",
  stateDir = DEFAULT_STATE_DIR,
} = {}) {
  let manager
  const recorder = new CommandRecorder({
    inlineLimitBytes: stdioInlineLimitBytes,
    resolveSession: (id) => manager?.sessions.get(id) ?? null,
  })
  const engine = new ExecEngine({ logger, defaultTimeoutMs: commandTimeoutMs, recorder })
  manager = new SessionManager({
    logger,
    engine,
    maxSessions,
    leaseTtlMs,
    reclaimIntervalMs,
    artifactBufferLimit,
    workspaceRetention,
    stateDir,
  })
  return manager
}

export function createExecutorServer({
  token,
  sessions,
  files,
  version = PACKAGE_VERSION,
  logger = null,
  shellBufferLimitBytes = DEFAULT_SHELL_BUFFER_LIMIT_BYTES,
} = {}) {
  if (typeof token !== "string" || token.length === 0) {
    throw new Error("EZTEST_EXECUTOR_TOKEN is required")
  }
  const sessionManager = sessions ?? makeSessionManager({ logger })
  // Task 8: workspace files, PTY shells, file-change recording, trace refs.
  const filesService = files ?? new WorkspaceFiles()
  const shells = new ShellEngine({
    logger,
    shellBufferLimitBytes,
    resolveSession: (id) => sessionManager.sessions.get(id) ?? null,
  })
  const watchers = new WorkspaceWatchers({
    artifactLimitBytes: filesService.artifactLimitBytes,
    logger,
  })
  // trace path -> sha256 already settled (one trace artifact per content).
  const traceSeen = new Map()
  // Task 9 lifecycle wiring: reclaim must clean up shells/watchers/trace state,
  // and the crash-recovery registry records live shell pids alongside commands.
  sessionManager.onReclaim = async (id) => {
    await shells.cancelSession(id)
    await watchers.closeFor(id)
    stopTrafficRecorder(sessionManager.sessions.get(id))
    traceSeen.delete(id)
  }
  // Every teardown (delete/reclaim/shutdown) closes the session's browser CLI
  // sessions so no detached CLI daemon (and its browser) is orphaned, and stops
  // the passive traffic recorder (no orphan CDP connection).
  sessionManager.onClose = async (session) => {
    stopTrafficRecorder(session)
    await closeBrowserCliSessions(session, { logger })
  }
  sessionManager.externalProcesses = (id) => {
    const session = sessionManager.sessions.get(id)
    if (!session) {
      return []
    }
    // Live PTY shell pids + browser CLI daemon pids (the daemon is detached, so
    // crash-orphan recovery must kill it explicitly).
    return [...shells.list(id).map((shell) => shell.pid), ...cliDaemonPids(session)]
  }
  const requireSession = (id) => {
    sessionManager.get(id) // typed session_not_found when unknown
    return sessionManager.sessions.get(id)
  }
  const server = http.createServer((req, res) => {
    handle(req, res).catch((error) => {
      if (res.destroyed || res.writableEnded) {
        return
      }
      if (res.headersSent) {
        res.destroy()
        return
      }
      const code = error instanceof ExecutorError ? error.code : "internal"
      // Capacity backpressure carries a Retry-After hint (earliest lease expiry).
      const extraHeaders =
        code === "session_limit" && Number.isInteger(error?.details?.retry_after_seconds)
          ? { "retry-after": String(error.details.retry_after_seconds) }
          : {}
      sendJson(res, statusForError(code), {
        ok: false,
        error: {
          code,
          message: error instanceof Error ? error.message : "internal executor error",
          // Typed errors may carry structured details (e.g. executor_timeout
          // keeps the recorded command result reachable for settlement).
          ...(error instanceof ExecutorError && error.details !== undefined
            ? { details: error.details }
            : {}),
        },
      }, extraHeaders)
    })
  })

  // Backstop for shutdown paths that close the HTTP server directly: kill
  // remaining PTY shells and stop workspace watchers.
  server.once("close", () => {
    void shells.close()
    void watchers.closeAll()
  })

  async function handle(req, res) {
    const startedAt = Date.now()
    let pathname = "/"
    let parsedSearch = new URLSearchParams()
    try {
      const parsed = new URL(req.url ?? "/", "http://127.0.0.1")
      pathname = parsed.pathname
      parsedSearch = parsed.searchParams
    } catch {
      // Keep the default path; the request will 404 below.
    }
    const requestId = String(req.headers[REQUEST_ID_HEADER] ?? "").trim()
    // Start and finish are separate lines so a hung request is still visible in
    // the executor log with the API-side correlation id (X-01).
    logger?.info?.(
      `[executor] request start ${req.method} ${pathname} request_id=${requestId || "-"}`,
    )
    res.once("finish", () => {
      logger?.info?.(
        `[executor] request finish ${req.method} ${pathname} ${res.statusCode} ${Date.now() - startedAt}ms request_id=${requestId || "-"}`,
      )
    })

    if (pathname === "/healthz") {
      if (req.method !== "GET") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      // The probe reports the SESSION service, not the browser: exec/shell/files
      // need no browser, so a missing Chromium must not degrade the workstation.
      // It never launches one (a health probe with a browser side effect wedged
      // the v1 surface). Drain is the only degraded state.
      const draining = sessionManager.draining
      // Reproducibility fingerprint (art.4.6): the floating CLI version AND the
      // engine it pins. Null when the CLI runtime is not installed.
      const cli = cliVersionInfo()
      return sendJson(res, draining ? 503 : 200, {
        status: draining ? "degraded" : "ok",
        version,
        sessions: sessionManager.size,
        maxSessions: sessionManager.maxSessions,
        draining,
        reclaimedSessions: sessionManager.reclaimedCount,
        ...(cli ? { browserCli: cli } : {}),
        ...(draining ? { error: "executor is draining; new sessions are rejected" } : {}),
      })
    }

    if (!isAuthorized(req, token)) {
      return sendError(res, 401, "unauthorized", "missing or invalid bearer token")
    }

    // ---- v2 lifecycle: drain (Task 9) ----

    if (pathname === "/v1/drain") {
      if (req.method === "GET") {
        return sendJson(res, 200, { draining: sessionManager.draining })
      }
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const body = await readJsonBody(req)
      return sendJson(res, 200, sessionManager.drain(body.enabled))
    }

    // ---- v2 protocol surface: sessions + exec (Task 7) ----

    if (pathname === "/v1/sessions") {
      if (req.method === "GET") {
        return sendJson(res, 200, { sessions: sessionManager.list() })
      }
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const body = await readJsonBody(req)
      const created = await sessionManager.create(body)
      return sendJson(res, 200, created)
    }

    const sessionExecMatch = /^\/v1\/sessions\/([^/]+)\/exec$/.exec(pathname)
    if (sessionExecMatch) {
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const sessionId = decodeId(sessionExecMatch[1])
      const session = requireSession(sessionId)
      const body = await readJsonBody(req)
      // Browser CLI convenience: align the CLI named session with this executor
      // session and resolve the installed CLI entry. Non-CLI argv is unchanged.
      const prepared = prepareBrowserCliInvocation(session, body)
      const isCli = isBrowserCliArgv(prepared.argv)
      const cliSub = isCli ? browserCliSubcommand(prepared.argv) : null
      // Traffic PURE RECORDING (T16, design §4.3): a browser `open` injects an
      // extra CDP debugging port through the CLI config so the passive recorder
      // can attach after launch. Injection never blocks the browser open; a
      // failure degrades recording explicitly below.
      let toRun = prepared
      let recorderPromise = null
      if (cliSub === "open") {
        toRun = await injectTrafficRecording(session, prepared, { logger })
        // Attach the passive recorder IN PARALLEL with the browser launch so it
        // is observing before the initial navigation completes (the first page
        // load is recorded too). Injection failure degrades explicitly below.
        if (!session.trafficInjectionError) {
          recorderPromise = startTrafficRecorder(session, { cdpPort: session.trafficCdpPort, logger })
        }
      }
      let result
      try {
        result = await sessionManager.exec(sessionId, toRun)
      } catch (error) {
        // A timed-out/cancelled CLI command may still have written output files;
        // settle them so the partial recording is not lost (art.4.5/7.6).
        if (isCli) {
          const partial = error?.details
          await settleBrowserCliArtifacts(session, {
            command_id: partial?.command_id ?? null,
            artifactLimitBytes: filesService.artifactLimitBytes,
            logger,
          }).catch(() => {})
          await watchers.settleFor(session, { command_id: partial?.command_id ?? null }).catch(() => {})
        }
        if (recorderPromise) {
          await recorderPromise.catch(() => {})
        }
        throw error
      }
      if (isCli) {
        recordCliDaemonPid(session, result.stdout)
        await settleBrowserCliArtifacts(session, {
          command_id: result.command_id,
          artifactLimitBytes: filesService.artifactLimitBytes,
          logger,
        })
      }
      if (cliSub === "open") {
        // Await the parallel attach (or record an explicit injection degradation).
        // A failed attach degrades EXPLICITLY: startTrafficRecorder settles a
        // degraded network artifact + the read reports degraded:true (browser
        // recording only, never silent).
        if (recorderPromise) {
          await recorderPromise
        } else {
          await startTrafficRecorder(session, {
            cdpPort: null,
            logger,
            degradationReason: session.trafficInjectionError ?? "cdp_inject_failed",
          })
        }
        session.trafficInjectionError = null
      }
      // Settlement-time scan: files this command wrote become file artifacts
      // attributed to it (the watch debounce covers background writes too).
      await watchers.settleFor(session, { command_id: result.command_id })
      return sendJson(res, 200, result)
    }

    const sessionCancelMatch = /^\/v1\/sessions\/([^/]+)\/cancel$/.exec(pathname)
    if (sessionCancelMatch) {
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const sessionId = decodeId(sessionCancelMatch[1])
      const result = await sessionManager.cancel(sessionId)
      // Cleanup is never lease-fenced. Cancel stops the session's running
      // commands; the browser CLI daemon idles out via its own session close.
      return sendJson(res, 200, { ok: true, result })
    }

    // Lease renewal / epoch takeover (Task 9 fencing contract).
    const sessionLeaseMatch = /^\/v1\/sessions\/([^/]+)\/lease$/.exec(pathname)
    if (sessionLeaseMatch) {
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const sessionId = decodeId(sessionLeaseMatch[1])
      const body = await readJsonBody(req)
      return sendJson(res, 200, sessionManager.renew(sessionId, body))
    }

    // ---- v2 PTY shell surface (Task 8) ----

    const shellCollectionMatch = /^\/v1\/sessions\/([^/]+)\/shell$/.exec(pathname)
    if (shellCollectionMatch) {
      const sessionId = decodeId(shellCollectionMatch[1])
      const session = requireSession(sessionId)
      if (req.method === "GET") {
        return sendJson(res, 200, { shells: shells.list(sessionId) })
      }
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const body = await readJsonBody(req)
      sessionManager.touch(sessionId, body.lease_epoch) // fence + renew (Task 9)
      return sendJson(res, 200, shells.create(session, body))
    }

    const shellOpMatch = /^\/v1\/sessions\/([^/]+)\/shell\/([^/]+)\/(write|read|close|wait)$/.exec(pathname)
    if (shellOpMatch) {
      const sessionId = decodeId(shellOpMatch[1])
      const shellId = decodeId(shellOpMatch[2])
      const op = shellOpMatch[3]
      const session = requireSession(sessionId)
      if (op === "read") {
        if (req.method !== "GET") {
          return sendError(res, 405, "invalid_request", "method not allowed")
        }
        const sinceRaw = parsedSearch.get("since") ?? "0"
        if (!/^\d+$/.test(sinceRaw)) {
          return sendError(res, 400, "invalid_arguments", "since must be a non-negative integer byte cursor")
        }
        return sendJson(res, 200, shells.read(sessionId, shellId, Number(sinceRaw)))
      }
      if (req.method !== "POST") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const body = await readJsonBody(req)
      if (op === "write") {
        sessionManager.touch(sessionId, body.lease_epoch) // fence + renew (Task 9)
        const result = shells.write(sessionId, shellId, body.data)
        return sendJson(res, 200, { ok: true, ...result })
      }
      if (op === "close") {
        sessionManager.touch(sessionId, body.lease_epoch) // fence + renew (Task 9)
        const result = await shells.closeShell(sessionId, shellId)
        await watchers.settleFor(session, { command_id: shellId })
        return sendJson(res, 200, { ok: true, result })
      }
      // wait
      const result = await shells.wait(sessionId, shellId, body.timeout_ms)
      await watchers.settleFor(session, { command_id: shellId })
      return sendJson(res, 200, result)
    }

    const shellItemMatch = /^\/v1\/sessions\/([^/]+)\/shell\/([^/]+)$/.exec(pathname)
    if (shellItemMatch) {
      if (req.method !== "GET") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const sessionId = decodeId(shellItemMatch[1])
      requireSession(sessionId)
      return sendJson(res, 200, shells.get(sessionId, decodeId(shellItemMatch[2])))
    }

    // ---- v2 workspace files / artifacts / trace surface (Task 8) ----

    const filesMatch = /^\/v1\/sessions\/([^/]+)\/files$/.exec(pathname)
    if (filesMatch) {
      const sessionId = decodeId(filesMatch[1])
      const session = requireSession(sessionId)
      if (req.method === "POST") {
        // Upload: JSON body cap (default 4MiB) is a protocol constant,
        // configurable; decoded content is bounded by the artifact limit.
        const body = await readJsonBody(req, filesService.jsonBodyLimitBytes)
        sessionManager.touch(sessionId, body.lease_epoch) // fence + renew (Task 9)
        const result = await filesService.upload(session, body)
        await watchers.settleFor(session, { command_id: null })
        return sendJson(res, 200, result)
      }
      if (req.method !== "GET") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const filePath = parsedSearch.get("path")
      if (filePath === null) {
        return sendJson(res, 200, await filesService.list(session, {}))
      }
      return sendJson(res, 200, await filesService.download(session, { path: filePath }))
    }

    const artifactsMatch = /^\/v1\/sessions\/([^/]+)\/artifacts$/.exec(pathname)
    if (artifactsMatch) {
      if (req.method !== "GET") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const sessionId = decodeId(artifactsMatch[1])
      const session = requireSession(sessionId)
      const sinceRaw = parsedSearch.get("since") ?? "0"
      if (!/^\d+$/.test(sinceRaw)) {
        return sendError(res, 400, "invalid_arguments", "since must be a non-negative integer cursor")
      }
      // Settlement scan before pulling: catches missed watch events and makes
      // background command settlements (and failures) observable. A background
      // browser CLI command settles its output files here too.
      if (session.cliSessions?.size > 0) {
        await settleBrowserCliArtifacts(session, {
          artifactLimitBytes: filesService.artifactLimitBytes,
          logger,
        })
      }
      await watchers.settleFor(session)
      const artifacts = Array.isArray(session.artifacts) ? session.artifacts : []
      // Fail closed on undeclared kinds (constitution art. 4.5) — never pass
      // an unknown artifact through silently.
      for (const artifact of artifacts) {
        assertArtifactKind(artifact?.kind)
      }
      const since = Number(sinceRaw)
      // Bounded buffer (Task 9): the cursor is an ABSOLUTE sequence; evicted
      // artifacts advance artifactBase. A client whose cursor fell behind the
      // base sees an explicit gap (counted, never silent) and can recover the
      // evicted evidence from the workspace spill via the files channel.
      const base = session.artifactBase ?? 0
      const total = base + artifacts.length
      const slice = artifacts.slice(Math.max(0, since - base))
      return sendJson(res, 200, {
        artifacts: slice,
        next_cursor: total,
        total,
        base_cursor: base,
        evicted: session.artifactsEvicted ?? 0,
        ...(since < base ? { gap: true, missed: base - since } : {}),
        ...(session.artifactEvictionFailed ? { artifact_eviction_failed: session.artifactEvictionFailed } : {}),
      })
    }

    const traceMatch = /^\/v1\/sessions\/([^/]+)\/trace$/.exec(pathname)
    if (traceMatch) {
      if (req.method !== "GET") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const sessionId = decodeId(traceMatch[1])
      const session = requireSession(sessionId)
      // Trace file ref: the playwright trace zip in the workspace. CLI
      // tracing is wired by Task 15; until then this honestly reports
      // availability instead of pretending a trace exists.
      const candidates = ["trace.zip", ".eztest/trace.zip"]
      for (const relative of candidates) {
        const absolute = path.join(session.workspace, ...relative.split("/"))
        let stat
        try {
          // lstat: a symlinked trace candidate is skipped, never followed
          // outside the workspace (same confinement as the files channel).
          stat = await fsp.lstat(absolute)
        } catch {
          continue
        }
        if (!stat.isFile()) {
          continue
        }
        let content = null
        let sha
        if (stat.size <= filesService.artifactLimitBytes) {
          content = await fsp.readFile(absolute)
          sha = crypto.createHash("sha256").update(content).digest("hex")
        } else {
          sha = await hashFile(absolute)
        }
        let seen = traceSeen.get(sessionId)
        if (!seen) {
          seen = new Map()
          traceSeen.set(sessionId, seen)
        }
        if (seen.get(relative) !== sha) {
          await appendArtifact(
            session,
            settleTraceArtifact({
              session_id: sessionId,
              path: relative,
              sha256: sha,
              size: stat.size,
              content,
            }),
            logger,
          )
          seen.set(relative, sha)
        }
        return sendJson(res, 200, {
          available: true,
          path: relative,
          bytes: stat.size,
          sha256: sha,
          mime: mimeForPath(relative),
        })
      }
      return sendJson(res, 200, { available: false, path: null })
    }

    // ---- browser traffic read (T16, design §4.3 pure recording) ----
    //
    // The passive recorder's network log: {items:[...], recording, degraded}.
    // A read settles the live capture as an immutable network artifact (deduped
    // by content sha). No recorder yet = not recording (distinct from degraded).
    const networkMatch = /^\/v1\/sessions\/([^/]+)\/network$/.exec(pathname)
    if (networkMatch) {
      if (req.method !== "GET") {
        return sendError(res, 405, "invalid_request", "method not allowed")
      }
      const sessionId = decodeId(networkMatch[1])
      const session = requireSession(sessionId)
      const tailRaw = parsedSearch.get("tail")
      let tail
      if (tailRaw !== null) {
        if (!/^\d+$/.test(tailRaw)) {
          return sendError(res, 400, "invalid_arguments", "tail must be a positive integer")
        }
        tail = Number(tailRaw)
      }
      const log = readSessionNetworkLog(session, { tail })
      await settleNetworkCapture(session, log, { logger })
      return sendJson(res, 200, log)
    }

    const sessionMatch = /^\/v1\/sessions\/([^/]+)$/.exec(pathname)
    if (sessionMatch) {
      const sessionId = decodeId(sessionMatch[1])
      if (req.method === "GET") {
        return sendJson(res, 200, sessionManager.get(sessionId))
      }
      if (req.method === "DELETE") {
        // Kill the session's PTY shells and stop its watcher before the
        // session record (and its artifacts) goes away.
        await shells.cancelSession(sessionId)
        await watchers.closeFor(sessionId)
        stopTrafficRecorder(sessionManager.sessions.get(sessionId))
        traceSeen.delete(sessionId)
        const result = await sessionManager.delete(sessionId)
        return sendJson(res, 200, { ok: true, result })
      }
      return sendError(res, 405, "invalid_request", "method not allowed")
    }

    return sendError(res, 404, "invalid_request", "not found")
  }

  return server
}

export function parseAddress(address = "127.0.0.1:13336") {
  const raw = String(address).trim()
  const index = raw.lastIndexOf(":")
  if (index <= 0 || index === raw.length - 1) {
    throw new Error(`invalid EZTEST_EXECUTOR_ADDR: ${address} (expected host:port)`)
  }
  let host = raw.slice(0, index)
  if (host.startsWith("[") && host.endsWith("]")) {
    host = host.slice(1, -1)
  }
  const port = Number(raw.slice(index + 1))
  if (!Number.isInteger(port) || port <= 0 || port > 65535) {
    throw new Error(`invalid EZTEST_EXECUTOR_ADDR port: ${address}`)
  }
  return { host, port }
}

// Command deadline for the v2 exec channel (default 15min, operational knob).
export function parseCommandTimeout(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric > 0) {
    return numeric
  }
  return DEFAULT_COMMAND_TIMEOUT_MS
}

// Inline stdio bound before spill-to-workspace (explicit truncated flag).
export function parseStdioInlineLimit(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric > 0) {
    return numeric
  }
  return DEFAULT_STDIO_INLINE_LIMIT_BYTES
}

// Files-channel JSON request body cap (protocol constant, default 4MiB).
export function parseJsonBodyLimit(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric > 0) {
    return numeric
  }
  return DEFAULT_JSON_BODY_LIMIT_BYTES
}

// Files/artifact content cap (protocol constant, default 200MiB).
export function parseArtifactLimit(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric > 0) {
    return numeric
  }
  return DEFAULT_ARTIFACT_LIMIT_BYTES
}

// Session capacity (default 8, EZTEST_EXECUTOR_MAX_SESSIONS). The v1
// max-contexts fallback is gone with the v1 protocol: that knob now only sizes
// the retained browser-context pool.
export function parseMaxSessions(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric > 0) {
    return numeric
  }
  return DEFAULT_MAX_SESSIONS
}

// Session lease TTL (default 300s, aligns backend executor_context_ttl_seconds).
export function parseLeaseTtl(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric > 0) {
    return numeric
  }
  return DEFAULT_LEASE_TTL_MS
}

// Orphan reclaim tick (default 10s; 0 disables the periodic sweep).
export function parseReclaimInterval(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric >= 0) {
    return numeric
  }
  return DEFAULT_RECLAIM_INTERVAL_MS
}

// Per-session in-memory artifact retention (default 512; 0 = unlimited).
export function parseArtifactBufferLimit(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric >= 0) {
    return numeric
  }
  return DEFAULT_ARTIFACT_BUFFER_LIMIT
}

// Per-shell in-memory output bound (default 1MiB; 0 = unlimited).
export function parseShellBufferLimit(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric >= 0) {
    return numeric
  }
  return DEFAULT_SHELL_BUFFER_LIMIT_BYTES
}

// Graceful-shutdown drain grace (default 30s; 0 = close immediately).
export function parseShutdownGrace(value) {
  const numeric = Number(value)
  if (Number.isInteger(numeric) && numeric >= 0) {
    return numeric
  }
  return 30_000
}

export async function startFromEnv(env = process.env) {
  const token = String(env.EZTEST_EXECUTOR_TOKEN ?? "").trim()
  if (token.length === 0) {
    console.error("[executor] EZTEST_EXECUTOR_TOKEN is required")
    process.exitCode = 1
    return null
  }
  const { host, port } = parseAddress(env.EZTEST_EXECUTOR_ADDR ?? "127.0.0.1:13336")
  // GD-01 (R-C3): the legacy executor-wide host allow-list env var is ignored;
  // there is no allow list. The retained browser-context pool knobs
  // (EZTEST_EXECUTOR_MAX_CONTEXTS / CONTEXT_CREATE_TIMEOUT_MS) retired with the
  // T16 bridge removal; the browser domain is the official playwright-cli.
  const logger = console
  const sessions = makeSessionManager({
    logger,
    commandTimeoutMs: parseCommandTimeout(env.EZTEST_EXECUTOR_COMMAND_TIMEOUT_MS),
    stdioInlineLimitBytes: parseStdioInlineLimit(env.EZTEST_EXECUTOR_STDIO_INLINE_LIMIT_BYTES),
    maxSessions: parseMaxSessions(env.EZTEST_EXECUTOR_MAX_SESSIONS),
    leaseTtlMs: parseLeaseTtl(env.EZTEST_EXECUTOR_LEASE_TTL_MS),
    reclaimIntervalMs: parseReclaimInterval(env.EZTEST_EXECUTOR_RECLAIM_INTERVAL_MS),
    artifactBufferLimit: parseArtifactBufferLimit(env.EZTEST_EXECUTOR_ARTIFACT_BUFFER_LIMIT),
    workspaceRetention: env.EZTEST_EXECUTOR_WORKSPACE_RETENTION === "delete" ? "delete" : "keep",
    stateDir: env.EZTEST_EXECUTOR_STATE_DIR || DEFAULT_STATE_DIR,
  })
  const files = new WorkspaceFiles({
    jsonBodyLimitBytes: parseJsonBodyLimit(env.EZTEST_EXECUTOR_JSON_BODY_LIMIT_BYTES),
    artifactLimitBytes: parseArtifactLimit(env.EZTEST_EXECUTOR_ARTIFACT_LIMIT_BYTES),
  })
  const version = env.EZTEST_EXECUTOR_VERSION ?? PACKAGE_VERSION
  const server = createExecutorServer({
    token,
    sessions,
    files,
    logger,
    version,
    shellBufferLimitBytes: parseShellBufferLimit(env.EZTEST_EXECUTOR_SHELL_BUFFER_LIMIT_BYTES),
  })
  // Startup orphan scan: reclaim sessions left by a prior crash before serving.
  const recovery = await sessions.recoverOrphans()
  if (recovery.reclaimed.length > 0) {
    console.log(`[executor] reclaimed ${recovery.reclaimed.length} crash-orphan session(s) at startup`)
  }
  await new Promise((resolve, reject) => {
    server.once("error", reject)
    server.listen(port, host, resolve)
  })
  console.log(
    `[executor] listening on http://${host}:${port} (max sessions ${sessions.maxSessions})`,
  )

  // Optional push registration/heartbeat. A missing EZTEST_API_URL keeps the
  // pull-only behavior. Capacity/active counts reflect v2 sessions.
  const push = startPushFromEnv({ env, sessions, version, logger })
  if (push) {
    console.log("[executor] push registration enabled (EZTEST_API_URL is set)")
  }

  const shutdownGraceMs = parseShutdownGrace(env.EZTEST_EXECUTOR_SHUTDOWN_GRACE_MS)
  const shutdown = async (signal) => {
    console.log(`[executor] received ${signal}, draining before shutdown`)
    // Graceful drain: reject new sessions, let existing ones finish/close (or
    // have their expired leases reclaimed) until the grace window elapses.
    sessions.drain(true)
    const deadline = Date.now() + shutdownGraceMs
    while (sessions.size > 0 && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 200))
    }
    push?.stop()
    await new Promise((resolve) => server.close(resolve))
    await sessions.close()
    process.exit(0)
  }
  process.once("SIGINT", () => {
    void shutdown("SIGINT")
  })
  process.once("SIGTERM", () => {
    void shutdown("SIGTERM")
  })
  return { server, sessions, push }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  startFromEnv().catch((error) => {
    console.error(`[executor] failed to start: ${error instanceof Error ? error.message : error}`)
    process.exit(1)
  })
}

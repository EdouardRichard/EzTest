/**
 * Browser traffic PURE RECORDING (executor v2, design v1.3.1 §4.3, SC-C3).
 *
 * GD-01: this is recording, NOT interception. No route guard, no allow-list, no
 * request pausing, no response rewriting — traffic flows untouched. The recorder
 * is a PASSIVE observer of the browser's own network telemetry.
 *
 * Mechanism (architecture-constraints.md P1 recording layer: "网络 MITM/CDP
 * 抓取"): the official playwright-cli browser is launched with an extra
 * `--remote-debugging-port` (injected through the CLI config file, coexisting
 * with the CLI's own Playwright pipe). This module attaches to that CDP endpoint
 * as a SECONDARY client, enables the Network domain on every page target, and
 * records requestWillBeSent / responseReceived / loadingFinished / loadingFailed
 * events into a bounded ring. CDP capture sees decrypted HTTPS traffic natively
 * and preserves the resource type (document/xhr/fetch/script/image/...) that the
 * Go discovery capture classifies on — full fidelity, no new dependency, no
 * certificate authority to install. A TLS-terminating forward proxy was the
 * alternative; CDP is the sanctioned equivalent that avoids minting a CA and
 * keeps resource_type.
 *
 * Degradation is EXPLICIT and browser-recording-only (design §4.3 原文口径):
 * if the CDP port cannot be injected or attached, ONLY the browser traffic
 * recording degrades — exec/shell command traffic never went through it. The
 * failure settles a degraded `network` artifact and the network read reports
 * `degraded:true` with a reason; it is never a silent drop (art.7.6).
 */

import crypto from "node:crypto"
import fsp from "node:fs/promises"
import net from "node:net"
import path from "node:path"

import { appendArtifact, assertArtifactKind } from "./recording.mjs"

// Bounded ring (the old bridge kept 500 entries; W1-15). The read tail is capped
// at the protocol maximum the Go discovery capture asks for (200).
export const NETWORK_RING_LIMIT = 500
export const NETWORK_TAIL_MAX = 200
const DEFAULT_CONNECT_TIMEOUT_MS = 8000
// Best-effort inline body preview bound (the full body stays retrievable through
// the CLI's named response-body read path; this is a convenience ref, not a cap
// on what was recorded).
const BODY_PREVIEW_BOUND = 4096

/** Allocates a free TCP port for the browser's extra CDP listener. */
export function allocateCdpPort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.once("error", reject)
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address()
      srv.close(() => resolve(port))
    })
  })
}

function pushRing(ring, item, limit) {
  ring.push(item)
  while (ring.length > limit) {
    ring.shift()
  }
}

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

// CDP Network.ResourceType -> the Playwright resource_type vocabulary the Go
// discovery capture classifies on (document/xhr/fetch/... lower-case).
function normalizeResourceType(type) {
  return String(type ?? "").toLowerCase()
}

function isTextualMime(mime) {
  const m = String(mime ?? "").toLowerCase()
  return (
    m.startsWith("text/") ||
    m.includes("json") ||
    m.includes("xml") ||
    m.includes("javascript") ||
    m.includes("ecmascript") ||
    m.includes("x-www-form-urlencoded")
  )
}

/**
 * Passive CDP network recorder. One recorder per executor session; it attaches
 * to the browser-level CDP endpoint and auto-attaches to every page target so
 * navigations and new tabs are recorded without re-arming.
 */
export class TrafficRecorder {
  constructor({ cdpPort, logger = null, ringLimit = NETWORK_RING_LIMIT, connectTimeoutMs = DEFAULT_CONNECT_TIMEOUT_MS } = {}) {
    this.cdpPort = cdpPort
    this.logger = logger
    this.ringLimit = ringLimit
    this.connectTimeoutMs = connectTimeoutMs
    this.items = []
    this.pending = new Map() // requestId -> { method, url, start, type, sessionId, status, mime }
    this.seen = new Set() // requestId dedup (a page may be attached twice)
    this.sessions = new Set()
    this.ws = null
    this.nextId = 1
    this.calls = new Map()
    this.recording = false
    this.degraded = false
    this.degradationReason = null
    this.stopped = false
  }

  /** Attaches to the CDP endpoint. Throws on failure (the caller degrades). */
  async connect() {
    // The browser launches during `open`; poll for its CDP endpoint so the
    // recorder attaches as soon as the browser is up — ideally BEFORE the
    // initial navigation completes, so the first page load is recorded too
    // (pure recording, no pausing: setAutoAttach observes targets as they appear).
    const deadline = Date.now() + this.connectTimeoutMs
    let version = null
    let lastError = null
    while (Date.now() < deadline) {
      try {
        version = await this._fetchVersion()
        if (version?.webSocketDebuggerUrl) {
          break
        }
        lastError = new Error("CDP /json/version returned no webSocketDebuggerUrl")
      } catch (error) {
        lastError = error
      }
      await new Promise((resolve) => setTimeout(resolve, 150))
    }
    const wsUrl = version?.webSocketDebuggerUrl
    if (!wsUrl) {
      throw lastError ?? new Error("CDP endpoint did not become available")
    }
    await this._openWebSocket(wsUrl)
    // Auto-attach to current and future page targets (flatten carries sessionId
    // on each event). Enable Network on every attached page session.
    await this._send("Target.setAutoAttach", {
      autoAttach: true,
      waitForDebuggerOnStart: false,
      flatten: true,
    })
    await this._send("Target.setDiscoverTargets", { discover: true })
    const targets = await this._send("Target.getTargets", {})
    for (const info of targets?.targetInfos ?? []) {
      if (info.type === "page") {
        await this._send("Target.attachToTarget", { targetId: info.targetId, flatten: true }).catch(() => {})
      }
    }
    this.recording = true
  }

  async _fetchVersion() {
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), this.connectTimeoutMs)
    try {
      const res = await fetch(`http://127.0.0.1:${this.cdpPort}/json/version`, { signal: controller.signal })
      if (!res.ok) {
        throw new Error(`CDP /json/version HTTP ${res.status}`)
      }
      return await res.json()
    } finally {
      clearTimeout(timer)
    }
  }

  _openWebSocket(wsUrl) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        try {
          ws.close()
        } catch {
          // ignore
        }
        reject(new Error("CDP WebSocket open timed out"))
      }, this.connectTimeoutMs)
      let ws
      try {
        ws = new WebSocket(wsUrl)
      } catch (error) {
        clearTimeout(timer)
        reject(error)
        return
      }
      this.ws = ws
      ws.onopen = () => {
        clearTimeout(timer)
        resolve()
      }
      ws.onerror = () => {
        clearTimeout(timer)
        reject(new Error("CDP WebSocket connection error"))
      }
      ws.onmessage = (event) => {
        try {
          this._onMessage(JSON.parse(event.data))
        } catch (error) {
          this.logger?.warn?.(`[executor] traffic recorder message parse failed: ${error?.message ?? error}`)
        }
      }
      ws.onclose = () => {
        // The browser closed (session close / crash). Stop recording; this is
        // not a degradation of a live capture, it is the end of the browser.
        this.recording = false
      }
    })
  }

  _send(method, params = {}, sessionId) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error("CDP WebSocket is not open"))
    }
    const id = this.nextId++
    const promise = new Promise((resolve, reject) => {
      this.calls.set(id, { resolve, reject })
    })
    this.ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
    return promise
  }

  _onMessage(msg) {
    if (msg.id && this.calls.has(msg.id)) {
      const { resolve, reject } = this.calls.get(msg.id)
      this.calls.delete(msg.id)
      if (msg.error) {
        reject(new Error(msg.error.message ?? "CDP error"))
      } else {
        resolve(msg.result)
      }
      return
    }
    if (!msg.method) {
      return
    }
    const sessionId = msg.sessionId
    switch (msg.method) {
      case "Target.attachedToTarget": {
        const sid = msg.params?.sessionId
        if (sid) {
          this.sessions.add(sid)
          this._send("Network.enable", {}, sid).catch(() => {})
        }
        return
      }
      case "Target.detachedFromTarget": {
        if (msg.params?.sessionId) {
          this.sessions.delete(msg.params.sessionId)
        }
        return
      }
      case "Network.requestWillBeSent": {
        const p = msg.params ?? {}
        // A redirect re-fires requestWillBeSent with the same requestId; keep the
        // first start so duration spans the whole exchange.
        if (!this.pending.has(p.requestId)) {
          this.pending.set(p.requestId, {
            method: p.request?.method ?? "GET",
            url: p.request?.url ?? "",
            start: p.timestamp ?? p.wallTime ?? Date.now() / 1000,
            type: p.type,
            sessionId,
            status: null,
            mime: null,
          })
        }
        return
      }
      case "Network.responseReceived": {
        const p = msg.params ?? {}
        const entry = this.pending.get(p.requestId)
        if (entry) {
          entry.status = p.response?.status ?? null
          entry.mime = p.response?.mimeType ?? null
          entry.type = entry.type ?? p.type
          entry.sessionId = entry.sessionId ?? sessionId
        }
        return
      }
      case "Network.loadingFinished": {
        this._finalize(msg.params?.requestId, msg.params?.timestamp, false, null)
        return
      }
      case "Network.loadingFailed": {
        this._finalize(msg.params?.requestId, msg.params?.timestamp, true, msg.params?.errorText ?? "failed")
        return
      }
      default:
        return
    }
  }

  _finalize(requestId, timestamp, failed, errorText) {
    if (!requestId || this.seen.has(requestId)) {
      return
    }
    const entry = this.pending.get(requestId)
    this.pending.delete(requestId)
    if (!entry) {
      return
    }
    this.seen.add(requestId)
    // Bound the dedup set so a long-lived session does not grow it without limit.
    if (this.seen.size > this.ringLimit * 4) {
      this.seen.clear()
    }
    const end = timestamp ?? Date.now() / 1000
    const durationMs = Math.max(0, Math.round((end - entry.start) * 1000))
    const item = {
      method: entry.method,
      url: entry.url,
      status: failed ? null : entry.status,
      duration_ms: durationMs,
      mime: entry.mime,
      resource_type: normalizeResourceType(entry.type),
      ...(failed ? { error: errorText ?? "" } : {}),
    }
    pushRing(this.items, item, this.ringLimit)
    // Best-effort body ref for textual responses (never blocks recording, never
    // degrades on failure — the full body stays readable via the CLI read path).
    if (!failed && entry.sessionId && isTextualMime(entry.mime)) {
      this._captureBody(requestId, entry.sessionId, item)
    }
  }

  _captureBody(requestId, sessionId, item) {
    this._send("Network.getResponseBody", { requestId }, sessionId)
      .then((result) => {
        if (!result) {
          return
        }
        const text = result.base64Encoded ? Buffer.from(result.body ?? "", "base64").toString("utf8") : result.body ?? ""
        const bytes = Buffer.from(text, "utf8")
        item.body_sha256 = sha256(bytes)
        item.body_bytes = bytes.length
        item.body_preview = text.length > BODY_PREVIEW_BOUND ? text.slice(0, BODY_PREVIEW_BOUND) : text
        item.body_truncated = text.length > BODY_PREVIEW_BOUND
      })
      .catch(() => {
        // Body unavailable (evicted from the browser cache, non-textual, gone):
        // the request record stands without a body ref. Not a degradation.
      })
  }

  /**
   * The recorded network log. `tail` is clamped to the protocol maximum. The
   * shape mirrors the retired bridge's network action ({items:[...]}) plus
   * explicit recording/degradation flags so a reader never mistakes "no
   * recording" for "no traffic".
   */
  getNetworkLog({ tail = 50 } = {}) {
    let n = Number(tail)
    if (!Number.isInteger(n) || n <= 0) {
      n = 50
    }
    n = Math.min(n, NETWORK_TAIL_MAX)
    return {
      items: this.items.slice(-n),
      recording: this.recording,
      degraded: this.degraded,
      ...(this.degraded && this.degradationReason ? { reason: this.degradationReason } : {}),
    }
  }

  /** Marks the recorder degraded with an explicit reason (browser recording only). */
  degrade(reason) {
    this.degraded = true
    this.degradationReason = reason
    this.recording = false
  }

  stop() {
    this.stopped = true
    this.recording = false
    for (const { reject } of this.calls.values()) {
      reject(new Error("traffic recorder stopped"))
    }
    this.calls.clear()
    try {
      this.ws?.close()
    } catch {
      // ignore
    }
    this.ws = null
  }
}

/**
 * Settles a network capture (or an explicit degradation record) as an immutable
 * `network` artifact. Content-addressed; the payload is the verbatim network log
 * ({items:[...]}) or a degradation marker ({degraded:true, reason, items:[]}).
 */
export async function settleNetworkArtifact(session, payload, { logger = null, command_id = null } = {}) {
  assertArtifactKind("network")
  const data = Buffer.from(JSON.stringify(payload), "utf8")
  const artifact = {
    kind: "network",
    session_id: session.id,
    path: ".eztest/network.json",
    sha256: sha256(data),
    size: data.length,
    mime: "application/json",
    command_id: command_id ?? null,
    filename: "network.json",
    base64: data.toString("base64"),
  }
  if (Array.isArray(session.artifacts)) {
    await appendArtifact(session, artifact, logger)
  }
  return artifact
}

/**
 * Settles the live network capture as an immutable artifact, deduped by content
 * sha so repeated reads of an unchanged log do not multiply evidence. Called at
 * read time (the artifacts endpoint's read-time settlement precedent). Only
 * live captures settle here; a degradation record is settled once at attach
 * failure by startTrafficRecorder.
 */
export async function settleNetworkCapture(session, log, { logger = null, command_id = null } = {}) {
  if (!log || log.degraded || !Array.isArray(log.items) || log.items.length === 0) {
    return null
  }
  if (!(session.trafficLedger instanceof Set)) {
    session.trafficLedger = new Set()
  }
  const payload = { items: log.items }
  const data = Buffer.from(JSON.stringify(payload), "utf8")
  const sha = sha256(data)
  if (session.trafficLedger.has(sha)) {
    return null
  }
  session.trafficLedger.add(sha)
  return settleNetworkArtifact(session, payload, { logger, command_id })
}

/**
 * Starts the session's traffic recorder against its injected CDP port. On
 * attach failure it degrades EXPLICITLY: a degraded recorder (the network read
 * reports degraded:true + reason) AND a persisted degraded network artifact, so
 * the failure is observable evidence, never a silent drop (art.7.6). Returns the
 * recorder (degraded or live) and stores it on the session.
 */
export async function startTrafficRecorder(session, { cdpPort, logger = null, connectTimeoutMs = DEFAULT_CONNECT_TIMEOUT_MS, degradationReason = null } = {}) {
  // A re-open replaces any prior recorder (one browser per session at a time).
  stopTrafficRecorder(session)
  const recorder = new TrafficRecorder({ cdpPort, logger, connectTimeoutMs })
  session.trafficRecorder = recorder
  // An injection failure already decided the degradation: do not attempt a
  // doomed attach, record the explicit reason directly (browser recording only).
  if (degradationReason) {
    recorder.degrade(degradationReason)
    logger?.warn?.(`[executor] browser traffic recording degraded session=${session.id}: ${degradationReason}`)
    await settleNetworkArtifact(session, { degraded: true, reason: degradationReason, items: [] }, { logger }).catch(() => {})
    return recorder
  }
  try {
    await recorder.connect()
    logger?.info?.(`[executor] traffic recording attached session=${session.id} cdp_port=${cdpPort}`)
  } catch (error) {
    const reason = `cdp_attach_failed: ${error?.message ?? error}`
    recorder.degrade(reason)
    logger?.warn?.(`[executor] browser traffic recording degraded session=${session.id}: ${reason}`)
    await settleNetworkArtifact(session, { degraded: true, reason, items: [] }, { logger }).catch(() => {})
  }
  return recorder
}

/** Stops and clears the session's traffic recorder (session close/reclaim). */
export function stopTrafficRecorder(session) {
  const recorder = session?.trafficRecorder
  if (recorder) {
    recorder.stop()
    session.trafficRecorder = null
  }
}

/**
 * Injects the traffic-recording CDP port into a playwright-cli `open` invocation.
 * Writes a CLI config file (browser.launchOptions.args carries the extra
 * --remote-debugging-port, coexisting with the CLI's Playwright pipe) under the
 * session's .eztest dir (excluded from the workspace watcher and the CLI output
 * ledger, so it is never double-settled), and adds `--config <path>` to the argv.
 * Stores the port on the session for the post-open recorder attach.
 *
 * Non-open argv is returned unchanged. A failure to allocate/write degrades
 * explicitly later (the recorder attach fails); injection itself never throws
 * into the command path — the browser still opens, recording degrades.
 */
export async function injectTrafficRecording(session, params, { logger = null } = {}) {
  try {
    const cdpPort = await allocateCdpPort()
    const dir = path.join(session.workspace, ".eztest")
    await fsp.mkdir(dir, { recursive: true })
    const configPath = path.join(dir, "traffic-cdp.json")
    const config = {
      browser: {
        launchOptions: {
          args: [`--remote-debugging-port=${cdpPort}`],
        },
      },
    }
    await fsp.writeFile(configPath, JSON.stringify(config), "utf8")
    session.trafficCdpPort = cdpPort
    const argv = [...params.argv, "--config", configPath]
    return { ...params, argv }
  } catch (error) {
    // Injection failed: leave the argv unchanged so the browser still opens;
    // mark the session so the post-open step records an explicit degradation.
    session.trafficInjectionError = `cdp_inject_failed: ${error?.message ?? error}`
    logger?.warn?.(`[executor] traffic recording injection failed session=${session.id}: ${session.trafficInjectionError}`)
    return params
  }
}

/** Reads the session's network log for the read endpoint (no recorder = not recording). */
export function readSessionNetworkLog(session, { tail } = {}) {
  const recorder = session?.trafficRecorder
  if (!recorder) {
    return { items: [], recording: false, degraded: false }
  }
  return recorder.getNetworkLog({ tail })
}
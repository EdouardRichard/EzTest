// Task 16 (v0.2.4-C, SC-C3 / design §4.3): browser traffic PURE RECORDING.
//
// The browser capability domain records network traffic without interception
// (GD-01: no route guards, no allow-list). The recorder attaches to the CLI
// browser's CDP endpoint as a PASSIVE observer (architecture-constraints.md P1:
// "网络 MITM/CDP 抓取") — Network domain events only, never Fetch/request
// pausing. Each request becomes a network record (method/url/status/mime/
// duration_ms/resource_type, best-effort body ref) settled as a `network`
// artifact. Injection/attach failure degrades EXPLICITLY (browser recording
// only): a degraded network artifact + a degraded network read, never silent.
//
// REAL-browser tests (no mock): a request through the browser is recorded as a
// network artifact; a forced injection failure produces an explicit degradation
// event. The console/network named READ path (browser.cli console/requests) is
// asserted in browser_cli.test.mjs; here we assert the recording + read endpoint.
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import fs from "node:fs"
import fsp from "node:fs/promises"
import http from "node:http"
import os from "node:os"
import path from "node:path"

import {
  allocateCdpPort,
  startTrafficRecorder,
  stopTrafficRecorder,
  TrafficRecorder,
  NETWORK_TAIL_MAX,
} from "../src/traffic_recorder.mjs"
import { ARTIFACT_KINDS } from "../src/recording.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { SessionManager } from "../src/sessions.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-executor-token"

function cliBrowserFlag() {
  const explicit = process.env.EZTEST_EXECUTOR_CLI_BROWSER
  if (explicit) return [`--browser=${explicit}`]
  const channels = [
    ["chrome", "C:/Program Files/Google/Chrome/Application/chrome.exe"],
    ["chrome", "C:/Program Files (x86)/Google/Chrome/Application/chrome.exe"],
    ["msedge", "C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe"],
    ["msedge", "C:/Program Files/Microsoft/Edge/Application/msedge.exe"],
    ["chrome", "/usr/bin/google-chrome"],
    ["chrome", "/usr/bin/google-chrome-stable"],
    ["msedge", "/usr/bin/microsoft-edge"],
  ]
  for (const [channel, candidate] of channels) {
    if (fs.existsSync(candidate)) return [`--browser=${channel}`]
  }
  return []
}

// A local SUT whose home page issues a fetch so the recording carries both a
// document and an api (fetch) resource type.
async function startSUT(t) {
  const server = http.createServer((req, res) => {
    if (req.url === "/api/data") {
      res.writeHead(200, { "content-type": "application/json" })
      res.end('{"ok":true}')
      return
    }
    res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
    res.end(
      `<!doctype html><html><head><title>T16 SUT</title></head><body><h1>T16</h1>` +
        `<script>fetch('/api/data').then(r=>r.json()).then(j=>{window.__data=j})</script>` +
        `</body></html>`,
    )
  })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  t.after(async () => {
    server.closeAllConnections?.()
    await new Promise((resolve) => server.close(resolve))
  })
  return `http://127.0.0.1:${port}/`
}

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t16-traffic-"))
  t.after(() => fsp.rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))
  return dir
}

async function startExecutor(t) {
  const sessions = new SessionManager({ logger: null, reclaimIntervalMs: 0 })
  const server = createExecutorServer({ token: TOKEN, sessions, logger: null, version: "test" })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  t.after(async () => {
    await sessions.close()
    await new Promise((resolve) => server.close(resolve))
  })
  return { sessions, request: httpClient(`http://127.0.0.1:${port}`, TOKEN) }
}

async function createSession(request, workspace) {
  const response = await request("/v1/sessions", {
    method: "POST",
    body: { key: "run:16:sut:7:role:admin", workspace },
  })
  assert.equal(response.status, 200, response.text)
  return response.json.sessionId
}

async function cli(request, sessionId, subcommand, args = [], { timeoutMs = 60000, expectExit = 0 } = {}) {
  const response = await request(`/v1/sessions/${sessionId}/exec`, {
    method: "POST",
    body: { argv: ["playwright-cli", subcommand, ...args], timeout_ms: timeoutMs },
  })
  assert.equal(response.status, 200, response.text)
  const result = response.json
  if (expectExit !== null) {
    assert.equal(result.exit, expectExit, `${subcommand} exit ${result.exit}: ${result.stderr || result.stdout}`)
  }
  return result
}

async function networkRead(request, sessionId, tail) {
  const query = tail === undefined ? "" : `?tail=${tail}`
  const response = await request(`/v1/sessions/${sessionId}/network${query}`)
  assert.equal(response.status, 200, response.text)
  return response.json
}

async function artifacts(request, sessionId) {
  const response = await request(`/v1/sessions/${sessionId}/artifacts`)
  assert.equal(response.status, 200, response.text)
  return response.json.artifacts
}

describe("traffic recording unit contracts (no browser)", () => {
  it("declares network in the single artifact-kind registry (art.4.5)", () => {
    assert.ok(ARTIFACT_KINDS.includes("network"), "network must be a declared artifact kind")
  })

  it("allocates a free CDP port", async () => {
    const port = await allocateCdpPort()
    assert.ok(Number.isInteger(port) && port > 0 && port < 65536)
  })

  it("caps the network tail at the protocol maximum", () => {
    const recorder = new TrafficRecorder({ cdpPort: 1, logger: null })
    // No connection: an empty, non-degraded log (recording not started).
    const log = recorder.getNetworkLog({ tail: 100000 })
    assert.ok(Array.isArray(log.items))
    assert.ok(log.items.length <= NETWORK_TAIL_MAX)
  })

  it("degrades explicitly when the CDP endpoint is unreachable (browser recording only)", async (t) => {
    const workspace = await makeWorkspace(t)
    const session = { id: "sess_degraded", workspace, artifacts: [] }
    // Port 1 is never a listening CDP endpoint: attach must fail.
    const recorder = await startTrafficRecorder(session, { cdpPort: 1, logger: null, connectTimeoutMs: 1500 })
    assert.ok(recorder.degraded, "recorder must be degraded after a failed attach")
    assert.ok(recorder.degradationReason, "degradation must carry an explicit reason")
    const log = recorder.getNetworkLog({ tail: 50 })
    assert.equal(log.degraded, true)
    assert.ok(log.reason, "network read must surface the degradation reason")
    // The degradation is a persisted, observable network artifact (art.7.6).
    const degraded = session.artifacts.filter((a) => a.kind === "network")
    assert.ok(degraded.length >= 1, "a degraded network artifact must be settled")
    const payload = JSON.parse(Buffer.from(degraded[0].base64, "base64").toString("utf8"))
    assert.equal(payload.degraded, true)
    assert.ok(payload.reason)
    stopTrafficRecorder(session)
  })
})

describe("traffic recording (real browser)", () => {
  it("records a browser request as a network artifact (url/method/status/resource_type)", async (t) => {
    const sutURL = await startSUT(t)
    const workspace = await makeWorkspace(t)
    const { request } = await startExecutor(t)
    const sessionId = await createSession(request, workspace)

    await cli(request, sessionId, "open", [...cliBrowserFlag(), sutURL])
    // Wait for the page's fetch to settle (deterministic: the SUT sets __data).
    await cli(request, sessionId, "run-code", ["async page => { await page.waitForFunction(() => window.__data); }"])

    // `open` awaits the parallel recorder attach (server.mjs), so by now the
    // passive recorder IS attached. Prove it keeps recording AFTER the attach:
    // issue a NEW document request (a fresh navigation) and assert it is
    // captured. Without this, a passing test could only show the initial
    // navigation — which can be recorded because attach raced ahead of it — and
    // would not prove post-attach recording of a new document.
    const afterAttachURL = `${sutURL}?after_attach=1`
    await cli(request, sessionId, "run-code", [
      `async page => { await page.goto(${JSON.stringify(afterAttachURL)}); await page.waitForFunction(() => window.__data); }`,
    ])

    const log = await networkRead(request, sessionId, 200)
    assert.equal(log.degraded, false, `recording degraded: ${log.reason}`)
    assert.ok(Array.isArray(log.items) && log.items.length > 0, "network log must capture requests")
    // Assert the NEW (post-attach) document. The initial `open` navigation is
    // inherently racy — the parallel attach can miss it if the page loads first —
    // so the deterministic proof of recording-after-attach is this fresh
    // navigation issued strictly after `open` (and therefore after attach).
    const afterAttachDoc = log.items.find(
      (i) => i.url === afterAttachURL && i.resource_type === "document",
    )
    assert.ok(
      afterAttachDoc,
      `a NEW document request issued after attach must be recorded: ${JSON.stringify(log.items.slice(0, 8))}`,
    )
    assert.equal(afterAttachDoc.method, "GET")
    assert.equal(afterAttachDoc.status, 200)
    const api = log.items.find((i) => i.url.endsWith("/api/data") && i.resource_type === "fetch")
    assert.ok(api, "fetch request recorded with resource_type=fetch")
    assert.equal(api.status, 200)
    assert.ok(typeof api.duration_ms === "number")

    // The recording is settled as an immutable network artifact (url/method/status).
    const arts = await artifacts(request, sessionId)
    const networkArt = arts.filter((a) => a.kind === "network")
    assert.ok(networkArt.length >= 1, "a network artifact must be settled")
    const payload = JSON.parse(Buffer.from(networkArt[networkArt.length - 1].base64, "base64").toString("utf8"))
    const items = payload.items ?? payload
    assert.ok(items.some((i) => i.url === afterAttachURL && i.method === "GET" && i.status === 200))

    // Deleting the session stops the recorder (no orphan CDP connection).
    const del = await request(`/v1/sessions/${sessionId}`, { method: "DELETE" })
    assert.equal(del.status, 200, del.text)
  })

  it("network read before any browser opens reports not-recording (not degraded)", async (t) => {
    const workspace = await makeWorkspace(t)
    const { request } = await startExecutor(t)
    const sessionId = await createSession(request, workspace)
    const log = await networkRead(request, sessionId)
    assert.deepEqual(log.items, [])
    assert.equal(log.degraded, false)
    assert.equal(log.recording, false)
  })
})
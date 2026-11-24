// Task 15 (v0.2.4-C, P10/Q3=B): the browser capability domain runs the OFFICIAL
// @playwright/cli through the general exec channel. These are REAL-browser tests
// (no mock): passthrough + snapshot refs + --raw, the five AC-010 named
// capabilities (multi-tab/popup, iframe, dblclick, file-dialog/upload, browser-
// state wait), and the snapshot/screenshot evidence-kind wiring (Q06).
//
// Per cli-command-face.md (T1): iframe has no named command (cross-frame snapshot
// refs + run-code contentFrame); browser-state wait has no named command nor a
// goto wait_until flag (run-code waitForLoadState/waitFor). Those two AC-010 items
// are asserted via their real escape-valve paths, NOT a "full passthrough" fudge.
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import fs from "node:fs"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import {
  isBrowserCliArgv,
  browserCliSubcommand,
  kindForCliOutput,
  resolvePlaywrightCli,
  cliVersionInfo,
  prepareBrowserCliInvocation,
  resetPlaywrightCliResolution,
  __forceCliResolutionForTest,
  runCliClose,
} from "../src/browser_cli.mjs"
import { ARTIFACT_KINDS } from "../src/recording.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { SessionManager } from "../src/sessions.mjs"
import { httpClient, startLocalPage } from "./helpers.mjs"

const TOKEN = "test-executor-token"

// The CLI's pinned chromium may be absent on a dev machine; fall back to a system
// Chrome/Edge channel (Playwright channels). CI installs the CLI chromium, so the
// default (no flag) is used there. EZTEST_EXECUTOR_CLI_BROWSER overrides.
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
    ["chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"],
  ]
  for (const [channel, candidate] of channels) {
    if (fs.existsSync(candidate)) return [`--browser=${channel}`]
  }
  return []
}

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t15-cli-"))
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
    body: { key: "run:15:sut:7:role:admin", workspace },
  })
  assert.equal(response.status, 200, response.text)
  return response.json.sessionId
}

// Runs one playwright-cli subcommand through the exec channel (the browser.cli
// tool's wire shape: argv = ["playwright-cli", subcommand, ...args]). The executor
// resolves the CLI entry and binds -s to the session.
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

async function artifacts(request, sessionId) {
  const response = await request(`/v1/sessions/${sessionId}/artifacts`)
  assert.equal(response.status, 200, response.text)
  return response.json.artifacts
}

describe("browser.cli unit contracts (no browser)", () => {
  it("detects playwright-cli argv and extracts the subcommand", () => {
    assert.equal(isBrowserCliArgv(["playwright-cli", "goto", "http://x"]), true)
    assert.equal(isBrowserCliArgv(["playwright-cli.cmd", "snapshot"]), true)
    assert.equal(isBrowserCliArgv([process.execPath, "/p/playwright-cli.js", "snapshot"]), true)
    assert.equal(isBrowserCliArgv(["node", "script.js"]), false)
    assert.equal(isBrowserCliArgv([]), false)
    assert.equal(browserCliSubcommand(["playwright-cli", "-s=sess_1", "tab-list"]), "tab-list")
    assert.equal(browserCliSubcommand(["playwright-cli", "snapshot", "--raw"]), "snapshot")
  })

  it("maps CLI output files to evidence kinds (snapshot is the Q06 producer)", () => {
    assert.equal(kindForCliOutput(".playwright-cli/page-1.yml", null), "snapshot")
    assert.equal(kindForCliOutput(".playwright-cli/page-1.png", null), "screenshot")
    assert.equal(kindForCliOutput(".playwright-cli/traces/trace-1.trace", null), "trace")
    assert.equal(kindForCliOutput(".playwright-cli/console-1.log", null), "console")
    assert.equal(kindForCliOutput("state.json", "state-save"), "file")
    assert.equal(kindForCliOutput("shot.png", "screenshot"), "screenshot")
    // Every mapped kind is declared in the single artifact registry (art.4.5).
    for (const kind of ["snapshot", "screenshot", "trace", "console", "file"]) {
      assert.ok(ARTIFACT_KINDS.includes(kind), `undeclared kind ${kind}`)
    }
  })

  it("reports the floating CLI version AND its pinned engine (art.4.6 fingerprint)", () => {
    const resolved = resolvePlaywrightCli()
    if (!resolved.argvPrefix) {
      // CLI not installed in this environment; the runtime error path is covered
      // by the server test below. Version info is honestly null.
      assert.equal(cliVersionInfo(), null)
      return
    }
    const info = cliVersionInfo()
    assert.ok(info, "version info must be readable when the CLI is installed")
    assert.match(info.cli_version, /^\d+\.\d+\.\d+/)
    assert.ok(info.engine_version, "the pinned engine version must be captured")
  })
})

describe("browser.cli real browser: passthrough + AC-010 five named capabilities", () => {
  it("goto/snapshot refs/click/fill/--raw passthrough and the five AC-010 items", async (t) => {
    const page = await startLocalPage()
    t.after(() => page.close())
    const { request } = await startExecutor(t)
    const workspace = await makeWorkspace(t)
    const sessionId = await createSession(request, workspace)
    const browserFlag = cliBrowserFlag()

    // --- passthrough: open + goto + snapshot with refs ---
    await cli(request, sessionId, "open", [...browserFlag, `${page.url}/cli-home`])
    const snap = await cli(request, sessionId, "snapshot")
    assert.match(snap.stdout, /\[ref=e\d+\]/, "snapshot must assign element refs")
    assert.match(snap.stdout, /CLI Home/, "snapshot must reflect the page")

    // --raw output is the bare ARIA value (jq/diff-able, no status/code header).
    const raw = await cli(request, sessionId, "snapshot", ["--raw"])
    assert.match(raw.stdout, /\[ref=e\d+\]/)
    assert.ok(!raw.stdout.includes("### Ran Playwright code"), "--raw strips the generated-code header")

    // click/fill via a CSS selector target pass through verbatim.
    await cli(request, sessionId, "click", ["#dbl"])
    await cli(request, sessionId, "fill", ["#text", "hello-cli"])
    const filled = await cli(request, sessionId, "eval", ["() => document.getElementById('text').value"])
    assert.match(filled.stdout, /hello-cli/, "fill must set the input value")

    // --- AC-010 #1: multi-tab / popup (tab-* commands) ---
    await cli(request, sessionId, "click", ["#popup"])
    const tabs = await cli(request, sessionId, "tab-list")
    assert.match(tabs.stdout, /cli-popup/, "popup must appear as a second tab")
    const tabLines = tabs.stdout.split("\n").filter((l) => /^\s*- \d+:/.test(l))
    assert.ok(tabLines.length >= 2, `tab-list must show >=2 tabs, got: ${tabs.stdout}`)
    await cli(request, sessionId, "tab-select", ["1"])
    const popupSnap = await cli(request, sessionId, "snapshot")
    assert.match(popupSnap.stdout, /cli-popup/, "tab-select 1 must switch to the popup URL")
    await cli(request, sessionId, "tab-select", ["0"])

    // --- AC-010 #2: iframe (cross-frame snapshot ref AND run-code contentFrame) ---
    const frameSnap = await cli(request, sessionId, "snapshot", ["--raw"])
    const frameRef = /button "Inner button" \[ref=(f\de\d+)\]/.exec(frameSnap.stdout)
    assert.ok(frameRef, `snapshot must assign a cross-frame ref to the iframe button: ${frameSnap.stdout}`)
    // Direct click on the cross-frame ref (no named iframe command — refs span frames).
    await cli(request, sessionId, "click", [frameRef[1]])
    const innerViaRef = await cli(request, sessionId, "run-code", [
      "async page => { const f = page.locator('#frame').contentFrame(); return await f.locator('#inner-out').textContent(); }",
    ])
    assert.match(innerViaRef.stdout, /inner-clicked/, "clicking the cross-frame ref must act inside the iframe")
    // run-code contentFrame escape valve (the second confirmed iframe path).
    const innerViaCode = await cli(request, sessionId, "run-code", [
      "async page => { const f = page.locator('#frame').contentFrame(); await f.locator('#inner').click(); return await f.locator('#inner-out').textContent(); }",
    ])
    assert.match(innerViaCode.stdout, /inner-clicked/)

    // --- AC-010 #3: double-click (named dblclick command) ---
    await cli(request, sessionId, "dblclick", ["#dbl"])
    const dbl = await cli(request, sessionId, "eval", ["() => document.getElementById('dbl-out').textContent"])
    assert.match(dbl.stdout, /double-clicked/, "dblclick must fire the dblclick handler")

    // --- AC-010 #4: file dialog / upload (click opens chooser, upload sets files) ---
    const uploadFile = path.join(workspace, "upload-me.txt")
    await fsp.writeFile(uploadFile, "upload-content")
    await cli(request, sessionId, "click", ["#file"])
    await cli(request, sessionId, "upload", [uploadFile])
    const uploaded = await cli(request, sessionId, "eval", ["() => document.getElementById('file-out').textContent"])
    assert.match(uploaded.stdout, /uploaded:upload-me\.txt/, "upload must fill the file input via the chooser")

    // --- AC-010 #5: browser-state wait (run-code escape valve; no named wait cmd) ---
    const loadState = await cli(request, sessionId, "run-code", [
      "async page => { await page.waitForLoadState('networkidle'); return 'networkidle-ok'; }",
    ])
    assert.match(loadState.stdout, /networkidle-ok/, "waitForLoadState must resolve")
    const waitFor = await cli(request, sessionId, "run-code", [
      "async page => { await page.locator('#late').waitFor({ state: 'visible', timeout: 5000 }); return 'late-visible'; }",
    ])
    assert.match(waitFor.stdout, /late-visible/, "locator.waitFor must observe the late element")

    // --- named convenience surfaces: route mock, console, requests, resize ---
    await cli(request, sessionId, "route", ["**/api/mock", "--body={\"ok\":true}", "--content-type=application/json"])
    const routes = await cli(request, sessionId, "route-list")
    assert.match(routes.stdout, /api\/mock/)
    const consoleOut = await cli(request, sessionId, "console")
    assert.equal(consoleOut.exit, 0)
    const requestsOut = await cli(request, sessionId, "requests")
    assert.equal(requestsOut.exit, 0)
    await cli(request, sessionId, "resize", ["800", "600"])

    // --- state-save / state-load roundtrip ---
    await cli(request, sessionId, "state-save", ["state.json"])
    const statePath = path.join(workspace, "state.json")
    assert.ok(fs.existsSync(statePath), "state-save must write the storage state file")
    const load = await cli(request, sessionId, "state-load", ["state.json"])
    assert.match(load.stdout, /restored/i)

    // --- tracing produces a trace file ---
    await cli(request, sessionId, "tracing-start")
    await cli(request, sessionId, "tracing-stop")
    const traceDir = path.join(workspace, ".playwright-cli", "traces")
    const traceFiles = fs.existsSync(traceDir) ? await fsp.readdir(traceDir) : []
    assert.ok(traceFiles.some((f) => f.endsWith(".trace")), `tracing-stop must write a .trace file: ${traceFiles}`)
  })
})

describe("browser.cli evidence-kind wiring (Q06 snapshot producer)", () => {
  it("settles snapshot and screenshot artifacts from the CLI output files", async (t) => {
    const page = await startLocalPage()
    t.after(() => page.close())
    const { request } = await startExecutor(t)
    const workspace = await makeWorkspace(t)
    const sessionId = await createSession(request, workspace)

    await cli(request, sessionId, "open", [...cliBrowserFlag(), `${page.url}/cli-home`])
    await cli(request, sessionId, "snapshot")
    await cli(request, sessionId, "screenshot")

    const settled = await artifacts(request, sessionId)
    const snapshot = settled.find((a) => a.kind === "snapshot")
    assert.ok(snapshot, `a snapshot artifact must settle (Q06 producer): kinds=${settled.map((a) => a.kind)}`)
    assert.ok(snapshot.base64, "snapshot artifact carries settle-ready content")
    const aria = Buffer.from(snapshot.base64, "base64").toString("utf8")
    assert.match(aria, /\[ref=e\d+\]/, "snapshot evidence must hold the ARIA refs")
    assert.match(snapshot.path, /\.playwright-cli\/page-.*\.yml/)

    const screenshot = settled.find((a) => a.kind === "screenshot")
    assert.ok(screenshot, `a screenshot artifact must settle: kinds=${settled.map((a) => a.kind)}`)
    assert.match(screenshot.path, /\.png$/)
    assert.ok(screenshot.sha256, "screenshot is content-addressed")

    // The command itself is recorded as command evidence (general exec channel).
    assert.ok(settled.some((a) => a.kind === "command"), "the CLI command is command evidence")
    // The CLI output dir is owned by the browser ledger, not double-ledgered as file.
    const cliFilesAsFile = settled.filter(
      (a) => a.kind === "file" && a.path.startsWith(".playwright-cli/"),
    )
    assert.equal(cliFilesAsFile.length, 0, "CLI output files must not also settle as generic file artifacts")
  })
})

describe("browser.cli session alignment + lifecycle", () => {
  it("isolates CLI sessions per executor session and closes them on delete", async (t) => {
    const page = await startLocalPage()
    t.after(() => page.close())
    const { sessions, request } = await startExecutor(t)
    const wsA = await makeWorkspace(t)
    const wsB = await makeWorkspace(t)
    const a = await createSession(request, wsA)
    const b = await createSession(request, wsB)
    const browserFlag = cliBrowserFlag()

    // Two executor sessions -> two isolated CLI sessions (separate storage).
    await cli(request, a, "open", [...browserFlag, `${page.url}/cli-home`])
    await cli(request, b, "open", [...browserFlag, `${page.url}/cli-home`])
    await cli(request, a, "run-code", ["async page => { await page.evaluate(() => localStorage.setItem('who', 'A')); return 'set'; }"])
    const readB = await cli(request, b, "run-code", ["async page => await page.evaluate(() => localStorage.getItem('who'))"])
    assert.ok(!readB.stdout.includes('"A"'), `session B must not see session A storage: ${readB.stdout}`)

    // The CLI session is bound to the executor session id (1:1 alignment).
    const sessionA = sessions.sessions.get(a)
    assert.ok(sessionA.cliSessions.has(a), "CLI session name must equal the executor session id")

    // Deleting the executor session closes its CLI session (no orphan daemon) and
    // removes the record. Hold the reference: onClose clears cliSessions before
    // the record leaves the map.
    const del = await request(`/v1/sessions/${a}`, { method: "DELETE" })
    assert.equal(del.status, 200, del.text)
    assert.equal(sessionA.cliSessions.size, 0, "CLI sessions cleared on delete")
    assert.equal(sessions.sessions.get(a), undefined, "session record removed on delete")
  })

  it("prepareBrowserCliInvocation binds -s to the session and resolves the entry", () => {
    const session = { id: "sess_align", workspace: "/ws", cliSessions: new Set(), cliOutputs: [] }
    const prepared = prepareBrowserCliInvocation(session, {
      argv: ["playwright-cli", "snapshot", "--raw"],
    })
    // The program prefix is resolved; -s=<session.id> is injected; args verbatim.
    assert.ok(prepared.argv.includes("-s=sess_align"), prepared.argv.join(" "))
    assert.ok(prepared.argv.includes("snapshot"))
    assert.ok(prepared.argv.includes("--raw"))
    assert.ok(session.cliSessions.has("sess_align"))
    // A non-CLI argv is returned unchanged.
    const plain = { argv: ["node", "script.js"] }
    assert.equal(prepareBrowserCliInvocation(session, plain), plain)
    // An explicit caller session is respected (not overwritten) and tracked.
    const named = prepareBrowserCliInvocation(session, { argv: ["playwright-cli", "-s=custom", "snapshot"] })
    assert.ok(named.argv.includes("-s=custom"))
    assert.ok(!named.argv.includes("-s=sess_align"))
    assert.ok(session.cliSessions.has("custom"))
    resetPlaywrightCliResolution()
  })

  it("throws typed browser_cli_unavailable when the CLI runtime is missing", () => {
    __forceCliResolutionForTest({ argvPrefix: null, packageDir: null })
    try {
      const session = { id: "sess_x", workspace: "/ws", cliSessions: new Set(), cliOutputs: [] }
      assert.throws(
        () => prepareBrowserCliInvocation(session, { argv: ["playwright-cli", "snapshot"] }),
        (error) => error.code === "browser_cli_unavailable",
      )
    } finally {
      resetPlaywrightCliResolution()
    }
  })
})

// Review fix round 1: the close-timeout kill must be platform-safe. The old
// Windows-only `spawn("taskkill", ...)` with no 'error' listener crashed the
// whole executor on POSIX (ENOENT -> uncaught exception) and never killed the
// wedged daemon there. This test forces the kill binary to be unresolvable
// (PATH stripped) and drives a real wedged child through the timeout path: the
// process must NOT crash, the close must reject gracefully, and the child must
// actually die (fallback kill). Runs on both platforms.
describe("browser.cli close-timeout kill (platform-safe)", () => {
  it("kills a wedged close child even when the kill binary is missing (no crash)", async (t) => {
    const workspace = await makeWorkspace(t)
    const pidFile = path.join(workspace, "wedged.pid")
    // A "CLI" that never exits: writes its pid, then loops forever. A script
    // FILE (not -e) so the appended `-s=<name> close` args land in process.argv
    // instead of being parsed as node options.
    const scriptPath = path.join(workspace, "wedged-cli.cjs")
    await fsp.writeFile(
      scriptPath,
      `require('fs').writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));\n` +
        `setInterval(() => {}, 1000);\n`,
    )
    // Force the Windows tree-kill binary (taskkill) to be unresolvable. POSIX
    // uses process.kill (no binary), so this is a no-op there — the timeout
    // path must still kill the child on both platforms.
    const prevPath = process.env.PATH
    process.env.PATH = path.join(workspace, "no-such-bin")
    t.after(() => {
      process.env.PATH = prevPath
    })

    const closed = runCliClose([process.execPath, scriptPath], "wedged", workspace, {
      timeoutMs: 500,
    })
    // Wait for the wedged child to report its pid.
    const deadline = Date.now() + 10000
    let pid = null
    while (pid === null) {
      try {
        pid = Number(await fsp.readFile(pidFile, "utf8"))
      } catch {
        if (Date.now() > deadline) throw new Error("wedged child never wrote its pid")
        await new Promise((resolve) => setTimeout(resolve, 50))
      }
    }
    assert.ok(Number.isInteger(pid) && pid > 0, `wedged pid = ${pid}`)

    // The timeout must reject gracefully — an unlistened spawn 'error' here
    // would crash the test process (the RED failure mode).
    await assert.rejects(closed, /close timed out/)

    // The wedged child must actually die (tree kill or fallback), never survive.
    const killDeadline = Date.now() + 10000
    for (;;) {
      let alive = true
      try {
        process.kill(pid, 0)
      } catch (error) {
        alive = error.code === "EPERM"
      }
      if (!alive) break
      if (Date.now() > killDeadline) {
        // Clean up so the suite does not leak a forever-process, then fail.
        try { process.kill(pid, "SIGKILL") } catch { /* already dead */ }
        throw new Error(`wedged child ${pid} survived the close-timeout kill`)
      }
      await new Promise((resolve) => setTimeout(resolve, 50))
    }
  })
})
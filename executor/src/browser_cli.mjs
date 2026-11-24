/**
 * Browser capability domain (executor v2, design v1.3.1 §4.2 P10/P11, Q3=B).
 *
 * The browser runtime is the OFFICIAL `@playwright/cli` (`playwright-cli`),
 * installed globally and FLOATING at `@latest` (ARTEX model: an image rebuild
 * upgrades the CLI; each CLI version pins its own — possibly alpha — Playwright
 * engine). This module is a thin convenience wrapper over the general exec
 * channel, NOT a capability ceiling and NOT a business-action enumeration:
 *
 *   - every `playwright-cli` subcommand passes through verbatim (102 today,
 *     more tomorrow); the tool layer never lists them;
 *   - the wrapper only aligns the CLI's named session (`-s`) with the executor
 *     session so concurrent case_runs never share browser state, resolves the
 *     installed CLI entry, and settles the CLI's output files as typed evidence
 *     (snapshot/screenshot/trace/console) through the recording layer;
 *   - the general exec/shell channel remains the escape valve (constitution
 *     art.2.6): a model can run `playwright-cli` (or anything else) directly.
 *
 * Recording: the CLI writes its outputs under `<workspace>/.playwright-cli/`
 * (page-*.yml snapshots, page-*.png screenshots, console-*.log, traces/*). That
 * directory is the browser channel's recording area, owned by THIS module's
 * ledger (the workspace watcher excludes it, like `.eztest`). Explicit
 * `--filename` outputs elsewhere are settled here with a typed kind and claimed
 * so the watcher does not double-ledger them. Nothing is silently dropped.
 */

import { spawn } from "node:child_process"
import crypto from "node:crypto"
import fs from "node:fs"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import { killPidTree } from "./exec.mjs"
import { appendArtifact, assertArtifactKind, mimeForPath } from "./recording.mjs"
import { ExecutorError } from "./protocol.mjs"

// The CLI's own output directory inside the session workspace. The workspace
// watcher excludes it; this module's ledger owns its settlement.
export const CLI_OUTPUT_DIR = ".playwright-cli"

// Operational bound for closing a CLI session on delete/reclaim (best effort).
const CLI_CLOSE_TIMEOUT_MS = 15_000

let cachedResolution = null

/**
 * Resolves the installed `@playwright/cli` entry point. Returns the argv prefix
 * that runs it: `[process.execPath, <playwright-cli.js>]` when the JS entry is
 * found (uniform cross-platform — avoids the Windows `.cmd` shim, which cannot
 * be spawned without a shell and whose Volta wrapper wedges piped stdio), or
 * `["playwright-cli"]` as a POSIX PATH fallback. `packageDir` locates the
 * package.json for version/engine fingerprinting.
 *
 * Resolution is cached; a missing CLI is a typed `browser_cli_unavailable`
 * error at invocation time (never a silent fallback to a different runtime).
 */
export function resolvePlaywrightCli() {
  if (cachedResolution) {
    return cachedResolution
  }
  const override = process.env.EZTEST_PLAYWRIGHT_CLI_JS
  if (override && fs.existsSync(override)) {
    cachedResolution = { argvPrefix: [process.execPath, override], packageDir: path.dirname(override) }
    return cachedResolution
  }
  const home = os.homedir()
  const voltaHome = process.env.VOLTA_HOME
  const candidateRoots = [
    // Volta global package image. Windows: %LOCALAPPDATA%/Volta; POSIX: ~/.volta
    // (or $VOLTA_HOME). The package nests its own node_modules.
    voltaHome ? path.join(voltaHome, "tools", "image", "packages", "@playwright", "cli", "node_modules") : null,
    process.env.LOCALAPPDATA
      ? path.join(process.env.LOCALAPPDATA, "Volta", "tools", "image", "packages", "@playwright", "cli", "node_modules")
      : null,
    path.join(home, ".volta", "tools", "image", "packages", "@playwright", "cli", "node_modules"),
    // npm global root (Windows: %APPDATA%/npm; POSIX: prefix/lib).
    process.env.APPDATA ? path.join(process.env.APPDATA, "npm", "node_modules") : null,
    process.env.npm_config_prefix ? path.join(process.env.npm_config_prefix, "lib", "node_modules") : null,
    "/usr/local/lib/node_modules",
    "/usr/lib/node_modules",
    path.join(home, ".npm-global", "lib", "node_modules"),
  ].filter(Boolean)
  // pnpm global store: $PNPM_HOME/global/<layout>/node_modules.
  const pnpmHome = process.env.PNPM_HOME
  if (pnpmHome) {
    const globalDir = path.join(pnpmHome, "global")
    try {
      for (const entry of fs.readdirSync(globalDir, { withFileTypes: true })) {
        if (entry.isDirectory()) {
          candidateRoots.push(path.join(globalDir, entry.name, "node_modules"))
        }
      }
    } catch {
      // no pnpm global dir
    }
  }
  for (const root of candidateRoots) {
    const packageDir = path.join(root, "@playwright", "cli")
    const entry = path.join(packageDir, "playwright-cli.js")
    if (fs.existsSync(entry)) {
      cachedResolution = { argvPrefix: [process.execPath, entry], packageDir }
      return cachedResolution
    }
  }
  // POSIX PATH fallback: the npm/global bin symlink is directly executable.
  if (process.platform !== "win32") {
    for (const dir of (process.env.PATH ?? "").split(path.delimiter)) {
      if (!dir) continue
      const candidate = path.join(dir, "playwright-cli")
      try {
        fs.accessSync(candidate, fs.constants.X_OK)
        cachedResolution = { argvPrefix: [candidate], packageDir: null }
        return cachedResolution
      } catch {
        // keep searching
      }
    }
  }
  cachedResolution = { argvPrefix: null, packageDir: null }
  return cachedResolution
}

/** Test hook: forget the cached resolution (e.g. after installing the CLI). */
export function resetPlaywrightCliResolution() {
  cachedResolution = null
}

/** Test hook: force a resolution result (e.g. {argvPrefix:null} to exercise the
 * typed browser_cli_unavailable path on a machine that has the CLI installed). */
export function __forceCliResolutionForTest(resolution) {
  cachedResolution = resolution
}

/**
 * CLI + engine version for the reproducibility fingerprint (constitution
 * art.4.6: the fingerprint must capture the CLI version AND the engine it
 * pins — `@latest` floats the CLI, and each CLI pins a specific engine).
 * Static package.json read; null when the CLI is not resolvable.
 */
export function cliVersionInfo() {
  const { packageDir } = resolvePlaywrightCli()
  if (!packageDir) {
    return null
  }
  try {
    const pkg = JSON.parse(fs.readFileSync(path.join(packageDir, "package.json"), "utf8"))
    const engine =
      pkg.dependencies?.["playwright-core"] ?? pkg.dependencies?.playwright ?? null
    return { cli_version: pkg.version ?? null, engine_version: engine }
  } catch {
    return null
  }
}

function baseName(program) {
  const base = path.basename(String(program)).toLowerCase()
  return base.replace(/\.(exe|cmd|bat|ps1)$/u, "")
}

/**
 * Reports whether an argv runs the playwright CLI. Matches the bare program
 * name (any extension), the resolved JS entry, or the bare name as argv[0].
 */
export function isBrowserCliArgv(argv) {
  if (!Array.isArray(argv) || argv.length === 0 || typeof argv[0] !== "string") {
    return false
  }
  if (baseName(argv[0]) === "playwright-cli") {
    return true
  }
  // [node, /path/playwright-cli.js, ...]
  if (
    argv.length >= 2 &&
    typeof argv[1] === "string" &&
    path.basename(argv[1]).toLowerCase() === "playwright-cli.js"
  ) {
    return true
  }
  return false
}

/** Extracts the CLI subcommand (first non-flag token after the program/-s). */
export function browserCliSubcommand(argv) {
  // Skip the program prefix (1 for bare name, 2 for [node, entry.js]).
  let i = baseName(argv[0]) === "playwright-cli" ? 1 : 2
  for (; i < argv.length; i += 1) {
    const token = argv[i]
    if (typeof token !== "string") continue
    if (token === "-s" || token === "--session") {
      i += 1 // skip its value
      continue
    }
    if (token.startsWith("-")) {
      continue
    }
    return token
  }
  return null
}

/** Reads an explicit `--filename`/`--filename=<v>` target from the argv. */
function explicitFilename(argv) {
  for (let i = 0; i < argv.length; i += 1) {
    const token = argv[i]
    if (typeof token !== "string") continue
    if (token.startsWith("--filename=")) {
      return token.slice("--filename=".length)
    }
    if (token === "--filename" && typeof argv[i + 1] === "string") {
      return argv[i + 1]
    }
  }
  return null
}

/** Reads an explicit `-s=<name>`/`-s <name>`/`--session` the caller supplied. */
function explicitSession(argv) {
  for (let i = 0; i < argv.length; i += 1) {
    const token = argv[i]
    if (typeof token !== "string") continue
    if (token.startsWith("-s=")) return token.slice(3)
    if (token.startsWith("--session=")) return token.slice("--session=".length)
    if ((token === "-s" || token === "--session") && typeof argv[i + 1] === "string") {
      return argv[i + 1]
    }
  }
  return null
}

/**
 * Prepares a `playwright-cli` exec invocation inside a session:
 *   - resolves the installed CLI entry and rewrites the program prefix;
 *   - injects `-s=<session.id>` so the CLI's named session is aligned 1:1 with
 *     the executor session (unless the caller named one explicitly, which is
 *     respected and tracked);
 *   - records the CLI session name + any explicit output target on the session
 *     for cleanup and typed settlement.
 *
 * Non-CLI argv is returned unchanged. A missing CLI is a typed error.
 */
export function prepareBrowserCliInvocation(session, params) {
  if (!params || !Array.isArray(params.argv) || !isBrowserCliArgv(params.argv)) {
    return params
  }
  const { argvPrefix } = resolvePlaywrightCli()
  if (!argvPrefix) {
    throw new ExecutorError(
      "browser_cli_unavailable",
      "playwright-cli is not installed; install @playwright/cli@latest (npm install -g @playwright/cli@latest) or set EZTEST_PLAYWRIGHT_CLI_JS",
    )
  }
  const original = params.argv
  // Strip the caller's program prefix (bare name or [node, entry.js]).
  const rest = baseName(original[0]) === "playwright-cli" ? original.slice(1) : original.slice(2)
  const named = explicitSession(rest)
  const cliSession = named ?? session.id
  const argv = [...argvPrefix]
  if (!named) {
    argv.push(`-s=${cliSession}`)
  }
  argv.push(...rest)
  // Track the CLI session for lifecycle cleanup and the daemon pid for crash
  // recovery (the daemon is detached, so it is not a child of the exec command).
  if (!(session.cliSessions instanceof Set)) {
    session.cliSessions = new Set()
  }
  session.cliSessions.add(cliSession)
  const filename = explicitFilename(rest)
  if (filename) {
    if (!Array.isArray(session.cliOutputs)) {
      session.cliOutputs = []
    }
    session.cliOutputs.push({ path: filename, subcommand: browserCliSubcommand(argv) })
  }
  return { ...params, argv }
}

/** Maps a CLI output file to its evidence kind by extension/name. */
export function kindForCliOutput(relativePath, subcommand) {
  const base = path.basename(relativePath).toLowerCase()
  const ext = path.extname(base)
  if (subcommand === "snapshot" || ext === ".yml" || ext === ".yaml") {
    return "snapshot"
  }
  if (subcommand === "screenshot" || [".png", ".jpg", ".jpeg", ".webp", ".gif"].includes(ext)) {
    return "screenshot"
  }
  if (subcommand === "tracing-stop" || ext === ".trace" || ext === ".network") {
    return "trace"
  }
  if (subcommand === "console" || base.startsWith("console-") || ext === ".log") {
    return "console"
  }
  return "file"
}

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

/**
 * Settles the CLI's output files as typed evidence artifacts. Scans the
 * `.playwright-cli` output dir (recursively) plus any explicit `--filename`
 * targets, dedupes by a per-session sha256 ledger, and appends settle-ready
 * artifacts (snapshot/screenshot/trace/console/file). Files above the artifact
 * limit ledger metadata with an explicit content_omitted flag (never silent).
 *
 * `command_id` attributes the artifacts to the command that produced them.
 * Returns the produced artifacts.
 */
export async function settleBrowserCliArtifacts(
  session,
  { command_id = null, artifactLimitBytes = 200 * 1024 * 1024, logger = null } = {},
) {
  if (!(session.cliLedger instanceof Map)) {
    session.cliLedger = new Map()
  }
  if (!(session.cliArtifactClaims instanceof Map)) {
    session.cliArtifactClaims = new Map()
  }
  const produced = []
  const targets = new Map() // relativePath -> subcommand hint

  // Explicit --filename outputs (may live outside .playwright-cli).
  for (const output of session.cliOutputs ?? []) {
    if (typeof output?.path === "string" && output.path.length > 0) {
      targets.set(output.path.split(path.sep).join("/"), output.subcommand ?? null)
    }
  }

  // The CLI output dir, scanned recursively.
  const outputRoot = path.join(session.workspace, CLI_OUTPUT_DIR)
  const stack = [outputRoot]
  while (stack.length > 0) {
    const dir = stack.pop()
    let entries
    try {
      entries = await fsp.readdir(dir, { withFileTypes: true })
    } catch {
      continue
    }
    for (const entry of entries) {
      const absolute = path.join(dir, entry.name)
      const relative = path.relative(session.workspace, absolute).split(path.sep).join("/")
      if (entry.isDirectory()) {
        stack.push(absolute)
        continue
      }
      if (!entry.isFile() || entry.isSymbolicLink()) {
        continue
      }
      if (!targets.has(relative)) {
        targets.set(relative, null)
      }
    }
  }

  for (const [relative, subcommandHint] of targets) {
    const absolute = path.join(session.workspace, ...relative.split("/"))
    let stat
    try {
      stat = await fsp.stat(absolute)
    } catch {
      continue
    }
    if (!stat.isFile()) {
      continue
    }
    const known = session.cliLedger.get(relative)
    if (known && known.size === stat.size && known.mtimeMs === stat.mtimeMs) {
      continue
    }
    let content = null
    let sha
    if (stat.size <= artifactLimitBytes) {
      try {
        content = await fsp.readFile(absolute)
      } catch {
        continue
      }
      sha = sha256(content)
    } else {
      sha = await hashFileStreaming(absolute)
    }
    if (known && known.sha256 === sha) {
      session.cliLedger.set(relative, { sha256: sha, size: stat.size, mtimeMs: stat.mtimeMs })
      continue
    }
    const kind = kindForCliOutput(relative, subcommandHint)
    assertArtifactKind(kind)
    const artifact = {
      kind,
      session_id: session.id,
      path: relative,
      sha256: sha,
      size: stat.size,
      mime: kind === "snapshot" ? "text/plain; charset=utf-8" : mimeForPath(relative),
      command_id: command_id ?? null,
      filename: relative.split("/").pop(),
      base64: Buffer.isBuffer(content) ? content.toString("base64") : null,
    }
    if (!Buffer.isBuffer(content)) {
      artifact.content_omitted = true
      artifact.content_reason = "exceeds_artifact_limit"
    }
    if (Array.isArray(session.artifacts)) {
      await appendArtifact(session, artifact, logger)
    }
    produced.push(artifact)
    session.cliLedger.set(relative, { sha256: sha, size: stat.size, mtimeMs: stat.mtimeMs })
    // Claim the path+sha so the workspace watcher does not double-ledger it.
    session.cliArtifactClaims.set(relative, sha)
  }
  return produced
}

function hashFileStreaming(absolutePath) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash("sha256")
    const stream = fs.createReadStream(absolutePath)
    stream.on("data", (chunk) => hash.update(chunk))
    stream.on("error", reject)
    stream.on("end", () => resolve(hash.digest("hex")))
  })
}

/**
 * Records the daemon pid the CLI reports on `open`/`attach` so crash-orphan
 * recovery can kill it (the daemon is detached, not a child of the exec
 * command). Best effort parse of "opened with pid <n>".
 */
export function recordCliDaemonPid(session, stdout) {
  if (typeof stdout !== "string") {
    return
  }
  const match = /opened with pid (\d+)/u.exec(stdout)
  if (!match) {
    return
  }
  const pid = Number(match[1])
  if (!Number.isInteger(pid) || pid <= 0) {
    return
  }
  if (!(session.cliDaemons instanceof Map)) {
    session.cliDaemons = new Map()
  }
  // Attribute to the most recently used CLI session name.
  const names = [...(session.cliSessions ?? [])]
  const name = names[names.length - 1] ?? session.id
  session.cliDaemons.set(name, pid)
}

/** Live CLI daemon pids of a session (for the crash-recovery registry). */
export function cliDaemonPids(session) {
  if (!(session.cliDaemons instanceof Map)) {
    return []
  }
  return [...session.cliDaemons.values()].filter((pid) => Number.isInteger(pid) && pid > 0)
}

/**
 * Closes every CLI session a session opened (graceful: the CLI closes its
 * browser and daemon). Best effort and bounded; a failure is logged, never
 * silent, and never blocks session deletion. Called on DELETE and on reclaim.
 */
export async function closeBrowserCliSessions(session, { logger = null } = {}) {
  const names = [...(session.cliSessions ?? [])]
  if (names.length === 0) {
    return
  }
  const { argvPrefix } = resolvePlaywrightCli()
  if (!argvPrefix) {
    logger?.warn?.("[executor] playwright-cli unavailable; CLI sessions left to idle-timeout")
    return
  }
  for (const name of names) {
    try {
      await runCliClose(argvPrefix, name, session.workspace)
      session.cliDaemons?.delete(name)
    } catch (error) {
      logger?.warn?.(
        `[executor] closing CLI session ${name} failed (left to idle-timeout): ${error?.message ?? error}`,
      )
    }
  }
  session.cliSessions.clear()
}

/**
 * Runs `playwright-cli -s=<name> close` with a bounded deadline. Exported with a
 * timeout seam so tests can exercise the close-timeout kill path directly.
 */
export function runCliClose(argvPrefix, name, cwd, { timeoutMs = CLI_CLOSE_TIMEOUT_MS } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(argvPrefix[0], [...argvPrefix.slice(1), `-s=${name}`, "close"], {
      cwd,
      stdio: ["ignore", "ignore", "pipe"],
      windowsHide: true,
    })
    let stderr = ""
    child.stderr.on("data", (chunk) => {
      stderr += chunk
    })
    const timer = setTimeout(() => {
      // Platform-branched tree kill (the exec engine's single authority):
      // Windows taskkill /T /F with an 'error' listener, POSIX process-group
      // SIGKILL. A raw Windows-only taskkill spawn here crashed the executor on
      // POSIX (ENOENT with no 'error' listener) and never killed the daemon.
      void killPidTree(child.pid).then(() => {
        // Best-effort fallback: if the tree kill could not run (e.g. the kill
        // binary is missing), terminate the direct child so a wedged close
        // never survives the timeout. No-op when already dead.
        try {
          child.kill("SIGKILL")
        } catch {
          // already dead
        }
      })
      reject(new Error("close timed out"))
    }, timeoutMs)
    timer.unref?.()
    child.on("error", (error) => {
      clearTimeout(timer)
      reject(error)
    })
    child.on("close", (code) => {
      clearTimeout(timer)
      if (code === 0) {
        resolve()
      } else {
        reject(new Error(`close exited ${code}: ${stderr.slice(0, 200)}`))
      }
    })
  })
}
#!/usr/bin/env node
/*
 * EzTest v0.22 W1-6 gate: explore / ingest residue is gone.
 *
 * The v0.22 W1-1 decision (C-4) keeps the server-side P9 discovery/API probing
 * but removes the LLM explore path. This gate proves the two residue classes
 * that can silently outlive the code deletion:
 *   1. docs/openapi/api.yaml still lists dead `explore.*` work item kinds or the
 *      removed `requirement.curate` work type anywhere (enum or prose);
 *   2. backend Go code still writes exploration facts with the removed
 *      `Source: "explorer"` label instead of the P9 runtime label `"p9"`.
 *
 * WorkTypeAttachmentIngest: W0 moved attachment extraction to the deterministic
 * server pipeline (the reconciler no longer enqueues the agent work item), but
 * the legacy symbol and its fail-closed runner branches are still referenced at
 * this baseline, so an identifier-only "must not appear" check cannot pass
 * without deleting live code. W1-6 is scoped to residue, not to that deletion;
 * this gate therefore rejects only the dead shape the plan names: a constant
 * that is declared and referenced nowhere else.
 *
 * Dependency-free (node:fs / node:path only). Exit 0 = clean, 1 = residue.
 */
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const relative = (file) => path.relative(root, file).split(path.sep).join("/")

const problems = []
const report = (file, line, message) => {
  problems.push(relative(file) + ":" + line + ": " + message)
}

// ---------------------------------------------------------------------------
// 1. OpenAPI: dead work item kinds and the removed requirement.curate literal.
// ---------------------------------------------------------------------------
const apiFile = path.join(root, "docs", "openapi", "api.yaml")
const apiText = fs.readFileSync(apiFile, "utf8")
apiText.split(/\r?\n/).forEach((line, index) => {
  if (/explore\.(login|page|api|coverage|gap)/.test(line)) {
    report(apiFile, index + 1, "dead explore work item kind")
  }
  if (/requirement\.curate/.test(line)) {
    report(apiFile, index + 1, "removed requirement.curate work type")
  }
})

// ---------------------------------------------------------------------------
// 2. Backend Go: legacy explorer fact source (and an unused ingest constant).
// ---------------------------------------------------------------------------
function walk(dir) {
  const files = []
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name === ".git") continue
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      files.push(...walk(full))
    } else if (entry.isFile() && entry.name.endsWith(".go")) {
      files.push(full)
    }
  }
  return files
}

const backendDir = path.join(root, "backend")
const goFiles = walk(backendDir)
const ingestFiles = []
for (const file of goFiles) {
  const text = fs.readFileSync(file, "utf8")
  if (text.includes("WorkTypeAttachmentIngest")) ingestFiles.push(file)
  text.split(/\r?\n/).forEach((line, index) => {
    if (/Source:\s*"explorer"/.test(line)) {
      report(file, index + 1, 'legacy Source: "explorer" (use "p9")')
    }
  })
}
// The constant is only residue when nothing references it any more. A
// declaration in ports_agent.go plus zero references elsewhere is dead code;
// live references mean the deterministic attachment ingest still owns it.
const ingestReferenced = ingestFiles.some((file) => path.basename(file) !== "ports_agent.go")
if (ingestFiles.length > 0 && !ingestReferenced) {
  report(ingestFiles[0], 1, "unused WorkTypeAttachmentIngest constant")
}

// ---------------------------------------------------------------------------
// Result.
// ---------------------------------------------------------------------------
if (problems.length > 0) {
  console.error("v0.22 explore residue: " + problems.length + " problem(s)")
  for (const problem of problems) console.error("  " + problem)
  process.exit(1)
}
console.log(
  "v0.22 explore residue: clean (" +
    goFiles.length +
    " Go files scanned, docs/openapi/api.yaml checked, 0 problems)",
)

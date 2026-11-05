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
 * WorkTypeAttachmentIngest: v0.22 R-W1-5 moved attachment extraction to the
 * deterministic server pipeline and deleted the Host `attachment.ingest` work
 * item path. The legacy symbol must therefore have zero references anywhere in
 * backend Go code (the plan's literal identifier check).
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
// 2. Backend Go: legacy explorer fact source and the deleted ingest work item.
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
for (const file of goFiles) {
  const text = fs.readFileSync(file, "utf8")
  text.split(/\r?\n/).forEach((line, index) => {
    if (line.includes("WorkTypeAttachmentIngest")) {
      report(file, index + 1, "deleted WorkTypeAttachmentIngest work item path")
    }
    if (/Source:\s*"explorer"/.test(line)) {
      report(file, index + 1, 'legacy Source: "explorer" (use "p9")')
    }
  })
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

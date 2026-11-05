#!/usr/bin/env node
/*
 * EzTest v0.22 W1-3 gate: the 16 business "API-only" routes are disposed.
 *
 * Registry: scripts/v022-w1-api-only.json
 *   - exactly 16 business routes (plan W1-3 table, order 1..16)
 *   - 4 x-api-only routes must carry `x-api-only: true` in docs/openapi/api.yaml
 *   - 12 ui routes must name the frontend file W1-4 creates
 *   - 4 infra routes (health/ready/internal register/heartbeat) are exempt
 *
 * `--strict` additionally requires every ui frontendFile to exist and to
 * contain the route path's last segment (the W1-4 call site). Before W1-4
 * creates those files, strict is expected to fail; non-strict is the W1-3 gate.
 *
 * YAML is read through the frontend workspace dependency because the repository
 * root has no node_modules of its own (`require("yaml")` would not resolve).
 *
 * Exit 0 = disposition complete, 1 = missing/unknown/stale item.
 */
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const strict = process.argv.includes("--strict")
const yaml = require(path.join(root, "frontend", "node_modules", "yaml"))

const spec = yaml.parse(fs.readFileSync(path.join(root, "docs", "openapi", "api.yaml"), "utf8"))
const registry = JSON.parse(fs.readFileSync(path.join(__dirname, "v022-w1-api-only.json"), "utf8"))

const problems = []
const fail = (message) => problems.push(message)

const HTTP_METHODS = new Set(["get", "post", "put", "patch", "delete", "head", "options", "trace"])
const methodOf = (item) => String(item.method || "").toLowerCase()
const labelOf = (item) => methodOf(item).toUpperCase() + " " + item.path
const operationAt = (route, method) => (spec.paths && spec.paths[route] ? spec.paths[route][method] : undefined)

// Every registry row carries the backend registration as file:line.
const checkEvidence = (label, evidence) => {
  if (typeof evidence !== "string" || evidence.trim() === "") {
    fail("missing evidence: " + label)
    return
  }
  const match = /^(.+):(\d+)$/.exec(evidence)
  if (!match) {
    fail("evidence must be file:line: " + label + " -> " + evidence)
    return
  }
  const file = path.join(root, match[1])
  if (!fs.existsSync(file)) {
    fail("evidence file missing: " + label + " -> " + match[1])
    return
  }
  const lineCount = fs.readFileSync(file, "utf8").split("\n").length
  const line = Number(match[2])
  if (line < 1 || line > lineCount) fail("evidence line out of range: " + label + " -> " + evidence)
}

const routes = Array.isArray(registry.routes) ? registry.routes : []
if (!Array.isArray(registry.routes)) fail("registry.routes must be an array")

const counts = { "x-api-only": 0, ui: 0 }
const seen = new Set()

for (const item of routes) {
  const method = methodOf(item)
  if (!HTTP_METHODS.has(method) || typeof item.path !== "string" || !item.path.startsWith("/")) {
    fail("invalid registry entry: " + JSON.stringify(item))
    continue
  }
  const label = labelOf(item)
  if (seen.has(label)) fail("duplicate registry entry: " + label)
  seen.add(label)

  if (item.disposition !== "x-api-only" && item.disposition !== "ui") {
    fail("unknown disposition: " + label + " -> " + item.disposition)
    continue
  }

  const op = operationAt(item.path, method)
  if (!op) {
    fail("missing operation " + label)
    continue
  }
  checkEvidence(label, item.evidence)

  if (item.disposition === "x-api-only") {
    counts["x-api-only"]++
    if (op["x-api-only"] !== true) fail("not x-api-only: " + label)
    else console.log("ok: x-api-only " + label)
    continue
  }

  counts.ui++
  if (op["x-api-only"] === true) fail("ui route is marked x-api-only: " + label)
  if (!item.frontendFile) {
    fail("ui entry missing frontendFile: " + label)
    continue
  }
  if (strict) {
    const file = path.join(root, item.frontendFile)
    if (!fs.existsSync(file)) {
      fail("ui file missing: " + item.frontendFile + " (" + label + ")")
    } else if (!fs.readFileSync(file, "utf8").includes(item.path.split("/").pop())) {
      fail("ui file does not call " + label + ": " + item.frontendFile)
    } else {
      console.log("ok: ui " + label + " -> " + item.frontendFile)
    }
  } else {
    console.log("ok: ui " + label + " -> " + item.frontendFile)
  }
}

// The plan card is the only baseline: exactly 16 business routes, 4 + 12.
if (routes.length !== 16) fail("registry must hold exactly 16 business routes (got " + routes.length + ")")
if (counts["x-api-only"] !== 4) fail("registry must hold exactly 4 x-api-only routes (got " + counts["x-api-only"] + ")")
if (counts.ui !== 12) fail("registry must hold exactly 12 ui routes (got " + counts.ui + ")")

// No other operation may be marked x-api-only, and every registry marker must
// exist in the spec (the reverse is reported per entry above).
const specApiOnly = new Set()
for (const [route, operations] of Object.entries(spec.paths || {})) {
  for (const [method, op] of Object.entries(operations || {})) {
    if (HTTP_METHODS.has(method) && op && op["x-api-only"] === true) {
      specApiOnly.add(method.toUpperCase() + " " + route)
    }
  }
}
for (const label of specApiOnly) {
  if (!seen.has(label)) fail("unknown x-api-only operation in OpenAPI: " + label)
}
if (specApiOnly.size !== 4) fail("OpenAPI must mark exactly 4 operations x-api-only (got " + specApiOnly.size + ")")

// Infrastructure routes are registered for completeness only; they never count
// toward the 16 business routes.
const infra = Array.isArray(registry.infraExempt) ? registry.infraExempt : []
if (!Array.isArray(registry.infraExempt) || infra.length !== 4) {
  fail("registry must hold exactly 4 infra-exempt routes (got " + infra.length + ")")
}
for (const item of infra) {
  const method = methodOf(item)
  if (!HTTP_METHODS.has(method) || typeof item.path !== "string" || !item.path.startsWith("/")) {
    fail("invalid infra-exempt entry: " + JSON.stringify(item))
    continue
  }
  const label = labelOf(item)
  if (item.disposition !== "infra-exempt") fail("infra entry has wrong disposition: " + label + " -> " + item.disposition)
  if (seen.has(label)) fail("business route also listed infra-exempt: " + label)
  if (!operationAt(item.path, method)) fail("missing infra operation " + label)
  checkEvidence("infra " + label, item.evidence)
}

if (problems.length) {
  console.error(problems.join("\n"))
  console.error("\nFAIL: " + problems.length + " problem(s)" + (strict ? " (strict)" : ""))
  process.exit(1)
}

console.log(
  "\nok: registry holds 16 business routes (4 x-api-only, 12 ui) + 4 infra-exempt" +
    (strict ? "; strict ui call-site check passed" : "; non-strict (run --strict after W1-4)"),
)

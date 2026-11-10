#!/usr/bin/env node
/*
 * EzTest v0.22 W1-3 gate: the 16 business "API-only" routes are disposed.
 *
 * Registry: scripts/v022-w1-api-only.json
 *   - exactly 16 business routes (plan W1-3 table, order 1..16)
 *   - 4 x-api-only routes must carry `x-api-only: true` in docs/openapi/api.yaml
 *   - 12 ui routes must name the frontend file W1-4 creates, the real
 *     api-client wrapper that file calls (`apiFunction`), and the feature api
 *     module that exports it (`apiModule`)
 *   - 4 infra routes (health/ready/internal register/heartbeat) are exempt
 *
 * `--strict` proves every ui entry is a real call site, not a comment:
 *   1. `frontendFile` exists;
 *   2. it contains the `apiFunction(` call form outside comments and string
 *      literals (a commented-out or quoted mention does not count);
 *   3. `apiFunction` is exported by the feature's api module (`apiModule` when
 *      present; otherwise `api.ts`/`api.tsx` in the feature directory).
 * Before W1-4 creates those files, strict is expected to fail; non-strict is
 * the W1-3 gate. Non-strict only checks the disposition registry itself.
 *
 * `--registry <path>` (or `--registry=<path>`) overrides the registry path so
 * the gate can be exercised against throwaway fixtures. Normal runs use
 * scripts/v022-w1-api-only.json.
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

const registryEquals = process.argv.find((arg) => arg.startsWith("--registry="))
const registrySeparate = process.argv.indexOf("--registry")
const registryFile = registryEquals
  ? path.resolve(root, registryEquals.slice("--registry=".length))
  : registrySeparate >= 0 && process.argv[registrySeparate + 1]
    ? path.resolve(root, process.argv[registrySeparate + 1])
    : path.join(__dirname, "v022-w1-api-only.json")

const relative = (file) => path.relative(root, file).split(path.sep).join("/")

const loadFailure = (what, file, error) => {
  const detail = error && error.message ? error.message : String(error)
  console.error("Error: cannot load " + what + " " + relative(file) + ": " + detail)
  process.exit(1)
}

const readText = (what, file) => {
  try {
    return fs.readFileSync(file, "utf8")
  } catch (error) {
    loadFailure(what, file, error)
  }
}

let yaml
try {
  // The repository root has no node_modules; reuse the frontend workspace copy.
  yaml = require(path.join(root, "frontend", "node_modules", "yaml"))
} catch (error) {
  console.error(
    "Error: cannot load yaml module from frontend/node_modules/yaml: " +
      (error && error.message ? error.message : String(error)),
  )
  process.exit(1)
}

const specFile = path.join(root, "docs", "openapi", "api.yaml")
let spec
try {
  spec = yaml.parse(readText("OpenAPI spec", specFile))
} catch (error) {
  loadFailure("OpenAPI spec", specFile, error)
}
let registry
try {
  registry = JSON.parse(readText("registry", registryFile))
} catch (error) {
  loadFailure("registry", registryFile, error)
}

if (!spec || typeof spec !== "object" || !spec.paths || typeof spec.paths !== "object") {
  console.error("Error: cannot load OpenAPI paths from " + relative(specFile) + ": missing `paths` object")
  process.exit(1)
}
if (!registry || typeof registry !== "object" || !Array.isArray(registry.routes)) {
  console.error(
    "Error: cannot load registry routes from " + relative(registryFile) + ": `routes` must be an array",
  )
  process.exit(1)
}

const problems = []
const fail = (message) => problems.push(message)

const HTTP_METHODS = new Set(["get", "post", "put", "patch", "delete", "head", "options", "trace"])
const methodOf = (item) => String(item.method || "").toLowerCase()
const labelOf = (item) => methodOf(item).toUpperCase() + " " + item.path
const operationAt = (route, method) => (spec.paths && spec.paths[route] ? spec.paths[route][method] : undefined)

/**
 * Reduces JS/TS source to code: comments and the contents of string/template
 * literals become whitespace. A commented-out or quoted `apiFunction(` mention
 * therefore cannot satisfy the strict call-site check.
 */
const stripNonCode = (source) => {
  let out = ""
  let index = 0
  while (index < source.length) {
    const char = source[index]
    const next = source[index + 1]
    if (char === "/" && next === "/") {
      while (index < source.length && source[index] !== "\n") index++
      out += "\n"
      continue
    }
    if (char === "/" && next === "*") {
      index += 2
      while (index < source.length && !(source[index] === "*" && source[index + 1] === "/")) {
        if (source[index] === "\n") out += "\n"
        index++
      }
      index += 2
      out += " "
      continue
    }
    if (char === '"' || char === "'" || char === "`") {
      const quote = char
      index++
      while (index < source.length) {
        if (source[index] === "\\") {
          index += 2
          continue
        }
        if (source[index] === "\n") out += "\n"
        const done = source[index] === quote
        index++
        if (done) break
      }
      out += '""'
      continue
    }
    out += char
    index++
  }
  return out
}

const escapeRegExp = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")

/** True when `name` is exported by the module (function/const/var/re-export/default). */
const hasExportedFunction = (source, name) => {
  const needle = escapeRegExp(name)
  return [
    "\\bexport\\s+(?:async\\s+)?function\\s+" + needle + "\\b",
    "\\bexport\\s+default\\s+(?:async\\s+)?function\\s+" + needle + "\\b",
    "\\bexport\\s+(?:const|let|var)\\s+" + needle + "\\b",
    "\\bexport\\s*\\{[^}]*\\b" + needle + "\\b[^}]*\\}",
  ].some((pattern) => new RegExp(pattern).test(source))
}

/** `frontend/src/features/<feature>/...` -> `frontend/src/features/<feature>`. */
const featureDirOf = (frontendFile) => {
  const parts = String(frontendFile).split("/")
  const index = parts.indexOf("features")
  if (index < 0 || index + 1 >= parts.length) return null
  return parts.slice(0, index + 2).join("/")
}

/**
 * Resolves the api module for a ui entry. `apiModule` in the registry wins;
 * otherwise the feature directory's api.ts/api.tsx is used (no guessing about
 * files outside the feature).
 */
const resolveApiModule = (item, label) => {
  if (typeof item.apiModule === "string" && item.apiModule.trim() !== "") {
    return { rel: item.apiModule, file: path.join(root, item.apiModule) }
  }
  const featureDir = featureDirOf(item.frontendFile)
  if (!featureDir) {
    fail("ui entry has no apiModule and frontendFile is not under features/: " + label)
    return null
  }
  const candidates = ["api.ts", "api.tsx"]
    .map((name) => featureDir + "/" + name)
    .filter((rel) => fs.existsSync(path.join(root, rel)))
  if (candidates.length === 0) {
    fail("api module missing for " + label + ": expected " + featureDir + "/api.ts or api.tsx")
    return null
  }
  return { rel: candidates[0], file: path.join(root, candidates[0]) }
}

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

const routes = registry.routes
if (!Array.isArray(registry.infraExempt)) {
  console.error(
    "Error: cannot load registry infra routes from " + relative(registryFile) + ": `infraExempt` must be an array",
  )
  process.exit(1)
}

const counts = { "x-api-only": 0, ui: 0 }
const seen = new Set()
const uiApiFunctions = new Map()

// Shared shape validation for the business and infra-exempt lists.
const basicEntry = (item, kind) => {
  if (!item || typeof item !== "object") {
    fail("invalid " + kind + " entry: " + JSON.stringify(item))
    return null
  }
  const method = methodOf(item)
  if (!HTTP_METHODS.has(method) || typeof item.path !== "string" || !item.path.startsWith("/")) {
    fail("invalid " + kind + " entry: " + JSON.stringify(item))
    return null
  }
  return { method, label: labelOf(item) }
}

for (const item of routes) {
  const basic = basicEntry(item, "registry")
  if (!basic) continue
  const { method, label } = basic
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
    if (typeof item.apiFunction !== "string" || item.apiFunction.trim() === "") {
      fail("ui entry missing apiFunction: " + label)
      continue
    }
    if (uiApiFunctions.has(item.apiFunction)) {
      fail(
        "duplicate ui apiFunction: " +
          item.apiFunction +
          " (" +
          uiApiFunctions.get(item.apiFunction) +
          " and " +
          label +
          ")",
      )
      continue
    }
    uiApiFunctions.set(item.apiFunction, label)

    let entryOk = true
    const frontendFile = path.join(root, item.frontendFile)
    if (!fs.existsSync(frontendFile)) {
      fail("ui file missing: " + item.frontendFile + " (" + label + ")")
      entryOk = false
    } else if (!stripNonCode(fs.readFileSync(frontendFile, "utf8")).includes(item.apiFunction + "(")) {
      fail(
        "ui call-site missing: " +
          item.apiFunction +
          "( not found (outside comments and strings) in " +
          item.frontendFile +
          " (" +
          label +
          ")",
      )
      entryOk = false
    }

    const moduleInfo = resolveApiModule(item, label)
    if (moduleInfo) {
      if (!fs.existsSync(moduleInfo.file)) {
        fail("api module missing: " + moduleInfo.rel + " (" + label + ")")
        entryOk = false
      } else if (
        !hasExportedFunction(stripNonCode(fs.readFileSync(moduleInfo.file, "utf8")), item.apiFunction)
      ) {
        fail(
          "api function export missing: " +
            item.apiFunction +
            " in " +
            moduleInfo.rel +
            " (" +
            label +
            ")",
        )
        entryOk = false
      }
    } else {
      entryOk = false
    }

    if (entryOk) {
      console.log(
        "ok: ui entry " + label + " -> " + item.frontendFile + " calls " + item.apiFunction + " from " + moduleInfo.rel,
      )
    }
  } else {
    console.log("ok: ui " + label + " -> " + item.frontendFile)
  }
}

// The plan card is the only baseline: exactly 16 business routes, 4 + 12.
if (routes.length !== 16) fail("registry must hold exactly 16 business routes (got " + routes.length + ")")
if (counts["x-api-only"] !== 5) fail("registry must hold exactly 5 x-api-only routes (got " + counts["x-api-only"] + ")")
if (counts.ui !== 11) fail("registry must hold exactly 11 ui routes (got " + counts.ui + ")")

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
if (specApiOnly.size !== 5) fail("OpenAPI must mark exactly 5 operations x-api-only (got " + specApiOnly.size + ")")

// Infrastructure routes are registered for completeness only; they never count
// toward the 16 business routes.
const infra = registry.infraExempt
if (infra.length !== 4) {
  fail("registry must hold exactly 4 infra-exempt routes (got " + infra.length + ")")
}
for (const item of infra) {
  const basic = basicEntry(item, "infra-exempt")
  if (!basic) continue
  const { method, label } = basic
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
  "\nok: registry holds 16 business routes (5 x-api-only, 11 ui) + 4 infra-exempt" +
    (strict
      ? "; strict ui entry: file + export + call-site present"
      : "; non-strict (run --strict after W1-4)"),
)

#!/usr/bin/env node
/*
 * EzTest v0.22 W0 additive contract gate.
 *
 * The 0.2.1 release artifact (docs/openapi/api.yaml) is frozen as a baseline and
 * may only grow through documented v0.22 additive blocks. This gate asserts:
 *   - the 0.2.1 identity/history survives (version/x-frozen 0.2.1, Task 85
 *     provenance, P8 history counts);
 *   - every expected v0.22 W0 path/operation/schema is present;
 *   - there are no OTHER additions: the current artifact minus the frozen
 *     0.2.1 contract must be exactly this documented W0 set (a new unrecorded
 *     path or schema fails the gate, so the contract cannot drift silently).
 *
 * Dependency-free (node:fs / node:path only). Exit 0 = v0.22 additive block
 * complete and exactly as documented; 1 = missing or undocumented entries.
 */
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const releaseFile = path.join(root, "docs", "openapi", "api.yaml")
const frozenFile = path.join(root, "docs", "version-V0.2", "contracts", "openapi.yaml")
const read = (file) => fs.readFileSync(file, "utf8").replace(/\r\n/g, "\n")
const release = read(releaseFile)
const frozen = read(frozenFile)

let failures = 0
const check = (ok, message) => {
  if (ok) {
    console.log("ok: " + message)
    return
  }
  failures++
  console.error("FAIL: " + message)
}

// Minimal path/method indexer: two-space path keys, four-space method keys.
const parsePaths = (text) => {
  const map = new Map()
  let current = null
  for (const line of text.split("\n")) {
    const pathMatch = /^  (\/[^:]+):\s*$/.exec(line)
    if (pathMatch) {
      current = pathMatch[1]
      map.set(current, new Set())
      continue
    }
    const methodMatch = /^    (get|post|put|patch|delete):\s*$/.exec(line)
    if (methodMatch && current) map.get(current).add(methodMatch[1])
  }
  return map
}

const componentSchemaNames = (text) => {
  const names = new Set()
  let inComponents = false
  let inSchemas = false
  for (const line of text.split("\n")) {
    if (line === "components:") {
      inComponents = true
      continue
    }
    if (inComponents && /^  [a-zA-Z]+:/.test(line)) {
      inSchemas = line === "  schemas:"
      continue
    }
    if (!inSchemas) continue
    const match = /^    ([A-Za-z][A-Za-z0-9_]*):$/.exec(line)
    if (match) names.add(match[1])
  }
  return names
}

// The exact v0.22 W0 additive surface (Task 37). Adding a new path/operation or
// schema requires updating this list in the same change.
const v022Operations = [
  ["PATCH", "/api/versions/{id}/plan-items/{itemId}"],
  ["GET", "/api/versions/{id}/handoff"],
  ["POST", "/api/versions/{id}/facts/{fid}/arbitrate"],
  ["GET", "/api/versions/{id}/requirements/extractions"],
  ["POST", "/api/versions/{id}/requirements/extractions"],
  ["POST", "/api/versions/{id}/requirements/extractions/{runID}/pause"],
  ["POST", "/api/versions/{id}/requirements/extractions/{runID}/resume"],
  ["POST", "/api/versions/{id}/requirements/extractions/{runID}/retry"],
  ["GET", "/api/versions/{id}/attachments/{attachmentId}/images"],
  ["POST", "/api/versions/{id}/attachments/{attachmentId}/reanalyze"],
  ["GET", "/api/versions/{id}/binding"],
  ["PUT", "/api/versions/{id}/binding"],
  ["PATCH", "/api/versions/{id}/binding"],
  ["GET", "/api/suts/{id}/repositories"],
  ["POST", "/api/suts/{id}/repositories"],
  ["GET", "/api/versions/{id}/accounts"],
  ["POST", "/api/versions/{id}/accounts"],
  ["GET", "/api/versions/{id}/questions"],
  ["POST", "/api/versions/{id}/conversations/{cid}/questions"],
  ["POST", "/api/versions/{id}/questions/{qid}/answer"],
  ["POST", "/api/versions/{id}/questions/{qid}/skip"],
  ["POST", "/api/versions/{id}/questions/{qid}/reopen"],
  ["POST", "/api/versions/{id}/stage/request"],
  ["POST", "/api/versions/{id}/stage/advance"],
  ["POST", "/api/versions/{id}/stage/block"],
  ["POST", "/api/versions/{id}/stage/unblock"],
  ["POST", "/api/versions/{id}/stage/pause"],
  ["POST", "/api/versions/{id}/stage/resume"],
  // W1-7 D1: POST reuses the existing GET /api/versions/{id}/waves path item
  // (0 new paths, +1 operation).
  ["POST", "/api/versions/{id}/waves"],
  // W1-8 D8: admin token-bearing executor auth probe (1 new path, +1 operation).
  ["POST", "/api/admin/executors/{id}/check"],
  // W1-13 R-EX-4/API-19: frozen wave scope-snapshot read (1 new path, +1 operation).
  ["GET", "/api/versions/{id}/scope-snapshot"],
  // W1-14A R-EX-5/API-36,37,39,40: discovery observations/deltas/coverage and
  // current gap facts (4 new paths, +4 operations).
  ["GET", "/api/versions/{id}/discovery/observations"],
  ["GET", "/api/versions/{id}/discovery/deltas"],
  ["GET", "/api/versions/{id}/coverage"],
  ["GET", "/api/versions/{id}/gaps"],
  // W2-2 R-W2-1/API-43: version-level human decision producer (the path item
  // already exists via the human-request GET; +1 operation, 0 new paths).
  ["POST", "/api/versions/{id}/human-requests"],
  // W2-4 R-W2-2/API-24: manual issue create/update (both path items already
  // exist from P4; +2 operations, 0 new paths, +2 request schemas).
  ["POST", "/api/versions/{versionId}/issues"],
  ["PATCH", "/api/versions/{versionId}/issues/{issueId}"],
]
const v022Schemas = [
  "AnswerQuestionRequest",
  "AttachmentImageView",
  "BindingCounts",
  "BindingOverview",
  "BindingRepositoryRow",
  "BindingResourceCounts",
  "BindingSnapshot",
  "BindingUpdateRequest",
  "CreateHumanRequestRequest",
  "CreateQuestionRequest",
  "ExtractionRunView",
  "ExtractionSegmentView",
  "ExtractionStartRequest",
  "ExtractionStartResult",
  "FactArbitrationFact",
  "FactArbitrationRequest",
  "FactArbitrationView",
  "HandoffDecision",
  "HandoffOpen",
  "HandoffPlan",
  "HandoffPlanItem",
  "HandoffQA",
  "HandoffRequirement",
  "HandoffResources",
  "HandoffView",
  "ManualIssueCreateRequest",
  "ManualIssueUpdateRequest",
  "PlanItemPatchRequest",
  "PlanItemWrite",
  "QuestionAnswerView",
  "QuestionItemView",
  "QuestionOptionView",
  "QuestionView",
  "SUTRepositoryView",
  "ScopeExclude",
  "StageBlockRequest",
  "VersionStage",
]

// 0.2.1 identity/history survives the additive block.
check(/^  version: 0\.2\.1$/m.test(release), "release openapi info.version is still 0.2.1")
check(/^x-frozen: 0\.2\.1$/m.test(release), "release openapi x-frozen is still 0.2.1")
check(release.includes("Task 85"), "release openapi keeps the Task 85 0.2.1 provenance")
check(
  /P8 快照历史计数仍为 162 paths \/ 200 operations \/ 264 schemas/.test(release),
  "release openapi keeps the P8 history counts",
)
check(
  release.includes("v0.22 W0 增量（Task 37，additive）"),
  "release openapi documents the v0.22 W0 additive section",
)

const releasePaths = parsePaths(release)
const frozenPaths = parsePaths(frozen)
const releaseSchemas = componentSchemaNames(release)
const frozenSchemas = componentSchemaNames(frozen)

for (const [method, route] of v022Operations) {
  check(
    Boolean(releasePaths.get(route)?.has(method.toLowerCase())),
    `v0.22 openapi ${method} ${route}`,
  )
}
for (const schema of v022Schemas) {
  check(releaseSchemas.has(schema), `v0.22 openapi schema ${schema}`)
}

// No unrecorded additions: every release-only operation/schema is expected.
const unexpectedOperations = []
for (const [route, methods] of releasePaths) {
  const frozenMethods = frozenPaths.get(route)
  for (const method of methods) {
    if (frozenMethods?.has(method)) continue
    const expected = v022Operations.some(([expectedMethod, expectedRoute]) => {
      return expectedMethod.toLowerCase() === method && expectedRoute === route
    })
    if (!expected) unexpectedOperations.push(`${method.toUpperCase()} ${route}`)
  }
}
const unexpectedSchemas = [...releaseSchemas].filter((schema) => {
  return !frozenSchemas.has(schema) && !v022Schemas.includes(schema)
})
check(
  unexpectedOperations.length === 0,
  `v0.22 openapi has no undocumented operations (${unexpectedOperations.join(", ") || "none"})`,
)
check(
  unexpectedSchemas.length === 0,
  `v0.22 openapi has no undocumented schemas (${unexpectedSchemas.join(", ") || "none"})`,
)

const missingOperations = v022Operations.filter(([method, route]) => {
  return !releasePaths.get(route)?.has(method.toLowerCase())
})
const missingSchemas = v022Schemas.filter((schema) => !releaseSchemas.has(schema))
const baselinePaths = frozenPaths.size
const baselineOperations = [...frozenPaths.values()].reduce((total, methods) => total + methods.size, 0)
const releaseOperations = [...releasePaths.values()].reduce((total, methods) => total + methods.size, 0)
check(
  releasePaths.size === baselinePaths + 29 && releaseOperations === baselineOperations + 38,
  `v0.22 openapi is exactly the 0.2.1 baseline + 29 paths / 38 operations (got ${releasePaths.size}/${releaseOperations}, baseline ${baselinePaths}/${baselineOperations})`,
)
check(
  releaseSchemas.size === frozenSchemas.size + 37,
  `v0.22 openapi is exactly the 0.2.1 baseline + 37 schemas (got ${releaseSchemas.size}, baseline ${frozenSchemas.size})`,
)
check(missingOperations.length === 0, `v0.22 openapi contains every documented operation (${missingOperations.length} missing)`)
check(missingSchemas.length === 0, `v0.22 openapi contains every documented schema (${missingSchemas.length} missing)`)

if (failures > 0) {
  console.error(`v0.22 additive contract check FAILED (${failures} problem(s))`)
  process.exit(1)
}
console.log(
  `v0.22 additive contract check PASSED: ${releasePaths.size} paths / ${releaseOperations} operations / ${releaseSchemas.size} schemas ` +
    `(0.2.1 baseline ${baselinePaths}/${baselineOperations}/${frozenSchemas.size} + documented W0 additions)`,
)

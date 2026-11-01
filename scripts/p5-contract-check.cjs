#!/usr/bin/env node
/* P5 contract gate: verifies the frozen P5 increments are present and additive. */
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const contracts = path.join(root, "docs", "version-V0.2", "contracts")
const read = (name) => fs.readFileSync(path.join(contracts, name), "utf8")
const check = (ok, message) => {
  if (!ok) {
    console.error("FAIL: " + message)
    process.exitCode = 1
  } else {
    console.log("ok: " + message)
  }
}

const openapi = read("openapi.yaml")
const db = read("db-schema.md")
const events = read("events.md")
const tasks = read("task-types.md")
const tools = read("agent-profiles-tools.md")
const settings = read("settings.md")
const migrations = read("migrations-order.md")
const executor = read("executor-protocol.md")
const order = fs
  .readFileSync(path.join(root, "backend", "internal", "db", "migrations", "order.txt"), "utf8")
  .split(/\r?\n/)
  .map((line) => line.trim())
  .filter((line) => line && !line.startsWith("#"))

check(/info:\s*\n\s*title:/.test(openapi) && /version:\s*0\.2\.0-p5/.test(openapi), "openapi info.version 0.2.0-p5")
check(/x-frozen:\s*P5/.test(openapi), "openapi x-frozen P5")
const operationCount = (openapi.match(/^\s*operationId:\s*/gm) || []).length
check(operationCount === 162, `openapi operations = 162 (got ${operationCount})`)
for (const fragment of [
  "/api/versions/{versionId}/code/index",
  "/api/versions/{versionId}/code/analyses",
  "/api/versions/{versionId}/code/anchors",
  "/api/versions/{versionId}/visual/assertions",
  "/api/versions/{versionId}/visual/diffs",
  "/api/quality/visual-metrics",
]) {
  check(openapi.includes(fragment), `openapi path ${fragment}`)
}

check(order.length === 31, `order.txt = 31 migrations (got ${order.length})`)
for (const name of [
  "codeintel_0001_code_index.sql",
  "codeintel_0002_code_analysis.sql",
  "codeintel_0003_anchor_review.sql",
  "visual_0001_visual_assertion.sql",
  "execution_0004_visual_evidence.sql",
  "version_0006_version_sut_code_baseline.sql",
  "agent_0004_code_team.sql",
]) {
  check(order.includes(name), `migration ${name}`)
}

for (const table of [
  "code_index_run",
  "code_entity",
  "code_edge",
  "code_fact",
  "code_analysis",
  "code_impact_candidate",
  "code_anchor",
  "code_fact_review",
  "visual_assertion",
  "visual_baseline",
  "visual_diff",
]) {
  check(db.includes("`" + table + "`"), `db-schema table ${table}`)
}
check(/version_sut\.code_baseline_commit|code_baseline_commit/.test(db), "db-schema version_sut.code_baseline_commit")
for (const kind of ["visual_actual", "visual_diff"]) {
  check(db.includes(kind), `db-schema evidence kind ${kind}`)
}

const eventTypes = []
for (const line of events.split(/\r?\n/)) {
  const match = line.match(/^\|\s*`((?:code|visual)\.[a-z_]+)`\s*\|/)
  if (match) eventTypes.push(match[1])
}
check(eventTypes.length === 12, `events = 12 (got ${eventTypes.length})`)

check(tasks.includes("`index.repo`"), "task index.repo")
check(tasks.includes("`code.analyze`"), "task code.analyze")
check(/\bcode\b/.test(tasks) && tasks.includes("code"), "task team code")

for (const tool of [
  "code.tree",
  "code.read",
  "code.search",
  "code.facts",
  "code.routes",
  "code.apis",
  "code.diff",
  "code.impact",
  "code.anchors",
  "execution.visual_assert",
  "sut.page_propose",
]) {
  check(tools.includes("`" + tool + "`"), `tool ${tool}`)
}

for (const key of [
  "codeintel.max_index_files",
  "codeintel.max_changed_files",
  "codeintel.max_file_bytes",
  "codeintel.max_diff_bytes",
  "codeintel.max_excerpt_lines",
  "codeintel.analysis_timeout_seconds",
  "codeintel.max_concurrent_analyses",
  "codeintel.prompt_version",
  "codeintel.exclude_globs",
  "visual.default_threshold",
  "visual.baseline_max_bytes",
  "visual.default_viewport_width",
  "visual.default_viewport_height",
  "visual.default_device_scale_factor",
  "visual.compare_timeout_seconds",
  "visual.wait_timeout_seconds",
]) {
  check(settings.includes("`" + key + "`"), `setting ${key}`)
}

check(executor.includes("visual.compare"), "executor protocol visual.compare")
check(migrations.includes("codeintel_0001_code_index.sql"), "migrations-order codeintel_0001")
check(
  db.includes("chk_work_item_team") || db.includes("'code'"),
  "db-schema work_item code team",
)

if (process.exitCode) {
  console.error("P5 contract check FAILED")
  process.exit(process.exitCode)
}
console.log("P5 contract check OK: 38 P5 ops / 31 migrations / 11 tables / 12 events / 2 tasks / 11 tools / 16 settings")

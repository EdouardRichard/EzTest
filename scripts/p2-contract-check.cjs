#!/usr/bin/env node
// P2 contract presence check. Parses the OpenAPI YAML and asserts the P2
// increments exist; the doc-level sections are checked by plain string search.
// Exits non-zero with a list of missing items. Intended for T1/T20 evidence.
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const contractDir = path.join(root, "docs", "version-V0.2", "contracts")
const yaml = require(path.join(root, "frontend", "node_modules", "yaml"))

const failures = []
const check = (ok, message) => {
  if (!ok) failures.push(message)
}
const read = (name) => fs.readFileSync(path.join(contractDir, name), "utf8")

const openapi = yaml.parse(read("openapi.yaml"))
check(openapi && typeof openapi === "object", "openapi parses to an object")
// P3 flips the root marker to x-frozen: P3 and removes x-draft. The P2 check is
// intentionally forward-compatible: it only proves the P2 increments are still
// present after later phases add their own (P3-D-37).
check(
  openapi["x-frozen"] === "P2" || openapi["x-frozen"] === "P3" || openapi["x-draft"] === "P3",
  "openapi root must be frozen at P2/P3 or draft P3",
)

const paths = [
  "/api/versions/{id}/design/graph",
  "/api/versions/{id}/design/overview",
  "/api/versions/{id}/design/cases/{caseId}",
  "/api/versions/{id}/design/confirmations",
  "/api/versions/{id}/exports",
  "/api/exports/{exportId}",
  "/api/exports/{exportId}/download",
]
for (const p of paths) check(Boolean(openapi.paths?.[p]), `missing path ${p}`)

const schemas = [
  "DesignGraphView",
  "DesignCaseDetail",
  "DesignOverview",
  "CaseConfirmationRequest",
  "CaseConfirmationResult",
  "ExportRequest",
  "ExportView",
  "CaseCounts",
]
for (const s of schemas) check(Boolean(openapi.components?.schemas?.[s]), `missing schema ${s}`)

const versionView = openapi.components?.schemas?.Version
check(Boolean(versionView?.properties?.pending_design_count), "Version.pending_design_count missing")
check(Boolean(versionView?.properties?.case_counts), "Version.case_counts missing")
const createConversation = openapi.components?.schemas?.CreateConversationRequest
check(
  Array.isArray(createConversation?.properties?.kind?.enum) &&
    createConversation.properties.kind.enum.includes("design"),
  "CreateConversationRequest.kind must allow design",
)

const events = read("events.md")
for (const name of [
  "design.round_started",
  "design.scope_activated",
  "design.graph_delta",
  "design.case_version_created",
  "design.case_version_stale",
  "design.case_version_superseded",
  "design.case_confirmed",
  "design.review_submitted",
  "design.export_ready",
]) {
  check(events.includes(name), `events.md missing ${name}`)
}

const dbSchema = read("db-schema.md")
for (const name of [
  "case_scenario",
  "case_entity",
  "case_version",
  "case_step",
  "case_assertion",
  "case_source",
  "case_confirmation",
  "design_round",
  "design_scope",
  "export_artifact",
  "pk_case_scenario",
  "uk_case_version_case_revision",
  "idx_export_artifact_version",
]) {
  check(dbSchema.includes(name), `db-schema.md missing ${name}`)
}

const order = read("migrations-order.md")
check(order.includes("design_0001_case_model.sql"), "migrations-order missing design migration")
check(order.includes("storage_0002_export_artifact.sql"), "migrations-order missing export migration")

const taskTypes = read("task-types.md")
for (const name of ["design.round", "design.requirement", "design.gap", "design.critic", "design.data_plan"]) {
  check(taskTypes.includes(name), `task-types.md missing ${name}`)
}

const profiles = read("agent-profiles-tools.md")
for (const name of ["design_host", "design_planner", "case_author", "design_critic", "data_planner"]) {
  check(profiles.includes(name), `agent-profiles-tools.md missing profile ${name}`)
}
for (const name of ["case.graph_propose", "design.graph_read", "design.review_submit", "design.data_plan_submit"]) {
  check(profiles.includes(name), `agent-profiles-tools.md missing tool ${name}`)
}
check(!profiles.includes("不注册 `case.confirm`") === false, "agent-profiles-tools.md must state case.confirm is not registered")

const settings = read("settings.md")
check(settings.includes("4b. P2 使用说明"), "settings.md missing P2 section")

if (failures.length > 0) {
  console.error("P2 contract check FAILED:")
  for (const failure of failures) console.error(" -", failure)
  process.exit(1)
}
const pathCount = Object.keys(openapi.paths).length
const schemaCount = Object.keys(openapi.components.schemas).length
console.log(`P2 contract check OK: ${pathCount} paths, ${schemaCount} schemas, P2 increments present`)

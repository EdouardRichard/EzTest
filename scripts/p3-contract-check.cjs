#!/usr/bin/env node
// P3 contract presence check.
//
// Two modes:
//   --draft : stage-internal mode. Asserts the P3 increments are present while
//             openapi.yaml still carries a draft/frozen marker at P3 or later.
//   default : frozen mode. Asserts the P3 increments are present and the root is
//             frozen at P3 or later (P4+ keeps this check forward-compatible).
//
// Forward-compatible by design (P4-D-10): every P3 increment is asserted item
// by item, but P4+ totals (migrations, events, settings) are never hard-coded.
// The OpenAPI document is parsed with the frontend's `yaml` dependency; the
// markdown contracts are checked with plain string searches and small parsers.
// Exits non-zero with a list of missing items.
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const contractDir = path.join(root, "docs", "version-V0.2", "contracts")
const migrationsDir = path.join(root, "backend", "internal", "db", "migrations")
const yaml = require(path.join(root, "frontend", "node_modules", "yaml"))

const draftMode = process.argv.includes("--draft")
const phaseOf = (value) => {
  const match = /^P(\d+)$/.exec(value ?? "")
  return match ? Number(match[1]) : 0
}

const failures = []
const check = (ok, message) => {
  if (!ok) failures.push(message)
}
const readContract = (name) => fs.readFileSync(path.join(contractDir, name), "utf8")
const read = (absolute) => fs.readFileSync(absolute, "utf8")

// ---------------------------------------------------------------------------
// OpenAPI: identity, freeze marker, tags, 22 P3 paths, P3 schemas.
// ---------------------------------------------------------------------------
let openapi = null
try {
  openapi = yaml.parse(readContract("openapi.yaml"))
} catch (error) {
  failures.push(`openapi.yaml does not parse: ${error.message}`)
}
check(openapi && typeof openapi === "object", "openapi parses to an object")
if (openapi && typeof openapi === "object") {
  const versionMatch = /^0\.2\.0-p(\d+)$/.exec(openapi.info?.version ?? "")
  check(
    versionMatch !== null && Number(versionMatch[1]) >= 3,
    "openapi info.version must be 0.2.0-p3 or later (P3 increment present)",
  )
  if (draftMode) {
    check(
      phaseOf(openapi["x-draft"]) >= 3 || phaseOf(openapi["x-frozen"]) >= 3,
      "draft mode: openapi root must carry x-draft/x-frozen at P3 or later",
    )
  } else {
    check(phaseOf(openapi["x-frozen"]) >= 3, "frozen mode: openapi root must be frozen at P3 or later")
    check(openapi["x-draft"] === undefined, "frozen mode: openapi root must not keep x-draft")
  }

  const tagNames = (openapi.tags ?? []).map((tag) => tag.name)
  for (const tag of ["Execution", "Evidence", "HumanRequests"]) {
    check(tagNames.includes(tag), `openapi missing tag ${tag}`)
  }

  const p3Paths = [
    ["/api/versions/{id}/execution/overview", "get", "getExecutionOverview"],
    ["/api/versions/{id}/execution/intents", "get", "listExecutionIntents"],
    ["/api/versions/{id}/execution/intents", "post", "createExecutionIntent"],
    ["/api/versions/{id}/execution/intents/{intentId}/cancel", "post", "cancelExecutionIntent"],
    ["/api/versions/{id}/waves", "get", "listRunWaves"],
    ["/api/versions/{id}/waves/{waveId}", "get", "getRunWave"],
    ["/api/versions/{id}/waves/{waveId}/pause", "post", "pauseRunWave"],
    ["/api/versions/{id}/waves/{waveId}/resume", "post", "resumeRunWave"],
    ["/api/versions/{id}/waves/{waveId}/cancel", "post", "cancelRunWave"],
    ["/api/versions/{id}/waves/{waveId}/case-runs", "get", "listWaveCaseRuns"],
    ["/api/versions/{id}/case-runs/{caseRunId}", "get", "getCaseRun"],
    ["/api/versions/{id}/case-runs/{caseRunId}/pause", "post", "pauseCaseRun"],
    ["/api/versions/{id}/case-runs/{caseRunId}/resume", "post", "resumeCaseRun"],
    ["/api/versions/{id}/evidence", "get", "listEvidence"],
    ["/api/versions/{id}/evidence/{evidenceId}", "get", "getEvidence"],
    ["/api/versions/{id}/evidence/{evidenceId}/content", "get", "downloadEvidence"],
    ["/api/versions/{id}/human-requests", "get", "listHumanRequests"],
    ["/api/human-requests/{humanRequestId}/resolve", "post", "resolveHumanRequest"],
    ["/api/human-requests/{humanRequestId}/cancel", "post", "cancelHumanRequest"],
    ["/api/admin/executors", "get", "listExecutors"],
    ["/api/admin/executors/{executorId}/drain", "post", "drainExecutor"],
    ["/api/admin/executors/{executorId}/activate", "post", "activateExecutor"],
  ]
  check(p3Paths.length === 22, "p3 path table must list 22 paths")
  for (const [route, method, operationId] of p3Paths) {
    const operation = openapi.paths?.[route]?.[method]
    check(Boolean(operation), `openapi missing ${method.toUpperCase()} ${route}`)
    check(operation?.operationId === operationId, `${method.toUpperCase()} ${route} operationId must be ${operationId}`)
  }

  // P2 increments must stay untouched.
  for (const route of [
    "/api/versions/{id}/design/graph",
    "/api/versions/{id}/design/overview",
    "/api/versions/{id}/design/cases/{caseId}",
    "/api/versions/{id}/design/confirmations",
    "/api/versions/{id}/exports",
    "/api/exports/{exportId}",
    "/api/exports/{exportId}/download",
  ]) {
    check(Boolean(openapi.paths?.[route]), `P2 path missing from openapi: ${route}`)
  }

  const p3Schemas = [
    "ExecutionOverview",
    "ExecutionQuotas",
    "ExecutorStatusView",
    "ExecutorView",
    "RunWaveView",
    "WaveCounts",
    "WaveSUTCounts",
    "CaseRunCounts",
    "CaseRunView",
    "CaseRunSummaryView",
    "CaseRunStepView",
    "CaseRunAssertionView",
    "CaseRunDetail",
    "EvidenceView",
    "HumanRequestView",
    "HumanRequestPayload",
    "HumanRequestResponseView",
    "IntentView",
    "CreateIntentRequest",
    "PauseWaveRequest",
    "PauseCaseRunRequest",
    "ResolveHumanRequestRequest",
  ]
  for (const schema of p3Schemas) {
    check(Boolean(openapi.components?.schemas?.[schema]), `openapi missing schema ${schema}`)
  }
  for (const schema of ["ListEnvelope", "ErrorEnvelope", "CaseConfirmationRequest", "ExportView"]) {
    check(Boolean(openapi.components?.schemas?.[schema]), `P0/P2 schema missing: ${schema}`)
  }

  // Key enum/structure spot checks (ID-as-string, view shape).
  const runWave = openapi.components?.schemas?.RunWaveView
  check(
    JSON.stringify(runWave?.properties?.status?.enum ?? []) === JSON.stringify(["planned", "running", "paused", "done", "failed", "cancelled"]),
    "RunWaveView.status enum mismatch",
  )
  const caseRun = openapi.components?.schemas?.CaseRunView
  const failureClasses = caseRun?.properties?.failure_class?.enum ?? []
  check(
    ["", "application", "environment", "session", "model", "automation", "timeout", "executor_lost", "blocked", "unknown"].every(
      (value) => failureClasses.includes(value),
    ),
    "CaseRunView.failure_class enum mismatch",
  )
  const evidence = openapi.components?.schemas?.EvidenceView
  check(
    ["screenshot", "snapshot", "console", "network", "http", "db", "download", "text"].every((value) =>
      (evidence?.properties?.kind?.enum ?? []).includes(value),
    ),
    "EvidenceView.kind enum mismatch",
  )
  check(
    !evidence?.properties?.content_ref_id && !evidence?.properties?.path,
    "EvidenceView must not expose content_ref_id/path",
  )
  const human = openapi.components?.schemas?.HumanRequestView
  check(
    ["open", "resolved", "cancelled", "expired"].every((value) => (human?.properties?.status?.enum ?? []).includes(value)),
    "HumanRequestView.status enum mismatch",
  )
  check(
    openapi.components?.schemas?.CaseRunSummaryView?.properties?.verdict_reason !== undefined,
    "CaseRunSummaryView.verdict_reason missing",
  )
  const openapiText = readContract("openapi.yaml")
  check(openapiText.includes("evidence_too_large"), "openapi must document error code evidence_too_large")
  check(openapiText.includes("executor_unavailable"), "openapi must document error code executor_unavailable")
}

// ---------------------------------------------------------------------------
// Events: §12 must contain exactly the 20 P3 event types.
// ---------------------------------------------------------------------------
const p3Events = [
  "execution.intent_created",
  "execution.intent_cancelled",
  "execution.intent_superseded",
  "wave.created",
  "wave.started",
  "wave.paused",
  "wave.resumed",
  "wave.finished",
  "case_run.started",
  "case_run.paused",
  "case_run.resumed",
  "case_run.finished",
  "case_run.step_finished",
  "case_run.assertion_finished",
  "evidence.created",
  "human_request.created",
  "human_request.resolved",
  "human_request.expired",
  "execution.failure_candidate",
  "execution.coverage_gap",
]
check(p3Events.length === 20, "p3 event table must list 20 events")
const eventsText = readContract("events.md")
const eventsSectionStart = eventsText.search(/^## 12\. P3 增量事件/m)
const eventsSectionFrom = eventsSectionStart >= 0 ? eventsText.slice(eventsSectionStart) : ""
const nextEventsHeading = eventsSectionFrom.slice(1).search(/^## /m)
const eventsSection = nextEventsHeading >= 0 ? eventsSectionFrom.slice(0, nextEventsHeading + 1) : eventsSectionFrom
check(eventsSection.length > 0, "events.md missing §12 P3 section")
check(eventsSection.includes("frozen: P3"), "events.md §12 must be marked frozen: P3")
const eventRows = [...eventsSection.matchAll(/^\| `([a-z_.]+)` \|/gm)].map((match) => match[1])
for (const event of p3Events) {
  check(eventRows.includes(event), `events.md §12 missing event ${event}`)
}
check(eventRows.length >= p3Events.length, `events.md §12 must keep all ${p3Events.length} P3 events, found ${eventRows.length}`)
check(eventsSection.includes("case_run.step_finished"), "events.md missing case_run.step_finished freeze note")
check(eventsSection.includes("executor"), "events.md must state no executor.* business events (P3-D-30)")

// ---------------------------------------------------------------------------
// db-schema: §9 with the 10 P3 tables and their frozen names.
// ---------------------------------------------------------------------------
const dbTables = [
  "execution_intent",
  "run_wave",
  "case_run",
  "case_run_sut_context",
  "step_result",
  "assertion_result",
  "evidence",
  "evidence_ref",
  "executor_instance",
  "human_request",
]
check(dbTables.length === 10, "p3 table table must list 10 tables")
const dbSchemaText = readContract("db-schema.md")
check(/^## 9\. P3 增量表/m.test(dbSchemaText), "db-schema.md missing §9 P3 section")
check(/frozen: P3/.test(dbSchemaText.slice(dbSchemaText.search(/^## 9\. P3 增量表/m))), "db-schema.md §9 must be marked frozen: P3")
for (const table of dbTables) {
  check(dbSchemaText.includes(`\`${table}\``) || dbSchemaText.includes(table), `db-schema.md missing table ${table}`)
}
for (const name of [
  "pk_execution_intent",
  "pk_run_wave",
  "pk_case_run",
  "pk_case_run_sut_context",
  "pk_step_result",
  "pk_assertion_result",
  "pk_evidence",
  "pk_evidence_ref",
  "pk_executor_instance",
  "pk_human_request",
  "uk_execution_intent_active",
  "uk_run_wave_active",
  "uk_case_run_wave_intent",
  "uk_case_run_sut_context",
  "uk_step_result_run_seq",
  "uk_assertion_result_run_step_assertion",
  "uk_evidence_ref_owner",
  "uk_executor_instance_endpoint",
  "idx_execution_intent_version_status",
  "idx_execution_intent_case_version",
  "idx_execution_intent_dedupe",
  "idx_run_wave_version",
  "idx_run_wave_status",
  "idx_case_run_wave",
  "idx_case_run_intent",
  "idx_case_run_case_version",
  "idx_case_run_lease",
  "idx_case_run_sut_context_run",
  "idx_case_run_sut_context_context",
  "idx_step_result_run",
  "idx_assertion_result_run",
  "idx_evidence_case_run",
  "idx_evidence_version_sut",
  "idx_evidence_content_ref",
  "idx_evidence_kind",
  "idx_evidence_ref_owner",
  "idx_executor_instance_status",
  "idx_human_request_version_status",
  "idx_human_request_case_run",
  "idx_human_request_expiry",
]) {
  check(dbSchemaText.includes(name), `db-schema.md missing ${name}`)
}
check(dbSchemaText.includes("paths.evidence_dir"), "db-schema.md must record the paths.evidence_dir P3 exception")

// ---------------------------------------------------------------------------
// Migrations: the 3 P3 files must stay present and ordered, and the P3 tail
// must still be applied. P4 re-pins the Go test tail to the P4 four, so the
// tail proof is anchored on order.txt (the applied order) and on the migration
// tests expecting every ordered file; the totals are derived, not hard-coded.
// ---------------------------------------------------------------------------
const p3Migrations = [
  "execution_0001_intent_wave.sql",
  "execution_0002_result_evidence.sql",
  "execution_0003_executor_human.sql",
]
for (const file of p3Migrations) {
  check(fs.existsSync(path.join(migrationsDir, file)), `missing migration file ${file}`)
}
const p3Tail = p3Migrations[p3Migrations.length - 1]
const orderLines = read(path.join(migrationsDir, "order.txt"))
  .split(/\r?\n/)
  .map((line) => line.trim())
  .filter((line) => line.length > 0 && !line.startsWith("#"))
check(p3Migrations.every((file) => orderLines.includes(file)), "order.txt missing P3 execution migrations")
check(orderLines.includes(p3Tail), `order.txt must still apply P3 tail ${p3Tail}`)
check(orderLines.length >= 20, `order.txt must keep at least 20 migrations, found ${orderLines.length}`)
const p3OrderIndexes = p3Migrations.map((file) => orderLines.indexOf(file))
check(
  p3OrderIndexes[0] >= 0 &&
    p3OrderIndexes[1] === p3OrderIndexes[0] + 1 &&
    p3OrderIndexes[2] === p3OrderIndexes[1] + 1,
  "order.txt must keep the 3 execution migrations consecutive and in order",
)
const migrateUnit = read(path.join(root, "backend", "internal", "db", "migrate_test.go"))
const unitCount = /len\(migrations\)\s*!=\s*(\d+)/.exec(migrateUnit)
check(
  unitCount !== null && Number(unitCount[1]) >= 20,
  "migrate_test.go must expect at least 20 migrations (P3 baseline preserved)",
)
check(
  unitCount !== null && Number(unitCount[1]) === orderLines.length,
  `migrate_test.go must expect every migration in order.txt (${orderLines.length})`,
)
check(migrateUnit.includes("orderedMigrations"), "migrate_test.go must keep the migration-order test")
const migrateIntegration = read(path.join(root, "backend", "internal", "db", "migrate_integration_test.go"))
const integrationCount = /len\(applied\)\s*!=\s*(\d+)/.exec(migrateIntegration)
check(
  integrationCount !== null && Number(integrationCount[1]) >= 20,
  "migrate_integration_test.go must expect at least 20 migrations (P3 baseline preserved)",
)
check(
  integrationCount !== null && Number(integrationCount[1]) === orderLines.length,
  `migrate_integration_test.go must expect every migration in order.txt (${orderLines.length})`,
)
check(migrateIntegration.includes("TestP3ExecutionTables"), "migrate_integration_test.go missing TestP3ExecutionTables")

const migrationsOrderText = readContract("migrations-order.md")
check(/^## 5c\. P3 增量迁移/m.test(migrationsOrderText), "migrations-order.md missing §5c")
check(migrationsOrderText.includes("frozen: P3"), "migrations-order.md §5c must be marked frozen: P3")
for (const file of p3Migrations) {
  check(migrationsOrderText.includes(file), `migrations-order.md missing ${file}`)
}

// ---------------------------------------------------------------------------
// Task types: §5 with the 3 P3 types, and §4 no longer lists them.
// ---------------------------------------------------------------------------
const taskTypesText = readContract("task-types.md")
const section5 = taskTypesText.slice(taskTypesText.search(/^## 5\. P3 任务类型/m))
check(section5.length > 0, "task-types.md missing §5 P3 section")
check(section5.includes("frozen: P3"), "task-types.md §5 must be marked frozen: P3")
for (const taskType of ["execute.case", "analyze.failure", "analyze.coverage"]) {
  check(section5.includes(taskType), `task-types.md §5 missing ${taskType}`)
}
const section4Start = taskTypesText.search(/^## 4\. P4\+ 保留类型/m)
const section4 = section4Start >= 0 ? taskTypesText.slice(section4Start, taskTypesText.search(/^## 5\. P3 任务类型/m)) : ""
check(section4.length > 0, "task-types.md §4 must be renamed to P4+ reserved types")
for (const taskType of ["execute.case", "analyze.failure", "analyze.coverage"]) {
  check(!section4.includes(taskType), `task-types.md §4 must not list implemented type ${taskType}`)
}
check(taskTypesText.includes("`requirement.close`"), "task-types.md must keep requirement.close (reserved or implemented)")

// ---------------------------------------------------------------------------
// Profiles/tools: §6 with 3 profiles + P3 tools, §4b keeps only reserved tools.
// ---------------------------------------------------------------------------
const profilesText = readContract("agent-profiles-tools.md")
const section6 = profilesText.slice(profilesText.search(/^## 6\. P3 Profiles/m))
check(section6.length > 0, "agent-profiles-tools.md missing §6 P3 section")
check(section6.includes("frozen: P3"), "agent-profiles-tools.md §6 must be marked frozen: P3")
for (const profile of ["case_executor", "failure_analyst", "coverage_analyst"]) {
  check(section6.includes(profile), `agent-profiles-tools.md §6 missing profile ${profile}`)
}
for (const tool of [
  "browser.goto",
  "browser.snapshot",
  "browser.click",
  "browser.fill",
  "browser.press",
  "browser.wait_for",
  "browser.screenshot",
  "browser.console",
  "browser.network",
  "browser.download",
  "browser.storage_state",
  "http.request",
  "db.query",
  "database.schema_read",
  "account.credentials",
  "execution.step_record",
  "execution.assertion_record",
  "execution.evidence",
  "execution.wait",
  "execution.case_finish",
  "human.request",
  "execution.result_read",
  "execution.evidence_read",
  "execution.coverage_read",
  "analysis.failure_submit",
  "analysis.coverage_submit",
  "context.task_pack",
]) {
  check(section6.includes(tool), `agent-profiles-tools.md §6 missing tool ${tool}`)
}
check(section6.includes("dispatch_epoch"), "agent-profiles-tools.md §6 must document dispatch_epoch scope")
check(section6.includes("dispatch_token"), "agent-profiles-tools.md §6 must document dispatch_token scope")
check(section6.includes("version_id"), "agent-profiles-tools.md §6 must document scope injection")
const section4b = profilesText.slice(profilesText.search(/^## 4b\./m), profilesText.search(/^## 5\./m))
check(section4b.length > 0, "agent-profiles-tools.md §4b missing")
for (const enabled of ["browser.*", "`human.request`", "`db.query`", "`execution.*`", "`account.credentials`"]) {
  check(!section4b.includes(enabled), `agent-profiles-tools.md §4b must move enabled tool ${enabled} to §6`)
}

// ---------------------------------------------------------------------------
// Settings: the P0/P3 keys must stay present item by item (P4 adds chain.*;
// the total is intentionally not hard-coded).
// ---------------------------------------------------------------------------
const settingsText = readContract("settings.md")
const settingsKeys = [...settingsText.matchAll(/^\|\s*\d+\s*\|\s*`([^`]+)`\s*\|/gm)].map((match) => match[1])
check(settingsKeys.length >= 49, `settings.md must keep at least 49 keys, found ${settingsKeys.length}`)
for (const key of [
  "runtime.snowflake_node_id",
  "paths.data_root",
  "paths.attachment_dir",
  "paths.evidence_dir",
  "paths.report_dir",
  "paths.export_dir",
  "execution.global_concurrency",
  "execution.per_sut_concurrency",
  "execution.cross_system_concurrency",
  "execution.executor_url",
  "execution.evidence_max_bytes",
  "execution.case_timeout_seconds",
  "agent.max_turns",
  "agent.run_soft_timeout_seconds",
  "agent.run_hard_timeout_seconds",
  "agent.max_concurrent_runs",
  "agent.tool_result_max_bytes",
  "llm.request_timeout_seconds",
  "llm.max_retries",
  "context.window_tokens",
  "context.output_reserve_tokens",
  "context.compact_threshold",
  "context.keep_recent_messages",
  "team_room.digest_every_n_posts",
  "team_room.digest_interval_minutes",
  "team_room.digest_keep_versions",
  "retention.temp_hours",
  "retention.replaced_export_days",
  "retention.unreferenced_content_days",
  "retention.gc_grace_hours",
  "retention.gc_dry_run",
  "report.template_version",
  "report.render_pdf",
  "report.section_summary",
  "report.section_results",
  "report.section_issues",
  "report.section_coverage",
  "report.section_evidence",
  "knowledge.injection_budget_tokens",
  "knowledge.review_required",
  "security.audit_retention_days",
  "security.mask_secrets",
  "feature.auto_gap_design",
  "feature.experimental",
  "execution.executor_token",
  "execution.executor_heartbeat_seconds",
  "execution.executor_offline_seconds",
  "execution.executor_context_ttl_seconds",
  "execution.human_request_timeout_seconds",
]) {
  check(settingsKeys.includes(key), `settings.md missing key ${key}`)
}
for (const key of [
  "execution.executor_token",
  "execution.executor_heartbeat_seconds",
  "execution.executor_offline_seconds",
  "execution.executor_context_ttl_seconds",
  "execution.human_request_timeout_seconds",
]) {
  check(settingsKeys.includes(key), `settings.md missing key ${key}`)
}
check(/^## 4c\. P3 使用说明/m.test(settingsText), "settings.md missing §4c P3 section")
check(settingsText.includes("frozen: P3"), "settings.md §4c must be marked frozen: P3")
check(settingsText.includes("is_secret"), "settings.md must document executor_token secret semantics")
check(settingsText.includes("paths.evidence_dir"), "settings.md must record the paths.evidence_dir P3 exception")

// ---------------------------------------------------------------------------
// Executor protocol + contracts README.
// ---------------------------------------------------------------------------
const protocolPath = path.join(contractDir, "executor-protocol.md")
check(fs.existsSync(protocolPath), "missing contracts/executor-protocol.md")
if (fs.existsSync(protocolPath)) {
  const protocol = read(protocolPath)
  for (const phrase of [
    "frozen: P3",
    "GET /healthz",
    "POST /v1/contexts",
    "GET /v1/contexts",
    "/v1/contexts/{id}/actions",
    "/v1/contexts/{id}/cancel",
    "DELETE /v1/contexts/{id}",
    "Authorization: Bearer",
    "EZTEST_EXECUTOR_TOKEN",
    "execution.executor_token",
    "execution.executor_heartbeat_seconds",
    "execution.executor_offline_seconds",
    "metadata",
    "allowedHosts",
    "maxContexts",
    "pull",
    "draining",
    "execution.wait",
    "content_ref",
    "P7",
  ]) {
    check(protocol.includes(phrase), `executor-protocol.md missing ${phrase}`)
  }
}
const readmeText = readContract("README.md")
check(readmeText.includes("executor-protocol.md"), "contracts/README.md missing executor-protocol.md row")
check(/P3 契约状态|P3 冻结状态/.test(readmeText), "contracts/README.md missing P3 status paragraph")
check(readmeText.includes("0.2.0-p3"), "contracts/README.md missing openapi version 0.2.0-p3")

// ---------------------------------------------------------------------------
// Report.
// ---------------------------------------------------------------------------
if (failures.length > 0) {
  console.error(`P3 contract check FAILED (${draftMode ? "draft" : "frozen"} mode):`)
  for (const failure of failures) console.error(" -", failure)
  process.exit(1)
}

const pathCount = Object.keys(openapi.paths).length
const operationCount = Object.values(openapi.paths).reduce((total, item) => total + Object.keys(item).length, 0)
const schemaCount = Object.keys(openapi.components.schemas).length
const settingsCount = settingsKeys.length
const migrationCount = orderLines.length
console.log(
  `P3 contract check OK (${draftMode ? "draft" : "frozen"} mode): ` +
    `${pathCount} paths / ${operationCount} operations / ${schemaCount} schemas; ` +
    `${p3Events.length} events; ${dbTables.length} tables; ${migrationCount} migrations; ` +
    `3 tasks; 3 profiles; ${settingsCount} settings keys; executor-protocol present`,
)

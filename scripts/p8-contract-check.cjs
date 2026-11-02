#!/usr/bin/env node
/*
 * P8 contract gate: verifies the frozen P8 contract increment exists and is
 * additive against P0-P7.
 *
 * P8 (hardening + acceptance) freezes:
 *   - openapi 0.2.0-p8 / x-frozen: P8 / 162 paths / 200 operations / 264 schemas
 *     (no new path/operation/schema; only additive response fields + 429 note)
 *   - InstanceView.active_requests/active_work + readiness inflight_requests/
 *     inflight_work (integer, default 0)
 *   - error code rate_limited + 429 TooManyRequests response (Retry-After)
 *   - migrations #41/#42 (api_instance in-flight columns; case_run_sut_context
 *     executor_id), order.txt 42 lines, still 85 business tables
 *   - settings 83 -> 85 (security.rate_limit_per_minute, retention.event_days)
 *   - no new event type (118) + audit action retention.cleanup
 *   - 19 task types / 14 profiles unchanged
 *   - executor-protocol §12.3 per-executor dispatch promoted to P8 implemented
 *     (wire protocol v1.2 unchanged)
 *
 * Totals below are the frozen P8 totals; a later phase should relax them to
 * ">=" instead of editing this file.
 */
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const contracts = path.join(root, "docs", "version-V0.2", "contracts")
const migrationsDir = path.join(root, "backend", "internal", "db", "migrations")
const catalogPath = path.join(root, "backend", "internal", "settings", "application", "catalog.go")
const read = (name) => fs.readFileSync(path.join(contracts, name), "utf8").replace(/\r\n/g, "\n")
const yaml = require(path.join(root, "frontend", "node_modules", "yaml"))

let failures = 0
const check = (ok, message) => {
  if (!ok) {
    failures++
    console.error("FAIL: " + message)
  } else {
    console.log("ok: " + message)
  }
}
const countMatches = (text, pattern) => (text.match(pattern) || []).length
const phaseOf = (value) => {
  const match = /^P(\d+)$/.exec(value ?? "")
  return match ? Number(match[1]) : 0
}

// Count event types from the event-type tables. Rows may group siblings with a
// shorthand suffix (e.g. `resource.sut.created` / `.updated` / `.deleted`), and
// audit-action tables are not events.
const countEventTypes = (text) => {
  const types = new Set()
  let heading = ""
  for (const line of text.split("\n")) {
    const headingMatch = line.match(/^#{2,3} (.+)$/)
    if (headingMatch) {
      heading = headingMatch[1]
      continue
    }
    if (heading.includes("审计 action")) continue
    const rowMatch = line.match(/^\|\s*([^|]+)\|/)
    if (!rowMatch) continue
    let prefix = ""
    for (const token of rowMatch[1].matchAll(/`(\.?[a-z][a-z0-9_.]*)`/g)) {
      let value = token[1]
      if (value.startsWith(".")) {
        if (!prefix) continue
        value = prefix + value
      } else {
        const dot = value.lastIndexOf(".")
        prefix = dot > 0 ? value.slice(0, dot) : ""
      }
      if (/^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$/.test(value)) types.add(value)
    }
  }
  return types
}

// Profiles seeded by the P1-P4 "Profiles（启动幂等 seed" tables; later whitelist
// increment tables are not additional profiles.
const countSeedProfiles = (text) => {
  let inSeedTable = false
  let count = 0
  const names = []
  for (const line of text.split("\n")) {
    const heading = line.match(/^#{2,3} (.+)$/)
    if (heading) {
      inSeedTable = heading[1].includes("Profiles（启动幂等 seed")
      continue
    }
    if (!inSeedTable) continue
    const row = line.match(/^\|\s*`([a-z_]+)`\s*\|/)
    if (row) {
      count++
      names.push(row[1])
    }
  }
  return { count, names }
}

const openapiText = read("openapi.yaml")
const db = read("db-schema.md")
const events = read("events.md")
const migrations = read("migrations-order.md")
const settings = read("settings.md")
const tasks = read("task-types.md")
const profiles = read("agent-profiles-tools.md")
const executor = read("executor-protocol.md")
const readme = read("README.md")

let openapi = null
try {
  openapi = yaml.parse(openapiText)
} catch (error) {
  failures++
  console.error("FAIL: openapi.yaml does not parse: " + error.message)
}

// ---------------------------------------------------------------------------
// OpenAPI identity, frozen totals and P8 additive fields.
// ---------------------------------------------------------------------------
if (openapi && typeof openapi === "object") {
  check(openapi.info?.version === "0.2.0-p8", `openapi info.version = 0.2.0-p8 (got ${openapi.info?.version})`)
  check(openapi["x-frozen"] === "P8", `openapi x-frozen: P8 (got ${openapi["x-frozen"]})`)
  check(openapi["x-draft"] === undefined, "openapi has no x-draft marker")
  check(!openapiText.includes("x-draft:"), "openapi text has no x-draft: marker")

  const paths = Object.keys(openapi.paths ?? {})
  const operations = []
  for (const [route, item] of Object.entries(openapi.paths ?? {})) {
    for (const [method, operation] of Object.entries(item ?? {})) {
      if (operation && operation.operationId) operations.push({ route, method, ...operation })
    }
  }
  const schemas = Object.keys(openapi.components?.schemas ?? {})
  check(paths.length === 162, `openapi paths = 162 (got ${paths.length})`)
  check(operations.length === 200, `openapi operations = 200 (got ${operations.length})`)
  check(schemas.length === 264, `openapi schemas = 264 (got ${schemas.length})`)

  const instance = openapi.components?.schemas?.InstanceView
  for (const field of ["active_requests", "active_work"]) {
    const property = instance?.properties?.[field]
    check(Boolean(property), `InstanceView.${field} present`)
    check(property?.type === "integer", `InstanceView.${field} is integer`)
    check(property?.minimum === 0, `InstanceView.${field} minimum 0`)
    check(property?.default === 0, `InstanceView.${field} default 0`)
  }
  check(
    !(instance?.required ?? []).includes("active_requests") && !(instance?.required ?? []).includes("active_work"),
    "InstanceView in-flight fields stay optional for old clients",
  )

  const readiness = openapi.components?.schemas?.ReadinessView
  for (const field of ["inflight_requests", "inflight_work"]) {
    const property = readiness?.properties?.[field]
    check(Boolean(property), `ReadinessView.${field} present`)
    check(property?.type === "integer", `ReadinessView.${field} is integer`)
    check(property?.minimum === 0, `ReadinessView.${field} minimum 0`)
    check(property?.default === 0, `ReadinessView.${field} default 0`)
  }
  check(
    !(readiness?.required ?? []).includes("inflight_requests") &&
      !(readiness?.required ?? []).includes("inflight_work"),
    "ReadinessView in-flight fields stay optional for old clients",
  )

  // Rate limit (P8-D-13/H11): error code + 429 response with Retry-After.
  const errorCode = openapi.components?.schemas?.ErrorEnvelope?.properties?.error?.properties?.code
  check(
    typeof errorCode?.description === "string" && errorCode.description.includes("rate_limited"),
    "ErrorEnvelope code description includes rate_limited",
  )
  const tooMany = openapi.components?.responses?.TooManyRequests
  check(Boolean(tooMany), "openapi response component TooManyRequests")
  check(
    String(tooMany?.description ?? "").includes("rate_limited") ||
      String(tooMany?.description ?? "").includes("限流"),
    "TooManyRequests description mentions rate_limited/限流",
  )
  check(Boolean(tooMany?.headers?.["Retry-After"]), "TooManyRequests has Retry-After header")
  check(openapiText.includes("429"), "openapi text documents 429")
  check(openapiText.includes("rate_limited"), "openapi text documents rate_limited")
  check(
    /security\.rate_limit_per_minute/.test(openapiText),
    "openapi 429 note references security.rate_limit_per_minute",
  )

  // P8 is additive only: no new path/operation/schema, only response fields/notes.
  check(
    openapiText.includes("### P8 增量（frozen"),
    "openapi has a P8 frozen increment section",
  )

  // Non-semantic stale-text fixes required by the P8 freeze.
  check(!openapiText.includes("x-frozen: P2"), "openapi has no stale x-frozen: P2")
  check(!/P3 增量（draft/.test(openapiText), "openapi P3 increment is marked frozen (not draft)")
  check(
    !/P3 execution (?:increments|schemas) \(draft/.test(openapiText),
    "openapi P3 execution comments are marked frozen (not draft)",
  )
  check(!openapiText.includes("P3 只有一个 endpoint"), "openapi removes stale 'P3 only one endpoint' wording")

  // P2-P7 increments must still be present (forward compatibility).
  for (const phase of ["P2", "P3", "P4", "P5", "P6", "P7"]) {
    check(
      new RegExp("### " + phase + " 增量（frozen").test(openapiText),
      `openapi ${phase} frozen increment section is still present`,
    )
  }
  const tagNames = (openapi.tags ?? []).map((tag) => tag.name)
  for (const tag of [
    "Execution",
    "Evidence",
    "HumanRequests",
    "Delivery",
    "Chain",
    "Code",
    "Visual",
    "Quality",
    "Search",
    "Workspace",
    "AgentCenter",
    "Knowledge",
    "Admin",
    "Internal",
  ]) {
    check(tagNames.includes(tag), `openapi P2-P7 tag ${tag} is still present`)
  }
  check(Boolean(openapi.components?.schemas?.DesignGraphView), "openapi P2 schema DesignGraphView present")
  check(Boolean(openapi.components?.schemas?.ExportView), "openapi P2 schema ExportView present")

  const p7Operations = [
    "listKnowledgeItems",
    "createKnowledgeItem",
    "getKnowledgeItem",
    "updateKnowledgeItem",
    "approveKnowledgeItem",
    "rejectKnowledgeItem",
    "revokeKnowledgeItem",
    "listKnowledgeRevisions",
    "createGCRun",
    "listGCRuns",
    "getGCRun",
    "listInstances",
    "drainInstance",
    "activateInstance",
    "getPlatformReadiness",
    "downloadReportPDF",
    "registerExecutor",
    "heartbeatExecutor",
  ]
  for (const operationId of p7Operations) {
    check(
      operations.filter((operation) => operation.operationId === operationId).length === 1,
      `openapi P7 operationId ${operationId} still present`,
    )
  }
}

// p7-api.md is folded into openapi.yaml and must not return.
check(!fs.existsSync(path.join(contracts, "p7-api.md")), "contracts/p7-api.md stays deleted (folded)")

// ---------------------------------------------------------------------------
// migrations-order.md + order.txt + migrations: #41/#42, 42 total, 85 tables.
// ---------------------------------------------------------------------------
const p8MigrationFiles = [
  "orchestration_0002_instance_inflight.sql",
  "execution_0005_case_run_sut_context_executor.sql",
]
const p7MigrationFiles = [
  "knowledge_0001_knowledge.sql",
  "storage_0003_gc.sql",
  "delivery_0004_report_pdf.sql",
  "orchestration_0001_instance.sql",
  "resource_0002_sut_family.sql",
  "delivery_0005_issue_recurrence.sql",
  "agent_0006_ops_team.sql",
]

check(/^## 10\. P8 增量迁移（frozen: P8/m.test(migrations), "migrations-order.md §10 P8 frozen")
check(/^## 9\. P7 增量迁移（frozen: P7/m.test(migrations), "migrations-order.md §9 P7 is still present")
for (const [number, file] of [
  [41, p8MigrationFiles[0]],
  [42, p8MigrationFiles[1]],
]) {
  check(migrations.includes("| " + number + " |"), `migrations-order row #${number}`)
  check(migrations.includes(file), `migrations-order migration ${file}`)
}
check(
  migrations.includes("order.txt") && /order\.txt.{0,80}42|42.{0,40}个迁移/.test(migrations),
  "migrations-order documents order.txt 42 migrations",
)
check(/不新增表|不新增业务表/.test(migrations.slice(migrations.indexOf("## 10."))), "migrations-order states P8 adds no table")
check(migrations.includes("P8 不新增"), "migrations-order states P8 adds no table (compat wording)")
check(migrations.includes("85"), "migrations-order records the 85 table total")
for (const file of p7MigrationFiles) {
  check(migrations.includes(file), `migrations-order P7 migration ${file} still present`)
}

const orderPath = path.join(migrationsDir, "order.txt")
check(fs.existsSync(orderPath), "migrations/order.txt exists")
let order = []
if (fs.existsSync(orderPath)) {
  order = fs
    .readFileSync(orderPath, "utf8")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith("#"))
  check(order.length === 42, `order.txt = 42 migrations (got ${order.length})`)
  check(new Set(order).size === order.length, "order.txt has no duplicate entries")
  check(
    JSON.stringify(order.slice(-2)) === JSON.stringify(p8MigrationFiles),
    "order.txt tail = P8 #41-#42",
  )
  check(
    JSON.stringify(order.slice(33, 40)) === JSON.stringify(p7MigrationFiles),
    "order.txt #34-#40 = P7 migrations (positions preserved)",
  )
}
const sqlFiles = fs.readdirSync(migrationsDir).filter((name) => name.endsWith(".sql"))
check(sqlFiles.length === 42, `migrations/*.sql = 42 files (got ${sqlFiles.length})`)
for (const file of p8MigrationFiles) {
  check(fs.existsSync(path.join(migrationsDir, file)), `migration file ${file}`)
}
const p8SQL = p8MigrationFiles
  .filter((file) => fs.existsSync(path.join(migrationsDir, file)))
  .map((file) => fs.readFileSync(path.join(migrationsDir, file), "utf8"))
  .join("\n")
check(countMatches(p8SQL, /CREATE TABLE /g) === 0, "P8 migrations create no table")
check(/ALTER TABLE api_instance/.test(p8SQL), "P8 migration #41 alters api_instance")
check(p8SQL.includes("active_requests") && p8SQL.includes("active_work"), "P8 #41 adds active_requests/active_work")
check(
  /active_requests[^\n]*DEFAULT 0/.test(p8SQL) && /active_work[^\n]*DEFAULT 0/.test(p8SQL),
  "P8 #41 in-flight columns default 0",
)
check(
  p8SQL.includes("ck_api_instance_active_requests") && p8SQL.includes("ck_api_instance_active_work"),
  "P8 #41 adds non-negative CHECK constraints",
)
check(/ALTER TABLE case_run_sut_context/.test(p8SQL), "P8 migration #42 alters case_run_sut_context")
check(/executor_id\s+BIGINT/.test(p8SQL), "P8 #42 adds executor_id BIGINT")
check(p8SQL.includes("ck_case_run_sut_context_executor"), "P8 #42 adds executor_id CHECK constraint")
check(!/REFERENCES\s+executor_instance/i.test(p8SQL), "P8 #42 executor_id is a logical reference (no FK)")

const tableCount = sqlFiles.reduce((sum, file) => {
  const sql = fs.readFileSync(path.join(migrationsDir, file), "utf8")
  return sum + countMatches(sql, /CREATE TABLE /g)
}, 0)
check(tableCount === 85, `business tables (CREATE TABLE count) = 85 (got ${tableCount})`)

// ---------------------------------------------------------------------------
// db-schema.md: §17 P8 columns, §5 total 85, P7 section preserved.
// ---------------------------------------------------------------------------
check(/^## 17\. P8 增量表结构（frozen: P8/m.test(db), "db-schema.md §17 P8 frozen")
check(/^## 16\. P7 增量表结构（frozen: P7/m.test(db), "db-schema.md §16 P7 is still present")
check(/^## 5\. 表归属/m.test(db), "db-schema.md §5 table ownership present")
check(db.includes("业务表合计 **85**"), "db-schema §5 states 85 business tables total")
check(db.includes("P8 不新增表") || db.includes("P8 **不新增表**"), "db-schema states P8 adds no table")
const p8DbSection = db.slice(db.indexOf("## 17."))
for (const needle of [
  "active_requests",
  "active_work",
  "ck_api_instance_active_requests",
  "ck_api_instance_active_work",
  "executor_id",
  "ck_case_run_sut_context_executor",
  "#41",
  "#42",
]) {
  check(p8DbSection.includes(needle), `db-schema §17 contains ${needle}`)
}
check(
  p8DbSection.includes("逻辑引用") && p8DbSection.includes("executor_instance"),
  "db-schema §17 executor_id is a logical reference to executor_instance",
)

// ---------------------------------------------------------------------------
// settings.md + catalog.go: 85 settings (83 -> 85), §4h and repaired counts.
// ---------------------------------------------------------------------------
const section = (text, startMarker, endMarker) => {
  const start = text.indexOf(startMarker)
  if (start < 0) return ""
  const end = endMarker ? text.indexOf(endMarker, start + startMarker.length) : -1
  return end < 0 ? text.slice(start) : text.slice(start, end)
}
const settingsBaseRows = countMatches(settings, /^\| \d+ \|/gm)
const settings4g = section(settings, "## 4g.", "## 4h.")
const settings4h = section(settings, "## 4h.", "## 5.")
const countKeyRows = (block) => block.split("\n").filter((line) => /^\|\s*`[^`]+`\s*\|/.test(line)).length
const settingsTotal = settingsBaseRows + countKeyRows(settings4g) + countKeyRows(settings4h)
check(settingsBaseRows === 72, `settings.md §3 base rows = 72 (got ${settingsBaseRows})`)
check(countKeyRows(settings4g) === 11, `settings.md §4g key rows = 11 (got ${countKeyRows(settings4g)})`)
check(countKeyRows(settings4h) === 2, `settings.md §4h key rows = 2 (got ${countKeyRows(settings4h)})`)
check(settingsTotal === 85, `settings.md keys = 85 (got ${settingsTotal})`)
check(/^## 4h\. P8 使用说明（frozen: P8/m.test(settings), "settings.md §4h P8 frozen")
check(/^## 4g\. P7 使用说明（frozen: P7/m.test(settings), "settings.md §4g P7 is still present")
check(settings.includes("83→85"), "settings.md 83→85")
check(settings.includes("P7 后 = **83**"), "settings.md keeps the P7 83 total")
check(settings.includes("P8 后共 **85**") || settings.includes("= **85**"), "settings.md total 85")
check(settings.includes("`security.rate_limit_per_minute`"), "settings key security.rate_limit_per_minute")
check(
  /security\.rate_limit_per_minute[^\n]*0=关闭|0=关闭[^\n]*security\.rate_limit_per_minute/.test(settings4h),
  "security.rate_limit_per_minute default 0 = disabled",
)
check(settings.includes("`retention.event_days`"), "settings key retention.event_days")
check(/retention\.event_days[^\n]*`90`/.test(settings4h), "retention.event_days default 90")
check(settings.includes("总速率=副本数×限额"), "settings.md documents total rate = replicas x limit")
check(settings.includes("P8 冻结时 85 项"), "settings.md §2 current setting count is 85")
check(!settings.includes("P3 起 49 项"), "settings.md §2 stale 49-key count removed")
check(settings.includes("消费方在启动时读取快照"), "settings.md §4g clarifies startup-snapshot consumers")
check(
  !settings.includes("11 键全部 `restart_required=false`、热加载"),
  "settings.md §4g stale 'all 11 hot-load' wording removed",
)
const eventDays = settings4h.slice(settings4h.indexOf("retention.event_days"))
check(
  /1[–-]3650/.test(eventDays),
  "retention.event_days range 1-3650",
)
const rateLimit = settings4h.slice(settings4h.indexOf("security.rate_limit_per_minute"))
check(/0[–-]60000/.test(rateLimit), "security.rate_limit_per_minute range 0-60000")

check(fs.existsSync(catalogPath), "settings catalog.go exists")
if (fs.existsSync(catalogPath)) {
  const catalog = fs.readFileSync(catalogPath, "utf8")
  const catalogKeys = countMatches(catalog, /\{Key: "/g)
  check(catalogKeys === 85, `settings catalog keys = 85 (got ${catalogKeys})`)
  check(catalog.includes('"security.rate_limit_per_minute"'), "catalog key security.rate_limit_per_minute")
  check(catalog.includes('"retention.event_days"'), "catalog key retention.event_days")
}

// ---------------------------------------------------------------------------
// events.md: 118 event types (no P8 event) + P8 audit action retention.cleanup.
// ---------------------------------------------------------------------------
const eventTypes = countEventTypes(events)
check(eventTypes.size === 118, `events.md event types = 118 (got ${eventTypes.size})`)
check(/^## 17\. P8 增量（frozen: P8/m.test(events), "events.md §17 P8 frozen")
check(/不新增事件类型|无新事件类型/.test(events.slice(events.indexOf("## 17."))), "events.md P8 adds no event type")
check(events.includes("`retention.cleanup`"), "events.md P8 audit action retention.cleanup")
const p8EventSection = events.slice(events.indexOf("## 17."))
for (const field of ["events_deleted", "audit_deleted", "cutoff"]) {
  check(p8EventSection.includes(field), `events.md retention.cleanup payload ${field}`)
}
for (const eventType of [
  "report.pdf_ready",
  "knowledge.proposed",
  "gc.completed",
  "instance.registered",
  "issue.recurred",
]) {
  check(events.includes("`" + eventType + "`"), `events.md P7 event ${eventType} still present`)
}

// ---------------------------------------------------------------------------
// task-types.md: 19 types, no duplicate P4/P7 reservation of report.render_pdf.
// ---------------------------------------------------------------------------
const taskRows = tasks
  .split("\n")
  .filter((line) =>
    /^\|\s*`[a-z][a-z0-9_.]*`\s*\|\s*`?(clarification|design|execution|delivery|steward|code|ops)`?\s*\|/.test(
      line,
    ),
  )
check(taskRows.length === 19, `task-types rows = 19 (got ${taskRows.length})`)
check(tasks.includes("任务类型合计 **19**"), "task-types total 19")
check(tasks.includes("| `report.render_pdf` | ops |"), "task report.render_pdf team ops")
check(/^## 8\. P7 任务类型（实现；frozen: P7/m.test(tasks), "task-types §8 P7 is still present")
check(
  !tasks.includes("`report.render_pdf`、`knowledge.review`、`gc.run`"),
  "task-types §4 no longer reserves report.render_pdf",
)
check(
  !tasks.includes("`report.render_pdf` 继续保留、**不注册**（P7）"),
  "task-types removes the stale 'report.render_pdf stays reserved' duplicate",
)
check(
  tasks.includes("P7 已把 `report.render_pdf`") || tasks.includes("P7 将 `report.render_pdf`"),
  "task-types §4 points to §8 for report.render_pdf",
)

// ---------------------------------------------------------------------------
// agent-profiles-tools.md: 14 profiles + P8 knowledge query derivation.
// ---------------------------------------------------------------------------
const seedProfiles = countSeedProfiles(profiles)
check(seedProfiles.count === 14, `agent-profiles-tools profiles = 14 (got ${seedProfiles.count})`)
check(new Set(seedProfiles.names).size === 14, "agent-profiles-tools profile names are unique (14)")
check(profiles.includes("profile 总数仍 **14**"), "agent-profiles-tools states 14 profiles")
check(profiles.includes("P8 起由 runner"), "agent-profiles-tools §10.3 documents P8 query derivation")
check(profiles.includes("`versionPack.Title`"), "knowledge query uses versionPack.Title")
check(profiles.includes("`versionPack.Requirements`"), "knowledge query uses versionPack.Requirements")
check(profiles.includes("≤512"), "knowledge query truncated to <=512 chars")
check(profiles.includes("空 `query`"), "knowledge query empty fallback documented")
check(profiles.includes("id DESC"), "knowledge query empty fallback keeps id DESC")

// ---------------------------------------------------------------------------
// executor-protocol.md: P8 per-executor dispatch promoted; wire protocol v1.2.
// ---------------------------------------------------------------------------
check(/^### 12\.3 容量选择与排空/m.test(executor), "executor-protocol §12.3 present")
const executor123 = section(executor, "### 12.3", "### 12.4")
check(/P8 (?:已)?实现/.test(executor123), "executor-protocol §12.3 says P8 implemented")
check(executor123.includes("per-executor"), "executor-protocol §12.3 per-executor routing")
check(executor123.includes("case_run_sut_context.executor_id"), "executor-protocol §12.3 lands executor_id")
check(executor123.includes("capacity DESC, last_heartbeat DESC, id ASC"), "executor-protocol capacity ordering")
check(/线协议(?:仍为|不变).{0,20}v1\.2|v1\.2.{0,20}(?:不变|保持)/.test(executor123), "executor-protocol wire protocol v1.2 unchanged")
check(executor123.includes("单端点兼容"), "executor-protocol §12.3 single-endpoint compatibility")
check(!executor.includes("P8 backlog"), "executor-protocol removes 'P8 backlog' reservation")
check(!executor.includes("派发只限主端点"), "executor-protocol removes 'primary endpoint only' reservation")
check(
  !/P7 保留（P3 不实现）[^\n]*多副本执行器调度/.test(executor),
  "executor-protocol §10 no longer duplicates the multi-endpoint reservation",
)

// ---------------------------------------------------------------------------
// contracts/README.md: P8 freeze status with the frozen totals.
// ---------------------------------------------------------------------------
check(/P8 冻结状态.*frozen: P8|P8 契约状态.*frozen: P8/.test(readme), "contracts README P8 frozen status")
for (const needle of [
  "0.2.0-p8",
  "42 迁移",
  "85 设置",
  "85 表",
  "118 事件",
  "19 任务",
  "14 profiles",
  "162 paths",
  "200 operations",
  "264 schemas",
  "active_requests",
  "inflight_requests",
  "rate_limited",
]) {
  check(readme.includes(needle), `README P8 increment ${needle}`)
}
for (const needle of ["18 个 operation", "6 新表", "7 迁移 #34–#40", "72→83", "`report.render_pdf`", "`knowledge.propose`"]) {
  check(readme.includes(needle), `README P7 increment ${needle} still present`)
}

if (failures > 0) {
  console.error(`P8 contract check FAILED (${failures} failed needle(s))`)
  process.exit(1)
}
console.log(
  "P8 contract check OK: 0.2.0-p8 / P8 frozen / 162 paths / 200 operations / 264 schemas / 42 migrations / 85 tables / 85 settings / 118 events / 19 tasks / 14 profiles / audit retention.cleanup / executor per-executor dispatch",
)

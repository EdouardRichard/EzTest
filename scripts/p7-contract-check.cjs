#!/usr/bin/env node
/*
 * P7 contract gate: verifies the frozen P7 contract increment exists and is
 * additive against P0-P6.
 *
 * P7 (P9 five deliverables) freezes:
 *   - openapi 0.2.0-p7 / x-frozen: P7 / 162 paths / 200 operations / 264 schemas
 *   - 18 new operations (16 public + 2 internal), tags Knowledge/Admin/Internal
 *   - 6 new tables (#34-#39) + #40 ops work-item team (7 migrations, total 40)
 *   - 3 widened tables
 *   - 12 new events (event PK is (channel, seq), NOTIFY payload {"channel","seq"})
 *   - 83 settings (72 -> 83)
 *   - report.render_pdf task (team ops) + knowledge.propose tool
 *   - PDF / recurrence / SUT family additive fields
 *
 * This gate is forward-compatible with P8+: it proves the P7 increment and
 * frozen totals still exist, but accepts later phases that only append
 * migrations/settings/operations (totals use ">=", phase markers accept P7+).
 */
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const contracts = path.join(root, "docs", "version-V0.2", "contracts")
const migrationsDir = path.join(root, "backend", "internal", "db", "migrations")
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
// OpenAPI identity, counts, tags and the 18 P7 operations.
// ---------------------------------------------------------------------------
if (openapi && typeof openapi === "object") {
  const openapiPhase = Number((/^0\.2\.0-p(\d+)$/.exec(openapi.info?.version ?? "") || [])[1] || 0)
  check(openapiPhase >= 7, `openapi info.version 0.2.0-p7+ (got ${openapi.info?.version ?? "missing"})`)
  check(phaseOf(openapi["x-frozen"]) >= 7, `openapi x-frozen P7+ (got ${openapi["x-frozen"] ?? "missing"})`)
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
  check(paths.length >= 162, `openapi paths >= 162 (got ${paths.length})`)
  check(operations.length >= 200, `openapi operations >= 200 (got ${operations.length})`)
  check(schemas.length >= 264, `openapi schemas >= 264 (got ${schemas.length})`)

  const tagNames = (openapi.tags ?? []).map((tag) => tag.name)
  for (const tag of ["Knowledge", "Admin", "Internal"]) {
    check(tagNames.includes(tag), `openapi tag ${tag}`)
  }

  const p7Operations = [
    ["listKnowledgeItems", "get", "/api/knowledge/items"],
    ["createKnowledgeItem", "post", "/api/knowledge/items"],
    ["getKnowledgeItem", "get", "/api/knowledge/items/{knowledgeId}"],
    ["updateKnowledgeItem", "patch", "/api/knowledge/items/{knowledgeId}"],
    ["approveKnowledgeItem", "post", "/api/knowledge/items/{knowledgeId}/approve"],
    ["rejectKnowledgeItem", "post", "/api/knowledge/items/{knowledgeId}/reject"],
    ["revokeKnowledgeItem", "post", "/api/knowledge/items/{knowledgeId}/revoke"],
    ["listKnowledgeRevisions", "get", "/api/knowledge/items/{knowledgeId}/revisions"],
    ["createGCRun", "post", "/api/admin/gc/runs"],
    ["listGCRuns", "get", "/api/admin/gc/runs"],
    ["getGCRun", "get", "/api/admin/gc/runs/{gcRunId}"],
    ["listInstances", "get", "/api/admin/instances"],
    ["drainInstance", "post", "/api/admin/instances/{id}/drain"],
    ["activateInstance", "post", "/api/admin/instances/{id}/activate"],
    ["getPlatformReadiness", "get", "/api/health/ready"],
    ["downloadReportPDF", "get", "/api/versions/{versionId}/reports/{reportId}/revisions/{revision}/pdf"],
    ["registerExecutor", "post", "/api/internal/executors/register"],
    ["heartbeatExecutor", "post", "/api/internal/executors/heartbeat"],
  ]
  check(p7Operations.length === 18, "p7 operation table must list 18 operations")
  for (const [operationId, method, route] of p7Operations) {
    const matches = operations.filter((operation) => operation.operationId === operationId)
    check(matches.length === 1, `openapi operationId ${operationId} (got ${matches.length})`)
    const operation = openapi.paths?.[route]?.[method]
    check(Boolean(operation), `openapi missing ${method.toUpperCase()} ${route}`)
    check(operation?.operationId === operationId, `${method.toUpperCase()} ${route} operationId = ${operationId}`)
  }

  const pdfOperation = openapi.paths?.[
    "/api/versions/{versionId}/reports/{reportId}/revisions/{revision}/pdf"
  ]?.get
  check(
    Boolean(pdfOperation?.responses?.["200"]?.content?.["application/pdf"]),
    "downloadReportPDF returns application/pdf",
  )
  check(
    openapi.paths?.["/api/health/ready"]?.get?.security?.length === 0,
    "GET /api/health/ready is public (security: [])",
  )
  check(
    JSON.stringify(openapi.paths?.["/api/internal/executors/register"]?.post?.security ?? []) ===
      JSON.stringify([{ internalBearerAuth: [] }]),
    "registerExecutor uses internalBearerAuth",
  )
  check(
    JSON.stringify(openapi.paths?.["/api/internal/executors/heartbeat"]?.post?.security ?? []) ===
      JSON.stringify([{ internalBearerAuth: [] }]),
    "heartbeatExecutor uses internalBearerAuth",
  )

  const revision = openapi.components?.schemas?.ReportRevisionView
  check(revision?.properties?.pdf_available?.type === "boolean", "ReportRevisionView.pdf_available is boolean")
  check(
    revision?.properties?.pdf_available?.enum === undefined,
    "ReportRevisionView.pdf_available has no enum const false",
  )
  check(
    JSON.stringify(revision?.properties?.pdf_status?.enum ?? []) ===
      JSON.stringify(["pending", "running", "done", "failed", "disabled"]),
    "ReportRevisionView.pdf_status enum",
  )
  check(revision?.properties?.pdf_error?.nullable === true, "ReportRevisionView.pdf_error is nullable")
  check(
    revision?.properties?.pdf_rendered_at?.nullable === true,
    "ReportRevisionView.pdf_rendered_at is nullable",
  )

  const issue = openapi.components?.schemas?.IssueView
  check(issue?.properties?.recurrence_count?.type === "integer", "IssueView.recurrence_count is integer")
  check(issue?.properties?.last_recurred_at?.nullable === true, "IssueView.last_recurred_at is nullable")
  check(issue?.properties?.merged_chain?.type === "array", "IssueView.merged_chain is an array")
  check(issue?.properties?.merged_chain?.nullable === true, "IssueView.merged_chain is nullable")
  check(
    issue?.properties?.merged_chain?.items?.pattern === "^[0-9]+$",
    "IssueView.merged_chain items are decimal strings",
  )

  for (const schemaName of ["SUT", "CreateSUTRequest", "UpdateSUTRequest"]) {
    const family = openapi.components?.schemas?.[schemaName]?.properties?.family
    check(Boolean(family), `${schemaName}.family present`)
    check(family?.nullable === true, `${schemaName}.family nullable`)
    check(family?.maxLength === 64, `${schemaName}.family maxLength 64`)
    check(
      typeof family?.pattern === "string" && family.pattern.includes("A-Z") && family.pattern.includes("\\s"),
      `${schemaName}.family lowercase/no-whitespace pattern`,
    )
  }

  const readiness = openapi.components?.schemas?.ReadinessView
  check(
    JSON.stringify(readiness?.properties?.database?.enum ?? []) === JSON.stringify(["ok", "down"]),
    "ReadinessView.database enum ok/down",
  )
  check(
    JSON.stringify(readiness?.properties?.status?.enum ?? []) === JSON.stringify(["ok", "degraded"]),
    "ReadinessView.status enum ok/degraded",
  )

  for (const schema of [
    "KnowledgeItemView",
    "KnowledgeRevisionView",
    "KnowledgeItemDetailView",
    "KnowledgeItemCreateRequest",
    "KnowledgeItemUpdateRequest",
    "KnowledgeReviewRequest",
    "KnowledgeListEnvelope",
    "KnowledgeRevisionListEnvelope",
    "GCRunView",
    "GCTombstoneView",
    "GCRunDetailView",
    "GCRunCreateRequest",
    "GCRunListEnvelope",
    "InstanceView",
    "InstanceListEnvelope",
    "ReadinessView",
    "ExecutorInstanceView",
    "ExecutorRegisterRequest",
    "ExecutorHeartbeatRequest",
  ]) {
    check(Boolean(openapi.components?.schemas?.[schema]), `openapi schema ${schema}`)
  }
}

// p7-api.md is folded into openapi.yaml and must not remain.
check(!fs.existsSync(path.join(contracts, "p7-api.md")), "contracts/p7-api.md is deleted (folded)")

// ---------------------------------------------------------------------------
// events.md: 12 P7 events + implemented LISTEN/NOTIFY payload.
// ---------------------------------------------------------------------------
check(/^## 16\. P7 增量事件（frozen: P7/m.test(events), "events.md §16 frozen: P7")
const p7EventSection = events.slice(events.indexOf("### 16.1"))
const p7EventBlock = p7EventSection.slice(0, p7EventSection.indexOf("### 16.2"))
const p7EventRows = p7EventBlock
  .split("\n")
  .filter((line) => /^\|\s*`[a-z]+\.[a-z_]+`\s*\|/.test(line))
check(p7EventRows.length === 12, `events.md P7 events = 12 (got ${p7EventRows.length})`)
for (const eventType of [
  "report.pdf_ready",
  "report.pdf_failed",
  "knowledge.proposed",
  "knowledge.approved",
  "knowledge.rejected",
  "knowledge.revoked",
  "gc.completed",
  "instance.registered",
  "instance.drained",
  "instance.activated",
  "instance.offline",
  "issue.recurred",
]) {
  check(events.includes("`" + eventType + "`"), `events.md event ${eventType}`)
}
check(events.includes('{"channel":"version:42","seq":17}'), "events.md NOTIFY payload {channel,seq}")
check(/主键.{0,12}\(channel, seq\)|\(channel, seq\).{0,20}主键/.test(events), "events.md event PK (channel, seq)")
check(events.includes("没有全局 event id"), "events.md states no global event id")
check(events.includes("重复 NOTIFY"), "events.md duplicate NOTIFY semantics")
check(events.includes("Last-Event-ID"), "events.md NOTIFY loss replay semantics")

// ---------------------------------------------------------------------------
// db-schema.md: §16, 6 new tables, widened tables.
// ---------------------------------------------------------------------------
check(/^## 16\. P7 增量表结构（frozen: P7/m.test(db), "db-schema.md §16 frozen: P7")
for (const table of [
  "knowledge_item",
  "knowledge_revision",
  "knowledge_usage",
  "gc_run",
  "gc_tombstone",
  "api_instance",
]) {
  check(db.includes("**`" + table + "`**"), `db-schema table ${table}`)
}
check(db.includes("新增 6 张表"), "db-schema states 6 new tables")
for (const column of [
  "pdf_status",
  "pdf_error",
  "pdf_attempts",
  "pdf_rendered_at",
  "recurrence_count",
  "last_recurred_at",
  "family",
]) {
  check(db.includes("`" + column + "`"), `db-schema P7 column ${column}`)
}
for (const object of [
  "uk_gc_run_running",
  "idx_content_ref_live_object",
  "idx_content_object_created_at",
  "idx_delivery_report_revision_pdf_pending",
  "idx_api_instance_status_heartbeat",
  "idx_sut_family",
  "idx_knowledge_item_search",
]) {
  check(db.includes(object), `db-schema object ${object}`)
}
check(
  db.includes("chk_work_item_team") && db.includes("`ops`"),
  "db-schema work_item team includes ops",
)

// ---------------------------------------------------------------------------
// migrations-order.md + order.txt: #34-#40 and 40 applied migrations.
// ---------------------------------------------------------------------------
check(/^## 9\. P7 增量迁移（frozen: P7/m.test(migrations), "migrations-order.md §9 frozen: P7")
const p7MigrationFiles = [
  "knowledge_0001_knowledge.sql",
  "storage_0003_gc.sql",
  "delivery_0004_report_pdf.sql",
  "orchestration_0001_instance.sql",
  "resource_0002_sut_family.sql",
  "delivery_0005_issue_recurrence.sql",
  "agent_0006_ops_team.sql",
]
for (const file of p7MigrationFiles) {
  check(migrations.includes(file), `migrations-order migration ${file}`)
}
for (const number of [34, 35, 36, 37, 38, 39, 40]) {
  check(migrations.includes("| " + number + " |"), `migrations-order row #${number}`)
}
check(migrations.includes("order.txt") && migrations.includes("40"), "migrations-order order.txt 40")
check(migrations.includes("6 张"), "migrations-order states 6 new tables")
check(
  migrations.includes("#40") && migrations.includes("ops"),
  "migrations-order documents #40 ops team correction",
)
check(
  migrations.includes("report.render_pdf") && migrations.includes("invalid_work_type"),
  "migrations-order documents why render jobs use ops",
)

const orderPath = path.join(migrationsDir, "order.txt")
check(fs.existsSync(orderPath), "migrations/order.txt exists")
let order = []
if (fs.existsSync(orderPath)) {
  order = fs
    .readFileSync(orderPath, "utf8")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith("#"))
  check(order.length >= 40, `order.txt >= 40 migrations (got ${order.length})`)
  const p7Start = order.indexOf(p7MigrationFiles[0])
  check(
    p7Start === 33,
    `order.txt P7 #34 starts at position 34 (got ${p7Start + 1})`,
  )
  check(
    p7Start >= 0 &&
      JSON.stringify(order.slice(p7Start, p7Start + 7)) === JSON.stringify(p7MigrationFiles),
    "order.txt contains P7 #34-#40 as a contiguous block (later phases append only)",
  )
  check(new Set(order).size === order.length, "order.txt has no duplicate entries")
}
const sqlFiles = fs
  .readdirSync(migrationsDir)
  .filter((name) => name.endsWith(".sql"))
check(sqlFiles.length >= 40, `migrations/*.sql >= 40 files (got ${sqlFiles.length})`)
for (const file of p7MigrationFiles) {
  check(fs.existsSync(path.join(migrationsDir, file)), `migration file ${file}`)
}
const p7SQL = p7MigrationFiles
  .filter((file) => fs.existsSync(path.join(migrationsDir, file)))
  .map((file) => fs.readFileSync(path.join(migrationsDir, file), "utf8"))
  .join("\n")
check(countMatches(p7SQL, /CREATE TABLE /g) === 6, "P7 migrations create exactly 6 tables")
check(/ALTER TABLE delivery_report_revision/.test(p7SQL), "P7 migration alters delivery_report_revision")
check(/ALTER TABLE delivery_issue/.test(p7SQL), "P7 migration alters delivery_issue")
check(/ALTER TABLE sut/.test(p7SQL), "P7 migration alters sut")
check(/ALTER TABLE work_item/.test(p7SQL), "P7 migration #40 alters work_item")
check(p7SQL.includes("'ops'"), "P7 migration #40 allows the ops team")

// ---------------------------------------------------------------------------
// settings.md + catalog.go: 83 settings and the 11 P7 keys.
// ---------------------------------------------------------------------------
check(/^## 4g\. P7 使用说明（frozen: P7/m.test(settings), "settings.md §4g frozen: P7")
check(settings.includes("72→83"), "settings.md 72→83")
check(settings.includes("新增 **11 键**"), "settings.md adds 11 keys")
check(settings.includes("= **83**"), "settings.md total 83")
for (const key of [
  "report.section_appendix",
  "report.render_max_attempts",
  "report.render_lease_seconds",
  "report.render_worker_url",
  "report.render_token",
  "knowledge.injection_max_items",
  "retention.gc_interval_minutes",
  "retention.gc_batch_size",
  "instance.heartbeat_seconds",
  "instance.offline_seconds",
  "execution.executor_urls",
]) {
  check(settings.includes("`" + key + "`"), `settings.md key ${key}`)
}
check(settings.includes('`report.template_version`'), "settings.md report.template_version default note")
check(
  settings.includes('默认由 `"1"` 改为 `"2"`') && settings.includes('允许值仅 `"1"`/`"2"`'),
  "report.template_version new-DB default 2 / allowed 1,2",
)

const catalogPath = path.join(root, "backend", "internal", "settings", "application", "catalog.go")
check(fs.existsSync(catalogPath), "settings catalog.go exists")
if (fs.existsSync(catalogPath)) {
  const catalog = fs.readFileSync(catalogPath, "utf8")
  const catalogKeys = countMatches(catalog, /\{Key: "/g)
  check(catalogKeys >= 83, `settings catalog keys >= 83 (got ${catalogKeys})`)
  for (const key of [
    "report.section_appendix",
    "report.render_max_attempts",
    "report.render_lease_seconds",
    "report.render_worker_url",
    "report.render_token",
    "knowledge.injection_max_items",
    "retention.gc_interval_minutes",
    "retention.gc_batch_size",
    "instance.heartbeat_seconds",
    "instance.offline_seconds",
    "execution.executor_urls",
  ]) {
    check(catalog.includes('"' + key + '"'), `catalog key ${key}`)
  }
}

// ---------------------------------------------------------------------------
// task-types.md: report.render_pdf registered; total 19.
// ---------------------------------------------------------------------------
check(/^## 8\. P7 任务类型（实现；frozen: P7/m.test(tasks), "task-types.md §8 frozen: P7")
check(tasks.includes("`report.render_pdf`"), "task report.render_pdf")
check(tasks.includes("`report:{report_id}:rev:{revision}`"), "task report.render_pdf dedupe key")
check(tasks.includes("{report_id,report_revision_id}"), "task report.render_pdf payload")
check(tasks.includes("`ClaimForTypes"), "task report.render_pdf ClaimForTypes")
check(tasks.includes("| `report.render_pdf` | ops |"), "task report.render_pdf team ops")
check(tasks.includes("任务类型合计 **19**"), "task-types total 19")

// ---------------------------------------------------------------------------
// agent-profiles-tools.md: knowledge.propose + injection fields/whitelist.
// ---------------------------------------------------------------------------
check(/^## 10\. P7 Profiles 与知识注入（frozen: P7/m.test(profiles), "agent-profiles-tools.md §10 frozen: P7")
check(profiles.includes("`knowledge.propose`"), "tool knowledge.propose")
for (const profile of [
  "clarification_host",
  "context_scout",
  "design_host",
  "design_planner",
  "case_author",
]) {
  check(
    new RegExp("\\| `" + profile + "` \\| `knowledge\\.propose` \\|").test(profiles),
    `profile ${profile} allows knowledge.propose`,
  )
}
check(profiles.includes("version_pack.knowledge"), "injection field version_pack.knowledge")
check(profiles.includes("knowledge.injection_max_items"), "injection max items setting")
check(profiles.includes("knowledge.injection_budget_tokens"), "injection budget setting")
check(profiles.includes("knowledge_usage"), "injection usage ledger")
check(profiles.includes("[K-"), "injection citation token")

// ---------------------------------------------------------------------------
// executor-protocol.md: push endpoints, Bearer token, multi-endpoint, dispatch.
// ---------------------------------------------------------------------------
check(/^## 12\. P7 增量：执行器 push/m.test(executor), "executor-protocol.md §12 frozen heading")
check(executor.includes("frozen: P7"), "executor-protocol.md §12 frozen: P7")
check(executor.includes("`POST /api/internal/executors/register`"), "executor-protocol register endpoint")
check(executor.includes("`POST /api/internal/executors/heartbeat`"), "executor-protocol heartbeat endpoint")
check(executor.includes("`execution.executor_token`"), "executor-protocol Bearer token")
check(executor.includes("`execution.executor_urls`"), "executor-protocol multi endpoint setting")
check(executor.includes("`max_contexts`"), "executor-protocol max_contexts alias")
check(
  executor.includes("单端点兼容") && executor.includes("`execution.executor_urls`"),
  "executor-protocol multi-endpoint + single-endpoint compatibility",
)
check(executor.includes("capacity DESC, last_heartbeat DESC, id ASC"), "executor-protocol capacity ordering")

// ---------------------------------------------------------------------------
// README.md: frozen P7 status with the increment counts.
// ---------------------------------------------------------------------------
check(/P7 冻结状态.*frozen: P7/.test(readme), "contracts README P7 frozen status")
for (const needle of [
  "18 个 operation",
  "6 新表",
  "7 迁移 #34–#40",
  "12 事件",
  "72→83",
  "`report.render_pdf`",
  "`knowledge.propose`",
  "200 operations",
  "264 schemas",
]) {
  check(readme.includes(needle), `README P7 increment ${needle}`)
}

if (failures > 0) {
  console.error(`P7 contract check FAILED (${failures} failed needle(s))`)
  process.exit(1)
}
console.log(
  "P7 contract check OK: 18 P7 ops / >=162 paths / >=200 operations / >=264 schemas / 6 tables / #34-#40 / 12 events / >=83 settings / report.render_pdf (ops) + knowledge.propose",
)

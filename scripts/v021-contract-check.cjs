#!/usr/bin/env node
/*
 * EzTest 0.2.1 contract presence gate.
 *
 * Dependency-free (node:fs / node:path only). Verifies that the 0.2.1
 * increments were synced into the four contracts owned by P0-T2:
 *   - docs/version-V0.2/contracts/events.md        (7 new event types)
 *   - docs/version-V0.2/contracts/openapi.yaml     (new paths/fields/schemas)
 *   - docs/version-V0.2/contracts/db-schema.md     (new tables + altered columns)
 *   - docs/version-V0.2/contracts/task-types.md    (11 work_item kinds, 7 roles,
 *                                                   four-way creation, created_by)
 *
 * The check proves the names from spec §5.2/§5.4/§12.3/§12.4 exist in the
 * frozen-version contract directory, plus the review-round-1 shape rules that
 * must not regress: parts belong to conversation_message (not agent_message),
 * nullable DB columns stay out of `required`, attachment enums match the DDL,
 * seq starts at 1, the role/kind/created_by catalogs are exact, and the counts
 * in the final summary are asserted rather than printed from constants. It
 * deliberately does not assert P0-P8 phase totals, so later 0.2.1 work
 * packages can keep appending.
 *
 * Exit code 0 = all 0.2.1 contract increments present, 1 = missing items.
 */
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const contracts = path.join(root, "docs", "version-V0.2", "contracts")
const read = (name) => fs.readFileSync(path.join(contracts, name), "utf8").replace(/\r\n/g, "\n")

let failures = 0
const check = (ok, message) => {
  if (ok) {
    console.log("ok: " + message)
    return
  }
  failures++
  console.error("FAIL: " + message)
}

// Return the slice between a start marker (inclusive) and the next end marker
// (exclusive). Missing start marker -> ""; missing end marker -> to EOF.
const section = (text, startMarker, endMarker) => {
  const start = text.indexOf(startMarker)
  if (start < 0) return ""
  const end = endMarker ? text.indexOf(endMarker, start + startMarker.length) : -1
  return end < 0 ? text.slice(start) : text.slice(start, end)
}

const events = read("events.md")
const openapi = read("openapi.yaml")
const db = read("db-schema.md")
const tasks = read("task-types.md")
const readme = read("README.md")

// ---------------------------------------------------------------------------
// events.md §18: seven 0.2.1 event types and their required payload fields.
// ---------------------------------------------------------------------------
const eventsSection = section(events, "## 18. 0.2.1", "\n## 19.")
check(eventsSection.length > 0, "events.md has a §18 0.2.1 increment section")

const eventPayloads = {
  "version.stage_changed": ["from", "to", "reason", "stage_entered_at"],
  "plan.item_changed": ["plan_item_id", "phase", "status", "order"],
  "fact.recorded": ["fact_id", "kind", "key", "confidence", "source", "content_hash", "conflict"],
  "job.progress": ["job_id", "kind", "current", "total", "status"],
  "job.finished": ["job_id", "kind", "status", "result_ref"],
  "knowledge.auto_approved": ["knowledge_id", "kind", "scope"],
  "context.cache_stats": ["run_id", "prefix_hash", "cached_tokens", "cache_hit", "briefing_hash"],
}
for (const [eventType, fields] of Object.entries(eventPayloads)) {
  const row = eventsSection.split("\n").find((line) => line.includes("`" + eventType + "`"))
  check(Boolean(row), `events.md §18 declares ${eventType}`)
  if (!row) continue
  for (const field of fields) {
    check(row.includes("`" + field + "`"), `events.md ${eventType} payload requires ${field}`)
  }
}
check(
  /job\.progress[\s\S]{0,400}job_id\s*=\s*work_item\.id|job_id\s*=\s*work_item\.id/.test(eventsSection),
  "events.md documents job_id = work_item.id",
)

// The §18.1 event table is a closed list: exactly the seven 0.2.1 types.
const eventsTable18 = section(eventsSection, "### 18.1", "### 18.2")
const declaredEvents = new Set()
for (const line of eventsTable18.split("\n")) {
  const match = /^\|\s*`([a-z][a-z0-9_.]+)`\s*\|/.exec(line)
  if (match) declaredEvents.add(match[1])
}
const expectedEvents = Object.keys(eventPayloads)
check(
  declaredEvents.size === expectedEvents.length &&
    expectedEvents.every((eventType) => declaredEvents.has(eventType)),
  `events.md §18.1 lists exactly ${expectedEvents.length} 0.2.1 event types (got ${declaredEvents.size})`,
)
check(
  /total\s*=\s*0[\s\S]{0,80}未知/.test(eventsSection),
  "events.md defines job.progress total=0 as unknown (no fake percentage)",
)
check(
  /`confidence`[\s\S]{0,160}null[\s\S]{0,60}未知/.test(eventsSection),
  "events.md keeps fact.recorded confidence null = unknown",
)
check(
  eventsSection.includes("命名映射") &&
    eventsSection.includes("`part_type`") &&
    eventsSection.includes("`MessagePart`") &&
    eventsSection.includes("`state`") &&
    eventsSection.includes("`status`"),
  "events.md documents the part_type/type and state/status naming mapping",
)
const auditSection18 = section(eventsSection, "### 18.3", "")
check(auditSection18.length > 0, "events.md §18.3 lists the 0.2.1 audit action")
check(
  /`knowledge\.auto_approved`/.test(auditSection18),
  "events.md audit catalog contains knowledge.auto_approved",
)

// ---------------------------------------------------------------------------
// openapi.yaml: 0.2.1 description marker, paths/operations, schemas, fields.
// ---------------------------------------------------------------------------
check(/^\s*### 0\.2\.1 增量（draft: 0\.2\.1/m.test(openapi), "openapi has a 0.2.1 draft increment section")

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
const openapiPaths = parsePaths(openapi)

const apiChecks = [
  ["/api/llm/models", "get", "公开模型列表"],
  ["/api/versions/{id}/conversations/{cid}", "patch", "会话模型覆盖"],
  ["/api/versions/{id}/conversations/{cid}/messages/{mid}/parts", "get", "消息 parts 历史"],
  ["/api/versions/{id}/stage", "get", "版本阶段"],
  ["/api/versions/{id}/stage/confirm-requirements", "post", "需求确认硬门"],
  ["/api/versions/{id}/stage/confirm-cases", "post", "用例确认硬门（可部分）"],
  ["/api/versions/{id}/stage/reject", "post", "退回澄清/设计"],
  ["/api/versions/{id}/work-items", "get", "work_item 查询"],
  ["/api/versions/{id}/work-items/{workItemId}", "get", "work_item 详情"],
  ["/api/versions/{id}/plan-items", "get", "plan 查询"],
  ["/api/versions/{id}/facts", "get", "facts 查询"],
  ["/api/knowledge/search", "get", "知识结构化检索"],
  ["/api/versions/{id}/jobs/{jobId}", "get", "作业状态"],
  ["/api/versions/{id}/jobs/{jobId}/cancel", "post", "作业取消"],
  ["/api/versions/{id}/jobs/{jobId}/retry", "post", "作业重试"],
]
for (const [route, method, label] of apiChecks) {
  check(Boolean(openapiPaths.get(route)?.has(method)), `openapi ${method.toUpperCase()} ${route} (${label})`)
}
// Knowledge read/propose/revoke reuse the frozen P7 endpoints.
for (const [route, method, label] of [
  ["/api/knowledge/items", "post", "知识 propose"],
  ["/api/knowledge/items/{knowledgeId}", "get", "知识 read"],
  ["/api/knowledge/items/{knowledgeId}/revoke", "post", "知识 revoke"],
]) {
  check(Boolean(openapiPaths.get(route)?.has(method)), `openapi keeps ${method.toUpperCase()} ${route} (${label})`)
}

const schemaChecks = [
  "StageView",
  "StageConfirmRequirementsRequest",
  "StageConfirmCasesRequest",
  "StageRejectRequest",
  "WorkItemView",
  "PlanItemView",
  "FactView",
  "MessagePartView",
  "JobView",
  "PublicModelView",
  "KnowledgeSearchHit",
]
for (const schema of schemaChecks) {
  check(new RegExp("^    " + schema + ":", "m").test(openapi), `openapi schema ${schema}`)
}

// Shape rules from review round 1: nullable DB columns must not be required,
// the attachment enum must match the DDL, and seq starts at 1.
const schemaBlock = (name) => {
  const marker = "\n    " + name + ":\n"
  const start = openapi.indexOf(marker)
  if (start < 0) return ""
  const rest = openapi.slice(start + marker.length)
  const end = rest.search(/\n    [A-Za-z][A-Za-z0-9]*:\n/)
  return end < 0 ? rest : rest.slice(0, end)
}
const requiredOf = (block) => {
  const match = /^      required: \[(.*)\]$/m.exec(block)
  return match ? match[1].split(",").map((item) => item.trim()).filter(Boolean) : []
}
const propertyOf = (block, name) => {
  const marker = "\n        " + name + ":\n"
  const start = block.indexOf(marker)
  if (start < 0) return ""
  const rest = block.slice(start + marker.length)
  const end = rest.search(/\n        [a-z][a-z0-9_]*:\n/)
  return end < 0 ? rest : rest.slice(0, end)
}

const workItemBlock = schemaBlock("WorkItemView")
const workItemRequired = requiredOf(workItemBlock)
check(workItemBlock.length > 0, "openapi WorkItemView schema block")
check(workItemRequired.length === 9, `WorkItemView.required has 9 fields (got ${workItemRequired.length})`)
check(!workItemRequired.includes("depends_on"), "WorkItemView.required omits nullable depends_on")
check(!workItemRequired.includes("idempotency_key"), "WorkItemView.required omits nullable idempotency_key")
check(/nullable:\s*true/.test(propertyOf(workItemBlock, "depends_on")), "WorkItemView.depends_on is nullable")
check(
  /nullable:\s*true/.test(propertyOf(workItemBlock, "idempotency_key")),
  "WorkItemView.idempotency_key is nullable",
)
check(/无依赖/.test(propertyOf(workItemBlock, "depends_on")), "WorkItemView.depends_on documents null/[] = no deps")

const factBlock = schemaBlock("FactView")
const factRequired = requiredOf(factBlock)
check(!factRequired.includes("confidence"), "FactView.required omits nullable confidence")
check(/nullable:\s*true/.test(propertyOf(factBlock, "confidence")), "FactView.confidence is nullable")
check(/未知/.test(propertyOf(factBlock, "confidence")), "FactView.confidence documents null = unknown")

const attachmentBlock = schemaBlock("AttachmentView")
check(
  /enum: \[document, image\]/.test(propertyOf(attachmentBlock, "kind")),
  "AttachmentView.kind enum is document|image (pdf/text are document sub-types)",
)
const parseErrorProp = propertyOf(attachmentBlock, "parse_error")
check(/^          type: string$/m.test(parseErrorProp), "AttachmentView.parse_error is a string (DB TEXT NULL)")
check(/nullable:\s*true/.test(parseErrorProp), "AttachmentView.parse_error is nullable")
check(!/type: object/.test(parseErrorProp), "AttachmentView.parse_error is not an object")

const partViewBlock = schemaBlock("MessagePartView")
check(/minimum:\s*1/.test(propertyOf(partViewBlock, "seq")), "MessagePartView.seq minimum 1 (DB CHECK seq > 0)")

// {mid} is conversation_message.id (the UI message), never agent_message.id.
const parameterBlock = (name) => {
  const marker = "\n    " + name + ":\n"
  const start = openapi.indexOf(marker)
  if (start < 0) return ""
  const rest = openapi.slice(start + marker.length)
  const end = rest.search(/\n    [A-Z][A-Za-z0-9]*:\n/)
  return end < 0 ? rest : rest.slice(0, end)
}
const midBlock = parameterBlock("MID")
check(/conversation_message\.id/.test(midBlock), "MID parameter is conversation_message.id")
check(!/description: 消息（`agent_message\.id`/.test(midBlock), "MID parameter is not defined as agent_message.id")
const partPathBlock = (() => {
  const marker = "\n  /api/versions/{id}/conversations/{cid}/messages/{mid}/parts:\n"
  const start = openapi.indexOf(marker)
  if (start < 0) return ""
  const rest = openapi.slice(start + marker.length)
  const end = rest.search(/\n  \/[^\n]+:\n/)
  return end < 0 ? rest : rest.slice(0, end)
})()
check(/conversation_message\.id/.test(partPathBlock), "parts path documents {mid} = conversation_message.id")
check(/seq/.test(partPathBlock), "parts path documents seq ordering")
check(/conversation_id|归属/.test(partPathBlock), "parts path documents conversation ownership filtering")
check(
  /P6-T3/.test(openapi) && /会话中可切/.test(openapi),
  "openapi notes P6-T3 implements the conversation model override (spec §1.2/§9.1)",
)

const operationIds = [...openapi.matchAll(/^      operationId: ([A-Za-z0-9_]+)$/gm)].map((match) => match[1])
for (const operationId of [
  "listPublicModels",
  "updateConversationModel",
  "listMessageParts",
  "getVersionStage",
  "confirmRequirements",
  "confirmCases",
  "rejectStage",
  "listWorkItems",
  "getWorkItem",
  "listPlanItems",
  "listFacts",
  "searchKnowledge",
  "getJob",
  "cancelJob",
  "retryJob",
]) {
  check(
    operationIds.filter((candidate) => candidate === operationId).length === 1,
    `openapi operationId ${operationId} appears once`,
  )
}

for (const needle of [
  ["model_override", "会话模型覆盖字段"],
  ["actual_model", "消息实际模型字段"],
  ["part_type", "part 类型字段"],
  ["enum: [thinking]", "thinking part"],
  ["enum: [tool_call]", "tool_call part"],
  ["enum: [job_card]", "job_card part"],
  ["enum: [case_card]", "case_card part"],
  ["enum: [image]", "image part"],
]) {
  check(openapi.includes(needle[0]), `openapi contains ${needle[0]} (${needle[1]})`)
}

// ---------------------------------------------------------------------------
// db-schema.md §18: three new tables + column increments on existing tables.
// ---------------------------------------------------------------------------
const dbSection = section(db, "## 18. 0.2.1", "\n## 19.")
check(dbSection.length > 0, "db-schema.md has a §18 0.2.1 increment section")

const newTables = {
  version_plan_item: ["id", "version_id", "phase", "title", "status", "depends_on", "work_item_id", "decision", "sort_order"],
  version_fact: [
    "id",
    "version_id",
    "kind",
    "key",
    "payload",
    "confidence",
    "source",
    "evidence_refs",
    "content_hash",
    "superseded_by",
  ],
  conversation_message_part: ["id", "message_id", "seq", "part_type", "payload"],
}
for (const [table, columns] of Object.entries(newTables)) {
  check(dbSection.includes("**`" + table + "`**"), `db-schema.md §18 declares table ${table}`)
  const block = section(dbSection, "**`" + table + "`**", "\n**`")
  for (const column of columns) {
    check(block.includes("`" + column + "`"), `db-schema.md ${table}.${column}`)
  }
}

// The parts table is version-context and belongs to conversation_message.
const partsTableBlock = section(dbSection, "**`conversation_message_part`**", "\n**`")
check(partsTableBlock.includes("REFERENCES conversation_message(id)"), "conversation_message_part FK conversation_message(id)")
check(
  partsTableBlock.includes("上下文：version"),
  "conversation_message_part lives in the version bounded context",
)
check(dbSection.includes("uk_conversation_message_part_message_seq"), "parts unique key is (message_id, seq)")
check(dbSection.includes("seq > 0"), "parts seq starts at 1")
check(!dbSection.includes("agent_message_part"), "db-schema §18 has no agent_message_part table")
check(
  dbSection.includes("version_0007_stage_plan_fact.sql") && dbSection.includes("resource_0003_page_url.sql"),
  "db-schema §18 provenance cites version_0007_stage_plan_fact.sql + resource_0003_page_url.sql",
)
check(
  !dbSection.includes("version_0002_stage_plan_fact.sql") && !dbSection.includes("resource_0002_page_url.sql"),
  "db-schema §18 provenance does not cite the superseded migration names",
)

const alteredTables = {
  version: ["stage", "stage_entered_at", "blocker"],
  work_item: [
    "kind",
    "goal",
    "target",
    "inputs",
    "expected",
    "budget",
    "depends_on",
    "created_by",
    "idempotency_key",
    "result",
  ],
  agent_run: ["model", "config_id", "fallback_from", "tokens", "cache"],
  llm_config: ["thinking_type", "reasoning_effort", "default_model_id"],
  llm_model: ["capabilities", "deprecated"],
  conversation: ["model_override"],
  knowledge_item: ["bindings", "last_verified_at", "status"],
  requirement_attachment: ["kind", "parse_status", "parse_error", "page_refs"],
  sut_page: ["normalized_url"],
}
for (const [table, columns] of Object.entries(alteredTables)) {
  const row = dbSection.split("\n").find((line) => line.startsWith("| `" + table + "` |"))
  check(Boolean(row), `db-schema.md §18 alters table ${table}`)
  if (!row) continue
  for (const column of columns) {
    check(row.includes("`" + column + "`"), `db-schema.md ${table}.${column}`)
  }
}

// Spec aliases must be documented so implementers do not invent new tables.
for (const needle of [
  "`llm_profile.thinking_type/reasoning_effort/default_model_id`",
  "`attachment.kind/parse_status/parse_error`",
  "`sut_page.normalized_url`",
]) {
  check(dbSection.includes(needle), `db-schema.md §18 documents ${needle}`)
}
check(dbSection.includes("uk_version_fact_version_kind_key_hash"), "db-schema.md version_fact dedupe unique key")
check(
  dbSection.includes("uk_sut_page_normalized_url") || dbSection.includes("(environment_id, normalized_url)"),
  "db-schema.md sut_page normalized_url unique key",
)

// ---------------------------------------------------------------------------
// task-types.md §9: 11 work_item kinds, 7 roles, four-way creation, created_by.
// ---------------------------------------------------------------------------
const tasksSection = section(tasks, "## 9. 0.2.1", "\n## 10.")
check(tasksSection.length > 0, "task-types.md has a §9 0.2.1 increment section")

const workItemKinds = [
  "explore.login",
  "explore.page",
  "explore.api",
  "explore.coverage",
  "explore.gap",
  "design.cases",
  "review.cases",
  "execute.batch",
  "analyze.failure",
  "report.version",
  "knowledge.propose",
]
for (const kind of workItemKinds) {
  check(tasksSection.includes("`" + kind + "`"), `task-types.md §9 work_item kind ${kind}`)
}

const roleKeys = [
  "assistant",
  "case_author",
  "case_reviewer",
  "explorer",
  "failure_analyst",
  "report_writer",
  "knowledge_curator",
]
for (const role of roleKeys) {
  check(tasksSection.includes("`" + role + "`"), `task-types.md §9 role key ${role}`)
}

check(
  tasksSection.includes("`state_machine|planner|assistant|trigger`"),
  "task-types.md §9 fixes created_by to state_machine|planner|assistant|trigger",
)
for (const actor of ["state_machine", "planner", "assistant", "trigger"]) {
  const row = tasksSection.split("\n").find((line) => line.startsWith("| `" + actor + "` |"))
  check(Boolean(row), `task-types.md §9 four-way creation row ${actor}`)
}
check(
  /执行者(?:不能|不得)创建/.test(tasksSection),
  "task-types.md §9 states executors cannot create work_item",
)
check(
  /`execute\.batch`[\s\S]{0,200}(确定性|无 Agent 角色|调度)/.test(tasksSection),
  "task-types.md §9 execute.batch maps to deterministic dispatch",
)

// Closed catalogs: exact 11 kinds / 7 role keys / 4 created_by values.
const kindRows = section(tasksSection, "### 9.1", "### 9.2")
const declaredKinds = new Set()
for (const line of kindRows.split("\n")) {
  const match = /^\|\s*`([a-z][a-z0-9_.]+)`\s*\|/.exec(line)
  if (match) declaredKinds.add(match[1])
}
check(
  declaredKinds.size === workItemKinds.length && workItemKinds.every((kind) => declaredKinds.has(kind)),
  `task-types.md §9.1 lists exactly ${workItemKinds.length} work_item kinds (got ${declaredKinds.size})`,
)
const roleRows = section(tasksSection, "### 9.2", "### 9.3")
const declaredRoles = new Set()
for (const line of roleRows.split("\n")) {
  const match = /^\|\s*[^|]+\|\s*`([a-z][a-z0-9_]+)`\s*\|/.exec(line)
  if (match) declaredRoles.add(match[1])
}
check(
  declaredRoles.size === roleKeys.length && roleKeys.every((role) => declaredRoles.has(role)),
  `task-types.md §9.2 lists exactly ${roleKeys.length} role keys (got ${declaredRoles.size})`,
)
const creationRows = section(tasksSection, "### 9.3", "### 9.4")
const declaredCreators = new Set()
for (const line of creationRows.split("\n")) {
  const match = /^\|\s*`([a-z][a-z0-9_]+)`\s*\|/.exec(line)
  if (match) declaredCreators.add(match[1])
}
const expectedCreators = ["state_machine", "planner", "assistant", "trigger"]
check(
  declaredCreators.size === expectedCreators.length &&
    expectedCreators.every((actor) => declaredCreators.has(actor)),
  `task-types.md §9.3 lists exactly ${expectedCreators.length} created_by values (got ${declaredCreators.size})`,
)
for (const kind of ["design.cases", "review.cases"]) {
  const row = tasksSection.split("\n").find((line) => line.startsWith("| `" + kind + "` |"))
  check(
    Boolean(row) && row.includes("assistant") && row.includes("job.start"),
    `task-types.md §9.1 ${kind} row shows the controlled assistant job.start bypass`,
  )
}

// ---------------------------------------------------------------------------
// contracts/README.md: 0.2.1 status line, without rewriting the P8 totals.
// ---------------------------------------------------------------------------
check(/0\.2\.1 契约状态/.test(readme), "contracts README has a 0.2.1 status line")
for (const needle of ["176 paths", "215 operations", "275 schemas", "125 events", "draft: 0.2.1"]) {
  check(readme.includes(needle), `contracts README 0.2.1 status contains ${needle}`)
}

// ---------------------------------------------------------------------------
// Result. Counts are derived from the parsed catalogs above, not hardcoded.
// ---------------------------------------------------------------------------
const summary = {
  events: declaredEvents.size,
  operations: apiChecks.length,
  newTables: Object.keys(newTables).length,
  alteredTables: Object.keys(alteredTables).length,
  kinds: declaredKinds.size,
  roles: declaredRoles.size,
  createdBy: declaredCreators.size,
}
check(summary.events === 7, `summary event count = 7 (got ${summary.events})`)
check(summary.operations === 15, `summary operation count = 15 (got ${summary.operations})`)
check(summary.newTables === 3, `summary new table count = 3 (got ${summary.newTables})`)
check(summary.alteredTables === 9, `summary altered table count = 9 (got ${summary.alteredTables})`)
check(summary.kinds === 11, `summary work_item kind count = 11 (got ${summary.kinds})`)
check(summary.roles === 7, `summary role count = 7 (got ${summary.roles})`)
check(summary.createdBy === 4, `summary created_by count = 4 (got ${summary.createdBy})`)

if (failures > 0) {
  console.error(`0.2.1 contract check FAILED (${failures} missing needle(s))`)
  process.exit(1)
}
console.log(
  `0.2.1 contract check OK: ${summary.events} events / ${summary.operations} operations / ` +
    `${summary.newTables} new tables / ${summary.alteredTables} altered tables / ` +
    `${summary.kinds} work_item kinds / ${summary.roles} roles / created_by ${summary.createdBy} values`,
)

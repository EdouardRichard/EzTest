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
 * The check is presence-only: it proves the names from spec §5.2/§5.4/§12.3/
 * §12.4 exist in the frozen-version contract directory. It deliberately does
 * not assert phase totals, so later 0.2.1 work packages can keep appending.
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
  agent_message_part: ["id", "message_id", "seq", "part_type", "payload"],
}
for (const [table, columns] of Object.entries(newTables)) {
  check(dbSection.includes("**`" + table + "`**"), `db-schema.md §18 declares table ${table}`)
  const block = section(dbSection, "**`" + table + "`**", "\n**`")
  for (const column of columns) {
    check(block.includes("`" + column + "`"), `db-schema.md ${table}.${column}`)
  }
}

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

// ---------------------------------------------------------------------------
// Result.
// ---------------------------------------------------------------------------
if (failures > 0) {
  console.error(`0.2.1 contract check FAILED (${failures} missing needle(s))`)
  process.exit(1)
}
console.log(
  "0.2.1 contract check OK: 7 events / 15 operations / 3 new tables / 9 altered tables / " +
    "11 work_item kinds / 7 roles / created_by 4 values",
)

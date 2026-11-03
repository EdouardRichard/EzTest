#!/usr/bin/env node
/*
 * EzTest 0.2.1 contract presence gate.
 *
 * Dependency-free (node:fs / node:path only). Verifies that the 0.2.1
 * increments were synced into the contracts:
 *   - docs/version-V0.2/contracts/events.md          (10 new event types,
 *                                                    3 new audit actions)
 *   - docs/version-V0.2/contracts/openapi.yaml       (new paths/fields/schemas,
 *                                                    canonical LLM/job/knowledge/
 *                                                    context-packet surfaces)
 *   - docs/version-V0.2/contracts/db-schema.md       (new tables + altered
 *                                                    columns + attempt marker +
 *                                                    pg_trgm reservation)
 *   - docs/version-V0.2/contracts/task-types.md      (11 work_item kinds, 7 roles,
 *                                                    four-way creation, created_by)
 *   - docs/version-V0.2/contracts/executor-protocol.md (storageState capture/
 *                                                    reuse, page-scoped close,
 *                                                    network capture is 0.3)
 *
 * The check proves the names from spec §5.2/§5.4/§12.3/§12.4 exist in the
 * frozen-version contract directory, plus the review-round-1 shape rules that
 * must not regress: parts belong to conversation_message (not agent_message),
 * nullable DB columns stay out of `required`, attachment enums match the DDL,
 * seq starts at 1, the role/kind/created_by catalogs are exact, the implemented
 * knowledge-search shape replaced the old score/version_id one, and the counts
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
const executor = read("executor-protocol.md")
const readme = read("README.md")

// 0.2.1 job kinds. The 11 frozen contract work_item kinds are the closed
// catalog; the three conversation work types are P1/P2 legacies that Task 61
// projects as conversation job_card parts. They are not work_item.kind values
// and must not widen the 11-kind catalog.
const frozenJobKinds = [
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
const conversationJobCardKinds = ["chat.turn", "requirement.curate", "design.round"]

// ---------------------------------------------------------------------------
// events.md §18: thirteen 0.2.1 event types and their required payload fields.
// ---------------------------------------------------------------------------
const eventsSection = section(events, "## 18. 0.2.1", "\n## 19.")
check(eventsSection.length > 0, "events.md has a §18 0.2.1 increment section")

const eventPayloads = {
  "version.stage_changed": ["from", "to", "reason", "stage_entered_at"],
  "plan.item_changed": ["plan_item_id", "phase", "status", "order"],
  "fact.recorded": ["fact_id", "kind", "key", "confidence", "source", "content_hash", "conflict"],
  "requirement.deduped": ["kept_ids", "obsolete_ids"],
  "requirement.merged": ["requirement_id", "source_requirement_ids"],
  "requirement.obsoleted": ["requirement_id", "status", "reason"],
  "job.progress": ["job_id", "kind", "current", "total", "status"],
  "job.finished": ["job_id", "kind", "status", "result_ref"],
  "job.canceled": ["job_id", "kind", "status"],
  "job.requeued": ["job_id", "kind", "status"],
  "case_run.blocked": ["wave_id", "case_run_id", "intent_id", "reason", "action"],
  "knowledge.auto_approved": ["knowledge_id", "kind", "scope"],
  "context.cache_stats": ["run_id", "prefix_hash", "cached_tokens", "cache_hit", "briefing_hash"],
}
for (const [eventType, fields] of Object.entries(eventPayloads)) {
  const row = eventsSection
    .split("\n")
    .find((line) => new RegExp("^\\|\\s*`" + eventType.replace(/[.]/g, "\\.") + "`\\s*\\|").test(line))
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
check(
  /job\.finished[\s\S]{0,120}cancelled/.test(eventsSection),
  "events.md documents job.finished{cancelled} as the cancel terminal event",
)
check(
  /job\.canceled[\s\S]{0,400}job\.finished[\s\S]{0,120}cancelled/.test(eventsSection),
  "events.md orders job.canceled before exactly-one job.finished{cancelled}",
)
check(
  /(?:no_executor|executor_degraded|executor_unavailable)/.test(eventsSection),
  "events.md lists case_run.blocked reasons",
)
check(
  /case_run_id[\s\S]{0,120}reason[\s\S]{0,120}(?:不重复|去重)/.test(eventsSection),
  "events.md documents case_run.blocked dedupe (same case_run+reason not repeated)",
)
check(eventsSection.includes("131") && eventsSection.includes("19"), "events.md 0.2.1 counts are 131 events / 19 audit actions")
check(/version:\{id\}[\s\S]{0,80}system/.test(eventsSection), "events.md documents 0.2.1 SSE channel scopes")
for (const kind of conversationJobCardKinds) {
  check(
    eventsSection.includes("`" + kind + "`"),
    `events.md §18 documents conversation job event kind ${kind}`,
  )
}

const auditSection18 = section(eventsSection, "### 18.3", "")
check(auditSection18.length > 0, "events.md §18.3 lists the 0.2.1 audit actions")
for (const action of [
  "knowledge.auto_approved",
  "job.canceled",
  "job.requeued",
  "requirement.deleted",
  "requirement.deduped",
  "requirement.merged",
  "requirement.obsoleted",
]) {
  check(auditSection18.includes("`" + action + "`"), `events.md audit catalog contains ${action}`)
}

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
const openapiOperationCount = [...openapiPaths.values()].reduce((total, methods) => total + methods.size, 0)
const countSchemas = (text) => {
  let inComponents = false
  let inSchemas = false
  let count = 0
  for (const line of text.split("\n")) {
    if (line === "components:") {
      inComponents = true
      continue
    }
    if (inComponents && /^  [a-zA-Z]+:/.test(line)) {
      inSchemas = line === "  schemas:"
      continue
    }
    if (inSchemas && /^    [A-Za-z][A-Za-z0-9_]*:/.test(line)) count++
  }
  return count
}
const openapiSchemaCount = countSchemas(openapi)

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
  ["/api/versions/{id}/jobs/{jobId}", "get", "作业状态（0.3 预留，deprecated）"],
  ["/api/jobs/{id}/cancel", "post", "作业取消 canonical 路由"],
  ["/api/jobs/{id}/retry", "post", "作业重试 canonical 路由"],
  ["/api/versions/{versionId}/jobs/{jobId}/cancel", "post", "作业取消版本级别名"],
  ["/api/versions/{versionId}/jobs/{jobId}/retry", "post", "作业重试版本级别名"],
  ["/api/versions/{id}/context-packets/latest", "get", "最新上下文包（admin-only）"],
  ["/api/admin/llm/configs/{id}/models/{modelId}", "patch", "模型能力 PATCH"],
  ["/api/versions/{id}/requirements/dedupe", "post", "需求草案去重"],
  ["/api/versions/{id}/requirements/merge", "post", "需求草案合并"],
  ["/api/versions/{id}/requirements/{rid}", "delete", "需求草案删除"],
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
  "KnowledgeListEnvelope",
  "KnowledgeItemView",
  "ContextPacketView",
  "ContextPacketSegment",
  "UpdateLLMModelRequest",
  "UpdateLLMConfigRequest",
  "RequirementDedupeResult",
  "RequirementMergeRequest",
  "RequirementMergeResult",
  "RequirementDeleteRequest",
]
for (const schema of schemaChecks) {
  check(new RegExp("^    " + schema + ":", "m").test(openapi), `openapi schema ${schema}`)
}
check(!/^    KnowledgeSearchHit:/m.test(openapi), "openapi removed the old KnowledgeSearchHit score schema")

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
const enumOf = (block, name) => {
  const match = /enum: \[([^\]]+)\]/.exec(propertyOf(block, name))
  return match
    ? match[1]
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean)
    : []
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
check(
  /enum: \[text, thinking, tool_call, job_card, attachment, requirement_card, case_card, error\]/.test(
    propertyOf(partViewBlock, "part_type"),
  ),
  "MessagePartView.part_type is the 8-type conversation_message_part enum",
)
check(!/enum: \[[^\]]*image/.test(propertyOf(partViewBlock, "part_type")), "image is snapshot-only, not a streamed part_type")

// 0.2.1 image part payload is {type,mime,data_ref,width,height} (R39), never
// the attachment {sha256,name,size} shape.
const messagePartBlock = schemaBlock("MessagePart")
const imageStart = messagePartBlock.indexOf("enum: [image]")
const imageBlock = imageStart < 0 ? "" : messagePartBlock.slice(imageStart)
check(imageBlock.includes("data_ref"), "openapi image part carries data_ref")
check(imageBlock.includes("image/png") && imageBlock.includes("image/webp"), "openapi image part MIME enum")
check(/width[\s\S]{0,120}minimum:\s*1/.test(imageBlock), "openapi image part width bound")
check(/height[\s\S]{0,120}minimum:\s*1/.test(imageBlock), "openapi image part height bound")
check(!/sha256:/.test(imageBlock), "openapi image part does not use the attachment sha256 field")
check(
  /enum: \[job_card\][\s\S]{0,900}cancelable/.test(messagePartBlock),
  "openapi job_card part carries optional cancelable",
)
// Task 75: a terminal failed job card also carries the typed failure code and
// the user-facing reason; both stay optional (not in the branch's required list).
// Slice from the branch's `- type: object` line, not from its `enum:` marker:
// `required:` precedes `type:` inside each oneOf branch, so an enum-anchored
// slice hides the required list and makes the optionality assertion vacuous.
const oneOfBranch = (block, marker) => {
  const anchor = block.indexOf(marker)
  if (anchor < 0) return ""
  const start = block.lastIndexOf("\n        - type: object", anchor)
  // Fail loudly instead of silently reading the previous branch's required list.
  if (start < 0) return ""
  const end = block.indexOf("\n        - type: object", anchor + marker.length)
  return block.slice(start + 1, end < 0 ? block.length : end)
}
const jobCardBranch = oneOfBranch(messagePartBlock, "enum: [job_card]")
check(/code:/.test(jobCardBranch), "openapi job_card part carries optional code")
check(/reason:/.test(jobCardBranch), "openapi job_card part carries optional reason")
check(
  /code:[\s\S]{0,160}maxLength:\s*64/.test(jobCardBranch),
  "openapi job_card.code is bounded by MessageErrorCodeMax (64)",
)
check(
  /reason:[\s\S]{0,160}maxLength:\s*2000/.test(jobCardBranch),
  "openapi job_card.reason is bounded by MessageErrorMessageMax (2000)",
)
check(
  /终态 failed/.test(jobCardBranch),
  "openapi job_card.code/reason are documented as terminal-failed fields",
)
const jobCardRequiredFields = (branch) => {
  const match = /^ {10}required: \[([^\]]*)\]$/m.exec(branch)
  return match
    ? match[1]
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean)
    : []
}
const jobCardOptionalityHolds = (branch) => {
  const required = jobCardRequiredFields(branch)
  return required.length > 0 && !required.includes("code") && !required.includes("reason")
}
check(
  jobCardOptionalityHolds(jobCardBranch),
  "openapi job_card.code/reason stay optional (absent from required)",
)
// Mutation probe: the optionality assertion is only meaningful if the slice
// actually contains the job_card branch's required list. Inject code/reason
// into that list in memory and prove the same predicate rejects it; if the
// slice goes vacuous again (required list hidden), the injection matches
// nothing and this probe fails instead of silently passing.
const mutatedJobCardBranch = jobCardBranch.replace(
  /^( {10})required: \[([^\]]*)\]$/m,
  "$1required: [$2, code, reason]",
)
check(
  mutatedJobCardBranch !== jobCardBranch,
  "mutation probe: job_card branch slice exposes its required list",
)
check(
  !jobCardOptionalityHolds(mutatedJobCardBranch),
  "mutation probe: optionality check rejects required code/reason",
)
// job_card.kind accepts the 11 frozen job kinds and the three conversation work
// types; every kind stays exact (no invented values, no dropped kind).
const jobCardKindEnum = (() => {
  if (!jobCardBranch) return []
  const kindAt = jobCardBranch.indexOf("kind:")
  if (kindAt < 0) return []
  const match = /enum: \[([^\]]+)\]/.exec(jobCardBranch.slice(kindAt))
  return match
    ? match[1]
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean)
    : []
})()
for (const kind of frozenJobKinds) {
  check(jobCardKindEnum.includes(kind), `openapi MessagePart.job_card.kind keeps frozen job kind ${kind}`)
}
for (const kind of conversationJobCardKinds) {
  check(
    jobCardKindEnum.includes(kind),
    `openapi MessagePart.job_card.kind accepts conversation work type ${kind}`,
  )
}
check(
  jobCardKindEnum.length === frozenJobKinds.length + conversationJobCardKinds.length,
  `openapi MessagePart.job_card.kind lists exactly ${frozenJobKinds.length + conversationJobCardKinds.length} kinds (got ${jobCardKindEnum.length})`,
)

const jobViewBlock = schemaBlock("JobView")
const jobViewRequired = requiredOf(jobViewBlock)
check(jobViewRequired.includes("attempts") && jobViewRequired.includes("max_attempts"), "JobView requires attempts/max_attempts")
check(!jobViewRequired.includes("current") && !jobViewRequired.includes("total"), "JobView does not require 0.3-only progress fields")
check(/取消前的真实/.test(propertyOf(jobViewBlock, "attempts")), "JobView.attempts documents the pre-cancel projection")
// JobView.kind is the same frozen 11-kind work_item catalog; a silent widening
// (e.g. accepting conversation job_card kinds) must fail here.
const jobViewKindEnum = enumOf(jobViewBlock, "kind")
check(
  jobViewKindEnum.length === frozenJobKinds.length &&
    frozenJobKinds.every((kind) => jobViewKindEnum.includes(kind)),
  `openapi JobView.kind stays exactly ${frozenJobKinds.length} frozen work_item kinds`,
)

const llmConfigBlock = schemaBlock("LLMConfig")
check(
  /enum: \[openai, openai_compatible, openai_chat_completions, openai_responses\]/.test(
    propertyOf(llmConfigBlock, "protocol"),
  ),
  "LLMConfig.protocol accepts canonical + legacy values",
)
for (const field of ["thinking_type", "reasoning_effort", "default_model_id", "models"]) {
  check(Boolean(propertyOf(llmConfigBlock, field)), `LLMConfig exposes ${field}`)
}
const llmModelBlock = schemaBlock("LLMModel")
for (const field of ["capabilities", "deprecated"]) {
  check(Boolean(propertyOf(llmModelBlock, field)), `LLMModel exposes ${field}`)
}
for (const field of ["supports_tools", "supports_vision", "supports_reasoning", "context_window", "supports_prompt_cache"]) {
  check(llmModelBlock.includes(field), `LLMModel.capabilities exposes ${field}`)
}
const updateModelBlock = schemaBlock("UpdateLLMModelRequest")
for (const field of ["context_window", "supports_tools", "supports_vision", "supports_reasoning", "supports_prompt_cache", "deprecated"]) {
  check(Boolean(propertyOf(updateModelBlock, field)), `UpdateLLMModelRequest patch field ${field}`)
}

const designCardBlock = schemaBlock("DesignCaseCard")
check(
  /enum: \[passed, failed, blocked, inconclusive\]/.test(propertyOf(designCardBlock, "execution_verdict")),
  "DesignCaseCard.execution_verdict enum",
)

const knowledgeViewBlock = schemaBlock("KnowledgeItemView")
check(Boolean(propertyOf(knowledgeViewBlock, "bindings")), "KnowledgeItemView exposes bindings")
check(Boolean(propertyOf(knowledgeViewBlock, "last_verified_at")), "KnowledgeItemView exposes last_verified_at")
check(
  requiredOf(knowledgeViewBlock).includes("bindings") && requiredOf(knowledgeViewBlock).includes("last_verified_at"),
  "KnowledgeItemView requires bindings/last_verified_at",
)

const contextPacketBlock = schemaBlock("ContextPacketView")
for (const field of ["prefix_hash", "segments", "cached_tokens", "cache_hit", "invalidation_reason", "briefing_hash"]) {
  check(Boolean(propertyOf(contextPacketBlock, field)), `ContextPacketView.${field}`)
}

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
  "cancelVersionJob",
  "retryVersionJob",
  "getLatestContextPacket",
  "updateLLMModel",
  "dedupeRequirements",
  "mergeRequirements",
  "deleteRequirement",
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

// Path block (start marker inclusive to the next two-space path key).
const pathBlock = (route) => {
  const marker = "\n  " + route + ":\n"
  const start = openapi.indexOf(marker)
  if (start < 0) return ""
  const rest = openapi.slice(start + marker.length)
  const end = rest.search(/\n  \/[^\n]+:\n/)
  return end < 0 ? rest : rest.slice(0, end)
}

// Knowledge search must match the implemented 0.2.1 shape, not the old score
// projection: query/q + five structured bindings + status + cursor/limit,
// returning the regular KnowledgeItemView page.
const searchBlock = pathBlock("/api/knowledge/search")
check(searchBlock.length > 0, "openapi knowledge search path block")
for (const parameter of [
  "name: query",
  "name: q",
  "name: system_id",
  "name: environment_id",
  "name: page_key",
  "name: api_key",
  "name: account_role",
  "KnowledgeStatusFilter",
  "Cursor",
  "Limit",
]) {
  check(searchBlock.includes(parameter), `openapi knowledge search has ${parameter}`)
}
check(searchBlock.includes("KnowledgeListEnvelope"), "openapi knowledge search returns KnowledgeListEnvelope")
check(!searchBlock.includes("KnowledgeSearchHit"), "openapi knowledge search does not return KnowledgeSearchHit")
for (const removed of ["name: version_id", "name: system_key", "score:", "match_reasons:"]) {
  check(!searchBlock.includes(removed), `openapi knowledge search removed ${removed}`)
}

// Canonical job control routes (plan Task 46) plus the version-scoped aliases
// the conversation JobCard uses. Cancel excludes waiting_human and retry
// resets attempts; GET job status is retained only as 0.3-reserved.
const canonicalCancel = pathBlock("/api/jobs/{id}/cancel")
const canonicalRetry = pathBlock("/api/jobs/{id}/retry")
const versionCancel = pathBlock("/api/versions/{versionId}/jobs/{jobId}/cancel")
const getJobBlock = pathBlock("/api/versions/{id}/jobs/{jobId}")
check(canonicalCancel.includes("operationId: cancelJob"), "openapi canonical cancel route operationId")
check(canonicalRetry.includes("operationId: retryJob"), "openapi canonical retry route operationId")
check(versionCancel.includes("operationId: cancelVersionJob"), "openapi version-scoped cancel alias")
check(versionCancel.includes("versionId"), "openapi version-scoped cancel forwards versionId")
check(/open\/claimed\/running/.test(canonicalCancel), "openapi cancel documents only open/claimed/running")
check(/waiting_human[\s\S]{0,80}(?:不可|排除|409)/.test(canonicalCancel), "openapi cancel rejects waiting_human")
check(/attempts[\s\S]{0,40}(?:重置|reset)/.test(canonicalRetry), "openapi retry documents attempts reset")
check(/job\.finished\{status:"cancelled"\}/.test(canonicalCancel), "openapi cancel documents job.finished{cancelled}")
check(getJobBlock.includes("deprecated: true"), "openapi GET job is deprecated/0.3-reserved")
check(
  /legacy[\s\S]{0,80}kind=''/.test(canonicalCancel) && /legacy[\s\S]{0,80}kind=''/.test(canonicalRetry),
  "openapi job control fences legacy kind='' work items",
)

const contextPacketPath = pathBlock("/api/versions/{id}/context-packets/latest")
check(contextPacketPath.includes("ContextPacketView"), "openapi context packet route returns ContextPacketView")
check(/admin/i.test(contextPacketPath) && /403/.test(contextPacketPath), "openapi context packet route is admin-only (403)")

const llmModelPatch = pathBlock("/api/admin/llm/configs/{id}/models/{modelId}")
check(llmModelPatch.includes("patch:"), "openapi LLM model path has PATCH")
check(llmModelPatch.includes("UpdateLLMModelRequest"), "openapi LLM model PATCH uses UpdateLLMModelRequest")

// Task 83: draft-only requirement cleanup (dedupe/delete/merge) is mounted and
// contractual. Routes stay additive on top of the frozen P1 requirement paths.
const dedupePath = pathBlock("/api/versions/{id}/requirements/dedupe")
const mergePath = pathBlock("/api/versions/{id}/requirements/merge")
const requirementDetailPath = pathBlock("/api/versions/{id}/requirements/{rid}")
check(dedupePath.includes("operationId: dedupeRequirements"), "openapi dedupe route operationId")
check(dedupePath.includes("RequirementDedupeResult"), "openapi dedupe route returns RequirementDedupeResult")
check(/draft/.test(dedupePath), "openapi dedupe route documents draft-only scanning")
check(/404/.test(dedupePath), "openapi dedupe route documents unknown-version 404")
check(mergePath.includes("operationId: mergeRequirements"), "openapi merge route operationId")
check(
  mergePath.includes("RequirementMergeRequest") && mergePath.includes("RequirementMergeResult"),
  "openapi merge route uses RequirementMergeRequest/RequirementMergeResult",
)
check(/409[\s\S]{0,200}state_conflict/.test(mergePath), "openapi merge route documents confirmed 409 state_conflict")
check(
  /invalid_argument[\s\S]{0,200}source_requirement_ids/.test(mergePath),
  "openapi merge route documents the source_requirement_ids 400 field",
)
check(requirementDetailPath.includes("operationId: deleteRequirement"), "openapi delete route operationId")
check(
  requirementDetailPath.includes("RequirementDeleteRequest"),
  "openapi delete route accepts the optional RequirementDeleteRequest body",
)
check(
  /status=obsolete/.test(requirementDetailPath) && /409[\s\S]{0,160}state_conflict/.test(requirementDetailPath),
  "openapi delete route returns obsolete detail and documents confirmed 409 state_conflict",
)

const dedupeResult = schemaBlock("RequirementDedupeResult")
check(
  requiredOf(dedupeResult).includes("kept_ids") && requiredOf(dedupeResult).includes("obsolete_ids"),
  "openapi RequirementDedupeResult requires kept_ids/obsolete_ids",
)
check(
  !/没有重复时(?:两个数组均为空|返回空数组)/.test(openapi),
  "openapi dedupe no longer claims a no-op returns two empty arrays",
)
check(
  /kept_ids:[\s\S]{0,200}仍保留的全部 draft/.test(dedupeResult),
  "openapi RequirementDedupeResult documents kept_ids as every surviving scanned draft",
)
check(
  /obsolete_ids:[\s\S]{0,200}为空表示没有需求被退休/.test(dedupeResult),
  "openapi RequirementDedupeResult documents empty obsolete_ids = nothing retired",
)
const mergeRequest = schemaBlock("RequirementMergeRequest")
const mergeRequestRequired = requiredOf(mergeRequest)
check(
  mergeRequestRequired.includes("target_requirement_id") &&
    mergeRequestRequired.includes("source_requirement_ids"),
  "openapi RequirementMergeRequest requires target/source IDs",
)
check(
  /source_requirement_ids:[\s\S]{0,120}minItems:\s*1/.test(mergeRequest) &&
    /source_requirement_ids:[\s\S]{0,160}maxItems:\s*200/.test(mergeRequest),
  "openapi RequirementMergeRequest bounds the source list (1..200)",
)
const mergeResult = schemaBlock("RequirementMergeResult")
check(
  requiredOf(mergeResult).includes("requirement") && requiredOf(mergeResult).includes("merged_ids"),
  "openapi RequirementMergeResult requires requirement/merged_ids",
)
const deleteRequest = schemaBlock("RequirementDeleteRequest")
check(
  /reason:[\s\S]{0,120}maxLength:\s*2000/.test(deleteRequest),
  "openapi RequirementDeleteRequest reason is bounded at 2000",
)

for (const needle of [
  ["openai_chat_completions", "canonical Chat Completions protocol"],
  ["openai_responses", "canonical Responses protocol"],
  ["thinking_type", "thinking mode field"],
  ["reasoning_effort", "reasoning effort field"],
  ["default_model_id", "explicit default model field"],
  ["supports_prompt_cache", "prompt-cache capability"],
  ["execution_verdict", "design execution verdict"],
  ["invalid_stage_for_gate", "stage gate error code"],
  ["no_requirements_to_confirm", "empty requirement gate error"],
  ["no_cases_to_confirm", "empty case gate error"],
  ["attachment_pending", "attachment pending error"],
  ["page_url_exists", "page URL conflict error"],
  ["invalid_protocol", "LLM protocol error"],
  ["llm_capability_mismatch", "LLM capability error"],
  ["llm_config_invalid", "LLM config error"],
  ["llm_protocol_error", "LLM upstream protocol error"],
  ["llm_auth_failed", "LLM credential error"],
  ["invalid_model", "invalid model error"],
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
  version: ["stage", "stage_entered_at", "blocker", "suspended_from"],
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

// 0.2.1 job-control persistence has no schema change: user cancel is stored as
// failed + marker + exhausted attempts, retry resets attempts, and the attempt
// log stays append-only/decoupled.
for (const needle of [
  ["cancelled by user", "cancel last_error marker"],
  ["GREATEST(attempts", "cancel exhausted-attempts marker"],
  ["重置为 0", "manual retry resets attempts"],
  ["work_item_attempt", "append-only attempt log"],
  ["marker", "cancel marker is query-excluded"],
]) {
  check(dbSection.includes(needle[0]), `db-schema.md §18 documents ${needle[1]}`)
}

// pg_trgm migration (Task 57) is committed with its GIN index.
check(dbSection.includes("pg_trgm"), "db-schema.md §18 documents pg_trgm retrieval")
check(
  dbSection.includes("knowledge_0003_trgm.sql"),
  "db-schema.md §18 names knowledge_0003_trgm.sql",
)
check(
  dbSection.includes("idx_knowledge_item_trgm") && dbSection.includes("gin_trgm_ops"),
  "db-schema.md §18 documents the GIN trigram index",
)
check(!/待提交|尚未进入已提交/.test(dbSection), "db-schema.md §18 no longer marks pg_trgm as pending")
check(
  dbSection.includes("login_session") && dbSection.includes("page:env:") && dbSection.includes("api:env:"),
  "db-schema.md §18 documents the page/api/login_session fact kinds",
)
check(
  dbSection.includes("context_packet") && /admin-only|admin only|admin/i.test(dbSection),
  "db-schema.md §18 documents the admin-only context_packet read semantics",
)
check(dbSection.includes("cancelable"), "db-schema.md §18 documents job_card.cancelable")
for (const kind of conversationJobCardKinds) {
  check(
    dbSection.includes("`" + kind + "`"),
    `db-schema.md §18 documents job_card kind ${kind}`,
  )
}

// ---------------------------------------------------------------------------
// task-types.md §9: 11 work_item kinds, 7 roles, four-way creation, created_by.
// ---------------------------------------------------------------------------
const tasksSection = section(tasks, "## 9. 0.2.1", "\n## 10.")
check(tasksSection.length > 0, "task-types.md has a §9 0.2.1 increment section")

const workItemKinds = frozenJobKinds
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
// executor-protocol.md §13: storageState capture/reuse, page-scoped close,
// visible element inventory, and network capture explicitly deferred to 0.3.
// ---------------------------------------------------------------------------
for (const needle of [
  ["storage_state_ref", "storageState ref reuse"],
  ["storage_state", "inline storageState"],
  ["include_storage_state", "opt-in storageState capture"],
  ["page_id", "page-scoped close"],
  ["visible", "element visibility inventory"],
  ["unknown storage_state_ref", "unknown ref is a cache miss"],
  ["0.3", "network capture is deferred to 0.3"],
  ["requests", "passive request buffer is not a discovery source"],
]) {
  check(executor.includes(needle[0]), `executor-protocol.md documents ${needle[1]}`)
}
check(
  /DELETE \/v1\/explore[\s\S]{0,160}page_id/.test(executor),
  "executor-protocol.md documents page-scoped DELETE /v1/explore?page_id=",
)
check(
  /网络捕获[\s\S]{0,160}(?:0\.3|推迟)/.test(executor),
  "executor-protocol.md marks network capture as 0.3",
)

// ---------------------------------------------------------------------------
// contracts/README.md: 0.2.1 status line, without rewriting the P8 totals.
// ---------------------------------------------------------------------------
check(/0\.2\.1 契约状态/.test(readme), "contracts README has a 0.2.1 status line")
for (const needle of ["181 paths", "222 operations", "281 schemas", "131 events", "draft: 0.2.1"]) {
  check(readme.includes(needle), `contracts README 0.2.1 status contains ${needle}`)
}
check(/22 个 0\.2\.1 operation/.test(readme), "contracts README documents the 22 0.2.1 operations")
check(
  readme.includes(`${openapiPaths.size} paths`) &&
    readme.includes(`${openapiOperationCount} operations`) &&
    readme.includes(`${openapiSchemaCount} schemas`),
  `contracts README status matches the parsed openapi (${openapiPaths.size}/${openapiOperationCount}/${openapiSchemaCount})`,
)

// ---------------------------------------------------------------------------
// Fix round 2: audit-before-event ordering, details.action scope, complete
// user-message part list, image/jpg alias, contract-first qualification, and
// exact frozen work_item kind catalogs (conversation job kinds never widen them).
// ---------------------------------------------------------------------------
const plain = (text) => text.replace(/[`*]/g, "")

// job.canceled is audited before it is published (job_control.go records then
// publishes); the event table must never claim the reverse.
const cancelEventRow = eventsTable18
  .split("\n")
  .find((line) => /^\|\s*`job\.canceled`\s*\|/.test(line))
check(Boolean(cancelEventRow), "events.md §18.1 has the job.canceled row")
check(
  Boolean(cancelEventRow) && !cancelEventRow.includes("先于同名审计"),
  "events.md job.canceled row does not publish before its same-named audit",
)
check(
  /先写同名审计[\s\S]{0,60}job\.finished/.test(cancelEventRow || ""),
  "events.md job.canceled row documents audit first, then the event and job.finished",
)
check(
  /先写审计再发布同名事件/.test(auditSection18),
  "events.md §18.3 audit table documents audit-before-publish for job.canceled",
)
check(
  /审计先于事件/.test(eventsSection),
  "events.md §18.2 fixes the audit-before-event ordering for job control",
)

// details.action is guaranteed only for the classified upstream LLM failures;
// config/capability/protocol-enum errors carry structured details but no action.
const errorEnvelope = plain(schemaBlock("ErrorEnvelope"))
check(
  /llm_protocol_error[\s\S]{0,80}llm_auth_failed[\s\S]{0,80}invalid_model[\s\S]{0,80}details\.action/.test(
    errorEnvelope,
  ),
  "openapi ErrorEnvelope guarantees details.action for llm_protocol_error/llm_auth_failed/invalid_model",
)
check(
  /llm_config_invalid[\s\S]{0,200}不保证 details\.action/.test(errorEnvelope),
  "openapi ErrorEnvelope scopes details.action away from llm_config_invalid/llm_capability_mismatch/invalid_protocol",
)
check(
  !plain(openapi).includes("LLM 类错误均非重试（retryable=false）且 details.action"),
  "openapi does not claim every LLM error carries details.action",
)
// The 0.2.1 overview paragraph (:218-219) must scope details.action exactly
// like ErrorEnvelope: only the classified three guarantee it. Without this the
// overview can contradict the envelope while the old exact-phrase fence passes.
const openapiOverview = plain(section(openapi, "错误码（HTTP 状态）：", "\nx-frozen:"))
check(
  /llm_protocol_error\/llm_auth_failed\/invalid_model[\s\S]{0,80}details\.action/.test(openapiOverview),
  "openapi overview guarantees details.action only for llm_protocol_error/llm_auth_failed/invalid_model",
)
check(
  /llm_config_invalid\/llm_capability_mismatch\/invalid_protocol[\s\S]{0,160}不保证\s*details\.action/.test(
    openapiOverview,
  ),
  "openapi overview scopes details.action away from llm_config_invalid/llm_capability_mismatch/invalid_protocol",
)
check(
  !plain(openapi).includes("LLM 类错误均非重试"),
  "openapi overview does not restore the universal LLM-error / details.action claim",
)

// User messages accept case_card (R95); the parts list must stay complete.
const postMessageBlock = schemaBlock("PostMessageRequest")
check(
  postMessageBlock.includes("`case_card`") &&
    /用户消息接受 `text`\/`attachment`\/`image`\/`case_card`/.test(postMessageBlock),
  "openapi PostMessageRequest user part list includes case_card",
)

// image/jpg is accepted by IsSupportedImageMIME as a jpeg alias; it must stay in
// the enum itself, not only in prose.
check(
  /enum: \[image\/png, image\/jpeg, image\/jpg, image\/webp\]/.test(imageBlock),
  "openapi image part MIME enum includes the image/jpg alias",
)

// The admin context-packet read route is contract-first until Task 48 mounts it;
// a missing packet is not_found with details.kind=context_packet (no own code).
const contextPacketPlain = plain(contextPacketPath)
check(
  /404 not_found[\s\S]{0,80}details\.kind="context_packet"/.test(contextPacketPlain),
  "openapi context packet 404 is not_found with details.kind=context_packet",
)
check(
  /Task 48/.test(contextPacketPlain) && /未挂载/.test(contextPacketPlain),
  "openapi context packet route notes it is not mounted until Task 48",
)

// The 22 documented operations are not all mounted: 4 reads are Task 62 and the
// GET job is 0.3-reserved.
check(
  /Task 62[\s\S]{0,80}契约先行/.test(plain(openapi)) && /Task 48[\s\S]{0,80}挂载/.test(plain(openapi)),
  "openapi qualifies the 22-operation list (Task 62 reads / Task 48 context packet)",
)
// The proximity bound stays at the original 400 characters (no relaxation):
// the 0.2.1 cleanup-route names were inserted before "Task 62", yet the needle
// is still within 400 characters, so a later edit that pushes the qualification
// farther away must be reviewed instead of silently widening this gate.
check(
  /22 个 0\.2\.1 operation[\s\S]{0,400}Task 62/.test(readme) &&
    readme.includes("0.3 预留") &&
    readme.includes("Task 48"),
  "contracts README qualifies the 22 operations (Task 62 reads / 0.3 GET job / Task 48 packet)",
)
check(
  /`docs\/openapi\/api\.yaml`[\s\S]{0,400}Task 85/.test(readme),
  "contracts README defers the docs/openapi/api.yaml 0.2.1 port to Task 85",
)

// Cache-first context_packet persistence is pending Task 48; the runner still
// stores the P1 builder packet. The body, not only the task label, must say so.
check(
  /当前 runner 仍保存 P1 `ContextBuilder`[\s\S]{0,80}尚未落库（Task 48）/.test(dbSection) &&
    /cache-first 持久化待 Task 48/.test(dbSection),
  "db-schema.md marks cache-first context_packet persistence pending Task 48",
)

// Frozen work_item kind catalogs stay exact: conversation work types are
// conversation job_card kinds, not work_item.kind values or filter options.
const workItemKindEnum = enumOf(workItemBlock, "kind")
check(
  workItemKindEnum.length === frozenJobKinds.length &&
    frozenJobKinds.every((kind) => workItemKindEnum.includes(kind)),
  `openapi WorkItemView.kind stays exactly ${frozenJobKinds.length} frozen work_item kinds`,
)
for (const kind of conversationJobCardKinds) {
  check(!workItemKindEnum.includes(kind), `openapi WorkItemView.kind excludes conversation work type ${kind}`)
}
check(
  /enum: \[explore\.login, explore\.page, explore\.api, explore\.coverage, explore\.gap, design\.cases, review\.cases, execute\.batch, analyze\.failure, report\.version, knowledge\.propose\]/.test(
    pathBlock("/api/versions/{id}/work-items"),
  ),
  "openapi work-items kind filter stays exactly the 11 frozen work_item kinds",
)

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
check(summary.events === 13, `summary event count = 13 (got ${summary.events})`)
check(summary.operations === 22, `summary operation count = 22 (got ${summary.operations})`)
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
    `${openapiPaths.size} paths / ${openapiOperationCount} openapi operations / ${openapiSchemaCount} schemas / ` +
    `${summary.newTables} new tables / ${summary.alteredTables} altered tables / ` +
    `${summary.kinds} work_item kinds / ${summary.roles} roles / created_by ${summary.createdBy} values`,
)

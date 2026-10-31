#!/usr/bin/env node
// P4 contract presence check (frozen mode).
//
// Asserts that the P4 delivery/chain increments are frozen in
// docs/version-V0.2/contracts and that the P0-P3 increments are still present.
// The OpenAPI document is parsed with the frontend's `yaml` dependency; the
// markdown contracts are checked with plain string searches and small regexes.
// Exits non-zero with the list of missing items.
//
// Migration *files* and `order.txt` are owned by the migration task; this check
// freezes the migration contract (contracts/migrations-order.md §6) only.
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const contractDir = path.join(root, "docs", "version-V0.2", "contracts")
const yaml = require(path.join(root, "frontend", "node_modules", "yaml"))

const failures = []
const check = (ok, message) => {
  if (!ok) failures.push(message)
}
const readContract = (name) => fs.readFileSync(path.join(contractDir, name), "utf8")
const lines = (text) =>
  text
    .trim()
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0)

// ---------------------------------------------------------------------------
// OpenAPI: identity, freeze marker, tags, 14 P4 operations.
// ---------------------------------------------------------------------------
let openapi = null
try {
  openapi = yaml.parse(readContract("openapi.yaml"))
} catch (error) {
  failures.push(`openapi.yaml does not parse: ${error.message}`)
}
check(openapi && typeof openapi === "object", "openapi parses to an object")
if (openapi && typeof openapi === "object") {
  check(openapi.info?.version === "0.2.0-p4", "missing openapi info.version 0.2.0-p4")
  check(openapi["x-frozen"] === "P4", "missing openapi x-frozen P4")
  check(openapi["x-draft"] === undefined, "frozen mode: openapi root must not keep x-draft")

  const tagNames = (openapi.tags ?? []).map((tag) => tag.name)
  for (const tag of ["Delivery", "Chain"]) {
    check(tagNames.includes(tag), `openapi missing tag ${tag}`)
  }

  const p4Ops = [
    ["/api/versions/{versionId}/issues", "get", "listIssues", "Delivery"],
    ["/api/versions/{versionId}/issues/{issueId}", "get", "getIssue", "Delivery"],
    ["/api/versions/{versionId}/issues/{issueId}/confirm", "post", "confirmIssue", "Delivery"],
    ["/api/versions/{versionId}/issues/{issueId}/dismiss", "post", "dismissIssue", "Delivery"],
    ["/api/versions/{versionId}/issues/{issueId}/reopen", "post", "reopenIssue", "Delivery"],
    ["/api/versions/{versionId}/issues/{issueId}/merge", "post", "mergeIssue", "Delivery"],
    ["/api/versions/{versionId}/reports", "get", "listReports", "Delivery"],
    ["/api/versions/{versionId}/reports/{reportId}", "get", "getReport", "Delivery"],
    ["/api/versions/{versionId}/reports/{reportId}/revisions/{revision}", "get", "getReportRevision", "Delivery"],
    ["/api/versions/{versionId}/reports/{reportId}/revisions/{revision}/html", "get", "downloadReportHTML", "Delivery"],
    ["/api/versions/{versionId}/reports/{reportId}/regenerate", "post", "regenerateReport", "Delivery"],
    ["/api/versions/{versionId}/exploration-chain", "get", "getExplorationChain", "Chain"],
    ["/api/versions/{versionId}/reopen", "post", "reopenVersion", "Versions"],
    ["/api/versions/{versionId}/requirements/{requirementId}/reopen", "post", "reopenRequirement", "Requirements"],
  ]
  check(p4Ops.length === 14, "p4 operation table must list 14 operations")
  for (const [route, method, operationId, tag] of p4Ops) {
    const operation = openapi.paths?.[route]?.[method]
    check(Boolean(operation), `openapi missing ${method.toUpperCase()} ${route}`)
    check(operation?.operationId === operationId, `${method.toUpperCase()} ${route} operationId must be ${operationId}`)
    check(Array.isArray(operation?.tags) && operation.tags.includes(tag), `${method.toUpperCase()} ${route} must use tag ${tag}`)
  }

  // P0-P3 paths must stay present (item by item, never a phase total).
  const priorPaths = lines(`
/healthz
/api/health
/api/auth/login
/api/auth/logout
/api/auth/me
/api/auth/password
/api/admin/users
/api/admin/users/{id}
/api/admin/users/{id}/reset-password
/api/admin/settings
/api/admin/settings/revisions
/api/admin/settings/{key}
/api/admin/llm/configs
/api/admin/llm/configs/{id}
/api/admin/llm/configs/{id}/test
/api/admin/llm/configs/{id}/refresh-models
/api/admin/llm/configs/{id}/models
/api/admin/llm/configs/{id}/models/{modelId}
/api/admin/llm/models
/api/admin/audit-logs
/api/events
/api/stream
/api/content
/api/content/{sha256}
/api/resources/catalog
/api/resources/suts
/api/resources/suts/{id}
/api/resources/suts/{id}/environments
/api/resources/environments/{id}
/api/resources/environments/{id}/pages
/api/resources/pages/{id}
/api/resources/environments/{id}/pages/import
/api/resources/environments/{id}/apis
/api/resources/apis/{id}
/api/resources/environments/{id}/apis/import
/api/resources/suts/{id}/databases
/api/resources/databases/{id}
/api/resources/suts/{id}/accounts
/api/resources/accounts/{id}
/api/resources/code-repos
/api/resources/code-repos/{id}
/api/versions
/api/versions/{id}
/api/versions/{id}/conversations
/api/versions/{id}/conversations/{cid}
/api/versions/{id}/conversations/{cid}/messages
/api/versions/{id}/requirements
/api/versions/{id}/requirements/confirmations
/api/versions/{id}/requirements/{rid}
/api/versions/{id}/requirements/{rid}/confirmations
/api/versions/{id}/requirements/{rid}/rollback
/api/versions/{id}/requirements/{rid}/relations
/api/versions/{id}/team-rooms/{team}
/api/versions/{id}/design/graph
/api/versions/{id}/design/overview
/api/versions/{id}/design/cases/{caseId}
/api/versions/{id}/design/confirmations
/api/versions/{id}/exports
/api/exports/{exportId}
/api/exports/{exportId}/download
/api/versions/{id}/execution/overview
/api/versions/{id}/execution/intents
/api/versions/{id}/execution/intents/{intentId}/cancel
/api/versions/{id}/waves
/api/versions/{id}/waves/{waveId}
/api/versions/{id}/waves/{waveId}/pause
/api/versions/{id}/waves/{waveId}/resume
/api/versions/{id}/waves/{waveId}/cancel
/api/versions/{id}/waves/{waveId}/case-runs
/api/versions/{id}/case-runs/{caseRunId}
/api/versions/{id}/case-runs/{caseRunId}/pause
/api/versions/{id}/case-runs/{caseRunId}/resume
/api/versions/{id}/evidence
/api/versions/{id}/evidence/{evidenceId}
/api/versions/{id}/evidence/{evidenceId}/content
/api/versions/{id}/human-requests
/api/human-requests/{humanRequestId}/resolve
/api/human-requests/{humanRequestId}/cancel
/api/admin/executors
/api/admin/executors/{executorId}/drain
/api/admin/executors/{executorId}/activate
`)
  for (const route of priorPaths) {
    check(Boolean(openapi.paths?.[route]), `P0-P3 path missing from openapi: ${route}`)
  }

  // P0-P3 schemas must stay present (item by item, never a phase total).
  const priorSchemas = lines(`
ErrorEnvelope
ListEnvelope
HealthData
User
Session
Setting
SettingRevision
LLMConfig
LLMModel
RemoteModel
LLMTestResult
AuditLogEntry
Event
EventSnapshotEnvelope
ContentObject
SUT
Environment
Page
SutApi
TargetDatabase
TestAccount
CodeRepository
Catalog
VersionSummary
Version
Binding
LoginRequest
ChangePasswordRequest
CreateUserRequest
UpdateUserRequest
ResetPasswordRequest
UpdateSettingRequest
CreateLLMConfigRequest
UpdateLLMConfigRequest
AddLLMModelRequest
CreateSUTRequest
UpdateSUTRequest
CreateEnvironmentRequest
UpdateEnvironmentRequest
PageInput
CreatePageRequest
UpdatePageRequest
ImportPagesRequest
PageImportResult
APIInput
CreateAPIRequest
UpdateAPIRequest
ImportAPIsRequest
ApiImportResult
CreateDatabaseRequest
UpdateDatabaseRequest
CreateAccountRequest
UpdateAccountRequest
CreateCodeRepositoryRequest
UpdateCodeRepositoryRequest
AccountInput
BindingInput
CreateVersionRequest
UpdateVersionRequest
ConversationSummary
Conversation
ConversationMessage
MessagePart
CreateConversationRequest
PostMessageRequest
AttachmentView
RequirementCounts
RequirementTargets
RequirementSummary
RequirementVersionView
RequirementConfirmationView
RequirementRelationView
RequirementDetail
CreateRequirementRequest
UpdateRequirementRequest
ConfirmRequirementResult
BatchConfirmRequest
BatchConfirmResult
RollbackRequest
TeamRoomPost
TeamRoomView
CaseCounts
DesignGraphStats
DesignCaseCard
DesignScenario
DesignRequirementGraph
DesignGraphView
DesignStep
DesignAssertion
DesignSource
DesignCaseVersionSummary
DesignCaseVersionDetail
DesignCaseDetail
DesignRoundView
DesignOverview
CaseConfirmationRequest
CaseConfirmationItem
CaseConfirmationResult
ExportRequest
ExportView
WaveSUTCounts
WaveCounts
CaseRunCounts
ExecutionQuotas
ExecutorStatusView
ExecutionOverview
RunWaveView
IntentView
CaseRunSummaryView
CaseRunView
CaseRunStepView
CaseRunAssertionView
EvidenceView
CaseRunDetail
HumanRequestPayload
HumanRequestResponseView
HumanRequestView
ExecutorView
CreateIntentRequest
PauseWaveRequest
PauseCaseRunRequest
ResolveHumanRequestRequest
`)
  for (const schema of priorSchemas) {
    check(Boolean(openapi.components?.schemas?.[schema]), `P0-P3 schema missing from openapi: ${schema}`)
  }

  // P4 schemas required by the contract.
  for (const schema of [
    "IssueView",
    "IssueDetailView",
    "IssueListEnvelope",
    "ReportView",
    "ReportRevisionView",
    "ReportListEnvelope",
    "ReportDetailView",
    "ChainView",
    "ChainNodeView",
    "ChainEdgeView",
    "ClosureSummary",
    "RegenerateReportRequest",
    "VersionReopenRequest",
    "RequirementReopenRequest",
  ]) {
    check(Boolean(openapi.components?.schemas?.[schema]), `openapi missing P4 schema ${schema}`)
  }

  // Version/Requirement views only grow optional fields.
  const versionSchema = openapi.components?.schemas?.Version
  check(Boolean(versionSchema?.properties?.closed_at), "Version.closed_at missing")
  check(Boolean(versionSchema?.properties?.close_reason), "Version.close_reason missing")
  const requirementSchema = openapi.components?.schemas?.RequirementSummary
  check(Boolean(requirementSchema?.properties?.closed_at), "RequirementSummary.closed_at missing")
  check(Boolean(requirementSchema?.properties?.closure), "RequirementSummary.closure missing")

  // Key P4 enums/fields.
  const issueStatus = openapi.components?.schemas?.IssueView?.properties?.status?.enum ?? []
  check(
    JSON.stringify(issueStatus) === JSON.stringify(["open", "confirmed", "dismissed", "merged"]),
    "IssueView.status enum must be open/confirmed/dismissed/merged",
  )
  const issueRefs = openapi.components?.schemas?.IssueView?.properties?.refs
  check(Boolean(issueRefs), "IssueView.refs missing")
  const report = openapi.components?.schemas?.ReportView
  check(
    JSON.stringify(report?.properties?.kind?.enum ?? []) === JSON.stringify(["wave", "version"]),
    "ReportView.kind enum mismatch",
  )
  check(
    JSON.stringify(report?.properties?.status?.enum ?? []) === JSON.stringify(["draft", "published", "stale"]),
    "ReportView.status enum mismatch",
  )
  const revision = openapi.components?.schemas?.ReportRevisionView
  for (const field of ["html_available", "pdf_available", "as_of", "input_fingerprint", "template_version", "render_status"]) {
    check(Boolean(revision?.properties?.[field]), `ReportRevisionView.${field} missing`)
  }
  check(
    revision?.properties?.pdf_available?.enum?.[0] === false,
    "ReportRevisionView.pdf_available must be const false in P4",
  )
  const chain = openapi.components?.schemas?.ChainView
  for (const field of ["snapshot_cursor", "filters", "counts", "nodes", "edges", "truncated", "truncation", "replay"]) {
    check(Boolean(chain?.properties?.[field]), `ChainView.${field} missing`)
  }
  const chainNode = openapi.components?.schemas?.ChainNodeView
  const nodeKinds = chainNode?.properties?.kind?.enum ?? []
  for (const kind of ["requirement", "scenario", "case_version", "wave", "case_run", "issue", "report", "closure"]) {
    check(nodeKinds.includes(kind), `ChainNodeView.kind missing ${kind}`)
  }
  const chainEdge = openapi.components?.schemas?.ChainEdgeView
  const edgeKinds = chainEdge?.properties?.kind?.enum ?? []
  for (const kind of ["has_scenario", "has_case", "executed_by", "contains", "raised", "reports", "closed_by", "included_in", "merged_into"]) {
    check(edgeKinds.includes(kind), `ChainEdgeView.kind missing ${kind}`)
  }

  // Report HTML download: text/html + ETag/CSP/nosniff headers.
  const reportHtml = openapi.paths?.["/api/versions/{versionId}/reports/{reportId}/revisions/{revision}/html"]?.get
  const htmlContent = reportHtml?.responses?.["200"]?.content ?? {}
  check(Boolean(htmlContent["text/html"]), "report HTML 200 response must be text/html")
  const htmlHeaders = reportHtml?.responses?.["200"]?.headers ?? {}
  for (const header of ["ETag", "Cache-Control", "X-Content-Type-Options", "Content-Security-Policy"]) {
    check(Boolean(htmlHeaders[header]), `report HTML 200 response missing header ${header}`)
  }
  check(Boolean(reportHtml?.responses?.["404"]), "report HTML must document 404")
  check(Boolean(reportHtml?.responses?.["409"]), "report HTML must document 409 report_content_corrupt")
}

// ---------------------------------------------------------------------------
// db-schema: §5 delivery ownership + §10 eight P4 tables.
// ---------------------------------------------------------------------------
const dbSchemaText = readContract("db-schema.md")
const section5Start = dbSchemaText.search(/^## 5\. 表归属/m)
const section5End = dbSchemaText.search(/^## 6\. 迁移幂等/m)
const section5 = section5Start >= 0 && section5End > section5Start ? dbSchemaText.slice(section5Start, section5End) : ""
check(section5.length > 0, "db-schema.md missing §5 table ownership")
for (const table of [
  "delivery_issue",
  "delivery_issue_ref",
  "delivery_issue_status_history",
  "delivery_report",
  "delivery_report_revision",
  "delivery_report_ref",
  "delivery_analysis_run",
]) {
  check(section5.includes(table), `db-schema.md §5 missing delivery table ${table}`)
}
check(section5.includes("chain") && /无表|不建表/.test(section5), "db-schema.md §5 must state chain has no table")

// P0-P3 tables/sections must stay present (item by item, never a phase total).
for (const priorTable of lines(`
system_setting
system_setting_revision
app_user
auth_token
llm_config
llm_model
event_channel
event
content_object
content_ref
audit_log
sut
sut_environment
sut_page
sut_api
target_database
test_account
code_repository
version
version_sut
version_sut_resource
work_item
work_item_attempt
team_room_post
team_room_digest
agent_profile
agent_run
agent_message
context_packet
requirement
requirement_version
requirement_confirmation
requirement_relation
conversation
conversation_message
conversation_summary
requirement_attachment
case_scenario
case_entity
case_version
case_step
case_assertion
case_source
case_confirmation
design_round
design_scope
export_artifact
execution_intent
run_wave
case_run
case_run_sut_context
step_result
assertion_result
evidence
evidence_ref
executor_instance
human_request
`)) {
  check(dbSchemaText.includes(priorTable), `db-schema.md missing P0-P3 table ${priorTable}`)
}
for (const [heading, freeze] of [
  ["## 7. P1 增量表结构", "frozen: P1"],
  ["## 8. P2 增量表结构", "frozen: P2"],
  ["## 9. P3 增量表结构", "frozen: P3"],
]) {
  check(dbSchemaText.includes(heading), `db-schema.md missing ${heading}`)
  check(dbSchemaText.includes(freeze), `db-schema.md missing ${freeze}`)
}

const section10Start = dbSchemaText.search(/^## 10\. P4 增量表结构/m)
const section10 = section10Start >= 0 ? dbSchemaText.slice(section10Start) : ""
check(section10.length > 0, "db-schema.md missing §10 P4 section")
check(section10.includes("frozen: P4"), "db-schema.md §10 must be marked frozen: P4")
for (const table of [
  "delivery_issue",
  "delivery_issue_ref",
  "delivery_issue_status_history",
  "delivery_report",
  "delivery_report_revision",
  "delivery_report_ref",
  "delivery_analysis_run",
  "requirement_closure",
]) {
  check(section10.includes(table), `db-schema.md §10 missing table ${table}`)
}
for (const name of [
  "pk_delivery_issue",
  "uk_delivery_issue_version_id_dedupe_key",
  "idx_delivery_issue_version_id_status_id",
  "idx_delivery_issue_merged_into_id",
  "idx_delivery_issue_created_at",
  "pk_delivery_issue_ref",
  "uk_delivery_issue_ref_issue_id_kind_ref_id_role",
  "idx_delivery_issue_ref_kind_ref_id",
  "idx_delivery_issue_ref_issue_id_id",
  "pk_delivery_issue_status_history",
  "idx_delivery_issue_status_history_issue_id_id",
  "pk_delivery_report",
  "uk_delivery_report_version_id_wave_id",
  "uk_delivery_report_version_id_kind",
  "idx_delivery_report_version_id_kind_id",
  "idx_delivery_report_current_revision_id",
  "pk_delivery_report_revision",
  "uk_delivery_report_revision_report_id_revision",
  "idx_delivery_report_revision_report_id_revision",
  "idx_delivery_report_revision_html_ref_id",
  "pk_delivery_report_ref",
  "uk_delivery_report_ref_report_revision_id_kind_ref_id",
  "idx_delivery_report_ref_kind_ref_id",
  "idx_delivery_report_ref_report_id_id",
  "pk_delivery_analysis_run",
  "uk_delivery_analysis_run_version_id_kind_dedupe_key",
  "uk_delivery_analysis_run_agent_run_id",
  "idx_delivery_analysis_run_version_id_kind_id",
  "pk_requirement_closure",
  "uk_requirement_closure_requirement_id_close_round",
  "idx_requirement_closure_requirement_id_id",
  "idx_requirement_closure_requirement_version_id",
]) {
  check(section10.includes(name), `db-schema.md §10 missing ${name}`)
}
for (const field of [
  "expected_revision",
  "merged_into_id",
  "reopen_count",
  "dedupe_key",
  "fingerprint",
  "input_fingerprint",
  "as_of",
  "template_version",
  "render_status",
  "check_result",
  "evidence_refs",
  "close_round",
]) {
  check(section10.includes(field), `db-schema.md §10 missing column ${field}`)
}
check(/append-only|追加只写/.test(section10), "db-schema.md §10 must document append-only tables")
check(section10.includes("requirement_closure"), "db-schema.md §10 missing requirement_closure")
check(
  /不删|不提供.*更新|不可变/.test(section10),
  "db-schema.md §10 must document P4 immutability invariants",
)

// ---------------------------------------------------------------------------
// Migrations: §6 four P4 files at positions 21-24.
// ---------------------------------------------------------------------------
const migrationsOrderText = readContract("migrations-order.md")
const section6Start = migrationsOrderText.search(/^## 6\. P4 增量迁移/m)
const section6End = migrationsOrderText.search(/^## 7\. /m)
const section6 = section6Start >= 0 && section6End > section6Start ? migrationsOrderText.slice(section6Start, section6End) : ""
check(section6.length > 0, "migrations-order.md missing §6 P4 section")
check(section6.includes("frozen: P4"), "migrations-order.md §6 must be marked frozen: P4")
const p4Migrations = [
  [21, "delivery_0001_issue_lifecycle.sql", "delivery"],
  [22, "delivery_0002_report_revision.sql", "delivery"],
  [23, "delivery_0003_analysis_run.sql", "delivery"],
  [24, "version_0005_requirement_closure.sql", "version"],
]
for (const [order, file, owner] of p4Migrations) {
  check(section6.includes(file), `migrations-order.md §6 missing ${file}`)
  check(
    new RegExp(`\\|\\s*${order}\\s*\\|\\s*\`${file.replace(/\./g, "\\.")}\``).test(section6),
    `migrations-order.md §6 must list ${file} as #${order}`,
  )
  check(section6.includes(owner), `migrations-order.md §6 missing ownership ${owner} for ${file}`)
}
for (const priorMigration of lines(`
settings_0001_settings.sql
identity_0001_identity.sql
llm_0001_llm.sql
eventing_0001_events.sql
storage_0001_content_ledger.sql
audit_0001_audit_log.sql
resource_0001_catalog.sql
version_0001_versions.sql
agent_0001_work_item.sql
agent_0002_team_room.sql
agent_0003_runtime.sql
version_0002_requirement.sql
version_0003_conversation.sql
version_0004_requirement_attachment.sql
eventing_0002_event_index.sql
design_0001_case_model.sql
storage_0002_export_artifact.sql
execution_0001_intent_wave.sql
execution_0002_result_evidence.sql
execution_0003_executor_human.sql
`)) {
  check(migrationsOrderText.includes(priorMigration), `migrations-order.md missing P0-P3 migration ${priorMigration}`)
}
for (const section of ["## 5a. P1 迁移清单", "## 5b. P2 迁移清单", "## 5c. P3 增量迁移"]) {
  check(migrationsOrderText.includes(section), `migrations-order.md missing ${section}`)
}

// ---------------------------------------------------------------------------
// Events: §13 ten P4 events.
// ---------------------------------------------------------------------------
const eventsText = readContract("events.md")
const section13Start = eventsText.search(/^## 13\. P4 增量事件/m)
const section13 = section13Start >= 0 ? eventsText.slice(section13Start) : ""
check(section13.length > 0, "events.md missing §13 P4 section")
check(section13.includes("frozen: P4"), "events.md §13 must be marked frozen: P4")
for (const event of [
  "issue.created",
  "issue.updated",
  "issue.reopened",
  "issue.merged",
  "report.published",
  "report.stale",
  "requirement.closed",
  "requirement.reopened",
  "version.closed",
  "version.reopened",
]) {
  check(section13.includes(event), `events.md §13 missing event ${event}`)
}
for (const payloadField of [
  "issue_id",
  "merged_into_id",
  "report_revision_id",
  "html_ref",
  "closure_id",
  "close_round",
  "close_run_id",
]) {
  check(section13.includes(payloadField), `events.md §13 missing payload field ${payloadField}`)
}
check(section13.includes("version:{id}"), "events.md §13 must document version:{id} channel")
check(/system/.test(section13), "events.md §13 must document the system channel for version events")

// P0-P3 event classes must stay present.
for (const section of ["## 10. P1 增量事件", "## 11. P2 增量事件", "## 12. P3 增量事件"]) {
  check(eventsText.includes(section), `events.md missing ${section}`)
}
for (const event of lines(`
version.started
conversation.created
requirement.created
agent.run.started
design.round_started
design.graph_delta
execution.intent_created
wave.finished
case_run.finished
case_run.step_finished
evidence.created
human_request.created
execution.failure_candidate
execution.coverage_gap
`)) {
  check(eventsText.includes(event), `events.md missing P0-P3 event ${event}`)
}

// ---------------------------------------------------------------------------
// Task types: §6 five P4 tasks, §4 no longer lists implemented types.
// ---------------------------------------------------------------------------
const taskTypesText = readContract("task-types.md")
const section6TaskStart = taskTypesText.search(/^## 6\. P4 任务类型/m)
const section6Task = section6TaskStart >= 0 ? taskTypesText.slice(section6TaskStart) : ""
check(section6Task.length > 0, "task-types.md missing §6 P4 section")
check(section6Task.includes("frozen: P4"), "task-types.md §6 must be marked frozen: P4")
for (const taskType of ["issue.triage", "report.wave", "report.version", "requirement.close", "version.close"]) {
  check(section6Task.includes(`\`${taskType}\``), `task-types.md §6 missing task ${taskType}`)
}
for (const profile of ["issue_triager", "report_writer", "version_steward"]) {
  check(section6Task.includes(profile), `task-types.md §6 missing profile ${profile}`)
}
for (const dedupe of ["triage:{issue_id}", "report:{report_id}:r{revision}", "req-close:", "ver-close:"]) {
  check(section6Task.includes(dedupe), `task-types.md §6 missing dedupe key ${dedupe}`)
}
check(section6Task.includes("predicate_fingerprint"), "task-types.md §6 missing version.close predicate_fingerprint")
const section4TaskStart = taskTypesText.search(/^## 4\. P4\+ 保留类型/m)
const section4TaskEnd = taskTypesText.search(/^## 5\. P3 任务类型/m)
const section4Task =
  section4TaskStart >= 0 && section4TaskEnd > section4TaskStart ? taskTypesText.slice(section4TaskStart, section4TaskEnd) : ""
check(section4Task.length > 0, "task-types.md §4 must remain the reserved-type section")
for (const taskType of ["issue.triage", "report.wave", "report.version", "requirement.close", "version.close"]) {
  check(!section4Task.includes(taskType), `task-types.md §4 must not reserve implemented type ${taskType}`)
}
check(section4Task.includes("report.render_pdf"), "task-types.md §4 must keep report.render_pdf reserved")

// ---------------------------------------------------------------------------
// Profiles/tools: §7 three profiles, eight tools, scope injection.
// ---------------------------------------------------------------------------
const profilesText = readContract("agent-profiles-tools.md")
const section7Start = profilesText.search(/^## 7\. P4 Profiles/m)
const section7 = section7Start >= 0 ? profilesText.slice(section7Start) : ""
check(section7.length > 0, "agent-profiles-tools.md missing §7 P4 section")
check(section7.includes("frozen: P4"), "agent-profiles-tools.md §7 must be marked frozen: P4")
for (const profile of ["issue_triager", "report_writer", "version_steward"]) {
  check(section7.includes(profile), `agent-profiles-tools.md §7 missing profile ${profile}`)
}
for (const tool of [
  "issue.read",
  "issue.propose",
  "report.data_read",
  "report.narrative_propose",
  "version.close_read",
  "requirement.goal_met",
  "version.close_propose",
  "requirement.read",
]) {
  check(section7.includes(`\`${tool}\``), `agent-profiles-tools.md §7 missing tool ${tool}`)
}
check(section7.includes("version_id"), "agent-profiles-tools.md §7 must document scope injection")
check(section7.includes("versionId"), "agent-profiles-tools.md §7 must state schemas exclude versionId")
for (const budget of ["max_turns:6", "soft_seconds:180", "hard_seconds:300"]) {
  check(section7.includes(budget), `agent-profiles-tools.md §7 missing budget ${budget}`)
}
check(
  /不得伪造|不得尝试确认|只调用白名单/.test(section7),
  "agent-profiles-tools.md §7 must document prompt constraints",
)
check(section7.includes("409"), "agent-profiles-tools.md §7 must document server-side 409 failures")
const section4bStart = profilesText.search(/^## 4b\./m)
const section4bEnd = profilesText.search(/^## 5\./m)
const section4b = profilesText.slice(section4bStart, section4bEnd)
for (const tool of ["`issue.read`", "`issue.propose`", "`report.data_read`", "`requirement.goal_met`", "`version.close_propose`"]) {
  check(!section4b.includes(tool), `agent-profiles-tools.md §4b must move enabled tool ${tool} to §7`)
}

// ---------------------------------------------------------------------------
// Settings: §4d four chain keys + report_dir/render_pdf notes.
// ---------------------------------------------------------------------------
const settingsText = readContract("settings.md")
const section4dStart = settingsText.search(/^## 4d\. P4 使用说明/m)
const section4dEnd = settingsText.search(/^## 5\. 冻结与变更控制/m)
const section4d =
  section4dStart >= 0 && section4dEnd > section4dStart ? settingsText.slice(section4dStart, section4dEnd) : ""
check(section4d.length > 0, "settings.md missing §4d P4 section")
check(section4d.includes("frozen: P4"), "settings.md §4d must be marked frozen: P4")
for (const [key, value] of [
  ["chain.max_nodes", "300"],
  ["chain.max_edges", "600"],
  ["chain.max_requirements", "100"],
  ["chain.replay_event_limit", "500"],
]) {
  check(
    new RegExp(`\\|\\s*\\d+\\s*\\|\\s*\`${key.replace(/\./g, "\\.")}\`\\s*\\|\\s*number\\s*\\|[^\\n]*\`${value}\``).test(
      settingsText,
    ),
    `settings.md missing setting ${key} default ${value}`,
  )
  check(section4d.includes(key), `settings.md §4d missing setting ${key}`)
}
check(section4d.includes("paths.report_dir"), "settings.md §4d must note paths.report_dir is unused in P4")
check(section4d.includes("report.render_pdf"), "settings.md §4d must note report.render_pdf is not consumed in P4")
check(/4 项 env|4 项.*env|严格 4/.test(section4d), "settings.md §4d must keep the 4-env rule")
for (const section of ["## 4a. P1 使用说明", "## 4b. P2 使用说明", "## 4c. P3 使用说明"]) {
  check(settingsText.includes(section), `settings.md missing ${section}`)
}

// ---------------------------------------------------------------------------
// contracts/README.md P4 frozen state.
// ---------------------------------------------------------------------------
const readmeText = readContract("README.md")
check(/P4 冻结状态|P4 契约状态/.test(readmeText), "contracts/README.md missing P4 status paragraph")
check(readmeText.includes("0.2.0-p4"), "contracts/README.md missing openapi version 0.2.0-p4")
check(readmeText.includes("Delivery") && readmeText.includes("Chain"), "contracts/README.md must mention Delivery/Chain")
check(readmeText.includes("frozen: P4"), "contracts/README.md must mention frozen: P4")

// ---------------------------------------------------------------------------
// Report.
// ---------------------------------------------------------------------------
if (failures.length > 0) {
  console.error("P4 contract check FAILED (frozen mode):")
  for (const failure of failures) console.error(" -", failure)
  process.exit(1)
}

const pathCount = Object.keys(openapi.paths).length
const operationCount = Object.values(openapi.paths).reduce((total, item) => total + Object.keys(item).length, 0)
const schemaCount = Object.keys(openapi.components.schemas).length
console.log(
  "P4 contract check OK (frozen mode): " +
    `14 P4 operations; ${pathCount} paths / ${operationCount} operations / ${schemaCount} schemas; ` +
    "8 P4 tables; 4 P4 migrations (#21-#24); 10 P4 events; " +
    "5 P4 tasks; 3 P4 profiles; 8 P4 tools; 4 P4 settings keys",
)

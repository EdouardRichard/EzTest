#!/usr/bin/env node
/*
 * P6 contract gate: verifies the P6 contract increment is present and additive.
 *
 * Scope of this task: contract files only. Migration execution / API implementation
 * checks are deliberately optional until the implementation lands:
 *   - if backend/internal/db/migrations/order.txt already contains the P6 files,
 *     their tail position and total count are enforced;
 *   - otherwise the migration implementation check is reported as skipped.
 *
 * Frozen P6 expectation:
 *   openapi 0.2.0-p6 / x-frozen: P6, 20 new operations, 18 new path items,
 *   tags Search/Workspace/AgentCenter, chain v2 params + fields.
 */
const fs = require("node:fs")
const path = require("node:path")

const root = path.resolve(__dirname, "..")
const contracts = path.join(root, "docs", "version-V0.2", "contracts")
const read = (name) =>
  fs.readFileSync(path.join(contracts, name), "utf8").replace(/\r\n/g, "\n")
let failures = 0
const check = (ok, message) => {
  if (!ok) {
    failures++
    console.error("FAIL: " + message)
  } else {
    console.log("ok: " + message)
  }
}

const openapi = read("openapi.yaml")
const db = read("db-schema.md")
const migrations = read("migrations-order.md")
const settings = read("settings.md")
const profiles = read("agent-profiles-tools.md")
const readme = read("README.md")

// ---------------------------------------------------------------------------
// OpenAPI
// ---------------------------------------------------------------------------
check(/^  version: 0\.2\.0-p6$/m.test(openapi), "openapi info.version = 0.2.0-p6")
check(/^x-frozen: P6$/m.test(openapi), "openapi x-frozen: P6")
check(!/^x-draft: P6$/m.test(openapi), "openapi has no x-draft: P6")
for (const tag of ["Search", "Workspace", "AgentCenter"]) {
  check(new RegExp("^  - name: " + tag + "$", "m").test(openapi), "openapi tag " + tag)
}

const operations = [
  "search",
  "listTodos",
  "listEvalRuns",
  "createEvalRun",
  "getEvalRun",
  "getQualityOverview",
  "listGlobalIssues",
  "listGlobalReports",
  "getAgentCatalog",
  "listAgentRuns",
  "getAgentCenterQuality",
  "listAgentProfiles",
  "getAgentProfile",
  "patchAgentProfile",
  "saveAgentPrompt",
  "resetAgentPrompt",
  "restoreAgentPrompt",
  "listAgentPromptRevisions",
  "previewAgentPrompt",
  "listAgentTools",
]
for (const operation of operations) {
  const matches = openapi.match(new RegExp("^\\s*operationId: " + operation + "$", "gm")) || []
  check(matches.length === 1, `openapi operationId ${operation} (got ${matches.length})`)
}
const operationCount = (openapi.match(/^\s*operationId:\s*/gm) || []).length
check(operationCount === 182, `openapi operations = 182 (got ${operationCount})`)

const newPaths = [
  "/api/search",
  "/api/todos",
  "/api/quality/eval-runs",
  "/api/quality/eval-runs/{id}",
  "/api/quality/overview",
  "/api/issues",
  "/api/reports",
  "/api/agent-center/catalog",
  "/api/agent-center/runs",
  "/api/agent-center/quality",
  "/api/agents/profiles",
  "/api/agents/profiles/{key}",
  "/api/agents/profiles/{key}/prompt",
  "/api/agents/profiles/{key}/prompt/reset",
  "/api/agents/profiles/{key}/prompt/restore",
  "/api/agents/profiles/{key}/prompt/revisions",
  "/api/agents/profiles/{key}/prompt/preview",
  "/api/agents/tools",
]
for (const apiPath of newPaths) {
  check(openapi.includes("\n  " + apiPath + ":\n"), "openapi path " + apiPath)
}

const newSchemas = [
  "SearchItem",
  "SearchGroup",
  "SearchResultView",
  "TodoItem",
  "TodoCounts",
  "TodoSnapshotView",
  "EvalSample",
  "EvalMetricView",
  "EvalRunView",
  "EvalRunCreateRequest",
  "EvalSummary",
  "OperationalMetric",
  "QualityOverviewView",
  "GlobalIssueView",
  "GlobalIssueListEnvelope",
  "GlobalReportView",
  "GlobalReportListEnvelope",
  "AgentRunStats",
  "CatalogProfile",
  "AgentTeam",
  "AgentCatalogView",
  "AgentRunView",
  "AgentRunListEnvelope",
  "AgentCenterQualityView",
  "ProfileBudgets",
  "ProfileView",
  "PromptVariable",
  "ProfileDetailView",
  "ProfilePatchRequest",
  "ProfilePatchResponse",
  "PromptSaveRequest",
  "PromptMutationView",
  "PromptRestoreRequest",
  "PromptRevisionView",
  "PromptRevisionListEnvelope",
  "PromptPreviewRequest",
  "PromptPreviewView",
  "AgentToolView",
  "ChainCaseRunStatusCounts",
  "ChainIssueSeverityCounts",
  "ChainIssueStatusCounts",
  "ChainCoverageCounts",
  "ChainTruncatedLayers",
  "ChainNodeMeta",
]
for (const schema of newSchemas) {
  check(new RegExp("^    " + schema + ":$", "m").test(openapi), "openapi schema " + schema)
}

for (const chainParam of [
  "ChainQ",
  "ChainAbnormalOnly",
  "ChainIssueStatus",
  "ChainIssueSeverity",
  "ChainRequirementLimit",
]) {
  check(
    new RegExp("^    " + chainParam + ":$", "m").test(openapi),
    "openapi parameter " + chainParam,
  )
  check(
    openapi.includes("$ref: '#/components/parameters/" + chainParam + "'"),
    "chain operation uses " + chainParam,
  )
}
for (const chainField of [
  "by_case_run_status",
  "by_issue_severity",
  "by_issue_status",
  "coverage",
  "degraded_layers",
  "truncated_layers",
  "event_limit",
]) {
  check(openapi.includes(chainField + ":"), "chain v2 field " + chainField)
}
check(openapi.includes("requirement_limit"), "chain v2 requirement_limit field")
check(openapi.includes("abnormal_only"), "chain v2 abnormal_only field")

// ---------------------------------------------------------------------------
// db-schema
// ---------------------------------------------------------------------------
for (const table of ["eval_run", "eval_metric", "agent_profile_prompt_revision"]) {
  check(db.includes("**`" + table + "`**"), "db-schema table " + table)
}
for (const object of [
  "pk_eval_run",
  "idx_eval_run_created_at",
  "pk_eval_metric",
  "fk_eval_metric_run",
  "idx_eval_metric_run",
  "uk_agent_profile_prompt_revision",
  "idx_agent_profile_prompt_revision_key",
]) {
  check(db.includes(object), "db-schema object " + object)
}
check(/^## 14\. P6 quality/m.test(db), "db-schema §14 P6 quality")
check(/^## 15\. P6 agent/m.test(db), "db-schema §15 P6 agent")
check(
  db.includes("P6 唯一新表 = 3 张") && db.includes("不新增其他表"),
  "db-schema states 3 new tables / no others",
)
check(
  /agent_profile\.updated_at/.test(db) && db.includes("now()"),
  "db-schema agent_profile.updated_at write path",
)

// ---------------------------------------------------------------------------
// migrations-order
// ---------------------------------------------------------------------------
check(/^## 8\. P6 增量迁移/m.test(migrations), "migrations-order §8 P6")
check(migrations.includes("quality_0001_eval.sql"), "migration quality_0001_eval.sql")
check(
  migrations.includes("agent_0005_profile_prompt_revision.sql"),
  "migration agent_0005_profile_prompt_revision.sql",
)
check(/#32/.test(migrations) && migrations.includes("32"), "migration order #32")
check(/#33/.test(migrations) && migrations.includes("33"), "migration order #33")
check(migrations.includes("order.txt") && migrations.includes("33"), "migrations-order order.txt 33")

const orderPath = path.join(root, "backend", "internal", "db", "migrations", "order.txt")
if (fs.existsSync(orderPath)) {
  const order = fs
    .readFileSync(orderPath, "utf8")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith("#"))
  if (order.includes("quality_0001_eval.sql") || order.includes("agent_0005_profile_prompt_revision.sql")) {
    check(order.length === 33, `order.txt = 33 migrations (got ${order.length})`)
    check(order[31] === "quality_0001_eval.sql", "order.txt #32 = quality_0001_eval.sql")
    check(
      order[32] === "agent_0005_profile_prompt_revision.sql",
      "order.txt #33 = agent_0005_profile_prompt_revision.sql",
    )
  } else {
    console.log("skip: order.txt P6 migrations not landed yet (contracts-only check)")
  }
} else {
  console.log("skip: order.txt not found (contracts-only check)")
}

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------
check(settings.includes("= 72 项"), "settings key count 72")
check(settings.includes("`search.max_results_per_type`"), "setting search.max_results_per_type")
check(settings.includes("`todos.max_items`"), "setting todos.max_items")
check(
  settings.includes("`quality.eval_baseline_regression`"),
  "setting quality.eval_baseline_regression",
)
check(/^## 4f\. P6 使用说明/m.test(settings), "settings §4f P6")

// ---------------------------------------------------------------------------
// agent-profiles-tools
// ---------------------------------------------------------------------------
check(/^## 9\. P6 Profiles 与提示词/m.test(profiles), "agent-profiles-tools §9 P6")
check(profiles.includes("[profile:<key>]"), "prompt marker [profile:<key>]")
check(profiles.includes("32768"), "prompt length 32768")
check(profiles.includes("agent_profile_prompt_revision"), "prompt revision table")
check(profiles.includes("agent.profile.updated"), "profile audit action")
for (const variable of [
  "Now",
  "ProfileKey",
  "ProfileName",
  "VersionID",
  "VersionTitle",
  "VersionStatus",
  "TaskType",
  "TaskGoal",
  "CaseRunID",
  "WaveID",
  "IntentID",
  "CaseVersionID",
  "CaseTitle",
  "Priority",
]) {
  check(profiles.includes("`" + variable + "`"), "prompt variable " + variable)
}
check(/下一次 run|下一 run/.test(profiles), "prompt next-run semantics")
check(/回退/.test(profiles), "prompt render fallback semantics")
check(profiles.includes("validateTemplate") || profiles.includes("模板语法"), "prompt template validation")

// ---------------------------------------------------------------------------
// README
// ---------------------------------------------------------------------------
check(/P6 冻结状态|P6 契约状态/.test(readme), "contracts README P6 status")
check(readme.includes("20") && readme.includes("Search"), "README P6 operation/search counts")
check(readme.includes("72") && readme.includes("33"), "README P6 settings/migration counts")

if (failures > 0) {
  console.error(`P6 contract check FAILED (${failures} failed needle(s))`)
  process.exit(1)
}
console.log(
  "P6 contract check OK: 20 P6 ops / 18 new path items / 3 tables / #32-#33 / 72 settings",
)

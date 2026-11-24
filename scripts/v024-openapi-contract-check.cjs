#!/usr/bin/env node
/*
 * Task 19 contract check (tracked gate in scripts/, wired into the ci.yml openapi-contract job).
 * Derives expectations from Go code where feasible (WaveCounts JSON tags from
 * execution/domain/wave.go; resource routes from server.go registerResourceRoutes)
 * so the check fails on drift, not on a hand-copied list.
 * Exit 0 = consistent; 1 = drift. Run: node scripts/v024-openapi-contract-check.cjs
 */
const fs = require("node:fs");
const path = require("node:path");
const ROOT = path.resolve(__dirname, ".."); // repo root from script location (cwd-independent)
const YAML = require(path.join(ROOT, "frontend/node_modules/yaml"));

const apiPath = path.join(ROOT, "docs/openapi/api.yaml");
const raw = fs.readFileSync(apiPath, "utf8");
const doc = YAML.parse(raw);
const schemas = doc.components.schemas;
const params = doc.components.parameters;
const failures = [];
const ok = (name, cond, detail) => { if (!cond) failures.push(name + (detail ? " :: " + detail : "")); };
const setEq = (a, b) => a.size === b.size && [...a].every((x) => b.has(x));
const diff = (a, b) => [...a].filter((x) => !b.has(x));
const keys = (o) => new Set(Object.keys(o || {}));
const req = (s) => new Set((schemas[s] && schemas[s].required) || []);
const props = (s) => keys(schemas[s] && schemas[s].properties);

// 1. No version_sut anywhere in the raw contract text.
const vsCount = (raw.match(/version_sut/g) || []).length;
ok("version_sut occurrences == 0", vsCount === 0, "found " + vsCount);

// 2. WaveCounts required + properties set-equal to the JSON tags wave.go emits.
const waveGo = fs.readFileSync(path.join(ROOT, "backend/internal/execution/domain/wave.go"), "utf8");
const wcBlock = waveGo.match(/type WaveCounts struct \{([\s\S]*?)\n\}/)[1];
const wcTags = new Set([...wcBlock.matchAll(/json:"([^",]+)(?:,omitempty)?"/g)].map((m) => m[1]));
ok("WaveCounts.required == wave.go tags", setEq(req("WaveCounts"), wcTags),
  "required-only=" + diff(req("WaveCounts"), wcTags) + " go-only=" + diff(wcTags, req("WaveCounts")));
ok("WaveCounts.properties == wave.go tags", setEq(props("WaveCounts"), wcTags),
  "props-only=" + diff(props("WaveCounts"), wcTags) + " go-only=" + diff(wcTags, props("WaveCounts")));
const scBlock = waveGo.match(/type SUTCounts struct \{([\s\S]*?)\n\}/)[1];
const scTags = new Set([...scBlock.matchAll(/json:"([^",]+)(?:,omitempty)?"/g)].map((m) => m[1]));
ok("WaveSUTCounts.properties == SUTCounts tags", setEq(props("WaveSUTCounts"), scTags));

// 3. No query parameter still named version_sut_id.
const badParams = Object.entries(params).filter(([, v]) => v && v.name === "version_sut_id").map(([k]) => k);
ok("no version_sut_id query parameter", badParams.length === 0, badParams.join(","));

// 4. Inverted resource schema shapes.
ok("SUT has environment_id/base_url/source/status", ["environment_id", "base_url", "source", "status"].every((f) => props("SUT").has(f)));
ok("SUT has pages/apis/databases", ["pages", "apis", "databases"].every((f) => props("SUT").has(f)));
ok("SUT no environments", !props("SUT").has("environments"));
ok("Environment has suts/description", ["suts", "description"].every((f) => props("Environment").has(f)));
ok("Environment no sut_id/base_url/is_default/assets", ["sut_id", "base_url", "is_default", "pages", "apis", "databases", "accounts"].every((f) => !props("Environment").has(f)));
ok("Page no environment_id", !props("Page").has("environment_id"));
ok("SutApi no environment_id", !props("SutApi").has("environment_id"));
ok("TargetDatabase no environment_id", !props("TargetDatabase").has("environment_id"));
ok("TestAccount sut_id optional", !req("TestAccount").has("sut_id"));
ok("TestAccount no environment_id", !props("TestAccount").has("environment_id"));
ok("Catalog required == environments,repositories,accounts", setEq(req("Catalog"), new Set(["environments", "repositories", "accounts"])));

// 5. Version detail + binding.
ok("Version has environment_id/binding/description/goal", ["environment_id", "binding", "description", "goal"].every((f) => props("Version").has(f)));
ok("Version no bindings/stage/stage_entered_at/blocker", ["bindings", "stage", "stage_entered_at", "blocker"].every((f) => !props("Version").has(f)));
ok("Binding has repositories(array) not singular repository", props("Binding").has("repositories") && !props("Binding").has("repository"));
ok("BindingOverview has environment_id, no sut/scope_exclude", props("BindingOverview").has("environment_id") && !props("BindingOverview").has("sut") && !props("BindingOverview").has("scope_exclude"));
ok("BindingResourceCounts no excluded/included", !props("BindingResourceCounts").has("excluded") && !props("BindingResourceCounts").has("included"));
ok("BindingUpdateRequest no repository_ids/scope_exclude", !props("BindingUpdateRequest").has("repository_ids") && !props("BindingUpdateRequest").has("scope_exclude"));
ok("BindingRepositoryRow has sut_id/code_baseline_commit, no included/source", props("BindingRepositoryRow").has("sut_id") && props("BindingRepositoryRow").has("code_baseline_commit") && !props("BindingRepositoryRow").has("included") && !props("BindingRepositoryRow").has("source"));
ok("SUTRepositoryView no source, has code_baseline_commit", !props("SUTRepositoryView").has("source") && props("SUTRepositoryView").has("code_baseline_commit"));

// 6. Requirement natural-language contract.
ok("RequirementVersionView has user_story/sut_description/covered? no target_version_suts/description",
  props("RequirementVersionView").has("user_story") && props("RequirementVersionView").has("sut_description") && !props("RequirementVersionView").has("target_version_suts") && !props("RequirementVersionView").has("description"));
ok("RequirementSummary no targets", !props("RequirementSummary").has("targets"));

// 7. Design natural-language contract.
ok("DesignAssertion has sut_id, no database_id/target_version_sut_id/target_api_id",
  props("DesignAssertion").has("sut_id") && ["database_id", "target_version_sut_id", "target_api_id"].every((f) => !props("DesignAssertion").has(f)));
ok("DesignStep has target_description, no target_version_sut_id/target_page_id/target_api_id",
  props("DesignStep").has("target_description") && ["target_version_sut_id", "target_page_id", "target_api_id"].every((f) => !props("DesignStep").has(f)));
ok("DesignCaseCard has sut_description/covered_acs, no version_sut_ids",
  props("DesignCaseCard").has("sut_description") && props("DesignCaseCard").has("covered_acs") && !props("DesignCaseCard").has("version_sut_ids"));

// 8. Handoff nested resources + V1 requirements.
ok("HandoffResources has environment/suts/truncated, no flat pages/apis/databases",
  ["environment", "suts", "truncated"].every((f) => props("HandoffResources").has(f)) && ["pages", "apis", "databases"].every((f) => !props("HandoffResources").has(f)));
ok("HandoffRequirement has user_story/sut_description, no description",
  props("HandoffRequirement").has("user_story") && props("HandoffRequirement").has("sut_description") && !props("HandoffRequirement").has("description"));

// 9. Execution / delivery / chain / codeintel rekeys.
ok("EvidenceView has sut_id not version_sut_id", props("EvidenceView").has("sut_id") && !props("EvidenceView").has("version_sut_id"));
ok("IntentView has sut_ids not version_sut_ids", props("IntentView").has("sut_ids") && !props("IntentView").has("version_sut_ids"));
ok("CaseRunStepView target_sut_id", props("CaseRunStepView").has("target_sut_id") && !props("CaseRunStepView").has("target_version_sut_id"));
ok("IssueRefView sut_id", props("IssueRefView").has("sut_id") && !props("IssueRefView").has("version_sut_id"));
ok("ChainNodeView sut_id", props("ChainNodeView").has("sut_id") && !props("ChainNodeView").has("version_sut_id"));
ok("CodeImpactCandidateView sut_id", props("CodeImpactCandidateView").has("sut_id") && !props("CodeImpactCandidateView").has("version_sut_id"));
ok("ReportRefView kind enum has sut not version_sut",
  (schemas.ReportRefView.properties.kind.enum || []).includes("sut") && !(schemas.ReportRefView.properties.kind.enum || []).includes("version_sut"));

// 10. Dead schemas removed.
["BindingInput", "AccountInput", "RequirementTargets", "ScopeExclude"].forEach((n) => ok("dead schema " + n + " absent", !schemas[n]));

// 11. Bidirectional resource routes: server.go registerResourceRoutes vs api.yaml.
const serverGo = fs.readFileSync(path.join(ROOT, "backend/internal/server/server.go"), "utf8");
const rrBlock = serverGo.match(/func registerResourceRoutes[\s\S]*?\n\}/)[0];
const goRoutes = new Set([...rrBlock.matchAll(/mux\.Handle\("([A-Z]+) (\/api\/resources\/[^"]+)"/g)].map((m) => m[1] + " " + m[2]));
const yamlRoutes = new Set();
for (const [p, item] of Object.entries(doc.paths)) {
  if (!p.startsWith("/api/resources/")) continue;
  for (const m of ["get", "post", "put", "patch", "delete"]) if (item[m]) yamlRoutes.add(m.toUpperCase() + " " + p);
}
ok("resource routes bidirectional (go vs yaml)", setEq(goRoutes, yamlRoutes),
  "go-only=[" + diff(goRoutes, yamlRoutes) + "] yaml-only=[" + diff(yamlRoutes, goRoutes) + "]");

// 12. New registrations route present.
ok("GET /api/resources/suts/{id}/registrations present", !!(doc.paths["/api/resources/suts/{id}/registrations"] || {}).get);

// 13. v0.2.4-C executor v2 / capability-domain faces (T21). Derived from Go where feasible.
// 13a. Evidence kind enum == the single Go registry (execution/domain/evidence.go EvidenceKinds()).
const evGo = fs.readFileSync(path.join(ROOT, "backend/internal/execution/domain/evidence.go"), "utf8");
const evKinds = new Set([...evGo.matchAll(/Evidence\w+\s+EvidenceKind = "([a-z_]+)"/g)].map((m) => m[1]));
ok("evidence kind registry has 11 members (8 + command/file/trace)", evKinds.size === 11, "found " + evKinds.size);
const evFilter = (params.EvidenceKindFilter && params.EvidenceKindFilter.schema && params.EvidenceKindFilter.schema.enum) || [];
ok("EvidenceKindFilter enum == Go EvidenceKinds()", setEq(new Set(evFilter), evKinds),
  "spec-only=[" + diff(new Set(evFilter), evKinds) + "] go-only=[" + diff(evKinds, new Set(evFilter)) + "]");
const evViewEnum = (schemas.EvidenceView && schemas.EvidenceView.properties && schemas.EvidenceView.properties.kind && schemas.EvidenceView.properties.kind.enum) || [];
ok("EvidenceView.kind enum == Go EvidenceKinds()", setEq(new Set(evViewEnum), evKinds),
  "spec-only=[" + diff(new Set(evViewEnum), evKinds) + "] go-only=[" + diff(evKinds, new Set(evViewEnum)) + "]");
// 13b. Skill usage ledger read route (T19) + replay trigger route (T20).
ok("GET /api/skill-usage present", !!(doc.paths["/api/skill-usage"] || {}).get);
ok("POST /api/versions/{id}/case-runs/{caseRunId}/replay present",
  !!(doc.paths["/api/versions/{id}/case-runs/{caseRunId}/replay"] || {}).post);
ok("SkillUsageView schema present", !!schemas.SkillUsageView);
ok("ReplayResultView schema present", !!schemas.ReplayResultView);
// 13c. Executor probe description must reference the v2 session route, not the deleted v1 context route.
ok("no /v1/contexts in contract text (v1 deleted, R-C2)", !raw.includes("/v1/contexts"), "stale v1 route reference");
ok("executor auth-check description references /v1/sessions", raw.includes("/v1/sessions"), "missing v2 probe route");
// 13d. C behavior-change events documented in the description (evidence.skipped / case_run.replayed).
ok("evidence.skipped event documented", raw.includes("evidence.skipped"), "missing evidence.skipped");
ok("case_run.replayed event documented", raw.includes("case_run.replayed"), "missing case_run.replayed");

// 14. v0.2.4-D streaming faces (T11). Derived from Go where feasible.
// 14a. EventChannel pattern == the Go channel whitelist (eventing/application/service.go).
const evSvcGo = fs.readFileSync(path.join(ROOT, "backend/internal/eventing/application/service.go"), "utf8");
const runPrefix = evSvcGo.match(/runChannelPrefix = "([^"]+)"/);
const chanParts = evSvcGo.match(/channelPattern = regexp\.MustCompile\(`([^`]*)` \+ runChannelPrefix \+ `([^`]*)`\)/);
ok("Go channelPattern parseable", !!(runPrefix && chanParts));
if (runPrefix && chanParts) {
  const goPattern = chanParts[1] + runPrefix[1] + chanParts[2];
  ok("EventChannel.pattern == Go channelPattern", params.EventChannel.schema.pattern === goPattern,
    "spec=" + params.EventChannel.schema.pattern + " go=" + goPattern);
}
// 14b. Admin-only transcript + activity read routes (T8/T10).
ok("GET /api/agent-center/runs/{id}/transcript present", !!(doc.paths["/api/agent-center/runs/{id}/transcript"] || {}).get);
ok("GET /api/agent-center/runs/{id}/activity present", !!(doc.paths["/api/agent-center/runs/{id}/activity"] || {}).get);
// 14c. D schemas present (field/required set-equality is the deep-check's job).
["MessageDelta", "TranscriptMessage", "TranscriptDelta", "TranscriptMessagePart", "TranscriptToolCall", "AgentActivityView"]
  .forEach((n) => ok("D schema " + n + " present", !!schemas[n]));
// 14d. stream_state enums == the version-domain authority (MessageStreamStates()).
const convGo = fs.readFileSync(path.join(ROOT, "backend/internal/version/domain/conversation.go"), "utf8");
const stateVals = {};
for (const m of convGo.matchAll(/(MessageStreamState[A-Za-z]+)\s*=\s*"([a-z]+)"/g)) stateVals[m[1]] = m[2];
const statesFn = convGo.match(/func MessageStreamStates\(\) \[\]string \{([\s\S]*?)\n\}/);
ok("Go MessageStreamStates parseable", !!statesFn);
if (statesFn) {
  const goStates = new Set([...statesFn[1].matchAll(/MessageStreamState[A-Za-z]+/g)].map((m) => stateVals[m[0]]));
  const specStates = (name) => new Set((((schemas[name] || {}).properties || {}).stream_state || {}).enum || []);
  ok("ConversationMessage.stream_state enum == Go MessageStreamStates()", setEq(specStates("ConversationMessage"), goStates),
    "spec=[" + [...specStates("ConversationMessage")] + "] go=[" + [...goStates] + "]");
  ok("TranscriptMessage.stream_state enum == Go MessageStreamStates()", setEq(specStates("TranscriptMessage"), goStates));
  ok("TranscriptDelta.stream_state enum == Go MessageStreamStates()", setEq(specStates("TranscriptDelta"), goStates));
}
// 14e. MessageDelta.part_type enum == the streaming part-type authority (StreamingMessagePartTypes()).
const partVals = {};
for (const m of convGo.matchAll(/(MessagePart[A-Za-z]+)\s*=\s*"([a-z_]+)"/g)) partVals[m[1]] = m[2];
const partsFn = convGo.match(/func StreamingMessagePartTypes\(\) \[\]string \{([\s\S]*?)\n\}/);
ok("Go StreamingMessagePartTypes parseable", !!partsFn);
if (partsFn) {
  const goParts = new Set([...partsFn[1].matchAll(/MessagePart[A-Za-z]+/g)].map((m) => partVals[m[0]]));
  const deltaParts = new Set(((schemas.MessageDelta || {}).properties || {}).part_type?.enum || []);
  ok("MessageDelta.part_type enum == Go StreamingMessagePartTypes()", setEq(deltaParts, goParts),
    "spec=[" + [...deltaParts] + "] go=[" + [...goParts] + "]");
  const viewParts = new Set(((schemas.MessagePartView || {}).properties || {}).part_type?.enum || []);
  ok("MessagePartView.part_type enum == Go StreamingMessagePartTypes()", setEq(viewParts, goParts));
}
// 14f. AgentActivityView.type enum == the activity-type authority (ActivityTypes()).
const actGo = fs.readFileSync(path.join(ROOT, "backend/internal/agent/domain/activity.go"), "utf8");
const actVals = {};
for (const m of actGo.matchAll(/(ActivityType[A-Za-z]+)\s+ActivityType\s*=\s*"([a-z_]+)"/g)) actVals[m[1]] = m[2];
const actFn = actGo.match(/func ActivityTypes\(\) \[\]string \{([\s\S]*?)\n\}/);
ok("Go ActivityTypes parseable", !!actFn);
if (actFn) {
  const goActs = new Set([...actFn[1].matchAll(/ActivityType[A-Za-z]+/g)].map((m) => actVals[m[0]]));
  const specActs = new Set(((schemas.AgentActivityView || {}).properties || {}).type?.enum || []);
  ok("AgentActivityView.type enum == Go ActivityTypes()", setEq(specActs, goActs),
    "spec=[" + [...specActs] + "] go=[" + [...goActs] + "]");
}
// 14g. D events + additive field documented; placeholder heads admitted.
ok("conversation.message.delta event documented", raw.includes("conversation.message.delta"));
ok("agent.transcript.delta event documented", raw.includes("agent.transcript.delta"));
ok("agent.activity activity_seq documented", raw.includes("activity_seq"));
ok("ConversationMessage.parts admits empty placeholder (no minItems)",
  schemas.ConversationMessage.properties.parts.minItems === undefined);

// 15. v0.2.4-E quality gate / independent judge faces (T17). Derived from Go where feasible.
// 15a. verdict/severity/review_state enums == the single design-domain authority (judgment.go).
const jGo = fs.readFileSync(path.join(ROOT, "backend/internal/design/domain/judgment.go"), "utf8");
const verdicts = new Set([...jGo.matchAll(/Verdict(?:Pass|Fail)\s*=\s*"([a-z]+)"/g)].map((m) => m[1]));
const severities = new Set([...jGo.matchAll(/Severity(?:Blocker|Important|Minor)\s*=\s*"([a-z]+)"/g)].map((m) => m[1]));
const reviewStates = new Set([...jGo.matchAll(/ReviewState[A-Za-z]+\s+ReviewState = "([a-z_]+)"/g)].map((m) => m[1]));
ok("Go judgment enums parseable (2 verdicts / 3 severities / 5 review states)",
  verdicts.size === 2 && severities.size === 3 && reviewStates.size === 5,
  "verdicts=" + verdicts.size + " severities=" + severities.size + " states=" + reviewStates.size);
const specEnum = (schema, prop) => new Set((((schemas[schema] || {}).properties || {})[prop] || {}).enum || []);
ok("DesignJudgmentView.verdict enum == Go verdicts", setEq(specEnum("DesignJudgmentView", "verdict"), verdicts),
  "spec=[" + [...specEnum("DesignJudgmentView", "verdict")] + "] go=[" + [...verdicts] + "]");
ok("DesignJudgmentFinding.severity enum == Go severities", setEq(specEnum("DesignJudgmentFinding", "severity"), severities));
// Card/detail carry the degraded "" sentinel (gate unwired/unreadable); the report endpoint
// fails closed instead, so its enum has no "".
const statesPlusDegraded = new Set([...reviewStates, ""]);
ok("DesignCaseCard.review_state enum == Go review states + '' degraded",
  setEq(specEnum("DesignCaseCard", "review_state"), statesPlusDegraded),
  "spec=[" + [...specEnum("DesignCaseCard", "review_state")] + "]");
ok("DesignCaseDetail.review_state enum == Go review states + '' degraded",
  setEq(specEnum("DesignCaseDetail", "review_state"), statesPlusDegraded));
ok("DesignJudgmentCaseView.review_state enum == Go review states (no degraded '')",
  setEq(specEnum("DesignJudgmentCaseView", "review_state"), reviewStates));
ok("DesignCaseCard.review_state required (Go non-omitempty)", req("DesignCaseCard").has("review_state"));
ok("DesignCaseDetail.review_state required (Go non-omitempty)", req("DesignCaseDetail").has("review_state"));
// 15b. override condition enum == the DOMAIN authority (design/domain/judgment.go).
// T10 (H-F10 #14) moved the Override condition constants from the application
// ports (now aliases) into the domain value object; the guard follows the
// authority so the literal parse stays non-vacuous.
const jpGo = fs.readFileSync(path.join(ROOT, "backend/internal/design/domain/judgment.go"), "utf8");
const conditions = new Set([...jpGo.matchAll(/OverrideCondition[A-Za-z]+\s*=\s*"([a-z_]+)"/g)].map((m) => m[1]));
ok("Go override conditions parseable (2)", conditions.size === 2, "found " + conditions.size);
ok("DesignJudgmentOverrideView.condition enum == Go override conditions",
  setEq(specEnum("DesignJudgmentOverrideView", "condition"), conditions));
// 15c. confirmation gate codes == the single Go authority (confirmation.go); documented.
const cfGo = fs.readFileSync(path.join(ROOT, "backend/internal/design/application/confirmation.go"), "utf8");
const gateCodes = new Set([...cfGo.matchAll(/Judgment(?:Pending|Failed)Code = "([a-z_]+)"/g)].map((m) => m[1]));
ok("Go judgment gate codes parseable (2)", gateCodes.size === 2, "found " + gateCodes.size);
for (const code of gateCodes) ok("gate code " + code + " documented", raw.includes(code));
ok("batch blocking_cases detail documented", raw.includes("blocking_cases"));
const rgGo = fs.readFileSync(path.join(ROOT, "backend/internal/design/application/review_gate.go"), "utf8");
ok("ac_coverage_gap == review_gate.go literal and documented",
  rgGo.includes('"ac_coverage_gap"') && raw.includes("ac_coverage_gap"));
ok("review gate missing_acs detail documented", raw.includes("missing_acs"));
// 15d. judgment routes: present in BOTH server.go registration and the spec (both directions).
const srvGo = fs.readFileSync(path.join(ROOT, "backend/internal/server/server.go"), "utf8");
const judgmentRoutes = [
  ["post", "/api/versions/{id}/design/judgments/{caseId}/override"],
  ["get", "/api/versions/{id}/design/judgments"],
];
for (const [m, p] of judgmentRoutes) {
  ok("route " + m.toUpperCase() + " " + p + " registered in server.go",
    srvGo.includes('mux.Handle("' + m.toUpperCase() + " " + p + '"'));
  ok("route " + m.toUpperCase() + " " + p + " present in spec", !!(doc.paths[p] || {})[m]);
}
// override is admin-only in the registration.
ok("override route admin-gated in server.go",
  /mux\.Handle\("POST \/api\/versions\/\{id\}\/design\/judgments\/\{caseId\}\/override", admin\(/.test(srvGo));
// 15e. E schemas present (field/required set-equality is the deep-check's job).
["DesignJudgmentFinding", "DesignJudgmentView", "DesignJudgmentOverrideRequest", "DesignJudgmentOverrideView",
  "DesignJudgmentCaseView", "DesignJudgmentReportView"]
  .forEach((n) => ok("E schema " + n + " present", !!schemas[n]));
// 15f. E events documented AND produced in Go (name literals in the producer files).
const eEvents = [
  ["design.judgment_submitted", "backend/internal/design/application/judgment.go"],
  ["design.judgment_overridden", "backend/internal/design/application/judgment.go"],
  ["design.rework_requested", "backend/internal/design/application/judge_scheduler.go"],
  // T10 moved the judge_exhausted emission into the design application service
  // (JudgeExhaustedEventType); the composition root no longer spells the literal.
  ["design.judge_exhausted", "backend/internal/design/application/judgment.go"],
];
for (const [ev, file] of eEvents) {
  ok("E event " + ev + " documented", raw.includes(ev));
  ok("E event " + ev + " produced in Go (" + file + ")", fs.readFileSync(path.join(ROOT, file), "utf8").includes('"' + ev + '"'));
}
// 15g. R-E2r: NEW rollback replacements are DRAFTs (legacy-only, retirement 0.3), but the
// idempotent replay returns the stored replacement AS-IS (discovery_revision.go pre-check +
// locked-superseded path; DiscoveryRevisionView comment): a pre-E legacy CONFIRMED replacement
// — or a post-E draft later human-confirmed — replays as confirmed. The enum must cover both;
// pinning [draft] alone would pin a false claim (T17 fix round 1). The misnomer event's
// semantic change is registered, not silently kept.
const caseGo = fs.readFileSync(path.join(ROOT, "backend/internal/design/domain/case.go"), "utf8");
const draftVal = (caseGo.match(/CaseStatusDraft\s*=\s*"([a-z]+)"/) || [])[1];
const confirmedVal = (caseGo.match(/CaseStatusConfirmed\s*=\s*"([a-z]+)"/) || [])[1];
ok("Go CaseStatusDraft/Confirmed parseable", draftVal === "draft" && confirmedVal === "confirmed",
  "found " + draftVal + "/" + confirmedVal);
ok("CaseRevisionRollbackView.status enum == [draft, confirmed] (R-E2r new=draft + idempotent replay carve-out)",
  setEq(specEnum("CaseRevisionRollbackView", "status"), new Set([draftVal, confirmedVal])),
  "spec=[" + [...specEnum("CaseRevisionRollbackView", "status")] + "]");
ok("rollback replay carve-out documented (confirmed 仅出现在幂等重放)",
  raw.includes("confirmed 仅出现在幂等重放"));
ok("case.revision.auto_confirmed semantic change registered (misnomer note)",
  raw.includes("case.revision.auto_confirmed") && raw.includes("语义已变更"));
ok("rollback retirement 0.3 registered", raw.includes("退役计划 0.3"));
// 15h. E settings keys documented AND registered in the settings catalog.
const catGo = fs.readFileSync(path.join(ROOT, "backend/internal/settings/application/catalog.go"), "utf8");
for (const key of ["validation.placeholder_terms", "validation.min_description_chars", "design.max_rework_rounds", "design.judge_model"]) {
  ok("E settings key " + key + " documented", raw.includes(key));
  ok("E settings key " + key + " registered in catalog.go", catGo.includes('{Key: "' + key + '"'));
}
// 15i. judgment_submit tool contract documented (model-facing six-surface authority is Go;
// the contract doc must name the tool and its no-round rule).
ok("design.judgment_submit tool contract documented", raw.includes("design.judgment_submit"));
ok("judgment_submit no-round rule documented", raw.includes("无 `round` 字段"));

// 16. v0.2.4-F execution autonomy faces (T15). Derived from Go where feasible.
// 16a. ExecutionQuotas single-global projection == the Go DTO json tags (the
// retired global/per_sut/cross_system trio is a breaking wire change, design P5).
const execViewsGo = fs.readFileSync(path.join(ROOT, "backend/internal/execution/interfaces/httpapi/views.go"), "utf8");
const eqBlock = execViewsGo.match(/type ExecutionQuotas struct \{([\s\S]*?)\n\}/);
ok("Go ExecutionQuotas parseable", !!eqBlock);
if (eqBlock) {
  const eqTags = new Set([...eqBlock[1].matchAll(/json:"([^",]+)(?:,omitempty)?"/g)].map((m) => m[1]));
  ok("ExecutionQuotas is the single global key (design P5)", setEq(eqTags, new Set(["max_concurrent_case_runs"])),
    "go=[" + [...eqTags] + "]");
  ok("ExecutionQuotas.required == Go tags", setEq(req("ExecutionQuotas"), eqTags),
    "spec-only=[" + diff(req("ExecutionQuotas"), eqTags) + "] go-only=[" + diff(eqTags, req("ExecutionQuotas")) + "]");
  ok("ExecutionQuotas.properties == Go tags", setEq(props("ExecutionQuotas"), eqTags),
    "spec-only=[" + diff(props("ExecutionQuotas"), eqTags) + "] go-only=[" + diff(eqTags, props("ExecutionQuotas")) + "]");
}
// 16b. Settings: the new key is registered AND documented; the retired trio is
// gone from the catalog (deletion delta lives in the release notes, 六.6).
const catGoF = fs.readFileSync(path.join(ROOT, "backend/internal/settings/application/catalog.go"), "utf8");
ok("execution.max_concurrent_case_runs registered in catalog.go", catGoF.includes('{Key: "execution.max_concurrent_case_runs"'));
ok("execution.max_concurrent_case_runs documented", raw.includes("execution.max_concurrent_case_runs"));
for (const key of ["execution.global_concurrency", "execution.per_sut_concurrency", "execution.cross_system_concurrency"]) {
  ok("retired quota key " + key + " absent from catalog", !catGoF.includes('{Key: "' + key + '"'));
}
// 16c. F tools documented AND declared as registry constants (the model-facing
// six-surface authority is Go; the contract doc must name the same tools).
const regGoF = fs.readFileSync(path.join(ROOT, "backend/internal/agent/application/tools/registry.go"), "utf8");
for (const tool of ["sut.read", "sut.register", "sut.asset_register", "fixture.upload", "fixture.download"]) {
  ok("F tool " + tool + " documented", raw.includes(tool));
  ok("F tool " + tool + " registry constant", regGoF.includes('= "' + tool + '"'));
}
// 16d. F events documented AND produced in Go (name literals in the producer files).
const fEvents = [
  ["sut.resolved", "backend/internal/execution/application/sut_resolution.go"],
  ["sut.asset_registered", "backend/internal/execution/application/asset_registration.go"],
  ["sut.registered", "backend/internal/execution/application/sut_registration.go"],
  ["session.saved", "backend/internal/execution/application/session_fixture.go"],
];
for (const [ev, file] of fEvents) {
  ok("F event " + ev + " documented", raw.includes(ev));
  ok("F event " + ev + " produced in Go (" + file + ")", fs.readFileSync(path.join(ROOT, file), "utf8").includes('"' + ev + '"'));
}
ok("sut.resolved ambiguous/needs_exploration payload documented", raw.includes("needs_exploration") && raw.includes("ambiguous"));
ok("sut.asset_registered actual-row status documented", raw.includes("gate_result"));
// 16e. F 409 codes documented AND present in the Go authority files.
const fCodes = [
  ["evidence_gate_failed", "backend/internal/execution/application/asset_registration.go"],
  ["database_identity_conflict", "backend/internal/resource/application/service.go"],
  ["database_disabled", "backend/internal/resource/application/service.go"],
  ["rejected_requires_fresh_proof", "backend/internal/resource/application/service.go"],
  ["code_baseline_unresolved", "backend/internal/execution/application/fingerprint.go"],
  ["replay_fingerprint_changed", "backend/internal/execution/application/replay.go"],
];
for (const [code, file] of fCodes) {
  ok("F 409 code " + code + " documented", raw.includes(code));
  ok("F 409 code " + code + " in Go (" + file + ")", fs.readFileSync(path.join(ROOT, file), "utf8").includes('"' + code + '"'));
}
// 16f. F REST faces: admin SUT reject (T11) + session fixture listing (T13).
ok("POST /api/resources/suts/{id}/reject present", !!(doc.paths["/api/resources/suts/{id}/reject"] || {}).post);
ok("GET /api/versions/{id}/session-fixtures present", !!(doc.paths["/api/versions/{id}/session-fixtures"] || {}).get);
ok("SessionFixtureView schema present", !!schemas.SessionFixtureView);
// 16g. F description section + behavior-change registrations.
ok("F increment section present", raw.includes("v0.2.4-F 执行输入链与自主反馈环"));
ok("case_run.blocked reason narrowing documented", raw.includes("reason 值集收窄"));
ok("scope-snapshot fingerprint carriers documented", raw.includes("repository_baselines") && raw.includes("environment_fingerprint"));
ok("task_pack NL fields documented", raw.includes("sut_description") && raw.includes("covered_acs") && raw.includes("target_description"));

// 17. v0.2.4-G report & deterministic closure faces (T13). Derived from Go where feasible.
// 17a. Team enums == the Go ParseTeam authority (agent/domain/team_room.go). GD-17 removed
// the steward team; the path param + room view must equal the 6-team Go set (no steward).
const teamGo = fs.readFileSync(path.join(ROOT, "backend/internal/agent/domain/team_room.go"), "utf8");
const goTeams = new Set([...teamGo.matchAll(/Team\w+\s+Team = "([a-z]+)"/g)].map((m) => m[1]));
ok("Go ParseTeam set parseable (6 teams, no steward)", goTeams.size === 6 && !goTeams.has("steward"),
  "go=[" + [...goTeams] + "]");
const teamParamEnum = new Set((params.Team && params.Team.schema && params.Team.schema.enum) || []);
ok("Team path param enum == Go ParseTeam set", setEq(teamParamEnum, goTeams),
  "spec=[" + [...teamParamEnum] + "] go=[" + [...goTeams] + "]");
ok("TeamRoomView.team enum == Go ParseTeam set",
  setEq(specEnum("TeamRoomView", "team"), goTeams), "spec=[" + [...specEnum("TeamRoomView", "team")] + "]");
// 17b. Agent catalog group enums == the converged v0.22 catalog team map (agent_center.go
// agentCenterTeams: conversation/execution), NOT the domain team enum. Code is truth: the
// catalog has emitted these since v0.22; the old [.. steward ..] enum over-promised.
const acGo = fs.readFileSync(path.join(ROOT, "backend/internal/agent/application/agent_center.go"), "utf8");
const acTeamsBlock = (acGo.match(/var agentCenterTeams = \[\]agentCenterTeamDefinition\{([\s\S]*?)\n\}/) || [])[1] || "";
const goCatalogTeams = new Set([...acTeamsBlock.matchAll(/team:\s*"([a-z]+)"/g)].map((m) => m[1]));
ok("Go agentCenterTeams parseable (conversation/execution)", setEq(goCatalogTeams, new Set(["conversation", "execution"])),
  "go=[" + [...goCatalogTeams] + "]");
ok("CatalogProfile.team enum == Go catalog team map", setEq(specEnum("CatalogProfile", "team"), goCatalogTeams),
  "spec=[" + [...specEnum("CatalogProfile", "team")] + "]");
ok("AgentTeam.team enum == Go catalog team map", setEq(specEnum("AgentTeam", "team"), goCatalogTeams),
  "spec=[" + [...specEnum("AgentTeam", "team")] + "]");
// 17c. No live contract enum promises the removed steward team/profile (grep-zero on the
// live surface; deletion notes in description text document absence, not presence).
const enumHasSteward = Object.entries(schemas).some(([, s]) =>
  Object.values((s && s.properties) || {}).some((p) => Array.isArray(p.enum) && p.enum.includes("steward")));
ok("no schema enum lists steward (GD-17 removed)", !enumHasSteward);
ok("Team param enum has no steward", !teamParamEnum.has("steward"));
// 17d. closure_kind enum == the single Go authority (version/domain/closure_kind.go ClosureKinds()).
const ckGo = fs.readFileSync(path.join(ROOT, "backend/internal/version/domain/closure_kind.go"), "utf8");
const goClosureKinds = new Set([...ckGo.matchAll(/ClosureKind\w+\s*=\s*"([a-z]+)"/g)].map((m) => m[1]));
ok("Go ClosureKinds parseable (normal/waived)", setEq(goClosureKinds, new Set(["normal", "waived"])),
  "go=[" + [...goClosureKinds] + "]");
ok("Version.closure_kind enum == Go ClosureKinds()", setEq(specEnum("Version", "closure_kind"), goClosureKinds),
  "spec=[" + [...specEnum("Version", "closure_kind")] + "]");
ok("WaiveCloseView.closure_kind enum == Go ClosureKinds()", setEq(specEnum("WaiveCloseView", "closure_kind"), goClosureKinds));
// 17e. Version.closure_kind/unverified + RequirementSummary.waived required (Go non-omitempty;
// deep-check owns full set-equality, this pins the G delta presence).
ok("Version.closure_kind/unverified required", req("Version").has("closure_kind") && req("Version").has("unverified"));
ok("RequirementSummary.waived required", req("RequirementSummary").has("waived"));
// 17f. Waiver routes present in BOTH the Go registration and the spec, admin-gated (Q7=A).
const whGo = fs.readFileSync(path.join(ROOT, "backend/internal/version/interfaces/httpapi/waiver_handler.go"), "utf8");
const waiverRoutes = [
  "/api/versions/{versionId}/waive-close",
  "/api/versions/{versionId}/requirements/{requirementId}/waive",
];
for (const p of waiverRoutes) {
  ok("waiver route POST " + p + " registered admin-gated in Go",
    whGo.includes('mux.Handle("POST ' + p + '", admin('));
  ok("waiver route POST " + p + " present in spec", !!(doc.paths[p] || {}).post);
}
ok("server.go wires RegisterWaiverRoutes behind admin",
  serverGo.includes("versionapi.RegisterWaiverRoutes(mux, admin,"));
// 17g. G waiver schemas present (field/required set-equality is the deep-check's job).
["WaiveCloseRequest", "WaiveRequirementRequest", "WaiveCloseView", "WaiveRequirementView"]
  .forEach((n) => ok("G schema " + n + " present", !!schemas[n]));
// 17h. G settings keys registered AND documented; template default flipped to "3" (R-G10b).
const catGoG = fs.readFileSync(path.join(ROOT, "backend/internal/settings/application/catalog.go"), "utf8");
for (const key of ["report.evidence_max_per_case_run", "report.evidence_max_total"]) {
  ok("G settings key " + key + " registered in catalog.go", catGoG.includes('{Key: "' + key + '"'));
  ok("G settings key " + key + " documented", raw.includes(key));
}
ok("report.template_version default \"3\" in catalog (R-G10b)",
  /Key: "report\.template_version"[^\n]*Default: str\("3"\)/.test(catGoG));
ok("report.template_version default 3 / allowed 1,2,3 documented",
  raw.includes("report.template_version") && raw.includes("默认由 `\"2\"` 改 **`\"3\"`**"));
// 17i. G events documented AND produced in Go (name literals in the producer files).
const gEvents = [
  ["report.stale", "backend/internal/delivery/application/report_service.go"],
  ["version.waived", "backend/internal/version/application/waiver_service.go"],
  ["requirement.waived", "backend/internal/version/application/waiver_service.go"],
];
for (const [ev, file] of gEvents) {
  ok("G event " + ev + " documented", raw.includes(ev));
  ok("G event " + ev + " produced in Go (" + file + ")", fs.readFileSync(path.join(ROOT, file), "utf8").includes('"' + ev + '"'));
}
ok("version.closed waived report_revision_id=\"0\" documented (T6 Minor #3)", raw.includes('report_revision_id="0"'));
ok("waiver waived_at payload documented", raw.includes("waived_at"));
// 17j. Issue structured fields (P21) exist in the domain AND are documented (report-card
// inputs, not REST IssueView wire fields).
const issueGo = fs.readFileSync(path.join(ROOT, "backend/internal/delivery/domain/types.go"), "utf8");
for (const f of ["FailedExpected", "FailedActual", "FailedReason"]) {
  ok("Issue domain structured field " + f + " present", issueGo.includes(f));
}
ok("Issue structured fields documented", raw.includes("failed_expected") && raw.includes("failed_actual") && raw.includes("failed_reason"));
// 17k. display_fingerprint internal column (delivery_0007) documented + migration present.
ok("display_fingerprint documented", raw.includes("display_fingerprint"));
ok("delivery_0007 display_fingerprint migration present",
  fs.existsSync(path.join(ROOT, "backend/internal/db/migrations/delivery_0007_v024_g_display_fingerprint.sql")));
// 17l. G description section + behavior-change registrations.
ok("G increment section present", raw.includes("v0.2.4-G 报告与确定性闭环"));
ok("P22 fingerprint updated_at exclusion documented", raw.includes("排除") && raw.includes("updated_at"));
ok("fingerprint sensitivity matrix 55 documented", raw.includes("敏感矩阵 55"));
ok("Steward/close task contract deletion notice documented (六.6)",
  raw.includes("requirement.close") && raw.includes("version.close") && raw.includes("通知期"));
ok("causal stale complete sweep documented", raw.includes("完整 sweep") && raw.includes("report.stale"));
ok("v3 human-useful report template documented", raw.includes("五要素卡片") && raw.includes("verified/unverified/failed/uncovered"));

// 18. v0.2.4-H gapfix: case_run.blocked has TWO payload shapes. The scheduler has two
// producers — publishBlockedPending (transient wait, 5 keys, no terminal) and the
// capacity-terminal escalation (8 keys incl. terminal=true). Assert each producer's
// payload separately (never a cross-block union), then require api.yaml to document
// exactly those two shapes.
const schedGo = fs.readFileSync(path.join(ROOT, "backend/internal/execution/application/scheduler.go"), "utf8");
const blockedCallStarts = [...schedGo.matchAll(/publishEvent\(ctx, s\.events, EventCaseRunBlocked/g)].map((m) => m.index);
ok("two EventCaseRunBlocked producers present", blockedCallStarts.length === 2, "found " + blockedCallStarts.length);
// The payload is the LAST map[string]any literal of each publishEvent call (the pending
// call also has a scope map; the capacity call passes scope as a variable). Read its
// balanced braces so no neighbouring call can bleed into the key set.
const blockedPayloadKeys = (callStart) => {
  const callEnd = schedGo.indexOf(", nil)", callStart);
  const callText = schedGo.slice(callStart, callEnd === -1 ? schedGo.length : callEnd);
  const maps = [...callText.matchAll(/map\[string\]any\{/g)];
  if (maps.length === 0) return null;
  const braceAt = callStart + maps[maps.length - 1].index + "map[string]any".length;
  let depth = 0;
  for (let i = braceAt; i < schedGo.length; i++) {
    if (schedGo[i] === "{") depth++;
    else if (schedGo[i] === "}") {
      depth--;
      if (depth === 0) {
        const body = schedGo.slice(braceAt + 1, i);
        return new Set([...body.matchAll(/"([^"]+)":/g)].map((m) => m[1]));
      }
    }
  }
  return null;
};
const blockedPayloads = blockedCallStarts.map(blockedPayloadKeys);
ok("both EventCaseRunBlocked producer payloads parseable", blockedPayloads.length === 2 && blockedPayloads.every(Boolean),
  "parsed=" + blockedPayloads.filter(Boolean).length);
const pendingKeys = blockedPayloads.find((s) => s && s.size === 5);
const capacityKeys = blockedPayloads.find((s) => s && s.size === 8);
ok("publishBlockedPending payload has exactly 5 keys", !!pendingKeys,
  "sizes=[" + blockedPayloads.map((s) => (s ? s.size : "unparsed")) + "]");
ok("capacity-terminal payload has exactly 8 keys", !!capacityKeys,
  "sizes=[" + blockedPayloads.map((s) => (s ? s.size : "unparsed")) + "]");
if (pendingKeys) ok("pending blocked payload has no terminal key", !pendingKeys.has("terminal"));
if (capacityKeys) ok("capacity-terminal payload has terminal key", capacityKeys.has("terminal"));
const blockedPayloadLine = raw.split(/\r?\n/).find((line) => line.includes("`case_run.blocked`："));
ok("case_run.blocked payload line present in api.yaml", !!blockedPayloadLine);
if (blockedPayloadLine && pendingKeys && capacityKeys) {
  const documentedSets = [...blockedPayloadLine.matchAll(/`\{([^}]*)\}`/g)]
    .map((m) => new Set(m[1].split(",").map((k) => k.trim()).filter(Boolean)));
  ok("api.yaml documents exactly two blocked payload shapes", documentedSets.length === 2,
    "found " + documentedSets.length);
  const documented = documentedSets.map((d) => [...d].join(","));
  ok("api.yaml pending blocked payload == publishBlockedPending keys",
    documentedSets.length === 2 && documentedSets.some((d) => setEq(d, pendingKeys)), "docs=[" + documented + "]");
  ok("api.yaml capacity blocked payload == capacity-terminal keys",
    documentedSets.length === 2 && documentedSets.some((d) => setEq(d, capacityKeys)), "docs=[" + documented + "]");
}

if (failures.length) {
  console.error("T19 CONTRACT CHECK: FAIL (" + failures.length + ")");
  for (const f of failures) console.error("  - " + f);
  process.exit(1);
}
console.log("T19 CONTRACT CHECK: PASS (" + Object.keys(schemas).length + " schemas, " + Object.keys(doc.paths).length + " paths)");

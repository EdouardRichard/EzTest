#!/usr/bin/env node
/**
 * v0.2.4 OpenAPI DEEP contract guard (T19, tracked; wired into ci.yml openapi-contract job).
 *
 *  (A) required + properties set-equality derived from Go json tags. The five shared DTOs
 *      (Page/SutApi/TargetDatabase/TestAccount/CodeRepository) discover EVERY Go source by
 *      grepping `type <name> struct` across backend/internal (json-tagged), assert a minimum
 *      source count (multi-source shape), and fail if the projections disagree (CRT-1 class).
 *  (B) per-operation query-param set-equality (BOTH directions) for execution + codeintel
 *      routes: route table parsed from EVERY handler file (all receiver types) -> handler ->
 *      that handler's actual query reads. Not tag-filtered, not module-pooled.
 *  (C) structural $ref checks, oneOf/zero-state.
 *
 * Resolves the repo root from its own location (cwd-independent). Needs frontend yaml.
 * V024_GO_OVERLAY (optional) redirects Go reads to an overlay dir so the mutant gate can
 * inject Go-side projection drift. Run: node scripts/v024-openapi-deep-check.cjs [spec.yaml]
 */
const fs = require("node:fs");
const path = require("node:path");
const ROOT = path.resolve(__dirname, "..");
const YAML = require(path.join(ROOT, "frontend/node_modules/yaml"));
const OVERLAY = process.env.V024_GO_OVERLAY || "";
const doc = YAML.parse(fs.readFileSync(process.argv[2] || path.join(ROOT, "docs/openapi/api.yaml"), "utf8"));
const schemas = doc.components.schemas;
const params = doc.components.parameters;
const failures = [];
const ok = (n, c, d) => { if (!c) failures.push(n + (d ? " :: " + d : "")); };
const setEq = (a, b) => a.size === b.size && [...a].every((x) => b.has(x));
const diff = (a, b) => [...a].filter((x) => !b.has(x));
const read = (f) => {
  if (OVERLAY) { const o = path.join(OVERLAY, f); if (fs.existsSync(o)) return fs.readFileSync(o, "utf8"); }
  return fs.readFileSync(path.join(ROOT, f), "utf8");
};

// ---- Go struct json-tag extraction ----
function structFields(file, name) {
  const m = read(file).match(new RegExp("type " + name + " struct \\{([\\s\\S]*?)\\n\\}"));
  if (!m) throw new Error("struct not found: " + name + " in " + file);
  const fields = [];
  for (const line of m[1].split("\n")) {
    const tm = line.match(/json:"([^",]+)(,omitempty)?"/);
    if (tm) fields.push({ tag: tm[1], omitempty: !!tm[2] });
  }
  return fields;
}
// recursive .go file list (non-test) under a repo-relative dir
function goFiles(dir, acc) {
  acc = acc || [];
  for (const e of fs.readdirSync(path.join(ROOT, dir), { withFileTypes: true })) {
    const rel = dir + "/" + e.name;
    if (e.isDirectory()) goFiles(rel, acc);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) acc.push(rel);
  }
  return acc;
}
let internalFiles = null;
function discoverSources(structName) {
  if (!internalFiles) internalFiles = goFiles("backend/internal");
  const re = new RegExp("type " + structName + " struct \\{([\\s\\S]*?)\\n\\}");
  const out = [];
  for (const f of internalFiles) { const m = read(f).match(re); if (m && /json:"/.test(m[1])) out.push(f); }
  return out.sort();
}

const R = "backend/internal/resource/interfaces/httpapi/dto.go";
const V = "backend/internal/version/interfaces/httpapi/dto.go";
// Shared DTOs: sources DISCOVERED by grep; min count asserted (multi-source shape guard).
const SHARED = {
  Page: { struct: "pageDTO", min: 2 },
  SutApi: { struct: "apiDTO", min: 2 },
  TargetDatabase: { struct: "databaseDTO", min: 2 },
  TestAccount: { struct: "accountDTO", min: 2 },
  CodeRepository: { struct: "codeRepositoryDTO", min: 3 },
};
// Single-projection schemas (explicit source).
const MAP = {
  SUT: [R, "sutDTO"],
  BindingSUT: [V, "sutDTO"],
  Environment: [R, "environmentDTO"],
  BindingEnvironment: [V, "environmentDTO"],
  SUTRegistration: [R, "sutRegistrationDTO"],
  SUTRepositoryView: [R, "sutRepositoryDTO"],
  Version: [V, "versionDTO"],
  VersionSummary: [V, "versionSummaryDTO"],
  Binding: [V, "bindingDTO"],
  BindingRepositoryRow: [V, "bindingRepositoryDTO"],
  BindingOverview: ["backend/internal/version/interfaces/httpapi/binding_handler.go", "bindingOverviewDTO"],
  BindingCounts: ["backend/internal/version/interfaces/httpapi/binding_handler.go", "bindingCountsDTO"],
  BindingResourceCounts: ["backend/internal/version/interfaces/httpapi/binding_handler.go", "bindingResourceCountsDTO"],
  BindingSnapshot: ["backend/internal/version/interfaces/httpapi/binding_handler.go", "bindingSnapshotDTO"],
  RequirementSummary: ["backend/internal/version/interfaces/httpapi/requirement_dto.go", "requirementSummaryDTO"],
  RequirementVersionView: ["backend/internal/version/interfaces/httpapi/requirement_dto.go", "requirementVersionDTO"],
  DesignCaseCard: ["backend/internal/design/interfaces/httpapi/dto.go", "caseCardDTO"],
  DesignScenario: ["backend/internal/design/interfaces/httpapi/dto.go", "scenarioGraphDTO"],
  DesignRequirementGraph: ["backend/internal/design/interfaces/httpapi/dto.go", "requirementGraphDTO"],
  DesignStep: ["backend/internal/design/interfaces/httpapi/dto.go", "caseStepDTO"],
  DesignAssertion: ["backend/internal/design/interfaces/httpapi/dto.go", "caseAssertionDTO"],
  DesignCaseVersionDetail: ["backend/internal/design/interfaces/httpapi/dto.go", "caseVersionDetailDTO"],
  DesignCaseVersionDetailEmpty: ["backend/internal/design/interfaces/httpapi/dto.go", "caseVersionDetailDTO"],
  DesignCaseDetail: ["backend/internal/design/interfaces/httpapi/dto.go", "caseDetailDTO"],
  WaveCounts: ["backend/internal/execution/domain/wave.go", "WaveCounts"],
  WaveSUTCounts: ["backend/internal/execution/domain/wave.go", "SUTCounts"],
  IntentView: ["backend/internal/execution/interfaces/httpapi/views.go", "IntentView"],
  CaseRunView: ["backend/internal/execution/interfaces/httpapi/views.go", "CaseRunView"],
  CaseRunStepView: ["backend/internal/execution/interfaces/httpapi/views.go", "CaseRunStepView"],
  CaseRunAssertionView: ["backend/internal/execution/interfaces/httpapi/views.go", "CaseRunAssertionView"],
  EvidenceView: ["backend/internal/execution/interfaces/httpapi/views.go", "EvidenceView"],
  HumanRequestPayload: ["backend/internal/execution/interfaces/httpapi/views.go", "HumanRequestPayloadView"],
  IssueRefView: ["backend/internal/delivery/interfaces/httpapi/dto.go", "IssueRefView"],
  ChainNodeView: ["backend/internal/chain/interfaces/httpapi/dto.go", "chainNodeDTO"],
  CodeImpactCandidateView: ["backend/internal/codeintel/interfaces/httpapi/views.go", "codeCandidateDTO"],
  ReportRefView: ["backend/internal/delivery/interfaces/httpapi/dto.go", "ReportRefView"],
  HandoffRequirement: ["backend/internal/version/interfaces/httpapi/handoff_handler.go", "handoffRequirementDTO"],
  HandoffResources: ["backend/internal/version/interfaces/httpapi/handoff_handler.go", "handoffResourcesDTO"],
  // v0.2.4-C (T21): replay trigger projection (T20) + skill usage ledger read (T19).
  ReplayResultView: ["backend/internal/execution/interfaces/httpapi/replay_handler.go", "replayResultView"],
  ReplayCommandView: ["backend/internal/execution/interfaces/httpapi/replay_handler.go", "replayCommandView"],
  ReplayAssertionView: ["backend/internal/execution/interfaces/httpapi/replay_handler.go", "replayAssertionView"],
  SkillUsageView: ["backend/internal/agent/interfaces/httpapi/skill_handler.go", "skillUsageDTO"],
  // v0.2.4-D (T11): streaming delta payloads + admin transcript/activity projections.
  MessageDelta: ["backend/internal/version/application/conversation_stream.go", "MessageDelta"],
  TranscriptMessage: ["backend/internal/agent/application/transcript_stream.go", "TranscriptMessage"],
  TranscriptDelta: ["backend/internal/agent/application/transcript_stream.go", "TranscriptDelta"],
  TranscriptMessagePart: ["backend/internal/agent/domain/runtime.go", "MessagePart"],
  TranscriptToolCall: ["backend/internal/agent/domain/runtime.go", "ToolCall"],
  AgentActivityView: ["backend/internal/agent/interfaces/httpapi/agent_center_dto.go", "agentActivityDTO"],
  // v0.2.4-E (T17): judgment fact/override/report projections + rollback draft shape.
  DesignJudgmentFinding: ["backend/internal/design/interfaces/httpapi/dto.go", "judgmentFindingDTO"],
  DesignJudgmentView: ["backend/internal/design/interfaces/httpapi/dto.go", "judgmentDTO"],
  DesignJudgmentOverrideRequest: ["backend/internal/design/interfaces/httpapi/dto.go", "overrideJudgmentRequest"],
  DesignJudgmentOverrideView: ["backend/internal/design/interfaces/httpapi/dto.go", "judgmentOverrideDTO"],
  DesignJudgmentCaseView: ["backend/internal/design/interfaces/httpapi/dto.go", "judgmentCaseDTO"],
  DesignJudgmentReportView: ["backend/internal/design/interfaces/httpapi/dto.go", "judgmentReportDTO"],
  CaseRevisionRollbackView: ["backend/internal/design/interfaces/httpapi/dto.go", "caseVersionRollbackDTO"],
  // v0.2.4-F (T15): single-global quotas projection (design P5) + session
  // fixture admin listing projection (P18, metadata only).
  ExecutionQuotas: ["backend/internal/execution/interfaces/httpapi/views.go", "ExecutionQuotas"],
  SessionFixtureView: ["backend/internal/execution/interfaces/httpapi/session_fixture_handler.go", "SessionFixtureView"],
  // v0.2.4-G (T13): two-level admin waiver request/response projections (SC-G11).
  // Both request schemas map to the shared Go waiveRequest{reason}; the per-endpoint
  // reason maxLength (500 close / 2000 requirement) is NOT a json-tag facet, so it is
  // derived from the Go domain constants further below (H-G4 #35). Version.closure_kind/
  // unverified + RequirementSummary.waived ride the existing Version/RequirementSummary
  // MAP entries (set-equality catches the G delta).
  WaiveCloseRequest: ["backend/internal/version/interfaces/httpapi/waiver_handler.go", "waiveRequest"],
  WaiveRequirementRequest: ["backend/internal/version/interfaces/httpapi/waiver_handler.go", "waiveRequest"],
  WaiveCloseView: ["backend/internal/version/interfaces/httpapi/waiver_handler.go", "waiveCloseResponseDTO"],
  WaiveRequirementView: ["backend/internal/version/interfaces/httpapi/waiver_handler.go", "waiveRequirementResponseDTO"],
  // v0.2.4-H (T16): drift-aligned projections (H-B12/G2) + request bodies, previously ungated.
  CaseRunSummaryView: ["backend/internal/execution/interfaces/httpapi/views.go", "CaseRunSummaryView"],
  CaseRunDispatchView: ["backend/internal/execution/interfaces/httpapi/views.go", "CaseRunDispatchView"],
  CaseRunUsageView: ["backend/internal/execution/interfaces/httpapi/views.go", "CaseRunUsageView"],
  CodeProposalView: ["backend/internal/codeintel/interfaces/httpapi/proposal_handler.go", "codeProposalDTO"],
  IssueTriageView: ["backend/internal/delivery/interfaces/httpapi/dto.go", "IssueTriageView"],
  CodeAnchorView: ["backend/internal/codeintel/interfaces/httpapi/anchor_handler.go", "codeAnchorDTO"],
  RetestIssueRequest: ["backend/internal/delivery/interfaces/httpapi/dto.go", "retestIssueRequest"],
  ImportPagesRequest: ["backend/internal/resource/interfaces/httpapi/dto.go", "importPagesRequest"],
  ImportAPIsRequest: ["backend/internal/resource/interfaces/httpapi/dto.go", "importAPIsRequest"],
};
// Request bodies: a JSON decoder leaves absent fields at the Go zero value, so a
// non-omitempty tag does NOT mean "required" for request DTOs (unlike response
// projections). Property set-equality still applies; required set-equality is skipped.
const REQUEST_SCHEMAS = new Set(["RetestIssueRequest", "ImportPagesRequest", "ImportAPIsRequest"]);
// Build the full source list: shared (discovered) + single (explicit).
const SOURCES = {};
const sourceCounts = {};
let multiSourceShapeOk = true;
for (const [schema, cfg] of Object.entries(SHARED)) {
  const srcs = discoverSources(cfg.struct);
  sourceCounts[schema] = srcs.length;
  const shapeOk = srcs.length >= cfg.min;
  ok(schema + " multi-source shape (>=" + cfg.min + " Go sources)", shapeOk, "found " + srcs.length + " [" + srcs.join(", ") + "]");
  if (!shapeOk) multiSourceShapeOk = false;
  SOURCES[schema] = srcs.map((f) => [f, cfg.struct]);
}
for (const [schema, src] of Object.entries(MAP)) SOURCES[schema] = [src];

for (const [schema, srcs] of Object.entries(SOURCES)) {
  if (!schemas[schema]) { ok("schema " + schema + " exists", false); continue; }
  let derived = null;
  for (const [file, struct] of srcs) {
    let fields;
    try { fields = structFields(file, struct); } catch (e) { ok("parse " + schema + " " + struct, false, e.message); continue; }
    const sig = JSON.stringify(fields.map((f) => [f.tag, f.omitempty]));
    if (derived === null) derived = { fields, sig, origin: file + ":" + struct };
    else ok(schema + " projections agree (" + derived.origin + " vs " + file + ":" + struct + ")", derived.sig === sig,
      "CRT-1 class: " + derived.origin + "=" + derived.sig + " vs " + file + ":" + struct + "=" + sig);
  }
  if (!derived) continue;
  const allTags = new Set(derived.fields.map((f) => f.tag));
  const reqTags = new Set(derived.fields.filter((f) => !f.omitempty).map((f) => f.tag));
  const specProps = new Set(Object.keys(schemas[schema].properties || {}));
  const specReq = new Set(schemas[schema].required || []);
  ok(schema + ".properties == Go json tags", setEq(specProps, allTags), "spec-only=[" + diff(specProps, allTags) + "] go-only=[" + diff(allTags, specProps) + "]");
  if (!REQUEST_SCHEMAS.has(schema))
    ok(schema + ".required == non-omitempty Go tags", setEq(specReq, reqTags), "spec-only=[" + diff(specReq, reqTags) + "] go-only=[" + diff(reqTags, specReq) + "]");
}

// ---- (C) structural ref / oneOf checks ----
const refOf = (node) => node && node.$ref;
ok("SUT keeps source/status required", (schemas.SUT.required || []).includes("source") && (schemas.SUT.required || []).includes("status"));
ok("BindingSUT has no source/status", !schemas.BindingSUT.properties.source && !schemas.BindingSUT.properties.status);
ok("Binding.environment -> BindingEnvironment", refOf(schemas.Binding.properties.environment) === "#/components/schemas/BindingEnvironment");
ok("BindingOverview.environment -> BindingEnvironment", refOf(schemas.BindingOverview.properties.environment) === "#/components/schemas/BindingEnvironment");
ok("BindingEnvironment.suts items -> BindingSUT", refOf(schemas.BindingEnvironment.properties.suts.items) === "#/components/schemas/BindingSUT");
const sel = schemas.DesignCaseDetail.properties.selected;
ok("DesignCaseDetail.selected in required", (schemas.DesignCaseDetail.required || []).includes("selected"));
ok("DesignCaseDetail.selected oneOf(full|empty), not nullable", !!(sel.oneOf && sel.oneOf.length === 2 && !sel.nullable
  && refOf(sel.oneOf[0]) === "#/components/schemas/DesignCaseVersionDetail"
  && refOf(sel.oneOf[1]) === "#/components/schemas/DesignCaseVersionDetailEmpty"));
const empty = schemas.DesignCaseVersionDetailEmpty;
ok("DesignCaseVersionDetailEmpty zero shape (case_version_id='0', revision=0)",
  empty.properties.case_version_id.enum[0] === "0" && empty.properties.revision.enum[0] === 0);

// ---- (D) drift-shape assertions: pattern / nullable / minItems / maxItems / per-endpoint limits ----
// The MAP set-equality above only compares json-tag and required sets. These assertions cover
// the constraint facets it cannot see, tying each to the Go source that produces the value.
const schemaProp = (name, field) => (schemas[name] && schemas[name].properties && schemas[name].properties[field]) || null;
// A raw pattern must be present AND accept "": deleting the pattern must NOT pass silently.
const allowsEmpty = (pattern) => typeof pattern === "string" && pattern.length > 0 && new RegExp(pattern).test("");

// nullability must match the Go projection: pointer fields emit null, value strings never do.
const NULLABLE = [
  ["CaseRunSummaryView", "human_request_id", true],
  ["CaseRunSummaryView", "dispatch", true],
  ["CaseRunSummaryView", "usage", true],
  ["CaseRunSummaryView", "last_error", false],
  ["CaseRunSummaryView", "terminal_cause", false],
  ["CaseRunSummaryView", "next_action", false],
  ["CaseRunSummaryView", "retry_reason", false],
  ["CaseRunDispatchView", "last_heartbeat", true],
  ["CodeProposalView", "url", false],
  ["CodeProposalView", "route_template", false],
  ["CodeProposalView", "method", false],
  ["CodeProposalView", "path_template", false],
  ["IssueTriageView", "analysis_run_id", true],
  ["IssueTriageView", "status", true],
  ["IssueTriageView", "classification", true],
  ["IssueTriageView", "duplicate_of_issue_id", true],
  ["IssueTriageView", "summary", true],
  ["CodeAnchorView", "excerpt_sha", false],
];
for (const [schema, field, nullable] of NULLABLE) {
  const p = schemaProp(schema, field);
  ok(schema + "." + field + " nullable==" + nullable, !!p && (p.nullable === true) === nullable,
    p ? "spec nullable=" + p.nullable : "property missing");
}

// H-B12 item 2: CaseRunView.case_id may be "". handler.go caseVersionProjection sets
// CaseID only when label.CaseID > 0, so a superseded/stale run emits the empty string.
const caseId = schemaProp("CaseRunView", "case_id");
ok("CaseRunView.case_id pattern accepts '' (superseded/stale run)", !!caseId && allowsEmpty(caseId.pattern),
  caseId ? "pattern=" + caseId.pattern : "property missing");

// H-B12 item 8: import bodies accept an empty batch (service returns created=0/skipped=0).
for (const name of ["ImportPagesRequest", "ImportAPIsRequest"]) {
  const p = schemaProp(name, "items");
  ok(name + ".items has no minItems (empty batch is a no-op)", !!p && !(p.minItems > 0), p ? "minItems=" + p.minItems : "property missing");
}

// H-B12 item 5: retest selection has no enforced cap (dto.parse only dedups; the service
// filters the selection to case_runs already linked to the issue — no 100-item bound).
const retest = schemaProp("RetestIssueRequest", "case_run_ids");
ok("RetestIssueRequest.case_run_ids has no maxItems (code enforces no cap)", !!retest && !(retest.maxItems > 0),
  retest ? "maxItems=" + retest.maxItems : "property missing");

// H-B12 item 3: one shared CodeLimit cannot describe three different handler limits. Assert
// each operation's limit default/maximum equals the listLimit(def,max) its handler passes.
function handlerListLimit(file, fn) {
  const m = read(file).match(new RegExp("func \\(h \\*\\w+\\) " + fn + "\\([^)]*\\) \\{([\\s\\S]*?)\\n\\}"));
  if (!m) return null;
  const lm = m[1].match(/listLimit\(r,\s*(\d+),\s*(\d+)\)/);
  return lm ? { def: Number(lm[1]), max: Number(lm[2]) } : null;
}
function opLimitSchema(pathKey) {
  const op = doc.paths[pathKey] && doc.paths[pathKey].get;
  if (!op) return null;
  for (const p of op.parameters || []) {
    if (p.$ref) { const r = params[p.$ref.split("/").pop()]; if (r && r.in === "query" && r.name === "limit") return r.schema; }
    else if (p.in === "query" && p.name === "limit") return p.schema;
  }
  return null;
}
const CI = "backend/internal/codeintel/interfaces/httpapi";
const LIMIT_OPS = [
  { path: "/api/code/repos/{repoId}/tree", file: CI + "/index_handler.go", fn: "Tree" },
  { path: "/api/code/repos/{repoId}/facts", file: CI + "/analysis_handler.go", fn: "Facts" },
  { path: "/api/code/repos/{repoId}/search", file: CI + "/analysis_handler.go", fn: "Search" },
];
for (const spec of LIMIT_OPS) {
  const code = handlerListLimit(spec.file, spec.fn);
  const sch = opLimitSchema(spec.path);
  // Loud on parse failure: if the handler is refactored so listLimit() can no longer be
  // located, the limit assertions must disappear into a FAILURE, never silently.
  ok("listLimit parsed: " + spec.fn, !!code, "handlerListLimit returned null (handler/regex drift) — limit assertions would silently skip");
  ok("limit schema present: " + spec.path, !!sch);
  if (code && sch) {
    ok(spec.path + " limit default == handler listLimit default (" + code.def + ")", sch.default === code.def, "spec default=" + sch.default);
    ok(spec.path + " limit maximum == handler listLimit max (" + code.max + ")", sch.maximum === code.max, "spec maximum=" + sch.maximum);
  }
}
// positive maxItems pins: list envelopes must match the handler listLimit max (tree/facts/search).
const treeCode = handlerListLimit(CI + "/index_handler.go", "Tree");
const factsCode = handlerListLimit(CI + "/analysis_handler.go", "Facts");
const searchCode = handlerListLimit(CI + "/analysis_handler.go", "Search");
ok("CodeTreeListEnvelope.data maxItems == tree listLimit max",
  !!treeCode && !!schemas.CodeTreeListEnvelope && schemas.CodeTreeListEnvelope.properties.data.maxItems === treeCode.max);
ok("CodeFactListEnvelope.data maxItems == facts listLimit max",
  !!factsCode && !!schemas.CodeFactListEnvelope && schemas.CodeFactListEnvelope.properties.data.maxItems === factsCode.max);
ok("CodeEntityListEnvelope.data maxItems == search listLimit max",
  !!searchCode && !!schemas.CodeEntityListEnvelope && schemas.CodeEntityListEnvelope.properties.data.maxItems === searchCode.max);

// H-G4 #35: the two waiver reason caps are per-endpoint (500 close / 2000 requirement)
// and are NOT json-tag facets, so the MAP set-equality above cannot see them. Derive each
// from the Go domain constant the service actually enforces and tie the spec maxLength to
// it — a domain-constant change must fail here, not drift silently.
function goIntConst(file, name) {
  const m = read(file).match(new RegExp("\\b" + name + "\\s*=\\s*(\\d+)\\b"));
  return m ? Number(m[1]) : null;
}
const WAIVE_REASON_MAX = [
  { schema: "WaiveCloseRequest", file: "backend/internal/version/domain/version_close.go", constName: "VersionCloseReasonMax" },
  { schema: "WaiveRequirementRequest", file: "backend/internal/version/domain/requirement.go", constName: "RequirementReasonMax" },
];
for (const w of WAIVE_REASON_MAX) {
  const want = goIntConst(w.file, w.constName);
  // Loud on parse failure: a renamed/removed domain constant must FAIL, never silently
  // skip the derived comparison.
  ok("go const parsed: " + w.constName, Number.isInteger(want), "goIntConst=" + want + " (" + w.file + ")");
  const reason = schemaProp(w.schema, "reason");
  ok(w.schema + ".reason exists", !!reason, "property missing");
  if (reason) {
    ok(w.schema + ".reason.maxLength == " + w.constName, Number.isInteger(want) && reason.maxLength === want,
      "spec maxLength=" + reason.maxLength + " go=" + want);
  }
}

// H-C1: the union endpoint writes httpx.List, so `data` is a LIST envelope (array of
// CodeImpactCandidateView), not the retired CodeImpactView object.
const impactResp = doc.paths["/api/versions/{versionId}/code/impact"] &&
  doc.paths["/api/versions/{versionId}/code/impact"].get.responses["200"].content["application/json"].schema;
const impactEnv = impactResp && impactResp.$ref ? schemas[impactResp.$ref.split("/").pop()] : impactResp;
const impactData = impactEnv && impactEnv.properties && impactEnv.properties.data;
ok("getCodeImpact data is a CodeImpactCandidateView array envelope",
  !!(impactData && impactData.type === "array" && impactData.items && impactData.items.$ref === "#/components/schemas/CodeImpactCandidateView"),
  impactData ? "data type=" + impactData.type : "data missing");

// ---- (B) per-operation query reads, all handler files / receiver types, both directions ----
const execDir = "backend/internal/execution/interfaces/httpapi";
const ciDir = "backend/internal/codeintel/interfaces/httpapi";
const ctxFiles = [...goFiles(execDir), ...goFiles(ciDir)];
// index handler bodies by file::name and by name (file-specific wins)
const bodies = {};
for (const f of ctxFiles) {
  const src = read(f);
  for (const m of src.matchAll(/func \(h \*\w+\) (\w+)\([^)]*\) \{([\s\S]*?)\n\}/g)) {
    bodies[f + "::" + m[1]] = m[2];
    if (!(m[1] in bodies)) bodies[m[1]] = m[2];
  }
}
// route table from EVERY ctx file, both registration patterns
const routeMap = {}; // "METHOD path" -> {name, file}
for (const f of ctxFiles) {
  const src = read(f);
  for (const m of src.matchAll(/mux\.Handle\("([A-Z]+) ([^"]+)",\s*(?:authenticated|admin)\(http\.HandlerFunc\(h\.(\w+)\)\)\)/g))
    routeMap[m[1] + " " + m[2]] = { name: m[3], file: f };
  for (const m of src.matchAll(/handle\("([A-Z]+) ([^"]+)",\s*h\.(\w+)\)/g))
    routeMap[m[1] + " " + m[2]] = { name: m[3], file: f };
}
function handlerReads(name, file) {
  const body = bodies[file + "::" + name] !== undefined ? bodies[file + "::" + name] : bodies[name];
  if (body === undefined) return null;
  const reads = new Set();
  for (const x of body.matchAll(/(?:optionalQueryID|requiredQueryID|requiredQuery|commitQuery|optionalCanonicalQueryID|optionalInt|queryIDRequired|queryIDOptional|optionalCommitQuery)\(r,\s*"([A-Za-z_]+)"/g)) reads.add(x[1]);
  for (const x of body.matchAll(/(?:query|Query\(\))\.Get\("([A-Za-z_]+)"\)/g)) reads.add(x[1]);
  for (const x of body.matchAll(/requireEnum\("([A-Za-z_]+)"/g)) reads.add(x[1]);
  if (/pageParams\(r\)/.test(body)) { reads.add("limit"); reads.add("before"); }
  if (/parseDiscoveryFilters\(r\)/.test(body)) { ["kind", "decision", "wave_id", "limit"].forEach((k) => reads.add(k)); }
  if (/listLimit\(r,/.test(body)) reads.add("limit");
  return reads;
}
function specQueryNames(op) {
  const names = new Set();
  for (const p of op.parameters || []) {
    if (p.$ref) { const r = params[p.$ref.split("/").pop()]; if (r && r.in === "query") names.add(r.name); }
    else if (p.in === "query") names.add(p.name);
  }
  return names;
}
let checkedOps = 0, mappedSpecOps = 0;
for (const [p, item] of Object.entries(doc.paths)) {
  for (const m of ["get", "post", "put", "patch", "delete"]) {
    const op = item[m]; if (!op) continue;
    const route = routeMap[m.toUpperCase() + " " + p];
    if (!route) continue;
    mappedSpecOps++;
    const reads = handlerReads(route.name, route.file);
    if (reads === null) { ok("handler body found: " + route.name + " (" + route.file + ")", false); continue; }
    const specNames = specQueryNames(op);
    checkedOps++;
    ok("exec/codeintel " + m.toUpperCase() + " " + p + " (" + route.name + ") query docs==reads", setEq(specNames, reads),
      "docs-only=[" + diff(specNames, reads) + "] reads-only=[" + diff(reads, specNames) + "]");
  }
}
// zero slack: every mapped spec operation must have been checked, and the aperture must
// cover the full execution+codeintel route table (no silent drop to a smaller floor).
ok("every mapped spec operation checked (zero slack)", checkedOps === mappedSpecOps, "checked=" + checkedOps + " mapped=" + mappedSpecOps);
ok("per-operation aperture covers >=59 routes", mappedSpecOps >= 59, "mapped=" + mappedSpecOps);

if (failures.length) {
  console.error("T19 DEEP CHECK: FAIL (" + failures.length + ")");
  for (const f of failures) console.error("  - " + f);
  process.exit(1);
}
const shapeNote = multiSourceShapeOk
  ? "multi-source projections agree (" + Object.entries(sourceCounts).map(([k, v]) => k + ":" + v).join(", ") + ")"
  : "MULTI-SOURCE SHAPE NOT VERIFIED";
console.log("T19 DEEP CHECK: PASS (" + Object.keys(SOURCES).length + " schemas, " + shapeNote + "; " + checkedOps + " exec/codeintel operations query docs==reads)");
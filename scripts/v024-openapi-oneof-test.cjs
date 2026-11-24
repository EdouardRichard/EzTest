#!/usr/bin/env node
/* T19 oneOf discrimination test (IMP-3 fix round 2): prove selected's oneOf admits the
 * normal detail and the exact zero state, each matching exactly ONE branch. Minimal
 * JSON-schema subset evaluator (required/type/enum/min/max/pattern/maxItems/minItems/
 * maxProperties), resolving $ref within the doc. Tracked gate in scripts/, wired into the
 * ci.yml openapi-contract job. Run: node scripts/v024-openapi-oneof-test.cjs */
const fs = require("node:fs");
const path = require("node:path");
const ROOT = path.resolve(__dirname, ".."); // repo root from script location (cwd-independent)
const YAML = require(path.join(ROOT, "frontend/node_modules/yaml"));
const doc = YAML.parse(fs.readFileSync(path.join(ROOT, "docs/openapi/api.yaml"), "utf8"));
function deref(s) { while (s && s.$ref) s = doc.components.schemas[s.$ref.split("/").pop()]; return s; }
function errs(schema, v, p) {
  schema = deref(schema); const e = [];
  if (!schema) return e;
  // Composition keywords. `not`/`anyOf`/`oneOf` are what make the D delta modes
  // mutually exclusive, so a subset evaluator that ignored them would pass any
  // mixed-mode payload (phantom governance, V-0026).
  if (schema.allOf) for (const sub of schema.allOf) e.push(...errs(sub, v, p));
  if (schema.anyOf && !schema.anyOf.some((sub) => errs(sub, v, p).length === 0)) e.push(p + " matches no anyOf branch");
  if (schema.oneOf) {
    const matches = schema.oneOf.filter((sub) => errs(sub, v, p).length === 0).length;
    if (matches !== 1) e.push(p + " matches " + matches + " oneOf branches (want exactly 1)");
  }
  if (schema.not && errs(schema.not, v, p).length === 0) e.push(p + " matches a forbidden (not) schema");
  if (schema.enum && !schema.enum.some((x) => JSON.stringify(x) === JSON.stringify(v))) e.push(p + " not in enum " + JSON.stringify(schema.enum));
  const t = schema.type;
  // A bare constraint schema (the oneOf branches carry only required/not, no
  // `type`) still has to enforce required against an object value.
  if (t === "object" || (t === undefined && (schema.required || schema.properties))) {
    if (v === null || typeof v !== "object" || Array.isArray(v)) { e.push(p + " not object"); return e; }
    for (const r of schema.required || []) if (!(r in v)) e.push(p + " missing required " + r);
    if (schema.maxProperties !== undefined && Object.keys(v).length > schema.maxProperties) e.push(p + " >maxProperties");
    for (const [k, ps] of Object.entries(schema.properties || {})) if (k in v) e.push(...errs(ps, v[k], p + "." + k));
  } else if (t === "array") {
    if (!Array.isArray(v)) { e.push(p + " not array"); return e; }
    if (schema.maxItems !== undefined && v.length > schema.maxItems) e.push(p + " >maxItems");
    if (schema.minItems !== undefined && v.length < schema.minItems) e.push(p + " <minItems");
    if (schema.items) v.forEach((x, i) => e.push(...errs(schema.items, x, p + "[" + i + "]")));
  } else if (t === "integer" || t === "number") {
    if (typeof v !== "number") { e.push(p + " not number"); return e; }
    if (schema.minimum !== undefined && v < schema.minimum) e.push(p + " <minimum " + schema.minimum);
    if (schema.maximum !== undefined && v > schema.maximum) e.push(p + " >maximum");
  } else if (t === "string") {
    if (typeof v !== "string") { e.push(p + " not string"); return e; }
    if (schema.pattern && !new RegExp(schema.pattern, "u").test(v)) e.push(p + " !~ " + schema.pattern);
    if (schema.minLength !== undefined && v.length < schema.minLength) e.push(p + " <minLength");
    if (schema.maxLength !== undefined && v.length > schema.maxLength) e.push(p + " >maxLength");
  } else if (t === "boolean") { if (typeof v !== "boolean") e.push(p + " not boolean"); }
  return e;
}
const full = doc.components.schemas.DesignCaseVersionDetail;
const empty = doc.components.schemas.DesignCaseVersionDetailEmpty;
const normal = { case_version_id: "123", revision: 1, title: "t", description: "", sut_description: "s", preconditions: "无", covered_acs: [], test_data: {}, priority: "P2", status: "draft", content_hash: "a".repeat(64), created_at: "2026-11-15T00:00:00Z", steps: [], assertions: [], sources: [] };
const zero = { case_version_id: "0", revision: 0, title: "", description: "", sut_description: "", preconditions: "", covered_acs: [], test_data: {}, priority: "", status: "", content_hash: "", created_at: "", steps: [], assertions: [], sources: [] };
const nFull = errs(full, normal, "normal/full").length === 0;
const nEmpty = errs(empty, normal, "normal/empty").length === 0;
const zFull = errs(full, zero, "zero/full").length === 0;
const zEmpty = errs(empty, zero, "zero/empty").length === 0;
console.log("normal: full=" + nFull + " empty=" + nEmpty + "  => oneOf matches " + ((nFull ? 1 : 0) + (nEmpty ? 1 : 0)));
console.log("zero:   full=" + zFull + " empty=" + zEmpty + "  => oneOf matches " + ((zFull ? 1 : 0) + (zEmpty ? 1 : 0)));
console.log("zero/full errors: " + JSON.stringify(errs(full, zero, "zero/full")));
const pass = nFull && !nEmpty && !zFull && zEmpty;
console.log(pass ? "ONEOF TEST PASS: each state matches exactly one branch (mutually exclusive)" : "ONEOF TEST FAIL");

// --- v0.2.4-D delta modes (fix wave #36) -------------------------------------
// MessageDelta (conversation.message.delta) and TranscriptDelta
// (agent.transcript.delta) both declare oneOf + not in api.yaml. Before this
// section the guard had ZERO references to them, so a future edit could drop the
// mutual exclusion and nothing would fail. Samples come from the Go authorities
// (versionapp.MessageDelta / agentapp.TranscriptDelta json tags) and the declared
// modes, not from observed output.
const dCases = [];
function dCase(schemaName, label, value, valid) {
  const schema = doc.components.schemas[schemaName];
  const problems = errs(schema, value, schemaName + "/" + label);
  const branches = (schema.oneOf || []).filter((sub) => errs(sub, value, label).length === 0).length;
  const ok = valid ? problems.length === 0 && branches === 1 : problems.length > 0;
  dCases.push(ok);
  console.log(
    "D " + schemaName + "/" + label + ": valid=" + valid + " branches=" + branches +
    " errors=" + problems.length + (ok ? " OK" : " MISMATCH " + JSON.stringify(problems)),
  );
}

const messageAppend = { message_id: "5001", conversation_id: "4001", seq: 1, part_type: "text", offset: 3, increment: "lo" };
const messageAppendNoOffset = { message_id: "5001", conversation_id: "4001", seq: 1, part_type: "text", increment: "Hi" };
const messageReplace = {
  message_id: "5001", conversation_id: "4001", seq: 2, part_type: "tool_call",
  part: { type: "tool_call", call_id: "call_1", name: "design.graph_read", status: "running" },
};
dCase("MessageDelta", "append", messageAppend, true);
dCase("MessageDelta", "append-offset-omitted", messageAppendNoOffset, true);
dCase("MessageDelta", "replace", messageReplace, true);
dCase("MessageDelta", "mixed-append-and-part", { ...messageAppend, part: messageReplace.part }, false);
dCase("MessageDelta", "replace-with-offset", { ...messageReplace, offset: 0 }, false);
dCase("MessageDelta", "replace-with-increment", { ...messageReplace, increment: "x" }, false);
dCase("MessageDelta", "neither-mode", { message_id: "5001", conversation_id: "4001", seq: 1, part_type: "text" }, false);

const transcriptRow = {
  id: "8001", run_id: "7001", role: "assistant",
  parts: [{ type: "text", text: "Hello" }], tool_calls: [], usage: {},
  stream_state: "complete", created_at: "2026-11-17T10:00:00Z",
};
const transcriptAppend = { run_id: "7001", message_id: "8001", stream_state: "streaming", seq: 1, part_type: "text", offset: 5, increment: "!" };
const transcriptAppendNoOffset = { run_id: "7001", message_id: "8001", stream_state: "streaming", seq: 1, part_type: "text", increment: "Hi" };
const transcriptSnapshot = { run_id: "7001", message_id: "8001", stream_state: "complete", message: transcriptRow };
dCase("TranscriptDelta", "append", transcriptAppend, true);
dCase("TranscriptDelta", "append-offset-omitted", transcriptAppendNoOffset, true);
dCase("TranscriptDelta", "snapshot", transcriptSnapshot, true);
dCase("TranscriptDelta", "mixed-append-and-message", { ...transcriptAppend, message: transcriptRow }, false);
dCase("TranscriptDelta", "snapshot-with-seq", { ...transcriptSnapshot, seq: 1 }, false);
dCase("TranscriptDelta", "snapshot-with-increment", { ...transcriptSnapshot, increment: "x" }, false);
dCase("TranscriptDelta", "neither-mode", { run_id: "7001", message_id: "8001", stream_state: "streaming" }, false);

const dPass = dCases.every(Boolean);
console.log(dPass ? "D ONEOF PASS: both delta modes match exactly one branch and mixed modes are rejected" : "D ONEOF FAIL");
process.exit(pass && dPass ? 0 : 1);
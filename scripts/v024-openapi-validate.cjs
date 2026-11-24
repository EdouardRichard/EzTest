#!/usr/bin/env node
/* T19 structural validation: YAML parse, $ref integrity, operationId uniqueness, counts.
 * Tracked gate in scripts/, wired into the ci.yml openapi-contract job.
 * Run: node scripts/v024-openapi-validate.cjs */
const fs = require("node:fs");
const path = require("node:path");
const ROOT = path.resolve(__dirname, ".."); // repo root from script location (cwd-independent)
const YAML = require(path.join(ROOT, "frontend/node_modules/yaml"));
const raw = fs.readFileSync(path.join(ROOT, "docs/openapi/api.yaml"), "utf8");
let doc;
try { doc = YAML.parse(raw); } catch (e) { console.error("YAML PARSE FAIL: " + e.message); process.exit(1); }
const errs = [];
// collect all $ref
const refs = new Set();
(function walk(n) {
  if (Array.isArray(n)) return n.forEach(walk);
  if (n && typeof n === "object") {
    for (const [k, v] of Object.entries(n)) {
      if (k === "$ref" && typeof v === "string") refs.add(v);
      else walk(v);
    }
  }
})(doc);
function resolves(ref) {
  if (!ref.startsWith("#/")) return false;
  let cur = doc;
  for (const seg of ref.slice(2).split("/")) {
    if (cur == null || typeof cur !== "object") return false;
    cur = cur[seg.replace(/~1/g, "/").replace(/~0/g, "~")];
  }
  return cur !== undefined;
}
for (const r of refs) if (!resolves(r)) errs.push("dangling $ref: " + r);
// operationId uniqueness
const opIds = {};
let operations = 0;
for (const [p, item] of Object.entries(doc.paths || {})) {
  for (const m of ["get", "post", "put", "patch", "delete", "head", "options"]) {
    if (item[m]) {
      operations++;
      const id = item[m].operationId;
      if (id) { (opIds[id] = opIds[id] || []).push(m.toUpperCase() + " " + p); }
    }
  }
}
for (const [id, sites] of Object.entries(opIds)) if (sites.length > 1) errs.push("duplicate operationId " + id + ": " + sites.join(" | "));
// counts
const counts = {
  paths: Object.keys(doc.paths || {}).length,
  operations,
  schemas: Object.keys((doc.components || {}).schemas || {}).length,
  parameters: Object.keys((doc.components || {}).parameters || {}).length,
  tags: (doc.tags || []).length,
  version: doc.info.version,
  xfrozen: doc["x-frozen"],
};
console.log("COUNTS " + JSON.stringify(counts));
console.log("unique $ref targets: " + refs.size + ", operationIds: " + Object.keys(opIds).length);
if (errs.length) { console.error("VALIDATE FAIL (" + errs.length + "):"); errs.forEach((e) => console.error("  - " + e)); process.exit(1); }
console.log("VALIDATE PASS: YAML parses, all $ref resolve, operationIds unique");
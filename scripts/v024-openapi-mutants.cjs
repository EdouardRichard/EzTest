#!/usr/bin/env node
/**
 * v0.2.4 OpenAPI negative-test gate (T19, tracked; wired into ci.yml openapi-contract job).
 *
 * Proves the deep guard catches regression CLASSES, not just fixed instances, AND that the
 * round-3/round-5 protections themselves are load-bearing:
 *   A evidence Before->Cursor      (infinite-pagination class; per-operation query check)
 *   B search phantom CodeCursor    (codeintel phantom-cursor class)
 *   C Page spec projection drift   (spec-side CRT-1 class)
 *   D Go-side version pageDTO drift (the ACTUAL r3 capability: a field added to version's
 *     pageDTO alone, injected via V024_GO_OVERLAY, must fail "Page projections agree")
 *   E MAP-shape regression         (Page forced to a single source must fail the multi-source
 *     shape assertion — a one-line MAP edit cannot silently reopen the Critical)
 * Cleans up temp dirs. Exit 0 = every class caught; 1 = a class slipped through.
 * Resolves the repo root from its own location (cwd-independent). Needs frontend yaml.
 */
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const cp = require("node:child_process");
const ROOT = path.resolve(__dirname, "..");
const YAML = require(path.join(ROOT, "frontend", "node_modules", "yaml"));
const deepCheck = path.join(__dirname, "v024-openapi-deep-check.cjs");
const real = fs.readFileSync(path.join(ROOT, "docs", "openapi", "api.yaml"), "utf8");
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "v024-openapi-mutants-"));
function runGuard(specFile, env, guardPath) {
  try { cp.execFileSync(process.execPath, [guardPath || deepCheck, specFile], { stdio: "pipe", env: { ...process.env, ...(env || {}) } }); return { code: 0, out: "" }; }
  catch (e) { return { code: e.status, out: (e.stdout ? e.stdout.toString() : "") + (e.stderr ? e.stderr.toString() : "") }; }
}
const realSpec = path.join(ROOT, "docs", "openapi", "api.yaml");
const missed = [];
function report(name, r, expect) {
  const caught = r.code !== 0 && r.out.includes(expect);
  console.log((caught ? "CAUGHT" : "MISSED") + " mutant " + name + " (guard exit=" + r.code + ")");
  if (!caught) missed.push(name);
}
// A/B/C: spec-side mutants
const specMutants = [
  { name: "A evidence Before->Cursor", expect: "/api/versions/{id}/evidence", mut: (d) => { const ev = d.paths["/api/versions/{id}/evidence"].get; ev.parameters = ev.parameters.map((p) => p.$ref === "#/components/parameters/Before" ? { $ref: "#/components/parameters/Cursor" } : p); } },
  { name: "B search phantom CodeCursor", expect: "/api/code/repos/{repoId}/search", mut: (d) => { d.paths["/api/code/repos/{repoId}/search"].get.parameters.push({ $ref: "#/components/parameters/CodeCursor" }); } },
  { name: "C Page spec projection drift", expect: "Page.properties", mut: (d) => { d.components.schemas.Page.properties.bogus_version_only_field = { type: "string" }; d.components.schemas.Page.required.push("bogus_version_only_field"); } },
];
for (const m of specMutants) {
  const d = YAML.parse(real); m.mut(d);
  const f = path.join(tmp, m.name.split(" ")[0] + ".yaml");
  fs.writeFileSync(f, YAML.stringify(d));
  report(m.name, runGuard(f), m.expect);
}
// D: Go-side drift — add a field to version's pageDTO ONLY, via overlay (the r3 capability).
const verDtoRel = "backend/internal/version/interfaces/httpapi/dto.go";
// Normalize CRLF→LF before injecting: the working tree is CRLF (core.autocrlf=true),
// and the injection regex anchors on `\n`. The overlay copy is LF; the deep-check's
// struct parser is line-ending agnostic, so the mutant semantics are unchanged.
const verDto = fs.readFileSync(path.join(ROOT, verDtoRel), "utf8").replace(/\r\n/g, "\n");
const mutatedVerDto = verDto.replace(/type pageDTO struct \{\n/, 'type pageDTO struct {\n\tBogusVersionOnly string `json:"bogus_version_only"`\n');
if (mutatedVerDto === verDto) { console.error("MUTANT GATE FAIL: could not inject Go-side pageDTO drift"); process.exit(1); }
const overlay = path.join(tmp, "overlay");
const overlayFile = path.join(overlay, verDtoRel);
fs.mkdirSync(path.dirname(overlayFile), { recursive: true });
fs.writeFileSync(overlayFile, mutatedVerDto);
report("D Go-side version pageDTO drift", runGuard(realSpec, { V024_GO_OVERLAY: overlay }), "Page projections agree");
// E: MAP-shape regression — force Page to a single source in a guard copy. The copy must
// live in scripts/ so its __dirname-derived ROOT still resolves to the repo root.
const guardSrc = fs.readFileSync(deepCheck, "utf8");
const brokenGuard = guardSrc.replace("const srcs = discoverSources(cfg.struct);", "const srcs = discoverSources(cfg.struct); if (schema === \"Page\") srcs.splice(1);");
if (brokenGuard === guardSrc) { console.error("MUTANT GATE FAIL: could not build single-source guard copy"); process.exit(1); }
const brokenGuardPath = path.join(__dirname, ".tmp-v024-broken-guard.cjs");
fs.writeFileSync(brokenGuardPath, brokenGuard);
try { report("E MAP-shape regression (Page single-source)", runGuard(realSpec, {}, brokenGuardPath), "Page multi-source shape"); }
finally { fs.rmSync(brokenGuardPath, { force: true }); }
fs.rmSync(tmp, { recursive: true, force: true });
if (missed.length) { console.error("MUTANT GATE FAIL: missed " + missed.join(", ")); process.exit(1); }
console.log("MUTANT GATE PASS: all 5 regression classes caught (incl. Go-side projection + MAP-shape protections)");
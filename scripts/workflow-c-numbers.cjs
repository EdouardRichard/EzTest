#!/usr/bin/env node
/*
 * Workflow C numbers gate (v0.2.4-H T27, H-H7).
 *
 * The constitution's Development Workflow C requires the release-time numbers
 * (tables / migrations / tools / profiles / teams / REST operations / settings
 * keys / runtime env / events / audit actions) to be verifiable. This script
 * DERIVES every metric from its authoritative source and compares it with the
 * declared "当前值" column of docs/design/workflow-c-numbers.md. Any drift fails
 * closed with a non-zero exit.
 *
 * The gate is wired into scripts/check-docs.ps1 ($v024Guards), which is invoked
 * by scripts/acceptance.ps1 -StaticOnly: a drift blocks the static gate.
 *
 * Method discipline: each metric's counting rule is documented in the table
 * itself. Do not "fix" a failure by copying the derived number without checking
 * that the authoritative source is the one you mean (see the table's §2).
 */
"use strict";

const fs = require("node:fs");
const path = require("node:path");

const ROOT = path.resolve(__dirname, "..");
const TABLE = path.join(ROOT, "docs", "design", "workflow-c-numbers.md");

function read(rel) {
  return fs.readFileSync(path.join(ROOT, rel), "utf8");
}

function walk(dirRel, predicate) {
  const out = [];
  const stack = [path.join(ROOT, dirRel)];
  while (stack.length > 0) {
    const current = stack.pop();
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) {
        stack.push(full);
      } else if (predicate(full)) {
        out.push(full);
      }
    }
  }
  return out.sort();
}

function stripComments(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|[^:])\/\/[^\n]*/g, "$1");
}

// ---------------------------------------------------------------------------
// Declared values from the Workflow C table.
// ---------------------------------------------------------------------------
function parseDeclared() {
  const text = fs.readFileSync(TABLE, "utf8");
  const declared = new Map();
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed.startsWith("|")) continue;
    const cells = trimmed.split("|").slice(1, -1).map((cell) => cell.trim());
    if (cells.length < 2) continue;
    const keyMatch = cells[0].match(/^`([a-z_]+)`/);
    if (!keyMatch) continue; // header / separator / non-metric tables
    // The declared value must be a plain integer. Do NOT strip non-digits: a
    // value like "92个" or "6（4+2）" must fail loudly instead of being silently
    // read as 92 / 642.
    const rawValue = cells[cells.length - 1];
    if (!/^\d+$/.test(rawValue)) {
      throw new Error(`workflow-c-numbers: row ${keyMatch[1]} declared value must be a plain integer (got ${JSON.stringify(rawValue)})`);
    }
    if (declared.has(keyMatch[1])) {
      throw new Error(`workflow-c-numbers: duplicate row ${keyMatch[1]}`);
    }
    declared.set(keyMatch[1], Number(rawValue));
  }
  if (declared.size === 0) {
    throw new Error("workflow-c-numbers: no metric rows parsed from docs/design/workflow-c-numbers.md");
  }
  return declared;
}

// ---------------------------------------------------------------------------
// Derivations. Each function returns an integer.
// ---------------------------------------------------------------------------
function deriveTables() {
  const files = walk("backend/internal/db/migrations", (f) => f.endsWith(".sql"));
  const names = new Set();
  let statements = 0;
  for (const file of files) {
    const sql = fs.readFileSync(file, "utf8")
      .replace(/\/\*[\s\S]*?\*\//g, "")
      // Strip only line-start comments. A global `--[^\n]*` strip would truncate
      // a statement that follows a string literal containing "--" on the same
      // line (for example `SELECT 'a -- b'; CREATE TABLE ...`), silently
      // under-counting tables.
      .replace(/^[ \t]*--[^\n]*/gm, "");
    const pattern = /CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?("?[\w.]+"?)/gi;
    let match;
    while ((match = pattern.exec(sql)) !== null) {
      statements += 1;
      const name = match[1].replace(/"/g, "").split(".").pop();
      if (names.has(name)) {
        throw new Error(`workflow-c-numbers: duplicate CREATE TABLE name ${name} in ${path.relative(ROOT, file)}`);
      }
      names.add(name);
    }
  }
  return statements;
}

function deriveMigrations() {
  const text = read("backend/internal/db/migrations/order.txt");
  return text.split(/\r?\n/).filter((line) => {
    const trimmed = line.trim();
    return trimmed !== "" && !trimmed.startsWith("#");
  }).length;
}

function deriveTools() {
  const text = read("docs/contracts/capability-list.md");
  const header = text.match(/\*\*Enabled\*\*：(\d+)\s*个注册工具/);
  if (!header) throw new Error("workflow-c-numbers: capability-list.md is missing the Enabled header count");
  const start = text.indexOf("## Enabled");
  if (start < 0) throw new Error("workflow-c-numbers: capability-list.md is missing the Enabled section");
  const rest = text.slice(start + 1);
  const endRel = rest.search(/\n## /);
  const section = endRel < 0 ? rest : rest.slice(0, endRel);
  const rows = section.split(/\r?\n/).filter((line) => /^\|\s*`[^`]+`\s*\|/.test(line.trim()));
  const declaredHeader = Number(header[1]);
  if (rows.length !== declaredHeader) {
    throw new Error(`workflow-c-numbers: capability-list.md Enabled rows ${rows.length} != header count ${declaredHeader}`);
  }
  return rows.length;
}

function deriveProfiles() {
  const files = [
    "backend/internal/agent/application/profiles.go",
    "backend/internal/agent/application/execution_profiles.go",
    "backend/internal/agent/application/delivery_profiles.go",
    "backend/internal/agent/application/knowledge_profiles.go",
  ];
  const constants = new Map();
  for (const rel of files) {
    const text = stripComments(read(rel));
    const pattern = /(ProfileKey\w+)\s*=\s*"([^"]+)"/g;
    let match;
    while ((match = pattern.exec(text)) !== null) {
      constants.set(match[1], match[2]);
    }
  }
  const keys = new Set();
  for (const rel of files) {
    const text = stripComments(read(rel));
    const pattern = /Key:\s+(?:"([^"]+)"|(ProfileKey\w+))/g;
    let match;
    while ((match = pattern.exec(text)) !== null) {
      const value = match[1] || constants.get(match[2]);
      if (!value) throw new Error(`workflow-c-numbers: unresolved profile key constant ${match[2]} in ${rel}`);
      keys.add(value);
    }
  }
  return keys.size;
}

function deriveTeams() {
  const text = stripComments(read("backend/internal/agent/domain/team_room.go"));
  const teams = new Set();
  const pattern = /^\s*(Team\w+)\s+Team\s*=\s*"([^"]+)"/gm;
  let match;
  while ((match = pattern.exec(text)) !== null) {
    teams.add(match[2]);
  }
  if (teams.size === 0) throw new Error("workflow-c-numbers: no Team enum constants parsed");
  return teams.size;
}

function deriveContexts() {
  const facility = new Set(["platform", "server", "config", "db", "testutil"]);
  const base = path.join(ROOT, "backend", "internal");
  const contexts = fs.readdirSync(base, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && !facility.has(entry.name))
    .map((entry) => entry.name);
  return contexts.length;
}

function deriveRestOperations() {
  let YAML;
  try {
    YAML = require(path.join(ROOT, "frontend", "node_modules", "yaml"));
  } catch (error) {
    throw new Error("workflow-c-numbers: frontend/node_modules/yaml is missing; run 'pnpm install' in frontend (the OpenAPI count parses YAML with that dependency)");
  }
  const doc = YAML.parse(read("docs/openapi/api.yaml"));
  if (!doc || typeof doc.paths !== "object") {
    throw new Error("workflow-c-numbers: docs/openapi/api.yaml has no paths object");
  }
  const methods = new Set(["get", "put", "post", "delete", "options", "head", "patch", "trace"]);
  let operations = 0;
  for (const item of Object.values(doc.paths)) {
    if (!item || typeof item !== "object") continue;
    for (const key of Object.keys(item)) {
      if (methods.has(key)) operations += 1;
    }
  }
  return operations;
}

function deriveSettingsKeys() {
  const text = stripComments(read("backend/internal/settings/application/catalog.go"));
  const matches = text.match(/Key:\s*"/g);
  return matches ? matches.length : 0;
}

function deriveApiEnv() {
  const files = walk("backend", (f) => f.endsWith(".go") && !f.endsWith("_test.go") && !f.split(path.sep).includes("testutil"));
  const envs = new Set();
  for (const file of files) {
    const text = stripComments(fs.readFileSync(file, "utf8"));
    const pattern = /"(EZTEST_[A-Z0-9_]+)"/g;
    let match;
    while ((match = pattern.exec(text)) !== null) envs.add(match[1]);
  }
  return envs.size;
}

function deriveExecutorEnv() {
  const base = path.join(ROOT, "executor", "src");
  const envs = new Set();
  for (const entry of fs.readdirSync(base).sort()) {
    if (!entry.endsWith(".mjs")) continue;
    const text = stripComments(fs.readFileSync(path.join(base, entry), "utf8"));
    const pattern = /\b(?:process\.)?env\.(EZTEST_[A-Z0-9_]+)/g;
    let match;
    while ((match = pattern.exec(text)) !== null) envs.add(match[1]);
  }
  return envs.size;
}

function deriveEvents() {
  const text = read("docs/contracts/events.md");
  const heading = text.match(/事件清单（(\d+) 类）/);
  if (!heading) throw new Error("workflow-c-numbers: events.md is missing the event list heading count");
  const start = text.indexOf("## 事件清单");
  const rest = text.slice(start + 1);
  const endRel = rest.search(/\n## /);
  const section = endRel < 0 ? rest : rest.slice(0, endRel);
  const rows = section.split(/\r?\n/).filter((line) => /^\|\s*`[^`]+`/.test(line.trim()));
  const declared = Number(heading[1]);
  if (rows.length !== declared) {
    throw new Error(`workflow-c-numbers: events.md rows ${rows.length} != heading count ${declared}`);
  }
  return rows.length;
}

function deriveAuditActions() {
  const text = stripComments(read("backend/internal/audit/domain/actions.go"));
  const matches = text.match(/\{Name:\s*"/g);
  return matches ? matches.length : 0;
}

// ---------------------------------------------------------------------------
// Compare and report.
// ---------------------------------------------------------------------------
function main() {
  const declared = parseDeclared();
  const derived = {
    tables: deriveTables(),
    migrations: deriveMigrations(),
    tools: deriveTools(),
    profiles: deriveProfiles(),
    teams: deriveTeams(),
    contexts: deriveContexts(),
    rest_operations: deriveRestOperations(),
    settings_keys: deriveSettingsKeys(),
    api_env: deriveApiEnv(),
    executor_env: deriveExecutorEnv(),
    events: deriveEvents(),
    audit_actions: deriveAuditActions(),
  };

  const failures = [];
  for (const [key, value] of Object.entries(derived)) {
    if (!declared.has(key)) {
      failures.push(`missing row for metric ${key} (derived ${value})`);
      continue;
    }
    const expected = declared.get(key);
    if (expected !== value) {
      failures.push(`${key}: derived ${value} != declared ${expected}`);
    } else {
      console.log(`OK ${key} = ${value}`);
    }
  }
  for (const key of declared.keys()) {
    if (!(key in derived)) {
      failures.push(`unknown metric row ${key} (no derivation implemented)`);
    }
  }

  if (failures.length > 0) {
    for (const failure of failures) console.error(`FAIL workflow-c-numbers: ${failure}`);
    process.exit(1);
  }
  console.log(`Workflow C numbers OK: ${Object.keys(derived).length} metrics match their authoritative sources.`);
}

main();

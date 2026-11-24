#!/usr/bin/env node
'use strict';

// Derived static gate: every /api/ path literal and settings key used by a
// VALIDATED current acceptance script must exist in the derived allowed sets.
//
// The allowed sets are DERIVED, never hand-maintained:
//   * paths      <- docs/openapi/api.yaml `paths:` keys
//   * operational<- route registrations in backend/internal/server/health.go
//   * settings   <- `Key: "..."` entries in settings/application/catalog.go
//
// Exclusions are explicit and printed in two categories:
//   * historical/retired: scripts/acceptance-021/**, scripts/v022-*,
//     OBSOLETE fail-fast drivers that cite the v0.2.4-H T22 ruling, and retired
//     harnesses for GD-22-deleted capabilities.
//   * deferred current: six T25-renamed drivers that still call removed
//     v0.2.4-B resource routes and are awaiting refactor/retirement. They are
//     NOT validated; the final OK line says how many were skipped for each
//     category, so the gate never claims full coverage.
//
// Anti-growth: the deferred/retired whitelists are literal maps. A script that
// needs exclusion but is not in a whitelist is scanned and fails on its stale
// references; an OBSOLETE header without the T22 citation also fails.

const fs = require('fs');
const path = require('path');

const repoRoot = path.resolve(__dirname, '..');

function rel(file) {
  return path.relative(repoRoot, file).split(path.sep).join('/');
}

function readUtf8(file) {
  return fs.readFileSync(file, 'utf8').replace(/^﻿/, '');
}

// ---------------------------------------------------------------------------
// Derived allowed sets
// ---------------------------------------------------------------------------

function parseOpenApiPaths(text) {
  const lines = text.split(/\r?\n/);
  let inPaths = false;
  const paths = [];
  for (const line of lines) {
    if (!inPaths) {
      if (/^paths:\s*$/.test(line)) inPaths = true;
      continue;
    }
    // A non-indented, non-comment line ends the top-level `paths:` block.
    if (/^[^\s#]/.test(line)) break;
    const match = /^ {2}(\/[^\s:]+):\s*$/.exec(line);
    if (match) paths.push(match[1]);
  }
  return paths;
}

function deriveOperationalPaths() {
  // The health probes are registered in Go, not necessarily in the OpenAPI
  // document. Derive them from the registration site instead of hand-writing a
  // second truth.
  const files = [
    'backend/internal/server/health.go',
  ];
  const found = new Set();
  const registration = /HandleFunc\(\s*"(?:GET|POST|PUT|PATCH|DELETE)\s+(\/[^"\s]+)"/g;
  for (const relative of files) {
    const full = path.join(repoRoot, relative);
    if (!fs.existsSync(full)) continue;
    const text = readUtf8(full);
    let match;
    while ((match = registration.exec(text)) !== null) {
      if (match[1].startsWith('/api/')) found.add(match[1]);
    }
  }
  return [...found];
}

function parseCatalogKeys(text) {
  const keys = new Set();
  const pattern = /\{Key:\s*"([^"]+)"/g;
  let match;
  while ((match = pattern.exec(text)) !== null) keys.add(match[1]);
  return keys;
}

// ---------------------------------------------------------------------------
// Path extraction / normalization
// ---------------------------------------------------------------------------

const API_CALL_MARKER = /\bInvoke-(?:Api|ApiJson|P5Api|P8Api|WebRequest|RestMethod|Measure|ApiRequest)\b|\b(?:api|must|fetch)\s*\(\s*['"`]/;
const REQUEST_DEFINITION = /(?:^|[;@{(\s])(?:Path|Uri)\s*=\s*['"(]/;
const PATH_ASSIGNMENT = /^\s*\$[A-Za-z_][A-Za-z0-9_:]*\s*=\s*['"]\/api\//;

function isApiPathLine(line, previousLine) {
  const trimmed = line.trim();
  if (trimmed === '' || trimmed.startsWith('#')) return false;
  if (API_CALL_MARKER.test(line) || REQUEST_DEFINITION.test(line) || PATH_ASSIGNMENT.test(line)) {
    return true;
  }
  // A -Path continuation line whose call started on the previous line.
  if (/^\s+-Path\b/.test(line) && previousLine && API_CALL_MARKER.test(previousLine)) {
    return true;
  }
  return false;
}

function skipDollar(text, index) {
  let i = index + 1;
  if (text[i] === '{') {
    let depth = 1;
    i += 1;
    while (i < text.length && depth > 0) {
      if (text[i] === '{') depth += 1;
      else if (text[i] === '}') depth -= 1;
      i += 1;
    }
    return i;
  }
  if (text[i] === '(') {
    let depth = 1;
    i += 1;
    while (i < text.length && depth > 0) {
      if (text[i] === '(') depth += 1;
      else if (text[i] === ')') depth -= 1;
      i += 1;
    }
    return i;
  }
  while (i < text.length && /[A-Za-z0-9_:]/.test(text[i])) i += 1;
  return i;
}

function skipBraces(text, index) {
  let i = index + 1;
  while (i < text.length && text[i] !== '}') i += 1;
  return i < text.length ? i + 1 : i;
}

// Extracts the first /api/ candidate on an API-call line. String concatenation
// (`"/api/versions/" + $id + "/x"`) and interpolation (`$id`, `{0}`, `%s`) are
// normalized to `{}`; the query string is stripped later.
function extractFirstCandidate(line) {
  const start = line.indexOf('/api/');
  if (start < 0) return null;
  const before = line.slice(0, start);
  if (!/["'`]/.test(before)) return null;

  let i = start;
  let out = '';
  let inString = true;
  let pendingGap = false;
  let afterPlus = false;
  while (i < line.length) {
    const ch = line[i];
    if (inString) {
      if (ch === '"' || ch === "'") {
        inString = false;
        i += 1;
        continue;
      }
      if (ch === '$') {
        out += '{}';
        i = skipDollar(line, i);
        continue;
      }
      if (ch === '{') {
        out += '{}';
        i = skipBraces(line, i);
        continue;
      }
      if (ch === '%' && line[i + 1] === 's') {
        out += '{}';
        i += 2;
        continue;
      }
      out += ch;
      i += 1;
      continue;
    }
    if (ch === ' ' || ch === '\t' || ch === '\r') {
      // Whitespace ends the path argument unless the expression continues with
      // a concatenation ("/api/x/" + $id + "/y"). Peek past the whitespace.
      let next = i;
      while (next < line.length && (line[next] === ' ' || line[next] === '\t' || line[next] === '\r')) next += 1;
      if (line[next] === '+') {
        afterPlus = true;
        i = next;
        continue;
      }
      if (afterPlus || pendingGap) {
        i = next;
        continue;
      }
      break;
    }
    if (ch === '+') {
      afterPlus = true;
      pendingGap = true;
      i += 1;
      continue;
    }
    if (ch === '"' || ch === "'") {
      if (pendingGap) {
        out += '{}';
        pendingGap = false;
      }
      afterPlus = false;
      inString = true;
      i += 1;
      continue;
    }
    if (afterPlus || pendingGap) {
      // Expression between concatenated literals (variable / method call).
      pendingGap = true;
      afterPlus = false;
      i += 1;
      continue;
    }
    break;
  }
  if (pendingGap) out += '{}';
  return out;
}

function normalizeSegments(raw) {
  let value = String(raw).trim();
  const query = value.search(/[?#]/);
  if (query >= 0) value = value.slice(0, query);
  value = value.replace(/\$\{[^}]*\}/g, '{}');
  value = value.replace(/\$\([^)]*\)/g, '{}');
  value = value.replace(/\$[A-Za-z_][A-Za-z0-9_:]*/g, '{}');
  value = value.replace(/\{[^}]*\}/g, '{}');
  value = value.replace(/%s/g, '{}');
  return value.split('/').filter((segment) => segment !== '');
}

function segmentsMatch(candidate, template) {
  if (candidate.length !== template.length) return false;
  for (let i = 0; i < candidate.length; i += 1) {
    const left = candidate[i];
    const right = template[i];
    if (left === right) continue;
    // A placeholder on either side matches one concrete segment (ids, team
    // keys, repository names, ...). Concrete-vs-concrete must still be equal.
    if (left === '{}' || right === '{}') continue;
    return false;
  }
  return true;
}

// ---------------------------------------------------------------------------
// Settings-key extraction
// ---------------------------------------------------------------------------

function blockInner(text, arrayStart) {
  let depth = 1;
  let i = arrayStart;
  while (i < text.length && depth > 0) {
    if (text[i] === '(') depth += 1;
    else if (text[i] === ')') depth -= 1;
    i += 1;
  }
  return { inner: text.slice(arrayStart, i - 1), innerOffset: arrayStart };
}

function collectInnerKeys(inner, innerOffset, results) {
  let found = false;
  const pairPattern = /@\(\s*["']([^"']+)["']\s*,/g;
  let match;
  while ((match = pairPattern.exec(inner)) !== null) {
    found = true;
    results.push({ value: match[1], index: innerOffset + match.index });
  }
  const hashtablePattern = /\bKey\s*=\s*["']([^"']+)["']/g;
  while ((match = hashtablePattern.exec(inner)) !== null) {
    found = true;
    results.push({ value: match[1], index: innerOffset + match.index });
  }
  if (!found) {
    // Plain key array: $p8SettingKeys = @("a.b", "c.d")
    const quoted = /["']([^"']+)["']/g;
    while ((match = quoted.exec(inner)) !== null) {
      results.push({ value: match[1], index: innerOffset + match.index });
    }
  }
}

function extractKeyBlock(text) {
  // Authored key lists:
  //   $requiredKeys = @(...)
  //   foreach ($key in @(...))
  //   $settings = @(@("key", value), ...) / @(@{ Key = "key"; ... }, ...)
  //   $p8SettingKeys = @("key", ...)
  // Only settings-named array variables are treated as key tables; event-name
  // arrays such as $httpActions/$required are not matched.
  const results = [];
  const fixedOpeners = [
    /\$requiredKeys\s*=\s*@\(/g,
    /foreach\s*\(\s*\$key\s+in\s*@\(/g,
  ];
  for (const opener of fixedOpeners) {
    let match;
    while ((match = opener.exec(text)) !== null) {
      const arrayStart = match.index + match[0].length;
      const block = blockInner(text, arrayStart);
      const quoted = /["']([^"']+)["']/g;
      let innerMatch;
      while ((innerMatch = quoted.exec(block.inner)) !== null) {
        results.push({ value: innerMatch[1], index: block.innerOffset + innerMatch.index });
      }
    }
  }

  const settingsArray = /\$([A-Za-z_][A-Za-z0-9_]*)\s*=\s*@\(/g;
  let match;
  while ((match = settingsArray.exec(text)) !== null) {
    if (!/setting/i.test(match[1])) continue;
    const arrayStart = match.index + match[0].length;
    const block = blockInner(text, arrayStart);
    collectInnerKeys(block.inner, block.innerOffset, results);
  }
  return results;
}

function extractInlineSettingsKeys(text) {
  const results = [];
  const patterns = [
    // Positional: Set-Setting "key" / Get-SettingValue "key"
    /\b(?:Set|Get)-[A-Za-z0-9]*Setting[A-Za-z0-9]*\s+["']([^"']+)["']/g,
    // Named: Set-Setting -Key "key" / Set-P8SettingTracked -Key "key" / Get-P8SettingValue -Key "key"
    /\b(?:Set|Get)-[A-Za-z0-9]*Setting[A-Za-z0-9]*\s+-Key\s+["']([^"']+)["']/g,
    /\$_\.key\s+-eq\s+["']([^"']+)["']/g,
  ];
  for (const pattern of patterns) {
    let match;
    while ((match = pattern.exec(text)) !== null) {
      results.push({ value: match[1], index: match.index });
    }
  }
  return results;
}

function looksLikeSettingsKey(value) {
  return /^[a-z][a-z0-9_]*\.[a-z0-9_]+$/.test(value);
}

function lineNumberAt(text, index) {
  let line = 1;
  for (let i = 0; i < index && i < text.length; i += 1) {
    if (text[i] === '\n') line += 1;
  }
  return line;
}

// ---------------------------------------------------------------------------
// Script surface
// ---------------------------------------------------------------------------

function listCandidateScripts() {
  const scriptsDir = path.join(repoRoot, 'scripts');
  const entries = fs.readdirSync(scriptsDir, { withFileTypes: true });
  const include = /(?:-acceptance(?:-llm)?|-e2e(?:-llm)?)\.ps1$|^slo(?:-chain)?\.ps1$|^capability-matrix\.ps1$|^hardening-[^/]+\.ps1$/;
  const candidates = [];
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    if (!entry.name.endsWith('.ps1')) continue;
    if (!include.test(entry.name)) continue;
    if (/^v022-/.test(entry.name) || /^dev-/.test(entry.name)) continue;
    candidates.push(path.join(scriptsDir, entry.name));
  }
  return candidates.sort();
}

// Current drivers that are still runnable in principle but have NOT been
// refactored to the v0.2.4-B resource model. They still call removed routes
// (suts/{id}/environments, environments/{id}/pages|apis, suts/{id}/accounts).
// They are DEFERRED, not validated: the gate must never claim coverage for
// them. Every entry is explicit; a new stale script cannot be added here
// silently because omitting it makes the gate scan it and fail.
const DEFERRED_CURRENT = new Map([
  ['clarification-acceptance.ps1', 'deferred current driver (P1-era): still calls removed environment-nested resource routes (v0.2.4-B); refactor/retire pending'],
  ['delivery-acceptance.ps1', 'deferred current driver (P4-era): still calls removed environment-nested resource routes (v0.2.4-B); refactor/retire pending'],
  ['delivery-e2e.ps1', 'deferred current driver (P4-era E2E): still calls removed environment-nested resource routes (v0.2.4-B); refactor/retire pending'],
  ['design-acceptance.ps1', 'deferred current driver (P2-era): still calls removed environment-nested resource routes (v0.2.4-B); refactor/retire pending'],
  ['execution-e2e.ps1', 'deferred current driver (P3-era E2E): still calls removed environment-nested resource routes (v0.2.4-B); refactor/retire pending'],
  ['resilience-acceptance.ps1', 'deferred current driver (P8-era): still calls removed environment-nested resource routes (v0.2.4-B); refactor/retire pending'],
]);

// Retired harnesses for capabilities deleted by GD-22. They are historical
// records, not current drivers.
const RETIRED_HISTORICAL = new Map([
  ['code-intelligence-e2e.ps1', 'retired P5 visual harness: /visual/assertions targets the GD-22-deleted visual surface'],
]);

const OBSOLETE_MARKER = /OBSOLETE and does not run/i;
const OBSOLETE_T22_REFERENCE = /v0\.2\.4-H T22/;

function classifyExclusion(file, text) {
  const base = path.basename(file);
  // Historical surfaces are frozen before they even become candidates.
  if (base.startsWith('v022-')) {
    return { category: 'historical', reason: 'historical v022 surface' };
  }
  if (OBSOLETE_MARKER.test(text) || /OBSOLETE guard/i.test(text)) {
    // Fail closed on implicit exclusions: an OBSOLETE header must cite the
    // ruling that made the driver historical (v0.2.4-H T22). Without the
    // citation the gate refuses to skip the script.
    if (!OBSOLETE_T22_REFERENCE.test(text)) {
      return {
        category: 'invalid',
        reason: 'OBSOLETE driver does not cite v0.2.4-H T22; implicit exclusion refused',
      };
    }
    return { category: 'historical', reason: 'OBSOLETE fail-fast driver (v0.2.4-H T22)' };
  }
  if (RETIRED_HISTORICAL.has(base)) {
    return { category: 'historical', reason: RETIRED_HISTORICAL.get(base) };
  }
  if (DEFERRED_CURRENT.has(base)) {
    return { category: 'deferred', reason: DEFERRED_CURRENT.get(base) };
  }
  return null;
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

function main() {
  const problems = [];

  const openapiFile = path.join(repoRoot, 'docs', 'openapi', 'api.yaml');
  if (!fs.existsSync(openapiFile)) {
    console.error('check-acceptance-paths: missing docs/openapi/api.yaml');
    process.exit(1);
  }
  const openapiPaths = parseOpenApiPaths(readUtf8(openapiFile));
  if (openapiPaths.length === 0) {
    console.error('check-acceptance-paths: parsed 0 OpenAPI paths; refusing to pass vacuously');
    process.exit(1);
  }

  const operationalPaths = deriveOperationalPaths();
  const allowedPaths = new Set([...openapiPaths, ...operationalPaths]);
  const allowedSegments = [...allowedPaths].map((entry) => ({
    raw: entry,
    segments: normalizeSegments(entry),
  }));

  const catalogFile = path.join(repoRoot, 'backend', 'internal', 'settings', 'application', 'catalog.go');
  if (!fs.existsSync(catalogFile)) {
    console.error('check-acceptance-paths: missing backend/internal/settings/application/catalog.go');
    process.exit(1);
  }
  const catalogKeys = parseCatalogKeys(readUtf8(catalogFile));
  if (catalogKeys.size === 0) {
    console.error('check-acceptance-paths: parsed 0 settings keys; refusing to pass vacuously');
    process.exit(1);
  }

  const scripts = listCandidateScripts();
  const scanned = [];
  const historical = [];
  const deferred = [];
  let checkedPaths = 0;
  let checkedKeys = 0;
  const seenDeferred = new Set();
  const seenRetired = new Set();

  for (const file of scripts) {
    const text = readUtf8(file);
    const exclusion = classifyExclusion(file, text);
    if (exclusion) {
      const entry = `${rel(file)} (${exclusion.reason})`;
      if (exclusion.category === 'deferred') {
        deferred.push(entry);
        seenDeferred.add(path.basename(file));
      } else {
        historical.push(entry);
        if (exclusion.category === 'invalid') {
          problems.push(`PROBLEM ${rel(file)}: ${exclusion.reason}`);
        }
      }
      if (RETIRED_HISTORICAL.has(path.basename(file))) seenRetired.add(path.basename(file));
      continue;
    }
    scanned.push(rel(file));

    const lines = text.split(/\r?\n/);
    for (let index = 0; index < lines.length; index += 1) {
      const line = lines[index];
      const previous = index > 0 ? lines[index - 1] : '';
      if (!isApiPathLine(line, previous)) continue;
      const candidate = extractFirstCandidate(line);
      if (!candidate) continue;
      const segments = normalizeSegments(candidate);
      if (segments.length === 0) continue;
      checkedPaths += 1;
      const matched = allowedSegments.some((entry) => segmentsMatch(segments, entry.segments));
      if (!matched) {
        problems.push(`UNMATCHED ${rel(file)}:${index + 1} ${candidate}`);
      }
    }

    // Settings keys are checked only for validated current scripts. Deferred
    // and historical scripts are not counted as validated coverage.
    const keyCandidates = [
      ...extractKeyBlock(text),
      ...extractInlineSettingsKeys(text),
    ];
    const seen = new Set();
    for (const item of keyCandidates) {
      if (!looksLikeSettingsKey(item.value)) continue;
      const line = lineNumberAt(text, item.index);
      const marker = `${line}:${item.value}`;
      if (seen.has(marker)) continue;
      seen.add(marker);
      checkedKeys += 1;
      if (!catalogKeys.has(item.value)) {
        problems.push(`UNMATCHED ${rel(file)}:${line} setting ${item.value}`);
      }
    }
  }

  // A stale whitelist entry is drift too: a deferred/retired script that was
  // renamed or removed must be re-triaged rather than silently dropped.
  for (const name of DEFERRED_CURRENT.keys()) {
    if (!seenDeferred.has(name)) {
      problems.push(`PROBLEM deferred whitelist entry not found: scripts/${name}`);
    }
  }
  for (const name of RETIRED_HISTORICAL.keys()) {
    if (!seenRetired.has(name)) {
      problems.push(`PROBLEM retired whitelist entry not found: scripts/${name}`);
    }
  }

  if (scanned.length === 0) {
    console.error('check-acceptance-paths: no current acceptance scripts matched the scan surface; refusing to pass vacuously');
    process.exit(1);
  }

  console.log(
    `check-acceptance-paths: openapi paths=${openapiPaths.length} (operational from code=${operationalPaths.length}) ` +
    `settings keys=${catalogKeys.size} validated scripts=${scanned.length} ` +
    `historical skipped=${historical.length} deferred current=${deferred.length} ` +
    `checked api paths=${checkedPaths} settings keys=${checkedKeys}`
  );
  console.log('HISTORICAL SURFACE (outside candidates): scripts/acceptance-021/**, scripts/v022-*');
  for (const item of historical) {
    console.log(`SKIPPED (historical) ${item}`);
  }
  for (const item of deferred) {
    console.log(`DEFERRED (current, not validated) ${item}`);
  }
  if (problems.length > 0) {
    for (const problem of problems) console.error(problem);
    console.error(`check-acceptance-paths FAILED: ${problems.length} problem(s)`);
    process.exit(1);
  }
  console.log(
    `check-acceptance-paths OK (${historical.length} historical skipped, ` +
    `${deferred.length} deferred current scripts not validated): ` +
    `validated ${scanned.length} current scripts against derived paths/settings`
  );
  process.exit(0);
}

main();

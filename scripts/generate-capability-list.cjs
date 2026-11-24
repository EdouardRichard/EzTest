#!/usr/bin/env node
/*
 * Capability list generator / bidirectional gate (v0.2.4-C T21).
 *
 * The agent tool capability list (docs/contracts/capability-list.md) is GENERATED
 * from the tool registry — the single authority (constitution art.5). The actual
 * generation + bidirectional check lives in a Go test
 * (backend/cmd/eztest/capability_list_test.go, TestCapabilityListContract) so it
 * rides the existing blocking CI gate: ci.yml `test` job → "Go unit tests"
 * (`go test -count=1 ./...`) → that test. This wrapper is the local convenience
 * entry point (method discipline ⑪: in git AND invoked AND blocking).
 *
 *   node scripts/generate-capability-list.cjs          # check (fails on drift)
 *   node scripts/generate-capability-list.cjs --write  # regenerate the committed file
 *
 * Resolves the repo root from its own location (cwd-independent). Exit code is
 * the Go test's exit code (0 = consistent / written; 1 = drift or build failure).
 */
const path = require("node:path");
const fs = require("node:fs");
const cp = require("node:child_process");

const ROOT = path.resolve(__dirname, "..");
const write = process.argv.includes("--write");

// Prefer the repo-local toolchain (.tools/go), fall back to PATH go.
function resolveGo() {
  const candidates = [
    path.join(ROOT, ".tools", "go", "bin", "go.exe"),
    path.join(ROOT, ".tools", "go", "bin", "go"),
  ];
  for (const c of candidates) {
    if (fs.existsSync(c)) return c;
  }
  return "go";
}

const go = resolveGo();
const env = { ...process.env };
if (write) env.EZTEST_CAPABILITY_LIST_WRITE = "1";

const result = cp.spawnSync(
  go,
  ["test", "./cmd/eztest/", "-run", "TestCapabilityListContract", "-count=1"],
  { cwd: path.join(ROOT, "backend"), env, stdio: "inherit" },
);
if (result.error) {
  console.error("capability-list: could not run go (" + go + "): " + result.error.message);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
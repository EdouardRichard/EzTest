#!/usr/bin/env node
"use strict";

// Generates scripts/testdata/fake-llm-p5-mirror.json — the Go-authoritative
// mirror that scripts/fake-llm.p5.test.cjs consumes. Before this generator the
// test hand-maintained three copies of the same facts (prompt constant names,
// role markers, tool names) and silently drifted out of step with the backend
// (H-D14). Now the fixture is derived from Go and a staleness gate fails the
// test (and this script, run without --write) the moment Go moves.
//
// Derived from:
//   backend/internal/agent/application/profiles.go     profile-keyed prompt
//                                                       constants + their
//                                                       [profile:<key>] marker
//   backend/internal/agent/application/tools/design.go design tool registry
//                                                       names
//
// Derivation fails LOUDLY (throws) when a declared scenario, profile key or tool
// constant no longer exists — it never falls back to an empty set, so the test
// can never go born-green.
//
// Convention (matches scripts/generate-*.cjs in this repo):
//   node scripts/generate-fake-llm-p5-mirror.cjs           # check, exit 1 on drift
//   node scripts/generate-fake-llm-p5-mirror.cjs --write   # rewrite the fixture
//
// Scenario coverage (Ruling T21-R2 and measured facts):
//   * design   -> live seeded profile prompt designAuthorPrompt ([profile:design_author])
//   * review   -> live seeded profile prompt designCriticPrompt ([profile:design_critic])
//   * explore  -> capability removed earlier (roles.go: "The Explorer role and
//                 explore.* wildcard are gone"); scenario deleted, not renamed.
//   * data_plan-> the data planner profile/prompt was retired (profiles_test.go
//                 removed-key list); no live Go prompt exists, so it cannot be
//                 derived. The design.data_plan_submit tool is still registered
//                 but unwired. Scenario deleted and flagged for the owner.

const fs = require("node:fs");
const path = require("node:path");

const repoRoot = path.resolve(__dirname, "..");
const profilesPath = path.join(repoRoot, "backend", "internal", "agent", "application", "profiles.go");
const designToolsPath = path.join(
  repoRoot,
  "backend",
  "internal",
  "agent",
  "application",
  "tools",
  "design.go",
);
const fixturePath = path.join(repoRoot, "scripts", "testdata", "fake-llm-p5-mirror.json");

// The live P5 scenarios and the Go artifacts that back them. This is the only
// hand-declared association; every referenced value is resolved from Go and the
// gate fails if any of them disappears.
const SCENARIOS = {
  design: {
    profileKey: "design_author",
    tools: ["ToolDesignGraphRead", "ToolCaseGraphPropose"],
  },
  review: {
    profileKey: "design_critic",
    tools: ["ToolDesignGraphRead", "ToolDesignReviewSubmit"],
  },
};

function readGo(file) {
  if (!fs.existsSync(file)) {
    throw new Error("missing Go source: " + file);
  }
  return fs.readFileSync(file, "utf8");
}

// parseProfilePrompts maps profileKey -> {constName, marker, body} by scanning
// every `const <Name> = `...`` prompt constant in profiles.go and reading the
// [profile:<key>] marker it carries. Go raw-string prompts never contain a
// backtick, so the non-greedy body capture is exact.
function parseProfilePrompts(source) {
  const pattern = /const\s+([A-Za-z_]\w*)\s*=\s*`([\s\S]*?)`/g;
  const out = new Map();
  let match = pattern.exec(source);
  while (match !== null) {
    const name = match[1];
    const body = match[2];
    const marker = /\[profile:([a-z0-9_]+)\]/.exec(body);
    if (marker !== null) {
      const key = marker[1];
      if (out.has(key)) {
        // A duplicate marker would silently let the later const shadow the
        // earlier one; fail loudly and name both, per "derivation must fail
        // loudly, never born-green".
        throw new Error(
          "duplicate [profile:" + key + "] marker in " + profilesPath +
            ": carried by both " + out.get(key).constName + " and " + name +
            " — refusing to silently overwrite",
        );
      }
      out.set(key, { constName: name, marker: "[profile:" + key + "]", body });
    }
    match = pattern.exec(source);
  }
  return out;
}

function parseProfileKeys(source) {
  const keys = new Set();
  const pattern = /ProfileKey\w+\s*=\s*"([a-z0-9_]+)"/g;
  let match = pattern.exec(source);
  while (match !== null) {
    keys.add(match[1]);
    match = pattern.exec(source);
  }
  return keys;
}

function parseToolConsts(source) {
  const out = new Map();
  const pattern = /\b(Tool\w+)\s*=\s*"([^"]+)"/g;
  let match = pattern.exec(source);
  while (match !== null) {
    out.set(match[1], match[2]);
    match = pattern.exec(source);
  }
  return out;
}

// wireName mirrors backend/internal/llm/infrastructure/openai_chat.go
// sanitizeToolName: every rune outside [A-Za-z0-9_-] becomes '_'. This is the
// function-name form a real model sees and echoes back on the wire.
function wireName(dotted) {
  return Array.from(dotted)
    .map((ch) => (/[A-Za-z0-9_-]/.test(ch) ? ch : "_"))
    .join("");
}

function buildFixture() {
  const profilesSource = readGo(profilesPath);
  const promptsByProfile = parseProfilePrompts(profilesSource);
  const liveProfileKeys = parseProfileKeys(profilesSource);
  const toolConsts = parseToolConsts(readGo(designToolsPath));

  const scenarios = {};
  const prompts = {};
  const toolWire = {};

  for (const [scenario, spec] of Object.entries(SCENARIOS)) {
    if (!liveProfileKeys.has(spec.profileKey)) {
      throw new Error(
        "scenario " + scenario + " references profile " + spec.profileKey +
          " which is not a live ProfileKey in " + profilesPath,
      );
    }
    const prompt = promptsByProfile.get(spec.profileKey);
    if (prompt === undefined) {
      throw new Error(
        "scenario " + scenario + " expects a prompt constant carrying [profile:" +
          spec.profileKey + "] in " + profilesPath + " — none found",
      );
    }
    const flow = spec.tools.map((constName) => {
      if (!toolConsts.has(constName)) {
        throw new Error(
          "scenario " + scenario + " expects tool const " + constName + " in " +
            designToolsPath + " — none found",
        );
      }
      const dotted = toolConsts.get(constName);
      toolWire[dotted] = wireName(dotted);
      return dotted;
    });
    scenarios[scenario] = {
      profileKey: spec.profileKey,
      promptConst: prompt.constName,
      marker: prompt.marker,
      flow,
    };
    prompts[prompt.constName] = prompt.body;
  }

  return {
    _comment:
      "GENERATED by scripts/generate-fake-llm-p5-mirror.cjs from Go sources. " +
      "Run without --write to check for drift; --write to regenerate. Do not hand-edit.",
    scenarios,
    prompts,
    toolWire,
  };
}

function serialize(fixture) {
  return JSON.stringify(fixture, null, 2) + "\n";
}

function main() {
  const write = process.argv.includes("--write");
  const fixture = buildFixture();
  const rendered = serialize(fixture);
  const existing = fs.existsSync(fixturePath) ? fs.readFileSync(fixturePath, "utf8") : null;

  if (write) {
    fs.mkdirSync(path.dirname(fixturePath), { recursive: true });
    fs.writeFileSync(fixturePath, rendered);
    process.stdout.write("wrote " + path.relative(repoRoot, fixturePath) + "\n");
    return 0;
  }
  if (existing === null) {
    process.stderr.write(
      "DRIFT: fixture " + path.relative(repoRoot, fixturePath) + " is missing; run with --write\n",
    );
    return 1;
  }
  if (existing !== rendered) {
    process.stderr.write(
      "DRIFT: fixture " + path.relative(repoRoot, fixturePath) +
        " no longer matches the Go sources; run with --write\n",
    );
    return 1;
  }
  process.stdout.write("ok: " + path.relative(repoRoot, fixturePath) + " is up to date\n");
  return 0;
}

module.exports = { buildFixture, serialize, fixturePath, wireName };

if (require.main === module) {
  process.exitCode = main();
}

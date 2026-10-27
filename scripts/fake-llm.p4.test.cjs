#!/usr/bin/env node
"use strict";

// Deterministic tests for the P4 scenarios of scripts/fake-llm.cjs (design spec
// §10.2). The fake upstream is spawned on an ephemeral port
// (EZTEST_FAKE_LLM_PORT=0) and answered through the same
// POST /v1/chat/completions wire shape the real provider uses, so the tests
// exercise the actual request parsing, mode switch, response schema and request
// log.
//
// The fixture-repository assertions read `.tmp/p4-e2e-repo`; create it first:
//   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fixture-codeintel-repo.ps1
// then run:
//   node scripts/fake-llm.p4.test.cjs

const assert = require("node:assert/strict");
const { spawn, execFileSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { after, before, test } = require("node:test");

const repoRoot = path.resolve(__dirname, "..");
const fakeLlmScript = path.join(__dirname, "fake-llm.cjs");
const fixtureRoot = path.join(repoRoot, ".tmp", "p4-e2e-repo");
const requestLogPath = path.join(repoRoot, ".tmp", "fake-llm-requests.jsonl");
const serverLogRoot = fs.mkdtempSync(path.join(os.tmpdir(), "eztest-fake-llm-p4-"));

const FILE_ANALYZER_MARKER = "EzTest 的只读代码分析器";
const IMPACT_ANALYZER_MARKER = "EzTest 的只读变更影响分析器";
const MODEL = "deepseek-flash";

// The P1 request shape: the agent always offers tools, so this keeps the
// pre-P4 fallback path identical to the production requests.
const P1_TOOLS = [
  {
    type: "function",
    function: {
      name: "file.read",
      description: "read a file",
      parameters: {
        type: "object",
        properties: { path: { type: "string" } },
        required: ["path"],
      },
    },
  },
];

let serverCounter = 0;

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function requireFixture() {
  assert.ok(
    fs.existsSync(path.join(fixtureRoot, ".git")),
    "missing fixture repository .tmp/p4-e2e-repo; run scripts/fixture-codeintel-repo.ps1 first",
  );
}

function fixtureCommit(rev) {
  requireFixture();
  return execFileSync("git", ["-C", fixtureRoot, "rev-parse", rev], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "ignore"],
  }).trim();
}

function readFixtureFile(relativePath) {
  requireFixture();
  const filePath = path.join(fixtureRoot, ...relativePath.split("/"));
  assert.ok(fs.existsSync(filePath), "fixture file is missing: " + relativePath);
  return fs.readFileSync(filePath, "utf8");
}

function fixtureLines(relativePath) {
  const lines = readFixtureFile(relativePath).split(/\r?\n/);
  if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/** Asserts that a fact/evidence range is a real range of the fixture file. */
function assertRangeContains(relativePath, startLine, endLine, needle) {
  const lines = fixtureLines(relativePath);
  assert.ok(Number.isInteger(startLine) && startLine >= 1, "startLine must be a positive integer");
  assert.ok(Number.isInteger(endLine) && endLine >= startLine, "endLine must be >= startLine");
  assert.ok(endLine <= lines.length, `range ${startLine}-${endLine} exceeds ${relativePath} (${lines.length} lines)`);
  const excerpt = lines.slice(startLine - 1, endLine).join("\n");
  assert.ok(
    excerpt.includes(needle),
    `${relativePath}:${startLine}-${endLine} does not contain ${JSON.stringify(needle)}:\n${excerpt}`,
  );
}

/** Builds the user message exactly like application.BuildFilePrompt (spec §8.3). */
function filePromptUser(relativePath, lines, commit) {
  const numbered = lines.map((line, index) => `${index + 1} | ${line}`).join("\n");
  return [
    "仓库：eztest-p4-fixture",
    `提交：${commit}`,
    `文件：${relativePath}`,
    `文件总行数：${lines.length}`,
    "文件内容（行号见左侧，仅用于定位证据）：",
    "<file-content>",
    numbered,
    "</file-content>",
    "",
    "输出 JSON schema：",
    '{"changeSummary":"string","facts":[],"edges":[]}',
  ].join("\n");
}

/** One T1 file-analysis request: system marker + user prompt, no tools. */
function fileAnalysisBody(relativePath, lines) {
  return {
    model: MODEL,
    stream: false,
    messages: [
      { role: "system", content: FILE_ANALYZER_MARKER + "。只输出 JSON 对象。" },
      {
        role: "user",
        content: filePromptUser(relativePath, lines || fixtureLines(relativePath), fixtureCommit("HEAD")),
      },
    ],
  };
}

/** One impact-reasoning request (spec §8.4), no tools. */
function impactAnalysisBody() {
  const requirements = [
    "11 | 订单退款页面展示 | 用户可以在订单页面看到退款状态。",
    "22 | P4-AI-IMPACT 退款原因 | 退款时需要展示用户选择的原因。",
  ].join("\n");
  const user = [
    "版本需求清单（id | 标题 | 描述摘要）：",
    requirements,
    "",
    "变更文件（path | status）：",
    "src/api/refund.ts | M",
    "server/refund.go | M",
    "",
    "变更事实（kind | name | path | summary | confidence | evidence）：",
    "client_call | POST /api/refund | src/api/refund.ts | 退款调用 | high | src/api/refund.ts:5-6",
    "api | POST /api/refund | server/refund.go | 退款接口 | high | server/refund.go:14-14",
    "",
    "变更摘要：",
    "退款调用与接口新增 reason 参数。",
    "",
    "输出 JSON schema：",
    '{"affected":[]}',
  ].join("\n");
  return {
    model: MODEL,
    stream: false,
    messages: [
      { role: "system", content: IMPACT_ANALYZER_MARKER + "。只输出 JSON 对象。" },
      { role: "user", content: user },
    ],
  };
}

/**
 * Starts one fake-llm.cjs on a process-unique port near 14000. The port is
 * retried on EADDRINUSE so these tests can run next to a manual dev server.
 */
async function startFakeLlm(envOverrides) {
  let lastError = null;
  for (let attempt = 0; attempt < 10; attempt += 1) {
    serverCounter += 1;
    const port = 14000 + ((process.pid + serverCounter * 37 + attempt * 11) % 9000);
    const logPath = path.join(serverLogRoot, `fake-llm-${serverCounter}.log`);
    const logFd = fs.openSync(logPath, "w");
    const child = spawn(process.execPath, [fakeLlmScript], {
      cwd: repoRoot,
      env: {
        ...process.env,
        EZTEST_FAKE_LLM_PORT: String(port),
        ...envOverrides,
      },
      stdio: ["ignore", logFd, logFd],
      windowsHide: true,
    });
    fs.closeSync(logFd);

    const base = `http://127.0.0.1:${port}`;
    const deadline = Date.now() + 6000;
    while (Date.now() < deadline) {
      if (child.exitCode !== null) break;
      try {
        const response = await fetch(base + "/v1/models");
        if (response.status === 200) {
          await response.arrayBuffer();
          return {
            base,
            child,
            logPath,
            stop() {
              if (child.exitCode === null) child.kill();
            },
          };
        }
      } catch {
        // Not ready yet.
      }
      await sleep(50);
    }

    if (child.exitCode === null) child.kill();
    lastError = fs.existsSync(logPath) ? fs.readFileSync(logPath, "utf8") : "";
    if (!/EADDRINUSE/.test(lastError)) {
      throw new Error(`fake LLM did not start on ${base}:\n${lastError}`);
    }
  }
  throw new Error("could not find a free port for the fake LLM:\n" + lastError);
}

async function withFakeLlm(mode, fn) {
  const server = await startFakeLlm({ EZTEST_FAKE_LLM_P4_MODE: mode });
  try {
    return await fn(server);
  } finally {
    server.stop();
  }
}

async function postChat(base, body) {
  const response = await fetch(base + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const text = await response.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    json = null;
  }
  return { status: response.status, text, json };
}

/** Extracts and parses the assistant text of a non-streaming completion. */
function parseCompletion(res, label) {
  assert.equal(res.status, 200, `${label}: expected HTTP 200, got ${res.status}: ${res.text}`);
  const content = res.json && res.json.choices && res.json.choices[0] && res.json.choices[0].message.content;
  assert.equal(typeof content, "string", `${label}: missing assistant content: ${res.text}`);
  try {
    return { content, parsed: JSON.parse(content) };
  } catch (error) {
    assert.fail(`${label}: assistant content is not JSON (${error.message}): ${content}`);
  }
}

function findFact(parsed, kind, name) {
  const fact = parsed.facts.find((candidate) => candidate.kind === kind && candidate.name === name);
  assert.ok(fact, `missing ${kind} fact ${JSON.stringify(name)} in ${JSON.stringify(parsed.facts)}`);
  return fact;
}

function assertNoEvidenceHash(parsed) {
  assert.ok(
    !JSON.stringify(parsed).includes("excerptHash"),
    "the fake LLM must never return a model-supplied excerpt hash (spec §6.3)",
  );
}

function lastLoggedP4(pathValue) {
  if (!fs.existsSync(requestLogPath)) return null;
  const lines = fs.readFileSync(requestLogPath, "utf8").split("\n").filter(Boolean);
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    let parsed;
    try {
      parsed = JSON.parse(lines[index]);
    } catch {
      continue;
    }
    if (parsed && parsed.p4 === true && (pathValue === undefined || parsed.path === pathValue)) {
      return parsed;
    }
  }
  return null;
}

let sharedServer = null;

before(async () => {
  sharedServer = await startFakeLlm({ EZTEST_FAKE_LLM_P4_MODE: "ok" });
});

after(() => {
  if (sharedServer) sharedServer.stop();
  fs.rmSync(serverLogRoot, { recursive: true, force: true });
});

test("refund.ts analysis returns a client_call with fixture-matching evidence", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const res = await postChat(sharedServer.base, fileAnalysisBody("src/api/refund.ts"));
  const { parsed } = parseCompletion(res, "refund.ts");

  assert.equal(typeof parsed.changeSummary, "string");
  assert.ok(parsed.changeSummary.length > 0);
  assert.ok(Array.isArray(parsed.facts));
  assert.ok(Array.isArray(parsed.edges));

  const fact = findFact(parsed, "client_call", "POST /api/refund");
  assert.equal(fact.path, "src/api/refund.ts");
  assert.equal(fact.confidence, "high");
  assert.equal(fact.startLine, 4);
  assert.equal(fact.endLine, 6);
  assert.deepEqual(fact.evidence, [{ file: "src/api/refund.ts", startLine: 5, endLine: 6 }]);
  assertNoEvidenceHash(parsed);

  // The evidence interval must point at the real fixture lines at HEAD.
  assertRangeContains("src/api/refund.ts", fact.evidence[0].startLine, fact.evidence[0].endLine, "/api/refund");
  assertRangeContains("src/api/refund.ts", fact.evidence[0].startLine, fact.evidence[0].endLine, "POST");
});

test("router.ts analysis returns both routes with fixture-matching evidence", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const res = await postChat(sharedServer.base, fileAnalysisBody("src/router.ts"));
  const { parsed } = parseCompletion(res, "router.ts");

  const orders = findFact(parsed, "route", "/orders");
  assert.equal(orders.path, "src/router.ts");
  assert.deepEqual(orders.evidence, [{ file: "src/router.ts", startLine: 5, endLine: 7 }]);
  assertRangeContains("src/router.ts", 5, 7, '"/orders"');

  const refundRoute = findFact(parsed, "route", "/orders/refund");
  assert.deepEqual(refundRoute.evidence, [{ file: "src/router.ts", startLine: 9, endLine: 11 }]);
  assertRangeContains("src/router.ts", 9, 11, '"/orders/refund"');
  assertNoEvidenceHash(parsed);
});

test("OrdersPage.tsx analysis returns a component fact and a component->route edge", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const res = await postChat(sharedServer.base, fileAnalysisBody("src/pages/OrdersPage.tsx"));
  const { parsed } = parseCompletion(res, "OrdersPage.tsx");

  const component = findFact(parsed, "component", "OrdersPage");
  assert.equal(component.path, "src/pages/OrdersPage.tsx");
  assert.deepEqual(component.evidence, [{ file: "src/pages/OrdersPage.tsx", startLine: 3, endLine: 3 }]);
  assertRangeContains("src/pages/OrdersPage.tsx", 3, 3, "OrdersPage");

  const edge = parsed.edges.find(
    (candidate) =>
      candidate &&
      candidate.from &&
      candidate.from.kind === "component" &&
      candidate.from.name === "OrdersPage" &&
      candidate.to &&
      candidate.to.kind === "route" &&
      candidate.to.name === "/orders",
  );
  assert.ok(edge, `missing component->route edge in ${JSON.stringify(parsed.edges)}`);
  assert.equal(edge.rel, "contains");
  assert.equal(edge.confidence, "high");
  assert.deepEqual(edge.evidence, [{ file: "src/pages/OrdersPage.tsx", startLine: 3, endLine: 3 }]);
  assertNoEvidenceHash(parsed);
});

test("refund.go analysis returns the POST /api/refund api fact", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const res = await postChat(sharedServer.base, fileAnalysisBody("server/refund.go"));
  const { parsed } = parseCompletion(res, "refund.go");

  const fact = findFact(parsed, "api", "POST /api/refund");
  assert.equal(fact.path, "server/refund.go");
  assert.equal(fact.startLine, 13);
  assert.equal(fact.endLine, 15);
  assert.deepEqual(fact.evidence, [{ file: "server/refund.go", startLine: 14, endLine: 14 }]);
  assertRangeContains("server/refund.go", 14, 14, "POST /api/refund");
  assertNoEvidenceHash(parsed);
});

test("unmapped and unknown files still return a valid empty analysis", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const format = parseCompletion(
    await postChat(sharedServer.base, fileAnalysisBody("src/utils/format.ts")),
    "format.ts",
  ).parsed;
  assert.deepEqual(format.facts, []);
  assert.deepEqual(format.edges, []);
  assert.equal(typeof format.changeSummary, "string");

  const unknown = parseCompletion(
    await postChat(
      sharedServer.base,
      fileAnalysisBody("docs/unknown.md", ["# Unknown fixture file", "", "No structured facts here."]),
    ),
    "unknown.md",
  ).parsed;
  assert.deepEqual(unknown.facts, []);
  assert.deepEqual(unknown.edges, []);
  assert.equal(typeof unknown.changeSummary, "string");
});

test("file analysis output is deterministic for the same request", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const body = fileAnalysisBody("src/api/refund.ts");
  const first = parseCompletion(await postChat(sharedServer.base, body), "first");
  const second = parseCompletion(await postChat(sharedServer.base, body), "second");
  assert.equal(first.content, second.content);
});

test("P4 requests are logged compactly with path and mode (no prompt content)", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  await postChat(sharedServer.base, fileAnalysisBody("src/api/refund.ts"));

  const logged = lastLoggedP4("src/api/refund.ts");
  assert.ok(logged, "no compact P4 request log line for src/api/refund.ts");
  assert.equal(logged.p4, true);
  assert.equal(logged.kind, "file_analysis");
  assert.equal(logged.mode, "ok");
  assert.equal(logged.commit, fixtureCommit("HEAD"));
  assert.equal(Object.hasOwn(logged, "messages"), false, "the P4 request log must not contain the prompt");
  assert.equal(Object.hasOwn(logged, "tools"), false);

  const p4Lines = fs
    .readFileSync(requestLogPath, "utf8")
    .split("\n")
    .filter((line) => line.includes('"p4":true'));
  assert.ok(p4Lines.length > 0);
  for (const line of p4Lines) {
    assert.ok(!line.includes("requestRefund"), "the P4 request log leaked fixture file content");
  }
});

test("impact analysis defaults to an empty affected list", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const { parsed } = parseCompletion(
    await postChat(sharedServer.base, impactAnalysisBody()),
    "impact default",
  );
  assert.deepEqual(parsed, { affected: [] });

  const logged = lastLoggedP4(null);
  assert.ok(logged, "no compact P4 request log line for the impact analysis");
  assert.equal(logged.kind, "impact_analysis");
  assert.equal(logged.mode, "ok");
});

test("impact-ai mode returns the requirement marked in the requirement list", async () => {
  await withFakeLlm("impact-ai", async (server) => {
    const { parsed } = parseCompletion(
      await postChat(server.base, impactAnalysisBody()),
      "impact-ai",
    );
    assert.equal(parsed.affected.length, 1);
    const candidate = parsed.affected[0];
    assert.equal(candidate.requirementId, "22");
    assert.equal(candidate.confidence, "high");
    assert.equal(typeof candidate.reason, "string");
    assert.ok(candidate.reason.length > 0);
    assert.deepEqual(candidate.evidence, [{ file: "src/api/refund.ts", startLine: 5, endLine: 6 }]);
  });
});

test("error mode returns HTTP 500 for P4 analysis requests", async () => {
  await withFakeLlm("error", async (server) => {
    const fileRes = await postChat(server.base, fileAnalysisBody("src/api/refund.ts"));
    assert.equal(fileRes.status, 500, fileRes.text);
    assert.ok(fileRes.json && fileRes.json.error && typeof fileRes.json.error.message === "string");

    const impactRes = await postChat(server.base, impactAnalysisBody());
    assert.equal(impactRes.status, 500, impactRes.text);
  });
});

test("partial mode returns invalid JSON for one file and valid JSON for the others", async () => {
  await withFakeLlm("partial", async (server) => {
    const broken = await postChat(server.base, fileAnalysisBody("src/router.ts"));
    assert.equal(broken.status, 200);
    const brokenContent = broken.json.choices[0].message.content;
    assert.throws(() => JSON.parse(brokenContent), "partial mode must return invalid JSON for src/router.ts");

    const valid = await postChat(server.base, fileAnalysisBody("src/api/refund.ts"));
    parseCompletion(valid, "partial/refund.ts");
  });
});

test("P1 fallback still answers the diagnostic scenario", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const res = await postChat(sharedServer.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "读取 README" }],
    tools: P1_TOOLS,
  });
  assert.equal(res.status, 200, res.text);
  const message = res.json.choices[0].message;
  assert.equal(message.tool_calls[0].function.name, "file_read");
  assert.deepEqual(JSON.parse(message.tool_calls[0].function.arguments), { path: "README.md" });
  assert.equal(typeof message.reasoning_content, "string");
});

test("P3 clarification scenario still answers", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const res = await postChat(sharedServer.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "P3-CLARIFY-1 version=7" }],
    tools: P1_TOOLS,
  });
  assert.equal(res.status, 200, res.text);
  const call = res.json.choices[0].message.tool_calls[0];
  assert.equal(call.function.name, "version_context_get");
  assert.deepEqual(JSON.parse(call.function.arguments), { versionId: "7" });
});

test("a non-P4 request without tools is still rejected", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const res = await postChat(sharedServer.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "hello" }],
    tools: [],
  });
  assert.equal(res.status, 400, res.text);
  assert.equal(res.json.error.message, "tools must be non-empty");
});

test("an unknown request shape still gets the terminal fallback", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const res = await postChat(sharedServer.base, {
    model: MODEL,
    stream: false,
    messages: [
      { role: "user", content: "hello" },
      { role: "assistant", content: "working" },
    ],
    tools: P1_TOOLS,
  });
  assert.equal(res.status, 200, res.text);
  assert.match(res.json.choices[0].message.content, /诊断完成/);
  assert.equal(res.json.choices[0].finish_reason, "stop");
});

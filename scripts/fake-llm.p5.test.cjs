#!/usr/bin/env node
"use strict";

// Deterministic tests for the P5 mindmap scenarios of scripts/fake-llm.cjs
// (design spec §10.6). The fake upstream is spawned on a process-unique port
// and answered through the same POST /v1/chat/completions wire shape the real
// provider uses, including the streaming path, so the tests exercise the actual
// system-prompt detection, per-run step counting, mode switch and the tool-call
// payloads the backend agent runtime consumes.
//
// The system prompts, role markers and expected tool wire names are derived from
// the backend sources by scripts/generate-fake-llm-p5-mirror.cjs and read from
// scripts/testdata/fake-llm-p5-mirror.json, so this suite cannot drift out of
// step with the product prompts again (H-D14). The staleness gate in loadMirror
// re-derives the fixture and fails on any mismatch.
//
// Run:
//   node scripts/fake-llm.p5.test.cjs

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { after, before, test } = require("node:test");
const { parseSse, parseSseChunks, foldSse } = require("./fake-llm-sse-test-helper.cjs");

const repoRoot = path.resolve(__dirname, "..");
const fakeLlmScript = path.join(__dirname, "fake-llm.cjs");
const serverLogRoot = fs.mkdtempSync(path.join(os.tmpdir(), "eztest-fake-llm-p5-"));

const MODEL = "deepseek-flash";
const VERSION_ID = "1838293000123";
const RUN_ID = "1838293000130";
const REQUIREMENT_ONE = "1838293000124";
const REQUIREMENT_TWO = "1838293000125";
const RELATION_ID = "1838293000999";
const SYSTEM_ID = "42";
const ENVIRONMENT_ID = "7";
const NODE_SCENARIO = "1838293000127";
const NODE_CASE_ONE = "1838293000128";
const NODE_CASE_TWO = "1838293000129";
const FIXTURE_BASE = "http://127.0.0.1:13335";

// Go-authoritative mirror (H-D14, Ruling T21-R1). The prompt constant names,
// [profile:<key>] markers, prompt bodies and tool wire names are DERIVED from the
// backend sources by scripts/generate-fake-llm-p5-mirror.cjs and committed as
// scripts/testdata/fake-llm-p5-mirror.json. This test no longer hand-maintains
// those strings; the staleness gate below fails the moment the fixture drifts
// from Go, and a missing/malformed fixture or Go source fails loudly too.
const mirror = require("./generate-fake-llm-p5-mirror.cjs");

function loadMirror() {
  assert.ok(
    fs.existsSync(mirror.fixturePath),
    "missing P5 mirror fixture " + mirror.fixturePath +
      " (run node scripts/generate-fake-llm-p5-mirror.cjs --write)",
  );
  const committed = JSON.parse(fs.readFileSync(mirror.fixturePath, "utf8"));
  // Staleness gate: a fresh derivation from Go must equal the committed fixture.
  const derived = JSON.parse(mirror.serialize(mirror.buildFixture()));
  assert.deepEqual(
    committed,
    derived,
    "P5 mirror fixture is stale vs the Go sources (run node scripts/generate-fake-llm-p5-mirror.cjs --write)",
  );
  return committed;
}

const MIRROR = loadMirror();
// Only scenarios with a live Go prompt are mirrored (design, review). explore was
// a removed capability (Ruling T21-R2); data_plan lost its seeded profile prompt.
const SCENARIOS = Object.keys(MIRROR.scenarios);

function scenarioBody(name) {
  return MIRROR.prompts[MIRROR.scenarios[name].promptConst];
}

function scenarioMarker(name) {
  return MIRROR.scenarios[name].marker;
}

// The tool wire names the fake must emit for one scenario, derived from the Go
// registry and sanitized exactly as backend/internal/llm does (dot -> underscore).
function scenarioWireFlow(name) {
  return MIRROR.scenarios[name].flow.map((dotted) => MIRROR.toolWire[dotted]);
}

let serverCounter = 0;

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// --- request bodies --------------------------------------------------------

/** Minimal agent tool list (Go-derived wire names); the fake echoes them. */
function agentTools() {
  const names = [...new Set(SCENARIOS.flatMap((name) => scenarioWireFlow(name)))];
  return names.map((name) => ({
    type: "function",
    function: {
      name,
      description: name,
      parameters: { type: "object", properties: {}, additionalProperties: false },
    },
  }));
}

/**
 * The P5 mindmap run instruction shape the legacy fake accepts: a `versionId=`
 * line, a `## 已确认需求` table of `- id=... title=...` rows, a `## 需求关系`
 * section and the submission footer. The original run provider
 * (backend/internal/mindmap/…, since deleted) emitted this shape; the fake only
 * parses the `versionId=`/labelled ids and the requirement tables, so the exact
 * header prose is not load-bearing.
 */
function designInstruction() {
  return [
    "## 版本",
    "versionId=" + VERSION_ID,
    "status=designing",
    "runKind=design",
    "mode=append",
    "systemId=" + SYSTEM_ID,
    "environmentId=" + ENVIRONMENT_ID,
    "baseUrl=" + FIXTURE_BASE,
    "## 已确认需求",
    "- id=" + REQUIREMENT_ONE + ' title="下单主流程" priority=must status=clarified',
    "  description: 用户提交订单时创建订单并扣减库存。",
    "- id=" + REQUIREMENT_TWO + ' title="退款流程" priority=should status=clarified',
    "  description: 用户申请退款后原路退回支付金额。",
    "## 需求关系",
    "- id=" +
      RELATION_ID +
      " from=" +
      REQUIREMENT_ONE +
      " to=" +
      REQUIREMENT_TWO +
      ' type=depends_on description="退款依赖下单"',
    "## 任务与提交要求",
    "任务：生成用例脑图（kind=design, mode=append）。",
    "必须调用 case_graph_propose 提交结构化提案。",
  ].join("\n");
}

function reviewInstruction() {
  return [
    "任务：对当前用例脑图做一次只读评审（kind=review）。",
    "versionId=" + VERSION_ID,
    "runId=" + RUN_ID,
    "已确认需求（id | 标题）：",
    REQUIREMENT_ONE + " | 下单主流程",
    REQUIREMENT_TWO + " | 退款流程",
    "必须调用 design.review_submit 提交 findings。",
  ].join("\n");
}

/** The graph summary a real design.graph_read tool returns (spec §8.2/§7.3). */
function graphReadResult() {
  return {
    data: {
      versionId: VERSION_ID,
      versionStatus: "designing",
      editable: true,
      nodes: [
        {
          id: NODE_SCENARIO,
          parentId: null,
          kind: "scenario",
          title: "P5 场景",
          priority: "P2",
          revision: 1,
          steps: [],
          assertions: [],
          sources: [],
          anchors: [],
        },
        {
          id: NODE_CASE_ONE,
          parentId: NODE_SCENARIO,
          kind: "case",
          title: "P5 正常用例",
          priority: "P1",
          revision: 3,
          steps: [
            { id: "1838293000201", seq: 1, action: "打开页面" },
            { id: "1838293000202", seq: 2, action: "提交操作" },
          ],
          assertions: [{ id: "1838293000203", stepId: null, kind: "ui", expression: "成功提示可见" }],
          sources: [{ id: "1838293000204", refKind: "requirement", refId: REQUIREMENT_ONE }],
          anchors: [],
        },
        {
          id: NODE_CASE_TWO,
          parentId: NODE_SCENARIO,
          kind: "case",
          title: "P5 边界用例",
          priority: "P2",
          revision: 2,
          steps: [{ id: "1838293000205", seq: 1, action: "提交非法输入" }],
          assertions: [{ id: "1838293000206", stepId: null, kind: "api", expression: "返回 400" }],
          sources: [{ id: "1838293000207", refKind: "requirement", refId: REQUIREMENT_TWO }],
          anchors: [],
        },
      ],
      coverage: {
        requirementIds: [REQUIREMENT_ONE, REQUIREMENT_TWO],
        uncoveredRequirementIds: [],
        staleRequirementIds: [],
        matrix: [
          { requirementId: REQUIREMENT_ONE, caseIds: [NODE_CASE_ONE] },
          { requirementId: REQUIREMENT_TWO, caseIds: [NODE_CASE_TWO] },
        ],
      },
    },
  };
}

// --- server ----------------------------------------------------------------

/**
 * Starts one fake-llm.cjs on a process-unique port. The port is retried on
 * EADDRINUSE so these tests can run next to a manual dev server.
 */
async function startFakeLlm(envOverrides) {
  let lastError = null;
  for (let attempt = 0; attempt < 10; attempt += 1) {
    serverCounter += 1;
    const port = 15000 + ((process.pid + serverCounter * 41 + attempt * 13) % 9000);
    const logPath = path.join(serverLogRoot, "fake-llm-" + serverCounter + ".log");
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

    const base = "http://127.0.0.1:" + port;
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
      throw new Error("fake LLM did not start on " + base + ":\n" + lastError);
    }
  }
  throw new Error("could not find a free port for the fake LLM:\n" + lastError);
}

async function withFakeLlm(mode, fn) {
  const server = await startFakeLlm({ EZTEST_FAKE_LLM_P5_MODE: mode });
  try {
    return await fn(server);
  } finally {
    server.stop();
  }
}

// --- wire helpers ----------------------------------------------------------

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

/** Sends one non-streaming chat request and asserts a usable completion. */
async function chat(base, messages) {
  const res = await postChat(base, {
    model: MODEL,
    stream: false,
    messages,
    tools: agentTools(),
  });
  assert.equal(res.status, 200, "chat request failed: " + res.text);
  assert.ok(res.json && Array.isArray(res.json.choices), "missing choices: " + res.text);
  return res.json.choices[0];
}

/** Sends one streaming chat request; returns the folded turn plus raw chunks. */
async function streamChat(base, messages) {
  const response = await fetch(base + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ model: MODEL, stream: true, messages, tools: agentTools() }),
  });
  assert.equal(response.status, 200);
  const text = await response.text();
  assert.match(text, /^data: /m, "the fake LLM must answer with SSE frames");
  assert.ok(text.includes("[DONE]"), "the SSE stream must end with [DONE]");
  const chunks = parseSseChunks(text);
  const folded = foldSse(chunks);
  folded.chunks = chunks;
  return folded;
}

/** Asserts a completion is one tool call with an underscore-only name. */
function assertToolCall(choice, expectedName) {
  const calls = choice.message.tool_calls || [];
  assert.equal(calls.length, 1, "expected exactly one tool call, got " + calls.length);
  const call = calls[0];
  assert.equal(call.function.name, expectedName);
  assertWireToolName(call.function.name);
  assert.equal(choice.finish_reason, "tool_calls");
  return JSON.parse(call.function.arguments);
}

/** Tool function names use the current Go registry sanitized to wire form. */
function assertWireToolName(name) {
  assert.equal(typeof name, "string");
  assert.ok(name.length > 0, "tool name must not be empty");
  assert.ok(!name.includes("."), "tool name " + JSON.stringify(name) + " must not contain a dot");
  assert.match(name, /^[a-z][a-z0-9_]*$/, "tool name " + JSON.stringify(name) + " must be lowercase snake_case");
}

/** Appends the assistant tool turn plus its tool result to the history. */
function appendToolResult(messages, choice, content) {
  const call = choice.message.tool_calls[0];
  messages.push(choice.message);
  messages.push({
    role: "tool",
    tool_call_id: call.id,
    content: typeof content === "string" ? content : JSON.stringify(content),
  });
}

/** Asserts a completion is terminal text with no tool call. */
function assertTextTurn(choice, label) {
  assert.equal(choice.finish_reason, "stop", label + ": expected stop");
  assert.equal(choice.message.tool_calls, undefined, label + ": text turn must not call tools");
  assert.equal(typeof choice.message.content, "string", label + ": missing text content");
  assert.ok(choice.message.content.length > 0, label + ": empty text content");
  return choice.message.content;
}

// --- conversation drivers --------------------------------------------------

/**
 * Drives one mock mindmap run: each expected tool name is requested in turn, the
 * matching tool result is appended and the final text turn is returned.
 */
async function driveRun(base, system, user, toolResults, expectedNames) {
  const messages = [
    { role: "system", content: system },
    { role: "user", content: user },
  ];
  const calls = [];
  for (let index = 0; index < expectedNames.length; index += 1) {
    const choice = await chat(base, messages);
    const args = assertToolCall(choice, expectedNames[index]);
    calls.push({ name: choice.message.tool_calls[0].function.name, args });
    const toolResult = toolResults[index] === undefined ? { ok: true } : toolResults[index];
    appendToolResult(messages, choice, toolResult);
  }
  const finalChoice = await chat(base, messages);
  const text = assertTextTurn(finalChoice, "final turn");
  return { calls, text, finalChoice };
}

function assertDesignProposal(args) {
  assert.equal(args.versionId, VERSION_ID);
  assert.equal(args.mode === undefined || args.mode === "append", true);

  assert.ok(Array.isArray(args.scenarios), "scenarios must be an array");
  assert.equal(args.scenarios.length, 1, "the fake proposal must carry exactly one scenario");
  const scenarioRef = args.scenarios[0].ref;
  assert.equal(typeof scenarioRef, "string");
  assert.ok(scenarioRef.length > 0);
  assert.equal(typeof args.scenarios[0].title, "string");
  assert.ok(args.scenarios[0].title.length > 0);

  assert.ok(Array.isArray(args.cases), "cases must be an array");
  assert.equal(args.cases.length, 2, "the fake proposal must carry exactly two cases");
  const refs = args.cases.map((item) => item.ref);
  assert.equal(new Set(refs).size, refs.length, "case refs must be unique");

  const expectedSources = [[REQUIREMENT_ONE], [REQUIREMENT_TWO]];
  const expectedPriorities = ["P1", "P2"];
  for (let index = 0; index < args.cases.length; index += 1) {
    const item = args.cases[index];
    const label = "case[" + index + "]";
    assert.equal(item.scenarioRef, scenarioRef, label + ": must reference the scenario");
    assert.equal(typeof item.title, "string");
    assert.ok(item.title.length > 0, label + ": empty title");
    assert.equal(item.priority, expectedPriorities[index], label + ": unexpected priority");
    assert.match(item.priority, /^P[0-3]$/);
    assert.deepEqual(item.requirementIds, expectedSources[index], label + ": requirement source");
    assert.ok(Array.isArray(item.steps), label + ": steps must be an array");
    assert.equal(item.steps.length, 2, label + ": the fake proposal must carry two steps");
    for (const step of item.steps) {
      assert.equal(typeof step.action, "string");
      assert.ok(step.action.length > 0, label + ": step action must not be empty");
      assert.equal(typeof step.expected, "string");
    }
    assert.ok(Array.isArray(item.assertions), label + ": assertions must be an array");
    assert.equal(item.assertions.length, 1, label + ": the fake proposal must carry one assertion");
    for (const assertion of item.assertions) {
      assert.ok(Number.isInteger(assertion.stepIndex), label + ": stepIndex must be an integer");
      assert.ok(assertion.stepIndex >= 0 && assertion.stepIndex < item.steps.length);
      assert.ok(["ui", "api", "db", "text"].includes(assertion.kind), label + ": invalid assertion kind");
      assert.equal(typeof assertion.expression, "string");
      assert.ok(assertion.expression.length > 0);
    }
  }
  assert.equal(typeof args.cases[0].preconditions, "string");
  assert.ok(args.cases[0].testData !== undefined, "case[0]: testData must be provided");
}

function assertReviewFindings(args) {
  assert.equal(args.versionId, VERSION_ID);
  assert.ok(Array.isArray(args.findings), "findings must be an array");
  assert.equal(args.findings.length, 4, "the fake review must carry four findings");
  const validCategories = new Set([
    "coverage",
    "assertion",
    "executability",
    "duplicate",
    "code_uncovered",
    "other",
  ]);
  const nodeIds = new Set([NODE_CASE_ONE, NODE_CASE_TWO]);
  for (const finding of args.findings) {
    assert.ok(validCategories.has(finding.category), "invalid category " + String(finding.category));
    assert.ok(["blocker", "major", "minor", "info"].includes(finding.severity));
    assert.equal(typeof finding.detail, "string");
    assert.ok(finding.detail.length > 0 && finding.detail.length <= 2000);
    if (finding.nodeId !== undefined) {
      assert.equal(typeof finding.nodeId, "string");
      assert.ok(nodeIds.has(finding.nodeId), "unknown nodeId " + finding.nodeId);
    }
  }

  // All four spec §8.2 categories are pinned with their severity, so dropping
  // any one of them (e.g. the assertion finding) fails this test.
  const byCategory = new Map(args.findings.map((finding) => [finding.category, finding]));
  for (const category of ["coverage", "assertion", "duplicate", "code_uncovered"]) {
    assert.ok(byCategory.has(category), "the review must report one " + category + " finding");
  }
  assert.equal(byCategory.get("coverage").severity, "major", "the coverage finding must be major");
  assert.ok(
    ["major", "minor"].includes(byCategory.get("assertion").severity),
    "the assertion finding must be major or minor",
  );
  assert.equal(byCategory.get("duplicate").severity, "minor", "the duplicate finding must be minor");
  const blockers = args.findings.filter((finding) => finding.severity === "blocker");
  assert.equal(blockers.length, 1, "the review must carry exactly one blocker");
  assert.equal(
    blockers[0].category,
    "code_uncovered",
    "the blocker must be the code_uncovered finding",
  );
}

// --- shared server ---------------------------------------------------------

let sharedServer = null;

before(async () => {
  sharedServer = await startFakeLlm({ EZTEST_FAKE_LLM_P5_MODE: "normal" });
});

after(() => {
  if (sharedServer) sharedServer.stop();
  fs.rmSync(serverLogRoot, { recursive: true, force: true });
});

// --- prompt drift ----------------------------------------------------------

test("each live P5 scenario's current Go prompt carries its derived marker", () => {
  for (const scenario of SCENARIOS) {
    const body = scenarioBody(scenario);
    const marker = scenarioMarker(scenario);
    assert.match(marker, /^\[profile:[a-z0-9_]+\]$/, "marker " + marker + " must be a [profile:<key>] marker");
    assert.ok(
      body.includes(marker),
      scenario + " prompt no longer contains the derived marker " + marker,
    );
  }
});

// --- shared SSE parser (Ruling T21-R4) -------------------------------------

test("shared parseSse accepts both data: forms and fails on malformed lines", () => {
  // Both `data: {...}` and `data:{...}` are valid SSE; neither frame may be
  // silently dropped (the no-space form used to fall outside the prefix match).
  const spaced = parseSse(
    'data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"a"}}]}\n\n',
  );
  assert.equal(spaced.content, "a", "the spaced data: frame must fold");
  const compact = parseSse(
    'data:{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"b"}}]}\n\n',
  );
  assert.equal(compact.content, "b", "the no-space data: frame must fold");
  // A malformed line that still starts with `data:` must fail, never be skipped.
  assert.throws(
    () => parseSse("data: {not json}\n\n"),
    /non-JSON data line/,
    "parseSse must fail loudly on malformed spaced SSE",
  );
  assert.throws(
    () => parseSse("data:{not json}\n\n"),
    /non-JSON data line/,
    "parseSse must fail loudly on malformed no-space SSE",
  );
});

// --- design ----------------------------------------------------------------

test("design role: design_graph_read -> case_graph_propose -> final text", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = scenarioBody("design");
  const { calls, text } = await driveRun(
    sharedServer.base,
    system,
    designInstruction(),
    [graphReadResult(), { ok: true }],
    scenarioWireFlow("design"),
  );

  assert.deepEqual(calls[0].args, { versionId: VERSION_ID });
  assertDesignProposal(calls[1].args);
  assert.match(text, /P5/);
});

test("design role streaming path emits framed chunks and the design_graph_read call", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = scenarioBody("design");
  const turn = await streamChat(sharedServer.base, [
    { role: "system", content: system },
    { role: "user", content: designInstruction() },
  ]);

  // Per-chunk framing pin (Ruling T21-R4): every chunk must carry the real
  // OpenAI chat-completions.chunk envelope the legacy streamToolTurn emits.
  assert.ok(turn.chunks.length > 0, "expected at least one SSE chunk");
  for (let index = 0; index < turn.chunks.length; index += 1) {
    const chunk = turn.chunks[index];
    const label = "chunk[" + index + "]";
    assert.equal(typeof chunk.id, "string", label + " id");
    assert.ok(chunk.id.startsWith("chatcmpl-fake-"), label + " id prefix");
    assert.equal(chunk.object, "chat.completion.chunk", label + " object");
    assert.equal(typeof chunk.created, "number", label + " created");
    assert.equal(chunk.model, MODEL, label + " model");
    assert.ok(Array.isArray(chunk.choices) && chunk.choices.length === 1, label + " choices");
    assert.equal(chunk.choices[0].index, 0, label + " choice index");
    assert.ok(
      chunk.choices[0].delta !== null && typeof chunk.choices[0].delta === "object",
      label + " delta",
    );
  }
  // The legacy tool turn opens with reasoning fragments, then the tool call.
  assert.equal(typeof turn.chunks[0].choices[0].delta.reasoning_content, "string");
  assert.ok(turn.reasoning.length > 0, "the tool turn must carry reasoning_content");

  const terminal = turn.chunks[turn.chunks.length - 1];
  assert.equal(terminal.choices[0].finish_reason, "tool_calls", "terminal finish_reason");
  assert.ok(terminal.usage && typeof terminal.usage.total_tokens === "number", "terminal usage");

  assert.equal(turn.finishReason, "tool_calls");
  assert.equal(turn.toolCalls.length, 1);
  assert.equal(turn.toolCalls[0].name, scenarioWireFlow("design")[0]);
  assertWireToolName(turn.toolCalls[0].name);
  assert.deepEqual(JSON.parse(turn.toolCalls[0].arguments), { versionId: VERSION_ID });
});

test("design role stops the requirement table at the next heading", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = scenarioBody("design");
  // One requirement plus one relation: if the parser ran past the heading, the
  // relation id would become the second case's requirement source.
  const instruction = [
    "versionId=" + VERSION_ID,
    "## 已确认需求",
    "- id=" + REQUIREMENT_ONE + ' title="下单主流程" priority=must status=clarified',
    "## 需求关系",
    "- id=" + RELATION_ID + " from=" + REQUIREMENT_ONE + " to=" + REQUIREMENT_ONE,
  ].join("\n");
  const { calls } = await driveRun(
    sharedServer.base,
    system,
    instruction,
    [graphReadResult(), { ok: true }],
    scenarioWireFlow("design"),
  );

  assert.deepEqual(calls[1].args.cases[0].requirementIds, [REQUIREMENT_ONE]);
  assert.deepEqual(calls[1].args.cases[1].requirementIds, [REQUIREMENT_ONE]);
  assert.ok(
    !JSON.stringify(calls[1].args.cases).includes(RELATION_ID),
    "a relation id must never become a case requirement source",
  );
});

test("design role emits empty requirementIds instead of fabricating ids", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = scenarioBody("design");
  // No labelled requirement list/table: runId and the graph coverage ids must
  // not be guessed as requirement sources (server validation must fail loudly).
  const instruction = [
    "versionId=" + VERSION_ID,
    "runId=" + RUN_ID,
    "systemId=" + SYSTEM_ID,
    "必须调用 case_graph_propose 提交结构化提案。",
  ].join("\n");
  const { calls } = await driveRun(
    sharedServer.base,
    system,
    instruction,
    [graphReadResult(), { ok: true }],
    scenarioWireFlow("design"),
  );

  for (const item of calls[1].args.cases) {
    assert.deepEqual(
      item.requirementIds,
      [],
      "without an explicit requirement list/table no requirement id may be emitted",
    );
  }
});

test("design role still tolerates the legacy pipe-separated requirement table", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = scenarioBody("design");
  const instruction = [
    "versionId=" + VERSION_ID,
    "已确认需求（id | 标题）：",
    REQUIREMENT_ONE + " | 下单主流程",
    REQUIREMENT_TWO + " | 退款流程",
  ].join("\n");
  const { calls } = await driveRun(
    sharedServer.base,
    system,
    instruction,
    [graphReadResult(), { ok: true }],
    scenarioWireFlow("design"),
  );

  assert.deepEqual(calls[1].args.cases[0].requirementIds, [REQUIREMENT_ONE]);
  assert.deepEqual(calls[1].args.cases[1].requirementIds, [REQUIREMENT_TWO]);
});

// --- review ----------------------------------------------------------------

test("review role: design_graph_read -> design_review_submit -> final text", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = scenarioBody("review");
  const { calls, text } = await driveRun(
    sharedServer.base,
    system,
    reviewInstruction(),
    [graphReadResult(), { ok: true }],
    scenarioWireFlow("review"),
  );

  assert.deepEqual(calls[0].args, { versionId: VERSION_ID });
  assertReviewFindings(calls[1].args);
  assert.match(text, /P5/);
});

// --- modes -----------------------------------------------------------------

test("invalid mode returns a proposal the server must reject", async () => {
  await withFakeLlm("invalid", async (server) => {
    const system = scenarioBody("design");
    const messages = [
      { role: "system", content: system },
      { role: "user", content: designInstruction() },
    ];

    const readChoice = await chat(server.base, messages);
    assertToolCall(readChoice, scenarioWireFlow("design")[0]);
    appendToolResult(messages, readChoice, graphReadResult());

    const proposeChoice = await chat(server.base, messages);
    const args = assertToolCall(proposeChoice, scenarioWireFlow("design")[1]);
    assert.equal(args.versionId, VERSION_ID);

    // Every independent §6.7 rule is broken at least once.
    const refs = args.cases.map((item) => item.ref);
    assert.ok(new Set(refs).size < refs.length, "invalid mode must duplicate case refs");
    const missingSources = args.cases.filter(
      (item) => !Array.isArray(item.requirementIds) || item.requirementIds.length === 0,
    );
    assert.ok(missingSources.length >= 1, "invalid mode must omit a required requirement source");
    const badStepIndexes = [];
    for (const item of args.cases) {
      for (const assertion of item.assertions || []) {
        if (!Number.isInteger(assertion.stepIndex) || assertion.stepIndex >= (item.steps || []).length) {
          badStepIndexes.push(assertion.stepIndex);
        }
      }
    }
    assert.ok(badStepIndexes.length >= 1, "invalid mode must use an out-of-range stepIndex");
    const badPriorities = args.cases.filter((item) => !/^P[0-3]$/.test(String(item.priority)));
    assert.ok(badPriorities.length >= 1, "invalid mode must use an unknown priority");
  });
});

test("degraded mode returns text only for every live role", async () => {
  await withFakeLlm("degraded", async (server) => {
    const instructions = { design: designInstruction, review: reviewInstruction };
    for (const scenario of SCENARIOS) {
      const choice = await chat(server.base, [
        { role: "system", content: scenarioBody(scenario) },
        { role: "user", content: instructions[scenario]() },
      ]);
      const text = assertTextTurn(choice, scenario + " degraded");
      assert.match(text, /降级/);
    }
  });
});

test("the runtime P5 mode control switches, rejects unknown modes and resets", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");

  const getMode = async () => {
    const response = await fetch(sharedServer.base + "/__p5-mode");
    assert.equal(response.status, 200, "GET /__p5-mode must answer 200");
    return (await response.json()).mode;
  };
  const setMode = async (mode) => {
    const response = await fetch(sharedServer.base + "/__p5-mode", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode }),
    });
    assert.equal(response.status, 200, "POST /__p5-mode must answer 200");
    return (await response.json()).mode;
  };

  try {
    assert.equal(await getMode(), "normal", "the default P5 mode must be normal");

    assert.equal(await setMode("invalid"), "invalid");
    assert.equal(await getMode(), "invalid");
    const designMessages = [
      { role: "system", content: scenarioBody("design") },
      { role: "user", content: designInstruction() },
    ];
    const readChoice = await chat(sharedServer.base, designMessages);
    assertToolCall(readChoice, scenarioWireFlow("design")[0]);
    appendToolResult(designMessages, readChoice, graphReadResult());
    const proposeChoice = await chat(sharedServer.base, designMessages);
    const invalidArgs = assertToolCall(proposeChoice, scenarioWireFlow("design")[1]);
    assert.ok(
      invalidArgs.cases.some((item) => !Array.isArray(item.requirementIds) || item.requirementIds.length === 0),
      "the runtime invalid mode must take effect without a restart",
    );

    assert.equal(await setMode("degraded"), "degraded");
    assert.equal(await getMode(), "degraded");
    const degraded = await chat(sharedServer.base, [
      { role: "system", content: scenarioBody("design") },
      { role: "user", content: designInstruction() },
    ]);
    assertTextTurn(degraded, "runtime degraded");

    const rejected = await fetch(sharedServer.base + "/__p5-mode", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode: "not-a-mode" }),
    });
    assert.equal(rejected.status, 400, "an unknown mode must be rejected");
    assert.equal(await getMode(), "degraded", "a rejected mode must not change the active mode");
  } finally {
    await setMode("normal");
  }
});

// --- regression guard: P1/P3/P4 dispatch is untouched ----------------------

test("P1, P3 and P4 requests keep their pre-P5 behavior", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");

  const p1 = await postChat(sharedServer.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "读取 README" }],
    tools: agentTools(),
  });
  assert.equal(p1.status, 200, p1.text);
  const p1Call = p1.json.choices[0].message.tool_calls[0];
  assert.equal(p1Call.function.name, "file.read");
  assert.deepEqual(JSON.parse(p1Call.function.arguments), { path: "README.md" });

  const p3 = await postChat(sharedServer.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "P3-CLARIFY-1 version=7" }],
    tools: agentTools(),
  });
  assert.equal(p3.status, 200, p3.text);
  const p3Call = p3.json.choices[0].message.tool_calls[0];
  assert.equal(p3Call.function.name, "version_context_get");
  assert.deepEqual(JSON.parse(p3Call.function.arguments), { versionId: "7" });

  const p4 = await postChat(sharedServer.base, {
    model: MODEL,
    stream: false,
    messages: [
      { role: "system", content: "EzTest 的只读代码分析器。只输出 JSON 对象。" },
      { role: "user", content: "文件：README.md\n提交：deadbeef" },
    ],
  });
  assert.equal(p4.status, 200, p4.text);
  const analysis = JSON.parse(p4.json.choices[0].message.content);
  assert.ok(Array.isArray(analysis.facts));
  assert.ok(Array.isArray(analysis.edges));
  assert.equal(typeof analysis.changeSummary, "string");
});

test("a non-P5 request without tools is still rejected", async () => {
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

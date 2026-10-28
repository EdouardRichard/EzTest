#!/usr/bin/env node
"use strict";

// Deterministic tests for the P5 mindmap scenarios of scripts/fake-llm.cjs
// (design spec §10.6). The fake upstream is spawned on a process-unique port
// and answered through the same POST /v1/chat/completions wire shape the real
// provider uses, including the streaming path, so the tests exercise the actual
// system-prompt detection, per-run step counting, mode switch and the tool-call
// payloads the backend agent runtime consumes.
//
// The system prompts are read from backend/internal/mindmap/domain/prompts.go
// instead of being duplicated here: the detector must keep matching the real
// Chinese role markers (task T7).
//
// Run:
//   node scripts/fake-llm.p5.test.cjs

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { after, before, test } = require("node:test");

const repoRoot = path.resolve(__dirname, "..");
const fakeLlmScript = path.join(__dirname, "fake-llm.cjs");
const promptsPath = path.join(repoRoot, "backend", "internal", "mindmap", "domain", "prompts.go");
const serverLogRoot = fs.mkdtempSync(path.join(os.tmpdir(), "eztest-fake-llm-p5-"));

const MODEL = "deepseek-flash";
const VERSION_ID = "1838293000123";
const RUN_ID = "1838293000130";
const REQUIREMENT_ONE = "1838293000124";
const REQUIREMENT_TWO = "1838293000125";
const SYSTEM_ID = "42";
const ENVIRONMENT_ID = "7";
const NODE_SCENARIO = "1838293000127";
const NODE_CASE_ONE = "1838293000128";
const NODE_CASE_TWO = "1838293000129";
const FIXTURE_BASE = "http://127.0.0.1:13335";

const PROMPT_CONSTS = {
  design: "DesignPrompt",
  review: "ReviewPrompt",
  data_plan: "DataPlanPrompt",
  explore: "ExplorePrompt",
};

// The exact Chinese role markers of spec §8.1/§10.6. They must stay substrings
// of the real prompt constants below. The data-plan prompt landed with the
// "数据/前置规划" spelling, so both variants are accepted.
const ROLE_MARKERS = {
  design: ["EzTest 的测试设计 Agent"],
  review: ["EzTest 的设计评审 Agent"],
  data_plan: ["EzTest 的数据与前置规划 Agent", "EzTest 的数据/前置规划 Agent"],
  explore: ["EzTest 的应用探索 Agent"],
};

const TOOL_NAMES = {
  design: ["case_graph_read", "case_graph_propose"],
  review: ["case_graph_read", "case_review_submit"],
  data_plan: ["case_graph_read", "case_data_plan_submit"],
  explore: ["explore_http", "explore_http", "app_model_propose"],
};

let serverCounter = 0;

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// --- prompts ---------------------------------------------------------------

function readPromptsSource() {
  assert.ok(
    fs.existsSync(promptsPath),
    "missing backend/internal/mindmap/domain/prompts.go; task T7 must land first",
  );
  return fs.readFileSync(promptsPath, "utf8");
}

/** Extracts one prompt constant body from prompts.go. */
function promptBody(name, source) {
  const pattern = new RegExp("const " + name + " = `([\\s\\S]*?)`");
  const match = pattern.exec(source);
  assert.ok(match, "prompts.go has no " + name + " constant");
  return match[1];
}

// --- request bodies --------------------------------------------------------

/** Minimal agent tool list; the fake validates tool names only by echoing them. */
function agentTools() {
  return [
    "case_graph_read",
    "case_graph_propose",
    "case_review_submit",
    "case_data_plan_submit",
    "app_model_propose",
    "explore_http",
  ].map((name) => ({
    type: "function",
    function: {
      name,
      description: name,
      parameters: { type: "object", properties: {}, additionalProperties: false },
    },
  }));
}

function designInstruction() {
  return [
    "任务：生成用例脑图（kind=design, mode=append）。",
    "versionId=" + VERSION_ID,
    "runId=" + RUN_ID,
    "已确认需求（id | 标题 | 描述摘要）：",
    REQUIREMENT_ONE + " | 下单主流程 | 用户提交订单并扣减库存。",
    REQUIREMENT_TWO + " | 退款流程 | 用户申请退款并原路退回。",
    "systemId=" + SYSTEM_ID,
    "environmentId=" + ENVIRONMENT_ID,
    "baseUrl=" + FIXTURE_BASE,
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
    "必须调用 case_review_submit 提交 findings。",
  ].join("\n");
}

function dataPlanInstruction() {
  return [
    "任务：为已有用例补齐前置条件与测试数据（kind=data_plan）。",
    "versionId=" + VERSION_ID,
    "runId=" + RUN_ID,
    "节点清单（id | revision | 标题）：",
    NODE_CASE_ONE + " | 3 | P5 正常用例",
    "必须调用 case_data_plan_submit 提交 items。",
  ].join("\n");
}

function exploreInstruction() {
  return [
    "任务：只读探索应用并提交应用模型（kind=explore）。",
    "versionId=" + VERSION_ID,
    "runId=" + RUN_ID,
    "systemId=" + SYSTEM_ID,
    "environmentId=" + ENVIRONMENT_ID,
    "baseUrl=" + FIXTURE_BASE,
    "只允许 GET/HEAD，必须调用 app_model_propose 提交 nodes。",
  ].join("\n");
}

/** The graph summary a real case_graph_read tool returns (spec §8.2/§7.3). */
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

/** Sends one streaming chat request and folds the SSE fragments together. */
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
  return parseSse(text);
}

/** Folds the OpenAI SSE chunks of one assistant turn into a single message. */
function parseSse(text) {
  const result = { content: "", reasoning: "", toolCalls: [], finishReason: null };
  for (const line of text.split("\n")) {
    if (!line.startsWith("data: ")) continue;
    const data = line.slice("data: ".length).trim();
    if (data === "" || data === "[DONE]") continue;
    let chunk;
    try {
      chunk = JSON.parse(data);
    } catch {
      continue;
    }
    const choice = chunk && Array.isArray(chunk.choices) ? chunk.choices[0] : null;
    if (!choice) continue;
    const delta = choice.delta || {};
    if (typeof delta.content === "string") result.content += delta.content;
    if (typeof delta.reasoning_content === "string") result.reasoning += delta.reasoning_content;
    if (Array.isArray(delta.tool_calls)) {
      for (const fragment of delta.tool_calls) {
        const index = typeof fragment.index === "number" ? fragment.index : 0;
        const slot = result.toolCalls[index] || (result.toolCalls[index] = { id: "", type: "", name: "", arguments: "" });
        if (fragment.id) slot.id = fragment.id;
        if (fragment.type) slot.type = fragment.type;
        if (fragment.function) {
          if (fragment.function.name) slot.name = fragment.function.name;
          if (typeof fragment.function.arguments === "string") {
            slot.arguments += fragment.function.arguments;
          }
        }
      }
    }
    if (choice.finish_reason) result.finishReason = choice.finish_reason;
  }
  return result;
}

/** Asserts a completion is one tool call with an underscore-only name. */
function assertToolCall(choice, expectedName) {
  const calls = choice.message.tool_calls || [];
  assert.equal(calls.length, 1, "expected exactly one tool call, got " + calls.length);
  const call = calls[0];
  assert.equal(call.function.name, expectedName);
  assertUnderscoreToolName(call.function.name);
  assert.equal(choice.finish_reason, "tool_calls");
  return JSON.parse(call.function.arguments);
}

/** Spec §10.6: tool function names use the P1 dot->underscore mapping. */
function assertUnderscoreToolName(name) {
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
  assert.equal(args.findings.length, 3, "the fake review must carry three findings");
  const categories = new Set();
  const severities = new Set();
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
    categories.add(finding.category);
    severities.add(finding.severity);
  }
  assert.ok(categories.has("coverage"), "the review must report one coverage finding");
  assert.ok(categories.has("duplicate"), "the review must report one duplicate finding");
  assert.ok(categories.has("code_uncovered"), "the review must report one code_uncovered finding");
  assert.ok(severities.has("major"));
  assert.ok(severities.has("minor"));
  assert.equal(
    args.findings.filter((finding) => finding.severity === "blocker").length,
    1,
    "the review must carry exactly one blocker",
  );
  assert.equal(
    args.findings.find((finding) => finding.severity === "blocker").category,
    "code_uncovered",
    "the blocker must be the code_uncovered finding",
  );
}

function assertDataPlanItems(args) {
  assert.equal(args.versionId, VERSION_ID);
  assert.ok(Array.isArray(args.items), "items must be an array");
  assert.ok(args.items.length >= 1, "the fake data plan must target the existing case");
  const nodeIds = new Set([NODE_CASE_ONE, NODE_CASE_TWO]);
  for (const item of args.items) {
    assert.equal(typeof item.nodeId, "string");
    assert.ok(nodeIds.has(item.nodeId), "unknown data plan nodeId " + item.nodeId);
    assert.ok(Number.isInteger(item.nodeRevision), "nodeRevision must be an integer");
    assert.ok(item.nodeRevision >= 1, "nodeRevision must be positive");
  }
  const first = args.items.find((item) => item.nodeId === NODE_CASE_ONE);
  assert.ok(first, "the fake data plan must target the first case node");
  assert.equal(first.nodeRevision, 3, "the nodeRevision must come from case_graph_read");
  assert.equal(typeof first.preconditions, "string");
  assert.ok(first.preconditions.length > 0);
  assert.ok(first.testData !== undefined, "the fake data plan must provide testData");
}

function assertAppModelProposal(args) {
  assert.equal(String(args.systemId), SYSTEM_ID);
  assert.ok(Array.isArray(args.nodes), "nodes must be an array");
  const kinds = new Set(args.nodes.map((node) => node.kind));
  for (const kind of ["page", "route", "api"]) {
    assert.ok(kinds.has(kind), "the app model proposal must include a " + kind + " node");
  }
  for (const node of args.nodes) {
    assert.equal(typeof node.key, "string");
    assert.equal(typeof node.name, "string");
    assert.ok(node.name.length > 0);
    if (node.kind === "page") assert.ok(node.key.startsWith("page:"), "page key " + node.key);
    if (node.kind === "route") assert.ok(node.key.startsWith("route:"), "route key " + node.key);
    if (node.kind === "api") {
      assert.match(node.key, /^api:[A-Z]+ \S/, "api key " + node.key);
      assert.equal(node.method, "GET");
    }
    if (node.path !== undefined) assert.ok(node.path.startsWith("/"), "node path " + node.path);
  }
}

/** Asserts one explore_http call targets a fixture page with GET. */
function assertExploreCall(args, expectedPath) {
  assert.equal(typeof args.url, "string");
  const url = new URL(args.url);
  assert.equal(url.origin, FIXTURE_BASE);
  assert.equal(url.pathname, expectedPath);
  assert.equal(args.method === undefined || args.method === "GET", true);
}

// --- shared server ---------------------------------------------------------

let sharedServer = null;
let promptsSource = "";

before(async () => {
  promptsSource = readPromptsSource();
  sharedServer = await startFakeLlm({ EZTEST_FAKE_LLM_P5_MODE: "normal" });
});

after(() => {
  if (sharedServer) sharedServer.stop();
  fs.rmSync(serverLogRoot, { recursive: true, force: true });
});

// --- prompt drift ----------------------------------------------------------

test("prompts.go still contains the four role markers the detector matches", () => {
  for (const [kind, name] of Object.entries(PROMPT_CONSTS)) {
    const body = promptBody(name, promptsSource);
    const markers = ROLE_MARKERS[kind];
    assert.ok(
      markers.some((marker) => body.includes(marker)),
      name + " no longer contains any P5 role marker of " + JSON.stringify(markers),
    );
  }
});

// --- design ----------------------------------------------------------------

test("design role: case_graph_read -> case_graph_propose -> final text", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = promptBody(PROMPT_CONSTS.design, promptsSource);
  const { calls, text } = await driveRun(
    sharedServer.base,
    system,
    designInstruction(),
    [graphReadResult(), { ok: true }],
    TOOL_NAMES.design,
  );

  assert.deepEqual(calls[0].args, { versionId: VERSION_ID });
  assertDesignProposal(calls[1].args);
  assert.match(text, /P5/);
});

test("design role streaming path emits the same case_graph_read tool call", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = promptBody(PROMPT_CONSTS.design, promptsSource);
  const turn = await streamChat(sharedServer.base, [
    { role: "system", content: system },
    { role: "user", content: designInstruction() },
  ]);

  assert.equal(turn.finishReason, "tool_calls");
  assert.equal(turn.toolCalls.length, 1);
  assert.equal(turn.toolCalls[0].name, "case_graph_read");
  assertUnderscoreToolName(turn.toolCalls[0].name);
  assert.ok(turn.reasoning.length > 0, "the tool turn must carry reasoning_content");
  assert.deepEqual(JSON.parse(turn.toolCalls[0].arguments), { versionId: VERSION_ID });
});

// --- review ----------------------------------------------------------------

test("review role: case_graph_read -> case_review_submit -> final text", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = promptBody(PROMPT_CONSTS.review, promptsSource);
  const { calls, text } = await driveRun(
    sharedServer.base,
    system,
    reviewInstruction(),
    [graphReadResult(), { ok: true }],
    TOOL_NAMES.review,
  );

  assert.deepEqual(calls[0].args, { versionId: VERSION_ID });
  assertReviewFindings(calls[1].args);
  assert.match(text, /P5/);
});

// --- data plan -------------------------------------------------------------

test("data plan role: case_graph_read -> case_data_plan_submit -> final text", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = promptBody(PROMPT_CONSTS.data_plan, promptsSource);
  const { calls, text } = await driveRun(
    sharedServer.base,
    system,
    dataPlanInstruction(),
    [graphReadResult(), { ok: true }],
    TOOL_NAMES.data_plan,
  );

  assert.deepEqual(calls[0].args, { versionId: VERSION_ID });
  assertDataPlanItems(calls[1].args);
  assert.match(text, /P5/);
});

// --- explore ---------------------------------------------------------------

test("explore role: two explore_http probes -> app_model_propose -> final text", async () => {
  assert.ok(sharedServer, "shared fake LLM server was not started");
  const system = promptBody(PROMPT_CONSTS.explore, promptsSource);
  const { calls, text } = await driveRun(
    sharedServer.base,
    system,
    exploreInstruction(),
    [{ status: 200, title: "P5 Home" }, { status: 200, title: "orders" }, { ok: true }],
    TOOL_NAMES.explore,
  );

  assertExploreCall(calls[0].args, "/p5/home");
  assertExploreCall(calls[1].args, "/api/orders");
  assert.notEqual(calls[0].args.url, calls[1].args.url);
  assertAppModelProposal(calls[2].args);
  assert.match(text, /P5/);
});

// --- modes -----------------------------------------------------------------

test("invalid mode returns a proposal the server must reject", async () => {
  await withFakeLlm("invalid", async (server) => {
    const system = promptBody(PROMPT_CONSTS.design, promptsSource);
    const messages = [
      { role: "system", content: system },
      { role: "user", content: designInstruction() },
    ];

    const readChoice = await chat(server.base, messages);
    assertToolCall(readChoice, "case_graph_read");
    appendToolResult(messages, readChoice, graphReadResult());

    const proposeChoice = await chat(server.base, messages);
    const args = assertToolCall(proposeChoice, "case_graph_propose");
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

test("degraded mode returns text only for every role", async () => {
  await withFakeLlm("degraded", async (server) => {
    const scenarios = [
      ["design", designInstruction()],
      ["review", reviewInstruction()],
      ["data_plan", dataPlanInstruction()],
      ["explore", exploreInstruction()],
    ];
    for (const [kind, instruction] of scenarios) {
      const system = promptBody(PROMPT_CONSTS[kind], promptsSource);
      const choice = await chat(server.base, [
        { role: "system", content: system },
        { role: "user", content: instruction },
      ]);
      const text = assertTextTurn(choice, kind + " degraded");
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
      { role: "system", content: promptBody(PROMPT_CONSTS.design, promptsSource) },
      { role: "user", content: designInstruction() },
    ];
    const readChoice = await chat(sharedServer.base, designMessages);
    assertToolCall(readChoice, "case_graph_read");
    appendToolResult(designMessages, readChoice, graphReadResult());
    const proposeChoice = await chat(sharedServer.base, designMessages);
    const invalidArgs = assertToolCall(proposeChoice, "case_graph_propose");
    assert.ok(
      invalidArgs.cases.some((item) => !Array.isArray(item.requirementIds) || item.requirementIds.length === 0),
      "the runtime invalid mode must take effect without a restart",
    );

    assert.equal(await setMode("degraded"), "degraded");
    assert.equal(await getMode(), "degraded");
    const degraded = await chat(sharedServer.base, [
      { role: "system", content: promptBody(PROMPT_CONSTS.design, promptsSource) },
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
  assert.equal(p1Call.function.name, "file_read");
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

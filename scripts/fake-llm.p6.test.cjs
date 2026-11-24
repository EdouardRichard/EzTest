#!/usr/bin/env node
"use strict";

// Deterministic tests for the case-executor execution scenario of
// scripts/fake-llm.cjs (v0.2.4-C T13 tool surface, v0.2.4-F T15 NL/sut_id/fixture
// contract) and the P6 fixture pages of scripts/fixture-target.cjs.
//
// Both servers are spawned on process-unique ports. The fake LLM is driven
// through the same POST /v1/chat/completions wire shape the real provider uses
// (streaming and non-streaming), so these tests exercise the actual system
// prompt detection, per-run step counting and mode switch.
//
// Contract (current agent tool surface, NOT the retired 0.2.0 underscore names):
//   * the scenario is selected by the seeded case_executor prompt marker
//     "[profile:case_executor]" (backend/internal/agent/application/execution_profiles.go);
//   * tool names are the dotted registry names (sut.read / exec.run / http.request /
//     execution.step_record / ...); the 13 browser.* atomic actions are retired;
//   * the case snapshot comes from context.task_pack -> result.execution
//     (steps[].seq / assertions[].step_seq+assertion_seq), not the retired
//     execution_case_context remaining_steps/assertion_id shape;
//   * v0.2.4-F NL contract: the design supplies sut_description / covered_acs /
//     per-step target_description (no design-target SUT/page/API id columns); the
//     scenario resolves the SUT via sut.read and carries sut_id as FACTUAL
//     attribution on http.request / step_record / assertion_record;
//   * recording carries seq/status/observed and step_seq/assertion_seq/verdict/
//     actual; evidence is settled automatically by the execution tools, so no
//     evidence_ids travel on the recording arguments;
//   * the "fixture" mode exercises the P18 chain (fixture.download -> state-save ->
//     fixture.upload);
//   * case_finish submits a candidate verdict/summary/failure_class.
//
// Run:
//   node scripts/fake-llm.p6.test.cjs

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { after, before, test } = require("node:test");
const { parseSse } = require("./fake-llm-sse-test-helper.cjs");

const repoRoot = path.resolve(__dirname, "..");
const fakeLlmScript = path.join(__dirname, "fake-llm.cjs");
const fixtureScript = path.join(__dirname, "fixture-target.cjs");
const serverLogRoot = fs.mkdtempSync(path.join(os.tmpdir(), "eztest-p6-servers-"));

const MODEL = "deepseek-flash";
const P6_MARKER = "[profile:case_executor]";
const FIXTURE_BASE = "http://127.0.0.1:13335";

// The context.task_pack result shape of an execute.case run
// (runner.go taskPackResult/executionTaskResult, v0.2.4-F T12/T15 NL contract).
// The scenario reads result.execution.steps / result.execution.assertions and
// resolves the SUT from result.execution.suts[].sut_id. The design supplies
// natural-language targets (sut_description / covered_acs / per-step
// target_description); the retired design-target SUT/page/API id columns are
// gone (steps[].target_sut_id no longer exists). assertions[].target_sut_id and
// prior_*[].target_sut_id are FACTUAL attribution and stay.
const TASK_PACK = {
  version_id: "9103",
  conversation_id: "0",
  agent_run_id: "9105",
  type: "execute.case",
  goal: "严格按任务上下文中的用例步骤顺序执行。",
  constraints: ["不得虚构 passed"],
  acceptance_criteria: ["每个执行过的 step 都有 execution.step_record"],
  scope: { version_id: "9103" },
  execution: {
    case_run_id: "9101",
    wave_id: "9102",
    intent_id: "9106",
    case_version_id: "9104",
    environment_id: "3",
    case_title: "P6 登录后创建订单",
    description: "用户使用账号密码登录后创建订单",
    sut_description: "订单管理系统（orders），提供订单创建与查询的 Web UI。",
    preconditions: "已创建测试账号",
    covered_acs: ["AC-1", "AC-2"],
    priority: "P2",
    suts: [
      {
        sut_id: "7",
        sut_name: "orders",
        intro: "订单管理系统",
        environment_id: "3",
        environment_name: "test",
        base_url: FIXTURE_BASE,
        page_urls: [FIXTURE_BASE + "/p6/orders/new"],
        api_endpoints: [],
        account_roles: ["admin"],
      },
    ],
    steps: [
      { seq: 1, action: "打开订单创建页并填写订单号", expected: "订单表单可见", account_role: "admin", target_description: "订单创建页的订单号输入框" },
      { seq: 2, action: "提交新建订单", expected: "订单创建成功", account_role: "admin", target_description: "订单创建页的提交按钮" },
    ],
    assertions: [
      { step_seq: 2, assertion_seq: 1, kind: "ui", expected: "订单创建成功", target_sut_id: "7" },
    ],
    prior_steps: [],
    prior_assertions: [],
  },
};

// sut.read detail result (sut_tools.go sutRead.Execute): identity/registration
// labels + effective asset summary. The scenario confirms the catalog match.
const SUT_READ_RESULT = {
  version_id: "9103",
  sut_id: "7",
  name: "orders",
  intro: "订单管理系统",
  base_url: FIXTURE_BASE,
  source: "admin",
  status: "approved",
  environment: "test",
  environment_id: "3",
  pages: [{ id: "71", name: "订单创建页", path: "/p6/orders/new" }],
  apis: [],
  databases: [],
  asset_counts: { pages: 1, apis: 0, databases: 0 },
};
// fixture.download on a first run: no fixture yet (the scenario then logs in and
// saves one). fixture.upload stores the saved state (P18 append-only fact).
const FIXTURE_DOWNLOAD_NOT_FOUND = { error: "not_found", message: "no session fixture for this reuse key" };
const FIXTURE_UPLOAD_RESULT = {
  version_id: "9103",
  case_run_id: "9101",
  fixture_id: "8501",
  sut_id: "7",
  environment_id: "3",
  role: "admin",
  storage_state_ref: "a".repeat(64),
  bytes: 128,
};
const BROWSER_CLI_RESULT = { ok: true, exit_code: 0, stdout: "", stderr: "" };

const EXEC_RUN_RESULT = { ok: true, exit_code: 0, stdout: "p6-step-prep\n", stderr: "", evidence_ids: ["8101"] };
const HTTP_RESULT = { status: 200, truncated: false, evidence_ids: ["8102"] };
const OK_RESULT = { ok: true };

let serverCounter = 0;
let sharedLlm = null;
let sharedFixture = null;

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// --- server bootstrap ------------------------------------------------------

async function startServer(script, envOverrides, healthPath, label) {
  let lastError = null;
  for (let attempt = 0; attempt < 10; attempt += 1) {
    serverCounter += 1;
    const port = 16000 + ((process.pid + serverCounter * 43 + attempt * 17) % 8000);
    const logPath = path.join(serverLogRoot, label + "-" + serverCounter + ".log");
    const logFd = fs.openSync(logPath, "w");
    const child = spawn(process.execPath, [script], {
      cwd: repoRoot,
      env: { ...process.env, FIXTURE_PORT: String(port), EZTEST_FAKE_LLM_PORT: String(port), ...envOverrides },
      stdio: ["ignore", logFd, logFd],
      windowsHide: true,
    });
    fs.closeSync(logFd);

    const base = "http://127.0.0.1:" + port;
    const deadline = Date.now() + 6000;
    while (Date.now() < deadline) {
      if (child.exitCode !== null) break;
      try {
        const response = await fetch(base + healthPath);
        if (response.status === 200) {
          await response.arrayBuffer();
          return { base, child, port, stop: () => child.exitCode === null && child.kill() };
        }
      } catch {
        // Not ready yet.
      }
      await sleep(50);
    }
    if (child.exitCode === null) child.kill();
    lastError = fs.existsSync(logPath) ? fs.readFileSync(logPath, "utf8") : "";
    if (!/EADDRINUSE/.test(lastError)) {
      throw new Error(label + " did not start on " + base + ":\n" + lastError);
    }
  }
  throw new Error("could not find a free port for " + label);
}

async function withFakeLlm(mode, fn) {
  const server = await startServer(fakeLlmScript, { EZTEST_FAKE_LLM_P6_MODE: mode }, "/v1/models", "fake-llm");
  try {
    return await fn(server);
  } finally {
    server.stop();
  }
}

// --- wire helpers ----------------------------------------------------------

function agentTools() {
  return [
    "context.task_pack",
    "sut.list",
    "sut.read",
    "exec.run",
    "browser.cli",
    "http.request",
    "db.query",
    "fixture.upload",
    "fixture.download",
    "execution.step_record",
    "execution.assertion_record",
    "execution.evidence",
    "execution.wait",
    "execution.case_finish",
    "human.request",
  ].map((name) => ({
    type: "function",
    function: {
      name,
      description: name,
      parameters: { type: "object", properties: {}, additionalProperties: false },
    },
  }));
}

function p6SystemPrompt() {
  return "你是 EzTest 的用例执行器（Case Executor）。\n" + P6_MARKER;
}

function p6Instruction() {
  return [
    "## 执行任务",
    "caseRunId=9101",
    "baseUrl=" + FIXTURE_BASE,
    "请按步骤执行并提交结构化结果。",
  ].join("\n");
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

async function chat(base, messages) {
  const res = await postChat(base, { model: MODEL, stream: false, messages, tools: agentTools() });
  assert.equal(res.status, 200, "chat request failed: " + res.text);
  assert.ok(res.json && Array.isArray(res.json.choices), "missing choices: " + res.text);
  return res.json.choices[0];
}

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

// The current registry names are dotted (namespace.tool); the retired 0.2.0
// gateway underscore conversion no longer exists. browser.cli is the CURRENT
// browser capability-domain passthrough (v0.2.4-C T15), not a retired atomic
// action — the 13 browser.* atomic actions stay forbidden.
function assertDottedToolName(name) {
  assert.equal(typeof name, "string");
  assert.match(name, /^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$/, "tool name " + JSON.stringify(name) + " must be a dotted registry name");
  assert.ok(!name.startsWith("browser.") || name === "browser.cli", "retired browser.* action emitted: " + name);
}

function assertToolCall(choice, expectedName) {
  const calls = choice.message.tool_calls || [];
  assert.equal(calls.length, 1, "expected exactly one tool call, got " + calls.length);
  const call = calls[0];
  assert.equal(call.function.name, expectedName);
  assertDottedToolName(call.function.name);
  assert.equal(choice.finish_reason, "tool_calls");
  return JSON.parse(call.function.arguments);
}

function appendToolResult(messages, choice, content) {
  const call = choice.message.tool_calls[0];
  messages.push(choice.message);
  messages.push({
    role: "tool",
    tool_call_id: call.id,
    content: typeof content === "string" ? content : JSON.stringify(content),
  });
}

function assertTextTurn(choice, label) {
  assert.equal(choice.finish_reason, "stop", label + ": expected stop");
  assert.equal(choice.message.tool_calls, undefined, label + ": text turn must not call tools");
  assert.equal(typeof choice.message.content, "string", label + ": missing text content");
  assert.ok(choice.message.content.length > 0, label + ": empty text content");
  return choice.message.content;
}

/** Appends the canonical tool result for a planned execution call. */
function resultFor(name) {
  switch (name) {
    case "context.task_pack":
      return TASK_PACK;
    case "sut.read":
      return SUT_READ_RESULT;
    case "exec.run":
      return EXEC_RUN_RESULT;
    case "browser.cli":
      return BROWSER_CLI_RESULT;
    case "http.request":
      return HTTP_RESULT;
    case "fixture.download":
      return FIXTURE_DOWNLOAD_NOT_FOUND;
    case "fixture.upload":
      return FIXTURE_UPLOAD_RESULT;
    case "human.request":
      return { status: "resolved", human_request_id: "8401" };
    case "execution.case_finish":
      return { case_run_id: "9101", status: "passed" };
    default:
      return OK_RESULT;
  }
}

/**
 * Drives one execution run: requests `count` tool turns, appends a matching
 * result and finally asks for the terminal turn. `options.toolResults`
 * overrides the canonical result of individual tool calls.
 */
async function driveToolTurns(base, count, options = {}) {
  const messages = [
    { role: "system", content: p6SystemPrompt() },
    { role: "user", content: p6Instruction() },
  ];
  const overrides = options.toolResults || {};
  const calls = [];
  for (let index = 0; index < count; index += 1) {
    const choice = await chat(base, messages);
    const name = choice.message.tool_calls[0].function.name;
    assertDottedToolName(name);
    calls.push({ name, args: JSON.parse(choice.message.tool_calls[0].function.arguments) });
    const result = Object.prototype.hasOwnProperty.call(overrides, name)
      ? overrides[name]
      : resultFor(name);
    appendToolResult(messages, choice, result);
  }
  const finalChoice = await chat(base, messages);
  return { calls, messages, finalChoice };
}

// --- detection -------------------------------------------------------------

test("the case_executor prompt marker selects the execution scenario", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const choice = await chat(sharedLlm.base, [
    { role: "system", content: p6SystemPrompt() },
    { role: "user", content: p6Instruction() },
  ]);
  assertToolCall(choice, "context.task_pack");
});

test("P1 and P5 keep their behavior while the execution scenario is registered", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");

  const p1 = await postChat(sharedLlm.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "读取 README" }],
    tools: agentTools(),
  });
  assert.equal(p1.status, 200, p1.text);
  assert.equal(p1.json.choices[0].message.tool_calls[0].function.name, "file.read");

  const p5 = await postChat(sharedLlm.base, {
    model: MODEL,
    stream: false,
    messages: [
      { role: "system", content: "EzTest 的测试设计 Agent" },
      { role: "user", content: "versionId=123\n必须调用 case_graph_propose。" },
    ],
    tools: agentTools(),
  });
  assert.equal(p5.status, 200, p5.text);
  assert.equal(p5.json.choices[0].message.tool_calls[0].function.name, "design_graph_read");
});

// --- normal ----------------------------------------------------------------

test("normal mode drives task_pack -> sut.read -> exec/http -> records -> passed finish", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const { calls, finalChoice } = await driveToolTurns(sharedLlm.base, 8);

  assert.deepEqual(
    calls.map((call) => call.name),
    [
      "context.task_pack",
      "sut.read",
      "exec.run",
      "http.request",
      "execution.step_record",
      "execution.step_record",
      "execution.assertion_record",
      "execution.case_finish",
    ],
  );

  assert.deepEqual(calls[0].args, {});
  // Resolution: sut.read carries the catalog sut_id (the single value source).
  assert.equal(calls[1].args.sut_id, "7");
  // exec.run carries an argv command (general-execution domain), never a
  // retired browser action.
  assert.ok(Array.isArray(calls[2].args.argv) && calls[2].args.argv.length > 0, "exec.run needs argv");
  // http.request uses a relative SUT path; the server prepends the base URL.
  // The resolved sut_id travels as factual attribution.
  assert.equal(calls[3].args.path, "/p6/orders/new");
  assert.equal(calls[3].args.method, "GET");
  assert.equal(calls[3].args.sut_id, "7");

  assert.equal(calls[4].args.seq, 1);
  assert.equal(calls[4].args.status, "passed");
  assert.ok(String(calls[4].args.observed).length > 0, "a step record needs observed");
  // The recording consumes the natural-language design target (NL contract).
  assert.match(String(calls[4].args.observed), /订单创建页的订单号输入框/);
  assert.equal(calls[4].args.sut_id, "7", "step record carries factual sut_id attribution");
  assert.equal(calls[5].args.seq, 2);
  assert.equal(calls[5].args.sut_id, "7");
  // Evidence is settled automatically by the execution tools: the recording
  // arguments carry no evidence_ids.
  assert.equal(Object.prototype.hasOwnProperty.call(calls[4].args, "evidence_ids"), false);
  // The design no longer supplies target SUT/page/API ids on steps: the scenario
  // never emits a design-target id column (factual sut_id attribution is distinct).
  assert.equal(Object.prototype.hasOwnProperty.call(calls[4].args, "target_sut_id"), false);
  assert.equal(Object.prototype.hasOwnProperty.call(calls[4].args, "target_page_id"), false);

  assert.equal(calls[6].args.step_seq, 2);
  assert.equal(calls[6].args.assertion_seq, 1);
  assert.equal(calls[6].args.verdict, "passed");
  assert.ok(String(calls[6].args.actual).length > 0, "an assertion record needs actual");
  assert.equal(calls[6].args.sut_id, "7", "assertion record carries factual sut_id attribution");

  assert.equal(calls[7].args.verdict, "passed");
  assert.equal(calls[7].args.failure_class, undefined);

  const text = assertTextTurn(finalChoice, "normal final");
  assert.match(text, /P6/);
});

test("normal mode streams the same tool turns", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const messages = [
    { role: "system", content: p6SystemPrompt() },
    { role: "user", content: p6Instruction() },
  ];
  const first = await streamChat(sharedLlm.base, messages);
  assert.equal(first.toolCalls[0].name, "context.task_pack");
  assert.equal(first.finishReason, "tool_calls");
  assert.deepEqual(JSON.parse(first.toolCalls[0].arguments), {});
});

test("identical requests stay deterministic", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const body = {
    model: MODEL,
    stream: false,
    messages: [
      { role: "system", content: p6SystemPrompt() },
      { role: "user", content: p6Instruction() },
    ],
    tools: agentTools(),
  };
  const first = await postChat(sharedLlm.base, body);
  const second = await postChat(sharedLlm.base, body);
  assert.equal(first.status, 200);
  assert.equal(second.status, 200);
  assert.equal(first.json.id, second.json.id, "same body must produce the same completion id");
  assert.equal(first.text, second.text, "same body must produce a byte-identical response");
});

// --- fail ------------------------------------------------------------------

test("fail mode records a failed assertion and finishes failed(application)", async () => {
  await withFakeLlm("fail", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 8);
    assert.deepEqual(
      calls.map((call) => call.name),
      [
        "context.task_pack",
        "sut.read",
        "exec.run",
        "http.request",
        "execution.step_record",
        "execution.step_record",
        "execution.assertion_record",
        "execution.case_finish",
      ],
    );
    assert.equal(calls[3].args.path, "/p6/assert-fail");
    assert.equal(calls[6].args.verdict, "failed");
    assert.ok(String(calls[6].args.actual).length > 0, "a failed assertion needs actual");
    assert.equal(calls[6].args.sut_id, "7");
    assert.equal(calls[7].args.verdict, "failed");
    assert.equal(calls[7].args.failure_class, "application");
    assertTextTurn(finalChoice, "fail final");
  });
});

// --- blocked ---------------------------------------------------------------

test("blocked mode records the blocked step and finishes blocked(environment)", async () => {
  await withFakeLlm("blocked", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 6);
    assert.deepEqual(
      calls.map((call) => call.name),
      [
        "context.task_pack",
        "sut.read",
        "exec.run",
        "http.request",
        "execution.step_record",
        "execution.case_finish",
      ],
    );
    assert.equal(calls[3].args.path, "/p6/requires-auth");
    assert.equal(calls[4].args.seq, 1);
    assert.equal(calls[4].args.status, "blocked");
    assert.ok(String(calls[4].args.observed).length > 0, "a blocked step needs observed");
    assert.equal(calls[4].args.sut_id, "7");
    // An environment-blocked run cannot execute assertions: none are recorded.
    assert.equal(calls.filter((call) => call.name === "execution.assertion_record").length, 0);
    assert.equal(calls[5].args.verdict, "blocked");
    assert.equal(calls[5].args.failure_class, "environment");
    assert.ok(String(calls[5].args.summary).length > 0, "blocked needs a summary");
    assertTextTurn(finalChoice, "blocked final");
  });
});

// --- takeover --------------------------------------------------------------

test("takeover mode requests a login takeover, then records and finishes", async () => {
  await withFakeLlm("takeover", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 9);
    assert.deepEqual(
      calls.map((call) => call.name),
      [
        "context.task_pack",
        "sut.read",
        "exec.run",
        "http.request",
        "human.request",
        "execution.step_record",
        "execution.step_record",
        "execution.assertion_record",
        "execution.case_finish",
      ],
    );
    assert.equal(calls[3].args.path, "/p6/login?mfa=1");
    assert.equal(calls[4].args.kind, "login_takeover");
    assert.match(String(calls[4].args.reason), /mfa/i);
    assert.equal(calls[4].args.account_role, "admin");
    assert.equal(calls[8].args.verdict, "passed");
    assertTextTurn(finalChoice, "takeover final");
  });
});

// --- fixture (P18 cross-case login-state chain) ----------------------------

test("fixture mode reuses-or-saves a session fixture via the P18 tool chain", async () => {
  await withFakeLlm("fixture", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 11);
    assert.deepEqual(
      calls.map((call) => call.name),
      [
        "context.task_pack",
        "sut.read",
        "fixture.download",
        "exec.run",
        "http.request",
        "browser.cli",
        "fixture.upload",
        "execution.step_record",
        "execution.step_record",
        "execution.assertion_record",
        "execution.case_finish",
      ],
    );
    // First run tries to reuse a fixture (not_found), then logs in and saves one.
    assert.equal(calls[2].args.sut_id, "7");
    assert.equal(calls[2].args.account_role, "admin");
    // CLI state-save writes the workspace state file; upload stores it server-side.
    assert.equal(calls[5].args.subcommand, "state-save");
    assert.equal(calls[5].args.sut_id, "7");
    assert.equal(calls[6].args.sut_id, "7");
    assert.equal(calls[6].args.path, "state.json");
    assert.equal(calls[10].args.verdict, "passed");
    assertTextTurn(finalChoice, "fixture final");
  });
});

// --- budget ----------------------------------------------------------------

test("budget mode never finishes and keeps calling exec.run until the budget is gone", async () => {
  await withFakeLlm("budget", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 12);
    assert.equal(calls[0].name, "context.task_pack");
    assert.ok(
      calls.slice(1).every((call) => call.name !== "execution.case_finish"),
      "budget mode must never submit case_finish",
    );
    assert.ok(
      calls.slice(1).every((call) => call.name === "exec.run"),
      "budget mode must keep calling the general-execution tool",
    );
    assert.equal(finalChoice.finish_reason, "tool_calls", "budget mode never answers with final text");
  });
});

// --- context guards --------------------------------------------------------

test("a task pack without an execution snapshot ends with text, no records", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const messages = [
    { role: "system", content: p6SystemPrompt() },
    { role: "user", content: p6Instruction() },
  ];
  const first = await chat(sharedLlm.base, messages);
  assertToolCall(first, "context.task_pack");
  appendToolResult(messages, first, { version_id: "9103", type: "execute.case" });
  const second = await chat(sharedLlm.base, messages);
  assertTextTurn(second, "empty context");
});

test("a finished run answers with final text instead of more tools", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const { calls, messages } = await driveToolTurns(sharedLlm.base, 8);
  assert.equal(calls[calls.length - 1].name, "execution.case_finish");
  const again = await chat(sharedLlm.base, messages);
  assertTextTurn(again, "finished run");
});

// --- mode control ----------------------------------------------------------

test("the runtime P6 mode control switches, rejects unknown modes and resets", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");

  const getMode = async () => {
    const response = await fetch(sharedLlm.base + "/__p6-mode");
    assert.equal(response.status, 200, "GET /__p6-mode must answer 200");
    return (await response.json()).mode;
  };
  const setMode = async (mode) => {
    const response = await fetch(sharedLlm.base + "/__p6-mode", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode }),
    });
    assert.equal(response.status, 200, "POST /__p6-mode must answer 200");
    return (await response.json()).mode;
  };

  try {
    assert.equal(await getMode(), "normal", "the default P6 mode must be normal");
    for (const mode of ["fail", "blocked", "takeover", "fixture", "budget", "normal"]) {
      assert.equal(await setMode(mode), mode);
      assert.equal(await getMode(), mode);
    }
    const rejected = await fetch(sharedLlm.base + "/__p6-mode", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode: "not-a-mode" }),
    });
    assert.equal(rejected.status, 400, "an unknown mode must be rejected");
    assert.equal(await getMode(), "normal", "a rejected mode must not change the active mode");
  } finally {
    await setMode("normal");
  }
});

// --- fixture pages ---------------------------------------------------------

async function fixtureLogin(server, options = {}) {
  const response = await fetch(server.base + "/p6/login", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      username: options.username ?? "fixture",
      password: options.password ?? "fixture-pass",
    }).toString(),
    redirect: "manual",
  });
  return response;
}

function cookieFrom(response, name) {
  const values = typeof response.headers.getSetCookie === "function" ? response.headers.getSetCookie() : [];
  for (const value of values) {
    const pair = value.split(";")[0];
    const separator = pair.indexOf("=");
    if (separator > 0 && pair.slice(0, separator).trim() === name) {
      return pair.slice(separator + 1).trim();
    }
  }
  return "";
}

function getWithCookie(server, pathname, cookie) {
  return fetch(server.base + pathname, {
    headers: cookie === "" ? {} : { Cookie: "p6_session=" + cookie },
    redirect: "manual",
  });
}

before(async () => {
  sharedLlm = await startServer(fakeLlmScript, { EZTEST_FAKE_LLM_P6_MODE: "normal" }, "/v1/models", "shared-llm");
  sharedFixture = await startServer(fixtureScript, {}, "/health", "fixture");
});

after(() => {
  if (sharedLlm) sharedLlm.stop();
  if (sharedFixture) sharedFixture.stop();
  fs.rmSync(serverLogRoot, { recursive: true, force: true });
});

test("GET /p6/login renders the form, and #mfa marks the MFA variant", async () => {
  assert.ok(sharedFixture, "fixture target was not started");
  const plain = await getWithCookie(sharedFixture, "/p6/login", "");
  assert.equal(plain.status, 200);
  const html = await plain.text();
  assert.match(html, /id="username"/);
  assert.match(html, /id="password"/);
  assert.match(html, /id="order-login"|action="\/p6\/login"|method="post"/i);
  assert.ok(!html.includes('id="mfa"'), "the plain login page must not show the MFA block");

  const mfa = await getWithCookie(sharedFixture, "/p6/login?mfa=1", "");
  assert.equal(mfa.status, 200);
  assert.match(await mfa.text(), /id="mfa"/);
});

test("POST /p6/login sets the session cookie or reports .login-error", async () => {
  assert.ok(sharedFixture, "fixture target was not started");

  const bad = await fixtureLogin(sharedFixture, { password: "wrong" });
  assert.equal(bad.status, 401);
  assert.match(await bad.text(), /class="login-error"/);

  const good = await fixtureLogin(sharedFixture);
  assert.equal(good.status, 302);
  assert.equal(good.headers.get("location"), "/p6/home");
  assert.equal(cookieFrom(good, "p6_session"), "ok");
});

test("the P6 app pages require the session cookie", async () => {
  assert.ok(sharedFixture, "fixture target was not started");

  for (const pathname of ["/p6/home", "/p6/orders", "/p6/orders/new", "/p6/requires-auth"]) {
    const response = await getWithCookie(sharedFixture, pathname, "");
    assert.equal(response.status, 302, pathname + " must redirect when unauthenticated");
    assert.equal(response.headers.get("location"), "/p6/login");
  }

  const login = await fixtureLogin(sharedFixture);
  const cookie = cookieFrom(login, "p6_session");
  assert.notEqual(cookie, "", "login must set the p6_session cookie");

  const home = await getWithCookie(sharedFixture, "/p6/home", cookie);
  assert.equal(home.status, 200);
  const homeHtml = await home.text();
  assert.match(homeHtml, /\/p6\/orders/);
  assert.match(homeHtml, /\/p6\/orders\/new/);

  const requiring = await getWithCookie(sharedFixture, "/p6/requires-auth", cookie);
  assert.equal(requiring.status, 200);
});

test("the order list, detail and JSON API expose the three fixture orders", async () => {
  assert.ok(sharedFixture, "fixture target was not started");
  const login = await fixtureLogin(sharedFixture);
  const cookie = cookieFrom(login, "p6_session");

  const list = await getWithCookie(sharedFixture, "/p6/orders", cookie);
  assert.equal(list.status, 200);
  const listHtml = await list.text();
  assert.equal((listHtml.match(/\/p6\/orders\/\d+/g) || []).length >= 3, true);

  const detail = await getWithCookie(sharedFixture, "/p6/orders/1", cookie);
  assert.equal(detail.status, 200);
  assert.match(await detail.text(), /P6-ORDER-1/);

  const missingPage = await getWithCookie(sharedFixture, "/p6/orders/999", cookie);
  assert.equal(missingPage.status, 404);

  const api = await getWithCookie(sharedFixture, "/api/p6/orders", cookie);
  assert.equal(api.status, 200);
  const apiJson = await api.json();
  assert.equal(Array.isArray(apiJson.orders), true);
  assert.equal(apiJson.orders.length, 3);

  const one = await getWithCookie(sharedFixture, "/api/p6/orders/1", cookie);
  assert.equal(one.status, 200);
  assert.equal((await one.json()).number, "P6-ORDER-1");

  const missingApi = await getWithCookie(sharedFixture, "/api/p6/orders/999", cookie);
  assert.equal(missingApi.status, 404);
});

test("GET/POST /p6/orders/new renders a form and creates an order", async () => {
  assert.ok(sharedFixture, "fixture target was not started");
  const login = await fixtureLogin(sharedFixture);
  const cookie = cookieFrom(login, "p6_session");

  const form = await getWithCookie(sharedFixture, "/p6/orders/new", cookie);
  assert.equal(form.status, 200);
  const formHtml = await form.text();
  assert.match(formHtml, /id="order-number"/);
  assert.match(formHtml, /name="number"/);

  const created = await fetch(sharedFixture.base + "/p6/orders/new", {
    method: "POST",
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      Cookie: "p6_session=" + cookie,
    },
    body: new URLSearchParams({ number: "P6-E2E-NEW", amount: "321" }).toString(),
    redirect: "manual",
  });
  assert.equal(created.status, 302);
  const location = created.headers.get("location");
  assert.match(location, /^\/p6\/orders\/\d+$/);

  const detail = await getWithCookie(sharedFixture, location, cookie);
  assert.equal(detail.status, 200);
  assert.match(await detail.text(), /P6-E2E-NEW/);
});

test("the slow, assert-fail and requires-auth fixtures answer as specified", async () => {
  assert.ok(sharedFixture, "fixture target was not started");

  const started = Date.now();
  const slow = await getWithCookie(sharedFixture, "/p6/slow?ms=200", "");
  assert.equal(slow.status, 200);
  assert.ok(Date.now() - started >= 150, "the slow fixture must wait for ?ms=");

  const login = await fixtureLogin(sharedFixture);
  const cookie = cookieFrom(login, "p6_session");

  const failing = await getWithCookie(sharedFixture, "/p6/assert-fail", cookie);
  assert.equal(failing.status, 200);
  assert.match(await failing.text(), /P6-asset-fail|P6 assert-fail|断言失败|unexpected/i);
});
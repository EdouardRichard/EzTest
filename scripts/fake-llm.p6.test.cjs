#!/usr/bin/env node
"use strict";

// Deterministic tests for the P6 execution scenarios of scripts/fake-llm.cjs and
// the P6 fixture pages of scripts/fixture-target.cjs (design spec §10.2).
//
// Both servers are spawned on process-unique ports. The fake LLM is driven
// through the same POST /v1/chat/completions wire shape the real provider uses
// (streaming and non-streaming), so these tests exercise the actual system
// prompt detection, per-run step counting, mode switch and the underscore tool
// call names the P1 gateway emits (`ReplaceAll(name, ".", "_")`).
//
// Run:
//   node scripts/fake-llm.p6.test.cjs

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { after, before, test } = require("node:test");

const repoRoot = path.resolve(__dirname, "..");
const fakeLlmScript = path.join(__dirname, "fake-llm.cjs");
const fixtureScript = path.join(__dirname, "fixture-target.cjs");
const serverLogRoot = fs.mkdtempSync(path.join(os.tmpdir(), "eztest-p6-servers-"));

const MODEL = "deepseek-flash";
const P6_MARKER = "EzTest 的执行 Agent";
const FIXTURE_BASE = "http://127.0.0.1:13335";

const CASE_CONTEXT = {
  case_run_id: "9101",
  batch_id: "9102",
  version_id: "9103",
  case_node_id: "9104",
  title: "P6 登录后创建订单",
  status: "running",
  attempt: 1,
  steps: [
    { seq: 1, action: "打开订单创建页并填写订单号", expected: "订单表单可见" },
    { seq: 2, action: "提交新建订单", expected: "订单创建成功" },
  ],
  remaining_steps: [
    { seq: 1, action: "打开订单创建页并填写订单号", expected: "订单表单可见" },
    { seq: 2, action: "提交新建订单", expected: "订单创建成功" },
  ],
  assertions: [
    {
      assertion_id: "7101",
      step_seq: 2,
      kind: "ui",
      target: "#result",
      expression: "text contains 订单创建成功",
      expected: "订单创建成功",
    },
  ],
  completed_seqs: [],
  cleanup_required: false,
  base_url: FIXTURE_BASE,
  allowed_hosts: ["127.0.0.1:13335"],
};

const EVIDENCE = { evidence_id: "8101", sha256: "a".repeat(64), size: 1234 };
const GOTO_RESULT = { url: FIXTURE_BASE + "/p6/orders/new", status: 200 };
const SNAPSHOT_RESULT = { url: FIXTURE_BASE + "/p6/orders/new", title: "New order", aria: "- heading" };
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
  throw new Error("could not find a free port for " + label + ":\n" + lastError);
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
    "execution_case_context",
    "browser_goto",
    "browser_snapshot",
    "browser_fill",
    "browser_click",
    "browser_screenshot",
    "execution_step_record",
    "execution_assertion_record",
    "execution_case_finish",
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
  return P6_MARKER + "\n\n你是执行 Agent，只执行给定用例快照。";
}

function p6Instruction() {
  return [
    "## 执行任务",
    "batchId=9102",
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

function parseSse(text) {
  const result = { content: "", toolCalls: [], finishReason: null };
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
    if (Array.isArray(delta.tool_calls)) {
      for (const fragment of delta.tool_calls) {
        const index = typeof fragment.index === "number" ? fragment.index : 0;
        const slot =
          result.toolCalls[index] || (result.toolCalls[index] = { id: "", name: "", arguments: "" });
        if (fragment.id) slot.id = fragment.id;
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

function assertToolCall(choice, expectedName) {
  const calls = choice.message.tool_calls || [];
  assert.equal(calls.length, 1, "expected exactly one tool call, got " + calls.length);
  const call = calls[0];
  assert.equal(call.function.name, expectedName);
  assertUnderscoreToolName(call.function.name);
  assert.equal(choice.finish_reason, "tool_calls");
  return JSON.parse(call.function.arguments);
}

function assertUnderscoreToolName(name) {
  assert.equal(typeof name, "string");
  assert.ok(!name.includes("."), "tool name " + JSON.stringify(name) + " must not contain a dot");
  assert.match(name, /^[a-z][a-z0-9_]*$/, "tool name " + JSON.stringify(name) + " must be snake_case");
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

/** Appends the canonical tool result for a planned P6 call. */
function resultFor(name) {
  switch (name) {
    case "execution_case_context":
      return CASE_CONTEXT;
    case "browser_goto":
      return GOTO_RESULT;
    case "browser_snapshot":
      return SNAPSHOT_RESULT;
    case "browser_screenshot":
      return EVIDENCE;
    case "execution_step_record":
      return { step_result_id: "8201", seq: 1, status: "passed", evidence_count: 1 };
    case "execution_assertion_record":
      return { assertion_result_id: "8301", assertion_id: "7101", verdict: "passed" };
    case "execution_case_finish":
      return { case_run_id: "9101", status: "passed" };
    default:
      return OK_RESULT;
  }
}

/**
 * Drives one P6 run: requests `count` tool turns, appends a matching result and
 * finally asks for the terminal turn. Returns every call plus the final text.
 */
async function driveToolTurns(base, count) {
  const messages = [
    { role: "system", content: p6SystemPrompt() },
    { role: "user", content: p6Instruction() },
  ];
  const calls = [];
  for (let index = 0; index < count; index += 1) {
    const choice = await chat(base, messages);
    const name = choice.message.tool_calls[0].function.name;
    assertUnderscoreToolName(name);
    calls.push({ name, args: JSON.parse(choice.message.tool_calls[0].function.arguments) });
    appendToolResult(messages, choice, resultFor(name));
  }
  const finalChoice = await chat(base, messages);
  return { calls, messages, finalChoice };
}

// --- detection -------------------------------------------------------------

test("the execution-agent system marker selects the P6 scenario", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const choice = await chat(sharedLlm.base, [
    { role: "system", content: p6SystemPrompt() },
    { role: "user", content: p6Instruction() },
  ]);
  assertToolCall(choice, "execution_case_context");
});

test("P1 and P5 keep their behavior while the P6 scenario is registered", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");

  const p1 = await postChat(sharedLlm.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "读取 README" }],
    tools: agentTools(),
  });
  assert.equal(p1.status, 200, p1.text);
  assert.equal(p1.json.choices[0].message.tool_calls[0].function.name, "file_read");

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
  assert.equal(p5.json.choices[0].message.tool_calls[0].function.name, "case_graph_read");
});

// --- normal ----------------------------------------------------------------

test("normal mode drives case_context -> browser form -> records -> passed finish", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const { calls, finalChoice } = await driveToolTurns(sharedLlm.base, 10);

  assert.deepEqual(
    calls.map((call) => call.name),
    [
      "execution_case_context",
      "browser_goto",
      "browser_snapshot",
      "browser_fill",
      "browser_click",
      "browser_screenshot",
      "execution_step_record",
      "execution_step_record",
      "execution_assertion_record",
      "execution_case_finish",
    ],
  );

  assert.deepEqual(calls[0].args, {});
  assert.equal(calls[1].args.url, FIXTURE_BASE + "/p6/orders/new");
  assert.deepEqual(calls[3].args, { target: { selector: "#order-number" }, value: "P6-E2E-ORDER" });
  assert.deepEqual(calls[4].args, { target: { role: "button", name: "Create order" } });
  assert.deepEqual(calls[5].args, {});

  assert.equal(calls[6].args.seq, 1);
  assert.equal(calls[6].args.status, "passed");
  assert.deepEqual(calls[6].args.evidence_ids, [EVIDENCE.evidence_id]);
  assert.equal(calls[7].args.seq, 2);
  assert.deepEqual(calls[7].args.evidence_ids, [EVIDENCE.evidence_id]);

  assert.equal(calls[8].args.assertion_id, "7101");
  assert.equal(calls[8].args.verdict, "passed");
  assert.deepEqual(calls[8].args.evidence_ids, [EVIDENCE.evidence_id]);

  assert.equal(calls[9].args.verdict, "passed");
  assert.equal(calls[9].args.failure_class, undefined);

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
  assert.equal(first.toolCalls[0].name, "execution_case_context");
  assert.equal(first.finishReason, "tool_calls");
  assert.deepEqual(JSON.parse(first.toolCalls[0].arguments), {});
});

// --- fail ------------------------------------------------------------------

test("fail mode records a failed assertion and finishes failed(application)", async () => {
  await withFakeLlm("fail", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 8);
    assert.deepEqual(
      calls.map((call) => call.name),
      [
        "execution_case_context",
        "browser_goto",
        "browser_snapshot",
        "browser_screenshot",
        "execution_step_record",
        "execution_step_record",
        "execution_assertion_record",
        "execution_case_finish",
      ],
    );
    assert.equal(calls[1].args.url, FIXTURE_BASE + "/p6/assert-fail");
    assert.equal(calls[6].args.verdict, "failed");
    assert.ok(String(calls[6].args.actual).length > 0, "a failed assertion needs actual");
    assert.deepEqual(calls[6].args.evidence_ids, [EVIDENCE.evidence_id]);
    assert.equal(calls[7].args.verdict, "failed");
    assert.equal(calls[7].args.failure_class, "application");
    assertTextTurn(finalChoice, "fail final");
  });
});

// --- blocked ---------------------------------------------------------------

test("blocked mode finishes blocked(environment)", async () => {
  await withFakeLlm("blocked", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 8);
    assert.deepEqual(
      calls.map((call) => call.name),
      [
        "execution_case_context",
        "browser_goto",
        "browser_snapshot",
        "browser_screenshot",
        "execution_step_record",
        "execution_step_record",
        "execution_assertion_record",
        "execution_case_finish",
      ],
    );
    assert.equal(calls[7].args.verdict, "blocked");
    assert.equal(calls[7].args.failure_class, "environment");
    assert.ok(String(calls[7].args.reason).length > 0, "blocked needs a reason");
    assertTextTurn(finalChoice, "blocked final");
  });
});

// --- takeover --------------------------------------------------------------

test("takeover mode visits the MFA fixture and finishes blocked(session)", async () => {
  await withFakeLlm("takeover", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 8);
    assert.equal(calls[1].name, "browser_goto");
    assert.match(calls[1].args.url, /\/p6\/login\?mfa=1$/);
    assert.equal(calls[7].name, "execution_case_finish");
    assert.equal(calls[7].args.verdict, "blocked");
    assert.equal(calls[7].args.failure_class, "session");
    assert.match(String(calls[7].args.reason), /mfa/i);
    assertTextTurn(finalChoice, "takeover final");
  });
});

// --- budget ----------------------------------------------------------------

test("budget mode never finishes and keeps calling tools until the budget is gone", async () => {
  await withFakeLlm("budget", async (server) => {
    const { calls, finalChoice } = await driveToolTurns(server.base, 12);
    assert.equal(calls[0].name, "execution_case_context");
    assert.ok(
      calls.slice(1).every((call) => call.name !== "execution_case_finish"),
      "budget mode must never submit case_finish",
    );
    assert.ok(
      calls.slice(1).some((call) => call.name === "browser_snapshot"),
      "budget mode must keep calling tools",
    );
    const choice = finalChoice;
    assert.equal(choice.finish_reason, "tool_calls", "budget mode never answers with final text");
  });
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
    for (const mode of ["fail", "blocked", "takeover", "budget", "normal"]) {
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

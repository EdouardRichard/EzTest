#!/usr/bin/env node
"use strict";

// Deterministic contract tests for the P7 scenarios of scripts/fake-llm.cjs
// (design spec §9.1-§9.5, §12.5):
//
//   - the three P7 role markers (`EzTest 的补全 Agent`, `EzTest 的失败分析 Agent`,
//     `EzTest 的问题整理 Agent`) select their deterministic tool sequences;
//   - POST/GET /__p7-mode switches normal | noop | degraded | invalid at runtime;
//   - every emitted tool name is the P1 gateway's dot->underscore wire form of a
//     frozen profile tool and every P7 id is a decimal string (D29);
//   - the execution agent calls execution.db_assert for kind=db assertions and
//     never sends SQL/target/credentials;
//   - P1/P3/P4/P5/P6 dispatch is untouched.
//
// Run:
//   node scripts/fake-llm.p7.test.cjs

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { after, before, test } = require("node:test");

const repoRoot = path.resolve(__dirname, "..");
const fakeLlmScript = path.join(__dirname, "fake-llm.cjs");
const serverLogRoot = fs.mkdtempSync(path.join(os.tmpdir(), "eztest-p7-servers-"));

const MODEL = "deepseek-flash";
const P7_MARKER_SUPPLEMENT = "EzTest 的补全 Agent";
const P7_MARKER_FAILURE = "EzTest 的失败分析 Agent";
const P7_MARKER_TRIAGE = "EzTest 的问题整理 Agent";
const P6_MARKER = "EzTest 的执行 Agent";
const FIXTURE_BASE = "http://127.0.0.1:13335";

// The wire names of the spec §9.2/§9.3/§9.4 tool allowlists (dot->underscore).
const PROFILE_TOOLS = {
  supplement: [
    "supplement_context_read",
    "supplement_case_propose",
    "supplement_affected_cases_propose",
    "supplement_escalate",
    "supplement_finish",
  ],
  failure: [
    "analysis_case_list",
    "analysis_case_detail",
    "analysis_evidence_meta",
    "analysis_code_read",
    "analysis_candidate_propose",
    "analysis_finish",
  ],
  triage: [
    "triage_candidate_list",
    "triage_issue_list",
    "triage_context_read",
    "triage_issue_draft",
    "triage_finish",
  ],
  execution: [
    "execution_case_context",
    "browser_goto",
    "browser_snapshot",
    "browser_fill",
    "browser_click",
    "browser_screenshot",
    "execution_step_record",
    "execution_assertion_record",
    "execution_db_assert",
    "execution_case_finish",
  ],
};

const PROFILE_SYSTEM = {
  supplement: P7_MARKER_SUPPLEMENT + "\n\n你是补全 Agent，只提议不写库。",
  failure: P7_MARKER_FAILURE + "\n\n只分析 failed(application) 用例。",
  triage: P7_MARKER_TRIAGE + "\n\n把候选整理成可复现的问题单。",
  execution: P6_MARKER + "\n\n你是执行 Agent，只执行给定用例快照。",
};

function supplementInstruction(mode) {
  const incremental = mode === "incremental_design";
  return [
    "## 版本",
    "versionId=7001",
    "runId=7002",
    'versionName="P7 E2E 版本"',
    incremental ? "status=continuing" : "status=executing",
    incremental ? "runKind=incremental_design" : "runKind=supplement",
    "mode=" + mode,
    "",
    "## 已确认需求",
    '- id=7101 title="下单校验" priority=high status=clarified',
    "",
    "## 当前脑图",
    '- nodeId=7201 kind=case title="旧下单用例" priority=P1 status=confirmed revision=3 steps=2 assertions=1 requirements=[7101] source=baseline',
    "",
    "## 任务与提交要求",
    "提交：新增用例必须调用 supplement.case_propose（单次运行最多 5 条）。",
    "增量设计：调用 supplement.affected_cases_propose 给出受影响的旧用例。",
  ].join("\n");
}

const FAILURE_INSTRUCTION = [
  "## Task",
  "You are the failure-analysis agent. Analyze only the failed application cases of this batch.",
  "",
  "## Scope",
  "versionId=7001",
  "batchId=7003",
  "analysisRunId=7004",
  "caseRunIds=7301,7302",
].join("\n");

const TRIAGE_INSTRUCTION = [
  "## Task",
  "You are the issue-triage agent. Turn the failure-analysis candidates into reproducible issue drafts.",
  "",
  "## Scope",
  "versionId=7001",
  "batchId=7003",
  "analysisRunId=7005",
  "failureCandidateCount=1",
].join("\n");

const EXECUTION_INSTRUCTION = [
  "## 执行任务",
  "batchId=9102",
  "caseRunId=9101",
  "baseUrl=" + FIXTURE_BASE,
  "请按步骤执行并提交结构化结果。",
].join("\n");

// --- canonical tool results ------------------------------------------------

function supplementContext(mode) {
  return {
    versionId: "7001",
    batchId: "7003",
    runId: "7002",
    runKind: mode === "incremental_design" ? "incremental_design" : "supplement",
    mode,
    requirements: [{ id: "7101", title: "下单校验", priority: "high", status: "clarified" }],
    cases: [
      {
        nodeId: "7201",
        title: "旧下单用例",
        priority: "P1",
        status: "confirmed",
        source: "baseline",
        stepCount: 2,
        assertionCount: 1,
        requirementIds: ["7101"],
      },
    ],
    coverage: { requirementIds: ["7101"], uncoveredRequirementIds: [], matrix: [] },
    impact: null,
    recentResults: null,
    warnings: [],
  };
}

const PROPOSE_RESULT = { accepted: 2, total: 2, warnings: [] };
const AFFECTED_RESULT = { accepted: 1, total: 1, warnings: [] };
const SUPPLEMENT_FINISH_RESULT = { status: "ok" };
const ANALYSIS_FINISH_RESULT = { finished: true };
const TRIAGE_FINISH_RESULT = { finished: true };
const DRAFT_RESULT = { draftCount: 1, warnings: [] };
const CANDIDATE_PROPOSE_RESULT = { candidateCount: 2, warnings: [] };

const CASE_LIST_RESULT = {
  batchId: "7003",
  cases: [
    {
      caseRunId: "7301",
      caseNodeId: "7201",
      title: "下单失败",
      failureClass: "application",
      failureReason: "断言失败",
      evidenceCount: 2,
    },
    {
      caseRunId: "7302",
      caseNodeId: "7202",
      title: "退款失败",
      failureClass: "application",
      failureReason: "断言失败",
      evidenceCount: 1,
    },
  ],
  truncated: false,
};

const CASE_DETAILS = {
  "7301": {
    caseRunId: "7301",
    caseNodeId: "7201",
    title: "下单失败",
    status: "failed",
    failureClass: "application",
    failureReason: "断言失败",
    steps: [{ seq: 1, action: "提交订单", expected: "订单创建成功", actual: "返回 500", status: "failed", evidenceCount: 1 }],
    assertions: [
      {
        assertionId: "7501",
        kind: "api",
        target: "/api/orders",
        expected: "201",
        actual: "500",
        verdict: "failed",
        failureReason: "状态码不符",
      },
    ],
    evidenceIds: ["7401", "7402"],
  },
  "7302": {
    caseRunId: "7302",
    caseNodeId: "7202",
    title: "退款失败",
    status: "failed",
    failureClass: "application",
    failureReason: "断言失败",
    steps: [{ seq: 1, action: "提交退款", expected: "退款成功", actual: "返回 500", status: "failed", evidenceCount: 1 }],
    assertions: [],
    evidenceIds: ["7403"],
  },
};

const EVIDENCE_META_RESULT = {
  evidence: [
    { evidenceId: "7401", kind: "screenshot", sha256: "a".repeat(64), size: 100, mime: "image/png" },
    { evidenceId: "7402", kind: "trace", sha256: "b".repeat(64), size: 200, mime: "application/zip" },
    { evidenceId: "7403", kind: "screenshot", sha256: "c".repeat(64), size: 300, mime: "image/png" },
  ],
  warnings: [],
};

const CANDIDATE_LIST_RESULT = {
  versionId: "7001",
  batchId: "7003",
  candidates: [
    {
      caseNodeId: "7201",
      caseRunId: "7301",
      title: "P7 候选：下单失败",
      description: "下单接口返回 500",
      severity: "major",
      confidence: "high",
      failureClass: "application",
      rerunSuggestion: "修复后重跑该用例",
      codeRefs: [{ path: "backend/internal/order/service.go", line: 42, reason: "返回 500 的分支" }],
      evidenceIds: ["7401", "7402"],
    },
  ],
};

const TRIAGE_CONTEXT_RESULT = {
  cases: [
    {
      caseRunId: "7301",
      caseNodeId: "7201",
      title: "下单失败",
      failureClass: "application",
      failureReason: "断言失败",
      stepCount: 1,
      assertionCount: 1,
      evidenceIds: ["7401", "7402"],
    },
  ],
  evidence: EVIDENCE_META_RESULT.evidence,
  codeRefs: [
    {
      caseRunId: "7301",
      caseNodeId: "7201",
      path: "backend/internal/order/service.go",
      line: 42,
      reason: "返回 500 的分支",
    },
  ],
};

const EXECUTION_CASE_CONTEXT = {
  case_run_id: "9101",
  batch_id: "9102",
  version_id: "9103",
  case_node_id: "9104",
  title: "P7 DB 断言用例",
  status: "running",
  attempt: 1,
  steps: [{ seq: 1, action: "打开订单页并提交", expected: "订单创建成功" }],
  remaining_steps: [{ seq: 1, action: "打开订单页并提交", expected: "订单创建成功" }],
  assertions: [
    {
      assertion_id: "7101",
      step_seq: 1,
      kind: "ui",
      target: "#result",
      expression: "text contains 订单创建成功",
      expected: "订单创建成功",
    },
    {
      assertion_id: "7102",
      step_seq: 1,
      kind: "db",
      target: "orders",
      expression: "SELECT count(*) FROM orders",
      expected: "1",
    },
  ],
  completed_seqs: [],
  cleanup_required: false,
  base_url: FIXTURE_BASE,
  allowed_hosts: ["127.0.0.1:13335"],
};

const EXECUTION_EVIDENCE = { evidence_id: "8101", sha256: "a".repeat(64), size: 1234 };
const EXECUTION_GOTO = { url: FIXTURE_BASE + "/p6/orders/new", status: 200 };
const EXECUTION_SNAPSHOT = { url: FIXTURE_BASE + "/p6/orders/new", title: "New order", aria: "- heading" };
const DB_ASSERT_RESULT = {
  verdict: "passed",
  rowCount: 1,
  truncated: false,
  elapsedMs: 5,
  actualPreview: "1",
};

let serverCounter = 0;
let sharedLlm = null;

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
      env: { ...process.env, EZTEST_FAKE_LLM_PORT: String(port), ...envOverrides },
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
  const server = await startServer(fakeLlmScript, { EZTEST_FAKE_LLM_P7_MODE: mode }, "/v1/models", "fake-llm");
  try {
    return await fn(server);
  } finally {
    server.stop();
  }
}

// --- wire helpers ----------------------------------------------------------

function agentTools(profile) {
  return PROFILE_TOOLS[profile].map((name) => ({
    type: "function",
    function: {
      name,
      description: name,
      parameters: { type: "object", properties: {}, additionalProperties: false },
    },
  }));
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

function newMessages(profile, instruction) {
  const user =
    instruction !== undefined
      ? instruction
      : profile === "supplement"
        ? supplementInstruction("post_batch")
        : profile === "failure"
          ? FAILURE_INSTRUCTION
          : profile === "triage"
            ? TRIAGE_INSTRUCTION
            : EXECUTION_INSTRUCTION;
  return [
    { role: "system", content: PROFILE_SYSTEM[profile] },
    { role: "user", content: user },
  ];
}

async function chat(base, messages, profile) {
  const res = await postChat(base, {
    model: MODEL,
    stream: false,
    messages,
    tools: agentTools(profile),
  });
  assert.equal(res.status, 200, "chat request failed: " + res.text);
  assert.ok(res.json && Array.isArray(res.json.choices), "missing choices: " + res.text);
  return res.json.choices[0];
}

async function streamChat(base, messages, profile) {
  const response = await fetch(base + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ model: MODEL, stream: true, messages, tools: agentTools(profile) }),
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
        const slot = result.toolCalls[index] || (result.toolCalls[index] = { id: "", name: "", arguments: "" });
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

function assertUnderscoreToolName(name) {
  assert.equal(typeof name, "string");
  assert.ok(!name.includes("."), "tool name " + JSON.stringify(name) + " must not contain a dot");
  assert.match(name, /^[a-z][a-z0-9_]*$/, "tool name " + JSON.stringify(name) + " must be snake_case");
}

/**
 * Asserts that every id-like field of a P7 tool argument object is a decimal
 * string (or an array of decimal strings), never a JSON number (D29).
 */
function assertDecimalStringIDs(value, pathValue) {
  if (Array.isArray(value)) {
    value.forEach((item, index) => assertDecimalStringIDs(item, pathValue + "[" + index + "]"));
    return;
  }
  if (!value || typeof value !== "object") return;
  for (const [key, child] of Object.entries(value)) {
    const childPath = pathValue + "." + key;
    if (/(?:Id|Ids)$/.test(key)) {
      const items = Array.isArray(child) ? child : [child];
      for (const item of items) {
        assert.equal(
          typeof item,
          "string",
          childPath + " must be a decimal string, got " + typeof item + " (" + JSON.stringify(item) + ")",
        );
        assert.match(item, /^[1-9][0-9]*$/, childPath + " must be a positive decimal string");
      }
      continue;
    }
    assertDecimalStringIDs(child, childPath);
  }
}

function expectToolCall(choice) {
  const calls = choice.message.tool_calls || [];
  assert.equal(calls.length, 1, "expected exactly one tool call, got " + calls.length);
  assert.equal(choice.finish_reason, "tool_calls");
  return calls[0];
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

/**
 * Drives a P7 run: takes `toolTurns` tool calls, appends the matching result,
 * then asserts the terminal text turn. Every parsed argument object is checked
 * for decimal-string ids.
 */
async function drive(base, profile, toolTurns, resultFor) {
  const messages = newMessages(profile);
  const calls = [];
  for (let index = 0; index < toolTurns; index += 1) {
    const choice = await chat(base, messages, profile);
    const call = expectToolCall(choice);
    assertUnderscoreToolName(call.function.name);
    assert.ok(
      PROFILE_TOOLS[profile].includes(call.function.name),
      profile + ": unexpected tool " + call.function.name,
    );
    const args = JSON.parse(call.function.arguments);
    assertDecimalStringIDs(args, "args." + call.function.name);
    calls.push({ name: call.function.name, args, rawArguments: call.function.arguments });
    appendToolResult(messages, choice, resultFor(call.function.name, args, index));
  }
  const finalChoice = await chat(base, messages, profile);
  const text = assertTextTurn(finalChoice, profile + " final");
  return { calls, messages, finalChoice, text };
}

function supplementResults(mode) {
  return (name) => {
    switch (name) {
      case "supplement_context_read":
        return supplementContext(mode);
      case "supplement_case_propose":
        return PROPOSE_RESULT;
      case "supplement_affected_cases_propose":
        return AFFECTED_RESULT;
      case "supplement_finish":
        return SUPPLEMENT_FINISH_RESULT;
      default:
        throw new Error("unexpected supplement tool " + name);
    }
  };
}

function failureResults(name) {
  switch (name) {
    case "analysis_case_list":
      return CASE_LIST_RESULT;
    case "analysis_case_detail":
      throw new Error("analysis_case_detail needs the arguments");
    case "analysis_evidence_meta":
      return EVIDENCE_META_RESULT;
    case "analysis_candidate_propose":
      return CANDIDATE_PROPOSE_RESULT;
    case "analysis_finish":
      return ANALYSIS_FINISH_RESULT;
    default:
      throw new Error("unexpected failure tool " + name);
  }
}

function triageResults(issueList) {
  return (name) => {
    switch (name) {
      case "triage_candidate_list":
        return CANDIDATE_LIST_RESULT;
      case "triage_issue_list":
        return issueList;
      case "triage_context_read":
        return TRIAGE_CONTEXT_RESULT;
      case "triage_issue_draft":
        return DRAFT_RESULT;
      case "triage_finish":
        return TRIAGE_FINISH_RESULT;
      default:
        throw new Error("unexpected triage tool " + name);
    }
  };
}

async function setP7Mode(base, mode) {
  const response = await fetch(base + "/__p7-mode", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ mode }),
  });
  assert.equal(response.status, 200, "POST /__p7-mode must answer 200");
  return (await response.json()).mode;
}

async function getP7Mode(base) {
  const response = await fetch(base + "/__p7-mode");
  assert.equal(response.status, 200, "GET /__p7-mode must answer 200");
  return (await response.json()).mode;
}

// --- marker detection ------------------------------------------------------

test("the three P7 role markers select their deterministic first tool", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");

  const supplement = await chat(sharedLlm.base, newMessages("supplement"), "supplement");
  const supplementCall = expectToolCall(supplement);
  assert.equal(supplementCall.function.name, "supplement_context_read");
  assert.deepEqual(JSON.parse(supplementCall.function.arguments), { mode: "post_batch" });

  const failure = await chat(sharedLlm.base, newMessages("failure"), "failure");
  const failureCall = expectToolCall(failure);
  assert.equal(failureCall.function.name, "analysis_case_list");
  assert.deepEqual(JSON.parse(failureCall.function.arguments), { batchId: "7003" });

  const triage = await chat(sharedLlm.base, newMessages("triage"), "triage");
  const triageCall = expectToolCall(triage);
  assert.equal(triageCall.function.name, "triage_candidate_list");
  assert.deepEqual(JSON.parse(triageCall.function.arguments), {});
});

test("the supplement scenario streams the same underscore tool turn", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const stream = await streamChat(sharedLlm.base, newMessages("supplement"), "supplement");
  assert.equal(stream.finishReason, "tool_calls");
  assert.equal(stream.toolCalls[0].name, "supplement_context_read");
  assert.deepEqual(JSON.parse(stream.toolCalls[0].arguments), { mode: "post_batch" });
});

// --- supplement ------------------------------------------------------------

test("normal post_batch proposes 2 new cases with decimal-string requirement ids", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const { calls, text } = await drive(sharedLlm.base, "supplement", 3, supplementResults("post_batch"));

  assert.deepEqual(
    calls.map((call) => call.name),
    ["supplement_context_read", "supplement_case_propose", "supplement_finish"],
  );
  assert.deepEqual(calls[0].args, { mode: "post_batch" });

  const cases = calls[1].args.cases;
  assert.ok(Array.isArray(cases) && cases.length >= 1 && cases.length <= 2, "propose 1-2 new cases");
  for (const supplementCase of cases) {
    assert.equal(typeof supplementCase.title, "string");
    assert.ok(supplementCase.title.length > 0, "a proposed case needs a title");
    assert.ok(Array.isArray(supplementCase.steps) && supplementCase.steps.length >= 1, "a case needs a step");
    assert.ok(
      Array.isArray(supplementCase.assertions) && supplementCase.assertions.length >= 1,
      "a case needs an assertion",
    );
    assert.ok(Array.isArray(supplementCase.requirementIds) && supplementCase.requirementIds.length >= 1);
    assert.deepEqual(supplementCase.requirementIds, ["7101"]);
    assert.ok(["high", "medium", "low"].includes(supplementCase.confidence));
  }
  assert.equal(typeof calls[2].args.summary, "string");
  assert.ok(calls[2].args.summary.length > 0, "supplement.finish needs a summary");
  assert.match(text, /P7/);
});

test("normal incremental_design also proposes one affected case with code refs", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const instruction = supplementInstruction("incremental_design");
  const messages = newMessages("supplement", instruction);
  const calls = [];
  for (let index = 0; index < 4; index += 1) {
    const choice = await chat(sharedLlm.base, messages, "supplement");
    const call = expectToolCall(choice);
    assertUnderscoreToolName(call.function.name);
    const args = JSON.parse(call.function.arguments);
    assertDecimalStringIDs(args, "args." + call.function.name);
    calls.push({ name: call.function.name, args });
    appendToolResult(messages, choice, supplementResults("incremental_design")(call.function.name, args, index));
  }
  const finalChoice = await chat(sharedLlm.base, messages, "supplement");
  assert.match(assertTextTurn(finalChoice, "incremental final"), /P7/);

  assert.deepEqual(
    calls.map((call) => call.name),
    [
      "supplement_context_read",
      "supplement_case_propose",
      "supplement_affected_cases_propose",
      "supplement_finish",
    ],
  );
  assert.deepEqual(calls[0].args, { mode: "incremental_design" });

  const affected = calls[2].args.cases;
  assert.ok(Array.isArray(affected) && affected.length === 1, "exactly one affected case");
  assert.equal(affected[0].caseNodeId, "7201");
  assert.ok(["high", "medium", "low"].includes(affected[0].confidence));
  assert.ok(["requirement", "code"].includes(affected[0].source));
  assert.equal(typeof affected[0].reason, "string");
  assert.ok(affected[0].reason.length > 0);
  assert.ok(Array.isArray(affected[0].codeRefs) && affected[0].codeRefs.length >= 1, "affected needs codeRefs");
  for (const ref of affected[0].codeRefs) {
    assert.equal(typeof ref.path, "string");
    assert.ok(ref.path.length > 0);
    assert.equal(typeof ref.line, "number");
    assert.ok(ref.line > 0);
    assert.equal(typeof ref.reason, "string");
  }
});

test("noop finishes the supplement run without any proposal", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  try {
    assert.equal(await setP7Mode(sharedLlm.base, "noop"), "noop");
    const { calls, text } = await drive(sharedLlm.base, "supplement", 2, supplementResults("post_batch"));
    assert.deepEqual(
      calls.map((call) => call.name),
      ["supplement_context_read", "supplement_finish"],
    );
    assert.match(text, /P7/);
  } finally {
    await setP7Mode(sharedLlm.base, "normal");
  }
});

// --- failure analysis ------------------------------------------------------

function failureResultFor(name, args) {
  if (name === "analysis_case_detail") {
    const detail = CASE_DETAILS[args.caseRunId];
    assert.ok(detail, "analysis.case_detail for an unexpected case run " + args.caseRunId);
    return detail;
  }
  return failureResults(name);
}

test("normal failure analysis proposes candidates from case evidence", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const { calls, text } = await drive(sharedLlm.base, "failure", 6, failureResultFor);

  assert.deepEqual(
    calls.map((call) => call.name),
    [
      "analysis_case_list",
      "analysis_case_detail",
      "analysis_case_detail",
      "analysis_evidence_meta",
      "analysis_candidate_propose",
      "analysis_finish",
    ],
  );
  assert.deepEqual(calls[0].args, { batchId: "7003" });
  assert.deepEqual(calls[1].args, { caseRunId: "7301" });
  assert.deepEqual(calls[2].args, { caseRunId: "7302" });
  assert.deepEqual(calls[3].args, { evidenceIds: ["7401", "7402", "7403"] });

  const candidates = calls[4].args.candidates;
  assert.ok(Array.isArray(candidates) && candidates.length >= 1 && candidates.length <= 2, "1-2 candidates");
  for (const candidate of candidates) {
    assert.equal(candidate.failureClass, "application");
    assert.ok(candidate.evidenceIds.length >= 1, "a candidate needs evidence");
    assert.ok(["high", "medium", "low"].includes(candidate.confidence));
    assert.equal(typeof candidate.title, "string");
    assert.ok(candidate.title.length > 0);
  }
  assert.deepEqual(
    candidates.map((candidate) => candidate.caseRunId),
    ["7301", "7302"],
  );
  assert.equal(typeof calls[5].args.summary, "string");
  assert.match(text, /P7/);
});

test("degraded P7 agents answer with text only and no tool call", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  try {
    assert.equal(await setP7Mode(sharedLlm.base, "degraded"), "degraded");
    for (const profile of ["supplement", "failure", "triage"]) {
      const choice = await chat(sharedLlm.base, newMessages(profile), profile);
      const text = assertTextTurn(choice, profile + " degraded");
      assert.match(text, /P7/);
    }
  } finally {
    await setP7Mode(sharedLlm.base, "normal");
  }
});

test("invalid failure analysis submits malformed candidate JSON then ends with text", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  try {
    assert.equal(await setP7Mode(sharedLlm.base, "invalid"), "invalid");
    const messages = newMessages("failure");
    const first = await chat(sharedLlm.base, messages, "failure");
    const firstCall = expectToolCall(first);
    assert.equal(firstCall.function.name, "analysis_case_list");
    appendToolResult(messages, first, CASE_LIST_RESULT);

    const second = await chat(sharedLlm.base, messages, "failure");
    const secondCall = expectToolCall(second);
    assert.equal(secondCall.function.name, "analysis_candidate_propose");
    assert.throws(() => JSON.parse(secondCall.function.arguments), /JSON/, "invalid mode must emit malformed JSON");
    appendToolResult(messages, second, { code: "invalid_arguments", message: "invalid JSON arguments" });

    const third = await chat(sharedLlm.base, messages, "failure");
    assert.match(assertTextTurn(third, "invalid failure final"), /P7/);
  } finally {
    await setP7Mode(sharedLlm.base, "normal");
  }
});

test("noop failure analysis finishes without proposals", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  try {
    assert.equal(await setP7Mode(sharedLlm.base, "noop"), "noop");
    const { calls } = await drive(sharedLlm.base, "failure", 2, failureResultFor);
    assert.deepEqual(
      calls.map((call) => call.name),
      ["analysis_case_list", "analysis_finish"],
    );
  } finally {
    await setP7Mode(sharedLlm.base, "normal");
  }
});

// --- issue triage ----------------------------------------------------------

test("normal triage drafts exactly one reproducible issue", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const { calls, text } = await drive(sharedLlm.base, "triage", 5, triageResults({ issues: [] }));

  assert.deepEqual(
    calls.map((call) => call.name),
    [
      "triage_candidate_list",
      "triage_issue_list",
      "triage_context_read",
      "triage_issue_draft",
      "triage_finish",
    ],
  );
  assert.deepEqual(calls[0].args, {});
  assert.deepEqual(calls[1].args, {});
  assert.deepEqual(calls[2].args, { caseRunIds: ["7301"], evidenceIds: ["7401", "7402"] });

  const issues = calls[3].args.issues;
  assert.ok(Array.isArray(issues) && issues.length === 1, "exactly one issue draft");
  const draft = issues[0];
  assert.equal(typeof draft.title, "string");
  assert.ok(draft.title.length > 0);
  assert.equal(draft.caseRunId, "7301");
  assert.equal(draft.caseNodeId, "7201");
  assert.deepEqual(draft.evidenceIds, ["7401", "7402"]);
  assert.ok(["blocker", "major", "minor", "info"].includes(draft.severity));
  assert.ok(["high", "medium", "low"].includes(draft.confidence));
  for (const field of ["description", "reproSteps", "expected", "actual"]) {
    assert.equal(typeof draft[field], "string", field + " must be a string");
    assert.ok(draft[field].length > 0, field + " must not be empty");
  }
  assert.ok(Array.isArray(draft.codeRefs) && draft.codeRefs.length >= 1, "the draft carries code refs");
  assert.equal(draft.mergeIntoIssueId, undefined, "no open issue to merge into");
  assert.match(text, /P7/);
});

test("triage merges into an open issue when the title matches", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const issueList = {
    issues: [{ issueId: "7501", title: "P7 候选：下单失败", severity: "major" }],
  };
  const { calls } = await drive(sharedLlm.base, "triage", 5, triageResults(issueList));
  const draft = calls[3].args.issues[0];
  assert.equal(draft.mergeIntoIssueId, "7501");
});

test("invalid triage submits malformed draft JSON then ends with text", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  try {
    assert.equal(await setP7Mode(sharedLlm.base, "invalid"), "invalid");
    const messages = newMessages("triage");
    const first = await chat(sharedLlm.base, messages, "triage");
    const firstCall = expectToolCall(first);
    assert.equal(firstCall.function.name, "triage_candidate_list");
    appendToolResult(messages, first, CANDIDATE_LIST_RESULT);

    const second = await chat(sharedLlm.base, messages, "triage");
    const secondCall = expectToolCall(second);
    assert.equal(secondCall.function.name, "triage_issue_draft");
    assert.throws(() => JSON.parse(secondCall.function.arguments), /JSON/, "invalid mode must emit malformed JSON");
    appendToolResult(messages, second, { code: "invalid_arguments", message: "invalid JSON arguments" });

    const third = await chat(sharedLlm.base, messages, "triage");
    assert.match(assertTextTurn(third, "invalid triage final"), /P7/);
  } finally {
    await setP7Mode(sharedLlm.base, "normal");
  }
});

test("invalid supplement submits malformed propose JSON then ends with text", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  try {
    assert.equal(await setP7Mode(sharedLlm.base, "invalid"), "invalid");
    const messages = newMessages("supplement");
    const first = await chat(sharedLlm.base, messages, "supplement");
    const firstCall = expectToolCall(first);
    assert.equal(firstCall.function.name, "supplement_context_read");
    appendToolResult(messages, first, supplementContext("post_batch"));

    const second = await chat(sharedLlm.base, messages, "supplement");
    const secondCall = expectToolCall(second);
    assert.equal(secondCall.function.name, "supplement_case_propose");
    assert.throws(() => JSON.parse(secondCall.function.arguments), /JSON/, "invalid mode must emit malformed JSON");
    appendToolResult(messages, second, { code: "invalid_arguments", message: "invalid JSON arguments" });

    const third = await chat(sharedLlm.base, messages, "supplement");
    assert.match(assertTextTurn(third, "invalid supplement final"), /P7/);
  } finally {
    await setP7Mode(sharedLlm.base, "normal");
  }
});

test("noop triage finishes without drafts", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  try {
    assert.equal(await setP7Mode(sharedLlm.base, "noop"), "noop");
    const { calls } = await drive(sharedLlm.base, "triage", 2, triageResults({ issues: [] }));
    assert.deepEqual(
      calls.map((call) => call.name),
      ["triage_candidate_list", "triage_finish"],
    );
  } finally {
    await setP7Mode(sharedLlm.base, "normal");
  }
});

// --- P7 execution addition: execution.db_assert ---------------------------

test("the execution agent calls execution.db_assert for kind=db assertions only", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  const messages = newMessages("execution");
  const calls = [];
  for (let index = 0; index < 10; index += 1) {
    const choice = await chat(sharedLlm.base, messages, "execution");
    const call = expectToolCall(choice);
    assertUnderscoreToolName(call.function.name);
    assert.ok(PROFILE_TOOLS.execution.includes(call.function.name));
    const args = JSON.parse(call.function.arguments);
    calls.push({ name: call.function.name, args });
    let result;
    switch (call.function.name) {
      case "execution_case_context":
        result = EXECUTION_CASE_CONTEXT;
        break;
      case "browser_goto":
        result = EXECUTION_GOTO;
        break;
      case "browser_snapshot":
        result = EXECUTION_SNAPSHOT;
        break;
      case "browser_fill":
      case "browser_click":
        result = { ok: true };
        break;
      case "browser_screenshot":
        result = EXECUTION_EVIDENCE;
        break;
      case "execution_step_record":
        result = { step_result_id: "8201", seq: 1, status: "passed", evidence_count: 1 };
        break;
      case "execution_assertion_record":
        result = { assertion_result_id: "8301", assertion_id: "7101", verdict: "passed" };
        break;
      case "execution_db_assert":
        result = DB_ASSERT_RESULT;
        break;
      case "execution_case_finish":
        result = { case_run_id: "9101", status: "passed" };
        break;
      default:
        throw new Error("unexpected execution tool " + call.function.name);
    }
    appendToolResult(messages, choice, result);
  }
  const finalChoice = await chat(sharedLlm.base, messages, "execution");
  assert.match(assertTextTurn(finalChoice, "execution final"), /P6/);

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
      "execution_assertion_record",
      "execution_db_assert",
      "execution_case_finish",
    ],
  );
  // The UI assertion still goes through execution_assertion_record; the db
  // assertion goes through the P7 tool exactly once.
  assert.equal(calls.filter((call) => call.name === "execution_db_assert").length, 1);
  const dbCall = calls.find((call) => call.name === "execution_db_assert");
  assert.deepEqual(dbCall.args, { assertionId: "7102" });
  // §9.5: the agent supplies only the assertion id, never SQL/target/credentials.
  for (const forbidden of ["sql", "expected", "target", "connection", "password", "credentials"]) {
    assert.equal(Object.prototype.hasOwnProperty.call(dbCall.args, forbidden), false, "db_assert must not send " + forbidden);
  }
});

// --- runtime mode control --------------------------------------------------

test("POST/GET /__p7-mode switches, rejects unknown modes and resets", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");
  try {
    assert.equal(await getP7Mode(sharedLlm.base), "normal", "the default P7 mode must be normal");
    for (const mode of ["noop", "degraded", "invalid", "normal"]) {
      assert.equal(await setP7Mode(sharedLlm.base, mode), mode);
      assert.equal(await getP7Mode(sharedLlm.base), mode);
    }
    const rejected = await fetch(sharedLlm.base + "/__p7-mode", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode: "not-a-mode" }),
    });
    assert.equal(rejected.status, 400, "an unknown mode must be rejected");
    assert.equal(await getP7Mode(sharedLlm.base), "normal", "a rejected mode must not change the active mode");

    const badBody = await fetch(sharedLlm.base + "/__p7-mode", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{",
    });
    assert.equal(badBody.status, 400, "an invalid JSON body must be rejected");
  } finally {
    await setP7Mode(sharedLlm.base, "normal");
  }
});

// --- regression guard: P1/P3/P4/P5/P6 dispatch is untouched ---------------

test("P1, P3, P4, P5 and P6 requests keep their pre-P7 behavior", async () => {
  assert.ok(sharedLlm, "shared fake LLM server was not started");

  const p1 = await postChat(sharedLlm.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "读取 README" }],
    tools: agentTools("execution"),
  });
  assert.equal(p1.status, 200, p1.text);
  assert.equal(p1.json.choices[0].message.tool_calls[0].function.name, "file_read");

  const p3 = await postChat(sharedLlm.base, {
    model: MODEL,
    stream: false,
    messages: [{ role: "user", content: "P3-CLARIFY-1 version=7" }],
    tools: agentTools("execution"),
  });
  assert.equal(p3.status, 200, p3.text);
  assert.equal(p3.json.choices[0].message.tool_calls[0].function.name, "version_context_get");

  const p4 = await postChat(sharedLlm.base, {
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

  const p5 = await postChat(sharedLlm.base, {
    model: MODEL,
    stream: false,
    messages: [
      { role: "system", content: "EzTest 的测试设计 Agent" },
      { role: "user", content: "versionId=123\n必须调用 case_graph_propose。" },
    ],
    tools: agentTools("supplement"),
  });
  assert.equal(p5.status, 200, p5.text);
  assert.equal(p5.json.choices[0].message.tool_calls[0].function.name, "case_graph_read");

  const p6 = await postChat(sharedLlm.base, {
    model: MODEL,
    stream: false,
    messages: newMessages("execution", EXECUTION_INSTRUCTION),
    tools: agentTools("execution"),
  });
  assert.equal(p6.status, 200, p6.text);
  assert.equal(p6.json.choices[0].message.tool_calls[0].function.name, "execution_case_context");
});

before(async () => {
  sharedLlm = await startServer(fakeLlmScript, { EZTEST_FAKE_LLM_P7_MODE: "normal" }, "/v1/models", "shared-llm");
});

after(() => {
  if (sharedLlm) sharedLlm.stop();
  fs.rmSync(serverLogRoot, { recursive: true, force: true });
});

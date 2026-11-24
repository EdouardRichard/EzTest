#!/usr/bin/env node
"use strict";

// Contract tests for the design_judge / design_author dispatchers in
// scripts/fake-llm.cjs (v0.2.4-E T19, spec SC-E12).
//
// Expectations are derived from the CURRENT tool contracts, NOT from the
// double's observed output (B fifth-layer lesson):
//   * design.judgment_submit — backend/internal/agent/application/tools/judgment.go:
//     top level {requirement_id, judgments[]} (both required); each judgment
//     {case_id, case_version_id, verdict(pass|fail), findings[]?, ac_coverage_matrix?};
//     findings[] {dimension, severity(blocker|important|minor), message, case_ref?};
//     round is SERVER-derived and must never be emitted; unknown fields are
//     hard-rejected (DisallowUnknownFields). A pass verdict cannot carry
//     blocker/important findings (designapp.JudgmentService.SubmitJudgment).
//   * the judge review scope — runner.go taskContext: one
//     "judge_case=case_id:<id>,case_version_id:<id>,content_hash:<64hex>" line per
//     pending draft, rendered in the USER task block (Task 80), plus the
//     requirement_id=<id> scope line.
//   * case.graph_propose — tools/design.go natural-language case contract
//     (sut_description/preconditions/covered_acs/prose targets; no resource ids).
//
// Run: node --test scripts/fake-llm-judge.test.cjs

const { after, before, test } = require("node:test");
const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const path = require("node:path");

const SCRIPT = path.join(__dirname, "fake-llm.cjs");

let child = null;
let baseUrl = "";
let serverStderr = "";

function startFake() {
  return new Promise((resolve, reject) => {
    child = spawn(process.execPath, [SCRIPT], {
      env: { ...process.env, FAKE_LLM_PORT: "0" },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    const timer = setTimeout(() => {
      reject(new Error("fake-llm did not start within 10s: " + serverStderr));
    }, 10000);
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
      const match = stdout.match(/listening on (http:\/\/127\.0\.0\.1:\d+)/);
      if (match && baseUrl === "") {
        baseUrl = match[1];
        clearTimeout(timer);
        resolve();
      }
    });
    child.stderr.on("data", (chunk) => {
      serverStderr += chunk;
    });
    child.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.on("exit", (code) => {
      if (baseUrl === "") {
        clearTimeout(timer);
        reject(new Error("fake-llm exited with " + code + ": " + serverStderr));
      }
    });
  });
}

before(async () => {
  await startFake();
});

after(() => {
  if (child) child.kill();
});

async function postChat(body) {
  const response = await fetch(baseUrl + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const json = await response.json();
  return { status: response.status, json };
}

async function postChatStream(body) {
  const response = await fetch(baseUrl + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...body, stream: true }),
  });
  const text = await response.text();
  return {
    status: response.status,
    contentType: response.headers.get("content-type") || "",
    text,
  };
}

async function setMode(route, mode) {
  const response = await fetch(baseUrl + route, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ mode }),
  });
  return { status: response.status, json: await response.json() };
}

async function getMode(route) {
  const response = await fetch(baseUrl + route);
  return { status: response.status, json: await response.json() };
}

// --- contract assertion helpers (expectations from judgment.go/design.go) ----

const DECIMAL_ID = /^[1-9][0-9]*$/;
const AC_ID_PATTERN = /^AC-[1-9][0-9]*$/;
const VERDICTS = ["pass", "fail"];
const SEVERITIES = ["blocker", "important", "minor"];
const DIMENSIONS = ["coverage", "executability", "system"];
// judgment.go judgmentSubmitPayload / judgmentCasePayload / judgmentFindingPayload:
// these are the ONLY fields the strict decoder accepts.
const JUDGMENT_TOP_FIELDS = ["requirement_id", "judgments"];
const JUDGMENT_CASE_FIELDS = ["case_id", "case_version_id", "verdict", "findings", "ac_coverage_matrix"];
const JUDGMENT_FINDING_FIELDS = ["dimension", "severity", "message", "case_ref"];

function assertOnlyFields(obj, allowed, label) {
  for (const key of Object.keys(obj)) {
    assert.ok(
      allowed.includes(key),
      label + " carries field " + key + " outside the tool contract: " + JSON.stringify(obj),
    );
  }
}

// assertJudgmentContract pins the design.judgment_submit schema from
// tools/judgment.go. scopedCases is the judge_case scope the runner rendered.
function assertJudgmentContract(args, requirementID, scopedCases) {
  assertOnlyFields(args, JUDGMENT_TOP_FIELDS, "judgment payload");
  assert.equal(typeof args.requirement_id, "string");
  assert.match(args.requirement_id, DECIMAL_ID);
  assert.equal(args.requirement_id, requirementID);
  assert.ok(Array.isArray(args.judgments) && args.judgments.length >= 1 && args.judgments.length <= 50);
  assert.equal(args.judgments.length, scopedCases.length, "one verdict per scoped judge_case");
  for (let i = 0; i < args.judgments.length; i += 1) {
    const item = args.judgments[i];
    assertOnlyFields(item, JUDGMENT_CASE_FIELDS, "judgments[" + i + "]");
    assert.equal(typeof item.case_id, "string");
    assert.match(item.case_id, DECIMAL_ID);
    assert.equal(item.case_id, scopedCases[i].caseID);
    assert.equal(typeof item.case_version_id, "string");
    assert.match(item.case_version_id, DECIMAL_ID);
    assert.equal(item.case_version_id, scopedCases[i].caseVersionID);
    assert.ok(VERDICTS.includes(item.verdict), "verdict enum: " + item.verdict);
    // round is server-supplied (T13): never a model field, at any level.
    assert.equal(Object.prototype.hasOwnProperty.call(item, "round"), false, "round must not be emitted");
    if (item.findings !== undefined) {
      assert.ok(Array.isArray(item.findings) && item.findings.length <= 50);
      for (const finding of item.findings) {
        assertOnlyFields(finding, JUDGMENT_FINDING_FIELDS, "finding");
        assert.ok(typeof finding.dimension === "string" && finding.dimension.length >= 1 && finding.dimension.length <= 64);
        assert.ok(DIMENSIONS.includes(finding.dimension), "rubric dimension: " + finding.dimension);
        assert.ok(SEVERITIES.includes(finding.severity), "severity enum: " + finding.severity);
        assert.ok(typeof finding.message === "string" && finding.message.length >= 1 && finding.message.length <= 2000);
        if (finding.case_ref !== undefined) {
          assert.equal(typeof finding.case_ref, "string");
          assert.match(finding.case_ref, DECIMAL_ID);
        }
        // SubmitJudgment rubric consistency: pass never carries blocker/important.
        if (item.verdict === "pass") {
          assert.equal(finding.severity, "minor", "pass verdict carries a non-minor finding");
        }
      }
    }
    if (item.ac_coverage_matrix !== undefined) {
      assert.equal(typeof item.ac_coverage_matrix, "object");
      assert.ok(item.ac_coverage_matrix !== null && !Array.isArray(item.ac_coverage_matrix));
      for (const key of Object.keys(item.ac_coverage_matrix)) {
        assert.match(key, AC_ID_PATTERN, "ac_coverage_matrix keyed by AC id: " + key);
      }
    }
  }
  assert.equal(Object.prototype.hasOwnProperty.call(args, "round"), false, "round must not be emitted");
}

function assistantToolCall(message) {
  assert.equal(message.role, "assistant");
  assert.equal(Array.isArray(message.tool_calls), true, "expected a tool call, got text: " + message.content);
  assert.equal(message.tool_calls.length, 1);
  const call = message.tool_calls[0];
  assert.equal(call.type, "function");
  const args = JSON.parse(call.function.arguments);
  return { name: call.function.name, args };
}

function toolResult(payload) {
  return { role: "tool", content: JSON.stringify(payload) };
}

// --- judge prompt fixtures (mirrors the real runner rendering) ---------------

const SCOPED_CASES = [
  { caseID: "7001", caseVersionID: "8001", contentHash: "a".repeat(64) },
  { caseID: "7002", caseVersionID: "8002", contentHash: "b".repeat(64) },
];

function judgeSystem() {
  return {
    role: "system",
    content:
      "你是 EzTest 的设计裁判（design_judge，后台作业，独立用例质量裁判）。\n" +
      "审查三维 rubric：覆盖率/可执行标准/关联系统。\n[profile:design_judge]",
  };
}

// judgeUser renders the task block exactly as runner.go taskContext/taskBlock
// does: the judge_case scope lines live in the USER turn (Task 80), not system.
function judgeUser(cases) {
  const scope = [
    "- version_id=1001",
    "- requirement_id=3001",
    "- requirement_version_id=3002",
    ...cases.map(
      (c) => "- judge_case=case_id:" + c.caseID + ",case_version_id:" + c.caseVersionID + ",content_hash:" + c.contentHash,
    ),
  ].join("\n");
  return {
    role: "user",
    content:
      "# 任务\n类型: design.judge\n目标: 独立审查当前需求的用例草案（三维 rubric），逐用例用 design.judgment_submit 提交 verdict 与 findings。\n" +
      "作用域:\n" + scope + "\n\n请独立审查需求 3001（需求版本 3002）作用域内列出的 " + cases.length + " 个待审用例草案，逐用例提交 verdict 与 findings。",
  };
}

const JUDGE_GRAPH = {
  version_id: "1001",
  graph_hash: "c".repeat(64),
  requirements: [
    {
      requirement_id: "3001",
      requirement_version_id: "3002",
      revision: 1,
      title: "登录",
      status: "clarified",
      scenarios: [
        {
          id: "5001",
          requirement_version_id: "3002",
          name: "正常登录",
          cases: [
            {
              case_id: "7001",
              current_version_id: "8001",
              revision: 1,
              title: "账号密码登录成功",
              status: "draft",
              sut_description: "登录页与登录接口。",
              covered_acs: ["AC-1"],
            },
            {
              case_id: "7002",
              current_version_id: "8002",
              revision: 1,
              title: "密码错误提示",
              status: "draft",
              sut_description: "登录页与登录接口。",
              covered_acs: ["AC-1"],
            },
          ],
        },
      ],
    },
  ],
};

const SUBMIT_RESULT = {
  submitted: [
    { judgment_id: "9001", case_id: "7001", case_version_id: "8001", content_hash: "a".repeat(64), round: 1, verdict: "pass" },
    { judgment_id: "9002", case_id: "7002", case_version_id: "8002", content_hash: "b".repeat(64), round: 1, verdict: "pass" },
  ],
  count: 2,
};

// --- author prompt fixtures ---------------------------------------------------

const AUTHOR_SYSTEM = {
  role: "system",
  content: "你是 EzTest 的设计 Author（后台作业，场景/用例作者）。\n[profile:design_author]",
};

function authorUser(type, extraScope) {
  const scope = ["- version_id=1001", "- requirement_id=3001", "- requirement_version_id=3002", ...(extraScope || [])].join("\n");
  return {
    role: "user",
    content: "# 任务\n类型: " + type + "\n作用域:\n" + scope + "\n\n请为需求 3001（需求版本 3002）设计场景与用例草案。",
  };
}

const AUTHOR_GRAPH_EMPTY = {
  version_id: "1001",
  graph_hash: "d".repeat(64),
  requirements: [
    { requirement_id: "3001", requirement_version_id: "3002", revision: 1, title: "登录", status: "clarified", scenarios: [] },
  ],
};

const AUTHOR_REQUIREMENT_READ = {
  requirement_id: "3001",
  requirement_version_id: "3002",
  revision: 1,
  status: "clarified",
  title: "登录",
  sut_description: "登录页与登录接口的被测系统描述。",
  acceptance_criteria: [
    { id: "AC-1", given: "已注册账号", when: "用户执行登录", then: "登录成功" },
    { id: "AC-2", given: "连续失败", when: "再次提交", then: "账号锁定" },
  ],
  ac_ids: ["AC-1", "AC-2"],
};

const PROPOSE_RESULT = {
  scenario_ids: ["5001"],
  case_ids: ["7001", "7002"],
  case_version_ids: ["8001", "8002"],
  graph_hash: "e".repeat(64),
};

// --- mode control -------------------------------------------------------------

test("judge mode control defaults to pass and validates the mode", async () => {
  const initial = await getMode("/__judge-mode");
  assert.equal(initial.status, 200);
  assert.equal(initial.json.mode, "pass");

  const bad = await setMode("/__judge-mode", "bogus");
  assert.equal(bad.status, 400);

  for (const mode of ["fail", "minor", "error", "pass"]) {
    const ok = await setMode("/__judge-mode", mode);
    assert.equal(ok.status, 200);
    assert.equal(ok.json.mode, mode);
  }
});

test("author mode control defaults to full and validates the mode", async () => {
  const initial = await getMode("/__author-mode");
  assert.equal(initial.status, 200);
  assert.equal(initial.json.mode, "full");

  const bad = await setMode("/__author-mode", "bogus");
  assert.equal(bad.status, 400);

  const ok = await setMode("/__author-mode", "partial");
  assert.equal(ok.status, 200);
  assert.equal(ok.json.mode, "partial");
  await setMode("/__author-mode", "full");
});

// --- judge dispatcher ----------------------------------------------------------

test("judge reads the design graph before submitting any judgment", async () => {
  await setMode("/__judge-mode", "pass");
  const { status, json } = await postChat({ messages: [judgeSystem(), judgeUser(SCOPED_CASES)] });
  assert.equal(status, 200);
  const call = assistantToolCall(json.choices[0].message);
  assert.equal(call.name, "design.graph_read");
});

test("fake-llm judge emits judgment_submit matching the tool schema (pass)", async () => {
  await setMode("/__judge-mode", "pass");
  const { status, json } = await postChat({
    messages: [judgeSystem(), judgeUser(SCOPED_CASES), toolResult(JUDGE_GRAPH)],
  });
  assert.equal(status, 200);
  const call = assistantToolCall(json.choices[0].message);
  assert.equal(call.name, "design.judgment_submit");
  assertJudgmentContract(call.args, "3001", SCOPED_CASES);
  for (const item of call.args.judgments) {
    assert.equal(item.verdict, "pass");
  }
});

test("judge fail mode emits fail verdicts with a blocker finding per case", async () => {
  await setMode("/__judge-mode", "fail");
  const { status, json } = await postChat({
    messages: [judgeSystem(), judgeUser(SCOPED_CASES), toolResult(JUDGE_GRAPH)],
  });
  assert.equal(status, 200);
  const call = assistantToolCall(json.choices[0].message);
  assert.equal(call.name, "design.judgment_submit");
  assertJudgmentContract(call.args, "3001", SCOPED_CASES);
  for (const item of call.args.judgments) {
    assert.equal(item.verdict, "fail");
    assert.ok(Array.isArray(item.findings) && item.findings.length >= 1);
    assert.ok(
      item.findings.some((f) => f.severity === "blocker" || f.severity === "important"),
      "a fail verdict must carry a blocker/important finding",
    );
  }
  await setMode("/__judge-mode", "pass");
});

test("judge minor mode emits pass verdicts carrying only minor findings", async () => {
  await setMode("/__judge-mode", "minor");
  const { status, json } = await postChat({
    messages: [judgeSystem(), judgeUser(SCOPED_CASES), toolResult(JUDGE_GRAPH)],
  });
  assert.equal(status, 200);
  const call = assistantToolCall(json.choices[0].message);
  assert.equal(call.name, "design.judgment_submit");
  assertJudgmentContract(call.args, "3001", SCOPED_CASES);
  for (const item of call.args.judgments) {
    assert.equal(item.verdict, "pass");
    assert.ok(Array.isArray(item.findings) && item.findings.length >= 1);
    for (const finding of item.findings) assert.equal(finding.severity, "minor");
  }
  await setMode("/__judge-mode", "pass");
});

test("judge finishes with text after the settled submit result (no more tools)", async () => {
  await setMode("/__judge-mode", "pass");
  const { status, json } = await postChat({
    messages: [judgeSystem(), judgeUser(SCOPED_CASES), toolResult(JUDGE_GRAPH), toolResult(SUBMIT_RESULT)],
  });
  assert.equal(status, 200);
  const message = json.choices[0].message;
  assert.equal(message.tool_calls, undefined);
  assert.equal(typeof message.content, "string");
  assert.ok(message.content.length > 0);
  assert.equal(json.choices[0].finish_reason, "stop");
});

test("judge error mode answers HTTP 500 server_error (LLM unavailable)", async () => {
  await setMode("/__judge-mode", "error");
  const nonStream = await postChat({ messages: [judgeSystem(), judgeUser(SCOPED_CASES)] });
  assert.equal(nonStream.status, 500);
  assert.equal(nonStream.json.error.type, "server_error");

  const stream = await postChatStream({ messages: [judgeSystem(), judgeUser(SCOPED_CASES)] });
  assert.equal(stream.status, 500);
  assert.ok(stream.text.includes("server_error"));

  // The error mode is judge-scoped: an author request still answers normally.
  const author = await postChat({ messages: [AUTHOR_SYSTEM, authorUser("design.requirement")] });
  assert.equal(author.status, 200);
  await setMode("/__judge-mode", "pass");
});

test("judge streams judgment_submit tool_calls that reassemble to the contract", async () => {
  await setMode("/__judge-mode", "pass");
  const { status, contentType, text } = await postChatStream({
    messages: [judgeSystem(), judgeUser(SCOPED_CASES), toolResult(JUDGE_GRAPH)],
  });
  assert.equal(status, 200);
  assert.ok(contentType.includes("text/event-stream"));
  const frames = text
    .split("\n")
    .filter((line) => line.startsWith("data: "))
    .map((line) => line.slice("data: ".length));
  assert.equal(frames[frames.length - 1], "[DONE]");
  const chunks = frames.slice(0, -1).map((frame) => JSON.parse(frame));
  // Reassemble the single tool call from its indexed argument fragments.
  let name = "";
  let argsText = "";
  let sawTerminal = false;
  for (const chunk of chunks) {
    const choice = chunk.choices[0];
    const delta = choice.delta || {};
    if (Array.isArray(delta.tool_calls)) {
      for (const call of delta.tool_calls) {
        assert.equal(call.index, 0);
        if (call.function && typeof call.function.name === "string") name = call.function.name;
        if (call.function && typeof call.function.arguments === "string") argsText += call.function.arguments;
      }
    }
    if (choice.finish_reason === "tool_calls") {
      sawTerminal = true;
      assert.ok(chunk.usage && chunk.usage.total_tokens > 0, "terminal chunk carries usage");
    }
  }
  assert.equal(sawTerminal, true);
  assert.equal(name, "design.judgment_submit");
  assertJudgmentContract(JSON.parse(argsText), "3001", SCOPED_CASES);
});

test("judge never emits case writes, review submissions or confirmations", async () => {
  const forbidden = ["case.graph_propose", "design.review_submit", "case.confirm", "design.data_plan_submit"];
  for (const mode of ["pass", "fail", "minor"]) {
    await setMode("/__judge-mode", mode);
    const turns = [
      [judgeSystem(), judgeUser(SCOPED_CASES)],
      [judgeSystem(), judgeUser(SCOPED_CASES), toolResult(JUDGE_GRAPH)],
      [judgeSystem(), judgeUser(SCOPED_CASES), toolResult(JUDGE_GRAPH), toolResult(SUBMIT_RESULT)],
    ];
    for (const messages of turns) {
      const { json } = await postChat({ messages });
      const message = json.choices[0].message;
      for (const call of message.tool_calls || []) {
        assert.equal(forbidden.includes(call.function.name), false, "judge emitted forbidden tool " + call.function.name);
      }
    }
  }
  await setMode("/__judge-mode", "pass");
});

// --- author dispatcher (design_author marker + rework + coverage modes) --------

test("design_author reads graph then requirement.read then proposes the full AC set", async () => {
  await setMode("/__author-mode", "full");
  const first = await postChat({ messages: [AUTHOR_SYSTEM, authorUser("design.requirement")] });
  assert.equal(assistantToolCall(first.json.choices[0].message).name, "design.graph_read");

  const second = await postChat({
    messages: [AUTHOR_SYSTEM, authorUser("design.requirement"), toolResult(AUTHOR_GRAPH_EMPTY)],
  });
  const readCall = assistantToolCall(second.json.choices[0].message);
  assert.equal(readCall.name, "requirement.read");
  assert.equal(readCall.args.requirement_id, "3001");

  const third = await postChat({
    messages: [AUTHOR_SYSTEM, authorUser("design.requirement"), toolResult(AUTHOR_GRAPH_EMPTY), toolResult(AUTHOR_REQUIREMENT_READ)],
  });
  const propose = assistantToolCall(third.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_id, "3001");
  assert.equal(propose.args.requirement_version_id, "3002");
  assert.ok(Array.isArray(propose.args.cases) && propose.args.cases.length >= 1);
  for (const item of propose.args.cases) {
    assert.deepEqual([...item.covered_acs].sort(), ["AC-1", "AC-2"], "full mode covers the whole AC set");
    assert.equal(typeof item.sut_description, "string");
    assert.ok(item.sut_description.length >= 1);
    assert.equal(typeof item.preconditions, "string");
    assert.ok(item.preconditions.length >= 1);
    for (const field of ["default_version_sut_id", "version_sut_ids", "page_ids", "api_ids"]) {
      assert.equal(Object.prototype.hasOwnProperty.call(item, field), false, "deleted field " + field);
    }
  }
});

test("design_author partial mode covers only the first AC (coverage-gap seed)", async () => {
  await setMode("/__author-mode", "partial");
  const { json } = await postChat({
    messages: [AUTHOR_SYSTEM, authorUser("design.requirement"), toolResult(AUTHOR_GRAPH_EMPTY), toolResult(AUTHOR_REQUIREMENT_READ)],
  });
  const propose = assistantToolCall(json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  for (const item of propose.args.cases) {
    assert.deepEqual(item.covered_acs, ["AC-1"], "partial mode covers only the first AC");
  }
  await setMode("/__author-mode", "full");
});

test("design_author rework revises the scoped case with changed content", async () => {
  await setMode("/__author-mode", "full");
  const reworkScope = ["- case_id=7001", "- case_version_id=8001"];
  const graphWithDraft = {
    ...JUDGE_GRAPH,
    requirements: JUDGE_GRAPH.requirements.map((requirement) => ({
      ...requirement,
      scenarios: requirement.scenarios.map((scenario) => ({
        ...scenario,
        cases: scenario.cases.slice(0, 1),
      })),
    })),
  };
  const { json } = await postChat({
    messages: [
      AUTHOR_SYSTEM,
      authorUser("design.rework", reworkScope),
      toolResult(graphWithDraft),
      toolResult(AUTHOR_REQUIREMENT_READ),
    ],
  });
  const propose = assistantToolCall(json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.cases.length, 1, "rework revises exactly the scoped case");
  const revised = propose.args.cases[0];
  assert.equal(revised.id, "7001", "rework updates the existing logical case");
  assert.ok(revised.steps.length >= 2, "rework adds executable detail");
  assert.ok(revised.assertions.length >= 1);
  assert.deepEqual([...revised.covered_acs].sort(), ["AC-1", "AC-2"]);
  for (const field of ["default_version_sut_id", "version_sut_ids", "page_ids", "api_ids"]) {
    assert.equal(Object.prototype.hasOwnProperty.call(revised, field), false, "deleted field " + field);
  }
});

test("design_author finishes with text after the propose result", async () => {
  const { json } = await postChat({
    messages: [AUTHOR_SYSTEM, authorUser("design.requirement"), toolResult(AUTHOR_GRAPH_EMPTY), toolResult(AUTHOR_REQUIREMENT_READ), toolResult(PROPOSE_RESULT)],
  });
  const message = json.choices[0].message;
  assert.equal(message.tool_calls, undefined);
  assert.ok(typeof message.content === "string" && message.content.length > 0);
});

test("design_author accepts the REAL requirement.read envelope ({requirement:{...}})", async () => {
  // tools/requirements.go requirementReadResult wraps the detail under
  // "requirement"; the author must draw covered_acs from that nested ac_ids,
  // not loop on requirement.read (the pre-T19 flat-only reader did).
  await setMode("/__author-mode", "full");
  const nested = { requirement: { ...AUTHOR_REQUIREMENT_READ, id: AUTHOR_REQUIREMENT_READ.requirement_id } };
  const { json } = await postChat({
    messages: [AUTHOR_SYSTEM, authorUser("design.requirement"), toolResult(AUTHOR_GRAPH_EMPTY), toolResult(nested)],
  });
  const propose = assistantToolCall(json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  for (const item of propose.args.cases) {
    assert.deepEqual([...item.covered_acs].sort(), ["AC-1", "AC-2"]);
    assert.equal(item.sut_description, AUTHOR_REQUIREMENT_READ.sut_description);
  }
});

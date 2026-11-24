#!/usr/bin/env node
"use strict";

// P2 contract tests for the design profiles in scripts/fake-llm.cjs. The
// server is spawned on an ephemeral loopback port so the suite never depends on
// a fixed port.
//
// Expectations are derived from the v0.2.4-B design contract (the agent tool
// validator in backend/internal/agent/application/tools/design.go and the task
// prompts in runner.go), NOT from the double's observed output:
//   * case_author / design_host read design.graph_read, then requirement.read
//     (the AC-id set is NOT in graph_read), then case.graph_propose.
//   * A proposed case carries the natural-language contract: sut_description,
//     preconditions, covered_acs (a non-empty subset of the requirement AC set),
//     executable steps with prose target_description, and requirement sources
//     keyed by requirement_version_id.
//   * The deleted resource-id fields (default_version_sut_id / version_sut_ids /
//     page_ids / api_ids on cases/scenarios, target_*_id on steps, database_id on
//     assertions) must NEVER appear: the decoder hard-rejects them.
//
// Run: node --test scripts/fake-llm-p2.test.cjs

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

async function postChat(body) {
  const response = await fetch(baseUrl + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const json = await response.json();
  return { status: response.status, json };
}

// postChatStream reads the raw SSE body (not JSON) so a stream:true request can
// be asserted against the OpenAI chat-completions streaming contract.
async function postChatStream(body) {
  const response = await fetch(baseUrl + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const text = await response.text();
  return {
    status: response.status,
    contentType: response.headers.get("content-type") || "",
    text,
  };
}

// parseSseFrames returns the ordered data payloads of an SSE body. Each event is
// a single "data: <payload>" line (JSON.stringify never emits newlines), so the
// payloads are recovered by prefix, preserving order incl. the [DONE] sentinel.
function parseSseFrames(text) {
  const frames = [];
  for (const line of text.split("\n")) {
    if (line.startsWith("data: ")) frames.push(line.slice("data: ".length));
  }
  return frames;
}

// toolCallFragments collects the streamed tool-call deltas in emission order.
function toolCallFragments(chunks) {
  const fragments = [];
  for (const chunk of chunks) {
    const delta = chunk.choices[0].delta;
    if (Array.isArray(delta.tool_calls)) {
      for (const call of delta.tool_calls) fragments.push(call);
    }
  }
  return fragments;
}

function systemMarked(profile, extra) {
  return { role: "system", content: "[profile:" + profile + "] 角色提示词" + (extra || "") };
}

function taskScope(requirementID) {
  return "\n# 任务\n作用域:\n- version_id=1001\n- requirement_id=" + requirementID;
}

function userText(text) {
  return { role: "user", content: text };
}

function toolResult(payload) {
  return { role: "tool", content: JSON.stringify(payload) };
}

function assistantToolCall(message) {
  assert.equal(message.role, "assistant");
  assert.equal(Array.isArray(message.tool_calls), true, "expected a tool call, got text: " + message.content);
  assert.equal(message.tool_calls.length, 1);
  const call = message.tool_calls[0];
  assert.equal(call.type, "function");
  assert.equal(typeof call.function.name, "string");
  const args = JSON.parse(call.function.arguments);
  return { name: call.function.name, args };
}

// --- contract assertion helpers (expectations from design.go, not the double) --

const AC_ID_PATTERN = /^AC-[1-9][0-9]*$/;
const DELETED_CASE_FIELDS = ["default_version_sut_id", "version_sut_ids", "page_ids", "api_ids"];
const DELETED_SCENARIO_FIELDS = ["version_sut_ids", "page_ids", "api_ids"];
const DELETED_STEP_FIELDS = ["target_page_id", "target_api_id", "target_version_sut_id"];
const DELETED_ASSERTION_FIELDS = ["target_version_sut_id", "target_api_id", "database_id"];

function assertNoFields(obj, fields, label) {
  for (const field of fields) {
    assert.equal(
      Object.prototype.hasOwnProperty.call(obj, field),
      false,
      label + " carries deleted field " + field + ": " + JSON.stringify(obj),
    );
  }
}

// assertScenarioContract pins the scenario schema: ref/name required, optional
// description/sort_order, and none of the deleted resource-id fields.
function assertScenarioContract(scenario) {
  assertNoFields(scenario, DELETED_SCENARIO_FIELDS, "scenario");
  assert.equal(typeof scenario.ref, "string");
  assert.ok(scenario.ref.length >= 1);
  assert.equal(typeof scenario.name, "string");
  assert.ok(scenario.name.length >= 1);
}

// assertCaseContract pins the v0.2.4-B natural-language case contract from the
// design validator (design.go caseSchema): required sut_description/preconditions/
// covered_acs/steps, prose step targets, valid assertion kinds, requirement
// sources keyed by requirement_version_id, and the absence of every deleted
// resource-id field. coveredACs is the requirement AC set (requirement.read.ac_ids),
// the contract source covered_acs must be drawn from.
function assertCaseContract(item, coveredACs, requirementVersionID) {
  assertNoFields(item, DELETED_CASE_FIELDS, "case");
  // sut_description: required prose, 1..2000 chars (design.go maxSutDescriptionChars).
  assert.equal(typeof item.sut_description, "string");
  assert.ok(
    item.sut_description.length >= 1 && item.sut_description.length <= 2000,
    "sut_description must be 1..2000 chars: " + JSON.stringify(item.sut_description),
  );
  // preconditions: required, non-empty (the contract requires "无" when none).
  assert.equal(typeof item.preconditions, "string");
  assert.ok(item.preconditions.length >= 1, "preconditions must be non-empty");
  // covered_acs: required non-empty subset of the requirement AC set.
  assert.ok(
    Array.isArray(item.covered_acs) && item.covered_acs.length >= 1,
    "covered_acs must be a non-empty array",
  );
  for (const ac of item.covered_acs) {
    assert.match(ac, AC_ID_PATTERN, "covered_acs id must be AC-<n>: " + ac);
    assert.ok(
      coveredACs.includes(ac),
      "covered_acs id " + ac + " is not in the requirement AC set " + JSON.stringify(coveredACs),
    );
  }
  // steps: required, each with seq/action/expected and a prose target_description.
  assert.ok(Array.isArray(item.steps) && item.steps.length >= 1);
  for (const step of item.steps) {
    assert.equal(typeof step.seq, "number");
    assert.ok(typeof step.action === "string" && step.action.length >= 1);
    assert.ok(typeof step.expected === "string" && step.expected.length >= 1);
    assert.ok(
      typeof step.target_description === "string" && step.target_description.length >= 1,
      "step must carry a prose target_description",
    );
    assertNoFields(step, DELETED_STEP_FIELDS, "step");
  }
  // assertions: each with step_seq/kind/target/expected; kind=db requires sut_id.
  assert.ok(Array.isArray(item.assertions));
  for (const assertion of item.assertions) {
    assert.equal(typeof assertion.step_seq, "number");
    assert.ok(["ui", "api", "db", "text"].includes(assertion.kind), "bad assertion kind: " + assertion.kind);
    assert.ok(typeof assertion.target === "string" && assertion.target.length >= 1);
    assert.ok(typeof assertion.expected === "string" && assertion.expected.length >= 1);
    if (assertion.kind === "db") {
      assert.ok(
        typeof assertion.sut_id === "string" && assertion.sut_id.length >= 1,
        "db assertion must carry sut_id",
      );
    }
    assertNoFields(assertion, DELETED_ASSERTION_FIELDS, "assertion");
  }
  // sources: requirement source keyed by requirement_version_id (not requirement_id).
  assert.ok(Array.isArray(item.sources) && item.sources.length >= 1);
  assert.equal(item.sources[0].kind, "requirement");
  assert.equal(item.sources[0].ref_id, requirementVersionID);
}

// --- graph_read fixtures (v0.2.4-B shape: no target_version_sut_ids; case cards
// carry sut_description/covered_acs, not resource ids) -------------------------

const GRAPH = {
  version_id: "1001",
  graph_hash: "a".repeat(64),
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
              current_version_id: "7002",
              revision: 1,
              title: "账号密码登录成功",
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

// REQUIREMENT_READ is the requirement.read result: it carries the AC-id set
// (ac_ids) that graph_read does not, plus the requirement's prose sut_description.
const REQUIREMENT_READ = {
  requirement_id: "3001",
  requirement_version_id: "3002",
  revision: 1,
  status: "clarified",
  title: "登录",
  sut_description: "登录页与登录接口的被测系统描述。",
  acceptance_criteria: [
    { id: "AC-1", given: "已注册账号", when: "用户执行登录", then: "登录成功" },
  ],
  ac_ids: ["AC-1"],
};

const GRAPH_EMPTY = {
  version_id: "1001",
  graph_hash: "b".repeat(64),
  requirements: [
    {
      requirement_id: "3001",
      requirement_version_id: "3002",
      title: "登录",
      status: "clarified",
      scenarios: [],
    },
  ],
};

// GRAPH_MIXED puts a non-clarified (rolled-back/draft) requirement first so the
// author must use the injected requirement scope rather than the first row.
const GRAPH_MIXED = {
  version_id: "1001",
  graph_hash: "e".repeat(64),
  requirements: [
    {
      requirement_id: "2001",
      requirement_version_id: "2002",
      revision: 2,
      title: "被回退的需求",
      status: "draft",
      scenarios: [],
    },
    {
      requirement_id: "3001",
      requirement_version_id: "3002",
      revision: 1,
      title: "登录",
      status: "clarified",
      scenarios: [],
    },
  ],
};

// GRAPH_REDESIGN models a requirement that changed version: the old scenario
// and its stale case belong to the previous requirement_version (v1).
const GRAPH_REDESIGN = {
  version_id: "1001",
  graph_hash: "f".repeat(64),
  requirements: [
    {
      requirement_id: "3001",
      requirement_version_id: "3002",
      revision: 2,
      title: "登录（新版）",
      status: "clarified",
      scenarios: [
        {
          id: "5001",
          requirement_version_id: "2902",
          name: "旧版正常登录",
          cases: [
            {
              case_id: "6001",
              current_version_id: "6002",
              revision: 1,
              title: "旧版用例",
              status: "stale",
              sut_description: "旧版登录页。",
              covered_acs: ["AC-1"],
            },
          ],
        },
      ],
    },
  ],
};

// GRAPH_MIXED_VERSIONS has an old-version scenario first and the current-version
// scenario second, so case selection must match requirement_version_id, not
// scenario order.
const GRAPH_MIXED_VERSIONS = {
  version_id: "1001",
  graph_hash: "9".repeat(64),
  requirements: [
    {
      requirement_id: "3001",
      requirement_version_id: "3002",
      revision: 2,
      title: "登录（新版）",
      status: "clarified",
      scenarios: [
        {
          id: "5001",
          requirement_version_id: "2902",
          name: "旧版登录",
          cases: [
            {
              case_id: "6001",
              current_version_id: "6002",
              revision: 1,
              title: "旧版用例",
              status: "stale",
              sut_description: "旧版登录页。",
              covered_acs: ["AC-1"],
            },
          ],
        },
        {
          id: "5101",
          requirement_version_id: "3002",
          name: "新版登录",
          cases: [
            {
              case_id: "7001",
              current_version_id: "7002",
              revision: 1,
              title: "新版用例",
              status: "draft",
              sut_description: "新版登录页。",
              covered_acs: ["AC-1"],
            },
          ],
        },
      ],
    },
  ],
};

// GRAPH_CONFIRMED_FIRST has a confirmed current case before a draft one, so the
// data planner must skip the confirmed case and target the draft revision.
const GRAPH_CONFIRMED_FIRST = {
  version_id: "1001",
  graph_hash: "8".repeat(64),
  requirements: [
    {
      requirement_id: "3001",
      requirement_version_id: "3002",
      revision: 1,
      title: "登录",
      status: "clarified",
      scenarios: [
        {
          id: "5101",
          requirement_version_id: "3002",
          name: "正常登录",
          cases: [
            {
              case_id: "7101",
              current_version_id: "7102",
              revision: 1,
              title: "已确认用例",
              status: "confirmed",
              sut_description: "登录页。",
              covered_acs: ["AC-1"],
            },
            {
              case_id: "7201",
              current_version_id: "7202",
              revision: 1,
              title: "草稿用例",
              status: "draft",
              sut_description: "登录页。",
              covered_acs: ["AC-1"],
            },
          ],
        },
      ],
    },
  ],
};

const GRAPH_ALL_CONFIRMED = {
  version_id: "1001",
  graph_hash: "7".repeat(64),
  requirements: [
    {
      requirement_id: "3001",
      requirement_version_id: "3002",
      title: "登录",
      status: "clarified",
      scenarios: [
        {
          id: "5101",
          requirement_version_id: "3002",
          name: "正常登录",
          cases: [
            {
              case_id: "7101",
              current_version_id: "7102",
              revision: 1,
              title: "已确认用例",
              status: "confirmed",
              sut_description: "登录页。",
              covered_acs: ["AC-1"],
            },
          ],
        },
      ],
    },
  ],
};

before(startFake);
after(() => {
  if (child && !child.killed) child.kill();
});

test("P0 echo and P1 clarification host behaviour are unchanged", async () => {
  const echo = await postChat({ model: "fake-llm", messages: [userText("你好")] });
  assert.equal(echo.status, 200);
  assert.equal(echo.json.choices[0].message.content, "EzTest fake LLM: 你好");

  const host = await postChat({
    model: "fake-llm",
    messages: [systemMarked("clarification_host"), userText("我想做一个登录功能")],
  });
  assert.equal(host.json.choices[0].message.tool_calls, undefined);
  assert.ok(host.json.choices[0].message.content.includes("我想做一个登录功能"));
});

test("design_host answers design chat without tools or confirmation", async () => {
  const { status, json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("当前脑图覆盖了哪些场景？")],
  });
  assert.equal(status, 200);
  const message = json.choices[0].message;
  assert.equal(message.role, "assistant");
  assert.equal(message.tool_calls, undefined);
  assert.ok(message.content.length > 0);
  assert.equal(message.content.startsWith("EzTest fake LLM:"), false, "design host fell back to P0 echo");
  assert.ok(message.content.includes("设计"), message.content);
  assert.ok(!/confirm/i.test(message.content));
});

test("design_host reads graph, reads requirement ACs, then proposes a draft when asked to modify", async () => {
  const first = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("请新增一条异常登录用例")],
  });
  const read = assistantToolCall(first.json.choices[0].message);
  assert.equal(read.name, "design.graph_read");
  assert.deepEqual(read.args, {});

  // graph_read does not carry the requirement AC set, so the next read is
  // requirement.read for the scoped requirement (design v1.3.1 §5.2).
  const second = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("请新增一条异常登录用例"), toolResult(GRAPH)],
  });
  const reqRead = assistantToolCall(second.json.choices[0].message);
  assert.equal(reqRead.name, "requirement.read");
  assert.equal(reqRead.args.requirement_id, "3001");

  const third = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("design_host"),
      userText("请新增一条异常登录用例"),
      toolResult(GRAPH),
      toolResult(REQUIREMENT_READ),
    ],
  });
  const propose = assistantToolCall(third.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_id, "3001");
  assert.equal(propose.args.requirement_version_id, "3002");
  assert.equal(propose.args.scenarios.length, 1);
  assert.equal(propose.args.scenarios[0].id, "5001");
  assertScenarioContract(propose.args.scenarios[0]);
  assert.equal(propose.args.cases.length, 1);
  assert.equal(propose.args.cases[0].id, "7001");
  assert.equal(propose.args.cases[0].ref, "c1");
  assertCaseContract(propose.args.cases[0], REQUIREMENT_READ.ac_ids, "3002");

  const fourth = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("design_host"),
      userText("请新增一条异常登录用例"),
      toolResult({ graph_hash: "c".repeat(64), scenario_ids: ["5001"], case_ids: ["7001"], case_version_ids: ["7002"] }),
    ],
  });
  assert.equal(fourth.json.choices[0].message.tool_calls, undefined);
  assert.ok(fourth.json.choices[0].message.content.length > 0);
});

test("design_host creates a scenario and case when the graph has none", async () => {
  // graph_read -> requirement.read -> propose, even with an empty graph.
  const readStep = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("新增用例"), toolResult(GRAPH_EMPTY)],
  });
  const reqRead = assistantToolCall(readStep.json.choices[0].message);
  assert.equal(reqRead.name, "requirement.read");
  assert.equal(reqRead.args.requirement_id, "3001");

  const { json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("新增用例"), toolResult(GRAPH_EMPTY), toolResult(REQUIREMENT_READ)],
  });
  const propose = assistantToolCall(json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.scenarios[0].id, undefined);
  assert.equal(propose.args.cases[0].id, undefined);
  assert.ok(propose.args.cases[0].steps.length >= 1);
  assertScenarioContract(propose.args.scenarios[0]);
  assertCaseContract(propose.args.cases[0], REQUIREMENT_READ.ac_ids, "3002");
});

test("design_host does not propose on the opening turn or without a modify request", async () => {
  const opening = "这是设计会话的开场。请向用户简要说明当前设计范围，并提示用户可以在对话中要求新增或修改用例。";
  const opened = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText(opening), toolResult(GRAPH)],
  });
  const openingMessage = opened.json.choices[0].message;
  assert.equal(openingMessage.tool_calls, undefined, "opening turn proposed a case");
  assert.ok(openingMessage.content.length > 0);
  assert.equal(openingMessage.content.startsWith("EzTest fake LLM:"), false);

  // A graph result with no user request at all must stay a greeting.
  const noUser = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), toolResult(GRAPH)],
  });
  assert.equal(noUser.json.choices[0].message.tool_calls, undefined, "graph-only turn proposed a case");
  assert.ok(noUser.json.choices[0].message.content.length > 0);

  // A plain question must not trigger a write either.
  const question = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("当前脑图覆盖了哪些场景？"), toolResult(GRAPH)],
  });
  assert.equal(question.json.choices[0].message.tool_calls, undefined, "question turn proposed a case");
});

test("design_host targets the first clarified requirement when a draft row is first", async () => {
  const readStep = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("新增用例"), toolResult(GRAPH_MIXED)],
  });
  const reqRead = assistantToolCall(readStep.json.choices[0].message);
  assert.equal(reqRead.name, "requirement.read");
  assert.equal(reqRead.args.requirement_id, "3001");

  const { json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("新增用例"), toolResult(GRAPH_MIXED), toolResult(REQUIREMENT_READ)],
  });
  const propose = assistantToolCall(json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_id, "3001");
  assert.equal(propose.args.requirement_version_id, "3002");
});

test("design_planner returns a scope-only text without tools", async () => {
  const { json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_planner"), userText("请说明本轮设计范围")],
  });
  const message = json.choices[0].message;
  assert.equal(message.tool_calls, undefined);
  assert.equal(message.content.startsWith("EzTest fake LLM:"), false, "design planner fell back to P0 echo");
  assert.ok(message.content.includes("范围"), message.content);
});

test("case_author reads graph, reads requirement ACs, then proposes one scenario and two cases", async () => {
  const first = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author"), userText("设计需求 3001")],
  });
  const read = assistantToolCall(first.json.choices[0].message);
  assert.equal(read.name, "design.graph_read");

  const second = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author"), userText("设计需求 3001"), toolResult(GRAPH)],
  });
  const reqRead = assistantToolCall(second.json.choices[0].message);
  assert.equal(reqRead.name, "requirement.read");
  assert.equal(reqRead.args.requirement_id, "3001");

  const third = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author"), userText("设计需求 3001"), toolResult(GRAPH), toolResult(REQUIREMENT_READ)],
  });
  const propose = assistantToolCall(third.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_id, "3001");
  assert.equal(propose.args.requirement_version_id, "3002");
  assert.equal(propose.args.scenarios.length, 1);
  assert.equal(propose.args.cases.length, 2);
  // The design tool validator dedupes by ref: every proposed case (and the
  // single scenario) must carry a unique ref, and each case must resolve to
  // the scenario ref.
  const caseRefs = propose.args.cases.map((item) => item.ref);
  assert.equal(new Set(caseRefs).size, caseRefs.length, "duplicate case refs: " + JSON.stringify(caseRefs));
  assert.deepEqual(caseRefs, ["c1", "c2"]);
  const scenarioRefs = propose.args.scenarios.map((item) => item.ref);
  assert.equal(new Set(scenarioRefs).size, scenarioRefs.length, "duplicate scenario refs: " + JSON.stringify(scenarioRefs));
  assertScenarioContract(propose.args.scenarios[0]);
  for (const item of propose.args.cases) {
    assert.ok(item.title.length > 0);
    assert.ok(scenarioRefs.includes(item.scenario_ref), "unknown scenario_ref: " + item.scenario_ref);
    // The natural-language case contract, derived from design.go (not the double):
    // covered_acs drawn from the requirement AC set, prose sut_description,
    // preconditions, executable steps with prose targets, and no deleted id fields.
    assertCaseContract(item, REQUIREMENT_READ.ac_ids, "3002");
    assert.deepEqual(item.covered_acs, REQUIREMENT_READ.ac_ids);
  }

  const fourth = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("case_author"),
      userText("设计需求 3001"),
      toolResult({ graph_hash: "d".repeat(64), case_ids: ["7001", "7003"], case_version_ids: ["7002", "7004"] }),
    ],
  });
  assert.equal(fourth.json.choices[0].message.tool_calls, undefined);
  assert.ok(fourth.json.choices[0].message.content.length > 0);
});

test("case_author selects the scoped clarified requirement", async () => {
  const scoped = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author", taskScope("3001")), userText("设计需求 3001"), toolResult(GRAPH_MIXED)],
  });
  const scopedRead = assistantToolCall(scoped.json.choices[0].message);
  assert.equal(scopedRead.name, "requirement.read");
  assert.equal(scopedRead.args.requirement_id, "3001");

  const scopedPropose = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("case_author", taskScope("3001")),
      userText("设计需求 3001"),
      toolResult(GRAPH_MIXED),
      toolResult(REQUIREMENT_READ),
    ],
  });
  const propose = assistantToolCall(scopedPropose.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_id, "3001");
  assert.equal(propose.args.requirement_version_id, "3002");

  // Without a scope marker the author falls back to the first clarified
  // requirement, never the draft/rolled-back row.
  const fallback = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author"), userText("设计需求"), toolResult(GRAPH_MIXED)],
  });
  const fallbackRead = assistantToolCall(fallback.json.choices[0].message);
  assert.equal(fallbackRead.name, "requirement.read");
  assert.equal(fallbackRead.args.requirement_id, "3001");
});

test("case_author on a new requirement version ignores old-version scenarios", async () => {
  const readStep = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author", taskScope("3001")), userText("重新设计需求 3001"), toolResult(GRAPH_REDESIGN)],
  });
  const reqRead = assistantToolCall(readStep.json.choices[0].message);
  assert.equal(reqRead.name, "requirement.read");
  assert.equal(reqRead.args.requirement_id, "3001");

  const response = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("case_author", taskScope("3001")),
      userText("重新设计需求 3001"),
      toolResult(GRAPH_REDESIGN),
      toolResult(REQUIREMENT_READ),
    ],
  });
  const propose = assistantToolCall(response.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_id, "3001");
  assert.equal(propose.args.requirement_version_id, "3002");
  // The only scenario in the graph belongs to the previous requirement_version
  // (2902), so redesign must create a new scenario/case instead of reusing it.
  assert.equal(propose.args.scenarios.length, 1);
  assert.equal(propose.args.scenarios[0].id, undefined);
  assert.equal(propose.args.cases.length, 2);
  for (const item of propose.args.cases) {
    assert.equal(item.id, undefined);
    assert.equal(item.scenario_ref, "s1");
    assertCaseContract(item, REQUIREMENT_READ.ac_ids, "3002");
  }
});

test("design_host updates only cases under the requirement's current version", async () => {
  const readStep = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("修改用例"), toolResult(GRAPH_MIXED_VERSIONS)],
  });
  const reqRead = assistantToolCall(readStep.json.choices[0].message);
  assert.equal(reqRead.name, "requirement.read");
  assert.equal(reqRead.args.requirement_id, "3001");

  const response = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("design_host"),
      userText("修改用例"),
      toolResult(GRAPH_MIXED_VERSIONS),
      toolResult(REQUIREMENT_READ),
    ],
  });
  const propose = assistantToolCall(response.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_version_id, "3002");
  assert.equal(propose.args.scenarios.length, 1);
  assert.equal(propose.args.scenarios[0].id, "5101");
  assert.equal(propose.args.cases[0].id, "7001");
  assertCaseContract(propose.args.cases[0], REQUIREMENT_READ.ac_ids, "3002");
});

test("design_critic submits one minor finding before the final text", async () => {
  const first = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_critic"), userText("请自审需求 3001")],
  });
  const review = assistantToolCall(first.json.choices[0].message);
  assert.equal(review.name, "design.review_submit");
  assert.equal(review.args.findings.length, 1);
  assert.equal(review.args.findings[0].severity, "minor");
  assert.ok(review.args.findings[0].message.length > 0);

  const second = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("design_critic"),
      userText("请自审需求 3001"),
      toolResult({ posted: true, count: 1 }),
    ],
  });
  assert.equal(second.json.choices[0].message.tool_calls, undefined);
});

test("data_planner reads the graph then submits data for the first draft case", async () => {
  const first = await postChat({
    model: "fake-llm",
    messages: [systemMarked("data_planner"), userText("补充需求 3001 的测试数据")],
  });
  const read = assistantToolCall(first.json.choices[0].message);
  assert.equal(read.name, "design.graph_read");

  const second = await postChat({
    model: "fake-llm",
    messages: [systemMarked("data_planner"), userText("补充需求 3001 的测试数据"), toolResult(GRAPH)],
  });
  const submit = assistantToolCall(second.json.choices[0].message);
  assert.equal(submit.name, "design.data_plan_submit");
  assert.equal(submit.args.case_version_id, "7002");
  assert.ok(typeof submit.args.preconditions === "string" && submit.args.preconditions.length > 0);
  assert.equal(typeof submit.args.test_data, "object");

  const third = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("data_planner"),
      userText("补充需求 3001 的测试数据"),
      toolResult({ case_id: "7001", case_version_id: "7005", revision: 2 }),
    ],
  });
  assert.equal(third.json.choices[0].message.tool_calls, undefined);
});

test("data_planner selects only a current draft case", async () => {
  const draft = await postChat({
    model: "fake-llm",
    messages: [systemMarked("data_planner"), userText("补充需求 3001 的测试数据"), toolResult(GRAPH_CONFIRMED_FIRST)],
  });
  const submit = assistantToolCall(draft.json.choices[0].message);
  assert.equal(submit.name, "design.data_plan_submit");
  assert.equal(submit.args.case_version_id, "7202", "data planner targeted the confirmed case instead of the draft");

  const confirmedOnly = await postChat({
    model: "fake-llm",
    messages: [systemMarked("data_planner"), userText("补充需求 3001 的测试数据"), toolResult(GRAPH_ALL_CONFIRMED)],
  });
  const message = confirmedOnly.json.choices[0].message;
  assert.equal(message.tool_calls, undefined, "data planner targeted a confirmed-only graph");
  assert.ok(message.content.length > 0);
});

test("no design profile ever emits case.confirm or granular design write tools", async () => {
  const bodies = [
    { messages: [systemMarked("design_host"), userText("请修改用例")] },
    { messages: [systemMarked("design_host"), userText("请修改用例"), toolResult(GRAPH)] },
    { messages: [systemMarked("design_host"), userText("请修改用例"), toolResult(GRAPH), toolResult(REQUIREMENT_READ)] },
    { messages: [systemMarked("case_author"), userText("设计需求 3001"), toolResult(GRAPH)] },
    { messages: [systemMarked("case_author"), userText("设计需求 3001"), toolResult(GRAPH), toolResult(REQUIREMENT_READ)] },
    { messages: [systemMarked("design_critic"), userText("自审")] },
    { messages: [systemMarked("data_planner"), userText("数据")] },
  ];
  const forbidden = new Set([
    "case.confirm",
    "design.scenario_propose",
    "design.case_propose",
    "design.case_update",
  ]);
  for (const body of bodies) {
    const { json } = await postChat({ model: "fake-llm", ...body });
    const message = json.choices[0].message;
    for (const call of message.tool_calls || []) {
      assert.equal(forbidden.has(call.function.name), false, "forbidden tool call: " + call.function.name);
    }
  }
});

// --- stream:true tool-call contract (v0.2.4-D) -------------------------------
// Expectations are derived from the OpenAI chat-completions streaming contract
// (incremental tool_calls deltas: first fragment carries id/type/function.name,
// later fragments carry only function.arguments at the same index; a terminal
// finish_reason:"tool_calls" chunk; usage when stream_options.include_usage is
// set; a trailing [DONE] sentinel), NOT from the double's observed output. The
// tool name and propose args come from the design contract (design.go), the
// same source the non-stream case_author test above pins.

test("case_author tool call streams as OpenAI SSE fragments when stream:true", async () => {
  const { status, contentType, text } = await postChatStream({
    model: "fake-llm",
    stream: true,
    stream_options: { include_usage: true },
    messages: [
      systemMarked("case_author"),
      userText("设计需求 3001"),
      toolResult(GRAPH),
      toolResult(REQUIREMENT_READ),
    ],
  });
  assert.equal(status, 200);
  assert.match(contentType, /text\/event-stream/);

  const frames = parseSseFrames(text);
  // Contract: the stream terminates with the [DONE] sentinel.
  assert.equal(frames[frames.length - 1], "[DONE]");

  const chunks = frames.slice(0, -1).map((payload) => JSON.parse(payload));
  for (const chunk of chunks) {
    assert.equal(chunk.object, "chat.completion.chunk");
    assert.ok(chunk.choices.length >= 1);
    assert.equal(chunk.choices[0].index, 0);
  }

  const fragments = toolCallFragments(chunks);
  // The propose arguments are a long JSON string, so the contract's incremental
  // delta stream carries more than one argument fragment.
  assert.ok(fragments.length >= 2, "expected fragmented tool-call arguments, got " + fragments.length);

  // First fragment carries id/type/function.name at index 0.
  const first = fragments[0];
  assert.equal(first.index, 0);
  assert.equal(first.type, "function");
  assert.equal(typeof first.id, "string");
  assert.ok(first.id.length >= 1);
  assert.equal(first.function.name, "case.graph_propose");
  assert.equal(typeof first.function.arguments, "string");

  // Later fragments carry only function.arguments at the same index (no id/type/name).
  for (const frag of fragments.slice(1)) {
    assert.equal(frag.index, 0);
    assert.equal(frag.id, undefined);
    assert.equal(frag.type, undefined);
    assert.equal(frag.function.name, undefined);
    assert.equal(typeof frag.function.arguments, "string");
  }

  // Reassembled arguments are valid JSON matching the design propose contract.
  const args = JSON.parse(fragments.map((frag) => frag.function.arguments).join(""));
  assert.equal(args.requirement_id, "3001");
  assert.equal(args.requirement_version_id, "3002");
  assert.equal(args.scenarios.length, 1);
  assert.equal(args.cases.length, 2);

  // Terminal chunk carries finish_reason "tool_calls".
  const terminal = chunks[chunks.length - 1];
  assert.equal(terminal.choices[0].finish_reason, "tool_calls");

  // stream_options.include_usage: the TERMINAL chunk carries the usage snapshot
  // (OpenAI puts usage on the final chunk; the Go adapter reads it from the
  // trailing chunks). Pinning terminal.usage — not "any frame has usage" — is
  // what guards SC-D1 telemetry parity: a usage frame emitted mid-stream would
  // satisfy a loose `some` assertion while the terminal snapshot the runner
  // merges could be missing.
  assert.ok(terminal.usage, "expected usage on the terminal chunk for stream_options.include_usage");
  assert.equal(typeof terminal.usage.prompt_tokens, "number");
  assert.equal(typeof terminal.usage.completion_tokens, "number");
  assert.equal(typeof terminal.usage.total_tokens, "number");
});

test("case_author graph_read tool call streams a single-fragment SSE turn when stream:true", async () => {
  const { status, contentType, text } = await postChatStream({
    model: "fake-llm",
    stream: true,
    stream_options: { include_usage: true },
    messages: [systemMarked("case_author"), userText("设计需求 3001")],
  });
  assert.equal(status, 200);
  assert.match(contentType, /text\/event-stream/);

  const frames = parseSseFrames(text);
  assert.equal(frames[frames.length - 1], "[DONE]");
  const chunks = frames.slice(0, -1).map((payload) => JSON.parse(payload));

  const fragments = toolCallFragments(chunks);
  // graph_read has empty arguments ("{}"), so the first (and only) fragment
  // still carries id/type/function.name.
  assert.ok(fragments.length >= 1);
  const first = fragments[0];
  assert.equal(first.index, 0);
  assert.equal(first.type, "function");
  assert.equal(typeof first.id, "string");
  assert.equal(first.function.name, "design.graph_read");
  const args = JSON.parse(fragments.map((frag) => frag.function.arguments).join(""));
  assert.deepEqual(args, {});

  const terminal = chunks[chunks.length - 1];
  assert.equal(terminal.choices[0].finish_reason, "tool_calls");
  assert.ok(terminal.usage, "expected usage on the terminal chunk");
});

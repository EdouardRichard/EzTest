#!/usr/bin/env node
"use strict";

// P2 contract tests for the design profiles in scripts/fake-llm.cjs. The
// server is spawned on an ephemeral loopback port so the suite never depends on
// a fixed port.
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
      target_version_sut_ids: ["9001"],
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
              default_version_sut_id: "9001",
              version_sut_ids: ["9001"],
            },
          ],
        },
      ],
    },
  ],
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
      target_version_sut_ids: ["9001"],
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
      target_version_sut_ids: ["9001"],
      scenarios: [],
    },
    {
      requirement_id: "3001",
      requirement_version_id: "3002",
      revision: 1,
      title: "登录",
      status: "clarified",
      target_version_sut_ids: ["9001"],
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
      target_version_sut_ids: ["9001"],
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
              default_version_sut_id: "9001",
              version_sut_ids: ["9001"],
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
      target_version_sut_ids: ["9001"],
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
              default_version_sut_id: "9001",
              version_sut_ids: ["9001"],
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
              default_version_sut_id: "9001",
              version_sut_ids: ["9001"],
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
      target_version_sut_ids: ["9001"],
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
              default_version_sut_id: "9001",
              version_sut_ids: ["9001"],
            },
            {
              case_id: "7201",
              current_version_id: "7202",
              revision: 1,
              title: "草稿用例",
              status: "draft",
              default_version_sut_id: "9001",
              version_sut_ids: ["9001"],
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
      target_version_sut_ids: ["9001"],
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
              default_version_sut_id: "9001",
              version_sut_ids: ["9001"],
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

test("design_host reads the graph then proposes a draft when asked to modify", async () => {
  const first = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("请新增一条异常登录用例")],
  });
  const read = assistantToolCall(first.json.choices[0].message);
  assert.equal(read.name, "design.graph_read");
  assert.deepEqual(read.args, {});

  const second = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("design_host"),
      userText("请新增一条异常登录用例"),
      { role: "assistant", content: null, tool_calls: [{ id: "call-1", type: "function", function: { name: "design.graph_read", arguments: "{}" } }] },
      toolResult(GRAPH),
    ],
  });
  const propose = assistantToolCall(second.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_id, "3001");
  assert.equal(propose.args.requirement_version_id, "3002");
  assert.equal(propose.args.scenarios.length, 1);
  assert.equal(propose.args.scenarios[0].id, "5001");
  assert.equal(propose.args.cases.length, 1);
  assert.equal(propose.args.cases[0].id, "7001");
  assert.equal(propose.args.cases[0].ref, "c1");

  const third = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("design_host"),
      userText("请新增一条异常登录用例"),
      toolResult({ graph_hash: "c".repeat(64), scenario_ids: ["5001"], case_ids: ["7001"], case_version_ids: ["7002"] }),
    ],
  });
  assert.equal(third.json.choices[0].message.tool_calls, undefined);
  assert.ok(third.json.choices[0].message.content.length > 0);
});

test("design_host creates a scenario and case when the graph has none", async () => {
  const { json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("新增用例"), toolResult(GRAPH_EMPTY)],
  });
  const propose = assistantToolCall(json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.scenarios[0].id, undefined);
  assert.equal(propose.args.cases[0].id, undefined);
  assert.equal(propose.args.cases[0].steps.length >= 1, true);
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

test("design_host proposes against the first clarified requirement when a draft row is first", async () => {
  const { json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("新增用例"), toolResult(GRAPH_MIXED)],
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

test("case_author reads the graph then proposes one scenario and two cases", async () => {
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
  const propose = assistantToolCall(second.json.choices[0].message);
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
  for (const item of propose.args.cases) {
    assert.ok(item.title.length > 0);
    assert.ok(scenarioRefs.includes(item.scenario_ref), "unknown scenario_ref: " + item.scenario_ref);
    assert.equal(item.default_version_sut_id, "9001");
    assert.ok(Array.isArray(item.version_sut_ids) && item.version_sut_ids.length > 0);
    assert.ok(item.steps.length >= 1);
    assert.ok(item.assertions.length >= 1);
    assert.ok(item.sources.length >= 1);
    assert.equal(item.sources[0].kind, "requirement");
    assert.equal(item.sources[0].ref_id, "3002");
  }

  const third = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("case_author"),
      userText("设计需求 3001"),
      toolResult({ graph_hash: "d".repeat(64), case_ids: ["7001", "7003"], case_version_ids: ["7002", "7004"] }),
    ],
  });
  assert.equal(third.json.choices[0].message.tool_calls, undefined);
  assert.ok(third.json.choices[0].message.content.length > 0);
});

test("case_author selects the scoped clarified requirement", async () => {
  const scoped = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author", taskScope("3001")), userText("设计需求 3001"), toolResult(GRAPH_MIXED)],
  });
  const propose = assistantToolCall(scoped.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_id, "3001");
  assert.equal(propose.args.requirement_version_id, "3002");

  // Without a scope marker the author falls back to the first clarified
  // requirement, never the draft/rolled-back row.
  const fallback = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author"), userText("设计需求"), toolResult(GRAPH_MIXED)],
  });
  const fallbackProposal = assistantToolCall(fallback.json.choices[0].message);
  assert.equal(fallbackProposal.args.requirement_id, "3001");
  assert.equal(fallbackProposal.args.requirement_version_id, "3002");
});

test("case_author on a new requirement version ignores old-version scenarios", async () => {
  const response = await postChat({
    model: "fake-llm",
    messages: [systemMarked("case_author", taskScope("3001")), userText("重新设计需求 3001"), toolResult(GRAPH_REDESIGN)],
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
  }
});

test("design_host updates only cases under the requirement's current version", async () => {
  const response = await postChat({
    model: "fake-llm",
    messages: [systemMarked("design_host"), userText("修改用例"), toolResult(GRAPH_MIXED_VERSIONS)],
  });
  const propose = assistantToolCall(response.json.choices[0].message);
  assert.equal(propose.name, "case.graph_propose");
  assert.equal(propose.args.requirement_version_id, "3002");
  assert.equal(propose.args.scenarios.length, 1);
  assert.equal(propose.args.scenarios[0].id, "5101");
  assert.equal(propose.args.cases[0].id, "7001");
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
    { messages: [systemMarked("case_author"), userText("设计需求 3001"), toolResult(GRAPH)] },
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

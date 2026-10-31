#!/usr/bin/env node
"use strict";

// P1 contract tests for scripts/fake-llm.cjs. The server is spawned on an
// ephemeral loopback port so the suite never depends on a fixed port.
//
// Run: node --test scripts/fake-llm-p1.test.cjs

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

async function postChat(body, headers) {
  const response = await fetch(baseUrl + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json", ...(headers || {}) },
    body: JSON.stringify(body),
  });
  const json = await response.json();
  return { status: response.status, json };
}

function systemMarked(profile) {
  return { role: "system", content: "[profile:" + profile + "] 角色提示词" };
}

function userText(text) {
  return { role: "user", content: text };
}

before(startFake);
after(() => {
  if (child && !child.killed) child.kill();
});

test("P0 echo behaviour is unchanged and deterministic", async () => {
  const body = { model: "fake-llm", messages: [userText("你好")] };
  const first = await postChat(body);
  const second = await postChat(body);

  assert.equal(first.status, 200);
  assert.equal(first.json.model, "fake-llm");
  assert.equal(first.json.choices[0].message.role, "assistant");
  assert.equal(first.json.choices[0].message.content, "EzTest fake LLM: 你好");
  assert.equal(first.json.choices[0].finish_reason, "stop");
  assert.ok(first.json.usage.total_tokens > 0);
  assert.deepEqual(second.json, first.json);
});

test("P0 model catalog keeps context and output metadata", async () => {
  const response = await fetch(baseUrl + "/v1/models");
  const json = await response.json();
  assert.equal(response.status, 200);
  const byId = new Map(json.data.map((model) => [model.id, model]));
  assert.ok(byId.has("fake-llm"));
  assert.equal(byId.get("fake-llm").context_window, 64000);
  assert.equal(byId.get("fake-llm").max_output_tokens, 8192);
  assert.equal(byId.get("fake-llm-mini").context_window, 32000);
});

test("clarification_host replies referencing the latest user text without tools", async () => {
  const { status, json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("clarification_host"), userText("我想做一个登录功能")],
    tools: [
      {
        type: "function",
        function: { name: "context.version_pack", description: "pack", parameters: { type: "object" } },
      },
    ],
  });
  assert.equal(status, 200);
  const message = json.choices[0].message;
  assert.equal(message.role, "assistant");
  assert.ok(message.content.includes("我想做一个登录功能"), message.content);
  assert.equal(message.tool_calls, undefined);
  assert.equal(json.choices[0].finish_reason, "stop");
});

test("clarification_host opening returns a clean greeting instead of echoing the task", async () => {
  const { status, json } = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("clarification_host"),
      userText("这是澄清会话的开场。请向用户提一个简短的开场问题，引导其描述需求。"),
    ],
  });
  assert.equal(status, 200);
  const content = json.choices[0].message.content;
  assert.equal(content, "你好，我是澄清 Host。请描述需求或上传附件，我会整理成结构化草案并逐项澄清。");
  assert.ok(!content.includes("这是澄清会话的开场"), content);
  assert.ok(!content.includes("引导其描述需求"), content);
});

test("requirement_curator requests requirement.draft_create deterministically", async () => {
  const body = {
    model: "fake-llm",
    messages: [systemMarked("requirement_curator"), userText("登录功能需要支持手机号")],
    tools: [
      {
        type: "function",
        function: {
          name: "requirement.draft_create",
          description: "create",
          parameters: { type: "object", property: { title: { type: "string" } } },
        },
      },
    ],
  };
  const first = await postChat(body);
  const second = await postChat(body);

  assert.equal(first.status, 200);
  assert.equal(first.json.choices[0].finish_reason, "tool_calls");
  const calls = first.json.choices[0].message.tool_calls;
  assert.ok(Array.isArray(calls) && calls.length === 1);
  assert.equal(calls[0].type, "function");
  assert.equal(calls[0].function.name, "requirement.draft_create");
  const args = JSON.parse(calls[0].function.arguments);
  assert.equal(args.title, "登录功能需要支持手机号");
  assert.equal(args.priority, "P1");
  assert.ok(args.description.length > 0);
  assert.ok(args.acceptance_criteria.length > 0);
  assert.deepEqual(second.json, first.json);
});

test("requirement_curator finishes with text after a tool result", async () => {
  const toolCall = {
    id: "call_fake_1",
    type: "function",
    function: { name: "requirement.draft_create", arguments: `{"title":"登录功能"}` },
  };
  const { status, json } = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("requirement_curator"),
      userText("做一个登录功能"),
      { role: "assistant", content: null, tool_calls: [toolCall] },
      {
        role: "tool",
        tool_call_id: "call_fake_1",
        content: JSON.stringify({ requirement_id: "3001", version_id: "1001", revision: 1, status: "draft", title: "登录功能" }),
      },
    ],
  });
  assert.equal(status, 200);
  const message = json.choices[0].message;
  assert.equal(message.tool_calls, undefined);
  assert.equal(json.choices[0].finish_reason, "stop");
  assert.ok(message.content.includes("登录功能"), message.content);
});

test("context_scout summarizes the latest attachment tool result", async () => {
  const { status, json } = await postChat({
    model: "fake-llm",
    messages: [
      systemMarked("context_scout"),
      userText("读一下附件"),
      {
        role: "tool",
        tool_call_id: "call_fake_1",
        content: JSON.stringify({
          attachment_id: "9001",
          name: "需求说明.md",
          mime: "text/markdown",
          text: "登录需求正文",
        }),
      },
    ],
  });
  assert.equal(status, 200);
  const content = json.choices[0].message.content;
  assert.ok(content.includes("需求说明.md"), content);
  assert.ok(content.includes("共 6 字符"), content);
});

test("context_scout without a tool result explains there is nothing to read", async () => {
  const { status, json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("context_scout"), userText("读一下附件")],
  });
  assert.equal(status, 200);
  assert.ok(json.choices[0].message.content.includes("未发现"));
});

test("context_scout summarizes the attachment context from the user message", async () => {
  const userMessage =
    "请阅读附件《login-spec.md》（text/markdown，共 120 字符）并总结要点：\n登录需求正文，支持手机号登录。";
  const { status, json } = await postChat({
    model: "fake-llm",
    messages: [systemMarked("context_scout"), userText(userMessage)],
  });
  assert.equal(status, 200);
  assert.equal(
    json.choices[0].message.content,
    "已读取附件：login-spec.md，共 120 字符。请在团队频道查看摘要要点。",
  );
});

test("context overflow marker raises a 400 once per request body", async () => {
  const body = {
    model: "fake-llm",
    messages: [{ role: "system", content: "[profile:clarification_host] [fake:context_overflow]" }, userText("hi")],
  };
  const first = await postChat(body);
  assert.equal(first.status, 400);
  assert.equal(first.json.error.code, "context_length_exceeded");
  assert.ok(String(first.json.error.message).toLowerCase().includes("context length"));

  const second = await postChat(body);
  assert.equal(second.status, 200);

  const other = {
    model: "fake-llm",
    messages: [{ role: "system", content: "[profile:clarification_host] [fake:context_overflow]" }, userText("another")],
  };
  const third = await postChat(other);
  assert.equal(third.status, 400);
});

test("context overflow can be injected through the X-Fake-Context-Overflow header", async () => {
  const body = {
    model: "fake-llm",
    messages: [systemMarked("clarification_host"), userText("header overflow")],
  };
  const first = await postChat(body, { "X-Fake-Context-Overflow": "1" });
  assert.equal(first.status, 400);
  assert.equal(first.json.error.code, "context_length_exceeded");

  const second = await postChat(body, { "X-Fake-Context-Overflow": "1" });
  assert.equal(second.status, 200);
});

test("tools without a profile marker keep the P0 echo text", async () => {
  const { status, json } = await postChat({
    model: "fake-llm",
    messages: [userText("plain request")],
    tools: [
      {
        type: "function",
        function: { name: "any.tool", description: "any", parameters: { type: "object" } },
      },
    ],
  });
  assert.equal(status, 200);
  assert.equal(json.choices[0].message.content, "EzTest fake LLM: plain request");
  assert.equal(json.choices[0].finish_reason, "stop");
});

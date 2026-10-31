#!/usr/bin/env node
"use strict";

// Deterministic OpenAI-compatible fake LLM upstream for EzTest 0.2 P0 + P1 + P2.
//
// P0 only needs configuration / model catalog / connectivity tests and the
// acceptance flow. P1 adds a deterministic agent mode used by the clarification
// runtime; P2 adds the design team profiles. Without a profile marker the P0
// behaviour is unchanged.
//
// Endpoints:
//   GET  /health               -> {"status":"ok","service":"fake-llm",...}
//   GET  /v1/models            -> OpenAI-compatible model list
//   POST /v1/chat/completions  -> non-stream JSON or stream=true SSE
//
// Determinism rules (same request body => byte-identical response):
//   * completion id  = "chatcmpl-fake-" + sha256(raw body)[0:12]
//   * created        = 1767225600 (fixed 2026-01-01T00:00:00Z)
//   * P0 reply       = "EzTest fake LLM: " + last user message (trimmed,
//                      truncated to 240 code points); "EzTest fake LLM: ok"
//                      when there is no user text
//   * P1 profiles (system messages containing "[profile:<key>]"):
//       - clarification_host -> short Chinese clarification text quoting the
//         latest user message, never tool calls
//       - requirement_curator -> first turn: one requirement.draft_create
//         tool_call; after a role:"tool" result: final text
//       - context_scout -> attachment summary derived from the latest role:
//         "tool" result
//       - design_host -> design chat text; when the user asks to add/modify a
//         case, reads design.graph_read then submits case.graph_propose
//       - design_planner -> scope-only text, no tools
//       - case_author -> design.graph_read, then case.graph_propose with one
//         scenario and two draft cases, then final text
//       - design_critic -> design.review_submit with one minor finding, then
//         final text
//       - data_planner -> design.graph_read, then design.data_plan_submit for
//         the first draft case, then final text
//       - no profile ever calls case.confirm (user/server-only)
//   * context overflow  = system marker "[fake:context_overflow]" or request
//         header X-Fake-Context-Overflow: 1 returns HTTP 400 with
//         code=context_length_exceeded once per distinct raw request body
//   * usage numbers  = derived only from the request/reply text lengths
//   * stream chunks  = reply split into fixed 16-code-point chunks
//
// Port: FAKE_LLM_PORT (default 13334); EZTEST_FAKE_LLM_PORT is accepted as a
// legacy fallback. Host is always 127.0.0.1. The fake is not an EzTest
// runtime env var and is never read by the API.
//
// This process never touches the network besides loopback and never writes
// request logs.

const crypto = require("node:crypto");
const http = require("node:http");

const HOST = "127.0.0.1";
const DEFAULT_PORT = 13334;
const FIXED_CREATED = 1767225600;
const REPLY_PREFIX = "EzTest fake LLM: ";
const MAX_REPLY_CHARS = 240;
const STREAM_CHUNK_CHARS = 16;
const MAX_BODY_BYTES = 10 * 1024 * 1024;
const PROFILE_PATTERN = /\[profile:(clarification_host|requirement_curator|context_scout|design_host|design_planner|case_author|design_critic|data_planner)\]/;
const DESIGN_MODIFY_HINT = /(新增|补充|修改)/;
const DESIGN_CASE_HINT = "用例";
const OVERFLOW_MARKER = "[fake:context_overflow]";
const OVERFLOW_HEADER = "x-fake-context-overflow";
const MAX_CURATOR_TITLE_CHARS = 40;
const MAX_CURATOR_QUOTE_CHARS = 40;
const OPENING_USER_MARKER = "这是澄清会话的开场";
const HOST_OPENING_REPLY = "你好，我是澄清 Host。请描述需求或上传附件，我会整理成结构化草案并逐项澄清。";
const ATTACHMENT_PROMPT_PATTERN = /请阅读附件《([^》]+)》（[^，]*，共\s*(\d+)\s*字符）/;

// overflowSeen makes the injected context overflow fire exactly once per
// distinct request body, so the runtime's compact-and-retry path can recover.
const overflowSeen = new Set();

const MODELS = [
  {
    id: "fake-llm",
    object: "model",
    created: FIXED_CREATED,
    owned_by: "eztest",
    // Extra OpenAI-compatible metadata used by the P0 model catalog refresh.
    context_window: 64000,
    max_output_tokens: 8192,
  },
  {
    id: "fake-llm-mini",
    object: "model",
    created: FIXED_CREATED,
    owned_by: "eztest",
    context_window: 32000,
    max_output_tokens: 4096,
  },
];

function parsePort(raw) {
  const text = String(raw == null || raw === "" ? DEFAULT_PORT : raw).trim();
  if (!/^[0-9]+$/.test(text)) {
    throw new Error("FAKE_LLM_PORT must be an integer, got: " + JSON.stringify(raw));
  }
  const port = Number(text);
  if (!Number.isSafeInteger(port) || port < 0 || port > 65535) {
    throw new Error("FAKE_LLM_PORT must be between 0 and 65535, got: " + text);
  }
  return port;
}

const PORT = parsePort(process.env.FAKE_LLM_PORT ?? process.env.EZTEST_FAKE_LLM_PORT);

function sendJson(res, status, body) {
  if (res.headersSent || res.writableEnded || res.destroyed) return;
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": Buffer.byteLength(payload),
    "Cache-Control": "no-store",
  });
  res.end(payload);
}

function sendError(res, status, message, type, code) {
  sendJson(res, status, {
    error: {
      message,
      type: type || (status >= 500 ? "server_error" : "invalid_request_error"),
      param: null,
      code: code || null,
    },
  });
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (chunk) => {
      size += chunk.length;
      if (size > MAX_BODY_BYTES) {
        reject(new Error("request body too large"));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

function messageText(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((part) => {
      if (typeof part === "string") return part;
      if (part && typeof part.text === "string") return part.text;
      return "";
    })
    .join("");
}

// buildReply returns the exact assistant text for a request body.
function buildReply(body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (!message || message.role !== "user") continue;
    const text = messageText(message.content).trim();
    if (text === "") continue;
    return REPLY_PREFIX + Array.from(text).slice(0, MAX_REPLY_CHARS).join("");
  }
  return REPLY_PREFIX + "ok";
}

// lastUserText returns the trimmed text of the last non-empty user message.
// The runtime prefixes the persisted context packet to the user prompt; keep
// deterministic replies anchored to the actual current user text.
function lastUserText(body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (!message || message.role !== "user") continue;
    let text = messageText(message.content).trim();
    if (text === "") continue;
    const marker = "# 当前用户消息";
    const markerIndex = text.lastIndexOf(marker);
    if (markerIndex >= 0) {
      text = text.slice(markerIndex + marker.length).trim();
    }
    return text;
  }
  return "";
}

// allSystemText joins every system message so profile/overflow markers are
// found regardless of their position in the prompt.
function allSystemText(body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  return messages
    .filter((message) => message && message.role === "system")
    .map((message) => messageText(message.content))
    .join("\n");
}

// profileOf returns the P1 profile marker in the system prompt, if any.
function profileOf(body) {
  const match = PROFILE_PATTERN.exec(allSystemText(body));
  return match ? match[1] : null;
}

// lastToolResult returns the parsed JSON of the most recent tool message.
function lastToolResult(body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (!message || message.role !== "tool") continue;
    const text = messageText(message.content).trim();
    if (text === "") continue;
    try {
      const parsed = JSON.parse(text);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed;
    } catch {
      // Not a JSON tool result; keep looking at older tool messages.
    }
  }
  return null;
}

function truncateChars(text, max) {
  return Array.from(text).slice(0, max).join("");
}

function textOutcome(text) {
  return { kind: "text", text };
}

function hostOutcome(body) {
  const text = lastUserText(body);
  if (text.includes(OPENING_USER_MARKER)) {
    return textOutcome(HOST_OPENING_REPLY);
  }
  const quoted = truncateChars(text === "" ? "（空消息）" : text, MAX_CURATOR_QUOTE_CHARS);
  return textOutcome(
    "已收到你的澄清需求：" + quoted + "。请补充使用场景、验收标准与优先级，我会继续追问。",
  );
}

function curatorOutcome(body, rawBody) {
  const toolResult = lastToolResult(body);
  if (toolResult) {
    const title =
      typeof toolResult.title === "string" && toolResult.title.trim() !== ""
        ? truncateChars(toolResult.title.trim(), MAX_CURATOR_TITLE_CHARS)
        : "新需求";
    return textOutcome("需求草案已生成：" + title + "。请在右侧需求面板确认或继续补充。");
  }
  const title = truncateChars(lastUserText(body) || "未命名需求", MAX_CURATOR_TITLE_CHARS);
  return {
    kind: "tool_calls",
    calls: [
      {
        id: "call-fake-" + bodyHash(rawBody) + "-1",
        name: "requirement.draft_create",
        arguments: JSON.stringify({
          title,
          description: "由澄清对话生成的需求草案，待用户确认。",
          acceptance_criteria: "由澄清对话生成的验收标准草案，待用户确认。",
          priority: "P1",
        }),
      },
    ],
  };
}

function scoutOutcome(body) {
  const toolResult = lastToolResult(body);
  if (!toolResult) {
    // attachment.ingest runs do not execute tools: the runtime embeds the parsed
    // attachment context in the user message, so derive the summary from there.
    const match = ATTACHMENT_PROMPT_PATTERN.exec(lastUserText(body));
    if (match) {
      return textOutcome(
        "已读取附件：" + match[1] + "，共 " + match[2] + " 字符。请在团队频道查看摘要要点。",
      );
    }
    return textOutcome("未发现可读附件，请先上传附件再让我总结。");
  }
  const name =
    typeof toolResult.name === "string" && toolResult.name.trim() !== "" ? toolResult.name : "附件";
  let count = 0;
  if (typeof toolResult.chars === "number" && Number.isFinite(toolResult.chars)) {
    count = Math.max(0, Math.trunc(toolResult.chars));
  } else if (typeof toolResult.text === "string") {
    count = Array.from(toolResult.text).length;
  }
  return textOutcome("已读取附件：" + name + "，共 " + count + " 字符。");
}

// --- P2 design profiles ----------------------------------------------------

function toolCallOutcome(rawBody, name, args) {
  return {
    kind: "tool_calls",
    calls: [
      {
        id: "call-fake-" + bodyHash(rawBody) + "-1",
        name,
        arguments: JSON.stringify(args),
      },
    ],
  };
}

// graphResult returns the parsed design.graph_read result, or null when the
// latest tool message is not a graph view (error payloads included).
function graphResult(body) {
  const result = lastToolResult(body);
  if (!result || result.error || !Array.isArray(result.requirements)) return null;
  return result;
}

// isProposeResult reports whether the latest tool result is a graph proposal
// result ({scenario_ids,case_ids,case_version_ids,graph_hash}).
function isProposeResult(result) {
  return Boolean(result && (Array.isArray(result.case_ids) || Array.isArray(result.scenario_ids)));
}

function firstGraphRequirement(graph) {
  return graph.requirements.find((requirement) => requirement && requirement.requirement_id) || null;
}

function requirementSUTIDs(requirement) {
  const raw = Array.isArray(requirement.target_version_sut_ids) ? requirement.target_version_sut_ids : [];
  const ids = raw.filter((value) => typeof value === "string" && /^[1-9][0-9]*$/.test(value));
  return ids.length > 0 ? ids : ["9001"];
}

function firstGraphCase(requirement) {
  const scenarios = Array.isArray(requirement.scenarios) ? requirement.scenarios : [];
  for (const scenario of scenarios) {
    const cases = scenario && Array.isArray(scenario.cases) ? scenario.cases : [];
    for (const item of cases) {
      if (item && item.case_id) return item;
    }
  }
  return null;
}

function scenarioProposal(requirement) {
  const suts = requirementSUTIDs(requirement);
  const scenarios = Array.isArray(requirement.scenarios) ? requirement.scenarios : [];
  const existing = scenarios.find((scenario) => scenario && scenario.id);
  const node = {
    ref: "s1",
    name: existing && typeof existing.name === "string" && existing.name !== "" ? existing.name : "AI 设计场景",
    description: "由 fake LLM 生成的确定性设计场景。",
    version_sut_ids: suts,
    page_ids: [],
    api_ids: [],
    sort_order: 0,
  };
  if (existing) node.id = String(existing.id);
  return node;
}

function caseProposal(requirement, existing, title) {
  const suts = requirementSUTIDs(requirement);
  const requirementVersionID = String(requirement.requirement_version_id || "");
  const node = {
    ref: "c1",
    scenario_ref: "s1",
    title,
    description: "由 fake LLM 生成的确定性用例草案，等待用户确认。",
    preconditions: "已准备测试账号与运行环境。",
    test_data: { fake: true },
    priority: "P2",
    default_version_sut_id: suts[0],
    version_sut_ids: suts,
    page_ids: [],
    api_ids: [],
    steps: [
      { seq: 1, action: "打开被测页面", expected: "页面加载完成", data: {} },
      { seq: 2, action: "执行核心操作", expected: "操作成功", data: {} },
    ],
    assertions: [
      { step_seq: 1, kind: "ui", target: "页面标题", expected: "标题可见" },
      { step_seq: 2, kind: "text", target: "结果提示", expected: "操作成功" },
    ],
    sources: [
      { kind: "requirement", ref_id: requirementVersionID, path: "", line: null, sha: "" },
    ],
  };
  if (existing) node.id = String(existing.case_id);
  return node;
}

function graphProposal(requirement, options) {
  const settings = options || {};
  const titles = Array.isArray(settings.titles) && settings.titles.length > 0 ? settings.titles : ["AI 新增用例"];
  const existing = settings.updateFirstCase ? firstGraphCase(requirement) : null;
  return {
    requirement_id: String(requirement.requirement_id),
    requirement_version_id: String(requirement.requirement_version_id),
    scenarios: [scenarioProposal(requirement)],
    cases: titles.map((title, index) => caseProposal(requirement, index === 0 ? existing : null, title)),
  };
}

function designHostOutcome(body, rawBody) {
  const result = lastToolResult(body);
  if (result && isProposeResult(result)) {
    return textOutcome("已提交用例草案（draft），请在脑图中确认或继续提出修改。");
  }
  const graph = graphResult(body);
  if (graph) {
    const requirement = firstGraphRequirement(graph);
    if (!requirement) {
      return textOutcome("当前版本还没有可设计的已澄清需求。");
    }
    return toolCallOutcome(
      rawBody,
      "case.graph_propose",
      graphProposal(requirement, { updateFirstCase: true, titles: ["AI 补充用例（设计对话）"] }),
    );
  }
  if (result && result.error) {
    return textOutcome("读取脑图失败，请稍后重试，或直接在对话中说明要修改的用例。");
  }
  const text = lastUserText(body);
  if (text.includes(DESIGN_CASE_HINT) && DESIGN_MODIFY_HINT.test(text)) {
    return toolCallOutcome(rawBody, "design.graph_read", {});
  }
  return textOutcome(
    "我是设计 Host。我会读取用例脑图，并可以按你的要求新增或修改场景/用例 draft；请说明要调整的内容。",
  );
}

function designPlannerOutcome() {
  return textOutcome(
    "本轮设计范围已就绪：设计团队将按当前版本的待设计需求生成场景与用例草案；用户可在脑图中查看并确认。",
  );
}

function caseAuthorOutcome(body, rawBody) {
  const result = lastToolResult(body);
  if (result && isProposeResult(result)) {
    return textOutcome("已提交 1 个场景与 2 条用例 draft，等待用户在脑图中确认。");
  }
  const graph = graphResult(body);
  if (graph) {
    const requirement = firstGraphRequirement(graph);
    if (!requirement) {
      return textOutcome("没有找到可设计的已澄清需求。");
    }
    return toolCallOutcome(
      rawBody,
      "case.graph_propose",
      graphProposal(requirement, { titles: ["AI 用例 1：核心流程", "AI 用例 2：异常流程"] }),
    );
  }
  if (result && result.error) {
    return textOutcome("读取脑图失败，请稍后重试。");
  }
  return toolCallOutcome(rawBody, "design.graph_read", {});
}

function designCriticOutcome(body, rawBody) {
  const result = lastToolResult(body);
  if (result) {
    return textOutcome("已完成内部自审并提交 finding；内部自审不构成人工确认关卡。");
  }
  return toolCallOutcome(rawBody, "design.review_submit", {
    summary: "内部自审完成：覆盖与断言基本完整。",
    findings: [
      { severity: "minor", message: "建议补充异常路径的断言与测试数据。" },
    ],
  });
}

function dataPlannerOutcome(body, rawBody) {
  const result = lastToolResult(body);
  if (result && (result.case_version_id !== undefined || result.revision !== undefined)) {
    return textOutcome("已为当前 draft 用例补充前置条件与测试数据。");
  }
  const graph = graphResult(body);
  if (graph) {
    const requirement = firstGraphRequirement(graph);
    const item = requirement ? firstGraphCase(requirement) : null;
    if (item && item.current_version_id) {
      return toolCallOutcome(rawBody, "design.data_plan_submit", {
        case_version_id: String(item.current_version_id),
        preconditions: "已登录被测系统并准备测试账号。",
        test_data: { account: "admin", fake: true },
      });
    }
    return textOutcome("当前需求没有可补充测试数据的 draft 用例。");
  }
  if (result && result.error) {
    return textOutcome("读取脑图失败，请稍后重试。");
  }
  return toolCallOutcome(rawBody, "design.graph_read", {});
}

// chatOutcome selects the deterministic P1 profile reply, falling back to the
// frozen P0 echo when no profile marker is present.
function chatOutcome(body, rawBody) {
  switch (profileOf(body)) {
    case "clarification_host":
      return hostOutcome(body);
    case "requirement_curator":
      return curatorOutcome(body, rawBody);
    case "context_scout":
      return scoutOutcome(body);
    case "design_host":
      return designHostOutcome(body, rawBody);
    case "design_planner":
      return designPlannerOutcome();
    case "case_author":
      return caseAuthorOutcome(body, rawBody);
    case "design_critic":
      return designCriticOutcome(body, rawBody);
    case "data_planner":
      return dataPlannerOutcome(body, rawBody);
    default:
      return textOutcome(buildReply(body));
  }
}

function wantsContextOverflow(req, body) {
  if (String(req.headers[OVERFLOW_HEADER] || "") === "1") return true;
  return allSystemText(body).includes(OVERFLOW_MARKER);
}

function sendContextOverflow(res) {
  sendError(
    res,
    400,
    "This model's maximum context length is 64000 tokens. Please shorten the messages.",
    "invalid_request_error",
    "context_length_exceeded",
  );
}

function bodyHash(rawBody) {
  return crypto.createHash("sha256").update(rawBody, "utf8").digest("hex").slice(0, 12);
}

function completionId(rawBody) {
  return "chatcmpl-fake-" + bodyHash(rawBody);
}

function makeUsage(body, reply) {
  const rawMessages = JSON.stringify(Array.isArray(body.messages) ? body.messages : []);
  const promptTokens = Math.max(1, Math.ceil(rawMessages.length / 4));
  const completionTokens = Math.max(1, Math.ceil(Array.from(reply).length / 4));
  return {
    prompt_tokens: promptTokens,
    completion_tokens: completionTokens,
    total_tokens: promptTokens + completionTokens,
  };
}

function modelFor(body) {
  if (body && typeof body.model === "string" && body.model.trim() !== "") {
    return body.model.trim();
  }
  return MODELS[0].id;
}

function completionMessage(outcome) {
  const message = {
    role: "assistant",
    content: outcome.kind === "tool_calls" ? null : outcome.text,
  };
  if (outcome.kind === "tool_calls") {
    message.tool_calls = outcome.calls.map((call) => ({
      id: call.id,
      type: "function",
      function: { name: call.name, arguments: call.arguments },
    }));
  }
  return message;
}

function completionBody(body, rawBody, outcome) {
  const usageText =
    outcome.kind === "tool_calls"
      ? outcome.calls.map((call) => call.arguments).join("")
      : outcome.text;
  return {
    id: completionId(rawBody),
    object: "chat.completion",
    created: FIXED_CREATED,
    model: modelFor(body),
    choices: [
      {
        index: 0,
        message: completionMessage(outcome),
        finish_reason: outcome.kind === "tool_calls" ? "tool_calls" : "stop",
      },
    ],
    usage: makeUsage(body, usageText),
  };
}

function streamChunk(id, model, delta, finishReason, usage) {
  const chunk = {
    id,
    object: "chat.completion.chunk",
    created: FIXED_CREATED,
    model,
    choices: [
      {
        index: 0,
        delta,
        finish_reason: finishReason === undefined ? null : finishReason,
      },
    ],
  };
  if (usage) chunk.usage = usage;
  return chunk;
}

function writeSseEvent(res, data) {
  return res.write("data: " + JSON.stringify(data) + "\n\n");
}

function streamCompletion(res, body, rawBody, reply) {
  res.writeHead(200, {
    "Content-Type": "text/event-stream; charset=utf-8",
    "Cache-Control": "no-cache, no-transform",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  });
  if (typeof res.flushHeaders === "function") res.flushHeaders();

  const id = completionId(rawBody);
  const model = modelFor(body);
  const chars = Array.from(reply);
  const usage = makeUsage(body, reply);

  // Opening role delta.
  writeSseEvent(res, streamChunk(id, model, { role: "assistant" }));

  let closed = false;
  const markClosed = () => {
    closed = true;
  };
  res.on("close", markClosed);
  res.on("error", markClosed);

  const chunks = [];
  for (let i = 0; i < chars.length; i += STREAM_CHUNK_CHARS) {
    chunks.push(chars.slice(i, i + STREAM_CHUNK_CHARS).join(""));
  }
  // A reply always has at least "ok", so chunks is never empty; guard anyway.
  if (chunks.length === 0) chunks.push("");

  for (const content of chunks) {
    if (closed) return;
    writeSseEvent(res, streamChunk(id, model, { content }));
  }
  if (closed) return;
  writeSseEvent(res, streamChunk(id, model, {}, "stop", usage));
  res.write("data: [DONE]\n\n");
  res.end();
}

async function handleChatCompletions(req, res) {
  const rawBody = await readBody(req);
  let body;
  try {
    body = JSON.parse(rawBody);
  } catch {
    sendError(res, 400, "invalid JSON body");
    return;
  }
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    sendError(res, 400, "request body must be a JSON object");
    return;
  }
  if (body.messages !== undefined && !Array.isArray(body.messages)) {
    sendError(res, 400, "messages must be an array");
    return;
  }

  const requestKey = bodyHash(rawBody);
  if (wantsContextOverflow(req, body) && !overflowSeen.has(requestKey)) {
    overflowSeen.add(requestKey);
    sendContextOverflow(res);
    return;
  }

  const outcome = chatOutcome(body, rawBody);
  if (body.stream === true && outcome.kind === "text") {
    streamCompletion(res, body, rawBody, outcome.text);
    return;
  }
  sendJson(res, 200, completionBody(body, rawBody, outcome));
}

async function handleRequest(req, res) {
  let pathname;
  try {
    pathname = new URL(req.url, "http://" + HOST + ":" + PORT).pathname;
  } catch {
    sendError(res, 400, "invalid request URL");
    return;
  }

  if (req.method === "GET" && pathname === "/health") {
    sendJson(res, 200, {
      status: "ok",
      service: "fake-llm",
      deterministic: true,
      models: MODELS.map((model) => model.id),
    });
    return;
  }
  if (req.method === "GET" && pathname === "/v1/models") {
    sendJson(res, 200, { object: "list", data: MODELS });
    return;
  }
  if (req.method === "POST" && pathname === "/v1/chat/completions") {
    await handleChatCompletions(req, res);
    return;
  }
  sendError(res, 404, "not found", "not_found_error");
}

const server = http.createServer((req, res) => {
  handleRequest(req, res).catch((error) => {
    console.error("fake-llm: request failed: " + (error && error.stack ? error.stack : error));
    if (!res.headersSent) {
      sendError(res, 500, "fake LLM internal error");
      return;
    }
    if (!res.writableEnded) {
      try {
        res.end();
      } catch {
        // Best effort on an already-broken stream.
      }
    }
  });
});

server.on("error", (error) => {
  console.error("fake-llm: server error: " + error.message);
  process.exitCode = 1;
});

server.listen(PORT, HOST, () => {
  const address = server.address();
  const port = address && typeof address === "object" ? address.port : PORT;
  console.log("fake-llm listening on http://" + HOST + ":" + port);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    server.close(() => process.exit(0));
  });
}

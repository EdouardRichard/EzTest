#!/usr/bin/env node
"use strict";

// Deterministic OpenAI-compatible fake LLM upstream for EzTest 0.2 P0 + P1.
//
// P0 only needs configuration / model catalog / connectivity tests and the
// acceptance flow. P1 adds a deterministic agent mode used by the clarification
// runtime; without a profile marker the P0 behaviour is unchanged.
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
const PROFILE_PATTERN = /\[profile:(clarification_host|requirement_curator|context_scout)\]/;
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
function lastUserText(body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (!message || message.role !== "user") continue;
    const text = messageText(message.content).trim();
    if (text !== "") return text;
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

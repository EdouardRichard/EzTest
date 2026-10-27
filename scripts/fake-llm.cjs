#!/usr/bin/env node
"use strict";

// Deterministic OpenAI-compatible fake LLM upstream for the EzTest agent E2E
// suite (design spec §12.2). It never calls the network: it only answers on
// 127.0.0.1 and scripts the tool-calling / reasoning / abort / config flows.
//
// Endpoints:
//   GET  /v1/models
//   POST /v1/chat/completions   (streaming and non-streaming)
//
// Every chat-completions request body is appended as one JSON line to
// .tmp/fake-llm-requests.jsonl so Playwright can assert the outbound wire
// contract (tools / thinking / reasoning_effort / max_tokens / reasoning echo).

const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");

const repoRoot = path.resolve(__dirname, "..");
const PORT = Number(process.env.EZTEST_FAKE_LLM_PORT || 13334);
const HOST = "127.0.0.1";
const REQUEST_LOG_PATH = path.join(repoRoot, ".tmp", "fake-llm-requests.jsonl");

const MODEL = "deepseek-flash";
const TOOL_CALL_ID = "call_1";
const REASONING_TEXT = "需要读取 README";
const README_ARGUMENTS = JSON.stringify({ path: "README.md" });
const FINAL_TEXT = "已读取 README.md 的构建说明，诊断完成。";
const FAILTOOL_REASONING = "需要调用 test.fail 验证工具错误链路";
const FAILTOOL_ARGUMENTS = JSON.stringify({ mode: "error", message: "fake-llm FAILTOOL" });
const SLOW_CHUNK_COUNT = 20;
const SLOW_CHUNK_INTERVAL_MS = 300;

const MODELS_BODY = {
  data: [
    {
      id: MODEL,
      context_window: 1000000,
      max_output_tokens: 131072,
      input_modalities: ["text", "image"],
    },
  ],
};

const DIAGNOSTIC_USAGE = makeUsage(128, 24, 16);
const FINAL_USAGE = makeUsage(256, 16, 0);
const FAILTOOL_USAGE = makeUsage(128, 12, 6);
const ECHO_USAGE = makeUsage(96, 10, 0);
const SLOW_USAGE = makeUsage(80, SLOW_CHUNK_COUNT, 0);

// --- wire helpers ----------------------------------------------------------

function makeUsage(promptTokens, completionTokens, reasoningTokens) {
  return {
    prompt_tokens: promptTokens,
    completion_tokens: completionTokens,
    total_tokens: promptTokens + completionTokens,
    completion_tokens_details: { reasoning_tokens: reasoningTokens },
  };
}

function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}

function extractText(content) {
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

function systemPromptText(messages) {
  const message = messages.find((candidate) => candidate && candidate.role === "system");
  return message ? extractText(message.content) : "";
}

function truncateCodePoints(text, max) {
  return Array.from(text).slice(0, max).join("");
}

function completionBody(message, finishReason, usage) {
  return {
    id: "chatcmpl-fake-" + nowSeconds(),
    object: "chat.completion",
    created: nowSeconds(),
    model: MODEL,
    choices: [{ index: 0, message: { role: "assistant", ...message }, finish_reason: finishReason }],
    usage,
  };
}

function toolCallPayload(name, args) {
  return [{ id: TOOL_CALL_ID, type: "function", function: { name, arguments: args } }];
}

function streamChunk(delta, finishReason, usage) {
  const chunk = {
    id: "chatcmpl-fake-" + nowSeconds(),
    object: "chat.completion.chunk",
    created: nowSeconds(),
    model: MODEL,
    choices: [{ index: 0, delta, finish_reason: finishReason === undefined ? null : finishReason }],
  };
  if (usage) chunk.usage = usage;
  return chunk;
}

function sendJson(res, status, body) {
  if (res.headersSent || res.writableEnded || res.destroyed) return;
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": Buffer.byteLength(payload),
  });
  res.end(payload);
}

function sendError(res, status, message) {
  sendJson(res, status, { error: { message } });
}

// --- request log -----------------------------------------------------------

function recordRequest(body) {
  try {
    fs.appendFileSync(REQUEST_LOG_PATH, JSON.stringify(body) + "\n", "utf8");
  } catch (error) {
    console.error("fake-llm: failed to append the request log: " + error.message);
  }
}

// --- streaming plumbing ----------------------------------------------------

function startSse(res) {
  res.writeHead(200, {
    "Content-Type": "text/event-stream; charset=utf-8",
    "Cache-Control": "no-cache, no-transform",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  });
  if (typeof res.flushHeaders === "function") res.flushHeaders();
}

// createStreamWriter stops writing as soon as the client disconnects. Every
// write returns false once the response is closed, and wait() resolves early
// on "close", so the SLOW loop cannot keep a dead socket alive.
function createStreamWriter(res) {
  let closed = false;
  const markClosed = () => {
    closed = true;
  };
  res.on("close", markClosed);
  res.on("error", markClosed);

  return {
    get closed() {
      return closed || res.destroyed || res.writableEnded;
    },
    write(payload) {
      if (this.closed) return false;
      const data = typeof payload === "string" ? payload : JSON.stringify(payload);
      try {
        res.write("data: " + data + "\n\n");
      } catch {
        closed = true;
        return false;
      }
      return true;
    },
    async wait(ms) {
      if (this.closed) return false;
      await new Promise((resolve) => {
        let done = false;
        const finish = () => {
          if (done) return;
          done = true;
          clearTimeout(timer);
          res.removeListener("close", finish);
          resolve();
        };
        const timer = setTimeout(finish, ms);
        res.once("close", finish);
      });
      return !this.closed;
    },
    end() {
      if (this.closed) return;
      try {
        res.end();
      } catch {
        // The client may have disconnected between the check and the write.
      }
      closed = true;
    },
  };
}

function splitText(text, parts) {
  if (text.length === 0) return [""];
  const size = Math.max(1, Math.ceil(text.length / parts));
  const chunks = [];
  for (let index = 0; index < text.length; index += size) {
    chunks.push(text.slice(index, index + size));
  }
  return chunks;
}

// --- scripted behaviors ----------------------------------------------------

function diagnosticMessage() {
  return {
    content: null,
    reasoning_content: REASONING_TEXT,
    tool_calls: toolCallPayload("file.read", README_ARGUMENTS),
  };
}

function failToolMessage() {
  return {
    content: null,
    reasoning_content: FAILTOOL_REASONING,
    tool_calls: toolCallPayload("test.fail", FAILTOOL_ARGUMENTS),
  };
}

function echoSystemMessage(messages) {
  return { content: "system=" + truncateCodePoints(systemPromptText(messages), 80) };
}

async function streamToolTurn(res, message, usage) {
  startSse(res);
  const writer = createStreamWriter(res);
  const reasoning = message.reasoning_content || "";
  for (const part of splitText(reasoning, 2)) {
    if (!writer.write(streamChunk({ reasoning_content: part }))) return;
  }

  const call = message.tool_calls[0];
  const argumentParts = splitText(call.function.arguments, 3);
  for (let index = 0; index < argumentParts.length; index += 1) {
    const fragment = { index: 0, function: { arguments: argumentParts[index] } };
    if (index === 0) {
      fragment.id = call.id;
      fragment.type = call.type;
      fragment.function.name = call.function.name;
    }
    if (!writer.write(streamChunk({ tool_calls: [fragment] }))) return;
  }

  if (!writer.write(streamChunk({}, "tool_calls", usage))) return;
  writer.write("[DONE]");
  writer.end();
}

async function streamTextTurn(res, text, usage) {
  startSse(res);
  const writer = createStreamWriter(res);
  for (const part of splitText(text, 2)) {
    if (!writer.write(streamChunk({ content: part }))) return;
  }
  if (!writer.write(streamChunk({}, "stop", usage))) return;
  writer.write("[DONE]");
  writer.end();
}

async function streamSlowTurn(res) {
  startSse(res);
  const writer = createStreamWriter(res);
  for (let index = 0; index < SLOW_CHUNK_COUNT; index += 1) {
    if (!writer.write(streamChunk({ content: "慢速分片" + (index + 1) + " " }))) return;
    if (index < SLOW_CHUNK_COUNT - 1) {
      if (!(await writer.wait(SLOW_CHUNK_INTERVAL_MS))) return;
    }
  }
  if (!writer.write(streamChunk({}, "stop", SLOW_USAGE))) return;
  writer.write("[DONE]");
  writer.end();
}

// --- chat completions ------------------------------------------------------

async function handleChatCompletions(req, res) {
  const raw = await readBody(req);
  let body;
  try {
    body = JSON.parse(raw);
  } catch {
    sendError(res, 400, "invalid json body");
    return;
  }
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    sendError(res, 400, "invalid json body");
    return;
  }
  recordRequest(body);

  if (!Array.isArray(body.tools) || body.tools.length === 0) {
    sendError(res, 400, "tools must be non-empty");
    return;
  }

  const messages = Array.isArray(body.messages) ? body.messages : [];
  const lastMessage = messages.length > 0 ? messages[messages.length - 1] : {};
  const lastRole = lastMessage && lastMessage.role;
  const stream = body.stream === true;

  if (lastRole === "user") {
    const text = extractText(lastMessage.content);
    if (text.includes("SLOW")) {
      if (stream) return streamSlowTurn(res);
      sendJson(res, 200, completionBody({ content: "慢速输出已结束" }, "stop", SLOW_USAGE));
      return;
    }
    if (text.includes("FAILTOOL")) {
      const message = failToolMessage();
      if (stream) return streamToolTurn(res, message, FAILTOOL_USAGE);
      sendJson(res, 200, completionBody(message, "tool_calls", FAILTOOL_USAGE));
      return;
    }
    if (text.includes("ECHO_SYSTEM")) {
      const message = echoSystemMessage(messages);
      if (stream) return streamTextTurn(res, message.content, ECHO_USAGE);
      sendJson(res, 200, completionBody(message, "stop", ECHO_USAGE));
      return;
    }
    const message = diagnosticMessage();
    if (stream) return streamToolTurn(res, message, DIAGNOSTIC_USAGE);
    sendJson(res, 200, completionBody(message, "tool_calls", DIAGNOSTIC_USAGE));
    return;
  }

  if (lastRole === "tool") {
    const hasAssistantReasoning = messages.some(
      (message) =>
        message &&
        message.role === "assistant" &&
        typeof message.reasoning_content === "string" &&
        message.reasoning_content.length > 0,
    );
    if (!hasAssistantReasoning) {
      sendError(res, 400, "missing reasoning_content");
      return;
    }
    if (stream) return streamTextTurn(res, FINAL_TEXT, FINAL_USAGE);
    sendJson(res, 200, completionBody({ content: FINAL_TEXT }, "stop", FINAL_USAGE));
    return;
  }

  // Defensive fallback: a history that does not end with a user or tool turn
  // still gets a terminal answer instead of hanging the run.
  if (stream) return streamTextTurn(res, FINAL_TEXT, FINAL_USAGE);
  sendJson(res, 200, completionBody({ content: FINAL_TEXT }, "stop", FINAL_USAGE));
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (chunk) => {
      size += chunk.length;
      if (size > 10 * 1024 * 1024) {
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

async function handleRequest(req, res) {
  let pathname;
  try {
    pathname = new URL(req.url, "http://" + HOST + ":" + PORT).pathname;
  } catch {
    sendError(res, 400, "invalid request url");
    return;
  }

  if (req.method === "GET" && pathname === "/v1/models") {
    sendJson(res, 200, MODELS_BODY);
    return;
  }
  if (req.method === "POST" && pathname === "/v1/chat/completions") {
    await handleChatCompletions(req, res);
    return;
  }
  sendError(res, 404, "not found");
}

// --- server bootstrap ------------------------------------------------------

fs.mkdirSync(path.dirname(REQUEST_LOG_PATH), { recursive: true });

const server = http.createServer((req, res) => {
  handleRequest(req, res).catch((error) => {
    console.error("fake-llm: request failed: " + (error && error.stack ? error.stack : error));
    if (!res.headersSent) {
      sendError(res, 500, "fake llm internal error");
    } else if (!res.writableEnded) {
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
  console.log("fake-llm listening on http://" + HOST + ":" + PORT);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    server.close(() => process.exit(0));
  });
}

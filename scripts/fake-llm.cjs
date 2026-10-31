#!/usr/bin/env node
"use strict";

// Deterministic OpenAI-compatible fake LLM upstream for EzTest 0.2 P0.
//
// P0 only needs configuration / model catalog / connectivity tests and the
// acceptance flow; it does not run agents or chat gateways. This server is the
// single local upstream used by E2E/acceptance so tests never call a real LLM.
//
// Endpoints:
//   GET  /health               -> {"status":"ok","service":"fake-llm",...}
//   GET  /v1/models            -> OpenAI-compatible model list
//   POST /v1/chat/completions  -> non-stream JSON or stream=true SSE
//
// Determinism rules (same request body => byte-identical response):
//   * completion id  = "chatcmpl-fake-" + sha256(raw body)[0:12]
//   * created        = 1767225600 (fixed 2026-01-01T00:00:00Z)
//   * reply text     = "EzTest fake LLM: " + last user message (trimmed,
//                      truncated to 240 code points); "EzTest fake LLM: ok"
//                      when there is no user text
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

function sendError(res, status, message, type) {
  sendJson(res, status, {
    error: {
      message,
      type: type || (status >= 500 ? "server_error" : "invalid_request_error"),
      param: null,
      code: null,
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

function completionId(rawBody) {
  const hash = crypto.createHash("sha256").update(rawBody, "utf8").digest("hex");
  return "chatcmpl-fake-" + hash.slice(0, 12);
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

function completionBody(body, rawBody, reply) {
  return {
    id: completionId(rawBody),
    object: "chat.completion",
    created: FIXED_CREATED,
    model: modelFor(body),
    choices: [
      {
        index: 0,
        message: { role: "assistant", content: reply },
        finish_reason: "stop",
      },
    ],
    usage: makeUsage(body, reply),
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

  const reply = buildReply(body);
  if (body.stream === true) {
    streamCompletion(res, body, rawBody, reply);
    return;
  }
  sendJson(res, 200, completionBody(body, rawBody, reply));
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

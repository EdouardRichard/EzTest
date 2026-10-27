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

// --- P3 deterministic clarification scenarios (design spec §10.2) ----------
//
// P3 requests are identified by stable markers in the latest user message
// (P3-CLARIFY-1/P3-CLARIFY-2) or by the one-shot analyze/recon instructions.
// Every turn first reads the real version context through version.context_get,
// then emits one tool call per turn so the persisted P1 history (and the
// transcript folding) exercise the real tool path. Only tool effects are
// asserted by Playwright; the reply markers exist to know when a run ended.
const P3_ENABLED = process.env.EZTEST_FAKE_LLM_P3 !== "0";
const P3_TOOL_USAGE = makeUsage(180, 12, 8);
const P3_TEXT_USAGE = makeUsage(220, 10, 0);

const P3 = {
  QUESTION_ONE: "P3 问题一：订单金额达到多少需要人工审核？",
  QUESTION_TWO: "P3 问题二：退款是否需要原路退回？",
  DRAFT_DESCRIPTION: "P3 草案更新：已补充人工审核阈值说明。",
  DECISION_TOPIC: "P3 退款政策",
  DECISION_TEXT: "P3 决策：支持 7 天无理由退款",
  DECISION_RATIONALE: "P3 决策理由：客户退款体验优先。",
  RELATION_DESCRIPTION: "P3 关系建议：取消依赖创建后的库存状态。",
  CLARIFY_ONE_REPLY: "P3 第一轮完成：已记录问题并更新需求草案。",
  CLARIFY_TWO_REPLY: "P3 第二轮完成：已提交关系与决策建议，请人工确认。",
  ANALYSIS_RELATION_DESCRIPTION: "P3 分析关系建议：退款与结算共享金额口径。",
  ANALYSIS_TOPIC: "P3 分析结论",
  ANALYSIS_DECISION: "P3 分析决策：退款与结算金额口径需一致。",
  ANALYSIS_REPLY: "P3 分析完成：已提交关系与决策建议。",
  RECON_SUMMARY: "P3 上下文摘要：已读取文本附件、截图与绑定代码；业务规则与未决问题已记录。",
  RECON_REPLY: "P3 侦察完成：已记录上下文摘要。",
};


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

function toolCallPayload(name, args, id) {
  return [{ id: id || TOOL_CALL_ID, type: "function", function: { name, arguments: args } }];
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

// --- P3 scripted behaviors -------------------------------------------------

function findLastUserMessage(messages) {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (message && message.role === "user") {
      return { index, text: extractText(message.content) };
    }
  }
  return null;
}

function parseJsonObject(text) {
  try {
    const parsed = JSON.parse(text);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

/**
 * Describes the P3 run that owns the trailing tool turns: how many tool-call
 * turns already happened after the marker user message, the latest parsed
 * version.context_get payload and the latest question-upsert result.
 */
function describeP3Run(messages, lastUserIndex) {
  const callNames = new Map();
  const toolResults = [];
  let step = 0;
  for (let index = lastUserIndex + 1; index < messages.length; index += 1) {
    const message = messages[index];
    if (!message) continue;
    if (message.role === "assistant" && Array.isArray(message.tool_calls) && message.tool_calls.length > 0) {
      step += 1;
      for (const call of message.tool_calls) {
        if (call && call.id) callNames.set(call.id, call.function ? call.function.name : "");
      }
      continue;
    }
    if (message.role === "tool") {
      toolResults.push({
        name: callNames.get(message.tool_call_id) || "",
        text: extractText(message.content),
      });
    }
  }
  let context = null;
  let questionId = null;
  for (const result of toolResults) {
    if (result.name === "version_context_get") {
      const parsed = parseJsonObject(result.text);
      if (parsed) context = parsed;
    }
    if (result.name === "clarification_question_upsert") {
      const parsed = parseJsonObject(result.text);
      if (parsed && typeof parsed.questionId === "string") questionId = parsed.questionId;
    }
  }
  return { step, context, questionId };
}

/** Matches one P3 scenario from the latest user message; null for P1/P2. */
function detectP3Scenario(text) {
  const clarify = /P3-CLARIFY-(1|2)/.exec(text);
  if (clarify) {
    const versionMatch = /version(?:Id)?\s*[=:]\s*"?(\d+)"?/i.exec(text);
    return {
      kind: "clarification",
      round: Number(clarify[1]),
      versionId: versionMatch ? versionMatch[1] : "",
    };
  }
  const analyze = /Analyze the requirements of version\s+(\d+)/i.exec(text);
  if (analyze) return { kind: "analysis", versionId: analyze[1] };
  const recon = /Recon the context of version\s+(\d+)/i.exec(text);
  if (recon) return { kind: "recon", versionId: recon[1] };
  return null;
}

function p3Tool(name, args, reasoning) {
  return { kind: "tool", name, arguments: JSON.stringify(args), reasoning };
}

function p3Text(text) {
  return { kind: "text", text };
}

function p3Requirements(context) {
  return context && Array.isArray(context.requirements) ? context.requirements : [];
}

/** Plans exactly one P3 turn from the scenario, the run state and the context. */
function planP3Turn(scenario, run) {
  const context = run.context;
  const versionId =
    scenario.versionId ||
    (context && context.version && typeof context.version.id === "string" ? context.version.id : "");
  if (versionId === "") {
    return p3Text("P3 无法确定版本上下文，本轮结束。");
  }
  const requirements = p3Requirements(context);
  const first = requirements[0];
  const second = requirements[1];

  if (scenario.kind === "clarification") {
    if (scenario.round === 1) {
      if (run.step === 0) {
        return p3Tool("version_context_get", { versionId }, "P3 场景：读取版本上下文");
      }
      if (run.step === 1 && first) {
        return p3Tool(
          "clarification_question_upsert",
          { versionId, requirementId: first.id, question: P3.QUESTION_ONE },
          "P3 场景：记录第一个澄清问题",
        );
      }
      if (run.step === 2 && first) {
        return p3Tool(
          "requirement_update_draft",
          {
            versionId,
            requirementId: first.id,
            revision: first.revision,
            patch: { description: P3.DRAFT_DESCRIPTION },
          },
          "P3 场景：把确认信息写回需求草案",
        );
      }
      return p3Text(P3.CLARIFY_ONE_REPLY);
    }
    if (run.step === 0) {
      return p3Tool("version_context_get", { versionId }, "P3 场景：读取第二轮上下文");
    }
    if (run.step === 1 && second) {
      return p3Tool(
        "clarification_question_upsert",
        { versionId, requirementId: second.id, question: P3.QUESTION_TWO },
        "P3 场景：记录第二个澄清问题",
      );
    }
    if (run.step === 2 && run.questionId && second) {
      return p3Tool(
        "decision.propose",
        {
          versionId,
          requirementId: second.id,
          questionId: run.questionId,
          topic: P3.DECISION_TOPIC,
          decision: P3.DECISION_TEXT,
          rationale: P3.DECISION_RATIONALE,
        },
        "P3 场景：提出等待人接受的决策建议",
      );
    }
    if (run.step === 3 && first && second) {
      return p3Tool(
        "relation.propose",
        {
          versionId,
          fromRequirementId: first.id,
          toRequirementId: second.id,
          relationType: "depends_on",
          description: P3.RELATION_DESCRIPTION,
        },
        "P3 场景：提交需求关系建议",
      );
    }
    return p3Text(P3.CLARIFY_TWO_REPLY);
  }

  if (scenario.kind === "analysis") {
    if (run.step === 0) {
      return p3Tool("version_context_get", { versionId }, "P3 场景：读取需求分析上下文");
    }
    if (run.step === 1 && first && second) {
      return p3Tool(
        "relation.propose",
        {
          versionId,
          fromRequirementId: first.id,
          toRequirementId: second.id,
          relationType: "shares_scope",
          description: P3.ANALYSIS_RELATION_DESCRIPTION,
        },
        "P3 场景：分析关系建议",
      );
    }
    if (run.step === 2 && first) {
      return p3Tool(
        "decision.propose",
        {
          versionId,
          requirementId: first.id,
          topic: P3.ANALYSIS_TOPIC,
          decision: P3.ANALYSIS_DECISION,
        },
        "P3 场景：提交分析结论",
      );
    }
    return p3Text(P3.ANALYSIS_REPLY);
  }

  if (scenario.kind === "recon") {
    if (run.step === 0) {
      return p3Tool("version_context_get", { versionId }, "P3 场景：读取侦察上下文");
    }
    if (run.step === 1) {
      return p3Tool(
        "context.summary_update",
        { versionId, summary: P3.RECON_SUMMARY },
        "P3 场景：写入上下文摘要",
      );
    }
    return p3Text(P3.RECON_REPLY);
  }

  return null;
}

/**
 * Handles one P3 scenario request. Returns true when the request belonged to a
 * P3 scenario, false so the P1/P2 behaviors below stay untouched.
 */
async function handleP3Scenario(messages, res, stream) {
  if (!P3_ENABLED) return false;
  const lastUser = findLastUserMessage(messages);
  if (lastUser === null) return false;
  const scenario = detectP3Scenario(lastUser.text);
  if (scenario === null) return false;

  const run = describeP3Run(messages, lastUser.index);
  const plan = planP3Turn(scenario, run);
  if (plan === null) return false;

  if (plan.kind === "tool") {
    const message = {
      content: null,
      reasoning_content: plan.reasoning,
      tool_calls: toolCallPayload(plan.name, plan.arguments, `p3_call_${run.step + 1}`),
    };
    if (stream) {
      await streamToolTurn(res, message, P3_TOOL_USAGE);
      return true;
    }
    sendJson(res, 200, completionBody(message, "tool_calls", P3_TOOL_USAGE));
    return true;
  }

  if (stream) {
    await streamTextTurn(res, plan.text, P3_TEXT_USAGE);
    return true;
  }
  sendJson(res, 200, completionBody({ content: plan.text }, "stop", P3_TEXT_USAGE));
  return true;
}

// --- scripted behaviors ----------------------------------------------------

function diagnosticMessage() {
  return {
    content: null,
    reasoning_content: REASONING_TEXT,
    tool_calls: toolCallPayload("file_read", README_ARGUMENTS),
  };
}

function failToolMessage() {
  return {
    content: null,
    reasoning_content: FAILTOOL_REASONING,
    tool_calls: toolCallPayload("test_fail", FAILTOOL_ARGUMENTS),
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

  // The P3 clarification/analysis/recon scenarios own every request whose last
  // user message carries their marker (or the one-shot instruction). P1/P2
  // requests never match this detector and keep the original behavior.
  if (await handleP3Scenario(messages, res, stream)) return;

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

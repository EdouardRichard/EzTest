#!/usr/bin/env node
"use strict";

// Deterministic OpenAI-compatible fake LLM upstream for the EzTest agent E2E
// suite (design spec §12.2). It never calls the network: it only answers on
// 127.0.0.1 and scripts the tool-calling / reasoning / abort / config flows.
//
// Endpoints:
//   GET  /v1/models
//   POST /v1/chat/completions   (streaming and non-streaming)
//   GET  /__p4-mode             (test-only: the active P4 scenario mode)
//   POST /__p4-mode             (test-only: switch the P4 scenario at runtime)
//   GET  /__p5-mode             (test-only: the active P5 mindmap scenario mode)
//   POST /__p5-mode             (test-only: switch the P5 scenario at runtime)
//   GET  /__p6-mode             (test-only: the active P6 execution scenario mode)
//   POST /__p6-mode             (test-only: switch the P6 scenario at runtime)
//   GET  /__p7-mode             (test-only: the active P7 autonomous-loop mode)
//   POST /__p7-mode             (test-only: switch the P7 scenario at runtime)
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

// --- P4 deterministic code-intelligence scenarios (design spec §10.2) -------
//
// P4 requests are identified by the stable system-prompt markers of design
// spec §8.3/§8.4. File analysis answers per repository-relative path with facts
// whose evidence line ranges match the fixture repository created by
// scripts/fixture-codeintel-repo.ps1 (`.tmp/p4-e2e-repo`, HEAD commit). Impact
// reasoning returns no candidate by default; EZTEST_FAKE_LLM_P4_MODE=impact-ai
// returns the requirement whose title carries P4-AI-IMPACT (or, when no title
// carries it, the first requirement of the given list).
//
// Modes (§10.2): ok (default) | error (HTTP 500 for P4 analysis requests) |
// partial (one file answers with invalid JSON) | impact-ai (ok facts + AI
// impact candidate). The requests are appended compactly — kind/path/commit/
// mode only — to .tmp/fake-llm-requests.jsonl: never the prompt or code body.
const P4_FILE_ANALYZER_MARKER = "EzTest 的只读代码分析器";
const P4_IMPACT_ANALYZER_MARKER = "EzTest 的只读变更影响分析器";
const P4_IMPACT_TITLE_MARKER = "P4-AI-IMPACT";
const P4_PARTIAL_INVALID_PATH = "src/router.ts";
const P4_MODES = new Set(["ok", "error", "partial", "impact-ai"]);
const P4_USAGE = makeUsage(1024, 192, 0);

// Line ranges below are pinned to the fixture HEAD commit content. They are
// verified against the real fixture files by scripts/fake-llm.p4.test.cjs.
const P4_FILE_TEMPLATES = {
  "src/api/refund.ts": {
    changeSummary: "退款 API 客户端：使用 POST /api/refund 提交退款请求，本次新增 reason 参数。",
    facts: [
      {
        kind: "client_call",
        name: "POST /api/refund",
        startLine: 4,
        endLine: 6,
        signature: "requestRefund(input: RefundRequest)",
        confidence: "high",
        summary: "调用后端退款接口 POST /api/refund。",
        evidence: [{ startLine: 5, endLine: 6 }],
      },
    ],
    edges: [],
  },
  "src/router.ts": {
    changeSummary: "页面路由表：注册订单列表页与新增的退款页路由。",
    facts: [
      {
        kind: "route",
        name: "/orders",
        startLine: 5,
        endLine: 7,
        signature: "GET /orders",
        confidence: "high",
        summary: "订单列表页路由。",
        evidence: [{ startLine: 5, endLine: 7 }],
      },
      {
        kind: "route",
        name: "/orders/refund",
        startLine: 9,
        endLine: 11,
        signature: "GET /orders/refund",
        confidence: "high",
        summary: "退款页路由。",
        evidence: [{ startLine: 9, endLine: 11 }],
      },
    ],
    edges: [],
  },
  "src/pages/OrdersPage.tsx": {
    changeSummary: "订单页面组件：展示订单列表并触发退款，本次在调用点补充 reason 参数。",
    facts: [
      {
        kind: "component",
        name: "OrdersPage",
        startLine: 3,
        endLine: 9,
        signature: "OrdersPage({ orders })",
        confidence: "high",
        summary: "订单列表页面组件。",
        evidence: [{ startLine: 3, endLine: 3 }],
      },
    ],
    edges: [
      {
        from: { kind: "component", name: "OrdersPage" },
        to: { kind: "route", name: "/orders" },
        rel: "contains",
        confidence: "high",
        evidence: [{ startLine: 3, endLine: 3 }],
      },
    ],
  },
  "server/refund.go": {
    changeSummary: "退款的 Go HTTP 接口：注册 POST /api/refund 并解析新增的 reason 参数。",
    facts: [
      {
        kind: "api",
        name: "POST /api/refund",
        startLine: 13,
        endLine: 15,
        signature: "func registerRefundRoutes(mux *http.ServeMux)",
        confidence: "high",
        summary: "注册退款接口 POST /api/refund。",
        evidence: [{ startLine: 14, endLine: 14 }],
      },
    ],
    edges: [],
  },
  "src/utils/format.ts": {
    changeSummary: "金额格式化工具函数：只调整格式化参数，未识别页面/路由/接口事实。",
    facts: [],
    edges: [],
  },
};

const P4_DEFAULT_FILE_ANALYSIS = {
  changeSummary: "未识别到页面、路由、接口或组件事实。",
  facts: [],
  edges: [],
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

// --- P4 scripted behaviors -------------------------------------------------

/**
 * Test-only runtime control of EZTEST_FAKE_LLM_P4_MODE (design spec §10.5 item
 * 3). The degraded E2E switches the fake to `error` while the servers are
 * already running and restores `ok` afterwards, so a whole restart is not
 * needed. The handle is only exposed by this local fake upstream.
 */
async function handleP4ModeControl(req, res) {
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
  const mode = String(body.mode || "")
    .trim()
    .toLowerCase();
  if (!P4_MODES.has(mode)) {
    sendError(res, 400, "mode must be one of ok, error, partial, impact-ai");
    return;
  }
  process.env.EZTEST_FAKE_LLM_P4_MODE = mode;
  sendJson(res, 200, { mode: p4Mode() });
}

function p4Mode() {
  const value = String(process.env.EZTEST_FAKE_LLM_P4_MODE || "ok")
    .trim()
    .toLowerCase();
  return P4_MODES.has(value) ? value : "ok";
}

/** Matches one P4 scenario from the system prompt; null for P1/P2/P3. */
function detectP4Scenario(systemText) {
  if (systemText.includes(P4_FILE_ANALYZER_MARKER)) return "file_analysis";
  if (systemText.includes(P4_IMPACT_ANALYZER_MARKER)) return "impact_analysis";
  return null;
}

function matchPromptField(text, label) {
  const match = new RegExp("(?:^|\\n)" + label + "：([^\\n]+)").exec(text);
  return match ? match[1].trim() : "";
}

/** Appends the compact P4 request line (never the prompt or file content). */
function recordP4Request(kind, pathValue, commit, mode) {
  const entry = {
    p4: true,
    kind,
    path: pathValue || null,
    commit: commit || null,
    mode,
  };
  try {
    fs.appendFileSync(REQUEST_LOG_PATH, JSON.stringify(entry) + "\n", "utf8");
  } catch (error) {
    console.error("fake-llm: failed to append the request log: " + error.message);
  }
}

/** Builds one valid §8.3 JSON object with the prompt path filled in. */
function p4FileAnalysisResponse(pathValue) {
  const template = Object.prototype.hasOwnProperty.call(P4_FILE_TEMPLATES, pathValue)
    ? P4_FILE_TEMPLATES[pathValue]
    : P4_DEFAULT_FILE_ANALYSIS;
  return {
    changeSummary: template.changeSummary,
    facts: template.facts.map((fact) => ({
      kind: fact.kind,
      name: fact.name,
      path: pathValue,
      startLine: fact.startLine,
      endLine: fact.endLine,
      signature: fact.signature,
      confidence: fact.confidence,
      summary: fact.summary,
      // Evidence is only file + line range: the server always recomputes the
      // excerpt hash from the commit blob (design spec §6.3).
      evidence: fact.evidence.map((item) => ({
        file: pathValue,
        startLine: item.startLine,
        endLine: item.endLine,
      })),
    })),
    edges: template.edges.map((edge) => ({
      from: { kind: edge.from.kind, name: edge.from.name },
      to: { kind: edge.to.kind, name: edge.to.name },
      rel: edge.rel,
      confidence: edge.confidence,
      evidence: edge.evidence.map((item) => ({
        file: pathValue,
        startLine: item.startLine,
        endLine: item.endLine,
      })),
    })),
  };
}

/** Extracts the version requirement list of the §8.4 impact prompt. */
function parseP4Requirements(userText) {
  const block = /版本需求清单（id \| 标题 \| 描述摘要）：\n([\s\S]*?)\n\n变更文件/.exec(userText);
  const section = block ? block[1] : userText;
  const requirements = [];
  const pattern = /^(\d+)\s*\|\s*(.*?)\s*\|/gm;
  let match = pattern.exec(section);
  while (match !== null) {
    requirements.push({ id: match[1], title: match[2] });
    match = pattern.exec(section);
  }
  return requirements;
}

/** Extracts the changed-file paths of the §8.4 impact prompt. */
function parseP4ChangedFiles(userText) {
  const block = /变更文件（path \| status）：\n([\s\S]*?)\n\n变更事实/.exec(userText);
  if (!block) return [];
  return block[1]
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && line !== "(无)")
    .map((line) => line.split("|")[0].trim())
    .filter((line) => line !== "");
}

/** Picks the first evidence interval of the prompt that names a changed file. */
function p4ImpactEvidence(userText, changedFiles) {
  for (const file of changedFiles) {
    const escaped = file.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const match = new RegExp(escaped + ":(\\d+)-(\\d+)").exec(userText);
    if (match) {
      return { file, startLine: Number(match[1]), endLine: Number(match[2]) };
    }
  }
  if (changedFiles.length > 0) {
    return { file: changedFiles[0], startLine: 1, endLine: 1 };
  }
  return null;
}

/**
 * Builds the §8.4 JSON object. It stays deterministic: the default mode returns
 * an empty affected list, impact-ai returns the P4-AI-IMPACT requirement (or the
 * first listed requirement) with a reason and validated-looking evidence.
 */
function p4ImpactResponse(userText, mode) {
  if (mode !== "impact-ai") return { affected: [] };
  const requirements = parseP4Requirements(userText);
  if (requirements.length === 0) return { affected: [] };

  const marked = requirements.filter((requirement) =>
    requirement.title.toUpperCase().includes(P4_IMPACT_TITLE_MARKER),
  );
  const chosen = marked.length > 0 ? marked : [requirements[0]];
  const changedFiles = parseP4ChangedFiles(userText);
  const evidence = p4ImpactEvidence(userText, changedFiles);

  return {
    affected: chosen.map((requirement) => {
      const affected = {
        requirementId: requirement.id,
        reason:
          "AI 推测：退款调用与接口均新增 reason 参数，需求「" +
          requirement.title +
          "」的展示与校验可能需要同步。",
        confidence: "high",
        evidence: evidence ? [evidence] : [],
      };
      return affected;
    }),
  };
}

/**
 * Handles one P4 analysis request. Returns true when the system prompt carried
 * a P4 marker, false so the P1/P3 behaviors below stay untouched.
 */
async function handleP4Scenario(messages, res, stream) {
  const kind = detectP4Scenario(systemPromptText(messages));
  if (kind === null) return false;

  const lastUser = findLastUserMessage(messages);
  const userText = lastUser ? lastUser.text : "";
  const pathValue = kind === "file_analysis" ? matchPromptField(userText, "文件") : "";
  const commit = matchPromptField(userText, "提交");
  const mode = p4Mode();
  recordP4Request(kind, pathValue, commit, mode);

  if (mode === "error") {
    sendError(res, 500, "fake P4 analysis upstream error");
    return true;
  }

  let text;
  if (mode === "partial" && kind === "file_analysis" && pathValue === P4_PARTIAL_INVALID_PATH) {
    // Deliberately not JSON so the parser records a parse_error for this file.
    text = "fake P4 partial mode: " + P4_PARTIAL_INVALID_PATH + " response is not valid JSON";
  } else if (kind === "file_analysis") {
    text = JSON.stringify(p4FileAnalysisResponse(pathValue));
  } else {
    text = JSON.stringify(p4ImpactResponse(userText, mode));
  }

  if (stream) {
    await streamTextTurn(res, text, P4_USAGE);
    return true;
  }
  sendJson(res, 200, completionBody({ content: text }, "stop", P4_USAGE));
  return true;
}

// --- P5 deterministic mindmap scenarios (design spec §10.6) ----------------
//
// P5 requests are identified by the stable Chinese role markers of the four
// mindmap profiles (`backend/internal/mindmap/domain/prompts.go`, spec §8.1).
// Each run is scripted from the number of assistant tool turns already present
// after the run instruction (same counting trick as P3):
//
//   design    case_graph_read -> case_graph_propose -> final text
//   review    case_graph_read -> case_review_submit -> final text
//   data_plan case_graph_read -> case_data_plan_submit -> final text
//   explore   explore_http x2 -> app_model_propose -> final text
//
// Tool function names use the P1 gateway's dot->underscore wire form
// (`internal/agent/infrastructure/llm/gateway.go`), so the backend registers
// `case.graph_propose` but the model must answer `case_graph_propose`.
//
// Modes (§10.6): normal (default) | invalid (structurally invalid proposal, so
// a design run must fail with zero materialization) | degraded (text only, no
// structured proposal). `degraded` is a SUCCESSFUL text completion, not an LLM
// failure (T16/T26 decision): the backend records a degraded run instead of a
// failed one. `POST /__p5-mode` switches the mode at runtime.
const P5_DESIGN_MARKER = "EzTest 的测试设计 Agent";
const P5_REVIEW_MARKER = "EzTest 的设计评审 Agent";
// The spec §10.6 wording and the landed T7 prompt body differ by one character
// ("与" vs "/"); both are accepted so a prompt sync cannot silently disable the
// data-plan scenario.
const P5_DATA_PLAN_MARKERS = ["EzTest 的数据与前置规划 Agent", "EzTest 的数据/前置规划 Agent"];
const P5_EXPLORE_MARKER = "EzTest 的应用探索 Agent";
const P5_MODES = new Set(["normal", "invalid", "degraded"]);
const P5_TOOL_USAGE = makeUsage(640, 128, 64);
const P5_TEXT_USAGE = makeUsage(320, 32, 0);
const P5_EXPLORE_PATHS = ["/p5/home", "/api/orders"];
const P5_DEGRADED_TEXT = "P5 降级：LLM 不可用，未提交结构化提案，请使用人工路径。";
const P5_NO_VERSION_TEXT = "P5 无法从运行指令确定 versionId，本轮未提交任何提案。";
const P5_FINAL_TEXTS = {
  design: "P5 设计完成：已提交 1 个场景与 2 条用例的脑图提案。",
  review: "P5 评审完成：已提交覆盖、断言、重复与代码未覆盖四类评审发现。",
  data_plan: "P5 数据规划完成：已提交前置条件与测试数据建议。",
  explore: "P5 探索完成：已提交页面、路由与接口线索。",
};

const P5_INVALID_REQUIREMENT_ID = "999999999999999999";

/** Test-only runtime control of EZTEST_FAKE_LLM_P5_MODE (spec §10.6). */
async function handleP5ModeControl(req, res) {
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
  const mode = String(body.mode || "")
    .trim()
    .toLowerCase();
  if (!P5_MODES.has(mode)) {
    sendError(res, 400, "mode must be one of normal, invalid, degraded");
    return;
  }
  process.env.EZTEST_FAKE_LLM_P5_MODE = mode;
  sendJson(res, 200, { mode: p5Mode() });
}

function p5Mode() {
  const value = String(process.env.EZTEST_FAKE_LLM_P5_MODE || "normal")
    .trim()
    .toLowerCase();
  return P5_MODES.has(value) ? value : "normal";
}

/** Matches one P5 profile from the system prompt; null for P1-P4. */
function detectP5Scenario(systemText) {
  if (systemText.includes(P5_DESIGN_MARKER)) return "design";
  if (systemText.includes(P5_REVIEW_MARKER)) return "review";
  if (P5_DATA_PLAN_MARKERS.some((marker) => systemText.includes(marker))) return "data_plan";
  if (systemText.includes(P5_EXPLORE_MARKER)) return "explore";
  return null;
}

/**
 * Collects the `key=value` / `key: value` identifier pairs of the run
 * instruction (spec §8.3 leaves the labels to the context builder, so both the
 * camelCase keys and the Chinese labels are accepted).
 */
function p5LabelledValues(text) {
  const values = new Map();
  const pattern =
    /([A-Za-z][A-Za-z0-9_]*|版本|系统|环境|运行|需求)\s*(?:ID|Id|id)?\s*[:=：]\s*"?([0-9]{1,20})"?(?![0-9])/g;
  let match = pattern.exec(text);
  while (match !== null) {
    const key = match[1].toLowerCase();
    if (!values.has(key)) values.set(key, match[2]);
    match = pattern.exec(text);
  }
  // Tolerate whitespace instead of a separator ("versionId 123", "版本 123").
  const spaced =
    /([A-Za-z][A-Za-z0-9_]*|版本|系统|环境|运行)\s*\(?(?:ID|Id|id)?\)?\s+(\d{1,20})(?![0-9])/g;
  match = spaced.exec(text);
  while (match !== null) {
    const key = match[1].toLowerCase();
    if (!values.has(key)) values.set(key, match[2]);
    match = spaced.exec(text);
  }
  return values;
}

function p5LabelledId(values, patterns) {
  for (const [key, value] of values) {
    if (patterns.some((pattern) => pattern.test(key))) return value;
  }
  return "";
}

/** All long decimal tokens (snowflake-like ids) of the instruction. */
function p5LongIds(text) {
  const matches = text.match(/\b\d{10,}\b/g);
  return matches === null ? [] : matches;
}

function p5Unique(values) {
  const seen = new Set();
  const out = [];
  for (const value of values) {
    if (seen.has(value)) continue;
    seen.add(value);
    out.push(value);
  }
  return out;
}

/** Depth-bounded lookup of the first scalar/array value for one JSON key. */
function p5FindKey(value, keys, depth) {
  if (depth > 8 || value === null || typeof value !== "object") return null;
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = p5FindKey(item, keys, depth + 1);
      if (found !== null) return found;
    }
    return null;
  }
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(value, key)) {
      const candidate = value[key];
      if (typeof candidate === "string" || typeof candidate === "number" || Array.isArray(candidate)) {
        return candidate;
      }
    }
  }
  for (const child of Object.values(value)) {
    const found = p5FindKey(child, keys, depth + 1);
    if (found !== null) return found;
  }
  return null;
}

/** Collects every node-shaped object of a case_graph_read tool result. */
function p5CollectNodes(value, out, depth) {
  const nodes = out === undefined ? [] : out;
  if (depth > 8 || value === null || typeof value !== "object") return nodes;
  if (Array.isArray(value)) {
    for (const item of value) p5CollectNodes(item, nodes, depth + 1);
    return nodes;
  }
  if (value.id !== undefined && (value.revision !== undefined || value.kind !== undefined)) {
    nodes.push(value);
  }
  for (const child of Object.values(value)) p5CollectNodes(child, nodes, depth + 1);
  return nodes;
}

/** Parses the graph tool results once and returns their case nodes. */
function p5CaseNodes(toolResults) {
  const nodes = [];
  const seen = new Set();
  for (const result of toolResults) {
    const parsed = parseJsonObject(result.text);
    if (parsed === null) continue;
    for (const node of p5CollectNodes(parsed)) {
      if (node.kind !== "case" || node.id === undefined) continue;
      const id = String(node.id);
      if (seen.has(id)) continue;
      seen.add(id);
      nodes.push(node);
    }
  }
  return nodes;
}

/**
 * Resolves the version id from the instruction labels/URL, then from the
 * case_graph_read result and finally from the first snowflake-like id.
 */
function p5VersionId(userText, toolResults) {
  const values = p5LabelledValues(userText);
  const labelled = p5LabelledId(values, [/version/, /版本/]);
  if (labelled !== "") return labelled;

  const pathMatch = /\/versions\/(\d+)/.exec(userText);
  if (pathMatch) return pathMatch[1];

  for (const result of toolResults) {
    const parsed = parseJsonObject(result.text);
    if (parsed === null) continue;
    const found = p5FindKey(parsed, ["versionId"], 0);
    if (found !== null && /^\d+$/.test(String(found))) return String(found);
  }

  const longIds = p5LongIds(userText);
  return longIds.length > 0 ? longIds[0] : "";
}

/**
 * Resolves the confirmed requirement ids from the explicit, labelled sources of
 * the run instruction only: a `requirementIds=[...]` list or the
 * `## 已确认需求` table of the T10 context builder
 * (`backend/internal/mindmap/application/run_service.go`, which emits
 * `- id=<id> title="..." priority=... status=...` lines; the legacy
 * pipe-separated table is still tolerated). The fake must never fabricate ids
 * (spec §8.1): without an explicit list/table it returns an empty array so the
 * server-side unconfirmed_requirements validation fails loudly instead of
 * covering a made-up requirement.
 */
function p5RequirementIds(userText) {
  const listMatch = /(?:requirementIds?|需求\s*(?:ID|id|Ids)?)\s*[:=：]\s*\[([^\]]*)\]/i.exec(userText);
  if (listMatch) {
    const ids = p5Unique(listMatch[1].match(/\d{1,20}/g) || []);
    if (ids.length > 0) return ids;
  }

  // Stop at the next heading so the relation section (`- id=... from=...`) can
  // never leak into the requirement list.
  const section = /(?:已确认需求|确认需求|需求清单|requirements)[^\n]*\n([\s\S]*?)(?:\n[ \t]*\n|\n#{1,6}\s|$)/i.exec(
    userText,
  );
  if (section === null) return [];
  const ids = [];
  for (const line of section[1].split("\n")) {
    const lineMatch = /^\s*[*-]?\s*(?:id\s*[:=：]\s*)?"?(\d{1,20})"?(?=\s|[|｜]|$)/.exec(line);
    if (lineMatch) ids.push(lineMatch[1]);
  }
  return p5Unique(ids);
}

/** One requirement source per case; the second case reuses the first when needed. */
function p5CaseRequirementIds(requirementIds, index) {
  if (requirementIds.length === 0) return [];
  return [requirementIds[Math.min(index, requirementIds.length - 1)]];
}

/** Valid §6.7 proposal: one scenario plus two cases, two steps and one assertion. */
function p5DesignProposal(versionId, requirementIds) {
  const steps = [
    { action: "打开目标页面", expected: "页面正常展示", data: "" },
    { action: "提交核心操作", expected: "操作成功并给出提示", data: "" },
  ];
  return {
    versionId,
    mode: "append",
    scenarios: [
      {
        ref: "s1",
        title: "P5 场景：核心业务流程",
        parentRef: null,
        description: "fake LLM P5 设计场景",
      },
    ],
    cases: [
      {
        ref: "c1",
        scenarioRef: "s1",
        title: "P5 正常主流程用例",
        priority: "P1",
        requirementIds: p5CaseRequirementIds(requirementIds, 0),
        preconditions: "已登录且测试数据已准备",
        testData: { source: "p5-fake", account: "fixture" },
        steps: steps.map((step) => ({ ...step })),
        assertions: [
          {
            stepIndex: 1,
            kind: "ui",
            target: "/",
            expression: "成功提示可见",
            expected: "展示成功提示",
          },
        ],
        anchors: [],
      },
      {
        ref: "c2",
        scenarioRef: "s1",
        title: "P5 异常边界用例",
        priority: "P2",
        requirementIds: p5CaseRequirementIds(requirementIds, 1),
        preconditions: "已登录且请求参数非法",
        testData: { source: "p5-fake", invalidField: "amount" },
        steps: steps.map((step) => ({ ...step })),
        assertions: [
          {
            stepIndex: 0,
            kind: "api",
            target: "/api/orders",
            expression: "返回 400",
            expected: "接口拒绝非法输入",
          },
        ],
        anchors: [],
      },
    ],
  };
}

/**
 * Structurally invalid §6.7 proposal used by the `invalid` mode: duplicate
 * scenario/case refs, missing requirement source, unknown priority, out-of-range
 * stepIndex, unknown assertion kind and a fabricated requirement id. The server
 * must reject it and materialize nothing (spec §6.7/§10.7 check 7).
 */
function p5InvalidProposal(versionId) {
  return {
    versionId,
    mode: "append",
    scenarios: [
      { ref: "s1", title: "P5 非法场景", parentRef: null, description: "" },
      { ref: "s1", title: "P5 重复场景 ref", parentRef: null, description: "" },
    ],
    cases: [
      {
        ref: "dup",
        scenarioRef: "s1",
        title: "P5 非法用例（缺少需求来源）",
        priority: "P9",
        requirementIds: [],
        preconditions: "",
        testData: null,
        steps: [{ action: "", expected: "" }],
        assertions: [
          {
            stepIndex: 9,
            kind: "bogus",
            target: "",
            expression: "",
            expected: "",
          },
        ],
      },
      {
        ref: "dup",
        scenarioRef: "missing_scenario",
        title: "P5 非法用例（重复 ref 与编造需求）",
        priority: "P1",
        requirementIds: [P5_INVALID_REQUIREMENT_ID],
        steps: [],
        assertions: [],
      },
    ],
  };
}

/** §8.2 case.review_submit payload: coverage/assertion/duplicate findings + one blocker. */
function p5ReviewProposal(versionId, toolResults) {
  const caseNodes = p5CaseNodes(toolResults);
  const findings = [
    {
      category: "coverage",
      severity: "major",
      detail: "P5 评审：已确认需求存在覆盖遗漏，请补充未覆盖需求对应的用例。",
    },
    {
      category: "assertion",
      severity: "major",
      detail: "P5 评审：用例缺少对关键预期的步骤级断言，无法验证操作结果。",
    },
    {
      category: "duplicate",
      severity: "minor",
      detail: "P5 评审：两条用例的步骤高度重复，建议合并或明确边界差异。",
    },
    {
      category: "code_uncovered",
      severity: "blocker",
      detail: "P5 评审：关键代码路径缺少用例覆盖，需补充对应用例后再批准。",
    },
  ];
  const attach = (category, node) => {
    const finding = findings.find((item) => item.category === category);
    if (finding) finding.nodeId = String(node.id);
  };
  if (caseNodes.length > 0) {
    attach("coverage", caseNodes[0]);
    attach("assertion", caseNodes[0]);
  }
  if (caseNodes.length > 1) attach("duplicate", caseNodes[1]);
  return { versionId, findings };
}

/** §8.2 case.data_plan_submit payload for the nodes returned by graph_read. */
function p5DataPlanProposal(versionId, toolResults) {
  const items = [];
  for (const node of p5CaseNodes(toolResults)) {
    if (items.length >= 3) break;
    const revision = Number.isInteger(node.revision)
      ? node.revision
      : Number.parseInt(String(node.revision), 10);
    items.push({
      nodeId: String(node.id),
      nodeRevision: Number.isFinite(revision) && revision >= 1 ? revision : 1,
      preconditions: "P5 fake 前置：使用测试账号登录并准备订单测试数据",
      testData: { source: "p5-fake", orderId: "P5-ORDER-1" },
    });
  }
  return { versionId, items };
}

/** §8.2 app_model_propose payload with one page, one route and one api node. */
function p5AppModelProposal(systemId) {
  const proposal = {
    nodes: [
      { key: "page:/p5/home", kind: "page", name: "P5 Home", path: "/p5/home" },
      { key: "route:/p5/orders", kind: "route", name: "P5 Orders", path: "/p5/orders" },
      {
        key: "api:GET /api/orders",
        kind: "api",
        name: "GET /api/orders",
        path: "/api/orders",
        method: "GET",
      },
    ],
  };
  if (systemId !== "") proposal.systemId = String(systemId);
  return proposal;
}

/** Resolves the exploration starting origin from the instruction or the env. */
function p5ExploreBaseUrl(userText) {
  const match = /https?:\/\/[^\s"'<>，。；;）)】]+/.exec(userText);
  if (match) {
    const candidate = match[0].replace(/[),.;，。；）)】]+$/g, "");
    try {
      return new URL(candidate).origin;
    } catch {
      // Fall through to the configured fixture.
    }
  }
  const configured = String(process.env.EZTEST_FAKE_LLM_P5_EXPLORE_BASE_URL || "").trim();
  if (configured !== "") return configured.replace(/\/+$/, "");
  const port = String(process.env.FIXTURE_PORT || process.env.EZTEST_E2E_FIXTURE_PORT || "13335");
  return "http://127.0.0.1:" + port;
}

function p5JoinUrl(base, pathValue) {
  return base.replace(/\/+$/, "") + pathValue;
}

/** Resolves the P2 system id of an exploration instruction. */
function p5SystemId(userText) {
  const values = p5LabelledValues(userText);
  const labelled = p5LabelledId(values, [/system/, /系统/]);
  if (labelled !== "") return labelled;
  const match = /(?:系统|system)[^\n]{0,40}?(?:id|ID|Id)?\s*[:=：]?\s*(\d{1,20})/.exec(userText);
  return match ? match[1] : "";
}

/** Describes how many tool turns the run already made and their results. */
function describeP5Run(messages, lastUserIndex) {
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
  return { step, toolResults };
}

function p5Tool(name, args, reasoning) {
  return { kind: "tool", name, arguments: JSON.stringify(args), reasoning };
}

function p5Text(text) {
  return { kind: "text", text };
}

/** Plans exactly one P5 turn from the role, the run state and the mode. */
function planP5Turn(kind, run, userText, mode) {
  // Degraded is a successful completion (200 + text only, no tool call), not an
  // upstream LLM failure, so the backend records a degraded run (T16/T26).
  if (mode === "degraded") return p5Text(P5_DEGRADED_TEXT);

  if (kind === "explore") {
    const baseUrl = p5ExploreBaseUrl(userText);
    if (run.step < P5_EXPLORE_PATHS.length) {
      const pathValue = P5_EXPLORE_PATHS[run.step];
      return p5Tool(
        "explore_http",
        { url: p5JoinUrl(baseUrl, pathValue), method: "GET" },
        "P5 探索：只读探测 " + pathValue,
      );
    }
    if (run.step === P5_EXPLORE_PATHS.length) {
      return p5Tool(
        "app_model_propose",
        p5AppModelProposal(p5SystemId(userText)),
        "P5 探索：提交发现的页面/路由/接口",
      );
    }
    return p5Text(P5_FINAL_TEXTS.explore);
  }

  const versionId = p5VersionId(userText, run.toolResults);

  if (kind === "design") {
    if (run.step === 0) {
      if (versionId === "") return p5Text(P5_NO_VERSION_TEXT);
      return p5Tool("case_graph_read", { versionId }, "P5 设计：读取现有脑图摘要");
    }
    if (run.step === 1) {
      if (versionId === "") return p5Text(P5_NO_VERSION_TEXT);
      const proposal =
        mode === "invalid"
          ? p5InvalidProposal(versionId)
          : p5DesignProposal(versionId, p5RequirementIds(userText));
      return p5Tool("case_graph_propose", proposal, "P5 设计：提交场景与用例提案");
    }
    return p5Text(P5_FINAL_TEXTS.design);
  }

  if (kind === "review") {
    if (run.step === 0) {
      if (versionId === "") return p5Text(P5_NO_VERSION_TEXT);
      return p5Tool("case_graph_read", { versionId }, "P5 评审：读取现有脑图摘要");
    }
    if (run.step === 1) {
      if (versionId === "") return p5Text(P5_NO_VERSION_TEXT);
      return p5Tool(
        "case_review_submit",
        p5ReviewProposal(versionId, run.toolResults),
        "P5 评审：提交覆盖/重复/代码未覆盖 findings",
      );
    }
    return p5Text(P5_FINAL_TEXTS.review);
  }

  if (run.step === 0) {
    if (versionId === "") return p5Text(P5_NO_VERSION_TEXT);
    return p5Tool("case_graph_read", { versionId }, "P5 数据规划：读取现有脑图摘要");
  }
  if (run.step === 1) {
    if (versionId === "") return p5Text(P5_NO_VERSION_TEXT);
    return p5Tool(
      "case_data_plan_submit",
      p5DataPlanProposal(versionId, run.toolResults),
      "P5 数据规划：提交前置条件与测试数据",
    );
  }
  return p5Text(P5_FINAL_TEXTS.data_plan);
}

/**
 * Handles one P5 mindmap request. Returns true when the system prompt carried a
 * P5 role marker, false so the P1-P4 behaviors stay untouched.
 */
async function handleP5Scenario(messages, res, stream) {
  const kind = detectP5Scenario(systemPromptText(messages));
  if (kind === null) return false;

  const lastUser = findLastUserMessage(messages);
  const run = lastUser ? describeP5Run(messages, lastUser.index) : { step: 0, toolResults: [] };
  const plan = lastUser ? planP5Turn(kind, run, lastUser.text, p5Mode()) : p5Text(P5_NO_VERSION_TEXT);

  if (plan.kind === "tool") {
    const message = {
      content: null,
      reasoning_content: plan.reasoning,
      tool_calls: toolCallPayload(plan.name, plan.arguments, `p5_call_${run.step + 1}`),
    };
    if (stream) {
      await streamToolTurn(res, message, P5_TOOL_USAGE);
      return true;
    }
    sendJson(res, 200, completionBody(message, "tool_calls", P5_TOOL_USAGE));
    return true;
  }

  if (stream) {
    await streamTextTurn(res, plan.text, P5_TEXT_USAGE);
    return true;
  }
  sendJson(res, 200, completionBody({ content: plan.text }, "stop", P5_TEXT_USAGE));
  return true;
}

// --- P6 deterministic execution scenarios (design spec §10.2) --------------
//
// P6 requests are identified by the stable Chinese system-prompt marker of the
// execution-agent profile (`backend/internal/execution/domain/profile.go`,
// spec §7.2). Every run is scripted from the number of assistant tool turns
// already present after the run instruction (same counting trick as P3/P5):
//
//   normal   case_context -> goto (/p6/orders/new) -> snapshot -> fill -> click
//            -> screenshot -> step_record xN -> assertion_record xM -> case_finish(passed)
//            (P7: a kind=db assertion uses execution_db_assert instead of
//            assertion_record; the server reads SQL/expected/target and records
//            the db_result evidence, spec §9.5)
//   fail     case_context -> goto (/p6/assert-fail) -> snapshot -> screenshot
//            -> step_record xN -> assertion_record xM(failed) -> case_finish(failed, application)
//   blocked  case_context -> goto (/p6/requires-auth) -> snapshot -> screenshot
//            -> records -> case_finish(blocked, environment)
//   takeover case_context -> goto (/p6/login?mfa=1) -> snapshot -> screenshot
//            -> records -> case_finish(blocked, session, reason=mfa_required)
//   budget   case_context -> two slow goto turns (pause window) -> snapshots forever
//
// The recorder validates step/assertion completeness and evidence, so the
// blocked takeover scenarios also record every step/assertion before finishing.
// The browser screenshot tool is the evidence source: its `evidence_id` is
// reused in the step/assertion/finish submissions.
//
// Modes: normal (default) | fail | blocked | takeover | budget.
// `POST /__p6-mode` switches the mode at runtime; every mode answers through
// the same system prompt so the E2E can switch without restarting the fake.
const P6_EXECUTION_MARKER = "EzTest 的执行 Agent";
const P6_MODES = new Set(["normal", "fail", "blocked", "takeover", "budget"]);
const P6_TOOL_USAGE = makeUsage(960, 220, 96);
const P6_TEXT_USAGE = makeUsage(220, 24, 0);
const P6_FINAL_TEXT = "P6 执行结束：已提交用例的步骤、断言与最终结果。";
const P6_EMPTY_CONTEXT_TEXT = "P6 执行结束：未能读取用例上下文，本轮未提交任何结果。";
const P6_ORDER_FORM_PATH = "/p6/orders/new";
const P6_FAIL_PATH = "/p6/assert-fail";
const P6_REQUIRES_AUTH_PATH = "/p6/requires-auth";
const P6_MFA_PATH = "/p6/login?mfa=1";

/** Test-only runtime control of EZTEST_FAKE_LLM_P6_MODE (spec §10.2). */
async function handleP6ModeControl(req, res) {
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
  const mode = String(body.mode || "")
    .trim()
    .toLowerCase();
  if (!P6_MODES.has(mode)) {
    sendError(res, 400, "mode must be one of normal, fail, blocked, takeover, budget");
    return;
  }
  process.env.EZTEST_FAKE_LLM_P6_MODE = mode;
  sendJson(res, 200, { mode: p6Mode() });
}

function p6Mode() {
  const value = String(process.env.EZTEST_FAKE_LLM_P6_MODE || "normal")
    .trim()
    .toLowerCase();
  return P6_MODES.has(value) ? value : "normal";
}

/** Matches the execution-agent system prompt; null for P1-P5. */
function detectP6Scenario(systemText) {
  return systemText.includes(P6_EXECUTION_MARKER) ? "execution" : null;
}

/** Appends the compact P6 request line (never the prompt or case data). */
function recordP6Request(mode, step) {
  const entry = { p6: true, mode, step };
  try {
    fs.appendFileSync(REQUEST_LOG_PATH, JSON.stringify(entry) + "\n", "utf8");
  } catch (error) {
    console.error("fake-llm: failed to append the request log: " + error.message);
  }
}

/** Parses one tool result JSON object; null when it is not an object. */
function p6ParseObject(text) {
  try {
    const parsed = JSON.parse(text);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

/** Returns the latest parsed execution.case_context result, or null. */
function p6CaseContext(toolResults) {
  for (let index = toolResults.length - 1; index >= 0; index -= 1) {
    if (toolResults[index].name !== "execution_case_context") continue;
    const parsed = p6ParseObject(toolResults[index].text);
    if (parsed !== null) return parsed;
  }
  return null;
}

/** Returns the latest screenshot evidence id, or "". */
function p6EvidenceId(toolResults) {
  for (let index = toolResults.length - 1; index >= 0; index -= 1) {
    if (toolResults[index].name !== "browser_screenshot") continue;
    const parsed = p6ParseObject(toolResults[index].text);
    if (parsed !== null && typeof parsed.evidence_id === "string" && /^\d+$/.test(parsed.evidence_id)) {
      return parsed.evidence_id;
    }
  }
  return "";
}

function p6Tool(name, args, reasoning) {
  return { kind: "tool", name, arguments: JSON.stringify(args), reasoning };
}

function p6Text(text) {
  return { kind: "text", text };
}

function p6JoinUrl(base, pathValue) {
  return String(base || "").replace(/\/+$/, "") + pathValue;
}

/** Builds one browser.goto plan for the fixture page of the mode. */
function p6GotoPlan(mode, baseUrl) {
  const pathValue =
    mode === "fail"
      ? P6_FAIL_PATH
      : mode === "blocked"
        ? P6_REQUIRES_AUTH_PATH
        : mode === "takeover"
          ? P6_MFA_PATH
          : P6_ORDER_FORM_PATH;
  return p6Tool(
    "browser_goto",
    { url: p6JoinUrl(baseUrl, pathValue) },
    "P6 执行：打开 " + pathValue,
  );
}

/**
 * Maps the latest execution.visual_assert tool result of the trailing run onto
 * the finish decision the visual-assertion prompt rule prescribes. It returns
 * null when the run has not called the visual tool yet, so every existing P6
 * scenario keeps its mode-driven finish unchanged.
 */
function p6VisualFinish(toolResults) {
  let text = "";
  for (let index = toolResults.length - 1; index >= 0; index -= 1) {
    if (toolResults[index].name !== "execution_visual_assert") continue;
    text = toolResults[index].text;
    break;
  }
  if (text === "") return null;
  if (text.includes("missing_visual_baseline")) {
    return { verdict: "inconclusive", failure_class: "case", reason: "missing_visual_baseline" };
  }
  if (text.includes("visual_environment_mismatch")) {
    return {
      verdict: "inconclusive",
      failure_class: "environment",
      reason: "visual_environment_mismatch",
    };
  }
  const parsed = p6ParseObject(text);
  if (parsed !== null && parsed.classification === "failed") {
    return {
      verdict: "failed",
      failure_class: "application",
      reason: typeof parsed.reason === "string" ? parsed.reason : "visual_mismatch",
    };
  }
  if (parsed !== null && parsed.classification === "passed") {
    return { verdict: "passed", reason: "视觉断言通过" };
  }
  return {
    verdict: "inconclusive",
    failure_class: "case",
    reason: (parsed !== null && typeof parsed.reason === "string" && parsed.reason) ||
      "visual_assertion_inconclusive",
  };
}

/** Plans exactly one P6 turn from the mode and the already executed turns. */
function planP6Turn(mode, run) {
  if (run.step === 0) {
    return p6Tool("execution_case_context", {}, "P6 执行：读取用例快照与恢复检查点");
  }

  const context = p6CaseContext(run.toolResults);
  if (context === null) return p6Text(P6_EMPTY_CONTEXT_TEXT);

  const finished = run.toolResults.some((result) => result.name === "execution_case_finish");
  if (finished) return p6Text(P6_FINAL_TEXT);

  const baseUrl = typeof context.base_url === "string" ? context.base_url : "";
  if (mode === "budget") {
    // Two slow navigations create a deterministic in-flight window for the
    // immediate-pause E2E, then rapid snapshots exhaust the tool-call budget.
    if (run.step === 1 || run.step === 2) {
      return p6Tool(
        "browser_goto",
        { url: p6JoinUrl(baseUrl, "/p6/slow?ms=8000") },
        "P6 预算场景：慢响应以验证暂停",
      );
    }
    return p6Tool("browser_snapshot", {}, "P6 预算场景：继续调用工具");
  }

  const evidenceId = p6EvidenceId(run.toolResults);
  const steps = (Array.isArray(context.remaining_steps) ? context.remaining_steps : []).filter(
    (step) => step && Number.isInteger(step.seq) && step.seq > 0,
  );
  const assertions = (Array.isArray(context.assertions) ? context.assertions : []).filter(
    (assertion) => assertion && typeof assertion.assertion_id === "string",
  );

  // The normal scenario exercises the fixture's create-order form; the other
  // modes only need a page whose content supports the intended verdict.
  const prepared =
    mode === "normal"
      ? [
          p6GotoPlan(mode, baseUrl),
          p6Tool("browser_snapshot", {}, "P6 执行：读取订单表单的 ARIA 快照"),
          p6Tool(
            "browser_fill",
            { target: { selector: "#order-number" }, value: "P6-E2E-ORDER" },
            "P6 执行：填写订单号",
          ),
          p6Tool(
            "browser_click",
            { target: { role: "button", name: "Create order" } },
            "P6 执行：提交新建订单",
          ),
          p6Tool("browser_screenshot", {}, "P6 执行：截图作为断言证据"),
        ]
      : [
          p6GotoPlan(mode, baseUrl),
          p6Tool("browser_snapshot", {}, "P6 执行：读取页面 ARIA 快照"),
          p6Tool("browser_screenshot", {}, "P6 执行：截图作为断言证据"),
        ];

  const phase = run.step - 1;
  if (phase < prepared.length) return prepared[phase];
  let index = phase - prepared.length;

  if (index < steps.length) {
    const step = steps[index];
    if (evidenceId === "") return p6Text(P6_EMPTY_CONTEXT_TEXT);
    return p6Tool(
      "execution_step_record",
      {
        seq: step.seq,
        status: "passed",
        actual: "步骤执行成功",
        evidence_ids: [evidenceId],
      },
      "P6 执行：记录步骤 " + step.seq,
    );
  }
  index -= steps.length;

  if (index < assertions.length) {
    const assertion = assertions[index];
    // P7 addition (spec §9.5): a kind=db snapshot assertion is executed by the
    // server-side execution.db_assert tool, not by execution.assertion_record.
    // The agent sends only the assertion id; SQL, expected value and the target
    // stay on the server. The tool itself records the db_result evidence.
    if (assertion.kind === "db") {
      return p6Tool(
        "execution_db_assert",
        { assertionId: assertion.assertion_id },
        "P7 执行：执行服务器端 db 断言 " + assertion.assertion_id,
      );
    }
    // P8 visual assertions (design spec §9.3) are executed by the server-owned
    // execution.visual_assert tool: the agent sends only the assertion id and
    // the recorded result/evidence drives the final case verdict below.
    if (assertion.kind === "visual") {
      return p6Tool(
        "execution_visual_assert",
        { assertionId: assertion.assertion_id },
        "P8 执行：执行视觉断言 " + assertion.assertion_id,
      );
    }
    if (evidenceId === "") return p6Text(P6_EMPTY_CONTEXT_TEXT);
    const failed = mode === "fail";
    return p6Tool(
      "execution_assertion_record",
      {
        assertion_id: assertion.assertion_id,
        verdict: failed ? "failed" : "passed",
        actual: failed ? "页面实际文案与用例期望不一致" : "断言通过：页面文案与期望一致",
        evidence_ids: [evidenceId],
      },
      "P6 执行：记录断言 " + assertion.assertion_id,
    );
  }
  index -= assertions.length;

  if (index === 0) {
    if (evidenceId === "") return p6Text(P6_EMPTY_CONTEXT_TEXT);
    // A visual assertion owns the verdict: a missing baseline is
    // inconclusive(case), an environment mismatch inconclusive(environment), a
    // pixel diff over the threshold failed(application), otherwise passed.
    const visualFinish = p6VisualFinish(run.toolResults);
    if (visualFinish !== null) {
      if (visualFinish.verdict === "passed") {
        return p6Tool(
          "execution_case_finish",
          {
            verdict: "passed",
            reason: visualFinish.reason,
            evidence_ids: [evidenceId],
          },
          "P8 执行：视觉断言通过",
        );
      }
      if (visualFinish.verdict === "failed") {
        return p6Tool(
          "execution_case_finish",
          {
            verdict: "failed",
            failure_class: "application",
            reason: visualFinish.reason,
            evidence_ids: [evidenceId],
          },
          "P8 执行：视觉断言不一致",
        );
      }
      return p6Tool(
        "execution_case_finish",
        {
          verdict: "inconclusive",
          failure_class: visualFinish.failure_class,
          reason: visualFinish.reason,
        },
        "P8 执行：视觉断言按分类结束",
      );
    }
    if (mode === "fail") {
      return p6Tool(
        "execution_case_finish",
        { verdict: "failed", failure_class: "application", reason: "断言失败：页面文案与期望不一致" },
        "P6 执行：用例判定为失败（应用缺陷）",
      );
    }
    if (mode === "blocked") {
      return p6Tool(
        "execution_case_finish",
        { verdict: "blocked", failure_class: "environment", reason: "环境不可达：fixture 目标请求失败" },
        "P6 执行：用例判定为阻塞（环境）",
      );
    }
    if (mode === "takeover") {
      return p6Tool(
        "execution_case_finish",
        { verdict: "blocked", failure_class: "session", reason: "mfa_required：会话需要人工接管" },
        "P6 执行：用例判定为阻塞（会话）",
      );
    }
    return p6Tool(
      "execution_case_finish",
      {
        verdict: "passed",
        reason: "全部步骤与断言通过",
        evidence_ids: [evidenceId],
      },
      "P6 执行：用例判定为通过",
    );
  }

  return p6Text(P6_FINAL_TEXT);
}

/**
 * Handles one P6 execution request. Returns true when the system prompt carried
 * the execution-agent marker, false so P1-P5 stay untouched.
 */
async function handleP6Scenario(messages, res, stream) {
  const kind = detectP6Scenario(systemPromptText(messages));
  if (kind === null) return false;

  const lastUser = findLastUserMessage(messages);
  const run = lastUser ? describeP5Run(messages, lastUser.index) : { step: 0, toolResults: [] };
  const mode = p6Mode();
  recordP6Request(mode, run.step);

  const plan = lastUser ? planP6Turn(mode, run) : p6Text(P6_EMPTY_CONTEXT_TEXT);
  if (plan.kind === "tool") {
    const message = {
      content: null,
      reasoning_content: plan.reasoning,
      tool_calls: toolCallPayload(plan.name, plan.arguments, `p6_call_${run.step + 1}`),
    };
    if (stream) {
      await streamToolTurn(res, message, P6_TOOL_USAGE);
      return true;
    }
    sendJson(res, 200, completionBody(message, "tool_calls", P6_TOOL_USAGE));
    return true;
  }

  if (stream) {
    await streamTextTurn(res, plan.text, P6_TEXT_USAGE);
    return true;
  }
  sendJson(res, 200, completionBody({ content: plan.text }, "stop", P6_TEXT_USAGE));
  return true;
}

// --- P7 autonomous-loop scenarios (design spec §9.1-§9.5, §12.5) -----------
//
// P7 requests are identified by the stable Chinese system-prompt markers of the
// three seeded P7 profiles (`backend/internal/mindmap/domain/profile_supplement.go`
// and `backend/internal/delivery/domain/profile.go`). Every run is scripted from
// the number of assistant tool turns already present after the run instruction
// (the same counting trick as P3/P5/P6) and from the ids the backend returned by
// the read tools, so proposals always cite real ids and never invent any:
//
//   supplement  context_read -> case_propose -> [affected_cases_propose] -> finish
//   failure     case_list -> case_detail xN -> evidence_meta -> candidate_propose -> finish
//   triage      candidate_list -> issue_list -> context_read -> issue_draft -> finish
//
// affected_cases_propose only runs for incremental_design runs: the backend
// rejects it for post_batch supplement runs (spec §9.2).
//
// Modes (default normal), switched at runtime by POST /__p7-mode:
//   noop      read once then finish without proposals (no supplement batch,
//             no failure candidate, no issue draft)
//   degraded  one text-only turn; a successful completion, not an upstream
//             failure (the backend records a degraded run, not a failed one)
//   invalid   the proposal call carries malformed JSON arguments so the tool
//             layer answers invalid_arguments and the run degrades
//
// Every P7 id emitted here is a decimal string (D29) and every tool call name is
// the P1 gateway's dot->underscore wire form (`supplement.case_propose` ->
// `supplement_case_propose`), exactly like P5/P6.
const P7_SUPPLEMENT_MARKER = "EzTest 的补全 Agent";
const P7_FAILURE_MARKER = "EzTest 的失败分析 Agent";
const P7_TRIAGE_MARKER = "EzTest 的问题整理 Agent";
const P7_MODES = new Set(["normal", "noop", "degraded", "invalid"]);
const P7_TOOL_USAGE = makeUsage(900, 180, 64);
const P7_TEXT_USAGE = makeUsage(200, 24, 0);
const P7_DEGRADED_TEXT = "P7 降级：LLM 不可用，未提交任何结构化结果，已按降级路径结束。";
const P7_INVALID_TEXT = "P7 提案参数无效：工具返回 invalid_arguments，本轮按降级结束。";
const P7_EMPTY_TEXT = "P7 无法读取运行指令，本轮未提交任何提案。";
const P7_FINISH_TEXTS = {
  supplement: "P7 补全完成：已提交新增用例与受影响用例建议。",
  supplementNoop: "P7 补全结束：本轮没有发现需要补全的用例。",
  failure: "P7 失败分析完成：已提交失败候选。",
  failureNoop: "P7 失败分析结束：本批次没有可提交的失败候选。",
  triage: "P7 问题整理完成：已提交问题单草稿。",
  triageNoop: "P7 问题整理结束：本批次没有可提交的问题单草稿。",
};
// Deliberately malformed JSON arguments for the invalid mode: the backend tool
// decoder must answer invalid_arguments instead of proposing anything.
const P7_INVALID_SUPPLEMENT_ARGS = '{"cases":[{"title":"P7 无效提案"';
const P7_INVALID_FAILURE_ARGS = '{"candidates":[{"caseRunId":"7301"';
const P7_INVALID_TRIAGE_ARGS = '{"issues":[{"title":"P7 无效草稿"';
const P7_CODE_REFS = [
  {
    path: "backend/internal/order/service.go",
    line: 42,
    reason: "P7 契约测试：失败分支位于下单服务的此处",
  },
];

/** Test-only runtime control of EZTEST_FAKE_LLM_P7_MODE (spec §12.5). */
async function handleP7ModeControl(req, res) {
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
  const mode = String(body.mode || "")
    .trim()
    .toLowerCase();
  if (!P7_MODES.has(mode)) {
    sendError(res, 400, "mode must be one of normal, noop, degraded, invalid");
    return;
  }
  process.env.EZTEST_FAKE_LLM_P7_MODE = mode;
  sendJson(res, 200, { mode: p7Mode() });
}

function p7Mode() {
  const value = String(process.env.EZTEST_FAKE_LLM_P7_MODE || "normal")
    .trim()
    .toLowerCase();
  return P7_MODES.has(value) ? value : "normal";
}

/** Matches one P7 profile from the system prompt; null for P1-P6. */
function detectP7Scenario(systemText) {
  if (systemText.includes(P7_SUPPLEMENT_MARKER)) return "supplement";
  if (systemText.includes(P7_FAILURE_MARKER)) return "failure";
  if (systemText.includes(P7_TRIAGE_MARKER)) return "triage";
  return null;
}

/** Appends the compact P7 request line (never the prompt or run data). */
function recordP7Request(profile, mode, step) {
  const entry = { p7: true, profile, mode, step };
  try {
    fs.appendFileSync(REQUEST_LOG_PATH, JSON.stringify(entry) + "\n", "utf8");
  } catch (error) {
    console.error("fake-llm: failed to append the request log: " + error.message);
  }
}

function p7Tool(name, args, reasoning) {
  return { kind: "tool", name, arguments: JSON.stringify(args), reasoning };
}

/** Builds a tool turn with raw (possibly malformed) argument text. */
function p7RawTool(name, argumentsText, reasoning) {
  return { kind: "tool", name, arguments: argumentsText, reasoning };
}

function p7Text(text) {
  return { kind: "text", text };
}

/** Returns the latest parsed tool result with the given wire name, or null. */
function p7LatestResult(toolResults, name) {
  for (let index = toolResults.length - 1; index >= 0; index -= 1) {
    if (toolResults[index].name !== name) continue;
    const parsed = parseJsonObject(toolResults[index].text);
    if (parsed !== null) return parsed;
  }
  return null;
}

function p7HasResult(toolResults, name) {
  return toolResults.some((result) => result.name === name);
}

/** Reads one `key=value` line of the run instruction. */
function p7InstructionValue(userText, key) {
  const match = new RegExp("(?:^|\\n)" + key + "=([^\\n]+)").exec(userText);
  return match ? match[1].trim() : "";
}

/** Keeps only positive decimal string ids from an arbitrary array. */
function p7StringIds(value) {
  if (!Array.isArray(value)) return [];
  const ids = [];
  for (const item of value) {
    if (typeof item === "string" && /^[1-9][0-9]*$/.test(item) && !ids.includes(item)) {
      ids.push(item);
    }
  }
  return ids;
}

/** The run mode carried by the supplement instruction (spec §6.4.1). */
function p7SupplementMode(userText) {
  return p7InstructionValue(userText, "mode") === "incremental_design"
    ? "incremental_design"
    : "post_batch";
}

/** Confirmed requirement ids returned by supplement.context_read. */
function p7ContextRequirementIds(toolResults) {
  const context = p7LatestResult(toolResults, "supplement_context_read");
  if (context === null || !Array.isArray(context.requirements)) return [];
  return p7StringIds(context.requirements.map((requirement) => (requirement ? requirement.id : "")));
}

/** Existing, non-draft case node ids returned by supplement.context_read. */
function p7ContextCaseNodeIds(toolResults) {
  const context = p7LatestResult(toolResults, "supplement_context_read");
  if (context === null || !Array.isArray(context.cases)) return [];
  const usable = context.cases.filter(
    (node) => node && typeof node.nodeId === "string" && node.status !== "draft",
  );
  return p7StringIds(usable.map((node) => node.nodeId));
}

/** One supplement.case_propose payload: 2 cases tied to confirmed requirements. */
function p7SupplementCases(requirementIds) {
  const requirementId = requirementIds[0];
  return [
    {
      title: "P7 补全：下单金额校验",
      description: "补全基线未覆盖的下单金额校验场景。",
      priority: "P1",
      preconditions: "用户已登录且购物车非空",
      requirementIds: [requirementId],
      confidence: "high",
      rationale: "基于已确认需求与覆盖缺口生成。",
      steps: [
        { action: "打开下单页并输入合法金额", expected: "金额校验通过", data: "amount=100" },
        { action: "提交订单", expected: "订单创建成功" },
      ],
      assertions: [
        {
          kind: "ui",
          target: "#order-result",
          expression: "text contains 订单创建成功",
          expected: "订单创建成功",
        },
      ],
    },
    {
      title: "P7 补全：退款幂等校验",
      description: "补全重复退款请求的幂等场景。",
      priority: "P2",
      preconditions: "存在可退款订单",
      requirementIds: [requirementId],
      confidence: "medium",
      rationale: "基于已确认需求生成。",
      steps: [{ action: "连续提交两次退款", expected: "只生成一笔退款", data: "order=P7-1" }],
      assertions: [
        { kind: "api", target: "/api/refunds", expression: "refundCount == 1", expected: "1" },
      ],
    },
  ];
}

/** One supplement.affected_cases_propose payload with code locations. */
function p7AffectedCase(nodeId, requirementIds) {
  const affected = {
    caseNodeId: nodeId,
    confidence: "high",
    reason: "需求变化影响该旧用例的断言与页面路径。",
    source: "code",
    codeRefs: P7_CODE_REFS,
  };
  if (requirementIds.length > 0) affected.requirementIds = [requirementIds[0]];
  return affected;
}

/** Plans one supplement-agent turn. */
function planP7SupplementTurn(run, userText, mode) {
  if (mode === "degraded") return p7Text(P7_DEGRADED_TEXT);

  const runMode = p7SupplementMode(userText);
  if (!p7HasResult(run.toolResults, "supplement_context_read")) {
    return p7Tool("supplement_context_read", { mode: runMode }, "P7 补全：读取需求/用例/覆盖/影响摘要");
  }
  const requirementIds = p7ContextRequirementIds(run.toolResults);

  if (mode === "invalid") {
    if (p7HasResult(run.toolResults, "supplement_case_propose")) return p7Text(P7_INVALID_TEXT);
    return p7RawTool("supplement_case_propose", P7_INVALID_SUPPLEMENT_ARGS, "P7 补全：提交无效提案");
  }
  if (mode === "noop") {
    if (p7HasResult(run.toolResults, "supplement_finish")) return p7Text(P7_FINISH_TEXTS.supplementNoop);
    return p7Tool(
      "supplement_finish",
      { summary: "P7 补全：本轮没有发现需要补全的用例" },
      "P7 补全：结束补全运行",
    );
  }
  if (p7HasResult(run.toolResults, "supplement_finish")) return p7Text(P7_FINISH_TEXTS.supplement);

  if (!p7HasResult(run.toolResults, "supplement_case_propose")) {
    if (requirementIds.length === 0) {
      return p7Tool(
        "supplement_finish",
        { summary: "P7 补全：缺少已确认需求，未提交提案" },
        "P7 补全：信息不足，结束运行",
      );
    }
    return p7Tool(
      "supplement_case_propose",
      { cases: p7SupplementCases(requirementIds) },
      "P7 补全：提交 1-2 条新增用例",
    );
  }

  if (runMode === "incremental_design" && !p7HasResult(run.toolResults, "supplement_affected_cases_propose")) {
    const nodeIds = p7ContextCaseNodeIds(run.toolResults);
    if (nodeIds.length > 0) {
      return p7Tool(
        "supplement_affected_cases_propose",
        { cases: [p7AffectedCase(nodeIds[0], requirementIds)] },
        "P7 增量设计：提交受影响的旧用例",
      );
    }
  }

  return p7Tool(
    "supplement_finish",
    { summary: "P7 补全完成：已提交新增用例与受影响用例建议" },
    "P7 补全：结束补全运行",
  );
}

/** Returns the latest analysis.case_detail result of one case run, or null. */
function p7CaseDetail(toolResults, caseRunId) {
  for (let index = toolResults.length - 1; index >= 0; index -= 1) {
    if (toolResults[index].name !== "analysis_case_detail") continue;
    const parsed = parseJsonObject(toolResults[index].text);
    if (parsed !== null && parsed.caseRunId === caseRunId) return parsed;
  }
  return null;
}

/** Builds the failure candidates from the case details read so far. */
function p7FailureCandidates(details) {
  const candidates = [];
  for (const detail of details) {
    const evidenceIds = p7StringIds(detail.evidenceIds);
    if (evidenceIds.length === 0) continue;
    const title = typeof detail.title === "string" && detail.title !== "" ? detail.title : "用例";
    candidates.push({
      caseRunId: detail.caseRunId,
      caseNodeId: detail.caseNodeId,
      title: "P7 失败分析：" + title,
      description: "失败断言与证据已复核，根因可由证据与代码位置解释。",
      severity: "major",
      confidence: "high",
      failureClass: "application",
      rerunSuggestion: "修复后重跑该用例并复核断言。",
      evidenceIds,
    });
  }
  return candidates;
}

/** Plans one failure-analysis-agent turn. */
function planP7FailureTurn(run, userText, mode) {
  if (mode === "degraded") return p7Text(P7_DEGRADED_TEXT);

  const batchId = p7InstructionValue(userText, "batchId");
  if (!p7HasResult(run.toolResults, "analysis_case_list")) {
    return p7Tool("analysis_case_list", { batchId }, "P7 失败分析：读取失败用例摘要");
  }
  if (mode === "invalid") {
    if (p7HasResult(run.toolResults, "analysis_candidate_propose")) return p7Text(P7_INVALID_TEXT);
    return p7RawTool("analysis_candidate_propose", P7_INVALID_FAILURE_ARGS, "P7 失败分析：提交无效候选");
  }
  if (mode === "noop") {
    if (p7HasResult(run.toolResults, "analysis_finish")) return p7Text(P7_FINISH_TEXTS.failureNoop);
    return p7Tool(
      "analysis_finish",
      { summary: "P7 失败分析结束：本轮没有可提交的候选" },
      "P7 失败分析：结束分析运行",
    );
  }

  const caseList = p7LatestResult(run.toolResults, "analysis_case_list");
  const failed = caseList && Array.isArray(caseList.cases) ? caseList.cases : [];
  const wanted = failed
    .filter((failedCase) => failedCase && typeof failedCase.caseRunId === "string")
    .slice(0, 2);
  // One detail turn per failed case, counted on the tool results so a malformed
  // or failed detail can never make the plan request it forever.
  const detailTurns = run.toolResults.filter((result) => result.name === "analysis_case_detail").length;
  if (detailTurns < wanted.length) {
    const failedCase = wanted[detailTurns];
    return p7Tool(
      "analysis_case_detail",
      { caseRunId: failedCase.caseRunId },
      "P7 失败分析：读取用例明细 " + failedCase.caseRunId,
    );
  }

  const details = wanted
    .map((failedCase) => p7CaseDetail(run.toolResults, failedCase.caseRunId))
    .filter((detail) => detail !== null);
  const evidenceIds = p7StringIds(details.flatMap((detail) => p7StringIds(detail.evidenceIds)));
  if (evidenceIds.length > 0 && !p7HasResult(run.toolResults, "analysis_evidence_meta")) {
    return p7Tool("analysis_evidence_meta", { evidenceIds }, "P7 失败分析：读取证据元数据");
  }

  if (!p7HasResult(run.toolResults, "analysis_candidate_propose")) {
    const candidates = p7FailureCandidates(details);
    if (candidates.length > 0) {
      return p7Tool(
        "analysis_candidate_propose",
        { candidates },
        "P7 失败分析：提交 1-2 条失败候选",
      );
    }
  }
  if (!p7HasResult(run.toolResults, "analysis_finish")) {
    return p7Tool(
      "analysis_finish",
      { summary: "P7 失败分析完成：已提交失败候选" },
      "P7 失败分析：结束分析运行",
    );
  }
  return p7Text(P7_FINISH_TEXTS.failure);
}

/** Sanitizes failure-candidate code refs for triage.issue_draft. */
function p7DraftCodeRefs(codeRefs) {
  if (!Array.isArray(codeRefs)) return [];
  const refs = [];
  for (const ref of codeRefs) {
    if (!ref || typeof ref.path !== "string" || ref.path === "") continue;
    if (!Number.isInteger(ref.line) || ref.line <= 0) continue;
    if (typeof ref.reason !== "string" || ref.reason === "") continue;
    refs.push({ path: ref.path, line: ref.line, reason: ref.reason });
  }
  return refs;
}

/** Builds one issue draft, merging into an open issue with the same title. */
function p7IssueDraft(candidate, issueList) {
  const title =
    typeof candidate.title === "string" && candidate.title !== "" ? candidate.title : "P7 失败候选：待整理";
  const draft = {
    title,
    description:
      typeof candidate.description === "string" && candidate.description !== ""
        ? candidate.description
        : "失败分析候选整理为可复现问题单。",
    reproSteps: "1. 运行失败用例 " + candidate.caseNodeId + "\n2. 按证据复现失败",
    expected: "用例断言通过",
    actual:
      typeof candidate.description === "string" && candidate.description !== ""
        ? candidate.description
        : "断言失败",
    severity: typeof candidate.severity === "string" ? candidate.severity : "major",
    confidence: typeof candidate.confidence === "string" ? candidate.confidence : "high",
    caseRunId: candidate.caseRunId,
    caseNodeId: candidate.caseNodeId,
    evidenceIds: p7StringIds(candidate.evidenceIds),
    codeRefs: p7DraftCodeRefs(candidate.codeRefs),
  };
  const openIssues = issueList && Array.isArray(issueList.issues) ? issueList.issues : [];
  const match = openIssues.find(
    (issue) =>
      issue &&
      issue.title === draft.title &&
      typeof issue.issueId === "string" &&
      /^[1-9][0-9]*$/.test(issue.issueId),
  );
  if (match) draft.mergeIntoIssueId = match.issueId;
  return draft;
}

/** Plans one issue-triage-agent turn. */
function planP7TriageTurn(run, mode) {
  if (mode === "degraded") return p7Text(P7_DEGRADED_TEXT);

  if (!p7HasResult(run.toolResults, "triage_candidate_list")) {
    return p7Tool("triage_candidate_list", {}, "P7 问题整理：读取失败候选");
  }
  if (mode === "invalid") {
    if (p7HasResult(run.toolResults, "triage_issue_draft")) return p7Text(P7_INVALID_TEXT);
    return p7RawTool("triage_issue_draft", P7_INVALID_TRIAGE_ARGS, "P7 问题整理：提交无效草稿");
  }
  if (mode === "noop") {
    if (p7HasResult(run.toolResults, "triage_finish")) return p7Text(P7_FINISH_TEXTS.triageNoop);
    return p7Tool(
      "triage_finish",
      { summary: "P7 问题整理结束：本轮没有可提交的草稿" },
      "P7 问题整理：结束整理运行",
    );
  }

  const candidateList = p7LatestResult(run.toolResults, "triage_candidate_list");
  const candidates =
    candidateList && Array.isArray(candidateList.candidates)
      ? candidateList.candidates.filter((candidate) => candidate && typeof candidate.caseRunId === "string")
      : [];

  if (!p7HasResult(run.toolResults, "triage_issue_list")) {
    return p7Tool("triage_issue_list", {}, "P7 问题整理：读取 open 问题摘要");
  }
  if (candidates.length === 0) {
    if (p7HasResult(run.toolResults, "triage_finish")) return p7Text(P7_FINISH_TEXTS.triageNoop);
    return p7Tool(
      "triage_finish",
      { summary: "P7 问题整理结束：没有可整理的候选" },
      "P7 问题整理：结束整理运行",
    );
  }

  const first = candidates[0];
  if (!p7HasResult(run.toolResults, "triage_context_read")) {
    return p7Tool(
      "triage_context_read",
      {
        caseRunIds: [first.caseRunId],
        evidenceIds: p7StringIds(first.evidenceIds),
      },
      "P7 问题整理：读取用例/证据/代码位置摘要",
    );
  }
  if (!p7HasResult(run.toolResults, "triage_issue_draft")) {
    const issueList = p7LatestResult(run.toolResults, "triage_issue_list") || { issues: [] };
    return p7Tool(
      "triage_issue_draft",
      { issues: [p7IssueDraft(first, issueList)] },
      "P7 问题整理：提交 1 张问题单草稿",
    );
  }
  if (!p7HasResult(run.toolResults, "triage_finish")) {
    return p7Tool(
      "triage_finish",
      { summary: "P7 问题整理完成：已提交问题单草稿" },
      "P7 问题整理：结束整理运行",
    );
  }
  return p7Text(P7_FINISH_TEXTS.triage);
}

/** Plans exactly one P7 turn from the profile, the run state and the mode. */
function planP7Turn(profile, run, userText, mode) {
  switch (profile) {
    case "supplement":
      return planP7SupplementTurn(run, userText, mode);
    case "failure":
      return planP7FailureTurn(run, userText, mode);
    default:
      return planP7TriageTurn(run, mode);
  }
}

/**
 * Handles one P7 agent request. Returns true when the system prompt carried a P7
 * role marker, false so P1-P6 stay untouched.
 */
async function handleP7Scenario(messages, res, stream) {
  const profile = detectP7Scenario(systemPromptText(messages));
  if (profile === null) return false;

  const lastUser = findLastUserMessage(messages);
  const run = lastUser ? describeP5Run(messages, lastUser.index) : { step: 0, toolResults: [] };
  const mode = p7Mode();
  recordP7Request(profile, mode, run.step);

  const plan = lastUser ? planP7Turn(profile, run, lastUser.text, mode) : p7Text(P7_EMPTY_TEXT);
  if (plan.kind === "tool") {
    const message = {
      content: null,
      reasoning_content: plan.reasoning,
      tool_calls: toolCallPayload(plan.name, plan.arguments, `p7_${profile}_call_${run.step + 1}`),
    };
    if (stream) {
      await streamToolTurn(res, message, P7_TOOL_USAGE);
      return true;
    }
    sendJson(res, 200, completionBody(message, "tool_calls", P7_TOOL_USAGE));
    return true;
  }

  if (stream) {
    await streamTextTurn(res, plan.text, P7_TEXT_USAGE);
    return true;
  }
  sendJson(res, 200, completionBody({ content: plan.text }, "stop", P7_TEXT_USAGE));
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
  const messages = Array.isArray(body.messages) ? body.messages : [];
  const stream = body.stream === true;

  // P4 file/impact analysis requests carry a system prompt marker and no
  // agent tools (the codeintel gateway sends a plain system + user pair), so
  // they are detected before the agent tool-call validation below. P1/P2/P3
  // requests never match these markers and keep the original behavior.
  if (await handleP4Scenario(messages, res, stream)) return;

  // P5 mindmap requests carry the role marker of one of the four mindmap
  // profiles. They are answered before recordRequest and before the agent tools
  // guard: the plan/step counting must own every turn of the run, and the
  // degraded mode answers without any tool call.
  if (await handleP5Scenario(messages, res, stream)) return;

  // P6 execution requests carry the execution-agent system marker. They are
  // answered after P5 and before recordRequest so the execution plan owns every
  // turn and P1-P5 request logging stays untouched.
  if (await handleP6Scenario(messages, res, stream)) return;

  // P7 supplement/failure/triage requests carry one of the three P7 role
  // markers (design spec §9.1-§9.4). They are answered after P6 and before
  // recordRequest so the P7 plan owns every turn, including the degraded mode
  // that answers without any tool call.
  if (await handleP7Scenario(messages, res, stream)) return;

  recordRequest(body);

  if (!Array.isArray(body.tools) || body.tools.length === 0) {
    sendError(res, 400, "tools must be non-empty");
    return;
  }

  const lastMessage = messages.length > 0 ? messages[messages.length - 1] : {};
  const lastRole = lastMessage && lastMessage.role;

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
  // Test-only P4 scenario control (design spec §10.5 item 3): never part of the
  // provider wire contract, so it sits outside /v1.
  if (req.method === "POST" && pathname === "/__p4-mode") {
    await handleP4ModeControl(req, res);
    return;
  }
  if (req.method === "GET" && pathname === "/__p4-mode") {
    sendJson(res, 200, { mode: p4Mode() });
    return;
  }
  // Test-only P5 mindmap scenario control (design spec §10.6).
  if (req.method === "POST" && pathname === "/__p5-mode") {
    await handleP5ModeControl(req, res);
    return;
  }
  if (req.method === "GET" && pathname === "/__p5-mode") {
    sendJson(res, 200, { mode: p5Mode() });
    return;
  }
  // Test-only P6 execution scenario control (design spec §10.2).
  if (req.method === "POST" && pathname === "/__p6-mode") {
    await handleP6ModeControl(req, res);
    return;
  }
  if (req.method === "GET" && pathname === "/__p6-mode") {
    sendJson(res, 200, { mode: p6Mode() });
    return;
  }
  // Test-only P7 autonomous-loop scenario control (design spec §12.5).
  if (req.method === "POST" && pathname === "/__p7-mode") {
    await handleP7ModeControl(req, res);
    return;
  }
  if (req.method === "GET" && pathname === "/__p7-mode") {
    sendJson(res, 200, { mode: p7Mode() });
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

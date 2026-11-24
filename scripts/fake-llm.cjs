#!/usr/bin/env node
"use strict";

// Deterministic OpenAI-compatible fake LLM upstream for EzTest 0.2 P0 + P1 + P2,
// the current case_executor execution scenario (v0.2.4-C T13), and the legacy
// P3-P7 scenario dispatchers used by the 0.2 regression suites.
//
// P0 only needs configuration / model catalog / connectivity tests and the
// acceptance flow. P1 adds a deterministic agent mode used by the clarification
// runtime; P2 adds the design team profiles. Without a profile marker the P0
// behaviour is unchanged. The scenario dispatchers live in
// scripts/fake-llm-legacy.cjs and are called before the current profile
// dispatcher; see that module for the scenario rules and the [profile:...]
// marker mapping. The P6 execution scenario answers the seeded
// "[profile:case_executor]" marker with the CURRENT capability-domain tool
// surface (context.task_pack / sut.read / exec.run / browser.cli / http.request /
// fixture.upload / fixture.download / execution.* / human.request, dotted registry
// names); the 13 browser.* atomic actions and the underscore wire form are retired.
// v0.2.4-F T15: the scenario consumes the natural-language task pack and carries
// sut_id as factual attribution; the "fixture" mode exercises the P18 reuse chain.
//
// Endpoints:
//   GET  /health               -> {"status":"ok","service":"fake-llm",...}
//   GET  /v1/models            -> OpenAI-compatible model list
//   POST /v1/chat/completions  -> non-stream JSON or stream=true SSE
//   GET/POST /__p4-mode        -> legacy P4 scenario mode (ok|error|partial|impact-ai)
//   GET/POST /__p5-mode        -> legacy P5 scenario mode (normal|invalid|degraded)
//   GET/POST /__p6-mode        -> P6 execution scenario mode (normal|fail|blocked|takeover|fixture|budget)
//   GET/POST /__p7-mode        -> legacy P7 scenario mode (normal|noop|degraded|invalid)
//   GET/POST /__judge-mode     -> design_judge scenario mode (pass|fail|minor|error)
//   GET/POST /__author-mode    -> design_author coverage mode (full|partial)
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
//         case, reads design.graph_read, then requirement.read (for the AC-id
//         set), then submits case.graph_propose
//       - design_planner -> scope-only text, no tools
//       - case_author -> design.graph_read, then requirement.read (ac_ids), then
//         case.graph_propose with one scenario and two draft cases, then final
//         text. Cases carry the v0.2.4-B natural-language contract: sut_description,
//         preconditions, covered_acs (from requirement.read.ac_ids) and prose step
//         targets; the deleted version_sut/page/api id fields are never emitted.
//       - design_critic -> design.review_submit with one minor finding, then
//         final text
//       - design_author (v0.2.4-E T19) -> same read/propose flow as case_author
//         under the CURRENT profile marker; /__author-mode partial covers only
//         the first AC (coverage-gap seed), full covers the whole AC set. A
//         design.rework task revises the scoped case (existing id, added step +
//         assertion) so the content hash changes and the judge re-reviews.
//       - design_judge (v0.2.4-E T19) -> design.graph_read once, then ONE
//         design.judgment_submit covering every judge_case scope entry with a
//         per-case verdict per /__judge-mode: pass (clean pass + AC matrix),
//         minor (pass + minor finding), fail (fail + blocker finding); error
//         answers HTTP 500 server_error (LLM unavailable). round is NEVER
//         emitted (server-derived, T13 contract).
//       - data_planner -> design.graph_read, then design.data_plan_submit for
//         the first draft case, then final text
//       - no profile ever calls case.confirm (user/server-only)
//   * execution scenario (P6 dispatcher, current T13 contract) + legacy P3-P7
//         scenarios: see scripts/fake-llm-legacy.cjs. P4 file/impact
//         requests are recorded compactly in .tmp/fake-llm-requests.jsonl
//         (mode/path/commit only, never prompt content).
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
// This process never touches the network besides loopback; the only file write
// is the compact legacy P4 request log described above.

const crypto = require("node:crypto");
const http = require("node:http");
// Scenario dispatchers: the current case_executor execution scenario (P6) plus
// the legacy P3-P7 suites committed in the 0.2.0 tree
// (scripts/fake-llm.p4/p5/p6/p7.test.cjs). See the module banner.
const legacyFake = require("./fake-llm-legacy.cjs");

const HOST = "127.0.0.1";
const DEFAULT_PORT = 13334;
const FIXED_CREATED = 1767225600;
const REPLY_PREFIX = "EzTest fake LLM: ";
const MAX_REPLY_CHARS = 240;
const STREAM_CHUNK_CHARS = 16;
// Tool-call arguments stream in 3-code-point fragments, mirroring the legacy
// streamToolTurn wire shape (fake-llm-legacy.cjs) so the current dispatcher and
// the legacy scenario suites emit identical incremental tool_calls deltas.
const TOOL_STREAM_CHUNK_CHARS = 3;
const MAX_BODY_BYTES = 10 * 1024 * 1024;
const PROFILE_PATTERN = /\[profile:(clarification_host|requirement_curator|context_scout|design_host|design_planner|case_author|design_author|design_critic|design_judge|data_planner)\]/;
const DESIGN_MODIFY_HINT = /(新增|补充|修改)/;
const DESIGN_CASE_HINT = "用例";
// Runtime-generated opening prompt; it mentions "新增或修改用例" but is not a
// user write request, so the Host must not propose on the opening turn.
const DESIGN_OPENING_MARKER = "设计会话的开场";
// The agent runner injects the task scope into the per-work-item task block
// (requirement_id=<decimal>; user turn since Task 80), which lets the fake
// author/judge pick the requirement the work item is actually scoped to instead
// of the first graph row.
const SCOPE_REQUIREMENT_PATTERN = /requirement_id=([1-9][0-9]*)/;
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

// allPromptText joins the system AND user messages: since Task 80 the runner
// renders the per-work-item task scope (requirement_id=, judge_case=, case_id=)
// into the USER turn, while older fixtures put it in the system section. The
// scope readers must find it in either place.
function allPromptText(body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  return messages
    .filter((message) => message && (message.role === "system" || message.role === "user"))
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

// toolResults returns every parsed JSON tool-message result, newest first, so a
// profile can look back past the latest result (the design flow accumulates a
// graph_read result AND a requirement.read result before proposing).
function toolResults(body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  const out = [];
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (!message || message.role !== "tool") continue;
    const text = messageText(message.content).trim();
    if (text === "") continue;
    try {
      const parsed = JSON.parse(text);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) out.push(parsed);
    } catch {
      // Not a JSON tool result; keep looking at older tool messages.
    }
  }
  return out;
}

// findGraphResult returns the newest design.graph_read result (a requirements
// array, no error), regardless of whether a later requirement.read result exists.
function findGraphResult(body) {
  for (const result of toolResults(body)) {
    if (!result.error && Array.isArray(result.requirements)) return result;
  }
  return null;
}

// findRequirementRead returns the newest requirement.read result, identified by
// its ac_ids array (the flat AC-id set the design author must draw covered_acs
// from; design v1.3.1 §5.2). graph_read does NOT carry the requirement AC set.
// The REAL tool wraps the detail as {"requirement": {...}} (tools/requirements.go
// requirementReadResult); older fixtures carry the flat shape, so both are
// accepted and the inner requirement object is returned.
function findRequirementRead(body) {
  for (const result of toolResults(body)) {
    if (result.error) continue;
    if (Array.isArray(result.ac_ids)) return result;
    const nested = result.requirement;
    if (nested && typeof nested === "object" && !Array.isArray(nested) && Array.isArray(nested.ac_ids)) {
      return nested;
    }
  }
  return null;
}

// requirementACIDs derives a case's covered_acs from the requirement.read ac_ids
// set (server-assigned AC-<n> ids). The contract requires covered_acs to be a
// non-empty subset of the requirement's current AC set, so the fake covers the
// whole set. The ["AC-1"] fallback only fires for a degenerate empty source; a
// confirmed requirement always has at least one AC.
function requirementACIDs(reqRead) {
  const raw = reqRead && Array.isArray(reqRead.ac_ids) ? reqRead.ac_ids : [];
  const ids = raw.filter((value) => typeof value === "string" && /^AC-[1-9][0-9]*$/.test(value));
  return ids.length > 0 ? ids : ["AC-1"];
}

// caseSutDescription derives the case's prose system-under-test description from
// the requirement's own sut_description (requirement.read). The contract allows a
// case to be as specific as or more specific than the requirement; the fake reuses
// it verbatim, with a deterministic prose fallback when the source is empty.
function caseSutDescription(reqRead) {
  const desc =
    reqRead && typeof reqRead.sut_description === "string" ? reqRead.sut_description.trim() : "";
  return desc !== "" ? desc : "被测系统的核心页面与接口（自然语言描述，执行团队据此解析目标）。";
}

function firstGraphCase(requirement) {
  const scenarios = Array.isArray(requirement.scenarios) ? requirement.scenarios : [];
  const currentVersion = String(requirement.requirement_version_id || "");
  for (const scenario of scenarios) {
    // Never target a case from a previous requirement version: after a
    // requirement change the old scenario/cases are stale history.
    if (!scenario || String(scenario.requirement_version_id) !== currentVersion) continue;
    const cases = Array.isArray(scenario.cases) ? scenario.cases : [];
    for (const item of cases) {
      if (item && item.case_id) return item;
    }
  }
  return null;
}

// firstDraftGraphCase selects the first current-version case whose current
// version is a draft. The data planner may only update draft case versions;
// confirmed/stale rows would fail with state_conflict.
function firstDraftGraphCase(requirement) {
  const scenarios = Array.isArray(requirement.scenarios) ? requirement.scenarios : [];
  const currentVersion = String(requirement.requirement_version_id || "");
  for (const scenario of scenarios) {
    if (!scenario || String(scenario.requirement_version_id) !== currentVersion) continue;
    const cases = Array.isArray(scenario.cases) ? scenario.cases : [];
    for (const item of cases) {
      if (item && item.case_id && item.status === "draft" && item.current_version_id) return item;
    }
  }
  return null;
}

// scopedRequirementID reads the runtime-injected requirement scope from the
// task block, so design.requirement/gap/rework/judge runs target the work item's
// requirement. The block lives in the user turn (Task 80); older fixtures carry
// it in the system section, so both are scanned.
function scopedRequirementID(body) {
  const match = SCOPE_REQUIREMENT_PATTERN.exec(allPromptText(body));
  return match ? match[1] : null;
}

// selectGraphRequirement prefers the requirement named in the injected scope,
// then the first clarified requirement (never a draft/rolled-back row).
function selectGraphRequirement(graph, body) {
  const requirements = Array.isArray(graph.requirements)
    ? graph.requirements.filter((requirement) => requirement && requirement.requirement_id)
    : [];
  const scopedID = scopedRequirementID(body);
  if (scopedID) {
    const scoped = requirements.find((requirement) => String(requirement.requirement_id) === scopedID);
    if (scoped) return scoped;
  }
  return requirements.find((requirement) => requirement.status === "clarified") || null;
}

function scenarioProposal(requirement) {
  const scenarios = Array.isArray(requirement.scenarios) ? requirement.scenarios : [];
  const currentVersion = String(requirement.requirement_version_id || "");
  // Only reuse a scenario bound to the current requirement version; scenarios
  // from an older version are stale history and cannot be updated.
  const existing = scenarios.find(
    (scenario) => scenario && scenario.id && String(scenario.requirement_version_id) === currentVersion,
  );
  // v0.2.4-B natural-language contract: a scenario carries only ref/name/
  // description/sort_order (+id when updating). version_sut_ids/page_ids/api_ids
  // are deleted and hard-rejected by the decoder (DisallowUnknownFields).
  const node = {
    ref: "s1",
    name: existing && typeof existing.name === "string" && existing.name !== "" ? existing.name : "AI 设计场景",
    description: "由 fake LLM 生成的确定性设计场景。",
    sort_order: 0,
  };
  if (existing) node.id = String(existing.id);
  return node;
}

function caseProposal(requirement, existing, title, ordinal, coveredACs, sutDescription) {
  const requirementVersionID = String(requirement.requirement_version_id || "");
  // v0.2.4-B natural-language case contract: sut_description (prose SUT),
  // preconditions, covered_acs (AC ids from requirement.read) and prose step
  // targets replace the deleted default_version_sut_id/version_sut_ids/page_ids/
  // api_ids resource ids, which the decoder hard-rejects.
  const node = {
    ref: "c" + (ordinal > 0 ? ordinal : 1),
    scenario_ref: "s1",
    title,
    description: "由 fake LLM 生成的确定性用例草案，等待用户确认。",
    sut_description: sutDescription,
    preconditions: "已准备测试账号与运行环境。",
    covered_acs: coveredACs,
    test_data: { fake: true },
    priority: "P2",
    steps: [
      { seq: 1, action: "打开被测页面", expected: "页面加载完成并显示主要内容", target_description: "被测系统的入口页面", data: {} },
      { seq: 2, action: "执行核心操作", expected: "页面显示操作成功的结果提示", target_description: "页面的核心操作控件", data: {} },
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
  const coveredACs =
    Array.isArray(settings.coveredACs) && settings.coveredACs.length > 0 ? settings.coveredACs : ["AC-1"];
  const sutDescription =
    typeof settings.sutDescription === "string" && settings.sutDescription !== ""
      ? settings.sutDescription
      : "被测系统的核心页面与接口。";
  return {
    requirement_id: String(requirement.requirement_id),
    requirement_version_id: String(requirement.requirement_version_id),
    scenarios: [scenarioProposal(requirement)],
    cases: titles.map((title, index) =>
      caseProposal(requirement, index === 0 ? existing : null, title, index + 1, coveredACs, sutDescription),
    ),
  };
}

// wantsGraphChange reports whether the latest human request asks the Host to
// add/modify cases. Graph tool results alone never trigger writes: the opening
// turn (runtime-generated prompt) and plain questions stay conversation-only.
function wantsGraphChange(body) {
  const text = lastUserText(body);
  if (text === "" || text.includes(DESIGN_OPENING_MARKER)) return false;
  return text.includes(DESIGN_CASE_HINT) && DESIGN_MODIFY_HINT.test(text);
}

function designHostOutcome(body, rawBody) {
  const result = lastToolResult(body);
  if (result && isProposeResult(result)) {
    return textOutcome("已提交用例草案（draft），请在脑图中确认或继续提出修改。");
  }
  if (result && result.error) {
    return textOutcome("读取脑图失败，请稍后重试，或直接在对话中说明要修改的用例。");
  }
  if (!wantsGraphChange(body)) {
    return textOutcome(
      "我是设计 Host。我会读取用例脑图，并可以按你的要求新增或修改场景/用例 draft；请说明要调整的内容。",
    );
  }
  const graph = findGraphResult(body);
  const reqRead = findRequirementRead(body);
  if (graph && reqRead) {
    const requirement = selectGraphRequirement(graph, body);
    if (!requirement) {
      return textOutcome("当前版本还没有可设计的已澄清需求。");
    }
    return toolCallOutcome(
      rawBody,
      "case.graph_propose",
      graphProposal(requirement, {
        updateFirstCase: true,
        titles: ["AI 补充用例（设计对话）"],
        coveredACs: requirementACIDs(reqRead),
        sutDescription: caseSutDescription(reqRead),
      }),
    );
  }
  if (graph) {
    const requirement = selectGraphRequirement(graph, body);
    if (!requirement) {
      return textOutcome("当前版本还没有可设计的已澄清需求。");
    }
    // covered_acs must come from the requirement AC set, which graph_read does not
    // carry: read it once via requirement.read before proposing.
    return toolCallOutcome(rawBody, "requirement.read", { requirement_id: String(requirement.requirement_id) });
  }
  return toolCallOutcome(rawBody, "design.graph_read", {});
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
  const graph = findGraphResult(body);
  const reqRead = findRequirementRead(body);
  if (graph && reqRead) {
    const requirement = selectGraphRequirement(graph, body);
    if (!requirement) {
      return textOutcome("没有找到可设计的已澄清需求。");
    }
    return toolCallOutcome(
      rawBody,
      "case.graph_propose",
      graphProposal(requirement, {
        titles: ["AI 用例 1：核心流程", "AI 用例 2：异常流程"],
        coveredACs: requirementACIDs(reqRead),
        sutDescription: caseSutDescription(reqRead),
      }),
    );
  }
  if (graph) {
    const requirement = selectGraphRequirement(graph, body);
    if (!requirement) {
      return textOutcome("没有找到可设计的已澄清需求。");
    }
    // The AC-id set covered_acs must be drawn from is not in graph_read; read it
    // once via requirement.read (design v1.3.1 §5.2) before proposing.
    return toolCallOutcome(rawBody, "requirement.read", { requirement_id: String(requirement.requirement_id) });
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

// --- v0.2.4-E T19: design_author / design_judge dispatchers ------------------
//
// Mode controls follow the legacy /__p6-mode pattern (test-only runtime switch,
// env-backed so the E2E can flip scenarios without restarting the fake):
//   /__judge-mode  pass (default) | fail | minor | error
//   /__author-mode full (default) | partial
const JUDGE_MODES = new Set(["pass", "fail", "minor", "error"]);
const AUTHOR_MODES = new Set(["full", "partial"]);

function judgeMode() {
  const value = String(process.env.EZTEST_FAKE_LLM_JUDGE_MODE || "pass").trim().toLowerCase();
  return JUDGE_MODES.has(value) ? value : "pass";
}

function authorMode() {
  const value = String(process.env.EZTEST_FAKE_LLM_AUTHOR_MODE || "full").trim().toLowerCase();
  return AUTHOR_MODES.has(value) ? value : "full";
}

async function handleModeControl(req, res, modes, envKey, current) {
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
  const mode = String(body.mode || "").trim().toLowerCase();
  if (!modes.has(mode)) {
    sendError(res, 400, "mode must be one of " + [...modes].join(", "));
    return;
  }
  process.env[envKey] = mode;
  sendJson(res, 200, { mode: current() });
}

// judgeCaseScope parses the review scope the runner renders into the task block
// (runner.go taskContext): one "judge_case=case_id:<id>,case_version_id:<id>,
// content_hash:<64hex>" line per pending draft. The scope entries are the
// authoritative review targets (T14), not the full-draft-set generation anchor.
const JUDGE_CASE_PATTERN = /judge_case=case_id:([1-9][0-9]*),case_version_id:([1-9][0-9]*),content_hash:([0-9a-f]{64})/g;
const REWORK_CASE_PATTERN = /case_id=([1-9][0-9]*)/;
const REWORK_TYPE_PATTERN = /类型:\s*design\.rework/;

function judgeCaseScope(body) {
  const text = allPromptText(body);
  const cases = [];
  for (const match of text.matchAll(JUDGE_CASE_PATTERN)) {
    cases.push({ caseID: match[1], caseVersionID: match[2], contentHash: match[3] });
  }
  return cases;
}

// findGraphCaseByID locates one case of the requirement's CURRENT revision by
// logical case id (rework targets the scoped case, not the first graph row).
function findGraphCaseByID(requirement, caseID) {
  const scenarios = Array.isArray(requirement.scenarios) ? requirement.scenarios : [];
  const currentVersion = String(requirement.requirement_version_id || "");
  for (const scenario of scenarios) {
    if (!scenario || String(scenario.requirement_version_id) !== currentVersion) continue;
    const cases = Array.isArray(scenario.cases) ? scenario.cases : [];
    for (const item of cases) {
      if (item && String(item.case_id) === String(caseID)) return item;
    }
  }
  return null;
}

// draftGraphCases lists the current-revision draft cases of one requirement
// (the judge fallback when no judge_case scope lines are present).
function draftGraphCases(requirement) {
  const scenarios = Array.isArray(requirement.scenarios) ? requirement.scenarios : [];
  const currentVersion = String(requirement.requirement_version_id || "");
  const out = [];
  for (const scenario of scenarios) {
    if (!scenario || String(scenario.requirement_version_id) !== currentVersion) continue;
    const cases = Array.isArray(scenario.cases) ? scenario.cases : [];
    for (const item of cases) {
      if (item && item.case_id && item.status === "draft" && item.current_version_id) {
        out.push({
          caseID: String(item.case_id),
          caseVersionID: String(item.current_version_id),
          coveredACs: Array.isArray(item.covered_acs) ? item.covered_acs : [],
        });
      }
    }
  }
  return out;
}

// judgeCoverageMatrix builds the optional ac_coverage_matrix object (T13
// contract: a free JSON object keyed by AC id) from the graph case's covered_acs.
function judgeCoverageMatrix(graph, body, target) {
  const requirement = selectGraphRequirement(graph, body);
  if (!requirement) return null;
  const item = findGraphCaseByID(requirement, target.caseID);
  if (!item || String(item.current_version_id) !== String(target.caseVersionID)) return null;
  const acs = Array.isArray(item.covered_acs) ? item.covered_acs.filter((ac) => /^AC-[1-9][0-9]*$/.test(ac)) : [];
  if (acs.length === 0) return null;
  const matrix = {};
  for (const ac of acs) {
    matrix[ac] = { covered: true, case_id: target.caseID, basis: "用例步骤与断言实现了该 AC 的 when/then。" };
  }
  return matrix;
}

// designJudgeOutcome is the independent judge flow: read the graph once, then
// submit ONE design.judgment_submit batch covering every scoped pending draft.
// The payload shape is the T13 tool contract (tools/judgment.go): no round, no
// unknown fields; pass never carries blocker/important findings.
function designJudgeOutcome(body, rawBody) {
  const result = lastToolResult(body);
  if (result && Array.isArray(result.submitted)) {
    return textOutcome("已完成独立审查：逐用例提交 verdict 与 findings，等待人工在 case_review 决断。");
  }
  const graph = findGraphResult(body);
  if (!graph) {
    if (result && result.error) {
      return textOutcome("读取脑图失败，本轮未提交判定。");
    }
    return toolCallOutcome(rawBody, "design.graph_read", {});
  }
  const requirementID = scopedRequirementID(body);
  let targets = judgeCaseScope(body);
  if (targets.length === 0) {
    const requirement = selectGraphRequirement(graph, body);
    if (requirement) targets = draftGraphCases(requirement);
  }
  if (!requirementID || targets.length === 0) {
    return textOutcome("当前需求没有待审的用例草案，本轮未提交判定。");
  }
  const mode = judgeMode();
  const judgments = targets.map((target) => {
    if (mode === "fail") {
      return {
        case_id: target.caseID,
        case_version_id: target.caseVersionID,
        verdict: "fail",
        findings: [
          {
            dimension: "coverage",
            severity: "blocker",
            message: "用例未覆盖该需求验收标准的反向/边界场景，且断言不足以证明 AC 的 then 成立；请补齐覆盖与可观察断言。",
            case_ref: target.caseID,
          },
        ],
      };
    }
    if (mode === "minor") {
      return {
        case_id: target.caseID,
        case_version_id: target.caseVersionID,
        verdict: "pass",
        findings: [
          {
            dimension: "executability",
            severity: "minor",
            message: "建议补充测试数据的具体取值，便于执行团队复现。",
            case_ref: target.caseID,
          },
        ],
      };
    }
    const item = { case_id: target.caseID, case_version_id: target.caseVersionID, verdict: "pass" };
    const matrix = judgeCoverageMatrix(graph, body, target);
    if (matrix) item.ac_coverage_matrix = matrix;
    return item;
  });
  return toolCallOutcome(rawBody, "design.judgment_submit", {
    requirement_id: requirementID,
    judgments,
  });
}

// reworkProposal revises ONE scoped case: it keeps the logical case id and
// title (natural key) and changes the content (description + one added step and
// assertion) so the server writes a new case version with a new content hash
// and the judge re-reviews it (spec SC-E4 rework loop).
function reworkProposal(requirement, target, coveredACs, sutDescription) {
  const node = caseProposal(requirement, target, target.title, 1, coveredACs, sutDescription);
  node.description = "按裁判打回意见修订：补齐反向/边界覆盖与可观察断言。";
  node.steps.push({
    seq: node.steps.length + 1,
    action: "验证边界场景",
    expected: "边界输入得到明确的错误提示或拦截",
    target_description: "页面的边界输入控件",
    data: {},
  });
  node.assertions.push({
    step_seq: node.steps.length,
    kind: "text",
    target: "边界提示",
    expected: "提示信息与验收标准一致",
  });
  return {
    requirement_id: String(requirement.requirement_id),
    requirement_version_id: String(requirement.requirement_version_id),
    scenarios: [scenarioProposal(requirement)],
    cases: [node],
  };
}

// designAuthorOutcome is the CURRENT design_author background job flow. It
// mirrors caseAuthorOutcome's read/propose contract under the current profile
// marker, adds the /__author-mode coverage knob (partial = first AC only, the
// SC-E4 coverage-gap seed) and the design.rework revision branch.
function designAuthorOutcome(body, rawBody) {
  const result = lastToolResult(body);
  if (result && isProposeResult(result)) {
    return textOutcome("已提交用例草案（draft），等待独立裁判审查与人工确认。");
  }
  const graph = findGraphResult(body);
  const reqRead = findRequirementRead(body);
  if (graph && reqRead) {
    const requirement = selectGraphRequirement(graph, body);
    if (!requirement) {
      return textOutcome("没有找到可设计的已澄清需求。");
    }
    const promptText = allPromptText(body);
    if (REWORK_TYPE_PATTERN.test(promptText)) {
      const caseMatch = REWORK_CASE_PATTERN.exec(promptText);
      const target = caseMatch
        ? findGraphCaseByID(requirement, caseMatch[1])
        : firstGraphCase(requirement);
      if (!target) {
        return textOutcome("打回的用例不在当前需求版本内，本轮未提交修订。");
      }
      return toolCallOutcome(
        rawBody,
        "case.graph_propose",
        reworkProposal(requirement, target, requirementACIDs(reqRead), caseSutDescription(reqRead)),
      );
    }
    const allACs = requirementACIDs(reqRead);
    const coveredACs = authorMode() === "partial" ? allACs.slice(0, 1) : allACs;
    return toolCallOutcome(
      rawBody,
      "case.graph_propose",
      graphProposal(requirement, {
        titles: ["AI 用例 1：核心流程", "AI 用例 2：异常流程"],
        coveredACs,
        sutDescription: caseSutDescription(reqRead),
      }),
    );
  }
  if (graph) {
    const requirement = selectGraphRequirement(graph, body);
    if (!requirement) {
      return textOutcome("没有找到可设计的已澄清需求。");
    }
    return toolCallOutcome(rawBody, "requirement.read", { requirement_id: String(requirement.requirement_id) });
  }
  if (result && result.error) {
    return textOutcome("读取脑图失败，请稍后重试。");
  }
  return toolCallOutcome(rawBody, "design.graph_read", {});
}

function dataPlannerOutcome(body, rawBody) {
  const result = lastToolResult(body);
  if (result && (result.case_version_id !== undefined || result.revision !== undefined)) {
    return textOutcome("已为当前 draft 用例补充前置条件与测试数据。");
  }
  const graph = graphResult(body);
  if (graph) {
    const requirement = firstGraphRequirement(graph);
    const item = requirement ? firstDraftGraphCase(requirement) : null;
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
    case "design_author":
      return designAuthorOutcome(body, rawBody);
    case "design_critic":
      return designCriticOutcome(body, rawBody);
    case "design_judge":
      return designJudgeOutcome(body, rawBody);
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

// openSse centralises the SSE response lifecycle both streaming writers share
// (H-E9): the identical writeHead/flushHeaders block, the close/error guard that
// stops writing to a dead socket, and the terminal `data: [DONE]` + res.end().
// streamCompletion and streamToolCompletion keep only their own delta loops.
function openSse(res) {
  res.writeHead(200, {
    "Content-Type": "text/event-stream; charset=utf-8",
    "Cache-Control": "no-cache, no-transform",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  });
  if (typeof res.flushHeaders === "function") res.flushHeaders();

  let closed = false;
  const markClosed = () => {
    closed = true;
  };
  res.on("close", markClosed);
  res.on("error", markClosed);

  return {
    isClosed: () => closed,
    event: (data) => writeSseEvent(res, data),
    done: () => {
      res.write("data: [DONE]\n\n");
      res.end();
    },
  };
}

function streamCompletion(res, body, rawBody, reply) {
  const sse = openSse(res);
  const id = completionId(rawBody);
  const model = modelFor(body);
  const chars = Array.from(reply);
  const usage = makeUsage(body, reply);

  // Opening role delta.
  sse.event(streamChunk(id, model, { role: "assistant" }));

  const chunks = [];
  for (let i = 0; i < chars.length; i += STREAM_CHUNK_CHARS) {
    chunks.push(chars.slice(i, i + STREAM_CHUNK_CHARS).join(""));
  }
  // A reply always has at least "ok", so chunks is never empty; guard anyway.
  if (chunks.length === 0) chunks.push("");

  for (const content of chunks) {
    if (sse.isClosed()) return;
    sse.event(streamChunk(id, model, { content }));
  }
  if (sse.isClosed()) return;
  sse.event(streamChunk(id, model, {}, "stop", usage));
  sse.done();
}

// streamToolCompletion streams a tool_calls outcome as OpenAI chat-completions
// SSE, mirroring the legacy streamToolTurn: each call's first argument fragment
// carries id/type/function.name, later fragments carry only function.arguments
// at the same index; a terminal finish_reason:"tool_calls" chunk carries usage
// (satisfying stream_options.include_usage), followed by [DONE]. Every call is
// streamed at its own index so multiple calls never collapse (no silent drop).
function streamToolCompletion(res, body, rawBody, outcome) {
  const sse = openSse(res);
  const id = completionId(rawBody);
  const model = modelFor(body);
  const usageText = outcome.calls.map((call) => call.arguments).join("");
  const usage = makeUsage(body, usageText);

  for (let callIndex = 0; callIndex < outcome.calls.length; callIndex += 1) {
    const call = outcome.calls[callIndex];
    const chars = Array.from(call.arguments);
    const parts = [];
    for (let i = 0; i < chars.length; i += TOOL_STREAM_CHUNK_CHARS) {
      parts.push(chars.slice(i, i + TOOL_STREAM_CHUNK_CHARS).join(""));
    }
    // Empty arguments still emit one fragment carrying id/type/name.
    if (parts.length === 0) parts.push("");
    for (let index = 0; index < parts.length; index += 1) {
      if (sse.isClosed()) return;
      const fragment = { index: callIndex, function: { arguments: parts[index] } };
      if (index === 0) {
        fragment.id = call.id;
        fragment.type = "function";
        fragment.function.name = call.name;
      }
      sse.event(streamChunk(id, model, { tool_calls: [fragment] }));
    }
  }
  if (sse.isClosed()) return;
  sse.event(streamChunk(id, model, {}, "tool_calls", usage));
  sse.done();
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

  // Judge-unavailable scenario (v0.2.4-E T19 loop ④): /__judge-mode error makes
  // every design_judge request fail as an upstream server error, so the real
  // runner classifies a model failure and the scheduler's bounded judge
  // generations exhaust into judge_exhausted. Other profiles are unaffected.
  if (profileOf(body) === "design_judge" && judgeMode() === "error") {
    sendError(res, 500, "fake judge upstream unavailable (EZTEST_FAKE_LLM_JUDGE_MODE=error)", "server_error");
    return;
  }

  // The legacy P3-P7 scenario suites (and their P1/P3/P4 regression guards) are
  // dispatched first: each detector only matches its own system-prompt marker,
  // and the P1/P3 fallbacks are gated so an unmarked current P1/P2 request
  // still reaches the current dispatcher below.
  const messages = Array.isArray(body.messages) ? body.messages : [];
  if (
    await legacyFake.handleLegacyScenario({
      body,
      messages,
      res,
      stream: body.stream === true,
      hasProfileMarker: profileOf(body) !== null,
    })
  ) {
    return;
  }

  const outcome = chatOutcome(body, rawBody);
  if (body.stream === true && (outcome.kind === "text" || outcome.kind === "tool_calls")) {
    if (outcome.kind === "tool_calls") {
      streamToolCompletion(res, body, rawBody, outcome);
    } else {
      streamCompletion(res, body, rawBody, outcome.text);
    }
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
  if (req.method === "GET" && (pathname === "/v1/models" || pathname === "/models")) {
    sendJson(res, 200, { object: "list", data: MODELS });
    return;
  }
  if (req.method === "POST" && pathname === "/v1/chat/completions") {
    await handleChatCompletions(req, res);
    return;
  }
  // Test-only scenario controls for the current design dispatchers (T19),
  // same GET/POST shape as the legacy /__p4-mode ... /__p7-mode controls.
  if (pathname === "/__judge-mode") {
    if (req.method === "POST") {
      await handleModeControl(req, res, JUDGE_MODES, "EZTEST_FAKE_LLM_JUDGE_MODE", judgeMode);
      return;
    }
    if (req.method === "GET") {
      sendJson(res, 200, { mode: judgeMode() });
      return;
    }
  }
  if (pathname === "/__author-mode") {
    if (req.method === "POST") {
      await handleModeControl(req, res, AUTHOR_MODES, "EZTEST_FAKE_LLM_AUTHOR_MODE", authorMode);
      return;
    }
    if (req.method === "GET") {
      sendJson(res, 200, { mode: authorMode() });
      return;
    }
  }
  // Test-only legacy scenario controls (/__p4-mode ... /__p7-mode).
  if (await legacyFake.handleLegacyControl(req, res, pathname)) return;
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

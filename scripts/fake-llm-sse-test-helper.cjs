#!/usr/bin/env node
"use strict";

// Shared SSE parsing for the fake-llm test family (H-E9/E10, Ruling T21-R3/R4).
// Before this module scripts/fake-llm.p5/p6/p7.test.cjs each carried their own
// copy of the same folder, and every copy silently skipped non-JSON `data:`
// lines with `catch { continue }`, so a malformed frame could hide inside a
// green test. This module keeps one implementation, folds chunks and — per
// Ruling T21-R4 — turns any non-JSON data line into a failing assertion instead
// of a silent skip.
//
// Every consumer returns the same folded shape so p5 (which also reads
// reasoning_content) and p6/p7 share one parser.

const assert = require("node:assert/strict");

// SSE allows both `data:{...}` and `data: {...}` (the spec strips one optional
// leading space). Matching the bare `data:` prefix and then trimming accepts
// both forms, so a no-space frame can never be silently ignored.
const DATA_PREFIX = "data:";

// parseSseChunks returns the raw JSON chunks and records every `data:` line that
// is neither the [DONE] sentinel nor a blank separator but fails JSON.parse.
// It asserts loudly on any bad line so a corrupted stream can never pass.
function parseSseChunks(text) {
  const chunks = [];
  const badLines = [];
  for (const line of text.split("\n")) {
    if (!line.startsWith(DATA_PREFIX)) continue;
    const data = line.slice(DATA_PREFIX.length).trim();
    if (data === "" || data === "[DONE]") continue;
    try {
      chunks.push(JSON.parse(data));
    } catch {
      badLines.push(data);
    }
  }
  assert.deepEqual(
    badLines,
    [],
    "parseSse found " + badLines.length + " non-JSON data line(s) instead of skipping them: " +
      badLines.join(" | "),
  );
  return chunks;
}

// foldSse folds parsed chunks into one assistant turn. It is a superset of the
// old per-file folders: reasoning_content and the tool-call `type` are carried
// even where a caller does not read them.
function foldSse(chunks) {
  const result = { content: "", reasoning: "", toolCalls: [], finishReason: null };
  for (const chunk of chunks) {
    const choice = chunk && Array.isArray(chunk.choices) ? chunk.choices[0] : null;
    if (!choice) continue;
    const delta = choice.delta || {};
    if (typeof delta.content === "string") result.content += delta.content;
    if (typeof delta.reasoning_content === "string") result.reasoning += delta.reasoning_content;
    if (Array.isArray(delta.tool_calls)) {
      for (const fragment of delta.tool_calls) {
        const index = typeof fragment.index === "number" ? fragment.index : 0;
        const slot =
          result.toolCalls[index] ||
          (result.toolCalls[index] = { id: "", type: "", name: "", arguments: "" });
        if (fragment.id) slot.id = fragment.id;
        if (fragment.type) slot.type = fragment.type;
        if (fragment.function) {
          if (fragment.function.name) slot.name = fragment.function.name;
          if (typeof fragment.function.arguments === "string") {
            slot.arguments += fragment.function.arguments;
          }
        }
      }
    }
    if (choice.finish_reason) result.finishReason = choice.finish_reason;
  }
  return result;
}

function parseSse(text) {
  return foldSse(parseSseChunks(text));
}

module.exports = { parseSse, parseSseChunks, foldSse };

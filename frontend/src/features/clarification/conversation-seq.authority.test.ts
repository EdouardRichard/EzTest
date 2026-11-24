import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { describe, expect, it } from "vitest"

import { STREAMING_PART_TYPES } from "@/features/conversation/types"

import type { ConversationMessage } from "./types"
import { streamingSnapshotParts } from "./useConversation"

/**
 * Cross-language behaviour gate for the conversation streaming seq assignment
 * (v0.2.4-H H-E19).
 *
 * The server assigns a 1-based seq, in part order, ONLY to an assistant
 * message's parts in the streaming enum (`publishMessageEvent` /
 * `assistantStreamParts` in backend/internal/version/application/conversation.go);
 * snapshot-only types (`image`/`rollback_proposal`) stay unsequenced. There is no
 * Go->TS codegen pipeline in this worktree, so the single-source mechanism is the
 * one the enum unions already use: a SHARED, checked case set. The fixture lives
 * next to the Go authority and is evaluated by BOTH sides —
 * `backend/internal/version/application/conversation_seq_gate_test.go` runs the
 * Go functions, this file runs the TypeScript mirror. A semantic drift on either
 * side fails its own gate instead of silently diverging.
 *
 * Path resolution mirrors `requirement-precheck.authority.test.ts`:
 * `fileURLToPath(import.meta.url)` (never `new URL(literal, import.meta.url)`,
 * which Vite/vitest rewrite — doc 19 §4 invariant ⑬).
 */
function sharedFixturePath(): string {
  // here = frontend/src/features/clarification → four levels up = repo root.
  const here = dirname(fileURLToPath(import.meta.url))
  return resolve(
    here,
    "../../../../backend/internal/version/application/testdata/conversation_seq_gate.json",
  )
}

type SeqGateCase = {
  name: string
  role: string
  parts: Array<{ type: string }>
  expected_seqs: Array<number | null>
}

type SeqGateFixture = { cases: SeqGateCase[] }

// Reading the file here (not lazily) means a missing/empty fixture fails loudly:
// the shared case set is the whole point of the gate.
const fixture = JSON.parse(readFileSync(sharedFixturePath(), "utf8")) as SeqGateFixture

const GATE_MIN_CASES = 6

describe("conversation seq cross-language authority gate (H-E19)", () => {
  it("loads a non-empty shared case set", () => {
    expect(fixture.cases.length).toBeGreaterThanOrEqual(GATE_MIN_CASES)
  })

  it("covers a snapshot-only type and a non-assistant role", () => {
    // Anti-vacuity: use the pinned streaming vocabulary to identify snapshot-only
    // types (never a re-typed list), and require a role the rule must skip.
    const streaming = new Set<string>(STREAMING_PART_TYPES)
    const hasSnapshotOnly = fixture.cases.some((test) =>
      test.parts.some((part) => !streaming.has(part.type)),
    )
    const hasNonAssistant = fixture.cases.some((test) => test.role !== "assistant")
    expect(hasSnapshotOnly).toBe(true)
    expect(hasNonAssistant).toBe(true)
  })

  for (const test of fixture.cases) {
    it(`seq assignment: ${test.name}`, () => {
      const message = {
        id: "1",
        conversation_id: "1",
        role: test.role,
        parts: test.parts.map((part) => ({ ...part })),
        refs: [],
        created_at: "2026-11-23T00:00:00Z",
        stream_state: "streaming",
      } as unknown as ConversationMessage
      const parts = streamingSnapshotParts(message) as Array<{ seq?: number }>
      const actual = parts.map((part) => (typeof part.seq === "number" ? part.seq : null))
      expect(actual).toEqual(test.expected_seqs)
    })
  }
})

import { describe, expect, it } from "vitest"

import type { MessageDelta, MessagePart } from "@/features/conversation/types"

import type { ConversationMessage } from "./types"
import {
  type ConversationStreamEntry,
  mergePartsBySeq,
  publishConversationDelta,
  publishConversationFrame,
  publishStreamReset,
  seedConversationSnapshot,
  subscribeConversationStream,
} from "./useConversation"

/**
 * Direct unit tests for the seq merge (T7, SC-D2). Expectations come from the
 * T5 §2.3 accumulation contract, NOT from observed output:
 * - delta with `part` -> replace the part at seq wholesale;
 * - delta with `increment` -> cur = accumulated UTF-16 length (String.length):
 *   cur == offset append; cur >= offset+len replay-skip; cur < offset gap;
 *   offset < cur < offset+len partial-overlap gap. Gaps never corrupt.
 */

type SequencedPart = MessagePart & { seq: number }

function textPart(seq: number, text: string): SequencedPart {
  return { seq, type: "text", text }
}

function delta(
  overrides: Partial<MessageDelta> & Pick<MessageDelta, "seq" | "part_type">,
): MessageDelta {
  return { message_id: "5001", conversation_id: "4001", ...overrides }
}

function appendDelta(seq: number, offset: number | undefined, increment: string): MessageDelta {
  return delta({ seq, part_type: "text", ...(offset === undefined ? {} : { offset }), increment })
}

function headMessage(
  parts: SequencedPart[],
  streamState: "streaming" | "complete" | "interrupted",
): ConversationMessage {
  return {
    id: "5001",
    conversation_id: "4001",
    role: "assistant",
    parts,
    refs: [],
    created_at: "2026-11-17T10:00:00Z",
    stream_state: streamState,
  }
}

/** A REST snapshot part: the HTTP message DTO carries no seq (only SSE frames do). */
function unsequencedText(text: string): MessagePart {
  return { type: "text", text }
}

/**
 * One assistant head as the HTTP messages endpoint returns it mid-reply: the
 * stored accumulated text, no per-part seq, and stream_state "streaming".
 */
function snapshotHead(parts: MessagePart[]): ConversationMessage {
  return {
    id: "5001",
    conversation_id: "4001",
    role: "assistant",
    parts,
    refs: [],
    created_at: "2026-11-17T10:00:00Z",
    stream_state: "streaming",
  }
}

describe("mergePartsBySeq increment accumulation (T5 §2.3)", () => {
  it("appends an increment at the same seq when cur == offset", () => {
    const merged = mergePartsBySeq([textPart(1, "Hel")], [appendDelta(1, 3, "lo")])
    expect(merged).toEqual([textPart(1, "Hello")])
  })

  it("creates the part from empty on the first delta (offset omitted)", () => {
    const merged = mergePartsBySeq([], [appendDelta(1, undefined, "Hi")])
    expect(merged).toEqual([textPart(1, "Hi")])
  })

  it("treats an omitted offset as 0 against existing text as a replay, not an append", () => {
    // cur=2 >= offset(0)+len(2): the whole first frame arrived again.
    const merged = mergePartsBySeq([textPart(1, "Hi")], [appendDelta(1, undefined, "Hi")])
    expect(merged).toEqual([textPart(1, "Hi")])
  })

  it("skips a replayed frame exactly at the accumulation boundary (cur == offset+len)", () => {
    const merged = mergePartsBySeq([textPart(1, "Hello")], [appendDelta(1, 3, "lo")])
    expect(merged).toEqual([textPart(1, "Hello")])
  })

  it("skips a replayed frame already covered by longer text (cur > offset+len)", () => {
    const merged = mergePartsBySeq([textPart(1, "Hello world")], [appendDelta(1, 3, "lo")])
    expect(merged).toEqual([textPart(1, "Hello world")])
  })

  it("does not corrupt on an out-of-order gap (cur < offset)", () => {
    const merged = mergePartsBySeq([textPart(1, "Hel")], [appendDelta(1, 9, "!!!")])
    expect(merged).toEqual([textPart(1, "Hel")])
  })

  it("does not corrupt on the partial-overlap band (offset < cur < offset+len)", () => {
    // cur=5, offset=3, len=8 -> 3 < 5 < 11: gap, never splice.
    const merged = mergePartsBySeq([textPart(1, "Hello")], [appendDelta(1, 3, "lo world")])
    expect(merged).toEqual([textPart(1, "Hello")])
  })

  it("does not fabricate text when a gap delta targets an unseen seq (cur=0 < offset)", () => {
    const merged = mergePartsBySeq([], [appendDelta(2, 5, "late")])
    expect(merged).toEqual([])
  })

  it("replaces the part at seq wholesale in replace mode (text rewrite)", () => {
    const replacement = delta({ seq: 1, part_type: "text", part: { type: "text", text: "new" } })
    const merged = mergePartsBySeq([textPart(1, "old text")], [replacement])
    expect(merged).toEqual([textPart(1, "new")])
  })

  it("replaces a tool_call part wholesale and annotates its seq", () => {
    const toolCall: MessagePart = {
      type: "tool_call",
      call_id: "call-1",
      name: "version.requirement.write",
      status: "running",
      arguments: { title: "退款时效" },
    }
    const merged = mergePartsBySeq([], [delta({ seq: 2, part_type: "tool_call", part: toolCall })])
    expect(merged).toEqual([{ ...toolCall, seq: 2 }])
  })

  it("replaces on a same-seq part-type change (thinking -> text)", () => {
    const thinking: SequencedPart = { seq: 1, type: "thinking", text: "核对", duration_ms: 0 }
    const merged = mergePartsBySeq(
      [thinking],
      [delta({ seq: 1, part_type: "text", part: { type: "text", text: "Hi" } })],
    )
    expect(merged).toEqual([textPart(1, "Hi")])
  })

  it("never appends against a different-type part at the same seq (gap, heal later)", () => {
    const thinking: SequencedPart = { seq: 1, type: "thinking", text: "核对", duration_ms: 0 }
    const merged = mergePartsBySeq([thinking], [appendDelta(1, 2, "退款")])
    expect(merged).toEqual([thinking])
  })

  it("creates a thinking part with duration_ms 0 and takes duration from a replace delta", () => {
    const first = mergePartsBySeq([], [delta({ seq: 1, part_type: "thinking", increment: "核对" })])
    expect(first).toEqual([{ seq: 1, type: "thinking", text: "核对", duration_ms: 0 }])
    const grown = mergePartsBySeq(first, [
      delta({ seq: 1, part_type: "thinking", offset: 2, increment: "退款时效" }),
    ])
    expect(grown).toEqual([{ seq: 1, type: "thinking", text: "核对退款时效", duration_ms: 0 }])
    const timed = mergePartsBySeq(grown, [
      delta({
        seq: 1,
        part_type: "thinking",
        part: { type: "thinking", text: "核对退款时效", duration_ms: 2000 },
      }),
    ])
    expect(timed).toEqual([{ seq: 1, type: "thinking", text: "核对退款时效", duration_ms: 2000 }])
  })

  it("keeps full frames and deltas consistent when mixed (full replace, then append)", () => {
    const accumulated = mergePartsBySeq(
      [],
      [appendDelta(1, undefined, "Hel"), appendDelta(1, 3, "lo")],
    )
    // A full frame snapshot for the same seq replaces wholesale.
    const healed = mergePartsBySeq(accumulated, [textPart(1, "Hello")])
    expect(healed).toEqual([textPart(1, "Hello")])
    // The next increment continues from the healed snapshot.
    const continued = mergePartsBySeq(healed, [appendDelta(1, 5, "!")])
    expect(continued).toEqual([textPart(1, "Hello!")])
  })

  it("sorts out-of-order seqs ascending (deltas and full parts mixed)", () => {
    const merged = mergePartsBySeq(
      [],
      [
        appendDelta(2, undefined, "second"),
        textPart(1, "first"),
        appendDelta(3, undefined, "third"),
      ],
    )
    expect(merged).toEqual([textPart(1, "first"), textPart(2, "second"), textPart(3, "third")])
  })

  it("counts offsets in UTF-16 code units for Chinese BMP text", () => {
    const start = mergePartsBySeq([], [delta({ seq: 1, part_type: "text", increment: "你" })])
    const merged = mergePartsBySeq(start, [appendDelta(1, 1, "好")])
    expect(merged).toEqual([textPart(1, "你好")])
    // Replayed "好" (offset 1, len 1) against cur=2: 2 >= 1+1 -> skip.
    expect(mergePartsBySeq(merged, [appendDelta(1, 1, "好")])).toEqual([textPart(1, "你好")])
  })

  it("counts offsets in UTF-16 code units for astral emoji (one rune = 2 units)", () => {
    const start = mergePartsBySeq([], [delta({ seq: 1, part_type: "text", increment: "😀" })])
    expect((start[0] as { text: string }).text.length).toBe(2)
    const merged = mergePartsBySeq(start, [appendDelta(1, 2, "!")])
    expect(merged).toEqual([textPart(1, "😀!")])
    // Replayed emoji frame (offset 0, len 2) against cur=3: 3 >= 0+2 -> skip.
    expect(mergePartsBySeq(merged, [appendDelta(1, 0, "😀")])).toEqual([textPart(1, "😀!")])
    // A byte/rune-based offset (1) would land in the partial-overlap band: gap.
    expect(mergePartsBySeq(merged, [appendDelta(1, 1, "😀")])).toEqual([textPart(1, "😀!")])
  })

  it("ignores malformed deltas (empty increment, negative offset, no mode)", () => {
    const existing = [textPart(1, "Hel")]
    expect(mergePartsBySeq(existing, [appendDelta(1, 3, "")])).toEqual(existing)
    expect(mergePartsBySeq(existing, [appendDelta(1, -1, "lo")])).toEqual(existing)
    expect(mergePartsBySeq(existing, [delta({ seq: 1, part_type: "text" })])).toEqual(existing)
    // Append mode is text/thinking only; an increment on tool_call is malformed.
    expect(
      mergePartsBySeq(existing, [delta({ seq: 1, part_type: "tool_call", increment: "x" })]),
    ).toEqual(existing)
  })

  it("keeps the legacy full-part semantics (same seq replace, seq-less snapshot rule)", () => {
    expect(mergePartsBySeq([textPart(1, "old")], [textPart(1, "new")])).toEqual([
      textPart(1, "new"),
    ])
    const legacy = [{ type: "text", text: "snapshot" } as MessagePart]
    expect(mergePartsBySeq(legacy, [{ type: "text", text: "newer" } as MessagePart])).toEqual([
      { type: "text", text: "newer" },
    ])
  })
})

/**
 * H-E18: the server assigns a streaming seq ONLY to assistant parts inside the
 * streaming enum (`publishMessageEvent` / `assistantStreamParts`). `image` and
 * `rollback_proposal` are snapshot-only and therefore NEVER carry a seq, so a
 * merge that only re-adds the INCOMING unsequenced parts silently deletes them
 * from the accumulated head — one text delta made the picture disappear.
 */
describe("mergePartsBySeq keeps snapshot-only parts (H-E18)", () => {
  const imagePart: MessagePart = {
    type: "image",
    mime: "image/png",
    data_ref: "sha256-ref",
    width: 4,
    height: 3,
  }

  it("keeps an existing image part when a text delta arrives at another part", () => {
    const merged = mergePartsBySeq([textPart(1, "Hel"), imagePart], [appendDelta(1, 3, "lo")])
    expect(merged).toEqual([textPart(1, "Hello"), imagePart])
  })

  it("keeps an existing rollback_proposal part (snapshot-only, never sequenced)", () => {
    const proposal = { type: "rollback_proposal", case_version_id: "77" }
    const merged = mergePartsBySeq<unknown>(
      [textPart(1, "Hel"), proposal],
      [appendDelta(1, 3, "lo")],
    )
    expect(merged).toEqual([textPart(1, "Hello"), proposal])
  })

  it("keeps every existing unsequenced part in its original relative order", () => {
    const first = { type: "image", mime: "image/png", data_ref: "a", width: 1, height: 1 }
    const second = { type: "rollback_proposal", case_version_id: "78" }
    const merged = mergePartsBySeq<unknown>(
      [textPart(1, "Hel"), first, second],
      [appendDelta(1, 3, "lo")],
    )
    expect(merged).toEqual([textPart(1, "Hello"), first, second])
  })

  it("never emits a delta-shaped item as a part (fail-closed on a malformed carrier)", () => {
    // A carrier without a valid seq is neither a delta nor a part: it must not
    // reach the rendered parts array (the old blanket `as T[]` let it through).
    const merged = mergePartsBySeq<unknown>(
      [textPart(1, "Hel")],
      [appendDelta(1, 3, "lo"), { part_type: "text", increment: " orphan" }],
    )
    expect(merged).toEqual([textPart(1, "Hello")])
  })
})

/**
 * H-E25: the pre-snapshot buffer used to keep the applicable prefix and DROP the
 * newest increments past 4096. Ruling T15-R1 replaces that loss with lossless
 * compaction: contiguous same-seq appends concatenate, a replace-mode delta
 * supersedes the earlier entries of its seq, and nothing is discarded.
 */
describe("pre-snapshot buffer is lossless (H-E25)", () => {
  function capture(versionId: string) {
    const seen: ConversationStreamEntry[] = []
    const unsubscribe = subscribeConversationStream((changedVersionId, entry) => {
      if (changedVersionId === versionId) seen.push(entry)
    })
    return { seen, unsubscribe }
  }

  it("keeps every increment past the old 4096-entry cap", () => {
    const { seen, unsubscribe } = capture("9120")
    const stored = "Hello"
    let accumulated = stored
    // The head is unknown to this client, so every increment is buffered.
    for (let index = 0; index < 5000; index += 1) {
      publishConversationDelta("9120", appendDelta(1, accumulated.length, "x"))
      accumulated += "x"
    }
    expect(seen).toHaveLength(0)

    seedConversationSnapshot("9120", snapshotHead([unsequencedText(stored)]))
    const parts = seen[seen.length - 1].message.parts as { text: string }[]
    expect(parts[0].text).toBe(accumulated)
    // "Hello" (5) + 5000 applied increments = 5005 UTF-16 units. (The earlier
    // draft asserted 5004 — an off-by-one in the test literal; the preceding
    // `toBe(accumulated)` already pinned the true length.)
    expect(parts[0].text.length).toBe(accumulated.length)
    expect(parts[0].text.length).toBe(5005)
    unsubscribe()
  })

  it("compacts a replayed identical increment instead of applying it twice", () => {
    const { seen, unsubscribe } = capture("9121")
    publishConversationDelta("9121", appendDelta(1, 5, "abc"))
    publishConversationDelta("9121", appendDelta(1, 5, "abc"))
    publishConversationDelta("9121", appendDelta(1, 8, "def"))

    seedConversationSnapshot("9121", snapshotHead([unsequencedText("Hello")]))
    expect(seen[seen.length - 1].message.parts).toEqual([textPart(1, "Helloabcdef")])
    unsubscribe()
  })

  it("keeps non-contiguous increments and other seqs independent", () => {
    const { seen, unsubscribe } = capture("9122")
    publishConversationDelta("9122", appendDelta(1, 5, "abc"))
    publishConversationDelta("9122", appendDelta(2, 2, "XY"))
    // A gap at seq 1 (offset 99) must stay a separate entry, not be spliced.
    publishConversationDelta("9122", appendDelta(1, 99, "!!"))

    seedConversationSnapshot(
      "9122",
      snapshotHead([unsequencedText("Hello"), unsequencedText("ab")]),
    )
    expect(seen[seen.length - 1].message.parts).toEqual([
      textPart(1, "Helloabc"),
      textPart(2, "abXY"),
    ])
    unsubscribe()
  })

  it("does not duplicate a snapshot-only part when a seed merges projection and snapshot", () => {
    // Regression guard for the H-E18 fix: keeping BOTH sides' unsequenced parts
    // must not render the same image twice.
    const { seen, unsubscribe } = capture("9123")
    const image = { type: "image", mime: "image/png", data_ref: "same", width: 1, height: 1 }
    publishConversationFrame("9123", {
      ...headMessage([], "complete"),
      parts: [textPart(1, "Hello"), image] as MessagePart[],
    })
    seedConversationSnapshot("9123", snapshotHead([unsequencedText("Hello"), image as MessagePart]))
    const parts = seen[seen.length - 1].message.parts as { type: string }[]
    expect(parts.filter((part) => part.type === "image")).toHaveLength(1)
    unsubscribe()
  })
})

describe("conversation stream projection (single accumulator, 039-safe)", () => {
  function capture(versionId: string) {
    const seen: ConversationStreamEntry[] = []
    const unsubscribe = subscribeConversationStream((changedVersionId, entry) => {
      if (changedVersionId === versionId) seen.push(entry)
    })
    return { seen, unsubscribe }
  }

  it("stores a full frame wholesale and notifies with fullFrame=true", () => {
    const { seen, unsubscribe } = capture("9101")
    const frame = headMessage([textPart(1, "Hello")], "complete")
    publishConversationFrame("9101", frame)
    expect(seen).toHaveLength(1)
    expect(seen[0].fullFrame).toBe(true)
    expect(seen[0].message).toEqual(frame)
    unsubscribe()
  })

  it("accumulates deltas into the stored head and notifies once per change", () => {
    const { seen, unsubscribe } = capture("9102")
    publishConversationFrame("9102", headMessage([], "streaming"))
    publishConversationDelta("9102", appendDelta(1, undefined, "Hel"))
    publishConversationDelta("9102", appendDelta(1, 3, "lo"))
    expect(seen).toHaveLength(3)
    expect(seen[2].fullFrame).toBe(false)
    expect(seen[2].message.parts).toEqual([textPart(1, "Hello")])
    expect(seen[2].message.stream_state).toBe("streaming")
    unsubscribe()
  })

  it("does not notify on replayed or gap deltas (no change, no corruption)", () => {
    const { seen, unsubscribe } = capture("9103")
    publishConversationFrame("9103", headMessage([], "streaming"))
    publishConversationDelta("9103", appendDelta(1, undefined, "Hel"))
    const before = seen.length
    publishConversationDelta("9103", appendDelta(1, undefined, "Hel")) // replay
    publishConversationDelta("9103", appendDelta(1, 9, "!!!")) // gap
    publishConversationDelta("9103", appendDelta(1, 1, "ello")) // partial overlap
    expect(seen).toHaveLength(before)
    unsubscribe()
  })

  it("heals a gap wholesale from the terminal full frame", () => {
    const { seen, unsubscribe } = capture("9104")
    publishConversationFrame("9104", headMessage([], "streaming"))
    publishConversationDelta("9104", appendDelta(1, undefined, "Hel"))
    publishConversationDelta("9104", appendDelta(1, 9, "!!!")) // gap: skipped
    publishConversationFrame("9104", headMessage([textPart(1, "Hello world!!!")], "complete"))
    const last = seen[seen.length - 1]
    expect(last.fullFrame).toBe(true)
    expect(last.message.parts).toEqual([textPart(1, "Hello world!!!")])
    expect(last.message.stream_state).toBe("complete")
    unsubscribe()
  })

  it("creates a skeleton head from a valid first append when no frame arrived yet", () => {
    const { seen, unsubscribe } = capture("9105")
    publishConversationDelta("9105", appendDelta(1, undefined, "Hi"))
    expect(seen).toHaveLength(1)
    expect(seen[0].message.id).toBe("5001")
    expect(seen[0].message.conversation_id).toBe("4001")
    expect(seen[0].message.role).toBe("assistant")
    expect(seen[0].message.parts).toEqual([textPart(1, "Hi")])
    unsubscribe()
  })

  it("resumes an unseen head from the HTTP snapshot instead of freezing (I-1)", () => {
    // User scenario: the page was reloaded (or a second tab opened) mid-reply.
    // The server's accumulated offset is already past 0, so the increment cannot
    // be applied yet — it must survive until the snapshot seeds the head, and
    // then continue the stored text. Pinning "no head is created" alone would
    // pin the freeze as the contract (method discipline ⑩).
    const { seen, unsubscribe } = capture("9106")
    publishConversationDelta("9106", appendDelta(1, 5, "late"))
    expect(seen).toHaveLength(0) // nothing to render yet: the head is unknown

    seedConversationSnapshot("9106", snapshotHead([unsequencedText("Hello")]))
    const last = seen[seen.length - 1]
    expect(last.message.parts).toEqual([textPart(1, "Hellolate")])
    expect(last.message.stream_state).toBe("streaming")

    // The live edge continues from the resumed text.
    publishConversationDelta("9106", appendDelta(1, 9, "!"))
    expect(seen[seen.length - 1].message.parts).toEqual([textPart(1, "Hellolate!")])
    unsubscribe()
  })

  it("assigns the server streaming seq to snapshot parts, skipping snapshot-only types", () => {
    const { seen, unsubscribe } = capture("9110")
    seedConversationSnapshot(
      "9110",
      snapshotHead([
        { type: "thinking", text: "核对", duration_ms: 1200 },
        unsequencedText("Hello"),
        { type: "image", mime: "image/png", data_ref: "ref", width: 1, height: 1 },
      ]),
    )
    const parts = seen[seen.length - 1].message.parts as (MessagePart & { seq?: number })[]
    expect(parts.map((part) => part.seq)).toEqual([1, 2, undefined])

    // The text part is at seq 2 (the image does not consume a streaming seq), so
    // its increment accumulates onto the stored text.
    publishConversationDelta("9110", appendDelta(2, 5, " world"))
    expect(seen[seen.length - 1].message.parts[1]).toEqual(textPart(2, "Hello world"))
    unsubscribe()
  })

  it("never regresses a newer live projection with an older HTTP snapshot", () => {
    const { seen, unsubscribe } = capture("9111")
    publishConversationFrame("9111", headMessage([], "streaming"))
    publishConversationDelta("9111", appendDelta(1, undefined, "Hello world"))
    // A snapshot refetch read the row before the last increments landed.
    seedConversationSnapshot("9111", snapshotHead([unsequencedText("Hello")]))
    expect(seen[seen.length - 1].message.parts).toEqual([textPart(1, "Hello world")])
    unsubscribe()
  })

  it("resumes after stream.reset dropped the projection mid-reply", () => {
    const { seen, unsubscribe } = capture("9112")
    publishConversationFrame("9112", headMessage([], "streaming"))
    publishConversationDelta("9112", appendDelta(1, undefined, "Hello"))
    publishStreamReset("9112", {})
    publishConversationDelta("9112", appendDelta(1, 5, " again"))
    seedConversationSnapshot("9112", snapshotHead([unsequencedText("Hello")]))
    expect(seen[seen.length - 1].message.parts).toEqual([textPart(1, "Hello again")])
    unsubscribe()
  })

  it("never revives a terminal head from an older streaming snapshot", () => {
    const { seen, unsubscribe } = capture("9114")
    publishConversationFrame("9114", headMessage([textPart(1, "Done")], "complete"))
    // A snapshot read before the terminal frame landed still says streaming.
    seedConversationSnapshot("9114", snapshotHead([unsequencedText("Done")]))
    expect(seen[seen.length - 1].message.stream_state).toBe("complete")
    unsubscribe()
  })

  it("replays buffered increments onto a full frame that never had a snapshot", () => {
    const { seen, unsubscribe } = capture("9113")
    publishConversationDelta("9113", appendDelta(1, 5, "late"))
    publishConversationFrame("9113", headMessage([textPart(1, "Hello")], "streaming"))
    const last = seen[seen.length - 1]
    expect(last.fullFrame).toBe(true)
    expect(last.message.parts).toEqual([textPart(1, "Hellolate")])
    unsubscribe()
  })

  it("ignores malformed delta payloads without notifying", () => {
    const { seen, unsubscribe } = capture("9107")
    publishConversationDelta("9107", {
      message_id: "5001",
      conversation_id: "4001",
      seq: 0,
      part_type: "text",
      increment: "x",
    })
    publishConversationDelta("9107", {
      message_id: "5001",
      conversation_id: "4001",
      seq: 1,
      part_type: "text",
    })
    publishConversationDelta("9107", {
      message_id: 5001,
      conversation_id: "4001",
      seq: 1,
      part_type: "text",
      increment: "x",
    })
    publishConversationDelta("9107", { seq: 1, part_type: "text", increment: "x" })
    expect(seen).toHaveLength(0)
    unsubscribe()
  })

  it("keeps versions isolated (a delta never lands in another version's stream)", () => {
    const a = capture("9108")
    const b = capture("9109")
    publishConversationFrame("9108", headMessage([], "streaming"))
    publishConversationDelta("9108", appendDelta(1, undefined, "Hel"))
    expect(a.seen).toHaveLength(2)
    expect(b.seen).toHaveLength(0)
    a.unsubscribe()
    b.unsubscribe()
  })
})

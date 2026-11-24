"use client"

import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react"

import type { JobStatus, MessageDelta, MessagePart } from "@/features/conversation/types"
import { STREAMING_PART_TYPES } from "@/features/conversation/types"
import type { ApiPage } from "@/lib/api-client"

import {
  listMessages,
  listQuestions,
  postMessage,
  type QuestionAnswer,
  type QuestionBatch,
  type QuestionItem,
  type QuestionPart,
  type QuestionStatus,
} from "./api"
import { isClarificationQuestionBatch } from "./question-kinds"
import type { ConversationMessage, NewMessagePart, SendOptions } from "./types"

/** Matches the messages endpoint default: a full page implies another page. */
const MESSAGES_PAGE_SIZE = 100

/**
 * How many authoritative snapshot re-reads a lagging streaming head may force
 * within one load generation before the thread stops re-reading and waits for
 * the terminal full frame to heal (bounded: never a refetch storm). Mirrors the
 * drawer's MAX_STALE_REREADS budget.
 */
export const MAX_FORWARD_GAP_REREADS = 2

/**
 * Debounce before a forward-offset-gap snapshot re-read. It must be at least one
 * bounded-flush period (coalesced persistence every 250ms) so the refetch reads
 * the text the live increment's offset refers to.
 */
export const FORWARD_GAP_REREAD_DELAY_MS = 300

/** Stable empty value for a conversation without a persisted question snapshot. */
const NO_QUESTIONS: QuestionBatch[] = []

/**
 * The messages endpoint returns one cursor page. A bare item list (older caller
 * or test double) is still accepted and promoted to a page; the page-size
 * heuristic only applies when the server envelope is absent.
 */
function toMessagePage(
  value: ApiPage<ConversationMessage> | ConversationMessage[],
): ApiPage<ConversationMessage> {
  if (!Array.isArray(value)) return value
  const last = value.length > 0 ? value[value.length - 1].id : ""
  const hasMore = value.length >= MESSAGES_PAGE_SIZE
  return { items: value, nextCursor: hasMore ? last : "", hasMore }
}

function newestMessageId(messages: readonly ConversationMessage[]): string {
  return messages.length > 0 ? messages[messages.length - 1].id : ""
}

/* --------------------------- streamed part merge ---------------------------- */

function seqOf(part: unknown): number | null {
  if (typeof part !== "object" || part === null) return null
  const seq = (part as { seq?: unknown }).seq
  return typeof seq === "number" && Number.isInteger(seq) && seq > 0 ? seq : null
}

/**
 * Detects a `conversation.message.delta` carrier (T5 §2.3) among incoming
 * items: a positive `seq` plus a string `part_type`. Full parts carry `type`
 * instead, so the two shapes never collide.
 */
function deltaOf(item: unknown): MessageDelta | null {
  if (typeof item !== "object" || item === null) return null
  const record = item as Record<string, unknown>
  if (typeof record.part_type !== "string" || seqOf(record) === null) return null
  return record as unknown as MessageDelta
}

/**
 * Validates the two delta modes of T5 §2.3. Replace mode needs a full part
 * snapshot (an object with a string `type`); append mode is text/thinking only,
 * with a non-empty increment and a non-negative integer offset. Anything else
 * is malformed and must not change the accumulated part.
 */
function deltaMode(delta: MessageDelta): "append" | "replace" | null {
  if (isRecord(delta.part) && typeof delta.part.type === "string") return "replace"
  if (
    typeof delta.increment === "string" &&
    delta.increment.length > 0 &&
    (delta.part_type === "text" || delta.part_type === "thinking")
  ) {
    const offset = delta.offset ?? 0
    if (Number.isInteger(offset) && offset >= 0) return "append"
  }
  return null
}

/**
 * Applies one delta to the part accumulated at its seq (T5 §2.3). Returns the
 * next part, or `undefined` when the delta must change nothing:
 * - replace mode (`part`): the snapshot replaces the part wholesale;
 * - append mode (`increment`): cur = accumulated text length in UTF-16 code
 *   units (`String.length`, the server's offset unit). The increment appends
 *   ONLY when `cur === offset` (strict equality). Any other accumulated length —
 *   a replayed frame (cur > offset), an out-of-order gap (cur < offset), or a
 *   partial overlap — leaves the part untouched and heals from the next full
 *   frame; this accumulation never splices partial text. An append against a
 *   different-type part is likewise left untouched (the server sends replace mode
 *   on type changes, so this means a missed frame).
 */
function applyDelta(current: unknown, delta: MessageDelta): unknown | undefined {
  const mode = deltaMode(delta)
  if (mode === "replace") {
    return { ...(delta.part as MessagePart), seq: delta.seq }
  }
  if (mode !== "append") return undefined
  let curText = ""
  let base: Record<string, unknown> = { type: delta.part_type }
  if (current !== undefined) {
    if (
      !isRecord(current) ||
      current.type !== delta.part_type ||
      typeof current.text !== "string"
    ) {
      return undefined
    }
    curText = current.text
    base = current
  }
  const offset = delta.offset ?? 0
  const increment = delta.increment as string
  if (curText.length !== offset) return undefined
  if (delta.part_type === "thinking" && base.duration_ms === undefined) {
    // duration_ms arrives via replace-mode deltas and the terminal full frame,
    // never per text increment; the streaming block reads "思考了 0s" until then.
    base = { ...base, duration_ms: 0 }
  }
  return { ...base, text: curText + increment, seq: delta.seq }
}

/** A rendered part must carry a string `type`; a seq-less delta carrier does not. */
function asPart<T>(item: unknown): T | null {
  if (!isRecord(item)) return null
  return typeof (item as { type?: unknown }).type === "string" ? (item as T) : null
}

/**
 * Content key for an unsequenced part. Snapshot-only parts (image,
 * rollback_proposal) are carried through the seq merge untouched, so a snapshot
 * seeded from an overlapping live projection must render each one once, not
 * twice; content-addressed parts are equal exactly when their JSON is.
 */
function partContentKey(part: unknown): string {
  try {
    return JSON.stringify(part)
  } catch {
    return ""
  }
}

/**
 * A snapshot-only part: a real part whose type is OUTSIDE the streaming enum
 * (`image`/`rollback_proposal`). These never get a seq, so a seq merge carries
 * them through untouched. An unsequenced STREAMING-type part is instead a legacy
 * raw snapshot copy of a part that got a seq in the live projection; the
 * sequenced copy supersedes it and must not be rendered a second time. (The
 * vocabulary is the pinned shared list, never a second copy; evaluated at call
 * time, after module init.)
 */
function isSnapshotOnlyPart(item: unknown): boolean {
  if (!isRecord(item)) return false
  const type = (item as { type?: unknown }).type
  return typeof type === "string" && !STREAMING_PART_TYPE_SET.has(type)
}

/**
 * Merges streamed message parts by their 1-based `seq` (OpenAPI
 * `MessagePartView.seq`). A later full payload for the same seq wins and the
 * result is ordered by seq, so a repeated SSE frame never renders the same part
 * twice.
 *
 * Incoming items may also be `conversation.message.delta` carriers (T5 §2.3):
 * a replace-mode delta swaps the part at seq wholesale, an append-mode delta
 * accumulates the text suffix at its offset (replay/gap-safe, see applyDelta).
 * This is the single accumulation rule for incremental Host replies.
 *
 * Parts WITHOUT a seq that are snapshot-only types (the server never assigns a
 * seq to `image`/`rollback_proposal`) are NOT touched by a delta and must
 * survive the merge: sequenced parts come first (sorted by seq), then the
 * snapshot-only parts in their original relative order (H-E18). An unsequenced
 * item of a STREAMING type is a legacy raw snapshot copy of a part that the
 * sequenced projection supersedes (not carried twice), and a seq-less item that
 * is not even a part (a malformed delta carrier) is dropped — never cast into the
 * result.
 *
 * Without any seq at all (the `conversation.message` full snapshot, P1 shape) the
 * newer snapshot replaces the older one; this also makes a duplicate frame
 * idempotent instead of appending a second copy.
 */
export function mergePartsBySeq<T>(
  existing: readonly T[],
  incoming: readonly (T | MessageDelta)[],
): T[] {
  const incomingSequenced = incoming.some((part) => seqOf(part) !== null)
  if (!incomingSequenced) {
    const parts: T[] = []
    for (const item of incoming) {
      const part = asPart<T>(item)
      if (part !== null) parts.push(part)
    }
    return parts.length >= existing.length ? parts : [...existing]
  }

  const bySeq = new Map<number, T>()
  for (const part of existing) {
    const seq = seqOf(part)
    if (seq === null) continue
    const narrowed = asPart<T>(part)
    if (narrowed !== null) bySeq.set(seq, narrowed)
  }
  for (const part of incoming) {
    const seq = seqOf(part)
    if (seq === null) continue
    const delta = deltaOf(part)
    if (delta !== null) {
      const next = applyDelta(bySeq.get(seq), delta)
      if (next !== undefined) {
        const narrowed = asPart<T>(next)
        if (narrowed !== null) bySeq.set(seq, narrowed)
      }
      continue
    }
    const narrowed = asPart<T>(part)
    if (narrowed !== null) bySeq.set(seq, narrowed)
  }
  const sequenced = [...bySeq.entries()]
    .sort(([leftSeq], [rightSeq]) => leftSeq - rightSeq)
    .map(([, part]) => part)
  const unsequenced: T[] = []
  const seenParts = new Set<string>()
  for (const part of existing) {
    if (seqOf(part) !== null) continue
    const narrowed = asPart<T>(part)
    if (narrowed === null || !isSnapshotOnlyPart(narrowed)) continue
    unsequenced.push(narrowed)
    seenParts.add(partContentKey(narrowed))
  }
  for (const part of incoming) {
    if (seqOf(part) !== null) continue
    const narrowed = asPart<T>(part)
    if (narrowed === null || !isSnapshotOnlyPart(narrowed)) continue
    const key = partContentKey(narrowed)
    if (seenParts.has(key)) continue
    seenParts.add(key)
    unsequenced.push(narrowed)
  }
  return [...sequenced, ...unsequenced]
}

/* ---------------------- conversation stream projection ---------------------- */

/**
 * One accumulated streaming message. `fullFrame` marks the last update as a
 * full `conversation.message` snapshot (authoritative parts, wholesale heal);
 * delta updates merge by seq instead.
 */
export type ConversationStreamEntry = {
  message: ConversationMessage
  fullFrame: boolean
}

type ConversationStreamListener = (versionId: string, entry: ConversationStreamEntry) => void

// The version channel has documented-intentional concurrent subscriptions
// (page-level header projection + tab workspace). Only the workspace owner —
// the useVersionEvents instance whose handlers include onConversationMessage —
// publishes here (defect 039): one accumulator per (version, message, seq) with
// offset dedup, so a replayed or double-delivered frame never appends twice.
const conversationStreams = new Map<string, Map<string, ConversationStreamEntry>>()
const conversationStreamListeners = new Set<ConversationStreamListener>()
/** Mirrors the workspace liveMessages cap so the projection cannot grow unbounded. */
const CONVERSATION_STREAM_LIMIT = 200

/** Subscribes a thread owner to changed streaming entries of any version. */
export function subscribeConversationStream(listener: ConversationStreamListener): () => void {
  conversationStreamListeners.add(listener)
  return () => {
    conversationStreamListeners.delete(listener)
  }
}

function storeConversationStreamEntry(versionId: string, entry: ConversationStreamEntry): void {
  let stream = conversationStreams.get(versionId)
  if (stream === undefined) {
    stream = new Map()
    conversationStreams.set(versionId, stream)
  }
  stream.set(entry.message.id, entry)
  while (stream.size > CONVERSATION_STREAM_LIMIT) {
    const oldest = stream.keys().next().value
    if (oldest === undefined) break
    stream.delete(oldest)
  }
  for (const listener of conversationStreamListeners) listener(versionId, entry)
}

/**
 * Stores one full `conversation.message` frame (Begin placeholder / terminal
 * snapshot). The frame is the server's authoritative parts snapshot: it replaces
 * the accumulated parts wholesale, healing any offset gap a delta left behind.
 * Deltas buffered before any head was known are replayed onto it, so a frame that
 * was read before those increments does not drop them.
 */
export function publishConversationFrame(versionId: string, message: ConversationMessage): void {
  const parts = replayPendingDeltas(versionId, message.id, message.parts)
  storeConversationStreamEntry(versionId, { message: { ...message, parts }, fullFrame: true })
}

/* ------------------------- snapshot resume (I-1) --------------------------- */

/** Runtime membership set derived from the pinned shared vocabulary. */
const STREAMING_PART_TYPE_SET: ReadonlySet<string> = new Set(STREAMING_PART_TYPES)

/**
 * Assigns the server's streaming seq to an HTTP snapshot's parts. This mirrors
 * the ONE server rule (`publishMessageEvent` / `assistantStreamParts` in
 * backend/internal/version/application/conversation.go): for an assistant
 * message, every part whose type is in the streaming enum gets the next 1-based
 * seq in part order; snapshot-only types (image/rollback_proposal) stay
 * unsequenced. The vocabulary is the pinned shared list, not a second copy.
 *
 * Without this, a thread loaded over HTTP has parts with no seq, so the next
 * `conversation.message.delta` finds no accumulated part at its seq, compares the
 * server's accumulated offset against empty text and skips every increment —
 * the live reply freezes until the turn's terminal frame.
 *
 * Exported (pure function) so the cross-language seq gate
 * (`conversation-seq.authority.test.ts`) evaluates THIS mirror against the shared
 * fixture the Go authority is also checked against (H-E19).
 */
export function streamingSnapshotParts(message: ConversationMessage): ConversationMessage["parts"] {
  if (message.role !== "assistant") return message.parts
  let seq = 0
  return message.parts.map((part) => {
    const type = (part as { type?: unknown }).type
    if (typeof type !== "string" || !STREAMING_PART_TYPE_SET.has(type)) return part
    seq += 1
    return { ...part, seq }
  })
}

/**
 * Deltas that arrived for a head this client has never seen (page reload, second
 * tab, `stream.reset` mid-reply). They cannot be applied yet — their offset is
 * the server's accumulated length, and there is nothing to accumulate onto — so
 * they wait for the HTTP snapshot instead of being dropped. Bounded: the queue
 * only lives until a snapshot or a full frame lands, and both caps below keep a
 * never-resolved head from growing without limit.
 */
const pendingConversationDeltas = new Map<string, Map<string, MessageDelta[]>>()
/**
 * Per-head queue cap for a never-resolved head (page reload / second tab /
 * `stream.reset` mid-reply). It is a BACKSTOP only: contiguous same-seq appends
 * coalesce to one entry, so a healthy stream stays at most one entry per seq
 * (well under this cap). It bounds a pathological non-contiguous flood.
 */
const PENDING_DELTA_LIMIT = 4096

/** The append range (offset) of a delta; null for replace-mode or a malformed carrier. */
function pendingAppendOffset(delta: MessageDelta): number | null {
  if (delta.part !== undefined) return null
  if (delta.part_type !== "text" && delta.part_type !== "thinking") return null
  if (typeof delta.increment !== "string" || delta.increment.length === 0) return null
  const offset = delta.offset ?? 0
  return Number.isInteger(offset) && offset >= 0 ? offset : null
}

/**
 * Losslessly compacts one delta into a head's pre-snapshot queue instead of
 * dropping the newest past a cap (H-E25, Ruling T15-R1):
 * - a contiguous same-seq, same-type append (`next.offset === prev.offset +
 *   prev.increment.length`, UTF-16 code units) coalesces into the existing
 *   entry, keeping the first offset and concatenating increments. The merged
 *   offset equals the first entry's and the concatenated length is unchanged, so
 *   `applyDelta`'s strict `curText.length !== offset` check still holds;
 * - an exact replay of an already-queued increment is dropped (applying it twice
 *   is a no-op under offset dedup);
 * - anything else (a gap, or a cross-type change) stays independent for the
 *   existing gap semantics.
 *
 * The pre-snapshot buffer only ever carries APPEND-mode deltas:
 * `publishConversationDelta` buffers a delta only when `part === undefined` and
 * its offset is positive, so replace-mode supersession has no production caller
 * here and is deliberately not implemented (no unwired branch).
 */
function compactPendingDelta(queue: MessageDelta[], delta: MessageDelta): void {
  const nextOffset = pendingAppendOffset(delta)
  if (nextOffset === null) {
    queue.push(delta)
    return
  }
  const index = queue.findIndex((entry) => entry.seq === delta.seq)
  if (index < 0) {
    queue.push(delta)
    return
  }
  const prev = queue[index]
  if (
    prev.part === undefined &&
    prev.part_type === delta.part_type &&
    (prev.offset ?? 0) === nextOffset &&
    prev.increment === delta.increment
  ) {
    return
  }
  const prevOffset = pendingAppendOffset(prev)
  if (
    prevOffset !== null &&
    prev.part_type === delta.part_type &&
    nextOffset === prevOffset + (prev.increment as string).length
  ) {
    queue[index] = { ...prev, increment: (prev.increment as string) + (delta.increment as string) }
    return
  }
  queue.push(delta)
}

function bufferPendingDelta(versionId: string, delta: MessageDelta): void {
  let stream = pendingConversationDeltas.get(versionId)
  if (stream === undefined) {
    stream = new Map()
    pendingConversationDeltas.set(versionId, stream)
  }
  let queue = stream.get(delta.message_id)
  if (queue === undefined) {
    queue = []
    stream.set(delta.message_id, queue)
  }
  compactPendingDelta(queue, delta)
  // Backstop (unreachable for a contiguous server stream, which coalesces to one
  // entry per seq): bound a pathological non-contiguous flood to the applicable
  // prefix instead of growing without limit.
  if (queue.length > PENDING_DELTA_LIMIT) queue.length = PENDING_DELTA_LIMIT
  while (stream.size > CONVERSATION_STREAM_LIMIT) {
    const oldest = stream.keys().next().value
    if (oldest === undefined) break
    stream.delete(oldest)
  }
}

function takePendingDeltas(versionId: string, messageId: string): MessageDelta[] {
  const stream = pendingConversationDeltas.get(versionId)
  const queue = stream?.get(messageId)
  if (queue === undefined) return []
  stream?.delete(messageId)
  if (stream?.size === 0) pendingConversationDeltas.delete(versionId)
  return queue
}

function replayPendingDeltas(
  versionId: string,
  messageId: string,
  parts: ConversationMessage["parts"],
): ConversationMessage["parts"] {
  let merged = parts
  for (const delta of takePendingDeltas(versionId, messageId)) {
    merged = mergePartsBySeq<unknown>(merged, [delta]) as ConversationMessage["parts"]
  }
  return merged
}

/**
 * A terminal head never goes back to streaming: if the projection already saw the
 * terminal frame, an older HTTP snapshot (read before that frame landed) must not
 * revive the generating state.
 */
function resumedStreamState(
  existing: ConversationStreamEntry | undefined,
  snapshot: ConversationMessage,
): ConversationMessage["stream_state"] {
  const seen = existing?.message.stream_state
  if (seen === "complete" || seen === "interrupted") return seen
  return snapshot.stream_state
}

/**
 * Seeds the live stream projection from an HTTP message snapshot, so incremental
 * rendering RESUMES from the stored reply instead of freezing (v0.2.4-D fix wave,
 * I-1/#24). Called by the thread owner after every snapshot fetch for a head the
 * server still reports as `streaming`.
 *
 * The projection wins per seq over the snapshot (it is the newer accumulator);
 * the snapshot supplies the parts/seqs the projection has not seen. Deltas that
 * arrived before the snapshot are then replayed in arrival order, which fills the
 * band between the stored text and the live edge exactly.
 */
export function seedConversationSnapshot(versionId: string, message: ConversationMessage): void {
  const snapshotParts = streamingSnapshotParts(message)
  const existing = conversationStreams.get(versionId)?.get(message.id)
  const baseParts =
    existing === undefined
      ? snapshotParts
      : (mergePartsBySeq<unknown>(
          snapshotParts,
          existing.message.parts,
        ) as ConversationMessage["parts"])
  const pending = takePendingDeltas(versionId, message.id)
  let parts = baseParts
  for (const delta of pending) {
    parts = mergePartsBySeq<unknown>(parts, [delta]) as ConversationMessage["parts"]
  }
  storeConversationStreamEntry(versionId, {
    message: { ...message, stream_state: resumedStreamState(existing, message), parts },
    // A pure snapshot is authoritative (wholesale heal); a snapshot extended by
    // replayed increments merges by seq like any other delta update.
    fullFrame: existing === undefined && pending.length === 0,
  })
}

/** Validates one raw delta payload against the T5 §2.3 schema (MessageDelta). */
function normalizeMessageDelta(payload: Record<string, unknown>): MessageDelta | null {
  const messageId = payload.message_id
  const conversationId = payload.conversation_id
  const seq = payload.seq
  const partType = payload.part_type
  if (typeof messageId !== "string" || !/^[0-9]+$/.test(messageId)) return null
  if (typeof conversationId !== "string" || !/^[0-9]+$/.test(conversationId)) return null
  if (typeof seq !== "number" || !Number.isInteger(seq) || seq <= 0) return null
  if (typeof partType !== "string" || partType.length === 0) return null
  const delta: MessageDelta = {
    message_id: messageId,
    conversation_id: conversationId,
    seq,
    part_type: partType as MessageDelta["part_type"],
  }
  if (typeof payload.offset === "number") delta.offset = payload.offset
  if (typeof payload.increment === "string") delta.increment = payload.increment
  if (isRecord(payload.part)) delta.part = payload.part as unknown as MessagePart
  return deltaMode(delta) === null ? null : delta
}

/**
 * Accumulates one `conversation.message.delta` into the stored head by
 * (message_id, seq) — the T5 §2.3 rule via mergePartsBySeq's increment branch.
 * A replayed or gap delta changes nothing and notifies nothing (the next full
 * frame heals); a valid first append against an unseen head creates a skeleton
 * so a missed Begin frame cannot stall the live render. An increment that cannot
 * be applied because the head is unknown (its offset is already past 0) is
 * BUFFERED for the HTTP snapshot to resume from, never dropped (I-1).
 */
export function publishConversationDelta(
  versionId: string,
  payload: Record<string, unknown>,
): void {
  const delta = normalizeMessageDelta(payload)
  if (delta === null) return
  let entry = conversationStreams.get(versionId)?.get(delta.message_id)
  if (entry !== undefined && isForwardOffsetGap(entry, delta)) {
    // The stored projection lags the published offset (bounded DB flush; T28c).
    // The strict-equality append would drop every increment until the turn
    // seals. Drop the stale head so the owner's debounced authoritative snapshot
    // refetch rebuilds it from stored truth, and buffer this increment to replay.
    conversationStreams.get(versionId)?.delete(delta.message_id)
    notifyForwardGap(versionId, delta.conversation_id, delta.message_id)
    entry = undefined
  }
  if (entry === undefined && delta.part === undefined && (delta.offset ?? 0) > 0) {
    // The head exists on the server but not in this client (reload / second tab /
    // stream.reset). Keep the increment for seedConversationSnapshot.
    bufferPendingDelta(versionId, delta)
    return
  }
  const base: ConversationMessage = entry?.message ?? {
    id: delta.message_id,
    conversation_id: delta.conversation_id,
    role: "assistant",
    parts: [],
    refs: [],
    created_at: "",
  }
  const parts = mergePartsBySeq<unknown>(base.parts, [delta]) as ConversationMessage["parts"]
  const changed =
    parts.length !== base.parts.length || parts.some((part, index) => part !== base.parts[index])
  if (!changed) return
  storeConversationStreamEntry(versionId, { message: { ...base, parts }, fullFrame: false })
}

/* ------------------------ forward offset gap re-read ----------------------- */

type ForwardGapListener = (versionId: string, conversationId: string, messageId: string) => void

const forwardGapListeners = new Set<ForwardGapListener>()

/**
 * Subscribes a thread owner to forward-offset-gap notifications. This is the
 * module→hook hand-off: `publishConversationDelta` detects the lag, but only the
 * `useConversation` owner knows the reload budget and can refetch.
 */
export function subscribeForwardGap(listener: ForwardGapListener): () => void {
  forwardGapListeners.add(listener)
  return () => {
    forwardGapListeners.delete(listener)
  }
}

function notifyForwardGap(versionId: string, conversationId: string, messageId: string): void {
  for (const listener of forwardGapListeners) listener(versionId, conversationId, messageId)
}

/**
 * True when an APPEND delta targets a part whose locally accumulated text is
 * SHORTER than the delta's offset — the server's published text is ahead of the
 * stored projection. That happens when the thread is opened mid-turn: the HTTP
 * snapshot (and the seeded projection) lags the published text by the bounded DB
 * flush, so the next increments carry offsets past the local length and the
 * strict-equality append drops them until the turn seals. Only a STREAMING
 * assistant head is eligible; a replay (offset <= local length), a terminal head,
 * a replace-mode delta and a non-creatable seq jump are not.
 */
function isForwardOffsetGap(entry: ConversationStreamEntry, delta: MessageDelta): boolean {
  const state = entry.message.stream_state
  if (state === "complete" || state === "interrupted") return false
  if (entry.message.role !== "assistant") return false
  if (deltaMode(delta) !== "append") return false
  const parts = entry.message.parts
  const partIndex = delta.seq - 1
  if (partIndex < 0) return false
  const part: unknown = parts[partIndex]
  let localLength = 0
  if (part === undefined) {
    // Only the next slot is creatable; a jump past it is not an offset gap.
    if (partIndex !== parts.length) return false
  } else {
    if (!isRecord(part) || part.type !== delta.part_type) return false
    const text = (part as { text?: unknown }).text
    if (text !== undefined && typeof text !== "string") return false
    localLength = typeof text === "string" ? text.length : 0
  }
  return (delta.offset ?? 0) > localLength
}

/* ------------------------------ stream resets ------------------------------ */

type StreamResetListener = (versionId: string, data: unknown) => void

const streamResetListeners = new Set<StreamResetListener>()

/** Notifies snapshot owners that the cursor fell out of the event window. */
export function subscribeStreamReset(listener: StreamResetListener): () => void {
  streamResetListeners.add(listener)
  return () => {
    streamResetListeners.delete(listener)
  }
}

/** Called by the single SSE owner (`useVersionEvents`) on `stream.reset`. */
export function publishStreamReset(versionId: string, data: unknown): void {
  // The cursor fell out of the event window: the accumulated stream state can
  // no longer be trusted to be complete. Drop it; the HTTP snapshot refetch and
  // the next full frames rebuild the thread (gaps heal wholesale, never splice).
  // Buffered pre-snapshot increments survive: the refetch seeds the projection
  // from the stored reply and replays them, so the reply resumes instead of
  // freezing until the turn ends (I-1).
  conversationStreams.delete(versionId)
  for (const listener of streamResetListeners) listener(versionId, data)
}

/* ------------------------------ job progress ------------------------------- */

/** The live slice of a job card that version-channel job events mutate. */
export type JobCardUpdate = {
  status?: JobStatus
  current?: number
  total?: number
}

/** Version-channel event types that mutate the rendered job card. */
export type JobEventType = "job.progress" | "job.finished" | "job.canceled" | "job.requeued"

const JOB_STATUSES: ReadonlySet<string> = new Set<JobStatus>([
  "open",
  "claimed",
  "running",
  "waiting_human",
  "done",
  "failed",
  "cancelled",
])

/** `done/failed/cancelled` are terminal: a card never regresses out of them. */
const TERMINAL_JOB_STATUSES: ReadonlySet<JobStatus> = new Set<JobStatus>([
  "done",
  "failed",
  "cancelled",
])

function isJobStatus(value: unknown): value is JobStatus {
  return typeof value === "string" && JOB_STATUSES.has(value)
}

function jobKey(versionId: string, jobId: string): string {
  return `${versionId}:${jobId}`
}

// Job events arrive on the version stream while the card lives inside a
// conversation message. This projection lets the stream owner publish without
// threading a prop through every workspace layer.
const jobUpdates = new Map<string, JobCardUpdate>()
const jobUpdateListeners = new Set<() => void>()

/**
 * Sticky-terminal merge for one job event.
 *
 * A terminal card (`done/failed/cancelled`) accepts only an idempotent terminal
 * repeat or an explicit revival: `job.requeued`, or an `open` status (which the
 * worker also emits after an automatic retry). Everything else is a stale
 * in-flight update from the fenced old owner — a coalesced `running` snapshot,
 * or a `failed` progress event that lost the race to a cancel/done — and is
 * dropped with its counters so the card cannot roll back. After a revival the
 * next owner's `running`/`finished` events apply normally.
 */
function mergeJobUpdate(
  current: JobCardUpdate | undefined,
  update: JobCardUpdate,
  eventType: JobEventType,
): JobCardUpdate | null {
  const currentStatus = current?.status
  if (currentStatus !== undefined && TERMINAL_JOB_STATUSES.has(currentStatus)) {
    const revivedByControlEvent =
      eventType === "job.requeued" &&
      update.status !== undefined &&
      !TERMINAL_JOB_STATUSES.has(update.status)
    const revivedByOpenStatus = update.status === "open"
    const idempotentTerminal = update.status !== undefined && update.status === currentStatus
    if (!revivedByControlEvent && !revivedByOpenStatus && !idempotentTerminal) return null
  }
  return { ...(current ?? {}), ...update }
}

/**
 * Parses one validated job event payload and merges it into the live job card
 * projection. `job.canceled`/`job.requeued` share the same merge path as
 * `job.progress`/`job.finished`; malformed payloads are ignored.
 */
export function publishJobEvent(
  versionId: string,
  payload: Record<string, unknown>,
  eventType: JobEventType,
): void {
  const rawId = payload.job_id
  const jobId =
    typeof rawId === "string"
      ? rawId
      : typeof rawId === "number" && Number.isInteger(rawId) && rawId > 0
        ? String(rawId)
        : null
  if (jobId === null || !/^[0-9]+$/.test(jobId)) return

  const update: JobCardUpdate = {}
  if (isJobStatus(payload.status)) update.status = payload.status
  if (
    typeof payload.current === "number" &&
    Number.isInteger(payload.current) &&
    payload.current >= 0
  ) {
    update.current = payload.current
  }
  if (typeof payload.total === "number" && Number.isInteger(payload.total) && payload.total >= 0) {
    update.total = payload.total
  }
  if (Object.keys(update).length === 0) return

  const key = jobKey(versionId, jobId)
  const next = mergeJobUpdate(jobUpdates.get(key), update, eventType)
  if (next === null) return
  jobUpdates.set(key, next)
  for (const listener of jobUpdateListeners) listener()
}

/**
 * Subscribes one rendered job card to the streamed job updates of its version.
 * Returns `null` until a job event (progress/finished/canceled/requeued) for
 * that job lands.
 */
export function useJobCardUpdate(versionId: string, jobId: string): JobCardUpdate | null {
  const subscribe = useCallback((listener: () => void): (() => void) => {
    jobUpdateListeners.add(listener)
    return () => {
      jobUpdateListeners.delete(listener)
    }
  }, [])

  return useSyncExternalStore(
    subscribe,
    () => jobUpdates.get(jobKey(versionId, jobId)) ?? null,
    () => null,
  )
}

function compareMessageIds(left: ConversationMessage, right: ConversationMessage): number {
  return compareIds(left.id, right.id)
}

function compareIds(left: string, right: string): number {
  if (left === right) return 0
  try {
    const leftId = BigInt(left)
    const rightId = BigInt(right)
    return leftId < rightId ? -1 : 1
  } catch {
    return left.localeCompare(right)
  }
}

function oldestMessageId(messages: readonly ConversationMessage[]): string {
  let oldest = ""
  for (const message of messages) {
    if (oldest === "" || compareIds(message.id, oldest) < 0) oldest = message.id
  }
  return oldest
}

function sortMessages(messages: ConversationMessage[]): ConversationMessage[] {
  return [...messages].sort(compareMessageIds)
}

/**
 * Which way the thread's history pagination runs.
 *
 * API-8 returns the newest page by default and older pages through `before`.
 * The P1 endpoint returned the oldest page first and newer pages through
 * `after`; the envelope of that legacy first page points its cursor at the
 * newest loaded id. Detecting that shape keeps the older P1 callers (and their
 * tests) working without a second endpoint, while the default remains the
 * spec's older-history direction.
 */
type HistoryDirection = "older" | "newer"

function resolveHistoryDirection(page: {
  items: readonly ConversationMessage[]
  nextCursor: string
  hasMore: boolean
}): HistoryDirection {
  const newest = newestMessageId(page.items)
  return page.nextCursor !== "" && page.nextCursor === newest ? "newer" : "older"
}

/**
 * Merges one SSE batch into the thread. A message that is already loaded keeps
 * its identity but receives the newest parts, merged by `seq` (a full snapshot
 * without seq replaces the older one). This makes duplicate or incremental
 * `conversation.message` frames idempotent instead of rendering twice.
 */
function mergeConversationMessages(
  current: ConversationMessage[],
  incoming: ConversationMessage[],
): ConversationMessage[] {
  if (incoming.length === 0) return current
  const byId = new Map<string, ConversationMessage>()
  for (const message of current) byId.set(message.id, message)
  for (const message of incoming) {
    const existing = byId.get(message.id)
    if (existing === undefined) {
      byId.set(message.id, message)
      continue
    }
    byId.set(message.id, {
      ...existing,
      ...message,
      parts: mergePartsBySeq(existing.parts, message.parts),
    })
  }
  return sortMessages([...byId.values()])
}

const QUESTION_STATUSES: ReadonlySet<string> = new Set<QuestionStatus>([
  "pending",
  "answered",
  "expired",
  "cancelled",
])

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function asString(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined
}

function normalizeQuestionBatch(value: unknown): QuestionBatch | null {
  if (!isRecord(value)) return null
  const id = asString(value.id)
  if (id === undefined || !Array.isArray(value.questions)) return null
  const status =
    typeof value.status === "string" && QUESTION_STATUSES.has(value.status)
      ? (value.status as QuestionStatus)
      : "pending"
  return {
    id,
    version_id: asString(value.version_id),
    conversation_id: typeof value.conversation_id === "string" ? value.conversation_id : null,
    message_id: typeof value.message_id === "string" ? value.message_id : null,
    kind: asString(value.kind),
    status,
    batch_id: asString(value.batch_id) ?? "",
    questions: value.questions as QuestionItem[],
    expires_at: asString(value.expires_at) ?? "",
    answers: Array.isArray(value.answers) ? (value.answers as QuestionAnswer[]) : [],
    answered_by: typeof value.answered_by === "string" ? value.answered_by : null,
    answered_at: typeof value.answered_at === "string" ? value.answered_at : null,
    skipped: value.skipped === true,
    created_at: asString(value.created_at),
  }
}

/**
 * Extracts the question batch of a conversation part. Accepts the Task 23
 * message payload shape (flat `question_id`/`questions`) and the normalized
 * `{type: "question", question}` shape used by the thread overlay, so the UI
 * does not depend on Task 36/37 finishing the message DTO `kind` field first.
 *
 * These pure parsing helpers live here (not in `api.ts`) so the existing thread
 * tests can keep fully mocking the API transport module.
 */
export function questionBatchFromPart(value: unknown): QuestionBatch | null {
  if (!isRecord(value) || value.type !== "question") return null
  let batch: QuestionBatch | null = null
  if (isRecord(value.question)) {
    batch = normalizeQuestionBatch(value.question)
  } else {
    // The DTO may expose the stored part payload either flat or nested; accept
    // both so the card does not depend on Task 36/37's exact part serialization.
    const source = isRecord(value.payload) ? value.payload : value
    if (isRecord(source.question)) {
      batch = normalizeQuestionBatch(source.question)
    } else if (asString(source.question_id) !== undefined && Array.isArray(source.questions)) {
      batch = normalizeQuestionBatch({
        id: source.question_id,
        batch_id: source.batch_id,
        questions: source.questions,
        expires_at: source.expires_at,
        status: source.status,
        answers: source.answers,
        conversation_id: source.conversation_id,
        message_id: source.message_id,
        kind: source.kind,
      })
    }
  }
  // W2-2 decision rows share the human_request/question DTO. They are never
  // clarification question parts; dropping them here keeps every renderer
  // (thread overlay and message snapshot) from creating an "共 0 题" card.
  return batch !== null && isClarificationQuestionBatch(batch) ? batch : null
}

/**
 * Finds the question batch carried by a message: `kind=question` with a
 * top-level payload, or a `question` part. `message_id`/`conversation_id` are
 * copied from the message when the part payload omits them.
 */
export function questionBatchFromMessage(value: unknown): QuestionBatch | null {
  if (!isRecord(value)) return null
  let batch: QuestionBatch | null = null
  if (value.kind === "question" && isRecord(value.question)) {
    const normalized = normalizeQuestionBatch(value.question)
    batch = normalized !== null && isClarificationQuestionBatch(normalized) ? normalized : null
  }
  if (batch === null && Array.isArray(value.parts)) {
    for (const part of value.parts) {
      batch = questionBatchFromPart(part)
      if (batch !== null) break
    }
  }
  if (batch === null) return null
  const messageID = asString(value.id)
  const conversationID = asString(value.conversation_id)
  return {
    ...batch,
    conversation_id: batch.conversation_id ?? conversationID ?? null,
    message_id: batch.message_id ?? messageID ?? null,
    kind: batch.kind ?? "clarification_question",
  }
}

/**
 * The effective state of a batch: a pending batch past its deadline is expired
 * (the server also lazily expires it on the next read). Expired questions never
 * block the thread (R-B3-4).
 */
export function questionBatchStatus(
  question: QuestionBatch,
  now: number = Date.now(),
): QuestionStatus {
  if (question.status !== "pending") return question.status
  const expiresAt = Date.parse(question.expires_at)
  if (Number.isFinite(expiresAt) && expiresAt <= now) return "expired"
  return "pending"
}

/**
 * Stable signature of the server-visible batch state (status, deadline, answer
 * echo). Local echoes and thread overrides are keyed by it: an unchanged parent
 * snapshot keeps the local answer, while a newer authoritative batch (a Host
 * reopen gets a new `expires_at` and cleared answers) supersedes it.
 */
export function questionBatchSignature(question: QuestionBatch): string {
  return JSON.stringify([
    question.id,
    question.status,
    question.expires_at,
    question.answers,
    question.answered_at,
    question.skipped,
  ])
}

function isQuestionPart(part: unknown): boolean {
  return (
    typeof part === "object" && part !== null && (part as { type?: unknown }).type === "question"
  )
}

/**
 * Overlays the persisted structured-question state onto the loaded thread.
 *
 * A question card is part of the conversation stream, but the conversation
 * message snapshot may not carry the refreshed `human_request` state (answered /
 * skipped / expired). The question API is the authoritative state: this merges
 * one `question` part into the message bound by `message_id` (or synthesizes the
 * card message when the thread page does not include it yet), which is what
 * makes a pending question survive a reload without local-only state.
 *
 * Only a *pending* batch is synthesized when its bound message is not in the
 * loaded page. A terminal batch (answered/expired/cancelled) belongs to old
 * history: synthesizing it would float an answered card above the loaded page
 * (and of its not-yet-loaded history), so it is dropped until that message page
 * is loaded and the card can be placed at its real position.
 */
export function mergeQuestionCards(
  messages: readonly ConversationMessage[],
  questions: readonly QuestionBatch[],
): ConversationMessage[] {
  if (questions.length === 0) return messages as ConversationMessage[]
  const byId = new Map<string, ConversationMessage>()
  for (const message of messages) byId.set(message.id, message)
  let changed = false
  for (const question of questions) {
    // W2-2 decision rows (approval/blocked_recovery/conflict) are human_request
    // rows with an empty questions array. They are decision cards, never
    // clarification cards, and must not reach `/questions/{id}/answer`.
    if (!isClarificationQuestionBatch(question)) continue
    const messageID = question.message_id ?? ""
    const existing = messageID !== "" ? byId.get(messageID) : undefined
    const part: QuestionPart = { type: "question", question }
    if (existing !== undefined) {
      const parts = existing.parts.filter(
        (candidate) =>
          !isQuestionPart(candidate) || questionBatchFromPart(candidate)?.id !== question.id,
      )
      byId.set(messageID, {
        ...existing,
        parts: [...parts, part],
      } as unknown as ConversationMessage)
      changed = true
      continue
    }
    if (questionBatchStatus(question) !== "pending") continue
    const id = messageID !== "" ? messageID : `question-${question.id}`
    const synthesized = {
      id,
      conversation_id: question.conversation_id ?? "",
      role: "assistant",
      parts: [part],
      refs: [],
      created_at: question.created_at ?? new Date().toISOString(),
    } as unknown as ConversationMessage
    byId.set(id, synthesized)
    changed = true
  }
  return changed ? sortMessages([...byId.values()]) : (messages as ConversationMessage[])
}

function createClientMessageId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID()
  }
  return `client_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`
}

export type UseConversationOptions = {
  versionId: string
  conversationId: string | null
  /** Messages pushed by the version SSE channel; merged by id. */
  liveMessages?: ConversationMessage[]
}

export type UseConversationResult = {
  messages: ConversationMessage[]
  /** True while the first snapshot of the current conversation is loading. */
  loading: boolean
  /** True while an already loaded snapshot is being refetched (thread stays). */
  refreshing: boolean
  error: unknown
  sending: boolean
  sendError: unknown
  /** True when older history can still be loaded through the `before` cursor. */
  hasMore: boolean
  loadingMore: boolean
  /** Error from the last `loadMore`; inline only, the thread stays mounted. */
  loadMoreError: unknown
  reload: () => void
  /** Loads the next older history page from the stored server cursor. */
  loadMore: () => Promise<void>
  /** Returns true when the API accepted the message (draft can be cleared). */
  send: (parts: NewMessagePart[], options?: SendOptions) => Promise<boolean>
}

/**
 * Owns one conversation thread: initial fetch, live SSE merge, send with a
 * client idempotency key, and reload for retries. The SSE connection itself is
 * owned by the workspace (`useVersionEvents`), so this hook never reconnects.
 */
export function useConversation({
  versionId,
  conversationId,
  liveMessages = [],
}: UseConversationOptions): UseConversationResult {
  const [messages, setMessages] = useState<ConversationMessage[]>([])
  const [loading, setLoading] = useState(conversationId !== null)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [loadMoreError, setLoadMoreError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)
  // Bounded forward-offset-gap re-read budget. It mirrors the drawer handshake:
  // an internal re-read keeps its count across the reload via
  // gapRereadInFlightRef; any external (re)load starts a fresh budget.
  const gapRereadCountRef = useRef(0)
  const gapRereadInFlightRef = useRef(false)
  const gapRereadTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<unknown>(null)
  // Persisted structured-question state of this conversation. The thread
  // overlays these cards, so answering/skipping survives a reload even before
  // Task 36/37 wires the question SSE events. The snapshot is keyed by
  // conversation identity so a switch can never overlay the previous thread's
  // questions while the new question fetch is still in flight.
  const [questionSnapshot, setQuestionSnapshot] = useState<{
    identity: string
    items: QuestionBatch[]
  }>({ identity: "", items: [] })

  // The history cursor belongs to the loaded snapshot only: live SSE messages
  // and optimistic sends never move it, so a higher live id cannot skip
  // unloaded history.
  const historyCursorRef = useRef("")
  // Direction of the loaded snapshot's pagination (`before` for API-8's latest
  // page, `after` only for the legacy P1 oldest-first envelope).
  const historyDirectionRef = useRef<HistoryDirection>("older")
  // Oldest rendered message id: guards the cursor against a backend/response
  // that returns the same page again, which would otherwise loop forever.
  const oldestLoadedIdRef = useRef("")
  // Bumped by every snapshot fetch and by unmount/switch, so a late `loadMore`
  // response can be recognized as stale and ignored.
  const generationRef = useRef(0)
  // Identity (`versionId:conversationId`) whose snapshot is currently rendered.
  // A fetch for this identity is a refresh of a loaded thread, not a new load.
  const loadedIdentityRef = useRef("")
  const identity = conversationId === null ? "" : `${versionId}:${conversationId}`
  const identityRef = useRef(identity)
  identityRef.current = identity
  const questions = questionSnapshot.identity === identity ? questionSnapshot.items : NO_QUESTIONS

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken is a manual refresh trigger
  useEffect(() => {
    generationRef.current += 1
    const generation = generationRef.current
    let cancelled = false
    const isCurrent = () => !cancelled && generationRef.current === generation
    const invalidate = () => {
      generationRef.current += 1
    }
    historyCursorRef.current = ""

    // An internal forward-gap re-read keeps its count across the reload; any
    // other (re)load — initial mount, conversation switch, stream.reset, retry —
    // resets the budget. Any queued gap re-read is cancelled here.
    if (gapRereadInFlightRef.current) {
      gapRereadInFlightRef.current = false
    } else {
      gapRereadCountRef.current = 0
    }
    if (gapRereadTimerRef.current !== null) {
      clearTimeout(gapRereadTimerRef.current)
      gapRereadTimerRef.current = null
    }

    if (conversationId === null) {
      loadedIdentityRef.current = ""
      historyDirectionRef.current = "older"
      oldestLoadedIdRef.current = ""
      setMessages([])
      setLoading(false)
      setRefreshing(false)
      setError(null)
      setHasMore(false)
      setLoadingMore(false)
      setLoadMoreError(null)
      return () => {
        cancelled = true
        invalidate()
      }
    }

    const fetchIdentity = `${versionId}:${conversationId}`
    // Refetching the already rendered conversation keeps the thread and the
    // Composer (with its unsent draft) mounted: only the initial load of a
    // conversation replaces the panel with the loading card.
    const isRefresh = loadedIdentityRef.current === fetchIdentity
    if (isRefresh) {
      setRefreshing(true)
    } else {
      setLoading(true)
      setRefreshing(false)
    }
    setError(null)
    setHasMore(false)
    setLoadingMore(false)
    setLoadMoreError(null)
    void listMessages(versionId, conversationId).then(
      (next) => {
        if (!isCurrent()) return
        const page = toMessagePage(next)
        const sorted = sortMessages(page.items)
        const direction = resolveHistoryDirection(page)
        const newest = newestMessageId(sorted)
        const oldest = oldestMessageId(sorted)
        historyDirectionRef.current = direction
        oldestLoadedIdRef.current = oldest
        setMessages(sorted)
        // Resume incremental rendering from the stored reply (I-1): a head the
        // server still reports as streaming seeds the live projection with its
        // snapshot parts (and their server seq), so the next delta continues the
        // text instead of gapping against an empty accumulator.
        for (const message of sorted) {
          if (message.role === "assistant" && message.stream_state === "streaming") {
            seedConversationSnapshot(versionId, message)
          }
        }
        if (direction === "older") {
          // API-8 default page: the cursor (when the server sends one) points
          // before the oldest loaded message. A full page also implies more
          // history even when the server omits the envelope.
          historyCursorRef.current = page.nextCursor || oldest
          setHasMore(page.hasMore || page.items.length >= MESSAGES_PAGE_SIZE)
        } else {
          historyCursorRef.current = page.nextCursor || newest
          setHasMore(page.hasMore)
        }
        loadedIdentityRef.current = fetchIdentity
        setRefreshing(false)
        setLoading(false)
      },
      (cause: unknown) => {
        if (!isCurrent()) return
        setError(cause)
        setRefreshing(false)
        setLoading(false)
      },
    )

    return () => {
      cancelled = true
      invalidate()
    }
  }, [versionId, conversationId, reloadToken])

  useEffect(() => {
    if (conversationId === null || liveMessages.length === 0) return
    const relevant = liveMessages.filter((message) => message.conversation_id === conversationId)
    if (relevant.length === 0) return
    setMessages((current) => mergeConversationMessages(current, relevant))
  }, [liveMessages, conversationId])

  // Incremental Host replies (v0.2.4-D, SC-D2): the workspace SSE owner
  // accumulates `conversation.message.delta` into the stream projection and
  // publishes full frames there too (the workspace liveMessages prop dedupes by
  // id, so the terminal frame would otherwise never reach the thread). A full
  // frame replaces the parts wholesale (server truth, gap heal); a delta entry
  // merges by seq so an unrelated part's update never regresses a newer HTTP
  // snapshot of another part.
  useEffect(
    () =>
      subscribeConversationStream((streamVersionId, entry) => {
        if (streamVersionId !== versionId) return
        if (conversationId === null || entry.message.conversation_id !== conversationId) return
        setMessages((current) => {
          const existing = current.find((message) => message.id === entry.message.id)
          if (existing === undefined) return sortMessages([...current, entry.message])
          if (entry.fullFrame) {
            return current.map((message) =>
              message.id === entry.message.id ? { ...message, ...entry.message } : message,
            )
          }
          return current.map((message) =>
            message.id === entry.message.id
              ? { ...message, parts: mergePartsBySeq(message.parts, entry.message.parts) }
              : message,
          )
        })
      }),
    [versionId, conversationId],
  )

  // Question state comes from the questions API (Task 23), not from local
  // component state: a reload/refresh must restore pending batches and the
  // answered/skipped/expired state. Failures are intentionally swallowed — the
  // questions route is mounted by Task 37, and questions never block the thread
  // (R-B3-4).
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken refreshes the question state together with the thread snapshot
  useEffect(() => {
    const fetchIdentity = conversationId === null ? "" : `${versionId}:${conversationId}`
    if (conversationId === null) {
      setQuestionSnapshot((current) =>
        current.identity === "" ? current : { identity: "", items: [] },
      )
      return
    }
    let cancelled = false
    void (async () => {
      try {
        const items = await listQuestions(versionId, { limit: 100 })
        if (cancelled) return
        setQuestionSnapshot({
          identity: fetchIdentity,
          // Only clarification questions belong to a conversation thread; the
          // decision kinds share the endpoint but never render as question cards.
          items: items.filter(
            (question) =>
              isClarificationQuestionBatch(question) && question.conversation_id === conversationId,
          ),
        })
      } catch {
        // Additive only: an unavailable question endpoint keeps the messages.
      }
    })()
    return () => {
      cancelled = true
    }
  }, [versionId, conversationId, reloadToken])

  const renderedMessages = useMemo(
    () => mergeQuestionCards(messages, questions),
    [messages, questions],
  )

  // `stream.reset` means the cursor fell out of the event window: refetch the
  // conversation snapshot through the existing HTTP path (no second SSE).
  useEffect(
    () =>
      subscribeStreamReset((resetVersionId) => {
        if (resetVersionId === versionId) setReloadToken((token) => token + 1)
      }),
    [versionId],
  )

  /**
   * Schedules ONE debounced authoritative snapshot refetch for a forward offset
   * gap. A burst of gap frames collapses to a single fetch (timer guard) and the
   * per-generation budget can never be exceeded (no refetch storm). The delay is
   * at least one bounded-flush period, so the DB has caught up.
   */
  const scheduleForwardGapReread = useCallback(() => {
    if (gapRereadTimerRef.current !== null) return
    if (gapRereadInFlightRef.current) return
    if (gapRereadCountRef.current >= MAX_FORWARD_GAP_REREADS) return
    gapRereadTimerRef.current = setTimeout(() => {
      gapRereadTimerRef.current = null
      if (gapRereadCountRef.current >= MAX_FORWARD_GAP_REREADS) return
      gapRereadCountRef.current += 1
      gapRereadInFlightRef.current = true
      setReloadToken((token) => token + 1)
    }, FORWARD_GAP_REREAD_DELAY_MS)
  }, [])

  // A streaming assistant head whose local text lags the server's published
  // offset (bounded DB flush) triggers the debounced re-read above; the terminal
  // full frame heals any remaining gap once the budget is spent.
  useEffect(
    () =>
      subscribeForwardGap((gapVersionId, gapConversationId) => {
        if (gapVersionId !== versionId) return
        if (conversationId === null || gapConversationId !== conversationId) return
        scheduleForwardGapReread()
      }),
    [versionId, conversationId, scheduleForwardGapReread],
  )

  // Cancel a queued forward-gap re-read on unmount.
  useEffect(
    () => () => {
      if (gapRereadTimerRef.current !== null) {
        clearTimeout(gapRereadTimerRef.current)
        gapRereadTimerRef.current = null
      }
    },
    [],
  )

  const loadMore = useCallback(async (): Promise<void> => {
    if (conversationId === null || loadingMore || !hasMore) return
    const cursor = historyCursorRef.current
    if (cursor === "") return
    const identityAtStart = `${versionId}:${conversationId}`
    const generationAtStart = generationRef.current
    const isCurrent = () =>
      identityRef.current === identityAtStart && generationRef.current === generationAtStart

    setLoadingMore(true)
    setLoadMoreError(null)
    try {
      const direction = historyDirectionRef.current
      const page = toMessagePage(
        await listMessages(
          versionId,
          conversationId,
          direction === "older"
            ? { before: cursor, limit: MESSAGES_PAGE_SIZE }
            : { after: cursor, limit: MESSAGES_PAGE_SIZE },
        ),
      )
      if (!isCurrent()) return

      if (direction === "older") {
        // Only truly older rows may be prepended; a backend that ignores the
        // cursor would otherwise re-merge the same page and keep `hasMore` on.
        const olderItems = sortMessages(page.items).filter(
          (message) => compareIds(message.id, cursor) < 0,
        )
        if (olderItems.length === 0) {
          setHasMore(false)
          return
        }
        setMessages((current) => mergeConversationMessages(current, olderItems))
        const oldest = oldestMessageId(olderItems)
        if (compareIds(oldest, oldestLoadedIdRef.current) < 0) {
          oldestLoadedIdRef.current = oldest
        }
        historyCursorRef.current = page.nextCursor || oldest
        setHasMore(page.hasMore || olderItems.length >= MESSAGES_PAGE_SIZE)
        return
      }

      setMessages((current) => mergeConversationMessages(current, page.items))
      const pageCursor = page.nextCursor || newestMessageId(sortMessages(page.items))
      historyCursorRef.current = pageCursor || cursor
      setHasMore(page.hasMore && pageCursor !== "")
    } catch (cause) {
      if (!isCurrent()) return
      setLoadMoreError(cause)
    } finally {
      if (isCurrent()) setLoadingMore(false)
    }
  }, [versionId, conversationId, loadingMore, hasMore])

  const reload = useCallback(() => {
    setReloadToken((token) => token + 1)
  }, [])

  const send = useCallback(
    async (parts: NewMessagePart[], options?: SendOptions): Promise<boolean> => {
      if (conversationId === null) return false
      setSending(true)
      setSendError(null)
      try {
        // C-04: the composer's retry chain supplies one stable id for every
        // attempt, so a retried POST can never insert a second message. A
        // first/manual send without an id still mints one per call.
        const message = await postMessage(versionId, conversationId, {
          client_message_id: options?.clientMessageId ?? createClientMessageId(),
          parts,
        })
        setMessages((current) =>
          current.some((existing) => existing.id === message.id)
            ? current
            : sortMessages([...current, message]),
        )
        return true
      } catch (cause) {
        setSendError(cause)
        return false
      } finally {
        setSending(false)
      }
    },
    [versionId, conversationId],
  )

  return {
    messages: renderedMessages,
    loading,
    refreshing,
    error,
    sending,
    sendError,
    hasMore,
    loadingMore,
    loadMoreError,
    reload,
    loadMore,
    send,
  }
}

"use client"

import {
  type ClipboardEvent,
  type DragEvent,
  type KeyboardEvent,
  useEffect,
  useRef,
  useState,
} from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { InlineError } from "@/components/ui/ErrorState"
import type { CaseCardPart } from "@/features/conversation/types"
import { ApiError } from "@/lib/api-client"

import { AttachmentUploader, type AttachmentUploaderHandle } from "./AttachmentUploader"
import { type AttachmentPendingProgress, getAttachmentPendingDetails } from "./api"
import type { NewMessagePart, SendOptions, UploadedAttachment } from "./types"

/** Hard cap of one automatic retry chain (~5 minutes, C-04). */
export const AUTO_RETRY_MAX_MS = 5 * 60 * 1000

/** True once the automatic retry window is exhausted; exported for its test. */
export function autoRetryExpired(startedAt: number, now: number = Date.now()): boolean {
  return now - startedAt >= AUTO_RETRY_MAX_MS
}

export type ComposerProps = {
  onSend: (parts: NewMessagePart[], options?: SendOptions) => Promise<boolean> | boolean
  /** Last send failure; shown inline without dropping the draft. */
  error?: unknown
  disabled?: boolean
  placeholder?: string
  /** External draft (e.g. the P2 "请求修改" action); controlled when defined. */
  text?: string
  onTextChange?: (value: string) => void
  /** Case references ("交给 AI 修改") carried by the next user message. */
  caseReferences?: CaseCardPart[]
  onCaseReferencesChange?: (references: CaseCardPart[]) => void
  /**
   * True while the draft holds an image attachment (0.2.1 vision gating). The
   * page passes it to `ModelSelector` so non-vision models are disabled.
   */
  onDraftHasImagesChange?: (hasImages: boolean) => void
  /**
   * Why the current draft cannot be sent (an image on a non-vision model,
   * Task 75). When set, Send is disabled and the reason is shown; the draft is
   * kept so the user can switch model or remove the image and retry.
   */
  sendBlockedReason?: string
}

const STAGE_LABEL: Record<string, string> = {
  queued: "附件排队解析中",
  parsing: "附件解析中",
  analyzing_images: "正在分析图片",
  done: "附件解析完成",
  failed: "解析失败",
}

/** One in-flight automatic retry chain: the message snapshot, its stable id and its age. */
type RetryChain = {
  clientMessageId: string
  parts: NewMessagePart[]
  startedAt: number
  cancelled: boolean
  doneSent: boolean
  inFlight: boolean
}

/** Structural equality of two outgoing messages (used to detect a new draft). */
function sameParts(a: readonly NewMessagePart[], b: readonly NewMessagePart[]): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}

/** Image detection for the draft; the mime comes from `POST /api/content`. */
function hasImageAttachment(items: readonly UploadedAttachment[]): boolean {
  return items.some((item) => item.mime.trim().toLowerCase().startsWith("image/"))
}

function createClientMessageId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID()
  }
  return `client_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`
}

/**
 * Chat composer: Enter sends, Shift+Enter inserts a newline, the send button is
 * disabled while a message/attachment is in flight, and the draft survives a
 * failed send. Files can be selected, pasted or dropped; all three paths use the
 * same upload pipeline (spec §8.4). `text`/`onTextChange` make the draft
 * controllable so other UI (design case detail) can prefill it without sending.
 */
export function Composer({
  onSend,
  error,
  disabled = false,
  placeholder = "描述需求，或粘贴 / 拖拽上传 doc / pdf / md / ppt…",
  text,
  onTextChange,
  caseReferences = [],
  onCaseReferencesChange,
  onDraftHasImagesChange,
  sendBlockedReason,
}: ComposerProps) {
  const [internalValue, setInternalValue] = useState("")
  const [attachments, setAttachments] = useState<UploadedAttachment[]>([])
  const [uploading, setUploading] = useState(false)
  const [sending, setSending] = useState(false)
  const [progress, setProgress] = useState<AttachmentPendingProgress | null>(null)
  const [countdown, setCountdown] = useState<number | null>(null)
  const [retrying, setRetrying] = useState(false)
  const [autoActive, setAutoActive] = useState(false)
  const [retryNote, setRetryNote] = useState<string | null>(null)
  // A `done` auto-send whose snapshot no longer matches the draft keeps the new
  // draft and reports here; the note deliberately survives the parent clearing
  // `error` after the successful POST.
  const [sentNote, setSentNote] = useState<string | null>(null)
  const uploaderRef = useRef<AttachmentUploaderHandle>(null)
  // C-04: one automatic retry chain per failed message. The stable client id
  // makes the eventual POST idempotent; the timers must be cleared on
  // unmount/cancel so a stale retry can never create a second message.
  const chainRef = useRef<RetryChain | null>(null)
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const countdownTimerRef = useRef<ReturnType<typeof setInterval> | null>(null)
  // The latest rendered draft, kept in a ref because a retry timer scheduled by
  // an earlier render captures `finishChain`/`buildParts` from that render. The
  // `error`-only effect never reschedules on a draft edit, so a delayed success
  // must compare the chain snapshot against this live value — otherwise a stale
  // closure sees equality and `clearDraft` wipes text typed during the wait.
  const livePartsRef = useRef<NewMessagePart[]>([])

  const value = text ?? internalValue
  const hasContent = value.trim().length > 0 || attachments.length > 0 || caseReferences.length > 0
  // A pending attachment upload/parse must finish before the message can be
  // submitted (spec §8.5); the server repeats this with a typed 409
  // attachment_pending when a referenced attachment is still parsing.
  const blocked = disabled || sending || uploading || retrying
  // The page-level vision gate (Task 75): an image draft on a non-vision model
  // never reaches POST 201. The draft and attachments stay editable/removable.
  const sendBlocked = sendBlockedReason !== undefined
  // The server rejects a send whose attachment is still parsing with a typed
  // 409. Task 10 adds the progress details used by the automatic retry chain;
  // an old server without `attachment_id` keeps the manual retry fallback.
  const attachmentPending = error instanceof ApiError && error.code === "attachment_pending"
  const draftHasImages = hasImageAttachment(attachments)

  // Publish the image-draft signal that gates non-vision models (0.2.1). The
  // attachment list is owned here (Task 25 FloatingPanel / Task 28 chips stay
  // untouched), so the page only needs the boolean.
  useEffect(() => {
    onDraftHasImagesChange?.(draftHasImages)
  }, [draftHasImages, onDraftHasImagesChange])
  // The next composer (conversation switch, collapse/expand, send that clears
  // the draft) starts empty; never leak a stale `true` into it.
  useEffect(() => {
    if (onDraftHasImagesChange === undefined) return
    return () => onDraftHasImagesChange(false)
  }, [onDraftHasImagesChange])

  function updateValue(next: string): void {
    if (text === undefined) setInternalValue(next)
    onTextChange?.(next)
  }

  function buildParts(): NewMessagePart[] {
    const parts: NewMessagePart[] = []
    const draft = value.trim()
    if (draft.length > 0) parts.push({ type: "text", text: draft })
    for (const attachment of attachments) {
      parts.push({
        type: "attachment",
        sha256: attachment.sha256,
        name: attachment.name,
        mime: attachment.mime,
        size: attachment.size,
      })
    }
    for (const reference of caseReferences) {
      parts.push({
        type: "case_card",
        case_id: reference.case_id,
        case_version_id: reference.case_version_id ?? null,
        title: reference.title,
        status: reference.status,
      })
    }
    return parts
  }

  // Refresh on every render so an async completion reads the draft as it is now.
  livePartsRef.current = buildParts()

  function clearDraft(): void {
    updateValue("")
    setAttachments([])
    onCaseReferencesChange?.([])
  }

  function clearRetryTimers(): void {
    if (retryTimerRef.current !== null) {
      clearTimeout(retryTimerRef.current)
      retryTimerRef.current = null
    }
    if (countdownTimerRef.current !== null) {
      clearInterval(countdownTimerRef.current)
      countdownTimerRef.current = null
    }
  }

  /**
   * A successful send ends the chain. The delivered message is the snapshot
   * taken when the chain started, so the current draft is only cleared while it
   * still matches that snapshot; text/attachments/references typed during the
   * parse wait are kept and a `已发送` note explains why.
   */
  function finishChain(chain: RetryChain): void {
    const draftUnchanged = sameParts(livePartsRef.current, chain.parts)
    chainRef.current = null
    clearRetryTimers()
    setProgress(null)
    setCountdown(null)
    setRetrying(false)
    setAutoActive(false)
    setRetryNote(null)
    if (draftUnchanged) {
      clearDraft()
      return
    }
    setSentNote("已发送；等待期间输入的新草稿已保留")
  }

  async function runSend(chain: RetryChain): Promise<boolean> {
    if (chain.inFlight) return false
    chain.inFlight = true
    try {
      const sent = await onSend(chain.parts, { clientMessageId: chain.clientMessageId })
      if (sent) finishChain(chain)
      return sent
    } catch {
      return false
    } finally {
      chain.inFlight = false
    }
  }

  /** Schedules the next poll; the timer and the countdown are always paired. */
  function scheduleRetry(seconds: number): void {
    const chain = chainRef.current
    if (chain === null || chain.cancelled) return
    clearRetryTimers()
    const waitSeconds = Math.max(1, Math.min(300, Math.ceil(seconds)))
    const deadline = Date.now() + waitSeconds * 1000
    setCountdown(waitSeconds)
    countdownTimerRef.current = setInterval(() => {
      setCountdown(Math.max(0, Math.ceil((deadline - Date.now()) / 1000)))
    }, 1000)
    retryTimerRef.current = setTimeout(() => {
      clearRetryTimers()
      setCountdown(0)
      void runRetry()
    }, waitSeconds * 1000)
  }

  async function runRetry(): Promise<void> {
    const chain = chainRef.current
    if (chain === null || chain.cancelled) return
    if (autoRetryExpired(chain.startedAt)) {
      stopForCap()
      return
    }
    setRetrying(true)
    const sent = await runSend(chain)
    if (sent) return
    const current = chainRef.current
    if (current === null || current.cancelled) {
      setRetrying(false)
      return
    }
    if (autoRetryExpired(current.startedAt)) {
      stopForCap()
      return
    }
    const latest = getAttachmentPendingDetails(error)
    if (latest === null) {
      // The parent has not published the next typed 409 yet; keep polling with
      // the documented default delay instead of stalling the chain.
      scheduleRetry(3)
      return
    }
    if (latest.stage === "failed") {
      setRetrying(false)
      setAutoActive(false)
      return
    }
    if (latest.stage === "done") {
      setRetrying(false)
      return
    }
    scheduleRetry(latest.retryAfterSeconds)
  }

  /** `done` triggers exactly one automatic send per chain. */
  async function sendDone(): Promise<void> {
    const chain = chainRef.current
    if (chain === null || chain.cancelled || chain.doneSent) return
    chain.doneSent = true
    setRetrying(true)
    const sent = await runSend(chain)
    if (!sent) {
      setRetrying(false)
      setRetryNote("附件解析完成，但自动发送失败，请手动重试")
    }
  }

  function stopForCap(): void {
    clearRetryTimers()
    setCountdown(null)
    setRetrying(false)
    setAutoActive(false)
    setRetryNote("自动重试已超过 5 分钟上限，请手动重试")
  }

  function cancelAutoRetry(): void {
    const chain = chainRef.current
    if (chain !== null) chain.cancelled = true
    clearRetryTimers()
    setCountdown(null)
    setRetrying(false)
    setAutoActive(false)
    setRetryNote("已取消自动重试，可手动重试")
  }

  async function submit(): Promise<void> {
    if (blocked || sendBlocked || !hasContent) return
    const parts = buildParts()
    const existing = chainRef.current
    // A resend of the exact pending message keeps the chain's stable id and its
    // 5-minute window (manual retry, old server without progress details). A
    // changed draft is a new message and gets a fresh id/window.
    const retryOfChain = existing !== null && sameParts(parts, existing.parts)
    const chain: RetryChain = retryOfChain
      ? { ...existing, parts, cancelled: false, doneSent: false }
      : {
          clientMessageId: createClientMessageId(),
          parts,
          startedAt: Date.now(),
          cancelled: false,
          doneSent: false,
          inFlight: false,
        }
    chainRef.current = chain
    clearRetryTimers()
    setCountdown(null)
    setRetryNote(null)
    setSentNote(null)

    setSending(true)
    chain.inFlight = true
    try {
      const sent = await onSend(parts, { clientMessageId: chain.clientMessageId })
      if (sent) finishChain(chain)
    } catch {
      // The parent surfaces send errors through `error`; keep the draft.
    } finally {
      chain.inFlight = false
      setSending(false)
    }
  }

  // The typed 409 is the only progress channel (no extra parse-progress
  // endpoint): every new error updates the stage/percent/ETA and schedules the
  // next poll. `failed` stops, `done` auto-sends once, and the 5-minute cap
  // falls back to the manual retry. The effect only depends on `error` because
  // that is the wire event; refs keep the timers and chain stable.
  //
  // DEFENSIVE ONLY: the backend's `isAttachmentPending` currently reports true
  // only for `pending`/`processing`, so it never emits a `done`/`failed` 409
  // stage (`attachmentPendingDetails` supports them for a future contract). The
  // `done`/`failed` branches below are therefore unreachable in production
  // today; do not remove them without changing the server contract first.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `error` is the only wire trigger; timers/chain live in refs.
  useEffect(() => {
    const details = getAttachmentPendingDetails(error)
    if (details === null) {
      // The parent clears `sendError` while a retry POST is in flight; keep the
      // chain and its progress visible until that attempt settles.
      if (chainRef.current?.inFlight === true) return
      if (!(error instanceof ApiError && error.code === "attachment_pending")) {
        clearRetryTimers()
        setCountdown(null)
        setRetrying(false)
        setAutoActive(false)
        setProgress(null)
        setRetryNote(null)
        if (chainRef.current !== null && !chainRef.current.inFlight) chainRef.current = null
      }
      return
    }
    const chain = chainRef.current
    if (chain === null) return
    setProgress(details)
    if (chain.cancelled) {
      setAutoActive(false)
      return
    }
    if (autoRetryExpired(chain.startedAt)) {
      stopForCap()
      return
    }
    // Defensive (see the effect header): the current backend only emits
    // pending/processing, so `failed`/`done` stages never arrive today.
    if (details.stage === "failed") {
      clearRetryTimers()
      setCountdown(null)
      setRetrying(false)
      setAutoActive(false)
      return
    }
    if (details.stage === "done") {
      clearRetryTimers()
      setCountdown(null)
      setAutoActive(false)
      void sendDone()
      return
    }
    setAutoActive(true)
    scheduleRetry(details.retryAfterSeconds)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [error])

  // Never let a queued retry fire after the composer unmounts.
  // biome-ignore lint/correctness/useExhaustiveDependencies: cleanup must run once on unmount, not on every render.
  useEffect(() => clearRetryTimers, [])

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>): void {
    if (event.key !== "Enter" || event.shiftKey) return
    if (event.nativeEvent.isComposing) return
    event.preventDefault()
    void submit()
  }

  function handlePaste(event: ClipboardEvent<HTMLTextAreaElement>): void {
    const files = event.clipboardData?.files
    if (files === undefined || files.length === 0) return
    event.preventDefault()
    uploaderRef.current?.addFiles(files)
  }

  function handleDragOver(event: DragEvent<HTMLTextAreaElement>): void {
    if (!Array.from(event.dataTransfer?.types ?? []).includes("Files")) return
    event.preventDefault()
  }

  function handleDrop(event: DragEvent<HTMLTextAreaElement>): void {
    const files = event.dataTransfer?.files
    if (files === undefined || files.length === 0) return
    event.preventDefault()
    uploaderRef.current?.addFiles(files)
  }

  return (
    <div className="border-t border-border px-3 py-2.5">
      <div
        className={cn(
          "rounded-xl border border-border bg-card px-3 py-2",
          "focus-within:border-primary/60 focus-within:ring-1 focus-within:ring-ring",
        )}
      >
        {caseReferences.length === 0 ? null : (
          <div data-testid="composer-case-references" className="mb-1.5 flex flex-wrap gap-1">
            {caseReferences.map((reference) => (
              <span
                key={reference.case_id}
                data-testid={`composer-case-ref-${reference.case_id}`}
                className="inline-flex items-center gap-1 rounded-full border border-primary/30 bg-primary/5 px-2 py-0.5 text-[10px] text-foreground"
              >
                用例 {reference.title}
                {onCaseReferencesChange === undefined ? null : (
                  <button
                    type="button"
                    data-testid={`composer-case-ref-remove-${reference.case_id}`}
                    aria-label={`移除用例引用 ${reference.title}`}
                    onClick={() =>
                      onCaseReferencesChange(
                        caseReferences.filter((item) => item.case_id !== reference.case_id),
                      )
                    }
                    className="rounded-full px-0.5 text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    ×
                  </button>
                )}
              </span>
            ))}
          </div>
        )}
        <textarea
          data-testid="composer-textarea"
          aria-label="需求澄清消息"
          rows={2}
          value={value}
          disabled={disabled}
          placeholder={placeholder}
          onChange={(event) => updateValue(event.target.value)}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          onDragOver={handleDragOver}
          onDrop={handleDrop}
          className={cn(
            "w-full resize-none bg-transparent text-sm text-foreground outline-none",
            "placeholder:text-muted-foreground disabled:cursor-not-allowed",
          )}
        />
        <div className="mt-1 flex items-center gap-2">
          <AttachmentUploader
            ref={uploaderRef}
            items={attachments}
            onChange={setAttachments}
            onUploadingChange={setUploading}
            disabled={disabled || sending}
          />
          <span className="ml-auto hidden text-[10px] text-muted-foreground sm:inline">
            Enter 发送 · Shift+Enter 换行
          </span>
          <Button
            size="sm"
            onClick={() => {
              void submit()
            }}
            disabled={blocked || sendBlocked || !hasContent}
          >
            {sending ? "发送中…" : uploading ? "解析中…" : "发送"}
          </Button>
        </div>
        {sendBlocked && sendBlockedReason !== undefined ? (
          <p
            data-testid="composer-send-blocked"
            role="status"
            className="mt-1.5 text-[11px] text-warning"
          >
            {sendBlockedReason}
          </p>
        ) : null}
      </div>
      {progress !== null ? (
        <div
          data-testid="composer-retry-progress"
          className="mt-1.5 rounded-lg border border-border bg-muted/40 px-2.5 py-2 text-[11px] text-foreground"
        >
          <div role="status">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="font-medium">
                {STAGE_LABEL[progress.stage ?? ""] ?? "等待附件解析"}
              </span>
              {progress.stage === "analyzing_images" ? (
                <span>
                  图片 {progress.imagesDone}/{progress.imagesTotal}
                </span>
              ) : null}
              {progress.stage === "analyzing_images" && progress.percent !== null ? (
                <span>{progress.percent}%</span>
              ) : null}
              {progress.stage === "analyzing_images" && progress.etaSeconds !== null ? (
                <span>预计剩余 {progress.etaSeconds} 秒</span>
              ) : null}
              {countdown !== null && countdown > 0 ? <span>{countdown} 秒后自动重试</span> : null}
              {progress.stage === "analyzing_images" &&
              (progress.imagesFailed > 0 || progress.imagesSkipped > 0) ? (
                <span className="text-muted-foreground">
                  失败 {progress.imagesFailed} 张 · 跳过 {progress.imagesSkipped} 张
                </span>
              ) : null}
            </div>
            {progress.stage === "failed" && progress.error !== "" ? (
              <p className="mt-1 text-destructive">失败原因：{progress.error}</p>
            ) : null}
            {retryNote !== null ? <p className="mt-1 text-muted-foreground">{retryNote}</p> : null}
          </div>
          {autoActive ? (
            <Button className="mt-1.5" variant="secondary" size="sm" onClick={cancelAutoRetry}>
              取消自动重试
            </Button>
          ) : null}
        </div>
      ) : retryNote !== null ? (
        <p
          data-testid="composer-retry-note"
          role="status"
          className="mt-1.5 text-[11px] text-muted-foreground"
        >
          {retryNote}
        </p>
      ) : null}
      {sentNote !== null ? (
        <p
          data-testid="composer-sent-note"
          role="status"
          className="mt-1.5 text-[11px] text-success"
        >
          {sentNote}
        </p>
      ) : null}
      {error !== undefined ? (
        <div className="flex flex-wrap items-center gap-2 pt-1.5">
          <InlineError error={error} />
          {attachmentPending && !autoActive ? (
            <Button
              variant="secondary"
              size="sm"
              disabled={blocked || sendBlocked || !hasContent}
              onClick={() => {
                void submit()
              }}
            >
              重试发送
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

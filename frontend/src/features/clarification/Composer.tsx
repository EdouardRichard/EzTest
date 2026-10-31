"use client"

import { type KeyboardEvent, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { InlineError } from "@/components/ui/ErrorState"

import { AttachmentUploader } from "./AttachmentUploader"
import type { NewMessagePart, UploadedAttachment } from "./types"

export type ComposerProps = {
  onSend: (parts: NewMessagePart[]) => Promise<boolean> | boolean
  /** Last send failure; shown inline without dropping the draft. */
  error?: unknown
  disabled?: boolean
  placeholder?: string
  /** External draft (e.g. the P2 "请求修改" action); controlled when defined. */
  text?: string
  onTextChange?: (value: string) => void
}

/**
 * Chat composer: Enter sends, Shift+Enter inserts a newline, the send button is
 * disabled while a message/attachment is in flight, and the draft survives a
 * failed send. `text`/`onTextChange` make the draft controllable so other UI
 * (design case detail) can prefill it without sending.
 */
export function Composer({
  onSend,
  error,
  disabled = false,
  placeholder = "描述需求，或上传 doc / pdf / md / ppt…",
  text,
  onTextChange,
}: ComposerProps) {
  const [internalValue, setInternalValue] = useState("")
  const [attachments, setAttachments] = useState<UploadedAttachment[]>([])
  const [uploading, setUploading] = useState(false)
  const [sending, setSending] = useState(false)

  const value = text ?? internalValue
  const hasContent = value.trim().length > 0 || attachments.length > 0
  const blocked = disabled || sending || uploading

  function updateValue(next: string): void {
    if (text === undefined) setInternalValue(next)
    onTextChange?.(next)
  }

  async function submit(): Promise<void> {
    if (blocked || !hasContent) return
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

    setSending(true)
    try {
      const sent = await onSend(parts)
      if (sent) {
        updateValue("")
        setAttachments([])
      }
    } catch {
      // The parent surfaces send errors through `error`; keep the draft.
    } finally {
      setSending(false)
    }
  }

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>): void {
    if (event.key !== "Enter" || event.shiftKey) return
    if (event.nativeEvent.isComposing) return
    event.preventDefault()
    void submit()
  }

  return (
    <div className="border-t border-border px-3 py-2.5">
      <div
        className={cn(
          "rounded-xl border border-border bg-card px-3 py-2",
          "focus-within:border-primary/60 focus-within:ring-1 focus-within:ring-ring",
        )}
      >
        <textarea
          data-testid="composer-textarea"
          aria-label="需求澄清消息"
          rows={2}
          value={value}
          disabled={disabled}
          placeholder={placeholder}
          onChange={(event) => updateValue(event.target.value)}
          onKeyDown={handleKeyDown}
          className={cn(
            "w-full resize-none bg-transparent text-sm text-foreground outline-none",
            "placeholder:text-muted-foreground disabled:cursor-not-allowed",
          )}
        />
        <div className="mt-1 flex items-center gap-2">
          <AttachmentUploader
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
            disabled={blocked || !hasContent}
          >
            {sending ? "发送中…" : "发送"}
          </Button>
        </div>
      </div>
      {error !== undefined ? (
        <div className="pt-1.5">
          <InlineError error={error} />
        </div>
      ) : null}
    </div>
  )
}

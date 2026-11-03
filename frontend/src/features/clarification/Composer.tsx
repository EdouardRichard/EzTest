"use client"

import { type ClipboardEvent, type DragEvent, type KeyboardEvent, useRef, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { InlineError } from "@/components/ui/ErrorState"
import type { CaseCardPart } from "@/features/conversation/types"
import { ApiError } from "@/lib/api-client"

import { AttachmentUploader, type AttachmentUploaderHandle } from "./AttachmentUploader"
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
  /** Case references ("交给 AI 修改") carried by the next user message. */
  caseReferences?: CaseCardPart[]
  onCaseReferencesChange?: (references: CaseCardPart[]) => void
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
}: ComposerProps) {
  const [internalValue, setInternalValue] = useState("")
  const [attachments, setAttachments] = useState<UploadedAttachment[]>([])
  const [uploading, setUploading] = useState(false)
  const [sending, setSending] = useState(false)
  const uploaderRef = useRef<AttachmentUploaderHandle>(null)

  const value = text ?? internalValue
  const hasContent = value.trim().length > 0 || attachments.length > 0 || caseReferences.length > 0
  // A pending attachment upload/parse must finish before the message can be
  // submitted (spec §8.5); the server repeats this with a typed 409
  // attachment_pending when a referenced attachment is still parsing.
  const blocked = disabled || sending || uploading
  // The server rejects a send whose attachment is still parsing with a typed
  // 409. Keep the draft/attachments and offer an explicit retry instead of
  // silently re-submitting or dropping the message.
  const attachmentPending = error instanceof ApiError && error.code === "attachment_pending"

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
    for (const reference of caseReferences) {
      parts.push({
        type: "case_card",
        case_id: reference.case_id,
        case_version_id: reference.case_version_id ?? null,
        title: reference.title,
        status: reference.status,
      })
    }

    setSending(true)
    try {
      const sent = await onSend(parts)
      if (sent) {
        updateValue("")
        setAttachments([])
        onCaseReferencesChange?.([])
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
            disabled={blocked || !hasContent}
          >
            {sending ? "发送中…" : uploading ? "解析中…" : "发送"}
          </Button>
        </div>
      </div>
      {error !== undefined ? (
        <div className="flex flex-wrap items-center gap-2 pt-1.5">
          <InlineError error={error} />
          {attachmentPending ? (
            <Button
              variant="secondary"
              size="sm"
              disabled={blocked || !hasContent}
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

"use client"

import { Paperclip, X } from "lucide-react"
import { useId, useRef, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { formatBytes } from "@/lib/format"

import { uploadContent } from "./api"
import type { UploadedAttachment } from "./types"

/** P1 attachment limit from `POST /api/content` (10 MiB, spec §7). */
export const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024

export type AttachmentUploaderProps = {
  items: UploadedAttachment[]
  onChange: (items: UploadedAttachment[]) => void
  /** Uploaded but not yet sent; the composer disables send while uploading. */
  onUploadingChange?: (uploading: boolean) => void
  maxBytes?: number
  disabled?: boolean
}

function describeError(cause: unknown): string {
  if (cause instanceof Error && cause.message.length > 0) return cause.message
  return "附件上传失败，请重试"
}

/**
 * Uploads files through `POST /api/content` before the message is sent. Each
 * successful upload becomes a `{sha256,name,mime,size}` part; failures stay
 * visible inline and never block the message composer.
 */
export function AttachmentUploader({
  items,
  onChange,
  onUploadingChange,
  maxBytes = MAX_ATTACHMENT_BYTES,
  disabled = false,
}: AttachmentUploaderProps) {
  const inputRef = useRef<HTMLInputElement>(null)
  const inputId = useId()
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function setUploadingState(next: boolean): void {
    setUploading(next)
    onUploadingChange?.(next)
  }

  async function handleFiles(files: FileList | null): Promise<void> {
    if (files === null || files.length === 0) return
    setError(null)

    const accepted: UploadedAttachment[] = []
    let failure: string | null = null
    setUploadingState(true)
    try {
      for (const file of Array.from(files)) {
        if (file.size > maxBytes) {
          failure = `附件「${file.name}」不能超过 ${formatBytes(maxBytes)}`
          continue
        }
        try {
          const content = await uploadContent(file)
          accepted.push({
            sha256: content.sha256,
            name: file.name,
            mime: content.mime,
            size: content.size,
          })
        } catch (cause) {
          failure = `${file.name}：${describeError(cause)}`
        }
      }
      if (accepted.length > 0) onChange([...items, ...accepted])
      if (failure !== null) setError(failure)
    } finally {
      setUploadingState(false)
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <input
          ref={inputRef}
          id={inputId}
          type="file"
          multiple
          className="hidden"
          data-testid="attachment-input"
          accept=".md,.markdown,.txt,.docx,.pptx,.pdf,.png,.jpg,.jpeg,.webp"
          disabled={disabled || uploading}
          onChange={(event) => {
            void handleFiles(event.target.files)
            event.target.value = ""
          }}
        />
        <Button
          variant="ghost"
          size="sm"
          aria-label="添加附件"
          disabled={disabled || uploading}
          onClick={() => inputRef.current?.click()}
          className="gap-1 px-2 text-muted-foreground"
        >
          <Paperclip className="size-3.5" aria-hidden="true" />
          {uploading ? "上传中…" : "附件"}
        </Button>
        {items.map((item) => (
          <span
            key={item.sha256}
            className={cn(
              "inline-flex items-center gap-1 rounded-full border border-border bg-muted px-2 py-0.5",
              "text-[11px] text-muted-foreground",
            )}
          >
            {item.name}
            <button
              type="button"
              aria-label={`移除附件 ${item.name}`}
              className="rounded-full p-0.5 hover:bg-background hover:text-foreground"
              onClick={() =>
                onChange(items.filter((candidate) => candidate.sha256 !== item.sha256))
              }
            >
              <X className="size-3" aria-hidden="true" />
            </button>
          </span>
        ))}
      </div>
      {error !== null ? (
        <p role="alert" className="text-[11px] text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  )
}

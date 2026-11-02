"use client"

import { Paperclip, X } from "lucide-react"
import { forwardRef, useId, useImperativeHandle, useRef, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { formatBytes } from "@/lib/format"

import { uploadContent } from "./api"
import type { UploadedAttachment } from "./types"

/** P1 attachment limit from `POST /api/content` (10 MiB, spec §7). */
export const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024

/** Native uploader accept list (spec §8.1). */
export const ATTACHMENT_ACCEPT =
  ".md,.markdown,.txt,.html,.htm,.docx,.pptx,.xlsx,.pdf,.png,.jpg,.jpeg,.webp"

/** Legacy binary Office formats are rejected with a save-as hint (spec §8.1). */
export function isLegacyOfficeFile(name: string): boolean {
  return /\.(doc|ppt)$/i.test(name.trim())
}

/** Mirrors the backend's save-as message so the hint is visible before upload. */
export function legacyOfficeMessage(name: string): string {
  return `附件「${name}」是旧版 Office 格式（.doc/.ppt），请另存为 .docx/.pptx 后重新上传`
}

export type AttachmentUploaderProps = {
  items: UploadedAttachment[]
  onChange: (items: UploadedAttachment[]) => void
  /** Uploaded but not yet sent; the composer disables send while uploading. */
  onUploadingChange?: (uploading: boolean) => void
  maxBytes?: number
  disabled?: boolean
}

/** Imperative bridge used by the composer's paste/drop handlers. */
export type AttachmentUploaderHandle = {
  addFiles: (files: FileList | File[]) => void
}

function describeError(cause: unknown): string {
  if (cause instanceof Error && cause.message.length > 0) return cause.message
  return "附件上传失败，请重试"
}

/**
 * Uploads files through `POST /api/content` before the message is sent. Each
 * successful upload becomes a `{sha256,name,mime,size}` part; failures stay
 * visible inline and never block the message composer. Paste/drop files enter
 * through `addFiles` so they use this same pipeline.
 */
export const AttachmentUploader = forwardRef<AttachmentUploaderHandle, AttachmentUploaderProps>(
  function AttachmentUploader(
    {
      items,
      onChange,
      onUploadingChange,
      maxBytes = MAX_ATTACHMENT_BYTES,
      disabled = false,
    }: AttachmentUploaderProps,
    ref,
  ) {
    const inputRef = useRef<HTMLInputElement>(null)
    const inputId = useId()
    const [uploading, setUploading] = useState(false)
    const [error, setError] = useState<string | null>(null)

    function setUploadingState(next: boolean): void {
      setUploading(next)
      onUploadingChange?.(next)
    }

    async function handleFiles(files: FileList | File[] | null): Promise<void> {
      if (files === null || files.length === 0) return
      setError(null)

      const accepted: UploadedAttachment[] = []
      let failure: string | null = null
      setUploadingState(true)
      try {
        for (const file of Array.from(files)) {
          if (isLegacyOfficeFile(file.name)) {
            failure = legacyOfficeMessage(file.name)
            continue
          }
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

    useImperativeHandle(ref, () => ({
      addFiles(files: FileList | File[]) {
        if (disabled) return
        void handleFiles(files)
      },
    }))

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
            accept={ATTACHMENT_ACCEPT}
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
  },
)

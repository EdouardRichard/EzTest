"use client"

import { Paperclip, X } from "lucide-react"
import { forwardRef, useEffect, useId, useImperativeHandle, useRef, useState } from "react"

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

/** One file queued for upload; rendered as a "解析中" chip until it resolves. */
type PendingUpload = {
  key: number
  name: string
  file: File
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
    const [pendingUploads, setPendingUploads] = useState<PendingUpload[]>([])
    // Latest committed list: uploads resolve asynchronously, so merging against
    // the stale `items` prop silently drops a concurrent paste/drop batch.
    const itemsRef = useRef(items)
    const queueRef = useRef<Promise<void>>(Promise.resolve())
    const pendingCountRef = useRef(0)
    const nextKeyRef = useRef(0)

    useEffect(() => {
      itemsRef.current = items
    }, [items])

    function setUploadingState(next: boolean): void {
      setUploading(next)
      onUploadingChange?.(next)
    }

    function beginPending(entries: PendingUpload[]): void {
      if (entries.length === 0) return
      const idle = pendingCountRef.current === 0
      pendingCountRef.current += entries.length
      setPendingUploads((current) => [...current, ...entries])
      if (idle) setUploadingState(true)
    }

    function finishPending(keys: number[]): void {
      if (keys.length === 0) return
      const finished = new Set(keys)
      pendingCountRef.current = Math.max(0, pendingCountRef.current - keys.length)
      setPendingUploads((current) => current.filter((entry) => !finished.has(entry.key)))
      if (pendingCountRef.current === 0) setUploadingState(false)
    }

    function commit(next: UploadedAttachment[]): void {
      itemsRef.current = next
      onChange(next)
    }

    async function uploadEntry(
      entry: PendingUpload,
    ): Promise<{ accepted?: UploadedAttachment; failure?: string }> {
      if (isLegacyOfficeFile(entry.name)) {
        return { failure: legacyOfficeMessage(entry.name) }
      }
      if (entry.file.size > maxBytes) {
        return { failure: `附件「${entry.name}」不能超过 ${formatBytes(maxBytes)}` }
      }
      try {
        const content = await uploadContent(entry.file)
        return {
          accepted: {
            sha256: content.sha256,
            name: entry.name,
            mime: content.mime,
            size: content.size,
          },
        }
      } catch (cause) {
        return { failure: `${entry.name}：${describeError(cause)}` }
      }
    }

    async function uploadBatch(entries: PendingUpload[]): Promise<void> {
      let failure: string | null = null
      const finished = new Set<number>()
      // Do not clear a previous batch's inline error up front: a later batch
      // succeeding must not hide an earlier failed upload.
      try {
        for (const entry of entries) {
          const result = await uploadEntry(entry)
          if (result.failure !== undefined) failure = result.failure
          if (result.accepted !== undefined) commit([...itemsRef.current, result.accepted])
          finished.add(entry.key)
          finishPending([entry.key])
        }
      } finally {
        // Defensive: an unexpected throw must not leave the send button wedged.
        const remaining = entries
          .filter((entry) => !finished.has(entry.key))
          .map((entry) => entry.key)
        finishPending(remaining)
        if (failure !== null) setError(failure)
      }
    }

    function addFiles(files: FileList | File[] | null): void {
      if (files === null || disabled) return
      const entries: PendingUpload[] = []
      for (const file of Array.from(files)) {
        entries.push({ key: nextKeyRef.current++, name: file.name, file })
      }
      if (entries.length === 0) return
      // Queue instead of running concurrently: two overlapping paste/drop
      // batches used to race on the items state and lose one of them.
      beginPending(entries)
      queueRef.current = queueRef.current
        .then(() => uploadBatch(entries))
        .catch(() => {
          // uploadBatch records its own failure; keep the queue usable.
        })
    }

    useImperativeHandle(ref, () => ({
      addFiles(files: FileList | File[]) {
        addFiles(files)
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
              addFiles(event.target.files)
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
          {pendingUploads.map((entry) => (
            <span
              key={`pending-${entry.key}`}
              data-testid="attachment-pending"
              aria-busy="true"
              className={cn(
                "inline-flex items-center gap-1 rounded-full border border-dashed border-border bg-muted px-2 py-0.5",
                "text-[11px] text-muted-foreground",
              )}
            >
              {entry.name}
              <span className="text-[10px]">解析中…</span>
            </span>
          ))}
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
                  commit(itemsRef.current.filter((candidate) => candidate.sha256 !== item.sha256))
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

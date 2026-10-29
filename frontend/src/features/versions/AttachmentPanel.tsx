"use client"

import { Download, Loader2, Paperclip, Trash2, Upload } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

import { ApiError } from "@/lib/api-client"

import { deleteAttachment, downloadAttachment, listAttachments, uploadAttachment } from "./api"
import type { Requirement, RequirementAttachment } from "./types"

/** Human labels for the closed attachment error reasons (spec §6.7, §7.1). */
const ATTACHMENT_REASON_LABELS: Record<string, string> = {
  duplicate_attachment: "相同内容的附件已存在。",
  attachment_too_large: "单个附件不能超过 10 MiB。",
  attachment_limit_exceeded: "附件数量已达上限（每条需求 10 个，每个版本 50 个）。",
  unsupported_attachment_type: "不支持该附件类型，请上传 txt/md/json/yaml/png/jpg。",
  invalid_attachment_content: "附件内容无效，文本附件必须是 UTF-8。",
  requirement_state_conflict: "当前需求状态不允许修改附件，请刷新后重试。",
  revision_conflict: "数据已被更新，请刷新后重试。",
}

function describeAttachmentError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = ATTACHMENT_REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

/** Compact byte label for attachment metadata; sizes are always > 0 (spec §5.1). */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

const ACCEPT = ".txt,.md,.json,.yaml,.yml,.png,.jpg,.jpeg"

export type AttachmentPanelProps = {
  versionId: string
  requirement: Requirement
  /** Called after a successful upload/delete so parent counts can refresh. */
  onChanged: () => void
}

/**
 * Requirement attachments (spec §6.7, §9.2): metadata list, multipart upload,
 * authenticated download and metadata delete. Only draft requirements can
 * upload or delete; clarified requirements keep their attachments readable.
 */
export function AttachmentPanel({ versionId, requirement, onChanged }: AttachmentPanelProps) {
  const [attachments, setAttachments] = useState<RequirementAttachment[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [uploading, setUploading] = useState(false)
  const [downloadId, setDownloadId] = useState<string | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const requestRef = useRef(0)

  const load = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoading(true)
    setError("")
    try {
      const items = await listAttachments(versionId, requirement.id)
      if (requestRef.current === requestId) setAttachments(items)
    } catch (caught) {
      if (requestRef.current === requestId) {
        setError(`加载附件失败：${describeAttachmentError(caught)}`)
      }
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [versionId, requirement.id])

  useEffect(() => {
    void load()
    return () => {
      // Invalidate requests that were started for this requirement/version.
      requestRef.current += 1
    }
  }, [load])

  const editable = requirement.status === "draft"

  async function handleUpload(file: File): Promise<void> {
    setUploading(true)
    setError("")
    try {
      await uploadAttachment(versionId, requirement.id, file)
      await load()
      onChanged()
    } catch (caught) {
      setError(describeAttachmentError(caught))
    } finally {
      setUploading(false)
    }
  }

  async function handleDownload(attachment: RequirementAttachment): Promise<void> {
    setDownloadId(attachment.id)
    setError("")
    try {
      const blob = await downloadAttachment(versionId, requirement.id, attachment.id)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement("a")
      anchor.href = url
      anchor.download = attachment.filename
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
      URL.revokeObjectURL(url)
    } catch (caught) {
      setError(describeAttachmentError(caught))
    } finally {
      setDownloadId(null)
    }
  }

  async function handleDelete(attachment: RequirementAttachment): Promise<void> {
    setDeletingId(attachment.id)
    setError("")
    try {
      await deleteAttachment(versionId, requirement.id, attachment.id)
      await load()
      onChanged()
    } catch (caught) {
      setError(describeAttachmentError(caught))
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <section aria-label="需求附件" className="space-y-3 rounded-xl border border-border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="flex items-center gap-1.5 text-sm font-medium">
          <Paperclip className="h-4 w-4" aria-hidden="true" />
          附件
        </h3>
        <span className="text-xs text-muted-foreground">{`共 ${attachments.length} 个`}</span>
      </div>

      {editable ? (
        <div className="space-y-1">
          <label
            htmlFor={`attachment-upload-${requirement.id}`}
            className="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-xs font-medium transition-colors hover:bg-muted focus-within:ring-2 focus-within:ring-ring"
          >
            {uploading ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <Upload className="h-3.5 w-3.5" aria-hidden="true" />
            )}
            {uploading ? "上传中…" : "上传附件"}
          </label>
          <input
            id={`attachment-upload-${requirement.id}`}
            type="file"
            aria-label="上传附件"
            accept={ACCEPT}
            disabled={uploading}
            onChange={(event) => {
              const file = event.target.files?.[0]
              if (file === undefined) return
              event.target.value = ""
              void handleUpload(file)
            }}
            className="sr-only"
          />
          <p className="text-[11px] text-muted-foreground">
            支持 txt/md/json/yaml/png/jpg，单文件 ≤10 MiB。
          </p>
        </div>
      ) : (
        <p className="rounded-lg border border-dashed border-border px-2.5 py-1.5 text-[11px] text-muted-foreground">
          已确认需求为只读，附件不可上传或删除。
        </p>
      )}

      {error === "" ? null : (
        <div
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          <p>{error}</p>
          <button
            type="button"
            onClick={() => void load()}
            className="mt-1.5 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            重试
          </button>
        </div>
      )}

      {loading ? (
        <p className="flex items-center gap-2 py-2 text-xs text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
          正在加载附件…
        </p>
      ) : null}

      {!loading && attachments.length === 0 && error === "" ? (
        <div className="rounded-lg border border-dashed border-border px-3 py-4 text-center">
          <p className="text-xs font-medium">暂无附件</p>
          <p className="mt-1 text-[11px] text-muted-foreground">
            可上传文本说明或截图作为需求补充材料。
          </p>
        </div>
      ) : null}

      {attachments.length === 0 ? null : (
        <ul aria-label="附件列表" className="space-y-1.5">
          {attachments.map((attachment) => (
            <li
              key={attachment.id}
              className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-2.5 py-1.5"
            >
              <div className="min-w-0">
                <p className="break-all text-xs font-medium">{attachment.filename}</p>
                <p className="text-[11px] text-muted-foreground">
                  {attachment.kind === "image" ? "图片" : "文本"} ·{" "}
                  {formatBytes(attachment.sizeBytes)}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-1">
                <button
                  type="button"
                  aria-label={`下载附件 ${attachment.filename}`}
                  onClick={() => void handleDownload(attachment)}
                  disabled={downloadId === attachment.id}
                  className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2 py-1 text-[11px] font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {downloadId === attachment.id ? (
                    <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                  ) : (
                    <Download className="h-3 w-3" aria-hidden="true" />
                  )}
                  下载
                </button>
                {editable ? (
                  <button
                    type="button"
                    aria-label={`删除附件 ${attachment.filename}`}
                    onClick={() => void handleDelete(attachment)}
                    disabled={deletingId === attachment.id}
                    className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2 py-1 text-[11px] font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {deletingId === attachment.id ? (
                      <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                    ) : (
                      <Trash2 className="h-3 w-3" aria-hidden="true" />
                    )}
                    删除
                  </button>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

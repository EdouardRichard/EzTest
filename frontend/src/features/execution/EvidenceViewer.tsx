"use client"

import { Download, FileText } from "lucide-react"
import Link from "next/link"
import { useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatBytes, formatDateTime } from "@/lib/format"

import { downloadEvidence } from "./api"
import { EVIDENCE_KIND_LABEL } from "./labels"
import type { Evidence } from "./types"

export type EvidenceViewerProps = {
  versionId: string
  evidence: Evidence | null
  open: boolean
  onClose: () => void
}

/** Kinds whose bytes are an image and can be previewed as a thumbnail. */
const IMAGE_EVIDENCE_KINDS = new Set<Evidence["kind"]>([
  "screenshot",
  "snapshot",
  "visual_actual",
  "visual_diff",
])

/**
 * Deep link to the execution tab's `?evidence=` handler (v0.22 FE-8/FE-9).
 * The link opens the immutable evidence dialog by ID without packaging bytes.
 */
export function evidenceDeepLinkHref(versionId: string, evidenceId: string): string {
  const params = new URLSearchParams({ tab: "execution", evidence: evidenceId })
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = filename
  anchor.rel = "noopener"
  anchor.style.display = "none"
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

export type EvidenceThumbnailProps = {
  versionId: string
  evidence: Evidence
  onOpen?: (evidence: Evidence) => void
}

/**
 * Compact evidence preview used by report pages (v0.22 FE-9): screenshot bytes
 * render as a small thumbnail through an authenticated blob URL, while every
 * kind keeps the execution deep link and the original-file download.
 */
export function EvidenceThumbnail({ versionId, evidence, onOpen }: EvidenceThumbnailProps) {
  const imageLike = IMAGE_EVIDENCE_KINDS.has(evidence.kind)
  const [previewUrl, setPreviewUrl] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  const [downloading, setDownloading] = useState(false)
  const [downloadError, setDownloadError] = useState<unknown>(null)

  useEffect(() => {
    if (!imageLike) return undefined
    let cancelled = false
    let objectUrl: string | null = null
    setPreviewUrl(null)
    setFailed(false)
    downloadEvidence(versionId, evidence.id)
      .then((result) => {
        if (cancelled) return
        objectUrl = URL.createObjectURL(result.blob)
        setPreviewUrl(objectUrl)
      })
      .catch(() => {
        if (!cancelled) setFailed(true)
      })
    return () => {
      cancelled = true
      if (objectUrl !== null) URL.revokeObjectURL(objectUrl)
    }
  }, [versionId, evidence.id, imageLike])

  async function handleDownload(): Promise<void> {
    if (downloading) return
    setDownloading(true)
    setDownloadError(null)
    try {
      const result = await downloadEvidence(versionId, evidence.id)
      saveBlob(
        result.blob,
        result.filename.length > 0
          ? result.filename
          : evidence.filename || `evidence-${evidence.id}`,
      )
    } catch (cause) {
      setDownloadError(cause)
    } finally {
      setDownloading(false)
    }
  }

  return (
    <div className="w-28 space-y-1" data-testid={`evidence-card-${evidence.id}`}>
      <button
        type="button"
        data-testid={`evidence-thumb-${evidence.id}`}
        aria-label={`查看证据 #${evidence.id}`}
        onClick={() => onOpen?.(evidence)}
        className="flex h-20 w-full items-center justify-center overflow-hidden rounded-md border border-border bg-muted/40 text-[10px] text-muted-foreground hover:bg-muted"
      >
        {previewUrl !== null ? (
          // biome-ignore lint/performance/noImgElement: evidence bytes are auth-gated blob URLs that next/image cannot optimize
          <img
            src={previewUrl}
            alt={evidence.filename.length > 0 ? evidence.filename : `证据 #${evidence.id}`}
            className="h-full w-full object-cover"
          />
        ) : (
          <span className="flex flex-col items-center gap-1">
            <FileText className="size-4" aria-hidden="true" />
            {failed ? "缩略图不可用" : EVIDENCE_KIND_LABEL[evidence.kind]}
          </span>
        )}
      </button>
      <div className="flex items-center justify-between gap-1 text-[10px] text-muted-foreground">
        <Link
          href={evidenceDeepLinkHref(versionId, evidence.id)}
          aria-label={`证据 #${evidence.id} 深链`}
          className="text-primary underline-offset-2 hover:underline"
        >
          深链
        </Link>
        <button
          type="button"
          aria-label={`下载证据 #${evidence.id} 原文件`}
          disabled={downloading}
          onClick={() => {
            void handleDownload()
          }}
          className="underline-offset-2 hover:underline disabled:opacity-50"
        >
          {downloading ? "下载中…" : "原文件"}
        </button>
      </div>
      {downloadError !== null ? (
        <p role="alert" className="text-[10px] text-destructive">
          原文件下载失败
        </p>
      ) : null}
    </div>
  )
}

/**
 * Evidence dialog (spec §12.1): the bytes always come from the authenticated
 * `apiDownload` path, screenshots render through a revoked object URL, and
 * text-like evidence renders inline. Evidence is immutable and read-only.
 */
export function EvidenceViewer({ versionId, evidence, open, onClose }: EvidenceViewerProps) {
  const [loading, setLoading] = useState(false)
  const [downloading, setDownloading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [previewUrl, setPreviewUrl] = useState<string | null>(null)
  const [text, setText] = useState<string | null>(null)
  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken/evidence trigger an explicit refetch
  useEffect(() => {
    if (!open || evidence === null) return undefined
    let cancelled = false
    let objectUrl: string | null = null

    setLoading(true)
    setError(null)
    setPreviewUrl(null)
    setText(null)

    downloadEvidence(versionId, evidence.id)
      .then(async (result) => {
        if (cancelled) return
        if (
          evidence.kind === "screenshot" ||
          evidence.kind === "snapshot" ||
          evidence.kind === "visual_actual" ||
          evidence.kind === "visual_diff"
        ) {
          objectUrl = URL.createObjectURL(result.blob)
          setPreviewUrl(objectUrl)
        } else {
          setText(await result.blob.text())
        }
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
      if (objectUrl !== null) URL.revokeObjectURL(objectUrl)
    }
  }, [open, evidence, versionId, reloadToken])

  if (evidence === null) return null

  async function handleDownload(): Promise<void> {
    if (evidence === null || downloading) return
    setDownloading(true)
    try {
      const result = await downloadEvidence(versionId, evidence.id)
      saveBlob(
        result.blob,
        result.filename.length > 0
          ? result.filename
          : evidence.filename || `evidence-${evidence.id}`,
      )
    } catch (cause) {
      setError(cause)
    } finally {
      setDownloading(false)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={`证据 #${evidence.id}`}
      description={evidence.filename.length > 0 ? evidence.filename : undefined}
      footer={
        <>
          <Link
            href={evidenceDeepLinkHref(versionId, evidence.id)}
            aria-label={`证据 #${evidence.id} 深链`}
            className="mr-auto text-[11px] text-primary underline-offset-2 hover:underline"
          >
            深链
          </Link>
          <Button variant="secondary" size="sm" onClick={onClose}>
            关闭
          </Button>
          <Button
            size="sm"
            disabled={loading || downloading}
            onClick={() => {
              void handleDownload()
            }}
          >
            <Download className="size-3.5" aria-hidden="true" />
            {downloading ? "下载中…" : "下载"}
          </Button>
        </>
      }
    >
      {loading ? <LoadingCard label="正在加载证据…" /> : null}

      {!loading && error !== null ? (
        <ErrorState
          error={error}
          title="证据加载失败"
          onRetry={() => setReloadToken((token) => token + 1)}
        />
      ) : null}

      {!loading && error === null ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
            <Badge tone="neutral">{EVIDENCE_KIND_LABEL[evidence.kind]}</Badge>
            <span>
              {EVIDENCE_KIND_LABEL[evidence.kind]} · {evidence.mime}
            </span>
            <Badge tone="info">不可变证据</Badge>
          </div>

          {previewUrl !== null ? (
            // biome-ignore lint/performance/noImgElement: evidence bytes are auth-gated blob URLs that next/image cannot optimize
            <img
              data-testid="evidence-image"
              src={previewUrl}
              alt={evidence.filename.length > 0 ? evidence.filename : "证据截图"}
              className="max-h-[420px] w-full rounded-lg border border-border object-contain"
            />
          ) : null}

          {text !== null ? (
            <pre
              data-testid="evidence-text"
              className="max-h-[420px] overflow-auto rounded-lg border border-border bg-muted/40 p-3 text-[11px] leading-relaxed"
            >
              {text}
            </pre>
          ) : null}

          <dl className="space-y-1 text-[11px] text-muted-foreground">
            <div>sha256: {evidence.sha256}</div>
            <div>大小 {formatBytes(evidence.size_bytes)}</div>
            <div>创建于 {formatDateTime(evidence.created_at)}</div>
          </dl>
        </div>
      ) : null}
    </Dialog>
  )
}

"use client"

import { Download } from "lucide-react"
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
      const url = URL.createObjectURL(result.blob)
      const anchor = document.createElement("a")
      anchor.href = url
      anchor.download = result.filename.length > 0 ? result.filename : evidence.filename
      anchor.rel = "noopener"
      anchor.click()
      URL.revokeObjectURL(url)
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

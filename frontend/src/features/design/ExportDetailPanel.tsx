"use client"

import Link from "next/link"
import { useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatBytes, formatDateTime } from "@/lib/format"

import { downloadExport, getExport } from "./api"
import { saveDownload } from "./download"
import type { ExportStatus, ExportView } from "./types"

export type ExportDetailPanelProps = {
  exportId: string
  /** The creating dialog already holds the row; it still re-reads the detail. */
  initialView?: ExportView | null
}

const STATUS_LABEL: Record<ExportStatus, string> = {
  ready: "已就绪",
  failed: "生成失败",
}

const STATUS_TONE: Record<ExportStatus, BadgeTone> = {
  ready: "success",
  failed: "danger",
}

function shortHash(hash: string): string {
  return hash.length <= 12 ? hash : hash.slice(0, 12)
}

/**
 * One export artifact's detail (`GET /api/exports/{exportId}`): metadata, the
 * authenticated byte download and a deep link back to the design workspace
 * that produced it. The download goes through api-client, never a bare anchor.
 */
export function ExportDetailPanel({ exportId, initialView = null }: ExportDetailPanelProps) {
  const [view, setView] = useState<ExportView | null>(initialView)
  const [error, setError] = useState<unknown>(null)
  const [downloadError, setDownloadError] = useState<unknown>(null)
  const [downloading, setDownloading] = useState(false)

  const load = useCallback(async () => {
    setError(null)
    try {
      setView(await getExport(exportId))
    } catch (cause) {
      setView(null)
      setError(cause)
    }
  }, [exportId])

  useEffect(() => {
    void load()
  }, [load])

  async function download(): Promise<void> {
    if (downloading) return
    setDownloading(true)
    setDownloadError(null)
    try {
      const download = await downloadExport(exportId)
      saveDownload(download, view?.filename ?? `export-${exportId}`)
    } catch (cause) {
      setDownloadError(cause)
    } finally {
      setDownloading(false)
    }
  }

  return (
    <section
      data-testid="export-detail"
      aria-label="导出详情"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold text-foreground">导出详情</h3>
          <p className="text-[11px] text-muted-foreground">
            {view === null
              ? "读取导出元数据与下载入口"
              : `${view.filename} · 修订 #${view.revision}`}
          </p>
        </div>
        {view !== null ? (
          <Badge tone={STATUS_TONE[view.status]}>{STATUS_LABEL[view.status]}</Badge>
        ) : null}
      </header>

      {error !== null ? (
        <ErrorState error={error} title="导出详情加载失败" onRetry={() => void load()} />
      ) : view === null ? (
        <LoadingCard label="正在加载导出详情…" />
      ) : (
        <div className="space-y-2">
          <dl className="grid grid-cols-2 gap-1.5 text-[11px] sm:grid-cols-4">
            <div>
              <dt className="text-muted-foreground">文件名</dt>
              <dd className="truncate font-medium text-foreground">{view.filename}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">格式</dt>
              <dd className="font-medium text-foreground">{view.format}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">修订</dt>
              <dd className="font-medium text-foreground">r{view.revision}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">大小</dt>
              <dd className="font-medium text-foreground">{formatBytes(view.size)}</dd>
            </div>
            <div className="col-span-2">
              <dt className="text-muted-foreground">MIME</dt>
              <dd className="font-medium text-foreground">{view.mime}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">所有者</dt>
              <dd className="font-medium text-foreground">
                {view.owner_kind} #{view.owner_id}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">创建时间</dt>
              <dd className="font-medium text-foreground">{formatDateTime(view.created_at)}</dd>
            </div>
            <div className="col-span-2 sm:col-span-4">
              <dt className="text-muted-foreground">SHA256</dt>
              <dd className="break-all font-mono text-[10px] text-foreground" title={view.sha256}>
                {shortHash(view.sha256)}
              </dd>
            </div>
          </dl>

          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" disabled={downloading} onClick={() => void download()}>
              {downloading ? "下载中…" : "下载导出"}
            </Button>
            <Link
              href={`/versions/${encodeURIComponent(view.version_id)}?tab=design&export=${encodeURIComponent(view.id)}`}
              className="text-[11px] text-primary underline underline-offset-2"
            >
              在设计中打开
            </Link>
            {downloadError !== null ? <InlineError error={downloadError} /> : null}
          </div>
        </div>
      )}
    </section>
  )
}

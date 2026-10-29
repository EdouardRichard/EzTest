"use client"

import { Download, Loader2 } from "lucide-react"
import { useState } from "react"

import { describeDeliveryError, exportReport } from "../api"

/** Downloads one blob through a short-lived object URL, always revoking it. */
function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = filename
  anchor.rel = "noopener"
  anchor.style.display = "none"
  document.body.appendChild(anchor)
  try {
    anchor.click()
  } finally {
    anchor.remove()
    URL.revokeObjectURL(url)
  }
}

export type ReportExportButtonProps = {
  versionId: string
  reportId: string
}

/**
 * Markdown export of one report revision (spec §10.4): the bytes go through
 * `apiStreamFetch` (inside `exportReport`), the filename comes from
 * `Content-Disposition`, and the `X-Content-SHA256` digest is shown as a
 * prefix. A failure stays on screen with the retryable hint.
 */
export function ReportExportButton({ versionId, reportId }: ReportExportButtonProps) {
  const [exporting, setExporting] = useState(false)
  const [notice, setNotice] = useState("")
  const [error, setError] = useState("")

  async function handleExport(): Promise<void> {
    if (exporting) return
    setExporting(true)
    setError("")
    setNotice("")
    try {
      const download = await exportReport(versionId, reportId)
      downloadBlob(download.blob, download.filename)
      const digest = download.sha256 === "" ? "" : ` · sha256 ${download.sha256.slice(0, 12)}`
      setNotice(`已导出 ${download.filename}${digest}`)
    } catch (caught) {
      setError(describeDeliveryError(caught))
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="flex flex-col items-end gap-1">
      <button
        type="button"
        data-testid="report-export-btn"
        onClick={() => void handleExport()}
        disabled={exporting}
        className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
      >
        {exporting ? (
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        ) : (
          <Download className="h-4 w-4" aria-hidden="true" />
        )}
        {exporting ? "导出中…" : "导出 Markdown"}
      </button>
      {notice === "" ? null : (
        <p
          role="status"
          data-testid="report-export-notice"
          className="max-w-[22rem] text-right text-[11px] text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      )}
      {error === "" ? null : (
        <p
          role="alert"
          data-testid="report-export-error"
          className="max-w-[22rem] text-right text-[11px] text-destructive"
        >
          {error}
        </p>
      )}
    </div>
  )
}

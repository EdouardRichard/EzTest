"use client"

import { useState } from "react"

import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { formatBytes } from "@/lib/format"

import { createExport, downloadExport } from "./api"
import { saveDownload } from "./download"
import { EXPORT_FORMATS, type ExportFormat, type ExportView } from "./types"

const FORMAT_LABEL: Record<ExportFormat, string> = {
  markdown: "Markdown",
  mermaid: "Mermaid",
  opml: "OPML",
  csv: "CSV",
}

export type ExportCasesDialogProps = {
  versionId: string
  open: boolean
  onClose: () => void
  onExported?: (view: ExportView) => void
}

/**
 * Whole-version case export dialog (spec §7.4/§10.2): pick one of the four
 * formats, create a new export revision, download it through the authenticated
 * api-client and show the result. Exports carry design content only.
 */
export function ExportCasesDialog({
  versionId,
  open,
  onClose,
  onExported,
}: ExportCasesDialogProps) {
  const [busyFormat, setBusyFormat] = useState<ExportFormat | null>(null)
  const [lastFormat, setLastFormat] = useState<ExportFormat>("markdown")
  const [error, setError] = useState<unknown>(null)
  const [result, setResult] = useState<ExportView | null>(null)

  async function run(format: ExportFormat): Promise<void> {
    if (busyFormat !== null) return
    setBusyFormat(format)
    setLastFormat(format)
    setError(null)
    setResult(null)
    try {
      const view = await createExport(versionId, {
        owner_kind: "case",
        owner_id: versionId,
        format,
      })
      const download = await downloadExport(view.id)
      saveDownload(download, view.filename)
      setResult(view)
      onExported?.(view)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusyFormat(null)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="导出用例"
      description="整版本导出当前用例设计（需求/场景/用例/步骤/断言/来源），可选 Markdown、Mermaid、OPML 或 CSV。"
      footer={
        <Button variant="secondary" size="sm" onClick={onClose}>
          关闭
        </Button>
      }
    >
      <div className="grid grid-cols-2 gap-2">
        {EXPORT_FORMATS.map((format) => (
          <Button
            key={format}
            variant="secondary"
            size="sm"
            data-testid={`export-format-${format}`}
            disabled={busyFormat !== null}
            onClick={() => {
              void run(format)
            }}
          >
            {busyFormat === format ? "导出中…" : FORMAT_LABEL[format]}
          </Button>
        ))}
      </div>

      {error !== null ? (
        <div className="mt-3 space-y-2">
          <InlineError error={error} />
          <Button
            size="sm"
            variant="secondary"
            onClick={() => {
              void run(lastFormat)
            }}
          >
            重试
          </Button>
        </div>
      ) : null}

      {result !== null && busyFormat === null ? (
        <p data-testid="export-status" role="status" className="mt-3 text-[11px] text-success">
          已导出 {result.filename}（{formatBytes(result.size)}）
        </p>
      ) : null}
    </Dialog>
  )
}

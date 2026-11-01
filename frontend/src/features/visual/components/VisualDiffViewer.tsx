"use client"

import { useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { downloadEvidence } from "@/features/execution/api"

import { getVisualDiff } from "../api"
import {
  formatRatio,
  VISUAL_CLASSIFICATION_LABEL,
  VISUAL_CLASSIFICATION_TONE,
  type VisualDiff,
  type VisualDiffDetail,
  visualReasonLabel,
} from "../types"

export type VisualDiffViewerProps = {
  versionId: string
  diff: VisualDiff | null
  open: boolean
  onClose: () => void
}

function ratioPercent(value: number): string {
  return formatRatio(value)
}

function dprLabel(value: number): string {
  return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(2)))
}

/**
 * Comparison detail dialog (spec §9.3): actual and diff images come from the
 * authenticated evidence download endpoint, never from a URL stored in the
 * database. It also renders the mismatch boxes and the environment fingerprint.
 */
export function VisualDiffViewer({ versionId, diff, open, onClose }: VisualDiffViewerProps) {
  const [detail, setDetail] = useState<VisualDiffDetail | null>(null)
  const [actualUrl, setActualUrl] = useState<string | null>(null)
  const [diffUrl, setDiffUrl] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken re-runs the explicit refetch
  useEffect(() => {
    if (!open || diff === null) return undefined
    let cancelled = false
    const objectUrls: string[] = []

    setLoading(true)
    setError(null)
    setDetail(null)
    setActualUrl(null)
    setDiffUrl(null)

    void (async () => {
      try {
        const nextDetail = await getVisualDiff(versionId, diff.id)
        if (cancelled) return
        setDetail(nextDetail)

        const actual = await downloadEvidence(versionId, nextDetail.actual_evidence_id)
        if (cancelled) return
        const actualObjectUrl = URL.createObjectURL(actual.blob)
        objectUrls.push(actualObjectUrl)
        setActualUrl(actualObjectUrl)

        if (nextDetail.diff_evidence_id !== null && nextDetail.diff_evidence_id !== undefined) {
          const diffDownload = await downloadEvidence(versionId, nextDetail.diff_evidence_id)
          if (cancelled) return
          const diffObjectUrl = URL.createObjectURL(diffDownload.blob)
          objectUrls.push(diffObjectUrl)
          setDiffUrl(diffObjectUrl)
        }
      } catch (cause) {
        if (!cancelled) setError(cause)
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()

    return () => {
      cancelled = true
      for (const url of objectUrls) URL.revokeObjectURL(url)
    }
  }, [open, diff, versionId, reloadToken])

  if (diff === null) return null

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={`视觉差异 #${diff.id}`}
      description={`用例运行 #${diff.case_run_id}`}
      className="max-w-3xl"
      footer={
        <Button variant="secondary" size="sm" onClick={onClose}>
          关闭
        </Button>
      }
    >
      {loading ? <LoadingCard label="正在加载视觉差异…" /> : null}

      {!loading && error !== null ? (
        <ErrorState
          error={error}
          title="视觉差异加载失败"
          onRetry={() => setReloadToken((token) => token + 1)}
        />
      ) : null}

      {!loading && error === null && detail !== null ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-[11px]">
            <Badge tone={VISUAL_CLASSIFICATION_TONE[detail.classification]}>
              {VISUAL_CLASSIFICATION_LABEL[detail.classification]}
            </Badge>
            <span className="text-muted-foreground">{visualReasonLabel(detail.reason)}</span>
            <span className="text-muted-foreground">
              环境{detail.environment_match ? "一致" : "不一致"}
            </span>
            <span className="ml-auto text-muted-foreground">
              差异比例 {ratioPercent(detail.diff_ratio)} · 阈值 {ratioPercent(detail.threshold)}
            </span>
          </div>

          <div className="grid gap-2 sm:grid-cols-2">
            <figure className="space-y-1">
              <figcaption className="text-[11px] text-muted-foreground">实际截图</figcaption>
              {actualUrl !== null ? (
                // biome-ignore lint/performance/noImgElement: auth-gated blob URL that next/image cannot optimize
                <img
                  data-testid="visual-actual-image"
                  src={actualUrl}
                  alt="视觉实际截图"
                  className="max-h-[360px] w-full rounded-lg border border-border object-contain"
                />
              ) : null}
            </figure>
            <figure className="space-y-1">
              <figcaption className="text-[11px] text-muted-foreground">差异图</figcaption>
              {diffUrl !== null ? (
                // biome-ignore lint/performance/noImgElement: auth-gated blob URL that next/image cannot optimize
                <img
                  data-testid="visual-diff-image"
                  src={diffUrl}
                  alt="视觉差异图"
                  className="max-h-[360px] w-full rounded-lg border border-border object-contain"
                />
              ) : (
                <p className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-[11px] text-muted-foreground">
                  本次比对未生成差异图（{visualReasonLabel(detail.reason)}）。
                </p>
              )}
            </figure>
          </div>

          <div className="flex flex-wrap items-center gap-3 text-[11px] text-muted-foreground">
            <span className="inline-flex items-center gap-1">
              <span>差异比例</span>
              <strong className="text-foreground">{ratioPercent(detail.diff_ratio)}</strong>
            </span>
            <span className="inline-flex items-center gap-1">
              <span>阈值</span>
              <strong className="text-foreground">{ratioPercent(detail.threshold)}</strong>
            </span>
            <span>不匹配区域 {detail.mismatch_boxes.length} 块</span>
            {detail.environment !== null && detail.environment !== undefined ? (
              <span>
                {`${detail.environment.viewport_width}×${detail.environment.viewport_height} @${dprLabel(detail.environment.device_scale_factor)}x`}
              </span>
            ) : null}
            {detail.environment !== null &&
            detail.environment !== undefined &&
            detail.environment.browser !== null &&
            detail.environment.browser !== undefined ? (
              <span>{detail.environment.browser}</span>
            ) : null}
          </div>
        </div>
      ) : null}
    </Dialog>
  )
}

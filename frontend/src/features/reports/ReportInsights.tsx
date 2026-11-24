"use client"

import { useCallback, useEffect, useState } from "react"

import { InlineError } from "@/components/ui/ErrorState"
import { downloadHTML } from "@/features/delivery/api"
import { downloadEvidence } from "@/features/execution/api"
import { revokeSoon } from "@/features/execution/EvidenceViewer"
import { getVersion } from "@/features/versions/api"
import type { Version } from "@/features/versions/types"

import { ReportContentView } from "./ReportContentView"
import { UnverifiedBadge } from "./UnverifiedBadge"
import { WaiverPanel } from "./WaiverPanel"

export type ReportInsightsProps = {
  versionId: string
  reportId: string | null
  revisionNumber: number | null
  htmlAvailable: boolean
  /** Notify the parent tab to refresh its report list/detail after a waiver. */
  onChanged?: () => void
}

/** The evidence content path emitted by the report template (T10). */
const EVIDENCE_PATH = /\/evidence\/(\d+)\/content/

/**
 * The human-useful report surface below the revision metadata (SC-G1/G2/G4/G11):
 * the waived/unverified badge, the admin waiver operations and the in-app view of
 * the authoritative report HTML (five-element cards, AC verification matrix,
 * judge display, failure explanations, viewable evidence). Self-contained: it
 * reads the version waiver projection and the selected revision's HTML through
 * the existing authenticated APIs, so the report body stays the server's single
 * source of truth (宪法五.1, HTML 权威).
 */
export function ReportInsights({
  versionId,
  reportId,
  revisionNumber,
  htmlAvailable,
  onChanged,
}: ReportInsightsProps) {
  const [version, setVersion] = useState<Version | null>(null)
  const [html, setHtml] = useState<string | null>(null)
  const [htmlLoading, setHtmlLoading] = useState(false)
  const [htmlError, setHtmlError] = useState<unknown>(null)
  const [htmlToken, setHtmlToken] = useState(0)
  const [versionToken, setVersionToken] = useState(0)
  const [evidenceError, setEvidenceError] = useState<unknown>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: versionToken forces a refetch after a waiver
  useEffect(() => {
    let cancelled = false
    getVersion(versionId)
      .then((next) => {
        if (!cancelled) setVersion(next)
      })
      .catch(() => {
        // The badge/waiver surface degrades to absent; the report body still renders.
        if (!cancelled) setVersion(null)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, versionToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: htmlToken forces a refetch after a waiver
  useEffect(() => {
    if (reportId === null || revisionNumber === null || !htmlAvailable) {
      setHtml(null)
      setHtmlLoading(false)
      setHtmlError(null)
      return undefined
    }
    let cancelled = false
    setHtmlLoading(true)
    setHtmlError(null)
    downloadHTML(versionId, reportId, revisionNumber)
      .then((download) => download.blob.text())
      .then((text) => {
        if (!cancelled) setHtml(text)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setHtmlError(cause)
      })
      .finally(() => {
        if (!cancelled) setHtmlLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, reportId, revisionNumber, htmlAvailable, htmlToken])

  const openEvidencePath = useCallback(
    (path: string) => {
      const match = EVIDENCE_PATH.exec(path)
      if (match === null) return
      setEvidenceError(null)
      downloadEvidence(versionId, match[1])
        .then((download) => {
          const url = URL.createObjectURL(download.blob)
          window.open(url, "_blank", "noopener,noreferrer")
          revokeSoon(url)
        })
        .catch((cause: unknown) => setEvidenceError(cause))
    },
    [versionId],
  )

  const handleWaived = useCallback(() => {
    setVersionToken((token) => token + 1)
    setHtmlToken((token) => token + 1)
    onChanged?.()
  }, [onChanged])

  return (
    <section
      data-testid="report-insights"
      aria-label="报告全文与豁免"
      className="surface-card space-y-3 p-4"
    >
      <header className="flex flex-wrap items-center gap-2 border-b border-border pb-2">
        <h2 className="text-sm font-semibold text-foreground">报告全文</h2>
        <UnverifiedBadge closureKind={version?.closure_kind} unverified={version?.unverified} />
      </header>

      <WaiverPanel
        versionId={versionId}
        versionStatus={version?.status ?? "running"}
        closureKind={version?.closure_kind}
        unverified={version?.unverified}
        onWaived={handleWaived}
      />

      {evidenceError !== null ? <InlineError error={evidenceError} /> : null}

      <ReportContentView
        html={html}
        loading={htmlLoading}
        error={htmlError}
        onRetry={() => setHtmlToken((token) => token + 1)}
        onOpenEvidencePath={openEvidencePath}
      />
    </section>
  )
}

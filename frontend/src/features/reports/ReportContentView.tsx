"use client"

import { useEffect, useRef } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"

export type ReportContentViewProps = {
  /** The authenticated, server-rendered report HTML (template v3), or null. */
  html: string | null
  loading: boolean
  error: unknown
  onRetry: () => void
  /**
   * Called with the evidence content API path when a report evidence link is
   * clicked. The report's links point at the authenticated content endpoint
   * (`/api/versions/{id}/evidence/{eid}/content`); a raw navigation would 401,
   * so the parent fetches the bytes with the bearer token and opens them.
   */
  onOpenEvidencePath?: (path: string) => void
}

/**
 * In-app view of the authoritative report (SC-G1/G2/G4/G7/G11). The report body
 * is the server's byte-frozen template v3 document — five-element defect cards,
 * AC verification matrix, judge display, failure explanations, evidence appendix
 * and waived content. We render that document VERBATIM into a shadow root so its
 * inline styles are isolated from the app and its fixed format is preserved
 * exactly; there is no second React rendering of the report data (宪法五.1, HTML
 * 权威). The shadow root also lets us intercept evidence links and route them
 * through the authenticated content fetch.
 */
export function ReportContentView({
  html,
  loading,
  error,
  onRetry,
  onOpenEvidencePath,
}: ReportContentViewProps) {
  const hostRef = useRef<HTMLDivElement>(null)

  // The loading/error/empty branches below return a DIFFERENT element, so the
  // shadow host container unmounts while a refetch is in flight and remounts as
  // a NEW element when ready returns. `loading` and `error` are therefore effect
  // dependencies: when ready returns with UNCHANGED html (a requirement waiver
  // does not regenerate the version report, so the refetch yields the same bytes),
  // html/onOpenEvidencePath alone would not re-run the effect and the freshly
  // mounted host would never get its shadow root attached — an empty report box
  // (final fix wave #3). Re-running on the loading/error transition re-attaches
  // the shadow root on every remount.
  // biome-ignore lint/correctness/useExhaustiveDependencies: loading/error force re-attach after the host remounts
  useEffect(() => {
    const host = hostRef.current
    if (host === null) return undefined
    const root = host.shadowRoot ?? host.attachShadow({ mode: "open" })
    if (html === null) {
      root.innerHTML = ""
      return undefined
    }
    root.innerHTML = html
    const handleClick = (event: Event): void => {
      const target = event.target as Element | null
      const anchor = target?.closest?.("a.evidence-link") as HTMLAnchorElement | null
      if (anchor === null) return
      const href = anchor.getAttribute("href")
      if (href === null || href.length === 0) return
      event.preventDefault()
      onOpenEvidencePath?.(href)
    }
    root.addEventListener("click", handleClick)
    return () => root.removeEventListener("click", handleClick)
  }, [html, onOpenEvidencePath, loading, error])

  if (loading) {
    return (
      <div data-testid="report-content-loading">
        <LoadingCard label="正在加载报告内容…" />
      </div>
    )
  }
  if (error !== null) {
    return (
      <div data-testid="report-content-error">
        <ErrorState error={error} title="报告内容加载失败" onRetry={onRetry} />
      </div>
    )
  }
  if (html === null) {
    return (
      <div data-testid="report-content-empty">
        <EmptyState
          title="报告内容不可用"
          description="选择一个已发布且渲染完成的修订即可查看报告全文。"
          className="border-none bg-transparent"
        />
      </div>
    )
  }
  // H-G4 #33: a generic (non-landmark) container. The verbatim report body
  // already carries its own `<main>`; wrapping it in a named `<section>`
  // (role=region landmark) would nest the report's main landmark inside an
  // extra region. A plain div keeps the shadow isolation and styles without
  // adding a landmark.
  return (
    <div
      ref={hostRef}
      data-testid="report-content"
      className="overflow-auto rounded-lg border border-border bg-white p-4 text-foreground"
    />
  )
}

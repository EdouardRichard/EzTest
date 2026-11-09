"use client"

import { Download, FileText, X } from "lucide-react"
import Link from "next/link"
import { useEffect, useMemo, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
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
  /**
   * Other evidence of the same case run (X-03). Used to pair a same-step,
   * same-kind before/after comparison; the viewer never pairs across steps.
   */
  candidates?: Evidence[]
}

/** Kinds whose bytes are an image and can be previewed as a thumbnail. */
const IMAGE_EVIDENCE_KINDS = new Set<Evidence["kind"]>([
  "screenshot",
  "snapshot",
  "visual_actual",
  "visual_diff",
])

/**
 * X-05: a blob URL may still be referenced by an `<img>` that is in the middle
 * of painting (or by a just-opened tab). Revoking it synchronously can surface
 * `ERR_FILE_NOT_FOUND`, so defer the revoke by one short tick.
 */
export const REVOKE_DELAY_MS = 1000

export function revokeSoon(url: string, delayMs: number = REVOKE_DELAY_MS): void {
  setTimeout(() => URL.revokeObjectURL(url), delayMs)
}

/* ------------------------------ HTTP evidence ------------------------------ */

export type HttpHeaderValue = string | string[]
export type HttpHeaders = Record<string, HttpHeaderValue>

export type HttpRequestRecord = {
  method: string
  url: string
  headers: HttpHeaders
  body: string
}

export type HttpResponseRecord = {
  status: number
  headers: HttpHeaders
  body: string
  body_truncated: boolean
}

export type HttpEvidenceRecord = {
  request: HttpRequestRecord
  response: HttpResponseRecord
}

function stringHeaderValue(item: unknown): string | null {
  if (typeof item === "string") return item
  if (typeof item === "number" || typeof item === "boolean") return String(item)
  return null
}

/**
 * Normalizes persisted headers to `string | string[]`. The backend persists
 * request headers as `map[string]string` but response headers as
 * `map[string][]string` (`execution/application/tools.go`), so an array value
 * is the normal production shape and must not be dropped.
 */
function stringHeaders(value: unknown): HttpHeaders {
  const headers: HttpHeaders = {}
  if (value === null || typeof value !== "object" || Array.isArray(value)) return headers
  for (const [name, item] of Object.entries(value as Record<string, unknown>)) {
    if (Array.isArray(item)) {
      const values = item.map(stringHeaderValue).filter((entry): entry is string => entry !== null)
      if (values.length > 0) headers[name] = values
    } else {
      const single = stringHeaderValue(item)
      if (single !== null) headers[name] = single
    }
  }
  return headers
}

/**
 * Parses the persisted `http` evidence payload written by the backend
 * (`execution/application/tools.go`): `{request:{method,url,headers,body},
 * response:{status,headers,body,body_truncated}}`. Returns `null` for anything
 * that is not that shape so the viewer can fall back to a plain text pane.
 */
export function parseHttpEvidence(text: string): HttpEvidenceRecord | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return null
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return null
  const record = parsed as Record<string, unknown>
  const request = record.request
  const response = record.response
  if (request === null || typeof request !== "object" || Array.isArray(request)) return null
  if (response === null || typeof response !== "object" || Array.isArray(response)) return null

  const requestRecord = request as Record<string, unknown>
  const responseRecord = response as Record<string, unknown>
  const method = typeof requestRecord.method === "string" ? requestRecord.method : ""
  const url = typeof requestRecord.url === "string" ? requestRecord.url : ""
  if (method === "" || url === "") return null

  const status = Number(responseRecord.status)
  if (!Number.isFinite(status)) return null

  return {
    request: {
      method,
      url,
      headers: stringHeaders(requestRecord.headers),
      body: typeof requestRecord.body === "string" ? requestRecord.body : "",
    },
    response: {
      status,
      headers: stringHeaders(responseRecord.headers),
      body: typeof responseRecord.body === "string" ? responseRecord.body : "",
      body_truncated: responseRecord.body_truncated === true,
    },
  }
}

/** Single-quotes a shell argument, escaping embedded single quotes. */
function shellQuote(value: string): string {
  return `'${value.replace(/'/g, "'\\''")}'`
}

/** Renders a header value; repeated (array) headers join with `, `. */
function headerValueText(value: HttpHeaderValue): string {
  return Array.isArray(value) ? value.join(", ") : value
}

/**
 * Builds an exact `curl` for the persisted request: method, URL, every header
 * in payload order and the verbatim body. Exported so the copy affordance can
 * be asserted without a clipboard.
 */
export function buildCurl(request: HttpRequestRecord): string {
  const parts = [`curl -X ${shellQuote(request.method)} ${shellQuote(request.url)}`]
  for (const [name, value] of Object.entries(request.headers)) {
    parts.push(`-H ${shellQuote(`${name}: ${headerValueText(value)}`)}`)
  }
  if (request.body.length > 0) {
    parts.push(`--data-raw ${shellQuote(request.body)}`)
  }
  return parts.join(" ")
}

/* --------------------------------- JSON diff -------------------------------- */

export type DiffLine = {
  type: "same" | "add" | "remove"
  text: string
}

/**
 * Above this before×after product the quadratic LCS matrix is not worth it for
 * an evidence dialog; fall back to a full remove/add block.
 */
const DIFF_LCS_LIMIT = 1_500_000

/** Line-level LCS diff (no dependency), used for structured JSON only. */
export function diffLines(before: string, after: string): DiffLine[] {
  const beforeLines = before.split("\n")
  const afterLines = after.split("\n")
  if (beforeLines.length * afterLines.length > DIFF_LCS_LIMIT) {
    return [
      ...beforeLines.map((text) => ({ type: "remove" as const, text })),
      ...afterLines.map((text) => ({ type: "add" as const, text })),
    ]
  }

  const rows = beforeLines.length
  const columns = afterLines.length
  const matrix: number[][] = Array.from({ length: rows + 1 }, () =>
    new Array<number>(columns + 1).fill(0),
  )
  for (let row = rows - 1; row >= 0; row -= 1) {
    for (let column = columns - 1; column >= 0; column -= 1) {
      matrix[row][column] =
        beforeLines[row] === afterLines[column]
          ? matrix[row + 1][column + 1] + 1
          : Math.max(matrix[row + 1][column], matrix[row][column + 1])
    }
  }

  const lines: DiffLine[] = []
  let beforeIndex = 0
  let afterIndex = 0
  while (beforeIndex < rows && afterIndex < columns) {
    if (beforeLines[beforeIndex] === afterLines[afterIndex]) {
      lines.push({ type: "same", text: beforeLines[beforeIndex] })
      beforeIndex += 1
      afterIndex += 1
    } else if (matrix[beforeIndex + 1][afterIndex] >= matrix[beforeIndex][afterIndex + 1]) {
      lines.push({ type: "remove", text: beforeLines[beforeIndex] })
      beforeIndex += 1
    } else {
      lines.push({ type: "add", text: afterLines[afterIndex] })
      afterIndex += 1
    }
  }
  while (beforeIndex < rows) {
    lines.push({ type: "remove", text: beforeLines[beforeIndex] })
    beforeIndex += 1
  }
  while (afterIndex < columns) {
    lines.push({ type: "add", text: afterLines[afterIndex] })
    afterIndex += 1
  }
  return lines
}

/** Pretty-prints a body only when it is a structured JSON object/array. */
function structuredJsonText(value: string): string | null {
  try {
    const parsed: unknown = JSON.parse(value)
    if (parsed === null || typeof parsed !== "object") return null
    return JSON.stringify(parsed, null, 2)
  } catch {
    return null
  }
}

/* -------------------------------- comparison -------------------------------- */

export type EvidenceComparison = {
  before: Evidence
  after: Evidence
}

function createdMs(evidence: Evidence): number {
  const parsed = Date.parse(evidence.created_at)
  return Number.isNaN(parsed) ? 0 : parsed
}

/** Numeric evidence id for tie-breaking; null for a non-numeric id. */
function evidenceIDValue(id: string): bigint | null {
  try {
    return BigInt(id)
  } catch {
    return null
  }
}

/**
 * Recency order for before/after: the RFC3339 instant first, then the numeric
 * evidence id (higher = newer) because two captures of one step can land in the
 * same second. Returns -1 when `a` is older, 1 when it is newer, 0 when the two
 * rows cannot be ordered.
 */
function compareRecency(a: Evidence, b: Evidence): number {
  const aTime = createdMs(a)
  const bTime = createdMs(b)
  if (aTime !== bTime) return aTime < bTime ? -1 : 1
  const aID = evidenceIDValue(a.id)
  const bID = evidenceIDValue(b.id)
  if (aID === null || bID === null || aID === bID) return 0
  return aID < bID ? -1 : 1
}

/**
 * Pairs a screenshot with another evidence row from the same case run, step
 * and kind (X-03 "前后对比"). The nearest `created_at` wins; the older row is
 * the "before" side (same-second captures tie-break by numeric id). Never pairs
 * across case runs, steps or kinds.
 */
export function findComparisonPair(
  evidence: Evidence,
  candidates: Evidence[],
): EvidenceComparison | null {
  const step = evidence.step_seq ?? null
  const matches = candidates.filter(
    (candidate) =>
      candidate.id !== evidence.id &&
      candidate.case_run_id === evidence.case_run_id &&
      candidate.kind === evidence.kind &&
      (candidate.step_seq ?? null) === step,
  )
  if (matches.length === 0) return null

  const evidenceTime = createdMs(evidence)
  let nearest = matches[0]
  for (const candidate of matches.slice(1)) {
    const candidateDistance = Math.abs(createdMs(candidate) - evidenceTime)
    const nearestDistance = Math.abs(createdMs(nearest) - evidenceTime)
    if (
      candidateDistance < nearestDistance ||
      (candidateDistance === nearestDistance && compareRecency(candidate, nearest) > 0)
    ) {
      nearest = candidate
    }
  }
  return compareRecency(nearest, evidence) < 0
    ? { before: nearest, after: evidence }
    : { before: evidence, after: nearest }
}

/* --------------------------------- download -------------------------------- */

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
  // X-05: the browser may not have consumed the download yet.
  revokeSoon(url)
}

/* -------------------------------- thumbnail -------------------------------- */

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
      if (objectUrl !== null) revokeSoon(objectUrl)
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

/* ---------------------------------- viewer --------------------------------- */

function HeaderList({ headers, testId }: { headers: HttpHeaders; testId: string }) {
  const entries = Object.entries(headers)
  if (entries.length === 0) {
    return (
      <p data-testid={testId} className="text-[10px] text-muted-foreground">
        无头信息
      </p>
    )
  }
  return (
    <dl data-testid={testId} className="space-y-0.5 font-mono text-[10px] text-muted-foreground">
      {entries.map(([name, value]) => (
        <div key={name} className="break-all">
          <dt className="inline font-semibold text-foreground">{name}</dt>
          <dd className="inline">: {headerValueText(value)}</dd>
        </div>
      ))}
    </dl>
  )
}

/**
 * Evidence dialog (spec §12.1): the bytes always come from the authenticated
 * `apiDownload` path, screenshots render through a delayed-revoke object URL,
 * HTTP evidence renders request/response panes with copy-curl and a JSON LCS
 * diff, and same-step screenshots offer a before/after comparison (X-03).
 * Evidence is immutable and read-only.
 */
export function EvidenceViewer({
  versionId,
  evidence,
  open,
  onClose,
  candidates = [],
}: EvidenceViewerProps) {
  const [loading, setLoading] = useState(false)
  const [downloading, setDownloading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [previewUrl, setPreviewUrl] = useState<string | null>(null)
  const [text, setText] = useState<string | null>(null)
  const [comparisonUrl, setComparisonUrl] = useState<string | null>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [lightboxOpen, setLightboxOpen] = useState(false)
  const [curlCopied, setCurlCopied] = useState(false)
  const [copyError, setCopyError] = useState<unknown>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken/evidence trigger an explicit refetch
  useEffect(() => {
    if (!open || evidence === null) return undefined
    let cancelled = false
    let objectUrl: string | null = null

    setLoading(true)
    setError(null)
    setPreviewUrl(null)
    setText(null)
    setLightboxOpen(false)
    setCurlCopied(false)
    setCopyError(null)

    downloadEvidence(versionId, evidence.id)
      .then(async (result) => {
        if (cancelled) return
        if (IMAGE_EVIDENCE_KINDS.has(evidence.kind)) {
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
      if (objectUrl !== null) revokeSoon(objectUrl)
    }
  }, [open, evidence, versionId, reloadToken])

  const comparison = useMemo(() => {
    if (evidence === null || !IMAGE_EVIDENCE_KINDS.has(evidence.kind)) return null
    return findComparisonPair(evidence, candidates)
  }, [evidence, candidates])

  const comparisonTarget =
    comparison === null
      ? null
      : comparison.before.id === evidence?.id
        ? comparison.after
        : comparison.before

  // biome-ignore lint/correctness/useExhaustiveDependencies: comparisonTarget?.id is the request key
  useEffect(() => {
    if (!open || evidence === null || comparisonTarget === null) return undefined
    let cancelled = false
    let objectUrl: string | null = null
    setComparisonUrl(null)
    downloadEvidence(versionId, comparisonTarget.id)
      .then((result) => {
        if (cancelled) return
        objectUrl = URL.createObjectURL(result.blob)
        setComparisonUrl(objectUrl)
      })
      .catch(() => {
        if (!cancelled) setComparisonUrl(null)
      })
    return () => {
      cancelled = true
      if (objectUrl !== null) revokeSoon(objectUrl)
    }
  }, [open, evidence, comparisonTarget?.id, versionId])

  const httpRecord = useMemo(() => (text === null ? null : parseHttpEvidence(text)), [text])

  const httpDiff = useMemo(() => {
    if (httpRecord === null) return null
    const before = structuredJsonText(httpRecord.request.body)
    const after = structuredJsonText(httpRecord.response.body)
    if (before === null || after === null) return null
    return diffLines(before, after)
  }, [httpRecord])

  if (evidence === null) return null

  function handleDialogClose(): void {
    if (lightboxOpen) {
      setLightboxOpen(false)
      return
    }
    onClose()
  }

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

  async function copyCurl(record: HttpEvidenceRecord): Promise<void> {
    try {
      await navigator.clipboard.writeText(buildCurl(record.request))
      setCurlCopied(true)
      setCopyError(null)
    } catch (cause) {
      setCurlCopied(false)
      setCopyError(cause)
    }
  }

  const altText = evidence.filename.length > 0 ? evidence.filename : "证据截图"

  return (
    <>
      <Dialog
        open={open}
        onClose={handleDialogClose}
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
            <Button variant="secondary" size="sm" onClick={handleDialogClose}>
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
              <div className="relative">
                {/* biome-ignore lint/performance/noImgElement: evidence bytes are auth-gated blob URLs that next/image cannot optimize */}
                <img
                  data-testid="evidence-image"
                  src={previewUrl}
                  alt={altText}
                  className="max-h-[420px] w-full rounded-lg border border-border object-contain"
                />
                <Button
                  variant="secondary"
                  size="sm"
                  aria-label="放大查看"
                  className="absolute right-2 top-2"
                  onClick={() => setLightboxOpen(true)}
                >
                  放大
                </Button>
              </div>
            ) : null}

            {httpRecord !== null ? (
              <div className="space-y-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-[11px] text-muted-foreground">HTTP 请求 / 响应</span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => {
                      void copyCurl(httpRecord)
                    }}
                  >
                    复制 curl
                  </Button>
                </div>
                {curlCopied ? (
                  <p role="status" className="text-[11px] text-success">
                    已复制 curl
                  </p>
                ) : null}
                {copyError !== null ? (
                  <InlineError error={copyError} message="复制 curl 失败" />
                ) : null}

                <div className="grid gap-2 md:grid-cols-2">
                  <section
                    data-testid="evidence-http-request"
                    aria-label="HTTP 请求"
                    className="min-w-0 rounded-lg border border-border bg-muted/30 p-2"
                  >
                    <h3 className="text-[11px] font-semibold text-foreground">请求</h3>
                    <p className="mt-1 break-all font-mono text-[11px] text-foreground">
                      <span className="font-semibold">{httpRecord.request.method}</span>{" "}
                      {httpRecord.request.url}
                    </p>
                    <HeaderList
                      headers={httpRecord.request.headers}
                      testId="evidence-http-request-headers"
                    />
                    {httpRecord.request.body.length > 0 ? (
                      <pre className="mt-1 max-h-64 overflow-auto break-all whitespace-pre-wrap rounded border border-border bg-card p-2 text-[10px] leading-relaxed">
                        {httpRecord.request.body}
                      </pre>
                    ) : null}
                  </section>

                  <section
                    data-testid="evidence-http-response"
                    aria-label="HTTP 响应"
                    className="min-w-0 rounded-lg border border-border bg-muted/30 p-2"
                  >
                    <h3 className="text-[11px] font-semibold text-foreground">响应</h3>
                    <p className="mt-1 font-mono text-[11px] text-foreground">
                      HTTP {httpRecord.response.status}
                      {httpRecord.response.body_truncated ? " · 响应体已截断" : ""}
                    </p>
                    <HeaderList
                      headers={httpRecord.response.headers}
                      testId="evidence-http-response-headers"
                    />
                    {httpRecord.response.body.length > 0 ? (
                      <pre className="mt-1 max-h-64 overflow-auto break-all whitespace-pre-wrap rounded border border-border bg-card p-2 text-[10px] leading-relaxed">
                        {httpRecord.response.body}
                      </pre>
                    ) : null}
                  </section>
                </div>

                {httpDiff !== null ? (
                  <section
                    data-testid="evidence-http-diff"
                    aria-label="HTTP 请求/响应 JSON 差异"
                    className="max-h-64 overflow-auto rounded-lg border border-border bg-card p-2 font-mono text-[10px] leading-relaxed"
                  >
                    {httpDiff.map((line, index) => (
                      <div
                        // biome-ignore lint/suspicious/noArrayIndexKey: diff lines are positional
                        key={index}
                        data-diff-type={line.type}
                        className={
                          line.type === "add"
                            ? "whitespace-pre-wrap break-all text-success"
                            : line.type === "remove"
                              ? "whitespace-pre-wrap break-all text-destructive"
                              : "whitespace-pre-wrap break-all text-muted-foreground"
                        }
                      >
                        {`${
                          line.type === "add" ? "+ " : line.type === "remove" ? "- " : "  "
                        }${line.text}`}
                      </div>
                    ))}
                  </section>
                ) : null}
              </div>
            ) : text !== null ? (
              <pre
                data-testid="evidence-text"
                className="max-h-[420px] overflow-auto rounded-lg border border-border bg-muted/40 p-3 text-[11px] leading-relaxed"
              >
                {text}
              </pre>
            ) : null}

            {comparison !== null && previewUrl !== null && comparisonUrl !== null ? (
              <section
                data-testid="evidence-compare"
                aria-label="前后对比"
                className="space-y-2 rounded-lg border border-border p-2"
              >
                <h3 className="text-[11px] font-semibold text-foreground">前后对比</h3>
                <div className="grid gap-2 sm:grid-cols-2">
                  <figure data-testid="evidence-compare-before" className="min-w-0 space-y-1">
                    {/* biome-ignore lint/performance/noImgElement: evidence bytes are auth-gated blob URLs that next/image cannot optimize */}
                    <img
                      src={comparison.before.id === evidence.id ? previewUrl : comparisonUrl}
                      alt={
                        comparison.before.filename.length > 0
                          ? comparison.before.filename
                          : "之前截图"
                      }
                      className="max-h-[260px] w-full rounded-md border border-border object-contain"
                    />
                    <figcaption className="text-[10px] text-muted-foreground">
                      <span>之前</span> ·{" "}
                      {comparison.before.filename.length > 0
                        ? comparison.before.filename
                        : `#${comparison.before.id}`}
                    </figcaption>
                  </figure>
                  <figure data-testid="evidence-compare-after" className="min-w-0 space-y-1">
                    {/* biome-ignore lint/performance/noImgElement: evidence bytes are auth-gated blob URLs that next/image cannot optimize */}
                    <img
                      src={comparison.after.id === evidence.id ? previewUrl : comparisonUrl}
                      alt={
                        comparison.after.filename.length > 0
                          ? comparison.after.filename
                          : "之后截图"
                      }
                      className="max-h-[260px] w-full rounded-md border border-border object-contain"
                    />
                    <figcaption className="text-[10px] text-muted-foreground">
                      <span>之后</span> ·{" "}
                      {comparison.after.filename.length > 0
                        ? comparison.after.filename
                        : `#${comparison.after.id}`}
                    </figcaption>
                  </figure>
                </div>
              </section>
            ) : null}

            <dl className="space-y-1 text-[11px] text-muted-foreground">
              <div>sha256: {evidence.sha256}</div>
              <div>大小 {formatBytes(evidence.size_bytes)}</div>
              <div>创建于 {formatDateTime(evidence.created_at)}</div>
            </dl>
          </div>
        ) : null}
      </Dialog>

      {open && lightboxOpen && previewUrl !== null ? (
        <div
          data-testid="evidence-lightbox"
          role="dialog"
          aria-modal="true"
          aria-label="证据大图"
          className="fixed inset-0 z-[60] flex items-center justify-center bg-slate-950/90 p-4"
        >
          <button
            type="button"
            aria-label="关闭大图"
            onClick={() => setLightboxOpen(false)}
            className="absolute right-4 top-4 rounded-md p-1 text-slate-200 transition-colors hover:bg-slate-800 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="size-5" aria-hidden="true" />
          </button>
          {/* biome-ignore lint/performance/noImgElement: evidence bytes are auth-gated blob URLs that next/image cannot optimize */}
          <img
            src={previewUrl}
            alt={altText}
            className="max-h-full max-w-full rounded-lg object-contain"
          />
        </div>
      ) : null}
    </>
  )
}

"use client"

import { Download, Loader2, X } from "lucide-react"
import { type KeyboardEvent as ReactKeyboardEvent, useEffect, useId, useRef, useState } from "react"

import { apiStreamFetch } from "@/lib/api-client"

import { getEvidenceMeta, getEvidenceUrl } from "../api"
import { describeExecutionError } from "../execution-errors"
import type { EvidenceMeta, EvidenceView } from "../types"

/** Human labels of the frozen evidence kinds (spec §5.7). */
export const EVIDENCE_KIND_LABELS: Record<string, string> = {
  screenshot: "截图",
  http_exchange: "HTTP 交换",
  console_log: "Console 日志",
  network_log: "网络日志",
  download: "下载文件",
}

/** Label for any evidence kind; unknown values stay visible instead of blank. */
export function evidenceKindLabel(kind: string): string {
  if (kind === "") return "证据"
  return EVIDENCE_KIND_LABELS[kind] ?? kind
}

type HttpHeaderMap = Record<string, string[]>

/** Redacted request half of an `http_exchange` evidence artifact (spec §5.7). */
export type HttpExchangeRequest = {
  method?: string
  url?: string
  headers?: HttpHeaderMap
  body?: string
  body_base64?: string
}

/** Redacted response half of an `http_exchange` evidence artifact. */
export type HttpExchangeResponse = {
  status?: number
  headers?: HttpHeaderMap
  body?: string
  body_base64?: string
  truncated?: boolean
}

/** One console/network entry persisted by the executor tail capture. */
export type LogEvidenceEntry = {
  type?: string
  text?: string
  ts?: number
  method?: string
  url?: string
  status?: number | null
  duration_ms?: number
  mime?: string | null
  error?: string
}

type ViewerState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; meta: EvidenceMeta; text: string; objectUrl: string }

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/** Kinds whose bytes are displayed/downloaded rather than parsed as JSON. */
function isBinaryKind(kind: string): boolean {
  return kind === "screenshot" || kind === "download"
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text) as unknown
  } catch {
    return null
  }
}

/** Splits one evidence document into its request/response halves defensively. */
export function parseHttpExchange(text: string): {
  request: HttpExchangeRequest
  response: HttpExchangeResponse
} {
  const payload = parseJson(text)
  const record = isRecord(payload) ? payload : {}
  return {
    request: isRecord(record.request) ? (record.request as HttpExchangeRequest) : {},
    response: isRecord(record.response) ? (record.response as HttpExchangeResponse) : {},
  }
}

/** Reads the `items` tail of a console/network evidence document. */
export function parseLogEntries(text: string): LogEvidenceEntry[] {
  const payload = parseJson(text)
  if (!isRecord(payload) || !Array.isArray(payload.items)) return []
  return payload.items.filter(isRecord).map((item) => item as unknown as LogEvidenceEntry)
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, "'\\''")}'`
}

/**
 * Builds a copyable curl command from the redacted request half. Sensitive
 * header values were masked by the backend before the artifact was stored.
 */
export function buildCurl(request: HttpExchangeRequest): string {
  if (typeof request.url !== "string" || request.url === "") return ""
  const parts = ["curl", "-X", request.method ?? "GET", shellQuote(request.url)]
  for (const [name, values] of Object.entries(request.headers ?? {})) {
    for (const value of values) parts.push("-H", shellQuote(`${name}: ${value}`))
  }
  if (typeof request.body === "string" && request.body !== "") {
    parts.push("--data-raw", shellQuote(request.body))
  }
  return parts.join(" ")
}

function formatHeaders(headers: HttpHeaderMap | undefined): string {
  if (headers === undefined) return ""
  return Object.entries(headers)
    .map(([name, values]) => `${name}: ${values.join(", ")}`)
    .join("\n")
}

function bodyText(body: string | undefined, bodyBase64: string | undefined): string {
  if (typeof body === "string" && body !== "") return body
  if (typeof bodyBase64 === "string" && bodyBase64 !== "") return `[base64] ${bodyBase64}`
  return "（无内容）"
}

/** Human-readable artifact size; never used to expose a filesystem path. */
export function formatBytes(bytes: number | undefined): string {
  if (typeof bytes !== "number" || !Number.isFinite(bytes) || bytes < 0) return "—"
  if (bytes < 1024) return `${bytes} B`
  const kilobytes = bytes / 1024
  if (kilobytes < 1024) return `${kilobytes.toFixed(1)} KB`
  return `${(kilobytes / 1024).toFixed(1)} MB`
}

/** Download filename derived from the artifact id and MIME, not from a path. */
export function evidenceFilename(meta: Pick<EvidenceMeta, "id" | "mime">): string {
  const subtype = (meta.mime.split(";")[0] ?? "").split("/")[1] ?? ""
  const extension = subtype.replace(/[^a-z0-9]+/gi, "") || "bin"
  return `evidence-${meta.id}.${extension}`
}

export type EvidenceViewerProps = {
  /** Version that owns the evidence; the bytes endpoint is version-scoped. */
  versionId: string
  evidenceId: string
  /** Summary already known by the case run, used until the meta read lands. */
  evidence?: EvidenceView | null
  onClose: () => void
}

/**
 * Evidence lightbox of the results tab (spec §9.3). The bytes endpoint requires
 * a bearer, so every kind (not only screenshots) is fetched through the shared
 * API client and rendered from an object URL; the filesystem path is never
 * displayed. Rendering is per kind: screenshot image, HTTP request/response
 * panes with a copyable curl, searchable console tail and a download link.
 */
export function EvidenceViewer({
  versionId,
  evidenceId,
  evidence = null,
  onClose,
}: EvidenceViewerProps) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const previousFocusRef = useRef<Element | null>(null)
  const [state, setState] = useState<ViewerState>({ status: "loading" })
  const [reloadKey, setReloadKey] = useState(0)
  const [query, setQuery] = useState("")
  const [copied, setCopied] = useState(false)

  // One abortable read per open/reload: metadata first (kind/mime), then bytes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey restarts the read
  useEffect(() => {
    const controller = new AbortController()
    let objectUrl = ""
    setState({ status: "loading" })
    setQuery("")
    setCopied(false)

    async function load(): Promise<void> {
      const meta = await getEvidenceMeta(versionId, evidenceId, { signal: controller.signal })
      if (controller.signal.aborted) return

      const response = await apiStreamFetch(getEvidenceUrl(versionId, evidenceId), {
        signal: controller.signal,
      })
      if (controller.signal.aborted) return

      if (isBinaryKind(meta.kind)) {
        const blob = await response.blob()
        const url = URL.createObjectURL(blob)
        objectUrl = url
        if (controller.signal.aborted) {
          URL.revokeObjectURL(url)
          return
        }
        setState({ status: "ready", meta, text: "", objectUrl: url })
        return
      }

      const text = await response.text()
      if (controller.signal.aborted) return
      setState({ status: "ready", meta, text, objectUrl: "" })
    }

    load().catch((caught: unknown) => {
      if (controller.signal.aborted) return
      setState({ status: "error", message: describeExecutionError(caught) })
    })

    return () => {
      controller.abort()
      if (objectUrl !== "") URL.revokeObjectURL(objectUrl)
    }
  }, [versionId, evidenceId, reloadKey])

  // Save the opener, focus the dialog, restore focus when it unmounts.
  useEffect(() => {
    previousFocusRef.current = document.activeElement
    dialogRef.current?.focus()
    return () => {
      const previous = previousFocusRef.current
      if (previous instanceof HTMLElement) previous.focus()
    }
  }, [])

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>): void => {
    if (event.key === "Escape") {
      event.stopPropagation()
      onClose()
      return
    }
    if (event.key !== "Tab") return

    const dialog = dialogRef.current
    if (dialog === null) return
    const focusable = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
    if (focusable.length === 0) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }

  const ready = state.status === "ready" ? state : null
  const kind = ready?.meta.kind ?? evidence?.kind ?? ""
  const mime = ready?.meta.mime ?? evidence?.mime ?? ""
  const size = ready?.meta.size_bytes ?? evidence?.size_bytes
  const sha256 = ready?.meta.sha256 ?? evidence?.sha256 ?? ""
  const exchange = ready !== null && kind === "http_exchange" ? parseHttpExchange(ready.text) : null
  const curl = exchange === null ? "" : buildCurl(exchange.request)

  async function handleCopyCurl(): Promise<void> {
    if (curl === "") return
    const clipboard = navigator.clipboard
    if (clipboard === undefined) return
    try {
      await clipboard.writeText(curl)
      setCopied(true)
    } catch {
      setCopied(false)
    }
  }

  function renderReady(content: Extract<ViewerState, { status: "ready" }>) {
    if (content.meta.kind === "screenshot") {
      return (
        <figure className="space-y-2">
          {/* biome-ignore lint/performance/noImgElement: evidence bytes come from an object URL, not a static asset */}
          <img
            src={content.objectUrl}
            alt={`截图证据 ${content.meta.id}`}
            data-testid="evidence-screenshot"
            className="mx-auto max-h-[68vh] w-auto rounded-xl border border-border bg-muted/30 object-contain"
          />
          <figcaption className="text-center text-[11px] text-muted-foreground">
            截图证据 · 点击遮罩或按 Esc 关闭
          </figcaption>
        </figure>
      )
    }

    if (content.meta.kind === "download") {
      return (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            该证据为下载文件，可通过下方链接保存（文件名由证据 ID 与 MIME 推导）。
          </p>
          <a
            href={content.objectUrl}
            download={evidenceFilename(content.meta)}
            data-testid="evidence-download"
            className="inline-flex items-center gap-2 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Download className="h-4 w-4" aria-hidden="true" />
            下载 {evidenceFilename(content.meta)}
          </a>
        </div>
      )
    }

    if (content.meta.kind === "http_exchange" && exchange !== null) {
      return (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm text-muted-foreground">请求与响应（敏感头已脱敏）</p>
            <button
              type="button"
              onClick={() => void handleCopyCurl()}
              disabled={curl === ""}
              className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
            >
              {copied ? "已复制" : "复制 curl"}
            </button>
          </div>

          <div className="grid gap-3 lg:grid-cols-2">
            <section
              data-testid="evidence-http-request"
              aria-label="HTTP 请求"
              className="min-w-0 rounded-xl border border-border bg-muted/30 p-3"
            >
              <h3 className="mb-2 text-xs font-semibold text-muted-foreground">请求</h3>
              <pre className="overflow-x-auto whitespace-pre-wrap break-words font-mono text-xs leading-relaxed">
                <code>
                  {`${exchange.request.method ?? "GET"} ${exchange.request.url ?? ""}\n\n${formatHeaders(
                    exchange.request.headers,
                  )}\n\n${bodyText(exchange.request.body, exchange.request.body_base64)}`}
                </code>
              </pre>
            </section>

            <section
              data-testid="evidence-http-response"
              aria-label="HTTP 响应"
              className="min-w-0 rounded-xl border border-border bg-muted/30 p-3"
            >
              <h3 className="mb-2 text-xs font-semibold text-muted-foreground">响应</h3>
              {exchange.response.truncated ? (
                <p className="mb-2 rounded-lg border border-amber-500/25 bg-amber-500/10 px-2 py-1 text-[11px] text-amber-700 dark:text-amber-300">
                  响应体已截断。
                </p>
              ) : null}
              <pre className="overflow-x-auto whitespace-pre-wrap break-words font-mono text-xs leading-relaxed">
                <code>
                  {`${exchange.response.status ?? "—"}\n\n${formatHeaders(
                    exchange.response.headers,
                  )}\n\n${bodyText(exchange.response.body, exchange.response.body_base64)}`}
                </code>
              </pre>
            </section>
          </div>

          <pre
            data-testid="evidence-curl"
            className="overflow-x-auto rounded-xl border border-border bg-muted/30 p-3 font-mono text-xs leading-relaxed"
          >
            <code>{curl === "" ? "无法生成 curl：证据缺少请求 URL。" : curl}</code>
          </pre>
        </div>
      )
    }

    if (content.meta.kind === "console_log") {
      const entries = parseLogEntries(content.text)
      const needle = query.trim().toLowerCase()
      const filtered =
        needle === ""
          ? entries
          : entries.filter((entry) =>
              `${entry.type ?? ""} ${entry.text ?? ""}`.toLowerCase().includes(needle),
            )

      return (
        <div className="space-y-3">
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>搜索日志</span>
            <input
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label="搜索 console 日志"
              placeholder="输入关键字过滤 console 日志"
              data-testid="evidence-console-search"
              className="min-w-0 flex-1 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </label>

          {entries.length === 0 ? (
            <p className="text-sm text-muted-foreground">该日志证据没有记录内容。</p>
          ) : filtered.length === 0 ? (
            <p className="text-sm text-muted-foreground">没有匹配的日志。</p>
          ) : (
            <ul className="space-y-1 font-mono text-xs">
              {filtered.map((entry, index) => (
                <li
                  key={`${entry.ts ?? "log"}:${entry.type ?? ""}:${entry.text ?? ""}`}
                  data-testid={`evidence-console-entry-${index}`}
                  className="rounded-lg border border-border bg-muted/30 px-3 py-1.5"
                >
                  <span className="mr-2 text-muted-foreground">{entry.type ?? "log"}</span>
                  <span className="whitespace-pre-wrap break-words">{entry.text ?? ""}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )
    }

    if (content.meta.kind === "network_log") {
      const entries = parseLogEntries(content.text)
      if (entries.length === 0) {
        return <p className="text-sm text-muted-foreground">该网络日志证据没有记录内容。</p>
      }
      return (
        <ul className="space-y-1 font-mono text-xs">
          {entries.map((entry, index) => (
            <li
              key={`${entry.ts ?? "network"}:${entry.method ?? ""}:${entry.url ?? ""}:${entry.status ?? ""}`}
              data-testid={`evidence-network-entry-${index}`}
              className="rounded-lg border border-border bg-muted/30 px-3 py-1.5"
            >
              <span className="mr-2 text-foreground">{entry.method ?? "GET"}</span>
              <span className="mr-2 text-muted-foreground">{entry.status ?? "—"}</span>
              <span className="break-all">{entry.url ?? ""}</span>
              {typeof entry.duration_ms === "number" ? (
                <span className="ml-2 text-muted-foreground">{entry.duration_ms}ms</span>
              ) : null}
              {entry.error === undefined ? null : (
                <span className="ml-2 text-red-700 dark:text-red-300">{entry.error}</span>
              )}
            </li>
          ))}
        </ul>
      )
    }

    return (
      <div className="space-y-3">
        <p className="text-sm text-muted-foreground">该类型证据暂不支持预览。</p>
        {content.text === "" ? null : (
          <pre className="overflow-x-auto rounded-xl border border-border bg-muted/30 p-3 font-mono text-xs leading-relaxed">
            <code>{content.text}</code>
          </pre>
        )}
      </div>
    )
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <button
        type="button"
        tabIndex={-1}
        aria-label="关闭证据查看器遮罩"
        onClick={onClose}
        className="absolute inset-0 bg-black/60"
      />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={handleKeyDown}
        data-testid="evidence-viewer"
        data-evidence-kind={kind === "" ? "unknown" : kind}
        className="relative flex max-h-[88vh] w-full max-w-5xl flex-col rounded-2xl border border-border bg-card shadow-2xl outline-none"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <p className="text-[11px] font-medium text-muted-foreground">证据</p>
            <h2 id={titleId} className="truncate text-sm font-semibold">
              {evidenceKindLabel(kind)}
            </h2>
            <p className="mt-0.5 flex flex-wrap gap-x-2 text-[11px] text-muted-foreground">
              <span>{mime === "" ? "未知类型" : mime}</span>
              <span aria-hidden="true">·</span>
              <span>{formatBytes(size)}</span>
              {sha256 === "" ? null : (
                <>
                  <span aria-hidden="true">·</span>
                  <span title={sha256}>sha256 {sha256.slice(0, 12)}…</span>
                </>
              )}
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭证据查看器"
            className="rounded-lg border border-border p-1.5 text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </header>

        <div className="min-h-0 flex-1 overflow-auto p-4">
          {state.status === "loading" ? (
            <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              正在加载证据…
            </p>
          ) : state.status === "error" ? (
            <div
              role="alert"
              className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-4 text-sm text-destructive"
            >
              <p>加载证据失败：{state.message}</p>
              <button
                type="button"
                onClick={() => setReloadKey((key) => key + 1)}
                className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                重试
              </button>
            </div>
          ) : (
            renderReady(state)
          )}
        </div>
      </div>
    </div>
  )
}

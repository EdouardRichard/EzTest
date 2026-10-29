"use client"

import { Loader2, X } from "lucide-react"
import {
  type KeyboardEvent as ReactKeyboardEvent,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react"

import { getFile } from "./api"
import { describeCodeError, shortCommit } from "./empty"
import type { Evidence, FileWindow } from "./types"

/** Lines of context rendered on each side of an evidence range (spec §9.2). */
export const EVIDENCE_CONTEXT_LINES = 20

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/** `file:start-end` (or just `file`) for the evidence link label (spec §7.3). */
export function formatEvidenceLocation(evidence: Evidence): string {
  if (typeof evidence.startLine !== "number") return evidence.file
  const end = evidence.endLine ?? evidence.startLine
  return end === evidence.startLine
    ? `${evidence.file}:${evidence.startLine}`
    : `${evidence.file}:${evidence.startLine}-${end}`
}

type SnippetWindow = {
  startLine?: number
  endLine?: number
}

/** Requests a small window around the evidence; the backend clamps the range. */
export function evidenceWindow(evidence: Evidence): SnippetWindow {
  if (typeof evidence.startLine !== "number") return { startLine: 1 }
  const end = evidence.endLine ?? evidence.startLine
  return {
    startLine: Math.max(1, evidence.startLine - EVIDENCE_CONTEXT_LINES),
    endLine: end + EVIDENCE_CONTEXT_LINES,
  }
}

type SnippetDrawerProps = {
  /** P2 repository id; a number, never parsed from a DTO string (spec §7.1). */
  repoId: number
  commit: string
  evidence: Evidence
  onClose: () => void
}

/**
 * Read-only code-snippet drawer (spec §9.2 `EvidenceLink`): fetches one line
 * window of the evidence file at the analysis head commit and highlights the
 * evidence lines. It is a modal dialog with ESC close, focus trap and focus
 * restoration, mirroring `DiffDrawer`.
 */
function SnippetDrawer({ repoId, commit, evidence, onClose }: SnippetDrawerProps) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const previousFocusRef = useRef<Element | null>(null)
  const requestRef = useRef(0)

  const [fileWindow, setFileWindow] = useState<FileWindow | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")

  const load = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    const range = evidenceWindow(evidence)
    setLoading(true)
    setError("")
    try {
      const result = await getFile(repoId, {
        commit,
        path: evidence.file,
        startLine: range.startLine,
        endLine: range.endLine,
      })
      if (requestRef.current === requestId) setFileWindow(result)
    } catch (caught) {
      if (requestRef.current === requestId) setError(describeCodeError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [repoId, commit, evidence])

  useEffect(() => {
    void load()
    return () => {
      requestRef.current += 1
    }
  }, [load])

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

  const startLine = evidence.startLine
  const endLine = evidence.endLine ?? evidence.startLine
  const lines =
    fileWindow === null || fileWindow.content === "" ? [] : fileWindow.content.split("\n")

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <button
        type="button"
        tabIndex={-1}
        aria-label="关闭代码片段遮罩"
        onClick={onClose}
        className="absolute inset-0 bg-black/50"
      />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={handleKeyDown}
        className="relative flex max-h-[85vh] w-full max-w-3xl flex-col rounded-2xl border border-border bg-card shadow-2xl outline-none"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <p className="text-[11px] font-medium text-muted-foreground">代码片段</p>
            <h2 id={titleId} className="truncate font-mono text-sm font-semibold">
              {formatEvidenceLocation(evidence)}
            </h2>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              证据位置 · commit <span title={commit}>{shortCommit(commit)}</span>
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭代码片段抽屉"
            className="rounded-lg border border-border p-1.5 text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </header>

        <div className="min-h-0 flex-1 overflow-auto p-4">
          {loading ? (
            <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              正在加载代码片段…
            </p>
          ) : error !== "" ? (
            <div
              role="alert"
              className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-4 text-sm text-destructive"
            >
              <p>加载代码片段失败：{error}</p>
              <button
                type="button"
                onClick={() => void load()}
                className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                重试
              </button>
            </div>
          ) : fileWindow === null || lines.length === 0 ? (
            <p className="text-sm text-muted-foreground">该位置没有可展示的代码内容。</p>
          ) : (
            <div className="space-y-3">
              {fileWindow.truncated ? (
                <p
                  role="status"
                  className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
                >
                  代码片段已截断，仅展示部分内容。
                </p>
              ) : null}
              <div className="overflow-x-auto rounded-xl border border-border bg-muted/30 py-2 font-mono text-xs leading-relaxed">
                {lines.map((text, index) => {
                  const lineNumber = fileWindow.startLine + index
                  const highlighted =
                    typeof startLine === "number" &&
                    lineNumber >= startLine &&
                    typeof endLine === "number" &&
                    lineNumber <= endLine
                  return (
                    <div
                      key={`${fileWindow.startLine}:${lineNumber}`}
                      data-line-number={lineNumber}
                      data-evidence-line={highlighted ? "true" : "false"}
                      className={
                        highlighted
                          ? "flex gap-3 bg-amber-500/15 px-3 text-amber-800 dark:text-amber-200"
                          : "flex gap-3 px-3 text-foreground"
                      }
                    >
                      <span
                        aria-hidden="true"
                        className="w-10 shrink-0 select-none text-right text-muted-foreground"
                      >
                        {lineNumber}
                      </span>
                      <span className="whitespace-pre">{text === "" ? " " : text}</span>
                    </div>
                  )
                })}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

export type EvidenceLinkProps = {
  repoId: number
  commit: string
  evidence: Evidence
}

/**
 * Evidence `file:line` link (spec §9.2). Clicking opens the snippet drawer for
 * the evidence range; anchor-sourced evidence without a line range still opens
 * the file from the first line.
 */
export function EvidenceLink({ repoId, commit, evidence }: EvidenceLinkProps) {
  const [open, setOpen] = useState(false)
  const label = formatEvidenceLocation(evidence)

  return (
    <>
      <button
        type="button"
        aria-label={`查看证据 ${label}`}
        onClick={() => setOpen(true)}
        className="inline-flex max-w-full items-center rounded font-mono text-xs text-sky-700 underline decoration-dotted underline-offset-2 transition-colors hover:text-sky-900 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:text-sky-300 dark:hover:text-sky-100"
      >
        <span className="truncate">{label}</span>
      </button>
      {open ? (
        <SnippetDrawer
          repoId={repoId}
          commit={commit}
          evidence={evidence}
          onClose={() => setOpen(false)}
        />
      ) : null}
    </>
  )
}

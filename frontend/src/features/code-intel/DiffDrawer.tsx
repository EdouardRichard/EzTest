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

import { getFileDiff } from "./api"
import { fileStatusLabel } from "./ChangedFiles"
import { describeCodeError, shortCommit } from "./empty"
import type { ChangedFile, FileDiff } from "./types"

type DiffLineKind = "add" | "del" | "hunk" | "meta" | "context"

const LINE_CLASSES: Record<DiffLineKind, string> = {
  add: "block whitespace-pre bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
  del: "block whitespace-pre bg-red-500/10 text-red-700 dark:text-red-300",
  hunk: "block whitespace-pre bg-sky-500/10 text-sky-700 dark:text-sky-300",
  meta: "block whitespace-pre text-muted-foreground",
  context: "block whitespace-pre text-foreground",
}

/** Classifies one unified-diff line for the +/- colouring (spec §9.2). */
function classifyLine(line: string): DiffLineKind {
  if (line.startsWith("@@")) return "hunk"
  if (line.startsWith("+++") || line.startsWith("---")) return "meta"
  if (line.startsWith("+")) return "add"
  if (line.startsWith("-")) return "del"
  if (
    line.startsWith("diff --git") ||
    line.startsWith("index ") ||
    line.startsWith("new file mode") ||
    line.startsWith("deleted file mode") ||
    line.startsWith("old mode") ||
    line.startsWith("new mode") ||
    line.startsWith("similarity index") ||
    line.startsWith("rename from") ||
    line.startsWith("rename to") ||
    line.startsWith("copy from") ||
    line.startsWith("copy to") ||
    line.startsWith("Binary files")
  ) {
    return "meta"
  }
  return "context"
}

type DiffLine = {
  key: string
  text: string
  kind: DiffLineKind
}

/** Splits a patch into keyed lines; keys use character offsets, not indices. */
function splitPatchLines(patch: string): DiffLine[] {
  const lines: DiffLine[] = []
  let offset = 0
  for (const text of patch.split("\n")) {
    lines.push({ key: `${offset}:${text}`, text, kind: classifyLine(text) })
    offset += text.length + 1
  }
  return lines
}

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

export type DiffDrawerProps = {
  /** P2 repository id; a number, never parsed from the diff DTO (spec §7.1). */
  repoId: number
  baseCommit: string
  headCommit: string
  file: ChangedFile
  onClose: () => void
}

/**
 * Right-hand unified-diff drawer (spec §9.2/§9.4). It fetches the diff on open,
 * renders it in a `<pre>` with +/- line styling, reports binary/truncated files
 * without failing, and behaves as an accessible modal dialog: `role="dialog"`,
 * ESC close, initial focus, Tab cycling and focus restoration to the opener.
 */
export function DiffDrawer({ repoId, baseCommit, headCommit, file, onClose }: DiffDrawerProps) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const previousFocusRef = useRef<Element | null>(null)
  const requestRef = useRef(0)

  const [diff, setDiff] = useState<FileDiff | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")

  const load = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoading(true)
    setError("")
    try {
      const result = await getFileDiff(repoId, {
        base: baseCommit,
        head: headCommit,
        path: file.path,
      })
      if (requestRef.current === requestId) setDiff(result)
    } catch (caught) {
      if (requestRef.current === requestId) setError(describeCodeError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [repoId, baseCommit, headCommit, file.path])

  useEffect(() => {
    void load()
    return () => {
      requestRef.current += 1
    }
  }, [load])

  // Save the opener, focus the dialog, and restore focus on close.
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

  const binary = diff?.binary === true || file.binary
  const truncated = diff?.truncated === true
  const patch = diff?.patch ?? ""

  return (
    <div className="fixed inset-0 z-50 flex justify-end">
      <button
        type="button"
        tabIndex={-1}
        aria-label="关闭 diff 抽屉遮罩"
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
        className="relative flex h-full w-full max-w-3xl flex-col border-l border-border bg-card shadow-2xl outline-none"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <p className="text-[11px] font-medium text-muted-foreground">文件 diff</p>
            <h2 id={titleId} className="truncate font-mono text-sm font-semibold">
              {file.path}
            </h2>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              <span>{fileStatusLabel(file.status)}</span>
              <span aria-hidden="true"> · </span>
              <span title={baseCommit}>{shortCommit(baseCommit)}</span>
              <span aria-hidden="true"> → </span>
              <span title={headCommit}>{shortCommit(headCommit)}</span>
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭 diff 抽屉"
            className="rounded-lg border border-border p-1.5 text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </header>

        <div className="min-h-0 flex-1 overflow-auto p-4">
          {loading ? (
            <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              正在加载 diff…
            </p>
          ) : error !== "" ? (
            <div
              role="alert"
              className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-4 text-sm text-destructive"
            >
              <p>加载 diff 失败：{error}</p>
              <button
                type="button"
                onClick={() => void load()}
                className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                重试
              </button>
            </div>
          ) : binary ? (
            <p className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-4 py-3 text-sm text-amber-700 dark:text-amber-300">
              二进制文件，无法展示 diff。
            </p>
          ) : (
            <div className="space-y-3">
              {truncated ? (
                <p
                  role="status"
                  className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-4 py-3 text-xs text-amber-700 dark:text-amber-300"
                >
                  diff 内容已截断，仅展示部分内容。
                </p>
              ) : null}
              {patch === "" ? (
                <p className="text-sm text-muted-foreground">该文件没有可展示的 diff 内容。</p>
              ) : (
                <pre className="overflow-x-auto rounded-xl border border-border bg-muted/30 p-3 font-mono text-xs leading-relaxed">
                  <code>
                    {splitPatchLines(patch).map((line) => (
                      <span
                        key={line.key}
                        data-line-kind={line.kind}
                        className={LINE_CLASSES[line.kind]}
                      >
                        {line.text === "" ? " " : line.text}
                      </span>
                    ))}
                  </code>
                </pre>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

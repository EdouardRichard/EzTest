"use client"

import { Loader2, Trash2, X } from "lucide-react"
import {
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react"

import { listRequirements } from "@/features/versions/api"
import type { Requirement } from "@/features/versions/types"

import { createAnchor, deleteAnchor, listAnchors } from "./api"
import { ConfidenceBadge } from "./ConfidenceBadge"
import { codeErrorReason, describeCodeError } from "./empty"
import type { Anchor, CreateAnchorBody } from "./types"

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/** Requirement anchors are the only subject kind exposed in the P4 UI (spec §6.7). */
const ANCHOR_REQUIREMENT_LIMIT = 100

/** Anchor-specific `details.reason` labels on top of the shared code errors. */
const ANCHOR_REASON_LABELS: Record<string, string> = {
  anchor_duplicate: "相同需求、文件与行区间的锚点已存在。",
  anchor_not_found: "锚点不存在或已被删除。",
  subject_not_in_version: "所选需求不属于该版本。",
  unsupported_subject_kind: "P4 仅支持需求锚点。",
}

/** `file:start-end` for one anchor; line numbers are optional (spec §7.3). */
export function formatAnchorLocation(
  anchor: Pick<Anchor, "filePath" | "startLine" | "endLine">,
): string {
  if (typeof anchor.startLine !== "number") return anchor.filePath
  if (typeof anchor.endLine !== "number" || anchor.endLine === anchor.startLine) {
    return `${anchor.filePath}:${anchor.startLine}`
  }
  return `${anchor.filePath}:${anchor.startLine}-${anchor.endLine}`
}

function describeAnchorError(error: unknown): string {
  const reason = codeErrorReason(error)
  const label = ANCHOR_REASON_LABELS[reason]
  if (label !== undefined) return label
  return describeCodeError(error)
}

export type AnchorDialogProps = {
  versionId: string
  /** Prefilled from the changed file row that opened the dialog. */
  filePath: string
  onClose: () => void
  /** Called after an anchor was created or deleted so the container refreshes. */
  onCreated: () => void
}

/**
 * Requirement-anchor dialog (spec §6.7/§9.2): lists the version's requirements,
 * prefills the changed file path, accepts an optional line range, and lists /
 * deletes the anchors of the selected requirement. Every logged-in user may use
 * it (the backend enforces login); it behaves as an accessible modal with ESC
 * close and focus restoration.
 */
export function AnchorDialog({ versionId, filePath, onClose, onCreated }: AnchorDialogProps) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const previousFocusRef = useRef<Element | null>(null)
  const anchorRequestRef = useRef(0)

  const [requirements, setRequirements] = useState<Requirement[]>([])
  const [requirementsLoading, setRequirementsLoading] = useState(true)
  const [requirementsError, setRequirementsError] = useState("")

  const [selectedRequirementId, setSelectedRequirementId] = useState("")
  const [path, setPath] = useState(filePath)
  const [startLine, setStartLine] = useState("")
  const [endLine, setEndLine] = useState("")

  const [anchors, setAnchors] = useState<Anchor[]>([])
  const [anchorsLoading, setAnchorsLoading] = useState(false)
  const [anchorsError, setAnchorsError] = useState("")

  const [creating, setCreating] = useState(false)
  const [deletingId, setDeletingId] = useState("")
  const [actionError, setActionError] = useState("")
  const [statusMessage, setStatusMessage] = useState("")

  const loadRequirements = useCallback(async (): Promise<void> => {
    setRequirementsLoading(true)
    setRequirementsError("")
    try {
      const page = await listRequirements(versionId, { limit: ANCHOR_REQUIREMENT_LIMIT })
      setRequirements(Array.isArray(page.items) ? page.items : [])
    } catch (caught) {
      setRequirementsError(`加载版本需求失败：${describeCodeError(caught)}`)
    } finally {
      setRequirementsLoading(false)
    }
  }, [versionId])

  const loadAnchors = useCallback(
    async (requirementId: string): Promise<void> => {
      const requestId = ++anchorRequestRef.current
      if (requirementId === "") {
        setAnchors([])
        setAnchorsError("")
        return
      }
      setAnchorsLoading(true)
      setAnchorsError("")
      try {
        const items = await listAnchors(versionId, {
          subjectKind: "requirement",
          subjectId: requirementId,
        })
        if (anchorRequestRef.current !== requestId) return
        setAnchors(
          Array.isArray(items)
            ? items.filter(
                (anchor) =>
                  anchor.subjectKind === "requirement" && anchor.subjectId === requirementId,
              )
            : [],
        )
      } catch (caught) {
        if (anchorRequestRef.current !== requestId) return
        setAnchorsError(`加载已有锚点失败：${describeCodeError(caught)}`)
      } finally {
        if (anchorRequestRef.current === requestId) setAnchorsLoading(false)
      }
    },
    [versionId],
  )

  useEffect(() => {
    void loadRequirements()
  }, [loadRequirements])

  useEffect(() => {
    setPath(filePath)
  }, [filePath])

  useEffect(() => {
    if (selectedRequirementId === "") {
      setAnchors([])
      return
    }
    void loadAnchors(selectedRequirementId)
    return () => {
      anchorRequestRef.current += 1
    }
  }, [selectedRequirementId, loadAnchors])

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

  /** Parses the optional range; returns an error message or the line numbers. */
  function parseLineRange(): { startLine?: number; endLine?: number } | string {
    const startText = startLine.trim()
    const endText = endLine.trim()
    if (startText === "" && endText === "") return {}
    if (startText === "" || endText === "") return "起始行与结束行需同时填写。"
    const start = Number(startText)
    const end = Number(endText)
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < 1) {
      return "行号必须为正整数。"
    }
    if (start > end) return "起始行不能大于结束行。"
    return { startLine: start, endLine: end }
  }

  async function handleCreate(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setActionError("")
    setStatusMessage("")
    if (selectedRequirementId === "") {
      setActionError("请选择需求。")
      return
    }
    const trimmedPath = path.trim()
    if (trimmedPath === "") {
      setActionError("请填写文件路径。")
      return
    }
    const range = parseLineRange()
    if (typeof range === "string") {
      setActionError(range)
      return
    }

    const body: CreateAnchorBody = {
      subjectKind: "requirement",
      subjectId: selectedRequirementId,
      filePath: trimmedPath,
    }
    if (range.startLine !== undefined && range.endLine !== undefined) {
      body.startLine = range.startLine
      body.endLine = range.endLine
    }

    setCreating(true)
    try {
      await createAnchor(versionId, body)
      setStartLine("")
      setEndLine("")
      setStatusMessage("已创建锚点。")
      onCreated()
      await loadAnchors(selectedRequirementId)
    } catch (caught) {
      setActionError(`创建锚点失败：${describeAnchorError(caught)}`)
    } finally {
      setCreating(false)
    }
  }

  async function handleDelete(anchor: Anchor): Promise<void> {
    setActionError("")
    setStatusMessage("")
    setDeletingId(anchor.id)
    try {
      await deleteAnchor(versionId, anchor.id)
      setStatusMessage("已删除锚点。")
      onCreated()
      await loadAnchors(selectedRequirementId)
    } catch (caught) {
      setActionError(`删除锚点失败：${describeAnchorError(caught)}`)
    } finally {
      setDeletingId("")
    }
  }

  const selectedRequirement = requirements.find(
    (requirement) => requirement.id === selectedRequirementId,
  )

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <button
        type="button"
        tabIndex={-1}
        aria-label="关闭关联需求遮罩"
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
        className="relative flex max-h-[85vh] w-full max-w-xl flex-col rounded-2xl border border-border bg-card shadow-2xl outline-none"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <p className="text-[11px] font-medium text-muted-foreground">代码锚点</p>
            <h2 id={titleId} className="text-base font-semibold">
              关联需求锚点
            </h2>
            <p className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">
              {filePath}
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭关联需求"
            className="rounded-lg border border-border p-1.5 text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </header>

        <div className="min-h-0 flex-1 space-y-4 overflow-auto p-4">
          <p className="text-xs text-muted-foreground">
            锚点命中变更文件后会成为影响分析的直接候选。任何登录用户都可以创建或删除。
          </p>

          {requirementsError === "" ? null : (
            <div
              role="alert"
              className="space-y-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
            >
              <p>{requirementsError}</p>
              <button
                type="button"
                onClick={() => void loadRequirements()}
                className="rounded-lg border border-border bg-card px-3 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted"
              >
                重试加载需求
              </button>
            </div>
          )}

          <form className="space-y-3" onSubmit={(event) => void handleCreate(event)}>
            <div className="space-y-1">
              <label htmlFor={`${titleId}-requirement`} className="text-xs font-medium">
                需求
              </label>
              <select
                id={`${titleId}-requirement`}
                value={selectedRequirementId}
                disabled={requirementsLoading}
                onChange={(event) => {
                  setSelectedRequirementId(event.target.value)
                  setActionError("")
                  setStatusMessage("")
                }}
                className="w-full rounded-lg border border-border bg-card px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
              >
                <option value="">{requirementsLoading ? "正在加载需求…" : "请选择需求"}</option>
                {requirements.map((requirement) => (
                  <option key={requirement.id} value={requirement.id}>
                    {requirement.title}（#{requirement.id}）
                  </option>
                ))}
              </select>
              {!requirementsLoading && requirements.length === 0 ? (
                <p className="text-xs text-muted-foreground">该版本暂无需求，无法创建需求锚点。</p>
              ) : null}
            </div>

            <div className="space-y-1">
              <label htmlFor={`${titleId}-path`} className="text-xs font-medium">
                文件路径
              </label>
              <input
                id={`${titleId}-path`}
                type="text"
                value={path}
                onChange={(event) => setPath(event.target.value)}
                className="w-full rounded-lg border border-border bg-card px-3 py-2 font-mono text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <label htmlFor={`${titleId}-start`} className="text-xs font-medium">
                  起始行（可选）
                </label>
                <input
                  id={`${titleId}-start`}
                  type="number"
                  min={1}
                  value={startLine}
                  onChange={(event) => setStartLine(event.target.value)}
                  className="w-full rounded-lg border border-border bg-card px-3 py-2 font-mono text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                />
              </div>
              <div className="space-y-1">
                <label htmlFor={`${titleId}-end`} className="text-xs font-medium">
                  结束行（可选）
                </label>
                <input
                  id={`${titleId}-end`}
                  type="number"
                  min={1}
                  value={endLine}
                  onChange={(event) => setEndLine(event.target.value)}
                  className="w-full rounded-lg border border-border bg-card px-3 py-2 font-mono text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                />
              </div>
            </div>

            {actionError === "" ? null : (
              <p
                role="alert"
                className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
              >
                {actionError}
              </p>
            )}
            {statusMessage === "" ? null : (
              <p role="status" className="text-xs text-emerald-700 dark:text-emerald-300">
                {statusMessage}
              </p>
            )}

            <button
              type="submit"
              disabled={creating || requirementsLoading || selectedRequirementId === ""}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {creating ? (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                  创建中…
                </>
              ) : (
                "创建锚点"
              )}
            </button>
          </form>

          <div className="space-y-2 border-t border-border pt-3">
            <div className="flex items-baseline justify-between gap-2">
              <h3 className="text-sm font-semibold">
                {selectedRequirement === undefined
                  ? "已有锚点"
                  : `${selectedRequirement.title} 的锚点`}
              </h3>
              <span className="text-xs text-muted-foreground">{anchors.length} 个</span>
            </div>

            {selectedRequirementId === "" ? (
              <p className="text-xs text-muted-foreground">选择需求后展示已有锚点。</p>
            ) : anchorsError !== "" ? (
              <p role="alert" className="text-xs text-destructive">
                {anchorsError}
              </p>
            ) : anchorsLoading ? (
              <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                正在加载锚点…
              </p>
            ) : anchors.length === 0 ? (
              <p className="text-xs text-muted-foreground">该需求暂无锚点。</p>
            ) : (
              <ul className="divide-y divide-border">
                {anchors.map((anchor) => {
                  const location = formatAnchorLocation(anchor)
                  return (
                    <li key={anchor.id} className="flex items-center justify-between gap-3 py-2">
                      <div className="min-w-0 space-y-1">
                        <p className="break-all font-mono text-xs text-foreground">{location}</p>
                        <div className="flex flex-wrap items-center gap-1.5">
                          <span className="text-[11px] text-muted-foreground">
                            锚点 #{anchor.id}
                          </span>
                          <ConfidenceBadge confidence={anchor.confidence} />
                        </div>
                      </div>
                      <button
                        type="button"
                        aria-label={`删除锚点 ${location}`}
                        disabled={deletingId === anchor.id}
                        onClick={() => void handleDelete(anchor)}
                        className="inline-flex shrink-0 items-center gap-1 rounded-lg border border-border px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-wait disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        {deletingId === anchor.id ? (
                          <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                        ) : (
                          <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                        )}
                        删除
                      </button>
                    </li>
                  )
                })}
              </ul>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

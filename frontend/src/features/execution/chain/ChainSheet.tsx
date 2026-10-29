"use client"

import { Loader2, X } from "lucide-react"
import { type KeyboardEvent as ReactKeyboardEvent, useEffect, useId, useRef, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import type { VersionStreamState } from "@/features/versions/useVersionStream"
import { cn } from "@/lib/utils"

import { FailureClassBadge } from "../components/FailureClassBadge"
import { useCaseRun } from "../hooks/useExecution"
import type { FailureClass } from "../types"
import {
  getCasePriorityLabel,
  getCaseStatusToken,
  getIssueCategoryLabel,
  getIssueConfidenceLabel,
  getIssueSeverityToken,
  getIssueStatusToken,
  getRequirementPriorityLabel,
  getRequirementStatusToken,
  getResultStatusToken,
  NODE_KIND_TOKENS,
} from "./chain-tokens"
import type { ChainSnapshotNode } from "./chain-types"
import { formatChainDuration } from "./nodes/ResultNode"

export type ChainSheetDeepLink = {
  kind: "requirement" | "case" | "result" | "issue"
  id: string
}

export type ChainSheetProps = {
  node: ChainSnapshotNode
  versionId: string
  /** Batch of the tab; used when the node's case run has no own batch id. */
  batchId: string
  stream?: VersionStreamState
  /** Batch of the result/case-run, resolved by the composition layer. */
  resultBatchId?: string
  onClose: () => void
  onOpenEvidence?: (evidenceId: string) => void
  onNavigate?: (target: ChainSheetDeepLink) => void
}

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])'

const FAILURE_CLASSES: ReadonlySet<FailureClass> = new Set<FailureClass>([
  "application",
  "environment",
  "session",
  "model",
  "case",
])

const BUTTON_CLASS =
  "inline-flex min-h-11 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

function toFailureClass(value: string | undefined): FailureClass | null {
  if (value === undefined || value === "") return null
  return FAILURE_CLASSES.has(value as FailureClass) ? (value as FailureClass) : null
}

function domainIdOf(nodeId: string): string {
  const separator = nodeId.indexOf(":")
  return separator === -1 ? nodeId : nodeId.slice(separator + 1)
}

function kindLabelOf(node: ChainSnapshotNode): string {
  return NODE_KIND_TOKENS[node.kind].label
}

function statusBadgeOf(node: ChainSnapshotNode): {
  label: string
  variant: StatusBadgeVariant
} {
  if (node.kind === "result") {
    const token = getResultStatusToken(node.status)
    return { label: token.label, variant: token.badgeVariant }
  }
  if (node.kind === "case") {
    const token = getCaseStatusToken(node.status)
    return { label: token.label, variant: token.badgeVariant }
  }
  if (node.kind === "issue") {
    const token = getIssueStatusToken(node.status)
    return { label: token.label, variant: token.badgeVariant }
  }
  const token = getRequirementStatusToken(node.status)
  return { label: token.label, variant: token.badgeVariant }
}

function summaryOf(node: ChainSnapshotNode): string {
  if (node.kind === "result") {
    return node.summary !== undefined && node.summary !== "" ? node.summary : "该执行结果暂无摘要。"
  }
  if (node.kind === "requirement") return "该需求派生出的用例与执行结果如下。"
  if (node.kind === "case") return "该用例的执行结果与证据如下。"
  return "该问题关联的用例执行结果可追溯证据；打开问题页查看复现步骤与处理记录。"
}

/**
 * Right-side overlay Sheet of one chain node (spec §5.5/§5.7).
 *
 * It is a modal dialog: focus moves to the close button on open, Tab is trapped,
 * Esc/overlay/close dismiss it, and focus returns to the element that opened it.
 * Evidence is never loaded with the chain; it is read lazily by `caseRunId` when
 * the user asks for it. An issue without a case run cannot resolve evidence and
 * points at the issue page instead of offering a dead-end button (C9).
 */
export function ChainSheet({
  node,
  versionId,
  batchId,
  stream,
  resultBatchId,
  onClose,
  onOpenEvidence,
  onNavigate,
}: ChainSheetProps) {
  const [evidenceRequested, setEvidenceRequested] = useState(false)
  const dialogRef = useRef<HTMLDivElement>(null)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const triggerRef = useRef<Element | null>(null)
  const titleId = useId()

  useEffect(() => {
    triggerRef.current = typeof document === "undefined" ? null : document.activeElement
    closeButtonRef.current?.focus()
    return () => {
      const trigger = triggerRef.current
      if (trigger instanceof HTMLElement && document.contains(trigger)) trigger.focus()
    }
  }, [])

  useEffect(() => {
    function handleDocumentKeyDown(event: globalThis.KeyboardEvent): void {
      if (event.key !== "Escape") return
      event.preventDefault()
      onClose()
    }
    document.addEventListener("keydown", handleDocumentKeyDown)
    return () => document.removeEventListener("keydown", handleDocumentKeyDown)
  }, [onClose])

  const domainId = domainIdOf(node.id)
  const caseRunId =
    node.kind === "result"
      ? (node.caseRunId ?? domainId)
      : node.kind === "issue"
        ? (node.caseRunId ?? "")
        : ""
  const evidenceCount = node.evidenceCount ?? 0
  const runBatchId = resultBatchId ?? batchId
  const detail = useCaseRun(
    versionId,
    runBatchId,
    evidenceRequested && caseRunId !== "" ? caseRunId : "",
    stream,
  )
  const evidenceItems = detail.caseRun?.evidence ?? []
  const evidenceUnavailable = node.kind === "issue" && evidenceCount > 0 && caseRunId === ""
  const offersEvidence = node.kind === "result" || node.kind === "issue"
  const failureClass = node.kind === "result" ? toFailureClass(node.failureClass) : null
  const status = statusBadgeOf(node)
  const kindLabel = kindLabelOf(node)

  function handleDialogKeyDown(event: ReactKeyboardEvent<HTMLDivElement>): void {
    if (event.key !== "Tab") return
    const dialog = dialogRef.current
    if (dialog === null) return
    const focusables = [...dialog.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)]
    if (focusables.length === 0) {
      event.preventDefault()
      return
    }
    const first = focusables[0]
    const last = focusables[focusables.length - 1]
    const active = document.activeElement
    if (event.shiftKey) {
      if (active === first || !dialog.contains(active)) {
        event.preventDefault()
        last.focus()
      }
      return
    }
    if (active === last || !dialog.contains(active)) {
      event.preventDefault()
      first.focus()
    }
  }

  return (
    <>
      <div
        data-testid="chain-sheet-overlay"
        aria-hidden="true"
        onClick={onClose}
        className="fixed inset-0 z-40 bg-black/40 backdrop-blur-[1px]"
      />
      <div
        ref={dialogRef}
        data-testid="chain-sheet"
        data-kind={node.kind}
        data-node-id={node.id}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={handleDialogKeyDown}
        className="fixed inset-y-0 right-0 z-50 flex w-full flex-col gap-4 overflow-y-auto border-l border-border bg-card p-4 shadow-2xl lg:max-w-md"
      >
        <header className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
              {`${kindLabel}节点`}
            </p>
            <h3 id={titleId} className="mt-1 break-words text-sm font-semibold">
              {node.title}
            </h3>
          </div>
          <button
            ref={closeButtonRef}
            type="button"
            data-testid="chain-sheet-close"
            aria-label="关闭节点详情"
            onClick={onClose}
            className={cn(BUTTON_CLASS, "min-w-11 justify-center")}
          >
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            关闭
          </button>
        </header>

        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <StatusBadge variant={status.variant}>{status.label}</StatusBadge>
          {node.kind === "result" && failureClass !== null ? (
            <FailureClassBadge failureClass={failureClass} />
          ) : null}
          {node.kind === "result" ? (
            <span data-testid="chain-sheet-attempt">{`第 ${node.attempt ?? 1} 次尝试`}</span>
          ) : null}
          {node.kind === "result" && (node.batchId ?? "") !== "" ? (
            <span>{`批次 ${node.batchId ?? ""}`}</span>
          ) : null}
          {node.kind === "result" && formatChainDuration(node.durationMs) !== "" ? (
            <span>{`耗时 ${formatChainDuration(node.durationMs)}`}</span>
          ) : null}
          {offersEvidence ? <span>{`证据 ${evidenceCount}`}</span> : null}
          {node.kind === "requirement" && getRequirementPriorityLabel(node.priority) !== "—" ? (
            <span>{`优先级：${getRequirementPriorityLabel(node.priority)}`}</span>
          ) : null}
          {node.kind === "case" && getCasePriorityLabel(node.priority) !== "—" ? (
            <span>{`优先级：${getCasePriorityLabel(node.priority)}`}</span>
          ) : null}
          {node.kind === "issue" ? (
            <>
              <span>{`严重级：${getIssueSeverityToken(node.severity).label}`}</span>
              <span>{`置信度：${getIssueConfidenceLabel(node.confidence)}`}</span>
              <span>{`分类：${getIssueCategoryLabel(node.category)}`}</span>
            </>
          ) : null}
        </div>

        <p className="text-sm leading-relaxed text-muted-foreground">{summaryOf(node)}</p>

        {node.kind === "case" ? (
          <p data-testid="chain-sheet-case-requirements" className="text-xs text-muted-foreground">
            {`关联需求：${(node.requirementIds ?? []).join("、") || "—"}`}
          </p>
        ) : null}

        {node.kind === "issue" ? (
          <p data-testid="chain-sheet-code" className="text-xs text-muted-foreground">
            {`代码位置：${node.codeRefCount ?? 0} 处（在问题页查看关联代码）`}
          </p>
        ) : null}

        <div className="flex flex-wrap items-center gap-2">
          {node.kind === "requirement" ? (
            <button
              type="button"
              data-testid="chain-sheet-open-requirement"
              onClick={() => onNavigate?.({ kind: "requirement", id: domainId })}
              className={BUTTON_CLASS}
            >
              查看需求
            </button>
          ) : null}
          {node.kind === "case" ? (
            <button
              type="button"
              data-testid="chain-sheet-open-case"
              onClick={() => onNavigate?.({ kind: "case", id: domainId })}
              className={BUTTON_CLASS}
            >
              查看用例
            </button>
          ) : null}
          {node.kind === "result" ? (
            <>
              {(node.caseNodeId ?? "") === "" ? null : (
                <button
                  type="button"
                  data-testid="chain-sheet-open-case"
                  onClick={() => onNavigate?.({ kind: "case", id: node.caseNodeId ?? "" })}
                  className={BUTTON_CLASS}
                >
                  查看用例
                </button>
              )}
              <button
                type="button"
                data-testid="chain-sheet-open-result"
                onClick={() => onNavigate?.({ kind: "result", id: domainId })}
                className={BUTTON_CLASS}
              >
                打开执行结果
              </button>
            </>
          ) : null}
          {node.kind === "issue" ? (
            <>
              <button
                type="button"
                data-testid="chain-sheet-open-issue"
                onClick={() => onNavigate?.({ kind: "issue", id: domainId })}
                className={BUTTON_CLASS}
              >
                查看问题
              </button>
              {(node.caseNodeId ?? "") === "" ? null : (
                <button
                  type="button"
                  data-testid="chain-sheet-open-case"
                  onClick={() => onNavigate?.({ kind: "case", id: node.caseNodeId ?? "" })}
                  className={BUTTON_CLASS}
                >
                  查看用例
                </button>
              )}
            </>
          ) : null}
        </div>

        {offersEvidence ? (
          <section data-testid="chain-sheet-evidence">
            <h4 className="text-xs font-semibold">证据</h4>
            {evidenceUnavailable ? (
              <p
                data-testid="chain-sheet-evidence-unavailable"
                className="mt-1 text-xs text-muted-foreground"
              >
                该问题未关联执行结果，无法加载证据；请点击“查看问题”查看复现步骤与处理记录。
              </p>
            ) : evidenceCount === 0 ? (
              <p className="mt-1 text-xs text-muted-foreground">该节点暂无可查看证据。</p>
            ) : evidenceRequested ? (
              detail.loading ? (
                <p
                  data-testid="chain-sheet-evidence-loading"
                  role="status"
                  className="mt-1 flex items-center gap-2 text-xs text-muted-foreground"
                >
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                  证据加载中…
                </p>
              ) : detail.error !== "" ? (
                <p
                  data-testid="chain-sheet-evidence-error"
                  role="alert"
                  className="mt-1 text-xs text-destructive"
                >
                  {`证据加载失败：${detail.error}`}
                </p>
              ) : evidenceItems.length === 0 ? (
                <p className="mt-1 text-xs text-muted-foreground">该节点暂无可查看证据。</p>
              ) : (
                <ul className="mt-1.5 flex flex-wrap gap-1.5">
                  {evidenceItems.map((evidence) => (
                    <li key={evidence.id}>
                      <button
                        type="button"
                        data-testid={`chain-sheet-evidence-${evidence.id}`}
                        onClick={() => onOpenEvidence?.(evidence.id)}
                        className={BUTTON_CLASS}
                      >
                        {`证据 ${evidence.id}`}
                      </button>
                    </li>
                  ))}
                </ul>
              )
            ) : (
              <button
                type="button"
                data-testid="chain-sheet-load-evidence"
                onClick={() => setEvidenceRequested(true)}
                className={cn(BUTTON_CLASS, "mt-1.5")}
              >
                {`加载证据（${evidenceCount}）`}
              </button>
            )}
          </section>
        ) : null}

        <section className="rounded-xl border border-dashed border-border px-3 py-2">
          <h4 className="text-xs font-semibold">问题</h4>
          <p className="mt-1 text-xs text-muted-foreground">
            {node.kind === "issue"
              ? "问题状态与处理结果以问题页为准。"
              : "该节点关联的问题节点会显示在链路右侧。"}
          </p>
        </section>
      </div>
    </>
  )
}

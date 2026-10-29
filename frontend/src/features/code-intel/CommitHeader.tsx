"use client"

import { GitBranch, Loader2, RefreshCw, RotateCcw, Sparkles } from "lucide-react"
import Link from "next/link"
import { useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import { shortCommit } from "./empty"
import type { Analysis } from "./types"

/** Derived UI state of the version's latest code analysis (spec §9.2). */
export type CodeAnalysisView = "none" | "running" | "done" | "degraded" | "failed"

const VIEW_META: Record<CodeAnalysisView, { label: string; variant: StatusBadgeVariant }> = {
  none: { label: "未分析", variant: "neutral" },
  running: { label: "运行中", variant: "info" },
  done: { label: "完成", variant: "success" },
  degraded: { label: "降级", variant: "warning" },
  failed: { label: "失败", variant: "danger" },
}

function formatTimestamp(value: string | null): string {
  if (value === null || value === "") return "—"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString("zh-CN", { hour12: false })
}

const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3.5 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type CommitHeaderProps = {
  /** P2 code repository id (number); null when the version is unbound. */
  repoId: number | null
  baseCommit: string
  headCommit: string
  analysis: Analysis | null
  view: CodeAnalysisView
  exhausted: boolean
  starting: boolean
  /** Start/refresh failure message shown inline. */
  actionError: string
  /** `force=true` requests a new analysis version after confirmation. */
  onStartAnalysis: (force: boolean) => void
  onRefresh: () => void
}

/**
 * Commit range + analysis status header (spec §9.2): `repo@base → head`, the
 * analysis version/time and status badge, the start/re-analyze/refresh actions,
 * and an admin-only index hint that links to the code repository page. A
 * destructive re-analysis is confirmed inline; no action is ever faked.
 */
export function CommitHeader({
  repoId,
  baseCommit,
  headCommit,
  analysis,
  view,
  exhausted,
  starting,
  actionError,
  onStartAnalysis,
  onRefresh,
}: CommitHeaderProps) {
  const [confirming, setConfirming] = useState(false)
  const meta = VIEW_META[view]

  function start(force: boolean): void {
    setConfirming(false)
    onStartAnalysis(force)
  }

  return (
    <section
      aria-label="提交与分析状态"
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h2 className="text-base font-semibold">提交与分析</h2>
          <p className="flex flex-wrap items-center gap-1.5 font-mono text-xs text-muted-foreground">
            <GitBranch className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span className="text-foreground">{repoId === null ? "repo" : `repo#${repoId}`}</span>
            <span aria-hidden="true">@</span>
            <span title={baseCommit}>{shortCommit(baseCommit)}</span>
            <span aria-hidden="true">→</span>
            <span title={headCommit}>{shortCommit(headCommit)}</span>
          </p>
          {analysis === null ? null : (
            <p className="text-xs text-muted-foreground">
              分析 v{analysis.analysisVersion} ·{" "}
              {formatTimestamp(analysis.finishedAt ?? analysis.createdAt)}
            </p>
          )}
        </div>
        <div className="flex flex-col items-end gap-1.5">
          <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
          <Link
            href="/code-repos"
            className="text-[11px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            title="索引刷新由管理员在代码仓库页面执行"
          >
            刷新索引
          </Link>
          <span className="text-[11px] text-muted-foreground">管理员可在代码仓库页面触发索引</span>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        {view === "none" ? (
          <button
            type="button"
            onClick={() => start(false)}
            disabled={starting}
            className={primaryButtonClass}
          >
            {starting ? (
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            ) : (
              <Sparkles className="h-4 w-4" aria-hidden="true" />
            )}
            {starting ? "启动中…" : "开始分析"}
          </button>
        ) : null}

        {view === "failed" ? (
          <button
            type="button"
            onClick={() => start(true)}
            disabled={starting}
            className={primaryButtonClass}
          >
            {starting ? (
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            ) : (
              <RotateCcw className="h-4 w-4" aria-hidden="true" />
            )}
            {starting ? "重试中…" : "重试分析"}
          </button>
        ) : null}

        {view === "done" || view === "degraded" ? (
          <button
            type="button"
            onClick={() => setConfirming(true)}
            disabled={starting}
            className={secondaryButtonClass}
          >
            <RotateCcw className="h-4 w-4" aria-hidden="true" />
            重新分析
          </button>
        ) : null}

        <button type="button" onClick={onRefresh} className={secondaryButtonClass}>
          <RefreshCw className="h-4 w-4" aria-hidden="true" />
          刷新
        </button>
      </div>

      {confirming ? (
        <div
          role="alertdialog"
          aria-label="确认重新分析"
          className="space-y-3 rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-3 text-xs text-amber-700 dark:text-amber-300"
        >
          <p>重新分析将忽略缓存并生成新的分析版本（强制分析），是否继续？</p>
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => start(true)}
              disabled={starting}
              className="rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              确认重新分析
            </button>
            <button
              type="button"
              onClick={() => setConfirming(false)}
              className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              取消
            </button>
          </div>
        </div>
      ) : null}

      {exhausted ? (
        <p role="status" className="text-xs text-amber-700 dark:text-amber-300">
          分析仍在运行，已停止自动刷新；请手动刷新。
        </p>
      ) : null}

      {actionError === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {actionError}
        </p>
      )}
    </section>
  )
}

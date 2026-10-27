import { AlertTriangle, FileText, GitBranch, Unplug } from "lucide-react"
import Link from "next/link"

import { EmptyState } from "@/components/ui/EmptyState"
import { ApiError } from "@/lib/api-client"

import type { IndexRun } from "./types"

/** Human labels for the closed code-intel `details.reason` set (spec §7.1). */
const REASON_LABELS: Record<string, string> = {
  code_repository_not_bound: "版本未绑定代码仓库。",
  code_repository_not_found: "绑定的代码仓库不存在或已被删除。",
  code_repository_disabled: "绑定的代码仓库已停用。",
  baseline_commit_missing: "版本没有代码基线 commit，请在资源绑定中重新保存。",
  invalid_head: "head commit 无效或不是基线的后代。",
  commit_not_found: "指定的 commit 不存在。",
  git_unavailable: "Git 仓库暂时不可用，请稍后重试。",
  path_not_found: "文件路径不存在。",
  path_not_file: "路径不是普通文件。",
  path_escape: "文件路径越出了仓库范围。",
  not_changed: "该文件不在本次变更集中。",
  unsupported_content: "文件内容不支持展示（二进制、非 UTF-8 或超出大小上限）。",
  index_in_progress: "该仓库已有索引任务在运行。",
  index_run_not_found: "尚未找到索引记录。",
  analysis_in_progress: "该版本已有分析任务在运行。",
  analysis_queue_full: "分析队列已满，请稍后重试。",
  analysis_not_found: "尚未找到分析记录。",
  analysis_not_done: "分析尚未完成，暂不能调整候选。",
}

/** The closed `details.reason` string of an ApiError, or "" when absent. */
export function codeErrorReason(error: unknown): string {
  if (!(error instanceof ApiError)) return ""
  return typeof error.details.reason === "string" ? error.details.reason : ""
}

/** Maps a code-intel failure to a user-facing Chinese message (spec §9.4). */
export function describeCodeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 401) return "登录状态已失效，请重新登录。"
  if (error.status === 403) return "没有权限执行该操作。"

  const reason = codeErrorReason(error)
  const label = REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

/** Abbreviates a commit SHA for display; the full value stays in a title. */
export function shortCommit(commit: string): string {
  return commit.length <= 8 ? commit : commit.slice(0, 8)
}

const primaryLinkClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const secondaryLinkClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3.5 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3.5 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type UnboundCodeStateProps = {
  /** Version whose binding needs editing; a decimal string (spec §7.1). */
  versionId: string
}

/** Empty state for a version that has no code repository binding (spec §9.2). */
export function UnboundCodeState({ versionId }: UnboundCodeStateProps) {
  return (
    <EmptyState
      icon={Unplug}
      title="未绑定代码仓库"
      description="该版本尚未绑定代码仓库。请先在「需求与澄清」的资源绑定中选择代码仓库，再查看变更与影响分析。"
      action={
        <div className="flex flex-wrap items-center justify-center gap-2">
          <Link href={`/versions/${versionId}?tab=requirements`} className={primaryLinkClass}>
            前往绑定代码仓库
          </Link>
          <Link href="/code-repos" className={secondaryLinkClass}>
            管理代码仓库
          </Link>
        </div>
      }
    />
  )
}

/** Empty state for a bound repository without a resolved baseline commit. */
export function NoBaselineCodeState({ versionId }: UnboundCodeStateProps) {
  return (
    <EmptyState
      icon={GitBranch}
      title="未配置代码基线"
      description="版本已绑定代码仓库，但尚无基线 commit。请在资源绑定中重新保存绑定，由后端解析固定分支的基线。"
      action={
        <Link href={`/versions/${versionId}?tab=requirements`} className={primaryLinkClass}>
          前往资源绑定
        </Link>
      }
    />
  )
}

export type UnindexedCodeStateProps = {
  /** Latest index run, or null when the repository has never been indexed. */
  run: IndexRun | null
  refreshing: boolean
  onRefresh: () => void
}

/**
 * Empty state for a repository that has no successful T0 index yet (spec §9.2).
 * Indexing itself is an admin action on the code repository page; this tab only
 * reports the state and offers a status refresh.
 */
export function UnindexedCodeState({ run, refreshing, onRefresh }: UnindexedCodeStateProps) {
  const indexing = run?.status === "queued" || run?.status === "running"
  const failed = run?.status === "failed"
  const title = indexing ? "代码仓库正在索引" : failed ? "代码索引失败" : "代码仓库尚未索引"
  const description = indexing
    ? "索引任务正在运行，完成后会自动刷新变更与文件级分析。"
    : failed
      ? "上一次索引任务失败。请联系管理员在「代码仓库」页面重新触发索引，然后刷新状态。"
      : "代码仓尚未完成 T0 索引。索引由管理员在「代码仓库」页面触发，完成后即可查看变更与 diff。"

  return (
    <EmptyState
      icon={GitBranch}
      title={title}
      description={description}
      action={
        <div className="flex flex-wrap items-center justify-center gap-2">
          <button
            type="button"
            onClick={onRefresh}
            disabled={refreshing}
            className={secondaryButtonClass}
          >
            {refreshing ? "刷新中…" : "刷新索引状态"}
          </button>
          <Link href="/code-repos" className={secondaryLinkClass}>
            前往代码仓库
          </Link>
        </div>
      }
    />
  )
}

export type NoChangesCodeStateProps = {
  baseCommit: string
  headCommit: string
  refreshing: boolean
  onRefresh: () => void
}

/** Empty state for a base→head range with no changed files (spec §9.2). */
export function NoChangesCodeState({
  baseCommit,
  headCommit,
  refreshing,
  onRefresh,
}: NoChangesCodeStateProps) {
  return (
    <EmptyState
      icon={FileText}
      title="无变更"
      description={`基线 ${shortCommit(baseCommit)} 与 head ${shortCommit(headCommit)} 之间没有文件变更。`}
      action={
        <button
          type="button"
          onClick={onRefresh}
          disabled={refreshing}
          className={secondaryButtonClass}
        >
          {refreshing ? "刷新中…" : "刷新变更"}
        </button>
      }
    />
  )
}

export type CodeLoadErrorStateProps = {
  message: string
  onRetry: () => void
}

/** Load/error fallback for the change preview itself (spec §9.4). */
export function CodeLoadErrorState({ message, onRetry }: CodeLoadErrorStateProps) {
  return (
    <EmptyState
      icon={AlertTriangle}
      title="加载代码变更失败"
      description={message}
      action={
        <button type="button" onClick={onRetry} className={secondaryButtonClass}>
          重试
        </button>
      }
    />
  )
}

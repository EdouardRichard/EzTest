"use client"

import { AlertCircle, GitBranch, Loader2, Pencil, Plus, RefreshCw, Trash2, X } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { useAuth } from "@/features/auth/AuthProvider"
import { ApiError } from "@/lib/api-client"

import {
  createCodeRepo,
  deleteCodeRepo,
  listCodeRepos,
  updateCodeRepo,
  validateCodeRepo,
} from "./api"
import type { CodeRepoDTO } from "./types"
import { type IndexView, indexView, type RepoIndexState, useIndexStatus } from "./useIndexStatus"

type CodeRepoForm = {
  key: string
  name: string
  localPath: string
  fixedBranch: string
  includeGlobs: string
  excludeGlobs: string
}

/**
 * Defaults from the code_repository DDL. The backend applies them only when
 * `excludeGlobs` is omitted, so the create form must send them explicitly.
 */
const DEFAULT_EXCLUDE_GLOBS = ".git/**, node_modules/**, dist/**, build/**, vendor/**"

const EMPTY_FORM: CodeRepoForm = {
  key: "",
  name: "",
  localPath: "",
  fixedBranch: "main",
  includeGlobs: "",
  excludeGlobs: DEFAULT_EXCLUDE_GLOBS,
}

/** Human labels for the closed `details.reason` set of spec §7.1/§6.7. */
const ERROR_REASON_LABELS: Record<string, string> = {
  path_not_found: "本地路径不存在",
  path_not_git_repo: "本地路径不是 Git 仓库",
  branch_not_found: "固定分支不存在",
  invalid_branch: "固定分支格式不合法",
  invalid_glob: "Glob 配置不合法",
  duplicate_key: "标识已存在",
  has_dependencies: "存在依赖，无法删除",
  code_repository_not_found: "代码仓库不存在或已被删除",
  code_repository_disabled: "代码仓库已停用",
  git_unavailable: "Git 仓库暂时不可用，请稍后重试",
  index_in_progress: "该仓库已有索引任务在运行",
  index_run_not_found: "尚未找到索引记录",
}

/** Badge copy/colour for each derived index state (spec §9.2). */
const INDEX_VIEW_META: Record<IndexView, { label: string; variant: StatusBadgeVariant }> = {
  unindexed: { label: "未索引", variant: "neutral" },
  queued: { label: "排队中", variant: "info" },
  running: { label: "索引中", variant: "info" },
  done: { label: "已索引", variant: "success" },
  failed: { label: "索引失败", variant: "danger" },
}

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 403) return "当前账户没有管理员权限。"

  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = reason === "" ? "" : ERROR_REASON_LABELS[reason]
  if (label) return `${label}（${reason}）`
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

/** Splits the comma-separated form value into the API's glob array. */
function splitGlobs(value: string): string[] {
  return value
    .split(",")
    .map((glob) => glob.trim())
    .filter((glob) => glob !== "")
}

function joinGlobs(globs: string[] | null | undefined): string {
  return (globs ?? []).join(", ")
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN")
}

function statusVariant(status: string): "success" | "warning" | "neutral" {
  if (status === "active") return "success"
  if (status === "disabled") return "warning"
  return "neutral"
}

function statusLabel(status: string): string {
  if (status === "active") return "启用"
  if (status === "disabled") return "停用"
  return status === "" ? "未知" : status
}

/** Short commit for the index cell; the full SHA stays in a title. */
function formatCommit(commit: string): string {
  return commit.length <= 8 ? commit : commit.slice(0, 8)
}

/** Merges the P4 index projection into the P2 repository row (spec §9.2). */
function resolveIndexState(
  repo: CodeRepoDTO,
  indexById: Record<number, RepoIndexState>,
): RepoIndexState {
  const state = indexById[repo.id]
  return {
    lastIndexedAt: state?.lastIndexedAt ?? repo.lastIndexedAt,
    run: state?.run ?? null,
  }
}

/**
 * Admin management of local Git repositories: list, create, edit, delete,
 * re-validate and trigger a T0 index. Editing never sends the immutable key.
 * Readonly users see the repository and index status without any write
 * affordance; the backend 403 remains the real guard for index triggers.
 */
export function CodeReposPage() {
  const { user } = useAuth()
  const canWrite = user?.role === "admin"

  const {
    indexById,
    refreshingId,
    exhausted,
    error: indexError,
    clearError: clearIndexError,
    loadIndexStatus,
    refreshIndex,
  } = useIndexStatus()

  const [repos, setRepos] = useState<CodeRepoDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")

  const [formOpen, setFormOpen] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [form, setForm] = useState<CodeRepoForm>({ ...EMPTY_FORM })
  const [saving, setSaving] = useState(false)

  const [busyId, setBusyId] = useState<number | null>(null)
  const [confirmingDeleteId, setConfirmingDeleteId] = useState<number | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      // P2 rows stay the source of truth for configuration; the P4 list only
      // contributes each repository's latest index projection.
      const [page] = await Promise.all([listCodeRepos(), loadIndexStatus()])
      setRepos(page.items)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [loadIndexStatus])

  useEffect(() => {
    void load()
  }, [load])

  // Guards the post-index list refresh when the page unmounts mid-run.
  const mountedRef = useRef(true)
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  function updateForm(patch: Partial<CodeRepoForm>) {
    setForm((previous) => ({ ...previous, ...patch }))
  }

  function startCreate() {
    setEditingId(null)
    setForm({ ...EMPTY_FORM })
    setFormOpen(true)
    setConfirmingDeleteId(null)
    setError("")
    setNotice("")
  }

  function startEdit(repo: CodeRepoDTO) {
    setEditingId(repo.id)
    setForm({
      key: repo.key,
      name: repo.name,
      localPath: repo.localPath,
      fixedBranch: repo.fixedBranch,
      includeGlobs: joinGlobs(repo.includeGlobs),
      excludeGlobs: joinGlobs(repo.excludeGlobs),
    })
    setFormOpen(true)
    setConfirmingDeleteId(null)
    setError("")
    setNotice("")
  }

  function cancelEdit() {
    setFormOpen(false)
    setEditingId(null)
    setForm({ ...EMPTY_FORM })
  }

  async function handleSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError("")
    setNotice("")

    const key = form.key.trim()
    const name = form.name.trim()
    const localPath = form.localPath.trim()
    const fixedBranch = form.fixedBranch.trim()

    if (editingId === null && key === "") {
      setError("请输入代码仓标识（key）。")
      return
    }
    if (name === "") {
      setError("请输入代码仓名称。")
      return
    }
    if (localPath === "") {
      setError("请输入本地路径。")
      return
    }
    if (fixedBranch === "") {
      setError("请输入固定分支。")
      return
    }

    const payload = {
      name,
      localPath,
      fixedBranch,
      includeGlobs: splitGlobs(form.includeGlobs),
      excludeGlobs: splitGlobs(form.excludeGlobs),
    }

    setSaving(true)
    try {
      const wasCreating = editingId === null
      const saved = wasCreating
        ? await createCodeRepo({ key, ...payload })
        : await updateCodeRepo(editingId, payload)
      setRepos((previous) =>
        [...previous.filter((item) => item.id !== saved.id), saved].sort(
          (left, right) => left.id - right.id,
        ),
      )
      setNotice(wasCreating ? `已创建代码仓 ${saved.name}。` : `已保存代码仓 ${saved.name}。`)
      setFormOpen(false)
      setEditingId(null)
      setForm({ ...EMPTY_FORM })
      await load()
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  async function handleValidate(repo: CodeRepoDTO) {
    setError("")
    setNotice("")
    setBusyId(repo.id)
    try {
      const updated = await validateCodeRepo(repo.id)
      setRepos((previous) => previous.map((item) => (item.id === updated.id ? updated : item)))
      setNotice(`已重新校验 ${updated.name}，当前 commit ${updated.lastCommit || "（空）"}。`)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setBusyId(null)
    }
  }

  async function handleDelete(repo: CodeRepoDTO) {
    setConfirmingDeleteId(null)
    setError("")
    setNotice("")
    setBusyId(repo.id)
    try {
      await deleteCodeRepo(repo.id)
      setRepos((previous) => previous.filter((item) => item.id !== repo.id))
      setNotice(`已删除代码仓 ${repo.name}。`)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setBusyId(null)
    }
  }

  async function handleRefreshIndex(repo: CodeRepoDTO) {
    setError("")
    setNotice("")
    await refreshIndex(repo.id)
    if (!mountedRef.current) return
    // Re-read both lists so `lastIndexedAt` and the latest run are consistent
    // after the run settles (or after a trigger failure).
    await load()
  }

  const indexErrorMessage = indexError === null ? "" : describeError(indexError)

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">代码仓</h1>
            {canWrite ? null : <StatusBadge variant="neutral">只读</StatusBadge>}
          </div>
          <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted-foreground">
            配置本地 Git 代码仓的路径、固定分支与扫描范围；保存与重新校验均为只读 Git 检查，
            管理员可在此触发 T0 索引并查看索引状态。
          </p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {canWrite ? (
            <button
              type="button"
              onClick={startCreate}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Plus className="h-4 w-4" aria-hidden="true" />
              新建代码仓
            </button>
          ) : null}
          <button
            type="button"
            onClick={() => {
              clearIndexError()
              void load()
            }}
            disabled={loading}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
            刷新
          </button>
        </div>
      </header>

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
      {notice ? (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      ) : null}
      {exhausted ? (
        <p
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-300"
        >
          索引仍在运行，已停止自动刷新；请稍后点击「刷新」查看最新状态。
        </p>
      ) : null}

      {formOpen && canWrite ? (
        <form
          onSubmit={(event) => void handleSave(event)}
          aria-labelledby="code-repo-form-title"
          className="space-y-4 rounded-xl border border-border bg-card p-4"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 id="code-repo-form-title" className="text-sm font-semibold">
              {editingId === null ? "新建代码仓" : `编辑代码仓：${form.name}`}
            </h2>
            <button
              type="button"
              onClick={cancelEdit}
              className="rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              取消
            </button>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <label htmlFor="code-repo-key" className="text-xs font-medium text-muted-foreground">
                标识
              </label>
              <input
                id="code-repo-key"
                value={form.key}
                onChange={(event) => updateForm({ key: event.target.value })}
                disabled={editingId !== null}
                autoComplete="off"
                placeholder="例如 eztest-web"
                className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
              />
              {editingId === null ? null : (
                <p className="mt-1 text-[11px] text-muted-foreground">标识创建后不可修改。</p>
              )}
            </div>
            <div>
              <label htmlFor="code-repo-name" className="text-xs font-medium text-muted-foreground">
                名称
              </label>
              <input
                id="code-repo-name"
                value={form.name}
                onChange={(event) => updateForm({ name: event.target.value })}
                autoComplete="off"
                placeholder="例如 EzTest 前端"
                className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
            <div className="sm:col-span-2">
              <label htmlFor="code-repo-path" className="text-xs font-medium text-muted-foreground">
                本地路径
              </label>
              <input
                id="code-repo-path"
                value={form.localPath}
                onChange={(event) => updateForm({ localPath: event.target.value })}
                autoComplete="off"
                placeholder="例如 D:/Project/EzTest"
                className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
            <div>
              <label
                htmlFor="code-repo-branch"
                className="text-xs font-medium text-muted-foreground"
              >
                固定分支
              </label>
              <input
                id="code-repo-branch"
                value={form.fixedBranch}
                onChange={(event) => updateForm({ fixedBranch: event.target.value })}
                autoComplete="off"
                placeholder="例如 main"
                className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
            <div>
              <label
                htmlFor="code-repo-include"
                className="text-xs font-medium text-muted-foreground"
              >
                包含 Globs（逗号分隔）
              </label>
              <input
                id="code-repo-include"
                value={form.includeGlobs}
                onChange={(event) => updateForm({ includeGlobs: event.target.value })}
                autoComplete="off"
                placeholder="例如 src/**, app/**"
                className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
            <div>
              <label
                htmlFor="code-repo-exclude"
                className="text-xs font-medium text-muted-foreground"
              >
                排除 Globs（逗号分隔）
              </label>
              <input
                id="code-repo-exclude"
                value={form.excludeGlobs}
                onChange={(event) => updateForm({ excludeGlobs: event.target.value })}
                autoComplete="off"
                placeholder="例如 .git/**, node_modules/**"
                className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
              <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">
                新建时默认排除常见构建目录；清空后保存将显式提交空列表。
              </p>
            </div>
          </div>

          <div className="flex justify-end">
            <button
              type="submit"
              disabled={saving}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {saving ? "保存中…" : "保存"}
            </button>
          </div>
        </form>
      ) : null}

      {loading && repos.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">
          正在加载代码仓…
        </p>
      ) : repos.length === 0 ? (
        <EmptyState
          icon={GitBranch}
          title="暂无代码仓"
          description={
            canWrite
              ? "点击「新建代码仓」配置第一个本地 Git 代码仓。"
              : "管理员尚未配置本地 Git 代码仓。"
          }
        />
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="min-w-full divide-y divide-border text-sm">
            <caption className="sr-only">代码仓列表</caption>
            <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">
                  名称
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  本地路径
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  固定分支
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  当前 commit
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  最近校验
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  索引状态
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  状态
                </th>
                {canWrite ? (
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    操作
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {repos.map((repo) => {
                const indexState = resolveIndexState(repo, indexById)
                const view = indexView(indexState)
                const meta = INDEX_VIEW_META[view]
                const indexedAt = indexState.lastIndexedAt ?? indexState.run?.finishedAt ?? null
                const indexCommit = indexState.run?.commit || repo.lastCommit
                const indexFailure = indexState.run?.errors?.[0]

                return (
                  <tr key={repo.id}>
                    <td className="px-3 py-2 align-top">
                      <div className="font-medium">{repo.name}</div>
                      <div className="break-all font-mono text-[11px] text-muted-foreground">
                        {repo.key}
                      </div>
                    </td>
                    <td className="px-3 py-2 align-top">
                      <span className="block max-w-[22rem] break-all font-mono text-xs">
                        {repo.localPath}
                      </span>
                    </td>
                    <td className="px-3 py-2 align-top">
                      <span className="break-all font-mono text-xs">{repo.fixedBranch}</span>
                    </td>
                    <td className="px-3 py-2 align-top">
                      <span className="block max-w-[16rem] break-all font-mono text-xs">
                        {repo.lastCommit || "—"}
                      </span>
                    </td>
                    <td className="px-3 py-2 align-top text-xs text-muted-foreground">
                      {repo.lastValidatedAt === null ? "未校验" : formatTime(repo.lastValidatedAt)}
                      {repo.lastValidationError ? (
                        <span className="mt-0.5 block break-all text-destructive">
                          {repo.lastValidationError}
                        </span>
                      ) : null}
                    </td>
                    <td className="px-3 py-2 align-top">
                      <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
                      {view === "done" && indexedAt !== null ? (
                        <span className="mt-0.5 block text-[11px] text-muted-foreground">
                          {formatTime(indexedAt)}
                          {indexCommit === "" ? null : (
                            <>
                              {" · "}
                              <span className="font-mono" title={indexCommit}>
                                {formatCommit(indexCommit)}
                              </span>
                            </>
                          )}
                        </span>
                      ) : null}
                      {view === "failed" ? (
                        <span className="mt-0.5 block max-w-[14rem] break-all text-[11px] text-destructive">
                          {indexFailure?.message || indexFailure?.code || "未知原因"}
                        </span>
                      ) : null}
                      {view === "queued" ? (
                        <span className="mt-0.5 block text-[11px] text-muted-foreground">
                          等待 worker 领取任务。
                        </span>
                      ) : null}
                      {view === "running" ? (
                        <span className="mt-0.5 block text-[11px] text-muted-foreground">
                          正在扫描仓库…
                        </span>
                      ) : null}
                      {view === "unindexed" ? (
                        <span className="mt-0.5 block text-[11px] text-muted-foreground">
                          尚未建立 T0 索引。
                        </span>
                      ) : null}
                    </td>
                    <td className="px-3 py-2 align-top">
                      <StatusBadge variant={statusVariant(repo.status)}>
                        {statusLabel(repo.status)}
                      </StatusBadge>
                    </td>
                    {canWrite ? (
                      <td className="px-3 py-2 align-top">
                        <div className="flex flex-wrap items-center justify-end gap-1.5">
                          <button
                            type="button"
                            onClick={() => startEdit(repo)}
                            className="inline-flex items-center gap-1 rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
                            编辑
                          </button>
                          <button
                            type="button"
                            onClick={() => void handleValidate(repo)}
                            disabled={busyId === repo.id}
                            className="inline-flex items-center gap-1 rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            <RefreshCw
                              className={`h-3.5 w-3.5 ${busyId === repo.id ? "animate-spin" : ""}`}
                              aria-hidden="true"
                            />
                            重新校验
                          </button>
                          <button
                            type="button"
                            onClick={() => void handleRefreshIndex(repo)}
                            disabled={refreshingId !== null || busyId === repo.id}
                            title={view === "failed" ? "重新触发索引" : "触发 T0 索引"}
                            className="inline-flex items-center gap-1 rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            {refreshingId === repo.id ? (
                              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                            ) : (
                              <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
                            )}
                            刷新索引
                          </button>
                          {confirmingDeleteId === repo.id ? (
                            <>
                              <button
                                type="button"
                                onClick={() => void handleDelete(repo)}
                                disabled={busyId === repo.id}
                                className="rounded-lg border border-destructive/40 bg-destructive/10 px-2.5 py-1.5 text-xs font-medium text-destructive transition-colors hover:bg-destructive/20 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                              >
                                确认删除
                              </button>
                              <button
                                type="button"
                                onClick={() => setConfirmingDeleteId(null)}
                                className="rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                              >
                                取消
                              </button>
                            </>
                          ) : (
                            <button
                              type="button"
                              onClick={() => setConfirmingDeleteId(repo.id)}
                              disabled={busyId === repo.id}
                              className="inline-flex items-center gap-1 rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            >
                              <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                              删除
                            </button>
                          )}
                        </div>
                      </td>
                    ) : null}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {indexErrorMessage === "" ? null : (
        <div
          role="alert"
          className="fixed bottom-4 right-4 z-50 flex max-w-sm items-start gap-2 rounded-xl border border-destructive/30 bg-card px-3 py-2 text-sm text-destructive shadow-lg"
        >
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          <p className="flex-1 leading-relaxed">{indexErrorMessage}</p>
          <button
            type="button"
            onClick={clearIndexError}
            aria-label="关闭提示"
            className="rounded-lg p-0.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      )}
    </div>
  )
}

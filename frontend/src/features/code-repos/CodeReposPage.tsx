"use client"

import { GitBranch, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
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

type CodeRepoForm = {
  key: string
  name: string
  localPath: string
  fixedBranch: string
  includeGlobs: string
  excludeGlobs: string
}

const EMPTY_FORM: CodeRepoForm = {
  key: "",
  name: "",
  localPath: "",
  fixedBranch: "main",
  includeGlobs: "",
  excludeGlobs: "",
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

/**
 * Admin management of local Git repositories: list, create, edit, delete and
 * re-validate. Editing never sends the immutable key. Readonly users see the
 * data without any write affordance. The index column stays a P4 placeholder
 * because P2 does not build an index.
 */
export function CodeReposPage() {
  const { user } = useAuth()
  const canWrite = user?.role === "admin"

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
      const page = await listCodeRepos()
      setRepos(page.items)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

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

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">代码仓</h1>
            {canWrite ? null : <StatusBadge variant="neutral">只读</StatusBadge>}
          </div>
          <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted-foreground">
            配置本地 Git 代码仓的路径、固定分支与扫描范围；保存与重新校验均为只读 Git
            检查，代码索引在 P4 阶段开放。
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
            onClick={() => void load()}
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
              {repos.map((repo) => (
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
                    {repo.lastIndexedAt === null ? (
                      <span className="mt-0.5 block text-[11px] text-muted-foreground">
                        未索引（P4）
                      </span>
                    ) : (
                      <span className="mt-0.5 block text-[11px] text-muted-foreground">
                        已索引：{formatTime(repo.lastIndexedAt)}
                      </span>
                    )}
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
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

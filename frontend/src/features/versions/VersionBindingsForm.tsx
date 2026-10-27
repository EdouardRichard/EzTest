"use client"

import { Loader2, Save } from "lucide-react"
import { type FormEvent, useEffect, useId, useState } from "react"

import { listCodeRepos } from "@/features/code-repos/api"
import type { CodeRepoDTO } from "@/features/code-repos/types"
import { listEnvironments, listSystems } from "@/features/resources/api"
import type { EnvironmentDTO, SystemDTO } from "@/features/resources/types"
import { ApiError } from "@/lib/api-client"

import { putBindings } from "./api"
import type { Version, VersionResource } from "./types"

const MAX_PAGE_SIZE = 100

/** Human labels for the closed binding error reasons (spec §6.8, §7.1). */
const REASON_LABELS: Record<string, string> = {
  version_state_conflict: "当前版本状态不允许修改绑定，请刷新后重试。",
  revision_conflict: "版本已被更新，请刷新后重试。",
  environment_system_mismatch: "所选环境不属于所选系统。",
  environment_not_found: "所选环境不存在或已删除。",
  system_not_found: "所选系统不存在或已删除。",
  system_disabled: "所选系统已停用。",
  code_repository_not_found: "所选代码仓不存在或已删除。",
  code_repository_disabled: "所选代码仓已停用。",
  branch_not_found: "分支不存在，请检查分支名。",
  path_not_git_repo: "仓库路径不是有效的 Git 仓库。",
  resource_unavailable: "绑定的资源已不可用，请重新选择。",
}

function describeBindingError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

function bindingLabel(binding: VersionResource | null): string {
  if (binding === null) return "未绑定"
  return binding.available ? binding.name : `${binding.name}（已删除）`
}

type BindingsFormState = {
  systemId: number | null
  environmentId: number | null
  codeRepositoryId: number | null
  branch: string
}

function formFromVersion(version: Version): BindingsFormState {
  return {
    systemId: version.systemId,
    environmentId: version.environmentId,
    codeRepositoryId: version.codeRepositoryId,
    branch: version.codeBranch ?? "",
  }
}

export type VersionBindingsFormProps = {
  version: Version
  /** Called after a successful PUT so the container can refetch the version. */
  onChanged: () => void
}

/**
 * Whole-set binding editor (spec §6.1, §6.8, §9.2). Draft/clarifying versions
 * may edit the system → environment cascade, the code repository and the
 * branch; designing and later states are read-only. The baseline commit is a
 * snapshot: it is preserved unless the repository or branch changes, in which
 * case the backend resolves a fresh one (sent as null).
 */
export function VersionBindingsForm({ version, onChanged }: VersionBindingsFormProps) {
  const uid = useId()
  const editable = version.status === "draft" || version.status === "clarifying"

  const [systems, setSystems] = useState<SystemDTO[]>([])
  const [environments, setEnvironments] = useState<EnvironmentDTO[]>([])
  const [repos, setRepos] = useState<CodeRepoDTO[]>([])
  const [resourcesFailed, setResourcesFailed] = useState(false)
  const [form, setForm] = useState<BindingsFormState>(() => formFromVersion(version))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")

  useEffect(() => {
    setForm(formFromVersion(version))
  }, [version])

  useEffect(() => {
    if (!editable) return
    let cancelled = false
    void Promise.allSettled([listSystems({ limit: MAX_PAGE_SIZE }), listCodeRepos()]).then(
      ([systemsResult, reposResult]) => {
        if (cancelled) return
        if (systemsResult.status === "fulfilled") setSystems(systemsResult.value.items)
        if (reposResult.status === "fulfilled") setRepos(reposResult.value.items)
        if (systemsResult.status === "rejected" || reposResult.status === "rejected") {
          setResourcesFailed(true)
        }
      },
    )
    return () => {
      cancelled = true
    }
  }, [editable])

  useEffect(() => {
    if (!editable || form.systemId === null) {
      setEnvironments([])
      return
    }
    let cancelled = false
    void listEnvironments(form.systemId)
      .then((items) => {
        if (!cancelled) setEnvironments(items)
      })
      .catch(() => {
        if (!cancelled) {
          setEnvironments([])
          setResourcesFailed(true)
        }
      })
    return () => {
      cancelled = true
    }
  }, [editable, form.systemId])

  function updateForm(patch: Partial<BindingsFormState>): void {
    setForm((previous) => ({ ...previous, ...patch }))
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    const branch =
      form.codeRepositoryId === null || form.branch.trim() === "" ? null : form.branch.trim()
    const unchangedSnapshot =
      form.codeRepositoryId === version.codeRepositoryId && branch === version.codeBranch
    const codeBaselineCommit =
      form.codeRepositoryId === null ? null : unchangedSnapshot ? version.codeBaselineCommit : null

    setSaving(true)
    setError("")
    setNotice("")
    try {
      await putBindings(version.id, {
        revision: version.revision,
        systemId: form.systemId,
        environmentId: form.environmentId,
        codeRepositoryId: form.codeRepositoryId,
        codeBranch: branch,
        codeBaselineCommit,
      })
      setNotice("绑定已保存。")
      onChanged()
    } catch (caught) {
      setError(describeBindingError(caught))
    } finally {
      setSaving(false)
    }
  }

  if (!editable) {
    return (
      <section
        aria-label="资源绑定"
        className="space-y-3 rounded-2xl border border-border bg-card p-4"
      >
        <h2 className="text-base font-semibold">资源绑定</h2>
        <p
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          {version.status === "designing"
            ? "设计中不可修改资源绑定。"
            : "当前版本状态不可修改资源绑定。"}
        </p>
        <dl className="grid gap-2 text-xs sm:grid-cols-2">
          <div className="flex gap-2">
            <dt className="shrink-0 text-muted-foreground">系统</dt>
            <dd className="break-all">{bindingLabel(version.bindings.system)}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-muted-foreground">环境</dt>
            <dd className="break-all">{bindingLabel(version.bindings.environment)}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-muted-foreground">代码仓</dt>
            <dd className="break-all">{bindingLabel(version.bindings.codeRepository)}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-muted-foreground">分支</dt>
            <dd className="break-all font-mono">{version.codeBranch ?? "—"}</dd>
          </div>
          <div className="flex gap-2 sm:col-span-2">
            <dt className="shrink-0 text-muted-foreground">基线 commit</dt>
            <dd className="break-all font-mono">{version.codeBaselineCommit ?? "—"}</dd>
          </div>
        </dl>
      </section>
    )
  }

  return (
    <form
      onSubmit={(event) => void handleSubmit(event)}
      aria-label="资源绑定"
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold">资源绑定</h2>
        <span className="text-xs text-muted-foreground">草稿与澄清中可修改</span>
      </div>

      {resourcesFailed ? (
        <p
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          系统、环境或代码仓列表加载失败，暂不能修改绑定。
        </p>
      ) : null}

      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <label htmlFor={`${uid}-system`} className="text-xs font-medium text-muted-foreground">
            系统
          </label>
          <select
            id={`${uid}-system`}
            value={form.systemId === null ? "" : String(form.systemId)}
            onChange={(event) => {
              updateForm({
                systemId: event.target.value === "" ? null : Number(event.target.value),
                environmentId: null,
              })
            }}
            className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">不绑定</option>
            {systems.map((system) => (
              <option key={system.id} value={system.id}>
                {system.name}
              </option>
            ))}
          </select>
        </div>

        <div>
          <label
            htmlFor={`${uid}-environment`}
            className="text-xs font-medium text-muted-foreground"
          >
            环境
          </label>
          <select
            id={`${uid}-environment`}
            value={form.environmentId === null ? "" : String(form.environmentId)}
            disabled={form.systemId === null}
            onChange={(event) => {
              updateForm({
                environmentId: event.target.value === "" ? null : Number(event.target.value),
              })
            }}
            className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
          >
            <option value="">{form.systemId === null ? "先选择系统" : "不绑定"}</option>
            {environments.map((environment) => (
              <option key={environment.id} value={environment.id}>
                {environment.name}
              </option>
            ))}
          </select>
        </div>

        <div>
          <label htmlFor={`${uid}-code-repo`} className="text-xs font-medium text-muted-foreground">
            代码仓
          </label>
          <select
            id={`${uid}-code-repo`}
            value={form.codeRepositoryId === null ? "" : String(form.codeRepositoryId)}
            onChange={(event) => {
              updateForm({
                codeRepositoryId: event.target.value === "" ? null : Number(event.target.value),
              })
            }}
            className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">不绑定</option>
            {repos.map((repo) => (
              <option key={repo.id} value={repo.id}>
                {repo.status === "active" ? repo.name : `${repo.name}（停用）`}
              </option>
            ))}
          </select>
        </div>

        <div>
          <label htmlFor={`${uid}-branch`} className="text-xs font-medium text-muted-foreground">
            分支
          </label>
          <input
            id={`${uid}-branch`}
            value={form.branch}
            maxLength={200}
            autoComplete="off"
            disabled={form.codeRepositoryId === null}
            placeholder="留空使用仓库默认分支"
            onChange={(event) => updateForm({ branch: event.target.value })}
            className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
          />
        </div>
      </div>

      <p className="text-xs text-muted-foreground">
        当前基线 commit：
        <span className="ml-1 break-all font-mono">{version.codeBaselineCommit ?? "未绑定"}</span>
        。重新绑定仓库或修改分支后由后端解析新的基线。
      </p>

      {error === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {error}
        </p>
      )}

      {notice === "" ? null : (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      )}

      <div className="flex justify-end">
        <button
          type="submit"
          disabled={saving || resourcesFailed}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {saving ? (
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          ) : (
            <Save className="h-4 w-4" aria-hidden="true" />
          )}
          {saving ? "保存中…" : "保存绑定"}
        </button>
      </div>
    </form>
  )
}

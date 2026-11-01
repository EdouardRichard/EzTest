import { RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/Button"
import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import { Spinner } from "@/components/ui/Spinner"

import { shortCommit } from "../format"
import type { CodeRepoStatusView } from "../types"

export type RepositoryStatusProps = {
  repos: CodeRepoStatusView[]
  selectedRepoId: string | null
  baselineCommit: string | null
  busy: boolean
  onSelectRepo: (repoId: string) => void
  onRefresh: () => void
}

/**
 * Repository selector for multi-SUT/multi-repo versions plus the resolved code
 * baseline commit (spec §9.2). The baseline is set on version binding or by the
 * first successful index; analysis is blocked until it exists.
 */
export function RepositoryStatus({
  repos,
  selectedRepoId,
  baselineCommit,
  busy,
  onSelectRepo,
  onRefresh,
}: RepositoryStatusProps) {
  const selected = repos.find((repo) => repo.repo_id === selectedRepoId) ?? null

  return (
    <section aria-label="代码仓库状态" className="surface-card flex flex-wrap items-end gap-4 p-4">
      <Field id="code-repo" label="代码仓库" className="min-w-56 flex-1">
        <Select
          id="code-repo"
          value={selectedRepoId ?? ""}
          disabled={busy || repos.length === 0}
          onChange={(event) => onSelectRepo(event.target.value)}
        >
          {repos.map((repo) => (
            <option key={repo.repo_id} value={repo.repo_id}>
              {repo.repository.name}
              {repo.repository.branch.length > 0 ? `（${repo.repository.branch}）` : ""}
            </option>
          ))}
        </Select>
      </Field>

      <div className="min-w-36 text-[11px]">
        <p className="text-muted-foreground">基线 commit</p>
        <p className="mt-0.5 font-mono text-xs font-medium text-foreground">
          {baselineCommit !== null ? shortCommit(baselineCommit) : "未解析"}
        </p>
        {selected !== null ? (
          <p className="mt-0.5 truncate text-muted-foreground" title={selected.repository.path}>
            {selected.repository.path}
          </p>
        ) : null}
      </div>

      <Button variant="secondary" size="sm" onClick={onRefresh} disabled={busy}>
        {busy ? (
          <span aria-hidden="true">
            <Spinner size="sm" label="刷新中" />
          </span>
        ) : (
          <RefreshCw className="size-3.5" aria-hidden="true" />
        )}
        刷新
      </Button>
    </section>
  )
}

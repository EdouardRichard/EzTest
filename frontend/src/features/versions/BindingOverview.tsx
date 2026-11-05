"use client"

import { RefreshCw } from "lucide-react"
import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import type { VersionStage } from "@/features/design/types"
import {
  bindingErrorDetails,
  getResourceCatalog,
  getVersionBinding,
  updateVersionBinding,
} from "./api"
import { VERSION_STAGE_LABEL } from "./stage"
import type {
  AccountResource,
  BindingScopeExclude,
  Catalog,
  UpdateBindingRequest,
  VersionBindingOverview,
} from "./types"
import { activeAccounts, findEnvironmentOption } from "./wizard"

export type BindingOverviewProps = {
  versionId: string
  /**
   * Optional observer invoked when the user starts inline editing. The inline
   * editor is always available while the binding is editable; a host may use
   * this to keep its own projection in sync.
   */
  onEdit?: (overview: VersionBindingOverview) => void
  /**
   * Called with the server projection after a successful save so the host can
   * apply the same refresh it applies to `version.binding.changed` SSE events.
   */
  onUpdated?: (overview: VersionBindingOverview) => void
  /** Nonce bumped by SSE (`version.binding.changed`) to re-read the overview. */
  refreshKey?: number
}

type BindingDraft = {
  repositoryIds: string[]
  accountIds: string[]
  scopeExclude: BindingScopeExclude
}

type ScopeResource = {
  id: string
  name: string
  /** Listed because it is still excluded but no longer selectable. */
  unavailable: boolean
}

function stageTone(stage: string): BadgeTone {
  if (stage === "completed") return "neutral"
  if (stage === "executing" || stage === "reporting" || stage === "paused") return "warning"
  if (stage === "blocked") return "danger"
  return "info"
}

function stageLabel(stage: string): string {
  return VERSION_STAGE_LABEL[stage as VersionStage] ?? stage
}

/** Why binding writes are currently disabled and how to make them writable again. */
function stageReadOnlyMessage(stage: string): string {
  if (stage === "completed") {
    return "已完成版本只读，如需调整绑定请克隆新版本。"
  }
  if (stage === "paused") {
    return "版本已暂停：暂停期间绑定只读。恢复后若仍处于执行/报告阶段，需回退到设计阶段才能修改绑定；改动只影响下一个 wave。"
  }
  if (stage === "blocked") {
    return "版本已阻塞：阻塞期间绑定只读。解除阻塞后若仍处于执行/报告阶段，需回退到设计阶段才能修改绑定；改动只影响下一个 wave。"
  }
  return `当前阶段为「${stageLabel(stage)}」，执行中/报告阶段需暂停或回退到设计阶段后才能修改绑定；改动只影响下一个 wave。`
}

function resourceName(resources: Array<{ id: string; name: string }>, id: string): string {
  return resources.find((resource) => resource.id === id)?.name ?? `#${id}`
}

function sameIds(left: string[], right: string[]): boolean {
  if (left.length !== right.length) return false
  const rightSet = new Set(right)
  return left.every((id) => rightSet.has(id))
}

function toggleId(ids: string[], id: string, checked: boolean): string[] {
  if (checked) return ids.includes(id) ? ids : [...ids, id]
  return ids.filter((current) => current !== id)
}

function withScopeIds(
  scope: BindingScopeExclude,
  kind: keyof BindingScopeExclude,
  ids: string[],
): BindingScopeExclude {
  if (kind === "pages") return { ...scope, pages: ids }
  if (kind === "apis") return { ...scope, apis: ids }
  return { ...scope, databases: ids }
}

function draftFromOverview(overview: VersionBindingOverview): BindingDraft {
  return {
    repositoryIds: overview.repositories
      .filter((row) => row.included)
      .map((row) => row.repository.id),
    accountIds: overview.accounts.map((account) => account.id),
    scopeExclude: {
      pages: [...overview.scope_exclude.pages],
      apis: [...overview.scope_exclude.apis],
      databases: [...overview.scope_exclude.databases],
    },
  }
}

/**
 * Builds the partial update body: only fields whose selection differs from the
 * overview are sent. `scope_exclude` is one document, so a change in any of its
 * three kinds sends all three arrays (an omitted array would mean "clear").
 */
function bindingPatch(overview: VersionBindingOverview, draft: BindingDraft): UpdateBindingRequest {
  const patch: UpdateBindingRequest = {}
  const repositoryIds = overview.repositories
    .filter((row) => row.included)
    .map((row) => row.repository.id)
  if (!sameIds(draft.repositoryIds, repositoryIds)) {
    patch.repository_ids = draft.repositoryIds
  }
  const accountIds = overview.accounts.map((account) => account.id)
  if (!sameIds(draft.accountIds, accountIds)) {
    patch.account_ids = draft.accountIds
  }
  if (
    !sameIds(draft.scopeExclude.pages, overview.scope_exclude.pages) ||
    !sameIds(draft.scopeExclude.apis, overview.scope_exclude.apis) ||
    !sameIds(draft.scopeExclude.databases, overview.scope_exclude.databases)
  ) {
    patch.scope_exclude = {
      pages: draft.scopeExclude.pages,
      apis: draft.scopeExclude.apis,
      databases: draft.scopeExclude.databases,
    }
  }
  return patch
}

/** Active accounts of the version environment from the shared catalog pool. */
function catalogAccounts(
  overview: VersionBindingOverview,
  catalog: Catalog | null,
): AccountResource[] {
  if (catalog === null) return []
  const environment = findEnvironmentOption(catalog, overview.environment.id)?.environment
  if (environment === undefined) return []
  return activeAccounts(environment)
}

/**
 * Selectable accounts: the catalog's active pool merged with the currently bound
 * accounts. `GET /binding` only returns the bound subset, so the pool is what
 * makes a removed account selectable again; the bound accounts are kept as rows
 * even when the pool read failed or they are missing from it, so they stay
 * removable instead of being re-sent invisibly.
 */
function accountOptions(
  overview: VersionBindingOverview,
  catalog: Catalog | null,
): AccountResource[] {
  const options: AccountResource[] = []
  const seen = new Set<string>()
  for (const account of [...catalogAccounts(overview, catalog), ...overview.accounts]) {
    if (seen.has(account.id)) continue
    seen.add(account.id)
    options.push(account)
  }
  return options
}

/**
 * Selectable resources for one exclusion kind: the approved/active environment
 * resources plus every currently excluded id. An id that was excluded while
 * selectable and has since become proposed/disabled (or vanished) still gets a
 * row, so it can be removed instead of being re-sent with every scope patch.
 */
function scopeResources(
  overview: VersionBindingOverview,
  kind: keyof BindingScopeExclude,
  excludedIds: string[],
): ScopeResource[] {
  let resources: Array<{ id: string; name: string; status: string }>
  if (kind === "pages") resources = overview.environment.pages
  else if (kind === "apis") resources = overview.environment.apis
  else resources = overview.environment.databases
  const selectableStatus = kind === "databases" ? "active" : "approved"
  const options: ScopeResource[] = []
  const seen = new Set<string>()
  for (const resource of resources) {
    if (resource.status !== selectableStatus) continue
    seen.add(resource.id)
    options.push({ id: resource.id, name: resource.name, unavailable: false })
  }
  for (const id of excludedIds) {
    if (seen.has(id)) continue
    seen.add(id)
    options.push({
      id,
      name: resources.find((resource) => resource.id === id)?.name ?? `#${id}`,
      unavailable: true,
    })
  }
  return options
}

type ChipRowProps = {
  kind: string
  ids: string[]
  resources: Array<{ id: string; name: string }>
}

function ExclusionRow({ kind, ids, resources }: ChipRowProps) {
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
      <span className="w-14 shrink-0 text-muted-foreground">{kind}</span>
      {ids.length === 0 ? (
        <span className="text-muted-foreground">未排除</span>
      ) : (
        ids.map((id) => (
          <Badge key={id} tone="warning">
            {resourceName(resources, id)}
          </Badge>
        ))
      )}
    </div>
  )
}

type ScopeEditorProps = {
  label: string
  options: ScopeResource[]
  excluded: string[]
  disabled: boolean
  onToggle: (id: string, checked: boolean) => void
}

function ScopeEditor({ label, options, excluded, disabled, onToggle }: ScopeEditorProps) {
  return (
    <fieldset className="space-y-1">
      <legend className="text-[11px] font-medium text-muted-foreground">{label}</legend>
      {options.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">环境没有可排除的{label}</p>
      ) : (
        <ul className="grid gap-1 sm:grid-cols-2">
          {options.map((option) => {
            const unavailableSuffix = option.unavailable ? "（不可用/未批准）" : ""
            return (
              <li key={option.id}>
                <label className="flex items-center gap-1.5 text-xs text-foreground">
                  <input
                    type="checkbox"
                    className="size-3.5 accent-primary"
                    aria-label={`排除${label} ${option.name}${unavailableSuffix}`}
                    checked={excluded.includes(option.id)}
                    disabled={disabled}
                    onChange={(event) => onToggle(option.id, event.target.checked)}
                  />
                  <span>{option.name}</span>
                  {option.unavailable ? (
                    <span className="text-[11px] text-muted-foreground">
                      已排除{unavailableSuffix}
                    </span>
                  ) : null}
                </label>
              </li>
            )
          })}
        </ul>
      )}
    </fieldset>
  )
}

type CountCardProps = {
  label: string
  counts: { approved: number; proposed: number; excluded: number; included: number }
}

function CountCard({ label, counts }: CountCardProps) {
  return (
    <div className="rounded-lg border border-border bg-card p-3">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className="mt-1 text-lg font-semibold text-foreground">{counts.included}</p>
      <p className="text-[11px] text-muted-foreground">
        已确认 {counts.approved} · 待确认 {counts.proposed} · 排除 {counts.excluded}
      </p>
    </div>
  )
}

/**
 * v0.22 SUT binding overview (R-C4-1/FE-5): the derived SUT/environment, the
 * effective repositories with roles and overrides, the enabled account subset,
 * the scope exclusion chips and the stage gate. While the backend gate reports
 * `editable`, the overview switches to an inline editor whose checkboxes map to
 * the partial binding update (repository_ids, account_ids, scope_exclude);
 * completed/non-editable versions stay read-only with the stage explanation.
 *
 * The editor's account rows come from the catalog's global active pool (the same
 * source as the wizard) so a removed account can be re-added; `GET /binding` only
 * exposes the bound subset. Scope rows also list still-excluded resources that
 * are no longer approved/active, so a stale exclusion stays removable.
 */
export function BindingOverview({
  versionId,
  onEdit,
  onUpdated,
  refreshKey = 0,
}: BindingOverviewProps) {
  const [overview, setOverview] = useState<VersionBindingOverview | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<BindingDraft | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<unknown>(null)
  // A 409 stage-gate rejection keeps the overview read-only until the next read.
  const [gateMessage, setGateMessage] = useState<string | null>(null)
  // The account pool is not part of the binding read; load it from the catalog
  // the first time the editor opens.
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [catalogLoading, setCatalogLoading] = useState(false)
  const [catalogError, setCatalogError] = useState<unknown>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE-driven reload nonce and reloadToken the explicit refresh counter; both intentionally retrigger the read.
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    setGateMessage(null)
    void getVersionBinding(versionId)
      .then((value) => {
        if (cancelled) return
        setOverview(value)
        // The server gate is authoritative: a refresh that reports a closed gate
        // must also leave any open inline editor.
        if (!value.editable || value.stage === "completed") {
          setEditing(false)
          setDraft(null)
        }
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setOverview(null)
          setError(cause)
          setEditing(false)
          setDraft(null)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, refreshKey, reloadToken])

  if (loading) {
    return <LoadingCard label="正在加载绑定概览…" />
  }

  if (error !== null) {
    return (
      <ErrorState
        title="绑定概览加载失败"
        error={error}
        onRetry={() => setReloadToken((current) => current + 1)}
      />
    )
  }

  if (overview === null) {
    return <LoadingCard label="正在加载绑定概览…" />
  }

  const canEdit = overview.editable && overview.stage !== "completed" && gateMessage === null
  const readOnly = !canEdit
  const editOpen = editing && draft !== null
  const hasChanges = draft !== null && Object.keys(bindingPatch(overview, draft)).length > 0
  const excludedTotal =
    overview.scope_exclude.pages.length +
    overview.scope_exclude.apis.length +
    overview.scope_exclude.databases.length
  const accounts = accountOptions(overview, catalog)

  /**
   * Reads the global account pool the wizard also uses. `GET /binding` only
   * returns the bound subset, so this is the only way a removed account becomes
   * selectable again. A failed read is non-fatal: the bound accounts stay
   * removable and the section explains the limitation.
   */
  function loadAccountPool(): void {
    if (catalog !== null || catalogLoading) return
    setCatalogLoading(true)
    setCatalogError(null)
    void getResourceCatalog()
      .then((value) => {
        setCatalog(value)
      })
      .catch((cause: unknown) => {
        setCatalogError(cause)
      })
      .finally(() => {
        setCatalogLoading(false)
      })
  }

  function startEditing(): void {
    if (overview === null) return
    loadAccountPool()
    setDraft(draftFromOverview(overview))
    setSaveError(null)
    setEditing(true)
    onEdit?.(overview)
  }

  function cancelEditing(): void {
    setEditing(false)
    setDraft(null)
    setSaveError(null)
  }

  function toggleScope(kind: keyof BindingScopeExclude, id: string, checked: boolean): void {
    setDraft((current) => {
      if (current === null) return current
      return {
        ...current,
        scopeExclude: withScopeIds(
          current.scopeExclude,
          kind,
          toggleId(current.scopeExclude[kind], id, checked),
        ),
      }
    })
  }

  async function handleSave(): Promise<void> {
    if (overview === null || draft === null || saving) return
    const patch = bindingPatch(overview, draft)
    if (Object.keys(patch).length === 0) return
    setSaving(true)
    setSaveError(null)
    try {
      const next = await updateVersionBinding(versionId, patch)
      setOverview(next)
      setEditing(false)
      setDraft(null)
      setGateMessage(null)
      onUpdated?.(next)
    } catch (cause: unknown) {
      const details = bindingErrorDetails(cause)
      if (details?.readOnly === true) {
        // The stage closed between the read and the write: drop the editor so the
        // stale draft cannot be saved, and keep the gate explanation visible.
        setGateMessage(details.message)
        setEditing(false)
        setDraft(null)
      } else {
        setSaveError(cause)
      }
    } finally {
      setSaving(false)
    }
  }

  return (
    <section aria-label="SUT 绑定概览" className="space-y-3">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">SUT 绑定概览</h2>
          <p className="mt-1 text-[11px] text-muted-foreground">SUT：{overview.sut.name}</p>
          <p className="text-[11px] text-muted-foreground">
            环境：{overview.environment.name}（{overview.environment.base_url}）
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone={stageTone(overview.stage)}>{stageLabel(overview.stage)}</Badge>
          {canEdit && !editOpen ? (
            <Button variant="secondary" size="sm" onClick={startEditing}>
              编辑绑定
            </Button>
          ) : null}
          <Button
            variant="ghost"
            size="sm"
            aria-label="刷新绑定"
            onClick={() => setReloadToken((current) => current + 1)}
          >
            <RefreshCw className="size-3.5" aria-hidden="true" />
          </Button>
        </div>
      </header>

      {gateMessage !== null ? (
        <Alert tone="warning" title="当前阶段绑定只读">
          保存未生效：{gateMessage}。绑定继续保持只读，请暂停版本或回退到设计阶段后再修改。
        </Alert>
      ) : readOnly ? (
        <Alert tone="warning" title="当前阶段绑定只读">
          {stageReadOnlyMessage(overview.stage)}
        </Alert>
      ) : null}

      <div className="grid gap-2 sm:grid-cols-3">
        <CountCard label="页面" counts={overview.counts.pages} />
        <CountCard label="API" counts={overview.counts.apis} />
        <CountCard label="目标库" counts={overview.counts.databases} />
      </div>

      <section aria-label="代码仓" className="rounded-xl border border-border bg-card p-3">
        <h3 className="mb-2 text-xs font-semibold text-foreground">代码仓（角色 / 覆盖）</h3>
        {overview.repositories.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">该 SUT 未关联代码仓</p>
        ) : editOpen && draft !== null ? (
          <ul className="space-y-1.5">
            {overview.repositories.map((row) => (
              <li key={row.repository.id}>
                <label className="flex flex-wrap items-center gap-1.5 text-xs text-foreground">
                  <input
                    type="checkbox"
                    className="size-3.5 accent-primary"
                    aria-label={`包含代码仓 ${row.repository.name}`}
                    checked={draft.repositoryIds.includes(row.repository.id)}
                    disabled={saving}
                    onChange={(event) => {
                      const checked = event.target.checked
                      setDraft((current) =>
                        current === null
                          ? current
                          : {
                              ...current,
                              repositoryIds: toggleId(
                                current.repositoryIds,
                                row.repository.id,
                                checked,
                              ),
                            },
                      )
                    }}
                  />
                  <span className="font-medium">{row.repository.name}</span>
                  {row.repository.branch ? (
                    <span className="font-mono text-[11px] text-muted-foreground">
                      {row.repository.branch}
                    </span>
                  ) : null}
                  {row.role ? <Badge tone="neutral">{row.role}</Badge> : null}
                  <Badge tone={row.source === "override" ? "info" : "neutral"}>
                    {row.source === "override" ? "版本覆盖" : "SUT 继承"}
                  </Badge>
                </label>
              </li>
            ))}
          </ul>
        ) : (
          <ul className="space-y-1.5">
            {overview.repositories.map((row) => (
              <li
                key={row.repository.id}
                className="flex flex-wrap items-center gap-1.5 text-xs text-foreground"
              >
                <span className="font-medium">{row.repository.name}</span>
                {row.repository.branch ? (
                  <span className="font-mono text-[11px] text-muted-foreground">
                    {row.repository.branch}
                  </span>
                ) : null}
                {row.role ? <Badge tone="neutral">{row.role}</Badge> : null}
                <Badge tone={row.source === "override" ? "info" : "neutral"}>
                  {row.source === "override" ? "版本覆盖" : "SUT 继承"}
                </Badge>
                {row.included ? (
                  <Badge tone="success">已包含</Badge>
                ) : (
                  <Badge tone="warning">已排除</Badge>
                )}
              </li>
            ))}
          </ul>
        )}
        {overview.code_baseline_commit ? (
          <p className="mt-2 font-mono text-[11px] text-muted-foreground">
            代码基线：{overview.code_baseline_commit}
          </p>
        ) : null}
      </section>

      <section aria-label="测试账号" className="rounded-xl border border-border bg-card p-3">
        <h3 className="mb-1 text-xs font-semibold text-foreground">测试账号</h3>
        <p className="mb-2 text-[11px] text-muted-foreground">
          {editOpen ? "全局测试账号池（仅启用账号）：勾选加入，取消勾选移除。" : "仅显示启用账号"}
        </p>
        {overview.accounts.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">未关联测试账号（执行将被拒绝）</p>
        ) : null}
        {editOpen && draft !== null ? (
          <>
            {accounts.length > 0 ? (
              <ul className="mt-1 grid gap-1 sm:grid-cols-2">
                {accounts.map((account) => (
                  <li key={account.id}>
                    <label className="flex flex-wrap items-center gap-1.5 text-xs text-foreground">
                      <input
                        type="checkbox"
                        className="size-3.5 accent-primary"
                        aria-label={`启用账号 ${account.username}`}
                        checked={draft.accountIds.includes(account.id)}
                        disabled={saving}
                        onChange={(event) => {
                          const checked = event.target.checked
                          setDraft((current) =>
                            current === null
                              ? current
                              : {
                                  ...current,
                                  accountIds: toggleId(current.accountIds, account.id, checked),
                                },
                          )
                        }}
                      />
                      <span className="font-medium">{account.username}</span>
                      {account.roles.length > 0 ? (
                        <span className="text-muted-foreground">· {account.roles.join(" / ")}</span>
                      ) : null}
                    </label>
                  </li>
                ))}
              </ul>
            ) : catalogLoading ? null : (
              <p className="text-[11px] text-muted-foreground">账号池暂无可用账号。</p>
            )}
            {catalogLoading ? (
              <p className="mt-1 text-[11px] text-muted-foreground">正在加载测试账号池…</p>
            ) : null}
            {catalogError !== null ? (
              <p className="mt-1 text-[11px] text-muted-foreground">
                测试账号池读取失败，暂时只能移除已绑定账号。
              </p>
            ) : null}
          </>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {overview.accounts.map((account) => (
              <Badge key={account.id} tone="info">
                {account.username}
                {account.roles.length > 0 ? ` · ${account.roles.join(" / ")}` : ""}
              </Badge>
            ))}
          </div>
        )}
      </section>

      <section aria-label="范围排除" className="rounded-xl border border-border bg-card p-3">
        <h3 className="mb-2 text-xs font-semibold text-foreground">范围排除</h3>
        {editOpen && draft !== null ? (
          <div className="space-y-2">
            <ScopeEditor
              label="页面"
              options={scopeResources(overview, "pages", overview.scope_exclude.pages)}
              excluded={draft.scopeExclude.pages}
              disabled={saving}
              onToggle={(id, checked) => toggleScope("pages", id, checked)}
            />
            <ScopeEditor
              label="API"
              options={scopeResources(overview, "apis", overview.scope_exclude.apis)}
              excluded={draft.scopeExclude.apis}
              disabled={saving}
              onToggle={(id, checked) => toggleScope("apis", id, checked)}
            />
            <ScopeEditor
              label="目标库"
              options={scopeResources(overview, "databases", overview.scope_exclude.databases)}
              excluded={draft.scopeExclude.databases}
              disabled={saving}
              onToggle={(id, checked) => toggleScope("databases", id, checked)}
            />
          </div>
        ) : (
          <>
            {excludedTotal === 0 ? (
              <p className="mb-1.5 text-[11px] text-muted-foreground">
                未设置排除项（默认包含全部）
              </p>
            ) : null}
            <div className="space-y-1.5">
              <ExclusionRow
                kind="页面"
                ids={overview.scope_exclude.pages}
                resources={overview.environment.pages}
              />
              <ExclusionRow
                kind="API"
                ids={overview.scope_exclude.apis}
                resources={overview.environment.apis}
              />
              <ExclusionRow
                kind="目标库"
                ids={overview.scope_exclude.databases}
                resources={overview.environment.databases}
              />
            </div>
          </>
        )}
      </section>

      {editOpen ? (
        <div
          data-testid="binding-edit-footer"
          className="space-y-2 rounded-xl border border-border bg-card p-3"
        >
          {saveError !== null ? <InlineError error={saveError} /> : null}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-[11px] text-muted-foreground">
              只提交有变化的字段；取消不会发出请求。
            </p>
            <div className="flex items-center gap-2">
              <Button variant="secondary" size="sm" disabled={saving} onClick={cancelEditing}>
                取消编辑
              </Button>
              <Button
                variant="primary"
                size="sm"
                disabled={saving || !hasChanges}
                onClick={() => {
                  void handleSave()
                }}
              >
                {saving ? "保存中…" : "保存绑定"}
              </Button>
            </div>
          </div>
        </div>
      ) : null}
    </section>
  )
}

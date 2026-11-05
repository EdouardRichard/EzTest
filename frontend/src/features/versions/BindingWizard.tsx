"use client"

import { RefreshCw } from "lucide-react"
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ErrorState } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"
import type { VersionStage } from "@/features/design/types"

import {
  bindingErrorDetails,
  createVersion,
  getResourceCatalog,
  getVersionBinding,
  listSutRepositories,
  updateVersionBinding,
} from "./api"
import { stageLabel } from "./stage"
import type {
  AccountResource,
  BindingScopeExclude,
  Catalog,
  SutRepository,
  SutResource,
  Version,
  VersionBindingOverview,
} from "./types"
import {
  activeAccounts,
  approvedAPIs,
  approvedPages,
  buildBindingUpdate,
  buildCreateVersionPayload,
  emptyScopeExclude,
  findEnvironmentOption,
  resourceCounts,
  scopeDatabases,
  stepForBindingField,
  toggleExclude,
  toggleSelection,
} from "./wizard"

export type BindingWizardProps = {
  /** Called with the created version after the binding PUT succeeds. */
  onCreated?: (version: Version) => void
  /** Optional cancel affordance (hosted by the create dialog). */
  onCancel?: () => void
}

type Step = 1 | 2 | 3 | 4

type ScopeOption = {
  id: string
  label: string
  hint?: string
}

function stepTitle(step: Step): string {
  if (step === 1) return "版本信息"
  if (step === 2) return "代码仓与测试账号"
  if (step === 3) return "范围与排除"
  return "确认创建"
}

/** Baseline (approved) plus the live effective counts of one resource kind. */
function scopeSummary(counts: {
  approved: number
  included: number
  excluded: number
  proposed: number
}): string {
  return `已确认 ${counts.approved} · 生效 ${counts.included} · 排除 ${counts.excluded} · 待确认 ${counts.proposed}`
}

type ScopeGroupProps = {
  legend: string
  options: ScopeOption[]
  excluded: string[]
  onToggle: (id: string) => void
  emptyText: string
}

/** One include/exclude list; unchecked means "excluded from this version". */
function ScopeGroup({ legend, options, excluded, onToggle, emptyText }: ScopeGroupProps) {
  return (
    <fieldset className="rounded-lg border border-border bg-muted/30 p-3">
      <legend className="px-1 text-xs font-medium text-foreground">{legend}</legend>
      {options.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">{emptyText}</p>
      ) : (
        <div className="grid gap-1.5 sm:grid-cols-2">
          {options.map((option) => {
            const included = !excluded.includes(option.id)
            return (
              <label
                key={option.id}
                className="flex cursor-pointer items-start gap-2 text-xs text-foreground"
              >
                <input
                  type="checkbox"
                  className="mt-0.5 size-3.5 accent-primary"
                  checked={included}
                  onChange={() => onToggle(option.id)}
                />
                <span className="min-w-0">
                  <span className="font-medium">{option.label}</span>
                  {option.hint ? (
                    <span className="ml-1 break-all text-[11px] text-muted-foreground">
                      {option.hint}
                    </span>
                  ) : null}
                </span>
              </label>
            )
          })}
        </div>
      )}
    </fieldset>
  )
}

type VersionOnlyRepository = { id: string; name: string; branch: string }

type RepositorySectionProps = {
  repositories: SutRepository[] | null
  loading: boolean
  error: unknown
  selectedIds: string[] | null
  versionOnlyRepositories: VersionOnlyRepository[]
  onToggle: (repoId: string) => void
  onRetry: () => void
  onRefresh: () => void
}

function RepositorySection({
  repositories,
  loading,
  error,
  selectedIds,
  versionOnlyRepositories,
  onToggle,
  onRetry,
  onRefresh,
}: RepositorySectionProps) {
  return (
    <fieldset className="rounded-xl border border-border bg-card p-3">
      <legend className="px-1 text-xs font-medium text-foreground">代码仓</legend>
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <p className="text-[11px] text-muted-foreground">
          默认继承 SUT 的代码仓；取消勾选只会为本版本排除，不会修改 SUT 关联。
        </p>
        <Button variant="ghost" size="sm" onClick={onRefresh} disabled={loading}>
          <RefreshCw className="size-3.5" aria-hidden="true" />
          刷新代码仓
        </Button>
      </div>

      {loading ? (
        <p role="status" className="text-[11px] text-muted-foreground">
          正在加载代码仓…
        </p>
      ) : null}

      {error ? (
        <Alert tone="warning" title="SUT 代码仓读取失败">
          <p>{error instanceof Error ? error.message : "代码仓暂不可用"}</p>
          <p className="mt-1 text-[11px]">创建后会继续继承 SUT 的代码仓；接口恢复后可在此调整。</p>
          <div className="mt-2">
            <Button variant="secondary" size="sm" onClick={onRetry}>
              重试
            </Button>
          </div>
        </Alert>
      ) : null}

      {!loading && !error && repositories !== null && repositories.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">该 SUT 尚未关联代码仓。</p>
      ) : null}

      {repositories !== null && repositories.length > 0 ? (
        <ul className="space-y-1.5">
          {repositories.map((row) => {
            const included = selectedIds?.includes(row.repository.id) ?? false
            return (
              <li key={row.id}>
                <label className="flex cursor-pointer items-start gap-2 text-xs text-foreground">
                  <input
                    type="checkbox"
                    className="mt-0.5 size-3.5 accent-primary"
                    checked={included}
                    onChange={() => onToggle(row.repository.id)}
                  />
                  <span className="min-w-0">
                    <span className="font-medium">{row.repository.name}</span>
                    {row.repository.branch ? (
                      <span className="ml-1 font-mono text-[11px] text-muted-foreground">
                        {row.repository.branch}
                      </span>
                    ) : null}
                    {row.role ? (
                      <Badge tone="neutral" className="ml-1">
                        {row.role}
                      </Badge>
                    ) : null}
                    <Badge tone="neutral" className="ml-1">
                      SUT 继承
                    </Badge>
                    {included ? null : (
                      <Badge tone="warning" className="ml-1">
                        已排除
                      </Badge>
                    )}
                  </span>
                </label>
              </li>
            )
          })}
        </ul>
      ) : null}

      {!error && !loading && versionOnlyRepositories.length > 0 ? (
        <div className="mt-3 border-t border-border pt-2">
          <p className="text-[11px] font-medium text-foreground">其他启用代码仓（仅本版本）</p>
          <p className="mb-1.5 text-[11px] text-muted-foreground">
            未关联到 SUT 的代码仓只会以「版本覆盖」加入本版本，不会影响 SUT 或后续版本。
          </p>
          <div className="grid gap-1.5 sm:grid-cols-2">
            {versionOnlyRepositories.map((repository) => (
              <label
                key={repository.id}
                className="flex cursor-pointer items-start gap-2 text-xs text-foreground"
              >
                <input
                  type="checkbox"
                  className="mt-0.5 size-3.5 accent-primary"
                  checked={selectedIds?.includes(repository.id) ?? false}
                  onChange={() => onToggle(repository.id)}
                />
                <span className="min-w-0">
                  <span className="font-medium">{repository.name}</span>
                  {repository.branch ? (
                    <span className="ml-1 font-mono text-[11px] text-muted-foreground">
                      {repository.branch}
                    </span>
                  ) : null}
                  <Badge tone="info" className="ml-1">
                    版本覆盖
                  </Badge>
                </span>
              </label>
            ))}
          </div>
        </div>
      ) : null}
    </fieldset>
  )
}

type AccountSectionProps = {
  accounts: AccountResource[]
  selectedIds: string[] | null
  onToggle: (accountId: string) => void
}

function AccountSection({ accounts, selectedIds, onToggle }: AccountSectionProps) {
  return (
    <fieldset className="rounded-xl border border-border bg-card p-3">
      <legend className="px-1 text-xs font-medium text-foreground">测试账号</legend>
      <p className="mb-2 text-[11px] text-muted-foreground">
        全局测试账号池 · 仅显示启用账号，不展示密码；未关联的账号不能用于执行。
      </p>
      {accounts.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">全局账号池暂无可用账号。</p>
      ) : (
        <div className="grid gap-1.5 sm:grid-cols-2">
          {accounts.map((account) => (
            <label
              key={account.id}
              className="flex cursor-pointer items-start gap-2 text-xs text-foreground"
            >
              <input
                type="checkbox"
                className="mt-0.5 size-3.5 accent-primary"
                checked={selectedIds?.includes(account.id) ?? false}
                onChange={() => onToggle(account.id)}
              />
              <span className="min-w-0">
                <span className="font-medium">{account.username}</span>
                {account.roles.length > 0 ? (
                  <span className="ml-1 text-[11px] text-muted-foreground">
                    {account.roles.join(" / ")}
                  </span>
                ) : null}
              </span>
            </label>
          ))}
        </div>
      )}
    </fieldset>
  )
}

type ReviewRowProps = {
  label: string
  children: ReactNode
}

function ReviewRow({ label, children }: ReviewRowProps) {
  return (
    <div className="grid gap-0.5 sm:grid-cols-[7rem_minmax(0,1fr)]">
      <dt className="text-[11px] text-muted-foreground">{label}</dt>
      <dd className="text-xs text-foreground">{children}</dd>
    </div>
  )
}

/**
 * v0.22 environment-only create wizard (R-C3-1..R-C3-4/API-15/API-16).
 *
 * Step 1 picks the version metadata and one environment; the SUT is derived.
 * Step 2 reviews SUT repositories and the global account threshold. Step 3
 * records `scope_exclude` for pages/APIs/databases (additive: resources are
 * never rewritten). Step 4 creates the version with the environment-only body
 * and then saves the binding through `PUT /api/versions/{id}/binding`.
 */
export function BindingWizard({ onCreated, onCancel }: BindingWizardProps) {
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [catalogLoading, setCatalogLoading] = useState(true)
  const [catalogError, setCatalogError] = useState<unknown>(null)

  const [step, setStep] = useState<Step>(1)
  const [title, setTitle] = useState("")
  const [description, setDescription] = useState("")
  const [goal, setGoal] = useState("")
  const [environmentId, setEnvironmentId] = useState("")
  const [titleError, setTitleError] = useState("")
  const [environmentError, setEnvironmentError] = useState("")

  const [repositories, setRepositories] = useState<SutRepository[] | null>(null)
  const [repositoryLoading, setRepositoryLoading] = useState(false)
  const [repositoryError, setRepositoryError] = useState<unknown>(null)
  const [selectedRepositoryIds, setSelectedRepositoryIds] = useState<string[] | null>(null)
  const repositoryRequest = useRef(0)

  const [selectedAccountIds, setSelectedAccountIds] = useState<string[] | null>(null)
  const [scopeExclude, setScopeExclude] = useState<BindingScopeExclude>(emptyScopeExclude)

  const [submitting, setSubmitting] = useState(false)
  const [submitPhase, setSubmitPhase] = useState<"create" | "binding" | null>(null)
  const [submitError, setSubmitError] = useState<unknown>(null)
  const [createdVersion, setCreatedVersion] = useState<Version | null>(null)
  const [savedVersion, setSavedVersion] = useState<Version | null>(null)
  const [refreshingBinding, setRefreshingBinding] = useState(false)
  const [refreshedBinding, setRefreshedBinding] = useState<VersionBindingOverview | null>(null)
  const [bindingRefreshError, setBindingRefreshError] = useState<unknown>(null)

  const loadCatalog = useCallback(async () => {
    setCatalogLoading(true)
    setCatalogError(null)
    try {
      setCatalog(await getResourceCatalog())
    } catch (error) {
      setCatalog(null)
      setCatalogError(error)
    } finally {
      setCatalogLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadCatalog()
  }, [loadCatalog])

  const environmentOption =
    catalog === null ? undefined : findEnvironmentOption(catalog, environmentId)
  const derivedSut: SutResource | undefined = environmentOption?.sut
  const environment = environmentOption?.environment
  const accounts = environment ? activeAccounts(environment) : []
  const environmentAccountsAvailable = environment !== undefined

  const loadRepositories = useCallback(async (sutId: string) => {
    const requestId = ++repositoryRequest.current
    setRepositoryLoading(true)
    setRepositoryError(null)
    try {
      const rows = await listSutRepositories(sutId)
      if (requestId !== repositoryRequest.current) return
      setRepositories(rows)
      setSelectedRepositoryIds(rows.filter((row) => row.enabled).map((row) => row.repository.id))
    } catch (error) {
      if (requestId !== repositoryRequest.current) return
      setRepositories(null)
      setSelectedRepositoryIds(null)
      setRepositoryError(error)
    } finally {
      if (requestId === repositoryRequest.current) setRepositoryLoading(false)
    }
  }, [])

  function selectEnvironment(nextEnvironmentId: string): void {
    setEnvironmentId(nextEnvironmentId)
    setEnvironmentError("")
    setRepositoryError(null)
    setRepositories(null)
    setSelectedRepositoryIds(null)
    setScopeExclude(emptyScopeExclude())

    const option = catalog === null ? undefined : findEnvironmentOption(catalog, nextEnvironmentId)
    if (option === undefined) {
      setSelectedAccountIds(null)
      return
    }
    setSelectedAccountIds(activeAccounts(option.environment).map((account) => account.id))
    void loadRepositories(option.sut.id)
  }

  function toggleRepository(repositoryId: string): void {
    setSelectedRepositoryIds((current) => toggleSelection(current ?? [], repositoryId))
  }

  function toggleAccount(accountId: string): void {
    setSelectedAccountIds((current) => toggleSelection(current ?? [], accountId))
  }

  function toggleScope(kind: keyof BindingScopeExclude, id: string): void {
    setScopeExclude((current) => toggleExclude(current, kind, id))
  }

  function goTo(nextStep: Step): void {
    setSubmitError(null)
    setSubmitPhase(null)
    setRefreshedBinding(null)
    setBindingRefreshError(null)
    setStep(nextStep)
  }

  function next(): void {
    if (step === 1) {
      let valid = true
      if (title.trim().length === 0) {
        setTitleError("请输入版本名称")
        valid = false
      }
      if (environmentId === "") {
        setEnvironmentError("请选择环境")
        valid = false
      }
      if (!valid) return
      setTitleError("")
      setEnvironmentError("")
    }
    if (step < 4) goTo((step + 1) as Step)
  }

  function previous(): void {
    if (step > 1) goTo((step - 1) as Step)
  }

  async function refreshCreatedBinding(): Promise<void> {
    if (createdVersion === null) return
    setRefreshingBinding(true)
    setBindingRefreshError(null)
    try {
      setRefreshedBinding(await getVersionBinding(createdVersion.id))
    } catch (error) {
      setRefreshedBinding(null)
      setBindingRefreshError(error)
    } finally {
      setRefreshingBinding(false)
    }
  }

  async function createAndBind(): Promise<void> {
    if (submitting) return
    setSubmitting(true)
    setSubmitError(null)
    setBindingRefreshError(null)
    setRefreshedBinding(null)
    try {
      let version = createdVersion
      if (version === null) {
        setSubmitPhase("create")
        version = await createVersion(
          buildCreateVersionPayload({
            title,
            description,
            goal,
            environmentId,
          }),
        )
        setCreatedVersion(version)
      }
      setSubmitPhase("binding")
      await updateVersionBinding(
        version.id,
        buildBindingUpdate({
          includedRepositoryIds: selectedRepositoryIds,
          accountIds: environmentAccountsAvailable ? selectedAccountIds : null,
          scopeExclude,
        }),
      )
      setSavedVersion(version)
      onCreated?.(version)
    } catch (error) {
      setSubmitError(error)
    } finally {
      setSubmitting(false)
    }
  }

  if (catalogLoading) {
    return <LoadingCard label="正在加载资源目录…" />
  }

  if (catalogError !== null) {
    return (
      <ErrorState
        title="资源目录加载失败"
        error={catalogError}
        onRetry={() => void loadCatalog()}
      />
    )
  }

  if (catalog === null) {
    return <LoadingCard label="正在加载资源目录…" />
  }

  if (savedVersion !== null) {
    return (
      <section aria-label="版本创建结果" className="space-y-3">
        <Alert tone="success" title="版本已创建">
          版本 #{savedVersion.id} 已创建，绑定已保存。
        </Alert>
        {onCancel ? (
          <div className="flex justify-end">
            <Button variant="secondary" onClick={onCancel}>
              完成
            </Button>
          </div>
        ) : null}
      </section>
    )
  }

  const counts = environment
    ? {
        pages: resourceCounts(environment.pages, scopeExclude.pages),
        apis: resourceCounts(environment.apis, scopeExclude.apis),
        databases: resourceCounts(scopeDatabases(environment), scopeExclude.databases),
      }
    : null
  const pageCounts = counts?.pages
  const apiCounts = counts?.apis
  const databaseCounts = counts?.databases
  const proposedTotal = (pageCounts?.proposed ?? 0) + (apiCounts?.proposed ?? 0)

  const scopePages: ScopeOption[] = environment
    ? approvedPages(environment).map((page) => ({
        id: page.id,
        label: page.name,
        hint: page.route_template,
      }))
    : []
  const scopeApis: ScopeOption[] = environment
    ? approvedAPIs(environment).map((api) => ({
        id: api.id,
        label: api.name,
        hint: `${api.method} ${api.path_template}`,
      }))
    : []
  const scopeDatabaseOptions: ScopeOption[] = environment
    ? scopeDatabases(environment).map((database) => ({
        id: database.id,
        label: database.name,
        hint: database.read_only ? "只读" : "可写",
      }))
    : []

  const associatedRepositoryIds = new Set((repositories ?? []).map((row) => row.repository.id))
  const versionOnlyRepositories = catalog.repositories
    .filter((repository) => repository.status === "active")
    .filter((repository) => !associatedRepositoryIds.has(repository.id))
    .map((repository) => ({
      id: repository.id,
      name: repository.name,
      branch: repository.branch,
    }))

  const selectedRepositoryRows =
    selectedRepositoryIds === null
      ? []
      : selectedRepositoryIds.flatMap((id) => {
          const inherited = repositories?.find((row) => row.repository.id === id)
          if (inherited !== undefined) {
            return [
              {
                label: `${inherited.repository.name}${
                  inherited.role ? `（${inherited.role}）` : ""
                }`,
              },
            ]
          }
          const repository = catalog.repositories.find((item) => item.id === id)
          return repository === undefined ? [] : [{ label: `${repository.name}（版本覆盖）` }]
        })
  const selectedAccounts = accounts.filter(
    (account) => selectedAccountIds?.includes(account.id) ?? false,
  )

  const submitDetails = submitError === null ? null : bindingErrorDetails(submitError)
  const submitFieldStep =
    submitDetails !== null && submitDetails.field !== ""
      ? stepForBindingField(submitDetails.field)
      : null
  const submitReadOnly = submitPhase === "binding" && submitDetails?.readOnly === true
  const submitTitle = submitReadOnly
    ? "绑定当前只读（409）"
    : submitPhase === "create"
      ? "创建版本失败"
      : "保存绑定失败"

  return (
    <section aria-label="版本创建向导" className="space-y-3">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <p className="text-[11px] font-medium text-muted-foreground">步骤 {step} / 4</p>
          <h2 className="text-sm font-semibold text-foreground">{stepTitle(step)}</h2>
        </div>
        <Badge tone="info">只选环境 · SUT 自动推导</Badge>
      </header>

      {step === 1 ? (
        <div className="space-y-3">
          {createdVersion !== null ? (
            <Alert tone="info" title="版本已创建：步骤 1 的修改不会自动保存">
              重试只会提交绑定（PUT），不会更新版本名称/描述/目标/环境，也不会重复创建版本。
            </Alert>
          ) : null}
          <Field id="wizard-title" label="版本名称" required error={titleError}>
            <Input
              id="wizard-title"
              value={title}
              placeholder="例如：订单中心 · 2026 Q4 迭代"
              autoComplete="off"
              aria-invalid={titleError !== "" ? true : undefined}
              onChange={(event) => setTitle(event.target.value)}
            />
          </Field>
          <Field id="wizard-description" label="版本描述">
            <Textarea
              id="wizard-description"
              value={description}
              placeholder="本次版本的背景/变更范围（可选）"
              onChange={(event) => setDescription(event.target.value)}
            />
          </Field>
          <Field id="wizard-goal" label="版本目标">
            <Input
              id="wizard-goal"
              value={goal}
              placeholder="例如：验证下单与退款主链路（可选）"
              onChange={(event) => setGoal(event.target.value)}
            />
          </Field>
          <Field id="wizard-environment" label="环境" required error={environmentError}>
            <Select
              id="wizard-environment"
              value={environmentId}
              aria-invalid={environmentError !== "" ? true : undefined}
              onChange={(event) => selectEnvironment(event.target.value)}
            >
              <option value="">请选择环境（SUT 自动推导）</option>
              {catalog.suts.flatMap((item) =>
                item.environments.map((option) => (
                  <option key={option.id} value={option.id}>
                    {item.name} · {option.name}
                    {option.is_default ? "（默认）" : ""}
                  </option>
                )),
              )}
            </Select>
          </Field>

          {catalog.suts.every((item) => item.environments.length === 0) ? (
            <Alert tone="warning" title="暂无可选环境">
              请先在资源页创建 SUT 与环境，再回来创建版本。
            </Alert>
          ) : null}

          {environment === undefined ? (
            <Alert tone="info" title="选择环境后自动推导 SUT">
              版本只会关联该环境所属的 SUT；页面/API/目标库默认全部绑定，无需手工勾选。
            </Alert>
          ) : (
            <>
              <p className="text-xs font-medium text-foreground">
                SUT：{derivedSut?.name}（由环境推导）
              </p>
              {pageCounts !== undefined &&
              apiCounts !== undefined &&
              databaseCounts !== undefined ? (
                <p className="text-[11px] text-muted-foreground">
                  环境资源：页面 {pageCounts.approved} · API {apiCounts.approved} · 目标库{" "}
                  {databaseCounts.approved} · 测试账号 {accounts.length}
                </p>
              ) : null}
              {proposedTotal > 0 ? (
                <p className="text-[11px] text-muted-foreground">
                  探索待确认：页面 {pageCounts?.proposed ?? 0} · API {apiCounts?.proposed ?? 0}
                  （确认后进入下一 wave，不在本次绑定范围）
                </p>
              ) : null}
            </>
          )}
        </div>
      ) : null}

      {step === 2 ? (
        <div className="space-y-3">
          <RepositorySection
            repositories={repositories}
            loading={repositoryLoading}
            error={repositoryError}
            selectedIds={selectedRepositoryIds}
            versionOnlyRepositories={versionOnlyRepositories}
            onToggle={toggleRepository}
            onRetry={() => {
              if (derivedSut !== undefined) void loadRepositories(derivedSut.id)
            }}
            onRefresh={() => {
              if (derivedSut !== undefined) void loadRepositories(derivedSut.id)
            }}
          />
          <AccountSection
            accounts={accounts}
            selectedIds={selectedAccountIds}
            onToggle={toggleAccount}
          />
        </div>
      ) : null}

      {step === 3 ? (
        <div className="space-y-3">
          <Alert tone="info" title="默认全部包含，只记录排除项">
            取消勾选仅写入 scope_exclude，不会删除或改写环境资源目录；计数按「已确认基线 −
            本版本排除」实时计算。执行中的 wave 使用已冻结快照，改动影响下一个 wave。
          </Alert>
          <ScopeGroup
            legend="页面"
            options={scopePages}
            excluded={scopeExclude.pages}
            onToggle={(id) => toggleScope("pages", id)}
            emptyText="该环境暂无已确认页面"
          />
          {pageCounts !== undefined ? (
            <p className="-mt-2 text-[11px] text-muted-foreground">
              页面 {scopeSummary(pageCounts)}
            </p>
          ) : null}
          <ScopeGroup
            legend="API"
            options={scopeApis}
            excluded={scopeExclude.apis}
            onToggle={(id) => toggleScope("apis", id)}
            emptyText="该环境暂无已确认 API"
          />
          {apiCounts !== undefined ? (
            <p className="-mt-2 text-[11px] text-muted-foreground">API {scopeSummary(apiCounts)}</p>
          ) : null}
          <ScopeGroup
            legend="目标库"
            options={scopeDatabaseOptions}
            excluded={scopeExclude.databases}
            onToggle={(id) => toggleScope("databases", id)}
            emptyText="该环境暂无目标数据库"
          />
          {databaseCounts !== undefined ? (
            <p className="-mt-2 text-[11px] text-muted-foreground">
              目标库 {scopeSummary(databaseCounts)}
            </p>
          ) : null}
        </div>
      ) : null}

      {step === 4 ? (
        <div className="space-y-3">
          <div className="rounded-xl border border-border bg-card p-4">
            <dl className="space-y-1.5">
              <ReviewRow label="版本名称">{title.trim()}</ReviewRow>
              <ReviewRow label="版本描述">{description.trim() || "未填写"}</ReviewRow>
              <ReviewRow label="版本目标">{goal.trim() || "未填写"}</ReviewRow>
              <ReviewRow label="SUT / 环境">
                {derivedSut?.name} / {environment?.name}
              </ReviewRow>
              <ReviewRow label="代码仓">
                {selectedRepositoryIds === null
                  ? "继承 SUT 的全部代码仓（代码仓接口未就绪，未覆盖）"
                  : selectedRepositoryRows.length === 0
                    ? "本版本不包含任何代码仓"
                    : selectedRepositoryRows.map((row) => row.label).join("、")}
              </ReviewRow>
              <ReviewRow label="测试账号">
                {selectedAccountIds === null
                  ? "默认关联全部启用账号"
                  : selectedAccounts.length === 0
                    ? "本版本不关联测试账号（执行将被拒绝）"
                    : selectedAccounts.map((account) => account.username).join("、")}
              </ReviewRow>
              <ReviewRow label="页面">
                {pageCounts !== undefined ? scopeSummary(pageCounts) : "-"}
              </ReviewRow>
              <ReviewRow label="API">
                {apiCounts !== undefined ? scopeSummary(apiCounts) : "-"}
              </ReviewRow>
              <ReviewRow label="目标库">
                {databaseCounts !== undefined ? scopeSummary(databaseCounts) : "-"}
              </ReviewRow>
            </dl>
          </div>

          {submitError !== null ? (
            <Alert tone="danger" title={submitTitle}>
              <p>
                {submitDetails?.message ??
                  (submitError instanceof Error ? submitError.message : "提交失败")}
              </p>
              {submitDetails !== null && submitDetails.field !== "" ? (
                <p className="mt-1 text-[11px]">字段：{submitDetails.field}</p>
              ) : null}
              {submitReadOnly ? (
                <p className="mt-1 text-[11px]">
                  当前阶段不允许修改绑定；请暂停或回退到设计阶段后重试。
                </p>
              ) : null}
              {createdVersion !== null ? (
                <p className="mt-1 text-[11px]">
                  版本已创建（#{createdVersion.id}），重试只会保存绑定，不会重复创建；步骤 1
                  的版本信息修改不会随重试提交。
                </p>
              ) : null}
              <div className="mt-2 flex flex-wrap items-center gap-2">
                {submitReadOnly ? (
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={refreshingBinding}
                    onClick={() => void refreshCreatedBinding()}
                  >
                    {refreshingBinding ? "刷新中…" : "刷新绑定"}
                  </Button>
                ) : null}
                {submitFieldStep !== null && !submitReadOnly ? (
                  <Button variant="secondary" size="sm" onClick={() => goTo(submitFieldStep)}>
                    返回步骤 {submitFieldStep} 修正
                  </Button>
                ) : null}
              </div>
              {refreshedBinding !== null ? (
                <div className="mt-2 space-y-1 rounded-md border border-border bg-muted/40 p-2 text-[11px]">
                  <p className="font-medium">
                    已读取最新绑定：阶段 {stageLabel(refreshedBinding.stage as VersionStage)}
                    ，代码仓 {refreshedBinding.repositories.length} 个，测试账号{" "}
                    {refreshedBinding.accounts.length} 个。
                  </p>
                  <p>
                    页面 {scopeSummary(refreshedBinding.counts.pages)} · API{" "}
                    {scopeSummary(refreshedBinding.counts.apis)} · 目标库{" "}
                    {scopeSummary(refreshedBinding.counts.databases)}
                  </p>
                  {refreshedBinding.repositories.length > 0 ? (
                    <ul className="space-y-0.5">
                      {refreshedBinding.repositories.map((row) => (
                        <li
                          key={row.repository.id}
                          className="flex flex-wrap items-center gap-1 text-foreground"
                        >
                          <span>{row.repository.name}</span>
                          <Badge tone={row.source === "override" ? "info" : "neutral"}>
                            {row.source === "override" ? "版本覆盖" : "SUT 继承"}
                          </Badge>
                          <Badge tone={row.included ? "success" : "warning"}>
                            {row.included ? "已包含" : "已排除"}
                          </Badge>
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </div>
              ) : null}
              {bindingRefreshError !== null ? (
                <p className="mt-1 text-[11px]">
                  刷新绑定失败：
                  {bindingRefreshError instanceof Error
                    ? bindingRefreshError.message
                    : "请稍后重试"}
                </p>
              ) : null}
            </Alert>
          ) : null}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3">
        <div>
          {onCancel ? (
            <Button variant="ghost" onClick={onCancel} disabled={submitting}>
              取消
            </Button>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          {step > 1 ? (
            <Button variant="secondary" onClick={previous} disabled={submitting}>
              上一步
            </Button>
          ) : null}
          {step < 4 ? (
            <Button onClick={next}>下一步</Button>
          ) : (
            <Button onClick={() => void createAndBind()} disabled={submitting}>
              {submitting ? "提交中…" : createdVersion !== null ? "重试保存绑定" : "创建版本"}
            </Button>
          )}
        </div>
      </div>
    </section>
  )
}

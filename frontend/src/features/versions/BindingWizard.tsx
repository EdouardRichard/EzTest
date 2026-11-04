"use client"

import { Plus, RefreshCw, Trash2 } from "lucide-react"
import { type ReactNode, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import { createApi, createDatabase, createPage } from "@/features/resources/api"
import {
  ApiDialog,
  DatabaseDialog,
  type DatabaseDialogValues,
  PageDialog,
} from "@/features/resources/dialogs"
import type { ApiFormValues, PageFormValues } from "@/features/resources/types"

import type { BindingDraft, BindingErrors, Catalog } from "./types"
import {
  createBindingDraft,
  findEnvironment,
  findSut,
  MAX_VERSION_BINDINGS,
  toggleSelection,
  updateBindingEnvironment,
  updateBindingSut,
} from "./wizard"

type ScopedOption = {
  id: string
  label: string
  hint?: string
}

type ScopedCheckboxGroupProps = {
  legend: string
  options: ScopedOption[]
  selected: string[]
  onToggle: (id: string) => void
  emptyText: string
  error?: string
  /** Optional inline action (create/discover) shown next to the option count. */
  action?: ReactNode
}

/** Multi-select list for one environment-scoped resource kind. */
function ScopedCheckboxGroup({
  legend,
  options,
  selected,
  onToggle,
  emptyText,
  error,
  action,
}: ScopedCheckboxGroupProps) {
  return (
    <fieldset className="rounded-lg border border-border bg-muted/30 p-3">
      <legend className="px-1 text-xs font-medium text-foreground">{legend}</legend>
      <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2">
        <p className="text-[11px] text-muted-foreground">
          {options.length === 0 ? emptyText : `可选 ${options.length} 个`}
        </p>
        {action}
      </div>
      {options.length === 0 ? null : (
        <div className="grid gap-1.5 sm:grid-cols-2">
          {options.map((option) => (
            <label
              key={option.id}
              className="flex cursor-pointer items-start gap-2 text-xs text-foreground"
            >
              <input
                type="checkbox"
                className="mt-0.5 size-3.5 accent-primary"
                checked={selected.includes(option.id)}
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
          ))}
        </div>
      )}
      {error ? (
        <p role="alert" className="mt-1.5 text-[11px] text-destructive">
          {error}
        </p>
      ) : null}
    </fieldset>
  )
}

type BindingCardProps = {
  binding: BindingDraft
  index: number
  catalog: Catalog
  errors?: BindingErrors
  disabled: boolean
  onReplace: (binding: BindingDraft) => void
  onRemove: () => void
  onCreatePage: () => void
  onCreateApi: () => void
  onCreateDatabase: () => void
}

function BindingCard({
  binding,
  index,
  catalog,
  errors,
  disabled,
  onReplace,
  onRemove,
  onCreatePage,
  onCreateApi,
  onCreateDatabase,
}: BindingCardProps) {
  const sut = findSut(catalog, binding.sutId)
  const environment = findEnvironment(catalog, binding.sutId, binding.environmentId)
  const idPrefix = `version-binding-${binding.key}-${index}`
  const scopedOptions = environment
    ? {
        pages: environment.pages.map((page) => ({
          id: page.id,
          label: page.name,
          hint:
            page.status === "proposed"
              ? `${page.route_template} · 待确认（探索发现）`
              : page.route_template,
        })),
        apis: environment.apis.map((api) => ({
          id: api.id,
          label: api.name,
          hint: `${api.method} ${api.path_template}${
            api.status === "proposed" ? " · 待确认（探索发现）" : ""
          }`,
        })),
        databases: environment.databases.map((database) => ({
          id: database.id,
          label: database.name,
          hint: database.read_only ? "只读" : "可写",
        })),
        accounts: environment.accounts.map((account) => ({
          id: account.id,
          label: account.username,
          hint: account.roles.join(" / "),
        })),
      }
    : {
        pages: [],
        apis: [],
        databases: [],
        accounts: [],
      }

  return (
    <article
      aria-label={`SUT 绑定 ${index + 1}`}
      className="rounded-xl border border-border bg-card p-4"
    >
      <header className="mb-3 flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <span className="flex size-6 items-center justify-center rounded-full bg-accent text-[11px] font-semibold text-accent-foreground">
            {index + 1}
          </span>
          <h3 className="text-sm font-semibold text-foreground">绑定 {index + 1}</h3>
        </div>
        <Button
          variant="ghost"
          size="sm"
          aria-label={`移除绑定 ${index + 1}`}
          disabled={disabled}
          onClick={onRemove}
        >
          <Trash2 className="size-3.5" aria-hidden="true" />
          移除
        </Button>
      </header>

      <div className="grid gap-3 sm:grid-cols-2">
        <Field id={`${idPrefix}-sut`} label="SUT" required error={errors?.sutId}>
          <Select
            id={`${idPrefix}-sut`}
            value={binding.sutId}
            disabled={disabled}
            aria-invalid={errors?.sutId ? true : undefined}
            aria-describedby={errors?.sutId ? `${idPrefix}-sut-error` : undefined}
            onChange={(event) => onReplace(updateBindingSut(binding, event.target.value))}
          >
            <option value="">请选择 SUT</option>
            {catalog.suts.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </Select>
        </Field>

        <Field id={`${idPrefix}-environment`} label="环境" required error={errors?.environmentId}>
          <Select
            id={`${idPrefix}-environment`}
            value={binding.environmentId}
            disabled={disabled || sut === undefined}
            aria-invalid={errors?.environmentId ? true : undefined}
            aria-describedby={errors?.environmentId ? `${idPrefix}-environment-error` : undefined}
            onChange={(event) => onReplace(updateBindingEnvironment(binding, event.target.value))}
          >
            <option value="">{sut ? "请选择环境" : "请先选择 SUT"}</option>
            {sut?.environments.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
                {item.is_default ? "（默认）" : ""}
              </option>
            ))}
          </Select>
        </Field>
      </div>

      <div className="mt-3 grid gap-3 lg:grid-cols-2">
        <ScopedCheckboxGroup
          legend="页面（可多选）"
          options={scopedOptions.pages}
          selected={binding.pageIds}
          onToggle={(id) =>
            onReplace({ ...binding, pageIds: toggleSelection(binding.pageIds, id) })
          }
          emptyText={
            environment
              ? "该环境暂无页面。可在这里新建，或先运行探索发现后刷新资源目录。"
              : "请先选择环境"
          }
          error={errors?.pages}
          action={
            environment ? (
              <Button variant="secondary" size="sm" disabled={disabled} onClick={onCreatePage}>
                <Plus className="size-3.5" aria-hidden="true" />
                新建页面
              </Button>
            ) : undefined
          }
        />
        <ScopedCheckboxGroup
          legend="API（可多选）"
          options={scopedOptions.apis}
          selected={binding.apiIds}
          onToggle={(id) => onReplace({ ...binding, apiIds: toggleSelection(binding.apiIds, id) })}
          emptyText={
            environment
              ? "该环境暂无 API。可在这里新建，或先运行探索发现后刷新资源目录。"
              : "请先选择环境"
          }
          error={errors?.apis}
          action={
            environment ? (
              <Button variant="secondary" size="sm" disabled={disabled} onClick={onCreateApi}>
                <Plus className="size-3.5" aria-hidden="true" />
                新建 API
              </Button>
            ) : undefined
          }
        />
        <ScopedCheckboxGroup
          legend="目标库（可多选）"
          options={scopedOptions.databases}
          selected={binding.databaseIds}
          onToggle={(id) =>
            onReplace({ ...binding, databaseIds: toggleSelection(binding.databaseIds, id) })
          }
          emptyText={environment ? "该环境暂无目标库。可在这里新建目标库后绑定。" : "请先选择环境"}
          error={errors?.databases}
          action={
            environment ? (
              <Button variant="secondary" size="sm" disabled={disabled} onClick={onCreateDatabase}>
                <Plus className="size-3.5" aria-hidden="true" />
                新建目标库
              </Button>
            ) : undefined
          }
        />
        <ScopedCheckboxGroup
          legend="测试账号（可多选）"
          options={scopedOptions.accounts}
          selected={binding.accountIds}
          onToggle={(id) =>
            onReplace({ ...binding, accountIds: toggleSelection(binding.accountIds, id) })
          }
          emptyText={environment ? "该环境暂无测试账号" : "请先选择环境"}
          error={errors?.accounts}
        />
      </div>

      <Field
        id={`${idPrefix}-repository`}
        label="代码仓（可选）"
        hint="可绑定一个代码仓；不选表示该 SUT 不关联代码。"
        error={errors?.repositoryId}
        className="mt-3 sm:max-w-sm"
      >
        <Select
          id={`${idPrefix}-repository`}
          value={binding.repositoryId}
          disabled={disabled}
          aria-invalid={errors?.repositoryId ? true : undefined}
          aria-describedby={errors?.repositoryId ? `${idPrefix}-repository-error` : undefined}
          onChange={(event) => onReplace({ ...binding, repositoryId: event.target.value })}
        >
          <option value="">不绑定代码仓</option>
          {catalog.repositories.map((repository) => (
            <option key={repository.id} value={repository.id}>
              {repository.name}
              {repository.branch ? `（${repository.branch}）` : ""}
            </option>
          ))}
        </Select>
      </Field>
    </article>
  )
}

export type BindingWizardProps = {
  catalog: Catalog
  value: BindingDraft[]
  onChange: (bindings: BindingDraft[]) => void
  errors: Record<string, BindingErrors>
  disabled?: boolean
  /**
   * Reloads the resource catalog. Called after an inline create and by the
   * "刷新资源目录" action, so pages/APIs discovered by the background
   * exploration analysis become bindable without leaving the wizard.
   */
  onRefreshCatalog?: () => Promise<void> | void
}

type CreateTarget = {
  kind: "page" | "api" | "database"
  bindingKey: string
}

/**
 * Multi-SUT binding wizard: each card walks SUT → environment → page/API →
 * databases/accounts → repository, and every pick is validated against the
 * selected environment at submit time.
 *
 * A bound environment with no pages/APIs/databases is the state the
 * product-owner trial hit ("该环境暂无页面/API/目标库"): the card now offers an
 * inline create for each kind plus a catalog refresh, so a user can create a
 * real resource and bind it without leaving the version flow.
 */
export function BindingWizard({
  catalog,
  value,
  onChange,
  errors,
  disabled = false,
  onRefreshCatalog,
}: BindingWizardProps) {
  const [createTarget, setCreateTarget] = useState<CreateTarget | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  function replaceBinding(key: string, next: BindingDraft): void {
    onChange(value.map((binding) => (binding.key === key ? next : binding)))
  }

  function removeBinding(key: string): void {
    onChange(value.filter((binding) => binding.key !== key))
  }

  function openCreate(kind: CreateTarget["kind"], bindingKey: string): void {
    setCreateTarget({ kind, bindingKey })
  }

  function targetBinding(target: CreateTarget): BindingDraft | undefined {
    return value.find((binding) => binding.key === target.bindingKey)
  }

  async function refreshCatalog(): Promise<void> {
    if (!onRefreshCatalog) return
    setRefreshing(true)
    try {
      await onRefreshCatalog()
    } finally {
      setRefreshing(false)
    }
  }

  /** Creates one resource and immediately selects it in its binding. */
  async function submitCreate(
    target: CreateTarget,
    create: (environmentId: string) => Promise<{ id: string }>,
    select: (binding: BindingDraft, id: string) => BindingDraft,
  ): Promise<void> {
    const binding = targetBinding(target)
    const environmentId = binding?.environmentId ?? ""
    if (!binding || environmentId.length === 0) {
      throw new Error("请先选择 SUT 和环境")
    }
    const created = await create(environmentId)
    onChange(value.map((item) => (item.key === binding.key ? select(item, created.id) : item)))
    try {
      await refreshCatalog()
    } catch {
      // The resource exists and is selected; a catalog refresh failure is
      // surfaced by the parent's catalog error state, not as a create failure.
    }
    setCreateTarget(null)
  }

  const activeTarget = createTarget
  const activeBinding = activeTarget ? targetBinding(activeTarget) : undefined
  const activeEnvironment = activeBinding
    ? findEnvironment(catalog, activeBinding.sutId, activeBinding.environmentId)
    : undefined

  return (
    <section aria-label="SUT 绑定向导" className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">SUT 绑定</h2>
          <p className="text-[11px] text-muted-foreground">
            同一个版本可绑定多个 SUT；每个 SUT 只能出现一次，页面/API/库/账号可为空。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone="neutral">
            {value.length} / {MAX_VERSION_BINDINGS} 个 SUT
          </Badge>
          {onRefreshCatalog ? (
            <Button
              variant="ghost"
              size="sm"
              disabled={disabled || refreshing}
              onClick={() => void refreshCatalog()}
            >
              <RefreshCw className="size-3.5" aria-hidden="true" />
              刷新资源目录
            </Button>
          ) : null}
        </div>
      </div>

      {value.length === 0 ? (
        <Alert tone="info" title="还没有 SUT 绑定">
          至少添加一个 SUT 绑定后才能创建版本。
        </Alert>
      ) : (
        value.map((binding, index) => (
          <BindingCard
            key={binding.key}
            binding={binding}
            index={index}
            catalog={catalog}
            errors={errors[binding.key]}
            disabled={disabled}
            onReplace={(next) => replaceBinding(binding.key, next)}
            onRemove={() => removeBinding(binding.key)}
            onCreatePage={() => openCreate("page", binding.key)}
            onCreateApi={() => openCreate("api", binding.key)}
            onCreateDatabase={() => openCreate("database", binding.key)}
          />
        ))
      )}

      <Button
        variant="secondary"
        size="sm"
        disabled={disabled || value.length >= MAX_VERSION_BINDINGS}
        onClick={() => onChange([...value, createBindingDraft()])}
      >
        <Plus className="size-3.5" aria-hidden="true" />
        添加 SUT 绑定
      </Button>

      {activeTarget?.kind === "page" && activeEnvironment ? (
        <PageDialog
          onClose={() => setCreateTarget(null)}
          onSubmit={(form: PageFormValues) =>
            submitCreate(
              activeTarget,
              (environmentId) => createPage(environmentId, form),
              (binding, id) => ({ ...binding, pageIds: toggleSelection(binding.pageIds, id) }),
            )
          }
        />
      ) : null}

      {activeTarget?.kind === "api" && activeEnvironment ? (
        <ApiDialog
          onClose={() => setCreateTarget(null)}
          onSubmit={(form: ApiFormValues) =>
            submitCreate(
              activeTarget,
              (environmentId) => createApi(environmentId, form),
              (binding, id) => ({ ...binding, apiIds: toggleSelection(binding.apiIds, id) }),
            )
          }
        />
      ) : null}

      {activeTarget?.kind === "database" && activeEnvironment ? (
        <DatabaseDialog
          environmentId={activeEnvironment.id}
          onClose={() => setCreateTarget(null)}
          onSubmit={(form: DatabaseDialogValues) =>
            submitCreate(
              activeTarget,
              (environmentId) =>
                createDatabase(activeBinding?.sutId ?? "", {
                  environmentId,
                  name: form.name,
                  dsn: form.dsn,
                  username: form.username,
                  password: form.password,
                  readOnly: form.readOnly,
                }),
              (binding, id) => ({
                ...binding,
                databaseIds: toggleSelection(binding.databaseIds, id),
              }),
            )
          }
        />
      ) : null}
    </section>
  )
}

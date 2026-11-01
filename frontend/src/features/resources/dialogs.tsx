"use client"

import type { FormEvent, ReactNode } from "react"
import { useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { Textarea } from "@/components/ui/Textarea"
import { ApiError } from "@/lib/api-client"

import type {
  AccountFormValues,
  ApiFormValues,
  ApiMethod,
  CodeRepository,
  DatabaseFormValues,
  EnvironmentFormValues,
  FormErrors,
  PageFormValues,
  RepositoryFormValues,
  ResourceStatus,
  Sut,
  SutApi,
  SutEnvironment,
  SutFormValues,
  SutPage,
  TargetDatabase,
  TestAccount,
} from "./types"
import {
  API_METHODS,
  hasErrors,
  type ImportParseResult,
  STATUS_LABELS,
  validateAccountForm,
  validateApiForm,
  validateDatabaseForm,
  validateEnvironmentForm,
  validatePageForm,
  validateRepositoryForm,
  validateSutBasicForm,
  validateSutForm,
} from "./validation"

/** Maps an API failure to an actionable inline message, including 409 guards. */
export function requestErrorText(caught: unknown, fallback: string): string {
  if (caught instanceof ApiError) {
    if (caught.status === 409) {
      return `操作冲突：${caught.message}。该资源可能已被版本绑定，或名称/键重复。`
    }
    if (caught.status === 403) return "当前账号没有权限执行此操作。"
    if (caught.status === 404) return "资源不存在或已被删除，请刷新后重试。"
    return caught.message.length > 0 ? caught.message : fallback
  }
  if (caught instanceof Error) return caught.message.length > 0 ? caught.message : fallback
  return fallback
}

type DialogFormProps = {
  title: string
  description?: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
  submitLabel: string
  pendingLabel: string
  pending: boolean
  error: string | null
  children: ReactNode
}

/** Shared dialog shell for the small resource forms (cancel/submit footer). */
function DialogForm({
  title,
  description,
  onClose,
  onSubmit,
  submitLabel,
  pendingLabel,
  pending,
  error,
  children,
}: DialogFormProps) {
  return (
    <Dialog open onClose={onClose} title={title} description={description}>
      <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
        {children}
        {error ? <Alert tone="danger">{error}</Alert> : null}
        <div className="flex items-center justify-end gap-2 pt-1">
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button type="submit" disabled={pending}>
            {pending ? pendingLabel : submitLabel}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

export type SutDialogProps = {
  initial?: Sut
  onClose: () => void
  onSubmit: (values: SutFormValues) => Promise<void>
}

/**
 * Creates a SUT together with its first default environment, or edits only the
 * SUT name/intro. The environment fields are hidden in edit mode.
 */
export function SutDialog({ initial, onClose, onSubmit }: SutDialogProps) {
  const editing = initial !== undefined
  const [name, setName] = useState(initial?.name ?? "")
  const [intro, setIntro] = useState(initial?.intro ?? "")
  const [family, setFamily] = useState(initial?.family ?? "")
  const [environmentName, setEnvironmentName] = useState("")
  const [baseUrl, setBaseUrl] = useState("")
  const [errors, setErrors] = useState<FormErrors<SutFormValues>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setFormError(null)

    const values: SutFormValues = {
      name: name.trim(),
      intro: intro.trim(),
      family: family.trim(),
      environmentName: environmentName.trim(),
      baseUrl: baseUrl.trim(),
    }
    const nextErrors = editing
      ? (validateSutBasicForm(values) as FormErrors<SutFormValues>)
      : validateSutForm(values)
    setErrors(nextErrors)
    if (hasErrors(nextErrors)) return

    setPending(true)
    try {
      await onSubmit(values)
      onClose()
    } catch (caught) {
      setFormError(requestErrorText(caught, "保存 SUT 失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <DialogForm
      title={editing ? "编辑 SUT" : "新建 SUT"}
      description={
        editing
          ? "更新 SUT 的名称与简介；默认环境在环境列表中单独维护。"
          : "创建 SUT 时会同时创建它的默认环境；后续可继续添加环境。"
      }
      onClose={onClose}
      onSubmit={(event) => void handleSubmit(event)}
      submitLabel={editing ? "保存 SUT" : "创建 SUT"}
      pendingLabel={editing ? "保存中…" : "创建中…"}
      pending={pending}
      error={formError}
    >
      <Field id="sut-name" label="SUT 名称" required error={errors.name}>
        <Input
          id="sut-name"
          autoComplete="off"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>
      <Field id="sut-intro" label="简介" error={errors.intro}>
        <Textarea
          id="sut-intro"
          className="min-h-20"
          value={intro}
          onChange={(event) => setIntro(event.target.value)}
        />
      </Field>
      <Field
        id="sut-family"
        label="系统族（family）"
        error={errors.family}
        hint="可选；小写 slug（a-z、0-9、-、_），最长 64；用于匹配 system 知识。"
      >
        <Input
          id="sut-family"
          autoComplete="off"
          value={family}
          onChange={(event) => setFamily(event.target.value)}
        />
      </Field>
      {editing ? null : (
        <>
          <Field
            id="sut-env-name"
            label="默认环境名称"
            required
            error={errors.environmentName}
            hint="例如：测试环境"
          >
            <Input
              id="sut-env-name"
              autoComplete="off"
              value={environmentName}
              onChange={(event) => setEnvironmentName(event.target.value)}
            />
          </Field>
          <Field
            id="sut-base-url"
            label="环境地址"
            required
            error={errors.baseUrl}
            hint="完整的 http(s) 地址，例如 https://order.test"
          >
            <Input
              id="sut-base-url"
              autoComplete="off"
              value={baseUrl}
              onChange={(event) => setBaseUrl(event.target.value)}
            />
          </Field>
        </>
      )}
    </DialogForm>
  )
}

export type EnvironmentDialogProps = {
  initial?: SutEnvironment
  onClose: () => void
  onSubmit: (values: EnvironmentFormValues) => Promise<void>
}

export function EnvironmentDialog({ initial, onClose, onSubmit }: EnvironmentDialogProps) {
  const editing = initial !== undefined
  const [name, setName] = useState(initial?.name ?? "")
  const [baseUrl, setBaseUrl] = useState(initial?.base_url ?? "")
  const [isDefault, setIsDefault] = useState(initial?.is_default ?? false)
  const [status, setStatus] = useState<ResourceStatus>(initial?.status ?? "active")
  const [errors, setErrors] = useState<FormErrors<EnvironmentFormValues>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setFormError(null)

    const values: EnvironmentFormValues = {
      name: name.trim(),
      baseUrl: baseUrl.trim(),
      isDefault,
      status,
    }
    const nextErrors = validateEnvironmentForm(values)
    setErrors(nextErrors)
    if (hasErrors(nextErrors)) return

    setPending(true)
    try {
      await onSubmit(values)
      onClose()
    } catch (caught) {
      setFormError(requestErrorText(caught, "保存环境失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <DialogForm
      title={editing ? "编辑环境" : "新建环境"}
      description="同一 SUT 下环境名称唯一；设为默认环境会取消其他默认环境。"
      onClose={onClose}
      onSubmit={(event) => void handleSubmit(event)}
      submitLabel={editing ? "保存环境" : "创建环境"}
      pendingLabel={editing ? "保存中…" : "创建中…"}
      pending={pending}
      error={formError}
    >
      <Field id="env-name" label="环境名称" required error={errors.name}>
        <Input
          id="env-name"
          autoComplete="off"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>
      <Field id="env-base-url" label="环境地址" required error={errors.baseUrl}>
        <Input
          id="env-base-url"
          autoComplete="off"
          value={baseUrl}
          onChange={(event) => setBaseUrl(event.target.value)}
        />
      </Field>
      <div className="flex flex-wrap items-center gap-4">
        <label htmlFor="env-default" className="flex items-center gap-2 text-xs text-foreground">
          <input
            id="env-default"
            type="checkbox"
            checked={isDefault}
            onChange={(event) => setIsDefault(event.target.checked)}
          />
          设为默认环境
        </label>
        <div className="w-32">
          <Field id="env-status" label="状态">
            <Select
              id="env-status"
              value={status}
              onChange={(event) => setStatus(event.target.value as ResourceStatus)}
            >
              {Object.entries(STATUS_LABELS).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          </Field>
        </div>
      </div>
    </DialogForm>
  )
}

export type PageDialogProps = {
  initial?: SutPage
  onClose: () => void
  onSubmit: (values: PageFormValues) => Promise<void>
}

export function PageDialog({ initial, onClose, onSubmit }: PageDialogProps) {
  const editing = initial !== undefined
  const [name, setName] = useState(initial?.name ?? "")
  const [url, setUrl] = useState(initial?.url ?? "")
  const [routeTemplate, setRouteTemplate] = useState(initial?.route_template ?? "")
  const [pageType, setPageType] = useState(initial?.page_type ?? "page")
  const [authRole, setAuthRole] = useState(initial?.auth_role ?? "")
  const [module, setModule] = useState(initial?.module ?? "")
  const [errors, setErrors] = useState<FormErrors<PageFormValues>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setFormError(null)

    const values: PageFormValues = {
      name: name.trim(),
      url: url.trim(),
      routeTemplate: routeTemplate.trim(),
      pageType: pageType.trim() || "page",
      authRole: authRole.trim(),
      module: module.trim(),
    }
    const nextErrors = validatePageForm(values)
    setErrors(nextErrors)
    if (hasErrors(nextErrors)) return

    setPending(true)
    try {
      await onSubmit(values)
      onClose()
    } catch (caught) {
      setFormError(requestErrorText(caught, "保存页面失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <DialogForm
      title={editing ? "编辑页面" : "新建页面"}
      description="页面 URL 必须完整；路由模板留空时由 API 从 URL 路径派生。"
      onClose={onClose}
      onSubmit={(event) => void handleSubmit(event)}
      submitLabel={editing ? "保存页面" : "创建页面"}
      pendingLabel={editing ? "保存中…" : "创建中…"}
      pending={pending}
      error={formError}
    >
      <Field id="page-name" label="页面名称" required error={errors.name}>
        <Input
          id="page-name"
          autoComplete="off"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>
      <Field id="page-url" label="页面 URL" required error={errors.url}>
        <Input
          id="page-url"
          autoComplete="off"
          placeholder="https://order.test/orders"
          value={url}
          onChange={(event) => setUrl(event.target.value)}
        />
      </Field>
      <Field
        id="page-route"
        label="路由模板"
        error={errors.routeTemplate}
        hint="可选；以 / 开头，例如 /orders/{id}"
      >
        <Input
          id="page-route"
          autoComplete="off"
          value={routeTemplate}
          onChange={(event) => setRouteTemplate(event.target.value)}
        />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field id="page-type" label="页面类型">
          <Input
            id="page-type"
            autoComplete="off"
            value={pageType}
            onChange={(event) => setPageType(event.target.value)}
          />
        </Field>
        <Field id="page-auth-role" label="所需角色">
          <Input
            id="page-auth-role"
            autoComplete="off"
            value={authRole}
            onChange={(event) => setAuthRole(event.target.value)}
          />
        </Field>
      </div>
      <Field id="page-module" label="模块">
        <Input
          id="page-module"
          autoComplete="off"
          value={module}
          onChange={(event) => setModule(event.target.value)}
        />
      </Field>
    </DialogForm>
  )
}

export type ApiDialogProps = {
  initial?: SutApi
  onClose: () => void
  onSubmit: (values: ApiFormValues) => Promise<void>
}

export function ApiDialog({ initial, onClose, onSubmit }: ApiDialogProps) {
  const editing = initial !== undefined
  const [name, setName] = useState(initial?.name ?? "")
  const [method, setMethod] = useState<ApiMethod>(initial?.method ?? "GET")
  const [pathTemplate, setPathTemplate] = useState(initial?.path_template ?? "")
  const [authRole, setAuthRole] = useState(initial?.auth_role ?? "")
  const [schemaRef, setSchemaRef] = useState(initial?.schema_ref ?? "")
  const [errors, setErrors] = useState<FormErrors<ApiFormValues>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setFormError(null)

    const values: ApiFormValues = {
      name: name.trim(),
      method,
      pathTemplate: pathTemplate.trim(),
      authRole: authRole.trim(),
      schemaRef: schemaRef.trim(),
    }
    const nextErrors = validateApiForm(values)
    setErrors(nextErrors)
    if (hasErrors(nextErrors)) return

    setPending(true)
    try {
      await onSubmit(values)
      onClose()
    } catch (caught) {
      setFormError(requestErrorText(caught, "保存 API 失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <DialogForm
      title={editing ? "编辑 API" : "新建 API"}
      description="方法枚举固定；路径模板必须以 / 开头。"
      onClose={onClose}
      onSubmit={(event) => void handleSubmit(event)}
      submitLabel={editing ? "保存 API" : "创建 API"}
      pendingLabel={editing ? "保存中…" : "创建中…"}
      pending={pending}
      error={formError}
    >
      <Field id="api-name" label="API 名称" required error={errors.name}>
        <Input
          id="api-name"
          autoComplete="off"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>
      <div className="grid grid-cols-[8rem_1fr] gap-3">
        <Field id="api-method" label="方法" error={errors.method}>
          <Select
            id="api-method"
            value={method}
            onChange={(event) => setMethod(event.target.value as ApiMethod)}
          >
            {API_METHODS.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="api-path" label="路径模板" required error={errors.pathTemplate}>
          <Input
            id="api-path"
            autoComplete="off"
            placeholder="/api/orders"
            value={pathTemplate}
            onChange={(event) => setPathTemplate(event.target.value)}
          />
        </Field>
      </div>
      <Field id="api-auth-role" label="所需角色">
        <Input
          id="api-auth-role"
          autoComplete="off"
          value={authRole}
          onChange={(event) => setAuthRole(event.target.value)}
        />
      </Field>
      <Field id="api-schema-ref" label="Schema 引用">
        <Input
          id="api-schema-ref"
          autoComplete="off"
          value={schemaRef}
          onChange={(event) => setSchemaRef(event.target.value)}
        />
      </Field>
    </DialogForm>
  )
}

export type ImportDialogProps<T> = {
  title: string
  itemLabel: string
  example: string
  parse: (text: string) => ImportParseResult<T>
  onClose: () => void
  onSubmit: (items: T[]) => Promise<{ created: number; skipped: number }>
}

/** JSON import dialog for pages/APIs; validates every item before POSTing. */
export function ImportDialog<T>({
  title,
  itemLabel,
  example,
  parse,
  onClose,
  onSubmit,
}: ImportDialogProps<T>) {
  const [text, setText] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [result, setResult] = useState<{ created: number; skipped: number } | null>(null)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError(null)
    setResult(null)

    const parsed = parse(text)
    if (!parsed.ok) {
      setError(parsed.error)
      return
    }

    setPending(true)
    try {
      setResult(await onSubmit(parsed.items))
    } catch (caught) {
      setError(requestErrorText(caught, `导入${itemLabel}失败，请稍后重试`))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title={title}
      description={`支持数组或 {"items": [...]}；逐项校验，重复键由 API 跳过。`}
    >
      <form
        onSubmit={(event) => void handleSubmit(event)}
        className="flex flex-col gap-4"
        noValidate
      >
        <Field id="import-json" label="导入 JSON" hint={`例如：${example}`}>
          <Textarea
            id="import-json"
            className="min-h-48 font-mono text-xs"
            placeholder={example}
            value={text}
            onChange={(event) => setText(event.target.value)}
          />
        </Field>
        {error ? <Alert tone="danger">{error}</Alert> : null}
        {result ? (
          <Alert tone="success">
            已创建 {result.created} 个{itemLabel}，跳过 {result.skipped} 个。
          </Alert>
        ) : null}
        <div className="flex items-center justify-end gap-2 pt-1">
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            取消
          </Button>
          {result ? (
            <Button onClick={onClose}>完成</Button>
          ) : (
            <Button type="submit" disabled={pending}>
              {pending ? "导入中…" : "导入"}
            </Button>
          )}
        </div>
      </form>
    </Dialog>
  )
}

export type DatabaseDialogValues = DatabaseFormValues & {
  /** Edit only: explicitly remove the stored password. */
  clearPassword: boolean
}

export type DatabaseDialogProps = {
  environmentId: string
  initial?: TargetDatabase
  onClose: () => void
  onSubmit: (values: DatabaseDialogValues) => Promise<void>
}

export function DatabaseDialog({ environmentId, initial, onClose, onSubmit }: DatabaseDialogProps) {
  const editing = initial !== undefined
  const [name, setName] = useState(initial?.name ?? "")
  const [dsn, setDsn] = useState(initial?.dsn ?? "")
  const [username, setUsername] = useState(initial?.username ?? "")
  const [password, setPassword] = useState("")
  const [readOnly, setReadOnly] = useState(initial?.read_only ?? true)
  const [clearPassword, setClearPassword] = useState(false)
  const [errors, setErrors] = useState<FormErrors<DatabaseFormValues>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setFormError(null)

    const values: DatabaseDialogValues = {
      environmentId: initial?.environment_id ?? environmentId,
      name: name.trim(),
      dsn: dsn.trim(),
      username: username.trim(),
      password,
      readOnly,
      clearPassword,
    }
    const nextErrors = validateDatabaseForm(values)
    setErrors(nextErrors)
    if (hasErrors(nextErrors)) return

    setPending(true)
    try {
      await onSubmit(values)
      onClose()
    } catch (caught) {
      setFormError(requestErrorText(caught, "保存目标库失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <DialogForm
      title={editing ? "编辑目标库" : "新建目标库"}
      description="DSN 必须以 postgres:// 或 postgresql:// 开头；密码只返回是否已设置。"
      onClose={onClose}
      onSubmit={(event) => void handleSubmit(event)}
      submitLabel={editing ? "保存目标库" : "创建目标库"}
      pendingLabel={editing ? "保存中…" : "创建中…"}
      pending={pending}
      error={formError}
    >
      <Field id="db-name" label="库名称" required error={errors.name}>
        <Input
          id="db-name"
          autoComplete="off"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>
      <Field id="db-dsn" label="DSN" required error={errors.dsn}>
        <Input
          id="db-dsn"
          autoComplete="off"
          placeholder="postgres://host:5432/db"
          value={dsn}
          onChange={(event) => setDsn(event.target.value)}
        />
      </Field>
      <Field id="db-username" label="用户名" required error={errors.username}>
        <Input
          id="db-username"
          autoComplete="off"
          value={username}
          onChange={(event) => setUsername(event.target.value)}
        />
      </Field>
      <Field
        id="db-password"
        label="密码"
        error={errors.password}
        hint={
          editing
            ? initial?.password_set
              ? "密码已设置；留空保持不变。"
              : "当前未设置密码。"
            : "可选；密码只保存不返回。"
        }
      >
        <Input
          id="db-password"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </Field>
      <div className="flex flex-wrap items-center gap-4">
        <label htmlFor="db-read-only" className="flex items-center gap-2 text-xs text-foreground">
          <input
            id="db-read-only"
            type="checkbox"
            checked={readOnly}
            onChange={(event) => setReadOnly(event.target.checked)}
          />
          只读连接
        </label>
        {editing && initial?.password_set ? (
          <label
            htmlFor="db-clear-password"
            className="flex items-center gap-2 text-xs text-destructive"
          >
            <input
              id="db-clear-password"
              type="checkbox"
              checked={clearPassword}
              onChange={(event) => setClearPassword(event.target.checked)}
            />
            清除已保存的密码
          </label>
        ) : null}
      </div>
    </DialogForm>
  )
}

export type AccountDialogValues = AccountFormValues & {
  /** Edit only: explicitly remove the stored password. */
  clearPassword: boolean
}

export type AccountDialogProps = {
  environmentId: string
  initial?: TestAccount
  onClose: () => void
  onSubmit: (values: AccountDialogValues) => Promise<void>
}

export function AccountDialog({ environmentId, initial, onClose, onSubmit }: AccountDialogProps) {
  const editing = initial !== undefined
  const [username, setUsername] = useState(initial?.username ?? "")
  const [password, setPassword] = useState("")
  const [roles, setRoles] = useState(initial?.roles.join(", ") ?? "")
  const [notes, setNotes] = useState(initial?.notes ?? "")
  const [clearPassword, setClearPassword] = useState(false)
  const [errors, setErrors] = useState<FormErrors<AccountFormValues>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setFormError(null)

    const values: AccountDialogValues = {
      environmentId: initial?.environment_id ?? environmentId,
      username: username.trim(),
      password,
      roles,
      notes: notes.trim(),
      clearPassword,
    }
    const nextErrors = validateAccountForm(values, { requirePassword: !editing })
    setErrors(nextErrors)
    if (hasErrors(nextErrors)) return

    setPending(true)
    try {
      await onSubmit(values)
      onClose()
    } catch (caught) {
      setFormError(requestErrorText(caught, "保存测试账号失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <DialogForm
      title={editing ? "编辑测试账号" : "新建测试账号"}
      description="账号归属于当前环境；密码只返回是否已设置。"
      onClose={onClose}
      onSubmit={(event) => void handleSubmit(event)}
      submitLabel={editing ? "保存账号" : "创建账号"}
      pendingLabel={editing ? "保存中…" : "创建中…"}
      pending={pending}
      error={formError}
    >
      <Field id="account-username" label="账号用户名" required error={errors.username}>
        <Input
          id="account-username"
          autoComplete="off"
          value={username}
          onChange={(event) => setUsername(event.target.value)}
        />
      </Field>
      <Field
        id="account-password"
        label="密码"
        required={!editing}
        error={errors.password}
        hint={
          editing
            ? initial?.password_set
              ? "密码已设置；留空保持不变。"
              : "当前未设置密码。"
            : "创建时必须填写初始密码。"
        }
      >
        <Input
          id="account-password"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </Field>
      <Field
        id="account-roles"
        label="角色（逗号分隔）"
        error={errors.roles}
        hint="最多 20 个角色；创建版本时可用于账号用途标注。"
      >
        <Input
          id="account-roles"
          autoComplete="off"
          value={roles}
          onChange={(event) => setRoles(event.target.value)}
        />
      </Field>
      <Field id="account-notes" label="备注" error={errors.notes}>
        <Textarea
          id="account-notes"
          className="min-h-20"
          value={notes}
          onChange={(event) => setNotes(event.target.value)}
        />
      </Field>
      {editing && initial?.password_set ? (
        <label
          htmlFor="account-clear-password"
          className="flex items-center gap-2 text-xs text-destructive"
        >
          <input
            id="account-clear-password"
            type="checkbox"
            checked={clearPassword}
            onChange={(event) => setClearPassword(event.target.checked)}
          />
          清除已保存的密码
        </label>
      ) : null}
    </DialogForm>
  )
}

export type RepositoryDialogProps = {
  initial?: CodeRepository
  onClose: () => void
  onSubmit: (values: RepositoryFormValues) => Promise<void>
}

export function RepositoryDialog({ initial, onClose, onSubmit }: RepositoryDialogProps) {
  const editing = initial !== undefined
  const [name, setName] = useState(initial?.name ?? "")
  const [path, setPath] = useState(initial?.path ?? "")
  const [branch, setBranch] = useState(initial?.branch ?? "")
  const [include, setInclude] = useState(initial?.include.join(", ") ?? "")
  const [exclude, setExclude] = useState(initial?.exclude.join(", ") ?? "")
  const [errors, setErrors] = useState<FormErrors<RepositoryFormValues>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setFormError(null)

    const values: RepositoryFormValues = {
      name: name.trim(),
      path: path.trim(),
      branch: branch.trim(),
      include,
      exclude,
    }
    const nextErrors = validateRepositoryForm(values)
    setErrors(nextErrors)
    if (hasErrors(nextErrors)) return

    setPending(true)
    try {
      await onSubmit(values)
      onClose()
    } catch (caught) {
      setFormError(requestErrorText(caught, "保存代码仓失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <DialogForm
      title={editing ? "编辑代码仓" : "新建代码仓"}
      description="任意登录用户都可以维护代码仓；被版本绑定引用时无法删除。"
      onClose={onClose}
      onSubmit={(event) => void handleSubmit(event)}
      submitLabel={editing ? "保存代码仓" : "创建代码仓"}
      pendingLabel={editing ? "保存中…" : "创建中…"}
      pending={pending}
      error={formError}
    >
      <Field id="repo-name" label="仓库名称" required error={errors.name}>
        <Input
          id="repo-name"
          autoComplete="off"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>
      <Field id="repo-path" label="仓库路径" required error={errors.path}>
        <Input
          id="repo-path"
          autoComplete="off"
          placeholder="D:/code/order-center-web"
          value={path}
          onChange={(event) => setPath(event.target.value)}
        />
      </Field>
      <Field id="repo-branch" label="分支" error={errors.branch}>
        <Input
          id="repo-branch"
          autoComplete="off"
          placeholder="main"
          value={branch}
          onChange={(event) => setBranch(event.target.value)}
        />
      </Field>
      <Field
        id="repo-include"
        label="包含规则（逗号分隔）"
        error={errors.include}
        hint="例如 src/**, tests/**；最多 100 条"
      >
        <Textarea
          id="repo-include"
          className="min-h-16 font-mono text-xs"
          value={include}
          onChange={(event) => setInclude(event.target.value)}
        />
      </Field>
      <Field
        id="repo-exclude"
        label="排除规则（逗号分隔）"
        error={errors.exclude}
        hint="例如 node_modules/**, dist/**；最多 100 条"
      >
        <Textarea
          id="repo-exclude"
          className="min-h-16 font-mono text-xs"
          value={exclude}
          onChange={(event) => setExclude(event.target.value)}
        />
      </Field>
    </DialogForm>
  )
}

export type ConfirmDialogProps = {
  title: string
  description?: string
  confirmLabel?: string
  onClose: () => void
  onConfirm: () => Promise<void>
}

/** Confirmation shell used by every delete action; keeps API errors visible. */
export function ConfirmDialog({
  title,
  description,
  confirmLabel = "确认删除",
  onClose,
  onConfirm,
}: ConfirmDialogProps) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleConfirm(): Promise<void> {
    setPending(true)
    setError(null)
    try {
      await onConfirm()
      onClose()
    } catch (caught) {
      setError(requestErrorText(caught, "操作失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title={title}
      description={description}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button variant="danger" onClick={() => void handleConfirm()} disabled={pending}>
            {pending ? "处理中…" : confirmLabel}
          </Button>
        </>
      }
    >
      {error ? <Alert tone="danger">{error}</Alert> : null}
      {error ? null : (
        <p className="text-xs leading-relaxed text-muted-foreground">
          删除后不可恢复；如果该资源已被版本绑定引用，API 会返回 409 并拒绝删除。
        </p>
      )}
    </Dialog>
  )
}

import type {
  AccountFormValues,
  ApiFormValues,
  ApiMethod,
  DatabaseFormValues,
  EnvironmentFormValues,
  FormErrors,
  PageFormValues,
  RepositoryFormValues,
  ResourceStatus,
  SutFormValues,
} from "./types"

export const API_METHODS: ApiMethod[] = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]

const SUT_NAME_MAX = 100
const SUT_INTRO_MAX = 2000
const SUT_FAMILY_MAX = 64
const SUT_FAMILY_PATTERN = /^[a-z0-9][a-z0-9_-]*$/
const ENVIRONMENT_NAME_MAX = 100
const PAGE_NAME_MAX = 200
const API_NAME_MAX = 200
const DATABASE_NAME_MAX = 100
const USERNAME_MAX = 100
const PASSWORD_MAX = 1000
const NOTES_MAX = 2000
const ROLES_MAX = 20
const REPOSITORY_NAME_MAX = 100
const REPOSITORY_PATH_MAX = 1000
const REPOSITORY_BRANCH_MAX = 200
const REPOSITORY_RULES_MAX = 100

/** Splits comma/newline separated list fields, dropping blank entries. */
export function splitList(value: string): string[] {
  return value
    .split(/[,\n]/)
    .map((part) => part.trim())
    .filter((part) => part.length > 0)
}

/** True for absolute http(s) URLs with a host. */
export function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value.trim())
    return (url.protocol === "http:" || url.protocol === "https:") && url.hostname.length > 0
  } catch {
    return false
  }
}

/** Validates the optional P7 SUT family: lowercase slug, at most 64 chars. */
function checkSutFamily(
  family: string,
  errors: FormErrors<{ family?: string }>,
): FormErrors<{ family?: string }> {
  if (family.length === 0) return errors
  if (family.length > SUT_FAMILY_MAX) {
    errors.family = "系统族不能超过 64 个字符"
  } else if (!SUT_FAMILY_PATTERN.test(family)) {
    errors.family = "系统族必须为小写 slug（a-z、0-9、-、_）"
  }
  return errors
}

export function validateSutForm(values: SutFormValues): FormErrors<SutFormValues> {
  const errors: FormErrors<SutFormValues> = {}
  const name = values.name.trim()
  const intro = values.intro.trim()
  const environmentName = values.environmentName.trim()
  const baseUrl = values.baseUrl.trim()

  if (name.length === 0) errors.name = "请输入 SUT 名称"
  else if (name.length > SUT_NAME_MAX) errors.name = "SUT 名称不能超过 100 个字符"
  if (intro.length > SUT_INTRO_MAX) errors.intro = "简介不能超过 2000 个字符"
  checkSutFamily((values.family ?? "").trim(), errors)
  if (environmentName.length === 0) errors.environmentName = "请输入默认环境名称"
  else if (environmentName.length > ENVIRONMENT_NAME_MAX) {
    errors.environmentName = "默认环境名称不能超过 100 个字符"
  }
  if (baseUrl.length === 0) errors.baseUrl = "请输入环境地址"
  else if (!isHttpUrl(baseUrl)) errors.baseUrl = "环境地址必须是完整的 http(s) 地址"

  return errors
}

export function validateSutBasicForm(values: {
  name: string
  intro: string
  family?: string
}): FormErrors<{ name: string; intro: string; family?: string }> {
  const errors: FormErrors<{ name: string; intro: string; family?: string }> = {}
  const name = values.name.trim()
  const intro = values.intro.trim()

  if (name.length === 0) errors.name = "请输入 SUT 名称"
  else if (name.length > SUT_NAME_MAX) errors.name = "SUT 名称不能超过 100 个字符"
  if (intro.length > SUT_INTRO_MAX) errors.intro = "简介不能超过 2000 个字符"
  checkSutFamily((values.family ?? "").trim(), errors)
  return errors
}

export function validateEnvironmentForm(
  values: EnvironmentFormValues,
): FormErrors<EnvironmentFormValues> {
  const errors: FormErrors<EnvironmentFormValues> = {}
  const name = values.name.trim()
  const baseUrl = values.baseUrl.trim()

  if (name.length === 0) errors.name = "请输入环境名称"
  else if (name.length > ENVIRONMENT_NAME_MAX) errors.name = "环境名称不能超过 100 个字符"
  if (baseUrl.length === 0) errors.baseUrl = "请输入环境地址"
  else if (!isHttpUrl(baseUrl)) errors.baseUrl = "环境地址必须是完整的 http(s) 地址"
  return errors
}

export function validatePageForm(values: PageFormValues): FormErrors<PageFormValues> {
  const errors: FormErrors<PageFormValues> = {}
  const name = values.name.trim()
  const url = values.url.trim()
  const routeTemplate = values.routeTemplate.trim()

  if (name.length === 0) errors.name = "请输入页面名称"
  else if (name.length > PAGE_NAME_MAX) errors.name = "页面名称不能超过 200 个字符"
  if (url.length === 0) errors.url = "请输入页面 URL"
  else if (!isHttpUrl(url)) errors.url = "页面 URL 必须是完整的 http(s) 地址"
  if (routeTemplate.length > 0 && !routeTemplate.startsWith("/")) {
    errors.routeTemplate = "路由模板必须以 / 开头"
  }
  return errors
}

export function validateApiForm(values: ApiFormValues): FormErrors<ApiFormValues> {
  const errors: FormErrors<ApiFormValues> = {}
  const name = values.name.trim()
  const pathTemplate = values.pathTemplate.trim()

  if (name.length === 0) errors.name = "请输入 API 名称"
  else if (name.length > API_NAME_MAX) errors.name = "API 名称不能超过 200 个字符"
  if (!API_METHODS.includes(values.method)) errors.method = "请选择有效的 HTTP 方法"
  if (pathTemplate.length === 0) errors.pathTemplate = "请输入 API 路径模板"
  else if (!pathTemplate.startsWith("/")) errors.pathTemplate = "API 路径模板必须以 / 开头"
  return errors
}

export function validateDatabaseForm(values: DatabaseFormValues): FormErrors<DatabaseFormValues> {
  const errors: FormErrors<DatabaseFormValues> = {}
  const name = values.name.trim()
  const dsn = values.dsn.trim()
  const username = values.username.trim()

  if (values.environmentId.trim().length === 0) errors.environmentId = "请选择环境"
  if (name.length === 0) errors.name = "请输入目标库名称"
  else if (name.length > DATABASE_NAME_MAX) errors.name = "目标库名称不能超过 100 个字符"
  if (dsn.length === 0) errors.dsn = "请输入 DSN"
  else if (!dsn.startsWith("postgres://") && !dsn.startsWith("postgresql://")) {
    errors.dsn = "DSN 必须以 postgres:// 或 postgresql:// 开头"
  }
  if (username.length === 0) errors.username = "请输入数据库用户名"
  else if (username.length > USERNAME_MAX) errors.username = "用户名不能超过 100 个字符"
  if (values.password.length > PASSWORD_MAX) errors.password = "密码不能超过 1000 个字符"
  return errors
}

export function validateAccountForm(
  values: AccountFormValues,
  options: { requirePassword?: boolean } = {},
): FormErrors<AccountFormValues> {
  const errors: FormErrors<AccountFormValues> = {}
  const username = values.username.trim()
  const roles = splitList(values.roles)

  if (values.environmentId.trim().length === 0) errors.environmentId = "请选择环境"
  if (username.length === 0) errors.username = "请输入账号用户名"
  else if (username.length > USERNAME_MAX) errors.username = "用户名不能超过 100 个字符"
  if (options.requirePassword === true && values.password.length === 0) {
    errors.password = "请输入初始密码"
  } else if (values.password.length > PASSWORD_MAX) {
    errors.password = "密码不能超过 1000 个字符"
  }
  if (roles.length > ROLES_MAX) errors.roles = "角色不能超过 20 个"
  if (values.notes.trim().length > NOTES_MAX) errors.notes = "备注不能超过 2000 个字符"
  return errors
}

export function validateRepositoryForm(
  values: RepositoryFormValues,
): FormErrors<RepositoryFormValues> {
  const errors: FormErrors<RepositoryFormValues> = {}
  const name = values.name.trim()
  const path = values.path.trim()
  const branch = values.branch.trim()

  if (name.length === 0) errors.name = "请输入仓库名称"
  else if (name.length > REPOSITORY_NAME_MAX) errors.name = "仓库名称不能超过 100 个字符"
  if (path.length === 0) errors.path = "请输入仓库路径"
  else if (path.length > REPOSITORY_PATH_MAX) errors.path = "仓库路径不能超过 1000 个字符"
  if (branch.length > REPOSITORY_BRANCH_MAX) errors.branch = "分支不能超过 200 个字符"
  if (splitList(values.include).length > REPOSITORY_RULES_MAX) {
    errors.include = "包含规则不能超过 100 条"
  }
  if (splitList(values.exclude).length > REPOSITORY_RULES_MAX) {
    errors.exclude = "排除规则不能超过 100 条"
  }
  return errors
}

export type ImportParseResult<T> = { ok: true; items: T[] } | { ok: false; error: string }

const IMPORT_FORMAT_ERROR = '请粘贴要导入的 JSON（支持数组或 {"items":[]}）'

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function readString(value: unknown): string {
  return typeof value === "string" ? value.trim() : ""
}

function readImportList(
  text: string,
): { ok: true; list: unknown[] } | { ok: false; error: string } {
  const trimmed = text.trim()
  if (trimmed.length === 0) return { ok: false, error: IMPORT_FORMAT_ERROR }

  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch {
    return { ok: false, error: "JSON 解析失败，请检查格式" }
  }

  const list = Array.isArray(parsed)
    ? parsed
    : isRecord(parsed) && Array.isArray(parsed.items)
      ? parsed.items
      : null
  if (list === null) {
    return { ok: false, error: "JSON 必须是数组或包含 items 数组的对象" }
  }
  if (list.length === 0) return { ok: false, error: "导入列表不能为空" }
  return { ok: true, list }
}

function firstError(errors: Record<string, string | undefined>): string | null {
  for (const message of Object.values(errors)) {
    if (message !== undefined) return message
  }
  return null
}

export function parsePagesImport(text: string): ImportParseResult<PageFormValues> {
  const parsed = readImportList(text)
  if (!parsed.ok) return parsed

  const items: PageFormValues[] = []
  for (const [index, raw] of parsed.list.entries()) {
    if (!isRecord(raw)) return { ok: false, error: `第 ${index + 1} 项：必须是 JSON 对象` }

    const values: PageFormValues = {
      name: readString(raw.name),
      url: readString(raw.url),
      routeTemplate: readString(raw.route_template),
      pageType: readString(raw.page_type) || "page",
      authRole: readString(raw.auth_role),
      module: readString(raw.module),
    }
    const error = firstError(validatePageForm(values))
    if (error !== null) return { ok: false, error: `第 ${index + 1} 项：${error}` }
    items.push(values)
  }
  return { ok: true, items }
}

export function parseApisImport(text: string): ImportParseResult<ApiFormValues> {
  const parsed = readImportList(text)
  if (!parsed.ok) return parsed

  const items: ApiFormValues[] = []
  for (const [index, raw] of parsed.list.entries()) {
    if (!isRecord(raw)) return { ok: false, error: `第 ${index + 1} 项：必须是 JSON 对象` }

    const method = (readString(raw.method) || "GET").toUpperCase() as ApiMethod
    const values: ApiFormValues = {
      name: readString(raw.name),
      method,
      pathTemplate: readString(raw.path_template),
      authRole: readString(raw.auth_role),
      schemaRef: readString(raw.schema_ref),
    }
    const error = firstError(validateApiForm(values))
    if (error !== null) return { ok: false, error: `第 ${index + 1} 项：${error}` }
    items.push(values)
  }
  return { ok: true, items }
}

/** Type guard used by dialogs to narrow FormErrors to an empty object. */
export function hasErrors<T>(errors: FormErrors<T>): boolean {
  return Object.values(errors).some((message) => message !== undefined)
}

/** Status labels shared by every status select/table. */
export const STATUS_LABELS: Record<ResourceStatus, string> = {
  active: "启用",
  disabled: "停用",
}

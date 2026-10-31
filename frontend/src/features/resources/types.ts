/** Wire types shared by the resources console (contracts/openapi.yaml). */

export type ResourceTab = "suts" | "pages" | "data" | "repos"

export type ResourceStatus = "active" | "disabled"

export type ApiMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "HEAD" | "OPTIONS"

export type PageStatus = "approved" | "proposed" | "rejected"

export type PageSource = "admin" | "ai" | "import"

export type ApiSource = "code" | "openapi" | "ai" | "manual" | "import"

export type SutPage = {
  id: string
  sut_id: string
  environment_id: string
  name: string
  url: string
  route_template: string
  page_type: string
  auth_role: string
  module: string
  status: PageStatus
  source: PageSource
  created_at: string
  updated_at: string
}

export type SutApi = {
  id: string
  sut_id: string
  environment_id: string
  method: ApiMethod
  path_template: string
  name: string
  auth_role: string
  source: ApiSource
  status: PageStatus
  schema_ref: string
  created_at: string
  updated_at: string
}

export type TargetDatabase = {
  id: string
  sut_id: string
  environment_id: string
  name: string
  dsn: string
  username: string
  password_set: boolean
  read_only: boolean
  status: ResourceStatus
  created_at: string
  updated_at: string
}

export type TestAccount = {
  id: string
  sut_id: string
  environment_id: string
  username: string
  roles: string[]
  notes: string
  password_set: boolean
  status: ResourceStatus
  created_at: string
  updated_at: string
}

export type SutEnvironment = {
  id: string
  sut_id: string
  name: string
  base_url: string
  is_default: boolean
  status: ResourceStatus
  pages: SutPage[]
  apis: SutApi[]
  databases: TargetDatabase[]
  accounts: TestAccount[]
  created_at: string
  updated_at: string
}

export type Sut = {
  id: string
  name: string
  intro: string
  environments: SutEnvironment[]
  created_at: string
  updated_at: string
}

export type CodeRepository = {
  id: string
  name: string
  path: string
  branch: string
  include: string[]
  exclude: string[]
  created_by: string
  status: ResourceStatus
  created_at: string
  updated_at: string
}

export type ImportResult<T> = {
  created: number
  skipped: number
  items: T[]
}

/** Editable shape of the create-SUT form (SUT + its first default environment). */
export type SutFormValues = {
  name: string
  intro: string
  environmentName: string
  baseUrl: string
}

export type EnvironmentFormValues = {
  name: string
  baseUrl: string
  isDefault: boolean
  status: ResourceStatus
}

export type PageFormValues = {
  name: string
  url: string
  routeTemplate: string
  pageType: string
  authRole: string
  module: string
}

export type ApiFormValues = {
  name: string
  method: ApiMethod
  pathTemplate: string
  authRole: string
  schemaRef: string
}

export type DatabaseFormValues = {
  environmentId: string
  name: string
  dsn: string
  username: string
  password: string
  readOnly: boolean
}

export type AccountFormValues = {
  environmentId: string
  username: string
  password: string
  roles: string
  notes: string
}

export type RepositoryFormValues = {
  name: string
  path: string
  branch: string
  include: string
  exclude: string
}

export type FormErrors<T> = Partial<Record<keyof T, string>>

/** Shared page-level resource scope (selected SUT + environment). */
export type ResourceScope = {
  suts: Sut[]
  sutId: string
  environmentId: string
  detail: Sut | null
  loading: boolean
  detailLoading: boolean
  error: unknown
  detailError: unknown
}

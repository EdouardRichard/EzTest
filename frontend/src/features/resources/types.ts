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
  /** Optional SUT affinity hint; omitted by the global account pool when absent. */
  sut_id?: string
  username: string
  roles: string[]
  notes: string
  password_set: boolean
  status: ResourceStatus
  created_at: string
  updated_at: string
}

/** SUT registration source (resource_0005, v0.2.4-B). */
export type SutSource = "admin" | "ai" | "import"

/** SUT registration status, separate from the active/disabled lifecycle. */
export type SutRegistrationStatus = "proposed" | "approved" | "rejected"

/**
 * One top-level environment (v0.2.4-B hierarchy inversion). It owns SUTs; the
 * old per-SUT environment carrying base_url/is_default/assets is gone.
 */
export type SutEnvironment = {
  id: string
  name: string
  description: string
  status: ResourceStatus
  suts: Sut[]
  created_at: string
  updated_at: string
}

export type Sut = {
  id: string
  environment_id: string
  name: string
  intro: string
  base_url: string
  /** Optional lowercase system family used by `knowledge.scope='system'` (P7). */
  family?: string | null
  source: SutSource
  status: SutRegistrationStatus
  pages: SutPage[]
  apis: SutApi[]
  databases: TargetDatabase[]
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

/* ---------------------------- page/API proposals --------------------------- */

/** `CodeProposalCounts` from `POST /versions/{id}/code/page-api-proposals`. */
export type CodeProposalCounts = {
  created: number
  skipped: number
  ids: string[]
}

/** `CodeProposalResultView` (server runs synchronously, ≤200 items). */
export type CodeProposalResult = {
  pages: CodeProposalCounts
  apis: CodeProposalCounts
}

export type CodeProposalKind = "page" | "api"

/** `CodeProposalView`: a proposed resource plus its code/network evidence. */
export type CodeProposal = {
  kind: CodeProposalKind
  id: string
  name: string
  url?: string | null
  route_template?: string | null
  method?: string | null
  path_template?: string | null
  status: "proposed"
  source: "ai" | "code"
  evidence?: Record<string, unknown>[]
  created_at: string
}

/** Editable shape of the create-SUT form (SUT + its first default environment). */
export type SutFormValues = {
  name: string
  intro: string
  family?: string
  environmentName: string
  baseUrl: string
}

export type EnvironmentFormValues = {
  name: string
  description: string
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
  name: string
  dsn: string
  username: string
  password: string
  readOnly: boolean
}

export type AccountFormValues = {
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

/** Shared page-level resource scope (selected SUT + its environment). */
export type ResourceScope = {
  suts: Sut[]
  /** Top-level environments (v0.2.4-B: environments own SUTs). */
  environments: SutEnvironment[]
  sutId: string
  environmentId: string
  detail: Sut | null
  loading: boolean
  detailLoading: boolean
  error: unknown
  detailError: unknown
}

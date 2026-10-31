/** Wire types shared by the versions feature (contracts/openapi.yaml). */

export type VersionStatus = "initial" | "running" | "completed"

export type ResourceStatus = "active" | "disabled"

export type VersionTab =
  | "overview"
  | "requirements"
  | "design"
  | "execution"
  | "chain"
  | "issues"
  | "reports"
  | "code"

export type PageResource = {
  id: string
  sut_id: string
  environment_id: string
  name: string
  url: string
  route_template: string
  page_type: string
  auth_role: string
  module: string
  status: "approved" | "proposed" | "rejected"
  source: "admin" | "ai" | "import"
  created_at: string
  updated_at: string
}

export type ApiResource = {
  id: string
  sut_id: string
  environment_id: string
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "HEAD" | "OPTIONS"
  path_template: string
  name: string
  auth_role: string
  source: "code" | "openapi" | "ai" | "manual" | "import"
  status: "approved" | "proposed" | "rejected"
  schema_ref: string
  created_at: string
  updated_at: string
}

export type DatabaseResource = {
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

export type AccountResource = {
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

export type EnvironmentResource = {
  id: string
  sut_id: string
  name: string
  base_url: string
  is_default: boolean
  status: ResourceStatus
  pages: PageResource[]
  apis: ApiResource[]
  databases: DatabaseResource[]
  accounts: AccountResource[]
  created_at: string
  updated_at: string
}

export type SutResource = {
  id: string
  name: string
  intro: string
  environments: EnvironmentResource[]
  created_at: string
  updated_at: string
}

export type CodeRepositoryResource = {
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

export type Catalog = {
  suts: SutResource[]
  repositories: CodeRepositoryResource[]
}

export type VersionSummary = {
  id: string
  title: string
  status: VersionStatus
  sut_count: number
  created_at: string
  updated_at: string
}

/** Current case-version projection for one version (P2 spec §7.1/§9.2). */
export type VersionCaseCounts = {
  draft: number
  confirmed: number
  stale: number
}

export type VersionBinding = {
  id: string
  sut: SutResource
  environment: EnvironmentResource
  repository?: CodeRepositoryResource | null
  pages: PageResource[]
  apis: ApiResource[]
  databases: DatabaseResource[]
  accounts: AccountResource[]
}

export type Version = {
  id: string
  title: string
  status: VersionStatus
  auto_design_new_requirements: boolean
  bindings: VersionBinding[]
  /** P2: clarified requirements not activated into any design round yet. */
  pending_design_count: number
  /** P2: current case versions by status; `superseded` history is excluded. */
  case_counts: VersionCaseCounts
  created_by: string
  created_at: string
  updated_at: string
}

/** `PATCH /api/versions/{id}` (P0 allowed fields + P2 auto-design flag). */
export type UpdateVersionRequest = {
  auto_design_new_requirements?: boolean
}

/** `CreateVersionRequest` from the OpenAPI contract. */
export type BindingInput = {
  sut_id: string
  environment_id: string
  code_repository_id?: string | null
  page_ids: string[]
  api_ids: string[]
  database_ids: string[]
  accounts: Array<{ account_id: string; role?: string }>
}

export type CreateVersionRequest = {
  title: string
  bindings: BindingInput[]
}

/** Local, editable shape of one SUT binding inside the create wizard. */
export type BindingDraft = {
  key: string
  sutId: string
  environmentId: string
  repositoryId: string
  pageIds: string[]
  apiIds: string[]
  databaseIds: string[]
  accountIds: string[]
}

export type VersionDraft = {
  title: string
  bindings: BindingDraft[]
}

export type BindingErrors = {
  sutId?: string
  environmentId?: string
  pages?: string
  apis?: string
  databases?: string
  accounts?: string
  repositoryId?: string
}

export type VersionDraftErrors = {
  title?: string
  bindings?: string
  byBinding: Record<string, BindingErrors>
  valid: boolean
}

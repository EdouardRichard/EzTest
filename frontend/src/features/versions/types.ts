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
  /** P5: resolved code baseline commit; null until the repo branch resolves/indexes. */
  code_baseline_commit?: string | null
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
  pending_design_count?: number
  /** P2: current case versions by status; `superseded` history is excluded. */
  case_counts?: VersionCaseCounts
  /** P4: set when status becomes `completed`; cleared on reopen. */
  closed_at?: string | null
  /** P4: close reason captured with `closed_at`; cleared on reopen. */
  close_reason?: string | null
  created_by: string
  created_at: string
  updated_at: string
}

/** `POST /api/versions/{id}/reopen` (reason is mandatory). */
export type ReopenVersionInput = {
  reason: string
}

/** `PATCH /api/versions/{id}` (P0 allowed fields + P2 auto-design flag). */
export type UpdateVersionRequest = {
  auto_design_new_requirements?: boolean
}

/**
 * `POST /api/versions` request (v0.22 API-15): only the version metadata and the
 * environment. The SUT is derived from the environment; pages, APIs, databases,
 * accounts and repositories are no longer accepted here (R-C3-2).
 */
export type CreateVersionRequest = {
  title: string
  description: string
  goal: string
  environment_id: string
}

/** One environment-level exclusion document (`version_sut.scope_exclude`). */
export type BindingScopeExclude = {
  pages: string[]
  apis: string[]
  databases: string[]
}

/** Per-resource counts of one environment-owned kind (Task 29 binding DTO). */
export type BindingResourceCounts = {
  approved: number
  proposed: number
  excluded: number
  included: number
}

export type BindingCounts = {
  pages: BindingResourceCounts
  apis: BindingResourceCounts
  databases: BindingResourceCounts
}

/**
 * One effective repository of a version binding. `included` reports whether the
 * repository is effective for the version; `source` is `sut` (inherited) or
 * `override` (version-level include).
 */
export type BindingRepositoryRow = {
  repository: CodeRepositoryResource
  role: string
  included: boolean
  source: string
}

/**
 * `GET /api/versions/{id}/binding` read model (v0.22 API-16, Task 29
 * `bindingOverviewDTO`). `pages`/`apis`/`databases` are the effective
 * (approved minus excluded) sets; `counts` carries approved/proposed/excluded/
 * included for every kind.
 */
export type VersionBindingOverview = {
  version_id: string
  stage: string
  editable: boolean
  sut: SutResource
  environment: EnvironmentResource
  scope_exclude: BindingScopeExclude
  counts: BindingCounts
  pages: PageResource[]
  apis: ApiResource[]
  databases: DatabaseResource[]
  accounts: AccountResource[]
  repositories: BindingRepositoryRow[]
  code_baseline_commit: string | null
}

/**
 * `PUT|PATCH /api/versions/{id}/binding` partial update (v0.22 API-16). Omitted
 * fields are untouched; an explicit empty list clears the selection.
 */
export type UpdateBindingRequest = {
  environment_id?: string
  repository_ids?: string[]
  account_ids?: string[]
  scope_exclude?: BindingScopeExclude
}

/**
 * One SUT-level repository association (v0.22 API-17, Task 29
 * `SUTRepositoryView`). Task 37 mounts the route; the wire shape mirrors the
 * resource application view.
 */
export type SutRepository = {
  id: string
  sut_id: string
  repository: CodeRepositoryResource
  role: string
  enabled: boolean
  created_at: string
  updated_at: string
}

/** `POST /api/suts/{id}/repositories` request body. */
export type CreateSutRepositoryRequest = {
  repository_id: string
  role: string
}

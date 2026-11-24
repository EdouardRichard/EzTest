/** Wire types shared by the versions feature (contracts/openapi.yaml). */

import type { VersionClosureKind } from "@/features/reports/types"

// Single-source (V-0017): validated against the Go authority version/domain
// Statuses() by backend/internal/version/domain/enum_authority_test.go. Do not
// add/remove a member here without updating the domain enum.
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

/**
 * One top-level environment (v0.2.4-B hierarchy inversion). It owns SUTs and no
 * longer carries base_url or assets directly; assets live on each SUT.
 */
export type EnvironmentResource = {
  id: string
  name: string
  description: string
  status: ResourceStatus
  suts: SutResource[]
  created_at: string
  updated_at: string
}

/**
 * One SUT nested under a version's bound environment (version-side `sutDTO`).
 * Unlike the resource-console SUT it carries no `source`/`status` registration
 * fields (version/interfaces/httpapi/dto.go).
 */
export type SutResource = {
  id: string
  environment_id: string
  name: string
  intro: string
  base_url: string
  family?: string | null
  pages: PageResource[]
  apis: ApiResource[]
  databases: DatabaseResource[]
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

/** `GET /api/resources/catalog` (v0.2.4-B): environments, code repositories, accounts. */
export type Catalog = {
  environments: EnvironmentResource[]
  repositories: CodeRepositoryResource[]
  accounts: AccountResource[]
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

/**
 * The resolved environment catalog of one version (v0.2.4-B `bindingDTO`). The
 * version binds one environment directly; the flattened asset unions, the
 * version account subset, the environment code pool and the pinned baseline
 * commit replace the old per-SUT bindings array.
 */
export type VersionBinding = {
  environment: EnvironmentResource
  pages: PageResource[]
  apis: ApiResource[]
  databases: DatabaseResource[]
  accounts: AccountResource[]
  repositories: BindingRepositoryRow[]
  /** Resolved code baseline commit; null/omitted until the repo branch resolves. */
  code_baseline_commit?: string | null
}

export type Version = {
  id: string
  title: string
  status: VersionStatus
  auto_design_new_requirements: boolean
  /** v0.2.4-B: the version binds one environment directly (NOT NULL). */
  environment_id: string
  binding: VersionBinding
  /** P2: clarified requirements not activated into any design round yet. */
  pending_design_count?: number
  /** P2: current case versions by status; `superseded` history is excluded. */
  case_counts?: VersionCaseCounts
  /**
   * R3-E-01: requirement status counts of `GET /api/versions/{id}`. The
   * reporting-stage completion panel reads them to explain the remaining work
   * without depending on the requirement list request.
   */
  requirement_counts?: {
    total: number
    draft: number
    clarified: number
    closed: number
    obsolete: number
  }
  /** P4: set when status becomes `completed`; cleared on reopen. */
  closed_at?: string | null
  /** P4: close reason captured with `closed_at`; cleared on reopen. */
  close_reason?: string | null
  /** v0.2.4-G T6: closure kind (normal vs admin waived); cleared on reopen. */
  closure_kind?: VersionClosureKind | null
  /** v0.2.4-G T6: waived closure marks the version unverified; cleared on reopen. */
  unverified?: boolean | null
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

/**
 * Per-resource counts of one asset kind (v0.2.4-B `bindingResourceCountsDTO`).
 * Exclusions disappeared with the version_sut chain, so approved is also the
 * included count.
 */
export type BindingResourceCounts = {
  approved: number
  proposed: number
}

export type BindingCounts = {
  pages: BindingResourceCounts
  apis: BindingResourceCounts
  databases: BindingResourceCounts
}

/**
 * One repository of the environment code pool (v0.2.4-B `bindingRepositoryDTO`).
 * `sut_id` names the SUT whose association supplied the row, which is also the
 * key the resolved code baseline is stored under.
 */
export type BindingRepositoryRow = {
  repository: CodeRepositoryResource
  sut_id: string
  role: string
  code_baseline_commit: string | null
}

/** R-C4-1 scope snapshot projection of the binding overview (v0.2.4-B). */
export type BindingSnapshot = {
  scope_version: string
  frozen_wave_id: string | null
}

/**
 * `GET /api/versions/{id}/binding` read model (v0.2.4-B `bindingOverviewDTO`).
 * `pages`/`apis`/`databases` are the effective approved sets of the bound
 * environment; `counts` carries approved/proposed for every kind.
 */
export type VersionBindingOverview = {
  version_id: string
  environment_id: string
  stage: string
  editable: boolean
  environment: EnvironmentResource
  counts: BindingCounts
  pages: PageResource[]
  apis: ApiResource[]
  databases: DatabaseResource[]
  accounts: AccountResource[]
  repositories: BindingRepositoryRow[]
  code_baseline_commit: string | null
  snapshot: BindingSnapshot
}

/**
 * `PUT|PATCH /api/versions/{id}/binding` partial update (v0.2.4-B). The code pool
 * is derived from the bound environment and every approved asset of its SUTs is
 * in scope, so only `environment_id` and `account_ids` remain. Omitted fields are
 * untouched; an explicit empty list clears the selection.
 */
export type UpdateBindingRequest = {
  environment_id?: string
  account_ids?: string[]
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

/* ------------------------- discovery observations (API-36..40) ---------------- */

/** DB-29 resource kind of one execution-time observation. */
export type DiscoveryKind = "page" | "api"

/** Five-way DB-29 decision of one observation. */
export type DiscoveryDecision = "new" | "proposed" | "approved" | "rejected" | "out_of_scope"

/**
 * One DB-29 observation (`GET /api/versions/{id}/discovery/observations`,
 * W1-14A/API-36). Every id is a decimal string and every nullable provenance
 * column is `null` rather than a zero id.
 */
export type DiscoveryObservation = {
  observation_id: string
  kind: DiscoveryKind
  method: string
  host: string
  resource_template: string
  status_code: number | null
  mime: string
  source_kind: string
  occurrences: number
  decision: DiscoveryDecision
  wave_id: string | null
  case_run_id: string | null
  sut_id: string
  environment_id: string | null
  resource_id: string | null
  fact_id: string | null
  first_seen_at: string
  last_seen_at: string
  created_at: string
  updated_at: string
}

/** Five-way decision counts of one delta pass (API-37). */
export type DiscoveryDecisionCounts = {
  new: number
  proposed: number
  approved: number
  rejected: number
  out_of_scope: number
}

/** Reduced per-resource projection of `GET .../discovery/deltas` (API-37). */
export type DiscoveryDeltaItem = {
  observation_id: string
  kind: DiscoveryKind
  method: string
  host: string
  resource_template: string
  decision: DiscoveryDecision
  occurrences: number
  wave_id: string | null
  case_run_id: string | null
  resource_id: string | null
  fact_id: string | null
}

/** `GET /api/versions/{id}/discovery/deltas?wave_id=` payload. */
export type DiscoveryDeltas = {
  counts: DiscoveryDecisionCounts
  items: DiscoveryDeltaItem[]
}

/**
 * Discovery-side coverage of one kind (`GET /api/versions/{id}/coverage`).
 * `observed` counts distinct resource templates; the four decision buckets are
 * row counts; `uncovered` lists the still-proposed resource identities (API
 * identities are `"METHOD template"`, page identities the template alone).
 */
export type DiscoveryCoverageBucket = {
  observed: number
  proposed: number
  approved: number
  rejected: number
  out_of_scope: number
  uncovered: string[]
}

/** `GET /api/versions/{id}/coverage` payload (W1 discovery-side semantics). */
export type DiscoveryCoverage = {
  pages: DiscoveryCoverageBucket
  apis: DiscoveryCoverageBucket
}

/** One current `version_fact kind='gap'` row (`GET /api/versions/{id}/gaps`). */
export type VersionGap = {
  fact_id: string
  key: string
  payload: Record<string, unknown>
  confidence: number | null
  created_at: string
}

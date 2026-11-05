import type {
  AccountResource,
  ApiResource,
  BindingResourceCounts,
  BindingScopeExclude,
  Catalog,
  CreateVersionRequest,
  DatabaseResource,
  EnvironmentResource,
  PageResource,
  SutResource,
  UpdateBindingRequest,
} from "./types"

/** One selectable environment with the SUT it derives (R-C3-1). */
export type EnvironmentOption = {
  sut: SutResource
  environment: EnvironmentResource
}

/** Flattens the catalog into one option per environment, in catalog order. */
export function environmentOptions(catalog: Catalog): EnvironmentOption[] {
  const options: EnvironmentOption[] = []
  for (const sut of catalog.suts) {
    for (const environment of sut.environments) {
      options.push({ sut, environment })
    }
  }
  return options
}

/** Resolves the environment and its derived SUT; undefined for unknown ids. */
export function findEnvironmentOption(
  catalog: Catalog,
  environmentId: string,
): EnvironmentOption | undefined {
  return environmentOptions(catalog).find((option) => option.environment.id === environmentId)
}

/** The version scope only inherits approved pages/APIs (R-C3-3). */
export function approvedPages(environment: EnvironmentResource): PageResource[] {
  return environment.pages.filter((page) => page.status === "approved")
}

export function approvedAPIs(environment: EnvironmentResource): ApiResource[] {
  return environment.apis.filter((api) => api.status === "approved")
}

/** Target databases are environment-owned; disabled rows are not selectable. */
export function scopeDatabases(environment: EnvironmentResource): DatabaseResource[] {
  return environment.databases.filter((database) => database.status === "active")
}

/**
 * Global test-account pool filtered by environment compatibility. In v0.22 the
 * pool is global (`AccountView.SUTID/EnvironmentID` are empty), so compatibility
 * means an active account whose legacy environment scope is empty or matches the
 * bound environment. Duplicates shared by catalog environments collapse.
 */
export function activeAccounts(environment: EnvironmentResource): AccountResource[] {
  const seen = new Set<string>()
  const accounts: AccountResource[] = []
  for (const account of environment.accounts) {
    if (account.status !== "active") continue
    if (account.environment_id !== "" && account.environment_id !== environment.id) continue
    if (seen.has(account.id)) continue
    seen.add(account.id)
    accounts.push(account)
  }
  return accounts
}

type Countable = { id: string; status: string }

/**
 * Mirrors the backend `countResources`: approved pages/APIs and active
 * databases count as approved, proposed ones are reported separately and the
 * exclusion document only subtracts approved resources.
 */
export function resourceCounts(
  resources: Countable[],
  excludedIds: string[],
): BindingResourceCounts {
  const excluded = new Set(excludedIds)
  const counts: BindingResourceCounts = { approved: 0, proposed: 0, excluded: 0, included: 0 }
  for (const resource of resources) {
    if (resource.status === "approved" || resource.status === "active" || resource.status === "") {
      counts.approved += 1
      if (excluded.has(resource.id)) counts.excluded += 1
    } else if (resource.status === "proposed") {
      counts.proposed += 1
    }
  }
  counts.included = Math.max(0, counts.approved - counts.excluded)
  return counts
}

export function emptyScopeExclude(): BindingScopeExclude {
  return { pages: [], apis: [], databases: [] }
}

/** Adds/removes one id while keeping the user's selection order. */
export function toggleSelection(ids: string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((current) => current !== id) : [...ids, id]
}

/** Adds/removes one resource id from the exclusion document of one kind. */
export function toggleExclude(
  scope: BindingScopeExclude,
  kind: keyof BindingScopeExclude,
  id: string,
): BindingScopeExclude {
  return { ...scope, [kind]: toggleSelection(scope[kind], id) }
}

export type CreateVersionInput = {
  title: string
  description: string
  goal: string
  environmentId: string
}

/** Builds the exact API-15 environment-only create body. */
export function buildCreateVersionPayload(input: CreateVersionInput): CreateVersionRequest {
  return {
    title: input.title.trim(),
    description: input.description,
    goal: input.goal,
    environment_id: input.environmentId,
  }
}

export type BindingUpdateInput = {
  /** null means the SUT repository read failed; the field is then omitted so
   * the version keeps inheriting the SUT repositories. */
  includedRepositoryIds: string[] | null
  /** null means the global account pool could not be read; omitted. */
  accountIds: string[] | null
  scopeExclude: BindingScopeExclude
}

/**
 * Builds the partial API-16 update. Explicit empty arrays are meaningful (they
 * clear the selection), while null selections omit the field entirely.
 */
export function buildBindingUpdate(input: BindingUpdateInput): UpdateBindingRequest {
  const payload: UpdateBindingRequest = {
    scope_exclude: {
      pages: [...input.scopeExclude.pages],
      apis: [...input.scopeExclude.apis],
      databases: [...input.scopeExclude.databases],
    },
  }
  if (input.includedRepositoryIds !== null)
    payload.repository_ids = [...input.includedRepositoryIds]
  if (input.accountIds !== null) payload.account_ids = [...input.accountIds]
  return payload
}

/** Maps a typed binding 400 `details.field` back to the wizard step to fix. */
export function stepForBindingField(field: string): 1 | 2 | 3 | 4 {
  const normalized = field.trim()
  if (normalized === "") return 4
  if (normalized.startsWith("repository_ids") || normalized.startsWith("account_ids")) return 2
  if (normalized.startsWith("scope_exclude")) return 3
  if (normalized === "title" || normalized === "environment_id") return 1
  return 4
}

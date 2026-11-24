import type {
  AccountResource,
  ApiResource,
  BindingResourceCounts,
  Catalog,
  CreateVersionRequest,
  DatabaseResource,
  EnvironmentResource,
  PageResource,
  SutResource,
  UpdateBindingRequest,
} from "./types"

/**
 * One selectable environment (v0.2.4-B). The version binds the environment
 * directly; `sut` is the environment's representative (first) SUT, a convenience
 * accessor. The wizard aggregates resources across the full `environment.suts`
 * catalog (code pool, pages, APIs, databases), not just the representative.
 */
export type EnvironmentOption = {
  sut: SutResource
  environment: EnvironmentResource
}

/** One option per top-level environment; environments without a SUT are skipped. */
export function environmentOptions(catalog: Catalog): EnvironmentOption[] {
  const options: EnvironmentOption[] = []
  for (const environment of catalog.environments) {
    const sut = environment.suts[0]
    if (sut === undefined) continue
    options.push({ sut, environment })
  }
  return options
}

/** Resolves the environment and its representative SUT; undefined for unknown ids. */
export function findEnvironmentOption(
  catalog: Catalog,
  environmentId: string,
): EnvironmentOption | undefined {
  return environmentOptions(catalog).find((option) => option.environment.id === environmentId)
}

/** Approved pages across every SUT of the environment (assets are per-SUT). */
export function approvedPages(environment: EnvironmentResource): PageResource[] {
  return environment.suts.flatMap((sut) => sut.pages).filter((page) => page.status === "approved")
}

export function approvedAPIs(environment: EnvironmentResource): ApiResource[] {
  return environment.suts.flatMap((sut) => sut.apis).filter((api) => api.status === "approved")
}

export function scopeDatabases(environment: EnvironmentResource): DatabaseResource[] {
  return environment.suts
    .flatMap((sut) => sut.databases)
    .filter((database) => database.status === "active")
}

/**
 * The global test-account pool filtered to active accounts, deduplicated by id
 * (v0.2.4-B: accounts are a global pool, not carried on the environment).
 */
export function activeAccounts(accounts: AccountResource[]): AccountResource[] {
  const options: AccountResource[] = []
  const seen = new Set<string>()
  for (const account of accounts) {
    if (account.status !== "active") continue
    if (seen.has(account.id)) continue
    seen.add(account.id)
    options.push(account)
  }
  return options
}

type Countable = { id: string; status: string }

/**
 * Mirrors the backend `countResources` (v0.2.4-B): exclusions disappeared with the
 * version_sut chain, so only approved/proposed remain. `active`/empty status count
 * as approved (databases use the active/disabled lifecycle).
 */
export function resourceCounts(resources: Countable[]): BindingResourceCounts {
  const counts: BindingResourceCounts = { approved: 0, proposed: 0 }
  for (const resource of resources) {
    if (resource.status === "approved" || resource.status === "active" || resource.status === "") {
      counts.approved += 1
    } else if (resource.status === "proposed") {
      counts.proposed += 1
    }
  }
  return counts
}

/** Adds/removes one id while keeping the user's selection order. */
export function toggleSelection(ids: string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((current) => current !== id) : [...ids, id]
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
  /** null means the global account pool could not be read; omitted. */
  accountIds: string[] | null
}

/**
 * Builds the partial API-16 update (v0.2.4-B). The code pool is derived from the
 * bound environment and every approved asset of its SUTs is in scope, so only
 * account_ids remains writable; repository_ids and scope_exclude were deleted.
 */
export function buildBindingUpdate(input: BindingUpdateInput): UpdateBindingRequest {
  const payload: UpdateBindingRequest = {}
  if (input.accountIds !== null) payload.account_ids = [...input.accountIds]
  return payload
}

/**
 * Maps a typed binding 400 `details.field` back to the wizard step to fix. Only
 * environment_id (step 1) and account_ids (step 2) are writable in v0.2.4-B.
 */
export function stepForBindingField(field: string): 1 | 2 | 3 | 4 {
  const normalized = field.trim()
  if (normalized === "") return 4
  if (normalized.startsWith("account_ids")) return 2
  if (normalized === "title" || normalized === "environment_id") return 1
  return 4
}

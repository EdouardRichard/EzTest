import { apiDelete, apiGet, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

import type {
  AccountFormValues,
  ApiFormValues,
  CodeProposal,
  CodeProposalResult,
  CodeRepository,
  DatabaseFormValues,
  EnvironmentFormValues,
  ImportResult,
  PageFormValues,
  RepositoryFormValues,
  Sut,
  SutApi,
  SutEnvironment,
  SutPage,
  TargetDatabase,
  TestAccount,
} from "./types"
import { splitList } from "./validation"

const PAGE_LIMIT = 100

// --- SUT ------------------------------------------------------------------

/** First page of SUTs; the P0 console lists up to the API maximum per request. */
export async function listSuts(): Promise<Sut[]> {
  const page = await apiGetPage<Sut>(`/api/resources/suts?limit=${PAGE_LIMIT}`)
  return page.items
}

/** One SUT aggregate with its environments and nested resources. */
export function getSut(id: string): Promise<Sut> {
  return apiGet<Sut>(`/api/resources/suts/${encodeURIComponent(id)}`)
}

export type SutInput = {
  name: string
  intro: string
  /**
   * Optional P7 family slug. Create and patch have DIFFERENT blank semantics: for
   * create a blank value is omitted (server default); for patch a PRESENT blank
   * value clears it and an absent value leaves it unchanged, mirroring the backend
   * `resource/application.SUTPatch` pointer (`Family *string`: "" clears, nil keeps).
   */
  family?: string
  /** SUT base URL (v0.2.4-B: base_url lives on the SUT, not the environment). */
  baseUrl?: string
}

/**
 * Builds the SUT CREATE body. Optional keys are omitted when blank so the server
 * applies its defaults. (`base_url` is required on create — a blank one yields a
 * server 400; that is a create-validation gap reported separately, not silently
 * worked around here.)
 */
function sutCreatePayload(input: SutInput): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    name: input.name.trim(),
    intro: input.intro.trim(),
  }
  const family = input.family?.trim() ?? ""
  if (family.length > 0) payload.family = family
  const baseUrl = input.baseUrl?.trim() ?? ""
  if (baseUrl.length > 0) payload.base_url = baseUrl
  return payload
}

/**
 * Builds the SUT PATCH body. Both optional keys treat presence (undefined = leave
 * unchanged) like the backend `*string`, but their EMPTY-string meaning differs:
 *   - family: "" CLEARS the stored family — the backend accepts a blank family
 *     (`Family *string`; `normalizeFamilyInput`), so a present blank is sent as "".
 *   - base_url: "" is INVALID, not a clear — base_url is required
 *     (`resource/domain/sut.go` `SUT.Validate` → `validation.URL` rejects a blank
 *     URL with 400), so there is no "clear": a blank/absent value is OMITTED (leave
 *     unchanged). Sending "" would fail the request.
 * Sharing the create payload builder made the UI's "clear family" a silent no-op
 * (H-C2): the key was dropped and the backend saw nil = no change.
 */
function sutPatchPayload(input: SutInput): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    name: input.name.trim(),
    intro: input.intro.trim(),
  }
  if (input.family !== undefined) payload.family = input.family.trim()
  const baseUrl = input.baseUrl?.trim() ?? ""
  if (baseUrl.length > 0) payload.base_url = baseUrl
  return payload
}

/** v0.2.4-B: a SUT is created under an environment (`POST /environments/{id}/suts`). */
export function createSut(environmentId: string, input: SutInput): Promise<Sut> {
  return apiPost<Sut>(
    `/api/resources/environments/${encodeURIComponent(environmentId)}/suts`,
    sutCreatePayload(input),
  )
}

export function updateSut(id: string, input: SutInput): Promise<Sut> {
  return apiPatch<Sut>(`/api/resources/suts/${encodeURIComponent(id)}`, sutPatchPayload(input))
}

export function deleteSut(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/suts/${encodeURIComponent(id)}`)
}

/**
 * Admin-only post-hoc audit/reject of an Agent-registered SUT (v0.2.4-F R-B4/R-F1).
 * The Agent auto-approves a new SUT on evidence; an admin can reject it afterwards,
 * or reject a proposed SUT the gate did not approve. proposed/approved → rejected
 * (409 when already rejected). A rejected SUT leaves the version's effective scope.
 */
export function rejectSut(id: string): Promise<Sut> {
  return apiPost<Sut>(`/api/resources/suts/${encodeURIComponent(id)}/reject`)
}

export type CreateSutWithDefaultEnvironmentInput = {
  sut: SutInput
  environment: {
    name: string
    base_url: string
  }
}

export type CreateSutWithDefaultEnvironmentResult = {
  sut: Sut
  environment: SutEnvironment
}

/**
 * A SUT create that stores the environment but fails to create the SUT under it.
 * The environment exists at this point; the UI refreshes the list and lets the
 * user add the SUT from the SUT section instead of retrying the whole flow.
 */
export class DefaultEnvironmentError extends Error {
  readonly environment: SutEnvironment
  readonly originalError: unknown

  constructor(environment: SutEnvironment, originalError: unknown) {
    const detail = originalError instanceof Error ? originalError.message : "未知错误"
    super(
      `环境「${environment.name}」已创建，但 SUT 创建失败：${detail}。请在 SUT 列表中补建 SUT。`,
    )
    this.name = "DefaultEnvironmentError"
    this.environment = environment
    this.originalError = originalError
  }
}

/**
 * Creates a top-level environment and then the SUT under it in one user flow
 * (v0.2.4-B: environments own SUTs; base_url lives on the SUT). The API has no
 * atomic environment+SUT create, so a partial failure is surfaced with the
 * created environment attached.
 */
export async function createSutWithDefaultEnvironment(
  input: CreateSutWithDefaultEnvironmentInput,
): Promise<CreateSutWithDefaultEnvironmentResult> {
  const environment = await createEnvironment({
    name: input.environment.name,
    description: "",
    status: "active",
  })
  try {
    const sut = await createSut(environment.id, {
      name: input.sut.name,
      intro: input.sut.intro,
      ...(input.sut.family !== undefined ? { family: input.sut.family } : {}),
      baseUrl: input.environment.base_url,
    })
    return { sut, environment }
  } catch (error) {
    throw new DefaultEnvironmentError(environment, error)
  }
}

// --- environments ---------------------------------------------------------

function environmentPayload(values: EnvironmentFormValues): Record<string, unknown> {
  return {
    name: values.name.trim(),
    description: values.description.trim(),
    status: values.status,
  }
}

/** First page of top-level environments (v0.2.4-B: environments own SUTs). */
export async function listEnvironments(): Promise<SutEnvironment[]> {
  const page = await apiGetPage<SutEnvironment>(`/api/resources/environments?limit=${PAGE_LIMIT}`)
  return page.items
}

export function createEnvironment(values: EnvironmentFormValues): Promise<SutEnvironment> {
  return apiPost<SutEnvironment>("/api/resources/environments", environmentPayload(values))
}

export function updateEnvironment(
  id: string,
  values: EnvironmentFormValues,
): Promise<SutEnvironment> {
  return apiPatch<SutEnvironment>(
    `/api/resources/environments/${encodeURIComponent(id)}`,
    environmentPayload(values),
  )
}

export function deleteEnvironment(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/environments/${encodeURIComponent(id)}`)
}

// --- pages ----------------------------------------------------------------

function pagePayload(values: PageFormValues): Record<string, unknown> {
  return {
    name: values.name.trim(),
    url: values.url.trim(),
    route_template: values.routeTemplate.trim(),
    page_type: values.pageType.trim() || "page",
    auth_role: values.authRole.trim(),
    module: values.module.trim(),
  }
}

export function createPage(sutId: string, values: PageFormValues): Promise<SutPage> {
  return apiPost<SutPage>(
    `/api/resources/suts/${encodeURIComponent(sutId)}/pages`,
    pagePayload(values),
  )
}

export function updatePage(id: string, values: PageFormValues): Promise<SutPage> {
  return apiPatch<SutPage>(`/api/resources/pages/${encodeURIComponent(id)}`, pagePayload(values))
}

export function deletePage(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/pages/${encodeURIComponent(id)}`)
}

export function importPages(
  sutId: string,
  items: PageFormValues[],
): Promise<ImportResult<SutPage>> {
  return apiPost<ImportResult<SutPage>>(
    `/api/resources/suts/${encodeURIComponent(sutId)}/pages/import`,
    { items: items.map(pagePayload) },
  )
}

// --- APIs -----------------------------------------------------------------

function apiPayload(values: ApiFormValues): Record<string, unknown> {
  return {
    method: values.method,
    path_template: values.pathTemplate.trim(),
    name: values.name.trim(),
    auth_role: values.authRole.trim(),
    schema_ref: values.schemaRef.trim(),
  }
}

export function createApi(sutId: string, values: ApiFormValues): Promise<SutApi> {
  return apiPost<SutApi>(
    `/api/resources/suts/${encodeURIComponent(sutId)}/apis`,
    apiPayload(values),
  )
}

export function updateApi(id: string, values: ApiFormValues): Promise<SutApi> {
  return apiPatch<SutApi>(`/api/resources/apis/${encodeURIComponent(id)}`, apiPayload(values))
}

export function deleteApi(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/apis/${encodeURIComponent(id)}`)
}

export function importApis(sutId: string, items: ApiFormValues[]): Promise<ImportResult<SutApi>> {
  return apiPost<ImportResult<SutApi>>(
    `/api/resources/suts/${encodeURIComponent(sutId)}/apis/import`,
    { items: items.map(apiPayload) },
  )
}

// --- target databases -----------------------------------------------------

export type DatabasePatch = {
  name: string
  dsn: string
  username: string
  readOnly: boolean
  /** Omitted keeps the stored password; an empty string clears it. */
  password?: string
}

export function createDatabase(sutId: string, values: DatabaseFormValues): Promise<TargetDatabase> {
  return apiPost<TargetDatabase>(`/api/resources/suts/${encodeURIComponent(sutId)}/databases`, {
    name: values.name.trim(),
    dsn: values.dsn.trim(),
    username: values.username.trim(),
    password: values.password,
    read_only: values.readOnly,
  })
}

export function updateDatabase(id: string, patch: DatabasePatch): Promise<TargetDatabase> {
  const body: Record<string, unknown> = {
    name: patch.name.trim(),
    dsn: patch.dsn.trim(),
    username: patch.username.trim(),
    read_only: patch.readOnly,
  }
  if (patch.password !== undefined) body.password = patch.password
  return apiPatch<TargetDatabase>(`/api/resources/databases/${encodeURIComponent(id)}`, body)
}

export function deleteDatabase(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/databases/${encodeURIComponent(id)}`)
}

// --- test accounts --------------------------------------------------------

export type AccountPatch = {
  username: string
  roles: string[]
  notes: string
  /** Omitted keeps the stored password; an empty string clears it. */
  password?: string
}

/** The global test-account pool (v0.2.4-B: accounts are not SUT-scoped). */
export async function listAccounts(): Promise<TestAccount[]> {
  const page = await apiGetPage<TestAccount>(`/api/resources/accounts?limit=${PAGE_LIMIT}`)
  return page.items
}

/**
 * Creates a row of the global account pool. `sutId` is the optional SUT affinity
 * hint (empty means no affinity); the account is not owned by the SUT.
 */
export function createAccount(sutId: string, values: AccountFormValues): Promise<TestAccount> {
  return apiPost<TestAccount>("/api/resources/accounts", {
    sut_id: sutId,
    username: values.username.trim(),
    password: values.password,
    roles: splitList(values.roles),
    notes: values.notes.trim(),
  })
}

export function updateAccount(id: string, patch: AccountPatch): Promise<TestAccount> {
  const body: Record<string, unknown> = {
    username: patch.username.trim(),
    roles: patch.roles,
    notes: patch.notes.trim(),
  }
  if (patch.password !== undefined) body.password = patch.password
  return apiPatch<TestAccount>(`/api/resources/accounts/${encodeURIComponent(id)}`, body)
}

export function deleteAccount(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/accounts/${encodeURIComponent(id)}`)
}

// --- code repositories ----------------------------------------------------

/** First page of code repositories; repositories are not SUT-scoped. */
export async function listRepositories(): Promise<CodeRepository[]> {
  const page = await apiGetPage<CodeRepository>(`/api/resources/code-repos?limit=${PAGE_LIMIT}`)
  return page.items
}

function repositoryPayload(values: RepositoryFormValues): Record<string, unknown> {
  return {
    name: values.name.trim(),
    path: values.path.trim(),
    branch: values.branch.trim(),
    include: splitList(values.include),
    exclude: splitList(values.exclude),
  }
}

export function createRepository(values: RepositoryFormValues): Promise<CodeRepository> {
  return apiPost<CodeRepository>("/api/resources/code-repos", repositoryPayload(values))
}

export function updateRepository(
  id: string,
  values: RepositoryFormValues,
): Promise<CodeRepository> {
  return apiPatch<CodeRepository>(
    `/api/resources/code-repos/${encodeURIComponent(id)}`,
    repositoryPayload(values),
  )
}

export function deleteRepository(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/code-repos/${encodeURIComponent(id)}`)
}

// --- page/API proposals (P5) ----------------------------------------------

/**
 * Generates proposed pages/APIs from indexed code facts and execution network
 * evidence (spec §8.1). The optional `repoId` narrows a multi-repo version;
 * omitted means the server walks every bound repository.
 */
export function generatePageApiProposals(
  versionId: string,
  options: { repoId?: string } = {},
): Promise<CodeProposalResult> {
  const body = options.repoId !== undefined ? { repoId: options.repoId } : undefined
  return apiPost<CodeProposalResult>(
    `/api/versions/${encodeURIComponent(versionId)}/code/page-api-proposals`,
    body,
  )
}

/** Lists the version's `status=proposed` pages/APIs with their source/evidence. */
export async function listPageApiProposals(versionId: string): Promise<CodeProposal[]> {
  const page = await apiGetPage<CodeProposal>(
    `/api/versions/${encodeURIComponent(versionId)}/code/page-api-proposals`,
  )
  return page.items
}

/** Admin-only: proposed page → approved (409 when the state changed). */
export function confirmPageProposal(id: string): Promise<SutPage> {
  return apiPost<SutPage>(`/api/resources/pages/${encodeURIComponent(id)}/confirm`)
}

export function rejectPageProposal(id: string): Promise<SutPage> {
  return apiPost<SutPage>(`/api/resources/pages/${encodeURIComponent(id)}/reject`)
}

/** Admin-only: proposed API → approved/rejected. */
export function confirmApiProposal(id: string): Promise<SutApi> {
  return apiPost<SutApi>(`/api/resources/apis/${encodeURIComponent(id)}/confirm`)
}

export function rejectApiProposal(id: string): Promise<SutApi> {
  return apiPost<SutApi>(`/api/resources/apis/${encodeURIComponent(id)}/reject`)
}

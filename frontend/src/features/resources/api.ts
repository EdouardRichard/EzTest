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
}

export function createSut(input: SutInput): Promise<Sut> {
  return apiPost<Sut>("/api/resources/suts", {
    name: input.name.trim(),
    intro: input.intro.trim(),
  })
}

export function updateSut(id: string, input: SutInput): Promise<Sut> {
  return apiPatch<Sut>(`/api/resources/suts/${encodeURIComponent(id)}`, {
    name: input.name.trim(),
    intro: input.intro.trim(),
  })
}

export function deleteSut(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/suts/${encodeURIComponent(id)}`)
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
 * A SUT create that stores the SUT but fails to create its default environment.
 * The SUT exists at this point; the UI refreshes the list and lets the user add
 * the environment from the environment section instead of retrying the create.
 */
export class DefaultEnvironmentError extends Error {
  readonly sut: Sut
  readonly originalError: unknown

  constructor(sut: Sut, originalError: unknown) {
    const detail = originalError instanceof Error ? originalError.message : "未知错误"
    super(`SUT「${sut.name}」已创建，但默认环境创建失败：${detail}。请在环境列表中补建环境。`)
    this.name = "DefaultEnvironmentError"
    this.sut = sut
    this.originalError = originalError
  }
}

/**
 * Creates a SUT and its first default environment in one user flow. The API has
 * no atomic SUT+environment create, so a partial failure is surfaced with the
 * created SUT attached.
 */
export async function createSutWithDefaultEnvironment(
  input: CreateSutWithDefaultEnvironmentInput,
): Promise<CreateSutWithDefaultEnvironmentResult> {
  const sut = await createSut(input.sut)
  try {
    const environment = await createEnvironment(sut.id, {
      name: input.environment.name,
      baseUrl: input.environment.base_url,
      isDefault: true,
      status: "active",
    })
    return { sut, environment }
  } catch (error) {
    throw new DefaultEnvironmentError(sut, error)
  }
}

// --- environments ---------------------------------------------------------

function environmentPayload(values: EnvironmentFormValues): Record<string, unknown> {
  return {
    name: values.name.trim(),
    base_url: values.baseUrl.trim(),
    is_default: values.isDefault,
    status: values.status,
  }
}

export function createEnvironment(
  sutId: string,
  values: EnvironmentFormValues,
): Promise<SutEnvironment> {
  return apiPost<SutEnvironment>(
    `/api/resources/suts/${encodeURIComponent(sutId)}/environments`,
    environmentPayload(values),
  )
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

export function createPage(environmentId: string, values: PageFormValues): Promise<SutPage> {
  return apiPost<SutPage>(
    `/api/resources/environments/${encodeURIComponent(environmentId)}/pages`,
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
  environmentId: string,
  items: PageFormValues[],
): Promise<ImportResult<SutPage>> {
  return apiPost<ImportResult<SutPage>>(
    `/api/resources/environments/${encodeURIComponent(environmentId)}/pages/import`,
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

export function createApi(environmentId: string, values: ApiFormValues): Promise<SutApi> {
  return apiPost<SutApi>(
    `/api/resources/environments/${encodeURIComponent(environmentId)}/apis`,
    apiPayload(values),
  )
}

export function updateApi(id: string, values: ApiFormValues): Promise<SutApi> {
  return apiPatch<SutApi>(`/api/resources/apis/${encodeURIComponent(id)}`, apiPayload(values))
}

export function deleteApi(id: string): Promise<void> {
  return apiDelete<void>(`/api/resources/apis/${encodeURIComponent(id)}`)
}

export function importApis(
  environmentId: string,
  items: ApiFormValues[],
): Promise<ImportResult<SutApi>> {
  return apiPost<ImportResult<SutApi>>(
    `/api/resources/environments/${encodeURIComponent(environmentId)}/apis/import`,
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
    environment_id: values.environmentId,
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

export function createAccount(sutId: string, values: AccountFormValues): Promise<TestAccount> {
  return apiPost<TestAccount>(`/api/resources/suts/${encodeURIComponent(sutId)}/accounts`, {
    environment_id: values.environmentId,
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

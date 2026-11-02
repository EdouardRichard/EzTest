import type {
  BindingDraft,
  BindingErrors,
  Catalog,
  CreateVersionRequest,
  EnvironmentResource,
  SutResource,
  VersionDraft,
  VersionDraftErrors,
} from "./types"

export const MAX_VERSION_BINDINGS = 20

let bindingSequence = 0

/** Creates one empty binding draft; callers may supply a stable key in tests. */
export function createBindingDraft(key?: string): BindingDraft {
  bindingSequence += 1
  return {
    key: key ?? `binding-${bindingSequence}`,
    sutId: "",
    environmentId: "",
    repositoryId: "",
    pageIds: [],
    apiIds: [],
    databaseIds: [],
    accountIds: [],
  }
}

/** A fresh create form: empty title and exactly one SUT binding slot. */
export function createVersionDraft(): VersionDraft {
  return { title: "", bindings: [createBindingDraft()] }
}

export function findSut(catalog: Catalog, sutId: string): SutResource | undefined {
  return catalog.suts.find((sut) => sut.id === sutId)
}

export function findEnvironment(
  catalog: Catalog,
  sutId: string,
  environmentId: string,
): EnvironmentResource | undefined {
  return findSut(catalog, sutId)?.environments.find(
    (environment) => environment.id === environmentId,
  )
}

/** Changing SUT invalidates the environment and every environment-scoped pick. */
export function updateBindingSut(binding: BindingDraft, sutId: string): BindingDraft {
  return {
    ...binding,
    sutId,
    environmentId: "",
    pageIds: [],
    apiIds: [],
    databaseIds: [],
    accountIds: [],
  }
}

/** Changing environment invalidates every environment-scoped pick. */
export function updateBindingEnvironment(
  binding: BindingDraft,
  environmentId: string,
): BindingDraft {
  return {
    ...binding,
    environmentId,
    pageIds: [],
    apiIds: [],
    databaseIds: [],
    accountIds: [],
  }
}

/** Adds/removes one id while keeping the user's selection order. */
export function toggleSelection(ids: string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((current) => current !== id) : [...ids, id]
}

function uniqueIds(ids: string[]): string[] {
  return Array.from(new Set(ids.filter((id) => id.length > 0)))
}

function missingFrom(selected: string[], allowed: Array<{ id: string }>): boolean {
  const allowedIds = new Set(allowed.map((item) => item.id))
  return selected.some((id) => !allowedIds.has(id))
}

function emptyErrors(): VersionDraftErrors {
  return { byBinding: {}, valid: false }
}

/**
 * Validates the create form against the catalog. At least one binding is
 * required, but pages/APIs/databases/accounts may stay empty (spec §7.3).
 */
export function validateVersionDraft(draft: VersionDraft, catalog: Catalog): VersionDraftErrors {
  const errors = emptyErrors()

  if (draft.title.trim().length === 0) errors.title = "请输入版本标题"
  if (draft.bindings.length === 0) {
    errors.bindings = "请至少添加一个 SUT 绑定"
  } else if (draft.bindings.length > MAX_VERSION_BINDINGS) {
    errors.bindings = `最多支持 ${MAX_VERSION_BINDINGS} 个 SUT 绑定`
  }

  const seenSuts = new Set<string>()
  for (const binding of draft.bindings) {
    const bindingErrors: BindingErrors = {}
    const sut = findSut(catalog, binding.sutId)

    if (binding.sutId.length === 0) {
      bindingErrors.sutId = "请选择 SUT"
    } else if (sut === undefined) {
      bindingErrors.sutId = "所选 SUT 不存在"
    } else if (seenSuts.has(binding.sutId)) {
      bindingErrors.sutId = "同一 SUT 只能绑定一次"
    } else {
      seenSuts.add(binding.sutId)

      if (binding.environmentId.length === 0) {
        bindingErrors.environmentId = "请选择环境"
      } else {
        const environment = findEnvironment(catalog, binding.sutId, binding.environmentId)
        if (environment === undefined) {
          bindingErrors.environmentId = "环境不属于所选 SUT"
        } else {
          if (missingFrom(binding.pageIds, environment.pages)) {
            bindingErrors.pages = "页面不属于所选环境"
          }
          if (missingFrom(binding.apiIds, environment.apis)) {
            bindingErrors.apis = "API 不属于所选环境"
          }
          if (missingFrom(binding.databaseIds, environment.databases)) {
            bindingErrors.databases = "目标库不属于所选环境"
          }
          if (missingFrom(binding.accountIds, environment.accounts)) {
            bindingErrors.accounts = "测试账号不属于所选环境"
          }
        }
      }
    }

    if (
      binding.repositoryId.length > 0 &&
      !catalog.repositories.some((repository) => repository.id === binding.repositoryId)
    ) {
      bindingErrors.repositoryId = "代码仓不存在"
    }

    if (Object.keys(bindingErrors).length > 0) errors.byBinding[binding.key] = bindingErrors
  }

  errors.valid =
    errors.title === undefined &&
    errors.bindings === undefined &&
    Object.keys(errors.byBinding).length === 0
  return errors
}

/**
 * Builds the exact `CreateVersionRequest` payload. Optional values are omitted
 * rather than sent as empty strings, and ids are deduplicated in selection order.
 */
export function buildCreateVersionPayload(draft: VersionDraft): CreateVersionRequest {
  return {
    title: draft.title.trim(),
    bindings: draft.bindings.map((binding) => {
      const accounts = uniqueIds(binding.accountIds).map((accountId) => ({
        account_id: accountId,
      }))
      return {
        sut_id: binding.sutId,
        environment_id: binding.environmentId,
        ...(binding.repositoryId.length > 0 ? { code_repository_id: binding.repositoryId } : {}),
        page_ids: uniqueIds(binding.pageIds),
        api_ids: uniqueIds(binding.apiIds),
        database_ids: uniqueIds(binding.databaseIds),
        accounts,
      }
    }),
  }
}

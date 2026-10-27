import { apiDelete, apiGet, apiPatch, apiPost } from "@/lib/api-client"

import type {
  AccessDTO,
  AccountDTO,
  AppModelNodeDTO,
  EnvironmentDTO,
  Page,
  SessionDTO,
  SnapshotDTO,
  SystemDTO,
} from "./types"

/**
 * API functions for the P2 public-resource context (spec §7). This module only
 * joins paths, types the calls and normalizes list pages; all transport,
 * auth and error-envelope handling stays in `@/lib/api-client`.
 */

const RESOURCES_ROOT = "/api/resources"

type QueryValue = string | number | undefined

function withQuery(path: string, query: Record<string, QueryValue>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined) continue
    search.set(key, String(value))
  }
  const suffix = search.toString()
  return suffix === "" ? path : `${path}?${suffix}`
}

type RawPage<T> = {
  items?: T[]
  data?: T[]
  nextCursor?: string | null
  next_cursor?: string | null
  hasMore?: boolean
  has_more?: boolean
}

/**
 * Normalizes a list response into `Page<T>`.
 *
 * The shared `apiGet` unwraps the `{"data": [...]}` envelope, so a list call
 * normally arrives here as a bare array and its cursor metadata is no longer
 * observable; list panels therefore request the backend's maximum page size
 * (100). A nested page payload is still mapped field by field when present so
 * pagination keeps working if the API ever returns one.
 */
function toPage<T>(payload: unknown): Page<T> {
  if (Array.isArray(payload)) {
    return { items: payload as T[], nextCursor: null, hasMore: false }
  }
  if (payload !== null && typeof payload === "object") {
    const page = payload as RawPage<T>
    const items = Array.isArray(page.items) ? page.items : Array.isArray(page.data) ? page.data : []
    return {
      items,
      nextCursor: page.nextCursor ?? page.next_cursor ?? null,
      hasMore: page.hasMore ?? page.has_more ?? false,
    }
  }
  return { items: [], nextCursor: null, hasMore: false }
}

function systemPath(id: number): string {
  return `${RESOURCES_ROOT}/systems/${id}`
}

function environmentPath(id: number): string {
  return `${RESOURCES_ROOT}/environments/${id}`
}

function appModelPath(id: number): string {
  return `${RESOURCES_ROOT}/app-models/${id}`
}

function accountPath(id: number): string {
  return `${RESOURCES_ROOT}/accounts/${id}`
}

function accessPath(id: number): string {
  return `${RESOURCES_ROOT}/accesses/${id}`
}

function sessionPath(id: number): string {
  return `${RESOURCES_ROOT}/sessions/${id}`
}

function takeoverPath(id: number): string {
  return `${RESOURCES_ROOT}/takeovers/${id}`
}

// --- Systems & environments (spec §7.2) -------------------------------------

export async function listSystems(
  params: { limit?: number; cursor?: string; status?: string } = {},
): Promise<Page<SystemDTO>> {
  const path = withQuery(`${RESOURCES_ROOT}/systems`, {
    limit: params.limit,
    cursor: params.cursor,
    status: params.status,
  })
  return toPage<SystemDTO>(await apiGet<unknown>(path))
}

export function createSystem(input: Partial<SystemDTO>): Promise<SystemDTO> {
  return apiPost<SystemDTO>(`${RESOURCES_ROOT}/systems`, input)
}

export function updateSystem(id: number, input: Partial<SystemDTO>): Promise<SystemDTO> {
  return apiPatch<SystemDTO>(systemPath(id), input)
}

export async function deleteSystem(id: number): Promise<void> {
  await apiDelete<void>(systemPath(id))
}

export function listEnvironments(systemId: number): Promise<EnvironmentDTO[]> {
  return apiGet<EnvironmentDTO[]>(`${systemPath(systemId)}/environments`)
}

export function createEnvironment(
  systemId: number,
  input: Partial<EnvironmentDTO>,
): Promise<EnvironmentDTO> {
  return apiPost<EnvironmentDTO>(`${systemPath(systemId)}/environments`, input)
}

export function updateEnvironment(
  id: number,
  input: Partial<EnvironmentDTO>,
): Promise<EnvironmentDTO> {
  return apiPatch<EnvironmentDTO>(environmentPath(id), input)
}

export async function deleteEnvironment(id: number): Promise<void> {
  await apiDelete<void>(environmentPath(id))
}

// --- App models (spec §7.3) -------------------------------------------------

export async function listAppModels(
  systemId: number,
  params: { kind?: string; limit?: number; cursor?: string } = {},
): Promise<Page<AppModelNodeDTO>> {
  const path = withQuery(`${systemPath(systemId)}/app-models`, {
    kind: params.kind,
    limit: params.limit,
    cursor: params.cursor,
  })
  return toPage<AppModelNodeDTO>(await apiGet<unknown>(path))
}

export function createAppModel(
  systemId: number,
  input: Partial<AppModelNodeDTO>,
): Promise<AppModelNodeDTO> {
  return apiPost<AppModelNodeDTO>(`${systemPath(systemId)}/app-models`, input)
}

export function importAppModels(
  systemId: number,
  nodes: Partial<AppModelNodeDTO>[],
): Promise<{ created: number; updated: number }> {
  return apiPost<{ created: number; updated: number }>(
    `${systemPath(systemId)}/app-models/import`,
    { nodes },
  )
}

export function updateAppModel(
  id: number,
  input: Partial<AppModelNodeDTO>,
): Promise<AppModelNodeDTO> {
  return apiPatch<AppModelNodeDTO>(appModelPath(id), input)
}

export async function deleteAppModel(id: number): Promise<void> {
  await apiDelete<void>(appModelPath(id))
}

// --- Accounts & accesses (spec §7.4) ----------------------------------------

export async function listAccounts(
  params: { limit?: number; cursor?: string; status?: string } = {},
): Promise<Page<AccountDTO>> {
  const path = withQuery(`${RESOURCES_ROOT}/accounts`, {
    limit: params.limit,
    cursor: params.cursor,
    status: params.status,
  })
  return toPage<AccountDTO>(await apiGet<unknown>(path))
}

export function createAccount(input: Partial<AccountDTO>): Promise<AccountDTO> {
  return apiPost<AccountDTO>(`${RESOURCES_ROOT}/accounts`, input)
}

export function updateAccount(id: number, input: Partial<AccountDTO>): Promise<AccountDTO> {
  return apiPatch<AccountDTO>(accountPath(id), input)
}

export async function deleteAccount(id: number): Promise<void> {
  await apiDelete<void>(accountPath(id))
}

export async function listAccesses(
  accountId: number,
  params: { limit?: number; cursor?: string } = {},
): Promise<AccessDTO[]> {
  const path = withQuery(`${accountPath(accountId)}/accesses`, {
    limit: params.limit,
    cursor: params.cursor,
  })
  return apiGet<AccessDTO[]>(path)
}

export function createAccess(
  accountId: number,
  input: Partial<AccessDTO> & { credential?: string },
): Promise<AccessDTO> {
  return apiPost<AccessDTO>(`${accountPath(accountId)}/accesses`, input)
}

export function updateAccess(
  id: number,
  input: Partial<AccessDTO> & { credential?: string },
): Promise<AccessDTO> {
  return apiPatch<AccessDTO>(accessPath(id), input)
}

export async function deleteAccess(id: number): Promise<void> {
  await apiDelete<void>(accessPath(id))
}

// --- Sessions (spec §7.5) ---------------------------------------------------

export async function listSessions(
  params: {
    accountId?: number
    systemId?: number
    environmentId?: number
    state?: string
    limit?: number
    cursor?: string
  } = {},
): Promise<Page<SessionDTO>> {
  const path = withQuery(`${RESOURCES_ROOT}/sessions`, {
    accountId: params.accountId,
    systemId: params.systemId,
    environmentId: params.environmentId,
    state: params.state,
    limit: params.limit,
    cursor: params.cursor,
  })
  return toPage<SessionDTO>(await apiGet<unknown>(path))
}

export function getSession(id: number): Promise<SessionDTO> {
  return apiGet<SessionDTO>(sessionPath(id))
}

export function importSession(
  accessId: number,
  input: {
    environmentId: number
    kind: "storage_state" | "token"
    storageState?: unknown
    headers?: Record<string, string>
    expiresAt?: string
    note?: string
  },
): Promise<SessionDTO> {
  return apiPost<SessionDTO>(`${accessPath(accessId)}/sessions`, input)
}

export function validateSession(id: number): Promise<SessionDTO> {
  return apiPost<SessionDTO>(`${sessionPath(id)}/validate`)
}

export function confirmSession(id: number, note?: string): Promise<SessionDTO> {
  return apiPost<SessionDTO>(`${sessionPath(id)}/confirm`, note === undefined ? {} : { note })
}

export function invalidateSession(id: number, reason: string): Promise<SessionDTO> {
  return apiPost<SessionDTO>(`${sessionPath(id)}/invalidate`, { reason })
}

export function revokeSession(id: number, reason: string): Promise<SessionDTO> {
  return apiPost<SessionDTO>(`${sessionPath(id)}/revoke`, { reason })
}

export async function listSnapshots(sessionId: number): Promise<Page<SnapshotDTO>> {
  return toPage<SnapshotDTO>(await apiGet<unknown>(`${sessionPath(sessionId)}/snapshots`))
}

// --- Takeovers (spec §7.6) --------------------------------------------------

type TakeoverResponse = {
  takeover: { id: number; status: string }
  session: SessionDTO
}

export function requestTakeover(
  sessionId: number,
  input: { reason: string; instructions?: unknown; expiresInSeconds?: number },
): Promise<TakeoverResponse> {
  return apiPost<TakeoverResponse>(`${sessionPath(sessionId)}/takeovers`, input)
}

export function completeTakeover(
  id: number,
  input: {
    kind: "storage_state" | "token"
    storageState?: unknown
    headers?: Record<string, string>
  },
): Promise<TakeoverResponse> {
  return apiPost<TakeoverResponse>(`${takeoverPath(id)}/complete`, input)
}

export function cancelTakeover(id: number): Promise<{ id: number; status: string }> {
  return apiPost<{ id: number; status: string }>(`${takeoverPath(id)}/cancel`)
}

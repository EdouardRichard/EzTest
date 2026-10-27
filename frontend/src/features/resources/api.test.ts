import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  cancelTakeover,
  completeTakeover,
  confirmSession,
  createAccess,
  createAccount,
  createAppModel,
  createEnvironment,
  createSystem,
  deleteAccess,
  deleteAccount,
  deleteAppModel,
  deleteEnvironment,
  deleteSystem,
  getSession,
  importAppModels,
  importSession,
  invalidateSession,
  listAccesses,
  listAccounts,
  listAppModels,
  listEnvironments,
  listSessions,
  listSnapshots,
  listSystems,
  requestTakeover,
  revokeSession,
  updateAccess,
  updateAccount,
  updateAppModel,
  updateEnvironment,
  updateSystem,
  validateSession,
} from "./api"
import type {
  AccessDTO,
  AccountDTO,
  AppModelNodeDTO,
  EnvironmentDTO,
  SessionDTO,
  SnapshotDTO,
  SystemDTO,
} from "./types"

const fetchMock = vi.fn()

const SYSTEM: SystemDTO = {
  id: 1,
  key: "shop",
  name: "Shop",
  kind: "web",
  baseUrl: "http://localhost:13335",
  defaultSessionTtlSeconds: 3600,
  notes: "",
  status: "active",
  createdAt: "2026-10-27T00:00:00Z",
  updatedAt: "2026-10-27T00:00:00Z",
}

const ENVIRONMENT: EnvironmentDTO = {
  id: 2,
  systemId: 1,
  key: "staging",
  name: "Staging",
  baseUrl: "http://localhost:13335",
  kind: "staging",
  isProd: false,
  allowedHosts: [],
  loginCheck: null,
  allowAutoLogin: false,
  requireApproval: false,
}

const NODE: AppModelNodeDTO = {
  id: 5,
  systemId: 1,
  parentId: null,
  kind: "route",
  key: "route:/orders",
  name: "Orders",
  path: "/orders",
  method: "",
  source: "manual",
  meta: {},
}

const ACCOUNT: AccountDTO = {
  id: 3,
  key: "qa01",
  label: "QA 01",
  username: "qa01",
  kind: "human",
  status: "active",
  notes: "",
}

const ACCESS: AccessDTO = {
  id: 4,
  accountId: 3,
  systemId: 1,
  authType: "manual",
  credentialSet: false,
  usernameOverride: "",
  loginUrl: "",
  enabled: true,
  concurrencyLimit: 1,
  sessionTtlSeconds: null,
  permissionNote: "",
}

const SESSION: SessionDTO = {
  id: 7,
  accountId: 3,
  systemId: 1,
  environmentId: 2,
  accessId: 4,
  state: "valid",
  acquisition: "import",
  storageStateKind: "storage_state",
  cookieCount: 2,
  originCount: 1,
  storageStateHash: "hash",
  expiresAt: null,
  lastValidatedAt: null,
  lastUsedAt: null,
  failureReason: "",
  invalidatedReason: "",
  expiring: false,
}

const SNAPSHOT: SnapshotDTO = {
  id: 8,
  sessionId: 7,
  kind: "import",
  items: [],
  summary: {},
  permissionHash: "",
  degraded: false,
  capturedAt: "2026-10-27T00:00:00Z",
}

/** Builds a JSON Response the way the real API envelope is shaped. */
function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" },
  })
}

/** Returns the URL and init of the most recent fetch call. */
function lastRequest(): { url: string; init: RequestInit } {
  const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit]
  return { url: String(url), init }
}

describe("resources api", () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("calls the system and environment endpoints from spec §7.2", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [SYSTEM], next_cursor: "next-page", has_more: true }),
    )
    const page = await listSystems({ limit: 20, cursor: "abc", status: "active" })
    expect(page.items).toEqual([SYSTEM])
    expect(
      lastRequest().url.endsWith("/api/resources/systems?limit=20&cursor=abc&status=active"),
    ).toBe(true)
    expect(lastRequest().init.method).toBe("GET")

    fetchMock.mockResolvedValue(jsonResponse({ data: SYSTEM }, 201))
    await expect(createSystem({ key: "shop", name: "Shop" })).resolves.toEqual(SYSTEM)
    expect(lastRequest().url.endsWith("/api/resources/systems")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({ key: "shop", name: "Shop" })

    fetchMock.mockResolvedValue(jsonResponse({ data: { ...SYSTEM, name: "Shop 2" } }))
    await expect(updateSystem(1, { name: "Shop 2" })).resolves.toMatchObject({ name: "Shop 2" })
    expect(lastRequest().url.endsWith("/api/resources/systems/1")).toBe(true)
    expect(lastRequest().init.method).toBe("PATCH")

    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(deleteSystem(1)).resolves.toBeUndefined()
    expect(lastRequest().url.endsWith("/api/resources/systems/1")).toBe(true)
    expect(lastRequest().init.method).toBe("DELETE")

    fetchMock.mockResolvedValue(jsonResponse({ data: [ENVIRONMENT] }))
    await expect(listEnvironments(1)).resolves.toEqual([ENVIRONMENT])
    expect(lastRequest().url.endsWith("/api/resources/systems/1/environments")).toBe(true)

    fetchMock.mockResolvedValue(jsonResponse({ data: ENVIRONMENT }, 201))
    await expect(createEnvironment(1, { key: "staging" })).resolves.toEqual(ENVIRONMENT)
    expect(lastRequest().url.endsWith("/api/resources/systems/1/environments")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")

    fetchMock.mockResolvedValue(jsonResponse({ data: ENVIRONMENT }))
    await expect(updateEnvironment(2, { name: "Staging" })).resolves.toEqual(ENVIRONMENT)
    expect(lastRequest().url.endsWith("/api/resources/environments/2")).toBe(true)
    expect(lastRequest().init.method).toBe("PATCH")

    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(deleteEnvironment(2)).resolves.toBeUndefined()
    expect(lastRequest().url.endsWith("/api/resources/environments/2")).toBe(true)
    expect(lastRequest().init.method).toBe("DELETE")
  })

  it("maps cursor page metadata when the API payload nests its items", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { items: [SYSTEM], next_cursor: "c1", has_more: true } }),
    )

    await expect(listSystems()).resolves.toEqual({
      items: [SYSTEM],
      nextCursor: "c1",
      hasMore: true,
    })
    expect(lastRequest().url.endsWith("/api/resources/systems")).toBe(true)
  })

  it("calls the app model endpoints from spec §7.3", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [NODE] }))
    await expect(listAppModels(1, { kind: "route" })).resolves.toEqual({
      items: [NODE],
      nextCursor: null,
      hasMore: false,
    })
    expect(lastRequest().url.endsWith("/api/resources/systems/1/app-models?kind=route")).toBe(true)

    fetchMock.mockResolvedValue(jsonResponse({ data: NODE }, 201))
    await expect(createAppModel(1, { key: "route:/orders", kind: "route" })).resolves.toEqual(NODE)
    expect(lastRequest().url.endsWith("/api/resources/systems/1/app-models")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")

    fetchMock.mockResolvedValue(jsonResponse({ data: { created: 2, updated: 1 } }))
    await expect(importAppModels(1, [NODE])).resolves.toEqual({ created: 2, updated: 1 })
    expect(lastRequest().url.endsWith("/api/resources/systems/1/app-models/import")).toBe(true)
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({ nodes: [NODE] })

    fetchMock.mockResolvedValue(jsonResponse({ data: NODE }))
    await expect(updateAppModel(5, { name: "Orders" })).resolves.toEqual(NODE)
    expect(lastRequest().url.endsWith("/api/resources/app-models/5")).toBe(true)
    expect(lastRequest().init.method).toBe("PATCH")

    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(deleteAppModel(5)).resolves.toBeUndefined()
    expect(lastRequest().url.endsWith("/api/resources/app-models/5")).toBe(true)
    expect(lastRequest().init.method).toBe("DELETE")
  })

  it("calls the account and access endpoints from spec §7.4", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [ACCOUNT], next_cursor: null, has_more: false }),
    )
    await expect(listAccounts({ limit: 20, status: "active" })).resolves.toMatchObject({
      items: [ACCOUNT],
    })
    expect(lastRequest().url.endsWith("/api/resources/accounts?limit=20&status=active")).toBe(true)

    fetchMock.mockResolvedValue(jsonResponse({ data: ACCOUNT }, 201))
    await expect(createAccount({ key: "qa01" })).resolves.toEqual(ACCOUNT)
    expect(lastRequest().url.endsWith("/api/resources/accounts")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")

    fetchMock.mockResolvedValue(jsonResponse({ data: ACCOUNT }))
    await expect(updateAccount(3, { label: "QA 01" })).resolves.toEqual(ACCOUNT)
    expect(lastRequest().url.endsWith("/api/resources/accounts/3")).toBe(true)
    expect(lastRequest().init.method).toBe("PATCH")

    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(deleteAccount(3)).resolves.toBeUndefined()
    expect(lastRequest().url.endsWith("/api/resources/accounts/3")).toBe(true)

    fetchMock.mockResolvedValue(jsonResponse({ data: [ACCESS] }))
    await expect(listAccesses(3)).resolves.toEqual([ACCESS])
    expect(lastRequest().url.endsWith("/api/resources/accounts/3/accesses")).toBe(true)

    fetchMock.mockResolvedValue(jsonResponse({ data: ACCESS }, 201))
    await expect(createAccess(3, { systemId: 1, authType: "manual" })).resolves.toEqual(ACCESS)
    expect(lastRequest().url.endsWith("/api/resources/accounts/3/accesses")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")

    fetchMock.mockResolvedValue(jsonResponse({ data: ACCESS }))
    await expect(updateAccess(4, { credential: "secret" })).resolves.toEqual(ACCESS)
    expect(lastRequest().url.endsWith("/api/resources/accesses/4")).toBe(true)
    expect(lastRequest().init.method).toBe("PATCH")

    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(deleteAccess(4)).resolves.toBeUndefined()
    expect(lastRequest().url.endsWith("/api/resources/accesses/4")).toBe(true)
  })

  it("calls the session endpoints and actions from spec §7.5", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [SESSION] }))
    await expect(
      listSessions({
        accountId: 3,
        systemId: 1,
        environmentId: 2,
        state: "valid",
        limit: 20,
        cursor: "next",
      }),
    ).resolves.toMatchObject({ items: [SESSION] })
    expect(
      lastRequest().url.endsWith(
        "/api/resources/sessions?accountId=3&systemId=1&environmentId=2&state=valid&limit=20&cursor=next",
      ),
    ).toBe(true)

    fetchMock.mockResolvedValue(jsonResponse({ data: SESSION }))
    await expect(getSession(7)).resolves.toEqual(SESSION)
    expect(lastRequest().url.endsWith("/api/resources/sessions/7")).toBe(true)

    fetchMock.mockResolvedValue(jsonResponse({ data: SESSION }, 201))
    await expect(
      importSession(4, { environmentId: 2, kind: "storage_state", storageState: { cookies: [] } }),
    ).resolves.toEqual(SESSION)
    expect(lastRequest().url.endsWith("/api/resources/accesses/4/sessions")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({
      environmentId: 2,
      kind: "storage_state",
      storageState: { cookies: [] },
    })

    fetchMock.mockResolvedValue(jsonResponse({ data: SESSION }))
    await expect(validateSession(7)).resolves.toEqual(SESSION)
    expect(lastRequest().url.endsWith("/api/resources/sessions/7/validate")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")

    fetchMock.mockResolvedValue(jsonResponse({ data: SESSION }))
    await expect(confirmSession(7, "人工确认")).resolves.toEqual(SESSION)
    expect(lastRequest().url.endsWith("/api/resources/sessions/7/confirm")).toBe(true)
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({ note: "人工确认" })

    fetchMock.mockResolvedValue(jsonResponse({ data: SESSION }))
    await expect(invalidateSession(7, "mfa")).resolves.toEqual(SESSION)
    expect(lastRequest().url.endsWith("/api/resources/sessions/7/invalidate")).toBe(true)
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({ reason: "mfa" })

    fetchMock.mockResolvedValue(jsonResponse({ data: SESSION }))
    await expect(revokeSession(7, "compromised")).resolves.toEqual(SESSION)
    expect(lastRequest().url.endsWith("/api/resources/sessions/7/revoke")).toBe(true)

    fetchMock.mockResolvedValue(
      jsonResponse({ data: [SNAPSHOT], next_cursor: null, has_more: false }),
    )
    await expect(listSnapshots(7)).resolves.toMatchObject({ items: [SNAPSHOT] })
    expect(lastRequest().url.endsWith("/api/resources/sessions/7/snapshots")).toBe(true)
  })

  it("calls the takeover endpoints from spec §7.6", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { takeover: { id: 11, status: "waiting" }, session: SESSION } }),
    )
    await expect(requestTakeover(7, { reason: "mfa" })).resolves.toEqual({
      takeover: { id: 11, status: "waiting" },
      session: SESSION,
    })
    expect(lastRequest().url.endsWith("/api/resources/sessions/7/takeovers")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({ reason: "mfa" })

    fetchMock.mockResolvedValue(
      jsonResponse({ data: { takeover: { id: 11, status: "completed" }, session: SESSION } }),
    )
    await expect(
      completeTakeover(11, { kind: "token", headers: { Authorization: "Bearer fixture" } }),
    ).resolves.toMatchObject({ takeover: { id: 11, status: "completed" } })
    expect(lastRequest().url.endsWith("/api/resources/takeovers/11/complete")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")

    fetchMock.mockResolvedValue(jsonResponse({ data: { id: 11, status: "cancelled" } }))
    await expect(cancelTakeover(11)).resolves.toEqual({ id: 11, status: "cancelled" })
    expect(lastRequest().url.endsWith("/api/resources/takeovers/11/cancel")).toBe(true)
    expect(lastRequest().init.method).toBe("POST")
  })
})

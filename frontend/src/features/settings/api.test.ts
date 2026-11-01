import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type AuditLogEntry,
  activateInstance,
  addLLMModel,
  createGCRun,
  createLLMConfig,
  createUser,
  deleteLLMConfig,
  deleteLLMModel,
  drainInstance,
  fetchLLMConfigs,
  fetchLLMModels,
  fetchSettings,
  fetchUsers,
  type GCRun,
  type GCRunDetail,
  getGCRun,
  groupSettings,
  type Instance,
  type LLMConfig,
  type LLMModel,
  listAuditLogs,
  listGCRuns,
  listInstances,
  refreshLLMModels,
  resetUserPassword,
  type Setting,
  testLLMConfig,
  type User,
  updateLLMConfig,
  updateSetting,
  updateUser,
} from "./api"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function readBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string") return undefined
  return JSON.parse(init.body) as unknown
}

const fetchMock = vi.fn<typeof fetch>()

function setting(overrides: Partial<Setting> = {}): Setting {
  return {
    key: "execution.global_concurrency",
    category: "execution",
    type: "number",
    value: 3,
    is_secret: false,
    restart_required: false,
    description: "Maximum concurrent case runs.",
    version: 1,
    updated_at: "2026-10-31T08:00:00Z",
    ...overrides,
  }
}

const user: User = {
  id: "9001",
  username: "qa01",
  display_name: "QA 01",
  role: "user",
  status: "active",
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
}

const config: LLMConfig = {
  id: "7001",
  name: "local-fake",
  provider: "openai",
  protocol: "openai_compatible",
  base_url: "http://127.0.0.1:19000/v1",
  api_key_set: true,
  headers: { "X-Tenant": "qa" },
  timeout_seconds: 60,
  max_retries: 2,
  is_default: true,
  status: "active",
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
}

const model: LLMModel = {
  id: "8001",
  config_id: "7001",
  model: "fake-model",
  display_name: "fake-model",
  context_window: 128000,
  max_output: 4096,
  supports_tools: true,
  supports_vision: false,
  source: "manual",
  status: "active",
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
}

const auditEntry: AuditLogEntry = {
  id: "9100",
  actor_id: "9001",
  action: "auth.login",
  target_kind: "session",
  target_id: "9001",
  request_id: "req_audit000001",
  ip: "127.0.0.1",
  user_agent: "vitest",
  metadata: { username: "admin" },
  created_at: "2026-10-31T08:00:00Z",
}

describe("settings API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("loads every setting from the admin endpoint", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [setting()] }))

    await expect(fetchSettings()).resolves.toEqual([setting()])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/admin/settings")
    expect(init?.method).toBe("GET")
  })

  it("patches a single setting value and returns the updated row", async () => {
    const updated = setting({ value: 5, version: 2 })
    fetchMock.mockResolvedValue(jsonResponse({ data: updated }))

    await expect(updateSetting("execution.global_concurrency", 5)).resolves.toEqual(updated)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/admin/settings/execution.global_concurrency")
    expect(init?.method).toBe("PATCH")
    expect(readBody(init)).toEqual({ value: 5 })
  })

  it("groups settings by category while preserving API order", () => {
    const groups = groupSettings([
      setting({ key: "execution.global_concurrency", category: "execution" }),
      setting({ key: "runtime.snowflake_node_id", category: "runtime" }),
      setting({ key: "execution.case_timeout_seconds", category: "execution" }),
      setting({ key: "custom.flag", category: "custom" }),
    ])

    expect(groups.map((group) => group.category)).toEqual(["execution", "runtime", "custom"])
    expect(groups[0]).toMatchObject({ label: "执行" })
    expect(groups[0].settings.map((item) => item.key)).toEqual([
      "execution.global_concurrency",
      "execution.case_timeout_seconds",
    ])
    expect(groups[2]).toMatchObject({ category: "custom", label: "custom" })
    expect(groupSettings([])).toEqual([])
  })
})

describe("LLM config API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("lists configs and models", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: [config] }))
      .mockResolvedValueOnce(jsonResponse({ data: [model] }))

    await expect(fetchLLMConfigs()).resolves.toEqual([config])
    await expect(fetchLLMModels()).resolves.toEqual([model])

    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      "http://localhost:13333/api/admin/llm/configs",
      "http://localhost:13333/api/admin/llm/models",
    ])
  })

  it("creates a config with the mapped request body", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: config }))

    await createLLMConfig({
      name: "local-fake",
      base_url: "http://127.0.0.1:19000/v1",
      protocol: "openai_compatible",
      api_key: "sk-test",
      headers: { "X-Tenant": "qa" },
      timeout_seconds: 60,
      max_retries: 2,
      is_default: true,
      status: "active",
    })

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/admin/llm/configs")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({
      name: "local-fake",
      base_url: "http://127.0.0.1:19000/v1",
      protocol: "openai_compatible",
      api_key: "sk-test",
      headers: { "X-Tenant": "qa" },
      timeout_seconds: 60,
      max_retries: 2,
      is_default: true,
      status: "active",
    })
  })

  it("updates and deletes one config", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: { ...config, name: "renamed" } }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await updateLLMConfig("7001", { name: "renamed", base_url: config.base_url })
    await deleteLLMConfig("7001")

    const [patchUrl, patchInit] = fetchMock.mock.calls[0]
    expect(patchUrl).toBe("http://localhost:13333/api/admin/llm/configs/7001")
    expect(patchInit?.method).toBe("PATCH")
    expect(readBody(patchInit)).toEqual({ name: "renamed", base_url: config.base_url })

    const [deleteUrl, deleteInit] = fetchMock.mock.calls[1]
    expect(deleteUrl).toBe("http://localhost:13333/api/admin/llm/configs/7001")
    expect(deleteInit?.method).toBe("DELETE")
  })

  it("tests connectivity and refreshes the model catalog", async () => {
    const testResult = {
      ok: true,
      latency_ms: 120,
      models: [{ id: "fake-model", context_window: 128000, max_output: 4096 }],
    }
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: testResult }))
      .mockResolvedValueOnce(jsonResponse({ data: [model] }))

    await expect(testLLMConfig("7001")).resolves.toEqual(testResult)
    await expect(refreshLLMModels("7001")).resolves.toEqual([model])

    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/admin/llm/configs/7001/test",
    )
    expect(fetchMock.mock.calls[0][1]?.method).toBe("POST")
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/admin/llm/configs/7001/refresh-models",
    )
    expect(fetchMock.mock.calls[1][1]?.method).toBe("POST")
  })

  it("adds and deletes a manual model", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: model }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await addLLMModel("7001", {
      model: "fake-model",
      context_window: 128000,
      max_output: 4096,
      supports_tools: true,
      supports_vision: false,
    })
    await deleteLLMModel("7001", "8001")

    const [addUrl, addInit] = fetchMock.mock.calls[0]
    expect(addUrl).toBe("http://localhost:13333/api/admin/llm/configs/7001/models")
    expect(addInit?.method).toBe("POST")
    expect(readBody(addInit)).toEqual({
      model: "fake-model",
      context_window: 128000,
      max_output: 4096,
      supports_tools: true,
      supports_vision: false,
    })

    const [deleteUrl, deleteInit] = fetchMock.mock.calls[1]
    expect(deleteUrl).toBe("http://localhost:13333/api/admin/llm/configs/7001/models/8001")
    expect(deleteInit?.method).toBe("DELETE")
  })
})

describe("users API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("loads one cursor page of users", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [user], next_cursor: "9000", has_more: true }))

    await expect(fetchUsers({ cursor: "9100", limit: 20 })).resolves.toEqual({
      items: [user],
      nextCursor: "9000",
      hasMore: true,
    })

    const url = new URL(String(fetchMock.mock.calls[0][0]))
    expect(url.pathname).toBe("/api/admin/users")
    expect(url.searchParams.get("cursor")).toBe("9100")
    expect(url.searchParams.get("limit")).toBe("20")
  })

  it("creates a user", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: user }, 201))

    await createUser({ username: "qa01", display_name: "QA 01", password: "qa-password" })

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/admin/users")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({
      username: "qa01",
      display_name: "QA 01",
      password: "qa-password",
    })
  })

  it("updates a user status", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: { ...user, status: "disabled" } }))

    await updateUser("9001", { status: "disabled" })

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/admin/users/9001")
    expect(init?.method).toBe("PATCH")
    expect(readBody(init)).toEqual({ status: "disabled" })
  })

  it("resets a password with the backend's new_password field", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))

    await resetUserPassword("9001", "reset-password")

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/admin/users/9001/reset-password")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({ new_password: "reset-password" })
  })
})

describe("audit API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("passes action, actor and cursor filters and maps the page envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [auditEntry], next_cursor: "9099", has_more: true }),
    )

    await expect(
      listAuditLogs({ action: "auth.login", actor: "9001", cursor: "9100", limit: 20 }),
    ).resolves.toEqual({ items: [auditEntry], nextCursor: "9099", hasMore: true })

    const url = new URL(String(fetchMock.mock.calls[0][0]))
    expect(url.pathname).toBe("/api/admin/audit-logs")
    expect(url.searchParams.get("action")).toBe("auth.login")
    expect(url.searchParams.get("actor")).toBe("9001")
    expect(url.searchParams.get("cursor")).toBe("9100")
    expect(url.searchParams.get("limit")).toBe("20")
  })

  it("omits blank filters", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [auditEntry], next_cursor: "", has_more: false }),
    )

    await listAuditLogs({ action: "  ", actor: "" })

    const url = new URL(String(fetchMock.mock.calls[0][0]))
    expect(url.searchParams.has("action")).toBe(false)
    expect(url.searchParams.has("actor")).toBe(false)
    expect(url.searchParams.has("cursor")).toBe(false)
  })
})

/* ---------------------------- admin GC / instances ---------------------------- */

const gcRun: GCRun = {
  id: "6001",
  mode: "dry_run",
  status: "completed",
  actor_id: "9001",
  actor_kind: "user",
  started_at: "2026-11-01T06:00:00Z",
  finished_at: "2026-11-01T06:00:01Z",
  scanned_objects: 12,
  scanned_refs: 30,
  deleted_objects: 0,
  deleted_refs: 0,
  deleted_bytes: 0,
  report: {
    mode: "dry_run",
    dry_run: true,
    status: "completed",
    candidates: { unreferenced: 2, temp: 1, superseded_export: 1, orphan_file: 0 },
    planned: { objects: 2, refs: 1, files: 2, bytes: 2048 },
    deleted: { objects: 0, refs: 0, files: 0, bytes: 0 },
  },
  error: null,
}

const gcDetail: GCRunDetail = {
  ...gcRun,
  tombstones: [
    {
      id: "6101",
      run_id: "6001",
      object_sha: "a".repeat(64),
      size: 1024,
      mime: "application/octet-stream",
      relative_path: "objects/aa/aa.pdf",
      reason: "unreferenced",
      deleted_at: "2026-11-01T06:00:01Z",
    },
  ],
}

const instance: Instance = {
  id: "5001",
  instance_key: "host-5001",
  hostname: "test-host",
  pid: 4242,
  api_port: 13333,
  node_id: 1,
  status: "active",
  version: "0.2.0-p7",
  last_error: null,
  started_at: "2026-11-01T05:00:00Z",
  heartbeat_at: "2026-11-01T06:00:00Z",
  draining_at: null,
  stopped_at: null,
  created_at: "2026-11-01T05:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
}

describe("admin GC and instance API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("lists GC runs with the default limit", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [gcRun], next_cursor: "", has_more: false }))

    const page = await listGCRuns()

    expect(page.items).toEqual([gcRun])
    expect(fetchMock.mock.calls[0][0]).toBe("http://localhost:13333/api/admin/gc/runs?limit=20")
    expect(fetchMock.mock.calls[0][1]?.method).toBe("GET")
  })

  it("starts a dry run without a confirmation flag", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: gcRun }))

    await expect(createGCRun("dry_run")).resolves.toEqual(gcRun)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/admin/gc/runs")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({ mode: "dry_run" })
  })

  it("sends confirm=true for an apply run", async () => {
    const applied: GCRun = { ...gcRun, mode: "apply", report: { ...gcRun.report, dry_run: false } }
    fetchMock.mockResolvedValue(jsonResponse({ data: applied }))

    await expect(createGCRun("apply", true)).resolves.toEqual(applied)

    expect(readBody(fetchMock.mock.calls[0][1])).toEqual({ mode: "apply", confirm: true })
  })

  it("reads one run detail with tombstones", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: gcDetail }))

    await expect(getGCRun("6001")).resolves.toEqual(gcDetail)

    expect(fetchMock.mock.calls[0][0]).toBe("http://localhost:13333/api/admin/gc/runs/6001")
  })

  it("lists instances and transitions drain/activate", async () => {
    const draining: Instance = {
      ...instance,
      status: "draining",
      draining_at: "2026-11-01T06:10:00Z",
    }
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: [instance], next_cursor: "", has_more: false }))
      .mockResolvedValueOnce(jsonResponse({ data: draining }))
      .mockResolvedValueOnce(jsonResponse({ data: instance }))

    const page = await listInstances()
    expect(page.items).toEqual([instance])
    await expect(drainInstance("5001")).resolves.toEqual(draining)
    await expect(activateInstance("5001")).resolves.toEqual(instance)

    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      "http://localhost:13333/api/admin/instances",
      "http://localhost:13333/api/admin/instances/5001/drain",
      "http://localhost:13333/api/admin/instances/5001/activate",
    ])
    expect(fetchMock.mock.calls[1][1]?.method).toBe("POST")
    expect(fetchMock.mock.calls[2][1]?.method).toBe("POST")
  })
})

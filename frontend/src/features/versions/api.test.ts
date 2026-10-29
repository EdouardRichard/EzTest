import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { setToken } from "@/lib/auth-storage"

import {
  analyzeClarification,
  completeClarificationSession,
  confirmRequirement,
  createClarificationQuestion,
  createDecision,
  createIncrementalBatch,
  createRelation,
  createRequirement,
  createVersion,
  deleteAttachment,
  deleteRelation,
  deleteRequirement,
  downloadAttachment,
  enterDesign,
  fetchVersionEvents,
  getClarification,
  getIncrementalPlan,
  getVersion,
  listAttachments,
  listClarificationMessages,
  listClarificationQuestions,
  listDecisions,
  listRelations,
  listRequirements,
  listVersions,
  putBindings,
  reconClarification,
  reopenRequirement,
  sendClarificationMessage,
  startClarification,
  startClarificationSession,
  updateClarificationQuestion,
  updateDecision,
  updateRelation,
  updateRequirement,
  updateVersion,
  uploadAttachment,
} from "./api"
import type {
  ClarificationDecision,
  ClarificationMessage,
  ClarificationQuestion,
  ClarificationSession,
  IncrementalPlan,
  Requirement,
  RequirementAttachment,
  RequirementRelation,
  Version,
  VersionEvent,
} from "./types"

/** A snowflake ID that a JS number cannot hold; it must stay a string verbatim. */
const VERSION_ID = "1844674407370955161"
const OTHER_VERSION_ID = "1844674407370955162"

const VERSION: Version = {
  id: VERSION_ID,
  name: "订单系统 2.0",
  label: "v2.0",
  description: "",
  status: "clarifying",
  systemId: 1,
  environmentId: 2,
  codeRepositoryId: 3,
  codeBranch: "main",
  codeBaselineCommit: "0123456789abcdef0123456789abcdef01234567",
  bindings: {
    system: { id: 1, name: "订单系统", available: true },
    environment: { id: 2, name: "staging", available: true },
    codeRepository: { id: 3, name: "order-web", available: true },
  },
  requirementCount: 3,
  clarifiedCount: 1,
  openQuestionCount: 2,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const EVENT: VersionEvent = {
  seq: 7,
  channel: `version:${VERSION_ID}`,
  type: "requirement.added",
  version_id: VERSION_ID,
  batch_id: null,
  case_id: null,
  step_id: null,
  agent_role: null,
  session_id: null,
  ts: "2026-10-27T10:00:00Z",
  summary: "新增需求",
  detail: { requirementId: OTHER_VERSION_ID },
  refs: { evidence: [], case: [], requirement: [OTHER_VERSION_ID], code: [] },
}

const fetchMock = vi.fn()

const REQUIREMENT_ID = "1844674407370956001"
const OTHER_REQUIREMENT_ID = "1844674407370956002"
const RELATION_ID = "1844674407370956101"
const ATTACHMENT_ID = "1844674407370956201"

const REQUIREMENT: Requirement = {
  id: REQUIREMENT_ID,
  versionId: VERSION_ID,
  title: "订单创建",
  description: "用户提交订单。",
  acceptanceCriteria: "订单出现在订单列表。",
  priority: "should",
  status: "draft",
  source: "user",
  lastUpdateSource: "user",
  revision: 3,
  confirmedAt: null,
  confirmedBy: null,
  openQuestionCount: 1,
  relationCount: 1,
  attachmentCount: 1,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const RELATION: RequirementRelation = {
  id: RELATION_ID,
  versionId: VERSION_ID,
  fromRequirementId: REQUIREMENT_ID,
  toRequirementId: OTHER_REQUIREMENT_ID,
  relationType: "depends_on",
  description: "创建订单后扣减库存",
  source: "user",
  createdBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const ATTACHMENT: RequirementAttachment = {
  id: ATTACHMENT_ID,
  kind: "text",
  filename: "notes.md",
  mimeType: "text/markdown",
  sizeBytes: 8,
  sha256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
  createdAt: "2026-10-27T10:00:00Z",
}

const UPLOAD_FILE = new File(["# notes\n"], "notes.md", { type: "text/markdown" })

const CLARIFICATION_SESSION_ID = "1844674407370957001"
const QUESTION_ID = "1844674407370957101"
const DECISION_ID = "1844674407370957201"

const CLARIFICATION_SESSION: ClarificationSession = {
  id: CLARIFICATION_SESSION_ID,
  status: "active",
  agentSessionId: 77,
  startedAt: "2026-10-27T10:00:00Z",
  endedAt: null,
  contextSummary: "",
  summaryUpdatedAt: null,
}

const CLARIFICATION_MESSAGE: ClarificationMessage = {
  id: 11,
  role: "assistant",
  parts: [{ type: "text", text: "请确认是否必填。" }],
  reasoning: "先核对需求上下文",
  toolCalls: [{ id: "call_1", name: "version.context_get", arguments: "{}" }],
  toolName: null,
  runId: 501,
  createdAt: "2026-10-27T10:00:05Z",
}

const CLARIFICATION_QUESTION: ClarificationQuestion = {
  id: QUESTION_ID,
  versionId: VERSION_ID,
  requirementId: REQUIREMENT_ID,
  sessionId: CLARIFICATION_SESSION_ID,
  question: "下单后如何扣减库存？",
  status: "open",
  answer: "",
  source: "ai",
  answeredVia: "ui",
  answeredBy: null,
  answeredAt: null,
  createdBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const CLARIFICATION_DECISION: ClarificationDecision = {
  id: DECISION_ID,
  versionId: VERSION_ID,
  requirementId: REQUIREMENT_ID,
  questionId: null,
  topic: "退款政策",
  decision: "支持 7 天无理由退款",
  rationale: "",
  status: "proposed",
  source: "ai",
  proposedBy: 1,
  decidedBy: null,
  decidedAt: null,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" },
  })
}

type CapturedRequest = {
  url: string
  method: string
  headers: Record<string, string>
  body: string | undefined
  /** The untouched body (string, FormData, …) for multipart assertions. */
  rawBody: BodyInit | null | undefined
}

/** Returns the URL, method, headers and body of the most recent fetch call. */
function lastRequest(): CapturedRequest {
  const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit]
  return {
    url: String(url),
    method: String(init.method),
    headers: (init.headers ?? {}) as Record<string, string>,
    body: typeof init.body === "string" ? init.body : undefined,
    rawBody: init.body,
  }
}

/** Parses the JSON body of a captured request, keeping undefined bodies undefined. */
function jsonBody(request: CapturedRequest): unknown {
  return request.body === undefined ? undefined : JSON.parse(request.body)
}

describe("versions api", () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    setToken("test-token")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("lists versions with filters and keeps the page envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [VERSION], next_cursor: "cursor-2", has_more: true }),
    )

    const page = await listVersions({ status: "clarifying", limit: 20, cursor: "cursor-1" })

    expect(page.items).toEqual([VERSION])
    expect(page.nextCursor).toBe("cursor-2")
    expect(page.hasMore).toBe(true)
    expect(typeof page.items[0].id).toBe("string")
    expect(page.items[0].id).toBe(VERSION_ID)

    const request = lastRequest()
    expect(request.url.endsWith("/api/versions?status=clarifying&limit=20&cursor=cursor-1")).toBe(
      true,
    )
    expect(request.method).toBe("GET")
    expect(request.headers.Authorization).toBe("Bearer test-token")
  })

  it("creates a version with P2 resource IDs as numbers", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: VERSION }, 201))

    const created = await createVersion({
      name: "订单系统 2.0",
      label: "v2.0",
      systemId: 1,
      environmentId: 2,
      codeRepositoryId: 3,
      codeBranch: "main",
    })

    expect(created).toEqual(VERSION)
    const request = lastRequest()
    expect(request.url.endsWith("/api/versions")).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body === undefined ? undefined : JSON.parse(request.body)).toEqual({
      name: "订单系统 2.0",
      label: "v2.0",
      systemId: 1,
      environmentId: 2,
      codeRepositoryId: 3,
      codeBranch: "main",
    })
  })

  it("reads and patches a version by its raw string id", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: VERSION }))
    await expect(getVersion(VERSION_ID)).resolves.toEqual(VERSION)
    expect(lastRequest().url.endsWith(`/api/versions/${VERSION_ID}`)).toBe(true)
    expect(lastRequest().method).toBe("GET")

    fetchMock.mockResolvedValue(
      jsonResponse({ data: { ...VERSION, name: "订单系统 3.0", revision: 3 } }),
    )
    const updated = await updateVersion(VERSION_ID, { revision: 2, name: "订单系统 3.0" })

    expect(updated.revision).toBe(3)
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}`)).toBe(true)
    expect(request.method).toBe("PATCH")
    expect(request.body === undefined ? undefined : JSON.parse(request.body)).toEqual({
      revision: 2,
      name: "订单系统 3.0",
    })
  })

  it("replaces bindings with a revision and explicit nulls", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: {
          ...VERSION,
          systemId: null,
          environmentId: null,
          codeRepositoryId: null,
          bindings: { system: null, environment: null, codeRepository: null },
          revision: 4,
        },
      }),
    )

    const bound = await putBindings(VERSION_ID, {
      revision: 3,
      systemId: null,
      codeBranch: "release",
      codeBaselineCommit: null,
    })

    expect(bound.bindings.system).toBeNull()
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/bindings`)).toBe(true)
    expect(request.method).toBe("PUT")
    expect(request.body === undefined ? undefined : JSON.parse(request.body)).toEqual({
      revision: 3,
      systemId: null,
      codeBranch: "release",
      codeBaselineCommit: null,
    })
  })

  it("starts clarification without a request body", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: VERSION }))

    await expect(startClarification(VERSION_ID)).resolves.toEqual(VERSION)

    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/start-clarification`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()
  })

  it("enters design with the acknowledgement flag and returns warnings", async () => {
    const designing: Version = { ...VERSION, status: "designing", revision: 5 }
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { version: designing, warnings: ["2 个需求未澄清"] } }),
    )

    const result = await enterDesign(VERSION_ID, true)

    expect(result.version.status).toBe("designing")
    expect(result.warnings).toEqual(["2 个需求未澄清"])
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/enter-design`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body === undefined ? undefined : JSON.parse(request.body)).toEqual({
      acknowledgeUnclarified: true,
    })

    fetchMock.mockResolvedValue(jsonResponse({ data: { version: designing } }))
    await expect(enterDesign(VERSION_ID, false)).resolves.toEqual({
      version: designing,
      warnings: [],
    })
  })

  it("fetches the event snapshot with the snapshot cursor preserved", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [EVENT], snapshot_cursor: 7, has_more: true }))

    const page = await fetchVersionEvents(VERSION_ID, { limit: 50 })

    expect(page).toEqual({ events: [EVENT], cursor: 7, hasMore: true })
    expect(page.events[0].version_id).toBe(VERSION_ID)
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/events?limit=50`)).toBe(true)
    expect(request.method).toBe("GET")
  })
})

describe("requirement workbench api", () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    setToken("test-token")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("lists requirement pages with filters and keeps string ids", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [REQUIREMENT], next_cursor: "cursor-r2", has_more: true }),
    )

    const page = await listRequirements(VERSION_ID, {
      status: "draft",
      limit: 20,
      cursor: "cursor-1",
    })

    expect(page.items).toEqual([REQUIREMENT])
    expect(page.nextCursor).toBe("cursor-r2")
    expect(page.hasMore).toBe(true)
    expect(typeof page.items[0].id).toBe("string")
    expect(page.items[0].versionId).toBe(VERSION_ID)

    const request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/requirements?status=draft&limit=20&cursor=cursor-1`,
      ),
    ).toBe(true)
    expect(request.method).toBe("GET")
  })

  it("creates a requirement with its body and string ids", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: REQUIREMENT }, 201))

    const created = await createRequirement(VERSION_ID, {
      title: "订单创建",
      description: "用户提交订单。",
      acceptanceCriteria: "订单出现在订单列表。",
      priority: "should",
    })

    expect(created).toEqual(REQUIREMENT)
    expect(typeof created.id).toBe("string")
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/requirements`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(jsonBody(request)).toEqual({
      title: "订单创建",
      description: "用户提交订单。",
      acceptanceCriteria: "订单出现在订单列表。",
      priority: "should",
    })
  })

  it("updates a requirement by id with its revision", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { ...REQUIREMENT, title: "订单创建（修订）", revision: 4 } }),
    )

    const updated = await updateRequirement(VERSION_ID, REQUIREMENT_ID, {
      revision: 3,
      title: "订单创建（修订）",
    })

    expect(updated.revision).toBe(4)
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}`)).toBe(
      true,
    )
    expect(request.method).toBe("PATCH")
    expect(jsonBody(request)).toEqual({ revision: 3, title: "订单创建（修订）" })
  })

  it("deletes a requirement by its raw string id", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))

    await expect(deleteRequirement(VERSION_ID, REQUIREMENT_ID)).resolves.toBeUndefined()

    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}`)).toBe(
      true,
    )
    expect(request.method).toBe("DELETE")
  })

  it("confirms and reopens a requirement without a request body", async () => {
    const clarified: Requirement = {
      ...REQUIREMENT,
      status: "clarified",
      openQuestionCount: 0,
      revision: 4,
      confirmedAt: "2026-10-27T11:00:00Z",
      confirmedBy: 1,
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: clarified }))

    await expect(confirmRequirement(VERSION_ID, REQUIREMENT_ID)).resolves.toEqual(clarified)

    let request = lastRequest()
    expect(
      request.url.endsWith(`/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}/confirm`),
    ).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()

    const reopened: Requirement = {
      ...clarified,
      status: "draft",
      revision: 5,
      confirmedAt: null,
      confirmedBy: null,
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: reopened }))
    await expect(reopenRequirement(VERSION_ID, REQUIREMENT_ID)).resolves.toEqual(reopened)

    request = lastRequest()
    expect(
      request.url.endsWith(`/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}/reopen`),
    ).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()
  })

  it("lists attachment metadata with the requirement ids in the path", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [ATTACHMENT] }))

    const attachments = await listAttachments(VERSION_ID, REQUIREMENT_ID)

    expect(attachments).toEqual([ATTACHMENT])
    const request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}/attachments`,
      ),
    ).toBe(true)
    expect(request.method).toBe("GET")
  })

  it("uploads a file as multipart FormData with the exact file field", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: ATTACHMENT }, 201))

    const uploaded = await uploadAttachment(VERSION_ID, REQUIREMENT_ID, UPLOAD_FILE)

    expect(uploaded).toEqual(ATTACHMENT)
    const request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}/attachments`,
      ),
    ).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()
    expect(request.rawBody instanceof FormData).toBe(true)
    const body = request.rawBody
    if (body instanceof FormData) {
      expect(Array.from(body.keys())).toEqual(["file"])
      expect(body.get("file")).toBe(UPLOAD_FILE)
    }
    expect(Object.keys(request.headers).map((key) => key.toLowerCase())).not.toContain(
      "content-type",
    )
  })

  it("downloads attachment content as an authenticated Blob", async () => {
    fetchMock.mockResolvedValue(
      new Response("attachment-bytes", {
        status: 200,
        headers: { "content-type": "text/plain" },
      }),
    )

    const blob = await downloadAttachment(VERSION_ID, REQUIREMENT_ID, ATTACHMENT_ID)

    expect(blob.size).toBe("attachment-bytes".length)
    expect(await blob.text()).toBe("attachment-bytes")
    const request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}/attachments/${ATTACHMENT_ID}/content`,
      ),
    ).toBe(true)
    expect(request.method).toBe("GET")
    expect(request.headers.Authorization).toBe("Bearer test-token")
  })

  it("deletes attachment metadata by id", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))

    await expect(
      deleteAttachment(VERSION_ID, REQUIREMENT_ID, ATTACHMENT_ID),
    ).resolves.toBeUndefined()

    const request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/requirements/${REQUIREMENT_ID}/attachments/${ATTACHMENT_ID}`,
      ),
    ).toBe(true)
    expect(request.method).toBe("DELETE")
  })

  it("lists every relation of a version", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [RELATION] }))

    const relations = await listRelations(VERSION_ID)

    expect(relations).toEqual([RELATION])
    expect(typeof relations[0].fromRequirementId).toBe("string")
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/relations`)).toBe(true)
    expect(request.method).toBe("GET")
  })

  it("creates a relation with string endpoint ids", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: RELATION }, 201))

    const created = await createRelation(VERSION_ID, {
      fromRequirementId: REQUIREMENT_ID,
      toRequirementId: OTHER_REQUIREMENT_ID,
      relationType: "depends_on",
      description: "创建订单后扣减库存",
    })

    expect(created).toEqual(RELATION)
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/relations`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(jsonBody(request)).toEqual({
      fromRequirementId: REQUIREMENT_ID,
      toRequirementId: OTHER_REQUIREMENT_ID,
      relationType: "depends_on",
      description: "创建订单后扣减库存",
    })
  })

  it("updates a relation and deletes it by its raw string id", async () => {
    const updated = {
      ...RELATION,
      relationType: "conflicts_with" as const,
      description: "同一时段互斥",
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: updated }))

    const result = await updateRelation(VERSION_ID, RELATION_ID, {
      relationType: "conflicts_with",
      description: "同一时段互斥",
    })

    expect(result).toEqual(updated)
    let request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/relations/${RELATION_ID}`)).toBe(true)
    expect(request.method).toBe("PATCH")
    expect(jsonBody(request)).toEqual({
      relationType: "conflicts_with",
      description: "同一时段互斥",
    })

    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(deleteRelation(VERSION_ID, RELATION_ID)).resolves.toBeUndefined()
    request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/relations/${RELATION_ID}`)).toBe(true)
    expect(request.method).toBe("DELETE")
  })
})

describe("clarification api", () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    setToken("test-token")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("reads the clarification overview and normalizes missing session and counters", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: {
          session: CLARIFICATION_SESSION,
          questions: { open: 2, answered: 1, dismissed: 0 },
          decisions: { proposed: 1, accepted: 0, rejected: 0 },
        },
      }),
    )

    await expect(getClarification(VERSION_ID)).resolves.toEqual({
      session: CLARIFICATION_SESSION,
      questions: { open: 2, answered: 1, dismissed: 0 },
      decisions: { proposed: 1, accepted: 0, rejected: 0 },
    })
    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/clarification`)).toBe(true)
    expect(request.method).toBe("GET")

    fetchMock.mockResolvedValue(jsonResponse({ data: { session: null } }))
    await expect(getClarification(VERSION_ID)).resolves.toEqual({
      session: null,
      questions: { open: 0, answered: 0, dismissed: 0 },
      decisions: { proposed: 0, accepted: 0, rejected: 0 },
    })
  })

  it("starts and completes a clarification session without a request body", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: CLARIFICATION_SESSION }, 201))
    await expect(startClarificationSession(VERSION_ID)).resolves.toEqual(CLARIFICATION_SESSION)
    let request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/clarification/sessions`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()

    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(
      completeClarificationSession(VERSION_ID, CLARIFICATION_SESSION_ID),
    ).resolves.toBeUndefined()
    request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/clarification/sessions/${CLARIFICATION_SESSION_ID}/complete`,
      ),
    ).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()
  })

  it("lists messages with before_id and normalizes optional fields", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [
          {
            id: 11,
            role: "assistant",
            parts: [{ type: "text", text: "请确认是否必填。" }],
            runId: 501,
            createdAt: "2026-10-27T10:00:05Z",
          },
          CLARIFICATION_MESSAGE,
        ],
        next_cursor: "10",
        has_more: true,
      }),
    )

    const page = await listClarificationMessages(VERSION_ID, { beforeId: 11, limit: 50 })

    expect(page.items[0]).toMatchObject({
      id: 11,
      reasoning: "",
      toolCalls: null,
      toolName: null,
    })
    expect(page.items[1]).toEqual(CLARIFICATION_MESSAGE)
    expect(page.nextCursor).toBe("10")
    expect(page.hasMore).toBe(true)
    const request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/clarification/messages?before_id=11&limit=50`,
      ),
    ).toBe(true)
    expect(request.method).toBe("GET")
  })

  it("sends a message and starts the analyze/recon runs without bodies", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        {
          data: {
            sessionId: CLARIFICATION_SESSION_ID,
            agentSessionId: 77,
            runId: 501,
            status: "running",
          },
        },
        202,
      ),
    )

    const sent = await sendClarificationMessage(VERSION_ID, "库存如何扣减？")

    expect(sent).toEqual({
      sessionId: CLARIFICATION_SESSION_ID,
      agentSessionId: 77,
      runId: 501,
      status: "running",
    })
    let request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/clarification/messages`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(jsonBody(request)).toEqual({ message: "库存如何扣减？" })

    const runResult = { agentSessionId: 77, runId: 501 }
    fetchMock.mockResolvedValue(jsonResponse({ data: runResult }, 202))
    await expect(analyzeClarification(VERSION_ID)).resolves.toEqual(runResult)
    request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/clarification/analyze`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()

    fetchMock.mockResolvedValue(jsonResponse({ data: { agentSessionId: 77, runId: 502 } }, 202))
    await expect(reconClarification(VERSION_ID)).resolves.toEqual({
      agentSessionId: 77,
      runId: 502,
    })
    request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/clarification/recon`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(request.body).toBeUndefined()
  })

  it("lists, creates and updates clarification questions", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [
          {
            id: QUESTION_ID,
            versionId: VERSION_ID,
            question: "下单后如何扣减库存？",
            status: "open",
            answer: "",
            source: "ai",
            answeredVia: "ui",
            answeredBy: null,
            answeredAt: null,
            createdBy: 1,
            createdAt: "2026-10-27T10:00:00Z",
            updatedAt: "2026-10-27T10:00:00Z",
          },
        ],
        next_cursor: "q2",
        has_more: true,
      }),
    )

    const page = await listClarificationQuestions(VERSION_ID, {
      status: "open",
      requirementId: REQUIREMENT_ID,
      limit: 50,
      cursor: "q1",
    })

    expect(page.items[0].requirementId).toBeNull()
    expect(page.items[0].sessionId).toBeNull()
    expect(page.nextCursor).toBe("q2")
    expect(page.hasMore).toBe(true)
    let request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/clarification/questions?status=open&requirementId=${REQUIREMENT_ID}&limit=50&cursor=q1`,
      ),
    ).toBe(true)
    expect(request.method).toBe("GET")

    fetchMock.mockResolvedValue(jsonResponse({ data: CLARIFICATION_QUESTION }, 201))
    await expect(
      createClarificationQuestion(VERSION_ID, {
        question: "下单后如何扣减库存？",
        requirementId: REQUIREMENT_ID,
      }),
    ).resolves.toEqual(CLARIFICATION_QUESTION)
    request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/clarification/questions`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(jsonBody(request)).toEqual({
      question: "下单后如何扣减库存？",
      requirementId: REQUIREMENT_ID,
    })

    fetchMock.mockResolvedValue(
      jsonResponse({ data: { ...CLARIFICATION_QUESTION, status: "answered", answer: "下单扣减" } }),
    )
    await updateClarificationQuestion(VERSION_ID, QUESTION_ID, {
      status: "answered",
      answer: "下单扣减",
    })
    request = lastRequest()
    expect(
      request.url.endsWith(`/api/versions/${VERSION_ID}/clarification/questions/${QUESTION_ID}`),
    ).toBe(true)
    expect(request.method).toBe("PATCH")
    expect(jsonBody(request)).toEqual({ status: "answered", answer: "下单扣减" })
  })

  it("lists, creates and updates decisions", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [
          {
            id: DECISION_ID,
            versionId: VERSION_ID,
            topic: "退款政策",
            decision: "支持 7 天无理由退款",
            rationale: "",
            status: "proposed",
            source: "ai",
            proposedBy: 1,
            decidedBy: null,
            decidedAt: null,
            createdAt: "2026-10-27T10:00:00Z",
            updatedAt: "2026-10-27T10:00:00Z",
          },
        ],
        next_cursor: "d2",
        has_more: true,
      }),
    )

    const page = await listDecisions(VERSION_ID, { status: "proposed", limit: 50, cursor: "d1" })

    expect(page.items[0].requirementId).toBeNull()
    expect(page.items[0].questionId).toBeNull()
    expect(page.nextCursor).toBe("d2")
    let request = lastRequest()
    expect(
      request.url.endsWith(
        `/api/versions/${VERSION_ID}/decisions?status=proposed&limit=50&cursor=d1`,
      ),
    ).toBe(true)
    expect(request.method).toBe("GET")

    fetchMock.mockResolvedValue(jsonResponse({ data: CLARIFICATION_DECISION }, 201))
    await expect(
      createDecision(VERSION_ID, {
        topic: "退款政策",
        decision: "支持 7 天无理由退款",
        requirementId: REQUIREMENT_ID,
      }),
    ).resolves.toEqual(CLARIFICATION_DECISION)
    request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/decisions`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(jsonBody(request)).toEqual({
      topic: "退款政策",
      decision: "支持 7 天无理由退款",
      requirementId: REQUIREMENT_ID,
    })

    fetchMock.mockResolvedValue(
      jsonResponse({ data: { ...CLARIFICATION_DECISION, status: "accepted" } }),
    )
    await updateDecision(VERSION_ID, DECISION_ID, { status: "accepted" })
    request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/decisions/${DECISION_ID}`)).toBe(true)
    expect(request.method).toBe("PATCH")
    expect(jsonBody(request)).toEqual({ status: "accepted" })
  })
})

// --- Continuing / incremental plan (spec §7.6/§7.7) ------------------------

const PARENT_BATCH_ID = "1844674407370955301"
const INCREMENTAL_BATCH_ID = "1844674407370955309"
const NEW_CASE_ID = "1844674407370955302"
const AFFECTED_CASE_ID = "1844674407370955303"
const LOW_CASE_ID = "1844674407370955304"
const PLAN_RUN_ID = "1844674407370955305"

const INCREMENTAL_PLAN: IncrementalPlan = {
  versionId: VERSION_ID,
  phase: "designing",
  parentBatchId: PARENT_BATCH_ID,
  run: {
    id: PLAN_RUN_ID,
    kind: "incremental_design",
    status: "done",
    result: {},
  },
  newCases: [
    {
      nodeId: NEW_CASE_ID,
      title: "新增退款用例",
      priority: "P1",
      confidence: "high",
      requirementIds: [REQUIREMENT_ID],
      status: "supplemented",
    },
  ],
  affectedCases: [
    {
      nodeId: AFFECTED_CASE_ID,
      title: "受影响的下单用例",
      confidence: "medium",
      reason: "退款接口变更",
      source: "code",
      selected: true,
    },
    {
      nodeId: LOW_CASE_ID,
      title: "低置信用例",
      confidence: "low",
      reason: "可能受影响",
      source: "requirement",
      selected: false,
    },
  ],
  unmappedChanges: [{ path: "src/api/refund.ts", summary: "退款接口新增 reason 参数" }],
  defaultCaseIds: [NEW_CASE_ID, AFFECTED_CASE_ID],
  warnings: [{ code: "impact_unavailable", message: "影响分析不可用" }],
  degraded: false,
}

const CREATED_BATCH = {
  id: INCREMENTAL_BATCH_ID,
  version_id: VERSION_ID,
  batch_no: 2,
  status: "planned" as const,
  kind: "incremental" as const,
  parentBatchId: PARENT_BATCH_ID,
  graphHash: "b".repeat(64),
  baseFallbackReason: "",
  paused: false,
  pause_mode: "",
  pause_reason: "",
  baseline_hash: "a".repeat(64),
  code_baseline_commit: null,
  head_commit: null,
  counts: {
    total: 2,
    passed: 0,
    failed: 0,
    blocked: 0,
    inconclusive: 0,
    skipped: 0,
    interrupted: 0,
  },
  created_by: "1",
  started_at: null,
  finished_at: null,
  failure_reason: "",
  created_at: "2026-10-28T10:00:00Z",
  updated_at: "2026-10-28T10:00:00Z",
}

describe("continuing and incremental plan api", () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    setToken("test-token")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("reads the incremental plan with string ids and collections intact", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: INCREMENTAL_PLAN }))

    const plan = await getIncrementalPlan(VERSION_ID)

    expect(plan).toEqual(INCREMENTAL_PLAN)
    expect(typeof plan.versionId).toBe("string")
    expect(typeof plan.parentBatchId).toBe("string")
    expect(typeof plan.defaultCaseIds[0]).toBe("string")
    expect(plan.defaultCaseIds).toEqual([NEW_CASE_ID, AFFECTED_CASE_ID])
    expect(plan.affectedCases.map((item) => item.selected)).toEqual([true, false])

    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/mindmap/incremental-plan`)).toBe(true)
    expect(request.method).toBe("GET")
    expect(request.headers.Authorization).toBe("Bearer test-token")
  })

  it("normalizes an empty plan so the panel never renders undefined", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: { versionId: VERSION_ID, phase: "", parentBatchId: null, run: null },
      }),
    )

    const plan = await getIncrementalPlan(VERSION_ID)

    expect(plan.run).toBeNull()
    expect(plan.newCases).toEqual([])
    expect(plan.affectedCases).toEqual([])
    expect(plan.unmappedChanges).toEqual([])
    expect(plan.defaultCaseIds).toEqual([])
    expect(plan.warnings).toEqual([])
    expect(plan.degraded).toBe(false)
  })

  it("creates an incremental batch with kind, caseIds and parentBatchId", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: CREATED_BATCH }, 201))

    const created = await createIncrementalBatch(VERSION_ID, {
      caseIds: [NEW_CASE_ID, AFFECTED_CASE_ID],
      parentBatchId: PARENT_BATCH_ID,
    })

    expect(created.id).toBe(INCREMENTAL_BATCH_ID)
    expect(typeof created.id).toBe("string")
    expect(created.batch_no).toBe(2)

    const request = lastRequest()
    expect(request.url.endsWith(`/api/versions/${VERSION_ID}/batches`)).toBe(true)
    expect(request.method).toBe("POST")
    expect(jsonBody(request)).toEqual({
      kind: "incremental",
      caseIds: [NEW_CASE_ID, AFFECTED_CASE_ID],
      parentBatchId: PARENT_BATCH_ID,
    })
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import {
  confirmRequirement,
  confirmRequirements,
  createConversation,
  createRequirement,
  getConversation,
  getRequirement,
  getTeamRoom,
  listConversations,
  listMessages,
  listRequirementRelations,
  listRequirements,
  postMessage,
  rollbackRequirement,
  updateRequirement,
  uploadContent,
} from "./api"
import {
  conversationFixture,
  messageFixture,
  requirementDetailFixture,
  requirementFixture,
  requirementVersionFixture,
  teamRoomFixture,
} from "./test-fixtures"
import type { RequirementUpdateRequest } from "./types"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function errorResponse(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({
      error: { code, message, request_id: "req_clarification", retryable: false, details: {} },
    }),
    { status, headers: { "Content-Type": "application/json" } },
  )
}

function readHeaders(init: RequestInit | undefined): Headers {
  return new Headers(init?.headers)
}

function readBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string") return undefined
  return JSON.parse(init.body) as unknown
}

const fetchMock = vi.fn<typeof fetch>()

describe("clarification api", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
    window.sessionStorage.setItem("eztest.token", "ezt_token")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("lists clarification conversations with the pinned query parameters", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [conversationFixture], next_cursor: "", has_more: false }),
    )

    await expect(listConversations("2001")).resolves.toEqual([conversationFixture])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(
      "http://localhost:13333/api/versions/2001/conversations?kind=clarification&limit=100",
    )
    expect(init?.method).toBe("GET")
    expect(readHeaders(init).get("Authorization")).toBe("Bearer ezt_token")
  })

  it("creates a conversation and returns the detail envelope", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: conversationFixture }, 201))

    await expect(
      createConversation("2001", { kind: "clarification", title: "退款澄清" }),
    ).resolves.toEqual(conversationFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/conversations")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({ kind: "clarification", title: "退款澄清" })
  })

  it("loads one conversation without its messages", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: conversationFixture }))

    await expect(getConversation("2001", "4001")).resolves.toEqual(conversationFixture)
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/conversations/4001",
    )
  })

  it("lists messages in ascending order with an after cursor", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [messageFixture], next_cursor: "5001", has_more: false }),
    )

    await expect(listMessages("2001", "4001", { after: "5000" })).resolves.toEqual([messageFixture])

    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/conversations/4001/messages?after=5000&limit=100",
    )
  })

  it("posts a user message with parts and a client message id", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: messageFixture }, 201))

    await expect(
      postMessage("2001", "4001", {
        client_message_id: "client_1",
        parts: [{ type: "text", text: "退款 T+1 到账" }],
      }),
    ).resolves.toEqual(messageFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/conversations/4001/messages")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({
      client_message_id: "client_1",
      parts: [{ type: "text", text: "退款 T+1 到账" }],
    })
  })

  it("lists requirements with an optional status filter", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [requirementFixture], next_cursor: "", has_more: false }),
    )

    await expect(listRequirements("2001", { status: "draft" })).resolves.toEqual([
      requirementFixture,
    ])
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/requirements?status=draft&limit=100",
    )
  })

  it("creates a manual draft requirement", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: requirementDetailFixture }, 201))

    await expect(
      createRequirement("2001", {
        title: "退款到账时效",
        description: "T+1 到账",
        acceptance_criteria: "审核通过后 T+1 到账",
        priority: "P1",
        targets: { version_suts: ["1002"], pages: [], apis: [] },
      }),
    ).resolves.toEqual(requirementDetailFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/requirements")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toMatchObject({ title: "退款到账时效", priority: "P1" })
  })

  it("lists requirements without a status filter and supports a before cursor", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [requirementFixture], next_cursor: "", has_more: false }),
    )
    await expect(listRequirements("2001")).resolves.toEqual([requirementFixture])
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/requirements?limit=100",
    )

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: [], next_cursor: "", has_more: false }))
    await expect(listRequirements("2001", { before: "6000" })).resolves.toEqual([])
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/requirements?before=6000&limit=100",
    )
  })

  it("loads one requirement detail with versions, confirmations and relations", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: requirementDetailFixture }))

    await expect(getRequirement("2001", "6001")).resolves.toEqual(requirementDetailFixture)
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/requirements/6001",
    )
  })

  it("patches a requirement into a new draft revision", async () => {
    const payload: RequirementUpdateRequest = {
      title: "退款到账时效",
      description: "T+1 到账",
      acceptance_criteria: "审核通过后 T+1 到账",
      priority: "P1",
      targets: { version_suts: ["1002"], pages: ["2301"], apis: [] },
      reason: "补充节假日规则",
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: requirementDetailFixture }))

    await expect(updateRequirement("2001", "6001", payload)).resolves.toEqual(
      requirementDetailFixture,
    )

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/requirements/6001")
    expect(init?.method).toBe("PATCH")
    expect(readBody(init)).toEqual(payload)
  })

  it("confirms one requirement", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { confirmed: true, requirement: requirementDetailFixture } }),
    )

    const result = await confirmRequirement("2001", "6001")
    expect(result.confirmed).toBe(true)
    expect(result.requirement).toEqual(requirementDetailFixture)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/requirements/6001/confirmations")
    expect(init?.method).toBe("POST")
  })

  it("batch confirms requirements and returns confirmed/skipped ids", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: { batch_id: "batch_1", confirmed: ["6001"], skipped: ["6002"] },
      }),
    )

    await expect(confirmRequirements("2001", ["6001", "6002"])).resolves.toEqual({
      batch_id: "batch_1",
      confirmed: ["6001"],
      skipped: ["6002"],
    })

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/requirements/confirmations")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({ requirement_ids: ["6001", "6002"] })
  })

  it("rolls a clarified requirement back to a new draft", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: requirementDetailFixture }))

    await expect(
      rollbackRequirement("2001", "6001", { reason: "时效定义变更", proposal_message_id: "5001" }),
    ).resolves.toEqual(requirementDetailFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/requirements/6001/rollback")
    expect(init?.method).toBe("POST")
    expect(readBody(init)).toEqual({
      reason: "时效定义变更",
      proposal_message_id: "5001",
    })
  })

  it("lists requirement relations through the relations endpoint", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [
          {
            id: "7001",
            from_requirement_id: "6001",
            to_requirement_id: "6002",
            kind: "conflicts_with",
            description: "金额口径冲突",
            created_at: "2026-10-31T10:00:00Z",
          },
        ],
      }),
    )

    const relations = await listRequirementRelations("2001", "6001")
    expect(relations).toHaveLength(1)
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/requirements/6001/relations",
    )
  })

  it("reads the read-only clarification team room", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: teamRoomFixture }))

    await expect(getTeamRoom("2001", "clarification")).resolves.toEqual(teamRoomFixture)
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/team-rooms/clarification?limit=20",
    )
  })

  it("uploads attachment content as multipart and returns the content object", async () => {
    const content = {
      sha256: "a".repeat(64),
      size: 12,
      mime: "text/plain",
      url: `/api/content/${"a".repeat(64)}`,
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: content }, 201))
    const file = new File(["hello 退款"], "退款.txt", { type: "text/plain" })

    await expect(uploadContent(file)).resolves.toEqual(content)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/content")
    expect(init?.method).toBe("POST")
    expect(init?.body).toBeInstanceOf(FormData)
    const form = init?.body as FormData
    expect(form.get("file")).toBeInstanceOf(File)
    expect((form.get("file") as File).name).toBe("退款.txt")
    const headers = readHeaders(init)
    expect(headers.get("Authorization")).toBe("Bearer ezt_token")
    expect(headers.has("Content-Type")).toBe(false)
  })

  it("maps the error envelope into ApiError for list and upload calls", async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(404, "not_found", "版本不存在"))
    await expect(listConversations("9999")).rejects.toMatchObject({
      code: "not_found",
      status: 404,
      message: "版本不存在",
      requestId: "req_clarification",
    })

    fetchMock.mockResolvedValueOnce(errorResponse(413, "payload_too_large", "附件超过 10 MiB"))
    await expect(uploadContent(new File(["x"], "big.pdf"))).rejects.toBeInstanceOf(ApiError)
  })

  it("keeps the requirement fixtures aligned with the canonical shape", () => {
    expect(requirementFixture.current_version_id).toBe(requirementVersionFixture.id)
    expect(requirementDetailFixture.current_version).toEqual(requirementVersionFixture)
    expect(requirementDetailFixture.history).toEqual([])
    expect(requirementDetailFixture.targets).toEqual({
      version_suts: ["1002"],
      pages: ["2301"],
      apis: [],
    })
  })
})

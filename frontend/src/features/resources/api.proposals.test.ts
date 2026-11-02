import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import {
  confirmApiProposal,
  confirmPageProposal,
  generatePageApiProposals,
  listPageApiProposals,
  rejectApiProposal,
  rejectPageProposal,
} from "./api"
import type { CodeProposal } from "./types"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

const proposalFixture: CodeProposal = {
  kind: "page",
  id: "1311",
  name: "退款结果页",
  url: "https://order.test/refund",
  route_template: "/refund",
  method: null,
  path_template: null,
  status: "proposed",
  source: "ai",
  evidence: [{ ref: "code_fact:9001", path: "src/pages/RefundPage.tsx" }],
  created_at: "2026-11-01T10:00:00Z",
}

describe("resources proposal API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
  })

  it("generates and lists page/API proposals for one version", async () => {
    fetchMock
      .mockResolvedValueOnce(
        jsonResponse({
          data: {
            pages: { created: 2, skipped: 1, ids: ["1311", "1312"] },
            apis: { created: 1, skipped: 2, ids: ["1411"] },
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({ data: [proposalFixture], next_cursor: null, has_more: false }),
      )

    await expect(generatePageApiProposals("2001", { repoId: "5001" })).resolves.toEqual({
      pages: { created: 2, skipped: 1, ids: ["1311", "1312"] },
      apis: { created: 1, skipped: 2, ids: ["1411"] },
    })
    await expect(listPageApiProposals("2001")).resolves.toEqual([proposalFixture])

    const [generateUrl, generateInit] = fetchMock.mock.calls[0]
    expect(generateUrl).toBe("http://localhost:13333/api/versions/2001/code/page-api-proposals")
    expect(generateInit?.method).toBe("POST")
    expect(JSON.parse(String(generateInit?.body))).toEqual({ repoId: "5001" })
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/code/page-api-proposals",
    )
  })

  it("omits the repoId body when no repository is selected", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: {
          pages: { created: 0, skipped: 0, ids: [] },
          apis: { created: 0, skipped: 0, ids: [] },
        },
      }),
    )

    await generatePageApiProposals("2001")
    const [, init] = fetchMock.mock.calls[0]
    expect(init?.body).toBeUndefined()
  })

  it("confirms and rejects pages/APIs through the resource endpoints", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ data: { id: "1311", status: "approved" } }))
      .mockResolvedValueOnce(jsonResponse({ data: { id: "1311", status: "rejected" } }))
      .mockResolvedValueOnce(jsonResponse({ data: { id: "1411", status: "approved" } }))
      .mockResolvedValueOnce(jsonResponse({ data: { id: "1411", status: "rejected" } }))

    await confirmPageProposal("1311")
    await rejectPageProposal("1311")
    await confirmApiProposal("1411")
    await rejectApiProposal("1411")

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "http://localhost:13333/api/resources/pages/1311/confirm",
      "http://localhost:13333/api/resources/pages/1311/reject",
      "http://localhost:13333/api/resources/apis/1411/confirm",
      "http://localhost:13333/api/resources/apis/1411/reject",
    ])
    for (const [, init] of fetchMock.mock.calls) expect(init?.method).toBe("POST")
  })

  it("surfaces the admin-only 403 for a normal user", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "forbidden",
            message: "admin required",
            request_id: "req_proposal",
            retryable: false,
            details: {},
          },
        }),
        { status: 403, headers: { "Content-Type": "application/json" } },
      ),
    )

    const failure = confirmPageProposal("1311")
    await expect(failure).rejects.toBeInstanceOf(ApiError)
    await expect(failure).rejects.toMatchObject({ status: 403, code: "forbidden" })
  })
})

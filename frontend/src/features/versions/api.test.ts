import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { StageView } from "@/features/design/types"

import {
  blockVersion,
  createVersion,
  getResourceCatalog,
  getVersion,
  listPlanItems,
  listVersions,
  type PlanItem,
  parseStageBlocker,
  pauseVersion,
  planConflictDetails,
  requestStage,
  resumeVersion,
  type StageBlocker,
  stageGateDetails,
  stageTransitionDetails,
  unblockVersion,
  updatePlanItem,
  updateVersion,
} from "./api"
import { catalogFixture, versionFixture, versionSummaryFixture } from "./test-fixtures"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

describe("versions API", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("lists versions through the pagination envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: [versionSummaryFixture],
        next_cursor: null,
        has_more: false,
      }),
    )

    await expect(listVersions()).resolves.toEqual([versionSummaryFixture])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions?limit=100")
    expect(init?.method).toBe("GET")
  })

  it("loads the resource catalog for the binding wizard", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: catalogFixture }))

    await expect(getResourceCatalog()).resolves.toEqual(catalogFixture)

    const [url] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/resources/catalog")
  })

  it("loads one version by its string id", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: versionFixture }))

    await expect(getVersion("2001")).resolves.toEqual(versionFixture)

    const [url] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001")
  })

  it("creates a version with a POST body", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: versionFixture }, 201))
    const payload = {
      title: "订单中心 · 2026 Q4 迭代",
      bindings: [
        {
          sut_id: "1001",
          environment_id: "1101",
          page_ids: ["1301"],
          api_ids: [],
          database_ids: [],
          accounts: [],
        },
      ],
    }

    await expect(createVersion(payload)).resolves.toEqual(versionFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions")
    expect(init?.method).toBe("POST")
    expect(JSON.parse(String(init?.body))).toEqual(payload)
  })

  it("patches the auto design flag for the version", async () => {
    const updated = { ...versionFixture, auto_design_new_requirements: true }
    fetchMock.mockResolvedValue(jsonResponse({ data: updated }))

    await expect(updateVersion("2001", { auto_design_new_requirements: true })).resolves.toEqual(
      updated,
    )

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001")
    expect(init?.method).toBe("PATCH")
    expect(JSON.parse(String(init?.body))).toEqual({ auto_design_new_requirements: true })
  })

  const stageFixture: StageView = {
    version_id: "2001",
    stage: "requirement_review",
    stage_entered_at: "2026-11-01T07:00:00Z",
    blocker: null,
  }

  const blockerFixture: StageBlocker = {
    kind: "executor",
    reason: "executor offline",
    action: "start executor",
    next: "retry",
  }

  const planItemFixture: PlanItem = {
    id: "9101",
    version_id: "2001",
    phase: "准备",
    title: "准备环境",
    status: "pending",
    decision: "",
    order: 1,
    revision: 4,
    depends_on: [],
    work_item_id: null,
    created_at: "2026-11-01T07:00:00Z",
    updated_at: "2026-11-01T07:00:00Z",
  }

  it("requests a stage transition through the stage API", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: stageFixture }))

    await expect(requestStage("2001", "requirement_review")).resolves.toEqual(stageFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/stage/request")
    expect(init?.method).toBe("POST")
    expect(JSON.parse(String(init?.body))).toEqual({ to: "requirement_review" })
  })

  it("blocks with structured fields, then unblocks, pauses and resumes", async () => {
    fetchMock.mockImplementation(async () => jsonResponse({ data: stageFixture }))

    await blockVersion("2001", blockerFixture)
    await unblockVersion("2001")
    await pauseVersion("2001", "维护窗口")
    await resumeVersion("2001")

    expect(fetchMock.mock.calls.map(([url, init]) => [String(url), init?.method])).toEqual([
      ["http://localhost:13333/api/versions/2001/stage/block", "POST"],
      ["http://localhost:13333/api/versions/2001/stage/unblock", "POST"],
      ["http://localhost:13333/api/versions/2001/stage/pause", "POST"],
      ["http://localhost:13333/api/versions/2001/stage/resume", "POST"],
    ])
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual(blockerFixture)
    expect(fetchMock.mock.calls[1][1]?.body).toBeUndefined()
    expect(JSON.parse(String(fetchMock.mock.calls[2][1]?.body))).toEqual({ reason: "维护窗口" })
    expect(fetchMock.mock.calls[3][1]?.body).toBeUndefined()
  })

  it("lists plan items through the pagination envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: [planItemFixture], next_cursor: null, has_more: false }),
    )

    await expect(listPlanItems("2001")).resolves.toEqual([planItemFixture])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/plan-items?limit=100")
    expect(init?.method).toBe("GET")
  })

  it("patches a plan item with its revision token", async () => {
    const updated = { ...planItemFixture, status: "done", revision: 5 }
    fetchMock.mockResolvedValue(jsonResponse({ data: updated }))

    await expect(updatePlanItem("2001", "9101", { status: "done", revision: 4 })).resolves.toEqual(
      updated,
    )

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/plan-items/9101")
    expect(init?.method).toBe("PATCH")
    expect(JSON.parse(String(init?.body))).toEqual({ status: "done", revision: 4 })
  })

  it("exposes typed stage gate, stage transition and plan conflict details", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        {
          error: {
            code: "stage_gate_failed",
            message: "阶段门禁未通过",
            details: {
              gate: "requirement",
              reason: "unconfirmed_requirements",
              action: "确认全部需求",
              next: "requirement_review",
            },
          },
        },
        409,
      ),
    )
    const gateError = await requestStage("2001", "designing").catch((error: unknown) => error)
    expect(stageGateDetails(gateError)).toEqual({
      gate: "requirement",
      reason: "unconfirmed_requirements",
      action: "确认全部需求",
      next: "requirement_review",
    })
    expect(stageGateDetails(new Error("boom"))).toBeNull()

    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        {
          error: {
            code: "invalid_stage_transition",
            message: "阶段迁移不合法",
            details: { from: "clarifying", to: "executing", action: "按顺序推进" },
          },
        },
        409,
      ),
    )
    const transitionError = await requestStage("2001", "executing").catch((error: unknown) => error)
    expect(stageTransitionDetails(transitionError)).toEqual({
      from: "clarifying",
      to: "executing",
      action: "按顺序推进",
    })

    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        {
          error: {
            code: "conflict",
            message: "计划项已被更新",
            details: { item_id: "9101", expected_revision: 4, actual_revision: 5 },
          },
        },
        409,
      ),
    )
    const conflictError = await updatePlanItem("2001", "9101", {
      status: "done",
      revision: 4,
    }).catch((error: unknown) => error)
    expect(planConflictDetails(conflictError)).toEqual({
      itemId: "9101",
      expectedRevision: 4,
      actualRevision: 5,
    })
    expect(planConflictDetails(gateError)).toBeNull()
  })

  it("parses structured JSON and legacy plain-text blockers", () => {
    expect(parseStageBlocker(JSON.stringify(blockerFixture))).toEqual(blockerFixture)
    expect(parseStageBlocker(blockerFixture)).toEqual(blockerFixture)
    expect(parseStageBlocker("executor offline")).toEqual({
      kind: "manual",
      reason: "executor offline",
      action: "",
      next: "",
    })
    expect(parseStageBlocker(null)).toBeNull()
    expect(parseStageBlocker("")).toBeNull()
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import {
  cancelHumanRequest,
  cancelIntent,
  cancelWave,
  createIntent,
  downloadEvidence,
  getCaseRun,
  getEvidence,
  getOverview,
  getWave,
  listCaseRuns,
  listEvidence,
  listHumanRequests,
  listIntents,
  listWaves,
  pauseCaseRun,
  pauseWave,
  resolveHumanRequest,
  resumeCaseRun,
  resumeWave,
} from "./api"
import {
  caseRunDetailFixture,
  evidenceScreenshotFixture,
  executionOverviewFixture,
  failedCaseRunFixture,
  humanRequestFixture,
  intentFixture,
  pausedWaveFixture,
  resolvedHumanRequestFixture,
  runningWaveFixture,
} from "./test-fixtures"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function errorResponse(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({
      error: { code, message, request_id: "req_execution", retryable: false, details: {} },
    }),
    { status, headers: { "Content-Type": "application/json" } },
  )
}

function listResponse(data: unknown[]): Response {
  return jsonResponse({ data, next_cursor: "", has_more: false })
}

function readBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string") return undefined
  return JSON.parse(init.body) as unknown
}

function headersOf(init: RequestInit | undefined): Headers {
  return new Headers(init?.headers)
}

const fetchMock = vi.fn<typeof fetch>()

describe("execution api", () => {
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

  it("loads the execution overview", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: executionOverviewFixture }))

    await expect(getOverview("2001")).resolves.toEqual(executionOverviewFixture)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/execution/overview")
    expect(init?.method).toBe("GET")
  })

  it("lists waves with the cursor page envelope", async () => {
    fetchMock.mockResolvedValue(listResponse([runningWaveFixture]))

    await expect(listWaves("2001")).resolves.toEqual([runningWaveFixture])
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/waves?limit=20",
    )

    fetchMock.mockResolvedValueOnce(listResponse([pausedWaveFixture]))
    await expect(listWaves("2001", { before: "9201", limit: 5 })).resolves.toEqual([
      pausedWaveFixture,
    ])
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/waves?before=9201&limit=5",
    )
  })

  it("loads one wave and lists its case runs with filters", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: runningWaveFixture }))
    await expect(getWave("2001", "9201")).resolves.toEqual(runningWaveFixture)
    expect(fetchMock.mock.calls[0][0]).toBe("http://localhost:13333/api/versions/2001/waves/9201")

    fetchMock.mockResolvedValueOnce(listResponse([failedCaseRunFixture]))
    await expect(
      listCaseRuns("2001", "9201", { status: "failed", before: "9302", limit: 10 }),
    ).resolves.toEqual([failedCaseRunFixture])
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/waves/9201/case-runs?status=failed&before=9302&limit=10",
    )
  })

  it("loads a case run detail with steps, assertions and evidence", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: caseRunDetailFixture }))

    await expect(getCaseRun("2001", "9302")).resolves.toEqual(caseRunDetailFixture)
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/case-runs/9302",
    )
  })

  it("pauses and resumes a wave with an Idempotency-Key", async () => {
    fetchMock.mockImplementation(async () => jsonResponse({ data: pausedWaveFixture }))

    await pauseWave("2001", "9202", { mode: "graceful", reason: "等待登录接管" })
    await resumeWave("2001", "9202")

    const [pauseUrl, pauseInit] = fetchMock.mock.calls[0]
    expect(pauseUrl).toBe("http://localhost:13333/api/versions/2001/waves/9202/pause")
    expect(pauseInit?.method).toBe("POST")
    expect(readBody(pauseInit)).toEqual({ mode: "graceful", reason: "等待登录接管" })
    expect(headersOf(pauseInit).get("Idempotency-Key")).toMatch(/^wave-pause-/)

    const [resumeUrl, resumeInit] = fetchMock.mock.calls[1]
    expect(resumeUrl).toBe("http://localhost:13333/api/versions/2001/waves/9202/resume")
    expect(resumeInit?.method).toBe("POST")
    expect(headersOf(resumeInit).get("Idempotency-Key")).toMatch(/^wave-resume-/)
  })

  it("cancels a wave and pauses/resumes a single case run", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ data: { ...runningWaveFixture, status: "cancelled" } }),
    )
    await cancelWave("2001", "9201")
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/waves/9201/cancel",
    )

    fetchMock.mockResolvedValueOnce(
      jsonResponse({ data: { ...failedCaseRunFixture, status: "paused" } }),
    )
    await pauseCaseRun("2001", "9302", { mode: "immediate" })
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/case-runs/9302/pause",
    )
    expect(readBody(fetchMock.mock.calls[1][1])).toEqual({ mode: "immediate" })

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: failedCaseRunFixture }))
    await resumeCaseRun("2001", "9302")
    expect(fetchMock.mock.calls[2][0]).toBe(
      "http://localhost:13333/api/versions/2001/case-runs/9302/resume",
    )

    for (const [, init] of fetchMock.mock.calls) {
      expect(headersOf(init).get("Idempotency-Key")).toMatch(
        /^(wave-cancel|case-run-pause|case-run-resume)-/,
      )
    }
  })

  it("lists evidence with filters and loads one evidence view", async () => {
    fetchMock.mockResolvedValueOnce(listResponse([evidenceScreenshotFixture]))
    await expect(
      listEvidence("2001", {
        caseRunId: "9302",
        versionSutId: "1002",
        kind: "screenshot",
        before: "9401",
        limit: 50,
      }),
    ).resolves.toEqual([evidenceScreenshotFixture])
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/evidence?case_run_id=9302&version_sut_id=1002&kind=screenshot&before=9401&limit=50",
    )

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: evidenceScreenshotFixture }))
    await expect(getEvidence("2001", "9401")).resolves.toEqual(evidenceScreenshotFixture)
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/evidence/9401",
    )
  })

  it("downloads evidence bytes through apiDownload", async () => {
    fetchMock.mockResolvedValue(
      new Response("png-bytes", {
        status: 200,
        headers: {
          "Content-Type": "image/png",
          "Content-Disposition": 'attachment; filename="step-3.png"',
        },
      }),
    )

    const download = await downloadEvidence("2001", "9401")
    expect(download.filename).toBe("step-3.png")
    expect(download.contentType).toBe("image/png")

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/versions/2001/evidence/9401/content")
    expect(init?.method).toBe("GET")
    expect(headersOf(init).get("Authorization")).toBe("Bearer ezt_token")
  })

  it("lists open human requests and resolves/cancels one", async () => {
    fetchMock.mockResolvedValueOnce(listResponse([humanRequestFixture]))
    await expect(listHumanRequests("2001", { status: "open" })).resolves.toEqual([
      humanRequestFixture,
    ])
    expect(fetchMock.mock.calls[0][0]).toBe(
      "http://localhost:13333/api/versions/2001/human-requests?status=open",
    )

    fetchMock.mockResolvedValueOnce(jsonResponse({ data: resolvedHumanRequestFixture }))
    await expect(resolveHumanRequest("9501", { code: "246810", note: "已输入" })).resolves.toEqual(
      resolvedHumanRequestFixture,
    )
    const [resolveUrl, resolveInit] = fetchMock.mock.calls[1]
    expect(resolveUrl).toBe("http://localhost:13333/api/human-requests/9501/resolve")
    expect(resolveInit?.method).toBe("POST")
    expect(readBody(resolveInit)).toEqual({ response: { code: "246810", note: "已输入" } })
    expect(headersOf(resolveInit).get("Idempotency-Key")).toMatch(/^human-resolve-/)

    fetchMock.mockResolvedValueOnce(
      jsonResponse({ data: { ...humanRequestFixture, status: "cancelled" } }),
    )
    await cancelHumanRequest("9501")
    expect(fetchMock.mock.calls[2][0]).toBe("http://localhost:13333/api/human-requests/9501/cancel")
    expect(headersOf(fetchMock.mock.calls[2][1]).get("Idempotency-Key")).toMatch(/^human-cancel-/)
  })

  it("creates retest/manual intents and cancels an intent", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: intentFixture }, 201))
    await expect(
      createIntent("2001", { case_version_id: "7004", purpose: "retest" }),
    ).resolves.toEqual(intentFixture)

    const [createUrl, createInit] = fetchMock.mock.calls[0]
    expect(createUrl).toBe("http://localhost:13333/api/versions/2001/execution/intents")
    expect(createInit?.method).toBe("POST")
    expect(readBody(createInit)).toEqual({ case_version_id: "7004", purpose: "retest" })
    expect(headersOf(createInit).get("Idempotency-Key")).toMatch(/^intent-create-/)

    fetchMock.mockResolvedValueOnce(
      jsonResponse({ data: { ...intentFixture, status: "cancelled" } }),
    )
    await cancelIntent("2001", "9101")
    expect(fetchMock.mock.calls[1][0]).toBe(
      "http://localhost:13333/api/versions/2001/execution/intents/9101/cancel",
    )
    expect(headersOf(fetchMock.mock.calls[1][1]).get("Idempotency-Key")).toMatch(/^intent-cancel-/)

    fetchMock.mockResolvedValueOnce(listResponse([intentFixture]))
    await expect(listIntents("2001", { status: "done" })).resolves.toEqual([intentFixture])
    expect(fetchMock.mock.calls[2][0]).toBe(
      "http://localhost:13333/api/versions/2001/execution/intents?status=done&limit=20",
    )
  })

  it("surfaces the API error envelope as ApiError", async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(409, "state_conflict", "该波次已暂停"))
    await expect(pauseWave("2001", "9202", { mode: "graceful" })).rejects.toMatchObject({
      code: "state_conflict",
      status: 409,
      message: "该波次已暂停",
      requestId: "req_execution",
    })

    fetchMock.mockResolvedValueOnce(errorResponse(503, "executor_unavailable", "执行器离线"))
    await expect(getOverview("2001")).rejects.toBeInstanceOf(ApiError)
  })
})

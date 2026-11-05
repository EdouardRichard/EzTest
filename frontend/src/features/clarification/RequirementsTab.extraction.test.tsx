import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const navigation = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  refresh: vi.fn(),
  back: vi.fn(),
  forward: vi.fn(),
  prefetch: vi.fn(),
}))
vi.mock("next/navigation", () => ({ useRouter: () => navigation }))

// The extraction card must work through the real api.ts/api-client fetch path,
// not a module mock: every request below goes through `global.fetch`.
vi.mock("./useVersionEvents", () => ({ useVersionEvents: () => "live" }))

import { RequirementsTab } from "./RequirementsTab"
import type { RequirementExtractionProgress } from "./types"

type RouteContext = {
  method: string
  path: string
  search: URLSearchParams
  body: unknown
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse(
    { error: { code, message, request_id: "req_extraction", retryable: false, details: {} } },
    status,
  )
}

function dataList<T>(items: T[]): Response {
  return jsonResponse({ data: items, next_cursor: "", has_more: false })
}

function readBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string" || init.body.length === 0) return undefined
  return JSON.parse(init.body) as unknown
}

function extractionFixture(
  overrides: Partial<RequirementExtractionProgress> = {},
): RequirementExtractionProgress {
  return {
    id: "9001",
    version_id: "2001",
    attachment_id: "8001",
    input_sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    status: "done",
    total_segments: 2,
    done_segments: 2,
    failed_segments: 0,
    expected: 58,
    extracted: 58,
    missing: 1,
    missing_ids: ["FR-003"],
    low_confidence: 2,
    skipped_images: 1,
    planned_count: 32,
    planned_ids: ["FR-059", "FR-060"],
    coverage: 1,
    created_at: "2026-11-05T06:00:00Z",
    updated_at: "2026-11-05T06:10:00Z",
    segments: [
      {
        id: "1",
        run_id: "9001",
        seq: 1,
        heading_path: "当前需求",
        char_start: 0,
        char_end: 10,
        text_sha256: "a",
        status: "done",
        attempts: 1,
        extracted_count: 40,
      },
      {
        id: "2",
        run_id: "9001",
        seq: 2,
        heading_path: "规划需求",
        char_start: 11,
        char_end: 20,
        text_sha256: "b",
        status: "done",
        attempts: 1,
        extracted_count: 18,
      },
    ],
    ...overrides,
  }
}

type ExtractionMock = {
  current: RequirementExtractionProgress | null
  routes: RouteContext[]
  pauseError?: Response
  resumeError?: Response
  retryError?: Response
}

const fetchMock = vi.fn<typeof fetch>()
const extraction = {} as ExtractionMock

function installFetchMock(): void {
  extraction.current = null
  extraction.routes = []
  extraction.pauseError = undefined
  extraction.resumeError = undefined
  extraction.retryError = undefined
  fetchMock.mockReset()
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input))
    const method = (init?.method ?? "GET").toUpperCase()
    const route: RouteContext = {
      method,
      path: url.pathname,
      search: url.searchParams,
      body: readBody(init),
    }
    extraction.routes.push(route)

    if (method === "GET" && url.pathname === "/api/versions/2001") {
      return jsonResponse({ data: { id: "2001", title: "退款系统", bindings: [] } })
    }
    if (method === "GET" && url.pathname === "/api/versions/2001/conversations") {
      return dataList([])
    }
    if (method === "GET" && url.pathname === "/api/versions/2001/requirements") {
      return dataList([])
    }
    if (method === "GET" && url.pathname === "/api/versions/2001/requirements/extractions") {
      return extraction.current === null
        ? errorResponse(404, "not_found", "requirement extraction run not found")
        : jsonResponse({ data: extraction.current })
    }
    if (method === "POST" && url.pathname === "/api/versions/2001/requirements/extractions") {
      extraction.current = extractionFixture({ status: "pending", total_segments: 0, expected: 58 })
      return jsonResponse({ data: { runs: [extraction.current] } })
    }
    if (method === "POST" && url.pathname.endsWith("/pause")) {
      if (extraction.pauseError !== undefined) return extraction.pauseError
      extraction.current = extractionFixture({ status: "paused" })
      return jsonResponse({ data: extraction.current })
    }
    if (method === "POST" && url.pathname.endsWith("/resume")) {
      if (extraction.resumeError !== undefined) return extraction.resumeError
      extraction.current = extractionFixture({ status: "running" })
      return jsonResponse({ data: extraction.current })
    }
    if (method === "POST" && url.pathname.endsWith("/retry")) {
      if (extraction.retryError !== undefined) return extraction.retryError
      extraction.current = extractionFixture({ status: "pending" })
      return jsonResponse({ data: extraction.current })
    }
    throw new Error(`unexpected request ${method} ${url.pathname}${url.search}`)
  })
}

async function renderWorkspace(): Promise<void> {
  render(<RequirementsTab versionId="2001" />)
  expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
}

describe("RequirementsTab extraction progress", () => {
  beforeEach(() => {
    installFetchMock()
    navigation.replace.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("renders the progress card and separates missing current ids from V2 planned ids", async () => {
    extraction.current = extractionFixture()
    await renderWorkspace()

    const card = await screen.findByTestId("extraction-progress-card")
    expect(card).toHaveTextContent("已完成")
    expect(screen.getByTestId("extraction-segments")).toHaveTextContent("2/2")
    expect(screen.getByTestId("extraction-counts")).toHaveTextContent("58/58")
    expect(screen.getByTestId("extraction-coverage")).toHaveTextContent("100%")
    expect(screen.getByTestId("extraction-metrics")).toHaveTextContent("缺失 1")
    expect(screen.getByTestId("extraction-metrics")).toHaveTextContent("规划 32")
    expect(screen.getByTestId("extraction-metrics")).toHaveTextContent("低置信 2")
    expect(screen.getByTestId("extraction-metrics")).toHaveTextContent("跳过图片 1")

    const reconciliation = screen.getByTestId("extraction-reconciliation")
    const missing = within(reconciliation).getByTestId("extraction-missing")
    const planned = within(reconciliation).getByTestId("extraction-planned")
    expect(missing).toHaveTextContent("FR-003")
    expect(planned).toHaveTextContent("FR-059")
    expect(planned).toHaveTextContent("FR-060")
    // A V2 planned id must never leak into the missing current ids.
    expect(missing).not.toHaveTextContent("FR-059")
    expect(missing).not.toHaveTextContent("FR-060")
    expect(planned).not.toHaveTextContent("FR-003")

    expect(
      extraction.routes.some(
        (route) =>
          route.method === "GET" && route.path === "/api/versions/2001/requirements/extractions",
      ),
    ).toBe(true)
  })

  it("shows the empty state and starts extraction from it", async () => {
    await renderWorkspace()

    expect(await screen.findByTestId("extraction-empty")).toBeInTheDocument()
    expect(screen.getByTestId("extraction-empty")).toHaveTextContent("尚未开始抽取")

    fireEvent.click(screen.getByRole("button", { name: "开始抽取" }))

    await waitFor(() =>
      expect(
        extraction.routes.some(
          (route) =>
            route.method === "POST" && route.path === "/api/versions/2001/requirements/extractions",
        ),
      ).toBe(true),
    )
    const start = extraction.routes.find(
      (route) =>
        route.method === "POST" && route.path === "/api/versions/2001/requirements/extractions",
    )
    expect(start?.body).toEqual({})

    await waitFor(() => expect(screen.getByTestId("extraction-progress-card")).toBeInTheDocument())
    expect(screen.getByTestId("extraction-status")).toHaveTextContent("待开始")
  })

  it("pauses a running run and resumes a paused run", async () => {
    extraction.current = extractionFixture({ status: "running", done_segments: 1 })
    await renderWorkspace()

    await screen.findByTestId("extraction-progress-card")
    fireEvent.click(screen.getByRole("button", { name: "暂停" }))
    await waitFor(() => expect(screen.getByTestId("extraction-status")).toHaveTextContent("已暂停"))
    expect(screen.getByRole("button", { name: "继续" })).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "继续" }))
    await waitFor(() => expect(screen.getByTestId("extraction-status")).toHaveTextContent("解析中"))
  })

  it("renders typed action errors returned by the API", async () => {
    extraction.current = extractionFixture({ status: "running" })
    extraction.pauseError = errorResponse(
      409,
      "state_conflict",
      "requirement extraction run state conflict",
    )
    await renderWorkspace()

    fireEvent.click(await screen.findByRole("button", { name: "暂停" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("requirement extraction run state conflict")
  })

  it("retries the failed run and one failed segment", async () => {
    extraction.current = extractionFixture({
      status: "failed",
      failed_segments: 1,
      done_segments: 1,
      segments: [
        extractionFixture().segments[0],
        {
          ...extractionFixture().segments[1],
          status: "failed",
          attempts: 3,
          error: "model extraction failed",
        },
      ],
    })
    await renderWorkspace()

    fireEvent.click(await screen.findByRole("button", { name: "重试" }))
    await waitFor(() =>
      expect(
        extraction.routes.some((route) => route.method === "POST" && route.path.endsWith("/retry")),
      ).toBe(true),
    )

    // Reset the failed state to exercise the per-segment retry button.
    extraction.current = extractionFixture({
      status: "failed",
      failed_segments: 1,
      segments: [
        extractionFixture().segments[0],
        {
          ...extractionFixture().segments[1],
          status: "failed",
          attempts: 3,
          error: "model extraction failed",
        },
      ],
    })
    cleanup()
    render(<RequirementsTab versionId="2001" />)
    expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
    fireEvent.click(await screen.findByRole("button", { name: "重试第 2 段" }))
    await waitFor(() => {
      const segmentRetry = extraction.routes.find(
        (route) =>
          route.method === "POST" &&
          route.path.endsWith("/retry") &&
          (route.body as { segment_seq?: number } | undefined)?.segment_seq === 2,
      )
      expect(segmentRetry).toBeDefined()
    })
  })
})

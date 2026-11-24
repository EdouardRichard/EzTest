import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { type AuthUser, clearSession, setSession } from "@/lib/auth-storage"

import { SessionFixturePanel } from "./SessionFixturePanel"
import type { SessionFixture } from "./session-fixtures"

const ADMIN: AuthUser = {
  id: "1",
  username: "admin",
  display_name: "管理员",
  role: "admin",
  status: "active",
}

const USER: AuthUser = { ...ADMIN, id: "2", username: "user", role: "user" }

const fixture: SessionFixture = {
  id: "9007199254740993",
  version_id: "100",
  sut_id: "9001",
  environment_id: "7001",
  role: "admin",
  storage_state_ref: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  created_at: "2026-11-19T10:00:00Z",
  created_by: "42",
}

function listResponse(data: unknown[]): Response {
  return new Response(JSON.stringify({ data, next_cursor: "", has_more: false }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}

function pageResponse(data: unknown[], nextCursor: string, hasMore: boolean): Response {
  return new Response(JSON.stringify({ data, next_cursor: nextCursor, has_more: hasMore }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

describe("SessionFixturePanel", () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
    clearSession()
  })

  afterEach(() => {
    cleanup()
    clearSession()
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
  })

  it("lists the version's fixtures for an admin (metadata only)", async () => {
    setSession("ezt_admin", ADMIN)
    fetchMock.mockResolvedValue(listResponse([fixture]))
    render(<SessionFixturePanel versionId="100" />)

    await waitFor(() => {
      expect(screen.getByTestId(`session-fixture-${fixture.id}`)).toBeInTheDocument()
    })
    const row = screen.getByTestId(`session-fixture-${fixture.id}`)
    expect(row).toHaveTextContent("admin")
    expect(row).toHaveTextContent("9001")
    expect(row).toHaveTextContent("7001")
    // The full state bytes are never fetched; only the short content reference shows.
    expect(row).toHaveTextContent("aaaaaaaaaaaa…")
    expect(row).not.toHaveTextContent(fixture.storage_state_ref)
    const url = String(fetchMock.mock.calls[0]?.[0])
    expect(url).toContain("/api/versions/100/session-fixtures")
  })

  it("renders nothing and fetches nothing for a non-admin", async () => {
    setSession("ezt_user", USER)
    fetchMock.mockResolvedValue(listResponse([fixture]))
    const { container } = render(<SessionFixturePanel versionId="100" />)
    expect(container).toBeEmptyDOMElement()
    await waitFor(() => {
      expect(fetchMock).not.toHaveBeenCalled()
    })
  })

  it("shows the empty state when no fixture is saved", async () => {
    setSession("ezt_admin", ADMIN)
    fetchMock.mockResolvedValue(listResponse([]))
    render(<SessionFixturePanel versionId="100" />)
    await waitFor(() => {
      expect(screen.getByTestId("session-fixture-empty")).toBeInTheDocument()
    })
  })

  it("surfaces a load failure observably", async () => {
    setSession("ezt_admin", ADMIN)
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "unavailable",
            message: "session fixture listing is not configured",
            request_id: "req_fixture",
            retryable: true,
            details: {},
          },
        }),
        { status: 503, headers: { "Content-Type": "application/json" } },
      ),
    )
    render(<SessionFixturePanel versionId="100" />)
    await waitFor(() => {
      expect(screen.getByTestId("session-fixture-error")).toHaveTextContent(
        "session fixture listing is not configured",
      )
    })
  })

  it("ignores an out-of-order earlier response after the version switches", async () => {
    setSession("ezt_admin", ADMIN)
    // Two deferred responses: the version-100 request resolves LAST (stale),
    // the version-200 request resolves FIRST (current). The requestId guard must
    // drop the late version-100 response so it cannot overwrite version 200.
    let resolveOld: (r: Response) => void = () => {}
    let resolveNew: (r: Response) => void = () => {}
    const oldResponse = new Promise<Response>((res) => {
      resolveOld = res
    })
    const newResponse = new Promise<Response>((res) => {
      resolveNew = res
    })
    fetchMock.mockReturnValueOnce(oldResponse).mockReturnValueOnce(newResponse)

    const { rerender } = render(<SessionFixturePanel versionId="100" />)
    // Switch version: invalidates the in-flight version-100 request, starts 200.
    rerender(<SessionFixturePanel versionId="200" />)

    resolveNew(listResponse([{ ...fixture, id: "2", role: "current" }]))
    await waitFor(() => {
      expect(screen.getByTestId("session-fixture-2")).toBeInTheDocument()
    })
    // The stale version-100 response arrives late — it must be ignored.
    resolveOld(listResponse([{ ...fixture, id: "1", role: "stale" }]))
    await new Promise((res) => setTimeout(res, 0))
    expect(screen.queryByTestId("session-fixture-1")).not.toBeInTheDocument()
    expect(screen.getByTestId("session-fixture-2")).toBeInTheDocument()
  })

  it("discards out-of-order earlier responses across two MANUAL refresh clicks", async () => {
    setSession("ezt_admin", ADMIN)
    // Three deferred responses: mount, refresh click #1, refresh click #2. The
    // latest click resolves FIRST; the two earlier requests resolve LAST and must
    // both be dropped. This drives the manual-refresh race specifically (the
    // version-switch test above cannot catch a guard that only bumps on version
    // change): if load() stopped bumping requestId, the late earlier responses
    // would overwrite the latest result.
    const deferred: Array<(r: Response) => void> = []
    fetchMock.mockImplementation(
      () =>
        new Promise<Response>((res) => {
          deferred.push(res)
        }),
    )
    render(<SessionFixturePanel versionId="100" />)
    const refresh = screen.getByTestId("session-fixture-refresh")
    fireEvent.click(refresh)
    fireEvent.click(refresh)
    await waitFor(() => {
      expect(deferred.length).toBe(3)
    })

    deferred[2](listResponse([{ ...fixture, id: "3", role: "latest" }]))
    await waitFor(() => {
      expect(screen.getByTestId("session-fixture-3")).toBeInTheDocument()
    })
    // The earlier refresh and the mount request arrive late — both ignored.
    deferred[1](listResponse([{ ...fixture, id: "2", role: "middle" }]))
    deferred[0](listResponse([{ ...fixture, id: "1", role: "mount" }]))
    await new Promise((res) => setTimeout(res, 0))
    expect(screen.queryByTestId("session-fixture-2")).not.toBeInTheDocument()
    expect(screen.queryByTestId("session-fixture-1")).not.toBeInTheDocument()
    expect(screen.getByTestId("session-fixture-3")).toBeInTheDocument()
  })

  it("walks further pages via the honest has_more cursor (complete admin coverage)", async () => {
    setSession("ezt_admin", ADMIN)
    fetchMock
      .mockResolvedValueOnce(pageResponse([{ ...fixture, id: "1" }], "1", true))
      .mockResolvedValueOnce(pageResponse([{ ...fixture, id: "2" }], "", false))
    render(<SessionFixturePanel versionId="100" />)

    await waitFor(() => {
      expect(screen.getByTestId("session-fixture-1")).toBeInTheDocument()
    })
    // has_more=true surfaces a load-more affordance (coverage is not silently
    // capped to the first page).
    fireEvent.click(screen.getByTestId("session-fixture-load-more"))
    await waitFor(() => {
      expect(screen.getByTestId("session-fixture-2")).toBeInTheDocument()
    })
    // Both pages are present (appended, not replaced); the cursor was forwarded.
    expect(screen.getByTestId("session-fixture-1")).toBeInTheDocument()
    const secondUrl = String(fetchMock.mock.calls[1]?.[0])
    expect(secondUrl).toContain("before=1")
    expect(secondUrl).toContain("limit=100")
    // The last page reports has_more=false: no further load-more affordance.
    expect(screen.queryByTestId("session-fixture-load-more")).not.toBeInTheDocument()
  })
})

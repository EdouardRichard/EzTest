import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { Version } from "@/features/versions/types"

const mocks = vi.hoisted(() => ({
  listVersions: vi.fn(),
  getVersion: vi.fn(),
}))

const nav = vi.hoisted(() => ({
  pathname: "/versions/11",
  router: { push: vi.fn() },
}))

vi.mock("@/features/versions/api", () => ({
  listVersions: mocks.listVersions,
  getVersion: mocks.getVersion,
}))

vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
  useRouter: () => nav.router,
}))

import { VersionSwitcher } from "./VersionSwitcher"

const RECENT_KEY = "eztest.recentVersions"

function version(overrides: Partial<Version> & Pick<Version, "id" | "name">): Version {
  return {
    label: "",
    description: "",
    status: "reviewing",
    systemId: null,
    environmentId: null,
    codeRepositoryId: null,
    codeBranch: null,
    codeBaselineCommit: null,
    bindings: { system: null, environment: null, codeRepository: null },
    requirementCount: 0,
    clarifiedCount: 0,
    openQuestionCount: 0,
    revision: 1,
    createdBy: 1,
    updatedBy: 1,
    createdAt: "2026-10-29T00:00:00Z",
    updatedAt: "2026-10-29T00:00:00Z",
    ...overrides,
  }
}

const VERSION_A = version({ id: "11", name: "版本甲", status: "reviewing" })
const VERSION_B = version({ id: "12", name: "版本乙", status: "executing" })
const VERSION_C = version({ id: "13", name: "版本丙", status: "draft" })

function page(items: Version[]) {
  return { items, nextCursor: "", hasMore: false }
}

function recentEntries(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    id: String(100 + index),
    name: `最近版本 ${index + 1}`,
    status: "executing",
  }))
}

function openDropdown() {
  fireEvent.click(screen.getByRole("button", { name: "切换版本" }))
  return screen.getByRole("dialog", { name: "版本切换" })
}

beforeEach(() => {
  nav.pathname = "/versions/11"
  nav.router.push.mockReset()
  mocks.listVersions.mockReset()
  mocks.getVersion.mockReset()
  mocks.getVersion.mockResolvedValue(VERSION_A)
  mocks.listVersions.mockResolvedValue(page([]))
  window.history.replaceState({}, "", "/versions/11")
  localStorage.clear()
})

afterEach(() => {
  cleanup()
  localStorage.clear()
})

describe("VersionSwitcher", () => {
  it("shows the current version name and its status badge", async () => {
    render(<VersionSwitcher />)

    await screen.findByText("版本甲")
    expect(screen.getByText("评审中")).not.toBeNull()
  })

  it("lists at most eight recent versions from localStorage", async () => {
    localStorage.setItem(RECENT_KEY, JSON.stringify(recentEntries(10)))
    mocks.getVersion.mockResolvedValue(VERSION_A)
    render(<VersionSwitcher />)
    await screen.findByText("版本甲")

    const dialog = openDropdown()
    const recent = within(dialog).getAllByTestId("recent-version")
    expect(recent).toHaveLength(8)
    expect(recent[0]?.textContent).toContain("最近版本 1")
  })

  it("filters the loaded versions by the search box", async () => {
    mocks.listVersions.mockResolvedValue(page([VERSION_A, VERSION_B, VERSION_C]))
    render(<VersionSwitcher />)
    await screen.findByText("版本甲")
    openDropdown()

    await within(screen.getByRole("dialog")).findByText("版本乙")
    fireEvent.change(screen.getByLabelText("搜索版本"), { target: { value: "乙" } })

    const dialog = screen.getByRole("dialog")
    expect(within(dialog).getByText("版本乙")).not.toBeNull()
    expect(within(dialog).queryByText("版本甲")).toBeNull()
    expect(within(dialog).queryByText("版本丙")).toBeNull()
  })

  it("switches version while preserving the tab and batch query", async () => {
    window.history.replaceState({}, "", "/versions/11?tab=results&batch=77")
    mocks.getVersion.mockResolvedValue(VERSION_A)
    mocks.listVersions.mockResolvedValue(page([VERSION_B]))
    render(<VersionSwitcher />)
    await screen.findByText("版本甲")
    const dialog = openDropdown()

    fireEvent.click(await within(dialog).findByTestId("version-option-12"))

    expect(nav.router.push).toHaveBeenCalledWith("/versions/12?tab=results&batch=77")
    const stored = JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]") as { id: string }[]
    expect(stored[0]?.id).toBe("12")
  })

  it("shows the empty state when no version exists", async () => {
    nav.pathname = "/versions"
    mocks.getVersion.mockRejectedValue(new Error("not found"))
    render(<VersionSwitcher />)
    await screen.findByRole("button", { name: "切换版本" })

    const dialog = openDropdown()
    expect(await within(dialog).findByText("暂无版本")).not.toBeNull()
  })

  it("shows a retry that recovers from a list failure", async () => {
    mocks.listVersions
      .mockRejectedValueOnce(new Error("network down"))
      .mockResolvedValueOnce(page([VERSION_B]))
    render(<VersionSwitcher />)
    await screen.findByText("版本甲")
    const dialog = openDropdown()

    expect(await within(dialog).findByText(/加载失败/)).not.toBeNull()
    fireEvent.click(within(dialog).getByRole("button", { name: "重试" }))
    await waitFor(() => expect(within(dialog).getByText("版本乙")).not.toBeNull())
  })
})

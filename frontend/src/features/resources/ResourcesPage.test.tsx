import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const navigation = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  refresh: vi.fn(),
  back: vi.fn(),
  forward: vi.fn(),
  prefetch: vi.fn(),
}))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
}))

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    listSuts: vi.fn(),
    getSut: vi.fn(),
    listRepositories: vi.fn(),
    createSutWithDefaultEnvironment: vi.fn(),
    createEnvironment: vi.fn(),
    updateEnvironment: vi.fn(),
    deleteEnvironment: vi.fn(),
    updateSut: vi.fn(),
    deleteSut: vi.fn(),
    createPage: vi.fn(),
    updatePage: vi.fn(),
    deletePage: vi.fn(),
    importPages: vi.fn(),
    createApi: vi.fn(),
    updateApi: vi.fn(),
    deleteApi: vi.fn(),
    importApis: vi.fn(),
    createDatabase: vi.fn(),
    updateDatabase: vi.fn(),
    deleteDatabase: vi.fn(),
    createAccount: vi.fn(),
    updateAccount: vi.fn(),
    deleteAccount: vi.fn(),
    createRepository: vi.fn(),
    updateRepository: vi.fn(),
    deleteRepository: vi.fn(),
  }
})

import * as api from "./api"
import { parseResourceTab, ResourcesPage } from "./ResourcesPage"
import {
  environmentFixture,
  secondEnvironmentFixture,
  secondSutFixture,
  sutFixture,
  sutListItemFixture,
} from "./test-fixtures"

function jsonFixtureDetail() {
  return {
    ...sutFixture,
    environments: [
      { ...secondEnvironmentFixture, id: "1110", is_default: false },
      { ...environmentFixture, id: "1111", is_default: true },
    ],
  }
}

describe("parseResourceTab", () => {
  it("normalizes unknown and array query values", () => {
    expect(parseResourceTab("pages")).toBe("pages")
    expect(parseResourceTab(["data", "repos"])).toBe("data")
    expect(parseResourceTab("nope")).toBe("suts")
    expect(parseResourceTab(undefined)).toBe("suts")
  })
})

describe("ResourcesPage", () => {
  beforeEach(() => {
    vi.mocked(api.listSuts).mockReset()
    vi.mocked(api.getSut).mockReset()
    vi.mocked(api.listRepositories).mockReset()
    vi.mocked(api.listSuts).mockResolvedValue([sutListItemFixture, secondSutFixture])
    vi.mocked(api.getSut).mockResolvedValue(sutFixture)
    vi.mocked(api.listRepositories).mockResolvedValue([])
    navigation.replace.mockReset()
  })

  afterEach(cleanup)

  it("opens the tab from ?tab= and keeps the URL in sync when switching", async () => {
    render(<ResourcesPage initialTab="pages" />)

    expect(await screen.findByRole("tab", { name: "页面与 API" })).toHaveAttribute(
      "aria-selected",
      "true",
    )
    expect(await screen.findByRole("table", { name: "页面列表" })).toBeInTheDocument()

    fireEvent.click(screen.getByRole("tab", { name: "代码仓库" }))
    expect(navigation.replace).toHaveBeenCalledWith("/resources?tab=repos")
    expect(await screen.findByRole("button", { name: "新建代码仓" })).toBeInTheDocument()
  })

  it("defaults to the first SUT and its default environment", async () => {
    render(<ResourcesPage initialTab="data" />)

    expect(await screen.findByLabelText("SUT")).toHaveValue("1001")
    expect(await screen.findByLabelText("环境")).toHaveValue("1101")
    expect(screen.getByRole("table", { name: "目标库列表" })).toBeInTheDocument()
  })

  it("prefers the environment flagged is_default when none is selected yet", async () => {
    vi.mocked(api.getSut).mockResolvedValue(jsonFixtureDetail())
    render(<ResourcesPage initialTab="pages" />)

    expect(await screen.findByLabelText("环境")).toHaveValue("1111")
  })

  it("loads another SUT aggregate when the scope selector changes", async () => {
    vi.mocked(api.getSut).mockImplementation(async (id: string) =>
      id === "1002" ? secondSutFixture : sutFixture,
    )
    render(<ResourcesPage initialTab="pages" />)
    await screen.findByRole("table", { name: "页面列表" })

    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "1002" } })

    await waitFor(() => expect(api.getSut).toHaveBeenCalledWith("1002"))
  })

  it("shows the empty SUT state and retries a failed list load", async () => {
    vi.mocked(api.listSuts).mockResolvedValueOnce([])
    const { unmount } = render(<ResourcesPage initialTab="suts" />)
    expect(await screen.findByText("还没有 SUT")).toBeInTheDocument()
    unmount()

    vi.mocked(api.listSuts)
      .mockRejectedValueOnce(
        new ApiError({
          status: 503,
          code: "unavailable",
          message: "资源服务暂不可用",
          retryable: true,
        }),
      )
      .mockResolvedValueOnce([sutListItemFixture])
    render(<ResourcesPage initialTab="suts" />)
    expect(await screen.findByText("资源服务暂不可用")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByRole("table", { name: "SUT 列表" })).toBeInTheDocument()
    expect(api.listSuts).toHaveBeenCalledTimes(3)
  })
})

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { versionSummaryFixture } from "@/features/versions/test-fixtures"
import { type AuthUser, clearSession, setSession } from "@/lib/auth-storage"

const api = vi.hoisted(() => ({
  createPage: vi.fn(),
  updatePage: vi.fn(),
  deletePage: vi.fn(),
  importPages: vi.fn(),
  createApi: vi.fn(),
  updateApi: vi.fn(),
  deleteApi: vi.fn(),
  importApis: vi.fn(),
  generatePageApiProposals: vi.fn(),
  listPageApiProposals: vi.fn(),
  confirmPageProposal: vi.fn(),
  rejectPageProposal: vi.fn(),
  confirmApiProposal: vi.fn(),
  rejectApiProposal: vi.fn(),
}))
vi.mock("./api", () => api)

const versionsApi = vi.hoisted(() => ({ listVersions: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

import { PageAPIPanel, type PageAPIPanelProps } from "./PageAPIPanel"
import {
  apiFixture,
  environmentFixture,
  pageFixture,
  sutFixture,
  sutListItemFixture,
} from "./test-fixtures"
import type { CodeProposal, ResourceScope, Sut, SutApi, SutPage } from "./types"

const ADMIN: AuthUser = {
  id: "9001",
  username: "admin",
  display_name: "管理员",
  role: "admin",
  status: "active",
}

const proposedPage: SutPage = {
  ...pageFixture,
  id: "1311",
  name: "退款结果页",
  url: "https://order.test/refund",
  route_template: "/refund",
  status: "proposed",
  source: "ai",
}

const proposedApi: SutApi = {
  ...apiFixture,
  id: "1411",
  name: "申请退款",
  method: "POST",
  path_template: "/api/refund",
  status: "proposed",
  source: "code",
}

const detail: Sut = {
  ...sutFixture,
  environments: [
    {
      ...environmentFixture,
      pages: [pageFixture, proposedPage],
      apis: [apiFixture, proposedApi],
    },
  ],
}

const baseScope: ResourceScope = {
  suts: [sutListItemFixture],
  sutId: "1001",
  environmentId: "1101",
  detail,
  loading: false,
  detailLoading: false,
  error: null,
  detailError: null,
}

const proposals: CodeProposal[] = [
  {
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
  },
  {
    kind: "api",
    id: "1411",
    name: "申请退款",
    url: null,
    route_template: null,
    method: "POST",
    path_template: "/api/refund",
    status: "proposed",
    source: "code",
    evidence: [],
    created_at: "2026-11-01T10:00:00Z",
  },
]

function renderPanel(overrides: Partial<PageAPIPanelProps> = {}) {
  const onRefresh = vi.fn().mockResolvedValue(undefined)
  const props: PageAPIPanelProps = {
    scope: baseScope,
    onSelectSut: vi.fn(),
    onSelectEnvironment: vi.fn(),
    onRefresh,
    onRetry: vi.fn(),
    ...overrides,
  }
  render(<PageAPIPanel {...props} />)
  return { onRefresh }
}

describe("PageAPIPanel proposals", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    for (const fn of Object.values(api)) fn.mockReset()
    versionsApi.listVersions.mockReset()
    versionsApi.listVersions.mockResolvedValue([versionSummaryFixture])
    api.listPageApiProposals.mockResolvedValue(proposals)
    api.generatePageApiProposals.mockResolvedValue({
      pages: { created: 2, skipped: 1, ids: ["1311", "1312"] },
      apis: { created: 1, skipped: 2, ids: ["1411"] },
    })
    api.confirmPageProposal.mockResolvedValue({ ...proposedPage, status: "approved" })
    api.rejectPageProposal.mockResolvedValue({ ...proposedPage, status: "rejected" })
    api.confirmApiProposal.mockResolvedValue({ ...proposedApi, status: "approved" })
    api.rejectApiProposal.mockResolvedValue({ ...proposedApi, status: "rejected" })
  })

  afterEach(() => {
    cleanup()
    clearSession()
  })

  it("shows proposed rows and lets an admin confirm/reject them", async () => {
    setSession("ezt_admin", ADMIN)
    const { onRefresh } = renderPanel()

    expect(screen.getAllByText("待确认").length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText("/refund")).toBeInTheDocument()
    expect(screen.getByText("申请退款")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "确认页面 退款结果页" }))
    await waitFor(() => expect(api.confirmPageProposal).toHaveBeenCalledWith("1311"))
    await waitFor(() => expect(onRefresh).toHaveBeenCalled())

    fireEvent.click(screen.getByRole("button", { name: "驳回 API 申请退款" }))
    await waitFor(() => expect(api.rejectApiProposal).toHaveBeenCalledWith("1411"))
  })

  it("hides proposal decisions from a normal user but keeps the rows visible", () => {
    renderPanel()

    expect(screen.getByText("退款结果页")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "确认页面 退款结果页" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "驳回 API 申请退款" })).not.toBeInTheDocument()
  })

  it("filters the two tables by status", () => {
    renderPanel()

    fireEvent.change(screen.getByLabelText("状态"), { target: { value: "proposed" } })
    expect(screen.getByText("退款结果页")).toBeInTheDocument()
    expect(screen.getByText("申请退款")).toBeInTheDocument()
    expect(screen.queryByText("订单列表")).not.toBeInTheDocument()
    expect(screen.queryByText("查询订单")).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("状态"), { target: { value: "all" } })
    expect(screen.getByText("订单列表")).toBeInTheDocument()
  })

  it("generates code proposals for the selected version and reports created/skipped", async () => {
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "展开代码提议" }))
    await waitFor(() => expect(versionsApi.listVersions).toHaveBeenCalled())
    await waitFor(() =>
      expect(api.listPageApiProposals).toHaveBeenCalledWith(versionSummaryFixture.id),
    )

    expect(await screen.findByText("代码事实来源")).toBeInTheDocument()
    expect(screen.getByText("src/pages/RefundPage.tsx")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "从代码生成提议" }))
    await waitFor(() =>
      expect(api.generatePageApiProposals).toHaveBeenCalledWith(versionSummaryFixture.id),
    )
    expect(await screen.findByRole("status")).toHaveTextContent(
      "已创建 2 个页面、1 个 API，跳过 3 个。",
    )
    expect(onRefresh).toHaveBeenCalled()
  })

  it("shows inline 422 errors from generation", async () => {
    api.generatePageApiProposals.mockRejectedValue(new Error("no bound repository"))
    renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "展开代码提议" }))
    await waitFor(() => expect(api.listPageApiProposals).toHaveBeenCalled())
    fireEvent.click(screen.getByRole("button", { name: "从代码生成提议" }))

    expect(await screen.findByText("no bound repository")).toBeInTheDocument()
  })
})

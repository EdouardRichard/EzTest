import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type { VersionTab } from "./types"

const api = vi.hoisted(() => ({
  listVersions: vi.fn(),
  getVersion: vi.fn(),
  createVersion: vi.fn(),
  getResourceCatalog: vi.fn(),
}))
vi.mock("./api", () => api)

vi.mock("@/features/clarification/RequirementsTab", async () => {
  const React = await import("react")
  return {
    RequirementsTab: (props: {
      versionId: string
      initialConversationId?: string
      initialRequirementId?: string
    }) =>
      React.createElement("div", {
        "data-testid": "clarification-workspace",
        "data-version-id": props.versionId,
        "data-conversation-id": props.initialConversationId,
        "data-requirement-id": props.initialRequirementId,
      }),
  }
})

vi.mock("@/features/design/DesignTab", async () => {
  const React = await import("react")
  return {
    DesignTab: (props: {
      versionId: string
      initialCaseId?: string
      initialConversationId?: string
    }) =>
      React.createElement("div", {
        "data-testid": "design-workspace",
        "data-version-id": props.versionId,
        "data-case-id": props.initialCaseId,
        "data-conversation-id": props.initialConversationId,
      }),
  }
})

vi.mock("@/features/execution/ExecutionTab", async () => {
  const React = await import("react")
  return {
    ExecutionTab: (props: {
      versionId: string
      initialWaveId?: string
      initialCaseRunId?: string
    }) =>
      React.createElement("div", {
        "data-testid": "execution-workspace",
        "data-version-id": props.versionId,
        "data-wave-id": props.initialWaveId,
        "data-case-run-id": props.initialCaseRunId,
      }),
  }
})

vi.mock("@/features/delivery/IssuesTab", async () => {
  const React = await import("react")
  return {
    IssuesTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "issues-workspace",
        "data-version-id": props.versionId,
      }),
  }
})

vi.mock("@/features/delivery/ReportsTab", async () => {
  const React = await import("react")
  return {
    ReportsTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "reports-workspace",
        "data-version-id": props.versionId,
      }),
  }
})

vi.mock("@/features/chain/ChainTab", async () => {
  const React = await import("react")
  return {
    ChainTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "chain-workspace",
        "data-version-id": props.versionId,
      }),
  }
})

const navigation = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  refresh: vi.fn(),
  back: vi.fn(),
  forward: vi.fn(),
  prefetch: vi.fn(),
}))
const search = vi.hoisted(() => ({ params: new URLSearchParams() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
  useSearchParams: () => search.params,
}))

import { versionFixture } from "./test-fixtures"
import { VersionDetailPage } from "./VersionDetailPage"

describe("VersionDetailPage", () => {
  beforeEach(() => {
    api.getVersion.mockReset()
    api.getVersion.mockResolvedValue(versionFixture)
    navigation.replace.mockReset()
    search.params = new URLSearchParams()
  })

  afterEach(cleanup)

  it("renders the header, SUT chips, all eight tabs and the binding overview", async () => {
    render(<VersionDetailPage versionId="2001" />)

    expect(
      await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" }),
    ).toBeInTheDocument()
    expect(api.getVersion).toHaveBeenCalledWith("2001")

    expect(screen.getByText("运行中")).toBeInTheDocument()
    expect(screen.getByText("订单中心")).toBeInTheDocument()
    expect(screen.getByText("支付网关")).toBeInTheDocument()

    expect(screen.getAllByRole("tab")).toHaveLength(8)
    expect(screen.getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true")

    const overview = screen.getByRole("region", { name: "绑定概览" })
    expect(overview).toHaveTextContent("测试环境")
    expect(overview).toHaveTextContent("https://order.test")
    expect(overview).toHaveTextContent("2 个页面")
    expect(overview).toHaveTextContent("1 个 API")
    expect(overview).toHaveTextContent("1 个目标库")
    expect(overview).toHaveTextContent("2 个测试账号")
    expect(overview).toHaveTextContent("order-center-web")
    expect(overview).toHaveTextContent("未绑定代码仓")
  })

  it("honours a deep-linked execution tab and mounts the execution workspace", async () => {
    render(<VersionDetailPage versionId="2001" initialTab="execution" />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    expect(screen.getByRole("tab", { name: "执行" })).toHaveAttribute("aria-selected", "true")
    const workspace = screen.getByTestId("execution-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(workspace).not.toHaveAttribute("data-wave-id")
    expect(workspace).not.toHaveAttribute("data-case-run-id")
    expect(screen.queryByText("执行将在 P3 阶段提供")).not.toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "绑定概览" })).not.toBeInTheDocument()
  })

  it("forwards ?wave= and ?case_run= deep links to the execution workspace", async () => {
    search.params = new URLSearchParams("tab=execution&wave=9202&case_run=9302")

    render(<VersionDetailPage versionId="2001" initialTab="execution" />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    const workspace = screen.getByTestId("execution-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(workspace).toHaveAttribute("data-wave-id", "9202")
    expect(workspace).toHaveAttribute("data-case-run-id", "9302")
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("keeps execution deep links when switching to the execution tab", async () => {
    search.params = new URLSearchParams("tab=overview&wave=9202&case_run=9302")

    render(<VersionDetailPage versionId="2001" />)
    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    fireEvent.click(screen.getByRole("tab", { name: "执行" }))

    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=execution&wave=9202&case_run=9302",
    )
  })

  it("honours a deep-linked issues tab and mounts the issues workspace", async () => {
    render(<VersionDetailPage versionId="2001" initialTab="issues" />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    expect(screen.getByRole("tab", { name: "问题" })).toHaveAttribute("aria-selected", "true")
    const workspace = screen.getByTestId("issues-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(screen.queryByText("问题将在 P4 阶段提供")).not.toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "绑定概览" })).not.toBeInTheDocument()
  })

  it("keeps the ?issue= deep link when switching to the issues tab", async () => {
    search.params = new URLSearchParams("tab=overview&issue=9101")

    render(<VersionDetailPage versionId="2001" />)
    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    fireEvent.click(screen.getByRole("tab", { name: "问题" }))

    expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=issues&issue=9101")
  })

  it("honours a deep-linked reports tab and mounts the reports workspace", async () => {
    render(<VersionDetailPage versionId="2001" initialTab="reports" />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    expect(screen.getByRole("tab", { name: "报告" })).toHaveAttribute("aria-selected", "true")
    const workspace = screen.getByTestId("reports-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(screen.queryByText("报告将在 P4 阶段提供")).not.toBeInTheDocument()
  })

  it("keeps ?report= and ?revision= deep links when switching to the reports tab", async () => {
    search.params = new URLSearchParams("tab=overview&report=8101&revision=1")

    render(<VersionDetailPage versionId="2001" />)
    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    fireEvent.click(screen.getByRole("tab", { name: "报告" }))

    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=reports&report=8101&revision=1",
    )
  })

  it("honours a deep-linked chain tab and mounts the chain workspace", async () => {
    render(<VersionDetailPage versionId="2001" initialTab="chain" />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    expect(screen.getByRole("tab", { name: "探索链路" })).toHaveAttribute("aria-selected", "true")
    const workspace = screen.getByTestId("chain-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(screen.queryByText("探索链路将在 P4 阶段提供")).not.toBeInTheDocument()
  })

  it("keeps ?node/issue/report/wave deep links when switching to the chain tab", async () => {
    search.params = new URLSearchParams("tab=overview&node=issue%3A9101&wave=9201")

    render(<VersionDetailPage versionId="2001" />)
    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    fireEvent.click(screen.getByRole("tab", { name: "探索链路" }))

    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=chain&node=issue%3A9101&wave=9201",
    )
  })

  it.each([["code", "代码与变更将在 P5 阶段提供"]] as const)(
    "keeps the %s placeholder for its real phase copy",
    async (tab, copy) => {
      render(<VersionDetailPage versionId="2001" initialTab={tab as VersionTab} />)

      await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
      expect(screen.getByText(copy)).toBeInTheDocument()
    },
  )

  it("falls back to the overview tab for an unknown deep link", async () => {
    render(<VersionDetailPage versionId="2001" initialTab={"unknown" as never} />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    expect(screen.getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true")
  })

  it("switches tabs, updates the URL and mounts the design workspace", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    fireEvent.click(screen.getByRole("tab", { name: "用例设计" }))

    expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=design")
    expect(screen.getByRole("tab", { name: "用例设计" })).toHaveAttribute("aria-selected", "true")
    const workspace = screen.getByTestId("design-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(screen.queryByText("用例设计将在 P2 阶段提供")).not.toBeInTheDocument()
  })

  it("forwards the design deep links (?case= and ?conversation=) to the design tab", async () => {
    search.params = new URLSearchParams("tab=design&case=7001&conversation=4009")

    render(<VersionDetailPage versionId="2001" initialTab="design" />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    const workspace = screen.getByTestId("design-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(workspace).toHaveAttribute("data-case-id", "7001")
    expect(workspace).toHaveAttribute("data-conversation-id", "4009")
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("mounts the clarification workspace when the requirements tab is selected", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    fireEvent.click(screen.getByRole("tab", { name: "需求与澄清" }))

    expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=requirements")
    const workspace = screen.getByTestId("clarification-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(workspace).not.toHaveAttribute("data-conversation-id")
    expect(workspace).not.toHaveAttribute("data-requirement-id")
  })

  it("forwards clarification deep links from the URL to the workspace", async () => {
    render(
      <VersionDetailPage
        versionId="2001"
        initialTab="requirements"
        initialConversationId="4001"
        initialRequirementId="6001"
      />,
    )

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    expect(screen.getByRole("tab", { name: "需求与澄清" })).toHaveAttribute("aria-selected", "true")
    const workspace = screen.getByTestId("clarification-workspace")
    expect(workspace).toHaveAttribute("data-version-id", "2001")
    expect(workspace).toHaveAttribute("data-conversation-id", "4001")
    expect(workspace).toHaveAttribute("data-requirement-id", "6001")
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("keeps clarification deep links when switching back to the requirements tab", async () => {
    render(
      <VersionDetailPage
        versionId="2001"
        initialTab="overview"
        initialConversationId="4001"
        initialRequirementId="6001"
      />,
    )
    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    fireEvent.click(screen.getByRole("tab", { name: "需求与澄清" }))

    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=requirements&conversation=4001&requirement=6001",
    )
  })

  it("shows a retryable error state when the version cannot be loaded", async () => {
    api.getVersion
      .mockRejectedValueOnce(
        new ApiError({
          status: 404,
          code: "not_found",
          message: "版本不存在",
          requestId: "req_version",
        }),
      )
      .mockResolvedValueOnce(versionFixture)

    render(<VersionDetailPage versionId="2001" />)

    expect(await screen.findByText("版本不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    expect(api.getVersion).toHaveBeenCalledTimes(2)
  })
})

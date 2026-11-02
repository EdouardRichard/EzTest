import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    createPage: vi.fn(),
    updatePage: vi.fn(),
    deletePage: vi.fn(),
    importPages: vi.fn(),
    createApi: vi.fn(),
    updateApi: vi.fn(),
    deleteApi: vi.fn(),
    importApis: vi.fn(),
  }
})

import * as api from "./api"
import { PageAPIPanel, type PageAPIPanelProps } from "./PageAPIPanel"
import {
  apiFixture,
  pageFixture,
  secondSutFixture,
  sutFixture,
  sutListItemFixture,
} from "./test-fixtures"
import type { ResourceScope } from "./types"

const baseScope: ResourceScope = {
  suts: [sutListItemFixture, secondSutFixture],
  sutId: "1001",
  environmentId: "1101",
  detail: sutFixture,
  loading: false,
  detailLoading: false,
  error: null,
  detailError: null,
}

function renderPanel(overrides: Partial<PageAPIPanelProps> = {}): {
  onRefresh: ReturnType<typeof vi.fn>
  onSelectEnvironment: ReturnType<typeof vi.fn>
  onSelectSut: ReturnType<typeof vi.fn>
} {
  const onRefresh = vi.fn().mockResolvedValue(undefined)
  const onSelectEnvironment = vi.fn()
  const onSelectSut = vi.fn()
  const props: PageAPIPanelProps = {
    scope: baseScope,
    onSelectSut,
    onSelectEnvironment,
    onRefresh,
    onRetry: vi.fn(),
    ...overrides,
  }
  render(<PageAPIPanel {...props} />)
  return { onRefresh, onSelectEnvironment, onSelectSut }
}

describe("PageAPIPanel", () => {
  beforeEach(() => {
    vi.mocked(api.createPage).mockReset()
    vi.mocked(api.updatePage).mockReset()
    vi.mocked(api.deletePage).mockReset()
    vi.mocked(api.importPages).mockReset()
    vi.mocked(api.createApi).mockReset()
    vi.mocked(api.updateApi).mockReset()
    vi.mocked(api.deleteApi).mockReset()
    vi.mocked(api.importApis).mockReset()
  })

  afterEach(cleanup)

  it("shows the environment pages and APIs with the scope selectors", () => {
    const { onSelectEnvironment, onSelectSut } = renderPanel()

    expect(screen.getByLabelText("SUT")).toHaveValue("1001")
    expect(screen.getByLabelText("环境")).toHaveValue("1101")
    expect(screen.getByRole("table", { name: "页面列表" })).toBeInTheDocument()
    expect(screen.getByText("订单列表")).toBeInTheDocument()
    expect(screen.getByRole("table", { name: "API 列表" })).toBeInTheDocument()
    expect(screen.getByText("查询订单")).toBeInTheDocument()
    expect(screen.getByText("GET")).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1102" } })
    expect(onSelectEnvironment).toHaveBeenCalledWith("1102")
    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "1002" } })
    expect(onSelectSut).toHaveBeenCalledWith("1002")
  })

  it("prompts for a SUT when none exists and for an environment when none is configured", () => {
    const { unmount } = render(
      <PageAPIPanel
        scope={{ ...baseScope, suts: [], sutId: "", detail: null }}
        onSelectSut={vi.fn()}
        onSelectEnvironment={vi.fn()}
        onRefresh={vi.fn().mockResolvedValue(undefined)}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("还没有 SUT")).toBeInTheDocument()
    unmount()

    render(
      <PageAPIPanel
        scope={{
          ...baseScope,
          detail: { ...sutFixture, environments: [] },
          environmentId: "",
        }}
        onSelectSut={vi.fn()}
        onSelectEnvironment={vi.fn()}
        onRefresh={vi.fn().mockResolvedValue(undefined)}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("还没有环境")).toBeInTheDocument()
  })

  it("validates the page form and creates a page in the selected environment", async () => {
    vi.mocked(api.createPage).mockResolvedValue(pageFixture)
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "新建页面" }))
    fireEvent.click(screen.getByRole("button", { name: "创建页面" }))
    expect(await screen.findByText("请输入页面名称")).toBeInTheDocument()
    expect(api.createPage).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText("页面名称"), { target: { value: "订单详情" } })
    fireEvent.change(screen.getByLabelText("页面 URL"), {
      target: { value: "https://order.test/orders/{id}" },
    })
    fireEvent.change(screen.getByLabelText("路由模板"), {
      target: { value: "/orders/{id}" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建页面" }))

    await waitFor(() =>
      expect(api.createPage).toHaveBeenCalledWith("1101", {
        name: "订单详情",
        url: "https://order.test/orders/{id}",
        routeTemplate: "/orders/{id}",
        pageType: "page",
        authRole: "",
        module: "",
      }),
    )
    expect(onRefresh).toHaveBeenCalled()
  })

  it("imports pages from the JSON textarea and reports created/skipped counts", async () => {
    vi.mocked(api.importPages).mockResolvedValue({
      created: 1,
      skipped: 2,
      items: [pageFixture],
    })
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "导入页面" }))
    fireEvent.change(screen.getByLabelText("导入 JSON"), { target: { value: "{oops" } })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))
    expect(await screen.findByText("JSON 解析失败，请检查格式")).toBeInTheDocument()
    expect(api.importPages).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText("导入 JSON"), {
      target: {
        value: JSON.stringify([
          {
            name: "订单详情",
            url: "https://order.test/orders/{id}",
            route_template: "/orders/{id}",
          },
        ]),
      },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    await waitFor(() =>
      expect(api.importPages).toHaveBeenCalledWith("1101", [
        {
          name: "订单详情",
          url: "https://order.test/orders/{id}",
          routeTemplate: "/orders/{id}",
          pageType: "page",
          authRole: "",
          module: "",
        },
      ]),
    )
    expect(await screen.findByText("已创建 1 个页面，跳过 2 个。")).toBeInTheDocument()
    expect(onRefresh).toHaveBeenCalled()
  })

  it("validates and creates an API definition", async () => {
    vi.mocked(api.createApi).mockResolvedValue(apiFixture)
    renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "新建 API" }))
    fireEvent.click(screen.getByRole("button", { name: "创建 API" }))
    expect(await screen.findByText("请输入 API 名称")).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("API 名称"), { target: { value: "查询订单" } })
    fireEvent.change(screen.getByLabelText("路径模板"), { target: { value: "/api/orders" } })
    fireEvent.click(screen.getByRole("button", { name: "创建 API" }))

    await waitFor(() =>
      expect(api.createApi).toHaveBeenCalledWith("1101", {
        name: "查询订单",
        method: "GET",
        pathTemplate: "/api/orders",
        authRole: "",
        schemaRef: "",
      }),
    )
  })

  it("surfaces the API 409 guard when a page cannot be deleted", async () => {
    vi.mocked(api.deletePage).mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "page is referenced by a version scope",
      }),
    )
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "删除页面 订单列表" }))
    fireEvent.click(screen.getByRole("button", { name: "删除页面" }))

    expect(await screen.findByText(/page is referenced by a version scope/)).toBeInTheDocument()
    expect(screen.getByText(/操作冲突/)).toBeInTheDocument()
    expect(api.deletePage).toHaveBeenCalledWith("1301")
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it("shows a detail loading state while the SUT aggregate is loading", () => {
    renderPanel({ scope: { ...baseScope, detailLoading: true } })
    expect(screen.getByText("正在加载环境与资源…")).toBeInTheDocument()
  })
})

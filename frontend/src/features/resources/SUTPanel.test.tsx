import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    createSutWithDefaultEnvironment: vi.fn(),
    createEnvironment: vi.fn(),
    updateEnvironment: vi.fn(),
    deleteEnvironment: vi.fn(),
    updateSut: vi.fn(),
    deleteSut: vi.fn(),
    rejectSut: vi.fn(),
  }
})

import * as api from "./api"
import { SUTPanel, type SUTPanelProps } from "./SUTPanel"
import {
  environmentFixture,
  secondEnvironmentFixture,
  secondSutFixture,
  sutFixture,
  sutListItemFixture,
} from "./test-fixtures"
import type { ResourceScope } from "./types"

const baseScope: ResourceScope = {
  suts: [sutListItemFixture, secondSutFixture],
  environments: [environmentFixture, secondEnvironmentFixture],
  sutId: "1001",
  environmentId: "1101",
  detail: sutFixture,
  loading: false,
  detailLoading: false,
  error: null,
  detailError: null,
}

function renderPanel(overrides: Partial<SUTPanelProps> = {}): {
  onSelectSut: ReturnType<typeof vi.fn>
  onRefresh: ReturnType<typeof vi.fn>
  onRetry: ReturnType<typeof vi.fn>
} {
  const onSelectSut = vi.fn()
  const onRefresh = vi.fn().mockResolvedValue(undefined)
  const onRetry = vi.fn()
  const props: SUTPanelProps = {
    scope: baseScope,
    onSelectSut,
    onRefresh,
    onRetry,
    ...overrides,
  }
  render(<SUTPanel {...props} />)
  return { onSelectSut, onRefresh, onRetry }
}

describe("SUTPanel", () => {
  beforeEach(() => {
    vi.mocked(api.createSutWithDefaultEnvironment).mockReset()
    vi.mocked(api.createEnvironment).mockReset()
    vi.mocked(api.updateEnvironment).mockReset()
    vi.mocked(api.deleteEnvironment).mockReset()
    vi.mocked(api.updateSut).mockReset()
    vi.mocked(api.deleteSut).mockReset()
    vi.mocked(api.rejectSut).mockReset()
  })

  afterEach(cleanup)

  it("lists SUTs and top-level environments, selects another SUT", () => {
    const { onSelectSut } = renderPanel()

    expect(screen.getByRole("table", { name: "SUT 列表" })).toBeInTheDocument()
    expect(screen.getByText("订单中心")).toBeInTheDocument()
    expect(screen.getByText("支付网关")).toBeInTheDocument()
    expect(screen.getByText("payments")).toBeInTheDocument()
    // v0.2.4-B: environments are top-level (they own SUTs); no per-SUT default flag.
    expect(screen.getByRole("table", { name: "环境列表" })).toBeInTheDocument()
    expect(screen.getByText("测试环境")).toBeInTheDocument()
    expect(screen.getByText("预发环境")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "支付网关" }))
    expect(onSelectSut).toHaveBeenCalledWith("1002")
  })

  it("rejects an Agent-registered SUT via the admin audit entry", async () => {
    vi.mocked(api.rejectSut).mockResolvedValue({ ...sutFixture, status: "rejected" })
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "驳回 SUT 订单中心" }))
    fireEvent.click(screen.getByRole("button", { name: "驳回 SUT" }))

    await waitFor(() => expect(api.rejectSut).toHaveBeenCalledWith("1001"))
    expect(onRefresh).toHaveBeenCalled()
  })

  it("hides the reject action for an already-rejected SUT", () => {
    renderPanel({
      scope: { ...baseScope, suts: [{ ...sutListItemFixture, status: "rejected" }] },
    })
    expect(screen.queryByRole("button", { name: "驳回 SUT 订单中心" })).not.toBeInTheDocument()
    expect(screen.getByText("已驳回")).toBeInTheDocument()
  })

  it("shows loading, error and empty states", () => {
    const { unmount } = render(
      <SUTPanel
        scope={{ ...baseScope, loading: true, suts: [], detail: null }}
        onSelectSut={vi.fn()}
        onRefresh={vi.fn().mockResolvedValue(undefined)}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("正在加载 SUT 列表…")).toBeInTheDocument()
    unmount()

    const onRetry = vi.fn()
    const error = new ApiError({
      status: 503,
      code: "unavailable",
      message: "SUT 列表暂不可用",
      retryable: true,
    })
    const second = render(
      <SUTPanel
        scope={{ ...baseScope, loading: false, suts: [], detail: null, error }}
        onSelectSut={vi.fn()}
        onRefresh={vi.fn().mockResolvedValue(undefined)}
        onRetry={onRetry}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
    second.unmount()

    render(
      <SUTPanel
        scope={{ ...baseScope, loading: false, suts: [], detail: null }}
        onSelectSut={vi.fn()}
        onRefresh={vi.fn().mockResolvedValue(undefined)}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("还没有 SUT")).toBeInTheDocument()
  })

  it("validates the create-SUT form and creates the default environment in the same flow", async () => {
    vi.mocked(api.createSutWithDefaultEnvironment).mockResolvedValue({
      sut: sutFixture,
      environment: environmentFixture,
    })
    const { onRefresh } = renderPanel({ scope: { ...baseScope, suts: [], detail: null } })

    fireEvent.click(screen.getAllByRole("button", { name: "新建 SUT" })[0] as HTMLElement)
    fireEvent.click(screen.getByRole("button", { name: "创建 SUT" }))

    expect(await screen.findByText("请输入 SUT 名称")).toBeInTheDocument()
    expect(api.createSutWithDefaultEnvironment).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText("SUT 名称"), { target: { value: "支付网关" } })
    fireEvent.change(screen.getByLabelText("简介"), { target: { value: "支付主链路" } })
    fireEvent.change(screen.getByLabelText("环境名称"), { target: { value: "测试环境" } })
    fireEvent.change(screen.getByLabelText("SUT 地址"), {
      target: { value: "https://pay.test" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建 SUT" }))

    await waitFor(() =>
      expect(api.createSutWithDefaultEnvironment).toHaveBeenCalledWith({
        sut: { name: "支付网关", intro: "支付主链路" },
        environment: { name: "测试环境", base_url: "https://pay.test" },
      }),
    )
    expect(onRefresh).toHaveBeenCalledWith("1001")
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  })

  it("creates a SUT with an optional family and rejects an uppercase family", async () => {
    vi.mocked(api.createSutWithDefaultEnvironment).mockResolvedValue({
      sut: sutFixture,
      environment: environmentFixture,
    })
    const { onRefresh } = renderPanel({ scope: { ...baseScope, suts: [], detail: null } })

    fireEvent.click(screen.getAllByRole("button", { name: "新建 SUT" })[0] as HTMLElement)
    fireEvent.change(screen.getByLabelText("SUT 名称"), { target: { value: "支付网关" } })
    fireEvent.change(screen.getByLabelText("环境名称"), { target: { value: "测试环境" } })
    fireEvent.change(screen.getByLabelText("SUT 地址"), {
      target: { value: "https://pay.test" },
    })
    fireEvent.change(screen.getByLabelText("系统族（family）"), {
      target: { value: "Payments" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建 SUT" }))

    expect(await screen.findByText("系统族必须为小写 slug（a-z、0-9、-、_）")).toBeInTheDocument()
    expect(api.createSutWithDefaultEnvironment).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText("系统族（family）"), {
      target: { value: "payments" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建 SUT" }))

    await waitFor(() =>
      expect(api.createSutWithDefaultEnvironment).toHaveBeenCalledWith({
        sut: { name: "支付网关", intro: "", family: "payments" },
        environment: { name: "测试环境", base_url: "https://pay.test" },
      }),
    )
    expect(onRefresh).toHaveBeenCalledWith("1001")
  })

  it("edits an existing SUT family and sends it in the update payload", async () => {
    vi.mocked(api.updateSut).mockResolvedValue({ ...sutFixture, family: "order-center" })
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "编辑 SUT 订单中心" }))
    fireEvent.change(screen.getByLabelText("系统族（family）"), {
      target: { value: "order-center" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存 SUT" }))

    await waitFor(() =>
      expect(api.updateSut).toHaveBeenCalledWith("1001", {
        name: "订单中心",
        intro: "订单主链路",
        family: "order-center",
      }),
    )
    expect(onRefresh).toHaveBeenCalled()
  })

  it("clears the SUT family on edit by sending an empty string in the update payload", async () => {
    vi.mocked(api.updateSut).mockResolvedValue(sutFixture)
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "编辑 SUT 订单中心" }))
    // The dialog pre-fills the current family ("payments"); clearing it must reach
    // the API as family:"" so the backend clears the stored value.
    fireEvent.change(screen.getByLabelText("系统族（family）"), { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: "保存 SUT" }))

    await waitFor(() =>
      expect(api.updateSut).toHaveBeenCalledWith("1001", {
        name: "订单中心",
        intro: "订单主链路",
        family: "",
      }),
    )
    expect(onRefresh).toHaveBeenCalled()
  })

  it("refreshes the list when the SUT create fails after the environment was created", async () => {
    vi.mocked(api.createSutWithDefaultEnvironment).mockRejectedValue(
      new api.DefaultEnvironmentError(
        environmentFixture,
        new ApiError({ status: 409, code: "conflict", message: "sut name already exists" }),
      ),
    )
    const { onRefresh } = renderPanel({ scope: { ...baseScope, suts: [], detail: null } })

    fireEvent.click(screen.getAllByRole("button", { name: "新建 SUT" })[0] as HTMLElement)
    fireEvent.change(screen.getByLabelText("SUT 名称"), { target: { value: "订单中心" } })
    fireEvent.change(screen.getByLabelText("环境名称"), { target: { value: "测试环境" } })
    fireEvent.change(screen.getByLabelText("SUT 地址"), {
      target: { value: "https://order.test" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建 SUT" }))

    expect(await screen.findByText(/SUT 创建失败/)).toBeInTheDocument()
    // The SUT was never created, so the refresh must NOT prefer a SUT id.
    expect(onRefresh).toHaveBeenCalledWith()
  })

  it("creates and edits top-level environments", async () => {
    vi.mocked(api.createEnvironment).mockResolvedValue(secondEnvironmentFixture)
    vi.mocked(api.updateEnvironment).mockResolvedValue(environmentFixture)
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "新建环境" }))
    fireEvent.click(screen.getByRole("button", { name: "创建环境" }))
    expect(await screen.findByText("请输入环境名称")).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("环境名称"), { target: { value: "预发环境" } })
    fireEvent.change(screen.getByLabelText("环境描述"), { target: { value: "预发联调" } })
    fireEvent.click(screen.getByRole("button", { name: "创建环境" }))

    // v0.2.4-B: environments are top-level; the body is {name, description, status}
    // (base_url/is_default were deleted with the per-SUT environment model).
    await waitFor(() =>
      expect(api.createEnvironment).toHaveBeenCalledWith({
        name: "预发环境",
        description: "预发联调",
        status: "active",
      }),
    )
    expect(onRefresh).toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "编辑环境 预发环境" }))
    fireEvent.change(screen.getByLabelText("环境名称"), { target: { value: "预发环境 2" } })
    fireEvent.click(screen.getByRole("button", { name: "保存环境" }))

    await waitFor(() =>
      expect(api.updateEnvironment).toHaveBeenCalledWith("1102", {
        name: "预发环境 2",
        description: "预发联调环境",
        status: "active",
      }),
    )
  })

  it("renders the referenced-version details when a SUT cannot be deleted", async () => {
    vi.mocked(api.deleteSut).mockRejectedValue(
      new ApiError({
        status: 409,
        code: "resource_referenced",
        message: "sut is referenced by a version binding",
        details: {
          kind: "sut",
          referencing_versions: [{ id: "2001", title: "订单中心 v1" }],
          resolution: "Delete the referencing versions.",
        },
      }),
    )
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "删除 SUT 订单中心" }))
    fireEvent.click(screen.getByRole("button", { name: "删除 SUT" }))

    expect(await screen.findByText("该SUT已被版本绑定引用，无法删除")).toBeVisible()
    expect(screen.getByRole("link", { name: "订单中心 v1" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=overview",
    )
    expect(screen.getByText(/请先在引用版本中改用其他 SUT/)).toBeVisible()
    expect(screen.queryByText(/sut is referenced by a version binding/)).toBeNull()
    expect(screen.queryByText(/名称\/键重复/)).toBeNull()
    expect(api.deleteSut).toHaveBeenCalledWith("1001")
    expect(onRefresh).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog")).toBeInTheDocument()
  })

  it("uses the generic no-list copy when a referenced SUT has no version rows", async () => {
    vi.mocked(api.deleteSut).mockRejectedValue(
      new ApiError({
        status: 409,
        code: "resource_referenced",
        message: "sut is referenced by a version binding",
        details: {
          kind: "sut",
          referencing_versions: [],
          resolution: "Delete the referencing versions.",
        },
      }),
    )
    renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "删除 SUT 订单中心" }))
    fireEvent.click(screen.getByRole("button", { name: "删除 SUT" }))

    expect(await screen.findByText("该SUT被版本绑定引用，无法删除")).toBeVisible()
    expect(screen.getByText(/请先在版本绑定中解除对该资源的引用后再删除/)).toBeVisible()
    expect(screen.queryByText(/这些版本/)).toBeNull()
    expect(screen.queryByRole("link")).toBeNull()
  })
})

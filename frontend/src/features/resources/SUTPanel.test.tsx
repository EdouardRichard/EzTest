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
  })

  afterEach(cleanup)

  it("lists SUTs, selects another SUT and shows the selected SUT environments", () => {
    const { onSelectSut } = renderPanel()

    expect(screen.getByRole("table", { name: "SUT 列表" })).toBeInTheDocument()
    expect(screen.getByText("订单中心")).toBeInTheDocument()
    expect(screen.getByText("支付网关")).toBeInTheDocument()
    expect(screen.getByText("payments")).toBeInTheDocument()
    expect(screen.getByRole("table", { name: "环境列表" })).toBeInTheDocument()
    expect(screen.getByText("测试环境")).toBeInTheDocument()
    expect(screen.getByText("预发环境")).toBeInTheDocument()
    expect(screen.getByText("默认")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "支付网关" }))
    expect(onSelectSut).toHaveBeenCalledWith("1002")
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
    fireEvent.change(screen.getByLabelText("默认环境名称"), { target: { value: "测试环境" } })
    fireEvent.change(screen.getByLabelText("环境地址"), {
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
    fireEvent.change(screen.getByLabelText("默认环境名称"), { target: { value: "测试环境" } })
    fireEvent.change(screen.getByLabelText("环境地址"), {
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

  it("refreshes the SUT list when the default environment fails after the SUT was created", async () => {
    vi.mocked(api.createSutWithDefaultEnvironment).mockRejectedValue(
      new api.DefaultEnvironmentError(
        sutFixture,
        new ApiError({ status: 409, code: "conflict", message: "environment name already exists" }),
      ),
    )
    const { onRefresh } = renderPanel({ scope: { ...baseScope, suts: [], detail: null } })

    fireEvent.click(screen.getAllByRole("button", { name: "新建 SUT" })[0] as HTMLElement)
    fireEvent.change(screen.getByLabelText("SUT 名称"), { target: { value: "订单中心" } })
    fireEvent.change(screen.getByLabelText("默认环境名称"), { target: { value: "测试环境" } })
    fireEvent.change(screen.getByLabelText("环境地址"), {
      target: { value: "https://order.test" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建 SUT" }))

    expect(await screen.findByText(/默认环境创建失败/)).toBeInTheDocument()
    expect(onRefresh).toHaveBeenCalledWith("1001")
  })

  it("creates and edits environments for the selected SUT", async () => {
    vi.mocked(api.createEnvironment).mockResolvedValue(secondEnvironmentFixture)
    vi.mocked(api.updateEnvironment).mockResolvedValue(environmentFixture)
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "新建环境" }))
    fireEvent.click(screen.getByRole("button", { name: "创建环境" }))
    expect(await screen.findByText("请输入环境名称")).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("环境名称"), { target: { value: "预发环境" } })
    fireEvent.change(screen.getByLabelText("环境地址"), {
      target: { value: "https://pre.order.test" },
    })
    fireEvent.click(screen.getByLabelText("设为默认环境"))
    fireEvent.click(screen.getByRole("button", { name: "创建环境" }))

    await waitFor(() =>
      expect(api.createEnvironment).toHaveBeenCalledWith("1001", {
        name: "预发环境",
        baseUrl: "https://pre.order.test",
        isDefault: true,
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
        baseUrl: "https://pre.order.test",
        isDefault: false,
        status: "active",
      }),
    )
  })

  it("surfaces a 409 when a SUT cannot be deleted because a version references it", async () => {
    vi.mocked(api.deleteSut).mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "sut is referenced by a version binding",
      }),
    )
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "删除 SUT 订单中心" }))
    fireEvent.click(screen.getByRole("button", { name: "删除 SUT" }))

    expect(await screen.findByText(/sut is referenced by a version binding/)).toBeInTheDocument()
    expect(screen.getByText(/操作冲突/)).toBeInTheDocument()
    expect(api.deleteSut).toHaveBeenCalledWith("1001")
    expect(onRefresh).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog")).toBeInTheDocument()
  })
})

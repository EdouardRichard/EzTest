import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const resourceApi = vi.hoisted(() => ({
  createPage: vi.fn(),
  createApi: vi.fn(),
  createDatabase: vi.fn(),
}))
vi.mock("@/features/resources/api", () => resourceApi)

import { BindingWizard } from "./BindingWizard"
import type { BindingDraft, Catalog, EnvironmentResource, SutResource } from "./types"

const TIMESTAMP = "2026-10-31T10:00:00Z"

/** An environment with no pages/APIs/databases, like the product-owner trial. */
const emptyEnvironment: EnvironmentResource = {
  id: "1101",
  sut_id: "1001",
  name: "local",
  base_url: "http://sut.test",
  is_default: true,
  status: "active",
  pages: [],
  apis: [],
  databases: [],
  accounts: [
    {
      id: "1601",
      sut_id: "1001",
      environment_id: "1101",
      username: "admin",
      roles: ["admin"],
      notes: "",
      password_set: true,
      status: "active",
      created_at: TIMESTAMP,
      updated_at: TIMESTAMP,
    },
  ],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const sut: SutResource = {
  id: "1001",
  name: "墨栈博客",
  intro: "博客",
  environments: [emptyEnvironment],
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

const emptyCatalog: Catalog = { suts: [sut], repositories: [] }

function bindingDraft(overrides: Partial<BindingDraft> = {}): BindingDraft {
  return {
    key: "binding-1",
    sutId: "1001",
    environmentId: "1101",
    repositoryId: "",
    pageIds: [],
    apiIds: [],
    databaseIds: [],
    accountIds: [],
    ...overrides,
  }
}

function renderWizard(overrides: Record<string, unknown> = {}) {
  const onChange = vi.fn()
  const onRefreshCatalog = vi.fn().mockResolvedValue(undefined)
  render(
    <BindingWizard
      catalog={emptyCatalog}
      value={[bindingDraft()]}
      onChange={onChange}
      errors={{}}
      onRefreshCatalog={onRefreshCatalog}
      {...overrides}
    />,
  )
  return { onChange, onRefreshCatalog }
}

describe("BindingWizard resource creation", () => {
  beforeEach(() => {
    resourceApi.createPage.mockReset()
    resourceApi.createApi.mockReset()
    resourceApi.createDatabase.mockReset()
  })

  afterEach(cleanup)

  it("offers inline create actions when the bound environment has no pages or APIs", () => {
    renderWizard()
    expect(screen.getByText(/该环境暂无页面/)).toBeInTheDocument()
    expect(screen.getByText(/该环境暂无 API/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "新建页面" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "新建 API" })).toBeInTheDocument()
  })

  it("marks discovered (proposed) pages so the user knows they are awaiting confirmation", () => {
    const proposed: EnvironmentResource = {
      ...emptyEnvironment,
      pages: [
        {
          id: "1301",
          sut_id: "1001",
          environment_id: "1101",
          name: "登录页",
          url: "http://sut.test/login",
          route_template: "/login",
          page_type: "page",
          auth_role: "",
          module: "",
          status: "proposed",
          source: "ai",
          created_at: TIMESTAMP,
          updated_at: TIMESTAMP,
        },
      ],
    }
    renderWizard({
      catalog: { suts: [{ ...sut, environments: [proposed] }], repositories: [] },
    })
    expect(screen.getByText("/login · 待确认（探索发现）")).toBeInTheDocument()
  })

  it("hides the create actions until an environment is selected", () => {
    renderWizard({ value: [bindingDraft({ environmentId: "" })] })
    expect(screen.getAllByText("请先选择环境").length).toBeGreaterThan(0)
    expect(screen.queryByRole("button", { name: "新建页面" })).not.toBeInTheDocument()
  })

  it("creates a page in the selected environment and selects it for binding", async () => {
    resourceApi.createPage.mockResolvedValue({ id: "1301" })
    const { onChange, onRefreshCatalog } = renderWizard()

    fireEvent.click(screen.getByRole("button", { name: "新建页面" }))
    const dialog = await screen.findByRole("dialog", { name: "新建页面" })
    fireEvent.change(within(dialog).getByLabelText("页面名称"), {
      target: { value: "登录页" },
    })
    fireEvent.change(within(dialog).getByLabelText("页面 URL"), {
      target: { value: "http://sut.test/login" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建页面" }))

    await waitFor(() =>
      expect(resourceApi.createPage).toHaveBeenCalledWith(
        "1101",
        expect.objectContaining({ name: "登录页", url: "http://sut.test/login" }),
      ),
    )
    await waitFor(() => expect(onRefreshCatalog).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith([
        expect.objectContaining({ key: "binding-1", pageIds: ["1301"] }),
      ]),
    )
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "新建页面" })).not.toBeInTheDocument(),
    )
  })

  it("creates an API in the selected environment and selects it for binding", async () => {
    resourceApi.createApi.mockResolvedValue({ id: "1401" })
    const { onChange, onRefreshCatalog } = renderWizard()

    fireEvent.click(screen.getByRole("button", { name: "新建 API" }))
    const dialog = await screen.findByRole("dialog", { name: "新建 API" })
    fireEvent.change(within(dialog).getByLabelText("API 名称"), {
      target: { value: "查询订单" },
    })
    fireEvent.change(within(dialog).getByLabelText("路径模板"), {
      target: { value: "/api/orders" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建 API" }))

    await waitFor(() =>
      expect(resourceApi.createApi).toHaveBeenCalledWith(
        "1101",
        expect.objectContaining({ name: "查询订单", pathTemplate: "/api/orders" }),
      ),
    )
    await waitFor(() => expect(onRefreshCatalog).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith([
        expect.objectContaining({ key: "binding-1", apiIds: ["1401"] }),
      ]),
    )
  })

  it("reloads the catalog so resources discovered elsewhere become bindable", async () => {
    const { onRefreshCatalog } = renderWizard()
    fireEvent.click(screen.getByRole("button", { name: "刷新资源目录" }))
    await waitFor(() => expect(onRefreshCatalog).toHaveBeenCalledTimes(1))
  })
})

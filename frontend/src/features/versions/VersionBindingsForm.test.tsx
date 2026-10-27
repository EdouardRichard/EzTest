import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  putBindings: vi.fn(),
  listSystems: vi.fn(),
  listEnvironments: vi.fn(),
  listCodeRepos: vi.fn(),
}))

vi.mock("./api", () => ({
  putBindings: mocks.putBindings,
}))

vi.mock("@/features/resources/api", () => ({
  listSystems: mocks.listSystems,
  listEnvironments: mocks.listEnvironments,
}))

vi.mock("@/features/code-repos/api", () => ({
  listCodeRepos: mocks.listCodeRepos,
}))

import type { CodeRepoDTO } from "@/features/code-repos/types"
import type { EnvironmentDTO, SystemDTO } from "@/features/resources/types"

import type { Version } from "./types"
import { VersionBindingsForm } from "./VersionBindingsForm"

const VERSION_ID = "1844674407370955161"
const COMMIT = "0123456789abcdef0123456789abcdef01234567"

const VERSION: Version = {
  id: VERSION_ID,
  name: "订单系统 2.0",
  label: "v2.0",
  description: "",
  status: "draft",
  systemId: 1,
  environmentId: 2,
  codeRepositoryId: 3,
  codeBranch: "main",
  codeBaselineCommit: COMMIT,
  bindings: {
    system: { id: 1, name: "订单系统", available: true },
    environment: { id: 2, name: "staging", available: true },
    codeRepository: { id: 3, name: "order-web", available: true },
  },
  requirementCount: 1,
  clarifiedCount: 0,
  openQuestionCount: 0,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const SYSTEMS: SystemDTO[] = [
  {
    id: 1,
    key: "shop",
    name: "订单系统",
    kind: "web",
    baseUrl: "http://localhost:13335",
    defaultSessionTtlSeconds: 3600,
    notes: "",
    status: "active",
    createdAt: "2026-10-27T00:00:00Z",
    updatedAt: "2026-10-27T00:00:00Z",
  },
  {
    id: 4,
    key: "pay",
    name: "支付系统",
    kind: "web",
    baseUrl: "http://localhost:13336",
    defaultSessionTtlSeconds: 3600,
    notes: "",
    status: "active",
    createdAt: "2026-10-27T00:00:00Z",
    updatedAt: "2026-10-27T00:00:00Z",
  },
]

const ENV_2: EnvironmentDTO = {
  id: 2,
  systemId: 1,
  key: "staging",
  name: "staging",
  baseUrl: "http://localhost:13335",
  kind: "staging",
  isProd: false,
  allowedHosts: [],
  loginCheck: null,
  allowAutoLogin: false,
  requireApproval: false,
}

const ENV_5: EnvironmentDTO = {
  ...ENV_2,
  id: 5,
  systemId: 4,
  key: "sandbox",
  name: "sandbox",
}

const REPO: CodeRepoDTO = {
  id: 3,
  key: "order-web",
  name: "order-web",
  localPath: "D:/repo",
  fixedBranch: "main",
  includeGlobs: ["src/**"],
  excludeGlobs: [".git/**"],
  status: "active",
  lastCommit: COMMIT,
  lastValidatedAt: null,
  lastValidationError: "",
  lastIndexedAt: null,
}

function page<T>(items: T[]) {
  return { items, nextCursor: null, hasMore: false }
}

describe("VersionBindingsForm", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listSystems.mockResolvedValue(page(SYSTEMS))
    mocks.listEnvironments.mockImplementation((systemId: number) =>
      Promise.resolve(systemId === 4 ? [ENV_5] : [ENV_2]),
    )
    mocks.listCodeRepos.mockResolvedValue(page([REPO]))
  })

  afterEach(cleanup)

  it("edits draft bindings and saves the whole binding set with its revision", async () => {
    const updated: Version = {
      ...VERSION,
      systemId: 4,
      environmentId: 5,
      bindings: {
        system: { id: 4, name: "支付系统", available: true },
        environment: { id: 5, name: "sandbox", available: true },
        codeRepository: { id: 3, name: "order-web", available: true },
      },
      revision: 3,
    }
    mocks.putBindings.mockResolvedValue(updated)
    const onChanged = vi.fn()

    render(<VersionBindingsForm version={VERSION} onChanged={onChanged} />)
    await screen.findByRole("option", { name: "支付系统" })

    expect((screen.getByLabelText("分支") as HTMLInputElement).value).toBe("main")
    expect(screen.getByText(COMMIT)).toBeTruthy()

    fireEvent.change(screen.getByLabelText("系统"), { target: { value: "4" } })
    await waitFor(() => expect(mocks.listEnvironments).toHaveBeenCalledWith(4))
    await screen.findByRole("option", { name: "sandbox" })

    const environment = screen.getByLabelText("环境") as HTMLSelectElement
    expect(environment.disabled).toBe(false)
    fireEvent.change(environment, { target: { value: "5" } })
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() =>
      expect(mocks.putBindings).toHaveBeenCalledWith(VERSION_ID, {
        revision: 2,
        systemId: 4,
        environmentId: 5,
        codeRepositoryId: 3,
        codeBranch: "main",
        codeBaselineCommit: COMMIT,
      }),
    )
    expect(onChanged).toHaveBeenCalledTimes(1)
    expect((await screen.findByRole("status")).textContent).toContain("绑定已保存。")
  })

  it("keeps bindings editable while the version is clarifying", async () => {
    render(
      <VersionBindingsForm version={{ ...VERSION, status: "clarifying" }} onChanged={vi.fn()} />,
    )
    await screen.findByRole("option", { name: "订单系统" })

    expect(screen.getByRole("button", { name: "保存绑定" })).toBeTruthy()
    expect((screen.getByLabelText("系统") as HTMLSelectElement).disabled).toBe(false)
    expect(screen.getByLabelText("代码仓")).toBeTruthy()
  })

  it("renders bindings read-only while the version is designing", async () => {
    render(
      <VersionBindingsForm version={{ ...VERSION, status: "designing" }} onChanged={vi.fn()} />,
    )

    expect(screen.getByText("设计中不可修改资源绑定。")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "保存绑定" })).toBeNull()
    expect(screen.queryAllByRole("combobox")).toHaveLength(0)
    expect(screen.getByText("订单系统")).toBeTruthy()
    expect(screen.getByText("staging")).toBeTruthy()
    expect(screen.getByText("order-web")).toBeTruthy()
    expect(screen.getByText("main")).toBeTruthy()
    expect(screen.getByText(COMMIT)).toBeTruthy()
    expect(screen.queryByText("绑定已保存。")).toBeNull()
  })

  it("clears the environment with the system and supports clearing every binding", async () => {
    mocks.putBindings.mockResolvedValue({
      ...VERSION,
      systemId: null,
      environmentId: null,
      codeRepositoryId: null,
      codeBranch: null,
      codeBaselineCommit: null,
      bindings: { system: null, environment: null, codeRepository: null },
      revision: 3,
    } as Version)

    render(<VersionBindingsForm version={VERSION} onChanged={vi.fn()} />)
    await screen.findByRole("option", { name: "订单系统" })

    fireEvent.change(screen.getByLabelText("系统"), { target: { value: "" } })
    fireEvent.change(screen.getByLabelText("代码仓"), { target: { value: "" } })

    expect((screen.getByLabelText("环境") as HTMLSelectElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    await waitFor(() =>
      expect(mocks.putBindings).toHaveBeenCalledWith(VERSION_ID, {
        revision: 2,
        systemId: null,
        environmentId: null,
        codeRepositoryId: null,
        codeBranch: null,
        codeBaselineCommit: null,
      }),
    )
  })

  it("maps a version_state_conflict to a refresh message", async () => {
    mocks.putBindings.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "version state conflict",
        details: { reason: "version_state_conflict" },
      }),
    )

    render(<VersionBindingsForm version={VERSION} onChanged={vi.fn()} />)
    await screen.findByRole("option", { name: "订单系统" })

    fireEvent.click(screen.getByRole("button", { name: "保存绑定" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("当前版本状态不允许修改绑定，请刷新后重试。")
  })
})

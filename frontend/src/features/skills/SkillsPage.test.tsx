import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const skillsApi = vi.hoisted(() => ({
  listSkills: vi.fn(),
  createSkill: vi.fn(),
  updateSkill: vi.fn(),
  deleteSkill: vi.fn(),
  listSkillVersions: vi.fn(),
}))
vi.mock("./api", () => skillsApi)

const auth = vi.hoisted(() => ({
  user: { role: "admin" as "admin" | "user" },
}))
vi.mock("@/features/auth/AuthProvider", () => ({ useAuth: () => auth }))

import { SkillsPage } from "./SkillsPage"
import type { SkillVersionView, SkillView } from "./types"

const skillFixture: SkillView = {
  id: "42",
  name: "api-recon",
  description: "API 侦察工作法",
  body: "# API recon\n\n第一步。",
  metadata: {},
  version: 2,
  enabled: true,
  created_at: "2026-11-16T10:00:00Z",
  updated_at: "2026-11-16T11:00:00Z",
}

const versionFixtures: SkillVersionView[] = [
  {
    version: 2,
    name: "api-recon",
    description: "API 侦察工作法",
    body: "# API recon\n\n第一步。",
    metadata: {},
    enabled: true,
    source: "admin",
    actor_id: "7",
    note: "运行态编辑",
    created_at: "2026-11-16T11:00:00Z",
  },
  {
    version: 1,
    name: "api-recon",
    description: "API 侦察工作法",
    body: "seed body",
    metadata: {},
    enabled: true,
    source: "seed",
    actor_id: null,
    note: "",
    created_at: "2026-11-16T10:00:00Z",
  },
]

describe("SkillsPage", () => {
  beforeEach(() => {
    auth.user = { role: "admin" }
    for (const fn of Object.values(skillsApi)) fn.mockReset()
    skillsApi.listSkills.mockResolvedValue([skillFixture])
    skillsApi.listSkillVersions.mockResolvedValue({
      items: versionFixtures,
      nextCursor: "",
      hasMore: false,
    })
    skillsApi.createSkill.mockResolvedValue({ ...skillFixture, name: "new-skill", version: 1 })
    skillsApi.updateSkill.mockResolvedValue({
      ...skillFixture,
      body: "# API recon v3\n\n运行态新内容。",
      version: 3,
    })
    skillsApi.deleteSkill.mockResolvedValue(undefined)
  })

  afterEach(cleanup)

  it("hides the console from normal users and never calls the API", async () => {
    auth.user = { role: "user" }
    render(<SkillsPage />)
    expect(screen.getByText("技能库仅管理员可用")).toBeInTheDocument()
    expect(screen.queryByTestId("skill-create-open")).not.toBeInTheDocument()
    expect(skillsApi.listSkills).not.toHaveBeenCalled()
  })

  it("lists skills with version and enabled state", async () => {
    render(<SkillsPage />)
    const row = await screen.findByTestId("skill-row-api-recon")
    expect(row).toHaveTextContent("api-recon")
    expect(row).toHaveTextContent("API 侦察工作法")
    expect(screen.getByText("v2")).toBeInTheDocument()
    expect(screen.getByText("启用")).toBeInTheDocument()
  })

  it("shows an empty state when the library is empty", async () => {
    skillsApi.listSkills.mockResolvedValue([])
    render(<SkillsPage />)
    expect(await screen.findByText("技能库为空")).toBeInTheDocument()
  })

  it("creates a skill from the dialog and refreshes the list", async () => {
    render(<SkillsPage />)
    await screen.findByTestId("skill-row-api-recon")
    fireEvent.click(screen.getByTestId("skill-create-open"))
    fireEvent.change(screen.getByLabelText(/名称（包名）/), { target: { value: "new-skill" } })
    fireEvent.change(screen.getByLabelText("描述"), { target: { value: "新技能描述" } })
    fireEvent.change(screen.getByLabelText(/正文（SKILL.md 内容）/), {
      target: { value: "# 新技能\n\n内容。" },
    })
    fireEvent.click(screen.getByTestId("skill-create-submit"))

    await waitFor(() =>
      expect(skillsApi.createSkill).toHaveBeenCalledWith({
        name: "new-skill",
        description: "新技能描述",
        body: "# 新技能\n\n内容。",
      }),
    )
    await waitFor(() => expect(skillsApi.listSkills).toHaveBeenCalledTimes(2))
  })

  it("rejects an invalid package name before calling the API", async () => {
    render(<SkillsPage />)
    await screen.findByTestId("skill-row-api-recon")
    fireEvent.click(screen.getByTestId("skill-create-open"))
    fireEvent.change(screen.getByLabelText(/名称（包名）/), { target: { value: "Bad Name" } })
    fireEvent.click(screen.getByTestId("skill-create-submit"))
    expect(await screen.findByText(/名称须匹配/)).toBeInTheDocument()
    expect(skillsApi.createSkill).not.toHaveBeenCalled()
  })

  it("saves a runtime edit and immediately shows the new content and version", async () => {
    render(<SkillsPage />)
    await screen.findByTestId("skill-row-api-recon")
    fireEvent.click(screen.getByTestId("skill-edit-api-recon"))

    const editor = await screen.findByTestId("skill-editor")
    const bodyField = screen.getByLabelText(/正文（SKILL.md 内容）/)
    expect(bodyField).toHaveValue(skillFixture.body)
    fireEvent.change(bodyField, { target: { value: "# API recon v3\n\n运行态新内容。" } })
    fireEvent.change(screen.getByLabelText("版本备注"), { target: { value: "运行态编辑" } })
    fireEvent.click(screen.getByTestId("skill-save"))

    await waitFor(() =>
      expect(skillsApi.updateSkill).toHaveBeenCalledWith("api-recon", {
        description: skillFixture.description,
        body: "# API recon v3\n\n运行态新内容。",
        enabled: true,
        note: "运行态编辑",
        expected_version: 2,
      }),
    )
    // The saved version and content are visible without any reload/restart.
    expect(await screen.findByText("已保存为 v3")).toBeInTheDocument()
    expect(editor).toHaveTextContent("v3")
    expect(bodyField).toHaveValue("# API recon v3\n\n运行态新内容。")
    await waitFor(() => expect(skillsApi.listSkills).toHaveBeenCalledTimes(2))
  })

  it("renders the append-only version history with source labels", async () => {
    render(<SkillsPage />)
    await screen.findByTestId("skill-row-api-recon")
    fireEvent.click(screen.getByTestId("skill-edit-api-recon"))

    const v1 = await screen.findByTestId("skill-version-1")
    expect(v1).toHaveTextContent("文件播种")
    const v2 = screen.getByTestId("skill-version-2")
    expect(v2).toHaveTextContent("管理员编辑")
    expect(v2).toHaveTextContent("运行态编辑")
    expect(skillsApi.listSkillVersions).toHaveBeenCalledWith("api-recon", { limit: 20 })

    fireEvent.click(screen.getByTestId("skill-version-view-1"))
    expect(await screen.findByText("seed body")).toBeInTheDocument()
  })

  it("deletes a skill after confirmation and refreshes the list", async () => {
    render(<SkillsPage />)
    await screen.findByTestId("skill-row-api-recon")
    fireEvent.click(screen.getByTestId("skill-delete-api-recon"))
    expect(await screen.findByTestId("skill-delete-dialog")).toBeInTheDocument()
    fireEvent.click(screen.getByTestId("skill-delete-confirm"))

    await waitFor(() => expect(skillsApi.deleteSkill).toHaveBeenCalledWith("api-recon"))
    await waitFor(() => expect(skillsApi.listSkills).toHaveBeenCalledTimes(2))
  })
})

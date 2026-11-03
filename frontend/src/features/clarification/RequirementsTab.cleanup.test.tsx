import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { versionFixture } from "@/features/versions/test-fixtures"
import { ApiError } from "@/lib/api-client"

import {
  requirementDetailFixture,
  requirementFixture,
  requirementSecondFixture,
  teamRoomFixture,
} from "./test-fixtures"
import type { Requirement } from "./types"

const duplicateDraft: Requirement = {
  ...requirementFixture,
  id: "6004",
  current_version_id: "6104",
  title: "退款到账时效（重复）",
}

const api = vi.hoisted(() => ({
  listConversations: vi.fn(),
  createConversation: vi.fn(),
  getConversation: vi.fn(),
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  listRequirements: vi.fn(),
  createRequirement: vi.fn(),
  getRequirement: vi.fn(),
  updateRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
  confirmRequirements: vi.fn(),
  confirmStageRequirements: vi.fn(),
  rollbackRequirement: vi.fn(),
  listRequirementRelations: vi.fn(),
  dedupeRequirements: vi.fn(),
  deleteRequirement: vi.fn(),
  mergeRequirements: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
}))
vi.mock("./api", () => api)

const versionsApi = vi.hoisted(() => ({ getVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

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

vi.mock("./useVersionEvents", () => ({
  useVersionEvents: () => "live",
}))

import { RequirementsTab } from "./RequirementsTab"

function draftList(): Requirement[] {
  return [requirementFixture, duplicateDraft, requirementSecondFixture]
}

async function selectDraft(title: string): Promise<void> {
  await screen.findByTestId("requirements-tab")
  fireEvent.click(screen.getByRole("checkbox", { name: `选择${title}` }))
}

describe("RequirementsTab draft cleanup", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    versionsApi.getVersion.mockReset()
    navigation.replace.mockReset()

    versionsApi.getVersion.mockResolvedValue(versionFixture)
    api.listConversations.mockResolvedValue([])
    api.listRequirements.mockResolvedValue(draftList())
    api.listMessages.mockResolvedValue([])
    api.getRequirement.mockResolvedValue(requirementDetailFixture)
    api.getTeamRoom.mockResolvedValue(teamRoomFixture)
    api.deleteRequirement.mockResolvedValue({ ...requirementDetailFixture, status: "obsolete" })
    api.mergeRequirements.mockResolvedValue({
      requirement: { ...requirementDetailFixture, revision: 2 },
      merged_ids: ["6004"],
    })
    api.dedupeRequirements.mockResolvedValue({ kept_ids: ["6001"], obsolete_ids: ["6004"] })
  })

  afterEach(cleanup)

  it("deletes only the selected drafts after confirmation and refreshes", async () => {
    api.listRequirements
      .mockResolvedValueOnce(draftList())
      .mockResolvedValue([requirementSecondFixture])
    render(<RequirementsTab versionId="2001" />)

    await selectDraft("退款到账时效")
    await selectDraft("退款到账时效（重复）")
    fireEvent.click(screen.getByTestId("cleanup-delete"))
    fireEvent.click(screen.getByTestId("cleanup-confirm"))

    await waitFor(() => expect(api.deleteRequirement).toHaveBeenCalledWith("2001", "6001"))
    expect(api.deleteRequirement).toHaveBeenCalledWith("2001", "6004")
    expect(api.deleteRequirement).not.toHaveBeenCalledWith("2001", "6002")
    expect(await screen.findByTestId("cleanup-notice")).toHaveTextContent(
      "已删除 2 条草案（标记为 obsolete）",
    )
    expect(api.listRequirements).toHaveBeenCalledTimes(2)
  })

  it("merges selected drafts into the chosen canonical target", async () => {
    api.listRequirements
      .mockResolvedValueOnce(draftList())
      .mockResolvedValue([requirementFixture, requirementSecondFixture])
    render(<RequirementsTab versionId="2001" />)

    await selectDraft("退款到账时效")
    await selectDraft("退款到账时效（重复）")
    fireEvent.click(screen.getByTestId("cleanup-merge"))
    fireEvent.change(screen.getByTestId("cleanup-merge-target"), { target: { value: "6001" } })
    fireEvent.click(screen.getByTestId("cleanup-confirm"))

    await waitFor(() =>
      expect(api.mergeRequirements).toHaveBeenCalledWith("2001", "6001", ["6004"]),
    )
    expect(await screen.findByTestId("cleanup-notice")).toHaveTextContent(
      "已将 1 条草案合并到「退款到账时效」",
    )
    expect(api.listRequirements).toHaveBeenCalledTimes(2)
  })

  it("dedupes the version's duplicate drafts and reports how many were retired", async () => {
    render(<RequirementsTab versionId="2001" />)
    await screen.findByTestId("requirements-tab")

    fireEvent.click(screen.getByTestId("cleanup-dedupe"))
    fireEvent.click(screen.getByTestId("cleanup-confirm"))

    await waitFor(() => expect(api.dedupeRequirements).toHaveBeenCalledWith("2001"))
    expect(await screen.findByTestId("cleanup-notice")).toHaveTextContent(
      "已去重 1 条重复草案，保留 1 条",
    )
    expect(api.listRequirements).toHaveBeenCalledTimes(2)
  })

  it("does not claim success when dedupe finds no duplicate", async () => {
    api.dedupeRequirements.mockResolvedValue({ kept_ids: ["6001", "6004"], obsolete_ids: [] })
    render(<RequirementsTab versionId="2001" />)
    await screen.findByTestId("requirements-tab")

    fireEvent.click(screen.getByTestId("cleanup-dedupe"))
    fireEvent.click(screen.getByTestId("cleanup-confirm"))

    expect(await screen.findByTestId("cleanup-notice")).toHaveTextContent("未发现重复草案")
  })

  it("shows the typed conflict and keeps the selection when a delete is rejected", async () => {
    api.deleteRequirement.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "only a draft requirement can be deleted; roll back a clarified requirement first",
      }),
    )
    render(<RequirementsTab versionId="2001" />)

    await selectDraft("退款到账时效")
    fireEvent.click(screen.getByTestId("cleanup-delete"))
    fireEvent.click(screen.getByTestId("cleanup-confirm"))

    expect(await screen.findByText(/roll back a clarified requirement first/)).toBeInTheDocument()
    expect(screen.queryByTestId("cleanup-notice")).not.toBeInTheDocument()
    expect(screen.getByRole("checkbox", { name: "选择退款到账时效" })).toBeChecked()
  })

  it("never offers delete or merge for a confirmed-only selection", async () => {
    render(<RequirementsTab versionId="2001" />)

    await selectDraft("大额订单人工审核")

    expect(screen.queryByTestId("cleanup-delete")).not.toBeInTheDocument()
    expect(screen.queryByTestId("cleanup-merge")).not.toBeInTheDocument()
    // The batch-confirm action remains available for the confirmed selection.
    expect(screen.getByTestId("bulk-confirm-bar")).toHaveTextContent("已选 1 条")
  })

  it("hides dedupe when the version has fewer than two drafts", async () => {
    api.listRequirements.mockResolvedValue([requirementFixture, requirementSecondFixture])
    render(<RequirementsTab versionId="2001" />)

    await screen.findByTestId("requirements-tab")
    expect(screen.queryByTestId("cleanup-dedupe")).not.toBeInTheDocument()
  })
})

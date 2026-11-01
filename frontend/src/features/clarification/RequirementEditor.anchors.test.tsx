import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const clarificationApi = vi.hoisted(() => ({
  updateRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
}))
vi.mock("./api", () => clarificationApi)

const anchorApi = vi.hoisted(() => ({
  listCodeAnchors: vi.fn(),
  listVersionCodeRepos: vi.fn(),
  createCodeAnchor: vi.fn(),
  validateCodeAnchor: vi.fn(),
  deleteCodeAnchor: vi.fn(),
}))
vi.mock("@/features/anchors/api", () => anchorApi)

import { AnchorPanel } from "@/features/anchors/AnchorPanel"
import { anchorFixture } from "@/features/anchors/test-fixtures"
import { RequirementEditor } from "./RequirementEditor"
import { requirementDetailFixture } from "./test-fixtures"

const targetOptions = { suts: [], pages: [], apis: [] }

describe("RequirementEditor code anchors", () => {
  beforeEach(() => {
    for (const fn of Object.values(anchorApi)) fn.mockReset()
    anchorApi.listCodeAnchors.mockResolvedValue([anchorFixture])
    anchorApi.listVersionCodeRepos.mockResolvedValue([])
  })

  afterEach(cleanup)

  it("renders the collapsible requirement anchor panel and keeps P2 save/confirm intact", async () => {
    render(
      <RequirementEditor
        versionId="2001"
        requirement={requirementDetailFixture}
        targetOptions={targetOptions}
        onUpdated={vi.fn()}
      />,
    )

    expect(screen.getByTestId("requirement-editor")).toHaveTextContent("退款到账时效")
    expect(screen.getByTestId("editor-save")).toBeInTheDocument()
    expect(screen.getByTestId("editor-confirm")).toBeInTheDocument()
    expect(anchorApi.listCodeAnchors).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId("anchor-panel-toggle"))

    await waitFor(() =>
      expect(anchorApi.listCodeAnchors).toHaveBeenCalledWith("2001", {
        subjectKind: "requirement",
        subjectId: requirementDetailFixture.id,
      }),
    )
    expect(await screen.findByText("src/pages/OrdersPage.tsx:10-24")).toBeInTheDocument()
  })

  it("exports the same shared AnchorPanel component for the other subjects", () => {
    expect(AnchorPanel).toBeTypeOf("function")
  })
})

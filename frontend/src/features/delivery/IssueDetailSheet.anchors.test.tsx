import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const anchorApi = vi.hoisted(() => ({
  listCodeAnchors: vi.fn(),
  listVersionCodeRepos: vi.fn(),
  createCodeAnchor: vi.fn(),
  validateCodeAnchor: vi.fn(),
  deleteCodeAnchor: vi.fn(),
}))
vi.mock("@/features/anchors/api", () => anchorApi)

import { anchorFixture } from "@/features/anchors/test-fixtures"
import { IssueDetailSheet } from "./IssueDetailSheet"
import type { IssueDetailView } from "./types"

const issue: IssueDetailView = {
  id: "9101",
  version_id: "2001",
  title: "结算页 · step 3: ui failure",
  summary: "点击提交后未跳转到支付页",
  status: "open",
  category: "ui",
  severity: "major",
  source: "failure_candidate",
  expected_revision: 1,
  merged_into_id: null,
  merged_at: null,
  reopen_count: 0,
  refs: [{ kind: "case_run", ref_id: "9302", version_sut_id: "3001", role: "source" }],
  triage: null,
  created_at: "2026-11-01T02:00:00Z",
  updated_at: "2026-11-01T03:00:00Z",
  history: [],
  related_refs: { case_run: 1 },
}

describe("IssueDetailSheet code anchors", () => {
  beforeEach(() => {
    for (const fn of Object.values(anchorApi)) fn.mockReset()
    anchorApi.listCodeAnchors.mockResolvedValue([
      { ...anchorFixture, subject_kind: "issue", subject_id: "9101" },
    ])
    anchorApi.listVersionCodeRepos.mockResolvedValue([])
  })

  afterEach(cleanup)

  it("adds the issue anchor panel as a separate section from refs/evidence", async () => {
    render(
      <IssueDetailSheet
        versionId="2001"
        issue={issue}
        loading={false}
        error={null}
        onClose={vi.fn()}
        onRetry={vi.fn()}
        onTransition={vi.fn()}
        onMerge={vi.fn()}
      />,
    )

    expect(screen.getByTestId("issue-refs")).toBeInTheDocument()
    expect(anchorApi.listCodeAnchors).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId("anchor-panel-toggle"))

    await waitFor(() =>
      expect(anchorApi.listCodeAnchors).toHaveBeenCalledWith("2001", {
        subjectKind: "issue",
        subjectId: "9101",
      }),
    )
    expect(await screen.findByText("src/pages/OrdersPage.tsx:10-24")).toBeInTheDocument()
  })
})

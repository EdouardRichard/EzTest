import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const delivery = vi.hoisted(() => ({ createManualIssue: vi.fn() }))
vi.mock("@/features/delivery/api", () => delivery)

import { CaseRunDetailView } from "./CaseRunDetail"
import { caseRunDetailFixture } from "./test-fixtures"
import type { CaseRunDetail } from "./types"

beforeEach(() => {
  delivery.createManualIssue.mockReset()
  delivery.createManualIssue.mockResolvedValue({ id: "9110" })
})

afterEach(cleanup)

describe("CaseRunDetail manual issue entry", () => {
  it("creates a manual issue linked to the failed run and its evidence", async () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={caseRunDetailFixture}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "创建问题" }))
    const dialog = await screen.findByRole("dialog", { name: "创建问题" })
    fireEvent.change(within(dialog).getByLabelText("标题"), { target: { value: "登录失败" } })
    fireEvent.change(within(dialog).getByLabelText("类别"), { target: { value: "api" } })
    fireEvent.change(within(dialog).getByLabelText("严重度"), { target: { value: "minor" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建问题" }))

    await waitFor(() =>
      expect(delivery.createManualIssue).toHaveBeenCalledWith(
        "2001",
        expect.objectContaining({
          title: "登录失败",
          category: "api",
          severity: "minor",
          case_run_id: "9302",
          evidence_ids: ["9401", "9402"],
        }),
      ),
    )
    expect(await within(dialog).findByRole("link", { name: "查看问题 #9110" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=issues&issue=9110",
    )
  })

  it("keeps the dialog open and shows the API error when creation fails", async () => {
    delivery.createManualIssue.mockRejectedValueOnce(new Error("issue creation failed"))

    render(
      <CaseRunDetailView
        versionId="2001"
        detail={caseRunDetailFixture}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "创建问题" }))
    const dialog = await screen.findByRole("dialog", { name: "创建问题" })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建问题" }))

    expect(await within(dialog).findByText("issue creation failed")).toBeInTheDocument()
    expect(screen.getByRole("dialog", { name: "创建问题" })).toBeInTheDocument()
  })

  it("does not offer manual issue creation for a passed run", () => {
    const passed: CaseRunDetail = {
      ...caseRunDetailFixture,
      case_run: {
        ...caseRunDetailFixture.case_run,
        status: "passed",
        failure_class: "",
      },
    }

    render(
      <CaseRunDetailView
        versionId="2001"
        detail={passed}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.queryByRole("button", { name: "创建问题" })).not.toBeInTheDocument()
  })
})

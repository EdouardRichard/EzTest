import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { DiscoveryRevisionNotice } from "./DiscoveryRevisionNotice"
import type { CaseRevisionRollbackView } from "./types"

const rollbackResult: CaseRevisionRollbackView = {
  case_id: "7001",
  case_version_id: "7010",
  rolled_back_from: "7009",
  revision: 3,
  status: "confirmed",
  trigger: "rollback",
}

describe("DiscoveryRevisionNotice", () => {
  afterEach(cleanup)

  it("shows auto-confirmed revision and rolls it back", async () => {
    const onRollback = vi.fn().mockResolvedValue(rollbackResult)
    const onRolledBack = vi.fn()

    render(
      <DiscoveryRevisionNotice
        revision={{
          id: "9",
          caseVersionId: "9",
          status: "confirmed",
          trigger: "discovery_delta",
        }}
        onRollback={onRollback}
        onRolledBack={onRolledBack}
      />,
    )

    expect(screen.getByText(/自动确认/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "回滚" }))

    await waitFor(() => expect(onRollback).toHaveBeenCalledWith("9"))
    expect(await screen.findByText(/已回滚到替代 revision 7010/)).toBeInTheDocument()
    expect(screen.getByText(/原 revision 7009/)).toBeInTheDocument()
    expect(onRolledBack).toHaveBeenCalledWith(rollbackResult)
    expect(screen.queryByText(/删除/)).not.toBeInTheDocument()
  })

  it("does not claim an auto-confirmation when the system did not confirm it", () => {
    render(
      <DiscoveryRevisionNotice
        revision={{
          caseVersionId: "7012",
          status: "confirmed",
          trigger: "discovery_delta",
          autoConfirmedBy: "42",
        }}
        onRollback={vi.fn()}
      />,
    )

    expect(screen.queryByText(/自动确认/)).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "回滚" })).not.toBeInTheDocument()
    expect(screen.getByText(/人工操作或历史投影/)).toBeInTheDocument()
  })

  it("shows a low-confidence discovery draft for manual confirmation", () => {
    render(
      <DiscoveryRevisionNotice
        revision={{
          caseId: "7001",
          caseVersionId: "7010",
          status: "draft",
          trigger: "discovery_delta",
          lowConfidenceReason: "insufficient_evidence",
        }}
      />,
    )

    expect(screen.getByText("低置信草案")).toBeInTheDocument()
    expect(screen.getByText(/需人工确认/)).toBeInTheDocument()
    expect(screen.getByText(/支撑证据少于 2 条/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "回滚" })).not.toBeInTheDocument()
    expect(screen.queryByText(/自动确认/)).not.toBeInTheDocument()
  })

  it("renders a rollback replacement with the replacement id and rollback_of", () => {
    render(
      <DiscoveryRevisionNotice
        revision={{
          caseId: "7001",
          caseVersionId: "7011",
          status: "confirmed",
          trigger: "rollback",
          rollbackOf: "7010",
          revision: 3,
        }}
      />,
    )

    expect(screen.getByText(/已回滚到替代 revision 7011/)).toBeInTheDocument()
    expect(screen.getByText(/原 revision 7010/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "回滚" })).not.toBeInTheDocument()
    // W2-C11: a rollback keeps the case; the UI must never claim it was deleted.
    expect(screen.queryByText(/删除/)).not.toBeInTheDocument()
  })

  it("passes an optional rollback reason and keeps the same replacement on a repeat", async () => {
    const onRollback = vi.fn().mockResolvedValue(rollbackResult)

    render(
      <DiscoveryRevisionNotice
        revision={{ caseVersionId: "7010", status: "confirmed", trigger: "discovery_delta" }}
        onRollback={onRollback}
      />,
    )

    fireEvent.change(screen.getByLabelText("回滚原因（可选）"), {
      target: { value: "探索误判" },
    })
    fireEvent.click(screen.getByRole("button", { name: "回滚" }))
    await waitFor(() => expect(onRollback).toHaveBeenCalledWith("7010", "探索误判"))

    fireEvent.click(screen.getByRole("button", { name: /再次回滚/ }))
    await waitFor(() => expect(onRollback).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.getByText(/已回滚到替代 revision 7010/)).toBeInTheDocument())
  })

  it("surfaces rollback errors and allows a retry", async () => {
    const onRollback = vi
      .fn()
      .mockRejectedValueOnce(
        new ApiError({
          status: 409,
          code: "state_conflict",
          message: "仅自动确认的探索修订可回滚",
          requestId: "req_rollback",
        }),
      )
      .mockResolvedValueOnce(rollbackResult)

    render(
      <DiscoveryRevisionNotice
        revision={{ caseVersionId: "7010", status: "confirmed", trigger: "discovery_delta" }}
        onRollback={onRollback}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "回滚" }))
    expect(await screen.findByText("仅自动确认的探索修订可回滚")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "回滚" }))
    await waitFor(() => expect(onRollback).toHaveBeenCalledTimes(2))
    expect(await screen.findByText(/已回滚到替代 revision 7010/)).toBeInTheDocument()
  })

  it("is read-only for non-admin viewers", () => {
    render(
      <DiscoveryRevisionNotice
        revision={{ caseVersionId: "7010", status: "confirmed", trigger: "discovery_delta" }}
        onRollback={vi.fn()}
        readOnly
      />,
    )

    expect(screen.getByText(/只读/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "回滚" })).not.toBeInTheDocument()
  })
})

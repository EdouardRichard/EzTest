import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ToolCallBlock } from "./ToolCallBlock"
import type { ToolCallPart } from "./types"

afterEach(cleanup)

function expand(part: ToolCallPart) {
  render(<ToolCallBlock part={part} />)
  fireEvent.click(screen.getByRole("button", { name: /查看结果/ }))
}

describe("tool call readable results", () => {
  it("renders the sut.database_list inventory with ids, names, kinds and read-only state", () => {
    expand({
      type: "tool_call",
      call_id: "call_db",
      name: "sut.database_list",
      status: "done",
      result: {
        version_id: "7",
        count: 2,
        databases: [
          { id: "1501", name: "order_db", kind: "postgres", read_only: true },
          { id: "1502", name: "audit_db", kind: "postgres", read_only: false },
        ],
      },
    })

    expect(screen.getByTestId("database-list")).toBeInTheDocument()
    expect(screen.getByText(/order_db/)).toBeInTheDocument()
    expect(screen.getByText(/1501/)).toBeInTheDocument()
    expect(screen.getAllByText(/postgres/).length).toBeGreaterThan(0)
    expect(screen.getByText(/只读/)).toBeInTheDocument()
    expect(screen.getByText(/可写/)).toBeInTheDocument()
  })

  it("renders a case.graph_propose result as scenario/case/case-version counts", () => {
    expand({
      type: "tool_call",
      call_id: "call_propose",
      name: "case.graph_propose",
      status: "done",
      result: {
        scenario_ids: ["101"],
        case_ids: ["201", "202"],
        case_version_ids: ["301", "302"],
        graph_hash: "a".repeat(64),
      },
    })

    expect(screen.getByTestId("graph-propose-result")).toBeInTheDocument()
    expect(screen.getByText(/1 个场景/)).toBeInTheDocument()
    expect(screen.getByText(/2 个用例(?!版本)/)).toBeInTheDocument()
    expect(screen.getByText(/2 个用例版本/)).toBeInTheDocument()
  })

  it("renders a structured graph_propose error with field, allowed values, hint and retry", () => {
    const onRetry = vi.fn()
    const part: ToolCallPart = {
      type: "tool_call",
      call_id: "call_failed",
      name: "case.graph_propose",
      status: "failed",
      error: "invalid_argument: database_id is required for db assertions",
      result: {
        error: {
          code: "invalid_argument",
          message: "database_id is required for db assertions",
          retryable: true,
          field: "cases[0].assertions[0].database_id",
          allowed: ["1501", "1502"],
          hint: "call sut.database_list first",
        },
      },
    }
    render(<ToolCallBlock part={part} onRetry={onRetry} />)
    fireEvent.click(screen.getByRole("button", { name: /查看结果/ }))

    expect(screen.getByTestId("tool-error-details")).toBeInTheDocument()
    expect(screen.getByText("cases[0].assertions[0].database_id")).toBeInTheDocument()
    expect(screen.getByText(/1501, 1502/)).toBeInTheDocument()
    expect(screen.getByText("call sut.database_list first")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledWith(part)
  })
})

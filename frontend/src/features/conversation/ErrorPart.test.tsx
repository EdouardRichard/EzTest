import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ErrorPart } from "./ErrorPart"
import type { ErrorPart as ErrorPartPayload } from "./types"

afterEach(cleanup)

describe("error part retry card", () => {
  it("renders the field path, allowed values, hint and a retry action", () => {
    const onAction = vi.fn()
    const part: ErrorPartPayload = {
      type: "error",
      code: "invalid_argument",
      message: "database_id is required for db assertions",
      retryable: true,
      field: "cases[0].assertions[0].database_id",
      allowed: ["1501", "1502"],
      hint: "call sut.database_list first",
    }
    render(<ErrorPart part={part} onAction={onAction} />)

    expect(screen.getByText("database_id is required for db assertions")).toBeInTheDocument()
    expect(screen.getByText("cases[0].assertions[0].database_id")).toBeInTheDocument()
    expect(screen.getByText(/1501, 1502/)).toBeInTheDocument()
    expect(screen.getByText("call sut.database_list first")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onAction).toHaveBeenCalledWith(part)
  })

  it("renders structured error details sent through the legacy details bag", () => {
    const onAction = vi.fn()
    const part: ErrorPartPayload = {
      type: "error",
      code: "invalid_argument",
      message: "invalid assertion",
      retryable: true,
      details: {
        field: "cases[0].assertions[0].kind",
        allowed: ["ui", "api", "db", "text", "visual"],
        hint: "choose one of the listed assertion kinds",
      },
    }
    render(<ErrorPart part={part} onAction={onAction} />)

    expect(screen.getByText("cases[0].assertions[0].kind")).toBeInTheDocument()
    expect(screen.getByText(/ui, api, db, text, visual/)).toBeInTheDocument()
    expect(screen.getByText("choose one of the listed assertion kinds")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onAction).toHaveBeenCalledWith(part)
  })

  it("does not offer retry for a non-retryable conflict", () => {
    const part: ErrorPartPayload = {
      type: "error",
      code: "state_conflict",
      message: "base_revision 2 is stale",
      retryable: false,
      field: "base_revision",
    }
    render(<ErrorPart part={part} onAction={vi.fn()} />)

    expect(screen.getByText("base_revision")).toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })
})

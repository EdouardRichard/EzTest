import { describe, expect, it } from "vitest"

import {
  DB_FAILURE_LABELS,
  dbFailureLabel,
  isDbAssertionKind,
  parseDbAssertionActual,
  parseDbResultEvidence,
} from "./db-result"

const DB_RESULT_EVIDENCE = {
  assertion_id: "1844674407370955161",
  sql: "SELECT id, name FROM orders ORDER BY id",
  columns: ["id", "name"],
  rows: [
    ["1", "张三"],
    ["2", "李四"],
  ],
  row_count: 5,
  truncated: true,
  elapsed_ms: 12,
}

describe("parseDbResultEvidence", () => {
  it("parses the db_result evidence document of spec §9.5", () => {
    const document = parseDbResultEvidence(JSON.stringify(DB_RESULT_EVIDENCE))

    expect(document).toEqual({
      assertionId: "1844674407370955161",
      sql: "SELECT id, name FROM orders ORDER BY id",
      columns: ["id", "name"],
      rows: [
        ["1", "张三"],
        ["2", "李四"],
      ],
      rowCount: 5,
      truncated: true,
      elapsedMs: 12,
    })
  })

  it("defaults missing columns, rows and flags defensively", () => {
    const document = parseDbResultEvidence(JSON.stringify({ sql: "SELECT 1" }))

    expect(document).toEqual({
      assertionId: "",
      sql: "SELECT 1",
      columns: [],
      rows: [],
      rowCount: null,
      truncated: false,
      elapsedMs: null,
    })
  })

  it("accepts camelCase counters and coerces scalar cells to strings", () => {
    const document = parseDbResultEvidence(
      JSON.stringify({
        columns: ["id"],
        rows: [[1], [true], [null]],
        rowCount: 3,
        elapsedMs: 7,
      }),
    )

    expect(document?.rowCount).toBe(3)
    expect(document?.elapsedMs).toBe(7)
    expect(document?.rows).toEqual([["1"], ["true"], [""]])
  })

  it("returns null when the artifact is not a JSON object", () => {
    expect(parseDbResultEvidence("not json")).toBeNull()
    expect(parseDbResultEvidence("[]")).toBeNull()
    expect(parseDbResultEvidence("null")).toBeNull()
  })
})

describe("parseDbAssertionActual", () => {
  it("parses the recorded assertion preview (camelCase)", () => {
    expect(
      parseDbAssertionActual(
        JSON.stringify({ columns: ["id"], rows: [["1"]], rowCount: 3, truncated: true }),
      ),
    ).toEqual({ rowCount: 3, truncated: true, elapsedMs: null })
  })

  it("accepts snake_case counters and an elapsed time when the server sends one", () => {
    expect(
      parseDbAssertionActual(JSON.stringify({ row_count: 2, truncated: false, elapsed_ms: 9 })),
    ).toEqual({ rowCount: 2, truncated: false, elapsedMs: 9 })
  })

  it("returns null for empty or malformed previews", () => {
    expect(parseDbAssertionActual("")).toBeNull()
    expect(parseDbAssertionActual("—")).toBeNull()
    expect(parseDbAssertionActual("[1,2]")).toBeNull()
  })
})

describe("db assertion helpers", () => {
  it("recognises db assertions only", () => {
    expect(isDbAssertionKind("db")).toBe(true)
    expect(isDbAssertionKind("ui")).toBe(false)
    expect(isDbAssertionKind("")).toBe(false)
  })

  it("maps every stable db failure reason to readable copy", () => {
    for (const [reason, label] of Object.entries(DB_FAILURE_LABELS)) {
      expect(dbFailureLabel(reason)).toBe(label)
    }
    expect(dbFailureLabel("db_readonly_violation")).toContain("只读")
    expect(dbFailureLabel("unknown_reason")).toBe("unknown_reason")
    expect(dbFailureLabel(undefined)).toBe("")
  })
})

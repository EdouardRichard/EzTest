import { describe, expect, it } from "vitest"

import { buildAuditCSV, buildAuditMarkdown } from "./audit-export"
import type { AuditEntry } from "./audit-types"

const ENTRIES: AuditEntry[] = [
  {
    id: 12,
    actor_user_id: 1,
    action: "version.requirement_created",
    target_type: "requirement",
    target_id: "77",
    request_id: "req_1",
    ip: "203.0.113.7",
    user_agent: "eztest/1.0",
    metadata: { status: "draft", note: 'quoted "value", with comma' },
    created_at: "2026-10-27T04:00:00Z",
  },
  {
    id: 11,
    actor_user_id: null,
    action: "auth.login_failed",
    target_type: "user",
    target_id: "",
    request_id: "",
    ip: "",
    user_agent: "",
    metadata: {},
    created_at: "2026-10-27T03:59:00Z",
  },
]

describe("audit export builders", () => {
  it("builds a CSV with a header, one line per entry and RFC 4180 escaping", () => {
    const csv = buildAuditCSV(ENTRIES)
    const lines = csv.trimEnd().split("\r\n")
    expect(lines[0]).toBe(
      "id,created_at,action,actor_user_id,target_type,target_id,request_id,ip,user_agent,metadata",
    )
    expect(lines).toHaveLength(3)
    expect(lines[1]).toContain("version.requirement_created")
    // The metadata cell is JSON in a quoted CSV cell; commas and quotes stay
    // inside the cell.
    expect(lines[1]).toContain("with comma")
    expect(lines[1]).toContain('""status"":""draft""')
    expect(lines[1].startsWith("12,2026-10-27T04:00:00Z,version.requirement_created,1,")).toBe(true)
    // An anonymous actor is exported as an empty cell, not "null".
    expect(lines[2]).toContain("auth.login_failed,,user,,")

    const escaped = buildAuditCSV([{ ...ENTRIES[0], action: 'a,b"c' }])
      .trimEnd()
      .split("\r\n")[1]
    expect(escaped).toContain('"a,b""c"')
  })

  it("builds a Markdown table with escaped pipes and newlines", () => {
    const markdown = buildAuditMarkdown([{ ...ENTRIES[0], action: "a|b\nc" }, ENTRIES[1]])
    expect(markdown).toContain("| ID | 时间 | 操作 | 操作者 | 目标 | 请求 ID |")
    expect(markdown).toContain("| 12 |")
    expect(markdown).toContain("a\\|b c")
    expect(markdown).not.toContain("a|b\nc")
    expect(markdown).toContain("| 11 |")
    expect(markdown).toContain("auth.login_failed")
  })

  it("handles an empty page with only the header", () => {
    expect(buildAuditCSV([]).trim()).toBe(
      "id,created_at,action,actor_user_id,target_type,target_id,request_id,ip,user_agent,metadata",
    )
    expect(buildAuditMarkdown([]).split("\n")[0]).toBe(
      "| ID | 时间 | 操作 | 操作者 | 目标 | 请求 ID |",
    )
  })
})

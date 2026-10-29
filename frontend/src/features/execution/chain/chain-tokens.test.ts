import { describe, expect, it } from "vitest"

import type { CaseRunStatus } from "../types"
import {
  CASE_STATUS_TOKENS,
  CHAIN_COLOR_STYLES,
  CHAIN_TOKENS,
  type ChainStatusToken,
  type ChainToken,
  EDGE_KIND_TOKENS,
  getCaseStatusToken,
  getEdgeKindToken,
  getIssueSeverityToken,
  getIssueStatusToken,
  getNodeKindToken,
  getRequirementStatusToken,
  getResultStatusToken,
  ISSUE_SEVERITY_TOKENS,
  ISSUE_STATUS_TOKENS,
  isResultStatus,
  NODE_KIND_TOKENS,
  REQUIREMENT_STATUS_TOKENS,
  RESULT_STATUS_TOKENS,
} from "./chain-tokens"
import type { ChainEdgeKindDTO, ChainNodeKindDTO } from "./chain-types"

const NODE_KINDS: ChainNodeKindDTO[] = ["requirement", "case", "result", "issue"]
const EDGE_KINDS: ChainEdgeKindDTO[] = [
  "requirement_case",
  "case_result",
  "result_issue",
  "case_issue",
  "impact_case",
]
const RESULT_STATUSES: CaseRunStatus[] = [
  "pending",
  "running",
  "passed",
  "failed",
  "blocked",
  "inconclusive",
  "skipped",
  "interrupted",
]

function expectToken(token: ChainToken): void {
  expect(token.icon).toBeTruthy()
  expect(token.label.trim()).not.toBe("")
  expect(token.color.trim()).not.toBe("")
  expect(token.stroke).toMatch(/^var\(/)
  expect(token.textClass.trim()).not.toBe("")
  expect(token.badgeClass.trim()).not.toBe("")
  expect(token.dotClass.trim()).not.toBe("")
  expect(token.ringClass.trim()).not.toBe("")
  expect(CHAIN_COLOR_STYLES[token.color]).toBeDefined()
  expect(CHAIN_COLOR_STYLES[token.color].stroke).toBe(token.stroke)
}

function expectUniqueColors(tokens: readonly ChainToken[]): void {
  const colors = tokens.map((token) => token.color)
  expect(new Set(colors).size).toBe(colors.length)
}

function expectStatusToken(token: ChainStatusToken): void {
  expectToken(token)
  expect(["success", "warning", "danger", "info", "neutral"]).toContain(token.badgeVariant)
}

describe("CHAIN_TOKENS node and edge kinds", () => {
  it("describes every node kind with an icon, Chinese label and unique colour", () => {
    expect(Object.keys(NODE_KIND_TOKENS).sort()).toEqual([...NODE_KINDS].sort())

    for (const kind of NODE_KINDS) {
      expectToken(NODE_KIND_TOKENS[kind])
    }
    expectUniqueColors(Object.values(NODE_KIND_TOKENS))
    expect(NODE_KIND_TOKENS.requirement.label).toBe("需求")
    expect(NODE_KIND_TOKENS.case.label).toBe("用例")
    expect(NODE_KIND_TOKENS.result.label).toBe("结果")
    expect(NODE_KIND_TOKENS.issue.label).toBe("问题")
  })

  it("describes every semantic edge kind with an icon, Chinese label and unique colour", () => {
    expect(Object.keys(EDGE_KIND_TOKENS).sort()).toEqual([...EDGE_KINDS].sort())

    for (const kind of EDGE_KINDS) {
      expectToken(EDGE_KIND_TOKENS[kind])
    }
    expectUniqueColors(Object.values(EDGE_KIND_TOKENS))
    expect(EDGE_KIND_TOKENS.requirement_case.label).toBe("覆盖")
    expect(EDGE_KIND_TOKENS.impact_case.label).toBe("影响")
  })
})

describe("CHAIN_TOKENS statuses and severities", () => {
  it("covers all eight result statuses with StatusBadge semantics", () => {
    expect(Object.keys(RESULT_STATUS_TOKENS).sort()).toEqual([...RESULT_STATUSES].sort())

    for (const status of RESULT_STATUSES) {
      expectStatusToken(RESULT_STATUS_TOKENS[status])
    }
    expectUniqueColors(Object.values(RESULT_STATUS_TOKENS))

    expect(RESULT_STATUS_TOKENS.pending.badgeVariant).toBe("info")
    expect(RESULT_STATUS_TOKENS.running.badgeVariant).toBe("info")
    expect(RESULT_STATUS_TOKENS.passed.badgeVariant).toBe("success")
    expect(RESULT_STATUS_TOKENS.failed.badgeVariant).toBe("danger")
    expect(RESULT_STATUS_TOKENS.blocked.badgeVariant).toBe("neutral")
    expect(RESULT_STATUS_TOKENS.inconclusive.badgeVariant).toBe("warning")
    expect(RESULT_STATUS_TOKENS.skipped.badgeVariant).toBe("neutral")
    expect(RESULT_STATUS_TOKENS.interrupted.badgeVariant).toBe("danger")
  })

  it("covers every issue severity and issue status", () => {
    for (const token of Object.values(ISSUE_SEVERITY_TOKENS)) expectStatusToken(token)
    for (const token of Object.values(ISSUE_STATUS_TOKENS)) expectStatusToken(token)
    expectUniqueColors(Object.values(ISSUE_SEVERITY_TOKENS))
    expectUniqueColors(Object.values(ISSUE_STATUS_TOKENS))

    expect(ISSUE_SEVERITY_TOKENS.blocker.label).toBe("阻断")
    expect(ISSUE_SEVERITY_TOKENS.major.label).toBe("严重")
    expect(ISSUE_SEVERITY_TOKENS.minor.label).toBe("一般")
    expect(ISSUE_SEVERITY_TOKENS.info.label).toBe("提示")
    expect(ISSUE_SEVERITY_TOKENS.blocker.badgeVariant).toBe("danger")
    expect(ISSUE_SEVERITY_TOKENS.major.badgeVariant).toBe("warning")

    expect(ISSUE_STATUS_TOKENS.open.label).toBe("待确认")
    expect(ISSUE_STATUS_TOKENS.confirmed.label).toBe("已确认")
    expect(ISSUE_STATUS_TOKENS.dismissed.label).toBe("已驳回")
  })

  it("covers requirement and case status labels with StatusBadge semantics", () => {
    for (const token of Object.values(REQUIREMENT_STATUS_TOKENS)) expectStatusToken(token)
    for (const token of Object.values(CASE_STATUS_TOKENS)) expectStatusToken(token)
    expectUniqueColors(Object.values(REQUIREMENT_STATUS_TOKENS))
    expectUniqueColors(Object.values(CASE_STATUS_TOKENS))

    expect(REQUIREMENT_STATUS_TOKENS.clarified.label).toBe("已澄清")
    expect(REQUIREMENT_STATUS_TOKENS.draft.badgeVariant).toBe("neutral")
    expect(REQUIREMENT_STATUS_TOKENS.clarified.badgeVariant).toBe("info")
    expect(REQUIREMENT_STATUS_TOKENS.verified.badgeVariant).toBe("success")
    expect(REQUIREMENT_STATUS_TOKENS.obsolete.badgeVariant).toBe("neutral")

    expect(CASE_STATUS_TOKENS.needs_revalidation.label).toBe("需重验")
    expect(CASE_STATUS_TOKENS.executed.badgeVariant).toBe("success")
    expect(CASE_STATUS_TOKENS.rejected.badgeVariant).toBe("danger")
    expect(CASE_STATUS_TOKENS.needs_revalidation.badgeVariant).toBe("warning")
  })

  it("exposes the aggregate palette consumed by the canvas", () => {
    expect(CHAIN_TOKENS.nodes).toBe(NODE_KIND_TOKENS)
    expect(CHAIN_TOKENS.edges).toBe(EDGE_KIND_TOKENS)
    expect(CHAIN_TOKENS.requirementStatus).toBe(REQUIREMENT_STATUS_TOKENS)
    expect(CHAIN_TOKENS.caseStatus).toBe(CASE_STATUS_TOKENS)
    expect(CHAIN_TOKENS.resultStatus).toBe(RESULT_STATUS_TOKENS)
    expect(CHAIN_TOKENS.issueSeverity).toBe(ISSUE_SEVERITY_TOKENS)
    expect(CHAIN_TOKENS.issueStatus).toBe(ISSUE_STATUS_TOKENS)
  })
})

describe("token getters", () => {
  it("looks every known value up", () => {
    expect(getNodeKindToken("issue").label).toBe("问题")
    expect(getEdgeKindToken("impact_case").label).toBe("影响")
    expect(getResultStatusToken("failed").label).toBe("失败")
    expect(getIssueSeverityToken("blocker").label).toBe("阻断")
    expect(getIssueStatusToken("open").label).toBe("待确认")
    expect(getRequirementStatusToken("verified").label).toBe("已验证")
  })

  it("falls back to a neutral token that keeps the raw label", () => {
    const unknown = getResultStatusToken("unknown_status")
    expect(unknown.label).toBe("unknown_status")
    expect(unknown.badgeVariant).toBe("neutral")

    const empty = getResultStatusToken(undefined)
    expect(empty.label).toBe("未知")
    expect(empty.icon).toBeTruthy()
    expect(empty.color).toBe("slate")
  })

  it("falls back for unknown issue severity and case status", () => {
    expect(getCaseStatusToken("new_state").label).toBe("new_state")
    expect(getIssueSeverityToken("critical").label).toBe("critical")
    expect(getIssueStatusToken("").label).toBe("—")
  })

  it("recognises exactly the eight result statuses", () => {
    for (const status of RESULT_STATUSES) expect(isResultStatus(status)).toBe(true)
    expect(isResultStatus("mystery")).toBe(false)
    expect(isResultStatus("")).toBe(false)
  })
})

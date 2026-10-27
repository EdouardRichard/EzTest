import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import type { UnmappedChange } from "./types"

import { MAX_UNMAPPED_ENTRIES, UnmappedChanges } from "./UnmappedChanges"

const ENTRIES: UnmappedChange[] = [
  {
    path: "src/legacy/refund.ts",
    status: "D",
    reason: "deleted_file",
    suggestion: "人工确认删除影响",
  },
  {
    path: "assets/logo.png",
    status: "M",
    reason: "unsupported_content",
    suggestion: "人工确认图片变更影响",
  },
  {
    path: "src/utils/format.ts",
    status: "M",
    reason: "no_fact_or_anchor",
    suggestion: "人工确认影响",
  },
  {
    path: "src/legacy/report.ts",
    status: "M",
    reason: "ai_unavailable",
    suggestion: "AI 恢复后重新分析",
  },
  {
    path: "src/api/refund.ts",
    status: "M",
    reason: "unmatched_contract",
    suggestion: "检查调用点与后端接口是否一致",
  },
]

function capEntries(count: number): UnmappedChange[] {
  return Array.from({ length: count }, (_value, index) => ({
    path: `src/generated/file-${index}.ts`,
    status: "M",
    reason: "no_fact_or_anchor",
    suggestion: "",
  }))
}

describe("UnmappedChanges", () => {
  afterEach(() => {
    cleanup()
  })

  it("renders every unmapped reason with path, status and suggestion", () => {
    render(<UnmappedChanges entries={ENTRIES} />)

    expect(screen.getByRole("heading", { name: "未映射变更" })).toBeTruthy()
    expect(screen.getByText("共 5 个文件")).toBeTruthy()
    expect(screen.getByText("src/legacy/refund.ts")).toBeTruthy()
    expect(screen.getByText("文件已删除")).toBeTruthy()
    expect(screen.getByText("内容不支持分析")).toBeTruthy()
    expect(screen.getByText("无事实或锚点覆盖")).toBeTruthy()
    expect(screen.getByText("AI 不可用")).toBeTruthy()
    expect(screen.getByText("契约未匹配")).toBeTruthy()
    expect(screen.getByText("建议：人工确认删除影响")).toBeTruthy()
    expect(screen.getByText("建议：检查调用点与后端接口是否一致")).toBeTruthy()
    expect(screen.getByText("删除")).toBeTruthy()
    expect(screen.getAllByText("修改")).toHaveLength(4)
  })

  it("shows the truncation hint and an empty state", () => {
    const truncated = render(<UnmappedChanges entries={ENTRIES.slice(0, 1)} truncated />)
    expect(screen.getByText("未映射变更超过上限，仅展示前 1 条。")).toBeTruthy()
    truncated.unmount()

    render(<UnmappedChanges entries={[]} />)
    expect(screen.getByText("没有未映射变更")).toBeTruthy()
  })

  it("warns when the backend marks the exact-cap list truncated", () => {
    render(<UnmappedChanges entries={capEntries(MAX_UNMAPPED_ENTRIES)} truncated />)

    expect(
      screen.getByText(`未映射变更超过上限，仅展示前 ${MAX_UNMAPPED_ENTRIES} 条。`),
    ).toBeTruthy()
  })

  it("warns for a list longer than the cap even when the flag is false", () => {
    render(<UnmappedChanges entries={capEntries(MAX_UNMAPPED_ENTRIES + 1)} truncated={false} />)

    expect(
      screen.getByText(`未映射变更超过上限，仅展示前 ${MAX_UNMAPPED_ENTRIES + 1} 条。`),
    ).toBeTruthy()
  })

  it("does not claim truncation when the list is exactly at the cap", () => {
    render(<UnmappedChanges entries={capEntries(MAX_UNMAPPED_ENTRIES)} />)

    expect(screen.getByText(`共 ${MAX_UNMAPPED_ENTRIES} 个文件`)).toBeTruthy()
    expect(screen.queryByText(/超过上限/)).toBeNull()
  })
})

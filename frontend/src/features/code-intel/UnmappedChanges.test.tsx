import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import type { UnmappedChange } from "./types"

import { UnmappedChanges } from "./UnmappedChanges"

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
})

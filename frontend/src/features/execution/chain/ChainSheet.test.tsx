import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionStreamState } from "@/features/versions/useVersionStream"
import type { CaseRun, EvidenceView } from "../types"
import { ChainSheet } from "./ChainSheet"
import type { ChainSnapshotNode } from "./chain-types"

const mocks = vi.hoisted(() => ({
  useCaseRun: vi.fn(),
}))

vi.mock("../hooks/useExecution", () => ({
  useCaseRun: mocks.useCaseRun,
}))

const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"
const C1 = "1844674407370955201"
const CR1 = "1844674407370955301"
const ISSUE_1 = "1844674407370955401"
const EVIDENCE_ID = "1844674407370955402"

const STREAM = { events: [], status: "live", reconnect: vi.fn() } as unknown as VersionStreamState

const EVIDENCE: EvidenceView = {
  id: EVIDENCE_ID,
  kind: "screenshot",
  mime: "image/png",
  size_bytes: 2048,
  sha256: "a".repeat(64),
}

function caseRun(): CaseRun {
  return {
    id: CR1,
    case_node_id: C1,
    status: "failed",
    batch_id: BATCH_ID,
    case_revision: 1,
    attempt: 2,
    failure_class: "application",
    failure_reason: "",
    summary: "提交订单返回 500",
    duration_ms: 1200,
    title: "提交订单",
    session: null,
    checkpoint: { completed_seqs: [], cleanup_required: false, interrupted_step_seq: null },
    steps: [],
    assertions: [],
    evidence: [EVIDENCE],
    pending_takeover: null,
  }
}

const RESULT_NODE: ChainSnapshotNode = {
  id: `result:${CR1}`,
  kind: "result",
  title: "提交订单",
  status: "failed",
  caseRunId: CR1,
  caseNodeId: C1,
  batchId: BATCH_ID,
  attempt: 2,
  durationMs: 1200,
  failureClass: "application",
  evidenceCount: 1,
  summary: "提交订单返回 500",
}

const ISSUE_NODE: ChainSnapshotNode = {
  id: `issue:${ISSUE_1}`,
  kind: "issue",
  title: "提交订单失败",
  status: "open",
  severity: "major",
  confidence: "high",
  category: "defect",
  caseNodeId: C1,
  caseRunId: CR1,
  evidenceCount: 1,
  codeRefCount: 2,
}

function sheetResult(detail: CaseRun | null) {
  return {
    caseRun: detail,
    loading: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn(),
  }
}

type HarnessProps = {
  node: ChainSnapshotNode
  resultBatchId?: string
  onOpenEvidence?: (evidenceId: string) => void
  onNavigate?: (target: { kind: string; id: string }) => void
}

/** Controlled open/close so the Sheet can exercise focus return to its trigger. */
function SheetHarness({ node, ...rest }: HarnessProps) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <button type="button" data-testid="sheet-trigger" onClick={() => setOpen(true)}>
        打开节点
      </button>
      {open ? (
        <ChainSheet
          node={node}
          versionId={VERSION_ID}
          batchId={BATCH_ID}
          stream={STREAM}
          resultBatchId={rest.resultBatchId}
          onClose={() => setOpen(false)}
          onOpenEvidence={rest.onOpenEvidence}
          onNavigate={rest.onNavigate}
        />
      ) : null}
    </div>
  )
}

function openSheet(props: HarnessProps) {
  const utils = render(<SheetHarness {...props} />)
  const trigger = screen.getByTestId("sheet-trigger")
  trigger.focus()
  fireEvent.click(trigger)
  return { ...utils, trigger }
}

beforeEach(() => {
  mocks.useCaseRun.mockReset()
  mocks.useCaseRun.mockReturnValue(sheetResult(null))
})

afterEach(() => {
  cleanup()
})

describe("ChainSheet", () => {
  it("is a modal dialog that takes focus on open and returns it on close", () => {
    const { trigger } = openSheet({ node: RESULT_NODE })

    const sheet = screen.getByTestId("chain-sheet")
    expect(sheet.getAttribute("role")).toBe("dialog")
    expect(sheet.getAttribute("aria-modal")).toBe("true")
    const close = screen.getByTestId("chain-sheet-close")
    expect(document.activeElement).toBe(close)

    fireEvent.keyDown(close, { key: "Escape" })

    expect(screen.queryByTestId("chain-sheet")).toBeNull()
    expect(document.activeElement).toBe(trigger)
  })

  it("closes on the overlay but keeps its content inside the dialog", () => {
    const { trigger } = openSheet({ node: RESULT_NODE })

    fireEvent.click(screen.getByTestId("chain-sheet-overlay"))

    expect(screen.queryByTestId("chain-sheet")).toBeNull()
    expect(document.activeElement).toBe(trigger)
  })

  it("traps Tab inside the dialog", () => {
    openSheet({ node: RESULT_NODE })

    const sheet = screen.getByTestId("chain-sheet")
    const close = within(sheet).getByTestId("chain-sheet-close")
    const focusables = sheet.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])',
    )
    const last = focusables[focusables.length - 1]
    expect(last).not.toBe(close)

    fireEvent.keyDown(close, { key: "Tab", shiftKey: true })
    expect(document.activeElement).toBe(last)

    fireEvent.keyDown(last, { key: "Tab" })
    expect(document.activeElement).toBe(close)
  })

  it("shows the result status, summary, attempt and batch", () => {
    openSheet({ node: RESULT_NODE })

    const sheet = screen.getByTestId("chain-sheet")
    expect(sheet.getAttribute("data-kind")).toBe("result")
    expect(sheet.textContent).toContain("提交订单")
    expect(sheet.textContent).toContain("失败")
    expect(sheet.textContent).toContain("提交订单返回 500")
    expect(within(sheet).getByTestId("chain-sheet-attempt").textContent).toContain("第 2 次尝试")
    expect(sheet.textContent).toContain(BATCH_ID)
  })

  it("loads evidence lazily through the caseRunId and opens it", () => {
    mocks.useCaseRun.mockReturnValue(sheetResult(caseRun()))
    const onOpenEvidence = vi.fn()
    openSheet({ node: RESULT_NODE, onOpenEvidence })

    // The chain itself never reads evidence; the Sheet opens with a disabled read.
    expect(mocks.useCaseRun).toHaveBeenLastCalledWith(VERSION_ID, BATCH_ID, "", STREAM)
    expect(screen.getByTestId("chain-sheet-load-evidence").textContent).toContain("证据")

    fireEvent.click(screen.getByTestId("chain-sheet-load-evidence"))
    expect(mocks.useCaseRun).toHaveBeenLastCalledWith(VERSION_ID, BATCH_ID, CR1, STREAM)

    fireEvent.click(screen.getByTestId(`chain-sheet-evidence-${EVIDENCE_ID}`))
    expect(onOpenEvidence).toHaveBeenCalledWith(EVIDENCE_ID)
  })

  it("offers the requirement/case/result deep links", () => {
    const onNavigate = vi.fn()
    const { unmount } = openSheet({ node: RESULT_NODE, onNavigate })

    fireEvent.click(screen.getByTestId("chain-sheet-open-case"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "case", id: C1 })
    fireEvent.click(screen.getByTestId("chain-sheet-open-result"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "result", id: CR1 })
    unmount()

    const requirement = openSheet({
      node: {
        id: "requirement:1844674407370955101",
        kind: "requirement",
        title: "登录需求",
        status: "clarified",
      },
      onNavigate,
    })
    fireEvent.click(screen.getByTestId("chain-sheet-open-requirement"))
    expect(onNavigate).toHaveBeenCalledWith({
      kind: "requirement",
      id: "1844674407370955101",
    })
    requirement.unmount()

    openSheet({
      node: {
        id: `case:${C1}`,
        kind: "case",
        title: "登录成功",
        status: "executed",
        requirementIds: ["1844674407370955101"],
      },
      onNavigate,
    })
    fireEvent.click(screen.getByTestId("chain-sheet-open-case"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "case", id: C1 })
  })

  it("shows the issue severity/status/code location and its deep links", () => {
    const onNavigate = vi.fn()
    openSheet({ node: ISSUE_NODE, onNavigate })

    const sheet = screen.getByTestId("chain-sheet")
    expect(sheet.getAttribute("data-kind")).toBe("issue")
    expect(sheet.textContent).toContain("严重")
    expect(sheet.textContent).toContain("待确认")
    expect(within(sheet).getByTestId("chain-sheet-code").textContent).toContain("代码位置")
    expect(within(sheet).getByTestId("chain-sheet-code").textContent).toContain("2")

    fireEvent.click(screen.getByTestId("chain-sheet-open-issue"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "issue", id: ISSUE_1 })
    fireEvent.click(screen.getByTestId("chain-sheet-open-case"))
    expect(onNavigate).toHaveBeenCalledWith({ kind: "case", id: C1 })
  })

  it("keeps the unavailable evidence wording for an issue without a caseRunId", () => {
    openSheet({
      node: { ...ISSUE_NODE, caseRunId: "" },
    })

    expect(screen.queryByTestId("chain-sheet-load-evidence")).toBeNull()
    const hint = screen.getByTestId("chain-sheet-evidence-unavailable")
    expect(hint.textContent).toContain("无法加载证据")
    expect(hint.textContent).toContain("查看问题")
    expect(mocks.useCaseRun).toHaveBeenLastCalledWith(VERSION_ID, expect.anything(), "", STREAM)
  })
})

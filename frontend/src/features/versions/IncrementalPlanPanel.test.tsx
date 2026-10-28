import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  getIncrementalPlan: vi.fn(),
  createIncrementalBatch: vi.fn(),
}))

vi.mock("./api", () => ({
  getIncrementalPlan: mocks.getIncrementalPlan,
  createIncrementalBatch: mocks.createIncrementalBatch,
}))

import { IncrementalPlanPanel } from "./IncrementalPlanPanel"
import type { IncrementalPlan } from "./types"

const VERSION_ID = "1844674407370955161"
const PARENT_BATCH_ID = "1844674407370955301"
const NEW_CASE_ID = "1844674407370955302"
const AFFECTED_CASE_ID = "1844674407370955303"
const LOW_CASE_ID = "1844674407370955304"
const RUN_ID = "1844674407370955305"
const CREATED_BATCH_ID = "1844674407370955309"

const PLAN: IncrementalPlan = {
  versionId: VERSION_ID,
  phase: "designing",
  parentBatchId: PARENT_BATCH_ID,
  run: { id: RUN_ID, kind: "incremental_design", status: "done", result: {} },
  newCases: [
    {
      nodeId: NEW_CASE_ID,
      title: "新增退款用例",
      priority: "P1",
      confidence: "high",
      requirementIds: ["1844674407370956001"],
      status: "supplemented",
    },
  ],
  affectedCases: [
    {
      nodeId: AFFECTED_CASE_ID,
      title: "受影响的下单用例",
      confidence: "medium",
      reason: "退款接口变更",
      source: "code",
      selected: true,
    },
    {
      nodeId: LOW_CASE_ID,
      title: "低置信用例",
      confidence: "low",
      reason: "可能受影响",
      source: "requirement",
      selected: false,
    },
  ],
  unmappedChanges: [{ path: "src/api/refund.ts", summary: "退款接口新增 reason 参数" }],
  defaultCaseIds: [NEW_CASE_ID, AFFECTED_CASE_ID],
  warnings: [{ code: "impact_unavailable", message: "影响分析不可用，请人工核对" }],
  degraded: true,
}

function checkbox(name: RegExp): HTMLInputElement {
  return screen.getByRole("checkbox", { name }) as HTMLInputElement
}

describe("IncrementalPlanPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.getIncrementalPlan.mockResolvedValue(PLAN)
    mocks.createIncrementalBatch.mockResolvedValue({
      id: CREATED_BATCH_ID,
      batch_no: 2,
      status: "planned",
      kind: "incremental",
      parentBatchId: PARENT_BATCH_ID,
    })
  })

  afterEach(cleanup)

  it("renders the plan with high/medium defaults and low-confidence unchecked", async () => {
    render(<IncrementalPlanPanel versionId={VERSION_ID} />)

    expect(await screen.findByTestId("incremental-plan")).toBeTruthy()
    expect(mocks.getIncrementalPlan).toHaveBeenCalledWith(VERSION_ID)

    const newCase = checkbox(/新增退款用例/)
    expect(newCase.checked).toBe(true)
    expect(newCase.value).toBe(NEW_CASE_ID)
    expect(screen.getByTestId("incremental-plan-new-case")).toBeTruthy()

    const affected = checkbox(/受影响的下单用例/)
    expect(affected.checked).toBe(true)
    expect(affected.value).toBe(AFFECTED_CASE_ID)
    expect(screen.getAllByTestId("incremental-plan-affected-case")).toHaveLength(2)

    const low = checkbox(/低置信用例/)
    expect(low.checked).toBe(false)

    expect(screen.getByText("高")).toBeTruthy()
    expect(screen.getByText("中")).toBeTruthy()
    expect(screen.getByText("低")).toBeTruthy()
    expect(screen.getByText(/退款接口变更/)).toBeTruthy()
    // Plan enum values are translated for display; unknown values stay verbatim.
    expect(screen.getByText("P1 高 · 已补全")).toBeTruthy()
    expect(screen.getByText("代码变更 · 退款接口变更")).toBeTruthy()
    expect(screen.getByText("增量设计")).toBeTruthy()

    expect(screen.getByTestId("incremental-plan-warning").textContent).toContain("影响分析不可用")
    expect(screen.getByTestId("incremental-plan-unmapped").textContent).toContain(
      "src/api/refund.ts",
    )
    expect(screen.getByText(/AI 分析不可用/)).toBeTruthy()
  })

  it("creates an incremental batch with the selected string case ids", async () => {
    const onCreated = vi.fn()
    render(<IncrementalPlanPanel versionId={VERSION_ID} onCreated={onCreated} />)

    await screen.findByTestId("incremental-plan")

    // Uncheck the default-selected affected case, then include the low one.
    fireEvent.click(checkbox(/受影响的下单用例/))
    fireEvent.click(checkbox(/低置信用例/))

    fireEvent.click(screen.getByTestId("incremental-plan-create-batch"))

    await waitFor(() =>
      expect(mocks.createIncrementalBatch).toHaveBeenCalledWith(VERSION_ID, {
        caseIds: [NEW_CASE_ID, LOW_CASE_ID],
        parentBatchId: PARENT_BATCH_ID,
      }),
    )
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1))
    expect(onCreated.mock.calls[0][0].id).toBe(CREATED_BATCH_ID)
    expect(typeof onCreated.mock.calls[0][0].id).toBe("string")
    expect(await screen.findByText(/已创建增量批次/)).toBeTruthy()
  })

  it("shows the create failure and keeps the selection", async () => {
    const onCreated = vi.fn()
    mocks.createIncrementalBatch.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "continuing batch conflict",
        details: { reason: "continuing_batch_conflict" },
      }),
    )

    render(<IncrementalPlanPanel versionId={VERSION_ID} onCreated={onCreated} />)
    await screen.findByTestId("incremental-plan")
    fireEvent.click(screen.getByTestId("incremental-plan-create-batch"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("增量批次创建失败")
    expect(onCreated).not.toHaveBeenCalled()
    expect(checkbox(/新增退款用例/).checked).toBe(true)
  })

  it("falls back to new + high/medium affected cases when defaultCaseIds is absent", async () => {
    mocks.getIncrementalPlan.mockResolvedValue({ ...PLAN, defaultCaseIds: [] })

    render(<IncrementalPlanPanel versionId={VERSION_ID} />)

    await screen.findByTestId("incremental-plan")
    expect(checkbox(/新增退款用例/).checked).toBe(true)
    expect(checkbox(/受影响的下单用例/).checked).toBe(true)
    expect(checkbox(/低置信用例/).checked).toBe(false)
  })

  it("shows an empty plan without inventing data", async () => {
    mocks.getIncrementalPlan.mockResolvedValue({
      versionId: VERSION_ID,
      phase: "",
      parentBatchId: null,
      run: null,
      newCases: [],
      affectedCases: [],
      unmappedChanges: [],
      defaultCaseIds: [],
      warnings: [],
      degraded: false,
    })

    render(<IncrementalPlanPanel versionId={VERSION_ID} />)

    expect(await screen.findByText("暂无可用的增量计划")).toBeTruthy()
    expect(screen.queryByTestId("incremental-plan-create-batch")).toBeNull()
  })

  it("reports a load failure and retries", async () => {
    mocks.getIncrementalPlan
      .mockRejectedValueOnce(
        new ApiError({ status: 0, code: "network_error", message: "connection refused" }),
      )
      .mockResolvedValueOnce(PLAN)

    render(<IncrementalPlanPanel versionId={VERSION_ID} />)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载增量计划失败")

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("incremental-plan")).toBeTruthy()
    expect(mocks.getIncrementalPlan).toHaveBeenCalledTimes(2)
  })
})

import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { DesignMindmap } from "./DesignMindmap"
import { designGraphFixture } from "./test-fixtures"
import type { DesignCaseSummary, DesignGraph } from "./types"
import { useSelection } from "./useSelection"

afterEach(cleanup)

describe("useSelection", () => {
  it("starts empty and toggles case ids in selection order", () => {
    const { result } = renderHook(() => useSelection())

    expect(result.current.selectedIds).toEqual([])
    act(() => result.current.toggle("7001"))
    act(() => result.current.toggle("7003"))
    expect(result.current.selectedIds).toEqual(["7001", "7003"])
    expect(result.current.isSelected("7003")).toBe(true)

    act(() => result.current.toggle("7001"))
    expect(result.current.selectedIds).toEqual(["7003"])
    expect(result.current.isSelected("7001")).toBe(false)
  })

  it("clears every selected id", () => {
    const { result } = renderHook(() => useSelection())

    act(() => result.current.toggle("7001"))
    act(() => result.current.toggle("7005"))
    act(() => result.current.clear())

    expect(result.current.selectedIds).toEqual([])
  })
})

/** A graph with a fourth case so "only these three" is distinguishable from "all". */
function graphWithFourthCase(): DesignGraph {
  const extraCase: DesignCaseSummary = {
    case_id: "7007",
    current_version_id: "7008",
    revision: 1,
    title: "退款超时重试",
    priority: "P2",
    status: "confirmed",
    default_version_sut_id: "1002",
    version_sut_ids: ["1002"],
    page_ids: [],
    api_ids: [],
    step_count: 1,
    assertion_count: 0,
    created_at: designGraphFixture.generated_at,
  }
  return {
    ...designGraphFixture,
    requirements: designGraphFixture.requirements.map((requirement) =>
      requirement.requirement_id !== "6001"
        ? requirement
        : {
            ...requirement,
            scenarios: requirement.scenarios.map((scenario) =>
              scenario.id !== "5002"
                ? scenario
                : { ...scenario, cases: [...scenario.cases, extraCase] },
            ),
          },
    ),
  }
}

const BATCH_GRAPH = graphWithFourthCase()

function renderMindmap(overrides: Partial<Parameters<typeof DesignMindmap>[0]> = {}) {
  const props: Parameters<typeof DesignMindmap>[0] = {
    graph: BATCH_GRAPH,
    selectedCaseId: null,
    onSelectCase: vi.fn(),
    onConfirmCase: vi.fn(),
    onConfirmScenario: vi.fn(),
    onConfirmRequirement: vi.fn(),
    onBatchConfirmCases: vi.fn(),
    onBatchExecuteCases: vi.fn(),
    onRequestAiChange: vi.fn(),
    ...overrides,
  }
  render(<DesignMindmap {...props} />)
  return props
}

async function selectCases(...caseIds: string[]): Promise<void> {
  for (const caseId of caseIds) {
    fireEvent.click(await screen.findByTestId(`case-checkbox-${caseId}`))
  }
}

describe("DesignMindmap multi-select batch actions", () => {
  it("shows how many cases are selected", async () => {
    renderMindmap()

    expect(await screen.findByTestId("case-selection-count")).toHaveTextContent("已选 0 条")
    await selectCases("7001", "7003", "7005")
    expect(screen.getByTestId("case-selection-count")).toHaveTextContent("已选 3 条")
  })

  it("batch confirms exactly the selected case ids", async () => {
    const props = renderMindmap()

    await selectCases("7001", "7003", "7005")
    fireEvent.click(screen.getByTestId("case-batch-confirm"))

    expect(props.onBatchConfirmCases).toHaveBeenCalledTimes(1)
    expect(props.onBatchConfirmCases).toHaveBeenCalledWith(["7001", "7003", "7005"])
    expect(props.onBatchConfirmCases).not.toHaveBeenCalledWith(["7001", "7003", "7005", "7007"])
  })

  it("batch executes exactly the selection, including draft cases", async () => {
    const props = renderMindmap()

    await selectCases("7003", "7007")
    expect(screen.getByTestId("case-batch-execute")).toBeEnabled()

    // Draft cases are allowed: the explicit case gate confirms the selection
    // before it creates the execute.batch job (Task 78 / D23).
    await selectCases("7001")
    expect(screen.getByTestId("case-batch-execute")).toBeEnabled()
    fireEvent.click(screen.getByTestId("case-batch-execute"))
    expect(props.onBatchExecuteCases).toHaveBeenCalledWith(["7003", "7007", "7001"])
    expect(props.onBatchExecuteCases).not.toHaveBeenCalledWith(["7003", "7007", "7001", "7005"])
  })

  it("hands the selected cases to the AI modification action", async () => {
    const props = renderMindmap()

    await selectCases("7001", "7005")
    fireEvent.click(screen.getByTestId("case-batch-ai-modify"))

    expect(props.onRequestAiChange).toHaveBeenCalledWith(["7001", "7005"])
  })

  it("clears the selection", async () => {
    renderMindmap()

    await selectCases("7001", "7003")
    fireEvent.click(screen.getByTestId("case-batch-clear"))

    expect(screen.getByTestId("case-selection-count")).toHaveTextContent("已选 0 条")
  })
})

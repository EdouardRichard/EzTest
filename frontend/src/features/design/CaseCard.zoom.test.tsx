import { cleanup, render, screen } from "@testing-library/react"
import type { NodeProps } from "@xyflow/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const zoomState = vi.hoisted(() => ({ value: 1 }))

// Node components render React Flow handles, and the canvas reads the current
// viewport zoom through `useStore`. Stub both so a single card can be rendered
// in isolation and the low-zoom guard (D-09) stays deterministic.
vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@xyflow/react")>()
  const React = await import("react")
  return {
    ...actual,
    Handle: () => React.createElement("span", { "data-testid": "node-handle" }),
    useStore: ((selector: (state: { transform: [number, number, number] }) => unknown) =>
      selector({ transform: [0, 0, zoomState.value] })) as unknown as typeof actual.useStore,
  }
})

import { CaseCard } from "./CaseCard"
import { DesignMindmap, destructiveActionsDisabledForZoom, MIN_ACTION_ZOOM } from "./DesignMindmap"
import type { DesignNodeActions } from "./nodeActions"
import { RequirementNode } from "./RequirementNode"
import { ScenarioNode } from "./ScenarioNode"
import { designGraphFixture } from "./test-fixtures"

const requirement = designGraphFixture.requirements[0]
const scenario = requirement.scenarios[0]
// 7001 is the draft fixture case; only draft cards render a confirm control.
const draftCase = scenario.cases[0]

function nodeActions(overrides: Partial<DesignNodeActions> = {}): DesignNodeActions {
  return {
    onSelectCase: vi.fn(),
    onConfirmCase: vi.fn(),
    onConfirmScenario: vi.fn(),
    onConfirmRequirement: vi.fn(),
    onToggleCaseSelected: vi.fn(),
    confirmingKey: null,
    caseReviewEnabled: true,
    ...overrides,
  }
}

function renderCaseCard(data: Record<string, unknown> = {}): void {
  const props = {
    id: `case-${draftCase.case_id}`,
    type: "case",
    data: {
      kind: "case",
      requirement,
      scenario,
      case: draftCase,
      actions: nodeActions(),
      selected: false,
      batchSelected: false,
      ...data,
    },
  }
  render(<CaseCard {...(props as unknown as NodeProps)} />)
}

function renderRequirementNode(data: Record<string, unknown> = {}): void {
  const props = {
    id: `requirement-${requirement.requirement_id}`,
    type: "requirement",
    data: {
      kind: "requirement",
      requirement,
      actions: nodeActions(),
      ...data,
    },
  }
  render(<RequirementNode {...(props as unknown as NodeProps)} />)
}

function renderScenarioNode(data: Record<string, unknown> = {}): void {
  const props = {
    id: `scenario-${scenario.id}`,
    type: "scenario",
    data: {
      kind: "scenario",
      requirement,
      scenario,
      draftCaseCount: 1,
      actions: nodeActions(),
      ...data,
    },
  }
  render(<ScenarioNode {...(props as unknown as NodeProps)} />)
}

function renderMindmap(): void {
  render(
    <DesignMindmap
      graph={designGraphFixture}
      onSelectCase={vi.fn()}
      onConfirmCase={vi.fn()}
      onConfirmScenario={vi.fn()}
      onConfirmRequirement={vi.fn()}
    />,
  )
}

describe("low-zoom destructive action guard (D-09)", () => {
  beforeEach(() => {
    zoomState.value = 1
  })

  afterEach(cleanup)

  it("defines 0.5 as the minimum action zoom", () => {
    expect(MIN_ACTION_ZOOM).toBe(0.5)
    expect(destructiveActionsDisabledForZoom(0.49)).toBe(true)
    expect(destructiveActionsDisabledForZoom(0.5)).toBe(false)
    expect(destructiveActionsDisabledForZoom(0.8)).toBe(false)
  })

  it("keeps selection but hides the case confirm control when destructive actions are disabled", () => {
    renderCaseCard({ destructiveActionsDisabled: true })

    expect(screen.getByTestId("case-select-7001")).toBeInTheDocument()
    expect(screen.queryByTestId("btn-confirm-case-7001")).not.toBeInTheDocument()
  })

  it("still shows the case confirm control by default", () => {
    renderCaseCard()

    expect(screen.getByTestId("btn-confirm-case-7001")).toBeInTheDocument()
  })

  it("hides the requirement and scenario confirm controls when disabled", () => {
    renderRequirementNode({ destructiveActionsDisabled: true })
    renderScenarioNode({ destructiveActionsDisabled: true })

    expect(screen.getByTestId("requirement-node-6001")).toBeInTheDocument()
    expect(screen.getByTestId("scenario-node-5001")).toBeInTheDocument()
    expect(screen.queryByTestId("btn-confirm-requirement-6001")).not.toBeInTheDocument()
    expect(screen.queryByTestId("btn-confirm-scenario-5001")).not.toBeInTheDocument()
  })

  it("hides every node confirm control when the live canvas zoom is below 0.5", async () => {
    zoomState.value = 0.3
    renderMindmap()

    await screen.findByTestId("case-card-7001")
    expect(screen.queryByTestId("btn-confirm-case-7001")).not.toBeInTheDocument()
    expect(screen.queryByTestId("btn-confirm-scenario-5001")).not.toBeInTheDocument()
    expect(screen.queryByTestId("btn-confirm-requirement-6001")).not.toBeInTheDocument()
    // Selection and navigation stay available at any zoom.
    expect(screen.getByTestId("case-select-7001")).toBeInTheDocument()
  })

  it("restores every node confirm control at a readable zoom", async () => {
    zoomState.value = 0.8
    renderMindmap()

    expect(await screen.findByTestId("btn-confirm-case-7001")).toBeInTheDocument()
    expect(screen.getByTestId("btn-confirm-scenario-5001")).toBeInTheDocument()
    expect(screen.getByTestId("btn-confirm-requirement-6001")).toBeInTheDocument()
  })
})

import { cleanup, render, screen } from "@testing-library/react"
import type { NodeProps } from "@xyflow/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

// Stub the React Flow handle/store so a single card renders in isolation
// (same pattern as CaseCard.zoom.test.tsx).
vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@xyflow/react")>()
  const React = await import("react")
  return {
    ...actual,
    Handle: () => React.createElement("span", { "data-testid": "node-handle" }),
    useStore: ((selector: (state: { transform: [number, number, number] }) => unknown) =>
      selector({ transform: [0, 0, 1] })) as unknown as typeof actual.useStore,
  }
})

import { CaseCard } from "./CaseCard"
import { reviewStateLabel, reviewStateTone } from "./judgment"
import type { DesignNodeActions } from "./nodeActions"
import { designGraphFixture } from "./test-fixtures"

const requirement = designGraphFixture.requirements[0]
const scenario = requirement.scenarios[0]
const draftCase = scenario.cases[0]

function nodeActions(): DesignNodeActions {
  return {
    onSelectCase: vi.fn(),
    onConfirmCase: vi.fn(),
    onConfirmScenario: vi.fn(),
    onConfirmRequirement: vi.fn(),
    onToggleCaseSelected: vi.fn(),
    confirmingKey: null,
    caseReviewEnabled: true,
  }
}

function renderCaseCard(caseOverride: Record<string, unknown> = {}): void {
  const props = {
    id: `case-${draftCase.case_id}`,
    type: "case",
    data: {
      kind: "case",
      requirement,
      scenario,
      case: { ...draftCase, ...caseOverride },
      actions: nodeActions(),
      selected: false,
      batchSelected: false,
    },
  }
  render(<CaseCard {...(props as unknown as NodeProps)} />)
}

describe("CaseCard review_state badge", () => {
  afterEach(cleanup)

  it.each(["pending", "pass", "reworking", "escalated_fail", "judge_unavailable"])(
    "renders a distinct badge for %s",
    (state) => {
      renderCaseCard({ review_state: state })
      const badge = screen.getByTestId(`case-review-state-${draftCase.case_id}`)
      expect(badge).toHaveTextContent(reviewStateLabel(state))
      expect(badge).toHaveAttribute("data-tone", reviewStateTone(state))
    },
  )

  it("separates pass from escalated_fail / judge_unavailable by tone", () => {
    renderCaseCard({ review_state: "pass" })
    expect(screen.getByTestId(`case-review-state-${draftCase.case_id}`)).toHaveAttribute(
      "data-tone",
      "success",
    )
    cleanup()
    renderCaseCard({ review_state: "escalated_fail" })
    expect(screen.getByTestId(`case-review-state-${draftCase.case_id}`)).toHaveAttribute(
      "data-tone",
      "danger",
    )
  })

  it("tolerates the degraded empty review_state as unknown", () => {
    renderCaseCard({ review_state: "" })
    const badge = screen.getByTestId(`case-review-state-${draftCase.case_id}`)
    expect(badge).toHaveTextContent("审查未知")
    expect(badge).toHaveAttribute("data-tone", "neutral")
  })

  it("omits the badge when review_state is absent (older fixture)", () => {
    renderCaseCard({ review_state: undefined })
    expect(screen.queryByTestId(`case-review-state-${draftCase.case_id}`)).toBeNull()
  })
})

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { Analysis, Candidate } from "./types"

const mocks = vi.hoisted(() => ({
  patchCandidate: vi.fn(),
  getFile: vi.fn(),
}))

vi.mock("./api", () => ({
  patchCandidate: mocks.patchCandidate,
  getFile: mocks.getFile,
}))

import { ImpactPanel } from "./ImpactPanel"

const REPO_ID = 3
const VERSION_ID = "1844674407370955161"
const ANALYSIS_ID = "1844674407370955162"
const HEAD = "89abcdef0123456789abcdef0123456789abcdef"

function candidateFixture(overrides: Partial<Candidate> = {}): Candidate {
  return {
    id: "7001",
    subjectKind: "requirement",
    subjectId: "9001",
    reason: "changed api POST /api/refund",
    confidence: "high",
    source: "direct",
    evidence: [{ file: "src/api/refund.ts", startLine: 80, endLine: 96 }],
    selected: true,
    ...overrides,
  }
}

function analysisFixture(overrides: Partial<Analysis> = {}): Analysis {
  return {
    id: ANALYSIS_ID,
    versionId: VERSION_ID,
    repoId: REPO_ID,
    baseCommit: "0123456789abcdef0123456789abcdef01234567",
    headCommit: HEAD,
    status: "done",
    degraded: false,
    analysisVersion: 1,
    model: "deepseek-chat",
    promptVersion: "p4.1",
    changeSetId: "1844674407370955163",
    changeSummary: "退款接口新增 reason 参数",
    stats: null,
    factVersions: [],
    candidates: [],
    unmapped: [],
    errors: [],
    createdAt: "2026-10-28T01:00:00Z",
    startedAt: "2026-10-28T01:00:00Z",
    finishedAt: "2026-10-28T01:02:03Z",
    ...overrides,
  }
}

const REQUIREMENT_CANDIDATE = candidateFixture()
const APP_MODEL_CANDIDATE = candidateFixture({
  id: "7002",
  subjectKind: "app_model",
  subjectId: "42",
  reason: "contract POST /api/refund changed",
  confidence: "medium",
  source: "contract",
  selected: false,
  evidence: [{ file: "src/api/refund.ts" }],
})
const LOW_CANDIDATE = candidateFixture({
  id: "7003",
  subjectId: "9003",
  reason: "related to R9001 via depends_on",
  confidence: "low",
  source: "relation",
  selected: false,
})

describe("ImpactPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
  })

  afterEach(() => {
    cleanup()
  })

  it("groups requirement and app-model candidates with metadata", () => {
    render(
      <ImpactPanel
        analysis={analysisFixture({
          candidates: [REQUIREMENT_CANDIDATE, APP_MODEL_CANDIDATE],
        })}
      />,
    )

    expect(screen.getByRole("heading", { name: "影响候选" })).toBeTruthy()
    expect(screen.getByRole("heading", { name: "需求候选" })).toBeTruthy()
    expect(screen.getByRole("heading", { name: "应用模型候选" })).toBeTruthy()

    expect(screen.getByText("changed api POST /api/refund")).toBeTruthy()
    expect(screen.getByText("来源 直接")).toBeTruthy()
    expect(screen.getByText("高置信")).toBeTruthy()
    expect(screen.getByRole("button", { name: "查看证据 src/api/refund.ts:80-96" })).toBeTruthy()

    expect(screen.getByText("contract POST /api/refund changed")).toBeTruthy()
    expect(screen.getByText("来源 契约")).toBeTruthy()
    expect(screen.getByText("中置信")).toBeTruthy()
    expect(screen.getByRole("button", { name: "查看证据 src/api/refund.ts" })).toBeTruthy()

    const requirementBox = screen.getByRole("checkbox", { name: "选择需求 9001" })
    expect((requirementBox as HTMLInputElement).checked).toBe(true)
    const appModelBox = screen.getByRole("checkbox", { name: "选择应用模型 42" })
    expect((appModelBox as HTMLInputElement).checked).toBe(false)
  })

  it("keeps low-confidence candidates unselected by default but selectable", async () => {
    mocks.patchCandidate.mockResolvedValue({ ...LOW_CANDIDATE, selected: true })

    render(<ImpactPanel analysis={analysisFixture({ candidates: [LOW_CANDIDATE] })} />)

    const box = screen.getByRole("checkbox", { name: "选择需求 9003" })
    expect((box as HTMLInputElement).checked).toBe(false)
    expect((box as HTMLInputElement).disabled).toBe(false)
    expect(screen.getByText("低置信")).toBeTruthy()

    fireEvent.click(box)
    await waitFor(() =>
      expect(mocks.patchCandidate).toHaveBeenCalledWith(VERSION_ID, ANALYSIS_ID, "7003", true),
    )
    await waitFor(() => expect((box as HTMLInputElement).checked).toBe(true))
  })

  it("updates optimistically and disables the candidate while saving", async () => {
    const onChanged = vi.fn()
    let resolvePatch: (candidate: Candidate) => void = () => {}
    mocks.patchCandidate.mockReturnValue(
      new Promise<Candidate>((resolve) => {
        resolvePatch = resolve
      }),
    )

    render(
      <ImpactPanel
        analysis={analysisFixture({ candidates: [REQUIREMENT_CANDIDATE] })}
        onChanged={onChanged}
      />,
    )

    const box = screen.getByRole("checkbox", { name: "选择需求 9001" })
    expect((box as HTMLInputElement).checked).toBe(true)
    fireEvent.click(box)

    // The checkbox flips immediately, before the PATCH resolves.
    expect((box as HTMLInputElement).checked).toBe(false)
    expect((box as HTMLInputElement).disabled).toBe(true)

    resolvePatch({ ...REQUIREMENT_CANDIDATE, selected: false })
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    expect((box as HTMLInputElement).checked).toBe(false)
    expect((box as HTMLInputElement).disabled).toBe(false)
  })

  it("rolls back and shows an error when the PATCH fails", async () => {
    mocks.patchCandidate.mockRejectedValue(
      new ApiError({ status: 409, code: "conflict", message: "analysis not done" }),
    )

    render(
      <ImpactPanel
        analysis={analysisFixture({
          candidates: [REQUIREMENT_CANDIDATE, APP_MODEL_CANDIDATE],
        })}
      />,
    )

    const box = screen.getByRole("checkbox", { name: "选择需求 9001" })
    fireEvent.click(box)
    expect((box as HTMLInputElement).checked).toBe(false)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("保存候选失败")
    await waitFor(() => expect((box as HTMLInputElement).checked).toBe(true))
    expect((box as HTMLInputElement).disabled).toBe(false)
  })

  it("shows the degraded banner for a T0-only analysis", () => {
    render(<ImpactPanel analysis={analysisFixture({ degraded: true })} />)

    expect(screen.getByText("AI 不可用，仅文件级结果")).toBeTruthy()
    expect(screen.getByText("暂无影响候选")).toBeTruthy()
  })
})

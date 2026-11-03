import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { JobCard } from "./JobCard"
import type { JobCardPart, JobStatus } from "./types"

const apiClient = vi.hoisted(() => ({ apiPost: vi.fn() }))
vi.mock("@/lib/api-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-client")>()),
  apiPost: apiClient.apiPost,
}))

const runningJob: JobCardPart = {
  type: "job_card",
  job_id: "701",
  kind: "design.cases",
  status: "running",
  current: 3,
  total: 10,
}

function jobWith(status: JobStatus): JobCardPart {
  return { ...runningJob, status }
}

beforeEach(() => {
  apiClient.apiPost.mockReset()
})

afterEach(cleanup)

describe("JobCardActionRules", () => {
  it.each(["open", "claimed", "running"] as JobStatus[])(
    "offers cancel while the job is %s (an API-cancellable state)",
    (status) => {
      render(<JobCard versionId="2001" part={jobWith(status)} />)

      expect(screen.getByRole("button", { name: "取消作业" })).toBeInTheDocument()
    },
  )

  it("does not offer cancel on waiting_human, which the API rejects", () => {
    render(<JobCard versionId="2001" part={jobWith("waiting_human")} />)

    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
  })

  it("offers retry on a failed job", () => {
    render(<JobCard versionId="2001" part={jobWith("failed")} />)

    expect(screen.getByRole("button", { name: "重试作业" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
  })

  it("does not offer retry on a cancelled job", () => {
    render(<JobCard versionId="2001" part={jobWith("cancelled")} />)

    expect(screen.queryByRole("button", { name: "重试作业" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
  })

  it.each(["done", "waiting_human"] as JobStatus[])("offers no controls on a %s job", (status) => {
    render(<JobCard versionId="2001" part={jobWith(status)} />)

    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "重试作业" })).not.toBeInTheDocument()
  })

  it("shows the persisted failure reason and typed code instead of only 失败", () => {
    render(
      <JobCard
        versionId="2001"
        part={{
          ...jobWith("failed"),
          code: "llm_capability_mismatch",
          reason: `llm model "text-only" does not support images; choose a model with vision`,
        }}
      />,
    )

    const reason = screen.getByTestId("job-card-reason")
    expect(reason).toHaveTextContent(/不支持图片|does not support images/)
    expect(reason).toHaveTextContent("llm_capability_mismatch")
  })

  it("shows a clear message when retry is rejected because the job is not retryable", async () => {
    apiClient.apiPost.mockRejectedValueOnce(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "job 701 has exhausted its attempt budget (3/3)",
      }),
    )
    render(<JobCard versionId="2001" part={jobWith("failed")} />)

    fireEvent.click(screen.getByRole("button", { name: "重试作业" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent(/不可重试/)
    expect(alert).toHaveTextContent(/尝试次数/)
    // The retry control stays available so the user can act after refreshing.
    expect(screen.getByRole("button", { name: "重试作业" })).toBeInTheDocument()
  })

  it("shows a clear message when cancel is rejected because the job changed state", async () => {
    apiClient.apiPost.mockRejectedValueOnce(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "job 701 cannot be canceled from status done",
      }),
    )
    render(<JobCard versionId="2001" part={jobWith("running")} />)

    fireEvent.click(screen.getByRole("button", { name: "取消作业" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent(/无法取消/)
    expect(alert).toHaveTextContent(/状态/)
  })
})

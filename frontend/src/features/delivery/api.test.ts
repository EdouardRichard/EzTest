import { beforeEach, describe, expect, it, vi } from "vitest"

const client = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetPage: vi.fn(),
  apiPost: vi.fn(),
  apiDownload: vi.fn(),
}))
vi.mock("@/lib/api-client", () => client)

import { createManualIssue, retestIssue } from "./api"

const retestResponse = {
  issue_id: "9101",
  version_id: "2001",
  wave_id: "9202",
  wave_status: "running",
  retest_status: "retesting" as const,
  case_run_ids: ["9402"],
}

describe("delivery issue write API", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    client.apiPost.mockResolvedValue(retestResponse)
  })

  it("posts the retest request without source case_run ids by default", async () => {
    const result = await retestIssue("2001", "9101")

    expect(client.apiPost).toHaveBeenCalledWith("/api/versions/2001/issues/9101/retest", {})
    expect(result).toEqual(retestResponse)
  })

  it("forwards an explicit retest selection as decimal strings", async () => {
    await retestIssue("2001", "9101", ["9302"])

    expect(client.apiPost).toHaveBeenCalledWith("/api/versions/2001/issues/9101/retest", {
      case_run_ids: ["9302"],
    })
  })

  it("posts the manual issue body with its optional source refs", async () => {
    client.apiPost.mockResolvedValue({ id: "9110" })

    await createManualIssue("2001", {
      title: "登录失败",
      summary: "点击登录后没有跳转",
      category: "api",
      severity: "major",
      case_run_id: "9302",
      evidence_ids: ["9401", "9402"],
      reason: "手工补录",
    })

    expect(client.apiPost).toHaveBeenCalledWith("/api/versions/2001/issues", {
      title: "登录失败",
      summary: "点击登录后没有跳转",
      category: "api",
      severity: "major",
      case_run_id: "9302",
      evidence_ids: ["9401", "9402"],
      reason: "手工补录",
    })
  })

  it("omits empty optional manual issue fields", async () => {
    client.apiPost.mockResolvedValue({ id: "9110" })

    await createManualIssue("2001", {
      title: "登录失败",
      category: "ui",
      severity: "minor",
      summary: "",
      evidence_ids: [],
    })

    expect(client.apiPost).toHaveBeenCalledWith("/api/versions/2001/issues", {
      title: "登录失败",
      category: "ui",
      severity: "minor",
    })
  })
})

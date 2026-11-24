import { beforeEach, describe, expect, it, vi } from "vitest"

const client = vi.hoisted(() => ({
  apiPost: vi.fn(),
}))
vi.mock("@/lib/api-client", () => client)

import { waiveClose, waiveRequirement } from "./api"

describe("reports waiver write API (T6 endpoints)", () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it("posts the version waive-close reason to the admin endpoint", async () => {
    const response = {
      version_id: "2001",
      status: "completed",
      closure_kind: "waived",
      unverified: true,
      close_reason: "剩余风险已人工评估",
      report_id: "8101",
      report_revision: 3,
    }
    client.apiPost.mockResolvedValue(response)

    const result = await waiveClose("2001", { reason: "剩余风险已人工评估" })

    expect(client.apiPost).toHaveBeenCalledWith("/api/versions/2001/waive-close", {
      reason: "剩余风险已人工评估",
    })
    expect(result).toEqual(response)
  })

  it("posts the requirement waive reason to the admin endpoint", async () => {
    client.apiPost.mockResolvedValue({ requirement_id: "7001", waived: true })

    const result = await waiveRequirement("2001", "7001", { reason: "该需求豁免" })

    expect(client.apiPost).toHaveBeenCalledWith("/api/versions/2001/requirements/7001/waive", {
      reason: "该需求豁免",
    })
    expect(result).toEqual({ requirement_id: "7001", waived: true })
  })

  it("URL-encodes the version and requirement ids", async () => {
    client.apiPost.mockResolvedValue({ requirement_id: "7001", waived: true })
    await waiveRequirement("20 01", "70/01", { reason: "x" })
    expect(client.apiPost).toHaveBeenCalledWith(
      "/api/versions/20%2001/requirements/70%2F01/waive",
      { reason: "x" },
    )
  })
})

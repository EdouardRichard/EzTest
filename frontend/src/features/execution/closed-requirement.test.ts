import { describe, expect, it } from "vitest"

import { ApiError } from "@/lib/api-client"

import { closedRequirementId, requirementClosedMessage } from "./closed-requirement"

const CLOSED_MESSAGE = "the requirement is closed; reopen it before creating an execution intent"

function closedError(
  details: Record<string, unknown> = { reason: "requirement_closed", requirement_id: "6001" },
): ApiError {
  return new ApiError({
    status: 409,
    code: "state_conflict",
    message: CLOSED_MESSAGE,
    requestId: "req_closed",
    details,
  })
}

describe("closedRequirementId", () => {
  it("returns the requirement id carried by a closed-requirement 409", () => {
    expect(closedRequirementId(closedError())).toBe("6001")
  })

  it("ignores errors without the typed reason", () => {
    expect(closedRequirementId(new Error(CLOSED_MESSAGE))).toBeNull()
    expect(closedRequirementId("the requirement is closed")).toBeNull()
    expect(closedRequirementId(null)).toBeNull()
    expect(closedRequirementId(closedError({ reason: "version_closing" }))).toBeNull()
    expect(
      closedRequirementId(closedError({ reason: "requirement_closed", requirement_id: 6001 })),
    ).toBeNull()
    expect(
      closedRequirementId(closedError({ reason: "requirement_closed", requirement_id: "" })),
    ).toBeNull()
    expect(closedRequirementId(closedError({ reason: "requirement_closed" }))).toBeNull()
  })
})

describe("requirementClosedMessage", () => {
  it("names the blocked action for a rerun", () => {
    const message = requirementClosedMessage("rerun")
    expect(message).toContain("无法重跑")
    expect(message).toContain("该用例所属需求已闭环")
  })

  it("names the blocked action for a retest", () => {
    const message = requirementClosedMessage("retest")
    expect(message).toContain("无法复测")
    expect(message).toContain("该用例所属需求已闭环")
  })
})

import { describe, expect, it } from "vitest"

import { ApiError } from "@/lib/api-client"

import { localizeApiError } from "./api-error-messages"

function apiError(
  status: number,
  code: string,
  message: string,
  details?: Record<string, unknown>,
): ApiError {
  return new ApiError({ status, code, message, details })
}

describe("localizeApiError generic codes", () => {
  it("maps unauthenticated to the Chinese credential copy", () => {
    const localized = localizeApiError(apiError(401, "unauthenticated", "authentication required"))

    expect(localized.message).toBe("用户名或密码错误")
  })

  it("maps disabled accounts to the admin-contact copy", () => {
    const localized = localizeApiError(apiError(403, "account_disabled", "account is disabled"))

    expect(localized.message).toBe("账号已被禁用，请联系管理员")
  })

  it("maps missing resources and drops the English message", () => {
    const notFound = localizeApiError(apiError(404, "not_found", "version not found"))
    expect(notFound.message).toBe("资源不存在或已被删除")
    expect(notFound.message).not.toContain("resource not found")
    expect(notFound.message).not.toContain("version not found")

    expect(
      localizeApiError(apiError(404, "resource_not_found", "resource not found")).message,
    ).toBe("资源不存在或已被删除")
  })

  it("keeps a specific Chinese not_found message and derives typed context", () => {
    const localized = localizeApiError(
      apiError(404, "not_found", "对话不存在", {
        next_action: "请从历史对话中重新选择",
        field: "conversation_id",
      }),
    )

    expect(localized.message).toBe("对话不存在")
    expect(localized.message).not.toContain("资源不存在")
    expect(localized.nextAction).toBe("请从历史对话中重新选择")
    expect(localized.field).toBe("conversation_id")
  })

  it("keeps a specific Chinese resource_not_found message", () => {
    const localized = localizeApiError(apiError(404, "resource_not_found", "证据不存在"))

    expect(localized.message).toBe("证据不存在")
  })

  it("maps generic English fallbacks to the Chinese not-found copy", () => {
    for (const message of ["resource not found", "not found", ""]) {
      expect(localizeApiError(apiError(404, "not_found", message)).message).toBe(
        "资源不存在或已被删除",
      )
    }
  })

  it("maps the legacy work item type copy", () => {
    const localized = localizeApiError(
      apiError(400, "invalid_work_type", "unsupported work item type"),
    )

    expect(localized.message).toBe("旧版作业类型已停用，不影响已生成用例")
  })
})

describe("localizeApiError requirement-closed conflicts", () => {
  const closed = (message: string) =>
    apiError(409, "state_conflict", message, {
      reason: "requirement_closed",
      requirement_id: "6001",
    })

  it("uses the retest copy for a retest context", () => {
    const localized = localizeApiError(closed("the requirement is closed"), { kind: "retest" })

    expect(localized.message).toContain("无法复测")
    expect(localized.message).not.toContain("the requirement is closed")
  })

  it("uses the rerun copy when no context is given", () => {
    const localized = localizeApiError(closed("the requirement is closed"))

    expect(localized.message).toContain("无法重跑")
    expect(localized.message).not.toContain("the requirement is closed")
  })
})

describe("localizeApiError vision requirements", () => {
  const vision = (reasonCode: string, details: Record<string, unknown> = {}) =>
    apiError(400, "vision_required", "no vision-capable model is configured", {
      reason_code: reasonCode,
      config_name: "acceptance",
      ...details,
    })

  it.each([
    ["no_default_config", /尚未配置默认视觉模型/],
    ["default_model_missing", /默认模型缺失/],
    ["default_model_disabled", /默认模型已被禁用/],
    ["default_model_not_vision", /默认模型不支持视觉/],
    ["gateway_unreachable", /网关当前不可达/],
  ])("maps reason_code %s to Chinese copy plus a next step", (reasonCode, expected) => {
    const localized = localizeApiError(vision(reasonCode))

    expect(localized.message).toMatch(expected)
    expect(localized.nextAction).toBeTruthy()
  })

  it("falls back to the API message for an unknown reason_code", () => {
    const localized = localizeApiError(vision("something_new"))

    expect(localized.message).toBe("no vision-capable model is configured")
    expect(localized.nextAction).toBeUndefined()
  })

  it("keeps a typed next_action instead of the generic one", () => {
    const localized = localizeApiError(
      vision("default_model_missing", {
        next_action: "把配置 acceptance 的默认模型改为 deepseek-flash",
      }),
    )

    expect(localized.message).toContain("默认模型缺失")
    expect(localized.nextAction).toBe("把配置 acceptance 的默认模型改为 deepseek-flash")
  })
})

describe("localizeApiError default-model capability errors", () => {
  it("maps a non-vision default model and exposes the offending field", () => {
    const localized = localizeApiError(
      apiError(400, "invalid_argument", "the default model must have supports_vision=true", {
        field: "default_model_id",
        model: "deepseek-chat",
        missing_capabilities: ["vision"],
      }),
    )

    expect(localized.message).toContain("默认模型必须支持视觉")
    expect(localized.field).toBe("default_model_id")
    expect(localized.nextAction).toBeTruthy()
  })

  it("only maps the default-model error when vision is the missing capability", () => {
    const localized = localizeApiError(
      apiError(400, "invalid_argument", "the default model must support tools", {
        field: "default_model_id",
        missing_capabilities: ["tools"],
      }),
    )

    expect(localized.message).toBe("the default model must support tools")
  })

  it("maps a vision downgrade on the default model and keeps the offending field", () => {
    const localized = localizeApiError(
      apiError(
        400,
        "invalid_argument",
        "the default model must have supports_vision=true; document images and image drafts require vision analysis",
        {
          field: "supports_vision",
          model: "vision-llm",
          missing_capabilities: ["vision"],
          reason: "default_model_vision_downgrade",
        },
      ),
    )

    expect(localized.message).toContain("默认模型必须支持视觉")
    expect(localized.message).not.toContain("supports_vision=true")
    expect(localized.field).toBe("supports_vision")
    expect(localized.nextAction).toBeTruthy()
  })

  it("maps the required-default error that carries no missing capabilities", () => {
    const localized = localizeApiError(
      apiError(
        400,
        "invalid_argument",
        "default_model_id is required when the configuration has selectable models",
        { field: "default_model_id" },
      ),
    )

    expect(localized.message).toContain("默认模型")
    expect(localized.message).not.toContain("is required")
    expect(localized.field).toBe("default_model_id")
    expect(localized.nextAction).toBeTruthy()
  })

  it("leaves unrelated invalid_argument errors untouched", () => {
    const localized = localizeApiError(
      apiError(400, "invalid_argument", "title is required", { field: "title" }),
    )

    expect(localized.message).toBe("title is required")
  })
})

describe("localizeApiError fallbacks", () => {
  it("keeps the API message and typed next action for unmapped codes", () => {
    const localized = localizeApiError(
      apiError(403, "forbidden", "insufficient permissions", {
        next_action: "请让管理员为用户分配该权限",
      }),
    )

    expect(localized.message).toBe("insufficient permissions")
    expect(localized.nextAction).toBe("请让管理员为用户分配该权限")
  })

  it("handles plain errors, strings and unknown values", () => {
    expect(localizeApiError(new Error("网络中断")).message).toBe("网络中断")
    expect(localizeApiError("请求失败").message).toBe("请求失败")
    expect(localizeApiError(undefined).message).toBe("发生未知错误，请稍后重试")
  })
})

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
  it("maps unauthenticated to the Chinese session-expiry copy, not the login credential copy", () => {
    const localized = localizeApiError(apiError(401, "unauthenticated", "authentication required"))

    expect(localized.message).toBe("登录状态已失效，请重新登录")
    expect(localized.message).not.toBe("用户名或密码错误")
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
    // The backend emits CodeLLMConfigInvalid (llm_config_invalid) for this
    // branch, not invalid_argument (llm/application/service.go).
    const localized = localizeApiError(
      apiError(
        400,
        "llm_config_invalid",
        "default_model_id is required when the configuration has selectable models",
        { field: "default_model_id" },
      ),
    )

    expect(localized.message).toContain("默认模型")
    expect(localized.message).not.toContain("is required")
    expect(localized.field).toBe("default_model_id")
    expect(localized.nextAction).toBeTruthy()
  })

  it("localizes a default model that is not an active model of the config", () => {
    const localized = localizeApiError(
      apiError(400, "invalid_argument", "must reference an active model that is not deprecated", {
        field: "default_model_id",
      }),
    )

    expect(localized.message).toContain("默认模型")
    expect(localized.message).not.toContain("must reference")
    expect(localized.field).toBe("default_model_id")
    expect(localized.nextAction).toBeTruthy()
  })

  it("localizes a default model that belongs to another LLM configuration", () => {
    const localized = localizeApiError(
      apiError(400, "invalid_argument", "must reference a model of this LLM configuration", {
        field: "default_model_id",
      }),
    )

    expect(localized.message).toContain("默认模型")
    expect(localized.message).not.toContain("must reference")
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

describe("localizeApiError stage gate rejections", () => {
  it("localizes the requirement_confirmation gate to Chinese guidance", () => {
    const localized = localizeApiError(
      apiError(
        409,
        "stage_gate_failed",
        "stage gate requirement_confirmation is closed: requirements_unconfirmed (confirm every current requirement before starting design)",
        {
          gate: "requirement_confirmation",
          reason: "requirements_unconfirmed",
          action: "confirm every current requirement before starting design",
          next: "designing",
          // Even a future typed hint must not override the Chinese catalog copy.
          next_action: "confirm every current requirement before starting design",
        },
      ),
    )

    expect(localized.message).toBe("需先确认全部需求，才能开始设计")
    expect(localized.message).not.toContain("requirements_unconfirmed")
    expect(localized.message).not.toContain("stage gate")
    expect(localized.nextAction).toContain("草案")
    expect(localized.nextAction).toContain("批量确认")
    expect(localized.nextAction).not.toContain("confirm every current requirement")
  })

  it("localizes the requirement_gap open-gap gate to actionable Chinese guidance", () => {
    const localized = localizeApiError(
      apiError(
        409,
        "stage_gate_failed",
        "stage gate requirement_gap is closed: open_gap (resolve or explicitly accept every open requirement gap)",
        {
          action: "resolve or explicitly accept every open requirement gap",
          gate: "requirement_gap",
          next: "designing",
          reason: "open_gap",
        },
      ),
    )

    expect(localized.message).toBe("还有待回答的问题或未接受的开放缺口，无法开始设计")
    expect(localized.message).not.toContain("open_gap")
    expect(localized.message).not.toContain("stage gate")
    expect(localized.nextAction).toContain("需求与澄清")
    expect(localized.nextAction).toContain("开放缺口")
    expect(localized.nextAction).not.toContain("resolve or explicitly accept")
  })

  it("localizes every system gate reason instead of falling back to the generic copy", () => {
    const cases: Array<[string, string]> = [
      ["missing_requirement", "需求"],
      ["environment_not_bound", "环境"],
      ["no_confirmed_case_draft", "用例"],
      ["coverage_not_computed", "覆盖"],
      ["cases_unconfirmed", "用例"],
      ["executor_offline", "执行器"],
      ["account_invalid", "账号"],
      ["waves_active", "执行波次"],
      ["report_not_published", "报告"],
      ["open_blocking_issue", "阻塞"],
      ["version_close_failed", "收尾"],
      ["gate_check_failed", "重试"],
      ["gate_not_configured", "管理员"],
      ["blocker_not_cleared", "阻塞"],
      ["wave_not_scoped", "刷新"],
      ["wave_not_terminal", "波次"],
    ]

    for (const [reason, needle] of cases) {
      const localized = localizeApiError(
        apiError(
          409,
          "stage_gate_failed",
          `stage gate demo is closed: ${reason} (english action for ${reason})`,
          {
            action: `english action for ${reason}`,
            gate: "demo",
            next: "somewhere",
            reason,
          },
        ),
      )

      expect(localized.message, reason).not.toBe("当前操作未通过版本阶段门禁")
      expect(localized.message, reason).not.toContain(reason)
      expect(`${localized.message} ${localized.nextAction ?? ""}`, reason).toContain(needle)
      expect(localized.nextAction, reason).not.toContain(`english action for ${reason}`)
    }
  })

  it("never leaks the raw English gate message for an unmapped gate reason", () => {
    const localized = localizeApiError(
      apiError(
        409,
        "stage_gate_failed",
        "stage gate wave is closed: future_gate_reason (wait for every wave to finish)",
        {
          gate: "wave",
          reason: "future_gate_reason",
          action: "wait for every wave to finish",
          next: "reporting",
        },
      ),
    )

    expect(localized.message).toBe("当前操作未通过版本阶段门禁")
    expect(localized.message).not.toContain("stage gate")
    expect(localized.message).not.toContain("future_gate_reason")
    expect(localized.nextAction).not.toContain("wait for every wave")
    expect(localized.nextAction).not.toContain("reporting")
  })
})

describe("localizeApiError default-model delete guard", () => {
  it("localizes the default-model delete guard and replaces the English next action", () => {
    const localized = localizeApiError(
      apiError(
        409,
        "default_model_in_use",
        'model "deepseek-flash" is the default model of llm config "acceptance-deepseek-flash"; set default_model_id to another model before deleting it',
        {
          config_id: "7001",
          config_name: "acceptance-deepseek-flash",
          default_model_id: "8001",
          field: "default_model_id",
          next_action: "choose another model as default_model_id, then delete this model",
        },
      ),
    )

    expect(localized.message).toBe("该模型是当前配置的默认模型，无法删除")
    expect(localized.nextAction).toBe("请先切换默认模型，再删除该模型")
    expect(localized.field).toBe("default_model_id")
    expect(localized.nextAction).not.toMatch(/choose another model/)
  })

  it("keeps the Chinese catalog copy when the English message embeds a CJK config name", () => {
    // The backend message embeds the user-editable config name, so a CJK name
    // inside an otherwise English message must not make it look "Chinese
    // enough" to keep as the main copy.
    const localized = localizeApiError(
      apiError(
        409,
        "default_model_in_use",
        'model "manual-a" is the default model of llm config "验收配置"; set default_model_id to another model before deleting it',
        {
          config_id: "7001",
          config_name: "验收配置",
          default_model_id: "8001",
          field: "default_model_id",
          next_action: "choose another model as default_model_id, then delete this model",
        },
      ),
    )

    expect(localized.message).toBe("该模型是当前配置的默认模型，无法删除")
    expect(localized.message).not.toContain("验收配置")
    expect(localized.message).not.toContain("is the default model of llm config")
    expect(localized.nextAction).toBe("请先切换默认模型，再删除该模型")
    expect(localized.nextAction).not.toContain("choose another model")
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

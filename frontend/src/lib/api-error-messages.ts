import { ApiError } from "./api-client"

/**
 * Chinese copy for a typed API error. The backend keeps its English `message`
 * (logs, tests, API consumers); the UI renders the localized catalog entry when
 * one exists and falls back to the original message otherwise.
 */
export type LocalizedError = {
  message: string
  nextAction?: string
  field?: string
}

export type LocalizeContext = {
  kind?: "rerun" | "retest"
}

type CatalogEntry = {
  message: string
  nextAction?: string
}

const UNKNOWN_ERROR_MESSAGE = "发生未知错误，请稍后重试"

const GENERIC_MESSAGES: Record<string, string> = {
  // A 401 outside the login form means the session expired; the credential copy
  // is login-specific and lives in `login-errors.ts`.
  unauthenticated: "登录状态已失效，请重新登录",
  account_disabled: "账号已被禁用，请联系管理员",
  not_found: "资源不存在或已被删除",
  resource_not_found: "资源不存在或已被删除",
  invalid_work_type: "旧版作业类型已停用，不影响已生成用例",
}

const REQUIREMENT_CLOSED_MESSAGES: Record<"rerun" | "retest", string> = {
  rerun: "无法重跑：该用例所属需求已闭环，请先在「需求与澄清」中重新打开该需求",
  retest: "无法复测：该用例所属需求已闭环，请先在「需求与澄清」中重新打开该需求",
}

const VISION_REASON_MESSAGES: Record<string, CatalogEntry> = {
  no_default_config: {
    message: "尚未配置默认视觉模型，无法分析文档中的图片",
    nextAction: "请先在「设置 → LLM 配置」中新增模型，并将支持视觉的模型设为默认模型",
  },
  default_model_missing: {
    message: "默认模型缺失，无法分析文档中的图片",
    nextAction: "请在「设置 → LLM 配置」中把默认模型指向一个已存在的模型",
  },
  default_model_disabled: {
    message: "默认模型已被禁用，无法分析文档中的图片",
    nextAction: "请在「设置 → LLM 配置」中启用默认模型，或改选其他支持视觉的模型",
  },
  default_model_not_vision: {
    message: "默认模型不支持视觉，无法分析文档中的图片",
    nextAction: "请在「设置 → LLM 配置」中把默认模型改为支持视觉的模型",
  },
  gateway_unreachable: {
    message: "视觉模型网关当前不可达，暂时无法分析文档中的图片",
    nextAction: "请稍后重试；若持续失败，请检查网络与 LLM 网关配置",
  },
}

const DEFAULT_MODEL_VISION_CAPABILITY: CatalogEntry = {
  message: "默认模型必须支持视觉能力，才能分析文档中的图片",
  nextAction: "请在「设置 → LLM 配置」中改选支持视觉的默认模型",
}

/** The capability editor cannot strip vision from the model already acting as default. */
const DEFAULT_MODEL_VISION_DOWNGRADE: CatalogEntry = {
  message: "默认模型必须支持视觉能力，不能关闭视觉支持",
  nextAction: "请先把默认模型改为其他支持视觉的模型，再关闭该模型的视觉能力",
}

/** A config with selectable models must pin one as its default. */
const DEFAULT_MODEL_REQUIRED: CatalogEntry = {
  message: "该配置存在可选模型，必须选择默认模型",
  nextAction: "请在「设置 → LLM 配置」中为该配置选择默认模型后再保存",
}

/** The chosen default is not an active, non-deprecated model of the config. */
const DEFAULT_MODEL_INACTIVE: CatalogEntry = {
  message: "默认模型必须是当前配置中可用且未弃用的模型",
  nextAction: "请在「设置 → LLM 配置」中改选一个可用且未弃用的模型作为默认模型",
}

/** The chosen default does not belong to the edited LLM configuration. */
const DEFAULT_MODEL_FOREIGN: CatalogEntry = {
  message: "默认模型必须属于当前 LLM 配置",
  nextAction: "请在「设置 → LLM 配置」中从该配置的模型里选择默认模型",
}

/**
 * 409 stage_gate_failed (versionapp.StageGateError.APIError). The backend keeps
 * the English gate/action/next machine hints; the UI owns the Chinese copy and
 * the actionable next step. Keyed by `details.reason`.
 */
const STAGE_GATE_MESSAGES: Record<string, CatalogEntry> = {
  requirements_unconfirmed: {
    message: "需先确认全部需求，才能开始设计",
    nextAction: "筛选「草案」→ 批量确认需求 → 再开始设计",
  },
}

/** Unknown gate reasons must never print the raw English StageGateError text. */
const STAGE_GATE_FALLBACK: CatalogEntry = {
  message: "当前操作未通过版本阶段门禁",
  nextAction: "请按页面提示完成前置条件后重试",
}

/** Deleting the config's current default model is blocked by the M-07 guard. */
const DEFAULT_MODEL_IN_USE: CatalogEntry = {
  message: "该模型是当前配置的默认模型，无法删除",
  nextAction: "请先切换默认模型，再删除该模型",
}

function readString(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined
}

const CJK_PATTERN = /[\u4e00-\u9fff]/

/**
 * The backend sometimes carries a more specific Chinese message than the
 * generic catalog copy (for example `对话不存在` for a missing conversation).
 * Keep that message instead of flattening it to the generic resource copy;
 * English (or empty) messages still receive the catalog copy.
 */
function preferSpecificMessage(apiMessage: string, catalogMessage: string): string {
  return CJK_PATTERN.test(apiMessage) ? apiMessage : catalogMessage
}

function hasCapability(value: unknown, capability: string): boolean {
  if (Array.isArray(value)) return value.some((item) => item === capability)
  return value === capability
}

/**
 * Maps a stable API `code` (and typed `details`) to Chinese copy. When nothing
 * matches, the original `ApiError.message` is returned so existing behaviour and
 * backend-owned guidance keep working.
 */
export function localizeApiError(error: unknown, context: LocalizeContext = {}): LocalizedError {
  if (error instanceof ApiError) {
    const details = error.details
    const field = readString(details.field)
    const typedNextAction = readString(details.next_action)
    const reason = readString(details.reason)
    const reasonCode = readString(details.reason_code)

    const fromCatalog = (entry: CatalogEntry): LocalizedError => ({
      message: preferSpecificMessage(error.message, entry.message),
      nextAction: typedNextAction ?? entry.nextAction,
      field,
    })

    if (reason === "requirement_closed") {
      return {
        message: preferSpecificMessage(
          error.message,
          REQUIREMENT_CLOSED_MESSAGES[context.kind ?? "rerun"],
        ),
        nextAction: typedNextAction,
        field,
      }
    }

    if (error.code === "stage_gate_failed") {
      // Never fall back to the raw English gate message or the English
      // details.action/next/next_action hints: the catalog owns the UI copy.
      const mappedGate = reason === undefined ? undefined : STAGE_GATE_MESSAGES[reason]
      const gateEntry = mappedGate ?? STAGE_GATE_FALLBACK
      return {
        message: preferSpecificMessage(error.message, gateEntry.message),
        nextAction: gateEntry.nextAction,
        field,
      }
    }

    if (error.code === "default_model_in_use") {
      // The backend message embeds the user-editable config name, which may
      // contain CJK even though the message itself is English, so
      // `preferSpecificMessage` cannot distinguish it from a Chinese backend
      // message. The catalog copy is authoritative for this code; the backend
      // types an English next_action that must not replace it either.
      return {
        message: DEFAULT_MODEL_IN_USE.message,
        nextAction: DEFAULT_MODEL_IN_USE.nextAction,
        field,
      }
    }

    if (error.code === "vision_required" && reasonCode !== undefined) {
      const visionEntry = VISION_REASON_MESSAGES[reasonCode]
      if (visionEntry !== undefined) return fromCatalog(visionEntry)
    }

    // The vision gate and the "must reference" default-model checks reach the
    // UI as invalid_argument; the required-default check is llm_config_invalid
    // (CodeLLMConfigInvalid in llm/application/chat.go). Both codes share the
    // default_model_id/supports_vision fields.
    if (
      (error.code === "invalid_argument" || error.code === "llm_config_invalid") &&
      (field === "default_model_id" || field === "supports_vision")
    ) {
      if (hasCapability(details.missing_capabilities, "vision")) {
        return fromCatalog(
          field === "supports_vision"
            ? DEFAULT_MODEL_VISION_DOWNGRADE
            : DEFAULT_MODEL_VISION_CAPABILITY,
        )
      }
      if (field === "default_model_id" && /required/i.test(error.message)) {
        return fromCatalog(DEFAULT_MODEL_REQUIRED)
      }
      if (field === "default_model_id" && /must reference/i.test(error.message)) {
        return fromCatalog(
          /deprecated/i.test(error.message) ? DEFAULT_MODEL_INACTIVE : DEFAULT_MODEL_FOREIGN,
        )
      }
    }

    const genericMessage = GENERIC_MESSAGES[error.code]
    if (genericMessage !== undefined) {
      return {
        message: preferSpecificMessage(error.message, genericMessage),
        nextAction: typedNextAction,
        field,
      }
    }

    return { message: error.message, nextAction: typedNextAction, field }
  }

  if (error instanceof Error) return { message: error.message }
  if (typeof error === "string" && error.length > 0) return { message: error }
  return { message: UNKNOWN_ERROR_MESSAGE }
}

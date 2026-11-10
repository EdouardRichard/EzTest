import { describe, expect, it } from "vitest"

import { ApiError } from "@/lib/api-client"

import { loginErrorMessage } from "./login-errors"

function apiError(status: number, code: string, message = "server message"): ApiError {
  return new ApiError({ status, code, message, requestId: "req_login" })
}

describe("loginErrorMessage", () => {
  it("maps a rejected credential to a Chinese 401 message", () => {
    expect(loginErrorMessage(apiError(401, "unauthenticated", "invalid credentials"))).toBe(
      "用户名或密码错误",
    )
  })

  it("maps a disabled account to its own Chinese message", () => {
    expect(loginErrorMessage(apiError(403, "account_disabled", "account is disabled"))).toBe(
      "账号已被禁用，请联系管理员",
    )
  })

  it("maps rate limiting to a Chinese retry-later message", () => {
    expect(loginErrorMessage(apiError(429, "rate_limited", "too many requests"))).toBe(
      "登录尝试过于频繁，请稍后再试",
    )
  })

  it("maps a network failure (status 0) to a Chinese connectivity message", () => {
    expect(loginErrorMessage(apiError(0, "network_error", "Failed to fetch"))).toBe(
      "网络连接失败，请检查网络后重试",
    )
  })

  it("falls back to a generic Chinese message for unknown failures", () => {
    expect(loginErrorMessage(apiError(403, "forbidden", "forbidden"))).toBe("登录失败，请稍后重试")
    expect(loginErrorMessage(apiError(500, "internal", "boom"))).toBe("登录失败，请稍后重试")
    expect(loginErrorMessage(new Error("boom"))).toBe("登录失败，请稍后重试")
    expect(loginErrorMessage(null)).toBe("登录失败，请稍后重试")
  })
})

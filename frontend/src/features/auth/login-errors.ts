import { ApiError } from "@/lib/api-client"

/**
 * Maps a failed login attempt to a stable Chinese message. The API returns an
 * English envelope (`401 unauthenticated`, `403 account_disabled`, `429
 * rate_limited`) and the browser surfaces transport failures as `status: 0`;
 * the login page never prints the raw server text.
 */
export function loginErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403 && error.code === "account_disabled") {
      return "账号已被禁用，请联系管理员"
    }
    if (error.status === 401) return "用户名或密码错误"
    if (error.status === 429) return "登录尝试过于频繁，请稍后再试"
    if (error.status === 0) return "网络连接失败，请检查网络后重试"
  }
  return "登录失败，请稍后重试"
}

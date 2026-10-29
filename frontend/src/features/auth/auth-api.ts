import { apiGet, apiPost } from "@/lib/api-client"
import type { AuthUser } from "@/lib/auth-storage"

export type { AuthUser }

/** POST /api/auth/login response payload. Never contains password material. */
export type LoginResult = {
  token: string
  expires_at: string
  user: AuthUser
}

export function loginRequest(username: string, password: string): Promise<LoginResult> {
  return apiPost<LoginResult>("/api/auth/login", { username, password }, { token: null })
}

export function fetchMe(): Promise<AuthUser> {
  return apiGet<AuthUser>("/api/auth/me")
}

export async function logoutRequest(): Promise<void> {
  await apiPost<void>("/api/auth/logout")
}

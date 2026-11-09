"use client"

import { usePathname, useRouter } from "next/navigation"
import type { ReactNode } from "react"
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react"

import { LoadingCard } from "@/components/ui/Spinner"
import { ApiError, apiGet, apiPost } from "@/lib/api-client"
import {
  AUTH_CLEARED_EVENT,
  type AuthUser,
  clearSession,
  getStoredUser,
  getToken,
  isAuthStorageKey,
  setSession,
  toAuthUser,
} from "@/lib/auth-storage"

export type User = AuthUser

export type AuthContextValue = {
  user: User | null
  token: string | null
  login: (username: string, password: string) => Promise<void>
  changePassword: (oldPassword: string, newPassword: string) => Promise<void>
  logout: () => Promise<void>
  loading: boolean
}

const AuthContext = createContext<AuthContextValue | null>(null)

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function readUserPayload(payload: unknown): User | null {
  if (isRecord(payload) && isRecord(payload.user)) return toAuthUser(payload.user)
  return toAuthUser(payload)
}

function readSessionPayload(
  payload: unknown,
): { token: string; user: User; expiresAt: string | null } | null {
  if (!isRecord(payload)) return null
  const token = typeof payload.token === "string" ? payload.token : ""
  const user = readUserPayload(payload)
  if (token.length === 0 || user === null) return null
  const expiresAt =
    typeof payload.expires_at === "string" && payload.expires_at.length > 0
      ? payload.expires_at
      : null
  return { token, user, expiresAt }
}

function invalidResponse(message: string): ApiError {
  return new ApiError({ status: 200, code: "invalid_response", message, retryable: false })
}

/**
 * Restricts a `next` redirect target to a same-origin path. Anything that could
 * leave the app (absolute URL, protocol-relative `//`, backslash normalization)
 * or loop back to /login falls back to the default landing page.
 */
export function safeNext(next: string | null | undefined): string {
  const fallback = "/versions"
  if (typeof next !== "string") return fallback
  const candidate = next.trim()
  if (candidate.length === 0) return fallback
  if (!candidate.startsWith("/")) return fallback
  if (candidate.startsWith("//")) return fallback
  if (candidate.includes("\\")) return fallback
  const path = candidate.split(/[?#]/, 1)[0] ?? ""
  if (path === "/login" || path.startsWith("/login/")) return fallback
  return candidate
}

/**
 * Owns the browser session: restores it through `GET /api/auth/me`, exposes the
 * auth commands, and reacts to a session cleared by api-client/sse-client 401s.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [token, setToken] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let active = true
    const storedToken = getToken()
    if (storedToken === null) {
      setLoading(false)
      return () => {
        active = false
      }
    }

    setToken(storedToken)
    void (async () => {
      try {
        const payload = await apiGet<unknown>("/api/auth/me")
        const nextUser = readUserPayload(payload)
        if (!active) return
        if (nextUser === null) throw invalidResponse("会话响应缺少用户信息")
        setUser(nextUser)
      } catch {
        if (!active) return
        clearSession()
        setUser(null)
        setToken(null)
      } finally {
        if (active) setLoading(false)
      }
    })()

    return () => {
      active = false
    }
  }, [])

  useEffect(() => {
    const onCleared = () => {
      setUser(null)
      setToken(null)
      setLoading(false)
    }
    window.addEventListener(AUTH_CLEARED_EVENT, onCleared)
    return () => window.removeEventListener(AUTH_CLEARED_EVENT, onCleared)
  }, [])

  // localStorage is shared by every tab: adopt a login/logout performed in
  // another tab instead of waiting for the next reload or API 401.
  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.storageArea !== null && event.storageArea !== window.localStorage) return
      if (!isAuthStorageKey(event.key)) return
      const nextToken = getToken()
      setToken(nextToken)
      if (nextToken === null) {
        setUser(null)
        setLoading(false)
        return
      }
      setUser(getStoredUser())
    }
    window.addEventListener("storage", onStorage)
    return () => window.removeEventListener("storage", onStorage)
  }, [])

  const login = useCallback(async (username: string, password: string) => {
    const payload = await apiPost<unknown>("/api/auth/login", { username, password })
    const session = readSessionPayload(payload)
    if (session === null) throw invalidResponse("登录响应缺少令牌或用户信息")

    setSession(session.token, session.user, session.expiresAt)
    setToken(session.token)
    setUser(session.user)
  }, [])

  const changePassword = useCallback(async (oldPassword: string, newPassword: string) => {
    await apiPost("/api/auth/password", {
      old_password: oldPassword,
      new_password: newPassword,
    })
  }, [])

  const logout = useCallback(async () => {
    try {
      await apiPost("/api/auth/logout")
    } catch {
      // A revoked/expired token must not keep the browser signed in.
    } finally {
      clearSession()
      setUser(null)
      setToken(null)
    }
  }, [])

  const value = useMemo<AuthContextValue>(
    () => ({ user, token, login, changePassword, logout, loading }),
    [user, token, login, changePassword, logout, loading],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const value = useContext(AuthContext)
  if (value === null) throw new Error("useAuth must be used inside AuthProvider")
  return value
}

/** Redirects signed-out visitors to /login and blocks protected content. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { loading, token } = useAuth()
  const router = useRouter()
  const pathname = usePathname()

  useEffect(() => {
    if (loading || token !== null) return
    // Keep the deep link so login can return the visitor to where they were.
    const search = typeof window === "undefined" ? "" : window.location.search
    const target = `${pathname}${search}`
    router.replace(`/login?next=${encodeURIComponent(target)}`)
  }, [loading, token, router, pathname])

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <LoadingCard label="正在验证会话…" />
      </div>
    )
  }

  if (token === null) return null
  return <>{children}</>
}

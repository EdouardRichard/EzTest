"use client"

import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react"

import {
  AUTH_CLEARED_EVENT,
  type AuthUser,
  clearSession,
  getStoredUser,
  getToken,
  setSession,
  setStoredUser,
} from "@/lib/auth-storage"

import { fetchMe, loginRequest, logoutRequest } from "./auth-api"

export type AuthContextValue = {
  user: AuthUser | null
  loading: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

/**
 * Holds the current session in memory and mirrors the token + public user in
 * sessionStorage. It never stores credentials or password material.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false

    async function restore() {
      if (!getToken()) {
        if (!cancelled) setLoading(false)
        return
      }

      const cached = getStoredUser()
      if (cached) {
        if (!cancelled) {
          setUser(cached)
          setLoading(false)
        }
        return
      }

      try {
        const me = await fetchMe()
        if (!cancelled) {
          setUser(me)
          setStoredUser(me)
        }
      } catch {
        if (!cancelled) clearSession()
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    void restore()

    const handleCleared = () => {
      setUser(null)
      setLoading(false)
    }
    window.addEventListener(AUTH_CLEARED_EVENT, handleCleared)
    return () => {
      cancelled = true
      window.removeEventListener(AUTH_CLEARED_EVENT, handleCleared)
    }
  }, [])

  const login = useCallback(async (username: string, password: string) => {
    const result = await loginRequest(username, password)
    setSession(result.token, result.user)
    setUser(result.user)
    setLoading(false)
  }, [])

  const logout = useCallback(async () => {
    try {
      await logoutRequest()
    } catch {
      // The server session may already be gone; the local session is still
      // cleared so the user always ends up signed out.
    } finally {
      clearSession()
      setUser(null)
      setLoading(false)
    }
  }, [])

  const value = useMemo<AuthContextValue>(
    () => ({ user, loading, login, logout }),
    [user, loading, login, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext)
  if (!context) throw new Error("useAuth must be used inside AuthProvider")
  return context
}

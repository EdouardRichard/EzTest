const TOKEN_KEY = "eztest.token"
const USER_KEY = "eztest.user"

/** Browser event dispatched whenever the local session is cleared. */
export const AUTH_CLEARED_EVENT = "eztest:auth-cleared"

export type AuthRole = "admin" | "user"
export type AuthStatus = "active" | "disabled"

/** The public account shape returned by the API. Never contains secrets. */
export type AuthUser = {
  id: number
  username: string
  role: AuthRole
  status: AuthStatus
}

function getStorage(): Storage | null {
  if (typeof window === "undefined") return null
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

function read(key: string): string | null {
  try {
    return getStorage()?.getItem(key) ?? null
  } catch {
    return null
  }
}

function write(key: string, value: string): void {
  try {
    getStorage()?.setItem(key, value)
  } catch {
    // Storage can be unavailable or full (private mode). The in-memory auth
    // state still works for the current page; the next reload will sign out.
  }
}

function remove(key: string): void {
  try {
    getStorage()?.removeItem(key)
  } catch {
    // Ignore storage failures: there is nothing else to clean up.
  }
}

/** Reads the bearer token from sessionStorage (never from a URL or log). */
export function getToken(): string | null {
  return read(TOKEN_KEY)
}

/** Persists the bearer token for the current browser tab. */
export function setToken(token: string): void {
  write(TOKEN_KEY, token)
}

/**
 * Removes the bearer token and notifies listeners (for example AuthProvider)
 * so the protected layout can redirect to /login after a 401.
 */
export function clearToken(): void {
  remove(TOKEN_KEY)
  if (typeof window !== "undefined") {
    window.dispatchEvent(new Event(AUTH_CLEARED_EVENT))
  }
}

export function getStoredUser(): AuthUser | null {
  const raw = read(USER_KEY)
  if (!raw) return null
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== "object" || parsed === null) return null
    const candidate = parsed as Partial<AuthUser>
    if (
      typeof candidate.id !== "number" ||
      typeof candidate.username !== "string" ||
      (candidate.role !== "admin" && candidate.role !== "user") ||
      (candidate.status !== "active" && candidate.status !== "disabled")
    ) {
      return null
    }
    return {
      id: candidate.id,
      username: candidate.username,
      role: candidate.role,
      status: candidate.status,
    }
  } catch {
    return null
  }
}

export function setStoredUser(user: AuthUser): void {
  write(USER_KEY, JSON.stringify(user))
}

/** Stores a complete login session. */
export function setSession(token: string, user: AuthUser): void {
  setToken(token)
  setStoredUser(user)
}

/** Clears both the token and the cached user. */
export function clearSession(): void {
  remove(USER_KEY)
  clearToken()
}

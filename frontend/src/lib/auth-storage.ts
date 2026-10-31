const TOKEN_KEY = "eztest.token"
const USER_KEY = "eztest.user"

/** Browser event dispatched whenever the local session is cleared. */
export const AUTH_CLEARED_EVENT = "eztest:auth-cleared"

export type AuthRole = "admin" | "user"
export type AuthStatus = "active" | "disabled"

/** The public account shape returned by the API. Never contains secrets. */
export type AuthUser = {
  id: string
  username: string
  display_name: string
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
    // state still works for the current page; the next reload signs the user out.
  }
}

function remove(key: string): void {
  try {
    getStorage()?.removeItem(key)
  } catch {
    // Ignore storage failures: there is nothing else to clean up.
  }
}

function readId(value: unknown): string | null {
  if (typeof value === "string" && value.trim().length > 0) return value
  if (typeof value === "number" && Number.isFinite(value)) return String(value)
  return null
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

/**
 * Validates and normalizes an API account payload into the public AuthUser
 * shape. Login and /me responses reuse it so every session has one shape.
 */
export function toAuthUser(value: unknown): AuthUser | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null
  const candidate = value as Record<string, unknown>
  const id = readId(candidate.id)
  const username = candidate.username
  const role = candidate.role
  const status = candidate.status
  if (typeof username !== "string" || username.length === 0) return null
  if (id === null) return null
  if (role !== "admin" && role !== "user") return null
  if (status !== "active" && status !== "disabled") return null
  const displayName =
    typeof candidate.display_name === "string" && candidate.display_name.length > 0
      ? candidate.display_name
      : username
  return { id, username, display_name: displayName, role, status }
}

function parseUser(raw: string): AuthUser | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  return toAuthUser(parsed)
}

export function getStoredUser(): AuthUser | null {
  const raw = read(USER_KEY)
  if (!raw) return null
  return parseUser(raw)
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

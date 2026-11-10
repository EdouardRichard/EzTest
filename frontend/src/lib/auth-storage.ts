const TOKEN_KEY = "eztest.token"
const USER_KEY = "eztest.user"
const EXPIRES_AT_KEY = "eztest.token_expires_at"

const AUTH_STORAGE_KEYS = new Set<string>([TOKEN_KEY, USER_KEY, EXPIRES_AT_KEY])

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

function getLocalStorage(): Storage | null {
  if (typeof window === "undefined") return null
  try {
    return window.localStorage
  } catch {
    return null
  }
}

function getSessionStorage(): Storage | null {
  if (typeof window === "undefined") return null
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

/** True for the keys the session uses; `null` (a full storage clear) counts too. */
export function isAuthStorageKey(key: string | null): boolean {
  return key === null || AUTH_STORAGE_KEYS.has(key)
}

function read(key: string): string | null {
  try {
    return getLocalStorage()?.getItem(key) ?? null
  } catch {
    return null
  }
}

function write(key: string, value: string): void {
  try {
    getLocalStorage()?.setItem(key, value)
  } catch {
    // Storage can be unavailable or full (private mode). The in-memory auth
    // state still works for the current page; the next reload signs the user out.
  }
}

function removeEverywhere(key: string): void {
  try {
    getLocalStorage()?.removeItem(key)
  } catch {
    // Ignore storage failures: there is nothing else to clean up.
  }
  try {
    getSessionStorage()?.removeItem(key)
  } catch {
    // Ignore storage failures: there is nothing else to clean up.
  }
}

/**
 * Copies a session written by an older build (sessionStorage) into
 * localStorage exactly once. The legacy copy is always removed so a later tab
 * cannot resurrect it over a newer localStorage session.
 */
function migrateLegacySession(): void {
  if (typeof window === "undefined") return
  let legacyToken: string | null = null
  let legacyUser: string | null = null
  try {
    const legacy = getSessionStorage()
    if (legacy === null) return
    legacyToken = legacy.getItem(TOKEN_KEY)
    legacyUser = legacy.getItem(USER_KEY)
    if (legacyToken === null && legacyUser === null) return

    // Only seed an empty localStorage; never overwrite a session created by
    // this (or another) tab after the upgrade.
    if (read(TOKEN_KEY) === null && read(USER_KEY) === null) {
      if (legacyToken !== null) write(TOKEN_KEY, legacyToken)
      if (legacyUser !== null) write(USER_KEY, legacyUser)
    }
    legacy.removeItem(TOKEN_KEY)
    legacy.removeItem(USER_KEY)
  } catch {
    // A blocked/partial sessionStorage is equivalent to no legacy session.
  }
}

function isSessionExpired(): boolean {
  const expiresAt = read(EXPIRES_AT_KEY)
  if (expiresAt === null) return false
  const timestamp = Date.parse(expiresAt)
  // Fail closed: an unparseable expiry must not keep a session alive.
  return !Number.isFinite(timestamp) || timestamp <= Date.now()
}

function readId(value: unknown): string | null {
  if (typeof value === "string" && value.trim().length > 0) return value
  if (typeof value === "number" && Number.isFinite(value)) return String(value)
  return null
}

/** Reads the bearer token from localStorage (never from a URL or log). */
export function getToken(): string | null {
  migrateLegacySession()
  const token = read(TOKEN_KEY)
  if (token === null) return null
  if (isSessionExpired()) {
    clearSession()
    return null
  }
  return token
}

/** Persists the bearer token; the server owns the authoritative TTL. */
export function setToken(token: string): void {
  migrateLegacySession()
  write(TOKEN_KEY, token)
}

/**
 * Removes the bearer token and its expiry and notifies listeners (for example
 * AuthProvider) so the protected layout can redirect to /login after a 401.
 */
export function clearToken(): void {
  removeEverywhere(TOKEN_KEY)
  removeEverywhere(EXPIRES_AT_KEY)
  notifyCleared()
}

function notifyCleared(): void {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new Event(AUTH_CLEARED_EVENT))
  }
}

/** Removes the whole stored session from localStorage and legacy sessionStorage. */
function clearPersistedSession(): void {
  removeEverywhere(TOKEN_KEY)
  removeEverywhere(USER_KEY)
  removeEverywhere(EXPIRES_AT_KEY)
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
  migrateLegacySession()
  if (isSessionExpired()) {
    // Drop the expired session silently: callers include render-time role
    // checks, and dispatching AUTH_CLEARED_EVENT mid-render would update the
    // provider from another component's render. The token/expiry go too.
    clearPersistedSession()
    return null
  }
  const raw = read(USER_KEY)
  if (!raw) return null
  return parseUser(raw)
}

export function setStoredUser(user: AuthUser): void {
  migrateLegacySession()
  write(USER_KEY, JSON.stringify(user))
}

/** Stores a complete login session, including the server-provided expiry. */
export function setSession(token: string, user: AuthUser, expiresAt?: string | Date | null): void {
  migrateLegacySession()
  write(TOKEN_KEY, token)
  write(USER_KEY, JSON.stringify(user))
  if (expiresAt === undefined || expiresAt === null || expiresAt === "") {
    removeEverywhere(EXPIRES_AT_KEY)
    return
  }
  write(EXPIRES_AT_KEY, expiresAt instanceof Date ? expiresAt.toISOString() : expiresAt)
}

/** Clears the token, expiry and cached user from both storage areas. */
export function clearSession(): void {
  clearPersistedSession()
  notifyCleared()
}

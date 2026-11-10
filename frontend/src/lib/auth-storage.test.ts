import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  AUTH_CLEARED_EVENT,
  clearSession,
  clearToken,
  getStoredUser,
  getToken,
  setSession,
  setStoredUser,
  setToken,
  toAuthUser,
} from "./auth-storage"

const user = {
  id: "9001",
  username: "admin",
  display_name: "管理员",
  role: "admin" as const,
  status: "active" as const,
}

const TOKEN_KEY = "eztest.token"
const USER_KEY = "eztest.user"
const EXPIRES_KEY = "eztest.token_expires_at"

function clearStorage(): void {
  window.localStorage.clear()
  window.sessionStorage.clear()
}

describe("auth-storage", () => {
  beforeEach(clearStorage)

  afterEach(clearStorage)

  it("returns null before a token is stored", () => {
    expect(getToken()).toBeNull()
    expect(getStoredUser()).toBeNull()
  })

  it("stores and reads the bearer token from localStorage", () => {
    setToken("ezt_abc")

    expect(getToken()).toBe("ezt_abc")
    expect(window.localStorage.getItem(TOKEN_KEY)).toBe("ezt_abc")
    expect(window.sessionStorage.getItem(TOKEN_KEY)).toBeNull()
  })

  it("stores a complete session with its server expiry", () => {
    setSession("ezt_abc", user, "2099-01-01T00:00:00Z")

    expect(getToken()).toBe("ezt_abc")
    expect(getStoredUser()).toEqual(user)
    expect(window.localStorage.getItem(EXPIRES_KEY)).toBe("2099-01-01T00:00:00Z")
  })

  it("treats an expired session as signed out and clears it", () => {
    setSession("ezt_old", user, "2020-01-01T00:00:00Z")

    expect(getToken()).toBeNull()
    expect(window.localStorage.getItem(TOKEN_KEY)).toBeNull()
    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
    expect(window.localStorage.getItem(EXPIRES_KEY)).toBeNull()
  })

  it("fails closed when the stored expiry cannot be parsed", () => {
    setSession("ezt_bad", user, "not-a-timestamp")

    expect(getToken()).toBeNull()
    expect(getStoredUser()).toBeNull()
  })

  it("clears the stored session when getStoredUser sees an expired session", () => {
    setSession("ezt_old", user, "2020-01-01T00:00:00Z")

    expect(getStoredUser()).toBeNull()
    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
    expect(window.localStorage.getItem(TOKEN_KEY)).toBeNull()
    expect(window.localStorage.getItem(EXPIRES_KEY)).toBeNull()
    expect(getToken()).toBeNull()
  })

  it("does not treat a session without an expiry as expired", () => {
    setSession("ezt_no_expiry", user)

    expect(getToken()).toBe("ezt_no_expiry")
  })

  it("migrates a legacy sessionStorage session exactly once", () => {
    window.sessionStorage.setItem(TOKEN_KEY, "ezt_legacy")
    window.sessionStorage.setItem(USER_KEY, JSON.stringify(user))

    expect(getToken()).toBe("ezt_legacy")
    expect(getStoredUser()).toEqual(user)
    expect(window.localStorage.getItem(TOKEN_KEY)).toBe("ezt_legacy")
    expect(window.sessionStorage.getItem(TOKEN_KEY)).toBeNull()
    expect(window.sessionStorage.getItem(USER_KEY)).toBeNull()

    // A second legacy write is discarded: the browser has already migrated and
    // localStorage is the single source of truth.
    window.sessionStorage.setItem(TOKEN_KEY, "ezt_second")
    expect(getToken()).toBe("ezt_legacy")
    expect(window.sessionStorage.getItem(TOKEN_KEY)).toBeNull()
  })

  it("keeps a newer localStorage session instead of a legacy copy", () => {
    setSession("ezt_local", user)
    window.sessionStorage.setItem(TOKEN_KEY, "ezt_stale")

    expect(getToken()).toBe("ezt_local")
    expect(window.sessionStorage.getItem(TOKEN_KEY)).toBeNull()
  })

  it("normalizes a numeric id to the decimal-string contract", () => {
    setStoredUser({ ...user, id: 9001 as unknown as string })
    expect(getStoredUser()?.id).toBe("9001")
  })

  it("falls back to the username when display_name is missing", () => {
    window.localStorage.setItem(
      USER_KEY,
      JSON.stringify({ id: "9001", username: "admin", role: "user", status: "active" }),
    )
    expect(getStoredUser()?.display_name).toBe("admin")
  })

  it("rejects a stored user that is not a valid public account", () => {
    window.localStorage.setItem(USER_KEY, JSON.stringify({ id: "9001", username: "" }))
    expect(getStoredUser()).toBeNull()

    window.localStorage.setItem(USER_KEY, "not-json")
    expect(getStoredUser()).toBeNull()
  })

  it("clears the token and notifies listeners", () => {
    const listener = vi.fn()
    window.addEventListener(AUTH_CLEARED_EVENT, listener)
    setSession("ezt_abc", user, "2099-01-01T00:00:00Z")

    clearToken()

    expect(getToken()).toBeNull()
    expect(window.localStorage.getItem(EXPIRES_KEY)).toBeNull()
    expect(listener).toHaveBeenCalledTimes(1)
    window.removeEventListener(AUTH_CLEARED_EVENT, listener)
  })

  it("clears both token and user from both storage areas for a full sign-out", () => {
    setSession("ezt_abc", user, "2099-01-01T00:00:00Z")
    window.sessionStorage.setItem(TOKEN_KEY, "ezt_legacy")
    window.sessionStorage.setItem(USER_KEY, JSON.stringify(user))
    window.sessionStorage.setItem(EXPIRES_KEY, "2099-01-01T00:00:00Z")

    clearSession()

    for (const storage of [window.localStorage, window.sessionStorage]) {
      expect(storage.getItem(TOKEN_KEY)).toBeNull()
      expect(storage.getItem(USER_KEY)).toBeNull()
      expect(storage.getItem(EXPIRES_KEY)).toBeNull()
    }
  })
})

describe("toAuthUser", () => {
  it("normalizes a valid public account, including a numeric id", () => {
    expect(
      toAuthUser({
        id: 9001,
        username: "admin",
        display_name: "管理员",
        role: "admin",
        status: "active",
      }),
    ).toEqual(user)
  })

  it("defaults display_name to the username", () => {
    expect(
      toAuthUser({ id: "9001", username: "admin", role: "user", status: "active" })?.display_name,
    ).toBe("admin")
  })

  it("rejects malformed accounts", () => {
    expect(toAuthUser(null)).toBeNull()
    expect(toAuthUser("admin")).toBeNull()
    expect(toAuthUser({ id: "9001", username: "" })).toBeNull()
    expect(
      toAuthUser({ id: "9001", username: "admin", role: "owner", status: "active" }),
    ).toBeNull()
    expect(
      toAuthUser({ id: "9001", username: "admin", role: "admin", status: "locked" }),
    ).toBeNull()
  })
})

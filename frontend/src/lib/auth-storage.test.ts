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

describe("auth-storage", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
  })

  afterEach(() => {
    window.sessionStorage.clear()
  })

  it("returns null before a token is stored", () => {
    expect(getToken()).toBeNull()
    expect(getStoredUser()).toBeNull()
  })

  it("stores and reads the bearer token", () => {
    setToken("ezt_abc")
    expect(getToken()).toBe("ezt_abc")
  })

  it("stores a complete session and reads the public user back", () => {
    setSession("ezt_abc", user)

    expect(getToken()).toBe("ezt_abc")
    expect(getStoredUser()).toEqual(user)
  })

  it("normalizes a numeric id to the decimal-string contract", () => {
    setStoredUser({ ...user, id: 9001 as unknown as string })
    expect(getStoredUser()?.id).toBe("9001")
  })

  it("falls back to the username when display_name is missing", () => {
    window.sessionStorage.setItem(
      "eztest.user",
      JSON.stringify({ id: "9001", username: "admin", role: "user", status: "active" }),
    )
    expect(getStoredUser()?.display_name).toBe("admin")
  })

  it("rejects a stored user that is not a valid public account", () => {
    window.sessionStorage.setItem("eztest.user", JSON.stringify({ id: "9001", username: "" }))
    expect(getStoredUser()).toBeNull()

    window.sessionStorage.setItem("eztest.user", "not-json")
    expect(getStoredUser()).toBeNull()
  })

  it("clears the token and notifies listeners", () => {
    const listener = vi.fn()
    window.addEventListener(AUTH_CLEARED_EVENT, listener)
    setToken("ezt_abc")

    clearToken()

    expect(getToken()).toBeNull()
    expect(listener).toHaveBeenCalledTimes(1)
    window.removeEventListener(AUTH_CLEARED_EVENT, listener)
  })

  it("clears both token and user for a full sign-out", () => {
    setSession("ezt_abc", user)

    clearSession()

    expect(getToken()).toBeNull()
    expect(getStoredUser()).toBeNull()
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

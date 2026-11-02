"use client"

import { getStoredUser } from "@/lib/auth-storage"

/**
 * Reads the cached session role. The server still enforces admin-only actions
 * (baseline approval, proposal confirmation); this only decides whether the UI
 * offers them (spec §4.1/§7.2).
 */
export function useIsAdmin(): boolean {
  return getStoredUser()?.role === "admin"
}

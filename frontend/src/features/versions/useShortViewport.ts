"use client"

import { useEffect, useState } from "react"

/**
 * The version page chrome (header + stage bar) compresses below this viewport
 * height so the inline design workspace keeps a usable canvas (R2-D-01). It is
 * also the signal for the design workspace to reclaim the reserved page bottom
 * inset (R2-D-02).
 */
export const SHORT_VIEWPORT_MEDIA_QUERY = "(max-height: 840px)"

/**
 * Tracks the short-viewport media query. The first render is always `false` so
 * server rendering and hydration agree; the effect then reads the real query and
 * follows its `change` events. Environments without `matchMedia` (SSR, older
 * jsdom) stay at `false` instead of throwing.
 */
export function useShortViewport(): boolean {
  const [shortViewport, setShortViewport] = useState(false)

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return undefined
    }
    const query = window.matchMedia(SHORT_VIEWPORT_MEDIA_QUERY)
    const update = (): void => setShortViewport(query.matches)
    update()
    if (typeof query.addEventListener === "function") {
      query.addEventListener("change", update)
      return () => query.removeEventListener("change", update)
    }
    // Safari < 14 only exposes the deprecated listener API.
    query.addListener(update)
    return () => query.removeListener(update)
  }, [])

  return shortViewport
}

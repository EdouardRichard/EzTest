import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SHORT_VIEWPORT_MEDIA_QUERY, useShortViewport } from "./useShortViewport"

/**
 * jsdom's matchMedia is a static false stub (vitest.setup.ts). The layout fix
 * needs a controllable query: `matches` is mutable and the change listeners are
 * real, so the hook is exercised through the same API a browser uses.
 */
function mockShortViewportMedia(initialMatches: boolean) {
  const listeners = new Set<(event: MediaQueryListEvent) => void>()
  const media = {
    matches: initialMatches,
    media: SHORT_VIEWPORT_MEDIA_QUERY,
    onchange: null,
    addEventListener: vi.fn((_type: string, listener: (event: MediaQueryListEvent) => void) => {
      listeners.add(listener)
    }),
    removeEventListener: vi.fn((_type: string, listener: (event: MediaQueryListEvent) => void) => {
      listeners.delete(listener)
    }),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(() => false),
  }
  vi.spyOn(window, "matchMedia").mockReturnValue(media as unknown as MediaQueryList)
  return {
    media,
    setMatches(next: boolean): void {
      media.matches = next
      for (const listener of listeners) listener({ matches: next } as MediaQueryListEvent)
    },
    listenerCount(): number {
      return listeners.size
    },
  }
}

describe("useShortViewport", () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("is false on a tall viewport", async () => {
    const viewport = mockShortViewportMedia(false)

    const { result } = renderHook(() => useShortViewport())

    await waitFor(() => expect(window.matchMedia).toHaveBeenCalledWith(SHORT_VIEWPORT_MEDIA_QUERY))
    expect(result.current).toBe(false)
    expect(viewport.listenerCount()).toBe(1)
  })

  it("is true when the max-height media query matches", async () => {
    mockShortViewportMedia(true)

    const { result } = renderHook(() => useShortViewport())

    await waitFor(() => expect(result.current).toBe(true))
  })

  it("follows media-query changes and unsubscribes on unmount", async () => {
    const viewport = mockShortViewportMedia(false)

    const { result, unmount } = renderHook(() => useShortViewport())
    await waitFor(() => expect(result.current).toBe(false))

    act(() => viewport.setMatches(true))
    expect(result.current).toBe(true)

    unmount()
    expect(viewport.listenerCount()).toBe(0)
  })

  it("stays false when matchMedia is unavailable (SSR-safe)", () => {
    vi.stubGlobal("matchMedia", undefined)

    const { result } = renderHook(() => useShortViewport())

    expect(result.current).toBe(false)
  })
})

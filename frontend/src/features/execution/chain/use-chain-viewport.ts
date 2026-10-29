"use client"

import { useReactFlow } from "@xyflow/react"
import { useCallback, useEffect, useRef, useState } from "react"

/** Media query that disables the chain's motion effects (spec §5.7). */
export const CHAIN_REDUCED_MOTION_QUERY = "(prefers-reduced-motion: reduce)"

/** Zoom applied when the viewport follows a running result. */
export const CHAIN_FOLLOW_ZOOM = 1.1
/** Animation duration of one follow jump; reduced motion jumps instantly. */
export const CHAIN_FOLLOW_DURATION_MS = 450

function reducedMotionQuery(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return null
  return window.matchMedia(CHAIN_REDUCED_MOTION_QUERY)
}

/**
 * Tracks the user's `prefers-reduced-motion` preference.
 *
 * The initial state is read synchronously on the client so the first render
 * already knows whether it may animate; the effect keeps it in sync with later
 * OS changes. On the server (where `matchMedia` does not exist) it defaults to
 * `false` and the client corrects it on hydration.
 */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState<boolean>(() => reducedMotionQuery()?.matches ?? false)

  useEffect(() => {
    const query = reducedMotionQuery()
    if (query === null) return
    setReduced(query.matches)

    const onChange = (event: MediaQueryListEvent): void => setReduced(event.matches)
    // Safari < 14 only ships the deprecated listener pair.
    if (typeof query.addEventListener === "function") {
      query.addEventListener("change", onChange)
      return () => query.removeEventListener("change", onChange)
    }
    query.addListener(onChange)
    return () => query.removeListener(onChange)
  }, [])

  return reduced
}

/** A positioned node the viewport may follow. */
export type ChainViewportTarget = {
  id: string
  x: number
  y: number
  width: number
  height: number
}

export type ChainViewportOptions = {
  /** Whether follow-execution is enabled by the toolbar. */
  follow: boolean
  /** The running result to focus, or null when nothing is running. */
  target: ChainViewportTarget | null
  /** Reduced motion turns the follow jump into an instant jump. */
  reducedMotion?: boolean
}

export type ChainViewportController = {
  /** True once the user panned/zoomed away from the followed node. */
  followPaused: boolean
  /** Called on a user-initiated pan/zoom start. */
  pauseFollow: () => void
  /** Resumes following and re-centres the current running result. */
  resumeFollow: () => void
}

/**
 * Follow-execution viewport controller (spec §5.5).
 *
 * While `follow` is on and a result is running, the viewport is centred on that
 * result exactly once per running node. A user-initiated pan/zoom pauses the
 * follow so the canvas never fights the user; re-enabling follow resumes it.
 *
 * Programmatic moves (`setCenter` itself) report a `null` event to
 * `onMoveStart`, so only real gestures pause the follow. Snapshot refreshes keep
 * the same node id and therefore never re-centre or re-fit the canvas.
 */
export function useChainViewport({
  follow,
  target,
  reducedMotion = false,
}: ChainViewportOptions): ChainViewportController {
  const { setCenter, viewportInitialized } = useReactFlow()
  const [followPaused, setFollowPaused] = useState(false)
  const lastFocusedIdRef = useRef<string | null>(null)
  const previousFollowRef = useRef(follow)

  // Turning follow off resets the remembered node; turning it on again starts a
  // fresh follow (and clears a pause left over from the previous session).
  useEffect(() => {
    const wasFollowing = previousFollowRef.current
    previousFollowRef.current = follow
    if (!follow) {
      lastFocusedIdRef.current = null
      return
    }
    if (!wasFollowing) {
      lastFocusedIdRef.current = null
      setFollowPaused(false)
    }
  }, [follow])

  useEffect(() => {
    if (!follow || followPaused || !viewportInitialized || target === null) return
    // A data refresh must not yank the viewport back: one focus per running node
    // is enough, even when nobody moved the canvas in between.
    if (lastFocusedIdRef.current === target.id) return
    lastFocusedIdRef.current = target.id
    void setCenter(target.x + target.width / 2, target.y + target.height / 2, {
      zoom: CHAIN_FOLLOW_ZOOM,
      duration: reducedMotion ? 0 : CHAIN_FOLLOW_DURATION_MS,
    })
  }, [follow, followPaused, viewportInitialized, target, reducedMotion, setCenter])

  const pauseFollow = useCallback(() => {
    setFollowPaused(true)
  }, [])

  const resumeFollow = useCallback(() => {
    lastFocusedIdRef.current = null
    setFollowPaused(false)
  }, [])

  return { followPaused, pauseFollow, resumeFollow }
}

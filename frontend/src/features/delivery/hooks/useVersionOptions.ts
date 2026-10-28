"use client"

import { useCallback } from "react"

import { listVersionOptions } from "../api"
import type { VersionOption } from "../types"
import { useDeliveryPage } from "./useDeliveryResource"

function neverRefresh(): boolean {
  return false
}

export type UseVersionOptionsResult = ReturnType<typeof useDeliveryPage<VersionOption>> & {
  /** Alias of `items` for the version filter selectors. */
  options: VersionOption[]
}

/**
 * One page (up to 100) of versions for the global delivery filters. The global
 * centres are read-only, so no live event type refreshes this list.
 */
export function useVersionOptions(): UseVersionOptionsResult {
  const fetcher = useCallback(
    (_cursor: string, signal: AbortSignal) => listVersionOptions({ signal }),
    [],
  )
  const page = useDeliveryPage<VersionOption>({
    versionId: "",
    key: "delivery|version-options",
    enabled: true,
    fetcher,
    isRelevantEvent: neverRefresh,
  })
  return { ...page, options: page.items }
}

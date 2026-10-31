import type { VersionEvent } from "@/features/clarification/types"
import type { RunWave } from "@/features/execution/types"
import { apiGet, apiGetPage } from "@/lib/api-client"

import type { ChainEventListOptions, ChainQuery, ChainView } from "./types"

/** Builds `?a=1&b=2`, skipping empty values (P0/P1 pagination style). */
function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === "") continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text.length > 0 ? `?${text}` : ""
}

function versionPath(versionId: string): string {
  return `/api/versions/${encodeURIComponent(versionId)}`
}

/**
 * `GET /api/versions/{id}/exploration-chain` (spec §8). The server assembles
 * the graph from the five read providers; the UI never reads other domains.
 */
export function getChain(versionId: string, options: ChainQuery = {}): Promise<ChainView> {
  return apiGet<ChainView>(
    `${versionPath(versionId)}/exploration-chain${query({
      sut_id: options.sutId,
      page_id: options.pageId,
      wave_id: options.waveId,
      status: options.status,
      result: options.result,
      focus_node: options.focusNode,
      event_from: options.eventFrom,
      event_to: options.eventTo,
      limit: options.limit ?? 300,
    })}`,
  )
}

/** Wave options for the historical replay filter (P3 read API). */
export async function listChainWaves(versionId: string): Promise<RunWave[]> {
  const page = await apiGetPage<RunWave>(`${versionPath(versionId)}/waves?limit=50`)
  return page.items
}

/**
 * `GET /api/events` for `version:{id}`. P4 reuses the P0 event endpoint for the
 * approximate replay timeline; it never adds a new events endpoint or channel.
 */
export async function listChainEvents(
  versionId: string,
  options: ChainEventListOptions = {},
): Promise<VersionEvent[]> {
  const page = await apiGetPage<VersionEvent>(
    `/api/events${query({
      channel: `version:${versionId}`,
      limit: options.limit ?? 20,
    })}`,
  )
  return page.items
}

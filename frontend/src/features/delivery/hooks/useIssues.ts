"use client"

import { useCallback } from "react"

import type { VersionEvent } from "@/features/versions/types"

import { getIssue, listGlobalIssues, listIssues } from "../api"
import type {
  DeliveryStream,
  GlobalIssue,
  GlobalIssueFilters,
  IssueDetail,
  IssueListFilters,
  IssueSummary,
} from "../types"
import {
  type DeliveryPage,
  type DeliveryResource,
  useDeliveryPage,
  useDeliveryResource,
} from "./useDeliveryResource"

/** issue.created / issue.updated (and any later issue.* type) refresh the view. */
function isIssueEvent(event: VersionEvent): boolean {
  return event.type.startsWith("issue.")
}

/** Rebuilds the filter object without undefined/empty keys for stable requests. */
function normalizeFilters(filters: IssueListFilters): IssueListFilters {
  const normalized: IssueListFilters = {}
  if (filters.status !== undefined && filters.status !== "") normalized.status = filters.status
  if (filters.severity !== undefined && filters.severity !== "") {
    normalized.severity = filters.severity
  }
  if (filters.category !== undefined && filters.category !== "") {
    normalized.category = filters.category
  }
  if (filters.batchId !== undefined && filters.batchId !== "") normalized.batchId = filters.batchId
  if (filters.caseRunId !== undefined && filters.caseRunId !== "") {
    normalized.caseRunId = filters.caseRunId
  }
  if (filters.caseNodeId !== undefined && filters.caseNodeId !== "") {
    normalized.caseNodeId = filters.caseNodeId
  }
  if (filters.q !== undefined && filters.q !== "") normalized.q = filters.q
  if (filters.limit !== undefined) normalized.limit = filters.limit
  return normalized
}

export type UseIssuesResult = DeliveryPage<IssueSummary> & {
  /** Alias of `items` for the issue list. */
  issues: IssueSummary[]
}

/**
 * One cursor page of the version's issues (spec §7.3). A provided version
 * stream refreshes the page on every issue.* event (spec §10.8); the event
 * payload is never merged into the rows, so an out-of-order frame cannot
 * corrupt the projection.
 */
export function useIssues(
  versionId: string,
  filters: IssueListFilters = {},
  stream?: DeliveryStream,
): UseIssuesResult {
  const status = filters.status ?? ""
  const severity = filters.severity ?? ""
  const category = filters.category ?? ""
  const batchId = filters.batchId ?? ""
  const caseRunId = filters.caseRunId ?? ""
  const caseNodeId = filters.caseNodeId ?? ""
  const q = filters.q ?? ""
  const limit = filters.limit

  const key = [
    versionId,
    "issues",
    status,
    severity,
    category,
    batchId,
    caseRunId,
    caseNodeId,
    q,
    limit ?? "",
  ].join("|")

  const fetcher = useCallback(
    (cursor: string, signal: AbortSignal) => {
      const normalized = normalizeFilters({
        status: status as IssueListFilters["status"],
        severity: severity as IssueListFilters["severity"],
        category: category as IssueListFilters["category"],
        batchId,
        caseRunId,
        caseNodeId,
        q,
        limit,
      })
      if (cursor !== "") normalized.cursor = cursor
      return listIssues(versionId, normalized, { signal })
    },
    [versionId, status, severity, category, batchId, caseRunId, caseNodeId, q, limit],
  )

  const page = useDeliveryPage<IssueSummary>({
    versionId,
    key,
    enabled: versionId !== "",
    fetcher,
    stream,
    isRelevantEvent: isIssueEvent,
  })

  return { ...page, issues: page.items }
}

export type UseIssueResult = DeliveryResource<IssueDetail> & {
  /** Alias of `data` for the issue detail. */
  issue: IssueDetail | null
}

/** One issue detail, refreshed by live issue.* events (spec §7.3). */
export function useIssue(
  versionId: string,
  issueId: string,
  stream?: DeliveryStream,
): UseIssueResult {
  const fetcher = useCallback(
    (signal: AbortSignal) => getIssue(versionId, issueId, { signal }),
    [versionId, issueId],
  )
  const resource = useDeliveryResource<IssueDetail>({
    versionId,
    key: `${versionId}|issue|${issueId}`,
    enabled: versionId !== "" && issueId !== "",
    fetcher,
    stream,
    isRelevantEvent: isIssueEvent,
  })
  return { ...resource, issue: resource.data }
}

export type UseGlobalIssuesResult = DeliveryPage<GlobalIssue> & {
  /** Alias of `items` for the cross-version issue list. */
  issues: GlobalIssue[]
}

/**
 * The read-only cross-version issue centre (spec §7.10). Global lists have no
 * live channel and no batch context, so only the explicit filters apply.
 */
export function useGlobalIssues(filters: GlobalIssueFilters = {}): UseGlobalIssuesResult {
  const versionId = filters.versionId ?? ""
  const status = filters.status ?? ""
  const severity = filters.severity ?? ""
  const category = filters.category ?? ""
  const q = filters.q ?? ""
  const limit = filters.limit
  const key = ["global-issues", versionId, status, severity, category, q, limit ?? ""].join("|")

  const fetcher = useCallback(
    (cursor: string, signal: AbortSignal) => {
      const normalized: GlobalIssueFilters = {}
      if (versionId !== "") normalized.versionId = versionId
      if (status !== "") normalized.status = status as GlobalIssueFilters["status"]
      if (severity !== "") normalized.severity = severity as GlobalIssueFilters["severity"]
      if (category !== "") normalized.category = category as GlobalIssueFilters["category"]
      if (q !== "") normalized.q = q
      if (limit !== undefined) normalized.limit = limit
      if (cursor !== "") normalized.cursor = cursor
      return listGlobalIssues(normalized, { signal })
    },
    [versionId, status, severity, category, q, limit],
  )

  const page = useDeliveryPage<GlobalIssue>({
    versionId: "",
    key,
    enabled: true,
    fetcher,
    isRelevantEvent: isIssueEvent,
  })
  return { ...page, issues: page.items }
}

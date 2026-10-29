"use client"

import { useCallback } from "react"

import type { VersionEvent } from "@/features/versions/types"

import { getReport, listGlobalReports, listReportBatches, listReports } from "../api"
import type {
  DeliveryStream,
  GlobalReport,
  GlobalReportFilters,
  ReportBatchSummary,
  ReportDetail,
  ReportListFilters,
  ReportSummary,
} from "../types"
import {
  type DeliveryPage,
  type DeliveryResource,
  useDeliveryPage,
  useDeliveryResource,
} from "./useDeliveryResource"

/** report.published / report.stale (and any later report.* type) refresh. */
function isReportEvent(event: VersionEvent): boolean {
  return event.type.startsWith("report.")
}

/** Report lists never change on their own: only an explicit refresh re-reads. */
function neverRefresh(): boolean {
  return false
}

export type UseReportsResult = DeliveryPage<ReportSummary> & {
  /** Alias of `items` for the report list. */
  reports: ReportSummary[]
}

/**
 * One cursor page of the version's reports, optionally scoped to a batch
 * (spec §7.4). A provided version stream refreshes the page on report.* events.
 */
export function useReports(
  versionId: string,
  filters: ReportListFilters = {},
  stream?: DeliveryStream,
): UseReportsResult {
  const batchId = filters.batchId ?? ""
  const status = filters.status ?? ""
  const limit = filters.limit

  const key = [versionId, "reports", batchId, status, limit ?? ""].join("|")

  const fetcher = useCallback(
    (cursor: string, signal: AbortSignal) => {
      const normalized: ReportListFilters = {}
      if (batchId !== "") normalized.batchId = batchId
      if (status !== "") normalized.status = status as ReportListFilters["status"]
      if (limit !== undefined) normalized.limit = limit
      if (cursor !== "") normalized.cursor = cursor
      return listReports(versionId, normalized, { signal })
    },
    [versionId, batchId, status, limit],
  )

  const page = useDeliveryPage<ReportSummary>({
    versionId,
    key,
    enabled: versionId !== "",
    fetcher,
    stream,
    isRelevantEvent: isReportEvent,
  })

  return { ...page, reports: page.items }
}

export type UseReportResult = DeliveryResource<ReportDetail> & {
  /** Alias of `data` for the report detail. */
  report: ReportDetail | null
}

/** One report revision, refreshed by live report.* events (spec §7.4). */
export function useReport(
  versionId: string,
  reportId: string,
  stream?: DeliveryStream,
): UseReportResult {
  const fetcher = useCallback(
    (signal: AbortSignal) => getReport(versionId, reportId, { signal }),
    [versionId, reportId],
  )
  const resource = useDeliveryResource<ReportDetail>({
    versionId,
    key: `${versionId}|report|${reportId}`,
    enabled: versionId !== "" && reportId !== "",
    fetcher,
    stream,
    isRelevantEvent: isReportEvent,
  })
  return { ...resource, report: resource.data }
}

export type UseReportBatchesResult = DeliveryPage<ReportBatchSummary> & {
  /** Alias of `items` for the batch selector. */
  batches: ReportBatchSummary[]
}

/**
 * The version's batches for the report selector (spec §10.4). It is a single
 * page of up to 100 newest batches; the selector does not paginate in P7.
 */
export function useReportBatches(
  versionId: string,
  stream?: DeliveryStream,
): UseReportBatchesResult {
  const fetcher = useCallback(
    (_cursor: string, signal: AbortSignal) => listReportBatches(versionId, { signal }),
    [versionId],
  )
  const page = useDeliveryPage<ReportBatchSummary>({
    versionId,
    key: `${versionId}|report-batches`,
    enabled: versionId !== "",
    fetcher,
    stream,
    isRelevantEvent: neverRefresh,
  })
  return { ...page, batches: page.items }
}

export type UseGlobalReportsResult = DeliveryPage<GlobalReport> & {
  /** Alias of `items` for the cross-version report list. */
  reports: GlobalReport[]
}

/** The read-only cross-version report centre (spec §7.10). */
export function useGlobalReports(filters: GlobalReportFilters = {}): UseGlobalReportsResult {
  const versionId = filters.versionId ?? ""
  const status = filters.status ?? ""
  const limit = filters.limit
  const key = ["global-reports", versionId, status, limit ?? ""].join("|")

  const fetcher = useCallback(
    (cursor: string, signal: AbortSignal) => {
      const normalized: GlobalReportFilters = {}
      if (versionId !== "") normalized.versionId = versionId
      if (status !== "") normalized.status = status as GlobalReportFilters["status"]
      if (limit !== undefined) normalized.limit = limit
      if (cursor !== "") normalized.cursor = cursor
      return listGlobalReports(normalized, { signal })
    },
    [versionId, status, limit],
  )

  const page = useDeliveryPage<GlobalReport>({
    versionId: "",
    key,
    enabled: true,
    fetcher,
    isRelevantEvent: neverRefresh,
  })
  return { ...page, reports: page.items }
}

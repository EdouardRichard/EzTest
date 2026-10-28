import type { Batch, BatchKind } from "../types"

/** Chinese labels of the three batch provenances (spec §10.6). */
export const BATCH_KIND_LABELS: Record<BatchKind, string> = {
  baseline: "基线",
  incremental: "增量",
  supplement: "补全",
}

/**
 * Effective provenance of a batch. P6 rows predate the `kind` column and the
 * backend maps them to `baseline`, so the UI applies the same default instead
 * of rendering "undefined".
 */
export function effectiveBatchKind(batch: Pick<Batch, "kind">): BatchKind {
  const kind = batch.kind
  return kind === "incremental" || kind === "supplement" ? kind : "baseline"
}

/** One-line option label: `批次 #2 · 增量（已完成）`. */
export function batchOptionLabel(batch: Batch, statusLabel: string): string {
  return `批次 #${batch.batch_no} · ${BATCH_KIND_LABELS[effectiveBatchKind(batch)]}（${statusLabel}）`
}

/**
 * Parent description of a chained batch. The parent's batch number is used when
 * the page already contains it; otherwise the decimal id is shown verbatim.
 */
export function batchParentLabel(batch: Batch, batches: readonly Batch[]): string {
  const parentId = batch.parentBatchId ?? ""
  if (parentId === "") return ""
  const parent = batches.find((candidate) => candidate.id === parentId)
  return parent === undefined ? `父批次 ${parentId}` : `父批次 #${parent.batch_no}`
}

/** Supplement batches are always created by the supplement Agent (spec §10.6). */
export function batchOriginLabel(batch: Pick<Batch, "kind">): string {
  return effectiveBatchKind(batch) === "supplement" ? "由补全 Agent 创建" : ""
}

/** Colour-independent label of one batch kind, used by badges and options. */
export function batchKindLabel(batch: Pick<Batch, "kind">): string {
  return BATCH_KIND_LABELS[effectiveBatchKind(batch)]
}

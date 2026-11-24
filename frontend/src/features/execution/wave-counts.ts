import type { CaseRunStatus } from "./types"

/**
 * The per-status counters of `run_wave.counts`, in backend field order.
 *
 * Single source (constitution 五.1/五.4): the authority is the Go `WaveCounts`
 * struct in `backend/internal/execution/domain/wave.go` — its status fields are
 * every integer field except the `total`/`evidence` aggregates and the `by_sut`
 * map. This tuple mirrors those status keys. `wave-counts.authority.test.ts`
 * reads the Go struct and fails CI if a backend addition/removal drifts from
 * this set. The parser matches the json tag NAME in any case/digits and allows
 * comma options (`,omitempty`), so a new count key is not silently missed on the
 * house tag style; mutation tests pin that property (review I1).
 *
 * `satisfies readonly CaseRunStatus[]` additionally ties every key to the
 * case-run status vocabulary at compile time.
 */
export const COUNT_STATUS_KEYS = [
  "pending",
  "running",
  "paused",
  "passed",
  "failed",
  "blocked",
  "inconclusive",
  "skipped",
  "interrupted",
  "superseded",
] as const satisfies readonly CaseRunStatus[]

export type CountStatusKey = (typeof COUNT_STATUS_KEYS)[number]

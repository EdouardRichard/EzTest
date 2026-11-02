/** Shared wire fixtures for the P5 visual feature tests. */

import type {
  VisualAssertion,
  VisualBaseline,
  VisualDiff,
  VisualDiffDetail,
  VisualMetrics,
  VisualOverview,
} from "./types"

const TIMESTAMP = "2026-11-01T10:00:00Z"
const APPROVED_AT = "2026-11-01T10:05:00Z"

export const visualAssertionFixture: VisualAssertion = {
  id: "7101",
  version_id: "2001",
  case_version_id: "7002",
  case_assertion_id: "6101",
  step_seq: 1,
  assertion_seq: 2,
  version_sut_id: "1002",
  selector: "#order-total",
  region: null,
  threshold: 0.005,
  viewport_width: 1280,
  viewport_height: 720,
  device_scale_factor: 1,
  wait_policy: { network_idle: true, fonts_ready: true },
  config_revision: 1,
  status: "active",
  baseline: {
    id: "7201",
    sha256: "c".repeat(64),
    width: 1280,
    height: 720,
    config_revision: 1,
    environment_fingerprint: "d".repeat(64),
    approved_at: APPROVED_AT,
  },
  created_at: TIMESTAMP,
  updated_at: TIMESTAMP,
}

export const visualAssertionWithoutBaselineFixture: VisualAssertion = {
  ...visualAssertionFixture,
  id: "7102",
  case_assertion_id: "6102",
  step_seq: 2,
  assertion_seq: 2,
  selector: "",
  config_revision: 2,
  baseline: null,
}

export const visualBaselineFixture: VisualBaseline = {
  id: "7201",
  visual_assertion_id: "7101",
  case_version_id: "7002",
  version_sut_id: "1002",
  evidence_id: "9401",
  sha256: "c".repeat(64),
  size_bytes: 20_480,
  width: 1280,
  height: 720,
  viewport_width: 1280,
  viewport_height: 720,
  device_scale_factor: 1,
  config_revision: 1,
  environment_fingerprint: "d".repeat(64),
  status: "approved",
  approved_by: "9001",
  approved_at: APPROVED_AT,
  superseded_at: null,
  created_at: TIMESTAMP,
}

export const visualDiffPassedFixture: VisualDiff = {
  id: "7301",
  visual_assertion_id: "7101",
  case_run_id: "9301",
  assertion_result_id: "9101",
  baseline_id: "7201",
  actual_evidence_id: "9401",
  diff_evidence_id: "9402",
  diff_ratio: 0,
  threshold: 0.005,
  classification: "passed",
  reason: "visual_match",
  environment_match: true,
  width: 1280,
  height: 720,
  created_at: TIMESTAMP,
}

export const visualDiffFailedFixture: VisualDiff = {
  ...visualDiffPassedFixture,
  id: "7302",
  diff_ratio: 0.012,
  classification: "failed",
  reason: "visual_mismatch",
}

export const visualDiffInconclusiveFixture: VisualDiff = {
  ...visualDiffPassedFixture,
  id: "7303",
  diff_evidence_id: null,
  diff_ratio: 0,
  classification: "inconclusive",
  reason: "missing_visual_baseline",
  environment_match: true,
}

export const visualDiffDetailFixture: VisualDiffDetail = {
  ...visualDiffFailedFixture,
  mismatch_boxes: [
    { x: 32, y: 64, width: 32, height: 32 },
    { x: 96, y: 64, width: 32, height: 32 },
  ],
  environment: {
    viewport_width: 1280,
    viewport_height: 720,
    device_scale_factor: 1,
    browser: "chromium",
    version: "141.0",
  },
}

export const visualOverviewFixture: VisualOverview = {
  version_id: "2001",
  assertion_total: 2,
  baseline_covered: 1,
  baseline_coverage_ratio: 0.5,
  passed: 3,
  failed: 1,
  inconclusive: 1,
  blocked: 0,
  pass_ratio: 0.75,
  inconclusive_reasons: [
    { reason: "missing_visual_baseline", count: 1 },
    { reason: "visual_environment_mismatch", count: 0 },
  ],
  diff_latency_ms: { p50_ms: 120, p95_ms: 340 },
  recent_failures: [visualDiffFailedFixture],
}

export const visualMetricsFixture: VisualMetrics = {
  scope: { version_id: "2001" },
  totals: {
    assertions: 2,
    baselines: 1,
    diffs: 5,
    passed: 3,
    failed: 1,
    inconclusive: 1,
    blocked: 0,
  },
  ratios: {
    baseline_coverage: 0.5,
    pass_rate: 0.75,
    inconclusive_rate: 0.2,
  },
  diff_latency_ms: { p50_ms: 120, p95_ms: 340 },
  top_inconclusive_reasons: [{ reason: "missing_visual_baseline", count: 1 }],
  generated_at: TIMESTAMP,
}

export const visualMetricsEmptyFixture: VisualMetrics = {
  scope: { version_id: null },
  totals: {
    assertions: 0,
    baselines: 0,
    diffs: 0,
    passed: 0,
    failed: 0,
    inconclusive: 0,
    blocked: 0,
  },
  ratios: { baseline_coverage: null, pass_rate: null, inconclusive_rate: null },
  diff_latency_ms: null,
  top_inconclusive_reasons: [],
  generated_at: TIMESTAMP,
}

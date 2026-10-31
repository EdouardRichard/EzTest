import type {
  CaseRun,
  CaseRunCounts,
  CaseRunDetail,
  Evidence,
  ExecutionOverview,
  HumanRequest,
  Intent,
  RunWave,
  WaveCounts,
} from "./types"

const TIMESTAMP = "2026-10-31T12:00:00Z"
const LATER = "2026-10-31T12:01:02Z"

export const waveCountsFixture: WaveCounts = {
  total: 4,
  pending: 1,
  running: 1,
  paused: 0,
  passed: 2,
  failed: 1,
  blocked: 0,
  inconclusive: 0,
  skipped: 0,
  interrupted: 0,
  superseded: 0,
  evidence: 7,
  by_version_sut: {
    "1001": { total: 3, passed: 1, failed: 1, blocked: 0, inconclusive: 0 },
    "1002": { total: 2, passed: 1, failed: 1, blocked: 0, inconclusive: 0 },
  },
}

export const runningWaveFixture: RunWave = {
  id: "9201",
  version_id: "2001",
  status: "running",
  trigger: "auto",
  counts: waveCountsFixture,
  started_at: TIMESTAMP,
  finished_at: null,
  paused_at: null,
  pause_mode: "",
  pause_reason: "",
  created_at: TIMESTAMP,
}

export const pausedWaveFixture: RunWave = {
  ...runningWaveFixture,
  id: "9202",
  status: "paused",
  paused_at: "2026-10-31T12:00:30Z",
  pause_mode: "graceful",
  pause_reason: "等待登录接管",
  counts: { ...waveCountsFixture, running: 0, paused: 1, pending: 1 },
}

export const doneWaveFixture: RunWave = {
  ...runningWaveFixture,
  id: "9199",
  status: "done",
  started_at: "2026-10-31T11:00:00Z",
  finished_at: "2026-10-31T11:02:00Z",
  counts: {
    ...waveCountsFixture,
    pending: 0,
    running: 0,
    passed: 3,
    failed: 1,
    total: 4,
  },
}

/** Version-wide projection (contract `CaseRunCounts` has no evidence/by-SUT keys). */
export const caseRunCountsFixture: CaseRunCounts = {
  total: 4,
  pending: 1,
  running: 1,
  paused: 0,
  passed: 2,
  failed: 1,
  blocked: 0,
  inconclusive: 0,
  skipped: 0,
  interrupted: 0,
  superseded: 0,
}

export const executionOverviewFixture: ExecutionOverview = {
  version_id: "2001",
  active_wave: runningWaveFixture,
  counts: waveCountsFixture,
  case_run_counts: caseRunCountsFixture,
  open_human_requests: 1,
  executor: {
    id: "9601",
    status: "active",
    endpoint: "http://127.0.0.1:13336",
    capacity: 8,
    active_contexts: 2,
    last_heartbeat: TIMESTAMP,
    version: "0.2.0-p3",
  },
  quotas: { global: 3, per_sut: 2, cross_system: 1 },
  human_request_timeout_seconds: 1800,
}

export const offlineExecutorOverviewFixture: ExecutionOverview = {
  ...executionOverviewFixture,
  open_human_requests: 0,
  executor: {
    id: "9601",
    status: "offline",
    endpoint: "http://127.0.0.1:13336",
    capacity: 8,
    active_contexts: 0,
    last_heartbeat: "2026-10-31T11:30:00Z",
    version: "0.2.0-p3",
  },
}

export const emptyOverviewFixture: ExecutionOverview = {
  ...executionOverviewFixture,
  active_wave: null,
  counts: {
    ...waveCountsFixture,
    total: 0,
    pending: 0,
    running: 0,
    passed: 0,
    failed: 0,
    blocked: 0,
    inconclusive: 0,
    evidence: 0,
  },
  case_run_counts: {
    ...caseRunCountsFixture,
    total: 0,
    pending: 0,
    running: 0,
    passed: 0,
    failed: 0,
    blocked: 0,
    inconclusive: 0,
  },
  open_human_requests: 0,
}

const baseCaseRun: CaseRun = {
  id: "9301",
  wave_id: "9201",
  intent_id: "9101",
  case_id: "7001",
  case_version_id: "7002",
  title: "退款 T+1 到账",
  status: "passed",
  failure_class: "",
  attempts: 1,
  summary: {
    verdict_reason: "",
    assertion_total: 2,
    assertion_failed: 0,
    step_total: 2,
    step_failed: 0,
    evidence_count: 2,
  },
  started_at: TIMESTAMP,
  finished_at: "2026-10-31T12:00:08Z",
  version_sut_ids: ["1002"],
  evidence_count: 2,
}

export const passedCaseRunFixture: CaseRun = baseCaseRun

export const failedCaseRunFixture: CaseRun = {
  ...baseCaseRun,
  id: "9302",
  intent_id: "9102",
  case_id: "7003",
  case_version_id: "7004",
  title: "跨系统退款金额一致",
  status: "failed",
  failure_class: "application",
  attempts: 1,
  summary: {
    verdict_reason: "assertion_failed",
    assertion_total: 2,
    assertion_failed: 1,
    step_total: 3,
    step_failed: 0,
    evidence_count: 3,
  },
  finished_at: "2026-10-31T12:00:41Z",
  version_sut_ids: ["1001", "1002"],
  evidence_count: 3,
}

export const blockedCaseRunFixture: CaseRun = {
  ...baseCaseRun,
  id: "9303",
  intent_id: "9103",
  case_id: "7005",
  case_version_id: "7006",
  title: "2FA 登录接管",
  status: "blocked",
  failure_class: "blocked",
  summary: { verdict_reason: "missing_account", evidence_count: 0 },
  finished_at: "2026-10-31T12:00:50Z",
  version_sut_ids: ["1001"],
  evidence_count: 0,
}

export const runningCaseRunFixture: CaseRun = {
  ...baseCaseRun,
  id: "9304",
  intent_id: "9104",
  case_id: "7007",
  case_version_id: "7008",
  title: "订单详情时间窗口",
  status: "running",
  finished_at: null,
  version_sut_ids: ["1001", "1002"],
  evidence_count: 1,
}

export const caseRunsFixture: CaseRun[] = [
  passedCaseRunFixture,
  failedCaseRunFixture,
  blockedCaseRunFixture,
  runningCaseRunFixture,
]

export const evidenceScreenshotFixture: Evidence = {
  id: "9401",
  case_run_id: "9302",
  version_sut_id: "1002",
  step_seq: 3,
  assertion_seq: 1,
  kind: "screenshot",
  sha256: "a".repeat(64),
  size_bytes: 20_480,
  mime: "image/png",
  filename: "step-3.png",
  created_at: TIMESTAMP,
}

export const evidenceTextFixture: Evidence = {
  id: "9402",
  case_run_id: "9302",
  version_sut_id: "1001",
  step_seq: 1,
  assertion_seq: null,
  kind: "text",
  sha256: "b".repeat(64),
  size_bytes: 128,
  mime: "text/plain",
  filename: "console.log",
  created_at: TIMESTAMP,
}

export const caseRunDetailFixture: CaseRunDetail = {
  case_run: failedCaseRunFixture,
  steps: [
    {
      seq: 1,
      target_version_sut_id: "1001",
      status: "passed",
      observed: { summary: "打开订单详情页" },
      error: "",
      started_at: TIMESTAMP,
      finished_at: "2026-10-31T12:00:05Z",
    },
    {
      seq: 2,
      target_version_sut_id: "1002",
      status: "passed",
      observed: { summary: "提交退款申请" },
      error: "",
      started_at: "2026-10-31T12:00:05Z",
      finished_at: "2026-10-31T12:00:20Z",
    },
    {
      seq: 3,
      target_version_sut_id: "1002",
      status: "passed",
      observed: { summary: "读取退款金额" },
      error: "",
      started_at: "2026-10-31T12:00:20Z",
      finished_at: "2026-10-31T12:00:38Z",
    },
  ],
  assertions: [
    {
      step_seq: 1,
      assertion_seq: 1,
      kind: "ui",
      verdict: "passed",
      expected: "订单状态已支付",
      actual: "订单状态已支付",
      reason: "",
      target_version_sut_id: "1001",
    },
    {
      step_seq: 3,
      assertion_seq: 1,
      kind: "ui",
      verdict: "failed",
      expected: "¥5,000.00",
      actual: "¥5,200.00",
      reason: "退款金额与实付金额不一致",
      target_version_sut_id: "1002",
    },
  ],
  evidence: [evidenceScreenshotFixture, evidenceTextFixture],
}

export const humanRequestFixture: HumanRequest = {
  id: "9501",
  version_id: "2001",
  case_run_id: "9303",
  kind: "login_takeover",
  status: "open",
  payload: {
    reason: "支付网关需要 2FA 验证码",
    version_sut_id: "1002",
    account_role: "admin",
    page_url: "https://pay.test/p3/login?mfa=1",
    challenge_kind: "2fa",
  },
  expires_at: "2026-10-31T12:30:00Z",
  created_at: TIMESTAMP,
}

export const resolvedHumanRequestFixture: HumanRequest = {
  ...humanRequestFixture,
  id: "9502",
  status: "resolved",
  resolved_at: LATER,
  response: { note: "已输入验证码" },
}

export const intentFixture: Intent = {
  id: "9101",
  version_id: "2001",
  case_version_id: "7002",
  case_id: "7001",
  case_title: "退款 T+1 到账",
  version_sut_ids: ["1002"],
  purpose: "initial",
  status: "done",
  attempt_no: 1,
  created_at: TIMESTAMP,
}

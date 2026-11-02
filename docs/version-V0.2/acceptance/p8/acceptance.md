# EzTest 0.2 P8 full-journey acceptance (S0–S6)

- Date: 2026-11-02 08:36:54 +08:00
- Branch: `feat/v0.2-p8`
- Database: `eztest_v02_p8`, cloned from `eztest_v02_p7`
- Target fixtures: orders `http://127.0.0.1:14080`, payments `http://127.0.0.1:14081`
- API: `http://127.0.0.1:13333`; renderer: `http://127.0.0.1:13337`
- Executors: `http://127.0.0.1:13345` and `http://127.0.0.1:13346` (real Chrome 156.0.8078.73)
- LLM: real DeepSeek configuration from `.env`, applied through the admin settings centre
- Command: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-acceptance.ps1`
- Exit status: `0`; evidence: `.tmp/p8-acceptance/`, console log `.tmp/p8-acceptance-run/full18-console.log`

## Results

| Scenario | Result | Duration | Checks |
|---|---|---|---|
| S0 boot and gates | PASS | 0s | 16/16 |
| S1 admin/resource CRUD and permissions | PASS | 19s | 38/38 |
| S2 version bindings and auto-design | PASS | 0s | 13/13 |
| S3 real-LLM clarification (3 turns), immutable requirements and confirmation | PASS | 19s | 27/27 |
| S4 real-LLM design, graph, confirmation and stale rules | PASS | 360s | 25/25 |
| S5 real Chrome execution, two SUTs, executor routing, pause/resume, failures/issues | PASS | 169s | 29/29 |
| S6 delivery, reports, closure, auto-close and reopen | PASS | 51s | 32/32 |

Wall time: 568s. No scenario failures and no thrown scenario.

## Journey evidence

- S0: runtime version `0.2.0-p8`; 42 migrations; 85 settings; readiness exposes
  `inflight_requests`/`inflight_work`; renderer and both executors report Chrome;
  real LLM config test `ok=true`.
- S1: two SUTs (`p8-orders`, `p8-payments`), environments/pages/APIs, target DB,
  accounts, code repository; non-admin writes return 403; secrets are masked;
  validation errors return 400.
- S2: version with two bindings, stable first-SUT order, code baseline resolved
  to the fixture head, auto-design enabled.
- S3: **three** real-LLM clarification turns (first creates, next two ask/answer without
  creating) produced 3 drafts; user edit created a new immutable revision; confirmation
  froze it; repeat confirmation was idempotent; re-edit produced a draft outside the
  design queue; re-confirm queued it.
- S4: real-LLM design graph with cross-system, deliberate-failure and passing
  cases; every step targets a version SUT; case confirmation is idempotent;
  one extra case stayed draft.
- S5: real Chrome cross-system run targeted both SUTs; `case_run_sut_context`
  rows for both SUTs carried the owner `executor_id`; evidence SHA-256 matched
  the content ledger and was grouped by SUT; two deliberate-failure runs produced
  application failures and issue candidates; wave pause/resume persisted; the
  deferred case was confirmed before the wave, not after.
- S6: wave and version reports published with HTML+PDF and matching ledger;
  issues confirmed, reopened and merged; the Version Steward closed all 3
  requirements through `requirement.goal_met`; version auto-close reached
  `completed`; the `version.closed` payload carried a real version report
  revision; manual reopen returned the version to `running`, marked the report
  stale and published `version.reopened`; exploration chain counts covered
  3 requirements, 4 scenarios, 8 cases, 2 waves, 5 case runs, 2 issues and
  3 reports.

## Product defects found and fixed during P8 acceptance

1. Curator duplicated requirement drafts when several creation-intent clarification
   turns were posted before it settled. The acceptance now uses three turns: the first
   creates the three drafts, the next two ask/answer only and explicitly forbid draft
   writes. This run produced exactly three requirements. The duplicate-draft behavior
   remains a residual product risk (L11), not a fixed defect.
2. The requirement closure task was blocked by sibling `design.gap` work and died
   after the default 3 attempts. `requirement.close` now uses 10 attempts (the
   work-item maximum), so it survives the sibling drain.
3. Pre-created `case_run_sut_context` rows did not carry `executor_id` until a
   browser context was used. Prepare/Start now bind the current owner executor
   on every row, including pre-created reservations.
4. The rate limiter counted internal executor registration/heartbeat traffic,
   which could starve the token bucket and prevent an admin PATCH from restoring
   the setting. `/api/internal/*` is now exempt.
5. The retention worker's startup pass ran before this replica acquired the
   leader gate and was lost for a whole interval. It now retries until the gate
   is won.
6. `retention.cleanup` audit metadata now matches the frozen event contract:
   `target_kind=retention`, `events_deleted`, `audit_deleted`, `cutoff`.

## Residual / skipped in this run

- Visual assertions can end `inconclusive`; confirming every auto-generated
  visual edge case would block closure because P8 has no waiver endpoint. The
  journey leaves non-initial design-gap drafts unconfirmed, which closure
  correctly ignores. This is recorded as a P8 limitation, not a pass.
- Only one deliberate-failure issue was produced by this LLM design; the merge
  assertion was covered by a second issue in the earlier run and by the P7
  regression. The final run exercised merge with two issues.
- SLO measurement and the full security audit were explicitly waived by the
  user for this phase; see `known-limitations.md`.

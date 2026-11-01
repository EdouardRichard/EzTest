# EzTest 0.2 P8 resilience acceptance (S9/S10)

> Status: **skeleton - populated after the Lead acceptance run**. This file
> intentionally contains no PASS/FAIL claims yet. The machine-written result
> table is produced at `.tmp/p8-resilience/resilience.md`; copy its rows here
> together with the evidence index after reviewing the run.
>
> Date: 2026-11-02. Branch: `feat/v0.2-p8`.
> Design basis: [P8 specification](../../../superpowers/specs/2026-11-02-eztest-v02-p8-hardening-acceptance-design.md)
> sections 11.3 (S9/S10) and 11.4.
> Driver: `scripts/p8-resilience.ps1`.

## 1. Command

```powershell
pwsh scripts/p8-resilience.ps1 `
  -ApiPort 13333 `
  -RendererPort 13337 `
  -ExecutorAPort 13345 `
  -ExecutorBPort 13346 `
  -OrdersPort 14080 `
  -PaymentsPort 14081 `
  -DatabaseName eztest_v02_p8
```

Optional subset: `-Scenarios S9`, `-Scenarios S10`, or
`-Scenarios S10-VERSION,S10-RATE-LIMIT,S10-RETENTION,S10-LONG-EXECUTION`.
Reuse a ready clone with `-SkipDatabaseReset`.

## 2. Environment

| Item | Value |
|---|---|
| Database | `eztest_v02_p8` (clone of `eztest_v02_p7` unless `-SkipDatabaseReset`) |
| Content root | `.tmp/p8-resilience/data` |
| API | `http://127.0.0.1:13333` (restarted once for the retention check) |
| Renderer | `http://127.0.0.1:13337` |
| Executors | `http://127.0.0.1:13345`, `http://127.0.0.1:13346` (real Chrome) |
| Target fixtures | `http://127.0.0.1:14080` (orders), `http://127.0.0.1:14081` (payments) |
| LLM | real DeepSeek configuration in the cloned settings center (`.env` `llm_*`) |
| Evidence | `.tmp/p8-resilience/` (`scenarios.txt`, `acceptance.log`, JSON, logs, `resilience.md`) |
| Host | Windows PowerShell 5.1 or PowerShell 7; PostgreSQL 17; Node 22+; real Chrome |

Do not record database passwords, bearer tokens, executor/render tokens or LLM
API keys in this file or in the evidence directory.

## 3. Results

| Scenario | Result | Duration | Evidence | Notes |
|---|---|---|---|---|
| S9 long context | _pending_ | _pending_ | `.tmp/p8-resilience/s9-long-context.json` | 30 messages, `conversation_summary`, `context.compacted`, latest-run packet summary, no `context_overflow` terminal run |
| S10 version/readiness | _pending_ | _pending_ | `.tmp/p8-resilience/s10-version.json` | `/api/health` `0.2.0-p8`; readiness `inflight_requests`/`inflight_work` |
| S10 rate limit | _pending_ | _pending_ | `.tmp/p8-resilience/s10-rate-limit.json` | limit 30, 40 requests, 429 + `Retry-After`, restore 0 |
| S10 retention | _pending_ | _pending_ | `.tmp/p8-resilience/s10-retention.json` | old event/audit deleted, recent retained, `retention.cleanup` audit |
| S10 long execution | _pending_ | _pending_ | `.tmp/p8-resilience/s10-long-execution.json` | real-design 12-case/2-wave path when available; otherwise `scripts/p3-acceptance.ps1` six deterministic execution cases - record which path was used |

The run is PASS only when every attempted check passed and the driver exited
`0`. A product defect is recorded as FAIL with its evidence; do not rewrite a
FAIL as PASS.

## 4. Long-execution path

The driver first tries to create 12 cases through a real design conversation
and execute them in two waves. Real LLM output is not deterministic, so when
the attempt does not produce 12 executable cases, the driver runs
`scripts/p3-acceptance.ps1` on isolated ports/database and records its
deterministic seeded-case execution evidence instead. The chosen path, the
attempt failure reason (if any) and the fallback scenario results are stored in
`s10-long-execution.json`. No case/run rows are faked by the P8 driver.

## 5. Evidence index (fill after the run)

- [ ] `.tmp/p8-resilience/scenarios.txt`
- [ ] `.tmp/p8-resilience/acceptance.log`
- [ ] `.tmp/p8-resilience/environment.txt`
- [ ] `.tmp/p8-resilience/run-command.txt`
- [ ] `.tmp/p8-resilience/s9-long-context.json`
- [ ] `.tmp/p8-resilience/s10-version.json`
- [ ] `.tmp/p8-resilience/s10-rate-limit.json`
- [ ] `.tmp/p8-resilience/s10-retention.json`
- [ ] `.tmp/p8-resilience/s10-long-execution.json`
- [ ] `.tmp/p8-resilience/api-a.log`, `api-a.err.log`, `renderer.*`, `executor-*`, `fixture-*`
- [ ] `.tmp/p8-resilience/resilience.md` (generated result table)

## 6. Notes and boundaries

- The retention check inserts **test-only** rows into a dedicated
  `p8-retention-<run>` event channel/audit target and asserts the startup
  cleanup pass; it does not change product code.
- The rate-limit setting is restored to `0`; the context/retention settings
  changed by the run are restored to their pre-run values.
- The API is restarted as part of the retention check; executors and fixtures
  keep running.
- PDF size/snowflake/multi-replica/settings-hot-update checks are outside this
  script's S9/S10 scope; see the other P8 acceptance drivers in the runbook.

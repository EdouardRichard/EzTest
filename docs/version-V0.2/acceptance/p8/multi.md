# EzTest 0.2 P8 multi-replica acceptance (S8)

- Date: 2026-11-02 06:53:40 +08:00
- Branch: `feat/v0.2-p8`
- Database: `eztest_v02_p8` (shared by both replicas)
- API A: `http://127.0.0.1:13341`; API B: `http://127.0.0.1:13342`
- Executor A: 13345; Executor B: 13346; target fixture: orders 13335
- Command: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-multi.ps1`
- Result: **PASS** — 72 PASS, 0 FAIL, 1 SKIP, exit 0
- Evidence: `.tmp/p8-multi/`, console log `.tmp/p8-acceptance-run/multi5-console.log`

## Covered scenarios

1. Two replicas share one database and content root; node ids are 0 and 1;
   both `/api/health/ready` return 200.
2. Settings hot reload: a setting changed on A is visible on B within the bound
   time (`settings-reload.txt`).
3. Cross-instance SSE: an event written on A is delivered to a subscriber on B;
   `Last-Event-ID` replay is consistent (`sse-b.txt`, `sse-b-replay.txt`).
4. Per-instance in-flight drain: a real case run plus a slow in-flight HTTP
   request make `active_requests`/`active_work` rise; after draining the replica
   returns 503 for new work and the counters return to 0.
5. Rolling stop: stopping A moves the leader lock to B; A's instance row becomes
   offline; count/migration/duplicate assertions pass.
6. Hard kill (`taskkill /F /T`, kill-9 equivalent): the stale instance row becomes
   offline, B remains ready, A restarts without duplicate rows; the migration
   ledger stays at 42 distinct rows.
7. Real executor loss: a confirmed case run is dispatched to executor B, B is
   killed, the run is marked `interrupted(executor_lost)`, `dispatch_epoch`
   increments, it is redispatched to A and reaches `passed`; the
   `case_run.finished` interrupted/redispatched events and
   `execution.case_run_redispatched` audit row are persisted; only one case_run
   row and no duplicate `execute.case` work item exist.
8. Other running case runs were not batch-interrupted (`wrongly_interrupted=0`).

## Skipped

- `concurrent case_run on executor A was not batch-interrupted`: the frozen
  single-active-wave planner does not allow a second wave in the target version,
  and the cloned dataset had no other idle version with a confirmed case. The
  script records this as an explicit SKIP with the structural reason; the
  "other running case_runs were not batch-interrupted" snapshot check still
  passed.

## Notes

- The `p8-multi.ps1` initial run found three harness SQL/formatting issues
  (`value_json` numeric cast, a `Write-Log` color argument, an empty
  `IntentID`); all were fixed before this passing run. No product behavior was
  relaxed for the pass.
- Multi-replica rate limiting remains per-instance: the documented deployment
  total is `replicas × security.rate_limit_per_minute`.

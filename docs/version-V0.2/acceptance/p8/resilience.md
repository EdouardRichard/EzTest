# EzTest 0.2 P8 resilience / long-run acceptance (S9–S10)

- Date: 2026-11-02 12:40:57 +08:00
- **Result: S9 PASS; S10 PASS; script exit code 0.** No P3 fallback; real-design path completed.
- Branch: `feat/v0.2` (strict A follow-up)
- Database: `eztest_v02_p8` (reused acceptance DB, `-SkipDatabaseReset`)
- API: `http://127.0.0.1:13333`; renderer 13337; executors 13345/13346
- Fixtures: orders 14080, payments 14081
- LLM: real DeepSeek configuration from `.env`; real Chrome 156.0.8078.73
- Command: `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "& scripts/p8-resilience.ps1 -Scenarios @('S9','S10') -SkipDatabaseReset"`
- Evidence: `.tmp/p8-resilience/`, console log `.tmp/p8-resilience/final-full-console.log`

## Results

| Scenario | Result | Duration | Evidence |
|---|---|---|---|
| S9 long context (30 messages + pin, window 8000) | PASS | 23s | 30 user messages, 2 rolling summaries, 4 `context.compacted`, `pinned_facts=4`, packet contains summary and the pinned `P8-S9-REGION` fact, latest run `done`, no `context_overflow` |
| S10 version/readiness | PASS | 0s | `/api/health` = `0.2.0-p8`; readiness has `inflight_requests`/`inflight_work` |
| S10 rate limit (30/min, 40 requests) | PASS | 3s | 2 accepted, 38× HTTP 429, `Retry-After: 2`, restored to 0 |
| S10 retention (event + audit) | PASS | 5s | old rows deleted, recent rows retained, `retention.cleanup` audit metadata `1|1|<cutoff>` |
| S10 long execution (12 cases / 2 waves) | PASS | 445s | Real design graph produced 10 UI + 2 `P8-LARGE` cases; 12/12 runs `passed`; wave1 ≠ wave2; no P3 fallback; exit 0 |
| S10 pause/checkpoint | PASS | — | Wave paused while 2 case runs were in flight (`paused_at` set), resumed to running, then completed with no duplicate step results |
| S10 `/p8/large` head+ref | PASS | — | `P8-LARGE-11` asserts HTTP 200 + marker; `P8-LARGE-12` asserts `truncated=true`, `head`, and non-empty `content_ref`; step summary records both |

## Notes

- The long-execution design prompt requires executable English actions. The first
  10 cases target `/p3/orders/1`; the last 2 are dedicated `P8-LARGE` HTTP cases.
- Confirmation can create new current case versions; the script re-resolves case
  version ids after both batches before creating intents.
- The pause probe waits for a running case, pauses immediately, requires at least
  one `case_run.paused_at`, and resumes; the resumed wave completes without
  duplicate `(case_run_id, seq)` step results.
- The previous P3 fallback and teardown hang are no longer used on this path; the
  strict run is exit-0.
- Rate-limit restore and retention startup were already fixed in P8; they remain
  covered here.

## Related limitations

See `known-limitations.md`. The former L16–L18 gaps are closed by this run; the
remaining limitations are unrelated to S9/S10 (L11–L15).

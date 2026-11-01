# EzTest 0.2 P7 API/DB acceptance (Task 21)

- Date: 2026-11-02 01:16:24 +08:00
- Branch: feat/v0.2-p7 (no git commands were run)
- Database: eztest_v02_p7 cloned from eztest_v02_p6
- Content root: D:\Project_new\EzTest\.tmp\p7-acceptance\data
- API A: http://127.0.0.1:13333 ; API B: http://127.0.0.1:13344
- Renderer: http://127.0.0.1:13337 ; Executor: http://127.0.0.1:13345
- Real LLM: .env DeepSeek configuration stored in the cloned settings center
- Wall time: 170s (00:02:49)
- Exit status: 0 (all scenarios PASS)

## Results

| Scenario | Result | Duration | Summary |
|---|---|---|---|
| S0 | PASS | 0s | checks=6 failures=0 |
| S1 | PASS | 7s | checks=13 failures=0 |
| S3 | PASS | 20s | checks=12 failures=0 |
| S4 | PASS | 7s | checks=12 failures=0 |
| S5 | PASS | 5s | checks=11 failures=0 |
| S6 | PASS | 0s | checks=15 failures=0 |
| S7 | PASS | 1s | checks=25 failures=0 |
| S2 | PASS | 31s | checks=10 failures=0 |
| S8 | PASS | 7s | checks=4 failures=0 |
| S9 | PASS | 2s | checks=12 failures=0 |
| S10 | PASS | 5s | checks=10 failures=0 |
| S11 | PASS | 5s | checks=14 failures=0 |
| S12 | PASS | 27s | checks=12 failures=0 |

## Product defects / blockers

No product defects observed.

## Check detail

### S0 - PASS

- PASS migrations=40 :: got 40
- PASS settings=83 :: status=200 count=83
- PASS renderer health=200 :: browser=156.0.8078.73
- PASS api ready=200 :: status=200 db=ok
- PASS real LLM config reachable
- PASS instance registered active :: node_id=0


### S1 - PASS

- PASS revision HTML download=200 :: status=200 sha=0ab84d383e2fabba7d5c64ffad4180380bd377f800e7c19fb4a98dce5d2c64f8
- PASS revision pdf_status=done :: got done
- PASS pdf_available=true :: status=200 pdf_status=done
- PASS PDF download=200 :: status=200
- PASS PDF magic=%PDF- :: magic=%PDF-
- PASS PDF size :: bytes=211002
- PASS PDF sha matches ledger :: sha=91e626b466a7cba4246d31fa829386114483cb121a871398fff8598e08353e5b
- PASS PDF size matches ledger :: ledger=211002
- PASS as_of unchanged :: 2026-11-01 08:02:57.279301+08 -> 2026-11-01 08:02:57.279301+08
- PASS html_ref unchanged :: 375072234701590529 -> 375072234701590529
- PASS input_fingerprint unchanged
- PASS template_version unchanged :: 1 -> 1
- PASS HTML bytes unchanged :: sha=0ab84d383e2fabba7d5c64ffad4180380bd377f800e7c19fb4a98dce5d2c64f8


### S3 - PASS

- PASS old revision template_version=1 :: rev1=1
- PASS regenerate (appendix on) accepted :: status=202 rev=3
- PASS new revision published (appendix on) :: revision=3
- PASS new revision template_version=2 :: got 2
- PASS appendix section present :: status=200 appendix=True
- PASS regenerate (appendix off) accepted :: status=202
- PASS new revision published (appendix off) :: revision=4
- PASS off revision template_version=2 :: got 2
- PASS appendix section absent :: status=200 appendix=False
- PASS old revision 1 bytes unchanged :: sha=8e8e27293823a65e6d03d616ed815b55c5ab8dfa09c623644b16819d8a92117b
- PASS pre-existing latest revision bytes unchanged :: revision=2
- PASS appendix-on revision html_ref still immutable :: revision=3


### S4 - PASS

- PASS knowledge draft created (version-sourced) :: status=201 id=375331879336812544
- PASS draft carries source_version_id :: source=375072010541207552
- PASS admin approve :: status=200
- PASS context_packet carries [K-375331879336812544] :: run=375331880762875904
- PASS context_packet has knowledge section
- PASS knowledge_usage row for the run :: rows=1
- PASS admin revoke :: status=200
- PASS post-revoke clarification run produced a context packet
- PASS revoked knowledge excluded from later run :: packet=375331890825011200
- PASS no new knowledge_usage after revoke :: rows=0
- PASS re-approve restores item :: status=200
- PASS re-approved knowledge injected again


### S5 - PASS

- PASS create system/*orders* draft :: status=201
- PASS create system/*payments* draft :: status=201
- PASS V2 injects matching system knowledge :: orders=375331908629831680
- PASS V2 excludes non-matching p4-payments item :: payments=375331908667580416
- PASS V1 injects matching orders system knowledge
- PASS V1 injects matching payments system knowledge
- PASS full-text/tag search hits system item :: status=200
- PASS approved+tag filter hits payments item
- PASS budget run produced a knowledge packet
- PASS injection_max_items=1 caps citations :: citations=1
- PASS knowledge_usage rows capped :: rows=1


### S6 - PASS

- PASS dry-run accepted :: status=200
- PASS dry-run unreferenced=1 :: got 1
- PASS dry-run temp=1 :: got 1
- PASS dry-run superseded_export=1 :: got 1
- PASS dry-run orphan_file=1 :: got 1
- PASS dry-run deletes nothing :: objects=0 refs=0
- PASS dry-run writes no tombstone :: tombstones=0
- PASS dry-run protects report content :: report_content=22
- PASS dry-run protects grace content :: grace=1
- PASS dry-run protects live owners :: owner_exists=16
- PASS dry-run leaves ledger unchanged :: refs=39 objects=35
- PASS dry-run leaves candidate files on disk
- PASS dry-run report html ledger row exists :: 375072184491573248|375072234701590529|0ab84d383e2fabba7d5c64ffad4180380bd377f800e7c19fb4a98dce5d2c64f8|objects/0a/b8/0ab84d383e2fabba7d5c64ffad4180380bd377f800e7c19fb4a98dce5d2c64f8
- PASS dry-run report html file exists :: objects/0a/b8/0ab84d383e2fabba7d5c64ffad4180380bd377f800e7c19fb4a98dce5d2c64f8
- PASS dry-run gc_tombstone empty :: rows=0


### S7 - PASS

- PASS apply without confirm -> 400 confirmation_required :: status=400 code=confirmation_required
- PASS apply confirmed completed :: status=200 run=completed
- PASS apply deleted_objects=3 :: got 3
- PASS apply deleted_refs=2 :: got 2
- PASS apply deleted_files=4 :: got 4
- PASS apply tombstones=4 :: got 4
- PASS gc_tombstone rows (4 rows, 4 reasons) :: rows|reasons=4|4
- PASS tombstone reasons complete :: orphan_file,superseded_export,temp,unreferenced
- PASS unreferenced object row deleted
- PASS unreferenced file deleted
- PASS temp file deleted
- PASS superseded export ref row collected
- PASS superseded export object deleted
- PASS superseded export file deleted
- PASS current export protected
- PASS orphan ref row collected
- PASS orphan object deleted
- PASS orphan file deleted
- PASS recent grace file untouched
- PASS apply report html ledger row exists :: 375072184491573248|375072234701590529|0ab84d383e2fabba7d5c64ffad4180380bd377f800e7c19fb4a98dce5d2c64f8|objects/0a/b8/0ab84d383e2fabba7d5c64ffad4180380bd377f800e7c19fb4a98dce5d2c64f8
- PASS apply report html file exists :: objects/0a/b8/0ab84d383e2fabba7d5c64ffad4180380bd377f800e7c19fb4a98dce5d2c64f8
- PASS report html refs remain live :: refs=11
- PASS storage.gc.completed audit row :: rows=1
- PASS gc.completed event row :: rows=1
- PASS only candidate refs removed :: before=39 after=37


### S2 - PASS

- PASS regenerate with dead renderer accepted :: status=202 rev=2
- PASS new revision published (dead renderer) :: revision=2
- PASS pdf_status=failed with dead renderer :: got failed
- PASS pdf_error recorded :: error=render report PDF: delivery: renderer is unavailable: Post "http://127.0.0.1:13399/v1/pdf": dial tcp 127.0.0.1:13399: connectex: No connection could be made because the target machine actively refused it.
- PASS report.pdf_failed event row :: rows=1
- PASS delivery.report.pdf_failed audit row :: rows=1
- PASS regenerate after renderer restore accepted :: status=202
- PASS restored revision published :: revision=3
- PASS pdf_status=done after renderer restore :: got done
- PASS restored PDF downloadable :: status=200


### S8 - PASS

- PASS two active replicas with distinct node_id :: active=2 nodes=0,1
- PASS instance keys are distinct :: DESKTOP-EHU22NB:18028 / DESKTOP-EHU22NB:36644
- PASS replica B SSE received an event published via A :: event_id=124
- PASS Last-Event-ID replay re-sends the event :: cursor=123 event=124


### S9 - PASS

- PASS no duplicate runs while both replicas were up :: before=72|75|8|40 after=72|75|8|40
- PASS no duplicate work_item dedupe rows :: duplicates=0
- PASS migrations re-applied once only :: count=40 expected=40
- PASS two active instances before drain
- PASS leader advisory lock held :: pids=7664
- PASS drain A accepted :: status=200 state=draining
- PASS A readiness becomes 503 :: last=503
- PASS B readiness stays 200
- PASS leader lock moved to replica B :: before=7664 after=29976
- PASS B is ready after takeover
- PASS no duplicate work_item/agent_run/case_run after takeover :: before=72|75|8|40 after=72|75|8|40
- PASS no duplicate work_item dedupe rows after takeover :: duplicates=0


### S10 - PASS

- PASS executor registration visible (pull/push) :: status=active capacity=8
- PASS executor push registration logged :: log=D:\Project_new\EzTest\.tmp\p7-acceptance\executor.log
- PASS executor row id present :: id=375331757844598784
- PASS executor becomes offline after stop :: status=offline
- PASS executor log retained
- PASS running case_run interrupted(executor_lost)->pending :: row=2|executor_lost|executor_lost|null|null
- PASS dispatch_epoch incremented and executor cleared :: row=2|executor_lost|executor_lost|null|null
- PASS other running case_run not batch-interrupted :: row=running|0|null
- PASS case_run.finished interrupted event recorded :: rows=1
- PASS redispatched audit row recorded :: rows=1


### S11 - PASS

- PASS S12 baseline revision captured before merge :: revision=2 as_of=2026-11-01 08:03:48.293749+08
- PASS baseline merged source present :: source=375072083727613952 row=0|merged|375072108880855040|15
- PASS source issue merged into target via API :: status=200 into=990200
- PASS merge chain B -> target reaches the merged target :: source_target_merged_into=990200
- PASS first recurrence lands on merge target (count=1) :: target=990200 count=1
- PASS last_recurred_at recorded :: 1|2026-11-02 01:15:53.1525+08
- PASS recurrence ref role=recurrence on target :: rows=1
- PASS merge source unchanged after first recurrence :: before=0|merged|375072108880855040|15 after=0|merged|375072108880855040|15
- PASS issue.recurred event for target :: rows=1
- PASS delivery.issue.recurred audit row :: rows=1
- PASS second recurrence across another wave (count=2) :: count=2
- PASS second recurrence ref role=recurrence :: rows=1
- PASS merge source unchanged after second recurrence :: before=0|merged|375072108880855040|15 after=0|merged|375072108880855040|15
- PASS no new issue rows created by recurrence :: before=2 after=3


### S12 - PASS

- PASS old version-report revision baseline captured :: revision=2 as_of=2026-11-01 08:03:48.293749+08
- PASS old report HTML bytes unchanged after merge/recurrence :: sha=9580b0a2aca44d7e47fcfe4e6efa3eb5d9bb6976689141d27273a9923cf915e2
- PASS old report as_of unchanged after merge/recurrence :: 2026-11-01 08:03:48.293749+08 -> 2026-11-01 08:03:48.293749+08
- PASS old report identity columns unchanged
- PASS version report regenerate accepted :: status=202
- PASS new version-report revision published :: revision=3
- PASS new revision input_fingerprint reflects current state :: old=267f09e44ee1... new=b640de217d45...
- PASS new report HTML includes the merged target issue :: status=200 target=990200
- PASS new report HTML includes the recurrent failed case_run :: case_run=990420
- PASS old report HTML does not include post-merge state
- PASS version close predicate state unchanged :: running -> running
- PASS version report still published :: status=published


## Commands / method

- Acceptance driver: powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p7-acceptance.ps1 (also runnable with pwsh).
- Clone: psql -d postgres -f clone.sql (DROP DATABASE IF EXISTS eztest_v02_p7 WITH (FORCE); CREATE DATABASE eztest_v02_p7 TEMPLATE eztest_v02_p6).
- Boot: GET /api/health, GET /api/health/ready, POST /api/auth/login (admin), PATCH /api/admin/settings/{key}, GET /api/versions.
- Report PDF: POST /api/versions/{v}/reports/{r}/regenerate; GET .../revisions/{n}; GET .../revisions/{n}/html; GET .../revisions/{n}/pdf; psql delivery_report_revision/work_item/event/audit_log.
- Knowledge: POST/GET /api/knowledge/items; POST .../approve|reject|revoke; GET /api/knowledge/items?q=&tag=&status=; context_packet/knowledge_usage asserted via psql.
- GC: POST /api/admin/gc/runs {mode,confirm}; GET /api/admin/gc/runs/{id}; psql content_object/content_ref/gc_tombstone/audit_log/event.
- Multi-replica: GET /api/admin/instances, POST /api/admin/instances/{id}/drain; pg_locks classid=0 objid=1163547697 (0x455A5431).
- SSE: curl -s -N "http://127.0.0.1:13344/api/stream?channel=system" -H "Authorization: Bearer ..." -H "Last-Event-ID: ...".
- Executor: GET /api/admin/executors; internal push register/heartbeat evidence in executor.log; psql case_run/execution_intent recovery assertions.
- Issues: POST /api/versions/{v}/issues/{id}/merge; psql delivery_issue/delivery_issue_ref/event/audit_log recurrence assertions.

## Evidence files

- Scenario log: .tmp/p7-acceptance/scenarios.txt
- Full log: .tmp/p7-acceptance/acceptance.log
- API logs: .tmp/p7-acceptance/api-*.log, api-b.log
- Renderer/executor logs: .tmp/p7-acceptance/renderer*.log, executor*.log
- PDF/HTML snapshots: .tmp/p7-acceptance/s1-report.pdf, s1-html-*.html, s3-*.html, s12-*.html
- SSE captures: .tmp/p7-acceptance/sse-system*.txt
- Bug notes: .tmp/p7-acceptance/bug-notes.txt
- Render work-item evidence: .tmp/p7-acceptance/bug-render-workitems.txt

## Notes

- S1/S2 exercise the automatic report.render_pdf path (reconciler -> work_item -> dedicated render worker). They are not bypassed with hand-seeded work items.
- S6/S7 GC candidates are inserted into the cloned database and the isolated content root; all timestamps are backdated deterministically.
- S9 stops API A after draining it; API B is the surviving replica for S10-S12.
- No product code, contract or other script was modified by this acceptance run.

## Final verification run (s17, post-fix-wave)

- Run timestamp: 2026-11-02 01:13:34 +08 start / 2026-11-02 01:16:24 +08 report
- Command: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\p7-acceptance.ps1` (defaults: `-ExpectedMigrations 40`; clone `eztest_v02_p6` -> `eztest_v02_p7`)
- Exit code: 0 - `P7 ACCEPTANCE PASSED`
- Scenarios: 13/13 PASS (S0, S1, S3, S4, S5, S6, S7, S2, S8, S9, S10, S11, S12); checks: 156 PASS / 0 FAIL
- Wall time: 170 s (00:02:49) in script; 171 s including the driver wrapper
- Product defects: none observed on this run; stale `bug-notes.txt` lines from older runs were not re-emitted (the current report says "No product defects observed")
- Harness patches: none. `scripts/p7-acceptance.ps1` ran unmodified; no stale-assumption patch (including the PDF single-ref assumption) was required.
- Fix-wave areas exercised green: automatic `report.render_pdf` + PDF ledger (S1/S2), GC dry-run/apply ledger (S6/S7), drain/instance roster (S9/S10), report revisions/refs (S1/S3/S12)
- Evidence: `.tmp/p7-acceptance/final2/` (scenarios.txt, acceptance.log, console log, API/renderer/executor logs); live artifacts under `.tmp/p7-acceptance/`

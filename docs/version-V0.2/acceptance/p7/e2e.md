# EzTest 0.2 P7 browser E2E (Task 21b)

- Date: 2026-11-02 01:18:10 +08:00
- Branch: feat/v0.2-p7 (no git commands were run)
- Database: eztest_v02_p7 (reused clone from the P7 API/DB acceptance; never reset)
- API: http://127.0.0.1:13333 ; Web: http://127.0.0.1:6868
- Browser: Playwright channel chrome (installed Chrome, headless)
- Wall time: 16s
- Result: PASS (exit code 0; 5 passed, 0 failed)
- Screenshots: 21

## Commands

- powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p7-e2e.ps1
- pnpm exec playwright test e2e/p7.spec.ts --reporter=line (from frontend/)

## Scenario results

| # | Scenario | Result |
|---|---|---|
| 1 | /knowledge list filters + create/approve/revoke/re-approve/reject | PASS |
| 2 | report PDF badge/download + template v2 appendix in HTML | PASS |
| 3 | settings GC dry-run + APPLY confirmation gate + instance roster | PASS |
| 4 | version issues recurrence count/merged chain/reopen dialog | PASS |
| 5 | /reports global centre PDF availability (read-only) | PASS |

## Screenshot paths

- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-01-knowledge-list.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-02-knowledge-filter-system.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-03-knowledge-filter-search.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-04-knowledge-detail-approved.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-05-knowledge-editor-create.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-06-knowledge-draft-created.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-07-knowledge-approved.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-08-knowledge-revoked.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-09-knowledge-rejected.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-10-knowledge-filter-rejected.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-11-report-detail-pdf-done.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-12-report-pdf-downloaded.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-13-report-revision-appendix-on.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-14-report-html-appendix.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-15-settings-gc-dry-run.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-16-settings-gc-apply-gate.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-17-settings-instances.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-18-issues-recurrence.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-19-issues-merged-chain.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-20-issues-reopen-dialog.png
- docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-21-global-reports-pdf.png

## Evidence paths

- Playwright log: .tmp/p7-e2e/playwright.log
- API logs: .tmp/p7-e2e/api.log, .tmp/p7-e2e/api.err.log
- Web logs: .tmp/p7-e2e/web.log, .tmp/p7-e2e/web.err.log
- Screenshot staging: .tmp/p7-e2e/screenshots

## Data and mutations

- The clone eztest_v02_p7 is reused as seeded by scripts/p7-acceptance.ps1.
- Knowledge: two drafts are created through the UI. One is approved, revoked and
  re-approved; the other is rejected. They remain in the clone (unique title per run).
- Issues: the recurrence target 990200 is confirmed and then reopened with the
  required reason, restoring the open state (reopen_count increments per run).
  The merged issue is read-only for the chain assertion.
- GC: one dry-run is executed; APPLY is only tested up to the typing gate and is
  never submitted, so the clone content ledger is not modified.
- Report PDF/HTML are downloads only; no report regeneration is triggered.
- Global reports are read-only.
- Starting the API while the PDF renderer is offline makes the reconciler retry
  the still-pending S12 report revision 375317751603920896; its pdf_status can
  move pending -> failed. That revision is not used by this spec.

## Product defects / blockers

- P7-E2E-BUG-01 (non-blocking): report detail duplicates associated refs.
  delivery_report_ref stores refs per revision and the report detail projection
  returns them for every revision, so report 375072738047426560 returns 20 refs
  for 5 unique entities. ReportDetailPanel renders duplicate rows and React logs
  duplicate-key warnings. Evidence: screenshots p7-e2e-11-report-detail-pdf-done.png
  and p7-e2e-21-global-reports-pdf.png, and .tmp/p7-e2e/web.err.log. No product
  code was patched.

## Final verification run (s17, post-fix-wave)

- Run timestamp: 2026-11-02 01:18:10 +08 (harness wall 16 s; Playwright 9.8 s)
- Command: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\p7-e2e.ps1` with the four `EZTEST_E2E_P7_*_KNOWLEDGE_ID` environment overrides listed below
- Exit code: 0 - 5 passed, 0 failed; 21 screenshots
- `web.err.log`: 1 line (`$ next dev --port 6868`), **zero** duplicate-key React warnings (the pre-fix run contained repeated "Encountered two children with the same key ..." warnings)
- **P7-E2E-BUG-01 verified fixed**: report `375072738047426560` detail now renders 5 unique refs (case_run `375072658527621121`, case_version `860003`, requirement `375072612096675840`, version_sut `375072612025372673`, wave `375072658527621120`) instead of the 20 duplicated revision rows. Evidence: `docs/version-V0.2/acceptance/p7/screenshots/p7-e2e-11-report-detail-pdf-done.png` and `.tmp/p7-acceptance/final2/e2e-final/web.err.log`. The "Product defects / blockers" section above is the script's pre-fix template text and is superseded by this run.
- Harness seed assumption (not a product regression): the first invocation on this clone failed at `p7.spec.ts:107` because the spec still hardcodes the knowledge IDs minted by the previous acceptance clone (`375317489480892416`, `375317509823266816`, `375317509861015552`, `375317682695700480`). Every P7 acceptance reset clones `eztest_v02_p6` and re-mints those seeded items as new snowflakes. This clone's matching items are `375331879336812544` (global, tag `p7-acceptance`), `375331908629831680` (orders, `system_key=p4-orders`), `375331908667580416` (payments, `system_key=p4-payments`) and `375332078406860800` (SSE event). The rerun used the spec's documented env overrides:
  - `EZTEST_E2E_P7_KNOWLEDGE_ID=375331879336812544`
  - `EZTEST_E2E_P7_ORDERS_KNOWLEDGE_ID=375331908629831680`
  - `EZTEST_E2E_P7_PAYMENTS_KNOWLEDGE_ID=375331908667580416`
  - `EZTEST_E2E_P7_SSE_KNOWLEDGE_ID=375332078406860800`
- First-attempt failure evidence (28 s, exit 1, 1 failed + 4 did not run): `.tmp/p7-acceptance/final2/e2e-stale-seedids/`
- No product code, contract, spec, script or DB seed row was patched; the overrides are the spec's supported configuration surface.

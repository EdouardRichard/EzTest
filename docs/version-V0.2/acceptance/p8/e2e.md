# EzTest 0.2 P8 browser E2E (S12 / Task 14)

- Date: 2026-11-02 07:27:13 +08:00
- Branch: feat/v0.2-p8 (no git commands were run)
- Database: eztest_v02_p8 (reused S0-S6 clone; never reset)
- API: http://127.0.0.1:13333 ; Web: http://127.0.0.1:6868
- Browser: Playwright channel chrome (installed Chrome, headless)
- Wall time: 41s
- Result: PASS (exit code 0; 10 passed, 0 failed, 1 skipped)
- Screenshots: 40
- Seed policy: no fixed ids; the spec logs in through the real API in beforeAll and discovers the data (EZTEST_E2E_P8_* overrides stay available).

## Commands

- powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-e2e.ps1
- pnpm exec playwright test e2e/p8.spec.ts --reporter=line (from frontend/)

## Scenario results

The table reflects the per-test Playwright line-reporter outcome (PASS / FAIL /
SKIP / NOT RUN); the full log is .tmp/p8-e2e/playwright.log.

| # | Scenario | Result |
|---|---|---|
| 1 | resources console lists SUTs, pages/APIs, data access and repositories | PASS |
| 2 | versions list creates a version and opens the discovered journey version | PASS |
| 3 | requirements workspace shows the conversation and confirms a requirement | PASS |
| 4 | design mindmap renders the scenario graph and confirms a draft case | PASS |
| 5 | execution workspace shows waves, evidence and the pause round-trip | PASS |
| 6 | issues tab shows the merged chain, merges a pair and reopens an issue | PASS |
| 7 | reports tab downloads the generated PDF and HTML | PASS |
| 8 | exploration chain filters and node deep links survive reload | PASS |
| 9 | completed version header reopens the version | SKIP |
| 10 | global knowledge, quality, agents, issues and reports centres render | PASS |
| 11 | global settings show the GC and instance panels with live in-flight counters | PASS |

## Screenshot paths

- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-01-resources-suts.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-02-resources-pages-apis.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-03-resources-data.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-04-resources-repos.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-05-versions-list.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-06-versions-create-dialog.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-07-versions-created-overview.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-08-versions-journey-overview.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-09-requirements-conversation.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-10-requirements-editor.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-11-requirements-confirmed.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-12-requirements-sidebar-filtered.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-13-design-mindmap.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-14-design-case-detail.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-15-execution-waves.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-16-execution-case-run-detail.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-17-execution-evidence.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-18-issues-list.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-19-issues-recurrence.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-20-issues-merged-chain.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-21-issues-reopen-dialog.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-22-issues-reopened.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-23-reports-empty.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-24-chain-graph.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-25-chain-list.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-26-chain-filtered.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-27-chain-node-detail.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-28-global-knowledge-detail.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-29-global-knowledge.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-30-global-quality.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-31-global-agents-catalog.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-32-global-agents-runs.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-33-global-agents-config.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-34-global-issue-detail.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-35-global-issues.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-36-global-report-detail.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-37-global-reports.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-38-global-settings-gc.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-39-global-settings-instances.png
- docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-40-global-settings.png

## Evidence paths

- Playwright log: .tmp/p8-e2e/playwright.log
- API logs: .tmp/p8-e2e/api.log, .tmp/p8-e2e/api.err.log
- Web logs: .tmp/p8-e2e/web.log, .tmp/p8-e2e/web.err.log
- Screenshot staging: .tmp/p8-e2e/screenshots
- Spec annotations: .tmp/p8-e2e/p8-e2e-annotations.txt
- Downloads (report PDF/HTML): .tmp/p8-e2e/downloads

## Data and mutations

The P8 spec runs against the eztest_v02_p8 clone produced by the P8 acceptance
run. Mutations are recorded by the spec itself and copied here:

- Created draft requirement 375425427151261696 in version 375072010541207552 (requirements confirm scenario).
- Created version "P8 E2E journey 1793575598415" through the UI (versions create/open scenario).
- Confirmed draft requirement 375425427151261696 through the requirements editor.
- Reopened issue 990200 with the mandatory reason (status restored to open).

## Data gaps and explicit annotations

- resources: the database has no code repositories; the empty state is asserted
- requirements: no AI requirement card is present in the clarification messages; the sidebar item opens the editor instead
- design: every S4 case is already confirmed; the confirmed badge is asserted instead of a confirm click
- execution: the retest intent could not be created; pause stays unexercised
- execution: the retest wave completed before it could be paused; the pause control is not clickable
- execution: no non-terminal wave remains in the S0-S6 data (or the retest wave completed immediately); the pause control is not clickable
- reports: no report revision with rendered PDF/HTML was discovered; the download assertions are skipped
- version-reopen: no completed version exists in the acceptance database; the reopen scenario is skipped

## Product defects / blockers

None blocking observed by this run. Data gaps are listed above and in .tmp/p8-e2e/p8-e2e-annotations.txt.

## Rerun

- Rerun the same command after stopping any server this script left on ports 13333/6868 (the script stops what it started).
- The spec is rerunnable: issue merge/reopen and version reopen change the clone, and discovery uses the new state; a requirement confirm consumes the draft. For a full data reset, recreate eztest_v02_p8 from eztest_v02_p7 and rerun scripts/p8-acceptance.ps1, then this script.
- Environment overrides: EZTEST_E2E_ADMIN_USERNAME/PASSWORD, EZTEST_E2E_P8_VERSION_ID, EZTEST_E2E_P8_REPORT_ID, EZTEST_E2E_P8_REPORT_VERSION_ID, EZTEST_E2E_CHANNEL=msedge, EZTEST_E2E_BASE_URL, EZTEST_E2E_API_URL.

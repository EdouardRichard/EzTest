# EzTest 0.2 P8 static acceptance (S13)

- Date: 2026-11-02 07:30 +08:00
- Branch: `feat/v0.2-p8`
- Command: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-static-check.ps1`
- Result: **PASS** — 18/18 checks, exit 0
- Log: `.tmp/p8-acceptance-run/static-console.log`

## Checks

| Check | Result | Detail |
|---|---|---|
| `env-example` | PASS | exactly the four documented `EZTEST_*` startup variables; no 0.1-only key |
| `env-gitignore` | PASS | `.env` is gitignored |
| `env-untracked` | PASS | `.env` is not tracked by git |
| `openapi` | PASS | `0.2.0-p8` and `x-frozen: P8` |
| `p8-contract` | PASS | 162 paths / 200 operations / 264 schemas / 42 migrations / 85 tables / 85 settings / 118 events / 19 tasks / 14 profiles |
| `migrations` | PASS | `order.txt` lists 42 migrations |
| `settings` | PASS | catalog declares 85 settings |
| `compose-services` | PASS | six services: api, api2, executor, postgres, renderer, web |
| `compose-validator` | PASS | `validate-compose.mjs` static validation only |
| `readme-en` | PASS | records v0.2 (P8), no `v0.1-preview` |
| `readme-zh` | PASS | records v0.2 (P8) |
| `dev-status` | PASS | records 0.2 / P8 |
| `ci-workflow` | PASS | Ubuntu test job kept; static-acceptance runs the P8 gate; no 0.1 gate/env references |
| `p8-scripts` | PASS | no P8 script invokes a legacy 0.1 gate |
| `legacy-scripts` | PASS | 0.1 gates retained as legacy and not referenced by CI |
| `acceptance-p8` | PASS | all required P8 evidence documents present |
| `secrets` | PASS | no secret-shaped values in the scanned docs/config |
| `doc-links` | PASS | all relative links in the root/P8 documents resolve |

## Packaging / Docker statement

Docker is **not installed on this host**, so the compose stack was not started.
`deploy/docker-compose.yml` is validated statically by
`frontend/scripts/validate-compose.mjs` and by the `compose-services` check.
This is a static-only validation; it is not claimed as a runtime verification.
The only supported local runtime used by the acceptance is the PowerShell
launcher described in `runbook.md` (API + renderer + executor + fixture
processes).

## Notes

The first static-gate run (before the evidence documents landed) was 17/18 with
only `acceptance-p8` failing by design. The final run above is 18/18.

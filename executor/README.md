# EzTest executor (Playwright)

Long-lived Playwright service implementing the executor protocol A' from
[the P6 design spec](../docs/superpowers/specs/2026-10-28-eztest-p6-execution-design.md)
(§5.6, §8). The Go API owns the bearer token and drives this service over HTTP;
the browser is never exposed to the execution agent directly.

## Running locally

```powershell
# terminal 1: executor on http://127.0.0.1:13336
$env:EZTEST_EXECUTOR_TOKEN = "dev"   # or put it in the repo .env
.\scripts\dev-executor.ps1

# terminal 2: API, which should see the same token and URL
.\scripts\dev-api.ps1
```

The API uses `EZTEST_EXECUTOR_URL` (default `http://127.0.0.1:13336`) and
`EZTEST_EXECUTOR_TOKEN`. `scripts/dev-api.ps1` only prints an executor health
hint; it never starts the executor.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `EZTEST_EXECUTOR_ADDR` | `127.0.0.1:13336` | listen address; the container overrides it to `0.0.0.0:13336` |
| `EZTEST_EXECUTOR_TOKEN` | required | bearer token for every `/v1` endpoint |
| `EZTEST_EXECUTOR_MAX_CONTEXTS` | `8` | concurrent browser contexts |
| `EZTEST_EXECUTOR_CHROMIUM_PATH` | unset | optional explicit Chromium binary (local dev only) |
| `EZTEST_EXECUTOR_ALLOW_DATA_PAGES` | unset | `1` allows `data:text/html` pages for self-tests only |

If the pinned Playwright revision is not installed, the service falls back to the
newest `chromium-*` already present in the local Playwright browser cache
(`%LOCALAPPDATA%\ms-playwright`, `~/.cache/ms-playwright`, ...). The Docker image
ships the matching revision, so the fallback never runs in containers.

## HTTP protocol

All endpoints except `/healthz` require `Authorization: Bearer <token>`.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/healthz` | — | `{status:"ok" \| "degraded",version,contexts,maxContexts,browserVersion?,error?}` (200 / 503) |
| GET | `/v1/contexts` | — | `{contexts:[{id,key,metadata?,createdAt}]}` (P3 extension) |
| POST | `/v1/contexts` | `{key, allowedHosts, baseUrl, storageState, sessionStorage, viewport, metadata}` | `{contextId, browserVersion}` |
| POST | `/v1/contexts/{id}/actions` | `{action, args}` | `{ok:true,result,artifacts}` or `{ok:false,error:{code,message}}` |
| POST | `/v1/contexts/{id}/cancel` | — | `{ok:true,result:{canceled}}` |
| DELETE | `/v1/contexts/{id}` | — | `{ok:true,result:{closed:true}}` |

`metadata` is an opaque JSON object (≤2KB) stored with the context and returned by
`GET /v1/contexts`; P3 uses it for `{case_run_id, version_sut_id, account_role,
dispatch_epoch, dispatch_token}` and for orphan-context reconciliation. A non-object
or oversized `metadata` is rejected with `400 invalid_arguments`. The list endpoint
is used by the API to reconcile contexts whose key starts with `run:`.

Actions: `goto`, `snapshot`, `click`, `fill`, `press`, `wait_for`, `screenshot`,
`console`, `network`, `download`, `storage_state`, `close`.

Targets: `{role,name,exact}`, `{text}`, `{test_id}` or `{selector}` — exactly
one kind per target. Zero matches → `target_not_found`; more than one match for a
single-element action → `target_ambiguous`.

Limits: request body ≤ 1MB, artifact ≤ 10MB, snapshot ≤ 12KB (truncated with
`truncated:true`), per-action timeout 15s (max 60s, `goto` 30s). Downloads are
sized with `stat` before being read; screenshots must be buffered by the browser
first, so their cap is applied after capture. `fill(secret=true)` values are
tracked for scrubbing in a bounded set (32 entries, 1024 characters each);
a fill that would exceed either bound is rejected with `invalid_arguments`
rather than silently evicting an earlier secret.

`/healthz` probes the browser (cached for 10s) and returns `503` with
`status:"degraded"` when Chromium cannot launch; the container healthcheck then
reports the service unhealthy.

## Security model

- Binds loopback by default; Compose publishes no host port and uses
  `shm_size: 1gb`.
- Bearer token required; the executor never logs request bodies or `fill`
  values. Secret fill values are scrubbed from action errors, console and
  network results, and ARIA snapshots (including the latter's title/URL).
- Every HTTP(S) request, including redirects and subresources, is checked
  against `allowedHosts` (exact, or subdomains for `*.suffix`/`.suffix`) plus the
  `baseUrl` host. The apex of a wildcard must be listed explicitly. Other hosts
  are aborted and surfaced as `host_not_allowed`; blocked subresources are
  reported in the navigation result as `blocked_hosts`. Redirect hops are not
  observable through Playwright's `context.route` in Chromium, so the main guard
  uses the CDP `Fetch` domain.
- Popups are not supported by the action protocol. Chromium reports a popup's
  first navigation before any page/frame exists, so that navigation is refused
  by a context-level route instead of allowing unguarded redirects; popups
  created as `about:blank` still receive the CDP guard when their page exists.
- Service workers are blocked (`serviceWorkers: "block"`): their requests bypass
  the page-level CDP session and therefore the allow list. Every page that does
  appear in a context gets the CDP guard.
- `file://`, `javascript:` and other schemes are refused; `data:text/html` is
  only reachable when `EZTEST_EXECUTOR_ALLOW_DATA_PAGES=1` (self-tests).
- A canceled action closes its page and recreates it before the queue releases,
  so the browser-side navigation/wait is actually terminated and the next action
  starts on a clean page.
- Downloads are written to a per-context temp directory, sized before reading,
  deleted after the action, and the directory is removed when the context
  closes.

## Tests

```powershell
cd executor
pnpm install
pnpm test
```

Tests use `node:test` with a local `http.createServer` fixture page; they never
depend on `scripts/fixture-target.cjs` or the Go stack. The suite starts real
headless Chromium, so it needs a working Playwright browser cache (or the
service's cached-browser fallback).

## Docker

`Dockerfile` is based on `mcr.microsoft.com/playwright:v1.64.1-noble`; Compose
builds it as the internal-only `executor` service and injects the token shared
with the API. The Dockerfile and Compose service have only been validated
statically on a host without Docker; build and run them on a Docker host before
relying on the container path.

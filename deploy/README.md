# EzTest deployment

This directory contains the container deployment for the EzTest 0.2 platform:
a multi-stage Go API image, a Next.js standalone web image, a Playwright
executor image, a Playwright PDF renderer image and a Compose file
with PostgreSQL 17 and **two API replicas**.

> **Validation status:** Docker is not installed on the development host, so the
> images have never been built and the Compose stack has never been started
> there. Only static validation is performed:
> `cd frontend && node scripts/validate-compose.mjs` and
> `scripts/check-docs.ps1`. Do not report the Compose deployment as
> "verified" — it is delivered and statically checked, not run. See
> `../docs/deployment.md` for the full deployment/runbook guidance.

P8 contract baseline: OpenAPI `0.2.0-p8` (`x-frozen: P8`) with **162 paths /
200 operations / 264 schemas**, **42 migrations**, **85 business tables** and
**85 database settings**. P8 adds no Compose service and no new startup
environment variable.

## Layout

| File | Purpose |
|---|---|
| `Dockerfile.api` | Multi-stage Go build; Alpine runtime; non-root `eztest` user; shared `/data` volume; `/app/.data -> /data` so the DB `paths.*` defaults land on the shared volume. |
| `Dockerfile.renderer` | Official Playwright image; only `renderer/` sources; non-root `pwuser`; listens on 13337; never contains API/executor/frontend source. |
| `Dockerfile.web` | Multi-stage Next.js `standalone` build; non-root `nextjs` user; listens on 6868. |
| `docker-compose.yml` | `postgres` + `api` + `api2` + `renderer` + internal `executor` + `web`; readiness checks; one shared `data` volume; startup ordering. |

## Prerequisites

- Docker Engine 24+ with Compose v2 (`docker compose`, not `docker-compose`).
- A repository-root `.env` created from `.env.example` with real deployment
  values. It is gitignored and must never be committed.
- The repository checked out at the build context root (the Compose
  `build.context` is `..`, so Dockerfiles can copy `backend/`, `frontend/`,
  `executor/`, `renderer/` and `docs/`).
- The repository-root `.dockerignore` keeps `.git`, `.tools`, `node_modules`,
  `.next`, local logs, `.env` and `.data` out of the build context.

## Required environment

Compose interpolates `POSTGRES_*` from the environment / `--env-file .env` and
passes the repository `.env` to both API containers with `env_file`.

| Variable | Used by | Notes |
|---|---|---|
| `POSTGRES_USER` | postgres + api URL | Defaults to `eztest`. |
| `POSTGRES_PASSWORD` | postgres + api URL | **Required**, no default. |
| `POSTGRES_DB` | postgres + api URL | Defaults to `eztest`. |
| `EZTEST_EXECUTOR_TOKEN` | api + executor | **Required**; shared Bearer token. |
| `EZTEST_RENDER_TOKEN` | renderer (optional) | PDF renderer Bearer token. Defaults to the DB seed value `eztest-dev-render-token`; if set, also set the DB setting `report.render_token` to the same value. |
| `NEXT_PUBLIC_API_BASE_URL` | web build | Build-time browser API origin, default `http://localhost:13333`. |
| `EZTEST_API_URL` | executor (optional) | Leave unset for pull-only registration; see below. |

The API process itself still reads only the four documented 0.2 startup
variables: `EZTEST_DATABASE_URL`, `EZTEST_API_PORT`, `EZTEST_WEB_PORT`,
`EZTEST_EXECUTOR_PORT`. Every other runtime knob lives in the database settings
center (paths, renderer URL/token, instance heartbeat, snowflake node id, ...).

### Renderer settings (required for PDFs)

The renderer container cannot configure the API: the API reads two DB settings
at boot/worker time. A fresh Compose boot must therefore point the API at the
Compose service name (the catalogue default `http://127.0.0.1:13337` is the
standalone/local default, not the container):

| DB setting | Compose value | Notes |
|---|---|---|
| `report.render_worker_url` | `http://renderer:13337` | Set once per stack (admin Settings UI or SQL below), then all revisions' PDFs render. |
| `report.render_token` | `eztest-dev-render-token` | Matches the renderer's default `EZTEST_RENDER_TOKEN`; override both together. |

Set the URL once after the first boot (Compose seeds no business settings):

```sql
UPDATE system_setting
SET value = '"http://renderer:13337"'::jsonb, updated_at = now()
WHERE key = 'report.render_worker_url';
```

(Use the admin Settings UI instead where available; the token is a secret and is
masked there.) After this, the leader-gated ensurer/worker backfills PDFs and
the compose validator remains the only required check (Docker is not installed
on the development host).

### Instance counters, rate limiting and retention (P8)

The two API replicas coordinate through PostgreSQL; P8 adds observability and
safety knobs without new containers or startup variables:

| DB setting | Default | Behaviour |
|---|---|---|
| `security.rate_limit_per_minute` | `0` (off) | Per-instance global token bucket. When enabled, excess requests get `429` with `rate_limited` and `Retry-After`; health/readiness/SSE/static paths are skipped. Total allowed rate = replicas × limit (two API replicas at 30/min allow roughly 60/min). |
| `retention.event_days` | `90` | Leader-gated daily cleanup of `event` rows older than the cutoff; `security.audit_retention_days` (default 90) does the same for audit rows. Uses `retention.gc_batch_size` per batch and writes the audit action `retention.cleanup`. |

`GET /api/admin/instances` reports `active_requests` and `active_work` per
instance, and `GET /api/health/ready` exposes the same counters as
`inflight_requests` / `inflight_work`. During a rolling upgrade, drain the old
instance and wait for both counters to reach 0 before stopping the process.

Both settings are hot-loaded. The retention worker runs on the elected leader;
rate limiting is enforced independently by each API replica.

### Executor push (optional)

The executor image defaults to P3 pull registration (`GET /healthz` polled by
the API). To enable the P7 push register/heartbeat as well, open
`docker-compose.yml`, add `EZTEST_API_URL: http://api:13333` to the executor
environment and restart the stack. Push registration stores the executor row
once; the pull healthz remains active. With two API replicas either replica may
poll; both write the same `executor_instance` row.

## Data volume

| Volume | Container path | Contents |
|---|---|---|
| `data` | `/data` on both `api` and `api2` | Content-addressed objects, requirement attachments, execution evidence, report HTML/PDF and exports (DB `paths.*` defaults resolve through `/app/.data -> /data`). |
| `pgdata` | `/var/lib/postgresql/data` | PostgreSQL data. |

Back up PostgreSQL and the `data` volume together; see
`../docs/operations.md`. The volume contract requires every API replica to share
one filesystem (a named volume on one Docker host, NFS/equivalent in a
multi-host deployment). Different `paths.data_root` values make readiness fail.

### Read-only code repositories (P4)

The API must be able to read the local Git worktrees registered as P2
`localPath`. Compose includes a commented example on `api`:

```yaml
# api.volumes:
#   - ${EZTEST_CODE_REPO_MOUNT:-./repos}:/repos:ro
```

Uncomment/adjust it on **both** `api` and `api2`, mount the repo tree at
`/repos` (read-only), then register `/repos/<name>` as the repository
`localPath` in `/code-repos`. P4 only reads the worktree; do not grant write
access.

## Build and run

From the repository root:

```powershell
# 1. Prepare a deployment environment file (never commit it).
Copy-Item .env.example .env
# Edit .env: set POSTGRES_PASSWORD and EZTEST_EXECUTOR_TOKEN.
# EZTEST_RENDER_TOKEN is optional (defaults to the DB seed token); set it only
# together with the DB setting report.render_token.

# 2. Build and start the stack.
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build

# 3. One-time: point the API at the Compose renderer (DB setting).
#    See "Renderer settings" above; then PDFs render on a fresh boot.

# 4. Inspect health and logs.
docker compose --env-file .env -f deploy/docker-compose.yml ps
docker compose --env-file .env -f deploy/docker-compose.yml logs -f api
```

Then open:

- Web UI: <http://localhost:6868>
- API liveness: <http://localhost:13333/healthz>
- API readiness: <http://localhost:13333/api/health/ready>
- Admin instance registry: <http://localhost:13333/api/admin/instances>

Rolling upgrade drill: start the replacement, wait for readiness, drain the old
instance through `POST /api/admin/instances/{instanceId}/drain`, wait for
`active_requests`/`active_work` to reach 0 (readiness turns 503 while draining),
then stop it.

Stop the stack without deleting data:

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml down
```

`down -v` deletes the `pgdata` and `data` volumes.

## Ports, volumes and health

| Service | Published port | Health check | Persistent data |
|---|---|---|---|
| `api` | 13333 | `GET /api/health/ready` | `data` -> `/data` |
| `api2` | none (internal 13333) | `GET /api/health/ready` | `data` -> `/data` |
| `renderer` | none (internal 13337) | `GET /healthz` via node fetch | — (PDF bytes go to the API content ledger) |
| `executor` | none (internal 13336) | `node src/healthcheck.mjs` | — (browser contexts are ephemeral) |
| `web` | 6868 | `GET /login` via wget | — |
| `postgres` | 5433 -> 5432 | `pg_isready` | `pgdata` -> `/var/lib/postgresql/data` |

Startup order is `postgres` (healthy) -> `api`/`api2` (ready) -> `web`; the
renderer and executor run alongside. The API applies all embedded migrations and
seeds the administrator on startup; repeated starts are idempotent. The second
replica gets a free `node_id` from the P7 instance registry automatically.

## Security notes

- No secrets are stored in this repository. `.env`, `.tools/` and
  `*.local.md` are gitignored.
- All runtime images run as non-root users.
- The executor and renderer are internal-only: they publish no host ports and
  share one Bearer token each with the API. Keep them on the private Compose
  network; never expose 13336/13337.
- EzTest does not provide TLS, multi-tenant isolation, a reverse proxy or public
  object URLs. Put a TLS-terminating proxy in front of 13333/6868 if exposed
  beyond localhost and restrict the PostgreSQL port.
- Back up the database settings that hold secrets (the DB dump contains the
  settings table; the key material to decrypt LLM credentials lives in the
  deployment environment). See `../docs/operations.md`.

## Static validation

```powershell
cd frontend
node scripts/validate-compose.mjs
```

The script uses the `yaml` dev dependency to assert the six services
(`api`/`api2`/`web`/`postgres`/`executor`/`renderer`), published ports (only
`api`, `web` and `postgres`; executor/renderer publish none), health checks on
all services, both API health checks targeting `/api/health/ready`, the shared
`data` volume mounted at `/data` by both replicas, the absence of the 0.1
content volumes and environment keys, the `depends_on` / `service_healthy`
ordering, and that the four Dockerfiles exist. It fails with exit code 1 and a
list of problems. It does not need or contact a Docker daemon.

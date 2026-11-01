# EzTest deployment

This directory contains the container deployment for the EzTest 0.2 platform:
a multi-stage Go API image, a Next.js standalone web image, a Playwright
executor image, a Playwright PDF renderer image and a P7 Docker Compose file
with PostgreSQL 17 and **two API replicas**.

> **Validation status:** Docker is not installed on the development host, so the
> images have never been built and the Compose stack has never been started
> there. Only static validation is performed:
> `cd frontend && node scripts/validate-compose.mjs` and
> `scripts/check-docs.ps1`. Do not report the Compose deployment as
> "verified" — it is delivered and statically checked, not run. See
> `../docs/deployment.md` for the full deployment/runbook guidance.

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
| `EZTEST_RENDER_TOKEN` | api + renderer | **Required**; PDF renderer Bearer token. |
| `NEXT_PUBLIC_API_BASE_URL` | web build | Build-time browser API origin, default `http://localhost:13333`. |
| `EZTEST_API_URL` | executor (optional) | Leave unset for pull-only registration; see below. |

The API process itself still reads only the four documented 0.2 startup
variables: `EZTEST_DATABASE_URL`, `EZTEST_API_PORT`, `EZTEST_WEB_PORT`,
`EZTEST_EXECUTOR_PORT`. Every other runtime knob lives in the database settings
center (paths, renderer URL/token, instance heartbeat, snowflake node id, ...).

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
# Edit .env: set POSTGRES_PASSWORD, EZTEST_EXECUTOR_TOKEN, EZTEST_RENDER_TOKEN.

# 2. Build and start the stack.
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build

# 3. Inspect health and logs.
docker compose --env-file .env -f deploy/docker-compose.yml ps
docker compose --env-file .env -f deploy/docker-compose.yml logs -f api
```

Then open:

- Web UI: <http://localhost:6868>
- API liveness: <http://localhost:13333/healthz>
- API readiness: <http://localhost:13333/api/health/ready>
- Admin instance registry: <http://localhost:13333/api/admin/instances>

Rolling upgrade drill: start the replacement, wait for readiness, drain the old
instance through `POST /api/admin/instances/{instanceId}/drain`, wait for its
in-flight work to finish (readiness turns 503), then stop it.

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

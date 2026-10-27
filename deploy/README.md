# EzTest deployment

This directory contains the container deployment for the P0 foundation and the
P1 Agent runtime: a multi-stage Go API image, a Next.js standalone web image and
a single-instance Docker Compose file with PostgreSQL 17.

> **Validation status:** Docker is not installed on the development host, so the
> images have never been built and the Compose stack has never been started
> there. Only static validation is performed:
> `cd frontend && node scripts/validate-compose.mjs`. Do not report the Compose
> deployment as "verified" — it is delivered and statically checked, not run.

## Layout

| File | Purpose |
|---|---|
| `Dockerfile.api` | Multi-stage Go build; Alpine runtime; non-root `eztest` user; ships `docs/openapi/api.yaml` at `./docs/openapi/api.yaml` in the working directory. |
| `Dockerfile.web` | Multi-stage Next.js `standalone` build; non-root `nextjs` user; listens on 6868. |
| `docker-compose.yml` | `api`, `web`, `postgres:17`; health checks; named volumes; startup ordering. |
| `../docs/openapi/api.yaml` | Hand-maintained OpenAPI document served at `GET /api/openapi.yaml`. |

## Prerequisites

- Docker Engine 24+ with Compose v2 (`docker compose`, not `docker-compose`).
- A repository-root `.env` created from `.env.example` with real deployment
  values. It is gitignored and must never be committed.
- The repository checked out at the build context root (the Compose `build.context`
  is `..`, so Dockerfiles can copy `backend/`, `frontend/` and `docs/`).
- The repository-root `.dockerignore` keeps `.git`, `.tools`, `node_modules`,
  `.next`, local logs and `.env` out of the build context; do not remove it.

## Required environment

Compose interpolates `POSTGRES_*` from the environment / `--env-file .env` and
passes the repository `.env` to the API container with `env_file`.

| Variable | Used by | Notes |
|---|---|---|
| `POSTGRES_USER` | postgres + api URL | Defaults to `eztest`. |
| `POSTGRES_PASSWORD` | postgres + api URL | **Required**, no default. Use the same password as the `EZTEST_DATABASE_URL` user. |
| `POSTGRES_DB` | postgres + api URL | Defaults to `eztest`. |
| `EZTEST_SECRET_KEYS` / `EZTEST_SECRET_KEY_ACTIVE` | api | Required AES-256 keyring; generate as shown in `.env.example`. |
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | api | Seeds the single P0 administrator on first boot. |
| `EZTEST_WEB_ORIGIN` | api | Browser origin for CORS, default `http://localhost:6868`. |
| `EZTEST_ENABLE_TEST_ENDPOINTS` | api | Default `false` in Compose. Set `true` only for the SSE test endpoint. |
| `EZTEST_AGENT_FILE_ROOT` | api | Optional read-only root for `file.list`/`file.read`. Empty (the default) disables the file tools. Mount the tree read-only and point this at the mount when they are needed. |
| `EZTEST_AGENT_HTTP_ALLOWLIST` / `EZTEST_AGENT_TOOL_TIMEOUT_MS` / `EZTEST_AGENT_MAX_CONCURRENT_RUNS` | api | Optional Agent limits; defaults `127.0.0.1,localhost`, `10000` and `4`. |
| `llm_base_url` / `llm_key` / `llm_model_name` | api | Optional first-boot LLM seed; lowercase names are intentional. Leave the key empty to disable real calls. |
| `NEXT_PUBLIC_API_BASE_URL` | web build | Build-time browser API origin, default `http://localhost:13333`. |

Other `EZTEST_*` values from `.env` are passed through unchanged; Compose
overrides `EZTEST_DATABASE_URL`, `EZTEST_HTTP_ADDR`, `EZTEST_STORAGE_DIR` and
`EZTEST_OPENAPI_PATH` so the containers talk to the `postgres` service and use
the mounted volumes and shipped OpenAPI file.

## Build and run

From the repository root:

```powershell
# 1. Prepare a deployment environment file (never commit it).
Copy-Item .env.example .env
# Edit .env: set POSTGRES_PASSWORD, EZTEST_ADMIN_PASSWORD and generate
# EZTEST_SECRET_KEYS (see the comments in .env.example).

# 2. Build and start the stack.
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build

# 3. Inspect health and logs.
docker compose --env-file .env -f deploy/docker-compose.yml ps
docker compose --env-file .env -f deploy/docker-compose.yml logs -f api
```

Then open:

- Web UI: <http://localhost:6868>
- API health: <http://localhost:13333/api/health>
- OpenAPI: <http://localhost:13333/api/openapi.yaml>

Stop the stack without deleting data:

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml down
```

`down -v` deletes the `pgdata` and `objects` volumes.

## Ports, volumes and health

| Service | Published port | Health check | Persistent data |
|---|---|---|---|
| `api` | 13333 | `GET /healthz` via wget | `objects` → `/data/objects` |
| `web` | 6868 | `GET /login` via wget | — |
| `postgres` | 5433 → 5432 | `pg_isready` | `pgdata` → `/var/lib/postgresql/data` |

Startup order is `postgres` (healthy) → `api` (healthy) → `web`. The API applies
all embedded migrations and seeds the administrator on startup; repeated starts
are idempotent.

## Security notes

- No secrets are stored in this repository. `.env`, `.tools/` and
  `*.local.md` are gitignored.
- Both runtime images run as non-root users (`eztest` UID 10001, `nextjs`
  UID 1001).
- The API stores LLM credentials encrypted with `EZTEST_SECRET_KEYS`. Losing
  that keyring makes stored credentials unrecoverable; back it up.
- P0/P1 is a single-instance, intranet-oriented deployment. It does not provide
  TLS, multi-tenant isolation, a reverse proxy or public object URLs. Put a
  TLS-terminating proxy in front of ports 13333/6868 if exposed beyond
  localhost, and restrict the PostgreSQL port.
- P1 Agent endpoints are administrator-only. The Agent file tools are disabled
  unless `EZTEST_AGENT_FILE_ROOT` is set; keep that root read-only and
  minimal. `http.get` only reaches `EZTEST_AGENT_HTTP_ALLOWLIST` hosts
  (localhost by default).
- The administrator password lives only in `.env` and, for the local
  development host, in the gitignored `docs/dev-setup.local.md`.

## Static validation

```powershell
cd frontend
node scripts/validate-compose.mjs
```

The script uses the `yaml` dev dependency to assert services `api`/`web`/
`postgres`, published ports, health checks, the `pgdata`/`objects` volumes and
the `depends_on` / `service_healthy` ordering. It fails with exit code 1 and a
list of problems. It does not need or contact a Docker daemon.

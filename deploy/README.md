# EzTest deployment

This directory contains the container deployment for the EzTest platform: a
multi-stage Go API image, a Next.js standalone web image, a Playwright executor
image and a single-instance Docker Compose file with PostgreSQL 17.

> **Validation status:** Docker is not installed on the development host, so the
> images have never been built and the Compose stack has never been started
> there. Only static validation is performed:
> `cd frontend && node scripts/validate-compose.mjs` (and the Compose contract in
> `scripts/acceptance.ps1 -StaticOnly`). Do not report the Compose deployment as
> "verified" — it is delivered and statically checked, not run. See
> `../docs/deployment.md` for the full deployment/runbook guidance.

## Layout

| File | Purpose |
|---|---|
| `Dockerfile.api` | Multi-stage Go build; Alpine runtime; non-root `eztest` user; creates `/data/{objects,version-attachments,evidence,reports,exports}`; ships `docs/openapi/api.yaml`. |
| `Dockerfile.web` | Multi-stage Next.js `standalone` build; non-root `nextjs` user; listens on 6868. |
| `docker-compose.yml` | `api`, `web`, `postgres:17`, internal `executor`; health checks; P8 named volumes; startup ordering; commented read-only code-repo mount example. |
| `../docs/openapi/api.yaml` | Hand-maintained OpenAPI document served at `GET /api/openapi.yaml`. |
| `../docs/deployment.md` | Deployment, `.env`, reverse proxy/TLS, backup preconditions, upgrade and troubleshooting. |

## Prerequisites

- Docker Engine 24+ with Compose v2 (`docker compose`, not `docker-compose`).
- A repository-root `.env` created from `.env.example` with real deployment
  values. It is gitignored and must never be committed.
- The repository checked out at the build context root (the Compose `build.context`
  is `..`, so Dockerfiles can copy `backend/`, `frontend/`, `executor/` and `docs/`).
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
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | api | Seeds the single administrator on first boot. |
| `EZTEST_EXECUTOR_TOKEN` | api + executor | **Required**; shared Bearer token, executor publishes no host port. |
| `EZTEST_WEB_ORIGIN` | api | Browser origin for CORS, default `http://localhost:6868`. |
| `NEXT_PUBLIC_API_BASE_URL` | web build | Build-time browser API origin, default `http://localhost:13333`. |
| `EZTEST_ENABLE_TEST_ENDPOINTS` | api | Default `false` in Compose. Set `true` only for the SSE test endpoint. |
| `EZTEST_AGENT_FILE_ROOT` | api | Optional read-only root for `file.list`/`file.read`. Empty (the default) disables the file tools. Mount the tree read-only and point this at the mount when they are needed. |
| `EZTEST_AGENT_HTTP_ALLOWLIST` / `EZTEST_AGENT_TOOL_TIMEOUT_MS` / `EZTEST_AGENT_MAX_CONCURRENT_RUNS` | api | Optional Agent limits; defaults `127.0.0.1,localhost`, `10000` and `4`. |
| `llm_base_url` / `llm_key` / `llm_model_name` | api | Optional first-boot LLM seed; lowercase names are intentional. Leave the key empty to disable real calls. |
| `EZTEST_RATE_LIMIT_RPS` / `BURST` / `SSE_MAX` | api | P8 rate limits (defaults 50 / 100 / 10). Process-local; per-instance budgets. |
| `EZTEST_GC_ENABLED` / `RETENTION_DAYS` / `BATCH_KEEP` | api | P8 evidence GC (default false / 30 / 3); non-dry-run requires `true`. |
| `EZTEST_INSTANCE_ID` / `EZTEST_LEADER_LOCK_KEY` | api | Multi-instance leader lock; instance id must be unique, lock key shared. |
| `EZTEST_KNOWLEDGE_MAX_INJECT` / `MAX_BYTES` | api | P8 knowledge injection budgets (default 5 / 4096). |

Other `EZTEST_*` values from `.env` are passed through unchanged; Compose
overrides `EZTEST_DATABASE_URL`, `EZTEST_HTTP_ADDR`, `EZTEST_OPENAPI_PATH` and the
five content directories so the containers talk to the `postgres` service and use
the mounted volumes.

### Content directories and volumes

| Volume | Container path | Key | Content |
|---|---|---|---|
| `objects` | `/data/objects` | `EZTEST_STORAGE_DIR` | Content-addressed objects. |
| `attachments` | `/data/version-attachments` | `EZTEST_VERSION_ATTACHMENT_DIR` | Requirement attachments. |
| `evidence` | `/data/evidence` | `EZTEST_EVIDENCE_DIR` | Execution evidence (the only GC target). |
| `reports` | `/data/reports` | `EZTEST_REPORT_DIR` | Report Markdown. |
| `exports` | `/data/exports` | `EZTEST_EXPORT_DIR` | Mindmap exports. |
| `pgdata` | `/var/lib/postgresql/data` | — | PostgreSQL data. |

Back up PostgreSQL and the five content directories together; see
`../docs/operations.md`.

### Read-only code repositories (P4)

The API must be able to read the local Git worktrees registered as P2
`localPath`. Compose includes a commented example:

```yaml
# api.volumes:
#   - ${EZTEST_CODE_REPO_MOUNT:-./repos}:/repos:ro
```

Uncomment/adjust it, mount the repo tree at `/repos` (read-only), then register
`/repos/<name>` as the repository `localPath` in `/code-repos`. P4 only reads the
worktree; do not grant write access.

## Build and run

From the repository root:

```powershell
# 1. Prepare a deployment environment file (never commit it).
Copy-Item .env.example .env
# Edit .env: set POSTGRES_PASSWORD, EZTEST_ADMIN_PASSWORD, EZTEST_EXECUTOR_TOKEN
# and generate EZTEST_SECRET_KEYS (see the comments in .env.example).

# 2. Build and start the stack.
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build

# 3. Inspect health and logs.
docker compose --env-file .env -f deploy/docker-compose.yml ps
docker compose --env-file .env -f deploy/docker-compose.yml logs -f api
```

Then open:

- Web UI: <http://localhost:6868>
- API liveness: <http://localhost:13333/healthz>
- API readiness: <http://localhost:13333/api/health>
- OpenAPI: <http://localhost:13333/api/openapi.yaml>

Stop the stack without deleting data:

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml down
```

`down -v` deletes the `pgdata`, `objects`, `attachments`, `evidence`, `reports`
and `exports` volumes.

## Ports, volumes and health

| Service | Published port | Health check | Persistent data |
|---|---|---|---|
| `api` | 13333 | `GET /healthz` via wget | `objects`, `attachments`, `evidence`, `reports`, `exports` |
| `web` | 6868 | `GET /login` via wget | — |
| `postgres` | 5433 → 5432 | `pg_isready` | `pgdata` → `/var/lib/postgresql/data` |
| `executor` | none (internal 13336) | `node src/healthcheck.mjs` | — (browser contexts are ephemeral) |

Startup order is `postgres` (healthy) → `api` (healthy) → `web`; `executor` runs
alongside and is only used when a batch executes. The API applies all embedded
migrations and seeds the administrator on startup; repeated starts are
idempotent.

## Security notes

- No secrets are stored in this repository. `.env`, `.tools/` and
  `*.local.md` are gitignored.
- Both runtime images run as non-root users (`eztest` UID 10001, `nextjs`
  UID 1001).
- The API stores LLM credentials encrypted with `EZTEST_SECRET_KEYS`. Losing
  that keyring makes stored credentials unrecoverable; back it up separately
  (never inside the backup archive).
- EzTest does not provide TLS, multi-tenant isolation, a reverse proxy or public
  object URLs. Put a TLS-terminating proxy in front of 13333/6868 if exposed
  beyond localhost, restrict the PostgreSQL port, and keep the executor
  internal-only. See `../docs/deployment.md` for an Nginx sketch.
- The Next image sets `X-Content-Type-Options`, `Referrer-Policy`,
  `X-Frame-Options`, `Permissions-Policy` and a report-only CSP; the proxy should
  add HSTS and any enforcing CSP. API responses do not carry the Next headers.
- P1 Agent endpoints are administrator-only. The Agent file tools are disabled
  unless `EZTEST_AGENT_FILE_ROOT` is set; keep that root read-only and
  minimal. `http.get` only reaches `EZTEST_AGENT_HTTP_ALLOWLIST` hosts
  (localhost by default).
- P8 rate limiting is in-process (per instance), not a cluster-wide quota. GC is
  dry-run by default and only removes execution evidence files.

## Static validation

```powershell
cd frontend
node scripts/validate-compose.mjs
```

The script uses the `yaml` dev dependency to assert services
`api`/`web`/`postgres`/`executor`, published ports, health checks, the
`pgdata`/`objects`/`attachments`/`evidence`/`reports`/`exports` volumes and the
`depends_on` / `service_healthy` ordering. It fails with exit code 1 and a list of
problems. It does not need or contact a Docker daemon. The broader static gate is:

```powershell
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly
```

Neither check builds an image or starts a container.

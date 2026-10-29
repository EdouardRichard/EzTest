# EzTest deployment (Docker Compose)

This directory contains the container deployment for the EzTest platform: a multi-stage Go API image, a
Next.js standalone web image, a Playwright executor image and a single-instance Docker Compose file with
PostgreSQL 17.

> **Validation status:** the maintainers did not run Docker on the development host, so the images have
> never been built and the Compose stack has never been started there. Only static validation is
> performed (`cd frontend; node scripts/validate-compose.mjs`). Do not treat the Compose path as a
> verified installation — validate it in your environment. For a Docker-free installation see
> [Option B in the root README](../README.md#option-b--portable-packages-windows--linux).

## Layout

| File | Purpose |
|---|---|
| `Dockerfile.api` | Multi-stage Go build; Alpine runtime; non-root `eztest` user; creates `/data/{objects,version-attachments,evidence,reports,exports}`; ships `docs/openapi/api.yaml`. |
| `Dockerfile.web` | Multi-stage Next.js `standalone` build; non-root `nextjs` user; listens on 6868. |
| `docker-compose.yml` | `api`, `web`, `postgres:17`, internal `executor`; health checks; named volumes; startup ordering; commented read-only code-repo mount example. |
| `../docs/openapi/api.yaml` | Hand-maintained OpenAPI document served at `GET /api/openapi.yaml`. |

## Prerequisites

- Docker Engine 24+ with Compose v2 (`docker compose`, not `docker-compose`).
- A repository-root `.env` created from `.env.example` with real deployment values. It is gitignored and
  must never be committed.
- The repository checked out at the build-context root (the Compose `build.context` is `..`, so the
  Dockerfiles can copy `backend/`, `frontend/`, `executor/` and `docs/`).
- The repository-root `.dockerignore` keeps `.git`, `.tools`, `node_modules`, `.next`, local logs and
  `.env` out of the build context; do not remove it.
- ≥ 4 vCPU / 8 GiB RAM / 20 GiB disk to start; the executor runs Chromium (`shm_size: 1gb`) and real
  execution needs more memory.

## Required environment

Compose interpolates `POSTGRES_*` from the environment / `--env-file .env` and passes the repository
`.env` to the API container with `env_file`.

| Variable | Used by | Notes |
|---|---|---|
| `POSTGRES_USER` | postgres + api URL | Defaults to `eztest`. |
| `POSTGRES_PASSWORD` | postgres + api URL | **Required**, no default. Must match the password in `EZTEST_DATABASE_URL`. |
| `POSTGRES_DB` | postgres + api URL | Defaults to `eztest`. |
| `EZTEST_SECRET_KEYS` / `EZTEST_SECRET_KEY_ACTIVE` | api | Required AES-256-GCM keyring; generate as shown in `.env.example`. Back it up separately. |
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | api | Seeds the single administrator on first boot. |
| `EZTEST_EXECUTOR_TOKEN` | api + executor | **Required**; shared bearer token. The executor publishes no host port. |
| `EZTEST_WEB_ORIGIN` | api | Browser origin for CORS, default `http://localhost:6868`. |
| `NEXT_PUBLIC_API_BASE_URL` | web build | Build-time browser API origin, default `http://localhost:13333`. |
| `EZTEST_ENABLE_TEST_ENDPOINTS` | api | Default `false` in Compose. Set `true` only for the SSE test endpoint. |
| `EZTEST_AGENT_FILE_ROOT` | api | Optional read-only root for the Agent file tools. Empty (default) disables them. |
| `EZTEST_AGENT_HTTP_ALLOWLIST` / `EZTEST_AGENT_TOOL_TIMEOUT_MS` / `EZTEST_AGENT_MAX_CONCURRENT_RUNS` | api | Optional Agent limits; defaults `127.0.0.1,localhost`, `10000` and `4`. |
| `EZTEST_RATE_LIMIT_RPS` / `EZTEST_RATE_LIMIT_BURST` / `EZTEST_RATE_LIMIT_SSE_MAX` | api | Per-instance rate limits (defaults 50 / 100 / 10). |
| `EZTEST_GC_ENABLED` / `EZTEST_GC_RETENTION_DAYS` / `EZTEST_GC_BATCH_KEEP` | api | Evidence GC (default false / 30 / 3); a non-dry-run purge requires `true`. |
| `EZTEST_INSTANCE_ID` / `EZTEST_LEADER_LOCK_KEY` | api | Multi-instance leader lock; the instance id must be unique, the lock key shared. |
| `EZTEST_KNOWLEDGE_MAX_INJECT` / `EZTEST_KNOWLEDGE_MAX_BYTES` | api | Knowledge injection budgets (default 5 / 4096). |
| `EZTEST_EXEC_MAX_TOOL_CALLS` | api | Per-case Agent tool-call budget, 1–200, default `30`. |
| `EZTEST_ANALYSIS_MAX_CASES` / `EZTEST_SUPPLEMENT_MAX_CASES` / `EZTEST_AUTONOMY_BATCHES_PER_TICK` | api | Analysis 1–50 (default 20), supplement 1–20 (default 5), autonomy 1–20 (default 4). |
| `EZTEST_EXEC_ALLOWED_HOSTS` | executor | Executor-wide WebSocket allow list (comma-separated, default `localhost,127.0.0.1`); remote targets must be added explicitly. |
| `EZTEST_VISUAL_BASELINE_MAX_BYTES` | api + executor process | Approved visual baseline / comparison cap, 64 KiB–20 MiB, default `5242880` (5 MiB). |
| `llm_base_url` / `llm_key` / `llm_model_name` | api | Optional first-boot LLM seed (lowercase names are intentional). Leave the key empty to configure the LLM in the UI. |

Other `EZTEST_*` values from `.env` are passed to the `api` service unchanged; Compose overrides
`EZTEST_DATABASE_URL`, `EZTEST_HTTP_ADDR`, `EZTEST_OPENAPI_PATH` and the content directories so the
containers talk to the `postgres` service and use the mounted volumes. The `executor` service has an
explicit environment list; extend it if the container needs extra executor variables.

### Content directories and volumes

| Volume | Container path | Key | Content |
|---|---|---|---|
| `objects` | `/data/objects` | `EZTEST_STORAGE_DIR` | Content-addressed objects. |
| `attachments` | `/data/version-attachments` | `EZTEST_VERSION_ATTACHMENT_DIR` | Requirement attachments. |
| `evidence` | `/data/evidence` | `EZTEST_EVIDENCE_DIR` | Execution evidence (the only GC target). |
| `reports` | `/data/reports` | `EZTEST_REPORT_DIR` | Report Markdown. |
| `exports` | `/data/exports` | `EZTEST_EXPORT_DIR` | Mindmap exports. |
| `pgdata` | `/var/lib/postgresql/data` | — | PostgreSQL data. |

Back up PostgreSQL and the five content directories together. The backup and restore scripts are not part
of this public snapshot; use `pg_dump -Fc` plus a tar of the directories, and keep the three parts
(database dump, content archive, manifest) together.

### Read-only code repositories

The API must be able to read the local Git worktrees registered as code repositories. Compose includes a
commented example:

```yaml
# api.volumes:
#   - ${EZTEST_CODE_REPO_MOUNT:-./repos}:/repos:ro
```

Uncomment/adjust it, mount the repo tree at `/repos` (read-only), then register `/repos/<name>` as the
repository `localPath` in `/code-repos`. EzTest only reads the worktree; do not grant write access.

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

`down -v` deletes the `pgdata`, `objects`, `attachments`, `evidence`, `reports` and `exports` volumes.

## Ports, volumes and health

| Service | Published port | Health check | Persistent data |
|---|---|---|---|
| `api` | 13333 | `GET /healthz` via wget | `objects`, `attachments`, `evidence`, `reports`, `exports` |
| `web` | 6868 | `GET /login` via wget | — |
| `postgres` | 5433 → 5432 | `pg_isready` | `pgdata` |
| `executor` | none (internal 13336) | `node src/healthcheck.mjs` | — (browser contexts are ephemeral) |

Startup order is `postgres` (healthy) → `api` (healthy) → `web`; `executor` runs alongside and is only used
when a batch executes. The API applies all embedded migrations and seeds the administrator on startup;
repeated starts are idempotent.

## Security notes

- No secrets are stored in this repository. `.env`, `.tools/` and `*.local.md` are gitignored; `.env` must
  never be committed.
- Both runtime images run as non-root users (`eztest` UID 10001, `nextjs` UID 1001).
- The API stores LLM credentials encrypted with `EZTEST_SECRET_KEYS`. Losing that keyring makes stored
  credentials unrecoverable; back it up separately (never inside the backup archive).
- Resource access credentials and browser session state are stored unencrypted by design (internal-tool
  boundary). Treat the database and its backups as sensitive.
- EzTest does not provide TLS, multi-tenant isolation, a reverse proxy or public object URLs. Put a
  TLS-terminating proxy in front of 13333/6868 if exposed beyond localhost, restrict the PostgreSQL port,
  and keep the executor internal-only.
- The Next image sets `X-Content-Type-Options`, `Referrer-Policy`, `X-Frame-Options`, `Permissions-Policy`
  and a report-only CSP; the proxy should add HSTS and any enforcing CSP. API responses do not carry the
  Next headers.
- Agent endpoints are administrator-only. Agent file tools are disabled unless `EZTEST_AGENT_FILE_ROOT` is
  set; keep that root read-only and minimal. `http.get` only reaches `EZTEST_AGENT_HTTP_ALLOWLIST` hosts
  (loopback by default).
- Rate limiting is in-process (per instance), not a cluster-wide quota. Evidence GC is dry-run by default
  and only removes execution evidence files.

## Static validation

```powershell
cd frontend
node scripts/validate-compose.mjs
```

The script uses the `yaml` dev dependency to assert the four services, published ports (the executor
publishes none and exposes only the internal `13336`), health checks on all four services, the named
volumes and API mounts, the `depends_on` / `service_healthy` ordering, and that the three Dockerfiles
exist. It does not need or contact a Docker daemon, and it does not build an image or start a container.

See the root README for the overall project status, installation options and known limitations.

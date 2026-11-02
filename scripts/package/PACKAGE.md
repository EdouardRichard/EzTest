# EzTest package (`__VERSION__`)

A self-contained, Docker-free build of the EzTest AI end-to-end testing
platform: the Go API, the Playwright execution service, the headless PDF
renderer and the Next.js web UI, plus a bundled Node.js runtime and the pinned
Chromium build the executor and renderer launch. Only **PostgreSQL 17** is an
external prerequisite.

| Content | Version |
|---|---|
| Package | `__VERSION__` |
| API | Go static binary (`bin/eztest.exe` / `bin/eztest`) |
| Web | Next.js standalone (`web/`) |
| Executor | Playwright `__PLAYWRIGHT_VERSION__` (`executor/`) |
| PDF renderer | Playwright `__PLAYWRIGHT_VERSION__` (`renderer/`) |
| Browsers | Chromium `__CHROMIUM_VERSION__` (revision `__CHROMIUM_REVISION__`), headless shell, FFmpeg `__FFMPEG_REVISION__` |
| Node.js runtime | `__NODE_VERSION__` (LTS) |
| Database | PostgreSQL 17, **not bundled** (external) |

EzTest 0.2 keeps runtime configuration in the database **settings center**
(admin UI). The API only reads four startup environment variables; see
section 4.

## Package layout

```
eztest-<version>-<os>-x64/
  VERSION                       package version (also reported by /api/health)
  manifest.json                 build metadata (version, commit, platform, versions)
  PACKAGE.md                    this file
  LICENSE                       EzTest license (Apache-2.0)
  THIRD_PARTY_NOTICES.md        bundled third-party components
  .env.example                  configuration template (init-config renders it)
  docs/openapi/api.yaml         OpenAPI 3 document (reference; 0.2 does not serve it)
  bin/eztest(.exe)              Go API binary
  web/                          Next.js standalone server (server.js, .next/)
  executor/                     Playwright protocol service (src/, node_modules/)
  renderer/                     headless HTML-to-PDF service (src/, node_modules/)
  playwright-browsers/          pinned Chromium, headless shell and FFmpeg
  runtime/node/                 bundled Node.js runtime
  logs/, run/                   created on the first start (logs and pid files)
  .data/                        created on the first start: attachments, evidence,
                                reports, exports (not shipped in the archive)
  init-config.ps1 / .sh         first-run configuration (creates .env)
  start-eztest.ps1 / .sh        starts the whole stack with health waits
  stop-eztest.ps1 / .sh         stops web -> renderer -> executor (tree) -> API
  status-eztest.ps1 / .sh       table of processes, health probes and PostgreSQL
  smoke-test.ps1 / .sh          9 end-to-end checks (see section 9)
  install-linux-deps.sh         Linux-only browser system dependency installer
```

Use the `.ps1` scripts on Windows (PowerShell 5.1+) and the `.sh` scripts on
Linux. Both script sets are shipped in every archive.

## 1. Prerequisite: PostgreSQL 17

The package does not ship a database server. Install PostgreSQL 17 and create a
database and role for EzTest:

**Windows**

1. Install PostgreSQL 17 (EDB installer) or use an existing PostgreSQL 17
   service; make sure it listens on a port you can reach (default 5432; the
   development setup used 5433).
2. Create the database/role, for example with `psql`:
   ```sql
   CREATE ROLE eztest LOGIN PASSWORD '<db-password>';
   CREATE DATABASE eztest OWNER eztest;
   ```
3. Build the connection string:
   `postgres://eztest:<db-password>@localhost:5432/eztest?sslmode=disable`

**Linux (Debian/Ubuntu)**

```bash
sudo apt install postgresql-17 postgresql-client-17
sudo systemctl start postgresql
sudo -u postgres psql -c "CREATE ROLE eztest LOGIN PASSWORD '<db-password>';"
sudo -u postgres psql -c "CREATE DATABASE eztest OWNER eztest;"
```

The API applies its embedded migrations automatically on startup and seeds the
administrator on the first start, so an empty database is enough.

## 2. Quick start

**Windows (PowerShell 5.1+)**

```powershell
# 1. Extract the ZIP (tar.exe is faster and handles long paths):
tar -xf eztest-__VERSION__-windows-x64.zip
cd eztest-__VERSION__-windows-x64

# 2. Create .env with the database URL:
.\init-config.ps1 -DatabaseUrl "postgres://eztest:<db-password>@localhost:5432/eztest?sslmode=disable"
#    (or edit EZTEST_DATABASE_URL in .env afterwards)

# 3. Start the API, executor, web UI and PDF renderer (waits for health):
.\start-eztest.ps1

# 4. Open http://localhost:6868 and sign in as admin / admin123.
#    Change that password immediately in the UI (the seed is fixed).

# Optional: verify the whole stack (9 checks):
.\smoke-test.ps1
```

**Linux (bash 4+, Debian/Ubuntu)**

```bash
# 1. Extract (tar preserves the directory tree; the scripts restore missing
#    executable bits automatically):
tar -xzf eztest-__VERSION__-linux-x64.tar.gz
cd eztest-__VERSION__-linux-x64

# 2. Install the Chromium system libraries once (root/sudo, network needed;
#    needed by both the executor and the PDF renderer):
sudo bash install-linux-deps.sh

# 3. Create .env with the database URL:
bash init-config.sh --database-url "postgres://eztest:<db-password>@localhost:5432/eztest?sslmode=disable"

# 4. Start the API, executor, web UI and PDF renderer (waits for health):
bash start-eztest.sh

# 5. Open http://localhost:6868 and sign in as admin / admin123, then change
#    the password in the UI.

# Optional: verify the whole stack (9 checks):
bash smoke-test.sh
```

The Windows ZIP can also be extracted with Explorer; if paths look broken,
extract with `tar -xf` instead. On Linux always start with
`bash start-eztest.sh` (a Windows-built tar loses POSIX executable bits; the
script restores them for the bundled binaries and the Chromium executables).

## 3. URLs, ports and lifecycle

| Endpoint | Default URL |
|---|---|
| Web UI | <http://localhost:6868> |
| API liveness | <http://127.0.0.1:13333/healthz> |
| API readiness (db check) | <http://127.0.0.1:13333/api/health> |
| Executor health | <http://127.0.0.1:13336/healthz> |
| PDF renderer health | <http://127.0.0.1:13337/healthz> |

| Command | What it does |
|---|---|
| `init-config` | Creates `.env` (renders the package-root paths and the database URL). `-Force`/`--force` regenerates and backs up the old file. |
| `start-eztest` | Preflights PostgreSQL/ports, creates `.data/`, starts API -> executor -> web -> renderer, waits for health, writes `run/*.pid` and `logs/*.log`. |
| `status-eztest` | Prints processes, the five health probes and PostgreSQL reachability. Exits 0 only when healthy. |
| `stop-eztest` | Stops web -> renderer -> executor (whole process tree/group, Chromium included) -> API. Leaves PostgreSQL and `.data/` untouched. |
| `smoke-test` | Runs the 9 checks listed in section 9 against the running stack. |

The administrator is the fixed first-start seed `admin` / `admin123`. It is
created once on the first API start; change the password in the admin UI
immediately and keep the admin email/credentials private. The scripts never
print token values.

## 4. Configuration (`.env`)

`init-config` renders `.env` from `.env.example`, replaces the package-root
placeholders with this package's absolute path and stores the database URL.

**EzTest 0.2 has two configuration layers:**

1. **The four API startup variables** (this is the complete list the Go API
   reads; everything else in the environment is ignored by the API):

   | Key | Meaning |
   |---|---|
   | `EZTEST_DATABASE_URL` | **Required.** PostgreSQL 17 URL. Changing it needs an API restart. |
   | `EZTEST_API_PORT` | API HTTP port (default `13333`). |
   | `EZTEST_WEB_PORT` | Web UI port (default `6868`); the API derives its CORS origins from it. |
   | `EZTEST_EXECUTOR_PORT` | Executor port (default `13336`); the API seeds its executor URL from it on the first start. |

2. **The database settings center** (admin UI): storage/content paths,
   retention, LLM provider, agent budgets, executor/renderer URLs and tokens,
   report layout, limits. Executor and PDF renderer URLs and tokens are
   configured there, not in the environment.

Other `.env` keys used by the package scripts:

| Key | Meaning |
|---|---|
| `EZTEST_EXECUTOR_ADDR` | Executor listen address (default `127.0.0.1:13336`; keep its port equal to `EZTEST_EXECUTOR_PORT`). |
| `EZTEST_EXECUTOR_TOKEN` | Executor Bearer token. Must equal the settings center value `execution.executor_token` (packaged default matches the API seed). |
| `EZTEST_RENDER_TOKEN` | PDF renderer Bearer token. Must equal the settings center value `report.render_token` (packaged default matches the API seed). |
| `EZTEST_RENDERER_ADDR` | Renderer listen address (default `127.0.0.1:13337`; the settings center's `report.render_worker_url` defaults to that port). |
| `EZTEST_EXECUTOR_MAX_CONTEXTS` | Concurrent browser contexts (default 8; each busy context needs ~0.5-1 GB RAM). |
| `EZTEST_EXEC_ALLOWED_HOSTS` | Executor-wide WebSocket allow list; add remote test-target hosts explicitly. |
| `EZTEST_VISUAL_BASELINE_MAX_BYTES` | Visual baseline request cap. |
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | Only used by `smoke-test` to log in. The API ignores them: the seed is `admin` / `admin123`. Update the password here after changing it in the UI. |
| `PORT` / `HOSTNAME` | Web server port/bind (default `6868` / `127.0.0.1`); keep `PORT` equal to `EZTEST_WEB_PORT`. |
| `PLAYWRIGHT_BROWSERS_PATH` | Rewritten by `init-config` to this package's `playwright-browsers/` (read by the executor and the renderer). |

Notes:

- Content storage defaults to `./.data` (`./.data/attachments`,
  `./.data/evidence`, `./.data/reports`, `./.data/exports`) relative to the API
  working directory, which is the package root. Change the paths in the
  settings center; `start-eztest` creates the four default directories first.
- The web bundle's API origin (`__API_BASE_URL__`) is **inlined at build time**.
  Changing the API host/port requires rebuilding the web bundle; changing the
  API port alone is not enough for a remote browser. For another machine to
  reach both services, put a reverse proxy in front (see section 10).
- Running with the packaged default tokens keeps executions and PDF reports
  working. If you change `EZTEST_EXECUTOR_TOKEN` / `EZTEST_RENDER_TOKEN` here,
  change the matching settings-center values first, or the calls fail with 401.
- Keep `.env` private (`chmod 600` / owner-only ACL; `init-config` sets it).

## 5. Linux browser dependencies

Both the executor and the PDF renderer launch Chromium headlessly. On a minimal
Debian/Ubuntu install the libraries are missing and their `/healthz` reports
`503 degraded` with a browser launch error (smoke checks 4/6 then fail loudly).
Install them once:

```bash
sudo bash install-linux-deps.sh
```

The script asks Playwright itself (`playwright install-deps chromium`) for the
authoritative package list, adds fonts/Xvfb, falls back to the documented
Ubuntu 24.04 / Debian 12 apt list, and finally verifies the bundled Chromium
has no missing shared libraries (`ldd`). Windows needs no extra packages.

## 6. Data, backup and upgrades

- User content lives in `.data/` (attachments, execution evidence, reports,
  exports); PostgreSQL holds the relational data. Both are created/seeded by the
  API on startup.
- **Back up PostgreSQL and `.data/` together**; neither is complete alone.
  `pg_dump`/`pg_restore` are not shipped; use the system PostgreSQL client tools
  of the same major version.
- Stop the stack (`stop-eztest`) before copying `.data/` for a consistent
  snapshot.
- Restore/upgrade: stop, replace the package directory with the new version but
  keep `.data/` and `.env` (absolute paths point at this package, so keep the
  directory name/location identical), then start and run `smoke-test`.
- Upgrading the package does **not** migrate a database backward; PostgreSQL
  migrations are forward-only.

## 7. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `Missing .env; run init-config` | Run `init-config.ps1` / `bash init-config.sh` first. |
| `PostgreSQL is not reachable at host:port` | PostgreSQL 17 is not running, the port/host in `EZTEST_DATABASE_URL` is wrong, or `pg_hba.conf` rejects the user. Start it and test with `psql "<url>"`. |
| `Port 13333/6868/13336/13337 is already in use` | Another process (often an older run or a dev stack) owns the port. Stop it with the matching package `stop` script or change the port in `.env`. |
| `EzTest is already running (pid N)` | A previous start is still up; run `status-eztest` and then `stop-eztest`. |
| API exits during startup | Read `logs/api.err.log`: most often a bad `EZTEST_DATABASE_URL`, an unreachable database, or a port conflict. |
| `executor ... degraded` / HTTP 503 | Chromium cannot launch. Linux: run `sudo bash install-linux-deps.sh`. Windows: no extra deps should be needed; check `logs/executor.err.log` and available RAM. |
| `renderer ... degraded` / HTTP 503 | Same as above for the PDF renderer; check `logs/renderer.err.log`. PDF report generation fails until this health check is green. |
| Executions or PDF reports fail with 401 | `EZTEST_EXECUTOR_TOKEN` / `EZTEST_RENDER_TOKEN` in `.env` no longer match the settings-center values; align both sides and restart. |
| `PORT ... and EZTEST_WEB_PORT ... differ` | Keep `PORT` and `EZTEST_WEB_PORT` equal so the API's CORS origins match the web UI, then restart. |
| Web opens but API calls fail | The API origin inlined in the web bundle must match this deployment (see section 4); the API must be healthy (`/api/health`). |
| `/login` returns 404 or pages have no CSS/JS | `web/.next/static` was not extracted/bundled; re-extract the complete archive. |
| `Permission denied` on Linux executables/Chromium | Start with `bash start-eztest.sh` (it restores executable bits) or `chmod +x` the reported file. |
| Windows SmartScreen/Defender warning on first start | The binaries are unsigned; allow the extracted directory. Large Chromium binaries may be scanned on first launch, which is slow. |
| Browser contexts fail with `context_limit` | Reduce concurrent runs or raise `EZTEST_EXECUTOR_MAX_CONTEXTS` (RAM permitting). |
| Remote target hosts blocked with `host_not_allowed` | Add the target host to the resource's allowed hosts **and** to `EZTEST_EXEC_ALLOWED_HOSTS` for WebSocket targets. |

## 8. Build metadata

`manifest.json` records the build (`version`, `commit`, `built_at`, `platform`,
`node`, `playwright`, `api`, `web`, `renderer` (playwright version and
directory), `postgres` required/bundled). `VERSION` is the version reported by
`GET /api/health`. Release assets also ship a `SHA256SUMS.txt`; verify an
archive with:

```powershell
Get-FileHash .\eztest-__VERSION__-windows-x64.zip -Algorithm SHA256
```
```bash
sha256sum -c SHA256SUMS.txt
```

## 9. Smoke test (9 checks)

`smoke-test` verifies a running package end to end:

1. PostgreSQL TCP reachable.
2. `GET /healthz` returns 200 with `status: ok`.
3. `GET /api/health` returns 200, `db: ok` and the version from `VERSION`.
4. PDF renderer `GET /healthz` returns 200 with `status: ok` and a browser
   version (proves the bundled Chromium launches for report rendering).
5. Admin login with the `.env` seed (`admin`/`admin123`) returns 200 and
   `GET /api/auth/me` returns the admin user.
6. Executor `GET /healthz` returns 200 with `status: ok` and a `browserVersion`.
7. Executor round-trip on the bundled web UI: create context, `goto
   <web>/login`, aria snapshot non-empty, PNG screenshot artifact, delete
   context; every action answers `ok: true`.
8. `GET /login` returns 200 and renders `EzTest`.
9. The `.data/` content directories (attachments, evidence, reports, exports)
   exist after start.

## 10. Security notes

- Never expose `13333/13336/13337` publicly; the executor and renderer are
  loopback-only by default and authenticated by shared Bearer tokens.
- The administrator seed `admin/admin123` is public knowledge: change the
  password immediately after the first login.
- The packages do not provide TLS, a reverse proxy, multi-tenancy or public
  object URLs. Put a TLS-terminating proxy in front of the web port if the UI
  is reachable beyond localhost, and keep PostgreSQL on a private interface.
- `.env` contains the database credentials and the executor/renderer tokens:
  keep it owner-only and out of shared backups.
- The release artifacts are unsigned; verify `SHA256SUMS.txt` from the release
  page before running them.
- API rate limits are per process; evidence GC is disabled by default.

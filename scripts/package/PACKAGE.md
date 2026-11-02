# EzTest package (`__VERSION__`)

A self-contained, Docker-free build of the EzTest AI end-to-end testing
platform: the Go API, the Playwright execution service and the Next.js web UI,
plus a bundled Node.js runtime and the pinned Chromium build the executor
launches. Only **PostgreSQL 17** is an external prerequisite.

| Content | Version |
|---|---|
| Package | `__VERSION__` |
| API | Go static binary (`bin/eztest.exe` / `bin/eztest`) |
| Web | Next.js standalone (`web/`) |
| Executor | Playwright `__PLAYWRIGHT_VERSION__` (`executor/`) |
| Browsers | Chromium `__CHROMIUM_VERSION__` (revision `__CHROMIUM_REVISION__`), headless shell, FFmpeg `__FFMPEG_REVISION__` |
| Node.js runtime | `__NODE_VERSION__` (LTS) |
| Database | PostgreSQL 17, **not bundled** (external) |

## Package layout

```
eztest-<version>-<os>-x64/
  VERSION                       package version (also reported by /api/health)
  manifest.json                 build metadata (version, commit, platform, versions)
  PACKAGE.md                    this file
  LICENSE                       EzTest license (Apache-2.0)
  THIRD_PARTY_NOTICES.md        bundled third-party components
  .env.example                  configuration template (init-config renders it)
  docs/openapi/api.yaml         served by GET /api/openapi.yaml
  bin/eztest(.exe)              Go API binary
  web/                          Next.js standalone server (server.js, .next/)
  executor/                     Playwright protocol service (src/, node_modules/)
  playwright-browsers/          pinned Chromium, headless shell and FFmpeg
  runtime/node/                 bundled Node.js runtime
  data/                         objects, version-attachments, evidence, reports, exports
  logs/, run/                   created on the first start (logs and pid files)
  init-config.ps1 / .sh         first-run configuration (creates .env, secrets)
  start-eztest.ps1 / .sh        starts the whole stack with health waits
  stop-eztest.ps1 / .sh         stops web -> executor (tree) -> API
  status-eztest.ps1 / .sh       table of processes, health probes and PostgreSQL
  smoke-test.ps1 / .sh          10 end-to-end checks (see below)
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

If a `pgsql/` directory exists in this package, a portable PostgreSQL 17 build
is bundled (Windows); see `pgsql/README-PORTABLE.md` if present. The smoke test
does **not** use it; it starts its own throw-away cluster on port 55432.

**Linux (Debian/Ubuntu)**

```bash
sudo apt install postgresql-17 postgresql-client-17
sudo systemctl start postgresql
sudo -u postgres psql -c "CREATE ROLE eztest LOGIN PASSWORD '<db-password>';"
sudo -u postgres psql -c "CREATE DATABASE eztest OWNER eztest;"
```

The API applies its embedded migrations and seeds the administrator on first
start, so an empty database is enough.

## 2. Quick start

**Windows (PowerShell 5.1+)**

```powershell
# 1. Extract the ZIP (tar.exe is faster and handles long paths):
tar -xf eztest-__VERSION__-windows-x64.zip
cd eztest-__VERSION__-windows-x64

# 2. Create .env with generated secrets and the database URL:
.\init-config.ps1 -DatabaseUrl "postgres://eztest:<db-password>@localhost:5432/eztest?sslmode=disable"
#    (or edit EZTEST_DATABASE_URL in .env afterwards)

# 3. Start the API, the executor and the web UI (waits for health):
.\start-eztest.ps1

# 4. Open http://localhost:6868 and sign in as admin.
#    The password is in .env (EZTEST_ADMIN_PASSWORD); it is never printed.

# Optional: verify the whole stack (10 checks):
.\smoke-test.ps1
```

**Linux (bash 4+, Debian/Ubuntu)**

```bash
# 1. Extract (tar preserves the directory tree; the scripts restore missing
#    executable bits automatically):
tar -xzf eztest-__VERSION__-linux-x64.tar.gz
cd eztest-__VERSION__-linux-x64

# 2. Install the Chromium system libraries once (root/sudo, network needed):
sudo bash install-linux-deps.sh

# 3. Create .env with generated secrets and the database URL:
bash init-config.sh --database-url "postgres://eztest:<db-password>@localhost:5432/eztest?sslmode=disable"

# 4. Start the API, the executor and the web UI (waits for health):
bash start-eztest.sh

# Optional: verify the whole stack (10 checks):
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
| OpenAPI document | <http://127.0.0.1:13333/api/openapi.yaml> |
| Executor health | <http://127.0.0.1:13336/healthz> |

| Command | What it does |
|---|---|
| `init-config` | Creates `.env` (secrets, absolute package paths, database URL). `-Force`/`--force` regenerates and backs up the old file. |
| `start-eztest` | Preflights PostgreSQL/ports, starts API -> executor -> web, waits for health, writes `run/*.pid` and `logs/*.log`. |
| `status-eztest` | Prints processes, the four health probes and PostgreSQL reachability. Exits 0 only when healthy. |
| `stop-eztest` | Stops web -> executor (whole process tree/group, Chromium included) -> API. Leaves PostgreSQL and `data/` untouched. |
| `smoke-test` | Runs the 10 checks listed in section 8 against the running stack. |

The administrator is `admin`; its password is generated by `init-config` and
stored only in `.env`. It is seeded on the first API start. Later password
changes go through the admin UI. Output is not secret-free by accident: the
scripts never print the password, tokens or keys.

## 4. Configuration (`.env`)

`init-config` renders `.env` from `.env.example`, replaces the package-root
placeholders with this package's absolute path and generates the secrets. The
most important keys:

| Key | Meaning |
|---|---|
| `EZTEST_DATABASE_URL` | **Required.** PostgreSQL 17 URL. Changing it needs an API restart only. |
| `EZTEST_HTTP_ADDR` | API bind address (default `127.0.0.1:13333`). |
| `EZTEST_WEB_ORIGIN` | Browser origin for CORS (default `http://localhost:6868`). |
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | First-boot administrator seed. |
| `EZTEST_SECRET_KEYS` / `EZTEST_SECRET_KEY_ACTIVE` | AES-256 keyring for stored LLM credentials. Back this up separately. |
| `EZTEST_EXECUTOR_URL` / `EZTEST_EXECUTOR_TOKEN` | API -> executor URL and the shared Bearer token. |
| `EZTEST_EXECUTOR_ADDR` / `EZTEST_EXECUTOR_MAX_CONTEXTS` | Executor bind address and concurrency (default 8; each busy context needs ~0.5-1 GB RAM). |
| `EZTEST_EXEC_ALLOWED_HOSTS` | Executor-wide WebSocket allow list; add remote test-target hosts explicitly. |
| `EZTEST_VISUAL_BASELINE_MAX_BYTES` | Visual baseline request cap (API + executor). |
| `EZTEST_OPENAPI_PATH` + the five content directories | Absolute paths inside this package, written by `init-config`. |
| `PORT` / `HOSTNAME` | Web server port/bind (default `6868` / `127.0.0.1`). |
| `EZTEST_GC_ENABLED` | Evidence GC; `false` keeps maintenance dry-run. |

Notes:

- The web bundle's API origin (`__API_BASE_URL__`) is **inlined at build time**.
  Changing the API host/port requires rebuilding the web bundle; changing the
  API bind address alone is not enough for a remote browser. For another
  machine to reach both services, bind `EZTEST_HTTP_ADDR`/`HOSTNAME` to a
  reachable interface and put a reverse proxy in front (see section 9).
- Changing only the web port (`PORT`) is fine; update `EZTEST_WEB_ORIGIN` so
  CORS matches the new browser origin.
- Keep `.env` private (`chmod 600` / owner-only ACL; `init-config` sets it).
  Losing `EZTEST_SECRET_KEYS` makes stored LLM keys unrecoverable.

## 5. Linux browser dependencies

The executor starts Chromium headlessly. On a minimal Debian/Ubuntu install the
libraries are missing and the executor `/healthz` reports `503 degraded` with a
browser launch error (smoke check 7 then fails loudly). Install them once:

```bash
sudo bash install-linux-deps.sh
```

The script asks Playwright itself (`playwright install-deps chromium`) for the
authoritative package list, adds fonts/Xvfb, falls back to the documented
Ubuntu 24.04 / Debian 12 apt list, and finally verifies the bundled Chromium
has no missing shared libraries (`ldd`). Windows needs no extra packages.

## 6. Data, backup and upgrades

- User content lives in `data/` (objects, version attachments, execution
  evidence, reports, exports). PostgreSQL holds the relational data.
- **Back up PostgreSQL and `data/` together**; neither is complete alone.
  `pg_dump`/`pg_restore` are not shipped unless a `pgsql/` directory is present;
  use the system PostgreSQL client tools of the same major version.
- Stop the stack (`stop-eztest`) before copying `data/` for a consistent
  snapshot.
- Restore/upgrade: stop, replace the package directory with the new version but
  keep `data/` and `.env` (absolute paths are this package's path, so keep the
  directory name/location identical), then start and run `smoke-test`.
- Upgrading the package does **not** migrate a database backward; PostgreSQL
  migrations are forward-only.

## 7. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `Missing .env; run init-config` | Run `init-config.ps1` / `bash init-config.sh` first. |
| `PostgreSQL is not reachable at host:port` | PostgreSQL 17 is not running, the port/host in `EZTEST_DATABASE_URL` is wrong, or `pg_hba.conf` rejects the user. Start it and test with `psql "<url>"`. |
| `Port 13333/6868/13336 is already in use` | Another process (often an older run or a dev stack) owns the port. Stop it with the matching package `stop` script or change the port in `.env`. |
| `EzTest is already running (pid N)` | A previous start is still up; run `status-eztest` and then `stop-eztest`. |
| API exits during startup | Read `logs/api.err.log`: most often a bad `EZTEST_DATABASE_URL`, a wrong keyring (`EZTEST_SECRET_KEYS`), or a port conflict. |
| `executor ... degraded` / HTTP 503 | Chromium cannot launch. Linux: run `sudo bash install-linux-deps.sh`. Windows: no extra deps should be needed; check `logs/executor.err.log` and available RAM. |
| Web opens but API calls fail | Check `EZTEST_WEB_ORIGIN` matches the origin in the address bar, the API is healthy (`/api/health`), and the API origin inlined in the web bundle matches this deployment (see section 4). |
| `/login` returns 404 or pages have no CSS/JS | `web/.next/static` was not extracted/bundled; re-extract the complete archive. |
| `Permission denied` on Linux executables/Chromium | Start with `bash start-eztest.sh` (it restores executable bits) or `chmod +x` the reported file. |
| Windows SmartScreen/Defender warning on first start | The binaries are unsigned; allow the extracted directory. Large Chromium binaries may be scanned on first launch, which is slow. |
| Browser contexts fail with `context_limit` | Reduce concurrent runs or raise `EZTEST_EXECUTOR_MAX_CONTEXTS` (RAM permitting). |
| Remote target hosts blocked with `host_not_allowed` | Add the target host to the resource's allowed hosts **and** to `EZTEST_EXEC_ALLOWED_HOSTS` for WebSocket targets. |

## 8. Smoke test (10 checks)

`smoke-test` verifies a running package end to end:

1. PostgreSQL TCP reachable.
2. `GET /healthz` returns 200 with `status: ok`.
3. `GET /api/health` returns 200, `db: ok` and the version from `VERSION`.
4. `GET /api/openapi.yaml` returns 200, `Content-Type: application/yaml`, > 100 kB.
5. `GET /api/versions` without a token returns 401.
6. Admin login returns 200 and `GET /api/auth/me` returns the admin user.
7. Executor `GET /healthz` returns 200 with `status: ok` and a `browserVersion`
   (proves the bundled Chromium launches).
8. Executor round-trip on the bundled web UI: create context, `goto
   <web>/login`, aria snapshot non-empty, PNG screenshot artifact, delete
   context; every action answers `ok: true`.
9. `GET /login` returns 200 and renders `EzTest`.
10. The five `data/` directories exist after start.

## 9. Security notes

- Never expose `13333/13336` publicly; the executor is loopback-only by
  default and authenticated by a shared Bearer token.
- The packages do not provide TLS, a reverse proxy, multi-tenancy or public
  object URLs. Put a TLS-terminating proxy in front of the web port if the UI
  is reachable beyond localhost, and keep PostgreSQL on a private interface.
- `.env` contains secrets: keep it owner-only and out of backups that are
  shared (back up `EZTEST_SECRET_KEYS` separately).
- The release artifacts are unsigned; verify `SHA256SUMS.txt` from the release
  page before running them.
- API rate limits are per process; evidence GC is disabled by default.

## 10. Build metadata

`manifest.json` records the build (`version`, `commit`, `built_at`, `platform`,
`playwright`, `node`, `postgres_required`, `postgres_bundled`). `VERSION` is the
version reported by `GET /api/health`. Release assets also ship a
`SHA256SUMS.txt`; verify an archive with:

```powershell
Get-FileHash .\eztest-__VERSION__-windows-x64.zip -Algorithm SHA256
```
```bash
sha256sum -c SHA256SUMS.txt
```

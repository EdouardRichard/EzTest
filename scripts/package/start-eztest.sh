#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# Starts the EzTest stack of an extracted Linux package.
#
# Starts, in order: the Go API (bin/eztest), the Playwright executor
# (runtime/node/bin/node executor/src/server.mjs) and the Next.js web server
# (runtime/node/bin/node web/server.js). Each process gets a pid file under
# run/ and a log under logs/; startup waits for the documented health checks
# (API /healthz + /api/health db=ok, executor /healthz with a browserVersion,
# web /login) before reporting success.
#
# Before starting anything the script refuses to run when:
#   - .env is missing (run init-config.sh first),
#   - a required package file or the OpenAPI document is missing,
#   - EZTEST_DATABASE_URL is unset/placeholder or PostgreSQL is not reachable
#     over TCP,
#   - one of the packaged ports (13333/6868/13336 by default) is in use,
#   - a pid recorded in run/ belongs to a live process (use stop first).
#
# Processes are started with setsid so stop-eztest.sh can kill whole process
# groups (Chromium children of the executor included). If any component fails
# to become healthy the already-started processes are stopped again and a
# non-zero exit code is returned. Secrets are never printed.
#
# Works on Debian/Ubuntu bash 4+. No curl/jq needed: HTTP probes use the
# bundled Node runtime.
# ---------------------------------------------------------------------------
set -u

PKG="$(cd "$(dirname "$0")" && pwd)"
cd "$PKG" || { printf 'ERROR: cannot enter %s\n' "$PKG" >&2; exit 1; }

ENV_FILE="$PKG/.env"
BIN="$PKG/bin/eztest"
NODE="$PKG/runtime/node/bin/node"
WEB_SERVER="$PKG/web/server.js"
EXEC_SERVER="$PKG/executor/src/server.mjs"
OPENAPI="$PKG/docs/openapi/api.yaml"
BROWSERS="$PKG/playwright-browsers"
LOGS="$PKG/logs"
RUN="$PKG/run"
DATA="$PKG/data"

STARTED=()
LAST_PID=""
WAIT_LAST=""
API_HEALTH_BODY=""
EXEC_BROWSER=""

fail() {
    printf 'ERROR: %s\n' "$1" >&2
    exit 1
}

info() { printf '%s\n' "$1"; }
ok() { printf ' %s\n' "$1"; }

# ---------------------------------------------------------------------------
# .env parsing (NOT `source`: values like {"1":"base64=="} must stay literal).
# ---------------------------------------------------------------------------
load_env() {
    local line trimmed key value
    while IFS= read -r line || [ -n "$line" ]; do
        line="${line%$'\r'}"
        trimmed="$(printf '%s' "$line" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
        [ -z "$trimmed" ] && continue
        case "$trimmed" in \#*) continue ;; esac
        key="${trimmed%%=*}"
        [ "$key" = "$trimmed" ] && continue
        key="$(printf '%s' "$key" | tr -d '[:space:]')"
        case "$key" in
            [!A-Za-z_]*|*[!A-Za-z0-9_]*) continue ;;
        esac
        value="${trimmed#*=}"
        case "$value" in
            \"*\") value="${value#\"}"; value="${value%\"}" ;;
            \'*\') value="${value#\'}"; value="${value%\'}" ;;
        esac
        export "$key=$value"
    done < "$ENV_FILE"
}

env_value() {
    local value
    value="$(printenv "$1" 2>/dev/null || true)"
    if [ -z "$value" ]; then printf '%s' "${2-}"; else printf '%s' "$value"; fi
}

# ---------------------------------------------------------------------------
# Small network helpers.
# ---------------------------------------------------------------------------
probe_host() {
    case "$1" in
        ""|0.0.0.0|::|\*) printf '127.0.0.1' ;;
        *) printf '%s' "$1" ;;
    esac
}

split_host_port() { # host:port -> "host port"; second arg is the default port
    local raw="$1" default_port="$2" host port
    if [ -z "$raw" ]; then
        printf '127.0.0.1 %s' "$default_port"
        return 0
    fi
    case "$raw" in
        *[!0-9]*) : ;;
        *) printf '127.0.0.1 %s' "$raw"; return 0 ;;
    esac
    if [ "${raw#*:}" = "$raw" ]; then
        printf '%s %s' "$raw" "$default_port"
        return 0
    fi
    host="${raw%:*}"
    port="${raw##*:}"
    host="${host#[}"; host="${host%]}"
    [ -z "$host" ] && host="0.0.0.0"
    case "$port" in
        ''|*[!0-9]*) printf 'ERROR: invalid port in "%s"\n' "$raw" >&2; return 1 ;;
    esac
    printf '%s %s' "$host" "$port"
}

db_host_port() { # postgres URL -> "host port"
    local rest="$1" host port
    rest="${rest#*://}"
    rest="${rest%%/*}"
    rest="${rest##*@}"
    case "$rest" in
        \[*\]:*)
            host="${rest%%]*}"; host="${host#[}"
            port="${rest##*:}"
            ;;
        *:*)
            host="${rest%:*}"
            port="${rest##*:}"
            case "$port" in ''|*[!0-9]*) host="$rest"; port="5432" ;; esac
            ;;
        *)
            host="$rest"
            port="5432"
            ;;
    esac
    printf '%s %s' "$host" "$port"
}

tcp_open() { # host port -> 0 when a TCP connection succeeds
    timeout 3 bash -c "exec 3<>/dev/tcp/${1}/${2}" >/dev/null 2>&1
}

# HTTP helper in Node (the package always ships it; no curl/jq dependency).
NODE_HTTP=$(cat <<'NODEJS'
const http = require("http")
const url = process.argv[1]
const method = (process.argv[2] || "GET").toUpperCase()
const headersJson = process.argv[3] || ""
const bodyJson = process.argv[4] || ""
let headers = {}
if (headersJson) { try { headers = JSON.parse(headersJson) } catch { process.exit(5) } }
let body = null
if (bodyJson) {
  body = Buffer.from(bodyJson, "utf8")
  headers["content-type"] = headers["content-type"] || "application/json"
  headers["content-length"] = String(body.length)
}
const request = http.request(url, { method, headers, timeout: 15000 }, (response) => {
  const chunks = []
  response.on("data", (chunk) => chunks.push(chunk))
  response.on("end", () => {
    process.stdout.write(String(response.statusCode) + "\n")
    process.stdout.write(Buffer.concat(chunks))
  })
})
request.on("error", () => process.exit(3))
request.on("timeout", () => { request.destroy(); process.exit(4) })
if (body) request.write(body)
request.end()
NODEJS
)

http_req() { # url [method] [headers-json] [body-json] -> "status\nbody"
    "$NODE" -e "$NODE_HTTP" "$1" "${2:-GET}" "${3:-}" "${4:-}"
}

json_field() { # field.path (reads JSON from stdin)
    local path="$1"
    "$NODE" -e '
let data = ""
process.stdin.on("data", (chunk) => { data += chunk }).on("end", () => {
  let value
  try { value = JSON.parse(data) } catch { process.exit(2) }
  for (const key of process.argv[1].split(".")) {
    if (value === null || value === undefined) process.exit(1)
    value = value[key]
  }
  if (value === null || value === undefined) process.exit(1)
  process.stdout.write(typeof value === "object" ? JSON.stringify(value) : String(value))
})
' "$path"
}

log_hint() {
    local name="$1" output="" file
    for file in "$LOGS/$name.err.log" "$LOGS/$name.log"; do
        [ -f "$file" ] || continue
        output="$output$(tail -n 10 "$file" 2>/dev/null | sed -e 's/[A-Za-z0-9+/=]\{40,\}/<redacted>/g' -e 's/^/  /')"$'\n'
    done
    if [ -z "$output" ]; then
        printf '  No log output was written; see the logs/ directory.'
    else
        printf 'Recent log lines:\n%s' "$output"
    fi
}

rotate_log() {
    [ -f "$1" ] && mv -f "$1" "$1.prev"
    return 0
}

# ---------------------------------------------------------------------------
# Process management (setsid gives every service its own process group).
# ---------------------------------------------------------------------------
start_detached() { # name workdir stdout-log stderr-log command...
    local name="$1" workdir="$2" out_log="$3" err_log="$4"
    shift 4
    rotate_log "$out_log"
    rotate_log "$err_log"
    (
        cd "$workdir" || exit 90
        if command -v setsid >/dev/null 2>&1; then
            exec setsid "$@"
        else
            exec "$@"
        fi
    ) >>"$out_log" 2>>"$err_log" &
    LAST_PID=$!
    printf '%s\n' "$LAST_PID" > "$RUN/$name.pid"
    STARTED+=("$name:$LAST_PID")
}

process_alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null; }

stop_tree() { # graceful group TERM, then KILL
    local pid="$1" i
    process_alive "$pid" || return 0
    if ! kill -TERM "-$pid" 2>/dev/null; then kill -TERM "$pid" 2>/dev/null || true; fi
    for i in $(seq 1 50); do
        process_alive "$pid" || return 0
        sleep 0.2
    done
    if ! kill -KILL "-$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
    return 0
}

rollback() {
    local i entry name pid
    for ((i = ${#STARTED[@]} - 1; i >= 0; i--)); do
        entry="${STARTED[$i]}"
        name="${entry%%:*}"
        pid="${entry##*:}"
        if process_alive "$pid"; then
            printf 'Stopping %s (pid %s)...\n' "$name" "$pid" >&2
            stop_tree "$pid"
        fi
        rm -f "$RUN/$name.pid"
    done
    STARTED=()
}

fail_with_rollback() {
    printf 'ERROR: %s\n' "$1" >&2
    rollback
    exit 1
}

# ---------------------------------------------------------------------------
# Health waits (the HTTP probes run the bundled Node runtime).
# ---------------------------------------------------------------------------
wait_api_ready() { # base-url pid seconds
    local base="$1" pid="$2" seconds="${3:-60}" last="no response" response health body db
    local deadline=$((SECONDS + seconds))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ! process_alive "$pid"; then
            WAIT_LAST="The API exited during startup.
$(log_hint api)"
            return 1
        fi
        if response="$(http_req "$base/healthz" GET "" "" 2>/dev/null)" && [ "${response%%$'\n'*}" = "200" ]; then
            if health="$(http_req "$base/api/health" GET "" "" 2>/dev/null)" && [ "${health%%$'\n'*}" = "200" ]; then
                body="${health#*$'\n'}"
                db="$(printf '%s' "$body" | json_field 'data.db' 2>/dev/null || true)"
                if [ "$db" = "ok" ]; then
                    API_HEALTH_BODY="$body"
                    return 0
                fi
                last="db=$db"
            else
                last="/api/health did not answer 200"
            fi
        else
            last="/healthz did not answer 200"
        fi
        sleep 0.5
    done
    WAIT_LAST="The API did not become healthy within ${seconds}s (last: ${last}).
$(log_hint api)"
    return 1
}

wait_executor_ready() { # base-url pid seconds
    local base="$1" pid="$2" seconds="${3:-90}" last="no response" response body status browser status_code
    local deadline=$((SECONDS + seconds))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ! process_alive "$pid"; then
            WAIT_LAST="The executor exited during startup.
$(log_hint executor)"
            return 1
        fi
        if response="$(http_req "$base/healthz" GET "" "" 2>/dev/null)"; then
            status_code="${response%%$'\n'*}"
            body="${response#*$'\n'}"
            if [ "$status_code" = "200" ]; then
                status="$(printf '%s' "$body" | json_field 'status' 2>/dev/null || true)"
                browser="$(printf '%s' "$body" | json_field 'browserVersion' 2>/dev/null || true)"
                if [ "$status" = "ok" ] && [ -n "$browser" ]; then
                    EXEC_BROWSER="$browser"
                    return 0
                fi
                last="status=${status} browserVersion=${browser}"
            else
                last="HTTP $status_code $body"
            fi
        else
            last="connection not ready"
        fi
        sleep 0.5
    done
    WAIT_LAST="The Playwright executor did not become ready within ${seconds}s (last: ${last}).
Bundled Chromium could not be launched. On Debian/Ubuntu install the browser system libraries:
  sudo bash install-linux-deps.sh
See PACKAGE.md troubleshooting and logs/executor.err.log.
$(log_hint executor)"
    return 1
}

wait_web_ready() { # base-url pid seconds
    local base="$1" pid="$2" seconds="${3:-60}" last="no response" response
    local deadline=$((SECONDS + seconds))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ! process_alive "$pid"; then
            WAIT_LAST="The web server exited during startup.
$(log_hint web)"
            return 1
        fi
        if response="$(http_req "$base/login" GET "" "" 2>/dev/null)" && [ "${response%%$'\n'*}" = "200" ]; then
            return 0
        fi
        last="connection not ready"
        sleep 0.5
    done
    WAIT_LAST="The web server did not answer /login within ${seconds}s (last: ${last}).
$(log_hint web)"
    return 1
}

# ---------------------------------------------------------------------------
# Permissions: a Windows-created tar loses POSIX executable bits, so make the
# package self-healing (documented in PACKAGE.md: run with `bash start-eztest.sh`).
# ---------------------------------------------------------------------------
prepare_permissions() {
    chmod +x "$NODE" "$BIN" 2>/dev/null || true
    chmod +x "$PKG"/*.sh 2>/dev/null || true
    find "$PKG" -type d -exec chmod a+rx {} + 2>/dev/null || true
    local browser_dir
    for browser_dir in "$BROWSERS"/chromium* "$BROWSERS"/ffmpeg-*; do
        [ -d "$browser_dir" ] || continue
        find "$browser_dir" -type f \( -name chrome -o -name chrome-headless-shell -o -name chrome_crashpad_handler -o -name chrome_sandbox -o -name 'ffmpeg*' \) -exec chmod a+x {} + 2>/dev/null || true
    done
}

# ---------------------------------------------------------------------------
# Start.
# ---------------------------------------------------------------------------
[ -f "$ENV_FILE" ] || fail "Missing $ENV_FILE. Run 'bash init-config.sh' first (it creates .env and generates the secrets)."

load_env

require_file() { [ -e "$1" ] || fail "$2"; }
require_file "$BIN" "The API binary is missing (bin/eztest). Extract the complete package archive."
require_file "$NODE" "The bundled Node runtime is missing. Extract the complete package archive."
require_file "$WEB_SERVER" "web/server.js is missing. Extract the complete package archive."
require_file "$EXEC_SERVER" "executor/src/server.mjs is missing. Extract the complete package archive."
require_file "$OPENAPI" "docs/openapi/api.yaml is missing; it is required by GET /api/openapi.yaml."
require_file "$BROWSERS" "playwright-browsers/ is missing. Extract the complete package archive."

prepare_permissions
mkdir -p "$LOGS" "$RUN"
for directory in objects version-attachments evidence reports exports; do
    mkdir -p "$DATA/$directory"
done

# Absolute package paths in the environment (repair hand-written relative values).
resolve_pkg_path() { # value default-relative [--create]
    local value="$1" default_relative="$2" create="${3:-}"
    [ -z "$value" ] && value="$default_relative"
    case "$value" in
        /*) : ;;
        *) value="$PKG/$value" ;;
    esac
    if [ "$create" = "create" ]; then mkdir -p "$value"; fi
    printf '%s' "$value"
}
export EZTEST_OPENAPI_PATH="$(resolve_pkg_path "$(env_value EZTEST_OPENAPI_PATH 'docs/openapi/api.yaml')" 'docs/openapi/api.yaml')"
export EZTEST_STORAGE_DIR="$(resolve_pkg_path "$(env_value EZTEST_STORAGE_DIR '')" 'data/objects' create)"
export EZTEST_VERSION_ATTACHMENT_DIR="$(resolve_pkg_path "$(env_value EZTEST_VERSION_ATTACHMENT_DIR '')" 'data/version-attachments' create)"
export EZTEST_EVIDENCE_DIR="$(resolve_pkg_path "$(env_value EZTEST_EVIDENCE_DIR '')" 'data/evidence' create)"
export EZTEST_REPORT_DIR="$(resolve_pkg_path "$(env_value EZTEST_REPORT_DIR '')" 'data/reports' create)"
export EZTEST_EXPORT_DIR="$(resolve_pkg_path "$(env_value EZTEST_EXPORT_DIR '')" 'data/exports' create)"
export PLAYWRIGHT_BROWSERS_PATH="$(resolve_pkg_path "$(env_value PLAYWRIGHT_BROWSERS_PATH '')" 'playwright-browsers')"

api_addr="$(env_value EZTEST_HTTP_ADDR '127.0.0.1:13333')"
read -r api_host api_port <<<"$(split_host_port "$api_addr" 13333)" || fail "Invalid EZTEST_HTTP_ADDR: $api_addr"
exec_addr="$(env_value EZTEST_EXECUTOR_ADDR '127.0.0.1:13336')"
read -r exec_host exec_port <<<"$(split_host_port "$exec_addr" 13336)" || fail "Invalid EZTEST_EXECUTOR_ADDR: $exec_addr"
web_port="$(env_value PORT '6868')"
case "$web_port" in ''|*[!0-9]*) fail "PORT in .env must be a number (got '$web_port')." ;; esac
web_host="$(env_value HOSTNAME '127.0.0.1')"
api_base="http://$(probe_host "$api_host"):$api_port"
exec_base="http://$(probe_host "$exec_host"):$exec_port"
web_base="http://$(probe_host "$web_host"):$web_port"

# Database preflight.
database_url="$(env_value EZTEST_DATABASE_URL '')"
case "$database_url" in
    ""|*"<"*) fail "EZTEST_DATABASE_URL is not configured in .env. Set it to a PostgreSQL 17 URL, for example:
  postgres://eztest:<password>@localhost:5433/eztest?sslmode=disable" ;;
    postgres://*|postgresql://*) : ;;
    *) fail "EZTEST_DATABASE_URL must start with postgres:// or postgresql://." ;;
esac
read -r db_host db_port <<<"$(db_host_port "$database_url")"
if ! tcp_open "$db_host" "$db_port"; then
    fail "PostgreSQL is not reachable at ${db_host}:${db_port} (from EZTEST_DATABASE_URL).

Install/start PostgreSQL 17 and make sure the database and user exist, for example:
  Debian/Ubuntu: sudo apt install postgresql-17 && sudo systemctl start postgresql
  Then create the role/database and update EZTEST_DATABASE_URL in .env.
See PACKAGE.md (\"PostgreSQL 17\") for the exact commands."
fi
executor_token="$(env_value EZTEST_EXECUTOR_TOKEN '')"
[ -n "$executor_token" ] || fail "EZTEST_EXECUTOR_TOKEN is empty in .env. Re-run 'bash init-config.sh --force' or set it manually."

# Already-running and port checks.
for component in api executor web; do
    pid_file="$RUN/$component.pid"
    if [ -f "$pid_file" ]; then
        recorded_pid="$(cat "$pid_file" 2>/dev/null || true)"
        case "$recorded_pid" in
            ''|*[!0-9]*) recorded_pid="" ;;
        esac
        if [ -n "$recorded_pid" ] && process_alive "$recorded_pid"; then
            fail "EzTest is already running ($component pid $recorded_pid). Run 'bash stop-eztest.sh' first, or 'bash status-eztest.sh' to inspect it."
        fi
        rm -f "$pid_file"
    fi
done
check_port_free() { # label host port
    if tcp_open "$2" "$3"; then
        fail "Port $3 is already in use ($1). Stop the process using it or change the port in .env."
    fi
}
check_port_free "API (EZTEST_HTTP_ADDR)" "$(probe_host "$api_host")" "$api_port"
check_port_free "executor (EZTEST_EXECUTOR_ADDR)" "$(probe_host "$exec_host")" "$exec_port"
check_port_free "web (PORT)" "$(probe_host "$web_host")" "$web_port"

# Warn about a world-readable .env without printing it.
if command -v stat >/dev/null 2>&1; then
    env_mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || true)"
    case "$env_mode" in
        600|400) : ;;
        "") : ;;
        *) printf 'WARNING: %s has permissions %s; restrict it with: chmod 600 %s\n' "$ENV_FILE" "$env_mode" "$ENV_FILE" >&2 ;;
    esac
fi

info "Starting EzTest from $PKG"
info "  API      : $api_base"
info "  executor : $exec_base"
info "  web      : $web_base"
info "  database : ${db_host}:${db_port} (credentials from .env)"
printf '\n'

# --- API -------------------------------------------------------------------
printf '[1/3] API ...'
start_detached api "$PKG" "$LOGS/api.log" "$LOGS/api.err.log" "$BIN"
api_pid=$LAST_PID
if ! wait_api_ready "$api_base" "$api_pid" 60; then
    fail_with_rollback "$WAIT_LAST"
fi
api_version="$(printf '%s' "$API_HEALTH_BODY" | json_field 'data.version' 2>/dev/null || true)"
ok "ready (version ${api_version:-unknown})"

# --- executor --------------------------------------------------------------
printf '[2/3] Playwright executor ...'
export EZTEST_EXECUTOR_TOKEN="$executor_token"
export EZTEST_EXECUTOR_ADDR="$exec_addr"
export EZTEST_EXECUTOR_MAX_CONTEXTS="$(env_value EZTEST_EXECUTOR_MAX_CONTEXTS '8')"
export EZTEST_EXEC_ALLOWED_HOSTS="$(env_value EZTEST_EXEC_ALLOWED_HOSTS 'localhost,127.0.0.1')"
export EZTEST_VISUAL_BASELINE_MAX_BYTES="$(env_value EZTEST_VISUAL_BASELINE_MAX_BYTES '5242880')"
start_detached executor "$PKG" "$LOGS/executor.log" "$LOGS/executor.err.log" "$NODE" "$EXEC_SERVER"
exec_pid=$LAST_PID
if ! wait_executor_ready "$exec_base" "$exec_pid" 90; then
    fail_with_rollback "$WAIT_LAST"
fi
ok "ready (browser $EXEC_BROWSER)"

# --- web -------------------------------------------------------------------
printf '[3/3] web ...'
export NODE_ENV="production"
export PORT="$web_port"
export HOSTNAME="$web_host"
start_detached web "$PKG/web" "$LOGS/web.log" "$LOGS/web.err.log" "$NODE" "$WEB_SERVER"
web_pid=$LAST_PID
if ! wait_web_ready "$web_base" "$web_pid" 60; then
    fail_with_rollback "$WAIT_LAST"
fi
ok "ready"

printf '\n'
printf 'EzTest is running.\n'
printf '  Web UI        : http://localhost:%s\n' "$web_port"
printf '  API health    : %s/api/health\n' "$api_base"
printf '  API liveness  : %s/healthz\n' "$api_base"
printf '  OpenAPI       : %s/api/openapi.yaml\n' "$api_base"
printf '  Executor      : %s/healthz\n' "$exec_base"
printf '  Administrator : admin (password is in .env - never printed)\n'
printf '\n'
printf '  Logs          : %s\n' "$LOGS"
printf '  Pid files     : %s\n' "$RUN"
printf '  Status/stop   : bash status-eztest.sh / bash stop-eztest.sh\n'
exit 0

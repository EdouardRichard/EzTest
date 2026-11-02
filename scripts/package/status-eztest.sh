#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# Shows the status of an extracted EzTest package (Linux).
#
# Reports the API/executor/web/renderer pid files, the five HTTP health probes
# (API /healthz, API /api/health with db=ok, executor /healthz with a browser
# version, renderer /healthz with a browser version, web /login) and the
# PostgreSQL TCP reachability from EZTEST_DATABASE_URL as a table. Exits 0 when
# everything is healthy, 1 when a component is stopped or degraded.
#
# No curl/jq needed: HTTP probes use the bundled Node runtime.
# ---------------------------------------------------------------------------
set -u

PKG="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="$PKG/.env"
NODE="$PKG/runtime/node/bin/node"
LOGS="$PKG/logs"
RUN="$PKG/run"

fail() {
    printf 'ERROR: %s\n' "$1" >&2
    exit 2
}

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

probe_host() {
    case "$1" in
        ""|0.0.0.0|::|\*) printf '127.0.0.1' ;;
        *) printf '%s' "$1" ;;
    esac
}

split_host_port() {
    local raw="$1" default_port="$2" host port
    if [ -z "$raw" ]; then printf '127.0.0.1 %s' "$default_port"; return 0; fi
    case "$raw" in *[!0-9]*) : ;; *) printf '127.0.0.1 %s' "$raw"; return 0 ;; esac
    if [ "${raw#*:}" = "$raw" ]; then printf '%s %s' "$raw" "$default_port"; return 0; fi
    host="${raw%:*}"; port="${raw##*:}"
    host="${host#[}"; host="${host%]}"
    [ -z "$host" ] && host="0.0.0.0"
    case "$port" in ''|*[!0-9]*) port="$default_port" ;; esac
    printf '%s %s' "$host" "$port"
}

db_host_port() {
    local rest="$1" host port
    rest="${rest#*://}"; rest="${rest%%/*}"; rest="${rest##*@}"
    case "$rest" in
        \[*\]:*)
            host="${rest%%]*}"; host="${host#[}"; port="${rest##*:}"
            ;;
        *:*)
            host="${rest%:*}"; port="${rest##*:}"
            case "$port" in ''|*[!0-9]*) host="$rest"; port="5432" ;; esac
            ;;
        *) host="$rest"; port="5432" ;;
    esac
    printf '%s %s' "$host" "$port"
}

tcp_open() { timeout 3 bash -c "exec 3<>/dev/tcp/${1}/${2}" >/dev/null 2>&1; }
process_alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null; }

NODE_HTTP=$(cat <<'NODEJS'
const http = require("http")
const url = process.argv[1]
const request = http.get(url, { timeout: 15000 }, (response) => {
  const chunks = []
  response.on("data", (chunk) => chunks.push(chunk))
  response.on("end", () => {
    process.stdout.write(String(response.statusCode) + "\n")
    process.stdout.write(Buffer.concat(chunks))
  })
})
request.on("error", () => process.exit(3))
request.on("timeout", () => { request.destroy(); process.exit(4) })
NODEJS
)

http_req() {
    "$NODE" -e "$NODE_HTTP" "$1"
}

json_field() {
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
' "$1"
}

row() { # component state detail
    printf '%-20s %-10s %s\n' "$1" "$2" "$3"
}

[ -f "$ENV_FILE" ] || fail "$ENV_FILE not found. Run 'bash init-config.sh' first."
[ -f "$NODE" ] || fail "The bundled Node runtime is missing. Extract the complete package archive."
# A Windows-created tar loses executable bits; init-config/start restore them, but
# make `status` work standalone too (it uses the bundled Node for HTTP probes).
chmod +x "$NODE" 2>/dev/null || true
load_env

api_port="$(env_value EZTEST_API_PORT 13333)"
case "$api_port" in ''|*[!0-9]*) api_port=13333 ;; esac
read -r exec_host exec_port <<<"$(split_host_port "$(env_value EZTEST_EXECUTOR_ADDR '127.0.0.1:13336')" 13336)"
read -r render_host render_port <<<"$(split_host_port "$(env_value EZTEST_RENDERER_ADDR '127.0.0.1:13337')" 13337)"
web_port="$(env_value PORT '6868')"
case "$web_port" in ''|*[!0-9]*) web_port=6868 ;; esac
web_host="$(env_value HOSTNAME '127.0.0.1')"
api_base="http://127.0.0.1:$api_port"
exec_base="http://$(probe_host "$exec_host"):$exec_port"
render_base="http://$(probe_host "$render_host"):$render_port"
web_base="http://$(probe_host "$web_host"):$web_port"

package_version=""
[ -f "$PKG/VERSION" ] && package_version="$(head -n 1 "$PKG/VERSION" | tr -d '\r')"

failures=0

printf '\nEzTest package status\n'
if [ -n "$package_version" ]; then printf '  version : %s\n' "$package_version"; fi
printf '  package : %s\n' "$PKG"
printf '\n'
row "Component" "State" "Detail"
row "---------" "-----" "------"

# Processes.
for component in api executor web renderer; do
    pid_file="$RUN/$component.pid"
    pid=""
    [ -f "$pid_file" ] && pid="$(cat "$pid_file" 2>/dev/null || true)"
    case "$pid" in ''|*[!0-9]*) pid="" ;; esac
    if [ -n "$pid" ] && process_alive "$pid"; then
        row "$component process" "RUNNING" "pid $pid"
    elif [ -n "$pid" ]; then
        row "$component process" "STOPPED" "stale pid file (pid $pid)"
        failures=$((failures + 1))
    else
        row "$component process" "STOPPED" "no pid file"
        failures=$((failures + 1))
    fi
done

# API probes.
api_health=""
if response="$(http_req "$api_base/healthz" 2>/dev/null)"; then
    status="${response%%$'\n'*}"
    if [ "$status" = "200" ]; then row "API /healthz" "OK" "200"; else row "API /healthz" "DEGRADED" "HTTP $status"; failures=$((failures + 1)); fi
else
    row "API /healthz" "UNREACHABLE" "$api_base"
    failures=$((failures + 1))
fi
if response="$(http_req "$api_base/api/health" 2>/dev/null)"; then
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    db="$(printf '%s' "$body" | json_field 'data.db' 2>/dev/null || true)"
    if [ "$status" = "200" ] && [ "$db" = "ok" ]; then
        version="$(printf '%s' "$body" | json_field 'data.version' 2>/dev/null || true)"
        commit="$(printf '%s' "$body" | json_field 'data.commit' 2>/dev/null || true)"
        detail="db=ok version=$version"
        [ -n "$commit" ] && detail="$detail commit=$commit"
        if [ -n "$package_version" ] && [ "$version" != "$package_version" ]; then
            detail="$detail (VERSION file says $package_version)"
        fi
        row "API /api/health" "OK" "$detail"
    else
        row "API /api/health" "DEGRADED" "HTTP $status db=$db"
        failures=$((failures + 1))
    fi
else
    row "API /api/health" "UNREACHABLE" "$api_base"
    failures=$((failures + 1))
fi

# Executor probe.
if response="$(http_req "$exec_base/healthz" 2>/dev/null)"; then
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    exec_status="$(printf '%s' "$body" | json_field 'status' 2>/dev/null || true)"
    browser="$(printf '%s' "$body" | json_field 'browserVersion' 2>/dev/null || true)"
    error_text="$(printf '%s' "$body" | json_field 'error' 2>/dev/null || true)"
    if [ "$status" = "200" ] && [ "$exec_status" = "ok" ]; then
        row "executor /healthz" "OK" "browser=$browser"
    else
        row "executor /healthz" "DEGRADED" "HTTP $status status=$exec_status ${error_text}"
        failures=$((failures + 1))
    fi
else
    row "executor /healthz" "UNREACHABLE" "$exec_base"
    failures=$((failures + 1))
fi

# Renderer probe.
if response="$(http_req "$render_base/healthz" 2>/dev/null)"; then
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    render_status="$(printf '%s' "$body" | json_field 'status' 2>/dev/null || true)"
    browser="$(printf '%s' "$body" | json_field 'browser' 2>/dev/null || true)"
    [ -z "$browser" ] && browser="$(printf '%s' "$body" | json_field 'browserVersion' 2>/dev/null || true)"
    error_text="$(printf '%s' "$body" | json_field 'error' 2>/dev/null || true)"
    if [ "$status" = "200" ] && [ "$render_status" = "ok" ] && [ -n "$browser" ]; then
        row "renderer /healthz" "OK" "browser=$browser"
    else
        row "renderer /healthz" "DEGRADED" "HTTP $status status=$render_status ${error_text}"
        failures=$((failures + 1))
    fi
else
    row "renderer /healthz" "UNREACHABLE" "$render_base"
    failures=$((failures + 1))
fi

# Web probe.
if response="$(http_req "$web_base/login" 2>/dev/null)"; then
    status="${response%%$'\n'*}"
    if [ "$status" = "200" ]; then row "web /login" "OK" "200 ($web_base)"; else row "web /login" "DEGRADED" "HTTP $status"; failures=$((failures + 1)); fi
else
    row "web /login" "UNREACHABLE" "$web_base"
    failures=$((failures + 1))
fi

# PostgreSQL probe.
database_url="$(env_value EZTEST_DATABASE_URL '')"
case "$database_url" in
    ""|*"<"*)
        row "PostgreSQL" "UNCONFIGURED" "EZTEST_DATABASE_URL is empty or a placeholder"
        failures=$((failures + 1))
        ;;
    *)
        read -r db_host db_port <<<"$(db_host_port "$database_url")"
        if tcp_open "$db_host" "$db_port"; then
            row "PostgreSQL" "REACHABLE" "${db_host}:${db_port}"
        else
            row "PostgreSQL" "UNREACHABLE" "${db_host}:${db_port} (from EZTEST_DATABASE_URL)"
            failures=$((failures + 1))
        fi
        ;;
esac

printf '\n'
if [ "$failures" -eq 0 ]; then
    printf 'Overall: healthy\n'
    exit 0
fi
printf 'Overall: %s check(s) failed or degraded. See the table above and logs/.\n' "$failures"
exit 1

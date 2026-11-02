#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# End-to-end smoke test for an extracted, running EzTest package (Linux).
#
# Runs the nine packaging checks from the release checklist against a stack
# started with start-eztest.sh:
#   1. PostgreSQL TCP reachable
#   2. GET /healthz = 200 {status:ok}
#   3. GET /api/health = 200, data.db = ok, data.version equals VERSION
#   4. PDF renderer GET /healthz = 200 status=ok with a browser version (the 0.2
#      replacement for the 0.1 GET /api/openapi.yaml check, which 0.2 does not
#      serve)
#   5. Admin login = 200 with the .env admin seed (admin/admin123) and
#      GET /api/auth/me = 200 username=admin
#   6. Executor GET /healthz = 200 status=ok with a browserVersion
#   7. Executor round-trip: context -> goto web /login -> aria snapshot ->
#      PNG screenshot artifact -> delete context (all ok:true)
#   8. GET /login = 200 and the body contains "EzTest"
#   9. The .data/ content directories exist after start
#
# Prints PASS/FAIL per check and a summary; exits non-zero when any check
# fails. Secrets are read from .env but never printed. No curl/jq needed: HTTP
# probes use the bundled Node runtime.
# ---------------------------------------------------------------------------
set -u

PKG="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="$PKG/.env"
NODE="$PKG/runtime/node/bin/node"

TOTAL=9
PASSED=0
FAILURES=()

fail_startup() {
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
const request = http.request(url, { method, headers, timeout: 20000 }, (response) => {
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

json_string() { # args are strings; prints a JSON array-safe object for the caller
    "$NODE" -e '
const [name, value] = process.argv.slice(1)
process.stdout.write(JSON.stringify({ [name]: value }))
' "$1" "$2"
}

check() { # number name check-function
    local number="$1" name="$2" function_name="$3" message
    printf '[%2d/%d] %-64s' "$number" "$TOTAL" "$name"
    if message="$("$function_name" 2>&1)"; then
        PASSED=$((PASSED + 1))
        printf ' PASS\n'
    else
        FAILURES+=("$number. $name - $message")
        printf ' FAIL\n'
        printf '         %s\n' "$message"
    fi
}

# ---------------------------------------------------------------------------
[ -f "$ENV_FILE" ] || fail_startup "$ENV_FILE not found. Run 'bash init-config.sh' and 'bash start-eztest.sh' first."
[ -f "$NODE" ] || fail_startup "The bundled Node runtime is missing. Extract the complete package archive."
# A Windows-created tar loses executable bits; make the bundled Node usable even
# when smoke-test.sh is run without a prior start.
chmod +x "$NODE" 2>/dev/null || true
load_env

PACKAGE_VERSION="dev"
[ -f "$PKG/VERSION" ] && PACKAGE_VERSION="$(head -n 1 "$PKG/VERSION" | tr -d '\r')"
ADMIN_USER="$(env_value EZTEST_ADMIN_USERNAME 'admin')"
ADMIN_PASSWORD="$(env_value EZTEST_ADMIN_PASSWORD '')"
EXECUTOR_TOKEN="$(env_value EZTEST_EXECUTOR_TOKEN '')"
DATABASE_URL="$(env_value EZTEST_DATABASE_URL '')"
case "$ADMIN_PASSWORD" in ""|*"<"*) fail_startup "EZTEST_ADMIN_PASSWORD is missing/placeholder in .env; run 'bash init-config.sh' first." ;; esac
[ -n "$EXECUTOR_TOKEN" ] || fail_startup "EZTEST_EXECUTOR_TOKEN is empty in .env; run 'bash init-config.sh' first."
case "$DATABASE_URL" in ""|*"<"*) fail_startup "EZTEST_DATABASE_URL is not configured in .env." ;; esac

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
read -r db_host db_port <<<"$(db_host_port "$DATABASE_URL")"

printf '\nEzTest package smoke test\n'
printf '  package : %s\n' "$PKG"
printf '  version : %s\n' "$PACKAGE_VERSION"
printf '  API     : %s\n' "$api_base"
printf '  web     : %s\n' "$web_base"
printf '  executor: %s\n' "$exec_base"
printf '  renderer: %s\n' "$render_base"
printf '  database: %s:%s (credentials are not displayed)\n\n' "$db_host" "$db_port"

# --- 1 ---------------------------------------------------------------------
check_1() {
    if tcp_open "$db_host" "$db_port"; then return 0; fi
    printf 'Cannot open a TCP connection to %s:%s. Is PostgreSQL 17 running and is EZTEST_DATABASE_URL correct?' "$db_host" "$db_port"
    return 1
}

# --- 2 ---------------------------------------------------------------------
check_2() {
    local response status body value
    response="$(http_req "$api_base/healthz" 2>/dev/null)" || { printf 'The API is not reachable at %s.' "$api_base"; return 1; }
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    [ "$status" = "200" ] || { printf 'GET /healthz returned HTTP %s.' "$status"; return 1; }
    value="$(printf '%s' "$body" | json_field 'status' 2>/dev/null || true)"
    [ "$value" = "ok" ] || { printf "GET /healthz status field is '%s'." "$value"; return 1; }
    return 0
}

# --- 3 ---------------------------------------------------------------------
check_3() {
    local response status body db version
    response="$(http_req "$api_base/api/health" 2>/dev/null)" || { printf 'The API is not reachable at %s.' "$api_base"; return 1; }
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    [ "$status" = "200" ] || { printf 'GET /api/health returned HTTP %s.' "$status"; return 1; }
    db="$(printf '%s' "$body" | json_field 'data.db' 2>/dev/null || true)"
    [ "$db" = "ok" ] || { printf "GET /api/health db field is '%s'." "$db"; return 1; }
    version="$(printf '%s' "$body" | json_field 'data.version' 2>/dev/null || true)"
    [ "$version" = "$PACKAGE_VERSION" ] || { printf "GET /api/health version is '%s', expected '%s' from the VERSION file." "$version" "$PACKAGE_VERSION"; return 1; }
    return 0
}

# --- 4 ---------------------------------------------------------------------
check_4() {
    local response status body value browser
    response="$(http_req "$render_base/healthz" 2>/dev/null)" || { printf 'The PDF renderer is not reachable at %s.' "$render_base"; return 1; }
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    if [ "$status" != "200" ]; then
        printf 'Renderer /healthz returned HTTP %s: %s' "$status" "$(printf '%s' "$body" | head -c 400)"
        return 1
    fi
    value="$(printf '%s' "$body" | json_field 'status' 2>/dev/null || true)"
    [ "$value" = "ok" ] || { printf "Renderer status is '%s'." "$value"; return 1; }
    browser="$(printf '%s' "$body" | json_field 'browser' 2>/dev/null || true)"
    [ -z "$browser" ] && browser="$(printf '%s' "$body" | json_field 'browserVersion' 2>/dev/null || true)"
    [ -n "$browser" ] || { printf 'The renderer reports no browser version: the bundled Chromium did not launch (check logs/renderer.err.log).'; return 1; }
    return 0
}

# --- 5 ---------------------------------------------------------------------
check_5() {
    local login_body response status body token me_response me_status me_body me_user
    login_body="$("$NODE" -e 'process.stdout.write(JSON.stringify({username:process.argv[1],password:process.argv[2]}))' "$ADMIN_USER" "$ADMIN_PASSWORD")"
    response="$(http_req "$api_base/api/auth/login" POST "" "$login_body" 2>/dev/null)" || { printf 'The API is not reachable at %s.' "$api_base"; return 1; }
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    [ "$status" = "200" ] || { printf 'POST /api/auth/login returned HTTP %s (wrong password in .env or the admin was not seeded?).' "$status"; return 1; }
    token="$(printf '%s' "$body" | json_field 'data.token' 2>/dev/null || true)"
    [ -n "$token" ] || { printf 'POST /api/auth/login did not return a token.'; return 1; }
    me_response="$(http_req "$api_base/api/auth/me" GET "{\"Authorization\":\"Bearer $token\"}" "" 2>/dev/null)" || { printf 'GET /api/auth/me is not reachable.'; return 1; }
    me_status="${me_response%%$'\n'*}"
    me_body="${me_response#*$'\n'}"
    [ "$me_status" = "200" ] || { printf 'GET /api/auth/me returned HTTP %s.' "$me_status"; return 1; }
    me_user="$(printf '%s' "$me_body" | json_field 'data.username' 2>/dev/null || true)"
    [ "$me_user" = "$ADMIN_USER" ] || { printf "GET /api/auth/me username is '%s', expected '%s'." "$me_user" "$ADMIN_USER"; return 1; }
    return 0
}

# --- 6 ---------------------------------------------------------------------
check_6() {
    local response status body value browser
    response="$(http_req "$exec_base/healthz" 2>/dev/null)" || { printf 'The executor is not reachable at %s.' "$exec_base"; return 1; }
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    if [ "$status" != "200" ]; then
        printf 'Executor /healthz returned HTTP %s: %s' "$status" "$(printf '%s' "$body" | head -c 400)"
        return 1
    fi
    value="$(printf '%s' "$body" | json_field 'status' 2>/dev/null || true)"
    browser="$(printf '%s' "$body" | json_field 'browserVersion' 2>/dev/null || true)"
    [ "$value" = "ok" ] || { printf "Executor status is '%s'." "$value"; return 1; }
    [ -n "$browser" ] || { printf 'The executor reports no browserVersion: the bundled Chromium did not launch (install Linux browser deps with: sudo bash install-linux-deps.sh).'; return 1; }
    return 0
}

# --- 7 ---------------------------------------------------------------------
smoke_context_actions() { # contextId
    local context_id="$1" headers action_url goto_body snapshot_body screenshot_body
    local goto_response goto_status goto_res_body ok snapshot_response snapshot_body_body
    local snapshot_json aria screenshot_response screenshot_json b64 png
    headers="{\"Authorization\":\"Bearer $EXECUTOR_TOKEN\"}"
    action_url="$exec_base/v1/contexts/$context_id/actions"

    goto_body="$("$NODE" -e 'process.stdout.write(JSON.stringify({action:"goto",args:{url:process.argv[1]}}))' "$web_base/login")"
    goto_response="$(http_req "$action_url" POST "$headers" "$goto_body" 2>/dev/null)" || { printf 'The executor is not reachable.'; return 1; }
    goto_status="${goto_response%%$'\n'*}"
    goto_res_body="${goto_response#*$'\n'}"
    ok="$(printf '%s' "$goto_res_body" | json_field 'ok' 2>/dev/null || true)"
    [ "$goto_status" = "200" ] && [ "$ok" = "true" ] || { printf 'goto %s/login failed: HTTP %s %s' "$web_base" "$goto_status" "$goto_res_body"; return 1; }

    snapshot_body='{"action":"snapshot","args":{}}'
    snapshot_response="$(http_req "$action_url" POST "$headers" "$snapshot_body" 2>/dev/null)" || { printf 'The executor is not reachable.'; return 1; }
    snapshot_status="${snapshot_response%%$'\n'*}"
    snapshot_body_body="${snapshot_response#*$'\n'}"
    ok="$(printf '%s' "$snapshot_body_body" | json_field 'ok' 2>/dev/null || true)"
    [ "$snapshot_status" = "200" ] && [ "$ok" = "true" ] || { printf 'snapshot failed: HTTP %s %s' "$snapshot_status" "$snapshot_body_body"; return 1; }
    aria="$(printf '%s' "$snapshot_body_body" | json_field 'result.aria' 2>/dev/null || true)"
    [ -n "$aria" ] || { printf 'snapshot returned an empty aria tree for %s/login.' "$web_base"; return 1; }

    screenshot_body='{"action":"screenshot","args":{}}'
    screenshot_response="$(http_req "$action_url" POST "$headers" "$screenshot_body" 2>/dev/null)" || { printf 'The executor is not reachable.'; return 1; }
    screenshot_status="${screenshot_response%%$'\n'*}"
    screenshot_json="${screenshot_response#*$'\n'}"
    ok="$(printf '%s' "$screenshot_json" | json_field 'ok' 2>/dev/null || true)"
    [ "$screenshot_status" = "200" ] && [ "$ok" = "true" ] || { printf 'screenshot failed: HTTP %s %s' "$screenshot_status" "$screenshot_json"; return 1; }
    b64="$(printf '%s' "$screenshot_json" | json_field 'artifacts.0.base64' 2>/dev/null || true)"
    [ -n "$b64" ] || { printf 'screenshot returned no base64 artifact.'; return 1; }
    # A screenshot's base64 payload easily exceeds the OS per-argument limit
    # (MAX_ARG_STRLEN is 128 KiB on Linux), so stream it over stdin instead of
    # passing it as an argv value.
    png="$(printf '%s' "$b64" | "$NODE" -e '
let data = ""
process.stdin.on("data", (chunk) => { data += chunk }).on("end", () => {
  const buffer = Buffer.from(data.trim(), "base64")
  process.stdout.write(String(buffer.length) + ":" + buffer.slice(0, 4).toString("hex"))
})
' 2>/dev/null || true)"
    case "$png" in
        0:*) printf 'screenshot artifact decoded to 0 bytes.'; return 1 ;;
        *:89504e47) return 0 ;;
        *) printf 'screenshot artifact is not a PNG (got %s).' "$png"; return 1 ;;
    esac
}

check_7() {
    local headers create_body create_response create_status create_json context_id actions_result
    headers="{\"Authorization\":\"Bearer $EXECUTOR_TOKEN\"}"
    create_body="$("$NODE" -e 'process.stdout.write(JSON.stringify({key:"smoke-"+Date.now(),allowedHosts:["localhost","127.0.0.1"],baseUrl:process.argv[1],viewport:{width:1280,height:720}}))' "$web_base")"
    create_response="$(http_req "$exec_base/v1/contexts" POST "$headers" "$create_body" 2>/dev/null)" || { printf 'The executor is not reachable at %s.' "$exec_base"; return 1; }
    create_status="${create_response%%$'\n'*}"
    create_json="${create_response#*$'\n'}"
    [ "$create_status" = "200" ] || { printf 'POST /v1/contexts returned HTTP %s: %s' "$create_status" "$create_json"; return 1; }
    context_id="$(printf '%s' "$create_json" | json_field 'contextId' 2>/dev/null || true)"
    [ -n "$context_id" ] || { printf 'POST /v1/contexts returned no contextId.'; return 1; }

    smoke_context_actions "$context_id"
    actions_result=$?
    # Always release the executor context, even when an assertion above failed.
    http_req "$exec_base/v1/contexts/$context_id" DELETE "$headers" "" >/dev/null 2>&1 || true
    if [ "$actions_result" -ne 0 ]; then
        printf 'Executor round-trip failed (context %s).' "$context_id"
        return 1
    fi
    return 0
}

# --- 8 ---------------------------------------------------------------------
check_8() {
    local response status body
    response="$(http_req "$web_base/login" 2>/dev/null)" || { printf 'The web server is not reachable at %s.' "$web_base"; return 1; }
    status="${response%%$'\n'*}"
    body="${response#*$'\n'}"
    [ "$status" = "200" ] || { printf 'GET /login returned HTTP %s (web/.next/static assets missing?).' "$status"; return 1; }
    case "$body" in
        *EzTest*) return 0 ;;
        *) printf "GET /login returned 200 but the body does not contain 'EzTest'."; return 1 ;;
    esac
}

# --- 9 ---------------------------------------------------------------------
check_9() {
    local missing="" directory
    for directory in attachments evidence reports exports; do
        [ -d "$PKG/.data/$directory" ] || missing="$missing $directory"
    done
    [ -z "$missing" ] || { printf 'Missing .data directories:%s' "$missing"; return 1; }
    return 0
}

check 1 "PostgreSQL TCP reachable" check_1
check 2 "API GET /healthz is 200 {status:ok}" check_2
check 3 "API GET /api/health has db=ok and the VERSION version" check_3
check 4 "Renderer GET /healthz is 200 with a browser version" check_4
check 5 "Admin login works and GET /api/auth/me is admin" check_5
check 6 "Executor /healthz is 200 with a browserVersion" check_6
check 7 "Executor round-trip: context, goto /login, snapshot, screenshot, delete" check_7
check 8 "Web GET /login is 200 and renders EzTest" check_8
check 9 "Data directories exist (.data)" check_9

printf '\n'
if [ "${#FAILURES[@]}" -eq 0 ]; then
    printf 'Smoke test PASSED (%s/%s checks).\n' "$PASSED" "$TOTAL"
    exit 0
fi
printf 'Smoke test FAILED (%s/%s checks passed).\n' "$PASSED" "$TOTAL"
for failure in "${FAILURES[@]}"; do
    printf '  - %s\n' "$failure"
done
printf '\n'
printf 'Check logs/ and the PACKAGE.md troubleshooting section. Common causes:\n'
printf '  - PostgreSQL 17 not running or EZTEST_DATABASE_URL uses the placeholder.\n'
printf '  - The admin password in .env does not match the seeded admin (admin123 until changed).\n'
printf '  - Missing Linux browser system libraries (run: sudo bash install-linux-deps.sh).\n'
printf '  - The PDF renderer or executor cannot launch Chromium (logs/renderer.err.log, logs/executor.err.log).\n'
exit 1

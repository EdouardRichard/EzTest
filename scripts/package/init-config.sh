#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# First-run configuration for an extracted EzTest 0.2 package (Linux).
#
# Copies the packaged .env.example template to .env, rewrites the
# __PACKAGE_ROOT__ tokens to the absolute path of this package and stores the
# PostgreSQL 17 connection string passed with --database-url (or
# EZTEST_DATABASE_URL). Secret values are never printed.
#
# EzTest 0.2 keeps runtime configuration in the database settings center; the
# API only reads the four startup variables EZTEST_DATABASE_URL, EZTEST_API_PORT,
# EZTEST_WEB_PORT and EZTEST_EXECUTOR_PORT. The first API start seeds the fixed
# administrator admin / admin123 (change it in the UI after the first login).
# The executor and PDF-renderer tokens in .env must match the packaged
# settings-center defaults; if you change one, change the settings center too.
#
# Re-running with --force backs up the current .env to .env.bak and renders it
# again from the template.
#
# Works on Debian/Ubuntu bash 4+.
# ---------------------------------------------------------------------------
set -u

PKG="$(cd "$(dirname "$0")" && pwd)"
TEMPLATE="$PKG/.env.example"
ENV_FILE="$PKG/.env"
BACKUP_FILE="$PKG/.env.bak"

FORCE=0
DB_URL_ARG=""

usage() {
    cat <<'EOF'
Usage: bash init-config.sh [options]

Options:
  --database-url URL   PostgreSQL 17 URL (also read from EZTEST_DATABASE_URL)
  --force, -f          Recreate .env (the previous file is kept as .env.bak)
  --help, -h           Show this help
EOF
}

fail() {
    printf 'ERROR: %s\n' "$1" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --database-url)
            [ $# -ge 2 ] || fail "--database-url needs a value"
            DB_URL_ARG="$2"
            shift 2
            ;;
        --database-url=*)
            DB_URL_ARG="${1#*=}"
            shift
            ;;
        --force|-f)
            FORCE=1
            shift
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            printf 'ERROR: unknown argument: %s\n' "$1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

[ -f "$TEMPLATE" ] || fail "Missing .env.example next to this script ($TEMPLATE). Extract the complete package archive and re-run."

if [ -f "$ENV_FILE" ] && [ "$FORCE" -ne 1 ]; then
    printf 'ERROR: %s already exists.\n' "$ENV_FILE" >&2
    printf '       Use --force to back it up to .env.bak and regenerate it, or edit the existing .env.\n' >&2
    exit 1
fi

# Reads KEY from a dotenv file; prints the trimmed value (empty when absent).
read_env_value() {
    _file="$1"
    _name="$2"
    [ -f "$_file" ] || return 0
    while IFS= read -r _line || [ -n "$_line" ]; do
        _line="${_line%$'\r'}"
        _trim="$(printf '%s' "$_line" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
        [ -z "$_trim" ] && continue
        case "$_trim" in \#*) continue ;; esac
        _key="${_trim%%=*}"
        _key="$(printf '%s' "$_key" | tr -d '[:space:]')"
        if [ "$_key" = "$_name" ]; then
            _value="${_trim#*=}"
            printf '%s' "$_value"
            return 0
        fi
    done < "$_file"
    return 0
}

# Prints "host:port" for a postgres URL (no credentials).
db_hostport() {
    _rest="${1#*://}"
    _rest="${_rest%%/*}"
    _rest="${_rest##*@}"
    case "$_rest" in
        \[*\]:*) printf '%s\n' "$_rest" ;;
        *:*)
            _host="${_rest%:*}"
            _port="${_rest##*:}"
            case "$_port" in
                ''|*[!0-9]*) printf '%s:5432\n' "$_rest" ;;
                *) printf '%s:%s\n' "$_host" "$_port" ;;
            esac
            ;;
        *) printf '%s:5432\n' "$_rest" ;;
    esac
}

# --- Resolve the database URL ------------------------------------------------
DB_URL="$DB_URL_ARG"
if [ -z "$DB_URL" ] && [ -n "${EZTEST_DATABASE_URL:-}" ]; then
    DB_URL="$EZTEST_DATABASE_URL"
fi
if [ -z "$DB_URL" ] && [ -f "$ENV_FILE" ]; then
    DB_URL="$(read_env_value "$ENV_FILE" EZTEST_DATABASE_URL)"
fi
DB_FROM_TEMPLATE=0
case "$DB_URL" in
    *"<"*|"")
        DB_URL="postgres://eztest:<db-password>@localhost:5433/eztest?sslmode=disable"
        DB_FROM_TEMPLATE=1
        ;;
    postgres://*|postgresql://*) : ;;
    *) fail "The database URL must start with postgres:// or postgresql://." ;;
esac

if [ -f "$ENV_FILE" ]; then
    mv -f "$ENV_FILE" "$BACKUP_FILE"
    printf 'Backed up the previous .env to .env.bak\n'
fi

# --- Rendered values --------------------------------------------------------
# 0.2 seeds a fixed administrator (admin / admin123) on the first API start, so
# the template values are rendered as that seed: the API ignores the admin
# variables, smoke-test.sh uses them to log in. The executor/renderer tokens are
# the packaged settings-center defaults; change both sides if you change them.
ADMIN_USERNAME="admin"
ADMIN_PASSWORD="admin123"
EXECUTOR_TOKEN="eztest-dev-executor-token"
RENDER_TOKEN="eztest-dev-render-token"

# --- Render .env from the template -----------------------------------------
PKG_FWD="${PKG//\\//}"
LINES=()
while IFS= read -r LINE || [ -n "$LINE" ]; do
    LINE="${LINE%$'\r'}"
    RENDERED="${LINE//__PACKAGE_ROOT__/$PKG_FWD}"
    case "$RENDERED" in
        EZTEST_DATABASE_URL=*) RENDERED="EZTEST_DATABASE_URL=$DB_URL" ;;
        EZTEST_ADMIN_USERNAME=*) RENDERED="EZTEST_ADMIN_USERNAME=$ADMIN_USERNAME" ;;
        EZTEST_ADMIN_PASSWORD=*) RENDERED="EZTEST_ADMIN_PASSWORD=$ADMIN_PASSWORD" ;;
        EZTEST_EXECUTOR_TOKEN=*) RENDERED="EZTEST_EXECUTOR_TOKEN=$EXECUTOR_TOKEN" ;;
        EZTEST_RENDER_TOKEN=*) RENDERED="EZTEST_RENDER_TOKEN=$RENDER_TOKEN" ;;
    esac
    LINES+=("$RENDERED")
done < "$TEMPLATE"

# Owner-only permissions; write through a temporary file and rename atomically.
(
    umask 077
    printf '%s\n' "${LINES[@]}" > "$ENV_FILE.tmp"
    mv -f "$ENV_FILE.tmp" "$ENV_FILE"
) || fail "Could not write $ENV_FILE"
chmod 600 "$ENV_FILE" 2>/dev/null || true

# --- Summary (never print secret values) -----------------------------------
printf '\n'
printf 'EzTest package initialized.\n'
printf '  package root : %s\n' "$PKG"
printf '  .env         : %s (owner-only permissions)\n' "$ENV_FILE"
if [ "$DB_FROM_TEMPLATE" -eq 1 ]; then
    printf '  database     : NOT SET - edit EZTEST_DATABASE_URL in .env before starting\n'
    printf '                 example: postgres://eztest:<password>@localhost:5433/eztest?sslmode=disable\n'
    printf '                 (PostgreSQL 17 must be running; see PACKAGE.md)\n'
else
    printf '  database     : %s (credentials are in .env, not shown)\n' "$(db_hostport "$DB_URL")"
fi
printf '  admin user   : admin / admin123 (fixed first-start seed; smoke-test.sh uses it)\n'
printf '                 Change the password in the UI after the first login and update\n'
printf '                 EZTEST_ADMIN_PASSWORD in .env afterwards.\n'
printf '  tokens       : executor/renderer use the packaged settings-center defaults;\n'
printf '                 if you change a token here, change the settings center value too.\n'
printf '\nNext steps:\n'
if [ "$DB_FROM_TEMPLATE" -eq 1 ]; then
    printf '  1. Edit %s and set EZTEST_DATABASE_URL.\n' "$ENV_FILE"
    printf '  2. Run: bash start-eztest.sh\n'
else
    printf '  1. Run: bash start-eztest.sh\n'
fi
printf '  2. Open http://localhost:6868, sign in as admin/admin123 and change the password.\n'
printf '  3. Optional: bash smoke-test.sh to verify the whole stack.\n'
exit 0

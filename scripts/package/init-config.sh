#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# First-run configuration for an extracted EzTest package (Linux).
#
# Copies the packaged .env.example template to .env, rewrites the
# __PACKAGE_ROOT__ tokens to the absolute path of this package, generates the
# administrator password, the AES-256 secret keyring and the executor Bearer
# token, and stores the PostgreSQL 17 connection string passed with
# --database-url (or EZTEST_DATABASE_URL). Secret values are never printed.
#
# Re-running with --force backs up the current .env to .env.bak and regenerates
# the secrets. Regenerating EZTEST_SECRET_KEYS makes LLM credentials already
# stored in PostgreSQL undecryptable.
#
# Works on Debian/Ubuntu bash 4+. Uses the bundled Node runtime when present
# (for cryptographically secure random values) and falls back to /dev/urandom.
# ---------------------------------------------------------------------------
set -u

PKG="$(cd "$(dirname "$0")" && pwd)"
TEMPLATE="$PKG/.env.example"
ENV_FILE="$PKG/.env"
BACKUP_FILE="$PKG/.env.bak"
NODE_BIN="$PKG/runtime/node/bin/node"

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

# --- Generate secrets -------------------------------------------------------
# 56-char alphabet; accept random bytes below 224 (56 * 4) to avoid bias.
gen_secrets() {
    if [ -f "$NODE_BIN" ]; then
        chmod +x "$NODE_BIN" 2>/dev/null || true
        "$NODE_BIN" -e '
const crypto = require("crypto");
const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
function password(length) {
  let out = "";
  while (out.length < length) {
    for (const byte of crypto.randomBytes(32)) {
      if (byte < 224) {
        out += alphabet[byte % 56];
        if (out.length >= length) break;
      }
    }
  }
  return out;
}
process.stdout.write([
  crypto.randomBytes(32).toString("base64"),
  crypto.randomBytes(24).toString("hex"),
  password(20),
].join("\n") + "\n");
' && return 0
    fi
    # Fallback: /dev/urandom + base64/od (always present on Debian/Ubuntu).
    _key="$(head -c 32 /dev/urandom | base64 -w0)"
    _token="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    _pwd="$(head -c 64 /dev/urandom | base64 -w0 | tr -dc 'A-Za-z0-9' | cut -c1-20)"
    printf '%s\n%s\n%s\n' "$_key" "$_token" "$_pwd"
}

SECRETS="$(gen_secrets)" || fail "Could not generate random secrets (is /dev/urandom available?)."
SECRET_KEY="$(printf '%s\n' "$SECRETS" | sed -n '1p')"
EXECUTOR_TOKEN="$(printf '%s\n' "$SECRETS" | sed -n '2p')"
ADMIN_PASSWORD="$(printf '%s\n' "$SECRETS" | sed -n '3p')"
[ -n "$SECRET_KEY" ] && [ -n "$EXECUTOR_TOKEN" ] && [ -n "$ADMIN_PASSWORD" ] || fail "Secret generation returned empty values."
SECRET_KEYS="{\"1\":\"$SECRET_KEY\"}"

# --- Render .env from the template -----------------------------------------
PKG_FWD="${PKG//\\//}"
LINES=()
while IFS= read -r LINE || [ -n "$LINE" ]; do
    LINE="${LINE%$'\r'}"
    RENDERED="${LINE//__PACKAGE_ROOT__/$PKG_FWD}"
    case "$RENDERED" in
        EZTEST_DATABASE_URL=*) RENDERED="EZTEST_DATABASE_URL=$DB_URL" ;;
        EZTEST_ADMIN_PASSWORD=*) RENDERED="EZTEST_ADMIN_PASSWORD=$ADMIN_PASSWORD" ;;
        EZTEST_SECRET_KEYS=*) RENDERED="EZTEST_SECRET_KEYS=$SECRET_KEYS" ;;
        EZTEST_EXECUTOR_TOKEN=*) RENDERED="EZTEST_EXECUTOR_TOKEN=$EXECUTOR_TOKEN" ;;
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
printf '  admin user   : admin (password is in .env, not shown)\n'
printf '  secrets      : generated EZTEST_SECRET_KEYS, EZTEST_ADMIN_PASSWORD, EZTEST_EXECUTOR_TOKEN\n'
printf '\nNext steps:\n'
if [ "$DB_FROM_TEMPLATE" -eq 1 ]; then
    printf '  1. Edit %s and set EZTEST_DATABASE_URL.\n' "$ENV_FILE"
    printf '  2. Run: bash start-eztest.sh\n'
else
    printf '  1. Run: bash start-eztest.sh\n'
fi
printf '  2. Open http://localhost:6868 and sign in as admin.\n'
printf '  3. Optional: bash smoke-test.sh to verify the whole stack.\n'
exit 0

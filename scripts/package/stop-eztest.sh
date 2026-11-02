#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# Stops the EzTest stack started by start-eztest.sh.
#
# Stops, in order: web, the PDF renderer, the Playwright executor (whole process group, so
# Chromium children die too) and the Go API. Pid files under run/ are removed.
# PostgreSQL and the .data/ directories are left untouched; a missing or stale
# pid file is reported without failing.
# ---------------------------------------------------------------------------
set -u

PKG="$(cd "$(dirname "$0")" && pwd)"
RUN="$PKG/run"

process_alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null; }

stop_tree() { # TERM the process group, wait, then KILL
    local pid="$1" i
    process_alive "$pid" || return 0
    if ! kill -TERM "-$pid" 2>/dev/null; then kill -TERM "$pid" 2>/dev/null || true; fi
    for i in $(seq 1 50); do
        process_alive "$pid" || return 0
        sleep 0.2
    done
    if ! kill -KILL "-$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
    for i in $(seq 1 25); do
        process_alive "$pid" || return 0
        sleep 0.2
    done
    return 1
}

if [ ! -d "$RUN" ]; then
    printf 'No run/ directory found; EzTest was not started from this package.\n'
    exit 0
fi

stopped=0
failed=0
for component in web renderer executor api; do
    pid_file="$RUN/$component.pid"
    if [ ! -f "$pid_file" ]; then
        printf '%-10s not running (no pid file)\n' "$component"
        continue
    fi
    pid="$(cat "$pid_file" 2>/dev/null || true)"
    case "$pid" in
        ''|*[!0-9]*) pid="" ;;
    esac
    if [ -z "$pid" ] || ! process_alive "$pid"; then
        printf '%-10s not running (stale pid file removed)\n' "$component"
        rm -f "$pid_file"
        continue
    fi
    printf '%-10s stopping pid %s ...' "$component" "$pid"
    if stop_tree "$pid"; then
        printf ' stopped\n'
        stopped=$((stopped + 1))
    else
        printf '\n'
        printf 'WARNING: %s (pid %s) is still running; kill it manually.\n' "$component" "$pid" >&2
        failed=$((failed + 1))
    fi
    rm -f "$pid_file"
done

printf '\n'
if [ "$failed" -gt 0 ]; then
    printf 'EzTest is only partially stopped. PostgreSQL and the .data/ directories were left untouched.\n' >&2
    exit 1
fi
if [ "$stopped" -gt 0 ]; then
    printf 'EzTest stopped. PostgreSQL and the .data/ directories were left untouched.\n'
else
    printf 'EzTest was not running. PostgreSQL and the .data/ directories were left untouched.\n'
fi
exit 0

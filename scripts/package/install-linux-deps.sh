#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# Installs the Linux system libraries the bundled Playwright Chromium needs.
#
# The executor launches Chromium headlessly; a bare Debian/Ubuntu image misses
# libraries such as libnss3, libgbm1 and libasound2, and the executor /healthz
# then reports "degraded" with a browser launch error. Run this once on the
# target machine (root or sudo, network access required):
#
#     sudo bash install-linux-deps.sh
#
# It first asks Playwright itself for the dependency set of the pinned
# browser (authoritative, picks the right t64/non-t64 package names), then
# installs fonts/emoji fonts and xvfb. If the Playwright CLI cannot run, a
# documented apt-get fallback list for Ubuntu 24.04 / Debian 12 is used.
#
# Nothing in the package itself is changed except restoring executable bits on
# the bundled binaries (a Windows-created tar loses them).
# ---------------------------------------------------------------------------
set -u

PKG="$(cd "$(dirname "$0")" && pwd)"
NODE="$PKG/runtime/node/bin/node"
PLAYWRIGHT_CLI="$PKG/executor/node_modules/playwright/cli.js"

fail() {
    printf 'ERROR: %s\n' "$1" >&2
    exit 1
}

# 1. Restore executable bits (no-op on a pristine extraction).
[ -f "$NODE" ] && chmod +x "$NODE" 2>/dev/null || true
[ -f "$PKG/bin/eztest" ] && chmod +x "$PKG/bin/eztest" 2>/dev/null || true
find "$PKG/playwright-browsers" -type f \( -name chrome -o -name chrome-headless-shell -o -name chrome_crashpad_handler -o -name chrome_sandbox -o -name 'ffmpeg*' \) -exec chmod +x {} + 2>/dev/null || true
find "$PKG" -type d -exec chmod a+rx {} + 2>/dev/null || true

[ -f "$NODE" ] || fail "The bundled Node runtime is missing ($NODE). Extract the complete package archive."

# 2. Root or sudo.
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    else
        fail "Run this script as root (or install sudo): su -c 'bash install-linux-deps.sh'"
    fi
fi

HAS_APT=0
HAS_DNF=0
command -v apt-get >/dev/null 2>&1 && HAS_APT=1
command -v dnf >/dev/null 2>&1 && HAS_DNF=1

printf 'EzTest Linux browser dependencies\n'
printf '  package : %s\n' "$PKG"
printf '  target  : %s\n' "$( . /etc/os-release 2>/dev/null && printf '%s' "${PRETTY_NAME:-unknown Linux}" || printf 'unknown Linux')"
printf '\n'

# 3. Preferred path: Playwright's own dependency list for the pinned Chromium.
if [ -f "$PLAYWRIGHT_CLI" ]; then
    printf 'Running Playwright install-deps (authoritative dependency list)...\n'
    if $SUDO "$NODE" "$PLAYWRIGHT_CLI" install-deps chromium; then
        printf 'Playwright dependency installation finished.\n'
    else
        printf 'WARNING: Playwright install-deps failed; trying the documented fallback list.\n' >&2
    fi
fi

# 4. Fonts and a virtual display (headless screenshots render text with these).
if [ "$HAS_APT" -eq 1 ]; then
    printf 'Installing fonts and xvfb...\n'
    $SUDO apt-get update -qq || true
    $SUDO apt-get install -y --no-install-recommends \
        fonts-liberation fonts-noto-color-emoji fonts-unifont xvfb || true
fi

# 5. Fallback library list when Playwright's CLI was unavailable or failed.
if [ ! -f "$PLAYWRIGHT_CLI" ] || [ "$HAS_APT" -eq 1 ]; then
    if [ "$HAS_APT" -eq 1 ]; then
        # Ubuntu 24.04 (noble) uses the t64 package names; Debian 12 does not.
        # Try the noble list first, then the pre-t64 names for anything missing.
        NOBLE_PACKAGES="libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 libcairo2 libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2"
        LEGACY_PACKAGES="libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 libcairo2 libcups2 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0 libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2"
        printf 'Installing the documented browser library fallback list...\n'
        # shellcheck disable=SC2086
        if ! $SUDO apt-get install -y --no-install-recommends $NOBLE_PACKAGES; then
            printf 't64 package names unavailable; retrying with the legacy Debian names...\n' >&2
            # shellcheck disable=SC2086
            $SUDO apt-get install -y --no-install-recommends $LEGACY_PACKAGES || true
        fi
    elif [ "$HAS_DNF" -eq 1 ]; then
        printf 'Installing Fedora/RHEL browser library fallback list...\n'
        $SUDO dnf install -y alsa-lib atk at-spi2-atk at-spi2-core cups-libs libdrm libgbm glib2 nss pango xorg-x11-server-Xvfb liberation-fonts google-noto-color-emoji-fonts || true
    fi
fi

# 6. Verify the packaged Chromium executable is present and executable.
CHROMIUM=""
if [ -x "$PKG/playwright-browsers/chromium-1248/chrome-linux64/chrome" ]; then
    CHROMIUM="$PKG/playwright-browsers/chromium-1248/chrome-linux64/chrome"
elif [ -x "$PKG/playwright-browsers/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell" ]; then
    CHROMIUM="$PKG/playwright-browsers/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell"
fi
if [ -z "$CHROMIUM" ]; then
    printf 'WARNING: no bundled Chromium executable found under playwright-browsers/; was the complete package extracted?\n' >&2
else
    printf 'Bundled Chromium: %s\n' "$CHROMIUM"
    if command -v ldd >/dev/null 2>&1; then
        MISSING_LIBS="$(ldd "$CHROMIUM" 2>/dev/null | grep -i 'not found' || true)"
        if [ -n "$MISSING_LIBS" ]; then
            printf 'WARNING: Chromium still has missing shared libraries:\n%s\n' "$MISSING_LIBS" >&2
            printf 'Re-run this script after fixing the package manager errors above.\n' >&2
            exit 1
        fi
        printf 'All shared libraries reported by ldd are present.\n'
    fi
fi

printf '\nDone. Start (or restart) the package with: bash start-eztest.sh\n'
printf 'Then verify the browser with: bash smoke-test.sh (check 7 must be PASS).\n'
exit 0

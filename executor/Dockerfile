# EzTest executor (protocol v2) — browser capability domain runtime.
#
# P10/Q3=B (ARTEX model): the browser runtime is the OFFICIAL @playwright/cli,
# FLOATING at @latest. An image rebuild upgrades the CLI; each CLI version pins
# its own (possibly alpha) Playwright engine, installed via install-browser. The
# base image floats on the Node 22 major tag. The transitional Playwright-library
# bridge was removed in T16 (browser traffic is pure-recorded via a passive CDP
# observer), so the stable playwright library is no longer installed.
FROM node:22-bookworm

ENV NODE_ENV=production \
    EZTEST_EXECUTOR_ADDR=0.0.0.0:13336 \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

# Global floating CLI; install Chromium for the CLI's own pinned engine
# (install-browser). --with-deps pulls the OS libraries. This layer is the
# upgrade point: rebuilding resolves @latest anew.
RUN npm install --global @playwright/cli@latest pnpm@11.28.5 \
    && playwright-cli install-browser chromium --with-deps \
    && rm -rf /root/.npm

WORKDIR /app

# The executor's own library deps (PTY prebuilt binary). The browser domain runs
# the global CLI, not a local playwright library; --no-frozen-lockfile lets a
# rebuild resolve the floating manifest anew.
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --prod --no-frozen-lockfile \
    && rm -rf /root/.local/share/pnpm/store

COPY src ./src

EXPOSE 13336

USER node

HEALTHCHECK --interval=10s --timeout=5s --retries=6 --start-period=20s \
    CMD ["node", "src/healthcheck.mjs"]

CMD ["node", "src/server.mjs"]
# EzTest executor (Playwright A' protocol).
#
# The official Playwright image already contains the browsers for the pinned
# Playwright version, so no browser download happens during the build.
FROM mcr.microsoft.com/playwright:v1.64.1-noble

ENV NODE_ENV=production \
    EZTEST_EXECUTOR_ADDR=0.0.0.0:13336 \
    EZTEST_EXECUTOR_MAX_CONTEXTS=8 \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

WORKDIR /app

COPY package.json pnpm-lock.yaml ./
RUN npm install --global pnpm@11.28.5 \
    && pnpm install --prod --frozen-lockfile \
    && rm -rf /root/.local/share/pnpm/store /root/.npm

COPY src ./src

EXPOSE 13336

USER pwuser

HEALTHCHECK --interval=10s --timeout=5s --retries=6 --start-period=20s \
    CMD ["node", "src/healthcheck.mjs"]

CMD ["node", "src/server.mjs"]

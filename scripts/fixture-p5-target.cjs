#!/usr/bin/env node
"use strict";

// Deterministic P5 fixture target (design spec §12.2). It stands in for the
// local application under test for the P5 code-intelligence and visual
// acceptance/E2E flows:
//
//   GET  /healthz                    -> 200 "ok"
//   GET  /p5/orders                  -> server-rendered orders page; it also
//                                       fetches /p5/api/orders in the browser so
//                                       execution runs record same-origin network
//                                       evidence for page/API proposals.
//   GET  /p5/visual?theme=light      -> deterministic visual baseline page.
//   GET  /p5/visual?theme=shifted    -> different palette, layout and text, so a
//                                       visual comparison produces a real diff.
//   GET  /p5/api/orders              -> stable JSON order list.
//
// The listening port is resolved from (highest priority first) `--port <n>` /
// `--port=<n>`, then `P5_FIXTURE_PORT`, then `FIXTURE_PORT`, then 13338. It
// listens on loopback only and has no dependencies. The HTML uses inline CSS,
// local font families and no external requests, animations or timers: repeated
// loads of the same URL are byte-identical apart from the HTTP framing, which
// keeps real Chrome screenshots stable. SIGINT/SIGTERM close the listener and
// exit with status 0.

const http = require("node:http");

const HOST = "127.0.0.1";
const DEFAULT_PORT = 13338;
const MIN_PORT = 1;
const MAX_PORT = 65535;

const P5_ORDERS = [
  { id: 1001, number: "ORD-1001", status: "paid", amount: "120.00", currency: "CNY" },
  { id: 1002, number: "ORD-1002", status: "created", amount: "90.00", currency: "CNY" },
  { id: 1003, number: "ORD-1003", status: "paid", amount: "220.00", currency: "CNY" },
];

// --- port resolution -------------------------------------------------------

/** Returns DEFAULT_PORT for an unset value, the integer port, or null if invalid. */
function parsePort(raw) {
  if (raw === undefined || raw === null || String(raw).trim() === "") {
    return DEFAULT_PORT;
  }
  const value = Number(String(raw).trim());
  if (!Number.isInteger(value) || value < MIN_PORT || value > MAX_PORT) {
    return null;
  }
  return value;
}

/** --port/--port= wins over P5_FIXTURE_PORT, which wins over FIXTURE_PORT. */
function resolvePort(argv, env) {
  const portEquals = argv.find((argument) => argument.startsWith("--port="));
  if (portEquals !== undefined) {
    return parsePort(portEquals.slice("--port=".length));
  }
  const portIndex = argv.indexOf("--port");
  if (portIndex >= 0) {
    if (portIndex + 1 >= argv.length) return null;
    return parsePort(argv[portIndex + 1]);
  }
  const envPort =
    env.P5_FIXTURE_PORT !== undefined && env.P5_FIXTURE_PORT !== ""
      ? env.P5_FIXTURE_PORT
      : env.FIXTURE_PORT;
  return parsePort(envPort);
}

const PORT = resolvePort(process.argv.slice(2), process.env);

if (PORT === null) {
  console.error(
    "fixture-p5-target: invalid port; use --port <1-65535>, P5_FIXTURE_PORT or FIXTURE_PORT",
  );
  process.exit(1);
}

// --- response helpers ------------------------------------------------------

function sendWithBody(res, status, contentType, body, extraHeaders) {
  if (res.headersSent || res.writableEnded || res.destroyed) return;
  const payload = Buffer.from(body, "utf8");
  res.writeHead(status, {
    "Content-Type": contentType,
    "Content-Length": payload.length,
    "Cache-Control": "no-store",
    "X-Content-Type-Options": "nosniff",
    ...extraHeaders,
  });
  res.end(payload);
}

function sendHtml(res, status, html, extraHeaders) {
  sendWithBody(res, status, "text/html; charset=utf-8", html, extraHeaders);
}

function sendJson(res, status, body) {
  sendWithBody(res, status, "application/json; charset=utf-8", JSON.stringify(body));
}

function sendText(res, status, text, extraHeaders) {
  sendWithBody(res, status, "text/plain; charset=utf-8", text, extraHeaders);
}

function sendMethodNotAllowed(res, allowed) {
  sendText(res, 405, "method not allowed", { Allow: allowed.join(", ") });
}

function allowGetOrHead(req, res) {
  if (req.method === "GET" || req.method === "HEAD") return true;
  sendMethodNotAllowed(res, ["GET", "HEAD"]);
  return false;
}

// --- pages -----------------------------------------------------------------

const ORDERS_STYLE = `
    :root { color-scheme: light; }
    * { box-sizing: border-box; }
    html, body { margin: 0; padding: 0; }
    body {
      background: #f6f7f9;
      color: #1f2937;
      font-family: Arial, Helvetica, sans-serif;
      font-size: 16px;
      line-height: 1.5;
    }
    #p5-orders { max-width: 960px; margin: 0 auto; padding: 32px 32px 48px; }
    h1 { margin: 0 0 12px; font-size: 30px; color: #1e3a8a; }
    #orders-summary { margin: 0 0 16px; color: #4b5563; }
    nav { display: flex; flex-wrap: wrap; gap: 16px; margin: 0 0 24px; }
    a { color: #1d4ed8; }
    table {
      width: 100%;
      border-collapse: collapse;
      background: #ffffff;
      border: 1px solid #d7deea;
    }
    th { background: #eef2f9; text-align: left; padding: 10px 12px; }
    td { border-top: 1px solid #e5e7eb; padding: 10px 12px; }
    #network-status { margin-top: 16px; color: #6b7280; font-size: 14px; }`;

function ordersTableRows() {
  return P5_ORDERS.map(
    (order) =>
      `<tr><td>${order.number}</td><td>${order.status}</td><td>${order.amount} ${order.currency}</td></tr>`,
  ).join("\n            ");
}

function p5OrdersPage() {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>P5 Orders</title>
    <style>${ORDERS_STYLE}
    </style>
  </head>
  <body>
    <main id="p5-orders">
      <h1>P5 Orders</h1>
      <p id="orders-summary">Orders rendered by the P5 fixture target.</p>
      <nav aria-label="P5 navigation">
        <a href="/p5/orders">Orders</a>
        <a href="/p5/visual?theme=light">Visual light</a>
        <a href="/p5/visual?theme=shifted">Visual shifted</a>
        <a href="/p5/api/orders">Orders API</a>
      </nav>
      <table id="orders-table">
        <thead>
          <tr><th>Number</th><th>Status</th><th>Amount</th></tr>
        </thead>
        <tbody id="orders-body">
            ${ordersTableRows()}
        </tbody>
      </table>
      <p id="network-status">Network evidence endpoint: /p5/api/orders</p>
    </main>
    <script>
      // Same-origin request only: it gives the executor real network evidence
      // for the page/API proposal flow without mutating the stable DOM.
      fetch("/p5/api/orders", { headers: { Accept: "application/json" } }).catch(function () {});
    </script>
  </body>
</html>
`;
}

const VISUAL_ORDERS_ROWS = P5_ORDERS.map(
  (order) =>
    `<tr><td>${order.number}</td><td>${order.status}</td><td>${order.amount} ${order.currency}</td></tr>`,
).join("\n            ");

const LIGHT_STYLE = `
    :root { color-scheme: light; }
    * { box-sizing: border-box; }
    html, body { margin: 0; padding: 0; }
    body {
      min-height: 100vh;
      background: #f3f6fb;
      color: #1f2937;
      font-family: Arial, Helvetica, sans-serif;
      font-size: 16px;
      line-height: 1.5;
    }
    #visual-root { max-width: 1080px; margin: 0 auto; padding: 32px 40px 48px; }
    #visual-header {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: 24px;
      padding-bottom: 20px;
      border-bottom: 2px solid #d7deea;
    }
    #visual-title { margin: 0; font-size: 34px; color: #1d4ed8; letter-spacing: 0; }
    #visual-subtitle { margin: 0; color: #4b5563; font-size: 16px; }
    #visual-summary { display: flex; gap: 20px; margin: 28px 0; }
    .metric {
      flex: 1 1 0;
      padding: 18px 20px;
      background: #ffffff;
      border: 1px solid #d7deea;
      border-radius: 8px;
    }
    .metric-label {
      display: block;
      color: #6b7280;
      font-size: 13px;
      text-transform: uppercase;
      letter-spacing: 0.08em;
    }
    .metric-value { display: block; margin-top: 6px; font-size: 28px; color: #111827; }
    #visual-orders {
      padding: 20px 24px;
      background: #ffffff;
      border: 1px solid #d7deea;
      border-radius: 8px;
    }
    #visual-orders-title { margin: 0 0 12px; font-size: 22px; color: #1f2937; }
    table { width: 100%; border-collapse: collapse; font-size: 15px; }
    th { background: #eef2f9; color: #374151; text-align: left; padding: 10px 12px; }
    td { border-top: 1px solid #e5e7eb; padding: 10px 12px; }
    #visual-footer { margin-top: 24px; color: #6b7280; font-size: 14px; }`;

const SHIFTED_STYLE = `
    :root { color-scheme: dark; }
    * { box-sizing: border-box; }
    html, body { margin: 0; padding: 0; }
    body {
      min-height: 100vh;
      background: #111827;
      color: #f9fafb;
      font-family: Arial, Helvetica, sans-serif;
      font-size: 17px;
      line-height: 1.6;
    }
    #visual-root { max-width: 860px; margin: 0 auto; padding: 48px 28px 56px; }
    #visual-header {
      display: block;
      text-align: center;
      padding-bottom: 28px;
      border-bottom: 4px solid #f59e0b;
    }
    #visual-title { margin: 0; font-size: 46px; color: #fbbf24; letter-spacing: 0.02em; }
    #visual-subtitle { margin: 12px 0 0; color: #d1d5db; font-size: 18px; }
    #visual-summary { display: flex; flex-direction: column; gap: 16px; margin: 36px 0; }
    .metric { padding: 24px 28px; background: #1f2937; border: 2px solid #f59e0b; border-radius: 0; }
    .metric-label {
      display: block;
      color: #fcd34d;
      font-size: 15px;
      text-transform: uppercase;
      letter-spacing: 0.16em;
    }
    .metric-value { display: block; margin-top: 10px; font-size: 36px; color: #ffffff; }
    #visual-orders { padding: 28px 32px; background: #1f2937; border: 2px solid #374151; border-radius: 0; }
    #visual-orders-title { margin: 0 0 20px; font-size: 28px; color: #fbbf24; }
    table { width: 100%; border-collapse: collapse; font-size: 16px; }
    th { background: #374151; color: #fde68a; text-align: left; padding: 14px 16px; }
    td { border-top: 1px solid #4b5563; color: #e5e7eb; padding: 14px 16px; }
    #visual-footer { margin-top: 36px; color: #9ca3af; font-size: 14px; text-align: center; }`;

const VISUAL_THEMES = {
  light: {
    style: LIGHT_STYLE,
    title: "Order Workspace",
    subtitle: "Visual regression fixture - light theme",
    openLabel: "Open orders",
    totalLabel: "Total due",
    totalValue: "430.00 CNY",
    ordersTitle: "Orders",
    footer: "Stable fixture. No external assets.",
  },
  shifted: {
    style: SHIFTED_STYLE,
    title: "Order Workspace Shifted",
    subtitle: "Visual regression fixture - shifted theme",
    openLabel: "Pending orders",
    totalLabel: "Outstanding total",
    totalValue: "430.00 CNY",
    ordersTitle: "Order queue",
    footer: "Shifted palette and layout. Stable fixture.",
  },
};

function p5VisualPage(theme) {
  const config = VISUAL_THEMES[theme];
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>P5 Visual ${theme}</title>
    <style>${config.style}
    </style>
  </head>
  <body class="theme-${theme}">
    <main id="visual-root" data-theme="${theme}">
      <header id="visual-header">
        <h1 id="visual-title">${config.title}</h1>
        <p id="visual-subtitle">${config.subtitle}</p>
      </header>
      <section id="visual-summary" aria-label="Order summary">
        <div class="metric">
          <span class="metric-label">${config.openLabel}</span>
          <strong class="metric-value">3</strong>
        </div>
        <div class="metric">
          <span class="metric-label">${config.totalLabel}</span>
          <strong class="metric-value">${config.totalValue}</strong>
        </div>
      </section>
      <section id="visual-orders" aria-label="Orders">
        <h2 id="visual-orders-title">${config.ordersTitle}</h2>
        <table id="visual-orders-table">
          <thead>
            <tr><th>Order</th><th>Status</th><th>Amount</th></tr>
          </thead>
          <tbody>
            ${VISUAL_ORDERS_ROWS}
          </tbody>
        </table>
      </section>
      <footer id="visual-footer">${config.footer}</footer>
    </main>
  </body>
</html>
`;
}

// --- request routing -------------------------------------------------------

async function handleRequest(req, res) {
  let requestUrl;
  try {
    requestUrl = new URL(req.url, "http://" + HOST + ":" + PORT);
  } catch {
    sendText(res, 400, "invalid request url");
    return;
  }
  const pathname = requestUrl.pathname;

  if (pathname === "/healthz") {
    if (!allowGetOrHead(req, res)) return;
    sendText(res, 200, "ok");
    return;
  }

  if (pathname === "/p5/orders") {
    if (!allowGetOrHead(req, res)) return;
    sendHtml(res, 200, p5OrdersPage());
    return;
  }

  if (pathname === "/p5/visual") {
    if (!allowGetOrHead(req, res)) return;
    const rawTheme = requestUrl.searchParams.get("theme");
    const theme = rawTheme === null || rawTheme === "" ? "light" : rawTheme;
    if (!Object.prototype.hasOwnProperty.call(VISUAL_THEMES, theme)) {
      sendText(res, 400, 'unknown theme; use "light" or "shifted"');
      return;
    }
    sendHtml(res, 200, p5VisualPage(theme));
    return;
  }

  if (pathname === "/p5/api/orders") {
    if (!allowGetOrHead(req, res)) return;
    sendJson(res, 200, { orders: P5_ORDERS });
    return;
  }

  sendText(res, 404, "not found");
}

// --- server bootstrap ------------------------------------------------------

const server = http.createServer((req, res) => {
  handleRequest(req, res).catch((error) => {
    console.error("fixture-p5-target: request failed: " + (error && error.stack ? error.stack : error));
    if (!res.headersSent) {
      sendText(res, 500, "fixture target internal error");
    } else if (!res.writableEnded) {
      try {
        res.end();
      } catch {
        // Best effort on an already-broken connection.
      }
    }
  });
});

server.on("error", (error) => {
  console.error("fixture-p5-target: server error: " + error.message);
  process.exit(1);
});

let shuttingDown = false;

/** Closes the listener once; a short fallback timer prevents a hung shutdown. */
function shutdown(signal) {
  if (shuttingDown) return;
  shuttingDown = true;
  console.log("fixture-p5-target: received " + signal + ", shutting down");
  server.close(() => process.exit(0));
  const fallback = setTimeout(() => process.exit(0), 5000);
  fallback.unref();
}

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => shutdown(signal));
}

server.listen(PORT, HOST, () => {
  console.log("fixture-p5-target listening on http://" + HOST + ":" + PORT);
  console.log(
    "fixture-p5-target endpoints: /healthz /p5/orders /p5/visual?theme=light|shifted /p5/api/orders",
  );
});

#!/usr/bin/env node
"use strict";

// Deterministic fixture target for the P2 session storage-state E2E and manual
// acceptance flows (design spec §9.2). It stands in for a local "external
// system" so a browser login can export a real storageState and a token API
// can be validated. It listens on loopback only and has no dependencies.
//
// Endpoints:
//   GET  /health      -> 200 "ok"
//   GET  /login       -> HTML login form (username / password)
//   POST /login       -> fixture / fixture-pass sets
//                        "fixture_session=ok; HttpOnly; Path=/" and 302 /account;
//                        any other credentials get 401.
//   GET  /account     -> 200 HTML containing "Fixture Account" when the session
//                        cookie is valid, otherwise 302 /login.
//   GET  /api/profile -> 200 {"name":"fixture"} for
//                        "Authorization: Bearer fixture-token", otherwise 401.
//
// P5 additive exploration fixtures (design spec §10.2):
//   GET  /p5/home       -> HTML with three same-origin links and one GET form.
//   GET  /p5/orders     -> order list page linking /p5/orders/{id} and /api/orders.
//   GET  /p5/orders/{id}-> one order page; unknown ids answer 404.
//   GET  /p5/admin      -> session-cookie guarded page; without the cookie 302 /login.
//   GET  /api/orders    -> {"orders":[...]} JSON.
//   GET  /api/orders/{id} -> one order JSON; unknown ids answer 404 JSON.
//
// P6 additive execution fixtures (design spec §10.2):
//   GET/POST /p6/login    -> form login; success sets the p6_session cookie and
//                            302 /p6/home, wrong creds show .login-error, and
//                            ?mfa=1 renders the #mfa challenge instead.
//   GET  /p6/home         -> authenticated page with orders/new/logout nav.
//   GET  /p6/orders       -> three-order list; /p6/orders/{id} -> detail.
//   GET/POST /p6/orders/new -> form + in-memory order creation.
//   GET  /api/p6/orders[/{id}] -> JSON list/detail.
//   GET  /p6/slow?ms=8000 -> deliberately slow page for pause tests.
//   GET  /p6/assert-fail  -> page whose result text contradicts the expectation.
//   GET  /p6/requires-auth-> authenticated page that 302s to /p6/login.
// Existing routes and their behavior are unchanged.
//
// P3 additive two-system execution fixtures (design spec §13.1):
//   GET/POST /p3/login    -> form login; the payments FIXTURE_VARIANT supports
//                            ?mfa=1: a valid password renders the #mfa code
//                            challenge and code 246810 completes the login.
//   GET  /p3/home         -> session-guarded landing page (p3_session cookie).
//   GET  /p3/orders       -> order list containing ORD-1001 (paid, 5200.00).
//   GET  /p3/orders/{id}  -> order detail; amount "¥5,200.00" and
//                            #refund-amount "¥5,000.00" (mismatch on purpose).
//   GET/POST /p3/refund   -> refund form / in-memory refund creation.
//   GET  /p3/payments     -> refund list (REF-5001, ¥5,000.00, created).
//   GET  /p3/2fa          -> standalone MFA page; POST code 246810 logs in.
//   GET  /p3/slow?ms=8000 -> deliberately slow page for pause tests.
//   GET  /p3/timeout-window?ms=10000 -> #confirm-dialog stays visible for ms,
//                            then is removed and #window-expired appears
//                            (ms is supported up to 120000).
//   GET  /api/p3/orders, /api/p3/payments -> JSON list endpoints.
//   FIXTURE_VARIANT=orders|payments selects the demo system (default orders);
//   FIXTURE_PORT selects the loopback port. No dependencies and loopback only.

const http = require("node:http");

const HOST = "127.0.0.1";
const PORT = Number(process.env.FIXTURE_PORT || 13335);

const USERNAME = "fixture";
const PASSWORD = "fixture-pass";
const TOKEN = "fixture-token";
const SESSION_COOKIE = "fixture_session";
const SESSION_VALUE = "ok";
const BEARER = "Bearer " + TOKEN;

const MAX_BODY_BYTES = 16 * 1024;

const LOGIN_FORM = `
    <form method="post" action="/login">
      <label for="username">Username</label>
      <input id="username" name="username" type="text" autocomplete="username" required />
      <label for="password">Password</label>
      <input id="password" name="password" type="password" autocomplete="current-password" required />
      <button type="submit">Sign in</button>
    </form>`;

const ACCOUNT_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>Fixture Account</title>
  </head>
  <body>
    <main>
      <h1>Fixture Account</h1>
      <p>You are signed in as ${USERNAME}.</p>
    </main>
  </body>
</html>
`;

// --- response helpers ------------------------------------------------------

function sendWithBody(res, status, contentType, body, extraHeaders) {
  if (res.headersSent || res.writableEnded || res.destroyed) return;
  const payload = Buffer.from(body, "utf8");
  res.writeHead(status, {
    "Content-Type": contentType,
    "Content-Length": payload.length,
    "Cache-Control": "no-store",
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

function redirect(res, location, extraHeaders) {
  if (res.headersSent || res.writableEnded || res.destroyed) return;
  res.writeHead(302, {
    Location: location,
    "Content-Length": 0,
    "Cache-Control": "no-store",
    ...extraHeaders,
  });
  res.end();
}

function sendMethodNotAllowed(res, allowed) {
  sendText(res, 405, "method not allowed", { Allow: allowed.join(", ") });
}

// --- request helpers -------------------------------------------------------

function readCookie(req, name) {
  const header = req.headers.cookie;
  if (typeof header !== "string" || header.length === 0) return null;
  for (const part of header.split(";")) {
    const separator = part.indexOf("=");
    if (separator < 0) continue;
    if (part.slice(0, separator).trim() === name) {
      return part.slice(separator + 1).trim();
    }
  }
  return null;
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (chunk) => {
      size += chunk.length;
      if (size > MAX_BODY_BYTES) {
        reject(new Error("request body too large"));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

// --- pages -----------------------------------------------------------------

// --- P5 exploration fixtures (design spec §10.2) ---------------------------

const P5_ORDERS = [
  { id: 1, number: "P5-ORDER-1", status: "created", amount: 100, currency: "CNY" },
  { id: 2, number: "P5-ORDER-2", status: "paid", amount: 250, currency: "CNY" },
];

const P5_HOME_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P5 Home</title>
  </head>
  <body>
    <main>
      <h1>P5 Home</h1>
      <p>EzTest P5 application exploration fixture home page.</p>
      <nav aria-label="P5 links">
        <a href="/p5/orders">Order list</a>
        <a href="/p5/orders/1">Order 1</a>
        <a href="/p5/admin">Admin</a>
      </nav>
      <form method="get" action="/p5/orders">
        <label for="q">Search orders</label>
        <input id="q" name="q" type="text" />
        <button type="submit">Search</button>
      </form>
    </main>
  </body>
</html>
`;

const P5_ADMIN_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P5 Admin</title>
  </head>
  <body>
    <main>
      <h1>P5 Admin</h1>
      <p>Only reachable with the fixture session cookie.</p>
      <a href="/p5/home">Back to home</a>
    </main>
  </body>
</html>
`;

const P5_ORDERS_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P5 Orders</title>
  </head>
  <body>
    <main>
      <h1>P5 Orders</h1>
      <a href="/p5/home">Home</a>
      <a href="/p5/orders/1">Order 1</a>
      <a href="/p5/orders/2">Order 2</a>
      <a href="/api/orders">Orders API</a>
    </main>
  </body>
</html>
`;

/** Builds one order detail page for a known fixture order. */
function p5OrderPage(order) {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P5 Order ${order.id}</title>
  </head>
  <body>
    <main>
      <h1>P5 Order ${order.id}</h1>
      <dl>
        <dt>Number</dt><dd>${order.number}</dd>
        <dt>Status</dt><dd>${order.status}</dd>
        <dt>Amount</dt><dd>${order.amount} ${order.currency}</dd>
      </dl>
      <a href="/p5/orders">Order list</a>
      <a href="/p5/home">Home</a>
    </main>
  </body>
</html>
`;
}

function p5OrderById(rawId) {
  const id = Number.parseInt(rawId, 10);
  return P5_ORDERS.find((order) => order.id === id) || null;
}

function p5OrderNotFoundPage() {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P5 Order not found</title>
  </head>
  <body>
    <main>
      <h1>P5 Order not found</h1>
      <a href="/p5/orders">Order list</a>
    </main>
  </body>
</html>
`;
}

// --- P6 execution fixtures (design spec §10.2) -----------------------------

const P6_USERNAME = "fixture";
const P6_PASSWORD = "fixture-pass";
const P6_COOKIE = "p6_session";
const P6_SESSION_VALUE = "ok";
const P6_ORDERS = [
  { id: 1, number: "P6-ORDER-1", status: "created", amount: 100, currency: "CNY" },
  { id: 2, number: "P6-ORDER-2", status: "paid", amount: 250, currency: "CNY" },
  { id: 3, number: "P6-ORDER-3", status: "cancelled", amount: 75, currency: "CNY" },
];

/**
 * The P6 login form. The action keeps the page's query string so a recipe that
 * navigates to `/p6/login?mfa=1` still reaches the MFA challenge after submit:
 * a bare `/p6/login` action would drop the query and silently complete a
 * password-only login, making the takeover path unreachable.
 */
function p6LoginForm(mfa) {
  const action = mfa ? "/p6/login?mfa=1" : "/p6/login";
  return `
    <form method="post" action="${action}">
      <label for="username">Username</label>
      <input id="username" name="username" type="text" autocomplete="username" required />
      <label for="password">Password</label>
      <input id="password" name="password" type="password" autocomplete="current-password" required />
      <button type="submit">Sign in</button>
    </form>`;
}

/** One P6 login page; `mfa` renders the visible #mfa challenge block. */
function p6LoginPage(message, mfa) {
  const error = message ? `<p class="login-error" role="alert">${message}</p>` : "";
  const challenge = mfa
    ? `<section id="mfa" aria-label="MFA challenge"><h2>MFA required</h2><p>Enter the authenticator code.</p></section>`
    : "";
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 Login</title>
  </head>
  <body>
    <main>
      <h1>P6 Login</h1>
      ${error}
      ${challenge}
      ${p6LoginForm(mfa)}
    </main>
  </body>
</html>
`;
}

function p6MfaPage() {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 MFA</title>
  </head>
  <body>
    <main>
      <h1>P6 MFA</h1>
      <section id="mfa" aria-label="MFA challenge">
        <p>Multi-factor authentication is required. Continue in the authenticated session.</p>
      </section>
    </main>
  </body>
</html>
`;
}

const P6_HOME_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 Home</title>
  </head>
  <body>
    <main id="p6-home">
      <h1>P6 Home</h1>
      <nav aria-label="P6 navigation">
        <a href="/p6/orders">Orders</a>
        <a href="/p6/orders/new">New order</a>
        <a href="/p6/logout">Logout</a>
      </nav>
      <p>Signed in to the P6 fixture application.</p>
    </main>
  </body>
</html>
`;

const P6_ORDERS_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 Orders</title>
  </head>
  <body>
    <main>
      <h1>P6 Orders</h1>
      <nav aria-label="P6 navigation">
        <a href="/p6/home">Home</a>
        <a href="/p6/orders/new">New order</a>
      </nav>
      <ul id="orders">
        ${P6_ORDERS.map(
          (order) =>
            `<li><a href="/p6/orders/${order.id}">${order.number}</a> <span class="order-status">${order.status}</span> <span class="order-amount">${order.amount} ${order.currency}</span></li>`,
        ).join("\n        ")}
      </ul>
    </main>
  </body>
</html>
`;

function p6OrderPage(order) {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 Order ${order.id}</title>
  </head>
  <body>
    <main>
      <h1>P6 Order ${order.id}</h1>
      <dl>
        <dt>Number</dt><dd id="order-number">${order.number}</dd>
        <dt>Status</dt><dd id="order-status">${order.status}</dd>
        <dt>Amount</dt><dd id="order-amount">${order.amount} ${order.currency}</dd>
      </dl>
      <a href="/p6/orders">Order list</a>
    </main>
  </body>
</html>
`;
}

function p6OrderNotFoundPage() {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 Order not found</title>
  </head>
  <body>
    <main>
      <h1>P6 Order not found</h1>
      <a href="/p6/orders">Order list</a>
    </main>
  </body>
</html>
`;
}

const P6_NEW_ORDER_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 New order</title>
  </head>
  <body>
    <main>
      <h1>New order</h1>
      <form method="post" action="/p6/orders/new">
        <label for="order-number">Order number</label>
        <input id="order-number" name="number" type="text" value="" required />
        <label for="order-amount">Amount</label>
        <input id="order-amount" name="amount" type="number" value="100" min="0" />
        <button type="submit">Create order</button>
      </form>
      <a href="/p6/orders">Order list</a>
    </main>
  </body>
</html>
`;

const P6_ASSERT_FAIL_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 Assert fail</title>
  </head>
  <body>
    <main>
      <h1>P6 Assert fail</h1>
      <p id="result">P6 assert-fail: unexpected result, the expected success text is absent.</p>
      <a href="/p6/orders">Order list</a>
    </main>
  </body>
</html>
`;

const P6_REQUIRES_AUTH_PAGE = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P6 Requires auth</title>
  </head>
  <body>
    <main>
      <h1>P6 Requires auth</h1>
      <p>Authenticated page. A missing session redirects to the login form.</p>
      <a href="/p6/home">Home</a>
    </main>
  </body>
</html>
`;

function p6SessionCookie() {
  return `${P6_COOKIE}=${P6_SESSION_VALUE}; HttpOnly; Path=/; SameSite=Lax`;
}

function p6Authorized(req) {
  return readCookie(req, P6_COOKIE) === P6_SESSION_VALUE;
}

function p6OrderById(rawId) {
  const id = Number.parseInt(rawId, 10);
  return P6_ORDERS.find((order) => order.id === id) || null;
}

/** Creates one in-memory order and returns it. */
function p6CreateOrder(number, amount) {
  const id = P6_ORDERS.reduce((max, order) => Math.max(max, order.id), 0) + 1;
  const order = {
    id,
    number: number.trim() || `P6-ORDER-${id}`,
    status: "created",
    amount: Number.isFinite(amount) ? amount : 0,
    currency: "CNY",
  };
  P6_ORDERS.push(order);
  return order;
}

/** GET renders the form; POST validates the fixture credentials. */
function handleP6LoginGet(req, res, mfa) {
  sendHtml(res, 200, p6LoginPage("", mfa));
}

async function handleP6LoginPost(req, res, mfa) {
  const raw = await readBody(req);
  const form = new URLSearchParams(raw);
  const username = form.get("username") || "";
  const password = form.get("password") || "";
  if (username !== P6_USERNAME || password !== P6_PASSWORD) {
    sendHtml(res, 401, p6LoginPage("Invalid username or password", mfa));
    return;
  }
  if (mfa) {
    // Password accepted, but the second factor is still outstanding: no session
    // cookie is issued so the login flow detects #mfa and requests a takeover.
    sendHtml(res, 200, p6MfaPage());
    return;
  }
  redirect(res, "/p6/home", { "Set-Cookie": p6SessionCookie() });
}

function loginPage(message) {
  const error = message ? `<p role="alert">${message}</p>` : "";
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>Fixture Login</title>
  </head>
  <body>
    <main>
      <h1>Fixture Login</h1>
      ${error}
      ${LOGIN_FORM}
    </main>
  </body>
</html>
`;
}

// --- P3 execution fixtures (design spec §13.1) -----------------------------

const P3_USERNAME = "fixture";
const P3_PASSWORD = "fixture-pass";
const P3_MFA_CODE = "246810";
const P3_COOKIE = "p3_session";
const P3_SESSION_VALUE = "ok";
const P3_VARIANT = process.env.FIXTURE_VARIANT === "payments" ? "payments" : "orders";
const P3_REFUND_AMOUNT = "5000.00";

const P3_ORDERS = [
  {
    id: 1,
    number: "ORD-1001",
    amount: "5200.00",
    currency: "CNY",
    status: "paid",
  },
];

// Seeded refund for order 1. Its amount (5000.00) intentionally differs from the
// paid amount (5200.00) so an application-level failure case stays available.
const P3_REFUNDS = [
  {
    id: 1,
    refundNumber: "REF-5001",
    orderId: 1,
    amount: P3_REFUND_AMOUNT,
    currency: "CNY",
    status: "created",
  },
];
let p3RefundSequence = 1;

function p3VariantName() {
  return P3_VARIANT === "payments" ? "Payment Gateway" : "Order Center";
}

/** Formats a numeric string ("5200.00") as the fixture's "¥5,200.00" text. */
function p3FormatAmount(amount) {
  const parts = String(amount).split(".");
  const whole = parts[0] || "0";
  const fraction = (parts[1] || "00").padEnd(2, "0").slice(0, 2);
  return "¥" + whole.replace(/\B(?=(\d{3})+(?!\d))/g, ",") + "." + fraction;
}

function p3SessionCookie() {
  return `${P3_COOKIE}=${P3_SESSION_VALUE}; HttpOnly; Path=/; SameSite=Lax`;
}

function p3Authorized(req) {
  return readCookie(req, P3_COOKIE) === P3_SESSION_VALUE;
}

function p3LoginForm(mfa) {
  const action = mfa ? "/p3/login?mfa=1" : "/p3/login";
  return `
    <form method="post" action="${action}">
      <label for="username">Username</label>
      <input id="username" name="username" type="text" autocomplete="username" required />
      <label for="password">Password</label>
      <input id="password" name="password" type="password" autocomplete="current-password" required />
      <button type="submit">Sign in</button>
    </form>`;
}

function p3Page(title, body) {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>P3 ${title}</title>
  </head>
  <body>
    <main>
      ${body}
    </main>
  </body>
</html>
`;
}

function p3LoginPage(message, mfa) {
  const error = message ? `<p class="error" role="alert">${message}</p>` : "";
  const hint = mfa
    ? `<p class="login-hint">A verification code is required after the password.</p>`
    : "";
  return p3Page(
    `${p3VariantName()} Login`,
    `<h1>P3 ${p3VariantName()} Login</h1>
      ${error}
      ${hint}
      ${p3LoginForm(mfa)}`,
  );
}

function p3MfaPage(message, action) {
  const error = message ? `<p class="error" role="alert">${message}</p>` : "";
  const target = action || "/p3/login?mfa=1";
  return p3Page(
    "Two-factor authentication",
    `<h1>P3 Two-factor authentication</h1>
      ${error}
      <section id="mfa" aria-label="Two-factor challenge">
        <h2>Verification required</h2>
        <p>Enter the 6-digit verification code to continue.</p>
        <form method="post" action="${target}">
          <label for="mfa-code">Verification code</label>
          <input id="mfa-code" name="code" type="text" inputmode="numeric" autocomplete="one-time-code" required />
          <button type="submit">Verify code</button>
        </form>
      </section>`,
  );
}

function p3HomePage(req) {
  const signedIn = p3Authorized(req)
    ? `<p id="session-state">Signed in as ${P3_USERNAME}.</p>`
    : "";
  return p3Page(
    p3VariantName(),
    `<h1 id="p3-home">P3 ${p3VariantName()}</h1>
      ${signedIn}
      <p>EzTest P3 two-system execution fixture (${P3_VARIANT}).</p>
      <nav aria-label="P3 navigation">
        <a href="/p3/orders">Orders</a>
        <a href="/p3/payments">Payments</a>
        <a href="/p3/timeout-window">Timeout window</a>
        <a href="/p3/slow">Slow page</a>
        <a href="/p3/2fa">Two-factor authentication</a>
        <a href="/p3/logout">Logout</a>
      </nav>`,
  );
}

function p3OrdersPage() {
  const rows = P3_ORDERS.map(
    (order) =>
      `<li><a href="/p3/orders/${order.id}">${order.number}</a> <span class="order-status">${order.status}</span> <span class="order-amount">${p3FormatAmount(order.amount)} (${order.amount} ${order.currency})</span></li>`,
  ).join("\n        ");
  return p3Page(
    "Orders",
    `<h1>P3 ${p3VariantName()} Orders</h1>
      <nav aria-label="P3 navigation">
        <a href="/p3/home">Home</a>
        <a href="/p3/payments">Payments</a>
      </nav>
      <ul id="orders">
        ${rows}
      </ul>`,
  );
}

function p3OrderPage(order) {
  return p3Page(
    `Order ${order.id}`,
    `<h1>P3 Order ${order.id}</h1>
      <dl>
        <dt>Number</dt><dd id="order-number">${order.number}</dd>
        <dt>Status</dt><dd id="order-status">${order.status}</dd>
        <dt>Amount</dt><dd id="order-amount">${p3FormatAmount(order.amount)}</dd>
        <dt>Refund amount</dt><dd id="refund-amount">${p3FormatAmount(P3_REFUND_AMOUNT)}</dd>
      </dl>
      <p id="refund-note">The refund amount does not match the paid amount.</p>
      <a href="/p3/orders">Order list</a>
      <a href="/p3/refund?order_id=${order.id}">Create refund</a>`,
  );
}

function p3OrderNotFoundPage() {
  return p3Page(
    "Order not found",
    `<h1>P3 Order not found</h1>
      <a href="/p3/orders">Order list</a>`,
  );
}

function p3RefundPage(orderId) {
  return p3Page(
    "Refund",
    `<h1>P3 Create refund</h1>
      <form method="post" action="/p3/refund">
        <input type="hidden" name="order_id" value="${orderId}" />
        <label for="refund-amount-input">Refund amount</label>
        <input id="refund-amount-input" name="amount" type="text" value="${P3_REFUND_AMOUNT}" required />
        <label for="refund-reason">Reason</label>
        <input id="refund-reason" name="reason" type="text" value="customer request" />
        <button type="submit">Create refund</button>
      </form>
      <a href="/p3/orders/1">Back to order</a>`,
  );
}

function p3PaymentsPage() {
  const rows = P3_REFUNDS.map((refund) => {
    const order = P3_ORDERS.find((candidate) => candidate.id === refund.orderId);
    const orderNumber = order ? order.number : "ORD-?";
    return `<li data-refund-number="${refund.refundNumber}"><span class="refund-number">${refund.refundNumber}</span> <span class="refund-amount">${p3FormatAmount(refund.amount)} (${refund.amount} ${refund.currency})</span> <span class="refund-status">${refund.status}</span> <a href="/p3/orders/${refund.orderId}">${orderNumber}</a></li>`;
  }).join("\n        ");
  return p3Page(
    "Payments",
    `<h1>P3 ${p3VariantName()} Payments</h1>
      <nav aria-label="P3 navigation">
        <a href="/p3/home">Home</a>
        <a href="/p3/orders">Orders</a>
      </nav>
      <ul id="refunds">
        ${rows}
      </ul>`,
  );
}

function p3ErrorPage(message) {
  return p3Page(
    "Error",
    `<h1>P3 Error</h1>
      <p class="error" role="alert">${message}</p>`,
  );
}

function p3OrdersPayload() {
  return {
    variant: P3_VARIANT,
    orders: P3_ORDERS.map((order) => ({
      id: order.id,
      order_number: order.number,
      amount: order.amount,
      currency: order.currency,
      status: order.status,
      amount_display: p3FormatAmount(order.amount),
    })),
  };
}

function p3PaymentsPayload() {
  return {
    variant: P3_VARIANT,
    refunds: P3_REFUNDS.map((refund) => ({
      id: refund.id,
      refund_number: refund.refundNumber,
      order_id: refund.orderId,
      amount: refund.amount,
      currency: refund.currency,
      status: refund.status,
    })),
  };
}

/** Creates one in-memory refund and returns it. */
function p3CreateRefund(orderId, amount) {
  p3RefundSequence += 1;
  const refund = {
    id: p3RefundSequence,
    refundNumber: "REF-" + (5000 + p3RefundSequence),
    orderId,
    amount: amount.toFixed(2),
    currency: "CNY",
    status: "created",
  };
  P3_REFUNDS.push(refund);
  return refund;
}

function p3OrderById(rawId) {
  const id = Number.parseInt(rawId, 10);
  return P3_ORDERS.find((order) => order.id === id) || null;
}

/** GET renders the form; POST validates credentials and the optional second factor. */
function handleP3LoginGet(req, res, mfa) {
  sendHtml(res, 200, p3LoginPage("", mfa));
}

async function handleP3LoginPost(req, res, mfa) {
  const raw = await readBody(req);
  const form = new URLSearchParams(raw);
  const code = form.get("code");
  if (code !== null) {
    // Second stage: the password was already accepted and no session exists yet.
    if (code.trim() === P3_MFA_CODE) {
      redirect(res, "/p3/home", { "Set-Cookie": p3SessionCookie() });
      return;
    }
    sendHtml(res, 401, p3MfaPage("Invalid verification code"));
    return;
  }
  const username = form.get("username") || "";
  const password = form.get("password") || "";
  if (username !== P3_USERNAME || password !== P3_PASSWORD) {
    sendHtml(res, 401, p3LoginPage("Invalid username or password", mfa));
    return;
  }
  if (mfa) {
    // Password accepted, but the second factor is outstanding: no session cookie
    // is issued so the execution Agent has to request the human takeover.
    sendHtml(res, 200, p3MfaPage(""));
    return;
  }
  redirect(res, "/p3/home", { "Set-Cookie": p3SessionCookie() });
}

async function handleP3TwoFactorPost(req, res) {
  const raw = await readBody(req);
  const code = new URLSearchParams(raw).get("code") || "";
  if (code.trim() !== P3_MFA_CODE) {
    sendHtml(res, 401, p3MfaPage("Invalid verification code", "/p3/2fa"));
    return;
  }
  redirect(res, "/p3/home", { "Set-Cookie": p3SessionCookie() });
}

async function handleP3RefundPost(req, res) {
  const raw = await readBody(req);
  const form = new URLSearchParams(raw);
  const orderId = Number.parseInt(form.get("order_id") || "", 10);
  const amount = Number.parseFloat((form.get("amount") || "").trim());
  const order = p3OrderById(orderId);
  if (order === null) {
    sendHtml(res, 400, p3ErrorPage("Unknown order for the refund"));
    return;
  }
  if (!Number.isFinite(amount) || amount <= 0) {
    sendHtml(res, 400, p3ErrorPage("Refund amount must be a positive number"));
    return;
  }
  p3CreateRefund(order.id, amount);
  redirect(res, "/p3/payments");
}

function p3TimeoutWindowPage(ms) {
  const countdownSeconds = Math.max(1, Math.ceil(ms / 1000));
  return p3Page(
    "Timeout window",
    `<h1>P3 Timeout Window</h1>
      <p id="window-meta" data-ms="${ms}">Closes after ${ms} ms.</p>
      <div id="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="confirm-title">
        <h2 id="confirm-title">Confirm refund</h2>
        <p>This confirmation window closes automatically in <span id="countdown">${countdownSeconds}</span> seconds.</p>
        <button id="confirm-button" type="button">Confirm</button>
      </div>
      <p id="window-expired" hidden></p>
      <script>
        (function () {
          var ms = ${ms};
          var remaining = ${countdownSeconds};
          var countdown = document.getElementById("countdown");
          var ticker = setInterval(function () {
            remaining -= 1;
            if (countdown && remaining > 0) countdown.textContent = String(remaining);
          }, 1000);
          setTimeout(function () {
            clearInterval(ticker);
            var dialog = document.getElementById("confirm-dialog");
            if (dialog && dialog.parentNode) dialog.parentNode.removeChild(dialog);
            var expired = document.getElementById("window-expired");
            if (expired) {
              expired.hidden = false;
              expired.textContent = "The confirmation window expired after " + ms + " ms.";
            }
          }, ms);
        })();
      </script>`,
  );
}

function p3SlowPage(delay) {
  return p3Page(
    "Slow",
    `<h1>P3 Slow</h1>
      <p id="slow-result">Slow response complete after ${delay} ms.</p>`,
  );
}

// --- handlers --------------------------------------------------------------

async function handleLogin(req, res) {
  const raw = await readBody(req);
  const form = new URLSearchParams(raw);
  const username = form.get("username") || "";
  const password = form.get("password") || "";
  if (username === USERNAME && password === PASSWORD) {
    redirect(res, "/account", {
      "Set-Cookie": `${SESSION_COOKIE}=${SESSION_VALUE}; HttpOnly; Path=/`,
    });
    return;
  }
  sendHtml(res, 401, loginPage("Invalid username or password"));
}

function handleAccount(req, res) {
  if (readCookie(req, SESSION_COOKIE) === SESSION_VALUE) {
    sendHtml(res, 200, ACCOUNT_PAGE);
    return;
  }
  redirect(res, "/login");
}

function handleProfile(req, res) {
  const authorization = req.headers.authorization;
  const authorized = typeof authorization === "string" && authorization.trim() === BEARER;
  if (authorized) {
    sendJson(res, 200, { name: USERNAME });
    return;
  }
  sendJson(res, 401, { error: "unauthorized" });
}

function handleP5Admin(req, res) {
  if (readCookie(req, SESSION_COOKIE) === SESSION_VALUE) {
    sendHtml(res, 200, P5_ADMIN_PAGE);
    return;
  }
  redirect(res, "/login");
}

function handleP5Orders(req, res) {
  sendJson(res, 200, { orders: P5_ORDERS });
}

function handleP5Order(req, res, order) {
  if (order === null) {
    sendJson(res, 404, { error: "order not found" });
    return;
  }
  sendJson(res, 200, order);
}

async function handleRequest(req, res) {
  let pathname;
  let requestUrl;
  try {
    requestUrl = new URL(req.url, "http://" + HOST + ":" + PORT);
    pathname = requestUrl.pathname;
  } catch {
    sendText(res, 400, "invalid request url");
    return;
  }

  if (pathname === "/health") {
    if (req.method !== "GET") {
      sendMethodNotAllowed(res, ["GET"]);
      return;
    }
    sendText(res, 200, "ok");
    return;
  }
  if (pathname === "/login") {
    if (req.method === "GET") {
      sendHtml(res, 200, loginPage(""));
      return;
    }
    if (req.method === "POST") {
      await handleLogin(req, res);
      return;
    }
    sendMethodNotAllowed(res, ["GET", "POST"]);
    return;
  }
  if (pathname === "/account") {
    if (req.method !== "GET") {
      sendMethodNotAllowed(res, ["GET"]);
      return;
    }
    handleAccount(req, res);
    return;
  }
  if (pathname === "/api/profile") {
    if (req.method !== "GET") {
      sendMethodNotAllowed(res, ["GET"]);
      return;
    }
    handleProfile(req, res);
    return;
  }

  // --- P5 additive exploration fixtures (design spec §10.2) ----------------
  // The routes below are new and leave every existing route untouched.
  if (pathname === "/p5/home" || pathname === "/p5/orders" || pathname === "/p5/admin" || pathname === "/p5/slow") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    if (pathname === "/p5/home") {
      sendHtml(res, 200, P5_HOME_PAGE);
      return;
    }
    if (pathname === "/p5/orders") {
      sendHtml(res, 200, P5_ORDERS_PAGE);
      return;
    }
    if (pathname === "/p5/slow") {
      // Test-only slow page so concurrent exploration runs overlap.
      setTimeout(() => sendHtml(res, 200, P5_HOME_PAGE), 2500);
      return;
    }
    handleP5Admin(req, res);
    return;
  }

  const p5OrderPagePath = /^\/p5\/orders\/(\d+)$/.exec(pathname);
  if (p5OrderPagePath !== null) {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    const order = p5OrderById(p5OrderPagePath[1]);
    if (order === null) {
      sendHtml(res, 404, p5OrderNotFoundPage());
      return;
    }
    sendHtml(res, 200, p5OrderPage(order));
    return;
  }

  if (pathname === "/api/orders") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    handleP5Orders(req, res);
    return;
  }

  const p5OrderAPIPath = /^\/api\/orders\/(\d+)$/.exec(pathname);
  if (p5OrderAPIPath !== null) {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    handleP5Order(req, res, p5OrderById(p5OrderAPIPath[1]));
    return;
  }

  // --- P6 additive execution fixtures (design spec §10.2) ------------------
  // The routes below are new and leave every existing route untouched.
  if (pathname === "/p6/login") {
    if (req.method !== "GET" && req.method !== "POST") {
      sendMethodNotAllowed(res, ["GET", "POST"]);
      return;
    }
    const mfa = requestUrl.searchParams.get("mfa") === "1";
    if (req.method === "GET") {
      handleP6LoginGet(req, res, mfa);
      return;
    }
    await handleP6LoginPost(req, res, mfa);
    return;
  }
  if (pathname === "/p6/logout") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    redirect(res, "/p6/login", {
      "Set-Cookie": `${P6_COOKIE}=; HttpOnly; Path=/; Max-Age=0; SameSite=Lax`,
    });
    return;
  }
  if (pathname === "/p6/slow") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    const rawMs = Number.parseInt(requestUrl.searchParams.get("ms") || "", 10);
    const delay = Number.isFinite(rawMs) ? Math.min(Math.max(rawMs, 0), 60000) : 8000;
    setTimeout(() => {
      sendHtml(
        res,
        200,
        `<!doctype html><html lang="en"><head><meta charset="utf-8" /><title>P6 Slow</title></head><body><main><h1>P6 Slow</h1><p id="slow-result">Slow response complete after ${delay} ms.</p></main></body></html>\n`,
      );
    }, delay);
    return;
  }

  const p6GuardedPage =
    pathname === "/p6/home" ||
    pathname === "/p6/orders" ||
    pathname === "/p6/orders/new" ||
    pathname === "/p6/assert-fail" ||
    pathname === "/p6/requires-auth";
  if (p6GuardedPage) {
    if (pathname === "/p6/orders/new") {
      if (req.method !== "GET" && req.method !== "POST" && req.method !== "HEAD") {
        sendMethodNotAllowed(res, ["GET", "POST", "HEAD"]);
        return;
      }
    } else if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    if (!p6Authorized(req)) {
      redirect(res, "/p6/login");
      return;
    }
    if (pathname === "/p6/home") {
      sendHtml(res, 200, P6_HOME_PAGE);
      return;
    }
    if (pathname === "/p6/orders") {
      sendHtml(res, 200, P6_ORDERS_PAGE);
      return;
    }
    if (pathname === "/p6/orders/new") {
      if (req.method === "POST") {
        const raw = await readBody(req);
        const form = new URLSearchParams(raw);
        const amount = Number.parseFloat(form.get("amount") || "0");
        const order = p6CreateOrder(form.get("number") || "", amount);
        redirect(res, `/p6/orders/${order.id}`);
        return;
      }
      sendHtml(res, 200, P6_NEW_ORDER_PAGE);
      return;
    }
    if (pathname === "/p6/assert-fail") {
      sendHtml(res, 200, P6_ASSERT_FAIL_PAGE);
      return;
    }
    sendHtml(res, 200, P6_REQUIRES_AUTH_PAGE);
    return;
  }

  const p6OrderPagePath = /^\/p6\/orders\/(\d+)$/.exec(pathname);
  if (p6OrderPagePath !== null) {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    if (!p6Authorized(req)) {
      redirect(res, "/p6/login");
      return;
    }
    const order = p6OrderById(p6OrderPagePath[1]);
    if (order === null) {
      sendHtml(res, 404, p6OrderNotFoundPage());
      return;
    }
    sendHtml(res, 200, p6OrderPage(order));
    return;
  }

  if (pathname === "/api/p6/orders") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    if (!p6Authorized(req)) {
      sendJson(res, 401, { error: "unauthorized" });
      return;
    }
    sendJson(res, 200, { orders: P6_ORDERS });
    return;
  }

  const p6OrderAPIPath = /^\/api\/p6\/orders\/(\d+)$/.exec(pathname);
  if (p6OrderAPIPath !== null) {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    if (!p6Authorized(req)) {
      sendJson(res, 401, { error: "unauthorized" });
      return;
    }
    const order = p6OrderById(p6OrderAPIPath[1]);
    if (order === null) {
      sendJson(res, 404, { error: "order not found" });
      return;
    }
    sendJson(res, 200, order);
    return;
  }

  // --- P3 additive two-system execution fixtures (design spec §13.1) ------
  // The routes below are new and leave every existing route untouched.
  if (pathname === "/p3/login") {
    if (req.method !== "GET" && req.method !== "POST" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "POST"]);
      return;
    }
    const mfa = requestUrl.searchParams.get("mfa") === "1";
    if (req.method === "POST") {
      await handleP3LoginPost(req, res, mfa);
      return;
    }
    handleP3LoginGet(req, res, mfa);
    return;
  }
  if (pathname === "/p3/logout") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    redirect(res, "/p3/login", {
      "Set-Cookie": `${P3_COOKIE}=; HttpOnly; Path=/; Max-Age=0; SameSite=Lax`,
    });
    return;
  }
  if (pathname === "/p3/2fa") {
    if (req.method !== "GET" && req.method !== "POST" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "POST"]);
      return;
    }
    if (req.method === "POST") {
      await handleP3TwoFactorPost(req, res);
      return;
    }
    sendHtml(res, 200, p3MfaPage("", "/p3/2fa"));
    return;
  }
  if (pathname === "/p3/home") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    if (!p3Authorized(req)) {
      redirect(res, "/p3/login");
      return;
    }
    sendHtml(res, 200, p3HomePage(req));
    return;
  }
  if (pathname === "/p3/orders") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    sendHtml(res, 200, p3OrdersPage());
    return;
  }
  if (pathname === "/p3/refund") {
    if (req.method !== "GET" && req.method !== "POST" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "POST"]);
      return;
    }
    if (req.method === "POST") {
      await handleP3RefundPost(req, res);
      return;
    }
    const rawOrderId = Number.parseInt(requestUrl.searchParams.get("order_id") || "1", 10);
    sendHtml(res, 200, p3RefundPage(Number.isFinite(rawOrderId) ? rawOrderId : 1));
    return;
  }
  if (pathname === "/p3/payments") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    sendHtml(res, 200, p3PaymentsPage());
    return;
  }
  if (pathname === "/p3/slow") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    const rawDelay = Number.parseInt(requestUrl.searchParams.get("ms") || "", 10);
    const delay = Number.isFinite(rawDelay) ? Math.min(Math.max(rawDelay, 0), 120000) : 8000;
    setTimeout(() => {
      sendHtml(res, 200, p3SlowPage(delay));
    }, delay);
    return;
  }
  if (pathname === "/p3/timeout-window") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    const rawWindow = Number.parseInt(requestUrl.searchParams.get("ms") || "", 10);
    const windowMs = Number.isFinite(rawWindow)
      ? Math.min(Math.max(rawWindow, 0), 300000)
      : 10000;
    sendHtml(res, 200, p3TimeoutWindowPage(windowMs));
    return;
  }

  const p3OrderPagePath = /^\/p3\/orders\/(\d+)$/.exec(pathname);
  if (p3OrderPagePath !== null) {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    const order = p3OrderById(p3OrderPagePath[1]);
    if (order === null) {
      sendHtml(res, 404, p3OrderNotFoundPage());
      return;
    }
    sendHtml(res, 200, p3OrderPage(order));
    return;
  }

  if (pathname === "/api/p3/orders") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    sendJson(res, 200, p3OrdersPayload());
    return;
  }
  if (pathname === "/api/p3/payments") {
    if (req.method !== "GET" && req.method !== "HEAD") {
      sendMethodNotAllowed(res, ["GET", "HEAD"]);
      return;
    }
    sendJson(res, 200, p3PaymentsPayload());
    return;
  }

  sendText(res, 404, "not found");
}

// --- server bootstrap ------------------------------------------------------

const server = http.createServer((req, res) => {
  handleRequest(req, res).catch((error) => {
    console.error("fixture-target: request failed: " + (error && error.stack ? error.stack : error));
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
  console.error("fixture-target: server error: " + error.message);
  process.exitCode = 1;
});

server.listen(PORT, HOST, () => {
  console.log("fixture-target listening on http://" + HOST + ":" + PORT);
  console.log("fixture-target variant: " + P3_VARIANT);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    server.close(() => process.exit(0));
  });
}

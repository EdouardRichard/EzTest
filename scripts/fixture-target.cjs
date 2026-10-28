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
// Existing routes and their behavior are unchanged.

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
  try {
    pathname = new URL(req.url, "http://" + HOST + ":" + PORT).pathname;
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
  if (pathname === "/p5/home" || pathname === "/p5/orders" || pathname === "/p5/admin") {
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
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    server.close(() => process.exit(0));
  });
}

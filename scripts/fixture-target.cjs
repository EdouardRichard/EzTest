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

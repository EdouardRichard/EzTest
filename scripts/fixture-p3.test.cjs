#!/usr/bin/env node
"use strict";

// Deterministic tests for the P3 two-system demo fixture in
// scripts/fixture-target.cjs (design spec §13.1, plan Task 13).
//
// The script starts the same fixture file twice - FIXTURE_VARIANT=orders and
// FIXTURE_VARIANT=payments - on process-unique loopback ports and exercises the
// HTTP contract the P3 acceptance flows rely on: login (including the payments
// FIXTURE_VARIANT ?mfa=1 two-stage challenge with code 246810), order pages,
// refund creation, the 120s-capable timeout window and 404 handling.
//
// Run:
//   node --test scripts/fixture-p3.test.cjs

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { after, before, test } = require("node:test");

const repoRoot = path.resolve(__dirname, "..");
const fixtureScript = path.join(__dirname, "fixture-target.cjs");
const logRoot = fs.mkdtempSync(path.join(os.tmpdir(), "eztest-p3-fixture-"));

const USERNAME = "fixture";
const PASSWORD = "fixture-pass";
const MFA_CODE = "246810";
const SESSION_COOKIE = "p3_session=ok";

let serverCounter = 0;
let orders = null;
let payments = null;

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// --- fixture bootstrap -----------------------------------------------------

async function startFixture(variant) {
  let lastLog = "";
  for (let attempt = 0; attempt < 10; attempt += 1) {
    serverCounter += 1;
    const port = 17000 + ((process.pid + serverCounter * 97 + attempt * 31) % 6000);
    const logPath = path.join(logRoot, variant + "-" + serverCounter + ".log");
    const logFd = fs.openSync(logPath, "w");
    const child = spawn(process.execPath, [fixtureScript], {
      cwd: repoRoot,
      env: { ...process.env, FIXTURE_PORT: String(port), FIXTURE_VARIANT: variant },
      stdio: ["ignore", logFd, logFd],
      windowsHide: true,
    });
    fs.closeSync(logFd);

    const base = "http://127.0.0.1:" + port;
    const deadline = Date.now() + 8000;
    while (Date.now() < deadline) {
      if (child.exitCode !== null) break;
      try {
        const response = await fetch(base + "/health");
        if (response.status === 200) {
          await response.text();
          return { base, child, port, variant };
        }
      } catch {
        // Not ready yet.
      }
      await sleep(50);
    }
    if (child.exitCode === null) child.kill();
    lastLog = fs.existsSync(logPath) ? fs.readFileSync(logPath, "utf8") : "";
    if (!/EADDRINUSE/.test(lastLog)) {
      throw new Error(variant + " fixture did not start on " + base + ":\n" + lastLog);
    }
  }
  throw new Error("could not find a free port for the " + variant + " fixture:\n" + lastLog);
}

async function stopFixture(server) {
  if (server === null || server.child.exitCode !== null) return;
  const exited = new Promise((resolve) => server.child.once("exit", resolve));
  server.child.kill();
  await Promise.race([exited, sleep(2000)]);
  if (server.child.exitCode === null) server.child.kill("SIGKILL");
}

before(async () => {
  orders = await startFixture("orders");
  payments = await startFixture("payments");
});

after(async () => {
  await stopFixture(payments);
  await stopFixture(orders);
});

// --- HTTP helpers ----------------------------------------------------------

function setCookies(response) {
  if (typeof response.headers.getSetCookie === "function") {
    return response.headers.getSetCookie();
  }
  return [];
}

function cookieValue(cookies, name) {
  for (const cookie of cookies) {
    const pair = cookie.split(";", 1)[0];
    const separator = pair.indexOf("=");
    if (separator < 0) continue;
    if (pair.slice(0, separator).trim() === name) {
      return pair.slice(separator + 1).trim();
    }
  }
  return null;
}

async function request(server, pathname, options = {}) {
  const headers = { ...(options.headers || {}) };
  if (options.cookie) headers.Cookie = options.cookie;
  const response = await fetch(server.base + pathname, {
    method: options.method || "GET",
    headers,
    body: options.body,
    redirect: "manual",
  });
  const body = await response.text();
  return {
    status: response.status,
    location: response.headers.get("location"),
    cookies: setCookies(response),
    body,
  };
}

function formBody(fields) {
  return new URLSearchParams(fields).toString();
}

async function postForm(server, pathname, fields, cookie) {
  return request(server, pathname, {
    method: "POST",
    cookie,
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: formBody(fields),
  });
}

async function login(server) {
  const response = await postForm(server, "/p3/login", {
    username: USERNAME,
    password: PASSWORD,
  });
  assert.equal(response.status, 302, "login must redirect: " + response.body);
  assert.equal(response.location, "/p3/home");
  assert.equal(cookieValue(response.cookies, "p3_session"), "ok");
  return SESSION_COOKIE;
}

async function loginWithMfa(server) {
  const first = await postForm(server, "/p3/login?mfa=1", {
    username: USERNAME,
    password: PASSWORD,
  });
  assert.equal(first.status, 200, "the password stage must render the MFA challenge");
  assert.match(first.body, /id="mfa"/);
  assert.match(first.body, /name="code"/);
  assert.ok(!first.body.includes(MFA_CODE), "the fixture page must not leak the 2FA code");
  assert.equal(cookieValue(first.cookies, "p3_session"), null, "no session before the second factor");

  const second = await postForm(server, "/p3/login?mfa=1", { code: MFA_CODE });
  assert.equal(second.status, 302);
  assert.equal(second.location, "/p3/home");
  assert.equal(cookieValue(second.cookies, "p3_session"), "ok");
  return SESSION_COOKIE;
}

// --- tests -----------------------------------------------------------------

test("legacy fixture routes are unchanged", async () => {
  const health = await request(orders, "/health");
  assert.equal(health.status, 200);
  assert.equal(health.body, "ok");

  const loginPage = await request(orders, "/login");
  assert.equal(loginPage.status, 200);
  assert.match(loginPage.body, /name="username"/);

  const profile = await request(orders, "/api/profile");
  assert.equal(profile.status, 401);
});

test("GET /p3/login renders the fixture login form", async () => {
  const response = await request(orders, "/p3/login");
  assert.equal(response.status, 200);
  assert.match(response.body, /<form method="post" action="\/p3\/login"/);
  assert.match(response.body, /id="username"/);
  assert.match(response.body, /id="password"/);
  assert.match(response.body, /fixture-pass|Sign in/);
});

test("POST /p3/login rejects a wrong password with .error", async () => {
  const response = await postForm(orders, "/p3/login", {
    username: USERNAME,
    password: "wrong-password",
  });
  assert.equal(response.status, 401);
  assert.match(response.body, /class="error"/);
  assert.match(response.body, /Invalid username or password/);
  assert.equal(cookieValue(response.cookies, "p3_session"), null);
});

test("orders login sets p3_session and guards /p3/home", async () => {
  const guarded = await request(orders, "/p3/home");
  assert.equal(guarded.status, 302);
  assert.equal(guarded.location, "/p3/login");

  const cookie = await login(orders);
  const home = await request(orders, "/p3/home", { cookie });
  assert.equal(home.status, 200);
  assert.match(home.body, /Order Center/);
  assert.match(home.body, /\/p3\/orders/);
});

test("payments login supports the ?mfa=1 two-stage challenge", async () => {
  const challenge = await request(payments, "/p3/login?mfa=1");
  assert.equal(challenge.status, 200);
  assert.match(challenge.body, /id="username"/);

  const wrongCode = await postForm(payments, "/p3/login?mfa=1", { code: "000000" });
  assert.equal(wrongCode.status, 401);
  assert.match(wrongCode.body, /class="error"/);
  assert.match(wrongCode.body, /Invalid verification code/);
  assert.equal(cookieValue(wrongCode.cookies, "p3_session"), null);

  const cookie = await loginWithMfa(payments);
  const home = await request(payments, "/p3/home", { cookie });
  assert.equal(home.status, 200);
  assert.match(home.body, /Payment Gateway/);
});

test("GET /p3/orders and /p3/orders/1 expose the P3 order data", async () => {
  const list = await request(orders, "/p3/orders");
  assert.equal(list.status, 200);
  assert.match(list.body, /ORD-1001/);
  assert.match(list.body, /5200\.00/);
  assert.match(list.body, /paid/);

  const detail = await request(orders, "/p3/orders/1");
  assert.equal(detail.status, 200);
  assert.match(detail.body, /¥5,200\.00/);
  assert.match(detail.body, /id="refund-amount"/);
  assert.match(detail.body, /¥5,000\.00/);

  const unknown = await request(orders, "/p3/orders/99");
  assert.equal(unknown.status, 404);
  assert.match(unknown.body, /not found/i);
});

test("POST /p3/refund creates a refund visible on the payments page", async () => {
  const seeded = await request(payments, "/p3/payments");
  assert.equal(seeded.status, 200);
  assert.match(seeded.body, /REF-5001/);
  assert.match(seeded.body, /5,000\.00/);
  assert.match(seeded.body, /created/);

  const created = await postForm(payments, "/p3/refund", {
    order_id: "1",
    amount: "5000.00",
    reason: "customer request",
  });
  assert.equal(created.status, 302, "refund creation must redirect: " + created.body);
  assert.equal(created.location, "/p3/payments");

  const page = await request(payments, "/p3/payments");
  assert.equal(page.status, 200);
  assert.match(page.body, /REF-5002/);
  assert.match(page.body, /5,000\.00/);

  const api = await request(payments, "/api/p3/payments");
  assert.equal(api.status, 200);
  const payload = JSON.parse(api.body);
  assert.ok(Array.isArray(payload.refunds), "the payments API must list refunds");
  assert.equal(payload.refunds.length, 2);
  assert.equal(payload.refunds[1].refund_number, "REF-5002");
  assert.equal(payload.refunds[1].amount, "5000.00");
  assert.equal(payload.refunds[1].status, "created");

  const ordersApi = await request(orders, "/api/p3/orders");
  assert.equal(ordersApi.status, 200);
  const ordersPayload = JSON.parse(ordersApi.body);
  assert.equal(ordersPayload.orders.length, 1);
  assert.equal(ordersPayload.orders[0].order_number, "ORD-1001");
  assert.equal(ordersPayload.orders[0].amount, "5200.00");
  assert.equal(ordersPayload.orders[0].status, "paid");
});

test("GET /p3/timeout-window renders a self-expiring confirmation dialog", async () => {
  const short = await request(orders, "/p3/timeout-window?ms=200");
  assert.equal(short.status, 200);
  assert.match(short.body, /id="confirm-dialog"/);
  assert.match(short.body, /id="window-expired"/);
  assert.match(short.body, /setTimeout/);
  assert.match(short.body, /Closes after 200 ms/);

  const long = await request(orders, "/p3/timeout-window?ms=120000");
  assert.equal(long.status, 200);
  assert.match(long.body, /Closes after 120000 ms/);
  assert.match(long.body, /id="window-expired"/);
});

test("GET /p3/slow honours the requested delay", async () => {
  const started = Date.now();
  const response = await request(orders, "/p3/slow?ms=120");
  const elapsed = Date.now() - started;
  assert.equal(response.status, 200);
  assert.ok(elapsed >= 100, "the slow page must wait for the requested delay, got " + elapsed + "ms");
  assert.match(response.body, /Slow response complete after 120 ms/);
});

test("GET/POST /p3/2fa handles the standalone MFA page", async () => {
  const page = await request(payments, "/p3/2fa");
  assert.equal(page.status, 200);
  assert.match(page.body, /id="mfa"/);
  assert.match(page.body, /id="mfa-code"/);
  assert.ok(!page.body.includes(MFA_CODE), "the 2FA page must not leak the code");

  const wrong = await postForm(payments, "/p3/2fa", { code: "111111" });
  assert.equal(wrong.status, 401);
  assert.match(wrong.body, /class="error"/);

  const accepted = await postForm(payments, "/p3/2fa", { code: MFA_CODE });
  assert.equal(accepted.status, 302);
  assert.equal(accepted.location, "/p3/home");
  assert.equal(cookieValue(accepted.cookies, "p3_session"), "ok");
});

test("unknown P3 routes answer 404", async () => {
  for (const pathname of ["/p3/nope", "/p3/orders/abc", "/api/p3/nope"]) {
    const response = await request(orders, pathname);
    assert.equal(response.status, 404, "expected 404 for " + pathname);
  }
});

import test, { after, before } from "node:test"
import assert from "node:assert/strict"
import http from "node:http"
import { once } from "node:events"

import { createExploreRuntime } from "../src/actions/explore.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-session-token"

const LOGIN_HTML = `<!doctype html>
<html>
  <head><title>Login</title></head>
  <body>
    <h1>Login</h1>
    <input id="username" aria-label="Username" />
    <input id="password" type="password" aria-label="Password" />
    <button id="submit" onclick="
      document.cookie = 'sid=session-42; path=/';
      localStorage.setItem('token', 'local-token');
      sessionStorage.setItem('flag', 'session-flag');
      document.getElementById('login-form').remove();
      const done = document.createElement('div');
      done.id = 'logged-in';
      done.textContent = 'logged-in';
      document.body.appendChild(done);
      console.log('signed-in');
    ">Log in</button>
    <form id="login-form"></form>
  </body>
</html>`

const STATE_HTML = `<!doctype html>
<html>
  <head><title>State</title></head>
  <body>
    <div id="state"></div>
    <script>
      document.getElementById('state').textContent = JSON.stringify({
        cookie: document.cookie,
        token: localStorage.getItem('token') || '',
        flag: sessionStorage.getItem('flag') || '',
      })
    </script>
  </body>
</html>`

let pageServer
let explore
let server
let request

before(async () => {
  pageServer = http.createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1")
    if (url.pathname === "/login") {
      res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
      res.end(LOGIN_HTML)
      return
    }
    if (url.pathname === "/state") {
      res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
      res.end(STATE_HTML)
      return
    }
    res.writeHead(404, { "content-type": "text/plain" })
    res.end("not found")
  })
  pageServer.listen(0, "127.0.0.1")
  await once(pageServer, "listening")
  pageServer.url = `http://127.0.0.1:${pageServer.address().port}`

  explore = createExploreRuntime({ logger: null })
  server = createExecutorServer({ token: TOKEN, explore, logger: null, version: "test" })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  request = httpClient(`http://127.0.0.1:${port}`, TOKEN)
})

after(async () => {
  await explore.close()
  await new Promise((resolve) => server.close(resolve))
  await new Promise((resolve) => pageServer.close(resolve))
})

async function act(action, args = {}) {
  return request("/v1/explore/actions", { method: "POST", body: { action, args } })
}

async function expectOK(action, args = {}) {
  const response = await act(action, args)
  assert.equal(response.status, 200, `${action}: ${response.text}`)
  assert.equal(response.json.ok, true, `${action}: ${response.text}`)
  return response.json.result
}

async function loginAndCapture() {
  const opened = await expectOK("explore.open", {
    url: `${pageServer.url}/login`,
    allowed_hosts: ["127.0.0.1"],
  })
  const pageId = opened.page_id
  await expectOK("explore.fill", { page_id: pageId, selector: "#username", value: "alice" })
  await expectOK("explore.fill", {
    page_id: pageId,
    selector: "#password",
    value: "s3cret-pw",
    secret: true,
  })
  await expectOK("explore.click", { page_id: pageId, selector: "#submit" })
  return expectOK("explore.snapshot", { page_id: pageId, include_storage_state: true })
}

test("storage state is captured and reused by a fresh explore page", async () => {
  const captured = await loginAndCapture()
  assert.match(captured.storage_state_ref, /^ss_/)
  assert.ok(Array.isArray(captured.storage_state.cookies), JSON.stringify(captured.storage_state))
  const cookie = captured.storage_state.cookies.find((item) => item.name === "sid")
  assert.equal(cookie?.value, "session-42")
  const origin = captured.storage_state.origins.find((item) => item.origin === pageServer.url)
  assert.ok(origin, "missing page origin in captured storage state")
  assert.deepEqual(origin.localStorage, [{ name: "token", value: "local-token" }])
  assert.deepEqual(origin.sessionStorage, [{ name: "flag", value: "session-flag" }])

  // Capturing the same page again yields the same stable reference.
  const again = await expectOK("explore.snapshot", {
    page_id: captured.page_id,
    include_storage_state: true,
  })
  assert.equal(again.storage_state_ref, captured.storage_state_ref)

  const restored = await expectOK("explore.open", {
    url: `${pageServer.url}/state`,
    allowed_hosts: ["127.0.0.1"],
    storage_state_ref: captured.storage_state_ref,
  })
  assert.notEqual(restored.page_id, captured.page_id)
  const state = await expectOK("explore.extract", {
    page_id: restored.page_id,
    selector: "#state",
  })
  const snapshot = JSON.parse(state.items[0].text)
  assert.equal(snapshot.cookie, "sid=session-42")
  assert.equal(snapshot.token, "local-token")
  assert.equal(snapshot.flag, "session-flag")
})

test("storage state can be injected inline on explore.open", async () => {
  const captured = await loginAndCapture()
  const restored = await expectOK("explore.open", {
    url: `${pageServer.url}/state`,
    allowed_hosts: ["127.0.0.1"],
    storage_state: captured.storage_state,
  })
  const state = await expectOK("explore.extract", {
    page_id: restored.page_id,
    selector: "#state",
  })
  const snapshot = JSON.parse(state.items[0].text)
  assert.equal(snapshot.cookie, "sid=session-42")
  assert.equal(snapshot.token, "local-token")
  assert.equal(snapshot.flag, "session-flag")
})

test("an unknown storage_state_ref is rejected with 400 invalid_arguments", async () => {
  const response = await act("explore.open", {
    url: `${pageServer.url}/login`,
    allowed_hosts: ["127.0.0.1"],
    storage_state_ref: "ss_does_not_exist",
  })
  assert.equal(response.status, 400, response.text)
  assert.equal(response.json.ok, false)
  assert.equal(response.json.error.code, "invalid_arguments")
})

test("storage_state_ref and inline storage_state cannot be combined", async () => {
  const captured = await loginAndCapture()
  const response = await act("explore.open", {
    url: `${pageServer.url}/state`,
    allowed_hosts: ["127.0.0.1"],
    storage_state_ref: captured.storage_state_ref,
    storage_state: captured.storage_state,
  })
  assert.equal(response.status, 400, response.text)
  assert.equal(response.json.error.code, "invalid_arguments")
})

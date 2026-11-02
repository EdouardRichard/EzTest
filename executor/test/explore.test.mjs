import test, { after, before } from "node:test"
import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"

import { EXPLORE_ACTIONS, createExploreRuntime } from "../src/actions/explore.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { httpClient, startLocalPage } from "./helpers.mjs"

const TOKEN = "test-explore-token"

let pageServer
let explore
let server
let request

before(async () => {
  pageServer = await startLocalPage()
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
  await pageServer.close()
})

async function act(action, args = {}) {
  return request("/v1/explore/actions", { method: "POST", body: { action, args } })
}

async function openPage(path = "/home") {
  const opened = await act("explore.open", {
    url: `${pageServer.url}${path}`,
    allowedHosts: ["127.0.0.1"],
  })
  assert.equal(opened.status, 200, opened.text)
  assert.equal(opened.json.ok, true, opened.text)
  return opened.json.result
}

test("explore actions run the full open→snapshot→click→fill→extract→screenshot chain", async () => {
  const opened = await openPage("/home")
  const pageId = opened.page_id
  assert.match(pageId, /^page_/)
  assert.equal(opened.route, "/home")
  assert.equal(opened.url, `${pageServer.url}/home`)
  assert.equal(opened.dom_snapshot.title, "EzTest Home")
  assert.match(opened.dom_snapshot.aria, /Login/)
  assert.ok(Array.isArray(opened.dom_snapshot.elements))
  assert.ok(
    opened.dom_snapshot.elements.some(
      (element) => element.selector === "#login-btn" && element.tag === "button",
    ),
    `missing #login-btn in ${JSON.stringify(opened.dom_snapshot.elements)}`,
  )
  assert.ok(
    opened.requests.some((item) => item.url === `${pageServer.url}/home` && item.status === 200),
    `missing navigation request in ${JSON.stringify(opened.requests)}`,
  )
  assert.ok(
    opened.console.some((item) => item.text.includes("console-hello")),
    `missing console-hello in ${JSON.stringify(opened.console)}`,
  )

  const snapshot = await act("explore.snapshot", { page_id: pageId })
  assert.equal(snapshot.status, 200, snapshot.text)
  assert.equal(snapshot.json.ok, true, snapshot.text)
  assert.equal(snapshot.json.result.page_id, pageId)
  assert.equal(snapshot.json.result.route, "/home")
  assert.ok(snapshot.json.result.dom_snapshot.element_count >= 4)
  assert.ok(
    snapshot.json.result.dom_snapshot.elements.some(
      (element) => element.selector === "#username" && element.tag === "input",
    ),
  )

  const clicked = await act("explore.click", { page_id: pageId, selector: "#login-btn" })
  assert.equal(clicked.status, 200, clicked.text)
  assert.equal(clicked.json.ok, true, clicked.text)
  assert.equal(clicked.json.result.selector, "#login-btn")
  assert.equal(clicked.json.result.index, 0)
  assert.ok(
    clicked.json.result.console.some((item) => item.text.includes("clicked:login")),
    `missing clicked:login in ${JSON.stringify(clicked.json.result.console)}`,
  )

  const filled = await act("explore.fill", {
    page_id: pageId,
    selector: "#username",
    value: "alice",
  })
  assert.equal(filled.status, 200, filled.text)
  assert.equal(filled.json.ok, true, filled.text)
  assert.equal(filled.json.result.value_set, true)
  assert.equal(filled.json.result.secret, false)

  const clickedText = await act("explore.extract", { page_id: pageId, selector: "#clicked" })
  assert.equal(clickedText.status, 200, clickedText.text)
  assert.equal(clickedText.json.ok, true, clickedText.text)
  assert.equal(clickedText.json.result.items.length, 1)
  assert.equal(clickedText.json.result.items[0].tag, "div")
  assert.equal(clickedText.json.result.items[0].text, "login")

  const username = await act("explore.extract", { page_id: pageId, selector: "#username" })
  assert.equal(username.json.ok, true, username.text)
  assert.equal(username.json.result.items[0].value, "alice")
  assert.ok(username.json.result.dom_snapshot.title === "EzTest Home")

  const shot = await act("explore.screenshot", { page_id: pageId, full_page: false })
  assert.equal(shot.status, 200, shot.text)
  assert.equal(shot.json.ok, true, shot.text)
  assert.equal(shot.json.result.page_id, pageId)
  assert.equal(shot.json.result.screenshot_ref.kind, "screenshot")
  assert.equal(shot.json.result.screenshot_ref.mime, "image/png")
  assert.match(shot.json.result.screenshot_ref.sha256, /^[0-9a-f]{64}$/)
  assert.ok(Number.isInteger(shot.json.result.screenshot_ref.bytes))
  assert.equal(shot.json.artifacts.length, 1)
  assert.equal(shot.json.artifacts[0].kind, "screenshot")
  assert.equal(shot.json.artifacts[0].mime, "image/png")
  assert.equal(shot.json.artifacts[0].sha256, shot.json.result.screenshot_ref.sha256)
})

test("unknown explore actions are rejected with 400 invalid_arguments", async () => {
  for (const action of ["explore.bash", "explore.teleport", "bash", "shell", "explore.exec"]) {
    const response = await act(action, { page_id: "page_1", command: "whoami" })
    assert.equal(response.status, 400, `${action}: ${response.text}`)
    assert.equal(response.json.ok, false)
    assert.equal(response.json.error.code, "invalid_arguments")
  }
})

test("explore actions report page_not_found for an unknown page_id", async () => {
  const response = await act("explore.snapshot", { page_id: "page_missing" })
  assert.equal(response.status, 200, response.text)
  assert.equal(response.json.ok, false)
  assert.equal(response.json.error.code, "page_not_found")
})

test("explore outputs redact fill(secret=true) values", async () => {
  const opened = await openPage("/secret-leak")
  const pageId = opened.page_id
  const secret = "hunter2-explore-secret"

  const filled = await act("explore.fill", {
    page_id: pageId,
    selector: "#secret-input",
    value: secret,
    secret: true,
  })
  assert.equal(filled.status, 200, filled.text)
  assert.equal(filled.json.ok, true, filled.text)
  assert.equal(filled.json.result.secret, true)
  assert.ok(!filled.text.includes(secret), "fill response leaked the secret")

  const clicked = await act("explore.click", { page_id: pageId, selector: "#leak-secret" })
  assert.equal(clicked.json.ok, true, clicked.text)
  assert.ok(!clicked.text.includes(secret), "click response leaked the secret")

  const snapshot = await act("explore.snapshot", { page_id: pageId })
  assert.equal(snapshot.json.ok, true, snapshot.text)
  assert.ok(!snapshot.text.includes(secret), "snapshot leaked the secret")

  const extracted = await act("explore.extract", { page_id: pageId, selector: "#secret-out" })
  assert.equal(extracted.json.ok, true, extracted.text)
  assert.equal(extracted.json.result.items[0].text, "***")
  assert.ok(!extracted.text.includes(secret), "extract leaked the secret")

  const console = await act("explore.snapshot", { page_id: pageId })
  const consoleText = JSON.stringify(console.json.result.console)
  assert.ok(consoleText.includes("leak:***"), `console was not redacted: ${consoleText}`)
})

test("explore action timeouts are reported and the page stays usable", async () => {
  const opened = await openPage("/home")
  const pageId = opened.page_id
  const timedOut = await act("explore.click", {
    page_id: pageId,
    selector: "#does-not-exist",
    timeout_ms: 50,
  })
  assert.equal(timedOut.status, 200, timedOut.text)
  assert.equal(timedOut.json.ok, false)
  assert.ok(
    ["timeout", "executor_timeout"].includes(timedOut.json.error.code),
    `unexpected error code ${timedOut.json.error.code}`,
  )
  const snapshot = await act("explore.snapshot", { page_id: pageId })
  assert.equal(snapshot.json.ok, true, snapshot.text)
  assert.equal(snapshot.json.result.page_id, pageId)
})

test("the explore action family is closed and has no shell path", async () => {
  assert.deepEqual([...EXPLORE_ACTIONS], [
    "explore.open",
    "explore.snapshot",
    "explore.click",
    "explore.fill",
    "explore.extract",
    "explore.screenshot",
  ])
  const source = await readFile(new URL("../src/actions/explore.mjs", import.meta.url), "utf8")
  assert.ok(
    !/node:child_process|\bexecSync\b|\bexecFile\b|\bspawnSync\b|\bspawn\b/.test(source),
    "explore actions must not import or spawn a shell/command",
  )
})

test("explore.cancel aborts an in-flight action", async () => {
  const pending = act("explore.open", {
    url: `${pageServer.url}/slow`,
    allowedHosts: ["127.0.0.1"],
    timeout_ms: 5000,
  })
  await new Promise((resolve) => setTimeout(resolve, 300))
  const canceled = await request("/v1/explore/cancel", { method: "POST" })
  assert.equal(canceled.status, 200, canceled.text)
  assert.equal(canceled.json.ok, true)
  assert.equal(canceled.json.result.canceled, true)

  const outcome = await pending
  assert.equal(outcome.status, 200, outcome.text)
  assert.equal(outcome.json.ok, false)
  assert.equal(outcome.json.error.code, "canceled")
})

import test, { after, before } from "node:test"
import assert from "node:assert/strict"
import crypto from "node:crypto"

import { ContextManager } from "../src/contexts.mjs"
import { executeAction, parseTarget } from "../src/actions.mjs"
import { startLocalPage } from "./helpers.mjs"

let pageServer
let manager

before(async () => {
  pageServer = await startLocalPage()
  manager = new ContextManager({ maxContexts: 4, logger: null })
})

after(async () => {
  await manager.close()
  await pageServer.close()
})

async function openContext(overrides = {}) {
  return manager.createContext({
    key: "test:context",
    allowedHosts: ["127.0.0.1"],
    baseUrl: pageServer.url,
    ...overrides,
  })
}

async function withContext(run, overrides = {}) {
  const context = await openContext(overrides)
  try {
    return await run(context)
  } finally {
    await manager.deleteContext(context.contextId).catch(() => {})
  }
}

function act(contextId, action, args = {}) {
  return manager.runAction(contextId, { action, args })
}

async function waitFor(check, timeoutMs, label) {
  const started = Date.now()
  while (Date.now() - started < timeoutMs) {
    if (check()) {
      return
    }
    await new Promise((resolve) => setTimeout(resolve, 50))
  }
  assert.fail(`condition not met within ${timeoutMs}ms: ${label}`)
}

test("parseTarget accepts exactly one locator kind", () => {
  assert.deepEqual(parseTarget({ role: "button", name: "Login", exact: true }), {
    kind: "role",
    role: "button",
    name: "Login",
    exact: true,
  })
  assert.deepEqual(parseTarget({ text: "Hi" }), { kind: "text", text: "Hi" })
  assert.deepEqual(parseTarget({ test_id: "node" }), { kind: "test_id", testId: "node" })
  assert.deepEqual(parseTarget({ selector: "#id" }), { kind: "selector", selector: "#id" })
})

test("parseTarget rejects empty, multi-key and malformed targets", () => {
  for (const target of [
    undefined,
    {},
    { role: "button", selector: "#id" },
    { text: "hi", test_id: "x" },
    { name: "Login" },
    { exact: true },
    { role: "" },
    { text: 42 },
  ]) {
    assert.throws(
      () => parseTarget(target),
      (error) => error.code === "invalid_arguments",
      `expected invalid_arguments for ${JSON.stringify(target)}`,
    )
  }
})

test("goto returns the final url and status", async () => {
  await withContext(async (context) => {
    const response = await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    assert.equal(response.ok, true)
    assert.equal(response.result.url, `${pageServer.url}/home`)
    assert.equal(response.result.status, 200)
  })
})

test("goto rejects a host outside allowedHosts before navigation", async () => {
  await withContext(async (context) => {
    const response = await act(context.contextId, "goto", { url: "http://example.com/" })
    assert.equal(response.ok, false)
    assert.equal(response.error.code, "host_not_allowed")
  })
})

test("goto maps connection failures and blocked redirects", async () => {
  await withContext(async (context) => {
    const refused = await act(context.contextId, "goto", { url: "http://127.0.0.1:65534/" })
    assert.equal(refused.ok, false)
    assert.equal(refused.error.code, "connection_failed")

    const redirected = await act(context.contextId, "goto", { url: `${pageServer.url}/redirect-blocked` })
    assert.equal(redirected.ok, false)
    assert.equal(redirected.error.code, "host_not_allowed")
  })
})

test("goto reports non-2xx status without failing the action", async () => {
  await withContext(async (context) => {
    const response = await act(context.contextId, "goto", { url: `${pageServer.url}/boom` })
    assert.equal(response.ok, true)
    assert.equal(response.result.status, 500)
  })
})

test("subresources outside allowedHosts are blocked and reported", async () => {
  await withContext(async (context) => {
    const response = await act(context.contextId, "goto", { url: `${pageServer.url}/mixed` })
    assert.equal(response.ok, true, JSON.stringify(response.error))
    assert.ok(
      (response.result.blocked_hosts ?? []).some((url) => url.includes("localhost:")),
      JSON.stringify(response.result),
    )
  })
})

test("window.open to a host outside allowedHosts is blocked", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/popup` })
    pageServer.clearHits()
    const clicked = await act(context.contextId, "click", { target: { selector: "#open-popup" } })
    assert.equal(clicked.ok, true, JSON.stringify(clicked.error))
    // Give the popup time to try to navigate; a bypass would hit the server.
    await new Promise((resolve) => setTimeout(resolve, 750))
    assert.equal(
      pageServer.hits.includes("/popup-target"),
      false,
      "window.open request reached the disallowed host",
    )
    const after = await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    assert.equal(after.ok, true)
  })
})

test("window.open cannot use an allowed redirect to reach a blocked host", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/popup-redirect` })
    pageServer.clearHits()
    const clicked = await act(context.contextId, "click", {
      target: { selector: "#open-popup-redirect" },
    })
    assert.equal(clicked.ok, true, JSON.stringify(clicked.error))
    await new Promise((resolve) => setTimeout(resolve, 750))
    // Popup navigations are refused before their first request, so neither the
    // allowed URL nor its disallowed 302 target is fetched.
    assert.equal(pageServer.hits.includes("/second"), false, "popup redirect bypassed the allow list")
    assert.equal(pageServer.hits.includes("/redirect-blocked"), false)
  })
})

test("iframe outside allowedHosts is blocked and reported", async () => {
  await withContext(async (context) => {
    const response = await act(context.contextId, "goto", { url: `${pageServer.url}/iframe-mixed` })
    assert.equal(response.ok, true, JSON.stringify(response.error))
    assert.ok(
      (response.result.blocked_hosts ?? []).some((url) => url.includes("localhost:")),
      JSON.stringify(response.result),
    )
    await new Promise((resolve) => setTimeout(resolve, 200))
    assert.equal(
      pageServer.hits.includes("/iframe-target"),
      false,
      "iframe request reached the disallowed host",
    )
  })
})

test("service workers are blocked so they cannot bypass the allow list", async () => {
  await withContext(async (context) => {
    const page = await act(context.contextId, "goto", { url: `${pageServer.url}/swtest` })
    assert.equal(page.ok, true, JSON.stringify(page.error))
    pageServer.clearHits()
    const waited = await act(context.contextId, "wait_for", {
      text: "sw-blocked",
      timeout_ms: 5_000,
    })
    assert.equal(waited.ok, true, JSON.stringify(waited.error))
    // A controlling service worker would fetch http://localhost:<port>/second,
    // which must never reach the server.
    await new Promise((resolve) => setTimeout(resolve, 300))
    assert.equal(pageServer.hits.includes("/second"), false)
  })
})

test("page WebSockets outside allowedHosts are recorded, closed and fail the action", async () => {
  await withContext(async (context) => {
    const response = await act(context.contextId, "goto", { url: `${pageServer.url}/wstest` })
    assert.equal(response.ok, false, JSON.stringify(response))
    assert.equal(response.error.code, "host_not_allowed")

    // The page observes the policy-violation close (1008), proving the guard
    // closed the routed socket instead of merely failing the action.
    await new Promise((resolve) => setTimeout(resolve, 250))
    const consoleResult = await act(context.contextId, "console", { tail: 20 })
    assert.equal(consoleResult.ok, true)
    assert.ok(
      consoleResult.result.items.some((item) => item.text === "ws-close:1008"),
      JSON.stringify(consoleResult.result),
    )

    const network = await act(context.contextId, "network", { tail: 20 })
    const rejected = network.result.items.find((item) => item.error === "host_not_allowed")
    assert.ok(rejected, JSON.stringify(network.result))
    assert.match(rejected.url, /blocked-ws/)
  })
})

test("workers are blocked so their WebSockets cannot bypass the allow list", async () => {
  await withContext(async (context) => {
    const page = await act(context.contextId, "goto", { url: `${pageServer.url}/workertest` })
    assert.equal(page.ok, true, JSON.stringify(page.error))
    const waited = await act(context.contextId, "wait_for", {
      text: "worker-blocked,sharedworker-blocked",
      timeout_ms: 5_000,
    })
    assert.equal(waited.ok, true, JSON.stringify(waited.error))

    // The blob worker's socket targets an allowed host and would reach the
    // server if Playwright routed worker WebSockets (it does not) or if worker
    // creation were still possible.
    await new Promise((resolve) => setTimeout(resolve, 300))
    assert.deepEqual(pageServer.upgrades, [], "a worker WebSocket bypassed the guard")
    const consoleResult = await act(context.contextId, "console", { tail: 20 })
    assert.ok(
      consoleResult.result.items.some((item) =>
        item.text.startsWith("worker-guard:worker-blocked,sharedworker-blocked"),
      ),
      JSON.stringify(consoleResult.result),
    )
    // The guard itself records the blocked constructors, so the violation stays
    // visible even if the page swallows the exception.
    assert.ok(
      consoleResult.result.items.some((item) => item.text.includes("worker_blocked")),
      JSON.stringify(consoleResult.result),
    )
  })
})

test("goto refuses file:// urls", async () => {
  await withContext(async (context) => {
    const response = await act(context.contextId, "goto", { url: "file:///C:/Windows/win.ini" })
    assert.equal(response.ok, false)
    assert.equal(response.error.code, "host_not_allowed")
  })
})

test("snapshot returns ARIA yaml without password values", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const response = await act(context.contextId, "snapshot")
    assert.equal(response.ok, true)
    assert.equal(response.result.title, "EzTest Home")
    assert.match(response.result.url, /\/home$/)
    assert.match(response.result.aria, /EzTest Home/)
    assert.equal(response.result.truncated, false)
    assert.equal(response.result.aria.includes("top-secret-pw"), false)
  })
})

test("snapshot honours max_chars and marks truncation", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const response = await act(context.contextId, "snapshot", { max_chars: 100 })
    assert.equal(response.ok, true)
    assert.equal(response.result.truncated, true)
    assert.equal(response.result.aria.length, 100)
  })
})

test("click resolves role, text, test_id and selector targets", async () => {
  await withContext(async (context) => {
    const cases = [
      [{ role: "button", name: "Login", exact: true }, "clicked:login"],
      [{ text: "Text target" }, "clicked:text"],
      [{ test_id: "test-id-node" }, "clicked:testid"],
      [{ selector: "#selector-node" }, "clicked:selector"],
    ]
    for (const [target, marker] of cases) {
      await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
      const clicked = await act(context.contextId, "click", { target })
      assert.equal(clicked.ok, true, JSON.stringify(clicked.error))
      const consoleResult = await act(context.contextId, "console", { tail: 10 })
      assert.equal(
        consoleResult.result.items.some((item) => item.text.includes(marker)),
        true,
        `missing ${marker}`,
      )
    }
  })
})

test("target resolution reports not found and ambiguous matches", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })

    const missing = await act(context.contextId, "click", { target: { selector: "#missing" } })
    assert.equal(missing.ok, false)
    assert.equal(missing.error.code, "target_not_found")

    const ambiguousRole = await act(context.contextId, "click", { target: { role: "button" } })
    assert.equal(ambiguousRole.ok, false)
    assert.equal(ambiguousRole.error.code, "target_ambiguous")

    const ambiguousText = await act(context.contextId, "click", { target: { text: "duplicate value" } })
    assert.equal(ambiguousText.ok, false)
    assert.equal(ambiguousText.error.code, "target_ambiguous")

    const multiKey = await act(context.contextId, "click", {
      target: { role: "button", selector: "#login-btn" },
    })
    assert.equal(multiKey.ok, false)
    assert.equal(multiKey.error.code, "invalid_arguments")

    const unknownAction = await act(context.contextId, "teleport")
    assert.equal(unknownAction.ok, false)
    assert.equal(unknownAction.error.code, "invalid_arguments")
  })
})

test("fill secret values are scrubbed from console, network, snapshot and results", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/secret-leak` })
    const secret = "s3cr3t-fill-value"
    const filled = await act(context.contextId, "fill", {
      target: { selector: "#secret-input" },
      value: secret,
      secret: true,
    })
    assert.equal(filled.ok, true)
    assert.equal(JSON.stringify(filled).includes(secret), false)

    // The page echoes the typed value into the console, a network URL and the
    // DOM, so each result would contain the secret without scrubbing.
    const clicked = await act(context.contextId, "click", { target: { selector: "#leak-secret" } })
    assert.equal(clicked.ok, true, JSON.stringify(clicked.error))
    await new Promise((resolve) => setTimeout(resolve, 300))

    const consoleResult = await act(context.contextId, "console", { tail: 50 })
    const consoleJson = JSON.stringify(consoleResult)
    assert.equal(consoleJson.includes(secret), false, consoleJson)
    assert.equal(consoleJson.includes("***"), true, "secret console message should be masked")

    const networkResult = await act(context.contextId, "network", { tail: 50 })
    const networkJson = JSON.stringify(networkResult)
    assert.equal(networkJson.includes(secret), false, networkJson)
    const echo = networkResult.result.items.find((item) => item.url.includes("/echo"))
    assert.ok(echo, "missing /echo network entry")
    assert.equal(echo.url.includes(secret), false)
    assert.equal(echo.url.includes("***"), true)

    const snapshot = await act(context.contextId, "snapshot")
    assert.equal(snapshot.result.aria.includes(secret), false)
    assert.equal(snapshot.result.aria.includes("***"), true)
  })
})

test("action errors scrub previously registered secret values", async () => {
  const secret = "error-secret-value"
  const entry = {
    secrets: new Set([secret]),
    allowedHosts: ["127.0.0.1"],
    allowDataPages: false,
    page: {
      goto: async () => {
        throw new Error(`navigation failed with ${secret}`)
      },
    },
  }
  await assert.rejects(
    () => executeAction(entry, { action: "goto", args: { url: "http://127.0.0.1/" } }),
    (error) => {
      assert.equal(error.code, "internal")
      assert.equal(error.message.includes(secret), false)
      assert.equal(error.message.includes("***"), true)
      return true
    },
  )
})

test("fill and press work on resolved targets", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const filled = await act(context.contextId, "fill", {
      target: { selector: "#username" },
      value: "alice",
    })
    assert.equal(filled.ok, true)

    const pressed = await act(context.contextId, "press", {
      target: { selector: "#username" },
      key: "c",
    })
    assert.equal(pressed.ok, true)

    const globalPress = await act(context.contextId, "press", { key: "Tab" })
    assert.equal(globalPress.ok, true)
  })
})

test("wait_for supports target, text and url_contains conditions", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const byTarget = await act(context.contextId, "wait_for", {
      target: { test_id: "test-id-node" },
    })
    assert.equal(byTarget.ok, true)

    const byUrl = await act(context.contextId, "wait_for", { url_contains: "/home" })
    assert.equal(byUrl.ok, true)

    await act(context.contextId, "goto", { url: `${pageServer.url}/second` })
    const byText = await act(context.contextId, "wait_for", { text: "Second page" })
    assert.equal(byText.ok, true)
  })
})

test("wait_for times out on a missing target", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const started = Date.now()
    const response = await act(context.contextId, "wait_for", {
      target: { selector: "#never" },
      timeout_ms: 300,
    })
    assert.equal(response.ok, false)
    assert.equal(response.error.code, "timeout")
    assert.ok(Date.now() - started < 5_000)
  })
})

test("screenshot returns a png artifact with a sha256 hash", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const response = await act(context.contextId, "screenshot", { full_page: true })
    assert.equal(response.ok, true)
    assert.equal(response.artifacts.length, 1)
    const artifact = response.artifacts[0]
    assert.equal(artifact.kind, "screenshot")
    assert.equal(artifact.mime, "image/png")
    assert.match(artifact.sha256, /^[0-9a-f]{64}$/)
    const bytes = Buffer.from(artifact.base64, "base64")
    assert.deepEqual([...bytes.subarray(0, 8)], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
    assert.equal(crypto.createHash("sha256").update(bytes).digest("hex"), artifact.sha256)
  })
})

test("console keeps a bounded tail of page messages", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/spam` })
    const bounded = await act(context.contextId, "console", { tail: 500 })
    assert.equal(bounded.ok, true)
    assert.equal(bounded.result.items.length, 200)
    assert.equal(bounded.result.items[0].text, "spam-400")
    assert.equal(bounded.result.items.at(-1).text, "spam-599")

    const small = await act(context.contextId, "console", { tail: 3 })
    assert.equal(small.result.items.length, 3)
    assert.ok(small.result.items.every((item) => typeof item.type === "string"))
  })
})

test("network records method, url, status, duration and mime", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const response = await act(context.contextId, "network", { tail: 50 })
    assert.equal(response.ok, true)
    const home = response.result.items.find((item) => item.url.endsWith("/home"))
    assert.ok(home, "missing /home network entry")
    assert.equal(home.method, "GET")
    assert.equal(home.status, 200)
    assert.match(home.mime, /text\/html/)
    assert.equal(typeof home.duration_ms, "number")
  })
})

test("download saves the stream into an artifact and removes the temp file", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const response = await act(context.contextId, "download", {
      target: { selector: "#download" },
    })
    assert.equal(response.ok, true, JSON.stringify(response.error))
    const artifact = response.artifacts[0]
    assert.equal(artifact.kind, "download")
    assert.equal(artifact.mime, "application/octet-stream")
    assert.equal(artifact.filename, "report.txt")
    const bytes = Buffer.from(artifact.base64, "base64")
    assert.equal(bytes.toString("utf8"), "download-body")
    assert.equal(crypto.createHash("sha256").update(bytes).digest("hex"), artifact.sha256)
  })
})

test("download larger than 10MB is rejected without buffering", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const response = await act(context.contextId, "download", {
      target: { selector: "#download-large" },
      timeout_ms: 30_000,
    })
    assert.equal(response.ok, false)
    assert.equal(response.error.code, "too_large")
  })
})

test("storage_state includes cookies, localStorage and sessionStorage", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const response = await act(context.contextId, "storage_state", {
      include_session_storage: true,
    })
    assert.equal(response.ok, true)
    assert.ok(Array.isArray(response.result.cookies))
    const origin = response.result.origins.find((entry) => entry.origin === pageServer.url)
    assert.ok(origin, "missing page origin in storage state")
    assert.deepEqual(origin.localStorage, [{ name: "local-key", value: "local-value" }])
    assert.deepEqual(origin.sessionStorage, [{ name: "session-key", value: "session-value" }])
  })
})

test("storageState and sessionStorage are injected into a fresh context", async () => {
  await withContext(
    async (context) => {
      await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
      const state = await act(context.contextId, "storage_state", { include_session_storage: true })
      const context2 = await openContext({
        storageState: { cookies: state.result.cookies, origins: state.result.origins },
        sessionStorage: { [pageServer.url]: [{ name: "injected-key", value: "injected-value" }] },
      })
      try {
        await act(context2.contextId, "goto", { url: `${pageServer.url}/second` })
        const restored = await act(context2.contextId, "storage_state", {
          include_session_storage: true,
        })
        const origin = restored.result.origins.find((entry) => entry.origin === pageServer.url)
        assert.ok(origin)
        assert.equal(origin.localStorage.length, 1)
        assert.equal(
          origin.sessionStorage.some((item) => item.name === "injected-key" && item.value === "injected-value"),
          true,
          JSON.stringify(origin.sessionStorage),
        )
      } finally {
        await manager.deleteContext(context2.contextId).catch(() => {})
      }
    },
    { sessionStorage: { [pageServer.url]: [{ name: "seed-key", value: "seed-value" }] } },
  )
})

test("cancel aborts an in-flight action and leaves the context usable", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    const started = Date.now()
    const pending = act(context.contextId, "wait_for", {
      target: { selector: "#never" },
      timeout_ms: 10_000,
    })
    await new Promise((resolve) => setTimeout(resolve, 200))
    const canceled = await manager.cancel(context.contextId)
    assert.equal(canceled.canceled, true)
    const response = await pending
    assert.equal(response.ok, false)
    assert.equal(response.error.code, "canceled")
    assert.ok(Date.now() - started < 3_000)

    const afterCancel = await act(context.contextId, "goto", { url: `${pageServer.url}/second` })
    assert.equal(afterCancel.ok, true)
  })
})

test("cancel terminates an in-flight navigation and the next action starts cleanly", async () => {
  await withContext(async (context) => {
    await act(context.contextId, "goto", { url: `${pageServer.url}/home` })
    pageServer.clearHits()
    const pending = act(context.contextId, "goto", {
      url: `${pageServer.url}/slow`,
      timeout_ms: 30_000,
    })
    await new Promise((resolve) => setTimeout(resolve, 300))
    const started = Date.now()
    const canceled = await manager.cancel(context.contextId)
    assert.equal(canceled.canceled, true)
    const response = await pending
    assert.equal(response.ok, false)
    assert.equal(response.error.code, "canceled")
    assert.ok(Date.now() - started < 3_000, "cancel did not settle promptly")

    // The browser-side navigation must be aborted, not left running.
    await waitFor(() => pageServer.aborted.includes("/slow"), 3_000, "slow request abort")

    const after = await act(context.contextId, "goto", { url: `${pageServer.url}/second` })
    assert.equal(after.ok, true, JSON.stringify(after.error))
    assert.equal(after.result.url, `${pageServer.url}/second`)
  })
})

test("close action releases the context", async () => {
  const context = await openContext()
  const closed = await act(context.contextId, "close")
  assert.equal(closed.ok, true)
  const after = await act(context.contextId, "snapshot")
  assert.equal(after.ok, false)
  assert.equal(after.error.code, "context_not_found")
})

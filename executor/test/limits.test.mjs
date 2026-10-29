/**
 * P8 executor hardening tests (design spec §10.5):
 *
 * - WebSocket connections of a browser context are checked against the context
 *   allow list and the executor-wide EZTEST_EXEC_ALLOWED_HOSTS allow list
 *   (default: localhost/127.0.0.1). A blocked connection is recorded, closed and
 *   surfaces a host_not_allowed action failure.
 * - A background action that outlives its hard deadline is canceled (its page is
 *   torn down and recreated) and reported as executor_timeout instead of
 *   hanging the context.
 */

import test from "node:test"
import assert from "node:assert/strict"

import { withAbort } from "../src/actions.mjs"
import { ContextManager } from "../src/contexts.mjs"
import {
  DEFAULT_ALLOWED_HOSTS,
  isWebSocketAllowed,
  parseAllowedHosts,
} from "../src/limits.mjs"

const CONTEXT_ID = "case_run:1"

/** Builds a fake Playwright Page good enough for the context guards/actions. */
function fakePage({ hanging = false, closeHangs = false } = {}) {
  const page = {
    closed: false,
    on() {},
    once() {},
    async close() {
      if (closeHangs) {
        // Simulates a wedged browser that ignores close; recovery must bound it.
        return new Promise(() => {})
      }
      page.closed = true
    },
    async title() {
      return "Fake page"
    },
    url() {
      return "http://127.0.0.1:1/"
    },
    async $$eval() {
      return []
    },
    async evaluate() {
      return []
    },
    locator() {
      return {
        async ariaSnapshot() {
          return '- heading "Fake page"'
        },
        first() {
          return page.locator()
        },
        async waitFor() {
          if (hanging) {
            // Simulates a background browser task that ignores its own
            // timeout, the only way the executor hard deadline can win.
            return new Promise(() => {})
          }
          return undefined
        },
      }
    },
    getByText() {
      return {
        first() {
          return {
            async waitFor() {
              if (hanging) {
                return new Promise(() => {})
              }
              return undefined
            },
          }
        },
      }
    },
    waitForURL() {
      return hanging ? new Promise(() => {}) : Promise.resolve()
    },
  }
  return page
}

/** Builds a fake browser/context pair tracked by the test. */
function fakeBrowser({
  pages = [],
  hanging = false,
  closeHangs = false,
  newPageHangsAfter = Number.POSITIVE_INFINITY,
} = {}) {
  let wsHandler = null
  const context = {
    mountedPages: pages,
    route: async () => {},
    routeWebSocket: (_pattern, handler) => {
      wsHandler = handler
    },
    addInitScript: async () => {},
    on() {},
    async newCDPSession() {
      return { send: async () => {}, on() {} }
    },
    async newPage() {
      if (pages.length >= newPageHangsAfter) {
        // Simulates a wedged browser that never creates the replacement page.
        return new Promise(() => {})
      }
      const page = fakePage({ hanging, closeHangs })
      pages.push(page)
      return page
    },
    async close() {},
    async storageState() {
      return { cookies: [], origins: [] }
    },
  }
  const browser = {
    async version() {
      return "fake"
    },
    async newContext() {
      return context
    },
    async close() {},
  }
  return {
    browser,
    context,
    pages,
    get wsHandler() {
      return wsHandler
    },
  }
}

/** Builds a context manager over the fake browser. */
async function createManager(options = {}) {
  const fake = fakeBrowser(options)
  const manager = new ContextManager({
    maxContexts: 2,
    logger: null,
    launch: async () => fake.browser,
    ...(options.allowedHosts ? { allowedHosts: options.allowedHosts } : {}),
    ...(options.recoveryTimeoutMs ? { recoveryTimeoutMs: options.recoveryTimeoutMs } : {}),
  })
  const created = await manager.createContext({
    baseUrl: "http://127.0.0.1:1",
    allowedHosts: options.contextAllowedHosts ?? ["127.0.0.1"],
  })
  return { manager, fake, contextId: created.contextId }
}

/** A minimal WebSocketRoute double. */
function fakeWebSocketRoute(url) {
  const route = {
    url: () => url,
    connected: false,
    closed: null,
    connectToServer() {
      route.connected = true
    },
    close(options = {}) {
      route.closed = options
    },
  }
  return route
}

test("parseAllowedHosts defaults to the loopback allow list", () => {
  assert.deepEqual(parseAllowedHosts(undefined), DEFAULT_ALLOWED_HOSTS)
  assert.deepEqual(parseAllowedHosts(""), DEFAULT_ALLOWED_HOSTS)
  assert.deepEqual(parseAllowedHosts(" app.example.test, *.internal.test "), [
    "app.example.test",
    "*.internal.test",
  ])
  assert.throws(() => parseAllowedHosts(","), /at least one host/)
})

test("isWebSocketAllowed requires both the context and executor allow lists", () => {
  const context = ["127.0.0.1", "app.example.test"]
  const executor = DEFAULT_ALLOWED_HOSTS
  assert.equal(isWebSocketAllowed("ws://127.0.0.1:9000/socket", context, executor), true)
  assert.equal(isWebSocketAllowed("wss://127.0.0.1/socket", context, executor), true)
  // The host is allowed by the executor but not by the context.
  assert.equal(isWebSocketAllowed("ws://app.example.test/socket", context, executor), false)
  // The host is allowed by the context but not by the executor allow list.
  const remoteContext = ["127.0.0.1", "app.example.test"]
  assert.equal(
    isWebSocketAllowed("ws://app.example.test/socket", remoteContext, ["app.example.test"]),
    true,
  )
  assert.equal(isWebSocketAllowed("ws://app.example.test/socket", remoteContext, []), false)
  // Only websocket schemes are accepted.
  assert.equal(isWebSocketAllowed("http://127.0.0.1/socket", context, executor), false)
  assert.equal(isWebSocketAllowed("data:text/plain,x", context, executor), false)
  assert.equal(isWebSocketAllowed("not a url", context, executor), false)
})

test("a blocked WebSocket is recorded, closed and fails the action", async () => {
  const { manager, fake, contextId } = await createManager()
  try {
    assert.equal(typeof fake.wsHandler, "function", "the context must install a WebSocket guard")

    const allowed = fakeWebSocketRoute("ws://127.0.0.1:9000/socket")
    fake.wsHandler(allowed)
    assert.equal(allowed.connected, true)
    assert.equal(allowed.closed, null)

    const blocked = fakeWebSocketRoute("ws://payments.example.com/socket")
    fake.wsHandler(blocked)
    assert.equal(blocked.connected, false)
    assert.equal(blocked.closed?.code, 1008, "blocked sockets close with policy violation")

    // The rejection also surfaces as a host_not_allowed action failure and is
    // recorded in the context network log.
    const failed = await manager.runAction(contextId, { action: "snapshot" })
    assert.equal(failed.ok, false)
    assert.equal(failed.error.code, "host_not_allowed")

    const network = await manager.runAction(contextId, { action: "network" })
    assert.equal(network.ok, true)
    const rejected = network.result.items.find((item) => item.error === "host_not_allowed")
    assert.ok(rejected, `network items must record the rejected websocket: ${JSON.stringify(network.result)}`)
    assert.match(rejected.url, /payments\.example\.com/)
  } finally {
    await manager.close()
  }
})

test("EZTEST_EXEC_ALLOWED_HOSTS can open the executor allow list", async () => {
  const { manager, fake, contextId } = await createManager({
    contextAllowedHosts: ["127.0.0.1", "app.example.test"],
    allowedHosts: ["127.0.0.1", "app.example.test"],
  })
  try {
    const remote = fakeWebSocketRoute("ws://app.example.test/socket")
    fake.wsHandler(remote)
    assert.equal(remote.connected, true)
    assert.equal(remote.closed, null)
  } finally {
    await manager.close()
  }
})

test("page recovery is bounded when page.close hangs", async () => {
  const { manager, fake, contextId } = await createManager({
    hanging: true,
    closeHangs: true,
    recoveryTimeoutMs: 50,
  })
  try {
    const started = Date.now()
    // The action itself must settle: pre-fix, the hanging page.close wedges the
    // queue forever, so bound the test-side await and fail instead of hanging.
    const outcome = await Promise.race([
      manager.runAction(contextId, {
        action: "wait_for",
        args: { text: "never", timeout_ms: 10 },
      }),
      new Promise((resolve) => setTimeout(() => resolve("test-timeout"), 3_000)),
    ])
    assert.notEqual(outcome, "test-timeout", "recovery waited forever on a hanging page.close")
    assert.equal(outcome.ok, false)
    assert.equal(outcome.error.code, "executor_timeout")
    assert.ok(Date.now() - started < 3_000, "recovery waited forever on a hanging page.close")

    // The context still recovered with a fresh page and serves actions.
    assert.equal(fake.pages.length, 2)
    const snapshot = await manager.runAction(contextId, { action: "snapshot" })
    assert.equal(snapshot.ok, true, JSON.stringify(snapshot))
  } finally {
    await manager.close()
  }
})

test("page recovery is bounded when page recreation hangs", async () => {
  const { manager, fake, contextId } = await createManager({
    hanging: true,
    newPageHangsAfter: 1,
    recoveryTimeoutMs: 50,
  })
  try {
    assert.equal(fake.pages.length, 1, "context creation opened the initial page")
    const started = Date.now()
    const outcome = await Promise.race([
      manager.runAction(contextId, {
        action: "wait_for",
        args: { text: "never", timeout_ms: 10 },
      }),
      new Promise((resolve) => setTimeout(() => resolve("test-timeout"), 3_000)),
    ])
    assert.notEqual(outcome, "test-timeout", "recovery waited forever on a hanging page recreation")
    assert.equal(outcome.ok, false)
    assert.equal(outcome.error.code, "executor_timeout")
    assert.ok(Date.now() - started < 3_000, "recovery waited forever on a hanging page recreation")
    assert.equal(fake.pages.length, 1, "a timed-out newPage must not register a page")
  } finally {
    await manager.close()
  }
})

test("withAbort reports a hard background timeout as executor_timeout", async () => {
  await assert.rejects(
    withAbort(new Promise(() => {}), { timeoutMs: 10, label: "hung action" }),
    (error) => error?.code === "executor_timeout",
  )
  const result = await withAbort(Promise.resolve("done"), { timeoutMs: 1000, label: "quick action" })
  assert.equal(result, "done")
})

test("a background timeout cancels the page and recovers the context", async () => {
  const { manager, fake, contextId } = await createManager({ hanging: true })
  try {
    assert.equal(fake.pages.length, 1, "context creation opens the initial page")
    const first = fake.pages[0]

    const outcome = await manager.runAction(contextId, {
      action: "wait_for",
      args: { text: "never", timeout_ms: 50 },
    })
    assert.equal(outcome.ok, false)
    assert.equal(outcome.error.code, "executor_timeout")
    assert.equal(first.closed, true, "the hung page must be torn down")
    assert.equal(fake.pages.length, 2, "the context must recover with a fresh page")

    // The recovered page serves subsequent actions: nothing hangs.
    const snapshot = await manager.runAction(contextId, { action: "snapshot" })
    assert.equal(snapshot.ok, true, JSON.stringify(snapshot))
  } finally {
    await manager.close()
  }
})

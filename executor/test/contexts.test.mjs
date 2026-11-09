import test from "node:test"
import assert from "node:assert/strict"

import { ContextManager } from "../src/contexts.mjs"

/**
 * Minimal fake page/context/browser that records the options the manager passes
 * to browser.newContext. Context creation is the only place where Playwright
 * accepts a viewport/deviceScaleFactor, so these options are the environment
 * fingerprint of every later visual comparison.
 */
function fakeBrowser(recordedOptions) {
  const page = {
    on() {},
    once() {},
    async close() {},
  }
  const context = {
    async addInitScript() {},
    async route() {},
    async routeWebSocket() {},
    on() {},
    async newPage() {
      return page
    },
    async newCDPSession() {
      return {
        async send() {},
        on() {},
        async detach() {},
      }
    },
    async close() {},
  }
  return {
    version: () => "fake-1.0.0",
    isConnected: () => true,
    async newContext(options) {
      recordedOptions.push(options)
      return context
    },
    async close() {},
  }
}

test("createContext passes viewport and deviceScaleFactor to the browser context", async () => {
  const recorded = []
  const manager = new ContextManager({
    maxContexts: 2,
    launch: async () => fakeBrowser(recorded),
    logger: null,
  })
  try {
    await manager.createContext({
      key: "case_run:1",
      allowedHosts: ["127.0.0.1"],
      viewport: { width: 1440, height: 900 },
      deviceScaleFactor: 2,
    })
    assert.equal(recorded.length, 1)
    assert.deepEqual(recorded[0].viewport, { width: 1440, height: 900 })
    assert.equal(recorded[0].deviceScaleFactor, 2)
  } finally {
    await manager.close()
  }
})

test("createContext omits deviceScaleFactor when the caller does not set one", async () => {
  const recorded = []
  const manager = new ContextManager({
    maxContexts: 1,
    launch: async () => fakeBrowser(recorded),
    logger: null,
  })
  try {
    await manager.createContext({ allowedHosts: ["127.0.0.1"], viewport: { width: 800, height: 600 } })
    assert.deepEqual(recorded[0].viewport, { width: 800, height: 600 })
    assert.equal(Object.hasOwn(recorded[0], "deviceScaleFactor"), false)
  } finally {
    await manager.close()
  }
})

test("createContext fails closed and closes the context when routeWebSocket is unavailable", async () => {
  let closed = false
  const page = { on() {}, once() {}, async close() {} }
  const context = {
    async addInitScript() {},
    async route() {},
    // routeWebSocket intentionally missing: an older Playwright cannot route
    // page WebSockets, so a context must never be created unguarded.
    on() {},
    async newPage() {
      return page
    },
    async newCDPSession() {
      return { async send() {}, on() {}, async detach() {} }
    },
    async close() {
      closed = true
    },
  }
  const browser = {
    version: () => "fake-1.0.0",
    isConnected: () => true,
    async newContext() {
      return context
    },
    async close() {},
  }
  const manager = new ContextManager({
    maxContexts: 1,
    launch: async () => browser,
    logger: null,
  })
  try {
    await assert.rejects(
      () => manager.createContext({ allowedHosts: ["127.0.0.1"] }),
      (error) => error && error.code === "internal" && /intercept WebSocket/i.test(error.message),
    )
    assert.equal(closed, true, "the unguarded browser context must be closed instead of leaked")
    assert.equal(manager.size, 0)
  } finally {
    await manager.close()
  }
})

test("createContext stores metadata and createdAt and lists contexts", async () => {
  const manager = new ContextManager({
    maxContexts: 2,
    launch: async () => fakeBrowser([]),
    logger: null,
  })
  try {
    const metadata = {
      case_run_id: 42,
      version_sut_id: 7,
      account_role: "admin",
      dispatch_epoch: 3,
      dispatch_token: "dispatch-token",
    }
    const created = await manager.createContext({
      key: "run:42:vs:7:role:admin",
      allowedHosts: ["127.0.0.1"],
      metadata,
    })
    const contexts = manager.list()
    assert.equal(contexts.length, 1)
    assert.equal(contexts[0].id, created.contextId)
    assert.equal(contexts[0].key, "run:42:vs:7:role:admin")
    assert.deepEqual(contexts[0].metadata, metadata)
    assert.equal(typeof contexts[0].createdAt, "string")
    assert.ok(Number.isFinite(Date.parse(contexts[0].createdAt)))

    await manager.deleteContext(created.contextId)
    assert.deepEqual(manager.list(), [])
  } finally {
    await manager.close()
  }
})

test("createContext rejects non-object or oversized metadata", async () => {
  const manager = new ContextManager({
    maxContexts: 1,
    launch: async () => fakeBrowser([]),
    logger: null,
  })
  try {
    for (const metadata of [["not", "an", "object"], "text", 42]) {
      await assert.rejects(
        () => manager.createContext({ allowedHosts: ["127.0.0.1"], metadata }),
        (error) => error && error.code === "invalid_arguments",
        `metadata ${JSON.stringify(metadata)} was accepted`,
      )
    }
    await assert.rejects(
      () => manager.createContext({ allowedHosts: ["127.0.0.1"], metadata: { blob: "x".repeat(3000) } }),
      (error) => error && error.code === "invalid_arguments",
    )
    // An absent or null metadata is valid and is not stored.
    const created = await manager.createContext({ allowedHosts: ["127.0.0.1"], metadata: null })
    assert.equal(manager.list()[0].metadata, undefined)
    await manager.deleteContext(created.contextId)
  } finally {
    await manager.close()
  }
})

test("createContext rejects an out-of-range deviceScaleFactor", async () => {
  const manager = new ContextManager({
    maxContexts: 1,
    launch: async () => fakeBrowser([]),
    logger: null,
  })
  try {
    for (const deviceScaleFactor of [0.25, 5, "two"]) {
      await assert.rejects(
        () =>
          manager.createContext({
            allowedHosts: ["127.0.0.1"],
            deviceScaleFactor,
          }),
        (error) => error && error.code === "invalid_arguments",
        `deviceScaleFactor ${String(deviceScaleFactor)} was accepted`,
      )
    }
  } finally {
    await manager.close()
  }
})

// v0.2.3 X-01: a hung browser.newContext must never hang the API request. The
// manager owns a hard create deadline (default 30s), reports it as a typed
// executor_timeout and exposes `creating` on health while the launch is pending.
test("createContext times out with executor_timeout and reports creating", { timeout: 5_000 }, async () => {
  const manager = new ContextManager({
    maxContexts: 1,
    contextCreateTimeoutMs: 50,
    launch: async () => ({
      version: () => "fake-1.0.0",
      isConnected: () => true,
      newContext: () => new Promise(() => {}),
      async close() {},
    }),
    logger: null,
  })
  try {
    const outcome = manager
      .createContext({ allowedHosts: ["127.0.0.1"] })
      .then(
        (value) => ({ value }),
        (error) => ({ error }),
      )
    await new Promise((resolve) => setTimeout(resolve, 10))
    const during = await manager.health()
    assert.equal(during.creating, 1, "health must expose the in-flight context creation")
    const { error } = await outcome
    assert.ok(error, "createContext must reject when newContext never settles")
    assert.equal(error.code, "executor_timeout")
    const after = await manager.health()
    assert.equal(after.creating, 0)
    assert.match(String(after.lastContextCreateError ?? ""), /executor_timeout/)
  } finally {
    await manager.close()
  }
})

// A browser.newContext that resolves after the deadline must not leak an
// unguarded, untracked browser context: the late result is closed.
test("createContext closes a context that appears after the deadline", { timeout: 5_000 }, async () => {
  let closed = false
  const page = { on() {}, once() {}, async close() {} }
  const context = {
    async addInitScript() {},
    async route() {},
    async routeWebSocket() {},
    on() {},
    async newPage() {
      return page
    },
    async newCDPSession() {
      return { async send() {}, on() {}, async detach() {} }
    },
    async close() {
      closed = true
    },
  }
  const browser = {
    version: () => "fake-1.0.0",
    isConnected: () => true,
    async newContext() {
      await new Promise((resolve) => setTimeout(resolve, 80))
      return context
    },
    async close() {},
  }
  const manager = new ContextManager({
    maxContexts: 1,
    contextCreateTimeoutMs: 20,
    launch: async () => browser,
    logger: null,
  })
  try {
    await assert.rejects(
      () => manager.createContext({ allowedHosts: ["127.0.0.1"] }),
      (error) => error && error.code === "executor_timeout",
    )
    await new Promise((resolve) => setTimeout(resolve, 150))
    assert.equal(closed, true, "the half-created context must be closed after the timeout")
    assert.equal(manager.size, 0)
  } finally {
    await manager.close()
  }
})

// Consecutive create failures mean the environment cannot create browser
// contexts: /healthz (and therefore the pushed heartbeat) must turn degraded.
test("repeated context create failures degrade health", async () => {
  const manager = new ContextManager({
    maxContexts: 1,
    createFailureThreshold: 3,
    launch: async () => ({
      version: () => "fake-1.0.0",
      isConnected: () => true,
      async newContext() {
        throw new Error("context launch exploded")
      },
      async close() {},
    }),
    logger: null,
  })
  try {
    for (let attempt = 0; attempt < 3; attempt += 1) {
      await assert.rejects(
        () => manager.createContext({ allowedHosts: ["127.0.0.1"] }),
        /context launch exploded/,
      )
    }
    const health = await manager.health()
    assert.equal(health.status, "degraded")
    assert.match(String(health.lastContextCreateError ?? ""), /context launch exploded/)
  } finally {
    await manager.close()
  }
})

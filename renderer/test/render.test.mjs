import test from "node:test"
import assert from "node:assert/strict"

import {
  DEFAULT_CHANNEL,
  DEFAULT_TIMEOUT_SECONDS,
  PDFRenderer,
  browserLaunchAttempts,
  launchBrowser,
} from "../src/render.mjs"

/** A fake browser/context/page that records the options the renderer passes. */
function fakeBrowser(recorded, { pdfImpl } = {}) {
  const page = {
    async setContent(html, options) {
      recorded.push({ kind: "setContent", html, options })
    },
    async pdf(options) {
      recorded.push({ kind: "pdf", options })
      if (pdfImpl) {
        return pdfImpl()
      }
      return Buffer.from("%PDF-1.7\n" + "x".repeat(2048))
    },
  }
  const context = {
    closed: false,
    async newPage() {
      recorded.push({ kind: "newPage" })
      return page
    },
    async close() {
      this.closed = true
      recorded.push({ kind: "close" })
    },
  }
  return {
    version: () => "fake-chromium 1.0",
    isConnected: () => true,
    async newContext() {
      recorded.push({ kind: "newContext" })
      return context
    },
    async close() {},
  }
}

test("browser launch attempts prefer the bundled chromium, then chrome, then msedge", () => {
  const previous = process.env.EZTEST_RENDER_CHROMIUM_PATH
  delete process.env.EZTEST_RENDER_CHROMIUM_PATH
  try {
    assert.deepEqual(browserLaunchAttempts(), [
      { headless: true },
      { headless: true, channel: "chrome" },
      { headless: true, channel: "msedge" },
    ])
    assert.deepEqual(browserLaunchAttempts("msedge"), [
      { headless: true },
      { headless: true, channel: "msedge" },
    ])
    process.env.EZTEST_RENDER_CHROMIUM_PATH = "C:/fake/chrome.exe"
    const withCached = browserLaunchAttempts()
    assert.deepEqual(withCached[1], { headless: true, executablePath: "C:/fake/chrome.exe" })
    assert.equal(withCached.length, 4)
  } finally {
    if (previous === undefined) {
      delete process.env.EZTEST_RENDER_CHROMIUM_PATH
    } else {
      process.env.EZTEST_RENDER_CHROMIUM_PATH = previous
    }
  }
})

test("launchBrowser falls back only for a missing bundled browser", async () => {
  const calls = []
  const browser = { version: () => "chrome" }
  const launched = await launchBrowser({
    launch: async (options) => {
      calls.push(options)
      if (calls.length === 1) {
        throw new Error("Executable doesn't exist at C:/playwright/chromium/chrome.exe")
      }
      return browser
    },
  })
  assert.equal(launched, browser)
  assert.deepEqual(calls, [
    { headless: true },
    { headless: true, channel: "chrome" },
  ])
})

test("launchBrowser fails fast on a non-missing-browser launch error", async () => {
  const calls = []
  await assert.rejects(
    () =>
      launchBrowser({
        launch: async (options) => {
          calls.push(options)
          throw new Error("cannot allocate display")
        },
      }),
    /cannot allocate display/,
  )
  assert.equal(calls.length, 1)
})

test("render uses an isolated context with A4 print backgrounds", async () => {
  const recorded = []
  const renderer = new PDFRenderer({ launch: async () => fakeBrowser(recorded) })
  const pdf = await renderer.render("<html><body>报告</body></html>")
  assert.equal(pdf.subarray(0, 5).toString("latin1"), "%PDF-")
  const content = recorded.find((entry) => entry.kind === "setContent")
  assert.equal(content.html, "<html><body>报告</body></html>")
  assert.deepEqual(content.options, { waitUntil: "load" })
  const pdfCall = recorded.find((entry) => entry.kind === "pdf")
  assert.equal(pdfCall.options.format, "A4")
  assert.equal(pdfCall.options.printBackground, true)
  assert.equal(recorded.filter((entry) => entry.kind === "newContext").length, 1)
  assert.equal(recorded.filter((entry) => entry.kind === "close").length, 1)
})

test("render reuses one browser and closes a context per request", async () => {
  const recorded = []
  let launches = 0
  const renderer = new PDFRenderer({
    launch: async () => {
      launches += 1
      return fakeBrowser(recorded)
    },
  })
  await renderer.render("<p>1</p>")
  await renderer.render("<p>2</p>")
  assert.equal(launches, 1)
  assert.equal(recorded.filter((entry) => entry.kind === "newContext").length, 2)
  assert.equal(recorded.filter((entry) => entry.kind === "close").length, 2)
  assert.equal(await renderer.browserVersion(), "fake-chromium 1.0")
})

test("render closes the context when rendering fails", async () => {
  const recorded = []
  const renderer = new PDFRenderer({
    launch: async () =>
      fakeBrowser(recorded, {
        pdfImpl: () => {
          throw new Error("page crashed")
        },
      }),
  })
  await assert.rejects(() => renderer.render("<p>boom</p>"), /page crashed/)
  assert.equal(recorded.filter((entry) => entry.kind === "close").length, 1)
})

test("render rejects immediately when the request was already aborted", async () => {
  const recorded = []
  const renderer = new PDFRenderer({ launch: async () => fakeBrowser(recorded) })
  const controller = new AbortController()
  controller.abort()
  await assert.rejects(() => renderer.render("<p>late</p>", { signal: controller.signal }), (error) => {
    assert.equal(error.code, "render_timeout")
    return true
  })
})

test("render closes the context when the request is aborted mid-render", async () => {
  const recorded = []
  let finishPDF
  const renderer = new PDFRenderer({
    launch: async () =>
      fakeBrowser(recorded, {
        pdfImpl: () =>
          new Promise((resolve) => {
            finishPDF = resolve
          }),
      }),
  })
  const controller = new AbortController()
  const pending = renderer.render("<p>slow</p>", { signal: controller.signal })
  await new Promise((resolve) => setTimeout(resolve, 20))
  controller.abort()
  await assert.rejects(pending, (error) => error.code === "render_timeout")
  finishPDF?.(Buffer.from("%PDF-1.7 late"))
  assert.equal(recorded.filter((entry) => entry.kind === "close").length, 1)
})

test("health degrades when the browser cannot launch and does not retry forever", async () => {
  let launches = 0
  const renderer = new PDFRenderer({
    launch: async () => {
      launches += 1
      throw new Error("browser unavailable")
    },
  })
  const first = await renderer.health()
  assert.equal(first.status, "degraded")
  assert.match(first.error, /browser unavailable/)
  const second = await renderer.health()
  assert.equal(second.status, "degraded")
  assert.equal(launches, 1)
})

test("default channel and timeout constants match the frozen renderer config", () => {
  assert.equal(DEFAULT_CHANNEL, "chrome")
  assert.equal(DEFAULT_TIMEOUT_SECONDS, 60)
})

// Real-browser integration: uses the bundled/cached/system browser when one is
// installed (the deterministic unit tests above inject a fake). CI installs the
// pinned Chromium for the executor gate; a machine without any browser skips
// this test instead of failing the protocol suite.
test("renders a real %PDF document with the installed browser", async (t) => {
  let browser
  try {
    browser = await launchBrowser()
  } catch (error) {
    t.skip(`no usable browser available: ${error instanceof Error ? error.message : error}`)
    return
  }
  const renderer = new PDFRenderer({ launch: async () => browser })
  try {
    const pdf = await renderer.render(
      '<!doctype html><html lang="zh-CN"><body><h1>EzTest PDF</h1><p>报告</p></body></html>',
    )
    assert.equal(pdf.subarray(0, 5).toString("latin1"), "%PDF-")
    assert.ok(pdf.length > 1024, `pdf bytes = ${pdf.length}`)
  } finally {
    await renderer.close()
  }
})

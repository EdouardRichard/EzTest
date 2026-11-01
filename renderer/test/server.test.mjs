import test from "node:test"
import assert from "node:assert/strict"
import http from "node:http"

import {
  MAX_HTML_BYTES,
  createRendererServer,
  parseAddress,
  parseTimeoutSeconds,
} from "../src/server.mjs"

const TOKEN = "test-render-token"

/** Minimal HTTP client: returns status/headers/text/buffer/json. */
function httpRequest(baseUrl, path, { method = "GET", token, body, headers = {} } = {}) {
  return new Promise((resolve, reject) => {
    const url = new URL(path, baseUrl)
    const payload =
      body === undefined ? null : Buffer.from(typeof body === "string" ? body : JSON.stringify(body))
    const requestHeaders = {
      ...(token === undefined ? {} : { authorization: `Bearer ${token}` }),
      ...(payload === null
        ? {}
        : { "content-type": "application/json", "content-length": payload.length }),
      ...headers,
    }
    const request = http.request(
      {
        hostname: url.hostname,
        port: url.port,
        path: url.pathname + url.search,
        method,
        headers: requestHeaders,
      },
      (response) => {
        const chunks = []
        response.on("data", (chunk) => chunks.push(chunk))
        response.on("end", () => {
          const buffer = Buffer.concat(chunks)
          resolve({
            status: response.statusCode,
            headers: response.headers,
            buffer,
            text: buffer.toString("utf8"),
            json: () => JSON.parse(buffer.toString("utf8")),
          })
        })
      },
    )
    request.on("error", reject)
    if (payload !== null) {
      request.write(payload)
    }
    request.end()
  })
}

/** A renderer stub whose render behaviour is controlled per test. */
function fakeRenderer({ render, health } = {}) {
  return {
    calls: 0,
    async render(html, options) {
      this.calls += 1
      if (render) {
        return render(html, options)
      }
      return syntheticPDF(html)
    },
    async health() {
      if (health) {
        return health()
      }
      return { status: "ok", browserVersion: "fake-browser 1.0" }
    },
  }
}

/** A >1KB synthetic PDF so tests can assert the bytes/headers without Chrome. */
function syntheticPDF(html) {
  const header = "%PDF-1.7\n"
  const body = `rendered ${html}\n${"x".repeat(2048)}`
  return Buffer.from(header + body, "utf8")
}

async function startServer(t, options = {}) {
  const renderer = options.renderer ?? fakeRenderer()
  const server = createRendererServer({ token: TOKEN, renderer, version: "test", ...options })
  server.listen(0, "127.0.0.1")
  await new Promise((resolve) => server.once("listening", resolve))
  const { port } = server.address()
  t.after(async () => {
    await new Promise((resolve) => server.close(resolve))
  })
  return { server, renderer, baseUrl: `http://127.0.0.1:${port}` }
}

test("GET /healthz is reachable without a token", async (t) => {
  const { baseUrl } = await startServer(t)
  const response = await httpRequest(baseUrl, "/healthz")
  assert.equal(response.status, 200)
  assert.deepEqual(response.json(), {
    status: "ok",
    version: "test",
    browser: "fake-browser 1.0",
  })
})

test("GET /healthz reports 503 when the browser is unavailable", async (t) => {
  const { baseUrl } = await startServer(t, {
    renderer: fakeRenderer({
      health: async () => ({ status: "degraded", error: "browser unavailable" }),
    }),
  })
  const response = await httpRequest(baseUrl, "/healthz")
  assert.equal(response.status, 503)
  assert.equal(response.json().status, "degraded")
  assert.equal(response.json().error, "browser unavailable")
})

test("POST /v1/pdf rejects missing or wrong bearer tokens", async (t) => {
  const { baseUrl, renderer } = await startServer(t)
  for (const token of [undefined, "wrong-token"]) {
    const response = await httpRequest(baseUrl, "/v1/pdf", {
      method: "POST",
      token,
      body: { html: "<p>report</p>" },
    })
    assert.equal(response.status, 401, response.text)
    assert.equal(response.json().error.code, "unauthorized")
  }
  assert.equal(renderer.calls, 0)
})

test("POST /v1/pdf requires a non-empty html string", async (t) => {
  const { baseUrl } = await startServer(t)
  for (const body of [{}, { html: "" }, { html: 42 }, { html: "   " }]) {
    const response = await httpRequest(baseUrl, "/v1/pdf", {
      method: "POST",
      token: TOKEN,
      body,
    })
    assert.equal(response.status, 400, response.text)
    assert.equal(response.json().error.code, "html_required")
  }
})

test("POST /v1/pdf rejects malformed JSON", async (t) => {
  const { baseUrl } = await startServer(t)
  const response = await httpRequest(baseUrl, "/v1/pdf", {
    method: "POST",
    token: TOKEN,
    body: '{"html":',
  })
  assert.equal(response.status, 400)
  assert.equal(response.json().error.code, "invalid_request")
})

test("POST /v1/pdf enforces the 2 MiB html cap", async (t) => {
  const { baseUrl, renderer } = await startServer(t)
  const oversized = "a".repeat(MAX_HTML_BYTES + 1)
  const response = await httpRequest(baseUrl, "/v1/pdf", {
    method: "POST",
    token: TOKEN,
    body: { html: oversized },
  })
  assert.equal(response.status, 400, response.text.slice(0, 200))
  assert.equal(response.json().error.code, "html_too_large")
  assert.equal(renderer.calls, 0)
})

test("POST /v1/pdf returns the rendered PDF bytes", async (t) => {
  const { baseUrl, renderer } = await startServer(t)
  const response = await httpRequest(baseUrl, "/v1/pdf", {
    method: "POST",
    token: TOKEN,
    body: { html: "<!doctype html><html><body>报告</body></html>" },
  })
  assert.equal(response.status, 200, response.text.slice(0, 200))
  assert.equal(response.headers["content-type"], "application/pdf")
  assert.ok(response.buffer.length > 1024, `pdf bytes = ${response.buffer.length}`)
  assert.equal(response.buffer.subarray(0, 5).toString("latin1"), "%PDF-")
  assert.equal(renderer.calls, 1)
})

test("POST /v1/pdf maps render failures to 422 render_failed", async (t) => {
  const { baseUrl } = await startServer(t, {
    renderer: fakeRenderer({
      render: async () => {
        throw new Error("page crashed")
      },
    }),
  })
  const response = await httpRequest(baseUrl, "/v1/pdf", {
    method: "POST",
    token: TOKEN,
    body: { html: "<p>report</p>" },
  })
  assert.equal(response.status, 422)
  const error = response.json().error
  assert.equal(error.code, "render_failed")
  assert.match(error.message, /page crashed/)
})

test("POST /v1/pdf returns 504 when rendering exceeds the timeout", async (t) => {
  const { baseUrl } = await startServer(t, {
    timeoutMs: 50,
    renderer: fakeRenderer({
      render: (html, options) =>
        new Promise((resolve, reject) => {
          options?.signal?.addEventListener("abort", () => reject(Object.assign(new Error("render timed out"), { code: "render_timeout" })))
        }),
    }),
  })
  const response = await httpRequest(baseUrl, "/v1/pdf", {
    method: "POST",
    token: TOKEN,
    body: { html: "<p>slow</p>" },
  })
  assert.equal(response.status, 504)
  assert.equal(response.json().error.code, "render_timeout")
})

test("POST /v1/pdf rejects concurrent requests above the concurrency limit", async (t) => {
  let release
  const gate = new Promise((resolve) => {
    release = resolve
  })
  const { baseUrl, renderer } = await startServer(t, {
    maxConcurrency: 2,
    renderer: fakeRenderer({
      render: async (html) => {
        await gate
        return syntheticPDF(html)
      },
    }),
  })
  const first = httpRequest(baseUrl, "/v1/pdf", { method: "POST", token: TOKEN, body: { html: "<p>1</p>" } })
  const second = httpRequest(baseUrl, "/v1/pdf", { method: "POST", token: TOKEN, body: { html: "<p>2</p>" } })
  // Give both requests time to enter the render call.
  await new Promise((resolve) => setTimeout(resolve, 100))
  const third = await httpRequest(baseUrl, "/v1/pdf", { method: "POST", token: TOKEN, body: { html: "<p>3</p>" } })
  assert.equal(third.status, 429, third.text)
  assert.equal(third.json().error.code, "busy")
  release()
  const responses = await Promise.all([first, second])
  assert.deepEqual(responses.map((response) => response.status), [200, 200])
  assert.equal(renderer.calls, 2)
})

test("unknown paths and methods are rejected", async (t) => {
  const { baseUrl } = await startServer(t)
  const notFound = await httpRequest(baseUrl, "/v1/unknown")
  assert.equal(notFound.status, 404)
  const wrongMethod = await httpRequest(baseUrl, "/v1/pdf", { token: TOKEN })
  assert.equal(wrongMethod.status, 405)
  const healthMethod = await httpRequest(baseUrl, "/healthz", { method: "POST", token: TOKEN })
  assert.equal(healthMethod.status, 405)
})

test("parseAddress and parseTimeoutSeconds validate their inputs", () => {
  assert.deepEqual(parseAddress("127.0.0.1:13337"), { host: "127.0.0.1", port: 13337 })
  assert.throws(() => parseAddress("nonsense"))
  assert.throws(() => parseAddress("127.0.0.1:0"))
  assert.equal(parseTimeoutSeconds("60"), 60)
  assert.equal(parseTimeoutSeconds(""), 60)
  assert.equal(parseTimeoutSeconds("0"), 60)
  assert.equal(parseTimeoutSeconds("abc"), 60)
})

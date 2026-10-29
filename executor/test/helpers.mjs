import http from "node:http"
import { once } from "node:events"

import { MAX_ARTIFACT_BYTES } from "../src/security.mjs"

const HOME_HTML = `<!doctype html>
<html>
  <head><title>EzTest Home</title></head>
  <body>
    <h1>EzTest Home</h1>
    <button id="login-btn" aria-label="Login" onclick="mark('login')">Login</button>
    <button id="submit-btn" onclick="mark('submit')">Submit</button>
    <input id="username" aria-label="Username" />
    <input id="password" type="password" aria-label="Password" value="top-secret-pw" />
    <input id="secret-input" aria-label="Secret" />
    <div id="text-target" onclick="mark('text')">Text target</div>
    <div data-testid="test-id-node" onclick="mark('testid')">By test id</div>
    <div id="selector-node" onclick="mark('selector')">By selector</div>
    <div class="dup">duplicate value</div>
    <div class="dup">duplicate value</div>
    <a id="next" href="/second">Next page</a>
    <a id="download" href="/download">Download</a>
    <a id="download-large" href="/download-large">Large download</a>
    <div id="clicked"></div>
    <script>
      function mark(name) {
        document.getElementById('clicked').textContent = name
        console.log('clicked:' + name)
      }
      localStorage.setItem('local-key', 'local-value')
      sessionStorage.setItem('session-key', 'session-value')
      console.log('console-hello')
    </script>
  </body>
</html>`

const SECOND_HTML = `<!doctype html>
<html><head><title>EzTest Second</title></head>
<body><h1>Second page</h1><a id="back" href="/home">Back</a></body></html>`

const SPAM_HTML = `<!doctype html>
<html><head><title>Spam</title></head>
<body><h1>Spam</h1>
<script>for (let i = 0; i < 600; i += 1) { console.log('spam-' + i) }</script>
</body></html>`

/**
 * Starts a local http server that serves the self-test pages. Tests must never
 * depend on the repository fixture scripts.
 */
export async function startLocalPage() {
  const hits = []
  const aborted = []
  const upgrades = []
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1")
    hits.push(url.pathname)
    switch (url.pathname) {
      case "/home":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(HOME_HTML)
        return
      case "/second":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(SECOND_HTML)
        return
      case "/spam":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(SPAM_HTML)
        return
      case "/mixed":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>Mixed</title></head><body><h1>Mixed</h1>` +
            `<img id="cross-host" src="http://localhost:${req.socket.localPort}/download" alt="blocked">` +
            `</body></html>`,
        )
        return
      case "/download":
        res.writeHead(200, {
          "content-type": "application/octet-stream",
          "content-disposition": 'attachment; filename="report.txt"',
          "content-length": String(Buffer.byteLength("download-body")),
        })
        res.end("download-body")
        return
      case "/redirect-blocked":
        // Same server, different hostname: resolves locally, but the executor
        // allow list only contains 127.0.0.1, so the redirect must be blocked.
        res.writeHead(302, { location: `http://localhost:${req.socket.localPort}/second` })
        res.end()
        return
      case "/redirect-home":
        res.writeHead(302, { location: "/second" })
        res.end()
        return
      case "/popup":
        // localhost is intentionally not in allowedHosts while 127.0.0.1 is.
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>Popup</title></head><body><h1>Popup</h1>` +
            `<button id="open-popup" onclick="window.open('http://localhost:${req.socket.localPort}/popup-target')">Open popup</button>` +
            `</body></html>`,
        )
        return
      case "/popup-redirect":
        // Opens an allowed URL that redirects to the disallowed localhost host.
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>Popup redirect</title></head><body><h1>Popup redirect</h1>` +
            `<button id="open-popup-redirect" onclick="window.open('http://127.0.0.1:${req.socket.localPort}/redirect-blocked')">Open</button>` +
            `</body></html>`,
        )
        return
      case "/popup-target":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(`<!doctype html><html><head><title>Popup target</title></head><body><h1>Popup target</h1></body></html>`)
        return
      case "/iframe-mixed":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>Iframe mixed</title></head><body><h1>Iframe mixed</h1>` +
            `<iframe id="cross-iframe" src="http://localhost:${req.socket.localPort}/iframe-target"></iframe>` +
            `</body></html>`,
        )
        return
      case "/iframe-target":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(`<!doctype html><html><body><h1>Iframe target</h1></body></html>`)
        return
      case "/swtest":
        // A service worker would otherwise be able to request hosts that the
        // page-level allow list guard never sees.
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>SW</title></head><body><h1>SW</h1>` +
            `<div id="sw-result">pending</div>` +
            `<script>
              setTimeout(() => {
                const node = document.getElementById('sw-result')
                if (node.textContent === 'pending') { node.textContent = 'sw-timeout' }
              }, 2000)
              if (!navigator.serviceWorker) {
                document.getElementById('sw-result').textContent = 'sw-unsupported'
              } else {
                navigator.serviceWorker.register('/sw.js').then(async () => {
                  const registrations = await navigator.serviceWorker.getRegistrations()
                  const active = registrations.length > 0 || Boolean(navigator.serviceWorker.controller)
                  document.getElementById('sw-result').textContent = active ? 'sw-active' : 'sw-blocked'
                  return active ? fetch('/sw-fetch') : undefined
                }).catch(() => {
                  document.getElementById('sw-result').textContent = 'sw-blocked'
                })
              }
            </script></body></html>`,
        )
        return
      case "/sw.js":
        res.writeHead(200, { "content-type": "application/javascript; charset=utf-8" })
        res.end(
          `self.addEventListener('fetch', (event) => {
             if (event.request.url.includes('/sw-fetch')) {
               event.respondWith(fetch('http://localhost:' + self.location.port + '/second'))
             }
           })`,
        )
        return
      case "/sw-fetch":
        res.writeHead(200, { "content-type": "text/plain" })
        res.end("sw-fetch")
        return
      case "/wstest":
        // Opened from the page itself: the context WebSocket guard must block
        // localhost (not in allowedHosts) and close the socket with 1008.
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>WS</title></head><body><h1>WS</h1>` +
            `<script>
              const socket = new WebSocket('ws://localhost:${req.socket.localPort}/blocked-ws')
              socket.onopen = () => console.log('ws-open')
              socket.onclose = (event) => console.log('ws-close:' + event.code)
              socket.onerror = () => console.log('ws-error')
            </script></body></html>`,
        )
        return
      case "/workertest":
        // A page-created Worker/SharedWorker is not covered by Playwright's
        // routeWebSocket (it only routes page WebSockets), so the executor must
        // block worker creation itself. The blob worker would open a WebSocket
        // to an allowed host; a successful upgrade would prove the bypass.
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>Worker</title></head><body><h1>Worker</h1>` +
            `<div id="worker-result">pending</div>` +
            `<script>
              const node = document.getElementById('worker-result')
              const source = "const socket = new WebSocket('ws://127.0.0.1:${req.socket.localPort}/worker-ws');" +
                "socket.onopen = () => self.postMessage('worker-ws-open');" +
                "socket.onerror = () => self.postMessage('worker-ws-error')"
              const blobUrl = URL.createObjectURL(new Blob([source], { type: 'text/javascript' }))
              const outcomes = []
              try {
                const worker = new Worker(blobUrl)
                worker.onmessage = (event) => { node.textContent = event.data }
                outcomes.push('worker-created')
              } catch (error) {
                outcomes.push('worker-blocked')
              }
              try {
                const shared = new SharedWorker(blobUrl)
                shared.port.close()
                outcomes.push('sharedworker-created')
              } catch (error) {
                outcomes.push('sharedworker-blocked')
              }
              node.textContent = outcomes.join(',')
              console.log('worker-guard:' + outcomes.join(','))
              setTimeout(() => {
                if (node.textContent === 'pending') { node.textContent = 'worker-timeout' }
              }, 2000)
            </script></body></html>`,
        )
        return
      case "/secret-leak":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>Secret leak</title></head><body><h1>Secret leak</h1>` +
            `<input id="secret-input" aria-label="Secret" />` +
            `<button id="leak-secret" onclick="
               const value = document.getElementById('secret-input').value;
               console.log('leak:' + value);
               document.getElementById('secret-out').textContent = value;
               fetch('/echo?value=' + encodeURIComponent(value)).catch(() => {});
             ">Leak</button>` +
            `<div id="secret-out"></div>` +
            `</body></html>`,
        )
        return
      case "/echo":
        res.writeHead(200, { "content-type": "text/plain" })
        res.end("ok")
        return
      case "/slow":
        setTimeout(() => {
          res.writeHead(200, { "content-type": "text/plain" })
          res.end("slow")
        }, 10_000)
        req.on("close", () => {
          if (!res.writableEnded) {
            aborted.push("/slow")
          }
        })
        return
      case "/download-large":
        res.writeHead(200, {
          "content-type": "application/octet-stream",
          "content-disposition": 'attachment; filename="large.bin"',
          "content-length": String(MAX_ARTIFACT_BYTES + 1),
        })
        res.end(Buffer.alloc(MAX_ARTIFACT_BYTES + 1, 0x61))
        return
      case "/boom":
        res.writeHead(500, { "content-type": "text/plain" })
        res.end("boom")
        return
      default:
        res.writeHead(404, { "content-type": "text/plain" })
        res.end("not found")
    }
  })
  server.on("upgrade", (req, socket) => {
    // A WebSocket handshake that reaches the server proves the allow-list guard
    // was bypassed (for example from a worker). Refuse it and record the path.
    upgrades.push(req.url)
    socket.destroy()
  })
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  const { port } = server.address()
  return {
    url: `http://127.0.0.1:${port}`,
    hits,
    aborted,
    upgrades,
    clearHits() {
      hits.length = 0
      aborted.length = 0
      upgrades.length = 0
    },
    async close() {
      server.closeAllConnections?.()
      await new Promise((resolve) => server.close(resolve))
    },
  }
}

/** Builds a JSON-friendly fetch client for the executor HTTP API. */
export function httpClient(baseUrl, token) {
  return async function request(path, options = {}) {
    const { method = "GET", body, raw, headers = {} } = options
    const requestToken = Object.prototype.hasOwnProperty.call(options, "token") ? options.token : token
    const response = await fetch(`${baseUrl}${path}`, {
      method,
      headers: {
        ...(requestToken ? { authorization: `Bearer ${requestToken}` } : {}),
        ...(body !== undefined || raw !== undefined ? { "content-type": "application/json" } : {}),
        ...headers,
      },
      body: raw ?? (body !== undefined ? JSON.stringify(body) : undefined),
    })
    const text = await response.text()
    let json
    try {
      json = JSON.parse(text)
    } catch {
      json = undefined
    }
    return { status: response.status, json, text }
  }
}

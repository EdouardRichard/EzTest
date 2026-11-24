import crypto from "node:crypto"
import http from "node:http"
import { once } from "node:events"

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
    <div id="clicked"></div>
    <input id="upload" type="file" aria-label="Upload" />
    <input id="upload-hidden" type="file" aria-label="Hidden upload" style="display:none" />
    <button id="upload-trigger" onclick="document.getElementById('upload-hidden').click()">Upload via dialog</button>
    <div id="uploaded"></div>
    <script>
      function mark(name) {
        document.getElementById('clicked').textContent = name
        console.log('clicked:' + name)
      }
      for (const id of ['upload', 'upload-hidden']) {
        document.getElementById(id).addEventListener('change', (event) => {
          const names = [...event.target.files].map((file) => file.name).join(',')
          document.getElementById('uploaded').textContent = 'uploaded:' + names
        })
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

// DEEP_LOGIN_HTML nests the form six levels below body, so the generated CSS
// path must keep every ancestor to resolve (SPA login modals routinely do
// this). No id/name/data-testid short-circuits selector generation.
const DEEP_LOGIN_HTML = `<!doctype html>
<html>
  <head><title>EzTest Deep Login</title></head>
  <body>
    <div class="app">
      <div class="shell">
        <div class="layer">
          <section class="panel">
            <form onsubmit="event.preventDefault()">
              <label>账号 <input aria-label="账号" /></label>
              <label>密码 <input type="password" aria-label="密码" /></label>
              <button type="submit">登录</button>
            </form>
          </section>
        </div>
      </div>
    </div>
  </body>
</html>`

const SPAM_HTML = `<!doctype html>
<html><head><title>Spam</title></head>
<body><h1>Spam</h1>
<script>for (let i = 0; i < 600; i += 1) { console.log('spam-' + i) }</script>
</body></html>`

// AC-010 (T15): the browser CLI capability fixtures. cli-home carries a
// double-click target, a file input (chooser flow), a same-origin iframe with an
// interactive button (cross-frame snapshot refs), a target=_blank popup link, and
// a button that appears after a delay (explicit wait strategies via run-code).
const CLI_HOME_HTML = `<!doctype html>
<html><head><title>CLI Home</title></head>
<body>
<h1>CLI Home</h1>
<button id="dbl" ondblclick="document.getElementById('dbl-out').textContent='double-clicked'">Double me</button>
<div id="dbl-out"></div>
<input id="text" aria-label="Text" />
<input id="file" type="file" aria-label="File" />
<div id="file-out"></div>
<iframe id="frame" src="/cli-child" width="320" height="140"></iframe>
<a id="popup" target="_blank" href="/cli-popup">Open tab</a>
<button id="late" style="display:none">Late button</button>
<script>
  document.getElementById('file').addEventListener('change', (e) => {
    document.getElementById('file-out').textContent = 'uploaded:' + [...e.target.files].map(f => f.name).join(',');
  });
  setTimeout(() => { document.getElementById('late').style.display = 'block'; }, 600);
</script>
</body></html>`

const CLI_CHILD_HTML = `<!doctype html>
<html><head><title>CLI Child</title></head>
<body>
<button id="inner" onclick="document.getElementById('inner-out').textContent='inner-clicked'">Inner button</button>
<div id="inner-out"></div>
</body></html>`

const CLI_POPUP_HTML = `<!doctype html>
<html><head><title>CLI Popup</title></head><body><h1>CLI Popup page</h1></body></html>`

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
      case "/deep-login":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(DEEP_LOGIN_HTML)
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
        // Same server, different hostname: resolves locally. GD-01 removed the
        // host allow list, so the cross-host redirect is followed.
        res.writeHead(302, { location: `http://localhost:${req.socket.localPort}/second` })
        res.end()
        return
      case "/redirect-home":
        res.writeHead(302, { location: "/second" })
        res.end()
        return
      case "/popup":
        // localhost is a different hostname than 127.0.0.1; GD-01 removed the
        // host allow list, so the popup navigation is allowed.
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>Popup</title></head><body><h1>Popup</h1>` +
            `<button id="open-popup" onclick="window.open('http://localhost:${req.socket.localPort}/popup-target')">Open popup</button>` +
            `</body></html>`,
        )
        return
      case "/popup-redirect":
        // Opens a URL that redirects to a different hostname (localhost).
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
        // GD-01: service workers are no longer blocked; registration succeeds
        // and the worker may issue its own requests.
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
        // Opened from the page itself: GD-01 removed the WebSocket guard, so
        // the cross-hostname handshake reaches the server and opens.
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
        // GD-01: page-created Worker/SharedWorker instances are no longer
        // blocked. The blob worker opens a WebSocket whose handshake reaches
        // the server, proving worker traffic is not intercepted.
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
      case "/visibility":
        // Mixes rendered and non-rendered interactive nodes so the inventory's
        // `visible` flag can be asserted for display:none, visibility:hidden and
        // the hidden attribute.
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(
          `<!doctype html><html><head><title>Visibility</title></head><body><h1>Visibility</h1>` +
            `<button id="visible-btn" onclick="mark('visible')">Visible</button>` +
            `<button id="hidden-display" style="display:none">Hidden display</button>` +
            `<button id="hidden-visibility" style="visibility:hidden">Hidden visibility</button>` +
            `<input id="hidden-password" type="password" aria-label="Hidden password" hidden />` +
            `<a id="visible-link" href="/second">Visible link</a>` +
            `</body></html>`,
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
      // AC-010 browser CLI fixtures (T15): double-click, file chooser, cross-frame
      // iframe, multi-tab popup, and a late-appearing element for wait strategies.
      case "/cli-home":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(CLI_HOME_HTML)
        return
      case "/cli-child":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(CLI_CHILD_HTML)
        return
      case "/cli-popup":
        res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
        res.end(CLI_POPUP_HTML)
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
    // GD-01: the executor installs no WebSocket guard, so handshakes reach the
    // server. Complete the handshake so pages/workers observe a real open event;
    // the recorded path proves the connection was not intercepted.
    upgrades.push(req.url)
    const key = req.headers["sec-websocket-key"]
    if (typeof key !== "string" || key.length === 0) {
      socket.destroy()
      return
    }
    const accept = crypto
      .createHash("sha1")
      .update(`${key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
      .digest("base64")
    socket.write(
      "HTTP/1.1 101 Switching Protocols\r\n" +
        "Upgrade: websocket\r\n" +
        "Connection: Upgrade\r\n" +
        `Sec-WebSocket-Accept: ${accept}\r\n` +
        "\r\n",
    )
    socket.on("error", () => {})
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
    const payload = raw ?? (body !== undefined ? JSON.stringify(body) : undefined)
    const requestHeaders = {
      ...(requestToken ? { authorization: `Bearer ${requestToken}` } : {}),
      ...(payload !== undefined ? { "content-type": "application/json" } : {}),
      ...headers,
    }
    const url = `${baseUrl}${path}`
    try {
      const response = await fetch(url, { method, headers: requestHeaders, body: payload })
      const text = await response.text()
      let json
      try {
        json = JSON.parse(text)
      } catch {
        json = undefined
      }
      return { status: response.status, json, text, headers: response.headers }
    } catch (error) {
      // Undici (global fetch) refuses the Fetch-spec "bad ports" such as 10080.
      // An ephemeral test listener can legitimately land on one, so fall back to
      // node:http for that case instead of making the test flaky.
      if (!isBadPortFetchError(error)) throw error
      return nodeHttpRequest(url, { method, headers: requestHeaders, body: payload })
    }
  }
}

function isBadPortFetchError(error) {
  return error instanceof TypeError && error.cause?.message === "bad port"
}

function nodeHttpRequest(url, { method, headers, body }) {
  return new Promise((resolve, reject) => {
    const request = http.request(url, { method, headers }, (response) => {
      const chunks = []
      response.on("data", (chunk) => chunks.push(chunk))
      response.on("end", () => {
        const text = Buffer.concat(chunks).toString("utf8")
        let json
        try {
          json = JSON.parse(text)
        } catch {
          json = undefined
        }
        const responseHeaders = new Headers()
        for (const [key, value] of Object.entries(response.headers)) {
          if (value === undefined) continue
          for (const item of Array.isArray(value) ? value : [value]) {
            responseHeaders.append(key, item)
          }
        }
        resolve({ status: response.statusCode, json, text, headers: responseHeaders })
      })
    })
    request.on("error", reject)
    if (body !== undefined) request.write(body)
    request.end()
  })
}


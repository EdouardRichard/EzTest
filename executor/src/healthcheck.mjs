/**
 * Container health probe: exits 0 only when GET /healthz returns 200.
 * Kept as a tiny ESM entry point so the Docker/Compose healthcheck does not
 * depend on curl being installed in the image.
 */

import http from "node:http"

const address = process.env.EZTEST_EXECUTOR_ADDR ?? "127.0.0.1:13336"
const separator = address.lastIndexOf(":")
// Always probe loopback: the container env sets ADDR to 0.0.0.0 for listen.
const host = "127.0.0.1"
const port = Number(address.slice(separator + 1)) || 13336

const request = http.get({ host, port, path: "/healthz", timeout: 4_000 }, (response) => {
  response.resume()
  process.exit(response.statusCode === 200 ? 0 : 1)
})
request.on("timeout", () => {
  request.destroy()
  process.exit(1)
})
request.on("error", () => {
  process.exit(1)
})

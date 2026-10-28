import test from "node:test"
import assert from "node:assert/strict"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import {
  ExecutorError,
  MAX_ARTIFACT_BYTES,
  MAX_REQUEST_BYTES,
  MAX_SECRET_ENTRIES,
  MAX_SECRET_LENGTH,
  ensureArtifactFileSize,
  ensureArtifactSize,
  isAllowedHost,
  isAllowedUrl,
  registerSecret,
  resolveTimeout,
} from "../src/security.mjs"

test("isAllowedHost matches exact hosts and subdomains only", () => {
  const allowed = ["127.0.0.1", "localhost", "*.example.com"]
  assert.equal(isAllowedHost("127.0.0.1", allowed), true)
  assert.equal(isAllowedHost("LOCALHOST", allowed), true)
  assert.equal(isAllowedHost("api.example.com", allowed), true)
  assert.equal(isAllowedHost("deep.api.example.com", allowed), true)
  // `*.suffix` must not match the apex unless it is listed explicitly.
  assert.equal(isAllowedHost("example.com", allowed), false)
  assert.equal(isAllowedHost("example.com", ["example.com", "*.example.com"]), true)
  assert.equal(isAllowedHost("evil-example.com", allowed), false)
  assert.equal(isAllowedHost("example.com.evil.test", allowed), false)
  assert.equal(isAllowedHost("127.0.0.1:13335", allowed), true)
})

test("the secret set rejects values beyond its entry and length bounds", () => {
  const secrets = new Set()
  for (let index = 0; index < MAX_SECRET_ENTRIES; index += 1) {
    registerSecret(secrets, `secret-${index}`)
  }
  assert.equal(secrets.size, MAX_SECRET_ENTRIES)
  assert.throws(
    () => registerSecret(secrets, "one-secret-too-many"),
    (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
  )
  // Earlier secrets stay tracked; nothing is evicted and therefore unscrubbed.
  assert.equal(secrets.has("secret-0"), true)
  assert.throws(
    () => registerSecret(new Set(), "x".repeat(MAX_SECRET_LENGTH + 1)),
    (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
  )
})

test("ensureArtifactFileSize rejects oversized files before they are read", async () => {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-size-"))
  const file = path.join(dir, "artifact.bin")
  try {
    await fsp.writeFile(file, Buffer.alloc(17))
    await assert.rejects(
      () => ensureArtifactFileSize(file, 16),
      (error) => error instanceof ExecutorError && error.code === "too_large",
    )
    assert.equal(await ensureArtifactFileSize(file, 17), 17)
  } finally {
    await fsp.rm(dir, { recursive: true, force: true })
  }
})

test("isAllowedUrl only accepts http(s) hosts from the allow list", () => {
  const options = { allowedHosts: ["127.0.0.1"], baseUrl: "http://base.example:8080" }
  assert.equal(isAllowedUrl("http://127.0.0.1:13335/home", options), true)
  assert.equal(isAllowedUrl("https://base.example/login", options), true)
  assert.equal(isAllowedUrl("http://other.example/", options), false)
  assert.equal(isAllowedUrl("file:///C:/Windows/win.ini", options), false)
  assert.equal(isAllowedUrl("javascript:alert(1)", options), false)
  assert.equal(isAllowedUrl("data:text/html,<h1>x</h1>", options), false)
  assert.equal(isAllowedUrl("data:text/html,<h1>x</h1>", { ...options, allowData: true }), true)
})

test("isAllowedUrl rejects malformed urls with invalid_arguments", () => {
  assert.throws(
    () => isAllowedUrl("not a url", { allowedHosts: ["127.0.0.1"] }),
    (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
  )
})

test("artifact and request limits are enforced", () => {
  assert.equal(MAX_REQUEST_BYTES, 1024 * 1024)
  assert.equal(MAX_ARTIFACT_BYTES, 10 * 1024 * 1024)
  assert.equal(ensureArtifactSize(Buffer.alloc(16)).length, 16)
  assert.throws(
    () => ensureArtifactSize(Buffer.alloc(MAX_ARTIFACT_BYTES + 1)),
    (error) => error instanceof ExecutorError && error.code === "too_large",
  )
})

test("resolveTimeout applies per-action defaults and clamps to the maximum", () => {
  assert.equal(resolveTimeout(undefined, 15_000), 15_000)
  assert.equal(resolveTimeout(1_000, 15_000), 1_000)
  assert.equal(resolveTimeout(999_999, 15_000), 60_000)
  assert.equal(resolveTimeout(0, 15_000), 1)
  assert.equal(resolveTimeout("abc", 15_000), 15_000)
})

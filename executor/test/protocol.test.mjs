import test from "node:test"
import assert from "node:assert/strict"

import * as protocol from "../src/protocol.mjs"
import { MAX_REQUEST_BYTES } from "../src/protocol.mjs"

// GD-01 (R-C3): the host allow list, scheme gate and secret-registration set are
// legacy mechanisms removed under the single-admin trusted deployment model.
// The module must no longer expose any of that surface.
test("the legacy allow-list and secret-registration surface is gone", () => {
  for (const name of [
    "isAllowedHost",
    "isAllowedUrl",
    "normalizeAllowedHosts",
    "hostFromUrl",
    "registerSecret",
    "MAX_SECRET_ENTRIES",
    "MAX_SECRET_LENGTH",
  ]) {
    assert.equal(protocol[name], undefined, `protocol.mjs must not export ${name}`)
  }
})

// Final-review finding 3 (DoD: no unwired code): the v1-era limits/helpers with
// no v2 production consumer are deleted, not parked. The live transfer bounds
// belong to files.mjs; the ring buffer to traffic_recorder.mjs.
test("the dead v1-era limits and helpers stay deleted", () => {
  for (const name of [
    "MAX_ARTIFACT_BYTES",
    "MAX_SNAPSHOT_CHARS",
    "DEFAULT_ACTION_TIMEOUT_MS",
    "GOTO_TIMEOUT_MS",
    "MAX_ACTION_TIMEOUT_MS",
    "RING_CAPACITY",
    "DEFAULT_MAX_CONTEXTS",
    "MAX_CONTEXT_METADATA_BYTES",
    "resolveTimeout",
    "ensureArtifactSize",
    "ensureArtifactFileSize",
    "pushRing",
  ]) {
    assert.equal(protocol[name], undefined, `protocol.mjs must not export ${name}`)
  }
})

test("the JSON request limit is enforced", () => {
  assert.equal(MAX_REQUEST_BYTES, 1024 * 1024)
})
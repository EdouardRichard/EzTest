import test from "node:test"
import assert from "node:assert/strict"

import {
  DEFAULT_MAX_SNAPSHOT_CHARS,
  stripSecretValues,
  truncateSnapshot,
} from "../src/snapshot.mjs"

test("truncateSnapshot keeps short snapshots untouched", () => {
  const result = truncateSnapshot("- heading \"Home\"", 12000)
  assert.equal(result.truncated, false)
  assert.equal(result.text, "- heading \"Home\"")
})

test("truncateSnapshot cuts long snapshots and flags truncated", () => {
  const result = truncateSnapshot("x".repeat(20_000), DEFAULT_MAX_SNAPSHOT_CHARS)
  assert.equal(result.truncated, true)
  assert.equal(result.text.length, DEFAULT_MAX_SNAPSHOT_CHARS)
})

test("truncateSnapshot clamps maxChars to the 12KB protocol limit", () => {
  const result = truncateSnapshot("y".repeat(20_000), 999_999)
  assert.equal(result.truncated, true)
  assert.equal(result.text.length, DEFAULT_MAX_SNAPSHOT_CHARS)
  const small = truncateSnapshot("z".repeat(200), 50)
  assert.equal(small.truncated, true)
  assert.equal(small.text.length, 50)
})

test("truncateSnapshot keeps multibyte snapshots within the 12KB byte budget", () => {
  const source = "中".repeat(6_000) // 18,000 UTF-8 bytes
  const result = truncateSnapshot(source, 12_000)
  assert.equal(result.truncated, true)
  assert.ok(Buffer.byteLength(result.text, "utf8") <= 12 * 1024)
  assert.equal(result.text.includes("\uFFFD"), false)
})

test("stripSecretValues masks every occurrence of known secret values", () => {
  const aria = [
    '- textbox "Username": alice',
    '- textbox "Password": top-secret-pw',
    '- textbox "Confirm": top-secret-pw',
  ].join("\n")
  const masked = stripSecretValues(aria, ["top-secret-pw", ""])
  assert.equal(masked.includes("top-secret-pw"), false)
  assert.equal(masked.split("***").length - 1, 2)
  assert.equal(masked.includes("alice"), true)
})

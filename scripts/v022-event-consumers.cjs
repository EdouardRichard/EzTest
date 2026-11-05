#!/usr/bin/env node
/*
 * EzTest v0.22 W1-5 gate: every publishable event type has a registered
 * consumer, and the registration is backed by real source, not by the
 * registry certifying itself.
 *
 * Registry: scripts/v022-w1-events.json
 *   { "events": [{ "type", "consumer": "ui"|"system", "evidence": "file:line",
 *                  "channel": "system"|"version:N", "prefix"?: "resource." }] }
 *
 * The published set comes from the W1-1 scanner (scanPublishedEvents), so the
 * registry must be set-equal to the source scan:
 *   - every published type is registered (no unregistered);
 *   - every registered type is still published (no unknown).
 *
 * Evidence is verified against the real source files:
 *   - ui entries: the evidence file exists, is under frontend/src, and contains
 *     the literal `case "<type>"`, or, for a prefix family, the literal
 *     `startsWith("<prefix>")` (and the type begins with that prefix);
 *   - system entries: the evidence file is exactly
 *     backend/internal/eventing/application/system_consumer.go, that file
 *     contains the `"system"` channel guard and a structured log call, and the
 *     entry's channel is `system`;
 *   - Topbar.tsx has a non-empty system-channel `onEvent` implementation that
 *     logs (`console.`) or refreshes (`refresh`).
 *
 * Exit 0 = all registered and source-verified, 1 = any problem.
 */
"use strict"

const fs = require("node:fs")
const path = require("node:path")

const { scanPublishedEvents } = require("./v022-w1-inventory.cjs")

const ROOT = path.resolve(__dirname, "..")
const REGISTRY = path.join(__dirname, "v022-w1-events.json")
const SYSTEM_CONSUMER = "backend/internal/eventing/application/system_consumer.go"
const TOPBAR = "frontend/src/features/shell/Topbar.tsx"

const readText = (relativePath) => fs.readFileSync(path.join(ROOT, relativePath), "utf8")

/** Split a `file:line` evidence string. */
function parseEvidence(evidence) {
  if (typeof evidence !== "string") return null
  const match = /^(.+):(\d+)$/.exec(evidence)
  if (!match) return null
  return { file: match[1], line: Number(match[2]) }
}

/** Extract every function body that follows an `onEvent` marker occurrence. */
function onEventBodies(source) {
  const bodies = []
  let from = 0
  for (;;) {
    const index = source.indexOf("onEvent", from)
    if (index < 0) break
    from = index + "onEvent".length

    // `onEvent: (event) => { ... }` and `onEvent = (event) => { ... }` both
    // put an assignment operator between the marker and the body brace.
    const candidates = [source.indexOf("=>", from), source.indexOf("=", from)]
      .filter((candidate) => candidate >= 0 && candidate - from < 120)
      .sort((a, b) => a - b)
    let brace = -1
    for (const candidate of candidates) {
      const opening = source.indexOf("{", candidate)
      if (opening >= 0 && opening - candidate < 120) {
        brace = opening
        break
      }
    }
    if (brace < 0) continue

    let depth = 1
    let cursor = brace + 1
    while (cursor < source.length && depth > 0) {
      if (source[cursor] === "{") depth += 1
      else if (source[cursor] === "}") depth -= 1
      cursor += 1
    }
    bodies.push(source.slice(brace + 1, cursor - 1))
  }
  return bodies
}

function main() {
  const problems = []

  let published
  try {
    published = new Set(scanPublishedEvents().map((event) => event.type))
  } catch (error) {
    console.error("Error: cannot scan publishable events: " + (error && error.message ? error.message : String(error)))
    process.exit(1)
  }

  let registry
  try {
    registry = JSON.parse(fs.readFileSync(REGISTRY, "utf8"))
  } catch (error) {
    console.error(
      "Error: cannot load registry scripts/v022-w1-events.json: " +
        (error && error.message ? error.message : String(error)),
    )
    process.exit(1)
  }

  if (!registry || !Array.isArray(registry.events)) {
    console.error("Error: registry must be an object with an `events` array")
    process.exit(1)
  }

  const byType = new Map()
  for (const entry of registry.events) {
    if (!entry || typeof entry.type !== "string" || entry.type.trim() === "") {
      problems.push("registry entry is missing a string `type`")
      continue
    }
    if (byType.has(entry.type)) {
      problems.push("duplicate registry entry: " + entry.type)
      continue
    }
    byType.set(entry.type, entry)

    if (entry.consumer !== "ui" && entry.consumer !== "system") {
      problems.push('consumer must be "ui" or "system": ' + entry.type)
    }
    if (typeof entry.evidence !== "string" || entry.evidence.trim() === "") {
      problems.push("consumer/evidence missing: " + entry.type)
    }
  }

  const unregistered = [...published].filter((type) => !byType.has(type))
  const unknown = [...byType.keys()].filter((type) => !published.has(type))
  for (const type of unregistered) problems.push("unregistered published event: " + type)
  for (const type of unknown) problems.push("registry has unknown event: " + type)

  let systemConsumerSource = null
  const systemConsumerRead = () => {
    if (systemConsumerSource !== null) return systemConsumerSource
    try {
      systemConsumerSource = readText(SYSTEM_CONSUMER)
    } catch (error) {
      problems.push("cannot read " + SYSTEM_CONSUMER + ": " + (error && error.message ? error.message : String(error)))
      systemConsumerSource = ""
    }
    return systemConsumerSource
  }

  for (const [type, entry] of byType) {
    if (!published.has(type)) continue
    if (entry.consumer !== "ui" && entry.consumer !== "system") continue
    if (typeof entry.evidence !== "string" || entry.evidence.trim() === "") continue

    const evidence = parseEvidence(entry.evidence)
    if (evidence === null) {
      problems.push('evidence must be "file:line": ' + type)
      continue
    }

    if (entry.consumer === "ui") {
      if (!evidence.file.startsWith("frontend/src/")) {
        problems.push("ui evidence must be under frontend/src: " + type + " -> " + evidence.file)
        continue
      }
      let source
      try {
        source = readText(evidence.file)
      } catch (error) {
        problems.push("ui evidence file missing: " + type + " -> " + evidence.file)
        continue
      }
      if (entry.prefix !== undefined) {
        if (typeof entry.prefix !== "string" || entry.prefix.length === 0) {
          problems.push("prefix entry needs a non-empty `prefix`: " + type)
        } else if (!type.startsWith(entry.prefix)) {
          problems.push("prefix does not match type: " + type + " !startsWith " + entry.prefix)
        } else if (!source.includes('startsWith("' + entry.prefix + '")')) {
          problems.push('ui evidence has no startsWith("' + entry.prefix + '"): ' + type + " -> " + evidence.file)
        }
      } else if (!source.includes('case "' + type + '"')) {
        problems.push('ui evidence has no case "' + type + '": ' + type + " -> " + evidence.file)
      }
      continue
    }

    if (evidence.file !== SYSTEM_CONSUMER) {
      problems.push("system evidence must be " + SYSTEM_CONSUMER + ": " + type + " -> " + evidence.file)
      continue
    }
    if (entry.channel !== "system") {
      problems.push('system entry channel must be "system": ' + type)
    }
    const source = systemConsumerRead()
    if (!source.includes('"system"')) {
      problems.push(SYSTEM_CONSUMER + ' must contain the "system" channel guard')
    }
    if (!/(?:logger|log)\.(?:Debug|DebugContext|Info|InfoContext|Warn|WarnContext|Error|ErrorContext)\s*\(/.test(source)) {
      problems.push(SYSTEM_CONSUMER + " must contain a structured log call")
    }
  }

  let topbar = ""
  try {
    topbar = readText(TOPBAR)
  } catch (error) {
    problems.push("cannot read " + TOPBAR + ": " + (error && error.message ? error.message : String(error)))
  }
  const topbarWired = onEventBodies(topbar).some(
    (body) => body.trim().length > 0 && (body.includes("console.") || body.includes("refresh")),
  )
  if (!topbarWired) {
    problems.push(TOPBAR + " system-channel onEvent must log (console.) or refresh (refresh): implementation is empty")
  }

  const ui = [...byType.values()].filter((entry) => entry.consumer === "ui").length
  const system = [...byType.values()].filter((entry) => entry.consumer === "system").length
  console.log(`unregistered=${unregistered.length} unknown=${unknown.length}`)

  if (problems.length > 0) {
    console.error(problems.join("\n"))
    process.exit(1)
  }

  console.log(`events=${published.size} ui=${ui} system=${system}`)
  console.log(`gate OK: ${published.size} published event types are registered and source-verified`)
}

main()

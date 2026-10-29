/**
 * Visual assertion comparison (design spec §9.3 / D7).
 *
 * The API sends only the approved baseline copy and the comparison
 * configuration. The executor captures the actual screenshot in the live page,
 * verifies the viewport/device-scale-factor fingerprint, and compares pixels
 * with pixelmatch. A missing baseline or an environment mismatch is reported to
 * the API (environment_match=false / no diff artifact) so the caller classifies
 * it inconclusive instead of failing the application.
 */

import crypto from "node:crypto"

import pixelmatch from "pixelmatch"
import { PNG } from "pngjs"

import { ExecutorError, invalidArguments } from "./security.mjs"

export const DEFAULT_VISUAL_BASELINE_MAX_BYTES = 5 * 1024 * 1024
export const MAX_VISUAL_THRESHOLD = 0.1
export const MIN_VISUAL_DEVICE_SCALE_FACTOR = 0.5
export const MAX_VISUAL_DEVICE_SCALE_FACTOR = 4
export const MAX_VISUAL_VIEWPORT_PX = 10_000
export const MAX_VISUAL_SELECTOR_LENGTH = 500

/** Per-pixel color-distance threshold of pixelmatch (independent of diffRatio). */
const PIXELMATCH_THRESHOLD = 0.1
/** Maximum number of mismatch boxes returned to the API. */
const MAX_MISMATCH_BOXES = 50
/** Side of one coarse mismatch box, in device pixels. */
const MISMATCH_BOX_SIZE = 32

/** Reads EZTEST_VISUAL_BASELINE_MAX_BYTES with the 5 MiB spec default. */
export function visualBaselineMaxBytes(env = process.env) {
  const raw = Number(env.EZTEST_VISUAL_BASELINE_MAX_BYTES)
  return Number.isInteger(raw) && raw > 0 ? raw : DEFAULT_VISUAL_BASELINE_MAX_BYTES
}

/** Decodes one PNG buffer, rejecting anything that is not a valid PNG. */
export function decodePng(buffer, label = "image") {
  if (!Buffer.isBuffer(buffer) || buffer.length === 0) {
    throw invalidArguments(`${label} must be a non-empty PNG buffer`)
  }
  try {
    return PNG.sync.read(buffer)
  } catch {
    throw invalidArguments(`${label} is not a valid PNG image`)
  }
}

function finiteNumber(value, label) {
  const numeric = typeof value === "number" ? value : Number(value)
  if (!Number.isFinite(numeric)) {
    throw invalidArguments(`${label} must be a finite number`)
  }
  return numeric
}

function positiveInteger(value, label, max = MAX_VISUAL_VIEWPORT_PX) {
  const numeric = Number(value)
  if (!Number.isInteger(numeric) || numeric <= 0 || numeric > max) {
    throw invalidArguments(`${label} must be a positive integer up to ${max}`)
  }
  return numeric
}

/**
 * Parses the normalized {x,y,width,height} crop. Absent or empty means the
 * whole image.
 */
export function parseVisualRegion(input) {
  if (input === undefined || input === null) {
    return null
  }
  if (typeof input !== "object" || Array.isArray(input)) {
    throw invalidArguments("region must be an object with x, y, width and height")
  }
  const region = {
    x: finiteNumber(input.x, "region.x"),
    y: finiteNumber(input.y, "region.y"),
    width: finiteNumber(input.width, "region.width"),
    height: finiteNumber(input.height, "region.height"),
  }
  if (region.width === 0 && region.height === 0 && region.x === 0 && region.y === 0) {
    return null
  }
  if (region.x < 0 || region.y < 0 || region.width <= 0 || region.height <= 0) {
    throw invalidArguments("region x/y must be non-negative and width/height positive")
  }
  if (region.x + region.width > 1 + 1e-9 || region.y + region.height > 1 + 1e-9) {
    throw invalidArguments("region must stay inside the normalized 0..1 image")
  }
  return region
}

/**
 * Validates and normalizes the visual.compare arguments. The baseline is capped
 * by EZTEST_VISUAL_BASELINE_MAX_BYTES (default 5 MiB).
 */
export function parseVisualCompareArgs(args, { env = process.env } = {}) {
  if (!args || typeof args !== "object" || Array.isArray(args)) {
    throw invalidArguments("args must be an object")
  }
  const encoded = args.baseline_base64
  if (typeof encoded !== "string" || encoded.length === 0) {
    throw invalidArguments("baseline_base64 is required")
  }
  const baseline = Buffer.from(encoded, "base64")
  if (baseline.length === 0) {
    throw invalidArguments("baseline_base64 is not valid base64")
  }
  const maxBytes = visualBaselineMaxBytes(env)
  if (baseline.length > maxBytes) {
    throw new ExecutorError(
      "too_large",
      `visual baseline is ${baseline.length} bytes, exceeding the ${maxBytes} byte limit`,
    )
  }

  let selector = ""
  if (args.selector !== undefined) {
    if (typeof args.selector !== "string") {
      throw invalidArguments("selector must be a string")
    }
    selector = args.selector.trim()
    if (selector.length > MAX_VISUAL_SELECTOR_LENGTH) {
      throw invalidArguments(
        `selector must be at most ${MAX_VISUAL_SELECTOR_LENGTH} characters`,
      )
    }
  }

  const threshold = finiteNumber(args.threshold, "threshold")
  if (threshold < 0 || threshold > MAX_VISUAL_THRESHOLD) {
    throw invalidArguments(`threshold must be between 0 and ${MAX_VISUAL_THRESHOLD}`)
  }

  const viewportInput = args.viewport
  if (!viewportInput || typeof viewportInput !== "object" || Array.isArray(viewportInput)) {
    throw invalidArguments("viewport must be an object with width and height")
  }
  const viewport = {
    width: positiveInteger(viewportInput.width, "viewport.width"),
    height: positiveInteger(viewportInput.height, "viewport.height"),
  }

  const deviceScaleFactor = finiteNumber(args.device_scale_factor, "device_scale_factor")
  if (
    deviceScaleFactor < MIN_VISUAL_DEVICE_SCALE_FACTOR ||
    deviceScaleFactor > MAX_VISUAL_DEVICE_SCALE_FACTOR
  ) {
    throw invalidArguments(
      `device_scale_factor must be between ${MIN_VISUAL_DEVICE_SCALE_FACTOR} and ${MAX_VISUAL_DEVICE_SCALE_FACTOR}`,
    )
  }

  return {
    baseline,
    selector,
    region: parseVisualRegion(args.region),
    threshold,
    viewport,
    deviceScaleFactor,
  }
}

/** True when the live viewport/DPR fingerprint matches the configured one. */
export function environmentMatches(live, viewport, deviceScaleFactor) {
  if (!live || typeof live !== "object") {
    return false
  }
  return (
    Number(live.width) === viewport.width &&
    Number(live.height) === viewport.height &&
    Math.abs(Number(live.dpr) - deviceScaleFactor) <= 0.01
  )
}

/**
 * Crops one decoded PNG to the normalized region. Returns a plain
 * {width, height, data} image so the comparison never depends on pngjs internals.
 */
export function cropPng(png, region) {
  if (!region) {
    return { width: png.width, height: png.height, data: png.data }
  }
  const x = Math.min(png.width - 1, Math.max(0, Math.round(region.x * png.width)))
  const y = Math.min(png.height - 1, Math.max(0, Math.round(region.y * png.height)))
  const width = Math.max(1, Math.min(png.width - x, Math.round(region.width * png.width)))
  const height = Math.max(1, Math.min(png.height - y, Math.round(region.height * png.height)))
  const data = Buffer.alloc(width * height * 4)
  for (let row = 0; row < height; row += 1) {
    const sourceStart = ((y + row) * png.width + x) * 4
    const targetStart = row * width * 4
    png.data.copy(data, targetStart, sourceStart, sourceStart + width * 4)
  }
  return { width, height, data }
}

/**
 * Collects the coarse rectangles that contain differing pixels. Each box is one
 * 32x32 device-pixel cell with any mismatch, clipped to the image and capped so
 * a full-page change cannot produce an unbounded response.
 */
export function mismatchBoxes(left, right, width, height) {
  const boxes = []
  for (let top = 0; top < height; top += MISMATCH_BOX_SIZE) {
    for (let leftX = 0; leftX < width; leftX += MISMATCH_BOX_SIZE) {
      const boxWidth = Math.min(MISMATCH_BOX_SIZE, width - leftX)
      const boxHeight = Math.min(MISMATCH_BOX_SIZE, height - top)
      let differs = false
      for (let row = 0; row < boxHeight && !differs; row += 1) {
        const start = ((top + row) * width + leftX) * 4
        const end = start + boxWidth * 4
        for (let index = start; index < end; index += 4) {
          if (
            left[index] !== right[index] ||
            left[index + 1] !== right[index + 1] ||
            left[index + 2] !== right[index + 2] ||
            left[index + 3] !== right[index + 3]
          ) {
            differs = true
            break
          }
        }
      }
      if (differs) {
        boxes.push({ x: leftX, y: top, width: boxWidth, height: boxHeight })
        if (boxes.length >= MAX_MISMATCH_BOXES) {
          return boxes
        }
      }
    }
  }
  return boxes
}

/**
 * Compares two decoded images after applying the same normalized crop. The
 * images must have the same pixel size; a mismatch means the approved baseline
 * was captured with a different configuration and is reported as internal (it
 * is not an application defect).
 */
export function comparePngImages(baselinePng, actualPng, { threshold, region } = {}) {
  const baseline = cropPng(baselinePng, region || null)
  const actual = cropPng(actualPng, region || null)
  if (baseline.width !== actual.width || baseline.height !== actual.height) {
    throw new ExecutorError(
      "internal",
      `baseline image ${baseline.width}x${baseline.height} does not match actual ${actual.width}x${actual.height}`,
    )
  }
  const width = baseline.width
  const height = baseline.height
  const diff = new PNG({ width, height })
  const diffPixels = pixelmatch(baseline.data, actual.data, diff.data, width, height, {
    threshold: PIXELMATCH_THRESHOLD,
  })
  return {
    diffRatio: width * height === 0 ? 0 : diffPixels / (width * height),
    width,
    height,
    mismatchBoxes: mismatchBoxes(baseline.data, actual.data, width, height),
    diffPng: PNG.sync.write(diff),
  }
}

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

function artifact(kind, mime, buffer) {
  return {
    kind,
    mime,
    base64: buffer.toString("base64"),
    sha256: sha256(buffer),
    bytes: buffer.length,
  }
}

/**
 * Runs the visual.compare action for one context entry. Returns the
 * protocol result object and the visual_actual/visual_diff artifacts.
 */
export async function runVisualCompare(entry, args, timeoutMs) {
  const config = parseVisualCompareArgs(args)
  const page = entry?.page
  if (!page) {
    throw invalidArguments("the context has no page")
  }
  const baselinePng = decodePng(config.baseline, "baseline")
  if (baselinePng.width <= 0 || baselinePng.height <= 0) {
    throw invalidArguments("baseline PNG has no pixels")
  }

  let actualBuffer
  if (config.selector !== "") {
    const locator = page.locator(config.selector)
    const count = await locator.count()
    if (count === 0) {
      throw new ExecutorError("target_not_found", `no element matched selector=${config.selector}`)
    }
    if (count > 1) {
      throw new ExecutorError(
        "target_ambiguous",
        `${count} elements matched selector=${config.selector}; refine the selector`,
      )
    }
    const single = locator.first()
    await single.waitFor({ state: "visible", timeout: timeoutMs })
    actualBuffer = await single.screenshot({ type: "png", timeout: timeoutMs })
  } else {
    actualBuffer = await page.screenshot({ type: "png", fullPage: false, timeout: timeoutMs })
  }
  const actualPng = decodePng(actualBuffer, "actual screenshot")

  const live = await page.evaluate(() => ({
    width: window.innerWidth,
    height: window.innerHeight,
    dpr: window.devicePixelRatio,
  }))
  const matches = environmentMatches(live, config.viewport, config.deviceScaleFactor)

  const result = {
    environment_match: matches,
    width: 0,
    height: 0,
    diff_ratio: 0,
    mismatch_boxes: [],
    actual_width: actualPng.width,
    actual_height: actualPng.height,
    actual_device_scale_factor: Number(live.dpr) || config.deviceScaleFactor,
  }
  const artifacts = [artifact("visual_actual", "image/png", actualBuffer)]
  if (!matches) {
    // Do not compare pixels across different environments: the caller must
    // classify this inconclusive(environment), not a visual mismatch.
    return { result, artifacts }
  }

  const wantedWidth = Math.round(config.viewport.width * config.deviceScaleFactor)
  const wantedHeight = Math.round(config.viewport.height * config.deviceScaleFactor)
  if (
    config.selector === "" &&
    (actualPng.width !== wantedWidth || actualPng.height !== wantedHeight)
  ) {
    // The live screenshot disagrees with the configured device-pixel viewport:
    // an environment problem, never a visual mismatch.
    result.environment_match = false
    return { result, artifacts }
  }
  if (baselinePng.width !== actualPng.width || baselinePng.height !== actualPng.height) {
    if (config.selector === "") {
      result.environment_match = false
      return { result, artifacts }
    }
    throw new ExecutorError(
      "internal",
      `baseline image ${baselinePng.width}x${baselinePng.height} does not match the element screenshot ${actualPng.width}x${actualPng.height}`,
    )
  }

  const comparison = comparePngImages(baselinePng, actualPng, {
    threshold: config.threshold,
    region: config.region,
  })
  result.width = comparison.width
  result.height = comparison.height
  result.diff_ratio = comparison.diffRatio
  result.mismatch_boxes = comparison.mismatchBoxes
  artifacts.push(artifact("visual_diff", "image/png", comparison.diffPng))
  return { result, artifacts }
}

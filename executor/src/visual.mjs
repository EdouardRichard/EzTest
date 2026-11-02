/**
 * Visual assertion comparison (design spec §4.6 / §7.3 / §7.4).
 *
 * The API sends only the approved baseline copy (or no baseline for a
 * capture-only first run) and the comparison configuration. The executor
 * captures the actual screenshot in the live page, verifies the
 * viewport/device-scale-factor fingerprint, and compares pixels with
 * pixelmatch. A missing baseline or an environment mismatch is reported to the
 * API (environment_match=false / no diff artifact) so the caller classifies it
 * inconclusive instead of failing the application.
 *
 * The baseline bytes never touch the executor disk; they are decoded in memory
 * and discarded after the comparison.
 */

import crypto from "node:crypto"

import pixelmatch from "pixelmatch"
import { PNG } from "pngjs"

import {
  DEFAULT_VISUAL_BASELINE_MAX_BYTES,
  ExecutorError,
  MAX_ARTIFACT_BYTES,
  invalidArguments,
} from "./security.mjs"

export const MAX_VISUAL_THRESHOLD = 0.1
export const MIN_VISUAL_DEVICE_SCALE_FACTOR = 0.5
export const MAX_VISUAL_DEVICE_SCALE_FACTOR = 4
export const MAX_VISUAL_VIEWPORT_PX = 10_000
export const MAX_VISUAL_SELECTOR_LENGTH = 500
export const DEFAULT_VISUAL_WAIT_TIMEOUT_MS = 15_000

/** Per-pixel color-distance threshold of pixelmatch (independent of diffRatio). */
const PIXELMATCH_THRESHOLD = 0.1
/** Maximum number of mismatch boxes returned to the API. */
const MAX_MISMATCH_BOXES = 50
/** Side of one coarse mismatch box, in device pixels. */
const MISMATCH_BOX_SIZE = 32
/** Default stabilization frames inserted before the screenshot. */
const DEFAULT_STABLE_FRAMES = 2

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

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
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
 * Parses the wait policy override. The default is network idle + fonts ready +
 * frozen animations/transitions/caret + two stable frames (spec §4.6/§7.3).
 */
export function parseVisualWaitPolicy(input) {
  const policy = {
    network_idle: true,
    fonts: true,
    freeze_animations: true,
    stable_frames: DEFAULT_STABLE_FRAMES,
  }
  if (input === undefined || input === null) {
    return policy
  }
  if (typeof input !== "object" || Array.isArray(input)) {
    throw invalidArguments("wait_policy must be an object")
  }
  for (const key of Object.keys(input)) {
    if (!(key in policy)) {
      throw invalidArguments(`wait_policy.${key} is not supported`)
    }
  }
  for (const key of ["network_idle", "fonts", "freeze_animations"]) {
    if (input[key] !== undefined) {
      if (typeof input[key] !== "boolean") {
        throw invalidArguments(`wait_policy.${key} must be a boolean`)
      }
      policy[key] = input[key]
    }
  }
  if (input.stable_frames !== undefined) {
    const frames = Number(input.stable_frames)
    if (!Number.isInteger(frames) || frames < 1 || frames > 5) {
      throw invalidArguments("wait_policy.stable_frames must be an integer between 1 and 5")
    }
    policy.stable_frames = frames
  }
  return policy
}

/**
 * Decodes the optional baseline. A null/absent/empty baseline selects the
 * capture-only first run (no pixel comparison, classification is decided by the
 * API).
 */
function parseBaseline(input, env) {
  if (
    input === undefined ||
    input === null ||
    (typeof input === "object" && !Array.isArray(input) && Object.keys(input).length === 0)
  ) {
    return null
  }
  let encoded
  let expectedSHA
  let expectedBytes
  if (typeof input === "string") {
    encoded = input
  } else if (typeof input === "object" && !Array.isArray(input)) {
    encoded = input.base64
    expectedSHA = input.sha256
    expectedBytes = input.bytes
  } else {
    throw invalidArguments("baseline must be an object with base64 or null")
  }
  if (typeof encoded !== "string" || encoded.length === 0) {
    throw invalidArguments("baseline.base64 is required")
  }
  const baseline = Buffer.from(encoded, "base64")
  if (baseline.length === 0) {
    throw invalidArguments("baseline.base64 is not valid base64")
  }
  const maxBytes = visualBaselineMaxBytes(env)
  if (baseline.length > maxBytes) {
    throw new ExecutorError(
      "too_large",
      `visual baseline is ${baseline.length} bytes, exceeding the ${maxBytes} byte limit`,
    )
  }
  if (expectedBytes !== undefined) {
    if (!Number.isInteger(Number(expectedBytes)) || Number(expectedBytes) !== baseline.length) {
      throw invalidArguments(
        `baseline.bytes ${expectedBytes} does not match the decoded ${baseline.length} bytes`,
      )
    }
  }
  if (expectedSHA !== undefined) {
    if (typeof expectedSHA !== "string" || !/^[0-9a-f]{64}$/.test(expectedSHA)) {
      throw invalidArguments("baseline.sha256 must be 64 lowercase hex characters")
    }
    if (sha256(baseline) !== expectedSHA) {
      throw invalidArguments("baseline.sha256 does not match the decoded baseline bytes")
    }
  }
  return baseline
}

/**
 * Validates and normalizes the visual.compare arguments. The baseline is capped
 * by EZTEST_VISUAL_BASELINE_MAX_BYTES (default 5 MiB); absence means
 * capture-only.
 */
export function parseVisualCompareArgs(args, { env = process.env } = {}) {
  if (!args || typeof args !== "object" || Array.isArray(args)) {
    throw invalidArguments("args must be an object")
  }
  let baselineInput = args.baseline
  if (baselineInput === undefined && args.baseline_base64 !== undefined) {
    // P5 foundation compatibility: the pre-contract argument shape.
    baselineInput = args.baseline_base64
  }
  const baseline = parseBaseline(baselineInput, env)

  let selector = ""
  if (args.selector !== undefined) {
    if (typeof args.selector !== "string") {
      throw invalidArguments("selector must be a string")
    }
    selector = args.selector.trim()
    if ([...selector].length > MAX_VISUAL_SELECTOR_LENGTH) {
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
    captureOnly: baseline === null,
    selector,
    region: parseVisualRegion(args.region),
    threshold,
    viewport,
    deviceScaleFactor,
    waitPolicy: parseVisualWaitPolicy(args.wait_policy),
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
 * Canonical environment fingerprint (spec §7.3): sha256 over the configured
 * viewport/DPR, selector/region, config revision and browser identity. The API
 * stores this with an approved baseline and recomputes it for later runs.
 */
export function environmentFingerprint(input) {
  if (!input || typeof input !== "object") {
    throw invalidArguments("environment must be an object")
  }
  const region = input.region
    ? {
        x: Number(input.region.x),
        y: Number(input.region.y),
        width: Number(input.region.width),
        height: Number(input.region.height),
      }
    : null
  const canonical = {
    viewport_width: Number(input.viewport_width),
    viewport_height: Number(input.viewport_height),
    device_scale_factor: Number(input.device_scale_factor),
    selector: input.selector ?? "",
    region,
    config_revision: Number(input.config_revision ?? 0),
    browser_name: input.browser_name ?? input.browser ?? "",
    browser_version: input.browser_version ?? input.version ?? "",
  }
  return sha256(Buffer.from(JSON.stringify(canonical), "utf8"))
}

/** Browser identity of the live context, when the page exposes it. */
function browserInfo(entry) {
  try {
    const browser = entry?.context?.browser?.()
    const name = browser?.browserType?.()?.name?.()
    const version = browser?.version?.()
    return {
      browser: typeof name === "string" && name.length > 0 ? name : null,
      version: typeof version === "string" && version.length > 0 ? version : null,
    }
  } catch {
    return { browser: null, version: null }
  }
}

/** Races one promise against the visual wait deadline. */
function withDeadline(promise, timeoutMs, label) {
  let timer
  return Promise.race([
    promise,
    new Promise((_resolve, reject) => {
      timer = setTimeout(
        () => reject(new ExecutorError("timeout", `${label} exceeded ${timeoutMs}ms`)),
        timeoutMs,
      )
    }),
  ]).finally(() => clearTimeout(timer))
}

/**
 * Applies the configured viewport when the executor context was created with a
 * different one (P3 contexts default to 1280x720). DPR cannot change at runtime;
 * a DPR mismatch stays an environment mismatch.
 */
async function applyConfiguredViewport(page, viewport, timeoutMs) {
  if (typeof page?.setViewportSize !== "function") {
    return
  }
  let live
  try {
    live = await page.evaluate(() => ({
      width: window.innerWidth,
      height: window.innerHeight,
      dpr: window.devicePixelRatio,
    }))
  } catch {
    return
  }
  if (Number(live?.width) === viewport.width && Number(live?.height) === viewport.height) {
    return
  }
  await withDeadline(
    page.setViewportSize({ width: viewport.width, height: viewport.height }),
    Math.min(timeoutMs, DEFAULT_VISUAL_WAIT_TIMEOUT_MS),
    "visual viewport",
  )
}

/**
 * Applies the wait policy (network idle + fonts + frozen animations + stable
 * frames) before the screenshot. Wait failures are reported to the caller so the
 * API can classify the capture as invalid instead of comparing a half-rendered
 * page.
 */
export async function stabilizePage(page, policy, timeoutMs) {
  const budget = Math.min(timeoutMs, DEFAULT_VISUAL_WAIT_TIMEOUT_MS)
  if (
    policy.network_idle &&
    typeof page?.waitForLoadState === "function"
  ) {
    await withDeadline(page.waitForLoadState("networkidle"), budget, "visual network idle")
  }
  if (typeof page?.evaluate === "function") {
    await withDeadline(
      page.evaluate(
        async ({ fonts, freeze, frames }) => {
          if (fonts && typeof document !== "undefined" && document.fonts?.ready) {
            await document.fonts.ready
          }
          if (freeze && typeof document !== "undefined") {
            const style = document.createElement("style")
            style.textContent =
              "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important;}"
            document.documentElement.appendChild(style)
          }
          if (frames > 0 && typeof requestAnimationFrame === "function") {
            for (let index = 0; index < frames; index += 1) {
              await new Promise((resolve) => requestAnimationFrame(() => resolve(null)))
            }
          }
        },
        {
          fonts: policy.fonts,
          freeze: policy.freeze_animations,
          frames: policy.stable_frames,
        },
      ),
      budget,
      "visual stabilization",
    )
  }
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
 * images must have the same pixel size; runVisualCompare classifies a size
 * difference as an environment mismatch before calling this helper, so the
 * check is a defensive invariant against comparing misaligned buffers.
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
    diffPixels,
    totalPixels: width * height,
    width,
    height,
    mismatchBoxes: mismatchBoxes(baseline.data, actual.data, width, height),
    diffPng: PNG.sync.write(diff),
  }
}

function artifact(kind, mime, buffer) {
  if (buffer.length > MAX_ARTIFACT_BYTES) {
    throw new ExecutorError(
      "too_large",
      `visual artifact is ${buffer.length} bytes, exceeding the ${MAX_ARTIFACT_BYTES} byte limit`,
    )
  }
  return {
    kind,
    mime,
    base64: buffer.toString("base64"),
    sha256: sha256(buffer),
    bytes: buffer.length,
  }
}

/**
 * Runs the visual.compare action for one context entry. Returns the protocol
 * result object and the actual_png/diff_png artifacts. A missing baseline is a
 * capture-only run: only the actual image is returned and no pixel comparison is
 * fabricated.
 */
export async function runVisualCompare(entry, args, timeoutMs) {
  const config = parseVisualCompareArgs(args)
  const page = entry?.page
  if (!page) {
    throw invalidArguments("the context has no page")
  }
  const browser = browserInfo(entry)

  await applyConfiguredViewport(page, config.viewport, timeoutMs)
  await stabilizePage(page, config.waitPolicy, timeoutMs)

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
    capture_only: config.captureOnly,
    environment_match: matches,
    environment: {
      viewport_width: config.viewport.width,
      viewport_height: config.viewport.height,
      device_scale_factor: Number(live.dpr) || config.deviceScaleFactor,
      browser: browser.browser,
      version: browser.version,
    },
    width: 0,
    height: 0,
    diff_ratio: 0,
    diff_pixels: 0,
    total_pixels: 0,
    mismatch_boxes: [],
    actual_width: actualPng.width,
    actual_height: actualPng.height,
    actual_device_scale_factor: Number(live.dpr) || config.deviceScaleFactor,
  }
  const artifacts = [artifact("actual_png", "image/png", actualBuffer)]
  const wantedWidth = Math.round(config.viewport.width * config.deviceScaleFactor)
  const wantedHeight = Math.round(config.viewport.height * config.deviceScaleFactor)
  if (
    config.selector === "" &&
    (actualPng.width !== wantedWidth || actualPng.height !== wantedHeight)
  ) {
    // The live screenshot disagrees with the configured device-pixel viewport:
    // an environment problem, never a visual mismatch.
    result.environment_match = false
  }
  if (config.captureOnly || !result.environment_match) {
    // Capture-only or environment mismatch: never compare across environments
    // and never fabricate a diff artifact.
    return { result, artifacts }
  }

  const baselinePng = decodePng(config.baseline, "baseline")
  if (baselinePng.width <= 0 || baselinePng.height <= 0) {
    throw invalidArguments("baseline PNG has no pixels")
  }
  if (baselinePng.width !== actualPng.width || baselinePng.height !== actualPng.height) {
    // The approved baseline and the live capture disagree in size. A full-page
    // assertion means the viewport or DPR changed under the baseline; a selector
    // assertion means the element box or its device-pixel size changed. Both are
    // environment changes: report a mismatch (no diff artifact) so the caller
    // classifies inconclusive(environment), never an internal error.
    result.environment_match = false
    return { result, artifacts }
  }

  const comparison = comparePngImages(baselinePng, actualPng, {
    threshold: config.threshold,
    region: config.region,
  })
  result.width = comparison.width
  result.height = comparison.height
  result.diff_ratio = comparison.diffRatio
  result.diff_pixels = comparison.diffPixels
  result.total_pixels = comparison.totalPixels
  result.mismatch_boxes = comparison.mismatchBoxes
  artifacts.push(artifact("diff_png", "image/png", comparison.diffPng))
  return { result, artifacts }
}

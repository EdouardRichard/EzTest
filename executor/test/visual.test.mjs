import test from "node:test"
import assert from "node:assert/strict"

import { PNG } from "pngjs"

import { ExecutorError } from "../src/security.mjs"
import {
  comparePngImages,
  cropPng,
  decodePng,
  environmentMatches,
  mismatchBoxes,
  parseVisualCompareArgs,
  parseVisualRegion,
  runVisualCompare,
} from "../src/visual.mjs"

/** Builds one PNG buffer; paint(x, y) returns [r, g, b, a]. */
function makePng(width, height, paint = () => [10, 20, 30, 255]) {
  const png = new PNG({ width, height })
  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const [r, g, b, a] = paint(x, y)
      const index = (y * width + x) * 4
      png.data[index] = r
      png.data[index + 1] = g
      png.data[index + 2] = b
      png.data[index + 3] = a
    }
  }
  return PNG.sync.write(png)
}

function baseArgs(overrides = {}) {
  return {
    baseline_base64: makePng(100, 100).toString("base64"),
    threshold: 0.01,
    viewport: { width: 100, height: 100 },
    device_scale_factor: 1,
    ...overrides,
  }
}

function assertInvalid(run, label) {
  assert.throws(run, (error) => {
    assert.ok(error instanceof ExecutorError, `${label}: expected ExecutorError, got ${error}`)
    assert.equal(error.code, "invalid_arguments", `${label}: ${error.message}`)
    return true
  })
}

test("parseVisualCompareArgs validates every bound", () => {
  const parsed = parseVisualCompareArgs(
    baseArgs({
      selector: " #chart ",
      region: { x: 0.1, y: 0.2, width: 0.5, height: 0.3 },
    }),
  )
  assert.equal(parsed.selector, "#chart")
  assert.equal(parsed.threshold, 0.01)
  assert.deepEqual(parsed.viewport, { width: 100, height: 100 })
  assert.equal(parsed.deviceScaleFactor, 1)
  assert.deepEqual(parsed.region, { x: 0.1, y: 0.2, width: 0.5, height: 0.3 })
  assert.ok(Buffer.isBuffer(parsed.baseline))

  assertInvalid(() => parseVisualCompareArgs(baseArgs({ baseline_base64: "" })), "empty baseline")
  assertInvalid(() => parseVisualCompareArgs(baseArgs({ threshold: 0.5 })), "threshold high")
  assertInvalid(() => parseVisualCompareArgs(baseArgs({ threshold: -0.1 })), "threshold low")
  assertInvalid(
    () => parseVisualCompareArgs(baseArgs({ viewport: { width: 0, height: 100 } })),
    "viewport",
  )
  assertInvalid(
    () => parseVisualCompareArgs(baseArgs({ device_scale_factor: 8 })),
    "device scale factor",
  )
  assertInvalid(
    () => parseVisualCompareArgs(baseArgs({ selector: "x".repeat(501) })),
    "long selector",
  )
  assertInvalid(
    () =>
      parseVisualCompareArgs(
        baseArgs({ region: { x: 0.8, y: 0, width: 0.5, height: 0.5 } }),
      ),
    "region outside",
  )
})

test("parseVisualCompareArgs enforces the configured baseline byte cap", () => {
  const env = { EZTEST_VISUAL_BASELINE_MAX_BYTES: "16" }
  assert.throws(
    () => parseVisualCompareArgs(baseArgs(), { env }),
    (error) => error instanceof ExecutorError && error.code === "too_large",
  )
})

test("parseVisualRegion treats an empty object as no crop", () => {
  assert.equal(parseVisualRegion(undefined), null)
  assert.equal(parseVisualRegion({ x: 0, y: 0, width: 0, height: 0 }), null)
  assert.deepEqual(parseVisualRegion({ x: 0, y: 0, width: 1, height: 1 }), {
    x: 0,
    y: 0,
    width: 1,
    height: 1,
  })
})

test("decodePng rejects non-PNG bytes", () => {
  assert.throws(
    () => decodePng(Buffer.from("not a png")),
    (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
  )
})

test("cropPng applies normalized coordinates", () => {
  const png = PNG.sync.read(makePng(100, 80, (x, y) => [x, y, 0, 255]))
  const cropped = cropPng(png, { x: 0.1, y: 0.25, width: 0.5, height: 0.5 })
  assert.equal(cropped.width, 50)
  assert.equal(cropped.height, 40)
  // The crop starts at (10, 20), so its first pixel is that source pixel.
  assert.deepEqual([...cropped.data.subarray(0, 4)], [10, 20, 0, 255])
})

test("comparePngImages reports identical images as zero diff", () => {
  const buffer = makePng(64, 64)
  const comparison = comparePngImages(decodePng(buffer), decodePng(buffer), { threshold: 0.01 })
  assert.equal(comparison.diffRatio, 0)
  assert.deepEqual(comparison.mismatchBoxes, [])
  assert.equal(comparison.width, 64)
  const diff = decodePng(comparison.diffPng)
  assert.equal(diff.width, 64)
})

test("comparePngImages reports a changed region as a diff box", () => {
  const baseline = makePng(64, 64)
  const changed = makePng(64, 64, (x, y) => (x < 8 && y < 8 ? [250, 0, 0, 255] : [10, 20, 30, 255]))
  const comparison = comparePngImages(decodePng(baseline), decodePng(changed), { threshold: 0.01 })
  assert.ok(comparison.diffRatio > 0, `diffRatio=${comparison.diffRatio}`)
  assert.equal(comparison.diffRatio, 64 / (64 * 64))
  assert.equal(comparison.mismatchBoxes.length, 1)
  assert.deepEqual(comparison.mismatchBoxes[0], { x: 0, y: 0, width: 32, height: 32 })
})

test("comparePngImages crops both images with the same region", () => {
  const baseline = makePng(100, 100)
  const changed = makePng(100, 100, (x, y) =>
    x > 50 && y > 50 ? [250, 0, 0, 255] : [10, 20, 30, 255],
  )
  const full = comparePngImages(decodePng(baseline), decodePng(changed), { threshold: 0.01 })
  const cropped = comparePngImages(decodePng(baseline), decodePng(changed), {
    threshold: 0.01,
    region: { x: 0, y: 0, width: 0.5, height: 0.5 },
  })
  assert.ok(full.diffRatio > 0)
  assert.equal(cropped.diffRatio, 0)
  assert.equal(cropped.width, 50)
})

test("mismatchBoxes caps a full-image change", () => {
  const width = 1024
  const height = 1024
  const left = Buffer.alloc(width * height * 4, 0)
  const right = Buffer.alloc(width * height * 4, 255)
  const boxes = mismatchBoxes(left, right, width, height)
  assert.equal(boxes.length, 50)
})

test("environmentMatches compares viewport and DPR", () => {
  assert.equal(environmentMatches({ width: 1280, height: 720, dpr: 1 }, { width: 1280, height: 720 }, 1), true)
  assert.equal(environmentMatches({ width: 1920, height: 720, dpr: 1 }, { width: 1280, height: 720 }, 1), false)
  assert.equal(environmentMatches({ width: 1280, height: 720, dpr: 2 }, { width: 1280, height: 720 }, 1), false)
})

/** Fake context entry whose page returns a fixed screenshot and fingerprint. */
function fakeEntry({ actualPng, live, locatorPng = null, selectorCount = 1 }) {
  return {
    page: {
      screenshot: async () => actualPng,
      evaluate: async () => live,
      locator: () => ({
        count: async () => selectorCount,
        first: () => ({
          waitFor: async () => {},
          screenshot: async () => locatorPng,
        }),
      }),
    },
  }
}

test("runVisualCompare reports an environment mismatch without a diff artifact", async () => {
  const actual = makePng(100, 100)
  const entry = fakeEntry({
    actualPng: actual,
    live: { width: 800, height: 600, dpr: 2 },
  })
  const outcome = await runVisualCompare(entry, baseArgs(), 1000)
  assert.equal(outcome.result.environment_match, false)
  assert.equal(outcome.result.diff_ratio, 0)
  assert.equal(outcome.artifacts.length, 1)
  assert.equal(outcome.artifacts[0].kind, "visual_actual")
  assert.equal(Buffer.from(outcome.artifacts[0].base64, "base64").equals(actual), true)
})

test("runVisualCompare compares a matching full-page screenshot", async () => {
  const page = makePng(100, 100)
  const entry = fakeEntry({ actualPng: page, live: { width: 100, height: 100, dpr: 1 } })
  const outcome = await runVisualCompare(entry, baseArgs(), 1000)
  assert.equal(outcome.result.environment_match, true)
  assert.equal(outcome.result.diff_ratio, 0)
  assert.equal(outcome.result.width, 100)
  assert.deepEqual(outcome.result.mismatch_boxes, [])
  assert.deepEqual(
    outcome.artifacts.map((item) => item.kind),
    ["visual_actual", "visual_diff"],
  )
})

test("runVisualCompare treats a screenshot size mismatch as an environment mismatch", async () => {
  const entry = fakeEntry({
    actualPng: makePng(50, 50),
    live: { width: 100, height: 100, dpr: 1 },
  })
  const outcome = await runVisualCompare(entry, baseArgs(), 1000)
  assert.equal(outcome.result.environment_match, false)
  assert.equal(outcome.artifacts.length, 1)
})

test("runVisualCompare supports an element selector", async () => {
  const element = makePng(40, 30)
  const entry = fakeEntry({
    actualPng: makePng(100, 100),
    live: { width: 100, height: 100, dpr: 1 },
    locatorPng: element,
  })
  const outcome = await runVisualCompare(
    entry,
    baseArgs({ selector: "#box", baseline_base64: element.toString("base64") }),
    1000,
  )
  assert.equal(outcome.result.environment_match, true)
  assert.equal(outcome.result.width, 40)
  assert.equal(outcome.result.height, 30)
  assert.equal(outcome.artifacts.length, 2)
})

test("runVisualCompare classifies a selector baseline size mismatch as an environment mismatch", async () => {
  // An element screenshot is device-pixel sized by the element box and DPR, so
  // it is not required to equal viewport×DPR. When the approved baseline and the
  // live element screenshot disagree in size, the environment changed under the
  // baseline: report it as a mismatch (no diff artifact, no internal error).
  const entry = fakeEntry({
    actualPng: makePng(100, 100),
    live: { width: 100, height: 100, dpr: 1 },
    locatorPng: makePng(40, 30),
  })
  const outcome = await runVisualCompare(
    entry,
    baseArgs({ selector: "#box", baseline_base64: makePng(50, 20).toString("base64") }),
    1000,
  )
  assert.equal(outcome.result.environment_match, false)
  assert.equal(outcome.result.diff_ratio, 0)
  assert.deepEqual(
    outcome.artifacts.map((item) => item.kind),
    ["visual_actual"],
  )
})

test("runVisualCompare checks the live device scale factor fingerprint", async () => {
  const page = makePng(100, 100)
  const entry = fakeEntry({
    actualPng: page,
    live: { width: 100, height: 100, dpr: 2 },
  })
  const outcome = await runVisualCompare(
    entry,
    baseArgs({ device_scale_factor: 1, baseline_base64: page.toString("base64") }),
    1000,
  )
  assert.equal(outcome.result.environment_match, false)
  assert.equal(outcome.result.actual_device_scale_factor, 2)
  assert.equal(outcome.artifacts.length, 1)
})

test("runVisualCompare rejects a missing selector target", async () => {
  const entry = fakeEntry({
    actualPng: makePng(100, 100),
    live: { width: 100, height: 100, dpr: 1 },
    selectorCount: 0,
  })
  await assert.rejects(
    () => runVisualCompare(entry, baseArgs({ selector: "#missing" }), 1000),
    (error) => error instanceof ExecutorError && error.code === "target_not_found",
  )
})

import { readFile } from "node:fs/promises"

import { expect, test } from "@playwright/test"

const PNG_SIGNATURE = "89504e470d0a1a0a"

/**
 * Browser capability spike: the installed Chrome/Edge channel can open a page,
 * render it and produce a real screenshot. It intentionally does not integrate
 * with the app; the other specs cover the product flow.
 */
test("browser environment produces a screenshot", async ({ page, browserName }, testInfo) => {
  await page.setContent("<h1>EzTest browser spike</h1>")
  await expect(page.getByRole("heading", { name: "EzTest browser spike" })).toBeVisible()

  const screenshotPath = testInfo.outputPath("browser-smoke.png")
  const png = await page.screenshot({ path: screenshotPath })

  expect(png.length).toBeGreaterThan(0)
  expect(png.subarray(0, 8).toString("hex")).toBe(PNG_SIGNATURE)
  expect(browserName).toBe("chromium")

  const userAgent = await page.evaluate(() => navigator.userAgent)
  expect(userAgent).toMatch(/Chrome\//)

  const saved = await readFile(screenshotPath)
  expect(saved.length).toBe(png.length)

  await testInfo.attach("browser-smoke", { path: screenshotPath, contentType: "image/png" })
})

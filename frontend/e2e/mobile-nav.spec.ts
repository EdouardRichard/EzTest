import { expect, type Page, test } from "@playwright/test"

const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""

function requireAdminPassword(): string {
  if (!ADMIN_PASSWORD) {
    throw new Error("EZTEST_E2E_ADMIN_PASSWORD must be set; run via scripts/e2e.ps1")
  }
  return ADMIN_PASSWORD
}

/** Logs in through the real login form and waits for the protected shell. */
async function login(page: Page): Promise<void> {
  await page.goto("/login")
  await page.getByLabel("用户名").fill(ADMIN_USERNAME)
  await page.getByLabel("密码").fill(requireAdminPassword())
  await page.getByRole("button", { name: "登录", exact: true }).click()
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 })
}

/** Fails when a horizontal overflow makes the shell wider than the viewport. */
async function expectNoHorizontalOverflow(page: Page): Promise<void> {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  )
  expect(overflow).toBeLessThanOrEqual(1)
}

/**
 * Under-1024 navigation smoke (spec §8.3): the hamburger opens the drawer and
 * the drawer navigates, closes on Escape/overlay/route change, and stays out of
 * the desktop layout. These are layout/viewport checks that jsdom cannot do.
 */
test.describe("phone navigation (375x667)", () => {
  test.use({ viewport: { width: 375, height: 667 } })

  test("opens the drawer, navigates and closes it", async ({ page }) => {
    await login(page)

    const sidebar = page.locator("aside")
    const hamburger = page.getByRole("button", { name: "导航菜单" })
    const drawer = page.getByRole("dialog", { name: "主导航菜单" })

    await expect(sidebar).toBeHidden()
    await expect(hamburger).toBeVisible()
    await expect(hamburger).toHaveAttribute("aria-expanded", "false")
    await expect(hamburger).toHaveAttribute("aria-controls", "mobile-nav-drawer")
    await expect(drawer).toBeHidden()
    await expectNoHorizontalOverflow(page)

    // Every navigation control in the drawer is a ≥44px touch target.
    const hamburgerBox = await hamburger.boundingBox()
    expect(hamburgerBox?.width ?? 0).toBeGreaterThanOrEqual(44)
    expect(hamburgerBox?.height ?? 0).toBeGreaterThanOrEqual(44)

    await hamburger.click()
    await expect(drawer).toBeVisible()
    await expect(hamburger).toHaveAttribute("aria-expanded", "true")

    const versionLink = drawer.getByRole("link", { name: /^版本/ })
    const versionBox = await versionLink.boundingBox()
    expect(versionBox?.height ?? 0).toBeGreaterThanOrEqual(44)

    await page.keyboard.press("Escape")
    await expect(drawer).toBeHidden()
    await expect(hamburger).toHaveAttribute("aria-expanded", "false")

    // Overlay close. Click the un-covered right side: the drawer owns the left.
    await hamburger.click()
    await expect(drawer).toBeVisible()
    await page.getByTestId("mobile-nav-overlay").click({ position: { x: 340, y: 320 } })
    await expect(drawer).toBeHidden()

    // Following a destination closes the drawer and changes the route.
    await hamburger.click()
    await drawer.getByRole("link", { name: /^版本/ }).click()
    await expect(page).toHaveURL(/\/versions/)
    await expect(drawer).toBeHidden()
    await expect(hamburger).toHaveAttribute("aria-expanded", "false")
    await expectNoHorizontalOverflow(page)
  })
})

test.describe("tablet navigation (768x1024)", () => {
  test.use({ viewport: { width: 768, height: 1024 } })

  test("keeps the sidebar behind the drawer at the tablet breakpoint", async ({ page }) => {
    await login(page)

    const hamburger = page.getByRole("button", { name: "导航菜单" })
    const drawer = page.getByRole("dialog", { name: "主导航菜单" })

    await expect(page.locator("aside")).toBeHidden()
    await expect(hamburger).toBeVisible()
    await expectNoHorizontalOverflow(page)

    await hamburger.click()
    await expect(drawer).toBeVisible()
    await expect(drawer.getByRole("link", { name: /^概览/ })).toBeVisible()
    await expect(drawer.getByRole("button", { name: "关闭导航菜单" })).toBeVisible()
  })
})

test.describe("desktop navigation (1280x720)", () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test("keeps the fixed sidebar and hides the hamburger from lg", async ({ page }) => {
    await login(page)

    const sidebar = page.locator("aside")
    const hamburger = page.getByRole("button", { name: "导航菜单" })

    await expect(sidebar).toBeVisible()
    await expect(hamburger).toBeHidden()
    await expectNoHorizontalOverflow(page)

    await sidebar.getByRole("link", { name: /^版本/ }).click()
    await expect(page).toHaveURL(/\/versions/)
  })
})

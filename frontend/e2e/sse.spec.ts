import { expect, type Locator, type Page, test } from "@playwright/test"

const ADMIN_USERNAME = process.env.EZTEST_E2E_ADMIN_USERNAME?.trim() || "admin"
const ADMIN_PASSWORD = process.env.EZTEST_E2E_ADMIN_PASSWORD ?? ""
const API_URL = process.env.EZTEST_E2E_API_URL?.trim() || "http://localhost:13333"
const STREAM_ROUTE = /\/api\/stream\?channel=system$/

type CapturedRequest = {
  headers: Record<string, string>
}

function requireAdminPassword(): string {
  if (!ADMIN_PASSWORD) {
    throw new Error("EZTEST_E2E_ADMIN_PASSWORD must be set; run via scripts/e2e.ps1")
  }
  return ADMIN_PASSWORD
}

async function login(page: Page, username: string, password: string): Promise<void> {
  await page.goto("/login")
  await page.getByLabel("用户名").fill(username)
  await page.getByLabel("密码").fill(password)
  await page.getByRole("button", { name: "登录", exact: true }).click()
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 })
}

function ssePanel(page: Page): Locator {
  return page.getByRole("region", { name: "SSE 联通性测试" })
}

/** Reads the seq column from the panel's event table. */
async function readSeqs(panel: Locator): Promise<number[]> {
  return panel
    .locator("tbody tr")
    .evaluateAll((rows) =>
      rows
        .map((row) => Number(row.querySelector("td")?.textContent?.trim() ?? ""))
        .filter((value) => Number.isFinite(value) && value > 0),
    )
}

/** Parses the seqs from the "已发送 3 条测试事件：seq 1、2、3。" notice. */
function parseNoticeSeqs(text: string): number[] {
  const match = /seq\s+(\d+(?:、\d+)*)/.exec(text)
  if (!match) return []
  return match[1].split("、").map((value) => Number(value))
}

/** Asserts the seq set is duplicate-free and gap-free. */
function expectContiguous(seqs: number[]): void {
  expect(seqs.length).toBeGreaterThan(0)
  const sorted = [...seqs].sort((left, right) => left - right)
  expect(new Set(sorted).size).toBe(sorted.length)
  for (let index = 1; index < sorted.length; index += 1) {
    expect(sorted[index]).toBe(sorted[index - 1] + 1)
  }
}

test.describe("SSE diagnostics", () => {
  test.describe.configure({ timeout: 180_000 })

  test("connects, replays events after a reconnect, and keeps seq gap-free", async ({
    page,
    context,
    request,
  }) => {
    const adminPassword = requireAdminPassword()
    const streamRequests: CapturedRequest[] = []
    // Capture the real browser requests so the test can prove the CORS-safe
    // header set (no Cache-Control) and the Last-Event-ID replay cursor.
    page.on("request", (captured) => {
      if (captured.method() === "GET" && captured.url().includes("/api/stream?channel=system")) {
        streamRequests.push({ headers: captured.headers() })
      }
    })

    await login(page, ADMIN_USERNAME, adminPassword)
    await page.goto("/settings")
    await page.getByRole("tab", { name: "SSE 测试" }).click()

    const panel = ssePanel(page)
    await expect(panel.getByRole("heading", { name: "SSE 联通性测试" })).toBeVisible()
    const statusValue = panel.locator("dd").nth(0)
    const lastSeqValue = panel.locator("dd").nth(1)

    await panel.getByRole("button", { name: "连接", exact: true }).click()
    await expect(statusValue).toHaveText("实时", { timeout: 30_000 })
    await expect.poll(() => streamRequests.length, { timeout: 10_000 }).toBeGreaterThanOrEqual(1)

    // Regression guard for the fixed SSE CORS bug: an explicit Cache-Control
    // request header is not in the API's closed CORS allow-list and would make
    // the browser preflight fail before the stream is opened.
    for (const captured of streamRequests) {
      expect(captured.headers["cache-control"]).toBeUndefined()
    }

    const notice = panel.getByRole("status")
    await panel.getByRole("button", { name: "发送 3 条测试事件" }).click()
    await expect(notice).toContainText("已发送 3 条测试事件")
    const initialPublished = parseNoticeSeqs((await notice.textContent()) ?? "")
    expect(initialPublished).toHaveLength(3)
    expectContiguous(initialPublished)

    await expect
      .poll(
        async () => {
          const seqs = await readSeqs(panel)
          return initialPublished.every((seq) => seqs.includes(seq))
        },
        { timeout: 20_000 },
      )
      .toBe(true)
    const initialMax = Math.max(...(await readSeqs(panel)))

    // Force a disconnect. The primary path is the real offline emulation; the
    // fallback aborts the next stream request so the client exercises the same
    // reconnect/backoff path deterministically.
    await context.setOffline(true)
    let offlineDisconnected = false
    try {
      await expect(statusValue).toHaveText("重连中", { timeout: 8_000 })
      offlineDisconnected = true
    } catch {
      offlineDisconnected = false
    }
    if (!offlineDisconnected) {
      await context.setOffline(false)
      await page.route(STREAM_ROUTE, (route) => route.abort())
      await panel.getByRole("button", { name: "断开", exact: true }).click()
      await panel.getByRole("button", { name: "连接", exact: true }).click()
      await expect(statusValue).toHaveText("重连中", { timeout: 20_000 })
    }

    // Publish while the browser is disconnected. The events must arrive after
    // the reconnect without a gap: the abort path first refetches the snapshot
    // (whose cursor still predates these events), then reconnects with
    // Last-Event-ID and replays them.
    const token = await page.evaluate(() => sessionStorage.getItem("eztest.token"))
    expect(token).toBeTruthy()
    const gapResponse = await request.post(`${API_URL}/api/admin/stream/test-events`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { count: 2, summary: "reconnect gap events" },
    })
    expect(gapResponse.ok()).toBeTruthy()
    const gapBody = (await gapResponse.json()) as { data?: { seqs?: number[] } }
    const gapSeqs = gapBody.data?.seqs ?? []
    expect(gapSeqs).toHaveLength(2)
    expectContiguous(gapSeqs)

    if (offlineDisconnected) {
      await context.setOffline(false)
    } else {
      await page.unroute(STREAM_ROUTE)
    }
    await expect(statusValue).toHaveText("实时", { timeout: 60_000 })
    await expect
      .poll(
        async () => {
          const seqs = await readSeqs(panel)
          return gapSeqs.every((seq) => seqs.includes(seq))
        },
        { timeout: 30_000 },
      )
      .toBe(true)

    // Send three more events through the UI after the reconnect.
    await panel.getByRole("button", { name: "发送 3 条测试事件" }).click()
    await expect
      .poll(
        async () => {
          const seqs = parseNoticeSeqs((await notice.textContent()) ?? "")
          return seqs.length === 3 && Math.max(...seqs) > Math.max(...gapSeqs)
        },
        { timeout: 20_000 },
      )
      .toBe(true)
    const uiSeqs = parseNoticeSeqs((await notice.textContent()) ?? "")
    expect(uiSeqs).toHaveLength(3)
    expectContiguous(uiSeqs)

    await expect
      .poll(
        async () => {
          const seqs = await readSeqs(panel)
          return uiSeqs.every((seq) => seqs.includes(seq))
        },
        { timeout: 20_000 },
      )
      .toBe(true)

    const finalSeqs = await readSeqs(panel)
    expectContiguous(finalSeqs)
    expect(finalSeqs).toEqual(expect.arrayContaining([...initialPublished, ...gapSeqs, ...uiSeqs]))
    await expect(statusValue).toHaveText("实时")
    await expect(lastSeqValue).toHaveText(String(Math.max(...finalSeqs)))

    // The reconnect actually carried a replay cursor and every stream request
    // stayed on the CORS-safe header set.
    await expect.poll(() => streamRequests.length, { timeout: 10_000 }).toBeGreaterThanOrEqual(2)
    const reconnectRequests = streamRequests.filter(
      (captured) => captured.headers["last-event-id"] !== undefined,
    )
    expect(reconnectRequests.length).toBeGreaterThanOrEqual(1)
    expect(Number(reconnectRequests[0].headers["last-event-id"])).toBeGreaterThanOrEqual(initialMax)
    for (const captured of streamRequests) {
      expect(captured.headers["cache-control"]).toBeUndefined()
    }

    if (offlineDisconnected) {
      // The real offline reconnect must resume exactly after the last event the
      // client rendered before the drop.
      expect(Number(reconnectRequests[0].headers["last-event-id"])).toBe(initialMax)
    }
  })
})

import { type APIResponse, expect, type Page, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  API_URL,
  adminApiToken,
  createRequirement,
  createVersion,
  getDefaultLlmConfig,
  login,
  pageToken,
  setDefaultLlmProtocol,
  skipP3SpecWhenHarnessMissing,
  startVersionClarification,
  uniqueSuffix,
  type VersionRecord,
} from "./p3-helpers"

/**
 * Design spec §10.5 item 3: when the AI runtime is unavailable, session and
 * message endpoints return the retryable 503 `ai_unavailable`, while the manual
 * question/decision/relation/confirm paths keep working and the page stays
 * usable. `Gateway.Prepare` refuses a non chat-completions default, so switching
 * the seeded configuration's protocol makes the readiness probe fail exactly
 * like an unconfigured deployment; the original protocol is restored afterwards.
 */

let versionId = ""
let versionName = ""
let reqAId = ""
let reqBId = ""
let reqATitle = ""
let reqBTitle = ""
let token = ""
let configId = 0
let originalProtocol = ""

function requirementButton(page: Page, title: string) {
  return page.getByRole("list", { name: "需求条目" }).getByRole("button").filter({ hasText: title })
}

async function expectAiUnavailable(response: APIResponse): Promise<void> {
  expect(response.status()).toBe(503)
  const body = (await response.json()) as {
    error?: { code?: string; retryable?: boolean; details?: { reason?: string } }
  }
  expect(body.error?.code).toBe("unavailable")
  expect(body.error?.retryable).toBe(true)
  expect(body.error?.details?.reason).toBe("ai_unavailable")
}

test.describe.configure({ mode: "serial" })

test.beforeAll(async ({ request }) => {
  if (await skipP3SpecWhenHarnessMissing()) return

  const suffix = uniqueSuffix()
  versionName = `P3 降级版本 ${suffix}`
  reqATitle = `P3 降级需求A ${suffix}`
  reqBTitle = `P3 降级需求B ${suffix}`
  token = await adminApiToken(request)

  const version = await createVersion(request, token, {
    name: versionName,
    label: `degraded-${suffix}`,
    description: "P3 AI 降级路径 E2E 版本",
  })
  versionId = version.id
  const reqA = await createRequirement(request, token, versionId, {
    title: reqATitle,
    description: "降级场景需求 A。",
    acceptanceCriteria: "A 可人工确认。",
  })
  const reqB = await createRequirement(request, token, versionId, {
    title: reqBTitle,
    description: "降级场景需求 B。",
    acceptanceCriteria: "B 可人工维护问题与决策。",
  })
  reqAId = reqA.id
  reqBId = reqB.id

  const clarifying = await startVersionClarification(request, token, versionId)
  expect(clarifying.status).toBe("clarifying")

  const config = await getDefaultLlmConfig(request, token)
  configId = config.id
  originalProtocol = config.protocol
})

test.afterAll(async ({ request }) => {
  if (configId > 0 && originalProtocol !== "") {
    try {
      await setDefaultLlmProtocol(request, token, configId, originalProtocol)
    } catch {
      // The e2e database is reset on the next run; restoring is best effort.
    }
  }
})

test("keeps manual clarification paths usable while the AI runtime is unavailable", async ({
  page,
  request,
}) => {
  test.setTimeout(180_000)
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
  await page.goto(`/versions/${versionId}`)
  await expect(page.getByRole("heading", { name: versionName, level: 1 })).toBeVisible()
  await expect(page.getByText("澄清中", { exact: true }).first()).toBeVisible()

  const chat = page.getByRole("region", { name: "澄清对话" })
  const questionRegion = page.getByRole("region", { name: "澄清问题" })
  const decisionRegion = page.getByRole("region", { name: "澄清决策" })
  await expect(chat.getByText("尚未开始澄清会话。")).toBeVisible()

  // 1. Make the default LLM unusable: the readiness probe now returns 503.
  await setDefaultLlmProtocol(request, token, configId, "responses")

  await chat.getByRole("button", { name: "开始澄清会话" }).click()
  const banner = chat.getByRole("alert").filter({
    hasText: "AI 服务暂不可用，请稍后重试。人工新增问题与决策不受影响。",
  })
  await expect(banner).toBeVisible()
  await expect(banner.getByRole("button", { name: "重试 AI" })).toBeVisible()
  await expect(chat.getByText("尚未开始澄清会话。")).toBeVisible()

  const sessionResponse = await request.post(
    `${API_URL}/api/versions/${versionId}/clarification/sessions`,
    { headers: { Authorization: `Bearer ${token}` } },
  )
  await expectAiUnavailable(sessionResponse)

  // 2. Manual question, decision, relation and requirement confirmation all work.
  await questionRegion.getByRole("button", { name: "新增问题" }).click()
  await questionRegion.getByLabel("问题内容").fill("P3 降级人工问题：是否需要发票？")
  await questionRegion.getByLabel("关联需求").selectOption({ label: reqBTitle })
  await questionRegion.getByRole("button", { name: "创建问题" }).click()
  const manualQuestion = questionRegion.getByRole("listitem").filter({
    hasText: "P3 降级人工问题：是否需要发票？",
  })
  await expect(manualQuestion).toBeVisible()
  await expect(manualQuestion.getByText("人工录入")).toBeVisible()

  await decisionRegion.getByRole("button", { name: "新增决策" }).click()
  await decisionRegion.getByLabel("决策主题").fill("P3 降级人工决策")
  await decisionRegion.getByLabel("决策内容").fill("支持电子发票。")
  await decisionRegion.getByLabel("关联需求").selectOption({ label: reqBTitle })
  await decisionRegion.getByRole("button", { name: "保存决策" }).click()
  const manualDecision = decisionRegion.getByRole("listitem").filter({
    hasText: "P3 降级人工决策",
  })
  await expect(manualDecision.getByText("已接受", { exact: true })).toBeVisible()
  await expect(manualDecision.getByText("人工记录")).toBeVisible()

  await page.locator("#relation-from").selectOption({ label: reqATitle })
  await page.locator("#relation-to").selectOption({ label: reqBTitle })
  await page.locator("#relation-type").selectOption({ label: "依赖" })
  await page.getByRole("button", { name: "新增关系" }).click()
  await expect(
    page.getByRole("list", { name: "关系列表" }).getByText(`${reqATitle} → ${reqBTitle}`),
  ).toBeVisible()

  await requirementButton(page, reqATitle).click()
  const editor = page.getByRole("region", { name: "需求编辑器" })
  await editor.getByRole("button", { name: "确认需求" }).click()
  await expect(editor.getByText("已澄清", { exact: true })).toBeVisible()
  await expect(page.getByText("已确认 1", { exact: true })).toBeVisible()

  // 3. Restore the AI long enough to start a session, then fail the message path.
  await setDefaultLlmProtocol(request, token, configId, originalProtocol)
  await banner.getByRole("button", { name: "重试 AI" }).click()
  await expect(chat.getByLabel("消息内容")).toBeEnabled({ timeout: 30_000 })
  await expect(chat.getByText("实时连接", { exact: true })).toBeVisible()
  await expect(banner).toHaveCount(0)

  await setDefaultLlmProtocol(request, token, configId, "responses")
  const composer = chat.getByLabel("消息内容")
  await composer.fill("P3-DEGRADED-MESSAGE")
  await chat.getByRole("button", { name: "发送", exact: true }).click()
  await expect(banner).toBeVisible()
  await expect(
    banner.getByText("AI 服务暂不可用，请稍后重试。人工新增问题与决策不受影响。"),
  ).toBeVisible()
  await expect(composer).toHaveValue("P3-DEGRADED-MESSAGE")

  const messageResponse = await request.post(
    `${API_URL}/api/versions/${versionId}/clarification/messages`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { message: "P3-DEGRADED-MESSAGE" },
    },
  )
  await expectAiUnavailable(messageResponse)

  // 4. Manual writes still work after the message failure; the page never crashes.
  await questionRegion.getByRole("button", { name: "新增问题" }).click()
  await questionRegion.getByLabel("问题内容").fill("P3 降级出错后问题：发票抬头必填吗？")
  await questionRegion.getByRole("button", { name: "创建问题" }).click()
  await expect(
    questionRegion.getByRole("listitem").filter({ hasText: "P3 降级出错后问题：发票抬头必填吗？" }),
  ).toBeVisible()

  await expect(page.getByRole("heading", { name: versionName, level: 1 })).toBeVisible()
  await expect(page.getByRole("list", { name: "需求条目" }).getByRole("button")).toHaveCount(2)
  await expect(decisionRegion.getByText("P3 降级人工决策")).toBeVisible()
  await expect(
    page.getByRole("list", { name: "关系列表" }).getByText(`${reqATitle} → ${reqBTitle}`),
  ).toBeVisible()
  await expect(
    page.getByRole("group", { name: /需求关系图：2 个需求节点，1 条关系/ }),
  ).toBeVisible()
  await expect(page.getByText("Application error")).toHaveCount(0)
  await expect(page.getByText("Something went wrong")).toHaveCount(0)

  const finalVersion = await request.get(`${API_URL}/api/versions/${versionId}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(finalVersion.status()).toBe(200)
  const finalBody = (await finalVersion.json()) as { data: VersionRecord }
  expect(finalBody.data.status).toBe("clarifying")
  expect(finalBody.data.clarifiedCount).toBe(1)
  expect(finalBody.data.openQuestionCount).toBeGreaterThanOrEqual(2)
  expect(reqAId).toMatch(/^\d{15,}$/)
  expect(reqBId).toMatch(/^\d{15,}$/)
  // The browser still owns a valid session and can retry manually.
  expect(await pageToken(page)).toBeTruthy()

  // Restore the default so a later spec never observes the degraded protocol.
  await setDefaultLlmProtocol(request, token, configId, originalProtocol)
})

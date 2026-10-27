import { expect, type Page, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  adminApiToken,
  buildOversizedPng,
  countImageParts,
  createRequirement,
  createVersion,
  fakeRequestText,
  getClarification,
  imageUrls,
  listRequirements,
  listVersionMessages,
  login,
  readPixelPng,
  screenshotPath,
  skipP3SpecWhenHarnessMissing,
  uniqueSuffix,
  uploadAttachment,
  type VersionRecord,
  waitForFakeRequest,
  waitForPersistedImageRef,
} from "./p3-helpers"

/**
 * Design spec §10.5 item 2: the fake LLM drives two clarification rounds
 * (question list + draft, then relation + decision), a human answers and accepts
 * in the UI, the accepted decision closes its question, the requirement is
 * confirmed, the live SSE transcript is visible, and reload/reconnect lose
 * nothing. Item 4 (multimodal recon) is covered at the end: the outgoing request
 * carries exactly one resolved `image_url`, the oversized image is annotated as
 * skipped, and no data URL reaches `agent_messages`.
 */

const QUESTION_ONE = "P3 问题一：订单金额达到多少需要人工审核？"
const QUESTION_TWO = "P3 问题二：退款是否需要原路退回？"
const ANSWER_ONE = "订单金额超过 5000 元需要人工审核。"
const DRAFT_UPDATE = "P3 草案更新：已补充人工审核阈值说明。"
const DECISION_TOPIC = "P3 退款政策"
const DECISION_TEXT = "P3 决策：支持 7 天无理由退款"
const RELATION_DESCRIPTION = "P3 关系建议：取消依赖创建后的库存状态。"
const ROUND_ONE = "P3-CLARIFY-1"
const ROUND_TWO = "P3-CLARIFY-2"
const ROUND_ONE_REPLY = "P3 第一轮完成"
const ROUND_TWO_REPLY = "P3 第二轮完成"
const RECON_REPLY = "P3 侦察完成"
const ANALYZE_REPLY = "P3 分析完成"
const ANALYSIS_DECISION = "P3 分析结论"

let versionId = ""
let versionName = ""
let reqAId = ""
let reqBId = ""
let reqATitle = ""
let reqBTitle = ""

/** Selects one requirement in the workbench list. */
function requirementButton(page: Page, title: string) {
  return page.getByRole("list", { name: "需求条目" }).getByRole("button").filter({ hasText: title })
}

/** Types and sends one clarification message once the composer is enabled. */
async function sendMessage(page: Page, text: string): Promise<void> {
  const chat = page.getByRole("region", { name: "澄清对话" })
  const composer = chat.getByLabel("消息内容")
  await expect(composer).toBeEnabled()
  await composer.fill(text)
  await chat.getByRole("button", { name: "发送", exact: true }).click()
}

/** Waits until the active clarification session has an assistant reply. */
async function waitForReply(page: Page, reply: string): Promise<void> {
  const chat = page.getByRole("region", { name: "澄清对话" })
  await expect(chat.getByText(reply, { exact: false })).toBeVisible({ timeout: 45_000 })
}

test.describe.configure({ mode: "serial" })

test.beforeAll(async ({ request }) => {
  if (await skipP3SpecWhenHarnessMissing()) return

  const suffix = uniqueSuffix()
  versionName = `P3 澄清版本 ${suffix}`
  reqATitle = `P3 需求A ${suffix}`
  reqBTitle = `P3 需求B ${suffix}`
  const token = await adminApiToken(request)

  const version = await createVersion(request, token, {
    name: versionName,
    label: `clari-${suffix}`,
    description: "P3 澄清与多模态 E2E 版本",
  })
  versionId = version.id
  const reqA = await createRequirement(request, token, versionId, {
    title: reqATitle,
    description: "订单创建与库存扣减。",
    acceptanceCriteria: "订单创建成功后库存减少。",
  })
  const reqB = await createRequirement(request, token, versionId, {
    title: reqBTitle,
    description: "订单取消与退款。",
    acceptanceCriteria: "取消后库存恢复且退款成功。",
  })
  reqAId = reqA.id
  reqBId = reqB.id

  await uploadAttachment(request, token, versionId, reqAId, {
    name: "P3 需求说明.md",
    mimeType: "text/markdown",
    buffer: Buffer.from("# 需求说明\n\n- 订单金额超过 5000 元需要人工审核。\n"),
  })
  await uploadAttachment(request, token, versionId, reqAId, {
    name: "pixel.png",
    mimeType: "image/png",
    buffer: readPixelPng(),
  })
  await uploadAttachment(request, token, versionId, reqBId, {
    name: "huge.png",
    mimeType: "image/png",
    buffer: buildOversizedPng(),
  })
})

test("clarifies with the fake LLM across two rounds and survives reload/reconnect", async ({
  page,
  request,
}) => {
  test.setTimeout(300_000)
  const token = await adminApiToken(request)
  const streamStatuses: number[] = []
  page.on("response", (response) => {
    if (response.url().includes(`/api/versions/${versionId}/stream`)) {
      streamStatuses.push(response.status())
    }
  })

  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
  await page.goto(`/versions/${versionId}`)
  await expect(page.getByRole("heading", { name: versionName, level: 1 })).toBeVisible()
  const versionHeader = page
    .getByRole("heading", { name: versionName, level: 1 })
    .locator("xpath=ancestor::header")
  await expect(page.getByText("草稿", { exact: true }).first()).toBeVisible()
  await expect(page.getByRole("list", { name: "需求条目" }).getByRole("button")).toHaveCount(2)

  // 1. Draft -> clarifying through the header, then start the clarification session.
  await page.getByRole("button", { name: "开始澄清", exact: true }).click()
  await expect(page.getByText("澄清中", { exact: true }).first()).toBeVisible()
  const chat = page.getByRole("region", { name: "澄清对话" })
  await expect(chat.getByText("尚未开始澄清会话。")).toBeVisible()
  await chat.getByRole("button", { name: "开始澄清会话" }).click()
  await expect(chat.getByLabel("消息内容")).toBeEnabled({ timeout: 30_000 })
  await expect(chat.getByText("实时连接", { exact: true })).toBeVisible()
  await expect.poll(() => streamStatuses.includes(200), { timeout: 20_000 }).toBe(true)

  // 2. Round one: fake LLM asks a question and updates the requirement draft.
  await sendMessage(page, `${ROUND_ONE} versionId=${versionId}`)
  await waitForReply(page, ROUND_ONE_REPLY)
  await expect(
    chat.getByRole("button", { name: "工具 clarification.question_upsert" }).first(),
  ).toBeVisible()
  await expect(
    chat.getByRole("button", { name: "工具 requirement.update_draft" }).first(),
  ).toBeVisible()

  const questionRegion = page.getByRole("region", { name: "澄清问题" })
  await expect(questionRegion.getByText(QUESTION_ONE)).toBeVisible()
  await expect(questionRegion.getByRole("button", { name: "待回答 1" })).toBeVisible()

  // 3. Human answers the first question in the UI.
  const questionOneRow = questionRegion.getByRole("listitem").filter({ hasText: QUESTION_ONE })
  await questionOneRow.getByLabel("回答内容").fill(ANSWER_ONE)
  await questionOneRow.getByRole("button", { name: "保存回答" }).click()
  await expect(questionOneRow.getByText(`回答：${ANSWER_ONE}`)).toBeVisible()
  await expect(questionRegion.getByRole("button", { name: "已回答 1" })).toBeVisible()

  // The AI draft edit is persisted. The editor keeps its local form state when
  // the same requirement object refreshes (reported as a UI bug), so assert the
  // persisted row here and re-check the rendered editor after the reload below.
  const persistedRequirements = await listRequirements(request, token, versionId)
  const persistedDraft = persistedRequirements.find((item) => item.id === reqAId)
  expect(persistedDraft?.description).toContain(DRAFT_UPDATE)
  const editor = page.getByRole("region", { name: "需求编辑器" })

  // 4. Round two: a second question, a relation suggestion and a linked decision.
  await sendMessage(page, `${ROUND_TWO} versionId=${versionId}`)
  await waitForReply(page, ROUND_TWO_REPLY)
  await expect(chat.getByRole("button", { name: "工具 decision.propose" }).first()).toBeVisible()
  await expect(chat.getByRole("button", { name: "工具 relation.propose" }).first()).toBeVisible()

  await expect(questionRegion.getByText(QUESTION_TWO)).toBeVisible()
  await expect(questionRegion.getByRole("button", { name: "待回答 1" })).toBeVisible()
  const decisionRegion = page.getByRole("region", { name: "澄清决策" })
  await expect(decisionRegion.getByText(DECISION_TOPIC)).toBeVisible()
  await expect(decisionRegion.getByText(DECISION_TEXT)).toBeVisible()
  await expect(decisionRegion.getByRole("button", { name: "待确认 1" })).toBeVisible()

  await page.getByRole("button", { name: "刷新工作台" }).click()
  await expect(
    page.getByRole("list", { name: "关系列表" }).getByText(RELATION_DESCRIPTION),
  ).toBeVisible()
  await expect(page.getByRole("img", { name: "需求关系图：2 个需求节点，1 条关系" })).toBeVisible()
  await page.screenshot({
    path: screenshotPath("versions-clarification-round2.png"),
    fullPage: true,
  })

  // 5. Accept the decision: the repository answers its linked open question.
  const decisionRow = decisionRegion.getByRole("listitem").filter({ hasText: DECISION_TEXT })
  await decisionRow.getByRole("button", { name: "接受", exact: true }).click()
  await expect(decisionRow.getByText("已接受", { exact: true })).toBeVisible()
  const questionTwoRow = questionRegion.getByRole("listitem").filter({ hasText: QUESTION_TWO })
  await expect(questionTwoRow.getByText(`回答：${DECISION_TEXT}`)).toBeVisible()
  await expect(questionRegion.getByRole("button", { name: "待回答 0" })).toBeVisible()
  await expect(versionHeader.getByText("待澄清问题 0", { exact: true })).toBeVisible()

  // 6. Confirm the requirement now that the open count reached zero.
  await requirementButton(page, reqATitle).click()
  await editor.getByRole("button", { name: "确认需求" }).click()
  await expect(editor.getByText("已澄清", { exact: true })).toBeVisible()
  await expect(page.getByText("已确认 1", { exact: true })).toBeVisible()
  const overview = await getClarification(request, token, versionId)
  expect(overview.session?.status).toBe("active")
  expect(overview.questions.open).toBe(0)
  expect(overview.decisions.accepted).toBe(1)

  // 7. Reload: transcript, questions, decisions and the relation all persist.
  await page.reload()
  await expect(page.getByRole("heading", { name: versionName, level: 1 })).toBeVisible()
  const restoredChat = page.getByRole("region", { name: "澄清对话" })
  await expect(restoredChat.getByText(ROUND_ONE, { exact: false })).toBeVisible()
  await expect(restoredChat.getByText(ROUND_TWO, { exact: false })).toBeVisible()
  await expect(restoredChat.getByText(ROUND_ONE_REPLY, { exact: false })).toBeVisible()
  await expect(restoredChat.getByText(ROUND_TWO_REPLY, { exact: false })).toBeVisible()
  await expect(page.getByRole("region", { name: "澄清问题" }).getByText(ANSWER_ONE)).toBeVisible()
  await expect(
    page.getByRole("region", { name: "澄清决策" }).getByText(DECISION_TEXT),
  ).toBeVisible()
  await expect(
    page.getByRole("list", { name: "关系列表" }).getByText(RELATION_DESCRIPTION),
  ).toBeVisible()
  await expect(versionHeader.getByText("待澄清问题 0", { exact: true })).toBeVisible()
  await requirementButton(page, reqATitle).click()
  await expect(editor.getByLabel("描述")).toHaveValue(new RegExp(DRAFT_UPDATE))

  // 8. Reconnect: force the stream to fail on reload, then restore it manually.
  const streamPattern = "**/api/versions/*/stream*"
  await page.route(streamPattern, (route) => route.abort())
  await page.reload()
  await expect(page.getByRole("heading", { name: versionName, level: 1 })).toBeVisible()
  const reconnectingChat = page.getByRole("region", { name: "澄清对话" })
  await expect(reconnectingChat.getByText("重连中…", { exact: true })).toBeVisible({
    timeout: 20_000,
  })
  await expect(reconnectingChat.getByText("暂无消息")).toHaveCount(0)
  await expect(reconnectingChat.getByText(ROUND_ONE, { exact: false })).toBeVisible()
  await expect(reconnectingChat.getByText(ROUND_TWO_REPLY, { exact: false })).toBeVisible()

  await page.unroute(streamPattern)
  const reconnectButton = reconnectingChat.getByRole("button", { name: "重新连接" })
  if (await reconnectButton.isVisible().catch(() => false)) {
    await reconnectButton.click()
  }
  await expect(reconnectingChat.getByText("实时连接", { exact: true })).toBeVisible({
    timeout: 30_000,
  })
  await expect(reconnectingChat.getByText(ROUND_ONE, { exact: false })).toHaveCount(1)
  await expect(reconnectingChat.getByText(ROUND_TWO_REPLY, { exact: false })).toHaveCount(1)

  // 9. Context recon: the resolved image data URL reaches only the LLM request.
  await expect(chat.getByLabel("消息内容")).toBeEnabled()
  await chat.getByRole("button", { name: "上下文侦察" }).click()
  await waitForReply(page, RECON_REPLY)
  const reconRequest = await waitForFakeRequest((candidate) =>
    fakeRequestText(candidate).includes(`Recon the context of version ${versionId}`),
  )
  const reconImages = imageUrls(reconRequest)
  expect(countImageParts(reconRequest)).toBe(1)
  expect(reconImages).toHaveLength(1)
  expect(reconImages[0]).toMatch(/^data:image\/png;base64,/)
  const reconText = fakeRequestText(reconRequest)
  expect(reconText).toContain("huge.png")
  expect(reconText).toContain("was too large to inject")

  await expect
    .poll(
      async () => (await getClarification(request, token, versionId)).session?.contextSummary ?? "",
    )
    .toContain("P3 上下文摘要")
  const imageStats = await waitForPersistedImageRef()
  if (imageStats !== null) {
    expect(imageStats.dataUrlRows).toBe(0)
    expect(imageStats.imageRefRows).toBeGreaterThan(0)
  }
  const persistedMessages = await listVersionMessages(request, token, versionId)
  expect(JSON.stringify(persistedMessages)).not.toContain("data:image")

  // 10. Requirement analysis is the third P3 profile; it also runs on the fake.
  await expect(chat.getByLabel("消息内容")).toBeEnabled()
  await chat.getByRole("button", { name: "需求分析" }).click()
  await waitForReply(page, ANALYZE_REPLY)
  await waitForFakeRequest((candidate) =>
    fakeRequestText(candidate).includes(`Analyze the requirements of version ${versionId}`),
  )
  await expect(
    page.getByRole("region", { name: "澄清决策" }).getByText(ANALYSIS_DECISION),
  ).toBeVisible({ timeout: 20_000 })
  await page.screenshot({ path: screenshotPath("versions-clarification-live.png"), fullPage: true })

  // P3 owns string IDs: neither requirement ID may leak into the URL as a number.
  expect(reqAId).toMatch(/^\d{15,}$/)
  expect(reqBId).toMatch(/^\d{15,}$/)
  const version: VersionRecord = await request
    .get(
      `${process.env.EZTEST_E2E_API_URL ?? "http://localhost:13333"}/api/versions/${versionId}`,
      {
        headers: { Authorization: `Bearer ${token}` },
      },
    )
    .then(async (response) => {
      expect(response.status()).toBe(200)
      return ((await response.json()) as { data: VersionRecord }).data
    })
  expect(version.status).toBe("clarifying")
  expect(version.clarifiedCount).toBe(1)
})

import { expect, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  adminApiToken,
  createCodeRepo,
  createEnvironment,
  createSystem,
  listAttachments,
  listRequirements,
  login,
  pageToken,
  prepareGitRepo,
  type RequirementRecord,
  screenshotPath,
  skipP3SpecWhenHarnessMissing,
  uniqueSuffix,
  type VersionRecord,
} from "./p3-helpers"

/**
 * Design spec §10.5 item 1: create a bound version, record three requirements and
 * an attachment, draw a relation, confirm one requirement, enter design with the
 * unclarified warnings, reload and switch through the nine-tab shell.
 *
 * The P2 resources are seeded through the real admin API because the version form
 * is the subject under test; everything version-scoped happens through the UI.
 */

let systemName = ""
let environmentName = ""
let repoName = ""
let repoPath = ""
let repoCommit = ""
let versionName = ""
let versionLabel = ""
let versionId = ""

const REQ_A = "订单创建与库存扣减"
const REQ_B = "订单取消与库存回滚"
const REQ_C = "订单查询与分页"
const ATTACHMENT_FILE = "订单规则补充.md"

/** Selects one requirement in the workbench list. */
function requirementButton(page: import("@playwright/test").Page, title: string) {
  return page.getByRole("list", { name: "需求条目" }).getByRole("button").filter({ hasText: title })
}

/** Creates one requirement through the real create form. */
async function createRequirementThroughUi(
  page: import("@playwright/test").Page,
  input: { title: string; description: string; acceptance: string; priority: string },
): Promise<void> {
  const list = page.getByRole("list", { name: "需求条目" })
  const before = await list.getByRole("button").count()
  await page.getByRole("button", { name: "新增需求" }).click()

  const editor = page.getByRole("region", { name: "需求编辑器" })
  await expect(editor.getByRole("heading", { name: "新增需求" })).toBeVisible()
  await editor.getByLabel("标题").fill(input.title)
  await editor.getByLabel("描述").fill(input.description)
  await editor.getByLabel("验收标准").fill(input.acceptance)
  await editor.getByLabel("优先级").selectOption({ label: input.priority })
  await editor.getByRole("button", { name: "创建需求" }).click()

  await expect(editor.getByRole("heading", { name: "编辑需求" })).toBeVisible()
  await expect(list.getByRole("button")).toHaveCount(before + 1)
  await expect(requirementButton(page, input.title)).toBeVisible()
}

test.describe.configure({ mode: "serial" })

test.beforeAll(async ({ request }) => {
  if (await skipP3SpecWhenHarnessMissing()) return

  const suffix = uniqueSuffix()
  systemName = `P3 系统 ${suffix}`
  environmentName = `P3 环境 ${suffix}`
  repoName = `P3 代码仓 ${suffix}`
  versionName = `P3 版本 ${suffix}`
  versionLabel = `v-${suffix}`

  const repo = prepareGitRepo()
  repoPath = repo.path
  repoCommit = repo.commit

  const token = await adminApiToken(request)
  const system = await createSystem(request, token, {
    key: `p3-system-${suffix}`,
    name: systemName,
    baseUrl: "http://127.0.0.1:13335",
  })
  const environment = await createEnvironment(request, token, system.id, {
    key: `p3-env-${suffix}`,
    name: environmentName,
    baseUrl: "http://127.0.0.1:13335",
  })
  const codeRepo = await createCodeRepo(request, token, {
    key: `p3-repo-${suffix}`,
    name: repoName,
    localPath: repoPath,
    fixedBranch: repo.branch,
  })
  expect(codeRepo.id).toBeGreaterThan(0)
  expect(environment.id).toBeGreaterThan(0)
})

test("creates a bound version, relations, confirmation, design entry, reload and tabs", async ({
  page,
  request,
}) => {
  test.setTimeout(240_000)
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
  await page.goto("/versions")
  await expect(page.getByRole("heading", { name: "版本", exact: true })).toBeVisible()

  // 1. Create the version through the real dialog, with the full binding cascade.
  await page.getByRole("button", { name: "新建版本" }).click()
  const dialog = page.getByRole("dialog")
  await expect(dialog.getByRole("heading", { name: "新建版本" })).toBeVisible()
  await dialog.getByLabel("名称", { exact: true }).fill(versionName)
  await dialog.getByLabel("标识", { exact: true }).fill(versionLabel)
  await dialog.getByLabel("描述", { exact: true }).fill("P3 E2E 全链路版本")

  const systemSelect = dialog.getByLabel("系统", { exact: true })
  await expect(systemSelect.locator("option").filter({ hasText: systemName })).toHaveCount(1)
  await systemSelect.selectOption({ label: systemName })

  const environmentSelect = dialog.getByLabel("环境", { exact: true })
  await expect(environmentSelect).toBeEnabled()
  await expect(
    environmentSelect.locator("option").filter({ hasText: environmentName }),
  ).toHaveCount(1)
  await environmentSelect.selectOption({ label: environmentName })

  const repoSelect = dialog.getByLabel("代码仓", { exact: true })
  await expect(repoSelect.locator("option").filter({ hasText: repoName })).toHaveCount(1)
  await repoSelect.selectOption({ label: repoName })

  await dialog.getByRole("button", { name: "创建版本" }).click()
  await expect(page).toHaveURL(/\/versions\/\d+/)
  versionId = page.url().split("/").pop() ?? ""
  expect(versionId).toMatch(/^\d+$/)
  await expect(page.getByRole("heading", { name: versionName, level: 1 })).toBeVisible()
  await expect(page.getByText("草稿", { exact: true }).first()).toBeVisible()
  await expect(page.getByText(`系统：${systemName}`)).toBeVisible()
  await expect(page.getByText(`环境：${environmentName}`)).toBeVisible()
  await expect(page.getByText(`代码仓：${repoName}`)).toBeVisible()
  await expect(page.getByText("当前基线 commit：")).toBeVisible()
  await expect(page.getByText(repoCommit, { exact: true })).toBeVisible()

  // 2. Record three requirements.
  await createRequirementThroughUi(page, {
    title: REQ_A,
    description: "用户提交订单时创建订单并扣减库存。",
    acceptance: "订单创建成功后库存数量减少。",
    priority: "必须",
  })
  await createRequirementThroughUi(page, {
    title: REQ_B,
    description: "用户取消订单时回滚库存。",
    acceptance: "取消订单后库存数量恢复。",
    priority: "应该",
  })
  await createRequirementThroughUi(page, {
    title: REQ_C,
    description: "用户按页查询自己的订单。",
    acceptance: "每页最多 20 条订单。",
    priority: "可以",
  })
  await expect(page.getByRole("list", { name: "需求条目" }).getByRole("button")).toHaveCount(3)

  // 3. Upload one attachment on the first (still draft) requirement and download it.
  await requirementButton(page, REQ_A).click()
  const editor = page.getByRole("region", { name: "需求编辑器" })
  await expect(editor.getByRole("heading", { name: "编辑需求" })).toBeVisible()
  const attachmentSection = page.getByRole("region", { name: "需求附件" })
  await attachmentSection.getByLabel("上传附件").setInputFiles({
    name: ATTACHMENT_FILE,
    mimeType: "text/markdown",
    buffer: Buffer.from("# 订单规则\n\n- 订单金额超过 5000 元需要人工审核。\n"),
  })
  await expect(attachmentSection.getByText(ATTACHMENT_FILE)).toBeVisible()

  const downloadPromise = page.waitForEvent("download")
  await page.getByRole("button", { name: `下载附件 ${ATTACHMENT_FILE}` }).click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe(ATTACHMENT_FILE)

  // The metadata API backs the download: content is readable before deletion.
  const token = await pageToken(page)
  const requirements = await listRequirements(request, token, versionId)
  const requirementA = requirements.find((item) => item.title === REQ_A) as RequirementRecord
  expect(requirementA).toBeTruthy()
  const attachments = await listAttachments(request, token, versionId, requirementA.id)
  const attachment = attachments.find((item) => item.filename === ATTACHMENT_FILE)
  expect(attachment).toBeTruthy()
  const attachmentContentUrl = `${process.env.EZTEST_E2E_API_URL ?? "http://localhost:13333"}/api/versions/${versionId}/requirements/${requirementA.id}/attachments/${attachment?.id ?? ""}/content`
  const beforeDelete = await request.get(attachmentContentUrl, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(beforeDelete.status()).toBe(200)

  // 4. Draw one relation through the real form and verify the SVG graph.
  await page.locator("#relation-from").selectOption({ label: REQ_A })
  await page.locator("#relation-to").selectOption({ label: REQ_B })
  await page.locator("#relation-type").selectOption({ label: "依赖" })
  await page.locator("#relation-description").fill("取消依赖创建后的库存状态。")
  await page.getByRole("button", { name: "新增关系" }).click()
  const relationList = page.getByRole("list", { name: "关系列表" })
  await expect(relationList.getByText(`${REQ_A} → ${REQ_B}`)).toBeVisible()
  await expect(page.getByRole("img", { name: "需求关系图：3 个需求节点，1 条关系" })).toBeVisible()
  await expect(page.locator('[data-testid^="relation-edge-"]')).toHaveCount(1)

  // 5. Confirm one requirement (no open questions -> the gate is satisfied).
  await requirementButton(page, REQ_B).click()
  await editor.getByRole("button", { name: "确认需求" }).click()
  await expect(editor.getByText("已澄清", { exact: true })).toBeVisible()
  await expect(page.getByText("已确认 1", { exact: true })).toBeVisible()
  await expect(editor.getByText("已确认需求为只读，可重开后继续编辑。")).toBeVisible()

  // 6. Move to clarifying, then enter design: the first attempt warns and the
  // acknowledged retry succeeds.
  await page.getByRole("button", { name: "开始澄清", exact: true }).click()
  await expect(page.getByText("澄清中", { exact: true }).first()).toBeVisible()
  await page.getByRole("button", { name: "进入设计" }).click()
  const designDialog = page.getByRole("alertdialog", { name: "确认进入设计" })
  await expect(designDialog).toBeVisible()
  await expect(designDialog).toContainText("有 2 条需求尚未澄清，且仍有 0 个待澄清问题。")
  await designDialog.getByRole("button", { name: "确认进入设计" }).click()
  await expect(
    page.getByRole("status").filter({ hasText: "设计中 · 含 2 条未澄清需求" }),
  ).toBeVisible()
  const designWarnings = page
    .getByRole("status")
    .filter({ hasText: "已进入设计，以下提示请继续跟进：" })
  await expect(designWarnings).toContainText("2 条需求未澄清")
  await expect(page.getByText("设计中不可修改资源绑定。")).toBeVisible()
  await page.screenshot({ path: screenshotPath("versions-design-entry.png"), fullPage: true })

  // 7. Reload: every persisted piece survives.
  await page.reload()
  await expect(page.getByRole("heading", { name: versionName, level: 1 })).toBeVisible()
  await expect(page.getByText("需求 3", { exact: true })).toBeVisible()
  await expect(page.getByText("已确认 1", { exact: true })).toBeVisible()
  await expect(page.getByText("未澄清需求 2", { exact: true })).toBeVisible()
  await expect(page.getByText("待澄清问题 0", { exact: true }).first()).toBeVisible()
  await expect(page.getByRole("list", { name: "需求条目" }).getByRole("button")).toHaveCount(3)
  await expect(page.getByRole("img", { name: "需求关系图：3 个需求节点，1 条关系" })).toBeVisible()
  await expect(requirementButton(page, REQ_B)).toContainText("已澄清")

  // 8. Tab switching, deep links and placeholders.
  await page.getByRole("tab", { name: "总览" }).click()
  await expect(page).toHaveURL(/[?&]tab=overview/)
  const overview = page.getByRole("region", { name: "版本指标" })
  await expect(overview).toBeVisible()
  await expect(overview.getByRole("heading", { name: "需求", exact: true })).toBeVisible()
  await expect(overview.getByText("已确认 1 · 未澄清 2")).toBeVisible()
  await expect(overview.getByText(systemName)).toBeVisible()
  await expect(overview.getByText(environmentName)).toBeVisible()
  await expect(overview.getByText(repoName)).toBeVisible()

  await page.reload()
  await expect(page.getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true")
  await expect(page.getByRole("region", { name: "版本指标" })).toBeVisible()

  await page.getByRole("tab", { name: "执行会话" }).click()
  await expect(page).toHaveURL(/[?&]tab=sessions/)
  await expect(page.getByRole("heading", { name: "执行会话", level: 2 })).toBeVisible()
  await expect(page.getByText("阶段 P6", { exact: true })).toBeVisible()
  await expect(page.getByText("占位 Tab", { exact: true })).toBeVisible()
  await expect(page.getByText("该 Tab 尚未接入数据，不展示占位假数据。")).toBeVisible()
  await page.getByRole("button", { name: "返回需求与澄清工作台" }).click()
  await expect(page).toHaveURL(/[?&]tab=requirements/)
  await expect(page.getByRole("region", { name: "需求与澄清工作台" })).toBeVisible()

  await page.getByRole("tab", { name: "代码与变更" }).click()
  await expect(page.getByText("阶段 P4", { exact: true })).toBeVisible()
  await expect(page.getByRole("heading", { name: "代码与变更", level: 2 })).toBeVisible()

  await page.goto(`/versions/${versionId}?tab=report`)
  await expect(page.getByRole("heading", { name: "报告", level: 2 })).toBeVisible()
  await expect(page.getByText("阶段 P6", { exact: true })).toBeVisible()
  await expect(page.getByRole("tab", { name: "报告" })).toHaveAttribute("aria-selected", "true")

  // 9. The attachment file is really gone after a UI delete (API 404).
  await page.getByRole("button", { name: "返回需求与澄清工作台" }).click()
  await requirementButton(page, REQ_A).click()
  await expect(attachmentSection.getByText(ATTACHMENT_FILE)).toBeVisible()
  await page.getByRole("button", { name: `删除附件 ${ATTACHMENT_FILE}` }).click()
  await expect(attachmentSection.getByText(ATTACHMENT_FILE)).toHaveCount(0)
  const afterDelete = await request.get(attachmentContentUrl, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(afterDelete.status()).toBe(404)
  await page.screenshot({ path: screenshotPath("versions-workbench.png"), fullPage: true })

  // The URL is a string ID and stays one: no precision loss in the browser.
  expect(versionId).toMatch(/^\d{15,}$/)
  const finalVersion = await request.get(
    `${process.env.EZTEST_E2E_API_URL ?? "http://localhost:13333"}/api/versions/${versionId}`,
    { headers: { Authorization: `Bearer ${token}` } },
  )
  expect(finalVersion.status()).toBe(200)
  const versionBody = (await finalVersion.json()) as { data: VersionRecord }
  expect(versionBody.data.status).toBe("designing")
  expect(versionBody.data.bindings.codeRepository?.name).toBe(repoName)
  expect(versionBody.data.codeBaselineCommit).toMatch(/^[0-9a-f]{40}$/)
})

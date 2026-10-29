import { expect, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  adminApiToken,
  createCodeRepo,
  createRequirement,
  login,
  type RequirementRecord,
  uniqueSuffix,
} from "./p3-helpers"
import {
  createP4Version,
  getLatestAnalysis,
  listP4Anchors,
  p4Fixture,
  skipP4SpecWhenHarnessMissing,
  triggerAndWaitForIndex,
  waitForAnalysis,
} from "./p4-helpers"

/**
 * Design spec §10.5 item 5 (anchors): create a requirement anchor from a
 * changed-file row, force a re-analysis and get a direct requirement candidate;
 * delete the anchor, force again and the candidate is gone. The analysis itself
 * is started/forced through the real header controls.
 */

let fixture = { repo: "", baseCommit: "", headCommit: "", branch: "" }
let versionId = ""
let req: RequirementRecord

test.describe.configure({ mode: "serial" })

test.beforeAll(async ({ request }) => {
  if (await skipP4SpecWhenHarnessMissing()) return

  fixture = p4Fixture()
  const suffix = uniqueSuffix()
  const token = await adminApiToken(request)

  const repo = await createCodeRepo(request, token, {
    key: `p4-anchor-repo-${suffix}`,
    name: `P4 锚点代码仓 ${suffix}`,
    localPath: fixture.repo,
    fixedBranch: fixture.branch,
  })
  const version = await createP4Version(request, token, {
    name: `P4 锚点版本 ${suffix}`,
    label: `p4-anchor-${suffix}`,
    description: "P4 需求锚点 → 直接候选 E2E 版本",
    codeRepositoryId: repo.id,
    codeBranch: fixture.branch,
    codeBaselineCommit: fixture.baseCommit,
  })
  versionId = version.id
  req = await createRequirement(request, token, versionId, {
    title: `P4 锚点需求 ${suffix}`,
    description: "为退款调用文件创建锚点后应成为直接需求候选。",
    acceptanceCriteria: "创建锚点后重跑分析出现直接候选，删除锚点后消失。",
  })
  await triggerAndWaitForIndex(request, token, repo.id)
})

test("creates and deletes a requirement anchor around force re-analysis", async ({
  page,
  request,
}) => {
  test.setTimeout(300_000)
  const token = await adminApiToken(request)
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
  await page.goto(`/versions/${versionId}?tab=code`)
  await expect(page.getByRole("heading", { name: "变更文件" })).toBeVisible()

  // 1. Baseline analysis without any anchor: no candidates.
  await page.getByRole("button", { name: "开始分析" }).click()
  await expect(page.getByText(/分析 v1/)).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText("分析完成", { exact: true })).toBeVisible({ timeout: 120_000 })
  const baseline = await waitForAnalysis(request, token, versionId)
  expect(baseline.analysisVersion).toBe(1)
  expect(baseline.candidates).toHaveLength(0)
  const impactSlot = page.getByTestId("code-impact-slot")
  await expect(impactSlot.getByText("暂无影响候选")).toBeVisible()

  // 2. Create the requirement anchor from the changed-file row.
  await page.getByRole("button", { name: "为 src/api/refund.ts 关联需求" }).click()
  const anchorDialog = page.getByRole("dialog")
  await expect(anchorDialog.getByRole("heading", { name: "关联需求锚点" })).toBeVisible()
  await anchorDialog
    .getByLabel("需求", { exact: true })
    .selectOption({ label: `${req.title}（#${req.id}）` })
  await anchorDialog.getByRole("button", { name: "创建锚点" }).click()
  await expect(anchorDialog.getByText("已创建锚点。")).toBeVisible()
  await expect(
    anchorDialog.getByRole("listitem").filter({ hasText: "src/api/refund.ts" }),
  ).toBeVisible()
  await anchorDialog.getByRole("button", { name: "关闭关联需求" }).click()
  await expect(page.getByRole("dialog")).toHaveCount(0)
  const anchors = await listP4Anchors(request, token, versionId)
  expect(anchors).toHaveLength(1)
  expect(anchors[0].subjectKind).toBe("requirement")
  expect(anchors[0].subjectId).toBe(req.id)

  // 3. Force a re-analysis through the header confirmation: the anchor becomes a
  // direct requirement candidate with reason/confidence/evidence.
  await page.getByRole("button", { name: "重新分析" }).click()
  const confirmDialog = page.getByRole("alertdialog", { name: "确认重新分析" })
  await expect(confirmDialog).toBeVisible()
  await confirmDialog.getByRole("button", { name: "确认重新分析" }).click()
  await expect(page.getByText(/分析 v2/)).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText("分析完成", { exact: true })).toBeVisible({ timeout: 120_000 })

  const withAnchor = await getLatestAnalysis(request, token, versionId)
  expect(withAnchor?.analysisVersion).toBe(2)
  expect(withAnchor?.candidates).toHaveLength(1)
  const candidate = withAnchor?.candidates[0]
  expect(candidate?.subjectKind).toBe("requirement")
  expect(candidate?.subjectId).toBe(req.id)
  expect(candidate?.source).toBe("direct")
  expect(candidate?.reason).toBe("changed file src/api/refund.ts")
  expect(candidate?.confidence).toBe("high")

  await expect(impactSlot.getByRole("heading", { name: "需求候选" })).toBeVisible()
  const candidateCheckbox = impactSlot.getByRole("checkbox", {
    name: `选择需求 ${req.id}`,
  })
  await expect(candidateCheckbox).toBeChecked()
  await expect(impactSlot.getByText("changed file src/api/refund.ts")).toBeVisible()
  await expect(impactSlot.getByText("高置信")).toBeVisible()
  await expect(impactSlot.getByRole("button", { name: "查看证据 src/api/refund.ts" })).toBeVisible()

  // 4. Delete the anchor again through the dialog.
  await page.getByRole("button", { name: "为 src/api/refund.ts 关联需求" }).click()
  const reopen = page.getByRole("dialog")
  await reopen
    .getByLabel("需求", { exact: true })
    .selectOption({ label: `${req.title}（#${req.id}）` })
  const anchorItem = reopen.getByRole("listitem").filter({ hasText: "src/api/refund.ts" })
  await expect(anchorItem).toBeVisible()
  await reopen.getByRole("button", { name: "删除锚点 src/api/refund.ts" }).click()
  await expect(reopen.getByText("已删除锚点。")).toBeVisible()
  await expect(anchorItem).toHaveCount(0)
  await reopen.getByRole("button", { name: "关闭关联需求" }).click()
  expect(await listP4Anchors(request, token, versionId)).toHaveLength(0)

  // 5. Force once more: the direct candidate disappears from the new version.
  await page.getByRole("button", { name: "重新分析" }).click()
  await expect(confirmDialog).toBeVisible()
  await confirmDialog.getByRole("button", { name: "确认重新分析" }).click()
  await expect(page.getByText(/分析 v3/)).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText("分析完成", { exact: true })).toBeVisible({ timeout: 120_000 })

  const afterDelete = await getLatestAnalysis(request, token, versionId)
  expect(afterDelete?.analysisVersion).toBe(3)
  expect(afterDelete?.candidates).toHaveLength(0)
  await expect(
    page.getByTestId("code-impact-slot").getByRole("checkbox", {
      name: `选择需求 ${req.id}`,
    }),
  ).toHaveCount(0)
  await expect(page.getByTestId("code-impact-slot").getByText("暂无影响候选")).toBeVisible()
})

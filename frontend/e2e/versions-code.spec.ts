import { expect, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  adminApiToken,
  createCodeRepo,
  createRequirement,
  createSystem,
  login,
  type RequirementRecord,
  uniqueSuffix,
} from "./p3-helpers"
import {
  countP4FileAnalysis,
  createP4AppModel,
  createP4Version,
  getLatestAnalysis,
  getLatestIndexRun,
  listP4Anchors,
  listVersionEvents,
  type P4AppModelNode,
  p4Fixture,
  skipP4SpecWhenHarnessMissing,
  startP4Analysis,
  waitForAnalysis,
} from "./p4-helpers"

/**
 * Design spec §10.5 item 1: configure the P4 fixture repository, bind a version
 * to it with the base commit, index it from the admin repository page, preview
 * the change set and one unified diff, analyze the change (fake `ok`), inspect
 * the T0/AI facts, requirement/app-model candidates (reason/confidence/
 * evidence), toggle a candidate and reload, read the unmapped list, refresh via
 * version SSE and finally check the no-change empty state.
 *
 * The version is created through the real version API because the P2 binding
 * form always snapshots the fixed-branch tip as the baseline (the fixture head);
 * the E2E needs the explicit base commit that makes the two fixture commits a
 * reviewable change range. Everything code-intelligence specific happens
 * through the real UI.
 */

let fixture = { repo: "", baseCommit: "", headCommit: "", branch: "" }
let repoId = 0
let repoName = ""
let systemName = ""
let versionId = ""
let versionName = ""
let noChangeVersionId = ""
let reqA: RequirementRecord
let reqB: RequirementRecord
let apiNode: P4AppModelNode
let routeNode: P4AppModelNode
let componentNode: P4AppModelNode
let pageNode: P4AppModelNode

test.describe.configure({ mode: "serial" })

test.beforeAll(async ({ request }) => {
  if (await skipP4SpecWhenHarnessMissing()) return

  fixture = p4Fixture()
  const suffix = uniqueSuffix()
  systemName = `P4 系统 ${suffix}`
  repoName = `P4 代码仓 ${suffix}`
  const token = await adminApiToken(request)

  const system = await createSystem(request, token, {
    key: `p4-system-${suffix}`,
    name: systemName,
    baseUrl: "http://127.0.0.1:13335",
  })
  apiNode = await createP4AppModel(request, token, system.id, {
    key: "api:POST /api/refund",
    kind: "api",
    name: "退款接口",
    path: "/api/refund",
    method: "POST",
  })
  routeNode = await createP4AppModel(request, token, system.id, {
    key: "route:/orders",
    kind: "route",
    name: "订单列表路由",
    path: "/orders",
  })
  componentNode = await createP4AppModel(request, token, system.id, {
    key: "component:OrdersPage",
    kind: "component",
    name: "订单页组件",
  })
  pageNode = await createP4AppModel(request, token, system.id, {
    key: "page:/orders",
    kind: "page",
    name: "订单列表页",
    path: "/orders",
  })

  const repo = await createCodeRepo(request, token, {
    key: `p4-repo-${suffix}`,
    name: repoName,
    localPath: fixture.repo,
    fixedBranch: fixture.branch,
  })
  repoId = repo.id

  const version = await createP4Version(request, token, {
    name: `P4 影响分析版本 ${suffix}`,
    label: `p4-code-${suffix}`,
    description: "P4 代码理解与影响分析 E2E 版本",
    systemId: system.id,
    codeRepositoryId: repo.id,
    codeBranch: fixture.branch,
    codeBaselineCommit: fixture.baseCommit,
  })
  versionId = version.id
  versionName = version.name

  reqA = await createRequirement(request, token, versionId, {
    title: `P4 退款原因提示 ${suffix}`,
    description: "退款调用新增 reason 参数后，订单页需要提示用户填写退款原因。",
    acceptanceCriteria: "退款原因在订单页可见且提交到 POST /api/refund。",
  })
  reqB = await createRequirement(request, token, versionId, {
    title: `P4 退款路由入口 ${suffix}`,
    description: "新增 /orders/refund 路由作为退款入口。",
    acceptanceCriteria: "订单页可跳转到退款路由。",
  })

  // A second version pinned to head->head proves the documented no-change state.
  const noChange = await createP4Version(request, token, {
    name: `P4 无变更版本 ${suffix}`,
    label: `p4-nochange-${suffix}`,
    description: "基线等于 head 的空变更版本",
    systemId: system.id,
    codeRepositoryId: repo.id,
    codeBranch: fixture.branch,
    codeBaselineCommit: fixture.headCommit,
  })
  noChangeVersionId = noChange.id
})

test("previews, indexes, analyzes, persists selection and refreshes via SSE", async ({
  page,
  request,
}) => {
  test.setTimeout(300_000)
  const token = await adminApiToken(request)
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)

  // 1. Before the index the code tab reports the documented unindexed empty state.
  await page.goto(`/versions/${versionId}?tab=code`)
  await expect(page.getByRole("heading", { name: versionName, level: 1 })).toBeVisible()
  await expect(page.getByText(`系统：${systemName}`)).toBeVisible()
  await expect(page.getByRole("heading", { name: "代码仓库尚未索引" })).toBeVisible()
  await expect(page.getByText("索引由管理员在「代码仓库」页面触发")).toBeVisible()

  // 2. The admin triggers the T0 index from the real repository page and the
  // status reaches done with the fixture head commit and an indexed time.
  await page.goto("/code-repos")
  const row = page.getByRole("row").filter({ hasText: repoName })
  await expect(row).toBeVisible()
  await expect(row).toContainText("未索引")
  await row.getByRole("button", { name: "刷新索引" }).click()
  const indexCell = row.locator("td").nth(5)
  await expect(indexCell).toContainText("已索引", { timeout: 60_000 })
  await expect(indexCell).toContainText(fixture.headCommit.slice(0, 8))
  await expect(indexCell).toContainText(/\d{4}\/\d{1,2}\/\d{1,2}/)

  const indexRun = await getLatestIndexRun(request, token, repoId)
  expect(indexRun.status).toBe("done")
  expect(indexRun.commit).toBe(fixture.headCommit)
  expect(indexRun.startedAt).not.toBeNull()
  expect(indexRun.finishedAt).not.toBeNull()
  expect(indexRun.stats?.files).toBeGreaterThan(0)

  // 3. The code tab shows the real file-level change preview.
  await page.goto(`/versions/${versionId}?tab=code`)
  await expect(page.getByRole("heading", { name: "变更文件" })).toBeVisible()
  await expect(page.getByText("共 7 个文件", { exact: false })).toBeVisible()
  await expect(
    page.getByText(fixture.baseCommit.slice(0, 8), { exact: true }).first(),
  ).toBeVisible()
  for (const path of [
    "src/api/refund.ts",
    "src/router.ts",
    "src/pages/OrdersPage.tsx",
    "server/refund.go",
    "src/utils/format.ts",
    "src/legacy/old.ts",
    "assets/logo.png",
  ]) {
    await expect(page.getByRole("button", { name: `查看 ${path} 的 diff` })).toBeVisible()
  }
  await expect(page.getByRole("button", { name: "查看 src/legacy/old.ts 的 diff" })).toContainText(
    "删除",
  )
  await expect(page.getByRole("button", { name: "查看 assets/logo.png 的 diff" })).toContainText(
    "二进制",
  )

  // 4. One unified diff opens in the drawer and closes with ESC.
  await page.getByRole("button", { name: "查看 src/api/refund.ts 的 diff" }).click()
  const diffDrawer = page.getByRole("dialog")
  await expect(diffDrawer.getByRole("heading", { name: "src/api/refund.ts" })).toBeVisible()
  await expect(diffDrawer.locator('[data-line-kind="add"]').first()).toBeVisible()
  await expect(diffDrawer.locator("pre")).toContainText("reason")
  await page.keyboard.press("Escape")
  await expect(diffDrawer).toHaveCount(0)

  // 5. Create a requirement anchor from the changed-file row (it becomes a
  // direct requirement candidate below).
  await page.getByRole("button", { name: "为 src/api/refund.ts 关联需求" }).click()
  const anchorDialog = page.getByRole("dialog")
  await expect(anchorDialog.getByRole("heading", { name: "关联需求锚点" })).toBeVisible()
  await anchorDialog
    .getByLabel("需求", { exact: true })
    .selectOption({ label: `${reqA.title}（#${reqA.id}）` })
  await anchorDialog.getByRole("button", { name: "创建锚点" }).click()
  await expect(anchorDialog.getByText("已创建锚点。")).toBeVisible()
  await expect(
    anchorDialog.getByRole("listitem").filter({ hasText: "src/api/refund.ts" }),
  ).toBeVisible()
  await anchorDialog.getByRole("button", { name: "关闭关联需求" }).click()
  await expect(page.getByRole("dialog")).toHaveCount(0)
  expect(await listP4Anchors(request, token, versionId)).toHaveLength(1)

  // 6. Start the analysis from the header (the request body stays `{}`; the
  // version's fixed branch tip is resolved by the backend).
  const p4RequestsBefore = countP4FileAnalysis(fixture.headCommit)
  await page.getByRole("button", { name: "开始分析" }).click()
  await expect(page.getByText(/分析 v1/)).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText("分析完成", { exact: true })).toBeVisible({ timeout: 120_000 })

  const analysis = await waitForAnalysis(request, token, versionId)
  expect(analysis.status).toBe("done")
  expect(analysis.degraded).toBe(false)
  expect(analysis.baseCommit).toBe(fixture.baseCommit)
  expect(analysis.headCommit).toBe(fixture.headCommit)
  expect(analysis.analysisVersion).toBe(1)
  expect(analysis.changeSummary).not.toBe("")
  expect(analysis.stats.changedFiles).toBe(7)
  expect(analysis.stats.analyzedFiles).toBe(5)
  expect(analysis.stats.facts).toBe(5)
  expect(analysis.candidates).toHaveLength(4)
  expect(analysis.unmapped).toHaveLength(3)
  // The analysis really went through the fake LLM: one request per analyzable
  // file (5 files; the deleted/binary files are skipped, spec §6.2).
  expect(countP4FileAnalysis(fixture.headCommit) - p4RequestsBefore).toBe(5)

  // 7. Fact panel: real T0/AI facts grouped by kind with evidence links.
  const factsSlot = page.getByTestId("code-facts-slot")
  await expect(factsSlot.getByRole("heading", { name: "代码事实" })).toBeVisible()
  await expect(factsSlot.locator('[data-fact-kind="route"]')).toHaveCount(2)
  await expect(factsSlot.locator('[data-fact-kind="client_call"]')).toHaveCount(1)
  await expect(factsSlot.locator('[data-fact-kind="api"]')).toHaveCount(1)
  await expect(factsSlot.locator('[data-fact-kind="component"]')).toHaveCount(1)
  await expect(factsSlot.getByText("低置信线索")).toHaveCount(0)
  await expect(factsSlot.getByRole("button", { name: "查看证据 src/router.ts:5-7" })).toBeVisible()

  // 8. Candidate panel: requirement + app-model groups with reason, confidence
  // and evidence; every direct candidate is selected by default.
  const impactSlot = page.getByTestId("code-impact-slot")
  await expect(impactSlot.getByRole("heading", { name: "影响候选" })).toBeVisible()
  await expect(impactSlot.getByRole("heading", { name: "需求候选" })).toBeVisible()
  await expect(impactSlot.getByRole("heading", { name: "应用模型候选" })).toBeVisible()
  await expect(impactSlot.getByText("4 个候选")).toBeVisible()

  const requirementCheckbox = impactSlot.getByRole("checkbox", {
    name: `选择需求 ${reqA.id}`,
  })
  await expect(requirementCheckbox).toBeChecked()
  await expect(impactSlot.getByText("changed file src/api/refund.ts")).toBeVisible()
  await expect(
    impactSlot.getByRole("button", { name: "查看证据 src/api/refund.ts", exact: true }),
  ).toBeVisible()
  await expect(impactSlot.getByText("高置信").first()).toBeVisible()
  await expect(impactSlot.locator('[data-source="direct"]')).toHaveCount(4)

  for (const node of [apiNode, routeNode, componentNode, pageNode]) {
    const checkbox = impactSlot.getByRole("checkbox", {
      name: `选择应用模型 ${node.id}`,
    })
    if (node === pageNode) {
      // The fixture has no `page` fact, so the page node must not be a candidate.
      await expect(checkbox).toHaveCount(0)
      continue
    }
    await expect(checkbox).toBeChecked()
  }
  // Only reqA was anchored: the untouched requirement must not be a candidate.
  await expect(impactSlot.getByRole("checkbox", { name: `选择需求 ${reqB.id}` })).toHaveCount(0)
  await expect(impactSlot.getByText("changed api POST /api/refund")).toBeVisible()
  await expect(impactSlot.getByText("changed route /orders")).toBeVisible()
  await expect(impactSlot.getByText("changed component OrdersPage")).toBeVisible()

  // 9. Unmapped changes: one entry per uncovered file with the closed reason set.
  const unmappedSlot = page.getByTestId("code-unmapped-slot")
  await expect(unmappedSlot.getByRole("heading", { name: "未映射变更" })).toBeVisible()
  await expect(unmappedSlot.locator('[data-unmapped-reason="no_fact_or_anchor"]')).toContainText(
    "src/utils/format.ts",
  )
  await expect(unmappedSlot.locator('[data-unmapped-reason="deleted_file"]')).toContainText(
    "src/legacy/old.ts",
  )
  await expect(unmappedSlot.locator('[data-unmapped-reason="unsupported_content"]')).toContainText(
    "assets/logo.png",
  )

  // 10. Toggle the requirement candidate off, verify the PATCH persisted through
  // the API, then reload and read the same unselected state from the database.
  await requirementCheckbox.click()
  await expect(requirementCheckbox).not.toBeChecked()
  await expect
    .poll(async () => {
      const latest = await getLatestAnalysis(request, token, versionId)
      return latest?.candidates.find(
        (candidate) => candidate.subjectKind === "requirement" && candidate.subjectId === reqA.id,
      )?.selected
    })
    .toBe(false)

  await page.reload()
  await expect(page.getByText(/分析 v1/)).toBeVisible({ timeout: 30_000 })
  const reloadedCheckbox = page
    .getByTestId("code-impact-slot")
    .getByRole("checkbox", { name: `选择需求 ${reqA.id}` })
  await expect(reloadedCheckbox).not.toBeChecked()

  // 11. SSE-driven refresh: a force analysis started through the API while the
  // code tab is open must update the page with no manual refresh. The page is
  // not polling a done analysis, so the `code.changed`/`code.impact_computed`
  // version events are the only refresh trigger (spec §9.3/§10.5).
  const force = await startP4Analysis(request, token, versionId, { force: true })
  expect(force.status).toBe(202)
  expect(force.data.analysisVersion).toBe(2)
  await expect(page.getByText(/分析 v2/)).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText("分析完成", { exact: true })).toBeVisible({ timeout: 120_000 })
  await expect
    .poll(async () => {
      const events = await listVersionEvents(request, token, versionId)
      return events.some((event) => event.type === "code.impact_computed")
    })
    .toBe(true)

  const refreshed = await getLatestAnalysis(request, token, versionId)
  expect(refreshed?.analysisVersion).toBe(2)
  expect(refreshed?.candidates).toHaveLength(4)
  await expect(
    page.getByTestId("code-impact-slot").getByRole("checkbox", { name: `选择需求 ${reqA.id}` }),
  ).toBeChecked()

  // 12. No-change empty state: a version whose baseline is its head.
  await page.goto(`/versions/${noChangeVersionId}?tab=code`)
  await expect(page.getByRole("heading", { name: "无变更" })).toBeVisible()
  await expect(page.getByText("之间没有文件变更。", { exact: false })).toBeVisible()
})

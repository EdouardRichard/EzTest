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
  createP4Anchor,
  createP4AppModel,
  createP4Version,
  type P4AppModelNode,
  p4Fixture,
  setP4Mode,
  skipP4SpecWhenHarnessMissing,
  startP4Analysis,
  triggerAndWaitForIndex,
  waitForAnalysis,
} from "./p4-helpers"

/**
 * Design spec §10.5 item 3 (degraded path): switch the fake LLM's P4 scenario to
 * `error` at runtime, then analyze. The analysis must finish with HTTP 2xx,
 * `degraded=true`, the 「AI 不可用，仅文件级结果」 banner, the deterministic T0
 * candidates from anchors and `ai_unavailable` unmapped entries. Restoring the
 * mode and forcing a re-analysis must succeed and clear the degraded state.
 *
 * The runtime switch uses the test-only `POST /__p4-mode` handle of
 * scripts/fake-llm.cjs; it is always reset to `ok` afterwards so later specs
 * never observe the degraded mode.
 */

let fixture = { repo: "", baseCommit: "", headCommit: "", branch: "" }
let repoId = 0
let versionId = ""
let req: RequirementRecord
let apiNode: P4AppModelNode

const AI_UNAVAILABLE_BANNER = "AI 不可用，仅文件级结果"

test.describe.configure({ mode: "serial" })

test.beforeAll(async ({ request }) => {
  if (await skipP4SpecWhenHarnessMissing()) return

  fixture = p4Fixture()
  const suffix = uniqueSuffix()
  const token = await adminApiToken(request)

  const system = await createSystem(request, token, {
    key: `p4-degraded-system-${suffix}`,
    name: `P4 降级系统 ${suffix}`,
    baseUrl: "http://127.0.0.1:13335",
  })
  apiNode = await createP4AppModel(request, token, system.id, {
    key: "api:POST /api/refund",
    kind: "api",
    name: "退款接口",
    path: "/api/refund",
    method: "POST",
  })
  const repo = await createCodeRepo(request, token, {
    key: `p4-degraded-repo-${suffix}`,
    name: `P4 降级代码仓 ${suffix}`,
    localPath: fixture.repo,
    fixedBranch: fixture.branch,
  })
  repoId = repo.id
  const version = await createP4Version(request, token, {
    name: `P4 降级版本 ${suffix}`,
    label: `p4-degraded-${suffix}`,
    description: "P4 AI 降级路径 E2E 版本",
    systemId: system.id,
    codeRepositoryId: repo.id,
    codeBranch: fixture.branch,
    codeBaselineCommit: fixture.baseCommit,
  })
  versionId = version.id
  req = await createRequirement(request, token, versionId, {
    title: `P4 降级需求 ${suffix}`,
    description: "AI 不可用时该需求的锚点仍必须产生确定性候选。",
    acceptanceCriteria: "降级分析仍展示直接候选与未映射变更。",
  })

  // Deterministic T0 candidates: one requirement anchor and one app-model anchor
  // (the UI only creates requirement anchors; the app-model kind is exercised
  // through the API so the degraded T0 path covers both subject kinds).
  await createP4Anchor(request, token, versionId, {
    subjectKind: "requirement",
    subjectId: req.id,
    filePath: "src/api/refund.ts",
  })
  await createP4Anchor(request, token, versionId, {
    subjectKind: "app_model",
    subjectId: String(apiNode.id),
    filePath: "src/router.ts",
  })
  await triggerAndWaitForIndex(request, token, repoId)
})

test.afterAll(async ({ request }) => {
  // The fake LLM mode is global to the harness; never leak the degraded mode.
  try {
    await setP4Mode(request, "ok")
  } catch {
    // The shared fake upstream may already be gone at teardown.
  }
})

test("degrades to T0 results when the AI is unavailable and recovers on force", async ({
  page,
  request,
}) => {
  test.setTimeout(240_000)
  const token = await adminApiToken(request)

  try {
    // 1. Switch the fake P4 scenario to error while the servers keep running.
    await setP4Mode(request, "error")

    // 2. The analysis still succeeds (2xx) and is marked degraded.
    const started = await startP4Analysis(request, token, versionId)
    expect(started.status).toBe(202)
    const degraded = await waitForAnalysis(request, token, versionId, started.data.id)
    expect(degraded.status).toBe("done")
    expect(degraded.degraded).toBe(true)
    expect(degraded.analysisVersion).toBe(1)

    // 3. The deterministic T0 candidates are still there (one per anchor) and the
    // uncovered files are explicitly unmapped as `ai_unavailable`.
    expect(degraded.candidates).toHaveLength(2)
    const requirementCandidate = degraded.candidates.find(
      (candidate) => candidate.subjectKind === "requirement",
    )
    expect(requirementCandidate?.subjectId).toBe(req.id)
    expect(requirementCandidate?.source).toBe("direct")
    expect(requirementCandidate?.reason).toBe("changed file src/api/refund.ts")
    const appModelCandidate = degraded.candidates.find(
      (candidate) => candidate.subjectKind === "app_model",
    )
    expect(appModelCandidate?.subjectId).toBe(String(apiNode.id))
    expect(appModelCandidate?.source).toBe("direct")
    expect(appModelCandidate?.reason).toBe("changed file src/router.ts")
    expect(degraded.unmapped.some((entry) => entry.reason === "ai_unavailable")).toBe(true)

    // 4. The UI shows the degraded banner together with the T0 candidates and
    // the `ai_unavailable` unmapped reason.
    await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
    await page.goto(`/versions/${versionId}?tab=code`)
    await expect(page.getByText(/分析 v1/)).toBeVisible({ timeout: 30_000 })
    await expect(page.getByText(AI_UNAVAILABLE_BANNER).first()).toBeVisible()
    await expect(page.getByText("降级", { exact: true }).first()).toBeVisible()

    const impactSlot = page.getByTestId("code-impact-slot")
    await expect(impactSlot.getByRole("checkbox", { name: `选择需求 ${req.id}` })).toBeChecked()
    await expect(
      impactSlot.getByRole("checkbox", { name: `选择应用模型 ${apiNode.id}` }),
    ).toBeChecked()
    await expect(impactSlot.getByText("changed file src/api/refund.ts")).toBeVisible()
    await expect(impactSlot.getByText("changed file src/router.ts")).toBeVisible()
    const unmappedSlot = page.getByTestId("code-unmapped-slot")
    await expect(
      unmappedSlot.locator('[data-unmapped-reason="ai_unavailable"]').first(),
    ).toBeVisible()
    await expect(page.getByTestId("code-facts-slot").getByText("暂无代码事实")).toBeVisible()

    // 5. Restore the fake AI and force a new analysis version: it succeeds
    // without degradation, keeps the T0 candidates and has no ai_unavailable
    // unmapped entries anymore.
    await setP4Mode(request, "ok")
    const forced = await startP4Analysis(request, token, versionId, { force: true })
    expect(forced.status).toBe(202)
    expect(forced.data.analysisVersion).toBe(2)
    const recovered = await waitForAnalysis(request, token, versionId, forced.data.id)
    expect(recovered.status).toBe("done")
    expect(recovered.degraded).toBe(false)
    expect(recovered.analysisVersion).toBe(2)
    expect(recovered.candidates.length).toBeGreaterThanOrEqual(2)
    expect(
      recovered.candidates.some(
        (candidate) =>
          candidate.subjectKind === "requirement" &&
          candidate.subjectId === req.id &&
          candidate.source === "direct",
      ),
    ).toBe(true)
    expect(recovered.unmapped.every((entry) => entry.reason !== "ai_unavailable")).toBe(true)

    // 6. The refreshed page no longer shows the degraded banner and renders the
    // recovered AI facts next to the T0 candidates.
    await page.reload()
    await expect(page.getByText(/分析 v2/)).toBeVisible({ timeout: 60_000 })
    await expect(page.getByText("分析完成", { exact: true })).toBeVisible({ timeout: 60_000 })
    await expect(page.getByText(AI_UNAVAILABLE_BANNER)).toHaveCount(0)
    await expect(
      page.getByTestId("code-impact-slot").getByRole("checkbox", { name: `选择需求 ${req.id}` }),
    ).toBeChecked()
    await expect(
      page.getByTestId("code-facts-slot").locator('[data-fact-kind="route"]').first(),
    ).toBeVisible()
  } finally {
    // Always restore the P4 scenario, even when an assertion above fails.
    await setP4Mode(request, "ok")
  }
})

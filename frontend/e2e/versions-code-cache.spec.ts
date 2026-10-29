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
  candidateProjection,
  countP4FileAnalysis,
  countP4Requests,
  createP4Anchor,
  createP4Version,
  getAnalysisById,
  p4Fixture,
  skipP4SpecWhenHarnessMissing,
  startP4Analysis,
  triggerAndWaitForIndex,
  waitForAnalysis,
} from "./p4-helpers"

/**
 * Design spec §10.5 item 2 (cache/versioning): the same version+commit is reused
 * (`reused=true`, zero new LLM calls), a second version bound to the same
 * repo/base analyzing the same head hits the file-level cache
 * (`stats.cacheHits>0`, no new file-analysis LLM calls), and a forced
 * re-analysis creates analysis version 2 while the old analysis stays readable
 * with unchanged candidates. The final UI assertion proves the latest analysis
 * version renders through the real version page.
 */

let fixture = { repo: "", baseCommit: "", headCommit: "", branch: "" }
let repoId = 0
let repoName = ""
let versionOneId = ""
let versionTwoId = ""
let reqOne: RequirementRecord
let reqTwo: RequirementRecord

test.describe.configure({ mode: "serial" })

test.beforeAll(async ({ request }) => {
  if (await skipP4SpecWhenHarnessMissing()) return

  fixture = p4Fixture()
  const suffix = uniqueSuffix()
  repoName = `P4 缓存代码仓 ${suffix}`
  const token = await adminApiToken(request)

  const repo = await createCodeRepo(request, token, {
    key: `p4-cache-repo-${suffix}`,
    name: repoName,
    localPath: fixture.repo,
    fixedBranch: fixture.branch,
  })
  repoId = repo.id

  const versionOne = await createP4Version(request, token, {
    name: `P4 缓存版本一 ${suffix}`,
    label: `p4-cache-1-${suffix}`,
    description: "P4 缓存与复用 E2E 版本一",
    codeRepositoryId: repo.id,
    codeBranch: fixture.branch,
    codeBaselineCommit: fixture.baseCommit,
  })
  const versionTwo = await createP4Version(request, token, {
    name: `P4 缓存版本二 ${suffix}`,
    label: `p4-cache-2-${suffix}`,
    description: "P4 缓存与复用 E2E 版本二（同 repo/base）",
    codeRepositoryId: repo.id,
    codeBranch: fixture.branch,
    codeBaselineCommit: fixture.baseCommit,
  })
  versionOneId = versionOne.id
  versionTwoId = versionTwo.id

  reqOne = await createRequirement(request, token, versionOneId, {
    title: `P4 缓存需求一 ${suffix}`,
    description: "版本一的需求锚点用于验证强制重跑后的候选不变。",
  })
  reqTwo = await createRequirement(request, token, versionTwoId, {
    title: `P4 缓存需求二 ${suffix}`,
    description: "版本二的需求锚点用于验证文件级缓存。",
  })
  await createP4Anchor(request, token, versionOneId, {
    subjectKind: "requirement",
    subjectId: reqOne.id,
    filePath: "src/api/refund.ts",
  })
  await createP4Anchor(request, token, versionTwoId, {
    subjectKind: "requirement",
    subjectId: reqTwo.id,
    filePath: "src/api/refund.ts",
  })
})

test("reuses identical input, caches file analysis and keeps old versions readable", async ({
  page,
  request,
}) => {
  test.setTimeout(240_000)
  const token = await adminApiToken(request)

  // 1. First analysis of version one: a fresh run through the fake LLM.
  const fileRequestsBeforeFirst = countP4FileAnalysis(fixture.headCommit)
  const first = await startP4Analysis(request, token, versionOneId)
  expect(first.status).toBe(202)
  expect(first.data.reused).toBe(false)
  expect(first.data.analysisVersion).toBe(1)
  const analysisOne = await waitForAnalysis(request, token, versionOneId, first.data.id)
  expect(analysisOne.status).toBe("done")
  expect(analysisOne.degraded).toBe(false)
  expect(analysisOne.baseCommit).toBe(fixture.baseCommit)
  expect(analysisOne.headCommit).toBe(fixture.headCommit)
  expect(analysisOne.candidates).toHaveLength(1)
  expect(analysisOne.candidates[0].subjectKind).toBe("requirement")
  expect(analysisOne.candidates[0].subjectId).toBe(reqOne.id)

  const fileRequestsAfterFirst = countP4FileAnalysis(fixture.headCommit)
  // This fresh repository misses the file-level cache: one LLM call per
  // analyzable file (5 files; the deleted/binary files are skipped).
  expect(fileRequestsAfterFirst - fileRequestsBeforeFirst).toBe(5)

  // 2. The same version+commit+model+prompt is reused with no new LLM call.
  const allRequestsAfterFirst = countP4Requests()
  const reused = await startP4Analysis(request, token, versionOneId)
  expect(reused.status).toBe(200)
  expect(reused.data.reused).toBe(true)
  expect(reused.data.id).toBe(first.data.id)
  expect(reused.data.analysisVersion).toBe(1)
  expect(countP4Requests()).toBe(allRequestsAfterFirst)

  // 3. A second version with the same repo/base and the same head reuses the
  // file-level cache: cacheHits > 0 and no new file-analysis LLM requests.
  const second = await startP4Analysis(request, token, versionTwoId)
  expect(second.status).toBe(202)
  expect(second.data.reused).toBe(false)
  const analysisTwo = await waitForAnalysis(request, token, versionTwoId, second.data.id)
  expect(analysisTwo.status).toBe("done")
  expect(analysisTwo.stats.cacheHits).toBeGreaterThan(0)
  expect(analysisTwo.stats.analyzedFiles).toBe(5)
  expect(countP4FileAnalysis(fixture.headCommit)).toBe(fileRequestsAfterFirst)
  expect(analysisTwo.candidates).toHaveLength(1)
  expect(analysisTwo.candidates[0].subjectId).toBe(reqTwo.id)
  expect(analysisTwo.candidates[0].source).toBe("direct")

  // 4. A forced re-analysis skips the cache, creates version 2, and leaves the
  // old analysis readable with an unchanged candidate projection.
  const analysisOneProjection = candidateProjection(analysisOne)
  const fileRequestsBeforeForce = countP4FileAnalysis(fixture.headCommit)
  const forced = await startP4Analysis(request, token, versionOneId, { force: true })
  expect(forced.status).toBe(202)
  expect(forced.data.analysisVersion).toBe(2)
  const analysisForced = await waitForAnalysis(request, token, versionOneId, forced.data.id)
  expect(analysisForced.id).not.toBe(first.data.id)
  expect(analysisForced.analysisVersion).toBe(2)
  expect(analysisForced.status).toBe("done")
  expect(countP4FileAnalysis(fixture.headCommit)).toBeGreaterThan(fileRequestsBeforeForce)

  const oldAnalysis = await getAnalysisById(request, token, versionOneId, first.data.id)
  expect(oldAnalysis.id).toBe(first.data.id)
  expect(oldAnalysis.status).toBe("done")
  expect(oldAnalysis.analysisVersion).toBe(1)
  expect(candidateProjection(oldAnalysis)).toEqual(analysisOneProjection)
  expect(candidateProjection(analysisForced)).toEqual(analysisOneProjection)

  // 5. The real version page renders the latest (forced) analysis version.
  await triggerAndWaitForIndex(request, token, repoId)
  await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
  await page.goto(`/versions/${versionOneId}?tab=code`)
  await expect(page.getByText(`代码仓：${repoName}`)).toBeVisible()
  await expect(page.getByText(/分析 v2/)).toBeVisible({ timeout: 60_000 })
  await expect(
    page.getByTestId("code-impact-slot").getByRole("checkbox", { name: `选择需求 ${reqOne.id}` }),
  ).toBeChecked()
})

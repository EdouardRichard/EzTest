import { readFileSync } from "node:fs"

import { type APIRequestContext, expect, type Page, test } from "@playwright/test"

import {
  ADMIN_PASSWORD,
  ADMIN_USERNAME,
  API_URL,
  login,
  screenshotPath,
  uniqueSuffix,
} from "./p3-helpers"
import { authHeaders } from "./p4-helpers"
import {
  getMindmap,
  type MindmapGraph,
  type MindmapNode,
  type MindmapSeed,
  readCaseEvents,
  seedMindmapVersion,
  skipMindmapSpecWhenHarnessMissing,
} from "./p5-helpers"

/**
 * P5 mindmap E2E (design spec §10.5, one serial scenario, fake LLM):
 *
 *  1. login + API seed (system/environment/app model/code repo/version/two
 *     clarified requirements);
 *  2. empty state dual entry -> generate -> fake design proposal materialized;
 *  3. edit title/priority/steps/assertions/reorder -> reload persistence, plus
 *     the "last requirement source" 409 negative (must run while editable);
 *  4. review run -> findings -> blocker acknowledgement -> approve -> baseline;
 *  5. frozen graph: disabled editor and a forced 409 `baseline_frozen` write;
 *  6. download Markdown/OPML/Mermaid (content) and PNG (magic bytes) through
 *     real Playwright `download` events;
 *  7. poll the version event snapshot for the four `case.*` events and check
 *     the live SSE badge;
 *  8. non-admin: no explore trigger in the run panel and a direct explore run
 *     is rejected with 403 `forbidden`.
 *
 * The spec deliberately goes through the real UI/API/fake LLM. It skips itself
 * when the deterministic harness (admin credentials + fake LLM) is absent.
 */

const NORMAL_USER_PASSWORD = process.env.EZTEST_E2E_USER_PASSWORD?.trim() || "qa-e2e-pass-123"

const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])

/** The four `case.*` events the E2E must observe (spec §6.11/§10.5 step 7). */
const REQUIRED_CASE_EVENTS = [
  "case.created",
  "case.reviewed",
  "case.baseline_frozen",
  "case.export_ready",
] as const

test.describe.configure({ mode: "serial" })

type DownloadedFile = { filename: string; bytes: Buffer }

/** Triggers one export button and returns its real browser download bytes. */
async function downloadFromButton(page: Page, testId: string): Promise<DownloadedFile> {
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    page.getByTestId(testId).click(),
  ])
  const path = await download.path()
  if (path === null) throw new Error(`${testId} did not produce a local download file`)
  return { filename: download.suggestedFilename(), bytes: readFileSync(path) }
}

/** Finds one node in a snapshot, failing with its id when it disappeared. */
function graphNode(graph: MindmapGraph, nodeId: string): MindmapNode {
  const node = graph.nodes.find((candidate) => candidate.id === nodeId)
  if (node === undefined) throw new Error(`mindmap node ${nodeId} is missing from the graph`)
  return node
}

/** Creates a normal (non-admin) user through the real admin API. */
async function createNormalUser(
  request: APIRequestContext,
  adminToken: string,
  username: string,
  password: string,
): Promise<void> {
  const response = await request.post(`${API_URL}/api/users`, {
    headers: authHeaders(adminToken),
    data: { username, password },
  })
  expect(response.status(), "create normal user").toBe(201)
}

/** Logs a normal user in through the real API (used for the 403 probe). */
async function normalUserToken(
  request: APIRequestContext,
  username: string,
  password: string,
): Promise<string> {
  const response = await request.post(`${API_URL}/api/auth/login`, {
    data: { username, password },
  })
  expect(response.status(), "normal user login").toBe(200)
  const body = (await response.json()) as { data: { token?: string } }
  const token = body.data.token ?? ""
  expect(token).not.toBe("")
  return token
}

/** Reads the API error envelope of a failed control request. */
async function readErrorReason(response: {
  json: () => Promise<unknown>
}): Promise<string | undefined> {
  const body = (await response.json()) as {
    error?: { code?: string; details?: { reason?: string } }
  }
  return body.error?.details?.reason ?? body.error?.code
}

test("generates, edits, reviews, freezes and exports a P5 case mindmap", async ({
  page,
  request,
}) => {
  test.setTimeout(420_000)
  if (await skipMindmapSpecWhenHarnessMissing()) return

  // --- 1. login + API seed -------------------------------------------------
  const seed: MindmapSeed = await test.step("1. seed version, requirements and scope", async () => {
    await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
    return seedMindmapVersion(request, { suffix: uniqueSuffix() })
  })
  const versionId = seed.version.id
  const token = seed.token
  const firstRequirement = seed.requirements[0]
  const secondRequirement = seed.requirements[1]
  if (firstRequirement === undefined || secondRequirement === undefined) {
    throw new Error("the P5 seed must create two clarified requirements")
  }

  const editedTitle = `P5 E2E 编辑用例 ${seed.suffix}`
  const newStepAction = "P5 E2E 新增步骤"
  const newStepExpected = "P5 E2E 新增预期"
  const newAssertionExpression = "P5 E2E 新增断言"
  const normalUsername = `p5user${seed.suffix}`

  // The version is still `designing` here, so a 403 can only come from the
  // admin-only explore rule (spec §6.3/§6.12), not from the version state.
  await test.step("1b. a non-admin explore run is forbidden", async () => {
    await createNormalUser(request, token, normalUsername, NORMAL_USER_PASSWORD)
    const userToken = await normalUserToken(request, normalUsername, NORMAL_USER_PASSWORD)
    const explore = await request.post(`${API_URL}/api/versions/${versionId}/mindmap/runs`, {
      headers: authHeaders(userToken),
      data: {
        kind: "explore",
        systemId: seed.systemId,
        environmentId: seed.environmentId,
      },
    })
    expect(explore.status(), "non-admin explore run").toBe(403)
    expect(await readErrorReason(explore)).toBe("forbidden")
  })

  // --- 2. empty state -> AI generation ------------------------------------
  await test.step("2. generate the mindmap from the fake design proposal", async () => {
    await page.goto(`/versions/${versionId}/mindmap`)
    await expect(page.getByTestId("mindmap-empty")).toBeVisible()
    await expect(page.getByTestId("btn-empty-generate")).toBeVisible()
    await expect(page.getByTestId("btn-empty-manual")).toBeVisible()
    await expect(page.getByTestId("btn-generate")).toBeEnabled()
    await page.screenshot({
      path: screenshotPath("mindmap-01-empty.png"),
      fullPage: true,
    })

    await page.getByTestId("btn-generate").click()

    const scenarioNodes = page.locator('[data-testid^="scenario-node-"]')
    const caseNodes = page.locator('[data-testid^="case-node-"]')
    await expect(scenarioNodes).toHaveCount(1, { timeout: 120_000 })
    await expect(caseNodes).toHaveCount(2, { timeout: 120_000 })
    await expect(scenarioNodes.first()).toContainText("场景 · AI")
    await expect(caseNodes.first()).toContainText("用例 · AI")

    const generated = await getMindmap(request, token, versionId)
    expect(generated.nodes).toHaveLength(3)
    expect(generated.nodes.filter((node) => node.kind === "case")).toHaveLength(2)
    for (const node of generated.nodes) {
      expect(node.source).toBe("ai_generated")
    }
    const generatedCases = generated.nodes.filter((node) => node.kind === "case")
    for (const node of generatedCases) {
      expect(node.steps.length).toBeGreaterThanOrEqual(2)
      expect(node.assertions.length).toBeGreaterThanOrEqual(1)
      expect(node.sources.some((source) => source.refKind === "requirement")).toBe(true)
    }
    expect(generated.coverage.requirementIds).toHaveLength(2)
    // A successful design run moves the version into the reviewing state (spec
    // §6.3); the review run and the approve gate below depend on it.
    await expect
      .poll(async () => (await getMindmap(request, token, versionId)).versionStatus, {
        timeout: 60_000,
      })
      .toBe("reviewing")
    await page.screenshot({
      path: screenshotPath("mindmap-02-generated.png"),
      fullPage: true,
    })
  })

  // --- 3. edit + reload persistence + last-source 409 ---------------------
  const caseId = await test.step("3. edit a case, add step/assertion and reorder", async () => {
    await expect(page.getByTestId("mindmap-canvas")).toBeVisible()
    const graph = await getMindmap(request, token, versionId)
    const target = graph.nodes.find((node) => node.kind === "case")
    if (target === undefined) throw new Error("the generated graph has no case node")
    const nodeId = target.id

    await page.getByTestId(`case-node-${nodeId}`).click()
    await expect(page.getByTestId("case-editor")).toBeVisible()

    await page.getByLabel("用例标题").fill(editedTitle)
    await page.getByLabel("优先级").selectOption("P0")
    await page.getByRole("button", { name: "保存用例" }).click()
    await expect(page.getByLabel("用例标题")).toHaveValue(editedTitle)
    await expect
      .poll(async () => graphNode(await getMindmap(request, token, versionId), nodeId).title, {
        timeout: 30_000,
      })
      .toBe(editedTitle)

    await page.getByLabel("新步骤动作").fill(newStepAction)
    await page.getByLabel("新步骤预期").fill(newStepExpected)
    await page.getByRole("button", { name: "新增步骤" }).click()
    const stepRows = page.locator('[data-testid^="step-row-"]')
    await expect(stepRows).toHaveCount(3)
    await page.getByLabel("新断言表达式").fill(newAssertionExpression)
    await page.getByLabel("新断言预期").fill("P5 E2E 新增断言预期")
    await page.getByRole("button", { name: "新增断言" }).click()
    await expect(page.locator('[data-testid^="assertion-row-"]')).toHaveCount(2)

    // Move the appended step (seq 3) above the original second step.
    await page.getByRole("button", { name: "上移步骤 3" }).click()
    await expect
      .poll(
        async () => {
          const current = graphNode(await getMindmap(request, token, versionId), nodeId)
          return current.steps.map((step) => step.action).join("|")
        },
        { timeout: 30_000 },
      )
      .toBe(`打开目标页面|${newStepAction}|提交核心操作`)

    await page.reload()
    await expect(page.getByTestId("mindmap-canvas")).toBeVisible()
    await page.getByTestId(`case-node-${nodeId}`).click()
    await expect(page.getByTestId("case-editor")).toBeVisible()
    await expect(page.getByLabel("用例标题")).toHaveValue(editedTitle)
    await expect(page.getByLabel("优先级")).toHaveValue("P0")
    await expect(page.locator('[data-testid^="step-row-"]')).toHaveCount(3)
    await expect(page.getByLabel("步骤 2 动作")).toHaveValue(newStepAction)
    await expect(page.locator('[data-testid^="assertion-row-"]')).toHaveCount(2)

    const persisted = graphNode(await getMindmap(request, token, versionId), nodeId)
    expect(persisted.priority).toBe("P0")
    expect(persisted.steps.map((step) => step.action)).toEqual([
      "打开目标页面",
      newStepAction,
      "提交核心操作",
    ])
    expect(persisted.assertions.some((item) => item.expression === newAssertionExpression)).toBe(
      true,
    )
    await page.screenshot({
      path: screenshotPath("mindmap-03-edited.png"),
      fullPage: true,
    })
    return nodeId
  })

  await test.step("3b. deleting the last requirement source returns 409", async () => {
    const before = graphNode(await getMindmap(request, token, versionId), caseId)
    const linkedRequirementIds = new Set(
      before.sources
        .filter((source) => source.refKind === "requirement" && source.refId !== null)
        .map((source) => source.refId as string),
    )
    const linkedRequirementId = [...linkedRequirementIds][0]
    if (linkedRequirementId === undefined) {
      throw new Error("the edited case has no requirement source")
    }
    const unlinked = seed.requirements.find(
      (requirement) => !linkedRequirementIds.has(requirement.id),
    )
    if (unlinked === undefined) throw new Error("no unlinked requirement is left to add")

    // Add a second requirement source, so the UI believes two rows exist.
    await expect(page.getByTestId("source-panel")).toBeVisible()
    await page.getByLabel("选择需求").selectOption(unlinked.id)
    await page.getByRole("button", { name: "添加需求来源" }).click()
    await expect(page.locator('[data-testid^="source-row-"]')).toHaveCount(2)

    const withTwo = graphNode(await getMindmap(request, token, versionId), caseId)
    const requirementSources = withTwo.sources.filter((source) => source.refKind === "requirement")
    expect(requirementSources).toHaveLength(2)
    const originalSource = requirementSources.find((source) => source.refId === linkedRequirementId)
    const addedSource = requirementSources.find((source) => source.refId === unlinked.id)
    if (originalSource === undefined || addedSource === undefined) {
      throw new Error("the added requirement source is missing from the graph")
    }

    // Drop the added row through the API while the page still shows both rows;
    // deleting the remaining requirement source is the documented 409 path.
    const removed = await request.delete(
      `${API_URL}/api/versions/${versionId}/mindmap/sources/${addedSource.id}`,
      { headers: authHeaders(token) },
    )
    expect(removed.status(), "delete the extra requirement source").toBe(204)

    // The UI refreshes after the API delete and now correctly disables the
    // last remaining requirement source, so assert the guard instead of
    // clicking it; the server-side 409 is asserted directly below.
    await expect(page.getByRole("button", { name: `删除来源 ${originalSource.id}` })).toBeDisabled()
    await expect(page.getByTestId("source-panel")).toContainText("最后一条不可删除")

    const rejected = await request.delete(
      `${API_URL}/api/versions/${versionId}/mindmap/sources/${originalSource.id}`,
      { headers: authHeaders(token) },
    )
    expect(rejected.status(), "deleting the last requirement source").toBe(409)
    expect(await readErrorReason(rejected)).toBe("invalid_case_tree")
    await page.screenshot({
      path: screenshotPath("mindmap-04-last-source-409.png"),
      fullPage: true,
    })
  })

  // --- 4. review run -> findings -> blocker ack -> approve ----------------
  await test.step("4. review, acknowledge blocker and freeze the baseline", async () => {
    await page.getByTestId("btn-run-review").click()
    const findingRows = page.locator('[data-testid^="finding-row-"]')
    await expect(findingRows).toHaveCount(4, { timeout: 120_000 })
    await expect(page.getByTestId("findings-group-blocker")).toContainText("阻塞 blocker")
    await expect(page.getByTestId("btn-approve")).toBeDisabled()
    await page.screenshot({
      path: screenshotPath("mindmap-05-findings.png"),
      fullPage: true,
    })

    await page.getByRole("checkbox", { name: "已知悉 blocker 并继续" }).check()
    await expect(page.getByTestId("btn-approve")).toBeEnabled()
    await page.getByTestId("btn-approve").click()

    await expect(page.getByTestId("readonly-banner")).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId("baseline-badge")).toBeVisible()
    await expect(page.getByTestId("toolbar-baseline")).toBeVisible()
    await expect
      .poll(async () => (await getMindmap(request, token, versionId)).versionStatus, {
        timeout: 60_000,
      })
      .toBe("executing")
    await expect
      .poll(async () => (await getMindmap(request, token, versionId)).baseline !== null, {
        timeout: 60_000,
      })
      .toBe(true)
    await expect(page.getByTestId("stream-status")).toHaveAttribute(
      "data-status",
      /live|reconnecting/,
    )
    await page.screenshot({
      path: screenshotPath("mindmap-06-frozen.png"),
      fullPage: true,
    })
  })

  // --- 5. frozen graph ----------------------------------------------------
  await test.step("5. the frozen graph is read-only and rejects forced writes", async () => {
    await expect(page.getByTestId("btn-generate")).toBeDisabled()
    await expect(page.getByTestId("btn-export-markdown")).toBeEnabled()

    await page.getByTestId(`case-node-${caseId}`).click()
    await expect(page.getByTestId("case-editor")).toBeVisible()
    await expect(page.getByTestId("case-editor-readonly")).toBeVisible()
    await expect(page.getByLabel("用例标题")).toBeDisabled()
    await expect(page.getByLabel("优先级")).toBeDisabled()
    await expect(page.getByRole("button", { name: "保存用例" })).toHaveCount(0)
    await expect(page.getByLabel("新步骤动作")).toHaveCount(0)

    const frozenGraph = await getMindmap(request, token, versionId)
    expect(frozenGraph.editable).toBe(false)
    const frozenCase = graphNode(frozenGraph, caseId)
    const forced = await request.patch(
      `${API_URL}/api/versions/${versionId}/mindmap/nodes/${caseId}`,
      {
        headers: authHeaders(token),
        data: { title: "P5 E2E 冻结后强改", revision: frozenCase.revision },
      },
    )
    expect(forced.status(), "forced edit after freeze").toBe(409)
    expect(await readErrorReason(forced)).toBe("baseline_frozen")
    await page.screenshot({
      path: screenshotPath("mindmap-07-readonly.png"),
      fullPage: true,
    })
  })

  // --- 6. exports ---------------------------------------------------------
  await test.step("6. export Markdown/OPML/Mermaid/PNG", async () => {
    const markdown = await downloadFromButton(page, "btn-export-markdown")
    const markdownText = markdown.bytes.toString("utf8")
    expect(markdown.filename).toMatch(/\.md$/i)
    expect(markdownText).toContain(editedTitle)
    expect(markdownText).toContain(newStepAction)
    expect(markdownText).toContain(firstRequirement.title)
    expect(markdownText).toContain(secondRequirement.title)
    expect(markdownText).toContain("覆盖")

    const opml = await downloadFromButton(page, "btn-export-opml")
    const opmlText = opml.bytes.toString("utf8")
    expect(opml.filename).toMatch(/\.opml$/i)
    expect(opmlText).toContain("<opml")
    expect(opmlText).toContain('version="2.0"')
    expect(opmlText).toContain(editedTitle)
    expect(opmlText).toContain("_note")

    const mermaid = await downloadFromButton(page, "btn-export-mermaid")
    const mermaidText = mermaid.bytes.toString("utf8")
    expect(mermaid.filename).toMatch(/\.(mmd|mermaid)$/i)
    expect(mermaidText).toContain("mindmap")
    expect(mermaidText).toContain(editedTitle)

    const png = await downloadFromButton(page, "btn-export-png")
    expect(png.filename).toMatch(/-mindmap\.png$/i)
    expect(png.bytes.length).toBeGreaterThan(PNG_SIGNATURE.length)
    expect(png.bytes.subarray(0, PNG_SIGNATURE.length).equals(PNG_SIGNATURE)).toBe(true)
    await page.screenshot({
      path: screenshotPath("mindmap-08-exported.png"),
      fullPage: true,
    })
  })

  // --- 7. events + stream status -----------------------------------------
  await test.step("7. the version event snapshot carries the four case events", async () => {
    await expect
      .poll(
        async () => {
          const events = await readCaseEvents(request, token, versionId)
          const types = new Set(events.map((event) => event.type))
          return REQUIRED_CASE_EVENTS.every((type) => types.has(type))
        },
        { timeout: 60_000, intervals: [250, 500, 1000, 2000] },
      )
      .toBe(true)

    const events = await readCaseEvents(request, token, versionId)
    const created = events.filter((event) => event.type === "case.created")
    expect(created.length).toBeGreaterThan(0)
    for (const event of created) {
      const caseRefs = event.refs?.case ?? []
      expect(caseRefs.length).toBeGreaterThan(0)
      for (const ref of caseRefs) expect(ref).toMatch(/^\d+$/)
    }
    await expect(page.getByTestId("stream-status")).toHaveAttribute(
      "data-status",
      /live|reconnecting/,
    )
  })

  // --- 8. non-admin explore trigger --------------------------------------
  await test.step("8. a normal user has no explore trigger", async () => {
    // The admin session of this same spec does see the explore entry (it is
    // disabled only because the baseline froze the run panel); the normal user
    // below must not see it at all (spec §6.12/§10.5 step 8).
    await expect(page.getByTestId("btn-explore")).toBeVisible()
    await expect(page.getByTestId("btn-explore")).toBeDisabled()

    // Switch the browser session to the normal user (auth is a session token).
    await page.evaluate(() => sessionStorage.clear())
    await login(page, normalUsername, NORMAL_USER_PASSWORD)
    await page.goto(`/versions/${versionId}/mindmap`)
    await expect(page.getByTestId("mindmap-canvas")).toBeVisible()
    await expect(page.getByTestId("readonly-banner")).toBeVisible()
    await expect(page.getByTestId("btn-explore")).toHaveCount(0)
    await expect(page.getByTestId("run-panel").getByRole("button", { name: /探索/ })).toHaveCount(0)
    await page.screenshot({
      path: screenshotPath("mindmap-09-normal-user.png"),
      fullPage: true,
    })
  })
})

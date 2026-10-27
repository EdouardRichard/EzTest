import { beforeEach, describe, expect, expectTypeOf, it, vi } from "vitest"

import type {
  Analysis,
  Anchor,
  Candidate,
  ChangeList,
  CodeEntity,
  FileDiff,
  FileWindow,
  IndexRun,
  RepoSummary,
  TreeEntry,
} from "./types"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetPage: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiDelete: vi.fn(),
}))

vi.mock("@/lib/api-client", () => ({
  apiGet: mocks.apiGet,
  apiGetPage: mocks.apiGetPage,
  apiPost: mocks.apiPost,
  apiPatch: mocks.apiPatch,
  apiDelete: mocks.apiDelete,
}))

import {
  createAnchor,
  deleteAnchor,
  getAnalysis,
  getCodeRepos,
  getDiff,
  getFacts,
  getFile,
  getFileDiff,
  getLatestAnalysis,
  getLatestIndexRun,
  getTree,
  getVersionChanges,
  listAnchors,
  patchCandidate,
  searchFacts,
  startAnalysis,
  startIndex,
} from "./api"

const REPO_SUMMARY: RepoSummary = {
  id: 3,
  key: "web-main",
  name: "Web 主仓",
  status: "active",
  fixedBranch: "main",
  lastCommit: "abc",
  lastIndexedAt: "2026-10-28T01:02:03Z",
  index: {
    runId: "9001",
    commit: "abc",
    status: "done",
    startedAt: "2026-10-28T01:00:00Z",
    finishedAt: "2026-10-28T01:02:03Z",
    stats: { files: 1200, truncated: false },
  },
}

const CANDIDATE: Candidate = {
  id: "7002",
  subjectKind: "requirement",
  subjectId: "9001",
  reason: "changed api POST /api/refund",
  confidence: "high",
  source: "direct",
  evidence: [
    {
      file: "src/api/refund.ts",
      startLine: 80,
      endLine: 96,
      excerptHash: "sha256:abc",
    },
  ],
  selected: true,
}

const ANALYSIS: Analysis = {
  id: "1844",
  versionId: "1844",
  repoId: 3,
  baseCommit: "abc",
  headCommit: "def",
  status: "done",
  degraded: false,
  analysisVersion: 1,
  model: "deepseek-chat",
  promptVersion: "p4.1",
  changeSetId: "8001",
  changeSummary: "退款接口新增 reason 参数",
  stats: {
    changedFiles: 12,
    analyzedFiles: 10,
    cacheHits: 7,
    facts: 18,
    edges: 6,
    contracts: 2,
    evidenceChecks: 30,
    evidenceFailures: 1,
    unmapped: 2,
    invalidFacts: 0,
    duplicateFacts: 0,
    unmatchedContracts: 1,
    truncated: false,
  },
  factVersions: [{ factKey: "api:POST /api/refund", version: 3 }],
  candidates: [CANDIDATE],
  unmapped: [
    {
      path: "src/utils/format.ts",
      status: "M",
      reason: "no_fact_or_anchor",
      suggestion: "人工确认影响",
    },
  ],
  errors: [],
  createdAt: "2026-10-28T01:00:00Z",
  startedAt: "2026-10-28T01:00:01Z",
  finishedAt: "2026-10-28T01:01:00Z",
}

const ANCHOR: Anchor = {
  id: "6001",
  versionId: "1844",
  subjectKind: "requirement",
  subjectId: "9001",
  filePath: "src/api/refund.ts",
  confidence: "high",
  createdByKind: "user",
  createdBy: 11,
  createdAt: "2026-10-28T01:05:00Z",
}

const CHANGE_LIST: ChangeList = {
  changeSetId: "8001",
  baseCommit: "abc",
  headCommit: "def",
  stats: { files: 1, additions: 40, deletions: 2, truncated: false },
  files: [
    {
      path: "src/api/refund.ts",
      oldPath: null,
      status: "M",
      additions: 40,
      deletions: 2,
      binary: false,
      headBlobHash: "git-sha1:abc",
    },
  ],
  nextCursor: null,
  hasMore: false,
}

const FILE_WINDOW: FileWindow = {
  commit: "abc",
  path: "src/api/refund.ts",
  startLine: 1,
  endLine: 5,
  totalLines: 40,
  truncated: false,
  content: "export function refund() {}",
}

const FILE_DIFF: FileDiff = {
  baseCommit: "abc",
  headCommit: "def",
  path: "src/api/refund.ts",
  status: "M",
  binary: false,
  truncated: false,
  patch: "@@ -1 +1 @@",
}

const TREE_ENTRY: TreeEntry = {
  path: "src/router.ts",
  type: "blob",
  size: 120,
  gitObjectId: "git-sha1:abc",
  mode: "100644",
  unsupported: false,
}

const CODE_ENTITY: CodeEntity = {
  id: "5001",
  repoId: 3,
  commit: "abc",
  kind: "route",
  path: "src/router.ts",
  name: "/orders",
  signature: "",
  blobHash: "git-sha1:abc",
  source: "ai",
  confidence: "high",
  evidence: [{ file: "src/router.ts", startLine: 3, endLine: 5 }],
}

describe("code-intel repository api", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset()
    mocks.apiGetPage.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiPatch.mockReset()
    mocks.apiDelete.mockReset()
  })

  it("lists code repositories from the repo-level endpoint", async () => {
    mocks.apiGet.mockResolvedValueOnce([REPO_SUMMARY])

    await expect(getCodeRepos()).resolves.toEqual([REPO_SUMMARY])
    expect(mocks.apiGet).toHaveBeenCalledWith("/api/code/repos")
  })

  it("starts an index run and keeps the run id a string", async () => {
    const started = { runId: "9001", status: "queued", commit: "abc" }
    mocks.apiPost.mockResolvedValueOnce(started)

    await expect(startIndex(3)).resolves.toEqual(started)
    expect(mocks.apiPost).toHaveBeenCalledWith("/api/code/repos/3/index")
  })

  it("reads the latest index run", async () => {
    const run: IndexRun = {
      runId: "9001",
      repoId: 3,
      commit: "abc",
      status: "done",
      startedAt: "2026-10-28T01:00:00Z",
      finishedAt: "2026-10-28T01:02:03Z",
    }
    mocks.apiGet.mockResolvedValueOnce(run)

    await expect(getLatestIndexRun(3)).resolves.toEqual(run)
    expect(mocks.apiGet).toHaveBeenCalledWith("/api/code/repos/3/index-runs/latest")
  })

  it("pages the tree and unwraps entries with the cursor", async () => {
    mocks.apiGetPage.mockResolvedValueOnce({
      items: [TREE_ENTRY],
      nextCursor: "src/router.ts",
      hasMore: true,
    })

    await expect(
      getTree(3, { commit: "abc", path: "src", cursor: "src/api", limit: 50 }),
    ).resolves.toEqual({
      entries: [TREE_ENTRY],
      nextCursor: "src/router.ts",
      hasMore: true,
    })
    expect(mocks.apiGetPage).toHaveBeenCalledWith(
      "/api/code/repos/3/tree?commit=abc&path=src&cursor=src%2Fapi&limit=50",
    )
  })

  it("requests a file window with camelCase line params", async () => {
    mocks.apiGet.mockResolvedValueOnce(FILE_WINDOW)

    await getFile(3, {
      commit: "abc",
      path: "src/api/refund.ts",
      startLine: 1,
      endLine: 5,
    })
    expect(mocks.apiGet).toHaveBeenCalledWith(
      "/api/code/repos/3/file?commit=abc&path=src%2Fapi%2Frefund.ts&startLine=1&endLine=5",
    )
  })

  it("reads a repo-scoped change list page", async () => {
    mocks.apiGet.mockResolvedValueOnce(CHANGE_LIST)

    await expect(
      getDiff(3, { base: "abc", head: "def", cursor: "src/x", limit: 20 }),
    ).resolves.toEqual(CHANGE_LIST)
    expect(mocks.apiGet).toHaveBeenCalledWith(
      "/api/code/repos/3/diff?base=abc&head=def&cursor=src%2Fx&limit=20",
    )
  })

  it("reads one unified file diff", async () => {
    mocks.apiGet.mockResolvedValueOnce(FILE_DIFF)

    await getFileDiff(3, { base: "abc", head: "def", path: "src/api/refund.ts" })
    expect(mocks.apiGet).toHaveBeenCalledWith(
      "/api/code/repos/3/diff/file?base=abc&head=def&path=src%2Fapi%2Frefund.ts",
    )
  })

  it("pages fact entities", async () => {
    const page = { items: [CODE_ENTITY], nextCursor: "", hasMore: false }
    mocks.apiGetPage.mockResolvedValueOnce(page)

    await expect(
      getFacts(3, { commit: "abc", kind: "route", path: "src", cursor: "n", limit: 50 }),
    ).resolves.toEqual(page)
    expect(mocks.apiGetPage).toHaveBeenCalledWith(
      "/api/code/repos/3/facts?commit=abc&kind=route&path=src&cursor=n&limit=50",
    )
  })

  it("searches path and name substrings", async () => {
    mocks.apiGetPage.mockResolvedValueOnce({
      items: [CODE_ENTITY],
      nextCursor: "m",
      hasMore: true,
    })

    await searchFacts(3, {
      commit: "abc",
      q: "refund api",
      kind: "api",
      cursor: "m",
      limit: 10,
    })
    expect(mocks.apiGetPage).toHaveBeenCalledWith(
      "/api/code/repos/3/search?commit=abc&q=refund+api&kind=api&cursor=m&limit=10",
    )
  })

  it("omits empty filters from list requests", async () => {
    mocks.apiGetPage.mockResolvedValueOnce({ items: [], nextCursor: "", hasMore: false })
    mocks.apiGet.mockResolvedValueOnce([])

    await getFacts(3)
    expect(mocks.apiGetPage).toHaveBeenCalledWith("/api/code/repos/3/facts")

    await listAnchors("1844")
    expect(mocks.apiGet).toHaveBeenCalledWith("/api/versions/1844/code/anchors")
  })
})

describe("code-intel version api", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset()
    mocks.apiGetPage.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiPatch.mockReset()
    mocks.apiDelete.mockReset()
  })

  it("loads version changes without and with an explicit head", async () => {
    mocks.apiGet.mockResolvedValue(CHANGE_LIST)

    await expect(getVersionChanges("1844")).resolves.toEqual(CHANGE_LIST)
    expect(mocks.apiGet).toHaveBeenLastCalledWith("/api/versions/1844/code/changes")

    await getVersionChanges("1844", "def")
    expect(mocks.apiGet).toHaveBeenLastCalledWith("/api/versions/1844/code/changes?head=def")
  })

  it("serialises ids without Number() and hits the right paths", async () => {
    mocks.apiPost.mockResolvedValueOnce({
      id: "1844",
      status: "queued",
      analysisVersion: 1,
      reused: false,
    })

    await startAnalysis("1844", { headCommit: "def", force: false })
    expect(mocks.apiPost).toHaveBeenCalledWith("/api/versions/1844/code/analyses", {
      headCommit: "def",
      force: false,
    })

    mocks.apiPost.mockResolvedValueOnce(ANCHOR)
    const anchor = await createAnchor("1844", {
      subjectKind: "requirement",
      subjectId: "9001",
      filePath: "src/api/refund.ts",
    })
    expect(anchor).toBeDefined()
    expect(mocks.apiPost).toHaveBeenLastCalledWith("/api/versions/1844/code/anchors", {
      subjectKind: "requirement",
      subjectId: "9001",
      filePath: "src/api/refund.ts",
    })

    mocks.apiPatch.mockResolvedValueOnce(CANDIDATE)
    await patchCandidate("1844", "7001", "7002", true)
    expect(mocks.apiPatch).toHaveBeenCalledWith(
      "/api/versions/1844/code/analyses/7001/candidates/7002",
      { selected: true },
    )
  })

  it("loads the latest analysis and one analysis by id", async () => {
    mocks.apiGet.mockResolvedValue(ANALYSIS)

    await expect(getLatestAnalysis("1844")).resolves.toEqual(ANALYSIS)
    expect(mocks.apiGet).toHaveBeenLastCalledWith("/api/versions/1844/code/analyses/latest")

    await getAnalysis("1844", "7001")
    expect(mocks.apiGet).toHaveBeenLastCalledWith("/api/versions/1844/code/analyses/7001")
  })

  it("lists anchors filtered by subject kind and string subject id", async () => {
    mocks.apiGet.mockResolvedValueOnce([ANCHOR])

    await expect(
      listAnchors("1844", { subjectKind: "requirement", subjectId: "9001" }),
    ).resolves.toEqual([ANCHOR])
    expect(mocks.apiGet).toHaveBeenCalledWith(
      "/api/versions/1844/code/anchors?subjectKind=requirement&subjectId=9001",
    )
  })

  it("creates and deletes an anchor by encoded id", async () => {
    mocks.apiPost.mockResolvedValueOnce(ANCHOR)
    await createAnchor("1844", {
      subjectKind: "requirement",
      subjectId: "9001",
      filePath: "src/api/refund.ts",
      startLine: 10,
      endLine: 12,
    })
    expect(mocks.apiPost).toHaveBeenCalledWith("/api/versions/1844/code/anchors", {
      subjectKind: "requirement",
      subjectId: "9001",
      filePath: "src/api/refund.ts",
      startLine: 10,
      endLine: 12,
    })

    mocks.apiDelete.mockResolvedValueOnce(undefined)
    await deleteAnchor("1844", "6001")
    expect(mocks.apiDelete).toHaveBeenCalledWith("/api/versions/1844/code/anchors/6001")
  })

  it("encodes each string id path segment instead of coercing to a number", async () => {
    mocks.apiGet.mockResolvedValueOnce(ANALYSIS)
    await getAnalysis("version/1", "analysis 2")
    expect(mocks.apiGet).toHaveBeenCalledWith(
      "/api/versions/version%2F1/code/analyses/analysis%202",
    )

    mocks.apiDelete.mockResolvedValueOnce(undefined)
    await deleteAnchor("1844", "anchor/9")
    expect(mocks.apiDelete).toHaveBeenCalledWith("/api/versions/1844/code/anchors/anchor%2F9")
  })

  it("keeps repoId as number and codeintel ids as string", async () => {
    expectTypeOf<RepoSummary["id"]>().toEqualTypeOf<number>()
    expectTypeOf<IndexRun["runId"]>().toEqualTypeOf<string>()
    expectTypeOf<Analysis["id"]>().toEqualTypeOf<string>()
    expectTypeOf<Analysis["versionId"]>().toEqualTypeOf<string>()
    expectTypeOf<Analysis["repoId"]>().toEqualTypeOf<number>()
    expectTypeOf<Analysis["createdAt"]>().toEqualTypeOf<string>()
    expectTypeOf<Candidate["id"]>().toEqualTypeOf<string>()
    expectTypeOf<Candidate["subjectId"]>().toEqualTypeOf<string>()
    expectTypeOf<Anchor["id"]>().toEqualTypeOf<string>()
    expectTypeOf<Anchor["subjectId"]>().toEqualTypeOf<string>()
    expectTypeOf<Anchor["createdBy"]>().toEqualTypeOf<number>()
    expectTypeOf<ChangeList["changeSetId"]>().toEqualTypeOf<string | null>()

    mocks.apiGet.mockResolvedValueOnce(ANALYSIS)
    const analysis = await getAnalysis("1844", "7001")
    expect(typeof analysis.id).toBe("string")
    expect(typeof analysis.versionId).toBe("string")
    expect(typeof analysis.repoId).toBe("number")
  })
})

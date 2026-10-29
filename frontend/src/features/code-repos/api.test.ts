import { beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiDelete: vi.fn(),
}))

vi.mock("@/lib/api-client", () => ({
  apiGet: mocks.apiGet,
  apiPost: mocks.apiPost,
  apiPatch: mocks.apiPatch,
  apiDelete: mocks.apiDelete,
}))

import {
  createCodeRepo,
  deleteCodeRepo,
  listCodeRepos,
  updateCodeRepo,
  validateCodeRepo,
} from "./api"

const REPO = {
  id: 7,
  key: "eztest",
  name: "EzTest",
  localPath: "D:/repo",
  fixedBranch: "dev",
  includeGlobs: ["src/**"],
  excludeGlobs: [".git/**"],
  status: "active",
  lastCommit: "abc",
  lastValidatedAt: null,
  lastValidationError: "",
  lastIndexedAt: null,
}

describe("code-repos api", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiPatch.mockReset()
    mocks.apiDelete.mockReset()
  })

  it("lists repos from the resource endpoint with the maximum page size", async () => {
    mocks.apiGet.mockResolvedValueOnce([REPO])

    await expect(listCodeRepos()).resolves.toEqual({
      items: [REPO],
      nextCursor: null,
      hasMore: false,
    })
    expect(mocks.apiGet).toHaveBeenCalledWith("/api/resources/code-repos?limit=100")
  })

  it("treats a missing list payload as an empty page", async () => {
    mocks.apiGet.mockResolvedValueOnce(undefined)

    await expect(listCodeRepos()).resolves.toEqual({
      items: [],
      nextCursor: null,
      hasMore: false,
    })
  })

  it("creates a repo with the resource endpoint", async () => {
    mocks.apiPost.mockResolvedValueOnce(REPO)

    await expect(createCodeRepo({ key: "eztest", name: "EzTest" })).resolves.toEqual(REPO)
    expect(mocks.apiPost).toHaveBeenCalledWith("/api/resources/code-repos", {
      key: "eztest",
      name: "EzTest",
    })
  })

  it("updates a repo by id", async () => {
    mocks.apiPatch.mockResolvedValueOnce({ ...REPO, localPath: "D:/moved" })

    await updateCodeRepo(7, { localPath: "D:/moved" })
    expect(mocks.apiPatch).toHaveBeenCalledWith("/api/resources/code-repos/7", {
      localPath: "D:/moved",
    })
  })

  it("re-validates through the action endpoint", async () => {
    mocks.apiPost.mockResolvedValueOnce({ ...REPO, lastCommit: "def456" })

    await expect(validateCodeRepo(7)).resolves.toMatchObject({ lastCommit: "def456" })
    expect(mocks.apiPost).toHaveBeenCalledWith("/api/resources/code-repos/7/validate")
  })

  it("deletes a repo by id", async () => {
    mocks.apiDelete.mockResolvedValueOnce(undefined)

    await deleteCodeRepo(7)
    expect(mocks.apiDelete).toHaveBeenCalledWith("/api/resources/code-repos/7")
  })
})

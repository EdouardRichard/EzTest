import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { ChangedFiles } from "./ChangedFiles"
import type { ChangedFile, ChangeList } from "./types"

function changedFile(overrides: Partial<ChangedFile> = {}): ChangedFile {
  return {
    path: "src/api/refund.ts",
    oldPath: null,
    status: "M",
    additions: 10,
    deletions: 2,
    binary: false,
    headBlobHash: "git-sha1:1111111111111111111111111111111111111111",
    ...overrides,
  }
}

function changeList(files: ChangedFile[]): ChangeList {
  return {
    changeSetId: null,
    baseCommit: "0123456789abcdef0123456789abcdef01234567",
    headCommit: "89abcdef0123456789abcdef0123456789abcdef",
    stats: {
      files: files.length,
      additions: 10,
      deletions: 2,
      truncated: false,
    },
    files,
    nextCursor: null,
    hasMore: false,
  }
}

describe("ChangedFiles", () => {
  afterEach(() => {
    cleanup()
  })

  it("hides the requirement anchor action for deleted files only", () => {
    const onAnchorFile = vi.fn()
    const deleted = changedFile({
      path: "src/legacy/old.ts",
      status: "D",
      additions: 0,
      deletions: 40,
    })

    render(
      <ChangedFiles
        changes={changeList([changedFile(), deleted])}
        onOpenFile={vi.fn()}
        onAnchorFile={onAnchorFile}
      />,
    )

    expect(screen.getByRole("button", { name: "为 src/api/refund.ts 关联需求" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: "为 src/legacy/old.ts 关联需求" })).toBeNull()
  })
})

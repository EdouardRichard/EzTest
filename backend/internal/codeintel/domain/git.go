package codeintel

import "time"

// TreeEntry is one git ls-tree entry (spec §5.1). Unsupported marks symlink and
// submodule entries, which never enter T1 analysis.
type TreeEntry struct {
	Path        string
	Mode        string
	Type        string
	BlobSHA     string
	Size        int64
	Binary      bool
	Unsupported bool
}

// TreeResult is one bounded ls-tree page.
type TreeResult struct {
	Entries   []TreeEntry
	Total     int
	Truncated bool
}

// ChangedFile is one entry of a base..head diff. HeadBlobSHA is nil for
// deletions.
type ChangedFile struct {
	Path        string
	OldPath     string
	Status      string
	Additions   int
	Deletions   int
	Binary      bool
	HeadBlobSHA *string
}

// DiffResult is one bounded change set.
type DiffResult struct {
	Files     []ChangedFile
	Truncated bool
}

// FileWindow is one 1-based line window of a file at a commit.
type FileWindow struct {
	Path       string
	Commit     string
	Lines      []string
	StartLine  int
	EndLine    int
	TotalLines int
	Truncated  bool
}

// FileDiff is one on-demand file patch (spec §5.1).
type FileDiff struct {
	Path      string
	Patch     string
	Truncated bool
	Binary    bool
	Deleted   bool
}

// CommitInfo is one read-only git commit metadata row (no file content). It is
// returned by the additive CommitReader port used by the T0 commit view.
type CommitInfo struct {
	SHA         string
	Message     string
	AuthorName  string
	AuthoredAt  time.Time
	CommittedAt time.Time
	Parents     []string
}

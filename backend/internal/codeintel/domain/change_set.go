package domain

import "time"

// ChangedFile is one file-level entry of a change set (design spec §6.1). Status
// is one of A, M, D, R<score>, C<score> or T.
type ChangedFile struct {
	Path         string
	Status       string
	OldPath      *string
	Additions    int
	Deletions    int
	Binary       bool
	HeadBlobHash string
}

// CodeChangeSet is the deterministic file-level diff between two commits.
type CodeChangeSet struct {
	ID         int64
	RepoID     int64
	BaseCommit string
	HeadCommit string
	Stats      []byte // JSONB
	Files      []ChangedFile
	Truncated  bool
	CreatedAt  time.Time
}

// ChangeSetData is the read projection of a change set. ChangeSetID is nil for
// a read-only preview that never persisted the change set.
type ChangeSetData struct {
	ChangeSetID *int64
	Stats       []byte // JSONB
	Files       []ChangedFile
	Truncated   bool
}

// TreeEntry is one entry of a commit's file tree (design spec §6.1).
type TreeEntry struct {
	Path        string
	Type        string
	GitObjectID string
	Mode        string
	Size        int64
	Unsupported bool
}

// TreePage is one page of a commit's file tree.
type TreePage struct {
	Commit     string
	Entries    []TreeEntry
	NextCursor string
	HasMore    bool
}

// FileWindow is one line window of a file read at a commit.
type FileWindow struct {
	Commit      string
	Path        string
	StartLine   int
	EndLine     int
	TotalLines  int
	Truncated   bool
	Binary      bool
	Unsupported bool
	Content     string
}

// FileDiff is the on-demand unified diff of one changed file.
type FileDiff struct {
	BaseCommit string
	HeadCommit string
	Path       string
	Status     string
	Binary     bool
	Truncated  bool
	Patch      string
}

// CommitInfo is read-only commit metadata.
type CommitInfo struct {
	SHA         string
	Message     string
	Author      string
	CommittedAt time.Time
}

package domain

// EntityFilter selects code entities at a commit (design spec §7.2 facts and
// search endpoints). Limit and Cursor implement keyset pagination.
type EntityFilter struct {
	Commit string
	Kind   string
	Path   string
	Query  string
	Limit  int
	Cursor string
}

// AnchorFilter selects the anchors of one version. SubjectID nil means all
// subjects.
type AnchorFilter struct {
	SubjectKind string
	SubjectID   *int64
}

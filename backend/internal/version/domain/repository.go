package domain

import "context"

// Page is a cursor page request. Cursor is the opaque pagination cursor from the
// API; the repository decodes it to the last seen id.
type Page struct {
	Limit  int
	Cursor string
}

// VersionRepository is the persistence port for versions. UpdateMetadata,
// UpdateBindings and UpdateStatus are compare-and-swap operations: a missing
// row or a mismatched revision/status is reported as a revision or state
// conflict.
type VersionRepository interface {
	Create(ctx context.Context, v *Version) error
	Get(ctx context.Context, id int64) (*Version, error)
	// List returns one page ordered by updated_at DESC, id DESC and reports
	// whether another row exists after it.
	List(ctx context.Context, f VersionFilter) ([]Version, bool, error)
	Counts(ctx context.Context, versionID int64) (VersionCounts, error)
	UpdateMetadata(ctx context.Context, id int64, expected int, name, label, desc string, actor int64) (*Version, error)
	UpdateBindings(ctx context.Context, id int64, expected int, b VersionBindings, actor int64) (*Version, error)
	UpdateStatus(ctx context.Context, id int64, from, to string, actor int64) (*Version, error)
	// ListUnclarified returns ids of requirements that are not clarified plus
	// the number of version-level open questions.
	ListUnclarified(ctx context.Context, versionID int64) ([]int64, int, error)
}

// RequirementRepository is the persistence port for requirements, their
// relations and their attachment metadata. Multi-row writes (attachment
// metadata, cascaded deletes) happen in one repository transaction.
type RequirementRepository interface {
	Create(ctx context.Context, r *Requirement) error
	Get(ctx context.Context, id int64) (*Requirement, error)
	// List returns one page ordered by created_at, id ascending and reports
	// whether another row exists after it.
	List(ctx context.Context, versionID int64, status string, p Page) ([]Requirement, bool, error)
	Update(ctx context.Context, r *Requirement, expected int) error
	Delete(ctx context.Context, id int64) error
	UpdateStatus(ctx context.Context, id int64, from, to string, actor int64) (*Requirement, error)
	CountOpenQuestions(ctx context.Context, requirementID int64) (int, error)

	ListRelations(ctx context.Context, versionID int64) ([]Relation, error)
	CreateRelation(ctx context.Context, r *Relation) error
	UpdateRelation(ctx context.Context, id int64, relType, desc string) (*Relation, error)
	DeleteRelation(ctx context.Context, id int64) error

	CreateAttachment(ctx context.Context, a *Attachment) error
	ListAttachments(ctx context.Context, requirementID int64) ([]Attachment, error)
	GetAttachment(ctx context.Context, id int64) (*Attachment, error)
	DeleteAttachment(ctx context.Context, id int64) error
	// CountAttachments returns the attachment counts of one requirement and of
	// its whole version.
	CountAttachments(ctx context.Context, requirementID, versionID int64) (int, int, error)
}

// ClarificationRepository is the persistence port for clarification sessions,
// questions and decisions. Completing a session and accepting a decision linked
// to a question happen in one repository transaction.
type ClarificationRepository interface {
	CreateSession(ctx context.Context, s *ClarificationSession) error
	GetActiveSession(ctx context.Context, versionID int64) (*ClarificationSession, error)
	GetSession(ctx context.Context, id int64) (*ClarificationSession, error)
	CompleteSession(ctx context.Context, id int64, actor int64) (*ClarificationSession, error)
	UpdateContextSummary(ctx context.Context, versionID int64, summary string, actor int64) error

	CreateQuestion(ctx context.Context, q *Question) error
	GetQuestion(ctx context.Context, id int64) (*Question, error)
	ListQuestions(ctx context.Context, f QuestionFilter) ([]Question, bool, error)
	UpdateQuestion(ctx context.Context, q *Question, fromStatus string) error

	CreateDecision(ctx context.Context, d *Decision) error
	GetDecision(ctx context.Context, id int64) (*Decision, error)
	ListDecisions(ctx context.Context, f DecisionFilter) ([]Decision, bool, error)
	UpdateDecision(ctx context.Context, d *Decision, fromStatus string) error
}

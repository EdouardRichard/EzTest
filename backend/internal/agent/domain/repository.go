package domain

import "context"

// ProfileRepository is the persistence port for agent profiles.
type ProfileRepository interface {
	// List returns every profile ordered by ID ascending.
	List(ctx context.Context) ([]AgentProfile, error)
	// Get returns one profile or ErrNotFound.
	Get(ctx context.Context, id int64) (AgentProfile, error)
	// Update applies a partial update, validates the merged profile and
	// returns the stored row. It returns ErrNotFound for an unknown profile.
	Update(ctx context.Context, id int64, params UpdateParams) (AgentProfile, error)
	// EnsureBuiltin inserts profile when its key does not exist yet and never
	// overwrites an existing row. created reports whether it inserted.
	EnsureBuiltin(ctx context.Context, profile AgentProfile) (bool, error)
}

// SessionRepository is the persistence port for agent sessions.
type SessionRepository interface {
	// List returns up to limit sessions ordered by ID descending. A cursor > 0
	// starts after that ID. When profileID is non-nil only sessions of that
	// profile are returned. hasMore reports whether another row exists after
	// the returned page. Every session carries its ProfileKey and, when
	// present, its active (running) run and its newest terminal run.
	List(ctx context.Context, cursor int64, limit int, profileID *int64) ([]SessionSummary, bool, error)
	// Create inserts a session and returns the stored row with ProfileKey.
	Create(ctx context.Context, profileID int64, title string, createdBy *int64) (Session, error)
	// Get returns one session with ProfileKey or ErrNotFound.
	Get(ctx context.Context, id int64) (Session, error)
	// UpdateTitle changes the title and returns the stored row. It returns
	// ErrNotFound for an unknown session.
	UpdateTitle(ctx context.Context, id int64, title string) (Session, error)
	// Delete removes a session; messages and runs are removed by the schema's
	// cascades. It returns ErrNotFound for an unknown session.
	Delete(ctx context.Context, id int64) error
}

// RunRepository is the persistence port for agent runs.
type RunRepository interface {
	// CreateTurn atomically inserts a running run, its user message and
	// refreshes the session's title/activity in one statement. It returns
	// ErrRunActive when the session already has a running run and ErrNotFound
	// when the session does not exist.
	CreateTurn(ctx context.Context, sessionID int64, content []Part, titlePreview string) (TurnIDs, error)
	// Get returns one run or ErrNotFound.
	Get(ctx context.Context, id int64) (Run, error)
	// Active returns the session's running run or ErrNotFound.
	Active(ctx context.Context, sessionID int64) (Run, error)
	// Latest returns the session's newest terminal run (a run whose status is
	// not running) or ErrNotFound. A running run is returned by Active.
	Latest(ctx context.Context, sessionID int64) (Run, error)
	// UpdateProgress records the turns and accumulated token usage completed
	// so far. It returns ErrNotFound for an unknown run.
	UpdateProgress(ctx context.Context, runID int64, turns int, usage Usage) error
	// Finish writes the terminal fields and ended_at. It returns ErrNotFound
	// for an unknown run.
	Finish(ctx context.Context, runID int64, result RunResult) error
	// MarkRunningInterrupted marks every running run as interrupted (process
	// restart recovery) and returns how many runs were changed.
	MarkRunningInterrupted(ctx context.Context) (int64, error)
}

// MessageRepository is the persistence port for agent messages.
type MessageRepository interface {
	// ListBySession returns the session's messages ordered by ID ascending.
	ListBySession(ctx context.Context, sessionID int64) ([]Message, error)
	// Get returns one message or ErrNotFound.
	Get(ctx context.Context, id int64) (Message, error)
	// Append inserts a message and returns the stored row.
	Append(ctx context.Context, message Message) (Message, error)
	// AppendAssistantPlaceholder inserts the empty assistant message that
	// anchors one model turn's streamed parts and returns its ID.
	AppendAssistantPlaceholder(ctx context.Context, sessionID, runID int64) (int64, error)
	// CompleteAssistant replaces the placeholder's content, reasoning, tool
	// calls and usage. It returns ErrNotFound when the message is missing or
	// is not an assistant message.
	CompleteAssistant(ctx context.Context, messageID int64, parts []Part, reasoning string, calls []ToolCall, usage *Usage) error
}

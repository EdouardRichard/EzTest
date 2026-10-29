package application

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// Center session statuses. They are the derived status of a session's active or
// newest terminal run; "idle" means the session has no run yet (spec §8.1).
const (
	CenterStatusIdle        = "idle"
	CenterStatusRunning     = "running"
	CenterStatusDone        = "done"
	CenterStatusFailed      = "failed"
	CenterStatusAborted     = "aborted"
	CenterStatusInterrupted = "interrupted"
)

// Center session page bounds. The HTTP layer validates the public 1–100 window;
// the service re-applies it for direct callers.
const (
	CenterDefaultSessionLimit = 20
	CenterMaxSessionLimit     = 100
)

// ErrUnknownCenterRole and ErrUnknownCenterStatus are request-validation
// sentinels: the HTTP layer maps them to 422 with details.field, so a typo is
// never silently rendered as an empty page.
var (
	ErrUnknownCenterRole   = errors.New("agent-center: unknown role")
	ErrUnknownCenterStatus = errors.New("agent-center: unknown status")
)

// ProfileLister is the read-only profile lookup consumed by the centre. The
// application *ProfileService satisfies it; tests can substitute a fake.
type ProfileLister interface {
	List(ctx context.Context) ([]domain.AgentProfile, error)
}

// CenterEval is the latest evaluation summary the quality context projects into
// the Agent centre (spec §8.1/§9.1). Task 23 owns the eval tables; before it is
// wired the provider is nil and the API returns an explicit null.
type CenterEval struct {
	// RunID is the decimal string form of the eval_run id (D29): Snowflake ids
	// exceed JavaScript's safe-integer range, so the Agent centre must never
	// project them as JSON numbers.
	RunID      string             `json:"run_id"`
	Passed     bool               `json:"passed"`
	Metrics    map[string]float64 `json:"metrics"`
	Thresholds map[string]float64 `json:"thresholds"`
	CreatedAt  time.Time          `json:"created_at"`
}

// QualityReader is the optional quality metrics provider. A nil provider (Task
// 23 not wired yet) yields null metrics rather than invented numbers.
type QualityReader interface {
	LatestEvaluation(ctx context.Context) (*CenterEval, error)
}

// CenterRoleStats is one role's run statistics. SuccessRate is
// succeeded/(succeeded+failed) and is 0 when no terminal run exists; an empty
// history is therefore reported as zeros, never as 100% or demo data.
type CenterRoleStats struct {
	RunCount       int64   `json:"run_count"`
	SucceededCount int64   `json:"succeeded_count"`
	FailedCount    int64   `json:"failed_count"`
	SuccessRate    float64 `json:"success_rate"`
}

// CenterRoleView is one catalogue row. It deliberately has no SystemPrompt
// field: the read-only catalogue is served to every logged-in user and must
// never project a prompt, model credential or other secret.
type CenterRoleView struct {
	Order         int             `json:"order"`
	Role          string          `json:"role"`
	Name          string          `json:"name"`
	Phase         string          `json:"phase"`
	Mode          string          `json:"mode"`
	Note          string          `json:"note"`
	ProfileKey    *string         `json:"profile_key"`
	ProfileID     *int64          `json:"profile_id"`
	Enabled       bool            `json:"enabled"`
	Model         string          `json:"model"`
	PromptVersion *string         `json:"prompt_version"`
	AllowedTools  []string        `json:"allowed_tools"`
	Stats         CenterRoleStats `json:"stats"`
}

// CenterCatalog is the GET /api/agent-center/catalog body. Roles is always a
// non-nil slice so an empty read renders [] instead of null.
type CenterCatalog struct {
	Roles      []CenterRoleView `json:"roles"`
	LatestEval *CenterEval      `json:"latest_eval"`
}

// CenterQuality is the GET /api/agent-center/quality body. Latest is null until
// an evaluation run exists (Task 23).
type CenterQuality struct {
	Latest *CenterEval `json:"latest"`
}

// CenterSessionQuery is the run-observation request. Empty Role/Status mean
// "all"; Limit is bounded by the service.
type CenterSessionQuery struct {
	Role   string
	Status string
	Limit  int
}

// CenterSessionItem is one read-only run-observation row. It carries no
// transcript, prompt or message content, only the session identity and status
// needed to open the existing P1 console.
type CenterSessionItem struct {
	ID             int64     `json:"id"`
	Role           string    `json:"role"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	Running        bool      `json:"running"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// CenterSessionPage is the run-observation page body. Items is always non-nil.
type CenterSessionPage struct {
	Items []CenterSessionItem `json:"items"`
}

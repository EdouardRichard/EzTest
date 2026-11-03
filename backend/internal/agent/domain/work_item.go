package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// WorkItemStatus is the task-board lifecycle state. P1 writes open, claimed,
// running, done, failed and dead; waiting_human is reserved for P3+.
type WorkItemStatus string

// Work item statuses (7-state design; P1 does not write waiting_human).
const (
	WorkItemOpen         WorkItemStatus = "open"
	WorkItemClaimed      WorkItemStatus = "claimed"
	WorkItemRunning      WorkItemStatus = "running"
	WorkItemWaitingHuman WorkItemStatus = "waiting_human"
	WorkItemDone         WorkItemStatus = "done"
	WorkItemFailed       WorkItemStatus = "failed"
	WorkItemDead         WorkItemStatus = "dead"
)

// Valid reports whether the status is part of the frozen 7-state enum.
func (s WorkItemStatus) Valid() bool {
	switch s {
	case WorkItemOpen, WorkItemClaimed, WorkItemRunning, WorkItemWaitingHuman,
		WorkItemDone, WorkItemFailed, WorkItemDead:
		return true
	default:
		return false
	}
}

// Terminal reports whether the work item needs no further worker processing.
func (s WorkItemStatus) Terminal() bool {
	switch s {
	case WorkItemDone, WorkItemFailed, WorkItemDead:
		return true
	default:
		return false
	}
}

// Work item kinds introduced by the 0.2.1 contract (spec §5.4,
// task-types.md §9.1). The DB keeps them in work_item.kind; the frozen P1–P8
// work_item.type column continues to carry the legacy task types.
const (
	WorkItemKindExploreLogin     = "explore.login"
	WorkItemKindExplorePage      = "explore.page"
	WorkItemKindExploreAPI       = "explore.api"
	WorkItemKindExploreCoverage  = "explore.coverage"
	WorkItemKindExploreGap       = "explore.gap"
	WorkItemKindDesignCases      = "design.cases"
	WorkItemKindReviewCases      = "review.cases"
	WorkItemKindExecuteBatch     = "execute.batch"
	WorkItemKindAnalyzeFailure   = "analyze.failure"
	WorkItemKindReportVersion    = "report.version"
	WorkItemKindKnowledgePropose = "knowledge.propose"
)

// ValidWorkItemKind reports whether kind is one of the eleven 0.2.1 kinds.
func ValidWorkItemKind(kind string) bool {
	switch kind {
	case WorkItemKindExploreLogin, WorkItemKindExplorePage, WorkItemKindExploreAPI,
		WorkItemKindExploreCoverage, WorkItemKindExploreGap,
		WorkItemKindDesignCases, WorkItemKindReviewCases, WorkItemKindExecuteBatch,
		WorkItemKindAnalyzeFailure, WorkItemKindReportVersion, WorkItemKindKnowledgePropose:
		return true
	default:
		return false
	}
}

// ClaimDefaultsForKind returns the product team and frozen work type a 0.2.1
// contract item of this kind must carry so the agent runtime can claim and
// dispatch it (Task 65). The kinds whose executor is an agent run map to the
// team that owns the producing role; a kind outside the frozen eleven (including
// the legacy P1-P8 rows, which have no kind) reports false and keeps the
// compatibility default.
//
// The team is the frozen work_item.team column, not the 0.2.1 job owner: the
// agent worker claims by team, the codeintel worker claims TeamCode, and TeamOps
// stays reserved for deterministic jobs such as report.render_pdf.
func ClaimDefaultsForKind(kind string) (Team, string, bool) {
	switch kind {
	case WorkItemKindDesignCases, WorkItemKindReviewCases:
		return TeamDesign, kind, true
	case WorkItemKindExecuteBatch, WorkItemKindAnalyzeFailure:
		return TeamExecution, kind, true
	case WorkItemKindReportVersion:
		return TeamDelivery, kind, true
	case WorkItemKindExploreLogin, WorkItemKindExplorePage, WorkItemKindExploreAPI,
		WorkItemKindExploreCoverage, WorkItemKindExploreGap, WorkItemKindKnowledgePropose:
		return TeamClarification, kind, true
	default:
		return TeamOps, "", false
	}
}

// The four allowed work_item.created_by values (D21, task-types.md §9.3). Only
// the dispatch layer creates items; executors claim them.
const (
	WorkItemCreatedByStateMachine = "state_machine"
	WorkItemCreatedByPlanner      = "planner"
	WorkItemCreatedByAssistant    = "assistant"
	WorkItemCreatedByTrigger      = "trigger"
)

// ValidWorkItemCreatedBy reports whether value is one of the four creators.
func ValidWorkItemCreatedBy(value string) bool {
	switch value {
	case WorkItemCreatedByStateMachine, WorkItemCreatedByPlanner,
		WorkItemCreatedByAssistant, WorkItemCreatedByTrigger:
		return true
	default:
		return false
	}
}

// DefaultWorkItemLease is the lease ClaimWorkItem grants. It must be longer
// than one executor action (spec §5.7: lease > 75s); callers renew it
// independently with RenewLease while the item runs.
const DefaultWorkItemLease = 5 * time.Minute

// AttemptStatus is the outcome of one lease attempt.
type AttemptStatus string

// Per-attempt outcomes persisted in work_item_attempt.
const (
	AttemptClaimed   AttemptStatus = "claimed"
	AttemptRunning   AttemptStatus = "running"
	AttemptDone      AttemptStatus = "done"
	AttemptFailed    AttemptStatus = "failed"
	AttemptDead      AttemptStatus = "dead"
	AttemptRecovered AttemptStatus = "recovered"
)

// Valid reports whether the status is part of the attempt enum.
func (s AttemptStatus) Valid() bool {
	switch s {
	case AttemptClaimed, AttemptRunning, AttemptDone, AttemptFailed, AttemptDead, AttemptRecovered:
		return true
	default:
		return false
	}
}

// Attempt bounds for a single execution.
const (
	MinMaxAttempts     = 1
	MaxMaxAttempts     = 10
	DefaultMaxAttempts = 3
)

// WorkItem is one row of the single execution queue.
//
// Kind/Goal/Target/Inputs/Expected/Budget/CreatedBy/IdempotencyKey/Result are
// the 0.2.1 contract fields. They coexist with the frozen P1–P8 Type/Team/
// DedupeKey columns: new rows use Kind as the authoritative task kind while
// Type stays available for the legacy worker mapping. DependsOn is a nullable
// array; nil and an empty slice both mean "ready".
type WorkItem struct {
	ID             int64
	VersionID      int64
	Team           Team
	Type           string
	Kind           string
	Goal           string
	Target         json.RawMessage
	Inputs         json.RawMessage
	Expected       json.RawMessage
	Budget         json.RawMessage
	Status         WorkItemStatus
	Priority       int
	DedupeKey      string
	DependsOn      []int64
	Payload        json.RawMessage
	CreatedBy      string
	IdempotencyKey *string
	Result         json.RawMessage
	ClaimedBy      *string
	LeaseUntil     *time.Time
	LeaseEpoch     int64
	Attempts       int
	MaxAttempts    int
	LastError      *string
	AvailableAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewWorkItem is the idempotent Ensure input. Payload holds references and
// small data only (never full chat transcripts).
type NewWorkItem struct {
	VersionID   int64
	Team        string
	Type        string
	Priority    int
	DedupeKey   string
	Payload     any
	MaxAttempts int
}

// Validate checks the required fields, team enum and attempt bound.
func (in NewWorkItem) Validate() error {
	if in.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if !Team(in.Team).Valid() {
		return platform.Invalid("team", "must be one of clarification, design, execution, delivery, steward, code, ops")
	}
	if strings.TrimSpace(in.Type) == "" {
		return platform.Invalid("type", "must not be empty")
	}
	if strings.TrimSpace(in.DedupeKey) == "" {
		return platform.Invalid("dedupe_key", "must not be empty")
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = DefaultMaxAttempts
	}
	if maxAttempts < MinMaxAttempts || maxAttempts > MaxMaxAttempts {
		return platform.Invalid("max_attempts", "must be between 1 and 10")
	}
	if in.Payload != nil {
		raw, err := json.Marshal(in.Payload)
		if err != nil {
			return platform.Invalid("payload", "must be JSON-encodable")
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return platform.Invalid("payload", "must be a JSON object")
		}
	}
	return nil
}

// EncodePayload renders the payload as a JSON object, defaulting to {}.
func (in NewWorkItem) EncodePayload() (json.RawMessage, error) {
	if in.Payload == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, platform.Invalid("payload", "must be JSON-encodable")
	}
	return raw, nil
}

// CreateWorkItemParams is the 0.2.1 idempotent dispatch input. The repository
// fills the frozen P1–P8 columns with compatibility defaults: a frozen kind maps
// to the product team and work type that execute it (ClaimDefaultsForKind), so
// the agent worker can claim the item; any other row keeps Team=ops (the one
// team the legacy agent worker never claims), Type=Kind, an empty Payload and a
// new unique dedupe key when IdempotencyKey is NULL.
type CreateWorkItemParams struct {
	VersionID      int64
	Kind           string
	Goal           string
	Target         json.RawMessage
	Inputs         json.RawMessage
	Expected       json.RawMessage
	Budget         json.RawMessage
	DependsOn      []int64
	CreatedBy      string
	IdempotencyKey string

	// Legacy P1–P8 columns. Zero values select the defaults described above.
	Team        Team
	Type        string
	Priority    int
	MaxAttempts int
	Payload     json.RawMessage
}

// Normalize trims strings, applies contract defaults and removes duplicate
// dependency IDs. It never errors; Validate performs the checks.
func (in CreateWorkItemParams) Normalize() CreateWorkItemParams {
	in.Kind = strings.TrimSpace(in.Kind)
	in.Goal = strings.TrimSpace(in.Goal)
	in.CreatedBy = strings.TrimSpace(in.CreatedBy)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	in.Type = strings.TrimSpace(in.Type)
	if in.CreatedBy == "" {
		in.CreatedBy = WorkItemCreatedByStateMachine
	}
	// A frozen 0.2.1 kind defaults to the team/type that really executes it. An
	// explicit Team/Type (legacy compatibility or a controlled test) wins.
	if team, workType, ok := ClaimDefaultsForKind(in.Kind); ok {
		if in.Team == "" {
			in.Team = team
		}
		if in.Type == "" {
			in.Type = workType
		}
	}
	if in.Team == "" {
		in.Team = TeamOps
	}
	if in.Type == "" {
		in.Type = in.Kind
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = DefaultMaxAttempts
	}
	in.DependsOn = normalizeDependsOn(in.DependsOn)
	return in
}

// Validate checks the 0.2.1 contract fields. The repository calls Normalize
// first, so zero CreatedBy/Team/MaxAttempts are accepted here for convenience.
func (in CreateWorkItemParams) Validate() error {
	if in.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if !ValidWorkItemKind(in.Kind) {
		return platform.Invalid("kind", "must be one of explore.login, explore.page, explore.api, explore.coverage, explore.gap, design.cases, review.cases, execute.batch, analyze.failure, report.version, knowledge.propose")
	}
	if utf8.RuneCountInString(in.Goal) > 500 {
		return platform.Invalid("goal", "must be at most 500 characters")
	}
	if in.CreatedBy != "" && !ValidWorkItemCreatedBy(in.CreatedBy) {
		return platform.Invalid("created_by", "must be one of state_machine, planner, assistant, trigger")
	}
	if len(in.IdempotencyKey) > 200 {
		return platform.Invalid("idempotency_key", "must be at most 200 characters")
	}
	for _, dependency := range in.DependsOn {
		if dependency <= 0 {
			return platform.Invalid("depends_on", "must contain only positive decimal work item IDs")
		}
	}
	if in.Team != "" && !in.Team.Valid() {
		return platform.Invalid("team", "must be one of clarification, design, execution, delivery, steward, code, ops")
	}
	if in.MaxAttempts != 0 && (in.MaxAttempts < MinMaxAttempts || in.MaxAttempts > MaxMaxAttempts) {
		return platform.Invalid("max_attempts", "must be between 1 and 10")
	}
	objects := []struct {
		field string
		raw   json.RawMessage
	}{
		{"target", in.Target},
		{"inputs", in.Inputs},
		{"expected", in.Expected},
		{"budget", in.Budget},
		{"payload", in.Payload},
	}
	for _, object := range objects {
		if err := validateJSONObject(object.field, object.raw); err != nil {
			return err
		}
	}
	return nil
}

// EncodeObjects renders the optional JSON object fields, defaulting nil to {}.
func (in CreateWorkItemParams) EncodeObjects() (json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, error) {
	var target, inputs, expected, budget, payload json.RawMessage
	fields := []struct {
		name string
		raw  json.RawMessage
		out  *json.RawMessage
	}{
		{"target", in.Target, &target},
		{"inputs", in.Inputs, &inputs},
		{"expected", in.Expected, &expected},
		{"budget", in.Budget, &budget},
		{"payload", in.Payload, &payload},
	}
	for _, field := range fields {
		encoded, err := encodeJSONObject(field.name, field.raw)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		*field.out = encoded
	}
	return target, inputs, expected, budget, payload, nil
}

func normalizeDependsOn(ids []int64) []int64 {
	if ids == nil {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized
}

func validateJSONObject(field string, raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return platform.Invalid(field, "must be a JSON object")
	}
	return nil
}

func encodeJSONObject(field string, raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if err := validateJSONObject(field, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// WorkItemAttempt is one immutable attempt-log row for a work item.
type WorkItemAttempt struct {
	ID         int64
	WorkItemID int64
	Attempt    int
	Status     AttemptStatus
	Error      *string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Validate checks the attempt row identity and status.
func (a WorkItemAttempt) Validate() error {
	if a.WorkItemID <= 0 {
		return platform.Invalid("work_item_id", "must be a positive decimal ID")
	}
	if a.Attempt <= 0 {
		return platform.Invalid("attempt", "must be positive")
	}
	if !a.Status.Valid() {
		return platform.Invalid("status", "must be a valid attempt status")
	}
	return nil
}

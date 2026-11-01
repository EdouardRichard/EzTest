package agentapp

import (
	"context"
	"regexp"
	"sort"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P6 agent-center read limits (spec §4.3.4). Run lists use the shared
// before+limit cursor with a default of 20 and a hard cap of 100.
const (
	DefaultAgentRunLimit = 20
	MaxAgentRunLimit     = 100
)

// agentProfileKeyPattern is the profile key shape pinned by the OpenAPI
// ProfileKey parameter. It is validated in the application layer so a direct
// caller can never pass an unbounded filter to the read source.
var agentProfileKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// RunStats is the per-profile run aggregate of GET /api/agent-center/catalog
// and the global aggregate of GET /api/agent-center/quality. Running runs count
// in RunCount but not in the success-rate denominator.
type RunStats struct {
	RunCount         int
	DoneCount        int
	FailedCount      int
	AbortedCount     int
	InterruptedCount int
	LastRunAt        *time.Time
}

// SuccessRate is done/(done+failed); a zero denominator reports 0 and
// aborted/interrupted runs never enter the denominator (spec §4.3.4).
func (s RunStats) SuccessRate() float64 {
	denominator := s.DoneCount + s.FailedCount
	if denominator == 0 {
		return 0
	}
	return float64(s.DoneCount) / float64(denominator)
}

// CatalogProfileRow is one agent_profile row enriched with the current prompt
// revision metadata. The row deliberately carries no transcript.
type CatalogProfileRow struct {
	Profile         agent.AgentProfile
	PromptVersion   int
	PromptUpdatedAt *time.Time
}

// CatalogProfile is one CatalogProfile of the frozen P6 contract. It never
// carries system_prompt, allowed_tools contents or a transcript.
type CatalogProfile struct {
	ProfileKey        string
	Name              string
	Team              string
	Order             int
	Mode              string
	Model             *string
	Enabled           bool
	Budgets           agent.ProfileBudgets
	AllowedToolsCount int
	PromptVersion     int
	PromptUpdatedAt   *time.Time
	UpdatedAt         time.Time
	Stats             RunStats
}

// AgentTeam is one team grouping of the catalog. The code team is a
// deterministic worker team and therefore has no agent profile.
type AgentTeam struct {
	Team        string
	Label       string
	ProfileKeys []string
	Note        string
}

// Catalog is the GET /api/agent-center/catalog read model.
type Catalog struct {
	Profiles []CatalogProfile
	Teams    []AgentTeam
}

// RunFilter is the validated input of AgentCenterService.Runs.
type RunFilter struct {
	ProfileKey string
	Status     string
	VersionID  *int64
	BeforeID   *int64
	Limit      int
}

// RunQuery is the normalized query handed to the run read source.
type RunQuery struct {
	ProfileKey string
	Status     string
	VersionID  *int64
	BeforeID   *int64
	Limit      int
}

// RunRow is one agent_run row joined with its work item.
type RunRow struct {
	ID             int64
	ProfileKey     string
	WorkItemID     *int64
	WorkType       *string
	VersionID      *int64
	ConversationID *int64
	Status         string
	TerminalReason *string
	Turns          int
	Usage          map[string]int
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// RunView is one AgentRunView of the frozen contract. VersionTitle is resolved
// through the version application port and omitted when the version is gone.
type RunView struct {
	ID             int64
	ProfileKey     string
	WorkItemID     *int64
	WorkType       *string
	VersionID      *int64
	VersionTitle   string
	ConversationID *int64
	Status         string
	TerminalReason *string
	Turns          int
	Usage          map[string]int
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// EvalSummary is the compact latest-eval projection of
// GET /api/agent-center/quality (contract EvalSummary).
type EvalSummary struct {
	ID            int64
	Mode          string
	Status        string
	CreatedAt     time.Time
	MetricsCount  int
	MetricsPassed int
}

// QualityView is the GET /api/agent-center/quality read model.
type QualityView struct {
	Latest   *EvalSummary
	RunStats RunStats
}

// AgentCenterReader is the agent context's P6 read-only source for the Agent
// center. It never writes and never exposes prompts/transcripts. Implementations
// batch catalog rows and run stats so the service never issues N+1 reads.
type AgentCenterReader interface {
	ListCatalogProfiles(ctx context.Context) ([]CatalogProfileRow, error)
	// ProfileRunStats returns one aggregate keyed by profile_key.
	ProfileRunStats(ctx context.Context) (map[string]RunStats, error)
	// ListRuns returns at most query.Limit+1 rows so the application can
	// report has_more without a COUNT query.
	ListRuns(ctx context.Context, query RunQuery) ([]RunRow, bool, error)
}

// VersionTitleReader resolves version titles in one batch. It is the
// version.application QueryService surface; the agent context never imports the
// version context.
type VersionTitleReader interface {
	VersionTitles(ctx context.Context, ids []int64) (map[int64]string, error)
}

// LatestEvalReader is the narrow quality-context port consumed by the agent
// center quality projection. The quality context or the assembly root adapts
// its latest eval_run read to this interface.
type LatestEvalReader interface {
	LatestEval(ctx context.Context) (*EvalSummary, error)
}

// AgentCenterService implements the P6 logged-in read-only Agent center.
type AgentCenterService struct {
	reader   AgentCenterReader
	versions VersionTitleReader
	evals    LatestEvalReader
}

// NewAgentCenterService wires the Agent center read service. versions and evals
// are optional ports: without them version titles are omitted and the latest
// eval is null, keeping an unwired deployment readable.
func NewAgentCenterService(reader AgentCenterReader, versions VersionTitleReader, evals LatestEvalReader) *AgentCenterService {
	return &AgentCenterService{reader: reader, versions: versions, evals: evals}
}

func (s *AgentCenterService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("agent center reader is not configured")
	}
	return nil
}

// Catalog returns the 14 fixed profiles grouped by team with their run stats
// and current prompt version. It never reads or returns the prompt body.
func (s *AgentCenterService) Catalog(ctx context.Context) (Catalog, error) {
	if err := s.requireReader(); err != nil {
		return Catalog{}, err
	}
	rows, err := s.reader.ListCatalogProfiles(ctx)
	if err != nil {
		return Catalog{}, err
	}
	stats, err := s.reader.ProfileRunStats(ctx)
	if err != nil {
		return Catalog{}, err
	}

	profiles := make([]CatalogProfile, 0, len(rows))
	grouped := make(map[string][]string, len(agentCenterTeams))
	for _, row := range rows {
		team, order, ok := catalogTeamForProfile(row.Profile.Key)
		if !ok {
			// Only the 14 seeded profiles are part of the frozen team map.
			continue
		}
		promptVersion := row.PromptVersion
		if promptVersion < 1 {
			// The startup backfill seeds v1; this guard keeps the view valid
			// for a database whose backfill has not run yet.
			promptVersion = 1
		}
		profile := CatalogProfile{
			ProfileKey:        row.Profile.Key,
			Name:              row.Profile.Name,
			Team:              team,
			Order:             order,
			Mode:              "agent",
			Model:             row.Profile.Model,
			Enabled:           row.Profile.Enabled,
			Budgets:           row.Profile.Budgets,
			AllowedToolsCount: len(row.Profile.AllowedTools),
			PromptVersion:     promptVersion,
			PromptUpdatedAt:   row.PromptUpdatedAt,
			UpdatedAt:         row.Profile.UpdatedAt,
			Stats:             stats[row.Profile.Key],
		}
		profiles = append(profiles, profile)
		grouped[team] = append(grouped[team], profile.ProfileKey)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Order < profiles[j].Order })

	teams := make([]AgentTeam, 0, len(agentCenterTeams))
	for _, definition := range agentCenterTeams {
		keys := grouped[definition.team]
		if keys == nil {
			keys = []string{}
		}
		teams = append(teams, AgentTeam{
			Team:        definition.team,
			Label:       definition.label,
			ProfileKeys: keys,
			Note:        definition.note,
		})
	}
	return Catalog{Profiles: profiles, Teams: teams}, nil
}

// Runs returns one AgentRunView page ordered by id DESC. Status and filter
// validation stays in the application layer so direct callers are protected.
func (s *AgentCenterService) Runs(ctx context.Context, filter RunFilter) ([]RunView, bool, error) {
	query, err := normalizeRunFilter(filter)
	if err != nil {
		return nil, false, err
	}
	if err := s.requireReader(); err != nil {
		return nil, false, err
	}
	rows, hasMore, err := s.reader.ListRuns(ctx, query)
	if err != nil {
		return nil, false, err
	}

	versionIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.VersionID != nil && *row.VersionID > 0 {
			versionIDs = append(versionIDs, *row.VersionID)
		}
	}
	titles := map[int64]string{}
	if s.versions != nil && len(versionIDs) > 0 {
		titles, err = s.versions.VersionTitles(ctx, versionIDs)
		if err != nil {
			return nil, false, err
		}
	}

	views := make([]RunView, 0, len(rows))
	for _, row := range rows {
		view := RunView{
			ID:             row.ID,
			ProfileKey:     row.ProfileKey,
			WorkItemID:     row.WorkItemID,
			WorkType:       row.WorkType,
			VersionID:      row.VersionID,
			ConversationID: row.ConversationID,
			Status:         row.Status,
			TerminalReason: row.TerminalReason,
			Turns:          row.Turns,
			Usage:          row.Usage,
			StartedAt:      row.StartedAt,
			FinishedAt:     row.FinishedAt,
		}
		if row.VersionID != nil {
			view.VersionTitle = titles[*row.VersionID]
		}
		views = append(views, view)
	}
	return views, hasMore, nil
}

// Quality returns the latest eval summary plus the global run aggregate. With
// no evaluation yet the latest block is null (never a fabricated summary).
func (s *AgentCenterService) Quality(ctx context.Context) (QualityView, error) {
	if err := s.requireReader(); err != nil {
		return QualityView{}, err
	}
	perProfile, err := s.reader.ProfileRunStats(ctx)
	if err != nil {
		return QualityView{}, err
	}
	total := RunStats{}
	for _, stats := range perProfile {
		total.RunCount += stats.RunCount
		total.DoneCount += stats.DoneCount
		total.FailedCount += stats.FailedCount
		total.AbortedCount += stats.AbortedCount
		total.InterruptedCount += stats.InterruptedCount
		if stats.LastRunAt != nil && (total.LastRunAt == nil || stats.LastRunAt.After(*total.LastRunAt)) {
			total.LastRunAt = stats.LastRunAt
		}
	}

	var latest *EvalSummary
	if s.evals != nil {
		latest, err = s.evals.LatestEval(ctx)
		if err != nil {
			return QualityView{}, err
		}
	}
	return QualityView{Latest: latest, RunStats: total}, nil
}

func normalizeRunFilter(filter RunFilter) (RunQuery, error) {
	profileKey := filter.ProfileKey
	if profileKey != "" && !agentProfileKeyPattern.MatchString(profileKey) {
		return RunQuery{}, platform.Invalid("profile_key", "must match ^[a-z][a-z0-9_]{0,63}$")
	}
	status := filter.Status
	if status != "" && !agent.AgentRunStatus(status).Valid() {
		return RunQuery{}, platform.Invalid("status", "must be one of running, done, failed, aborted, interrupted")
	}
	if filter.VersionID != nil && *filter.VersionID <= 0 {
		return RunQuery{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if filter.BeforeID != nil && *filter.BeforeID <= 0 {
		return RunQuery{}, platform.Invalid("before", "must be a positive decimal ID")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultAgentRunLimit
	}
	if limit > MaxAgentRunLimit {
		limit = MaxAgentRunLimit
	}
	return RunQuery{
		ProfileKey: profileKey,
		Status:     status,
		VersionID:  filter.VersionID,
		BeforeID:   filter.BeforeID,
		Limit:      limit,
	}, nil
}

/* --------------------------------- teams -------------------------------- */

type agentCenterTeamDefinition struct {
	team  string
	label string
	note  string
	keys  []string
}

// agentCenterTeams is the frozen team map of the 14 profiles (spec §8.1). The
// code team owns deterministic workers and deliberately has no profile.
var agentCenterTeams = []agentCenterTeamDefinition{
	{
		team:  "clarification",
		label: "澄清",
		keys:  []string{ProfileKeyClarificationHost, ProfileKeyRequirementCurator, ProfileKeyContextScout},
	},
	{
		team:  "design",
		label: "设计",
		keys:  []string{ProfileKeyDesignHost, ProfileKeyDesignPlanner, ProfileKeyCaseAuthor, ProfileKeyDesignCritic, ProfileKeyDataPlanner},
	},
	{
		team:  "execution",
		label: "执行",
		keys:  []string{ProfileKeyCaseExecutor, ProfileKeyFailureAnalyst, ProfileKeyCoverageAnalyst},
	},
	{
		team:  "delivery",
		label: "交付",
		keys:  []string{ProfileKeyIssueTriager, ProfileKeyReportWriter},
	},
	{
		team:  "steward",
		label: "版本管家",
		keys:  []string{ProfileKeyVersionSteward},
	},
	{
		team:  "code",
		label: "代码",
		note:  "确定性 worker，无 agent profile",
	},
}

// catalogTeamForProfile maps a seeded profile key to its team and 1-based catalog
// order. Unknown keys are not part of the frozen catalog.
func catalogTeamForProfile(profileKey string) (string, int, bool) {
	order := 0
	for _, definition := range agentCenterTeams {
		for _, key := range definition.keys {
			order++
			if key == profileKey {
				return definition.team, order, true
			}
		}
	}
	return "", 0, false
}

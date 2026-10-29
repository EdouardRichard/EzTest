package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// CenterServiceDeps wires the read-only Agent centre. Profiles is required;
// Reads is required for the catalogue statistics and run observation; Quality
// is optional until Task 23 wires the evaluation context.
type CenterServiceDeps struct {
	Profiles ProfileLister
	Reads    domain.CenterReadRepository
	Quality  QualityReader
}

// CenterService is the read-only P8 Agent centre use case: the 13-role
// catalogue, the run observation and the latest evaluation summary. It never
// mutates profiles, sessions or runs, and it never projects prompts or
// credentials (spec §8.1/D60).
type CenterService struct {
	profiles ProfileLister
	reads    domain.CenterReadRepository
	quality  QualityReader
}

// NewCenterService returns the Agent centre read service.
func NewCenterService(deps CenterServiceDeps) *CenterService {
	return &CenterService{profiles: deps.Profiles, reads: deps.Reads, quality: deps.Quality}
}

// Catalog assembles the 13-role directory. A missing profile row keeps its
// logical role visible with enabled=false and a nil profile id rather than
// dropping the role or inventing configuration. Statistics failures are
// authoritative (no fabricated zeros); a quality failure only degrades
// latest_eval to null.
func (s *CenterService) Catalog(ctx context.Context) (CenterCatalog, error) {
	if s == nil || s.profiles == nil {
		return CenterCatalog{}, errors.New("agent-center: profile source is not configured")
	}
	if s.reads == nil {
		return CenterCatalog{}, errors.New("agent-center: run-statistics source is not configured")
	}

	profiles, err := s.profiles.List(ctx)
	if err != nil {
		return CenterCatalog{}, fmt.Errorf("agent-center: list profiles: %w", err)
	}
	stats, err := s.reads.ProfileRunStats(ctx)
	if err != nil {
		return CenterCatalog{}, fmt.Errorf("agent-center: load run statistics: %w", err)
	}

	byKey := make(map[string]domain.AgentProfile, len(profiles))
	for _, profile := range profiles {
		byKey[profile.Key] = profile
	}
	statsByKey := make(map[string]domain.ProfileRunStats, len(stats))
	for _, row := range stats {
		statsByKey[row.ProfileKey] = row
	}

	roles := CenterRoles()
	catalog := CenterCatalog{
		Roles:      make([]CenterRoleView, 0, len(roles)),
		LatestEval: s.latestEvaluation(ctx),
	}
	for _, role := range roles {
		view := CenterRoleView{
			Order:        role.Order,
			Role:         role.Role,
			Name:         role.Name,
			Phase:        role.Phase,
			Mode:         role.Mode,
			Note:         role.Note,
			AllowedTools: []string{},
			Stats:        centerRoleStats(statsByKey[role.ProfileKey]),
		}
		if role.ProfileKey == "" {
			// Report generation and knowledge curation have no independent
			// profile (spec §8.1); they are labelled deterministic/manual
			// rather than given a fabricated profile id or prompt.
			catalog.Roles = append(catalog.Roles, view)
			continue
		}

		profileKey := role.ProfileKey
		view.ProfileKey = &profileKey
		if profile, ok := byKey[role.ProfileKey]; ok {
			profileID := profile.ID
			view.ProfileID = &profileID
			view.Enabled = profile.Enabled
			view.Model = profile.Model
			view.AllowedTools = append([]string{}, profile.AllowedTools...)
		}
		// A stored profile has no prompt-version column: the version is
		// recorded on runs by the owning context. Never invent one here.
		catalog.Roles = append(catalog.Roles, view)
	}
	return catalog, nil
}

// Sessions returns one read-only page of recent sessions. role filters by the
// logical role (profile key); an empty role means every role. A profile-less
// role can never have sessions, so it short-circuits to an empty page. Unknown
// role/status values are request errors so a typo is not mistaken for "nothing
// ran".
func (s *CenterService) Sessions(ctx context.Context, query CenterSessionQuery) (CenterSessionPage, error) {
	status := strings.TrimSpace(query.Status)
	if status != "" && !validCenterStatus(status) {
		return CenterSessionPage{}, ErrUnknownCenterStatus
	}

	roleKey := strings.TrimSpace(query.Role)
	if roleKey != "" {
		role, ok := centerRoleByKey(roleKey)
		if !ok {
			return CenterSessionPage{}, ErrUnknownCenterRole
		}
		if role.ProfileKey == "" {
			// Deterministic/manual roles own no agent session; do not query and
			// do not fake a row.
			return CenterSessionPage{Items: []CenterSessionItem{}}, nil
		}
		roleKey = role.ProfileKey
	}
	if s == nil || s.reads == nil {
		return CenterSessionPage{}, errors.New("agent-center: run-observation source is not configured")
	}

	limit := query.Limit
	if limit <= 0 {
		limit = CenterDefaultSessionLimit
	}
	if limit > CenterMaxSessionLimit {
		limit = CenterMaxSessionLimit
	}

	rows, err := s.reads.CenterSessions(ctx, domain.CenterSessionFilter{
		RoleKey: roleKey,
		Status:  status,
		Limit:   limit,
	})
	if err != nil {
		return CenterSessionPage{}, fmt.Errorf("agent-center: load recent sessions: %w", err)
	}
	page := CenterSessionPage{Items: make([]CenterSessionItem, 0, len(rows))}
	for _, row := range rows {
		page.Items = append(page.Items, CenterSessionItem{
			ID:             row.ID,
			Role:           row.ProfileKey,
			Title:          row.Title,
			Status:         row.Status,
			Running:        row.Running,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
			LastActivityAt: row.LastActivityAt,
		})
	}
	return page, nil
}

// Quality returns the latest evaluation summary, or an explicit null when the
// quality context is not wired (Task 23) or has not run an evaluation yet.
func (s *CenterService) Quality(ctx context.Context) (CenterQuality, error) {
	if s == nil || s.quality == nil {
		return CenterQuality{}, nil
	}
	latest, err := s.quality.LatestEvaluation(ctx)
	if err != nil {
		return CenterQuality{}, fmt.Errorf("agent-center: load latest evaluation: %w", err)
	}
	return CenterQuality{Latest: latest}, nil
}

// latestEvaluation best-effort loads the quality summary for the catalogue. A
// metrics outage must not hide the role directory, so the error is logged and
// the catalogue reports latest_eval=null.
func (s *CenterService) latestEvaluation(ctx context.Context) *CenterEval {
	if s == nil || s.quality == nil {
		return nil
	}
	latest, err := s.quality.LatestEvaluation(ctx)
	if err != nil {
		slog.WarnContext(ctx, "agent-center: latest evaluation unavailable", "error", err)
		return nil
	}
	return latest
}

// centerRoleStats maps one repository row to the public statistics. An absent
// row (or no terminal runs) is all zeros; success_rate is the ratio over
// terminal runs only and is 0 when nothing has finished.
func centerRoleStats(row domain.ProfileRunStats) CenterRoleStats {
	stats := CenterRoleStats{
		RunCount:       row.RunCount,
		SucceededCount: row.SucceededCount,
		FailedCount:    row.FailedCount,
	}
	terminal := row.SucceededCount + row.FailedCount
	if terminal > 0 {
		stats.SuccessRate = float64(row.SucceededCount) / float64(terminal)
	}
	return stats
}

// validCenterStatus reports whether status is one of the public run-observation
// states.
func validCenterStatus(status string) bool {
	switch status {
	case CenterStatusIdle,
		CenterStatusRunning,
		CenterStatusDone,
		CenterStatusFailed,
		CenterStatusAborted,
		CenterStatusInterrupted:
		return true
	default:
		return false
	}
}

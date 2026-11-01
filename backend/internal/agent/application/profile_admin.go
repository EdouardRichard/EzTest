package agentapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ActionProfileUpdated is the single audit action of every profile config or
// prompt write (spec §8.5). Metadata never contains the prompt body.
const ActionProfileUpdated = "agent.profile.updated"

// PromptRevision is one append-only prompt revision of the admin API.
type PromptRevision = agent.PromptRevision

// ProfileView is the admin ProfileView of the frozen contract (includes the
// current prompt body; this projection is never exposed to normal users).
type ProfileView struct {
	ID            int64
	Key           string
	Name          string
	SystemPrompt  string
	AllowedTools  []string
	Model         *string
	Budgets       agent.ProfileBudgets
	Enabled       bool
	PromptVersion int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ProfileDetail is the admin profile detail with its built-in default prompt,
// variable catalog and current prompt version.
type ProfileDetail struct {
	Profile             ProfileView
	DefaultSystemPrompt string
	Variables           []PromptVariable
	PromptVersion       int
}

// PatchInput is one validated profile PATCH. SetModel distinguishes an absent
// model field from an explicit null/empty (clear to default).
type PatchInput struct {
	SetModel     bool
	Model        *string
	Budgets      *agent.ProfileBudgets
	AllowedTools *[]string
}

// SavePromptInput is one PUT prompt request.
type SavePromptInput struct {
	SystemPrompt    string
	Note            string
	ExpectedVersion *int
}

// AgentToolView is one read-only tool-catalog entry (contract AgentToolView).
type AgentToolView struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolRegistry is the P5 tool registry surface. *tools.Registry satisfies it.
type ToolRegistry interface {
	Names() []string
	Get(name string) (tools.Tool, bool)
}

// ModelCatalog is the default LLM model catalog surface. *llmapp.Service
// satisfies it: a non-empty profile model must resolve to a catalog model.
type ModelCatalog interface {
	ResolveModel(ctx context.Context, model string) (llmapp.ModelInfo, error)
}

// ProfileAdminRepository is the persistence port of the admin API. The
// concrete agentpg.RuntimeRepository implements it.
type ProfileAdminRepository interface {
	ListProfiles(ctx context.Context) ([]agent.AgentProfile, error)
	GetProfile(ctx context.Context, key string) (agent.AgentProfile, error)
	UpdateProfile(ctx context.Context, key string, update agent.ProfileUpdate) (agent.AgentProfile, bool, error)
	LatestPromptVersions(ctx context.Context) (map[string]agent.PromptVersionInfo, error)
	SavePromptRevision(ctx context.Context, write agent.PromptWrite) (agent.PromptWriteResult, error)
	GetPromptRevision(ctx context.Context, key string, version int) (agent.PromptRevision, error)
	ListPromptRevisions(ctx context.Context, key string, limit int, beforeVersion *int) ([]agent.PromptRevision, bool, error)
}

// ProfileAdminService implements the P6 admin profile and prompt use cases.
type ProfileAdminService struct {
	repo   ProfileAdminRepository
	tools  ToolRegistry
	models ModelCatalog
	audit  auditapp.Recorder
}

// NewProfileAdminService wires the admin profile service. tools, models and
// audit may be nil: a nil registry/model catalog reports 503 for the writes
// that need it, and a nil audit sink keeps writes successful.
func NewProfileAdminService(
	repo ProfileAdminRepository,
	registry ToolRegistry,
	models ModelCatalog,
	audit auditapp.Recorder,
) *ProfileAdminService {
	return &ProfileAdminService{repo: repo, tools: registry, models: models, audit: audit}
}

// List returns every profile (prompt included) ordered by the frozen seed
// order. Admin-only route.
func (s *ProfileAdminService) List(ctx context.Context) ([]ProfileView, error) {
	if s == nil || s.repo == nil {
		return nil, platform.Unavailable("agent profile repository is not configured")
	}
	profiles, err := s.repo.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	versions, err := s.repo.LatestPromptVersions(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]ProfileView, 0, len(profiles))
	for _, profile := range profiles {
		views = append(views, toProfileView(profile, versions[profile.Key]))
	}
	sort.SliceStable(views, func(i, j int) bool {
		left, right := profileSeedOrder(views[i].Key), profileSeedOrder(views[j].Key)
		if left != right {
			return left < right
		}
		return views[i].Key < views[j].Key
	})
	return views, nil
}

// Get returns one profile with the built-in default prompt and variable
// catalog.
func (s *ProfileAdminService) Get(ctx context.Context, key string) (ProfileDetail, error) {
	if err := validateProfileKey(key); err != nil {
		return ProfileDetail{}, err
	}
	if s == nil || s.repo == nil {
		return ProfileDetail{}, platform.Unavailable("agent profile repository is not configured")
	}
	profile, err := s.repo.GetProfile(ctx, key)
	if err != nil {
		return ProfileDetail{}, profileRepoError(err)
	}
	versions, err := s.repo.LatestPromptVersions(ctx)
	if err != nil {
		return ProfileDetail{}, err
	}
	view := toProfileView(profile, versions[key])
	defaultPrompt, ok := DefaultPrompt(key)
	if !ok {
		defaultPrompt = profile.SystemPrompt
	}
	return ProfileDetail{
		Profile:             view,
		DefaultSystemPrompt: defaultPrompt,
		Variables:           PromptVariables(key),
		PromptVersion:       view.PromptVersion,
	}, nil
}

// Patch updates model/budgets/allowed_tools only. enabled/name/key and
// system_prompt are not writable here. The next run observes the new values.
func (s *ProfileAdminService) Patch(ctx context.Context, actor platform.Actor, key string, input PatchInput) (ProfileView, bool, error) {
	if err := requireAdmin(actor); err != nil {
		return ProfileView{}, false, err
	}
	if err := validateProfileKey(key); err != nil {
		return ProfileView{}, false, err
	}
	if s == nil || s.repo == nil {
		return ProfileView{}, false, platform.Unavailable("agent profile repository is not configured")
	}
	profile, err := s.repo.GetProfile(ctx, key)
	if err != nil {
		return ProfileView{}, false, profileRepoError(err)
	}

	update := agent.ProfileUpdate{}
	fields := make([]string, 0, 3)
	if input.SetModel {
		var next *string
		if input.Model != nil {
			trimmed := strings.TrimSpace(*input.Model)
			if trimmed != "" {
				next = &trimmed
			}
		}
		if next != nil {
			if err := s.validateModel(ctx, *next); err != nil {
				return ProfileView{}, false, err
			}
		}
		update.SetModel = true
		update.Model = next
		fields = append(fields, "model")
	}
	if input.Budgets != nil {
		if err := validateProfileBudgets(*input.Budgets); err != nil {
			return ProfileView{}, false, err
		}
		update.Budgets = input.Budgets
		fields = append(fields, "budgets")
	}
	if input.AllowedTools != nil {
		allowed, err := s.validateAllowedTools(*input.AllowedTools)
		if err != nil {
			return ProfileView{}, false, err
		}
		update.AllowedTools = &allowed
		fields = append(fields, "allowed_tools")
	}

	if update.Empty() {
		view, err := s.viewForProfile(ctx, profile)
		return view, false, err
	}
	stored, changed, err := s.repo.UpdateProfile(ctx, key, update)
	if err != nil {
		return ProfileView{}, false, profileRepoError(err)
	}
	view, err := s.viewForProfile(ctx, stored)
	if err != nil {
		return ProfileView{}, false, err
	}
	s.recordAudit(ctx, actor, stored, fields, stored.SystemPrompt, view.PromptVersion)
	return view, changed, nil
}

// SavePrompt appends a new admin prompt revision, updates the current body in
// the same transaction and returns the stored profile. A no-op submission does
// not append; expected_version mismatch is a 409.
func (s *ProfileAdminService) SavePrompt(ctx context.Context, actor platform.Actor, key string, input SavePromptInput) (ProfileView, int, bool, error) {
	return s.writePrompt(ctx, actor, key, input.SystemPrompt, input.Note, agent.PromptSourceAdmin, input.ExpectedVersion, true)
}

// ResetPrompt appends the built-in default body as a new revision
// (source=reset).
func (s *ProfileAdminService) ResetPrompt(ctx context.Context, actor platform.Actor, key string) (ProfileView, int, bool, error) {
	if err := requireAdmin(actor); err != nil {
		return ProfileView{}, 0, false, err
	}
	if err := validateProfileKey(key); err != nil {
		return ProfileView{}, 0, false, err
	}
	if s == nil || s.repo == nil {
		return ProfileView{}, 0, false, platform.Unavailable("agent profile repository is not configured")
	}
	if _, err := s.repo.GetProfile(ctx, key); err != nil {
		return ProfileView{}, 0, false, profileRepoError(err)
	}
	defaultPrompt, ok := DefaultPrompt(key)
	if !ok {
		return ProfileView{}, 0, false, platform.Invalid("key", "profile has no built-in default prompt")
	}
	return s.writePrompt(ctx, actor, key, defaultPrompt, "", agent.PromptSourceReset, nil, false)
}

// RestorePrompt appends the body of an older revision as a new revision
// (source=admin, note=restore vN); history rows are never modified.
func (s *ProfileAdminService) RestorePrompt(ctx context.Context, actor platform.Actor, key string, version int) (ProfileView, int, bool, error) {
	if err := requireAdmin(actor); err != nil {
		return ProfileView{}, 0, false, err
	}
	if err := validateProfileKey(key); err != nil {
		return ProfileView{}, 0, false, err
	}
	if version <= 0 {
		return ProfileView{}, 0, false, platform.Invalid("version", "must be a positive integer")
	}
	if s == nil || s.repo == nil {
		return ProfileView{}, 0, false, platform.Unavailable("agent profile repository is not configured")
	}
	if _, err := s.repo.GetProfile(ctx, key); err != nil {
		return ProfileView{}, 0, false, profileRepoError(err)
	}
	revision, err := s.repo.GetPromptRevision(ctx, key, version)
	if err != nil {
		if errors.Is(err, agent.ErrNotFound) {
			return ProfileView{}, 0, false, platform.NotFound("prompt_revision")
		}
		return ProfileView{}, 0, false, err
	}
	return s.writePrompt(ctx, actor, key, revision.SystemPrompt, fmt.Sprintf("restore v%d", version), agent.PromptSourceAdmin, nil, false)
}

func (s *ProfileAdminService) writePrompt(
	ctx context.Context,
	actor platform.Actor,
	key, prompt, note, source string,
	expectedVersion *int,
	noOpOnSame bool,
) (ProfileView, int, bool, error) {
	if err := requireAdmin(actor); err != nil {
		return ProfileView{}, 0, false, err
	}
	if err := validateProfileKey(key); err != nil {
		return ProfileView{}, 0, false, err
	}
	if s == nil || s.repo == nil {
		return ProfileView{}, 0, false, platform.Unavailable("agent profile repository is not configured")
	}
	if _, err := s.repo.GetProfile(ctx, key); err != nil {
		return ProfileView{}, 0, false, profileRepoError(err)
	}
	if err := agent.ValidatePrompt(key, prompt, PromptVariables(key)); err != nil {
		return ProfileView{}, 0, false, err
	}
	result, err := s.repo.SavePromptRevision(ctx, agent.PromptWrite{
		ProfileKey:      key,
		SystemPrompt:    prompt,
		Note:            note,
		Source:          source,
		ActorID:         actorID(actor),
		ExpectedVersion: expectedVersion,
		NoOpOnSame:      noOpOnSame,
	})
	if err != nil {
		switch {
		case errors.Is(err, agent.ErrPromptVersionConflict):
			return ProfileView{}, 0, false, platform.StateConflict("prompt was changed by another writer")
		case errors.Is(err, agent.ErrNotFound):
			return ProfileView{}, 0, false, platform.NotFound("agent_profile")
		default:
			return ProfileView{}, 0, false, err
		}
	}
	stored, err := s.repo.GetProfile(ctx, key)
	if err != nil {
		return ProfileView{}, 0, false, profileRepoError(err)
	}
	version := result.Version
	if version < 1 {
		if info, ok := s.currentPromptVersion(ctx, key); ok {
			version = info
		}
	}
	view := toProfileView(stored, agent.PromptVersionInfo{Version: version})
	if view.PromptVersion < 1 {
		view.PromptVersion = 1
	}
	s.recordAudit(ctx, actor, stored, []string{"system_prompt"}, prompt, version)
	return view, version, result.Changed, nil
}

// Revisions returns one version DESC page of prompt history. The profile must
// exist (404 otherwise); limit is normalized to 1–100 with a default of 20.
func (s *ProfileAdminService) Revisions(ctx context.Context, key string, limit int, beforeVersion *int) ([]PromptRevision, bool, error) {
	if err := validateProfileKey(key); err != nil {
		return nil, false, err
	}
	if s == nil || s.repo == nil {
		return nil, false, platform.Unavailable("agent profile repository is not configured")
	}
	if _, err := s.repo.GetProfile(ctx, key); err != nil {
		return nil, false, profileRepoError(err)
	}
	if beforeVersion != nil && *beforeVersion <= 0 {
		return nil, false, platform.Invalid("before_version", "must be a positive integer")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return s.repo.ListPromptRevisions(ctx, key, limit, beforeVersion)
}

// Preview renders a template with the variable example values plus the
// supplied sample overrides. Nothing is persisted.
func (s *ProfileAdminService) Preview(ctx context.Context, key, tmpl string, sample map[string]string) (string, error) {
	if err := validateProfileKey(key); err != nil {
		return "", err
	}
	if s == nil || s.repo == nil {
		return "", platform.Unavailable("agent profile repository is not configured")
	}
	profile, err := s.repo.GetProfile(ctx, key)
	if err != nil {
		return "", profileRepoError(err)
	}
	if strings.TrimSpace(tmpl) == "" {
		tmpl = profile.SystemPrompt
	}
	return RenderPromptSample(key, tmpl, sample)
}

// Tools returns the read-only P5 tool catalog. Tool parameters are returned as
// JSON schema only; tools are never executed.
func (s *ProfileAdminService) Tools() ([]AgentToolView, error) {
	if s == nil || s.tools == nil {
		return nil, platform.Unavailable("tool registry is not configured")
	}
	names := s.tools.Names()
	views := make([]AgentToolView, 0, len(names))
	for _, name := range names {
		tool, ok := s.tools.Get(name)
		if !ok {
			continue
		}
		parameters := tool.Parameters()
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{}`)
		}
		views = append(views, AgentToolView{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  parameters,
		})
	}
	return views, nil
}

/* -------------------------------- helpers -------------------------------- */

func requireAdmin(actor platform.Actor) error {
	if !actor.IsAdmin() {
		return platform.Forbidden()
	}
	return nil
}

func validateProfileKey(key string) error {
	if !agentProfileKeyPattern.MatchString(key) {
		return platform.Invalid("key", "must match ^[a-z][a-z0-9_]{0,63}$")
	}
	return nil
}

func validateProfileBudgets(budgets agent.ProfileBudgets) error {
	if budgets.MaxTurns < 0 {
		return platform.Invalid("budgets.max_turns", "must not be negative")
	}
	if budgets.SoftSeconds < 0 || budgets.HardSeconds < 0 {
		return platform.Invalid("budgets", "timeouts must not be negative")
	}
	if budgets.SoftSeconds > 0 && budgets.HardSeconds > 0 && budgets.SoftSeconds > budgets.HardSeconds {
		return platform.Invalid("budgets", "soft_seconds must not exceed hard_seconds")
	}
	return nil
}

func (s *ProfileAdminService) validateModel(ctx context.Context, model string) error {
	if s.models == nil {
		return platform.Unavailable("LLM model catalog is not configured")
	}
	if _, err := s.models.ResolveModel(ctx, model); err != nil {
		return platform.Invalid("model", "must be a model of the default LLM configuration")
	}
	return nil
}

func (s *ProfileAdminService) validateAllowedTools(names []string) ([]string, error) {
	if s.tools == nil {
		return nil, platform.Unavailable("tool registry is not configured")
	}
	seen := make(map[string]struct{}, len(names))
	deduped := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return nil, platform.Invalid("allowed_tools", "must not contain empty items")
		}
		if _, ok := s.tools.Get(trimmed); !ok {
			return nil, platform.Invalid("allowed_tools", "unknown tool: "+trimmed)
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		deduped = append(deduped, trimmed)
	}
	return deduped, nil
}

func (s *ProfileAdminService) currentPromptVersion(ctx context.Context, key string) (int, bool) {
	versions, err := s.repo.LatestPromptVersions(ctx)
	if err != nil {
		return 0, false
	}
	info, ok := versions[key]
	return info.Version, ok
}

func (s *ProfileAdminService) viewForProfile(ctx context.Context, profile agent.AgentProfile) (ProfileView, error) {
	versions, err := s.repo.LatestPromptVersions(ctx)
	if err != nil {
		return ProfileView{}, err
	}
	return toProfileView(profile, versions[profile.Key]), nil
}

func toProfileView(profile agent.AgentProfile, info agent.PromptVersionInfo) ProfileView {
	version := info.Version
	if version < 1 {
		// The startup backfill always seeds v1; keep the required >=1 view
		// shape for a database whose backfill has not run yet.
		version = 1
	}
	tools := profile.AllowedTools
	if tools == nil {
		tools = []string{}
	}
	return ProfileView{
		ID:            profile.ID,
		Key:           profile.Key,
		Name:          profile.Name,
		SystemPrompt:  profile.SystemPrompt,
		AllowedTools:  append([]string(nil), tools...),
		Model:         profile.Model,
		Budgets:       profile.Budgets,
		Enabled:       profile.Enabled,
		PromptVersion: version,
		CreatedAt:     profile.CreatedAt,
		UpdatedAt:     profile.UpdatedAt,
	}
}

func profileRepoError(err error) error {
	if errors.Is(err, agent.ErrNotFound) {
		return platform.NotFound("agent_profile")
	}
	return err
}

func actorID(actor platform.Actor) *int64 {
	if actor.ID <= 0 {
		return nil
	}
	id := actor.ID
	return &id
}

// recordAudit writes the single agent.profile.updated action used by every
// profile/prompt write. Metadata is limited to identity, changed fields,
// version, prompt digest and length; the prompt body never enters the log.
func (s *ProfileAdminService) recordAudit(
	ctx context.Context,
	actor platform.Actor,
	profile agent.AgentProfile,
	fields []string,
	prompt string,
	version int,
) {
	if s == nil || s.audit == nil {
		return
	}
	digest := sha256.Sum256([]byte(prompt))
	if fields == nil {
		fields = []string{}
	}
	metadata := map[string]any{
		"profile_key":    profile.Key,
		"fields":         fields,
		"prompt_version": version,
		"prompt_sha256":  hex.EncodeToString(digest[:]),
		"prompt_length":  utf8.RuneCountInString(prompt),
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actorID(actor),
		Action:     ActionProfileUpdated,
		TargetKind: "agent_profile",
		TargetID:   fmt.Sprintf("%d", profile.ID),
		Metadata:   metadata,
	})
}

// profileSeedOrder returns the frozen 1-based seed order of a profile key. It
// mirrors the Agent center team map so both admin lists agree.
func profileSeedOrder(key string) int {
	if _, order, ok := catalogTeamForProfile(key); ok {
		return order
	}
	return len(agentCenterTeams) + 1000
}

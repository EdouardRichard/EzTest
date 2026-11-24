// Package resourceapp implements the resource-catalog use cases: environment,
// SUT, page, API, target database, test account and code repository CRUD,
// catalog aggregation and bulk import.
//
// v0.2.4-B inverted the hierarchy (SC-B1): an environment is a top-level entity,
// a SUT belongs to one environment and carries its base_url, and pages, APIs and
// target databases belong to a SUT.
package resourceapp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Service is the resource-catalog application service. It implements
// CatalogReader for the version context.
type Service struct {
	repo      resource.Repository
	ids       platformid.Generator
	clock     clock.Clock
	usage     UsageChecker
	audit     auditapp.Recorder
	events    eventingapp.Publisher
	knowledge KnowledgeInvalidator
}

// New wires a resource service. usage, audit and events may be nil in unit
// tests; the assembly root sets the real implementations.
func New(
	repo resource.Repository,
	ids platformid.Generator,
	clk clock.Clock,
	usage UsageChecker,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
) *Service {
	return &Service{repo: repo, ids: ids, clock: clk, usage: usage, audit: audit, events: events}
}

// SetUsageChecker injects the version-binding usage guard after construction.
// The version service needs a CatalogReader, which is this service, so the
// assembly root wires the two contexts through this setter.
func (s *Service) SetUsageChecker(usage UsageChecker) {
	s.usage = usage
}

// SetKnowledgeInvalidator injects the knowledge lifecycle sink after
// construction. The knowledge service is built later in the assembly root than
// the resource catalog, so the cross-context port is attached through this
// setter (nil disables invalidation).
func (s *Service) SetKnowledgeInvalidator(invalidator KnowledgeInvalidator) {
	s.knowledge = invalidator
}

// invalidateKnowledge forwards one genuinely changed binding signal to the
// knowledge context. It is best effort: the resource write already committed, so
// a knowledge failure or a partially failed CAS pass is logged and never
// surfaced to the caller (spec §10.1 页面/路由/账号变更 → stale).
func (s *Service) invalidateKnowledge(ctx context.Context, change KnowledgeBindingChange) {
	if s.knowledge == nil {
		return
	}
	result, err := s.knowledge.MarkBindingsStale(ctx, change)
	if err != nil {
		slog.Default().WarnContext(ctx, "knowledge binding invalidation failed",
			"reason", change.Reason, "error", err)
		return
	}
	fields := []any{
		"reason", change.Reason,
		"scanned", result.Scanned,
		"matched", result.Matched,
		"marked", result.Marked,
		"failed", result.Failed,
	}
	if result.Failed > 0 {
		slog.Default().WarnContext(ctx, "knowledge binding invalidation partially failed", fields...)
		return
	}
	if result.Marked > 0 {
		slog.Default().InfoContext(ctx, "knowledge binding invalidation marked stale", fields...)
	}
}

// ---------- inputs and patches ----------

// EnvironmentInput is the editable payload for creating a top-level environment.
type EnvironmentInput struct {
	Name        string
	Description string
	Status      string
}

// EnvironmentPatch is a partial environment update; nil fields are untouched.
type EnvironmentPatch struct {
	Name        *string
	Description *string
	Status      *string
}

// SUTInput is the editable payload for creating a SUT inside an environment.
type SUTInput struct {
	Name    string
	Intro   string
	BaseURL string
	Family  *string
}

// SUTPatch is a partial SUT update; nil fields are left untouched. A non-nil
// Family replaces the stored family; the empty string clears it.
type SUTPatch struct {
	Name    *string
	Intro   *string
	BaseURL *string
	Family  *string
}

// PageInput is the editable payload for creating a page.
type PageInput struct {
	Name          string
	URL           string
	RouteTemplate string
	PageType      string
	AuthRole      string
	Module        string
}

// PagePatch is a partial page update; nil fields are untouched.
type PagePatch struct {
	Name          *string
	URL           *string
	RouteTemplate *string
	PageType      *string
	AuthRole      *string
	Module        *string
}

// APIInput is the editable payload for creating an API.
type APIInput struct {
	Method       string
	PathTemplate string
	Name         string
	AuthRole     string
	SchemaRef    string
}

// APIPatch is a partial API update; nil fields are untouched.
type APIPatch struct {
	Method       *string
	PathTemplate *string
	Name         *string
	AuthRole     *string
	SchemaRef    *string
}

// ImportPagesResult reports a page import.
type ImportPagesResult struct {
	Created int
	Skipped int
	Items   []PageView
}

// ImportAPIsResult reports an API import.
type ImportAPIsResult struct {
	Created int
	Skipped int
	Items   []APIView
}

// DatabaseInput is the editable payload for creating a target database of a SUT.
type DatabaseInput struct {
	Name     string
	DSN      string
	Username string
	Password string
	ReadOnly *bool
	Status   string
}

// DatabasePatch is a partial target-database update; nil fields are untouched.
type DatabasePatch struct {
	Name     *string
	DSN      *string
	Username *string
	Password *string
	ReadOnly *bool
	Status   *string
}

// AccountInput is the editable payload for creating a test account of the global
// pool. SUTID is the optional affinity hint; nil means "no affinity".
type AccountInput struct {
	SUTID    *int64
	Username string
	Password string
	Roles    []string
	Notes    string
	Status   string
}

// AccountPatch is a partial test-account update; a nil Password keeps the stored
// password and a nil SUTID keeps the stored affinity. Clearing an affinity is
// part of the credential-affinity behaviour owned by a later subproject.
type AccountPatch struct {
	SUTID    *int64
	Username *string
	Password *string
	Roles    *[]string
	Notes    *string
	Status   *string
}

// SUTRepositoryInput is the payload for associating one global code repository
// with a SUT (v0.22 API-17).
type SUTRepositoryInput struct {
	RepositoryID int64
	Role         string
}

// RepositoryInput is the editable payload for creating a code repository.
type RepositoryInput struct {
	Name    string
	Path    string
	Branch  string
	Include []string
	Exclude []string
	Status  string
}

// RepositoryPatch is a partial code-repository update.
type RepositoryPatch struct {
	Name    *string
	Path    *string
	Branch  *string
	Include *[]string
	Exclude *[]string
	Status  *string
}

// normalizeFamilyInput trims an optional API family. A blank value means no
// family (explicit clear on update, unset on create); present values must pass
// the domain family rules.
func normalizeFamilyInput(raw *string) (*string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	return resource.NormalizeFamily(raw)
}

// ---------- environment ----------

// CreateEnvironment stores a new top-level environment.
func (s *Service) CreateEnvironment(ctx context.Context, input EnvironmentInput, actorID int64) (EnvironmentView, error) {
	at := s.clock.Now()
	environment := resource.Environment{
		Name:        input.Name,
		Description: input.Description,
		Status:      defaultStatus(input.Status),
		CreatedAt:   at,
		UpdatedAt:   at,
	}
	if err := environment.Validate(); err != nil {
		return EnvironmentView{}, err
	}
	environment.ID = s.ids.Next()
	created, err := s.repo.CreateEnvironment(ctx, environment)
	if err != nil {
		return EnvironmentView{}, mapRepoError(err, "environment", "environment name already exists")
	}
	s.record(ctx, actorID, "resource.environment.created", "environment", platformid.Format(created.ID), map[string]any{
		"name": created.Name,
	})
	return toEnvironmentView(created, nil), nil
}

// UpdateEnvironment applies a partial environment update.
func (s *Service) UpdateEnvironment(ctx context.Context, id int64, patch EnvironmentPatch, actorID int64) (EnvironmentView, error) {
	environment, err := s.repo.GetEnvironment(ctx, id)
	if err != nil {
		return EnvironmentView{}, mapRepoError(err, "environment", "")
	}
	if patch.Name != nil {
		environment.Name = *patch.Name
	}
	if patch.Description != nil {
		environment.Description = *patch.Description
	}
	if patch.Status != nil {
		environment.Status = *patch.Status
	}
	environment.UpdatedAt = s.clock.Now()
	if err := environment.Validate(); err != nil {
		return EnvironmentView{}, err
	}
	updated, err := s.repo.UpdateEnvironment(ctx, environment)
	if err != nil {
		return EnvironmentView{}, mapRepoError(err, "environment", "environment name already exists")
	}
	s.record(ctx, actorID, "resource.environment.updated", "environment", platformid.Format(updated.ID), map[string]any{
		"name": updated.Name,
	})
	return s.GetEnvironment(ctx, updated.ID)
}

// DeleteEnvironment deletes an environment when no version binds it. Its SUTs
// and their assets cascade.
func (s *Service) DeleteEnvironment(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.EnvironmentInUse(ctx, id)
	}, "environment", id); err != nil {
		return err
	}
	if err := s.repo.DeleteEnvironment(ctx, id); err != nil {
		return mapRepoError(err, "environment", "")
	}
	s.record(ctx, actorID, "resource.environment.deleted", "environment", platformid.Format(id), nil)
	return nil
}

// ListEnvironments returns one page of environments ordered by ID descending.
// The SUT catalog is not nested; use GetEnvironment for the tree.
func (s *Service) ListEnvironments(ctx context.Context, limit int, beforeID *int64) ([]EnvironmentView, bool, error) {
	limit = normalizeLimit(limit)
	items, err := s.repo.ListEnvironments(ctx, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	views := make([]EnvironmentView, 0, len(items))
	for _, item := range items {
		views = append(views, toEnvironmentView(item, nil))
	}
	return views, hasMore, nil
}

// GetEnvironment returns one environment with its whole SUT catalog nested. It
// is the CatalogReader entry point the version context resolves a binding with.
func (s *Service) GetEnvironment(ctx context.Context, id int64) (EnvironmentView, error) {
	graph, err := s.repo.GetEnvironmentGraph(ctx, id)
	if err != nil {
		return EnvironmentView{}, mapRepoError(err, "environment", "")
	}
	return toEnvironmentDetailView(graph), nil
}

// ---------- SUT ----------

// CreateSUT validates and stores a new SUT inside an environment. This is the
// ADMIN registration path (R-B4): the SUT is created with source=admin and
// status=approved, and one append-only sut_registration birth fact records the
// registration so it can be read back through ListSUTRegistrations. The SUT and
// its birth fact are written in ONE transaction (I-1) so a failed append rolls the
// SUT back — a SUT can never exist without its birth record.
func (s *Service) CreateSUT(ctx context.Context, environmentID int64, input SUTInput, actorID int64) (SUTView, error) {
	if _, err := s.repo.GetEnvironment(ctx, environmentID); err != nil {
		return SUTView{}, mapRepoError(err, "environment", "")
	}
	family, err := normalizeFamilyInput(input.Family)
	if err != nil {
		return SUTView{}, err
	}
	at := s.clock.Now()
	sut := resource.SUT{
		EnvironmentID: environmentID,
		Name:          input.Name,
		Intro:         input.Intro,
		BaseURL:       input.BaseURL,
		Family:        family,
		Source:        resource.SUTSourceAdmin,
		Status:        resource.SUTStatusApproved,
		CreatedBy:     actorID,
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	if err := sut.Validate(); err != nil {
		return SUTView{}, err
	}
	sut.ID = s.ids.Next()
	// The birth registration fact (append-only; the admin path needs no AI evidence
	// and is approved on creation). Every registration row gets an independently
	// generated ID (the version_fact convention): a registration id and a SUT id are
	// different entities and must not share a value space. The birth record is
	// identified by its content (sut_id + resolution_method=SUTResolutionAdminCreate),
	// not by id equality with the SUT. Later registrations of the same SUT (the Agent
	// proposal path subproject F will add) append through AppendSUTRegistration.
	registration := resource.SUTRegistration{
		ID:               s.ids.Next(),
		SUTID:            sut.ID,
		Source:           sut.Source,
		EvidenceRefs:     []string{},
		ResolutionMethod: resource.SUTResolutionAdminCreate,
		GateResult:       sut.Status,
		CreatedAt:        at,
		CreatedBy:        actorID,
	}
	if err := registration.Validate(); err != nil {
		return SUTView{}, err
	}
	// One transaction (I-1): the SUT and its birth fact commit together, so a failed
	// append rolls the SUT back rather than leaving it without a birth record. The
	// conflict message is kept neutral: the only caller-reachable conflict is the SUT
	// name, but a registration-side conflict must not be blamed on the SUT name.
	created, err := s.repo.CreateSUTWithRegistration(ctx, sut, registration)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "sut already exists in this environment")
	}
	s.record(ctx, actorID, "resource.sut.created", "sut", platformid.Format(created.ID), map[string]any{
		"environment_id": platformid.Format(created.EnvironmentID),
		"name":           created.Name,
	})
	return toSUTView(created), nil
}

// ListSUTRegistrations returns one SUT's append-only registration facts in time
// order. It is the production read path behind
// GET /api/resources/suts/{id}/registrations (R-B4; a fact table with no reader
// would repeat violations.md V-0019).
func (s *Service) ListSUTRegistrations(ctx context.Context, sutID int64) ([]SUTRegistrationView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return nil, mapRepoError(err, "sut", "")
	}
	registrations, err := s.repo.ListSUTRegistrations(ctx, sutID)
	if err != nil {
		return nil, mapRepoError(err, "sut_registration", "")
	}
	views := make([]SUTRegistrationView, 0, len(registrations))
	for _, registration := range registrations {
		views = append(views, toSUTRegistrationView(registration))
	}
	return views, nil
}

// RegisterAISUT is the Agent new-SUT registration path (v0.2.4-F R-B4/R-F1,
// SC-F4). The execution evidence gate calls it after a resource matched no existing
// SUT: it creates a source=ai SUT inside the version's bound environment and writes
// the SAME append-only sut_registration birth fact the admin path uses, in ONE
// transaction (I-1). The gate outcome decides the status: approved=true (effective
// proof passed) → status=approved AUTO (no human approval; GD-01 evidence gate, not
// approval gate); approved=false → status=proposed (fail-closed: a proposed SUT
// contributes nothing to effective scope, and an admin can audit/reject it). The
// proving evidence refs and gate_result (=status) are recorded on the fact so the
// registration history survives later transitions. A name collision inside the
// environment is a 409 (the agent matches existing SUTs first via P16).
//
// Rebirth guard (controller ruling P22, extended to new-SUT rename-rebirth): a
// rename of a REJECTED SUT (same base_url/domain, new name) citing PRE-reject
// evidence must not overturn the human reject. When the proposed base_url/domain
// matches a rejected SUT of the environment, auto-approval additionally requires
// provenAt (the newest proving-evidence created_at, server-derived via the shared
// evidence gate's proofTime anchor) to be LATER than that SUT's rejection
// timestamp; otherwise the registration downgrades to proposed so the admin
// adjudicates (constitution 一.3: the human judgment point is preserved).
func (s *Service) RegisterAISUT(ctx context.Context, environmentID int64, input SUTInput, evidenceRefs []string, approved bool, actorID int64, provenAt time.Time) (SUTView, error) {
	if _, err := s.repo.GetEnvironment(ctx, environmentID); err != nil {
		return SUTView{}, mapRepoError(err, "environment", "")
	}
	family, err := normalizeFamilyInput(input.Family)
	if err != nil {
		return SUTView{}, err
	}
	status := resource.SUTStatusProposed
	rebirthDowngraded := false
	if approved {
		stale, err := s.rebirthProofIsStale(ctx, environmentID, input.BaseURL, provenAt)
		if err != nil {
			return SUTView{}, err
		}
		if stale {
			rebirthDowngraded = true
		} else {
			status = resource.SUTStatusApproved
		}
	}
	refs := evidenceRefs
	if refs == nil {
		refs = []string{}
	}
	at := s.clock.Now()
	sut := resource.SUT{
		EnvironmentID: environmentID,
		Name:          input.Name,
		Intro:         input.Intro,
		BaseURL:       input.BaseURL,
		Family:        family,
		Source:        resource.SUTSourceAI,
		Status:        status,
		CreatedBy:     actorID,
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	if err := sut.Validate(); err != nil {
		return SUTView{}, err
	}
	sut.ID = s.ids.Next()
	registration := resource.SUTRegistration{
		ID:               s.ids.Next(),
		SUTID:            sut.ID,
		Source:           sut.Source,
		EvidenceRefs:     refs,
		ResolutionMethod: resource.SUTResolutionAgentRegister,
		GateResult:       status,
		CreatedAt:        at,
		CreatedBy:        actorID,
	}
	if err := registration.Validate(); err != nil {
		return SUTView{}, err
	}
	created, err := s.repo.CreateSUTWithRegistration(ctx, sut, registration)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "sut already exists in this environment")
	}
	metadata := map[string]any{
		"environment_id": platformid.Format(created.EnvironmentID),
		"name":           created.Name,
		"source":         created.Source,
		"status":         created.Status,
		"gate_result":    status,
	}
	if rebirthDowngraded {
		// Observable downgrade (constitution 七.6): the proof passed the evidence
		// gate but predates a human reject of the same system — recorded, never a
		// silent approval or a silent drop.
		metadata["downgrade_reason"] = "stale_proof_vs_rejected_sut"
	}
	s.record(ctx, actorID, "resource.sut.registered", "sut", platformid.Format(created.ID), metadata)
	return toSUTView(created), nil
}

// rebirthProofIsStale reports whether the proposed base_url/domain matches a
// REJECTED SUT of the environment whose rejection is NOT superseded by fresh proof
// (provenAt must be strictly later than the rejection timestamp). The rejection
// timestamp is the rejected row's UpdatedAt (RejectSUT sets it; a later admin edit
// only raises the bar — conservative, the same anchor T10's rejected-asset revival
// uses). A zero provenAt is treated as old evidence: fail-closed. The match is by
// hostname (a rename may also vary the path) with an exact-URL fallback, so the
// guard is scoped to the rejected system's domain and never over-blocks an
// unrelated discovery.
func (s *Service) rebirthProofIsStale(ctx context.Context, environmentID int64, baseURL string, provenAt time.Time) (bool, error) {
	suts, err := s.repo.ListSUTsByEnvironment(ctx, environmentID)
	if err != nil {
		return false, err
	}
	host := sutURLHost(baseURL)
	for _, candidate := range suts {
		if candidate.Status != resource.SUTStatusRejected {
			continue
		}
		sameDomain := host != "" && sutURLHost(candidate.BaseURL) == host
		sameURL := strings.EqualFold(strings.TrimSpace(candidate.BaseURL), strings.TrimSpace(baseURL))
		if !sameDomain && !sameURL {
			continue
		}
		if !provenAt.After(candidate.UpdatedAt) {
			return true, nil
		}
	}
	return false, nil
}

// sutURLHost extracts the lowercased hostname of a base_url for the rebirth
// domain match; an unparseable/hostless URL yields "" (no domain match, the
// exact-URL fallback still applies).
func sutURLHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// RejectSUT is the admin post-hoc audit/reject entry (v0.2.4-F SC-F4). The Agent
// auto-approves a new SUT on evidence (R-F1); an admin can still reject it after the
// fact, or reject a proposed SUT the gate did not approve. Both proposed and approved
// SUTs may be rejected; a rejected SUT is terminal (a second reject is a 409). The
// registration fact is append-only and independent of current status, so reject only
// changes the SUT status (no new fact) — the audit/event record the transition. A
// rejected SUT contributes nothing to effective scope (IsApproved gate), including
// its repository associations (the environment code pool query gates on status).
func (s *Service) RejectSUT(ctx context.Context, id int64, actorID int64) (SUTView, error) {
	sut, err := s.repo.GetSUT(ctx, id)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "")
	}
	if sut.Status == resource.SUTStatusRejected {
		return SUTView{}, platform.Conflict("sut is already rejected")
	}
	sut.Status = resource.SUTStatusRejected
	sut.UpdatedAt = s.clock.Now()
	updated, err := s.repo.UpdateSUT(ctx, sut)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "sut name already exists in this environment")
	}
	s.record(ctx, actorID, "resource.sut.rejected", "sut", platformid.Format(updated.ID), map[string]any{
		"environment_id": platformid.Format(updated.EnvironmentID),
		"name":           updated.Name,
		"source":         updated.Source,
	})
	return s.GetSUT(ctx, updated.ID)
}

// UpdateSUT applies a partial update to a SUT. Its environment is not movable:
// the assets and the repository associations are scoped to the SUT inside its
// environment, so moving a SUT means creating a new one.
func (s *Service) UpdateSUT(ctx context.Context, id int64, patch SUTPatch, actorID int64) (SUTView, error) {
	sut, err := s.repo.GetSUT(ctx, id)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "")
	}
	if patch.Name != nil {
		sut.Name = *patch.Name
	}
	if patch.Intro != nil {
		sut.Intro = *patch.Intro
	}
	if patch.BaseURL != nil {
		sut.BaseURL = *patch.BaseURL
	}
	if patch.Family != nil {
		family, err := normalizeFamilyInput(patch.Family)
		if err != nil {
			return SUTView{}, err
		}
		sut.Family = family
	}
	sut.UpdatedAt = s.clock.Now()
	if err := sut.Validate(); err != nil {
		return SUTView{}, err
	}
	updated, err := s.repo.UpdateSUT(ctx, sut)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "sut name already exists in this environment")
	}
	s.record(ctx, actorID, "resource.sut.updated", "sut", platformid.Format(updated.ID), map[string]any{
		"environment_id": platformid.Format(updated.EnvironmentID),
		"name":           updated.Name,
	})
	return s.GetSUT(ctx, updated.ID)
}

// DeleteSUT deletes a SUT when no version binding references it. Its assets
// cascade; the SUT repository associations are cleared by the repository.
func (s *Service) DeleteSUT(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) { return s.usage.SUTInUse(ctx, id) }, "sut", id); err != nil {
		return err
	}
	if err := s.repo.DeleteSUT(ctx, id); err != nil {
		return mapRepoError(err, "sut", "")
	}
	s.record(ctx, actorID, "resource.sut.deleted", "sut", platformid.Format(id), nil)
	return nil
}

// ListSUTs returns one page of SUTs across every environment ordered by ID
// descending. The assets are not nested; use GetSUT for the tree.
func (s *Service) ListSUTs(ctx context.Context, limit int, beforeID *int64) ([]SUTView, bool, error) {
	limit = normalizeLimit(limit)
	items, err := s.repo.ListSUTs(ctx, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	views := make([]SUTView, 0, len(items))
	for _, item := range items {
		views = append(views, toSUTView(item))
	}
	return views, hasMore, nil
}

// ListSUTsByEnvironment returns the SUT catalog of one environment with each
// SUT's assets nested, in SUT ID order.
func (s *Service) ListSUTsByEnvironment(ctx context.Context, environmentID int64) ([]SUTView, error) {
	if environmentID <= 0 {
		return nil, platform.Invalid("environment_id", "must be a positive decimal ID")
	}
	graph, err := s.repo.GetEnvironmentGraph(ctx, environmentID)
	if err != nil {
		return nil, mapRepoError(err, "environment", "")
	}
	return environmentSUTViews(graph), nil
}

// GetSUT returns one SUT with the pages, APIs and target databases it owns.
func (s *Service) GetSUT(ctx context.Context, id int64) (SUTView, error) {
	sut, err := s.repo.GetSUT(ctx, id)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "")
	}
	assets, err := s.loadAssets(ctx, id)
	if err != nil {
		return SUTView{}, err
	}
	view := toSUTView(sut)
	view.Pages = assets.Pages
	view.APIs = assets.APIs
	view.Databases = assets.Databases
	return view, nil
}

// ListAssetsBySUT returns the pages, APIs and target databases of one SUT.
func (s *Service) ListAssetsBySUT(ctx context.Context, sutID int64) (SUTAssets, error) {
	if sutID <= 0 {
		return SUTAssets{}, platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return SUTAssets{}, mapRepoError(err, "sut", "")
	}
	return s.loadAssets(ctx, sutID)
}

func (s *Service) loadAssets(ctx context.Context, sutID int64) (SUTAssets, error) {
	pages, err := s.repo.ListPages(ctx, sutID)
	if err != nil {
		return SUTAssets{}, err
	}
	apis, err := s.repo.ListAPIs(ctx, sutID)
	if err != nil {
		return SUTAssets{}, err
	}
	databases, err := s.repo.ListDatabases(ctx, sutID)
	if err != nil {
		return SUTAssets{}, err
	}
	assets := SUTAssets{
		Pages:     make([]PageView, 0, len(pages)),
		APIs:      make([]APIView, 0, len(apis)),
		Databases: make([]DatabaseView, 0, len(databases)),
	}
	for _, page := range pages {
		assets.Pages = append(assets.Pages, toPageView(page))
	}
	for _, api := range apis {
		assets.APIs = append(assets.APIs, toAPIView(api))
	}
	for _, database := range databases {
		assets.Databases = append(assets.Databases, toDatabaseView(database))
	}
	return assets, nil
}

func defaultStatus(status string) string {
	if status == "" {
		return resource.StatusActive
	}
	return status
}

// ---------- pages ----------

// CreatePage stores a page under a SUT. The route template is derived from the
// URL when omitted; source is admin and status approved.
func (s *Service) CreatePage(ctx context.Context, sutID int64, input PageInput, actorID int64) (PageView, error) {
	sut, err := s.repo.GetSUT(ctx, sutID)
	if err != nil {
		return PageView{}, mapRepoError(err, "sut", "")
	}
	page, err := s.buildPage(sut, input, resource.PageSourceAdmin)
	if err != nil {
		return PageView{}, err
	}
	created, err := s.insertPage(ctx, page, actorID)
	if err != nil {
		return PageView{}, mapPageError(err)
	}
	return toPageView(created), nil
}

func (s *Service) buildPage(sut resource.SUT, input PageInput, source string) (resource.Page, error) {
	routeTemplate := input.RouteTemplate
	if routeTemplate == "" {
		derived, err := resource.DeriveRouteTemplate(input.URL)
		if err != nil {
			return resource.Page{}, err
		}
		routeTemplate = derived
	}
	pageType := input.PageType
	if pageType == "" {
		pageType = "page"
	}
	at := s.clock.Now()
	page := resource.Page{
		SUTID:         sut.ID,
		Name:          input.Name,
		URL:           input.URL,
		RouteTemplate: routeTemplate,
		PageType:      pageType,
		AuthRole:      input.AuthRole,
		Module:        input.Module,
		Status:        resource.PageStatusApproved,
		Source:        source,
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	if err := page.Validate(); err != nil {
		return resource.Page{}, err
	}
	page.ID = s.ids.Next()
	return page, nil
}

func (s *Service) insertPage(ctx context.Context, page resource.Page, actorID int64) (resource.Page, error) {
	created, err := s.repo.CreatePage(ctx, page)
	if err != nil {
		return resource.Page{}, err
	}
	s.record(ctx, actorID, "resource.page.created", "page", platformid.Format(created.ID), map[string]any{
		"sut_id":         platformid.Format(created.SUTID),
		"name":           created.Name,
		"route_template": created.RouteTemplate,
	})
	return created, nil
}

// UpdatePage applies a partial page update. Clearing route_template re-derives
// it from the (possibly patched) URL.
func (s *Service) UpdatePage(ctx context.Context, id int64, patch PagePatch, actorID int64) (PageView, error) {
	page, err := s.repo.GetPage(ctx, id)
	if err != nil {
		return PageView{}, mapRepoError(err, "page", "")
	}
	previousRoute := strings.TrimSpace(page.RouteTemplate)
	if patch.Name != nil {
		page.Name = *patch.Name
	}
	if patch.URL != nil {
		page.URL = *patch.URL
	}
	if patch.RouteTemplate != nil {
		page.RouteTemplate = *patch.RouteTemplate
	}
	if patch.PageType != nil {
		page.PageType = *patch.PageType
	}
	if patch.AuthRole != nil {
		page.AuthRole = *patch.AuthRole
	}
	if patch.Module != nil {
		page.Module = *patch.Module
	}
	if strings.TrimSpace(page.RouteTemplate) == "" {
		derived, deriveErr := resource.DeriveRouteTemplate(page.URL)
		if deriveErr != nil {
			return PageView{}, deriveErr
		}
		page.RouteTemplate = derived
	}
	page.UpdatedAt = s.clock.Now()
	if err := page.Validate(); err != nil {
		return PageView{}, err
	}
	updated, err := s.repo.UpdatePage(ctx, page)
	if err != nil {
		return PageView{}, mapPageError(err)
	}
	s.record(ctx, actorID, "resource.page.updated", "page", platformid.Format(updated.ID), map[string]any{
		"sut_id":         platformid.Format(updated.SUTID),
		"name":           updated.Name,
		"route_template": updated.RouteTemplate,
	})
	// Only a genuinely changed route key invalidates knowledge. The page's owning
	// SUT is not passed: it did not change, and naming it would mark every item
	// bound to the whole SUT stale.
	if currentRoute := strings.TrimSpace(updated.RouteTemplate); previousRoute != "" && previousRoute != currentRoute {
		s.invalidateKnowledge(ctx, KnowledgeBindingChange{
			PageKeys: []string{previousRoute},
			Reason:   "page route changed",
		})
	}
	return toPageView(updated), nil
}

// DeletePage deletes a page when no version binding references it.
func (s *Service) DeletePage(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.PageInUse(ctx, id)
	}, "page", id); err != nil {
		return err
	}
	page, err := s.repo.GetPage(ctx, id)
	if err != nil {
		return mapRepoError(err, "page", "")
	}
	if err := s.repo.DeletePage(ctx, id); err != nil {
		return mapRepoError(err, "page", "")
	}
	s.record(ctx, actorID, "resource.page.deleted", "page", platformid.Format(id), nil)
	if route := strings.TrimSpace(page.RouteTemplate); route != "" {
		s.invalidateKnowledge(ctx, KnowledgeBindingChange{
			PageKeys: []string{route},
			Reason:   "page deleted",
		})
	}
	return nil
}

// ListPages returns every page of a SUT.
func (s *Service) ListPages(ctx context.Context, sutID int64) ([]PageView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return nil, mapRepoError(err, "sut", "")
	}
	pages, err := s.repo.ListPages(ctx, sutID)
	if err != nil {
		return nil, err
	}
	views := make([]PageView, 0, len(pages))
	for _, page := range pages {
		views = append(views, toPageView(page))
	}
	return views, nil
}

// GetPage returns one page.
func (s *Service) GetPage(ctx context.Context, id int64) (PageView, error) {
	page, err := s.repo.GetPage(ctx, id)
	if err != nil {
		return PageView{}, mapRepoError(err, "page", "")
	}
	return toPageView(page), nil
}

// FindPageByRouteTemplate resolves one page by (sut_id, route_template) in any
// status. It is the W1-12 "already registered" lookup used by the discovery
// delta service: observations carry host + template only, so the normalized_url
// dedupe path of CreateProposedPage cannot be used. found=false means the route
// is unregistered; an empty template is never found.
func (s *Service) FindPageByRouteTemplate(ctx context.Context, sutID int64, routeTemplate string) (PageView, bool, error) {
	if sutID <= 0 {
		return PageView{}, false, platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	if strings.TrimSpace(routeTemplate) == "" {
		return PageView{}, false, nil
	}
	page, err := s.repo.GetPageByRouteTemplate(ctx, sutID, routeTemplate)
	if errors.Is(err, resource.ErrNotFound) {
		return PageView{}, false, nil
	}
	if err != nil {
		return PageView{}, false, err
	}
	return toPageView(page), true, nil
}

// ImportPages validates the whole batch first, then inserts it. Pages whose
// normalized URL already exists inside the SUT are skipped; any invalid item
// rejects the batch with 400.
func (s *Service) ImportPages(ctx context.Context, sutID int64, inputs []PageInput, actorID int64) (ImportPagesResult, error) {
	sut, err := s.repo.GetSUT(ctx, sutID)
	if err != nil {
		return ImportPagesResult{}, mapRepoError(err, "sut", "")
	}
	pages := make([]resource.Page, 0, len(inputs))
	for index, input := range inputs {
		page, err := s.buildPage(sut, input, resource.PageSourceImport)
		if err != nil {
			return ImportPagesResult{}, importItemError(index, err)
		}
		pages = append(pages, page)
	}

	result := ImportPagesResult{Items: []PageView{}}
	for _, page := range pages {
		created, err := s.insertPage(ctx, page, actorID)
		if err != nil {
			if errors.Is(err, resource.ErrDuplicate) || errors.Is(err, resource.ErrPageURLDuplicate) {
				result.Skipped++
				continue
			}
			return ImportPagesResult{}, err
		}
		result.Created++
		result.Items = append(result.Items, toPageView(created))
	}
	return result, nil
}

// ---------- APIs ----------

// CreateAPI stores an API under a SUT. Source is manual and status approved.
func (s *Service) CreateAPI(ctx context.Context, sutID int64, input APIInput, actorID int64) (APIView, error) {
	sut, err := s.repo.GetSUT(ctx, sutID)
	if err != nil {
		return APIView{}, mapRepoError(err, "sut", "")
	}
	api, err := s.buildAPI(sut, input, resource.APISourceManual)
	if err != nil {
		return APIView{}, err
	}
	created, err := s.insertAPI(ctx, api, actorID)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "api route already exists in this sut")
	}
	return toAPIView(created), nil
}

func (s *Service) buildAPI(sut resource.SUT, input APIInput, source string) (resource.API, error) {
	at := s.clock.Now()
	api := resource.API{
		SUTID:        sut.ID,
		Method:       input.Method,
		PathTemplate: input.PathTemplate,
		Name:         input.Name,
		AuthRole:     input.AuthRole,
		Source:       source,
		Status:       resource.APIStatusApproved,
		SchemaRef:    input.SchemaRef,
		CreatedAt:    at,
		UpdatedAt:    at,
	}
	if err := api.Validate(); err != nil {
		return resource.API{}, err
	}
	api.ID = s.ids.Next()
	return api, nil
}

func (s *Service) insertAPI(ctx context.Context, api resource.API, actorID int64) (resource.API, error) {
	created, err := s.repo.CreateAPI(ctx, api)
	if err != nil {
		return resource.API{}, err
	}
	s.record(ctx, actorID, "resource.api.created", "api", platformid.Format(created.ID), map[string]any{
		"sut_id":        platformid.Format(created.SUTID),
		"name":          created.Name,
		"method":        created.Method,
		"path_template": created.PathTemplate,
	})
	return created, nil
}

// UpdateAPI applies a partial API update.
func (s *Service) UpdateAPI(ctx context.Context, id int64, patch APIPatch, actorID int64) (APIView, error) {
	api, err := s.repo.GetAPI(ctx, id)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "")
	}
	if patch.Method != nil {
		api.Method = *patch.Method
	}
	if patch.PathTemplate != nil {
		api.PathTemplate = *patch.PathTemplate
	}
	if patch.Name != nil {
		api.Name = *patch.Name
	}
	if patch.AuthRole != nil {
		api.AuthRole = *patch.AuthRole
	}
	if patch.SchemaRef != nil {
		api.SchemaRef = *patch.SchemaRef
	}
	api.UpdatedAt = s.clock.Now()
	if err := api.Validate(); err != nil {
		return APIView{}, err
	}
	updated, err := s.repo.UpdateAPI(ctx, api)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "api route already exists in this sut")
	}
	s.record(ctx, actorID, "resource.api.updated", "api", platformid.Format(updated.ID), map[string]any{
		"sut_id":        platformid.Format(updated.SUTID),
		"name":          updated.Name,
		"method":        updated.Method,
		"path_template": updated.PathTemplate,
	})
	return toAPIView(updated), nil
}

// DeleteAPI deletes an API when no version binding references it.
func (s *Service) DeleteAPI(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.APIInUse(ctx, id)
	}, "api", id); err != nil {
		return err
	}
	if err := s.repo.DeleteAPI(ctx, id); err != nil {
		return mapRepoError(err, "api", "")
	}
	s.record(ctx, actorID, "resource.api.deleted", "api", platformid.Format(id), nil)
	return nil
}

// ListAPIs returns every API of a SUT.
func (s *Service) ListAPIs(ctx context.Context, sutID int64) ([]APIView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return nil, mapRepoError(err, "sut", "")
	}
	apis, err := s.repo.ListAPIs(ctx, sutID)
	if err != nil {
		return nil, err
	}
	views := make([]APIView, 0, len(apis))
	for _, api := range apis {
		views = append(views, toAPIView(api))
	}
	return views, nil
}

// GetAPI returns one API.
func (s *Service) GetAPI(ctx context.Context, id int64) (APIView, error) {
	api, err := s.repo.GetAPI(ctx, id)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "")
	}
	return toAPIView(api), nil
}

// FindAPIByTemplate resolves one API by (sut_id, method, path_template) in any
// status (W1-12). found=false means the endpoint is unregistered.
func (s *Service) FindAPIByTemplate(ctx context.Context, sutID int64, method, pathTemplate string) (APIView, bool, error) {
	if sutID <= 0 {
		return APIView{}, false, platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	if strings.TrimSpace(method) == "" || strings.TrimSpace(pathTemplate) == "" {
		return APIView{}, false, nil
	}
	api, err := s.repo.GetAPIByTemplate(ctx, sutID, method, pathTemplate)
	if errors.Is(err, resource.ErrNotFound) {
		return APIView{}, false, nil
	}
	if err != nil {
		return APIView{}, false, err
	}
	return toAPIView(api), true, nil
}

// ImportAPIs validates the whole batch first, then inserts it. Duplicate
// method/path pairs inside the SUT are skipped; any invalid item rejects the
// batch with 400.
func (s *Service) ImportAPIs(ctx context.Context, sutID int64, inputs []APIInput, actorID int64) (ImportAPIsResult, error) {
	sut, err := s.repo.GetSUT(ctx, sutID)
	if err != nil {
		return ImportAPIsResult{}, mapRepoError(err, "sut", "")
	}
	apis := make([]resource.API, 0, len(inputs))
	for index, input := range inputs {
		api, err := s.buildAPI(sut, input, resource.APISourceImport)
		if err != nil {
			return ImportAPIsResult{}, importItemError(index, err)
		}
		apis = append(apis, api)
	}

	result := ImportAPIsResult{Items: []APIView{}}
	for _, api := range apis {
		created, err := s.insertAPI(ctx, api, actorID)
		if err != nil {
			if errors.Is(err, resource.ErrDuplicate) {
				result.Skipped++
				continue
			}
			return ImportAPIsResult{}, err
		}
		result.Created++
		result.Items = append(result.Items, toAPIView(created))
	}
	return result, nil
}

// ---------- proposals ----------

// ProposedPageInput is the payload for proposing a page from code facts or
// network evidence (spec §8.1). Source is always ai for pages.
type ProposedPageInput struct {
	SUTID         int64
	Name          string
	URL           string
	RouteTemplate string
	PageType      string
	AuthRole      string
	Module        string
	Source        string
}

// ProposedAPIInput is the payload for proposing an API from code facts or
// network evidence. Source is ai or code.
type ProposedAPIInput struct {
	SUTID        int64
	Method       string
	PathTemplate string
	Name         string
	AuthRole     string
	SchemaRef    string
	Source       string
}

// CreateProposedPage inserts or refreshes one proposed page. Approved pages are
// never downgraded and rejected pages are not re-proposed (both are returned
// unchanged so the caller counts them as skipped).
func (s *Service) CreateProposedPage(ctx context.Context, input ProposedPageInput) (PageView, error) {
	sut, err := s.repo.GetSUT(ctx, input.SUTID)
	if err != nil {
		return PageView{}, mapRepoError(err, "sut", "")
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = resource.PageSourceAI
	}
	if source != resource.PageSourceAI {
		return PageView{}, platform.Invalid("source", "proposed pages must use the ai source")
	}
	routeTemplate := strings.TrimSpace(input.RouteTemplate)
	if routeTemplate == "" {
		derived, deriveErr := resource.DeriveRouteTemplate(input.URL)
		if deriveErr != nil {
			return PageView{}, deriveErr
		}
		routeTemplate = derived
	}
	pageType := strings.TrimSpace(input.PageType)
	if pageType == "" {
		pageType = "page"
	}
	at := s.clock.Now()
	page := resource.Page{
		SUTID:         sut.ID,
		Name:          input.Name,
		URL:           input.URL,
		RouteTemplate: routeTemplate,
		PageType:      pageType,
		AuthRole:      input.AuthRole,
		Module:        input.Module,
		Status:        resource.PageStatusProposed,
		Source:        source,
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	if err := page.Validate(); err != nil {
		return PageView{}, err
	}
	normalizedURL, err := resource.NormalizePageURL(page.URL)
	if err != nil {
		return PageView{}, err
	}

	existing, listErr := s.repo.ListPages(ctx, sut.ID)
	if listErr != nil {
		return PageView{}, listErr
	}
	for _, candidate := range existing {
		candidateURL, normalizeErr := resource.NormalizePageURL(candidate.URL)
		if normalizeErr != nil || candidateURL != normalizedURL {
			continue
		}
		if candidate.Status == resource.PageStatusProposed {
			page.ID = candidate.ID
			page.CreatedAt = candidate.CreatedAt
			updated, updateErr := s.repo.UpdatePage(ctx, page)
			if updateErr != nil {
				return PageView{}, mapPageError(updateErr)
			}
			s.record(ctx, 0, "resource.page.proposed", "page", platformid.Format(updated.ID), map[string]any{
				"sut_id":         platformid.Format(updated.SUTID),
				"route_template": updated.RouteTemplate,
				"source":         updated.Source,
			})
			return toPageView(updated), nil
		}
		return toPageView(candidate), nil
	}

	page.ID = s.ids.Next()
	created, createErr := s.repo.CreatePage(ctx, page)
	if createErr != nil {
		return PageView{}, mapPageError(createErr)
	}
	s.record(ctx, 0, "resource.page.proposed", "page", platformid.Format(created.ID), map[string]any{
		"sut_id":         platformid.Format(created.SUTID),
		"route_template": created.RouteTemplate,
		"source":         created.Source,
	})
	return toPageView(created), nil
}

// ConfirmPage turns a proposed page into an approved one.
func (s *Service) ConfirmPage(ctx context.Context, pageID, actorID int64) (PageView, error) {
	page, err := s.repo.GetPage(ctx, pageID)
	if err != nil {
		return PageView{}, mapRepoError(err, "page", "")
	}
	if page.Status != resource.PageStatusProposed {
		return PageView{}, platform.Conflict("page is not proposed")
	}
	page.Status = resource.PageStatusApproved
	page.UpdatedAt = s.clock.Now()
	updated, err := s.repo.UpdatePage(ctx, page)
	if err != nil {
		return PageView{}, mapPageError(err)
	}
	s.record(ctx, actorID, "resource.page.confirmed", "page", platformid.Format(updated.ID), map[string]any{
		"sut_id":         platformid.Format(updated.SUTID),
		"route_template": updated.RouteTemplate,
		"source":         updated.Source,
	})
	return toPageView(updated), nil
}

// RejectPage turns a proposed page into a rejected one.
func (s *Service) RejectPage(ctx context.Context, pageID, actorID int64) (PageView, error) {
	page, err := s.repo.GetPage(ctx, pageID)
	if err != nil {
		return PageView{}, mapRepoError(err, "page", "")
	}
	if page.Status != resource.PageStatusProposed {
		return PageView{}, platform.Conflict("page is not proposed")
	}
	page.Status = resource.PageStatusRejected
	page.UpdatedAt = s.clock.Now()
	updated, err := s.repo.UpdatePage(ctx, page)
	if err != nil {
		return PageView{}, mapPageError(err)
	}
	s.record(ctx, actorID, "resource.page.rejected", "page", platformid.Format(updated.ID), map[string]any{
		"sut_id":         platformid.Format(updated.SUTID),
		"route_template": updated.RouteTemplate,
		"source":         updated.Source,
	})
	return toPageView(updated), nil
}

// CreateProposedAPI inserts or refreshes one proposed API. Approved and rejected
// rows are returned unchanged (skipped).
func (s *Service) CreateProposedAPI(ctx context.Context, input ProposedAPIInput) (APIView, error) {
	sut, err := s.repo.GetSUT(ctx, input.SUTID)
	if err != nil {
		return APIView{}, mapRepoError(err, "sut", "")
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = resource.APISourceAI
	}
	if source != resource.APISourceAI && source != resource.APISourceCode {
		return APIView{}, platform.Invalid("source", "proposed APIs must use the ai or code source")
	}
	at := s.clock.Now()
	api := resource.API{
		SUTID:        sut.ID,
		Method:       input.Method,
		PathTemplate: input.PathTemplate,
		Name:         input.Name,
		AuthRole:     input.AuthRole,
		SchemaRef:    input.SchemaRef,
		Status:       resource.APIStatusProposed,
		Source:       source,
		CreatedAt:    at,
		UpdatedAt:    at,
	}
	if err := api.Validate(); err != nil {
		return APIView{}, err
	}

	existing, listErr := s.repo.ListAPIs(ctx, sut.ID)
	if listErr != nil {
		return APIView{}, listErr
	}
	for _, candidate := range existing {
		if candidate.Method != api.Method || candidate.PathTemplate != api.PathTemplate {
			continue
		}
		if candidate.Status == resource.APIStatusProposed {
			api.ID = candidate.ID
			api.CreatedAt = candidate.CreatedAt
			updated, updateErr := s.repo.UpdateAPI(ctx, api)
			if updateErr != nil {
				return APIView{}, mapRepoError(updateErr, "api", "api route already exists in this sut")
			}
			s.record(ctx, 0, "resource.api.proposed", "api", platformid.Format(updated.ID), map[string]any{
				"sut_id":        platformid.Format(updated.SUTID),
				"method":        updated.Method,
				"path_template": updated.PathTemplate,
				"source":        updated.Source,
			})
			return toAPIView(updated), nil
		}
		return toAPIView(candidate), nil
	}

	api.ID = s.ids.Next()
	created, createErr := s.repo.CreateAPI(ctx, api)
	if createErr != nil {
		return APIView{}, mapRepoError(createErr, "api", "api route already exists in this sut")
	}
	s.record(ctx, 0, "resource.api.proposed", "api", platformid.Format(created.ID), map[string]any{
		"sut_id":        platformid.Format(created.SUTID),
		"method":        created.Method,
		"path_template": created.PathTemplate,
		"source":        created.Source,
	})
	return toAPIView(created), nil
}

// ConfirmAPI turns a proposed API into an approved one.
func (s *Service) ConfirmAPI(ctx context.Context, apiID, actorID int64) (APIView, error) {
	api, err := s.repo.GetAPI(ctx, apiID)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "")
	}
	if api.Status != resource.APIStatusProposed {
		return APIView{}, platform.Conflict("api is not proposed")
	}
	api.Status = resource.APIStatusApproved
	api.UpdatedAt = s.clock.Now()
	updated, err := s.repo.UpdateAPI(ctx, api)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "api route already exists in this sut")
	}
	s.record(ctx, actorID, "resource.api.confirmed", "api", platformid.Format(updated.ID), map[string]any{
		"sut_id":        platformid.Format(updated.SUTID),
		"method":        updated.Method,
		"path_template": updated.PathTemplate,
		"source":        updated.Source,
	})
	return toAPIView(updated), nil
}

// RejectAPI turns a proposed API into a rejected one.
func (s *Service) RejectAPI(ctx context.Context, apiID, actorID int64) (APIView, error) {
	api, err := s.repo.GetAPI(ctx, apiID)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "")
	}
	if api.Status != resource.APIStatusProposed {
		return APIView{}, platform.Conflict("api is not proposed")
	}
	api.Status = resource.APIStatusRejected
	api.UpdatedAt = s.clock.Now()
	updated, err := s.repo.UpdateAPI(ctx, api)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "api route already exists in this sut")
	}
	s.record(ctx, actorID, "resource.api.rejected", "api", platformid.Format(updated.ID), map[string]any{
		"sut_id":        platformid.Format(updated.SUTID),
		"method":        updated.Method,
		"path_template": updated.PathTemplate,
		"source":        updated.Source,
	})
	return toAPIView(updated), nil
}

// ---------- AI proven-asset registration (v0.2.4-F P17, SC-F3) ----------

// RegisterAIPage registers a proven discovered page as source=ai/status=approved.
// The execution evidence gate calls it ONLY after a step used the page and its
// assertion passed (server verdict); this is the catalog write of that proof,
// not an approval gate (auto on proof). It is idempotent on the SUT's normalized
// page URL (the single page-identity authority): an existing approved page is
// returned unchanged, a proposed page is approved on the proof, and a REJECTED
// page revives ONLY on fresh proof — provenAt (the newest proving-evidence
// created_at, server-derived) must be later than the rejection timestamp;
// replayed old evidence fails closed and the row stays rejected (the human
// judgment point is preserved). Otherwise a new ai/approved page is created.
// Reuses buildPage (the page-construction authority) so validation/route
// derivation stay single-source.
func (s *Service) RegisterAIPage(ctx context.Context, sutID int64, input PageInput, actorID int64, provenAt time.Time) (PageView, error) {
	sut, err := s.repo.GetSUT(ctx, sutID)
	if err != nil {
		return PageView{}, mapRepoError(err, "sut", "")
	}
	page, err := s.buildPage(sut, input, resource.PageSourceAI)
	if err != nil {
		return PageView{}, err
	}
	normalizedURL, err := resource.NormalizePageURL(page.URL)
	if err != nil {
		return PageView{}, err
	}
	existing, err := s.repo.ListPages(ctx, sut.ID)
	if err != nil {
		return PageView{}, err
	}
	for _, candidate := range existing {
		candidateURL, normalizeErr := resource.NormalizePageURL(candidate.URL)
		if normalizeErr != nil || candidateURL != normalizedURL {
			continue
		}
		if candidate.Status == resource.PageStatusApproved {
			return toPageView(candidate), nil
		}
		if candidate.Status == resource.PageStatusRejected && !provenAt.After(candidate.UpdatedAt) {
			// A human reject is overturned only by FRESH proof (evidence created
			// after the rejection). UpdatedAt is the rejection timestamp (RejectPage
			// sets it; a later admin edit only raises the bar — conservative). A zero
			// provenAt is treated as old evidence: fail-closed.
			return PageView{}, rejectedRequiresFreshProofError("page")
		}
		page.ID = candidate.ID
		page.CreatedAt = candidate.CreatedAt
		updated, updateErr := s.repo.UpdatePage(ctx, page)
		if updateErr != nil {
			return PageView{}, mapPageError(updateErr)
		}
		s.record(ctx, actorID, "resource.page.registered", "page", platformid.Format(updated.ID), map[string]any{
			"sut_id":         platformid.Format(updated.SUTID),
			"route_template": updated.RouteTemplate,
			"source":         updated.Source,
		})
		return toPageView(updated), nil
	}
	created, err := s.insertPage(ctx, page, actorID)
	if err != nil {
		return PageView{}, mapPageError(err)
	}
	return toPageView(created), nil
}

// RegisterAIAPI registers a proven discovered API as source=ai/status=approved.
// Idempotent on (SUT, method, path_template): an existing approved API is returned
// unchanged, a proposed one is approved on the proof, and a REJECTED one revives
// only on fresh proof (provenAt later than the rejection timestamp; replayed old
// evidence fails closed, the row stays rejected). Otherwise a new ai/approved API
// is created. Reuses buildAPI (the API-construction authority).
func (s *Service) RegisterAIAPI(ctx context.Context, sutID int64, input APIInput, actorID int64, provenAt time.Time) (APIView, error) {
	sut, err := s.repo.GetSUT(ctx, sutID)
	if err != nil {
		return APIView{}, mapRepoError(err, "sut", "")
	}
	api, err := s.buildAPI(sut, input, resource.APISourceAI)
	if err != nil {
		return APIView{}, err
	}
	existing, err := s.repo.ListAPIs(ctx, sut.ID)
	if err != nil {
		return APIView{}, err
	}
	for _, candidate := range existing {
		if candidate.Method != api.Method || candidate.PathTemplate != api.PathTemplate {
			continue
		}
		if candidate.Status == resource.APIStatusApproved {
			return toAPIView(candidate), nil
		}
		if candidate.Status == resource.APIStatusRejected && !provenAt.After(candidate.UpdatedAt) {
			// Same revival rule as pages: only proof newer than the rejection
			// overturns a human reject; old evidence fails closed.
			return APIView{}, rejectedRequiresFreshProofError("api")
		}
		api.ID = candidate.ID
		api.CreatedAt = candidate.CreatedAt
		updated, updateErr := s.repo.UpdateAPI(ctx, api)
		if updateErr != nil {
			return APIView{}, mapRepoError(updateErr, "api", "api route already exists in this sut")
		}
		s.record(ctx, actorID, "resource.api.registered", "api", platformid.Format(updated.ID), map[string]any{
			"sut_id":        platformid.Format(updated.SUTID),
			"method":        updated.Method,
			"path_template": updated.PathTemplate,
			"source":        updated.Source,
		})
		return toAPIView(updated), nil
	}
	created, err := s.insertAPI(ctx, api, actorID)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "api route already exists in this sut")
	}
	return toAPIView(created), nil
}

// RegisterAIDatabase registers a proven discovered target database. The database
// lifecycle vocabulary is active/disabled (it has no proposed/approved workflow);
// the registration is recorded as source=ai/gate_result=approved in the execution
// asset_registration fact, while the catalog row is active. Idempotent on
// (SUT, name) ONLY when the functional identity matches: a database's identity is
// its DSN (+username) — a same-name row with different connection facts is NOT
// the asset the proof touched and is a 409 identity conflict (never silently
// returned as a successful registration). A disabled same-identity row is an
// explicit 409 (registration does not reactivate a disabled database). Reuses the
// TargetDatabase domain validation.
func (s *Service) RegisterAIDatabase(ctx context.Context, sutID int64, input DatabaseInput, actorID int64) (DatabaseView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return DatabaseView{}, mapRepoError(err, "sut", "")
	}
	readOnly := true
	if input.ReadOnly != nil {
		readOnly = *input.ReadOnly
	}
	at := s.clock.Now()
	database := resource.TargetDatabase{
		SUTID:     sutID,
		Name:      input.Name,
		Kind:      "postgres",
		DSN:       input.DSN,
		Username:  input.Username,
		Password:  input.Password,
		ReadOnly:  readOnly,
		Status:    resource.StatusActive,
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := database.Validate(); err != nil {
		return DatabaseView{}, err
	}
	existing, err := s.repo.ListDatabases(ctx, sutID)
	if err != nil {
		return DatabaseView{}, err
	}
	for _, candidate := range existing {
		// EqualFold mirrors the (sut_id, name) unique key of the schema.
		if !strings.EqualFold(candidate.Name, database.Name) {
			continue
		}
		if candidate.DSN != database.DSN || candidate.Username != database.Username {
			return DatabaseView{}, platform.NewError(http.StatusConflict, "database_identity_conflict",
				"a database with this name already exists in this sut with a different DSN or username; the proof does not match the registered database")
		}
		if candidate.Status != resource.StatusActive {
			return DatabaseView{}, platform.NewError(http.StatusConflict, "database_disabled",
				"the registered database is disabled; registration does not reactivate it")
		}
		return toDatabaseView(candidate), nil
	}
	database.ID = s.ids.Next()
	created, err := s.repo.CreateDatabase(ctx, database)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "database name already exists in this sut")
	}
	s.record(ctx, actorID, "resource.database.registered", "database", platformid.Format(created.ID), map[string]any{
		"sut_id": platformid.Format(created.SUTID),
		"name":   created.Name,
	})
	return toDatabaseView(created), nil
}

// rejectedRequiresFreshProofError is the fail-closed outcome of replaying old
// evidence against a rejected asset: the rejection stands until proof newer than
// the rejection timestamp arrives (the human judgment point is preserved).
func rejectedRequiresFreshProofError(kind string) error {
	err := platform.NewError(http.StatusConflict, "rejected_requires_fresh_proof",
		kind+" was rejected; reviving it requires evidence created after the rejection (replayed old evidence does not overturn a reject)")
	err.Details = map[string]any{"kind": kind, "reason": "old_evidence"}
	return err
}

func importItemError(index int, err error) error {
	details := map[string]any{"field": "items", "index": index}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		details["reason"] = apiErr.Message
		if field, ok := apiErr.Details["field"]; ok {
			details["item_field"] = field
		}
	}
	return &platform.Error{
		Status:  400,
		Code:    "invalid_argument",
		Message: "import item is invalid",
		Details: details,
	}
}

// ---------- target databases ----------

// CreateDatabase stores a target database under a SUT.
func (s *Service) CreateDatabase(ctx context.Context, sutID int64, input DatabaseInput, actorID int64) (DatabaseView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return DatabaseView{}, mapRepoError(err, "sut", "")
	}
	readOnly := true
	if input.ReadOnly != nil {
		readOnly = *input.ReadOnly
	}
	at := s.clock.Now()
	database := resource.TargetDatabase{
		SUTID:     sutID,
		Name:      input.Name,
		Kind:      "postgres",
		DSN:       input.DSN,
		Username:  input.Username,
		Password:  input.Password,
		ReadOnly:  readOnly,
		Status:    defaultStatus(input.Status),
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := database.Validate(); err != nil {
		return DatabaseView{}, err
	}
	database.ID = s.ids.Next()
	created, err := s.repo.CreateDatabase(ctx, database)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "database name already exists in this sut")
	}
	s.record(ctx, actorID, "resource.database.created", "database", platformid.Format(created.ID), map[string]any{
		"sut_id": platformid.Format(created.SUTID),
		"name":   created.Name,
	})
	return toDatabaseView(created), nil
}

// UpdateDatabase applies a partial target-database update. A nil password keeps
// the stored value.
func (s *Service) UpdateDatabase(ctx context.Context, id int64, patch DatabasePatch, actorID int64) (DatabaseView, error) {
	database, err := s.repo.GetDatabase(ctx, id)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "")
	}
	if patch.Name != nil {
		database.Name = *patch.Name
	}
	if patch.DSN != nil {
		database.DSN = *patch.DSN
	}
	if patch.Username != nil {
		database.Username = *patch.Username
	}
	if patch.Password != nil {
		database.Password = *patch.Password
	}
	if patch.ReadOnly != nil {
		database.ReadOnly = *patch.ReadOnly
	}
	if patch.Status != nil {
		database.Status = *patch.Status
	}
	database.Kind = "postgres"
	database.UpdatedAt = s.clock.Now()
	if err := database.Validate(); err != nil {
		return DatabaseView{}, err
	}
	updated, err := s.repo.UpdateDatabase(ctx, database)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "database name already exists in this sut")
	}
	s.record(ctx, actorID, "resource.database.updated", "database", platformid.Format(updated.ID), map[string]any{
		"sut_id": platformid.Format(updated.SUTID),
		"name":   updated.Name,
	})
	return toDatabaseView(updated), nil
}

// DeleteDatabase deletes a target database when no version binding references it.
func (s *Service) DeleteDatabase(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.DatabaseInUse(ctx, id)
	}, "database", id); err != nil {
		return err
	}
	if err := s.repo.DeleteDatabase(ctx, id); err != nil {
		return mapRepoError(err, "database", "")
	}
	s.record(ctx, actorID, "resource.database.deleted", "database", platformid.Format(id), nil)
	return nil
}

// ListDatabases returns every target database of a SUT.
func (s *Service) ListDatabases(ctx context.Context, sutID int64) ([]DatabaseView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return nil, mapRepoError(err, "sut", "")
	}
	databases, err := s.repo.ListDatabases(ctx, sutID)
	if err != nil {
		return nil, err
	}
	views := make([]DatabaseView, 0, len(databases))
	for _, database := range databases {
		views = append(views, toDatabaseView(database))
	}
	return views, nil
}

// GetDatabase returns one target database.
func (s *Service) GetDatabase(ctx context.Context, id int64) (DatabaseView, error) {
	database, err := s.repo.GetDatabase(ctx, id)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "")
	}
	return toDatabaseView(database), nil
}

// ---------- test accounts ----------

// CreateAccount stores a test account in the global pool. The optional SUT
// affinity is recorded but never used for a decision in this release.
func (s *Service) CreateAccount(ctx context.Context, input AccountInput, actorID int64) (AccountView, error) {
	if input.SUTID != nil {
		if _, err := s.repo.GetSUT(ctx, *input.SUTID); err != nil {
			return AccountView{}, mapRepoError(err, "sut", "")
		}
	}
	roles := input.Roles
	if roles == nil {
		roles = []string{}
	}
	at := s.clock.Now()
	account := resource.TestAccount{
		SUTID:     input.SUTID,
		Username:  input.Username,
		Password:  input.Password,
		Roles:     roles,
		Notes:     input.Notes,
		Status:    defaultStatus(input.Status),
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := account.Validate(); err != nil {
		return AccountView{}, err
	}
	account.ID = s.ids.Next()
	created, err := s.repo.CreateAccount(ctx, account)
	if err != nil {
		return AccountView{}, mapRepoError(err, "account", "account username already exists")
	}
	s.record(ctx, actorID, "resource.account.created", "account", platformid.Format(created.ID), map[string]any{
		"username": created.Username,
	})
	return toAccountView(created), nil
}

// UpdateAccount applies a partial test-account update. A nil password keeps the
// stored password and a nil SUTID keeps the stored affinity.
func (s *Service) UpdateAccount(ctx context.Context, id int64, patch AccountPatch, actorID int64) (AccountView, error) {
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return AccountView{}, mapRepoError(err, "account", "")
	}
	previousRoles := append([]string(nil), account.Roles...)
	if patch.SUTID != nil {
		if _, err := s.repo.GetSUT(ctx, *patch.SUTID); err != nil {
			return AccountView{}, mapRepoError(err, "sut", "")
		}
		account.SUTID = patch.SUTID
	}
	if patch.Username != nil {
		account.Username = *patch.Username
	}
	if patch.Password != nil {
		account.Password = *patch.Password
	}
	if patch.Roles != nil {
		account.Roles = *patch.Roles
	}
	if patch.Notes != nil {
		account.Notes = *patch.Notes
	}
	if patch.Status != nil {
		account.Status = *patch.Status
	}
	account.UpdatedAt = s.clock.Now()
	if err := account.ValidateUpdate(); err != nil {
		return AccountView{}, err
	}
	updated, err := s.repo.UpdateAccount(ctx, account)
	if err != nil {
		return AccountView{}, mapRepoError(err, "account", "account username already exists")
	}
	s.record(ctx, actorID, "resource.account.updated", "account", platformid.Format(updated.ID), map[string]any{
		"username": updated.Username,
	})
	// Only roles removed by the edit invalidate knowledge: a login recipe is
	// bound to the account_role, and a newly added role has no knowledge yet.
	// The account is global and has no system/environment binding, so those
	// dimensions are omitted to keep the invalidation scoped to the roles.
	if removed := removedStrings(previousRoles, updated.Roles); len(removed) > 0 {
		s.invalidateKnowledge(ctx, KnowledgeBindingChange{
			AccountRoles: removed,
			Reason:       "account roles changed",
		})
	}
	return toAccountView(updated), nil
}

// DeleteAccount deletes a test account when no version binding references it.
func (s *Service) DeleteAccount(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.AccountInUse(ctx, id)
	}, "account", id); err != nil {
		return err
	}
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return mapRepoError(err, "account", "")
	}
	if err := s.repo.DeleteAccount(ctx, id); err != nil {
		return mapRepoError(err, "account", "")
	}
	s.record(ctx, actorID, "resource.account.deleted", "account", platformid.Format(id), nil)
	if roles := removedStrings(account.Roles, nil); len(roles) > 0 {
		s.invalidateKnowledge(ctx, KnowledgeBindingChange{
			AccountRoles: roles,
			Reason:       "account deleted",
		})
	}
	return nil
}

// ListAccounts returns the global test-account pool (v0.22 DB-15). The pool is
// not SUT-owned, so the optional affinity is a property of each row, not a
// filter.
func (s *Service) ListAccounts(ctx context.Context) ([]AccountView, error) {
	return s.ListAllAccounts(ctx)
}

// ListAllAccounts returns the global test-account pool ordered by ID.
func (s *Service) ListAllAccounts(ctx context.Context) ([]AccountView, error) {
	accounts, err := s.repo.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]AccountView, 0, len(accounts))
	for _, account := range accounts {
		views = append(views, toAccountView(account))
	}
	return views, nil
}

// GetAccount returns one test account.
func (s *Service) GetAccount(ctx context.Context, id int64) (AccountView, error) {
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return AccountView{}, mapRepoError(err, "account", "")
	}
	return toAccountView(account), nil
}

// GetAccountSecret returns the stored username/password of one test account.
// It is an application-only port for the execution context (P3 §6.6) and is
// never exposed through HTTP or logged.
func (s *Service) GetAccountSecret(ctx context.Context, accountID int64) (username, password string, err error) {
	if accountID <= 0 {
		return "", "", platform.Invalid("account_id", "must be a positive decimal ID")
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return "", "", mapRepoError(err, "account", "")
	}
	return account.Username, account.Password, nil
}

// GetDatabaseSecret returns the stored DSN/username/password of one target
// database. It is an application-only port for the execution context (P3 §6.5)
// and is never exposed through HTTP or logged.
func (s *Service) GetDatabaseSecret(ctx context.Context, databaseID int64) (dsn, username, password string, err error) {
	if databaseID <= 0 {
		return "", "", "", platform.Invalid("database_id", "must be a positive decimal ID")
	}
	database, err := s.repo.GetDatabase(ctx, databaseID)
	if err != nil {
		return "", "", "", mapRepoError(err, "database", "")
	}
	return database.DSN, database.Username, database.Password, nil
}

// ---------- code repositories ----------

// CreateRepository stores a code repository. Any authenticated user may create
// one; CreatedBy records the actor.
func (s *Service) CreateRepository(ctx context.Context, input RepositoryInput, actorID int64) (CodeRepositoryView, error) {
	include := input.Include
	if include == nil {
		include = []string{}
	}
	exclude := input.Exclude
	if exclude == nil {
		exclude = []string{}
	}
	at := s.clock.Now()
	repository := resource.CodeRepository{
		Name:      input.Name,
		Path:      input.Path,
		Branch:    input.Branch,
		Include:   include,
		Exclude:   exclude,
		CreatedBy: actorID,
		Status:    defaultStatus(input.Status),
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := repository.Validate(); err != nil {
		return CodeRepositoryView{}, err
	}
	repository.ID = s.ids.Next()
	created, err := s.repo.CreateRepository(ctx, repository)
	if err != nil {
		return CodeRepositoryView{}, mapRepoError(err, "code repository", "repository name already exists")
	}
	s.record(ctx, actorID, "resource.code_repo.created", "code_repository", platformid.Format(created.ID), map[string]any{
		"name": created.Name,
	})
	return toCodeRepositoryView(created), nil
}

// UpdateRepository applies a partial code-repository update.
func (s *Service) UpdateRepository(ctx context.Context, id int64, patch RepositoryPatch, actorID int64) (CodeRepositoryView, error) {
	repository, err := s.repo.GetRepository(ctx, id)
	if err != nil {
		return CodeRepositoryView{}, mapRepoError(err, "code repository", "")
	}
	if patch.Name != nil {
		repository.Name = *patch.Name
	}
	if patch.Path != nil {
		repository.Path = *patch.Path
	}
	if patch.Branch != nil {
		repository.Branch = *patch.Branch
	}
	if patch.Include != nil {
		repository.Include = *patch.Include
	}
	if patch.Exclude != nil {
		repository.Exclude = *patch.Exclude
	}
	if patch.Status != nil {
		repository.Status = *patch.Status
	}
	repository.UpdatedAt = s.clock.Now()
	if err := repository.Validate(); err != nil {
		return CodeRepositoryView{}, err
	}
	updated, err := s.repo.UpdateRepository(ctx, repository)
	if err != nil {
		return CodeRepositoryView{}, mapRepoError(err, "code repository", "repository name already exists")
	}
	s.record(ctx, actorID, "resource.code_repo.updated", "code_repository", platformid.Format(updated.ID), map[string]any{
		"name": updated.Name,
	})
	return toCodeRepositoryView(updated), nil
}

// DeleteRepository deletes a code repository when no SUT association references
// it.
func (s *Service) DeleteRepository(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.RepositoryInUse(ctx, id)
	}, "repository", id); err != nil {
		return err
	}
	if err := s.repo.DeleteRepository(ctx, id); err != nil {
		return mapRepoError(err, "code repository", "")
	}
	s.record(ctx, actorID, "resource.code_repo.deleted", "code_repository", platformid.Format(id), nil)
	return nil
}

// ListRepositories returns one page of code repositories ordered by ID
// descending.
func (s *Service) ListRepositories(ctx context.Context, limit int, beforeID *int64) ([]CodeRepositoryView, bool, error) {
	limit = normalizeLimit(limit)
	items, err := s.repo.ListRepositories(ctx, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	views := make([]CodeRepositoryView, 0, len(items))
	for _, item := range items {
		views = append(views, toCodeRepositoryView(item))
	}
	return views, hasMore, nil
}

// GetRepository returns one code repository.
func (s *Service) GetRepository(ctx context.Context, id int64) (CodeRepositoryView, error) {
	repository, err := s.repo.GetRepository(ctx, id)
	if err != nil {
		return CodeRepositoryView{}, mapRepoError(err, "code repository", "")
	}
	return toCodeRepositoryView(repository), nil
}

// ListSUTRepositories returns the SUT-level repositories in deterministic
// repository-ID order (v0.22 R-C2/API-17).
func (s *Service) ListSUTRepositories(ctx context.Context, sutID int64) ([]SUTRepositoryView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return nil, mapRepoError(err, "sut", "")
	}
	associations, err := s.repo.ListSUTRepositories(ctx, sutID)
	if err != nil {
		return nil, err
	}
	return s.toSUTRepositoryViews(ctx, associations)
}

// ListEnvironmentRepositories returns the environment code pool (v0.2.4-B
// GD-03): the union of the repositories of every APPROVED SUT in the environment,
// de-duplicated by repository ID and ordered by repository ID so the
// deterministic baseline repository (the lowest ID) is stable. Disabled
// associations, inactive repositories and non-approved SUTs (R-B4) do not
// contribute, so an unapproved SUT can never supply pool[0]/the code baseline.
//
// H-B1 (v0.2.4-H): the SUT gate is applied HERE through the single
// resourceapp.SUTView.IsApproved predicate, not in the adapter's SQL. The
// repository read is raw catalog state, so the rule has one implementation and
// every reader of the pool (version binding, handoff, codeintel) inherits it.
func (s *Service) ListEnvironmentRepositories(ctx context.Context, environmentID int64) ([]SUTRepositoryView, error) {
	if environmentID <= 0 {
		return nil, platform.Invalid("environment_id", "must be a positive decimal ID")
	}
	if _, err := s.repo.GetEnvironment(ctx, environmentID); err != nil {
		return nil, mapRepoError(err, "environment", "")
	}
	suts, err := s.repo.ListSUTsByEnvironment(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	approved := make(map[int64]struct{}, len(suts))
	for _, sut := range suts {
		if toSUTView(sut).IsApproved() {
			approved[sut.ID] = struct{}{}
		}
	}
	associations, err := s.repo.ListEnvironmentSUTRepositories(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	pool := make([]resource.SUTRepository, 0, len(associations))
	decided := make(map[int64]struct{}, len(associations))
	for _, association := range associations {
		if _, ok := approved[association.SUTID]; !ok {
			continue
		}
		if !association.Enabled {
			continue
		}
		if _, seen := decided[association.RepoID]; seen {
			continue
		}
		repository, err := s.repo.GetRepository(ctx, association.RepoID)
		if err != nil {
			return nil, mapRepoError(err, "code repository", "")
		}
		decided[association.RepoID] = struct{}{}
		if repository.Status != resource.StatusActive {
			continue
		}
		pool = append(pool, association)
	}
	return s.toSUTRepositoryViews(ctx, pool)
}

func (s *Service) toSUTRepositoryViews(ctx context.Context, associations []resource.SUTRepository) ([]SUTRepositoryView, error) {
	views := make([]SUTRepositoryView, 0, len(associations))
	for _, association := range associations {
		repository, err := s.repo.GetRepository(ctx, association.RepoID)
		if err != nil {
			return nil, mapRepoError(err, "code repository", "")
		}
		views = append(views, toSUTRepositoryView(association, repository))
	}
	return views, nil
}

// AddSUTRepository associates one global code repository with a SUT. The
// (sut_id, repo_id) natural key is unique; an unknown repository is a 404.
func (s *Service) AddSUTRepository(ctx context.Context, sutID int64, input SUTRepositoryInput, actorID int64) (SUTRepositoryView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return SUTRepositoryView{}, mapRepoError(err, "sut", "")
	}
	if input.RepositoryID <= 0 {
		return SUTRepositoryView{}, platform.Invalid("repository_id", "must be a positive decimal ID")
	}
	repository, err := s.repo.GetRepository(ctx, input.RepositoryID)
	if err != nil {
		return SUTRepositoryView{}, mapRepoError(err, "code repository", "")
	}
	at := s.clock.Now()
	association := resource.SUTRepository{
		ID:        s.ids.Next(),
		SUTID:     sutID,
		RepoID:    input.RepositoryID,
		Role:      strings.TrimSpace(input.Role),
		Enabled:   true,
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := association.Validate(); err != nil {
		return SUTRepositoryView{}, err
	}
	created, err := s.repo.CreateSUTRepository(ctx, association)
	if err != nil {
		return SUTRepositoryView{}, mapRepoError(err, "sut repository", "repository is already associated with this sut")
	}
	s.record(ctx, actorID, "resource.sut_repository.created", "sut_repository",
		platformid.Format(created.ID), map[string]any{
			"sut_id":  platformid.Format(created.SUTID),
			"repo_id": platformid.Format(created.RepoID),
			"role":    created.Role,
		})
	return toSUTRepositoryView(created, repository), nil
}

// SetSUTRepositoryBaselineCommit pins the resolved code baseline of one
// (SUT, repository) pair. An empty commit clears it back to unresolved. It is
// the write path the version binding resolution and the code index backfill
// share; the malformed-SHA guard keeps the application and the database CHECK
// constraint in agreement.
func (s *Service) SetSUTRepositoryBaselineCommit(ctx context.Context, sutID, repoID int64, commitSHA string) error {
	if sutID <= 0 {
		return platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	if repoID <= 0 {
		return platform.Invalid("repo_id", "must be a positive decimal ID")
	}
	commitSHA = strings.TrimSpace(commitSHA)
	if err := (resource.SUTRepository{SUTID: sutID, RepoID: repoID, CodeBaselineCommit: commitSHA}).Validate(); err != nil {
		return err
	}
	if err := s.repo.SetSUTRepositoryBaselineCommit(ctx, sutID, repoID, commitSHA); err != nil {
		return mapRepoError(err, "sut repository", "")
	}
	s.record(ctx, 0, "resource.sut_repository.baseline_set", "sut_repository", platformid.Format(sutID), map[string]any{
		"sut_id":     platformid.Format(sutID),
		"repo_id":    platformid.Format(repoID),
		"commit_sha": commitSHA,
	})
	return nil
}

// ---------- catalog ----------

// Catalog returns every environment with its full nested SUT and asset tree,
// ordered by environment ID ascending.
func (s *Service) Catalog(ctx context.Context) ([]EnvironmentView, error) {
	graphs, err := s.repo.ListCatalog(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]EnvironmentView, 0, len(graphs))
	for _, graph := range graphs {
		views = append(views, toEnvironmentDetailView(graph))
	}
	return views, nil
}

var (
	_ CatalogReader               = (*Service)(nil)
	_ SUTRepositoryBaselineWriter = (*Service)(nil)
)

// ---------- audit, events and errors ----------

// record writes the best-effort audit entry and system event for a mutation.
func (s *Service) record(ctx context.Context, actorID int64, action, kind, targetID string, metadata map[string]any) {
	payload := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		payload[key] = value
	}
	payload["id"] = targetID

	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	requestID := requestid.From(ctx)

	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    actor,
			Action:     action,
			TargetKind: kind,
			TargetID:   targetID,
			RequestID:  requestID,
			Metadata:   payload,
		})
	}
	if s.events != nil {
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   "system",
			Type:      action,
			Scope:     map[string]any{},
			Payload:   payload,
			ActorID:   actor,
			RequestID: requestID,
		})
	}
}

// maxReferencingVersions caps the version list carried by a 409 conflict. The
// most useful references come first (the version repository orders by ID), so a
// long list never bloats the error envelope.
const maxReferencingVersions = 20

// checkUsage runs a usage guard; a nil guard means "not referenced". When the
// resource is referenced, the version context resolves the referencing versions
// so the 409 carries stable, actionable details instead of a bare conflict.
func (s *Service) checkUsage(ctx context.Context, check func(context.Context) (bool, error), kind string, id int64) error {
	if s.usage == nil {
		return nil
	}
	inUse, err := check(ctx)
	if err != nil {
		return err
	}
	if !inUse {
		return nil
	}
	refs, err := s.usage.ReferencingVersions(ctx, kind, id)
	if err != nil {
		return err
	}
	return resourceReferencedError(kind, id, refs)
}

// resourceReferencedError builds the stable 409 returned when a delete is
// blocked by a version binding. The message stays English (clients localize by
// code/details); referencing_versions is capped and de-duplicated by version ID,
// and every entry carries the version status/stage plus whether the binding is
// still editable so the client never suggests an impossible edit.
func resourceReferencedError(kind string, id int64, refs []VersionRef) error {
	apiErr := platform.NewError(http.StatusConflict, "resource_referenced", kind+" is referenced by a version binding")
	apiErr.Details = map[string]any{
		"kind":                 kind,
		"resource_id":          platformid.Format(id),
		"referencing_versions": referencingVersionDetails(refs),
		"resolution":           resourceReferencedResolution(kind, refs),
	}
	return apiErr
}

// referencingVersionDetails projects the version references into JSON-ready
// details: at most maxReferencingVersions entries, de-duplicated by version ID,
// preserving the caller's order. An empty result is a non-nil empty slice so
// the envelope always carries an array.
func referencingVersionDetails(refs []VersionRef) []map[string]any {
	out := make([]map[string]any, 0, maxReferencingVersions)
	seen := make(map[int64]struct{}, len(refs))
	for _, ref := range refs {
		if len(out) >= maxReferencingVersions {
			break
		}
		if _, duplicate := seen[ref.ID]; duplicate {
			continue
		}
		seen[ref.ID] = struct{}{}
		out = append(out, map[string]any{
			"id":       platformid.Format(ref.ID),
			"title":    ref.Title,
			"status":   ref.Status,
			"stage":    ref.Stage,
			"editable": ref.Editable,
		})
	}
	return out
}

// resourceReferencedResolution is the human-resolution hint for one resource
// kind: what the operator can change so the delete is no longer blocked. Only
// editable versions can be rebound; executing/reporting/completed versions are
// read-only, so the hint names the administrator/operations path instead of
// suggesting an edit those versions reject.
func resourceReferencedResolution(kind string, refs []VersionRef) string {
	editable, terminal := 0, 0
	for _, ref := range refs {
		if ref.Editable {
			editable++
		} else {
			terminal++
		}
	}

	var hint string
	switch kind {
	case "environment":
		hint = "Delete the referencing versions or bind another environment in editable versions."
	case "sut":
		hint = "Delete the referencing versions or bind an environment without this SUT in editable versions."
	case "page":
		hint = "Delete this page or bind an environment without its SUT in editable versions (created through case_review)."
	case "api":
		hint = "Delete this API or bind an environment without its SUT in editable versions (created through case_review)."
	case "database":
		hint = "Deactivate or delete this database, or bind an environment without its SUT in editable versions."
	case "account":
		hint = "Disable this account in editable versions."
	case "repository":
		hint = "Remove the SUT repository association in editable versions."
	default:
		hint = "Remove this resource from the bindings of editable versions."
	}

	if terminal == 0 {
		return hint
	}
	if editable == 0 {
		return "All referencing versions are read-only (executing, reporting or completed); removing this " +
			kind + " from their scope requires an administrator or operations process."
	}
	return hint + " Executing, reporting and completed versions are read-only; terminal references require an administrator or operations process."
}

// removedStrings returns the trimmed values of before that are absent from
// after, in their original order. It feeds the account-role invalidation: only
// roles genuinely removed by an edit (or all roles of a deleted account) can
// leave knowledge pointing at a surface that no longer exists.
func removedStrings(before, after []string) []string {
	kept := make(map[string]struct{}, len(after))
	for _, value := range after {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			kept[trimmed] = struct{}{}
		}
	}
	out := make([]string, 0, len(before))
	seen := make(map[string]struct{}, len(before))
	for _, value := range before {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, present := kept[trimmed]; present {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// mapPageError maps page write failures. A normalized-URL collision is a
// field-level 409 so clients can highlight the URL input instead of showing a
// generic conflict.
func mapPageError(err error) error {
	if errors.Is(err, resource.ErrPageURLDuplicate) {
		apiErr := platform.NewError(http.StatusConflict, "page_url_exists", "page url already exists in this sut")
		apiErr.Details = map[string]any{"field": "url"}
		return apiErr
	}
	return mapRepoError(err, "page", "page already exists in this sut")
}

func mapRepoError(err error, kind, conflictMessage string) error {
	switch {
	case errors.Is(err, resource.ErrNotFound):
		return platform.NotFound(kind)
	case errors.Is(err, resource.ErrDuplicate):
		if conflictMessage == "" {
			conflictMessage = kind + " already exists"
		}
		return platform.Conflict(conflictMessage)
	default:
		return err
	}
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return pagination.DefaultLimit
	}
	if limit > pagination.MaxLimit {
		return pagination.MaxLimit
	}
	return limit
}

// ---------- view projections ----------

func toEnvironmentView(environment resource.Environment, suts []SUTView) EnvironmentView {
	if suts == nil {
		suts = []SUTView{}
	}
	return EnvironmentView{
		ID:          platformid.Format(environment.ID),
		Name:        environment.Name,
		Description: environment.Description,
		Status:      environment.Status,
		SUTs:        suts,
		CreatedAt:   environment.CreatedAt,
		UpdatedAt:   environment.UpdatedAt,
	}
}

func toSUTView(sut resource.SUT) SUTView {
	return SUTView{
		ID:            platformid.Format(sut.ID),
		EnvironmentID: platformid.Format(sut.EnvironmentID),
		Name:          sut.Name,
		Intro:         sut.Intro,
		BaseURL:       sut.BaseURL,
		Family:        sut.Family,
		Source:        sut.Source,
		Status:        sut.Status,
		Pages:         []PageView{},
		APIs:          []APIView{},
		Databases:     []DatabaseView{},
		CreatedAt:     sut.CreatedAt,
		UpdatedAt:     sut.UpdatedAt,
	}
}

func toSUTRegistrationView(registration resource.SUTRegistration) SUTRegistrationView {
	evidence := registration.EvidenceRefs
	if evidence == nil {
		evidence = []string{}
	}
	return SUTRegistrationView{
		ID:               platformid.Format(registration.ID),
		SUTID:            platformid.Format(registration.SUTID),
		Source:           registration.Source,
		EvidenceRefs:     evidence,
		ResolutionMethod: registration.ResolutionMethod,
		GateResult:       registration.GateResult,
		CreatedAt:        registration.CreatedAt,
		CreatedBy:        registration.CreatedBy,
	}
}

// environmentSUTViews projects one environment graph into its SUT catalog, each
// SUT carrying only the assets it owns.
func environmentSUTViews(graph resource.EnvironmentGraph) []SUTView {
	pagesBySUT := make(map[int64][]PageView, len(graph.SUTs))
	for _, page := range graph.Pages {
		pagesBySUT[page.SUTID] = append(pagesBySUT[page.SUTID], toPageView(page))
	}
	apisBySUT := make(map[int64][]APIView, len(graph.SUTs))
	for _, api := range graph.APIs {
		apisBySUT[api.SUTID] = append(apisBySUT[api.SUTID], toAPIView(api))
	}
	databasesBySUT := make(map[int64][]DatabaseView, len(graph.SUTs))
	for _, database := range graph.Databases {
		databasesBySUT[database.SUTID] = append(databasesBySUT[database.SUTID], toDatabaseView(database))
	}
	views := make([]SUTView, 0, len(graph.SUTs))
	for _, sut := range graph.SUTs {
		view := toSUTView(sut)
		view.Pages = nonNilPages(pagesBySUT[sut.ID])
		view.APIs = nonNilAPIs(apisBySUT[sut.ID])
		view.Databases = nonNilDatabases(databasesBySUT[sut.ID])
		views = append(views, view)
	}
	return views
}

func nonNilPages(views []PageView) []PageView {
	if views == nil {
		return []PageView{}
	}
	return views
}

func nonNilAPIs(views []APIView) []APIView {
	if views == nil {
		return []APIView{}
	}
	return views
}

func nonNilDatabases(views []DatabaseView) []DatabaseView {
	if views == nil {
		return []DatabaseView{}
	}
	return views
}

func toEnvironmentDetailView(graph resource.EnvironmentGraph) EnvironmentView {
	return toEnvironmentView(graph.Environment, environmentSUTViews(graph))
}

func toPageView(page resource.Page) PageView {
	return PageView{
		ID:            platformid.Format(page.ID),
		SUTID:         platformid.Format(page.SUTID),
		Name:          page.Name,
		URL:           page.URL,
		RouteTemplate: page.RouteTemplate,
		PageType:      page.PageType,
		AuthRole:      page.AuthRole,
		Module:        page.Module,
		Status:        page.Status,
		Source:        page.Source,
		CreatedAt:     page.CreatedAt,
		UpdatedAt:     page.UpdatedAt,
	}
}

func toAPIView(api resource.API) APIView {
	return APIView{
		ID:           platformid.Format(api.ID),
		SUTID:        platformid.Format(api.SUTID),
		Method:       api.Method,
		PathTemplate: api.PathTemplate,
		Name:         api.Name,
		AuthRole:     api.AuthRole,
		Source:       api.Source,
		Status:       api.Status,
		SchemaRef:    api.SchemaRef,
		CreatedAt:    api.CreatedAt,
		UpdatedAt:    api.UpdatedAt,
	}
}

func toDatabaseView(database resource.TargetDatabase) DatabaseView {
	return DatabaseView{
		ID:          platformid.Format(database.ID),
		SUTID:       platformid.Format(database.SUTID),
		Name:        database.Name,
		DSN:         database.DSN,
		Username:    database.Username,
		PasswordSet: database.Password != "",
		ReadOnly:    database.ReadOnly,
		Status:      database.Status,
		CreatedAt:   database.CreatedAt,
		UpdatedAt:   database.UpdatedAt,
	}
}

func toAccountView(account resource.TestAccount) AccountView {
	roles := account.Roles
	if roles == nil {
		roles = []string{}
	}
	view := AccountView{
		ID:          platformid.Format(account.ID),
		Username:    account.Username,
		Roles:       roles,
		Notes:       account.Notes,
		Status:      account.Status,
		PasswordSet: account.Password != "",
		CreatedAt:   account.CreatedAt,
		UpdatedAt:   account.UpdatedAt,
	}
	if account.SUTID != nil {
		view.SUTID = platformid.Format(*account.SUTID)
	}
	return view
}

func toSUTRepositoryView(item resource.SUTRepository, repository resource.CodeRepository) SUTRepositoryView {
	return SUTRepositoryView{
		ID:                 platformid.Format(item.ID),
		SUTID:              platformid.Format(item.SUTID),
		Repository:         toCodeRepositoryView(repository),
		Role:               item.Role,
		CodeBaselineCommit: item.CodeBaselineCommit,
		Enabled:            item.Enabled,
		CreatedAt:          item.CreatedAt,
		UpdatedAt:          item.UpdatedAt,
	}
}

func toCodeRepositoryView(repository resource.CodeRepository) CodeRepositoryView {
	include := repository.Include
	if include == nil {
		include = []string{}
	}
	exclude := repository.Exclude
	if exclude == nil {
		exclude = []string{}
	}
	return CodeRepositoryView{
		ID:        platformid.Format(repository.ID),
		Name:      repository.Name,
		Path:      repository.Path,
		Branch:    repository.Branch,
		Include:   include,
		Exclude:   exclude,
		CreatedBy: repository.CreatedBy,
		Status:    repository.Status,
		CreatedAt: repository.CreatedAt,
		UpdatedAt: repository.UpdatedAt,
	}
}

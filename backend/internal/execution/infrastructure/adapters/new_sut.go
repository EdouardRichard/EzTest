package adapters

import (
	"context"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// NewSUTAdapter is the execution->resource cross-context write port of the R-B4
// new-SUT evidence gate (v0.2.4-F, SC-F4). After the gate decides the outcome, it
// registers a source=ai SUT inside the version's bound environment through the
// resource application service — the single SUT-create authority — which writes the
// SUT and its append-only sut_registration birth fact in one transaction. The
// execution context never writes resource tables directly (constitution art.5).
//
// The version's bound environment is resolved from the version read model (the model
// never picks the environment; it is injected by the version binding), so a new SUT
// always lands in the environment the executing version is bound to.
type NewSUTAdapter struct {
	resource *resourceapp.Service
	versions VersionService
}

// NewNewSUTAdapter wires the new-SUT registration adapter over the resource service
// and the version read surface (*versionapp.Service satisfies VersionService).
func NewNewSUTAdapter(resource *resourceapp.Service, versions VersionService) *NewSUTAdapter {
	return &NewSUTAdapter{resource: resource, versions: versions}
}

var _ executionapp.NewSUTWriter = (*NewSUTAdapter)(nil)

// RegisterNewSUT resolves the version's bound environment and creates the new
// source=ai SUT (status approved on proof, proposed otherwise) with its birth fact.
// provenAt (the newest proving-evidence created_at) is forwarded so the resource
// rebirth guard can require fresh proof over a rejected same-domain SUT (P22).
// A classified failure (400/404/409) propagates to the gate.
func (a *NewSUTAdapter) RegisterNewSUT(ctx context.Context, versionID int64, in tools.SUTRegisterInput, evidenceRefs []string, approved bool, actorID int64, provenAt time.Time) (executionapp.NewSUTResult, error) {
	if a.resource == nil || a.versions == nil {
		return executionapp.NewSUTResult{}, errNotConfigured("resource/version service")
	}
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return executionapp.NewSUTResult{}, err
	}
	environmentID := parseViewID(view.Binding.Environment.ID)
	if environmentID <= 0 {
		return executionapp.NewSUTResult{}, platform.NotFound("environment")
	}
	input := resourceapp.SUTInput{
		Name:    in.Name,
		Intro:   in.Intro,
		BaseURL: in.BaseURL,
	}
	if family := strings.TrimSpace(in.Family); family != "" {
		input.Family = &family
	}
	created, err := a.resource.RegisterAISUT(ctx, environmentID, input, evidenceRefs, approved, actorID, provenAt)
	if err != nil {
		return executionapp.NewSUTResult{}, err
	}
	return executionapp.NewSUTResult{
		SUTID:         parseViewID(created.ID),
		EnvironmentID: parseViewID(created.EnvironmentID),
		Name:          created.Name,
		BaseURL:       created.BaseURL,
		Source:        created.Source,
		Status:        created.Status,
	}, nil
}

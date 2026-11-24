package adapters

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// ProvenAssetAdapter is the execution->resource cross-context write port of the
// asset evidence gate (v0.2.4-F, SC-F3). After the gate proves a discovery, it
// registers the asset into the resource catalog through the resource application
// service — the single asset-create authority. The execution context never writes
// resource tables directly (constitution art.5: cross-context via ports/adapters).
// provenAt (the newest proving-evidence created_at) is forwarded so a rejected
// page/api revives only on proof newer than the rejection; the returned
// Source/Status describe the ACTUAL catalog row (an idempotent hit on an
// admin-created approved page reports admin/approved, a database reports active).
type ProvenAssetAdapter struct {
	resource *resourceapp.Service
}

// NewProvenAssetAdapter wires the proven-asset registration adapter.
func NewProvenAssetAdapter(resource *resourceapp.Service) *ProvenAssetAdapter {
	return &ProvenAssetAdapter{resource: resource}
}

var _ executionapp.ProvenAssetWriter = (*ProvenAssetAdapter)(nil)

// RegisterProvenAsset creates (or idempotently resolves) the proven catalog asset
// of the given kind and returns its decimal id parsed back to int64 (the
// asset_registration fact stores a logical BIGINT asset_id) plus the row's actual
// source/status. The resource service validates the descriptor and the SUT
// existence; a classified failure (400/404/409, including the rejected-asset
// fresh-proof conflict and the database identity/disabled conflicts) propagates
// to the gate.
func (a *ProvenAssetAdapter) RegisterProvenAsset(ctx context.Context, in tools.AssetRegisterInput, actorID int64, provenAt time.Time) (executionapp.RegisteredAsset, error) {
	if a.resource == nil {
		return executionapp.RegisteredAsset{}, errNotConfigured("resource service")
	}
	switch in.AssetKind {
	case execution.AssetKindPage:
		view, err := a.resource.RegisterAIPage(ctx, in.SUTID, resourceapp.PageInput{
			Name: in.Name,
			URL:  in.URL,
		}, actorID, provenAt)
		if err != nil {
			return executionapp.RegisteredAsset{}, err
		}
		id, err := parseRegisteredAssetID(view.ID)
		if err != nil {
			return executionapp.RegisteredAsset{}, err
		}
		return executionapp.RegisteredAsset{ID: id, Source: view.Source, Status: view.Status}, nil
	case execution.AssetKindAPI:
		view, err := a.resource.RegisterAIAPI(ctx, in.SUTID, resourceapp.APIInput{
			Method:       in.Method,
			PathTemplate: in.Path,
			Name:         in.Name,
		}, actorID, provenAt)
		if err != nil {
			return executionapp.RegisteredAsset{}, err
		}
		id, err := parseRegisteredAssetID(view.ID)
		if err != nil {
			return executionapp.RegisteredAsset{}, err
		}
		return executionapp.RegisteredAsset{ID: id, Source: view.Source, Status: view.Status}, nil
	case execution.AssetKindDB:
		view, err := a.resource.RegisterAIDatabase(ctx, in.SUTID, resourceapp.DatabaseInput{
			Name:     in.Name,
			DSN:      in.DSN,
			Username: in.Username,
			Password: in.Password,
			ReadOnly: in.ReadOnly,
		}, actorID)
		if err != nil {
			return executionapp.RegisteredAsset{}, err
		}
		id, err := parseRegisteredAssetID(view.ID)
		if err != nil {
			return executionapp.RegisteredAsset{}, err
		}
		// A target database has no source column; the registration path is ai (the
		// same source the asset_registration fact records). Status is the row's real
		// lifecycle value (active), never a hardcoded "approved".
		return executionapp.RegisteredAsset{ID: id, Source: execution.AssetSourceAI, Status: view.Status}, nil
	default:
		return executionapp.RegisteredAsset{}, platform.Invalid("asset_kind", "must be one of page, api, db")
	}
}

func parseRegisteredAssetID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse registered asset id %q: %w", raw, err)
	}
	return id, nil
}

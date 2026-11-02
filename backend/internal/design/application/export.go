package designapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// P2 case export limits (spec §8).
const (
	MaxExportBytes = 10 << 20
	MaxExportLimit = 100
)

// ExportInput is one POST /versions/{id}/exports request.
type ExportInput struct {
	OwnerKind string
	OwnerID   int64
	Format    string
}

// ExportView is the export artifact API read model.
type ExportView struct {
	ID          int64
	VersionID   int64
	OwnerKind   string
	OwnerID     int64
	Revision    int
	Format      string
	Size        int64
	MIME        string
	Filename    string
	SHA256      string
	Status      string
	CreatedAt   time.Time
	DownloadURL string
}

// ExportService renders and stores whole-version case exports. Bytes live in
// the storage content ledger (P2-D-29), metadata in export_artifact.
type ExportService struct {
	repo   design.Repository
	store  ExportStore
	ids    platformid.Generator
	clock  clock.Clock
	events eventingapp.Publisher
	audit  auditapp.Recorder
}

// NewExportService wires the design export service.
func NewExportService(
	repo design.Repository,
	store ExportStore,
	ids platformid.Generator,
	clk clock.Clock,
	events eventingapp.Publisher,
	audit auditapp.Recorder,
) *ExportService {
	if clk == nil {
		clk = clock.System{}
	}
	return &ExportService{repo: repo, store: store, ids: ids, clock: clk, events: events, audit: audit}
}

// Create renders the current design graph in the requested format, stores the
// bytes and records the next export revision (spec §7.4/§8).
func (s *ExportService) Create(ctx context.Context, versionID int64, in ExportInput, actorID int64) (ExportView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return ExportView{}, err
	}
	ownerKind := strings.TrimSpace(in.OwnerKind)
	if ownerKind != "case" {
		return ExportView{}, platform.Invalid("owner_kind", "P2 only supports case exports")
	}
	if in.OwnerID != versionID {
		return ExportView{}, platform.Invalid("owner_id", "P2 case exports must target the whole version")
	}
	format := strings.TrimSpace(in.Format)
	if !design.ValidExportFormat(format) {
		return ExportView{}, platform.Invalid("format", "must be one of markdown, mermaid, opml, csv")
	}
	if s.ids == nil {
		return ExportView{}, platform.NewError(500, "internal", "id generator is not configured")
	}

	data, err := s.buildExportData(ctx, versionID)
	if err != nil {
		return ExportView{}, err
	}
	content, err := design.RenderExport(data, format)
	if err != nil {
		return ExportView{}, err
	}
	if len(content) > MaxExportBytes {
		return ExportView{}, platform.NewError(413, "payload_too_large", "export content exceeds the 10 MiB limit")
	}

	exportID := s.ids.Next()
	filename := fmt.Sprintf("case-export-%d%s", exportID, design.ExportExtension(format))
	record, err := s.store.Create(ctx, ExportRecordInput{
		ID:        exportID,
		VersionID: versionID,
		OwnerID:   in.OwnerID,
		OwnerKind: ownerKind,
		Format:    format,
		MIME:      design.ExportMIME(format),
		Filename:  filename,
		Data:      content,
		CreatedBy: asInt64Ptr(actorID),
	})
	if err != nil {
		return ExportView{}, mapDesignError(err, "export")
	}
	view := exportRecordView(record)
	s.publishExportReady(ctx, versionID, view, actorID)
	s.recordExportAudit(ctx, versionID, view, actorID)
	return view, nil
}

// Get returns one export artifact view.
func (s *ExportService) Get(ctx context.Context, exportID int64) (ExportView, error) {
	if err := requirePositiveID("export_id", exportID); err != nil {
		return ExportView{}, err
	}
	record, err := s.store.Get(ctx, exportID)
	if err != nil {
		return ExportView{}, mapDesignError(err, "export")
	}
	return exportRecordView(record), nil
}

// List returns one page of export views in ID-descending order.
func (s *ExportService) List(ctx context.Context, versionID int64, format string, limit int, beforeID *int64) ([]ExportView, bool, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return nil, false, err
	}
	format = strings.TrimSpace(format)
	if format != "" && !design.ValidExportFormat(format) {
		return nil, false, platform.Invalid("format", "must be one of markdown, mermaid, opml, csv")
	}
	if beforeID != nil {
		if err := requirePositiveID("before", *beforeID); err != nil {
			return nil, false, err
		}
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > MaxExportLimit {
		limit = MaxExportLimit
	}
	records, hasMore, err := s.store.List(ctx, versionID, format, limit, beforeID)
	if err != nil {
		return nil, false, mapDesignError(err, "export")
	}
	views := make([]ExportView, 0, len(records))
	for _, record := range records {
		views = append(views, exportRecordView(record))
	}
	return views, hasMore, nil
}

// Open returns one export view and hash-verified bytes.
func (s *ExportService) Open(ctx context.Context, exportID int64) (ExportView, io.ReadCloser, error) {
	if err := requirePositiveID("export_id", exportID); err != nil {
		return ExportView{}, nil, err
	}
	record, reader, err := s.store.Open(ctx, exportID)
	if err != nil {
		return ExportView{}, nil, mapDesignError(err, "export")
	}
	return exportRecordView(record), reader, nil
}

// buildExportData assembles the renderer snapshot from the current graph. Only
// current case versions are exported; superseded versions never appear.
func (s *ExportService) buildExportData(ctx context.Context, versionID int64) (design.ExportData, error) {
	title, err := s.repo.VersionTitle(ctx, versionID)
	if err != nil {
		return design.ExportData{}, mapDesignError(err, "version")
	}
	graph, err := s.repo.ReadGraph(ctx, versionID)
	if err != nil {
		return design.ExportData{}, mapDesignError(err, "version")
	}
	data := design.ExportData{
		VersionID:    versionID,
		VersionTitle: title,
		GeneratedAt:  s.clock.Now().UTC(),
		Requirements: make([]design.ExportRequirement, 0, len(graph.Requirements)),
	}
	for _, requirement := range graph.Requirements {
		exportRequirement := design.ExportRequirement{
			ID:                   requirement.Requirement.ID,
			RequirementVersionID: requirement.Requirement.CurrentVersionID,
			Revision:             requirement.Requirement.Revision,
			Title:                requirement.Requirement.Title,
			Status:               requirement.Requirement.Status,
			TargetVersionSUTIDs:  requirement.Requirement.TargetVersionSUTIDs,
			Scenarios:            make([]design.ExportScenario, 0, len(requirement.Scenarios)),
		}
		for _, scenario := range requirement.Scenarios {
			exportScenario := design.ExportScenario{
				ID:            scenario.Scenario.ID,
				Name:          scenario.Scenario.Name,
				Description:   scenario.Scenario.Description,
				VersionSUTIDs: scenario.Scenario.VersionSUTIDs,
				PageIDs:       scenario.Scenario.PageIDs,
				APIIDs:        scenario.Scenario.APIIDs,
				Cases:         make([]design.ExportCase, 0, len(scenario.Cases)),
			}
			for _, item := range scenario.Cases {
				if item.Current.Status == design.CaseStatusSuperseded {
					continue
				}
				currentVersionID := derefInt64(item.Case.CurrentVersionID)
				if currentVersionID <= 0 {
					currentVersionID = item.Current.ID
				}
				detail, err := s.repo.ReadCase(ctx, versionID, item.Case.ID, &currentVersionID)
				if err != nil {
					if errors.Is(err, design.ErrNotFound) {
						continue
					}
					return design.ExportData{}, mapDesignError(err, "case")
				}
				exportCase := design.ExportCase{
					CaseID:              item.Case.ID,
					VersionID:           detail.Selected.ID,
					Revision:            detail.Selected.Revision,
					Title:               detail.Selected.Title,
					Description:         detail.Selected.Description,
					Preconditions:       detail.Selected.Preconditions,
					TestData:            detail.Selected.TestData,
					Priority:            detail.Selected.Priority,
					Status:              detail.Selected.Status,
					DefaultVersionSUTID: detail.Selected.DefaultVersionSUTID,
					VersionSUTIDs:       detail.Selected.VersionSUTIDs,
					PageIDs:             detail.Selected.PageIDs,
					APIIDs:              detail.Selected.APIIDs,
					CreatedAt:           detail.Selected.CreatedAt,
					Steps:               detail.Selected.Steps,
					Assertions:          detail.Selected.Assertions,
					Sources:             detail.Selected.Sources,
				}
				exportScenario.Cases = append(exportScenario.Cases, exportCase)
			}
			exportRequirement.Scenarios = append(exportRequirement.Scenarios, exportScenario)
		}
		data.Requirements = append(data.Requirements, exportRequirement)
	}
	return data, nil
}

func exportRecordView(record ExportRecord) ExportView {
	return ExportView{
		ID: record.ID, VersionID: record.VersionID, OwnerKind: record.OwnerKind,
		OwnerID: record.OwnerID, Revision: record.Revision, Format: record.Format,
		Size: record.Size, MIME: record.MIME, Filename: record.Filename,
		SHA256: record.ContentObjectID, Status: record.Status,
		CreatedAt:   utcNow(record.CreatedAt),
		DownloadURL: "/api/exports/" + platformid.Format(record.ID) + "/download",
	}
}

func (s *ExportService) publishExportReady(ctx context.Context, versionID int64, view ExportView, actorID int64) {
	if s.events == nil {
		return
	}
	payload := map[string]any{
		"export_id": platformid.Format(view.ID),
		"format":    view.Format,
		"size":      view.Size,
		"sha256":    view.SHA256,
	}
	event := eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      "design.export_ready",
		Scope:     map[string]any{"version_id": platformid.Format(versionID)},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	}
	if actorID > 0 {
		actor := actorID
		event.ActorID = &actor
	}
	_ = s.events.Publish(ctx, event)
}

func (s *ExportService) recordExportAudit(ctx context.Context, versionID int64, view ExportView, actorID int64) {
	if s.audit == nil {
		return
	}
	event := auditapp.Event{
		Action:     "export.created",
		TargetKind: "export",
		TargetID:   platformid.Format(view.ID),
		RequestID:  requestid.From(ctx),
		Metadata: map[string]any{
			"version_id": platformid.Format(versionID),
			"format":     view.Format,
			"size":       view.Size,
			"sha256":     view.SHA256,
		},
	}
	if actorID > 0 {
		actor := actorID
		event.ActorID = &actor
	}
	_ = s.audit.Record(ctx, event)
}

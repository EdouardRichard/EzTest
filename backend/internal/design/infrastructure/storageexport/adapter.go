// Package storageexport adapts the storage content/exports application service
// to the design context's ExportStore port. main wires it; the design services
// stay independent of the storage infrastructure.
package storageexport

import (
	"context"
	"io"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// Adapter implements designapp.ExportStore on top of storageapp.ExportService.
type Adapter struct {
	service *storageapp.ExportService
}

var _ designapp.ExportStore = (*Adapter)(nil)

// New wraps the storage export service.
func New(service *storageapp.ExportService) *Adapter {
	return &Adapter{service: service}
}

// Create maps the design record input onto the storage service.
func (a *Adapter) Create(ctx context.Context, in designapp.ExportRecordInput) (designapp.ExportRecord, error) {
	artifact, err := a.service.Create(ctx, storageapp.CreateExportInput{
		ID:        in.ID,
		VersionID: in.VersionID,
		OwnerID:   in.OwnerID,
		OwnerKind: in.OwnerKind,
		Format:    in.Format,
		MIME:      in.MIME,
		Filename:  in.Filename,
		Data:      in.Data,
	}, deref(in.CreatedBy))
	if err != nil {
		return designapp.ExportRecord{}, err
	}
	return record(artifact), nil
}

// Get returns one artifact record.
func (a *Adapter) Get(ctx context.Context, exportID int64) (designapp.ExportRecord, error) {
	artifact, err := a.service.Get(ctx, exportID)
	if err != nil {
		return designapp.ExportRecord{}, err
	}
	return record(artifact), nil
}

// List returns one page of artifact records.
func (a *Adapter) List(ctx context.Context, versionID int64, format string, limit int, beforeID *int64) ([]designapp.ExportRecord, bool, error) {
	artifacts, hasMore, err := a.service.List(ctx, versionID, format, limit, beforeID)
	if err != nil {
		return nil, false, err
	}
	records := make([]designapp.ExportRecord, 0, len(artifacts))
	for _, artifact := range artifacts {
		records = append(records, record(artifact))
	}
	return records, hasMore, nil
}

// Open returns the artifact record and its hash-verified bytes.
func (a *Adapter) Open(ctx context.Context, exportID int64) (designapp.ExportRecord, io.ReadCloser, error) {
	artifact, reader, err := a.service.Open(ctx, exportID)
	if err != nil {
		return designapp.ExportRecord{}, nil, err
	}
	return record(artifact), reader, nil
}

func record(artifact storage.ExportArtifact) designapp.ExportRecord {
	return designapp.ExportRecord{
		ID:              artifact.ID,
		VersionID:       artifact.VersionID,
		OwnerID:         artifact.OwnerID,
		OwnerKind:       artifact.OwnerKind,
		Revision:        artifact.Revision,
		Format:          artifact.Format,
		ContentObjectID: artifact.ContentObjectID,
		Size:            artifact.Size,
		MIME:            artifact.MIME,
		Filename:        artifact.Filename,
		Status:          artifact.Status,
		CreatedAt:       artifact.CreatedAt,
	}
}

func deref(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

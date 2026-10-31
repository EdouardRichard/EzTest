package designapp

import (
	"context"
	"io"
	"time"
)

// ExportRecordInput is one artifact write handed to the storage context.
type ExportRecordInput struct {
	ID        int64
	VersionID int64
	OwnerID   int64
	OwnerKind string
	Format    string
	MIME      string
	Filename  string
	Data      []byte
	CreatedBy *int64
}

// ExportRecord is the storage-context artifact projection.
type ExportRecord struct {
	ID              int64
	VersionID       int64
	OwnerID         int64
	OwnerKind       string
	Revision        int
	Format          string
	ContentObjectID string
	Size            int64
	MIME            string
	Filename        string
	Status          string
	CreatedAt       time.Time
}

// ExportStore is the storage-context export port consumed by the design
// export service. main adapts it to storageapp.ExportService.
type ExportStore interface {
	Create(ctx context.Context, in ExportRecordInput) (ExportRecord, error)
	Get(ctx context.Context, exportID int64) (ExportRecord, error)
	List(ctx context.Context, versionID int64, format string, limit int, beforeID *int64) ([]ExportRecord, bool, error)
	Open(ctx context.Context, exportID int64) (ExportRecord, io.ReadCloser, error)
}

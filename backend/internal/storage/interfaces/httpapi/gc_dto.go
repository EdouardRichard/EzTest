package storageapi

import (
	"encoding/json"
	"time"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// gcRunDTO is the admin GC run view. IDs use the decimal-string convention and
// the report is passed through as the stored JSON object.
type gcRunDTO struct {
	ID             string          `json:"id"`
	Mode           string          `json:"mode"`
	Status         string          `json:"status"`
	ActorID        *string         `json:"actor_id"`
	ActorKind      string          `json:"actor_kind"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
	ScannedObjects int             `json:"scanned_objects"`
	ScannedRefs    int             `json:"scanned_refs"`
	DeletedObjects int             `json:"deleted_objects"`
	DeletedRefs    int             `json:"deleted_refs"`
	DeletedBytes   int64           `json:"deleted_bytes"`
	Report         json.RawMessage `json:"report"`
	Error          *string         `json:"error"`
}

// gcTombstoneDTO is one deleted-object tombstone returned by the run detail.
type gcTombstoneDTO struct {
	ID           string    `json:"id"`
	RunID        string    `json:"run_id"`
	ObjectSHA    string    `json:"object_sha"`
	Size         int64     `json:"size"`
	MIME         string    `json:"mime"`
	RelativePath string    `json:"relative_path"`
	Reason       string    `json:"reason"`
	DeletedAt    time.Time `json:"deleted_at"`
}

// gcRunDetailDTO is the flat run view plus tombstones (spec §6.2).
type gcRunDetailDTO struct {
	gcRunDTO
	Tombstones []gcTombstoneDTO `json:"tombstones"`
}

func toGCRunDTO(run storage.GCRun) gcRunDTO {
	view := gcRunDTO{
		ID:             platformid.Format(run.ID),
		Mode:           string(run.Mode),
		Status:         string(run.Status),
		ActorKind:      string(run.ActorKind),
		StartedAt:      run.StartedAt,
		FinishedAt:     run.FinishedAt,
		ScannedObjects: run.ScannedObjects,
		ScannedRefs:    run.ScannedRefs,
		DeletedObjects: run.DeletedObjects,
		DeletedRefs:    run.DeletedRefs,
		DeletedBytes:   run.DeletedBytes,
		Report:         run.Report,
	}
	if run.ActorID != nil {
		actorID := platformid.Format(*run.ActorID)
		view.ActorID = &actorID
	}
	if run.Error != "" {
		message := run.Error
		view.Error = &message
	}
	if len(view.Report) == 0 || string(view.Report) == "null" {
		view.Report = json.RawMessage(`{}`)
	}
	return view
}

func toGCTombstoneDTO(tombstone storage.GCTombstone) gcTombstoneDTO {
	return gcTombstoneDTO{
		ID:           platformid.Format(tombstone.ID),
		RunID:        platformid.Format(tombstone.RunID),
		ObjectSHA:    tombstone.ObjectSHA,
		Size:         tombstone.Size,
		MIME:         tombstone.MIME,
		RelativePath: tombstone.RelativePath,
		Reason:       tombstone.Reason,
		DeletedAt:    tombstone.DeletedAt,
	}
}

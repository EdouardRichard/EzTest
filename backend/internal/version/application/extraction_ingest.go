package versionapp

// v0.22 A-7 ingest handoff and progress reads (Task 18, R-A7-9).
//
// The attachment.ingest worker (Task 20 path, wired by Task 36/37) finishes a
// document and must open the deterministic extraction run for it. There is no
// production attachment-to-extraction hook in the tree yet, so this file
// exposes EnqueueAfterIngest as the single handoff entry point: Task 36/37 calls
// it after the attachment row is marked done (requirement.attachment_ingested),
// with the deterministic segments produced by SegmentExtraction. It only
// creates/replays the pending run and writes the plan start milestone; segment
// processing stays with the extraction worker (ProcessRun/ProcessNext), which
// keeps the HTTP path and the ingest task short.
//
// StartVersion backs POST /api/versions/{id}/requirements/extractions with
// attachment_id (one attachment) or 0 (every ready attachment of the version).
// The concrete attachment-side resolution is an ExtractionIngestSource implemented by
// the composition root, because the version application package must not grow a
// dependency on the attachment service (Task 36/37 wiring). A missing provider
// is a typed 503 instead of a silent empty success.

import (
	"context"
	"errors"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// errExtractionNoRepository is the degraded-assembly guard shared by the
// progress/milestone reads.
func errExtractionNoRepository() error {
	return errors.New("extraction service has no repository")
}

// ExtractionDocument is the canonical parsed document of one attachment, used
// to finalize a run: the plan title, the persisted reconciliation report and
// the skipped-image count of the parse/fact payloads.
type ExtractionDocument struct {
	// Name is the attachment name shown in the plan item title.
	Name string
	// Text is the canonical document text (the same text SegmentExtraction
	// consumed) used by Reconcile to count declared FR ids.
	Text string
	// SkippedImages is the parser coverage count of human-visible images that
	// were not analyzed (A §7.9).
	SkippedImages int
}

// ExtractionIngestSource resolves the version's ready attachments and their parsed
// documents. Task 36/37 implements it on top of the attachment service
// (ExtractionInputs from the stored extraction result + SegmentExtraction);
// tests use a fake.
type ExtractionIngestSource interface {
	// ExtractionInputs returns the ready extraction inputs of one attachment
	// (attachmentID > 0) or of every ready attachment of the version
	// (attachmentID == 0). An unknown/unready attachment yields an empty slice;
	// the service maps that to its typed not-found/state-conflict response.
	ExtractionInputs(ctx context.Context, versionID, attachmentID int64) ([]ExtractionStartInput, error)
	// ExtractionDocument returns the canonical parsed document of one run's
	// input (version, attachment, input_sha256) for reconciliation and the plan
	// title. The sha identifies the run's own parse even when the attachment has
	// since been re-parsed: implementations must resolve the document for that
	// input, not merely the attachment's current document. Implementations must
	// return platform.NotFound when the attachment or its extracted text is
	// missing.
	ExtractionDocument(ctx context.Context, versionID, attachmentID int64, inputSHA256 string) (ExtractionDocument, error)
}

// SetSource wires the attachment-side extraction source (Task 36/37). A nil
// source disables StartVersion until the composition root supplies one.
func (s *ExtractionService) SetSource(source ExtractionIngestSource) {
	if s == nil {
		return
	}
	s.source = source
}

// ListSegments reads the segment cursor of one run for the progress API. The
// rows never carry the segment text (only positions and the text sha).
func (s *ExtractionService) ListSegments(ctx context.Context, runID int64) ([]ExtractionSegment, error) {
	if s == nil || s.repo == nil {
		return nil, errExtractionNoRepository()
	}
	if runID <= 0 {
		return nil, ErrExtractionRunNotFound
	}
	return s.repo.ListSegments(ctx, runID)
}

// StartVersion opens or replays the extraction run(s) of a version. A positive
// attachmentID starts that one attachment; 0 starts every ready attachment.
// Runs are created pending; the extraction worker processes the segments.
//
// Typed outcomes: a missing/not-ready attachment is a 404, a version with no
// ready attachment is a 409, and a missing source/provider is a 503. Start
// itself is idempotent by (version_id, attachment_id, input_sha256), so
// "重新抽取" never duplicates a run.
func (s *ExtractionService) StartVersion(ctx context.Context, versionID, attachmentID int64) ([]ExtractionRun, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if attachmentID < 0 {
		return nil, platform.Invalid("attachment_id", "must not be negative")
	}
	if s == nil || s.repo == nil {
		return nil, errExtractionNoRepository()
	}
	if s.source == nil {
		return nil, platform.Unavailable("requirement extraction input provider is not configured")
	}
	inputs, err := s.source.ExtractionInputs(ctx, versionID, attachmentID)
	if err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		if attachmentID > 0 {
			return nil, platform.NotFound("requirement attachment")
		}
		return nil, platform.StateConflict("no attachment is ready for requirement extraction")
	}
	runs := make([]ExtractionRun, 0, len(inputs))
	for index := range inputs {
		if inputs[index].VersionID == 0 {
			inputs[index].VersionID = versionID
		}
		if inputs[index].VersionID != versionID {
			return nil, platform.Invalid("version_id", "extraction input belongs to another version")
		}
		if inputs[index].AttachmentID <= 0 {
			return nil, platform.Invalid("attachment_id", "extraction input has no attachment")
		}
		if attachmentID > 0 && inputs[index].AttachmentID != attachmentID {
			return nil, platform.Invalid("attachment_id", "extraction input belongs to another attachment")
		}
		run, startErr := s.Start(ctx, inputs[index])
		if startErr != nil {
			return nil, startErr
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// EnqueueAfterIngest is the attachment.ingest -> requirement extraction handoff
// (R-A7-9). It opens/replays the pending run for one parsed attachment and
// writes the plan start milestone; it does not process segments, so the ingest
// task returns as soon as the run is queued. Replaying the same
// (version_id, attachment_id, input_sha256) returns the same run and never
// duplicates plan items.
//
// Task 36/37 wiring: call it from the attachment.ingest completion path
// (after requirement.attachment_ingested / MarkDone), with the deterministic
// SegmentExtraction output. The parser coverage count and the canonical
// document text are resolved from the ExtractionIngestSource by the run's
// input_sha256 when the terminal projection reconciles. No
// requirement.curate (or any other legacy) work item is created here.
func (s *ExtractionService) EnqueueAfterIngest(ctx context.Context, input ExtractionStartInput) (ExtractionRun, error) {
	return s.Start(ctx, input)
}

// attachmentDocumentName resolves the attachment name for the plan title when
// the Start input did not carry it. Missing source/metadata is not an error: the
// plan title falls back to the attachment id.
func (s *ExtractionService) attachmentDocumentName(ctx context.Context, run ExtractionRun) string {
	if s.source == nil {
		return ""
	}
	document, err := s.source.ExtractionDocument(ctx, run.VersionID, run.AttachmentID, run.InputSHA256)
	if err != nil {
		return ""
	}
	return document.Name
}

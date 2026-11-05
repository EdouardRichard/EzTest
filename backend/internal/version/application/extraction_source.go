package versionapp

// v0.22 A-7 production extraction source (Task 37 fix round 1).
//
// AttachmentService implements the ExtractionIngestSource port the extraction
// service needs: it re-parses the attachment's original content object (the
// parser is deterministic, so the same input bytes always produce the same
// segments) and returns the canonical document used both for the run's start
// input and for the terminal reconciliation. The run identity is the original
// content object SHA-256, so a run can only reconcile the document it parsed:
// ExtractionDocument/ExtractionSegments verify input_sha256 instead of blindly
// returning the attachment's current text.
//
// This keeps the version application package free of a second document store:
// the content ledger already owns the immutable original bytes, and the parse
// is cheap relative to the model extraction it feeds.

import (
	"context"
	"errors"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ExtractionSegments re-derives the deterministic segment plan of the run's
// input document. The deterministic segment processor uses it to retrieve the
// segment text the model/processor sees; the segment row in the DB only stores
// its identity (seq/positions/text sha), never the text itself.
type ExtractionSegmentsSource interface {
	ExtractionSegments(ctx context.Context, versionID, attachmentID int64, inputSHA256 string) ([]Segment, error)
}

var (
	_ ExtractionIngestSource   = (*AttachmentService)(nil)
	_ ExtractionSegmentsSource = (*AttachmentService)(nil)
)

// ExtractionInputs returns the ready (done) extraction inputs of one
// attachment or of every ready attachment of the version. An unknown or
// non-ready attachment yields an empty slice (the extraction service maps that
// to its typed 404/409 responses).
func (s *AttachmentService) ExtractionInputs(ctx context.Context, versionID, attachmentID int64) ([]ExtractionStartInput, error) {
	if s == nil || s.repo == nil || s.storage == nil || s.extractor == nil {
		return nil, platform.Unavailable("requirement extraction input provider is not configured")
	}
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if attachmentID < 0 {
		return nil, platform.Invalid("attachment_id", "must not be negative")
	}
	attachments, err := s.readyExtractionAttachments(ctx, versionID, attachmentID)
	if err != nil {
		return nil, err
	}
	inputs := make([]ExtractionStartInput, 0, len(attachments))
	for _, attachment := range attachments {
		sha, document, segments, planErr := s.extractionPlan(ctx, attachment)
		if planErr != nil {
			return nil, planErr
		}
		inputs = append(inputs, ExtractionStartInput{
			VersionID:      attachment.VersionID,
			AttachmentID:   attachment.ID,
			InputSHA256:    sha,
			Segments:       segments,
			DocumentText:   document.Text,
			AttachmentName: attachment.Name,
		})
	}
	return inputs, nil
}

// ExtractionDocument resolves the canonical parsed document of one run's input
// (version, attachment, input_sha256) for reconciliation and the plan title.
// The sha must match the attachment's original content object: a run whose
// document can no longer be reproduced is reported as not found instead of
// reconciling against a different parse.
func (s *AttachmentService) ExtractionDocument(ctx context.Context, versionID, attachmentID int64, inputSHA256 string) (ExtractionDocument, error) {
	attachment, err := s.readyExtractionAttachment(ctx, versionID, attachmentID)
	if err != nil {
		return ExtractionDocument{}, err
	}
	sha, document, _, planErr := s.extractionPlan(ctx, attachment)
	if planErr != nil {
		return ExtractionDocument{}, planErr
	}
	if !strings.EqualFold(strings.TrimSpace(sha), strings.TrimSpace(inputSHA256)) {
		return ExtractionDocument{}, platform.NotFound("requirement attachment document")
	}
	return document, nil
}

// ExtractionSegments re-derives the run's segment plan, honoring input_sha256
// exactly like ExtractionDocument (see ExtractionSegmentsSource).
func (s *AttachmentService) ExtractionSegments(ctx context.Context, versionID, attachmentID int64, inputSHA256 string) ([]Segment, error) {
	attachment, err := s.readyExtractionAttachment(ctx, versionID, attachmentID)
	if err != nil {
		return nil, err
	}
	sha, _, segments, planErr := s.extractionPlan(ctx, attachment)
	if planErr != nil {
		return nil, planErr
	}
	if !strings.EqualFold(strings.TrimSpace(sha), strings.TrimSpace(inputSHA256)) {
		return nil, platform.NotFound("requirement attachment document")
	}
	return segments, nil
}

// readyExtractionAttachments resolves the done document attachments of one
// version (or exactly one when attachmentID > 0). A missing/non-done
// attachment is an empty slice, not an error: the caller maps it to its typed
// API outcome.
func (s *AttachmentService) readyExtractionAttachments(ctx context.Context, versionID, attachmentID int64) ([]version.Attachment, error) {
	if attachmentID > 0 {
		attachment, err := s.repo.GetInVersion(ctx, versionID, attachmentID)
		if err != nil {
			if errors.Is(err, version.ErrAttachmentNotFound) {
				return nil, nil
			}
			return nil, mapAttachmentRepositoryError(err)
		}
		if attachment.ExtractionStatus != version.AttachmentDone {
			return nil, nil
		}
		return []version.Attachment{attachment}, nil
	}
	attachments, err := s.repo.ListByVersion(ctx, versionID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	ready := make([]version.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.ExtractionStatus != version.AttachmentDone {
			continue
		}
		ready = append(ready, attachment)
	}
	return ready, nil
}

// readyExtractionAttachment loads exactly one done attachment and maps a
// missing/not-ready row to a typed 404.
func (s *AttachmentService) readyExtractionAttachment(ctx context.Context, versionID, attachmentID int64) (version.Attachment, error) {
	if s == nil || s.repo == nil || s.storage == nil || s.extractor == nil {
		return version.Attachment{}, platform.Unavailable("requirement extraction input provider is not configured")
	}
	if versionID <= 0 {
		return version.Attachment{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if attachmentID <= 0 {
		return version.Attachment{}, platform.Invalid("attachment_id", "must be a positive decimal ID")
	}
	attachment, err := s.repo.GetInVersion(ctx, versionID, attachmentID)
	if err != nil {
		return version.Attachment{}, mapAttachmentRepositoryError(err)
	}
	if attachment.ExtractionStatus != version.AttachmentDone {
		return version.Attachment{}, platform.NotFound("requirement attachment document")
	}
	return attachment, nil
}

// extractionPlan re-parses one attachment's original content and returns the
// run identity (original object sha), the canonical document and the
// deterministic segment plan.
func (s *AttachmentService) extractionPlan(ctx context.Context, attachment version.Attachment) (string, ExtractionDocument, []Segment, error) {
	sha, err := s.originalObjectID(ctx, attachment.ID, attachment.ContentRefID)
	if err != nil {
		return "", ExtractionDocument{}, nil, err
	}
	_, reader, err := s.storage.Open(ctx, sha)
	if err != nil {
		return "", ExtractionDocument{}, nil, err
	}
	defer reader.Close()
	parsed, err := s.extractor.Extract(ctx, attachment.Name, attachment.MIME, reader)
	if err != nil {
		return "", ExtractionDocument{}, nil, err
	}
	segments, err := SegmentExtraction(parsed, SegmentOptions{SourcePath: attachment.Name})
	if err != nil {
		return "", ExtractionDocument{}, nil, err
	}
	skipped := parsed.Coverage.ImagesFound - parsed.Coverage.ImagesParsed
	if skipped < 0 {
		skipped = 0
	}
	document := ExtractionDocument{
		Name:          attachment.Name,
		Text:          parsed.Text,
		SkippedImages: skipped,
	}
	return sha, document, segments, nil
}

// mapAttachmentLookupError is deliberately not exposed: a not-found attachment
// is handled by readyExtractionAttachments as the documented empty-input case.

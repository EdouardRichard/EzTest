package visualadapter

import (
	"bytes"
	"context"
	"errors"
	"image/png"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
)

// EvidenceSource reads one immutable execution evidence row.
type EvidenceSource interface {
	Get(ctx context.Context, id int64) (execution.Evidence, error)
}

// CaseRunSource reads one case_run row (repository).
type CaseRunSource interface {
	Get(ctx context.Context, id int64) (execution.CaseRun, error)
}

// IntentSource reads one execution_intent row (repository).
type IntentSource interface {
	Get(ctx context.Context, id int64) (execution.ExecutionIntent, error)
}

// ContentSource reads one immutable blob from the content ledger.
type ContentSource interface {
	Open(ctx context.Context, sha256 string) ([]byte, error)
}

// EvidenceReaderDeps wires the execution evidence reader of the visual context.
type EvidenceReaderDeps struct {
	Evidence EvidenceSource
	CaseRuns CaseRunSource
	Intents  IntentSource
	Content  ContentSource
}

// EvidenceReader implements visualapp.EvidenceReader: baseline approval reads
// one visual_actual evidence plus its bytes, never a filesystem path.
type EvidenceReader struct {
	evidence EvidenceSource
	caseRuns CaseRunSource
	intents  IntentSource
	content  ContentSource
}

// NewEvidenceReader wires the execution evidence reader.
func NewEvidenceReader(deps EvidenceReaderDeps) *EvidenceReader {
	return &EvidenceReader{
		evidence: deps.Evidence, caseRuns: deps.CaseRuns,
		intents: deps.Intents, content: deps.Content,
	}
}

var _ visualapp.EvidenceReader = (*EvidenceReader)(nil)

// VisualEvidence returns the evidence metadata and bytes scoped to a version.
func (r *EvidenceReader) VisualEvidence(ctx context.Context, versionID, evidenceID int64) (visualapp.VisualEvidence, error) {
	if r.evidence == nil || r.caseRuns == nil || r.intents == nil {
		return visualapp.VisualEvidence{}, platform.Unavailable("visual evidence reader is not configured")
	}
	evidence, err := r.evidence.Get(ctx, evidenceID)
	if err != nil {
		return visualapp.VisualEvidence{}, mapExecutionReadError(err, "evidence")
	}
	caseRun, err := r.caseRuns.Get(ctx, evidence.CaseRunID)
	if err != nil {
		return visualapp.VisualEvidence{}, mapExecutionReadError(err, "case_run")
	}
	intent, err := r.intents.Get(ctx, caseRun.IntentID)
	if err != nil {
		return visualapp.VisualEvidence{}, mapExecutionReadError(err, "execution_intent")
	}
	if intent.VersionID != versionID {
		return visualapp.VisualEvidence{}, platform.NotFound("evidence")
	}
	var data []byte
	if r.content != nil && evidence.SHA256 != "" {
		data, err = r.content.Open(ctx, evidence.SHA256)
		if err != nil {
			return visualapp.VisualEvidence{}, platform.Unavailable("the evidence content is not readable")
		}
	}
	width, height := 0, 0
	if len(data) > 0 {
		if config, decodeErr := png.DecodeConfig(bytes.NewReader(data)); decodeErr == nil {
			width, height = config.Width, config.Height
		}
	}
	return visualapp.VisualEvidence{
		ID: evidence.ID, CaseRunID: evidence.CaseRunID, CaseVersionID: caseRun.CaseVersionID,
		VersionSUTID: evidence.VersionSUTID, Kind: string(evidence.Kind),
		SHA256: evidence.SHA256, SizeBytes: evidence.SizeBytes, MIME: evidence.MIME,
		Data: data, Width: width, Height: height,
		// The evidence table has no config-revision column; the visual service
		// falls back to the capture timestamp when this is 0.
		ConfigRevision: 0,
		CreatedAt:      evidence.CreatedAt,
	}, nil
}

func mapExecutionReadError(err error, kind string) error {
	if err == nil {
		return nil
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	if errors.Is(err, execution.ErrNotFound) {
		return platform.NotFound(kind)
	}
	return err
}

package resource

import (
	"context"
	"errors"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
)

// IndexStateMarker is the narrow slice of the P2 code repository service the
// writer needs. *resource/application.CodeRepoService satisfies it through
// MarkIndexed.
type IndexStateMarker interface {
	MarkIndexed(ctx context.Context, repoID int64, commit string, at time.Time) error
}

// IndexStateWriter adapts the P2 MarkIndexed use case to the codeintel
// IndexStateWriter port. It writes only last_indexed_at through the P2
// application layer (design spec §3.5/INV-14).
type IndexStateWriter struct {
	codeRepos IndexStateMarker
}

// NewIndexStateWriter wires the writer to the P2 code repository service.
func NewIndexStateWriter(codeRepos IndexStateMarker) *IndexStateWriter {
	return &IndexStateWriter{codeRepos: codeRepos}
}

var _ codeintelapp.IndexStateWriter = (*IndexStateWriter)(nil)

// MarkIndexed forwards the index completion state to the P2 application
// service. A missing dependency is a configuration error rather than a silent
// success, so a mis-wired process cannot report an index as recorded.
func (w *IndexStateWriter) MarkIndexed(ctx context.Context, repoID int64, commit string, at time.Time) error {
	if w == nil || w.codeRepos == nil {
		return errors.New("codeintel: resource index state writer is not configured")
	}
	return w.codeRepos.MarkIndexed(ctx, repoID, commit, at)
}

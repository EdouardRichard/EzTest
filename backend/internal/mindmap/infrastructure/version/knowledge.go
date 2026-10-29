// Package version adapts the P3 version application services to the P5 mindmap
// ports (spec §4.3). This file is the approved-knowledge adapter: the version
// context remains the owner of knowledge_entry, the mindmap context only
// consumes the rendered "版本知识" segment of a design run (spec §9.4).
package version

import (
	"context"
	"errors"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// KnowledgeService is the narrow slice of the P3 knowledge service the adapter
// needs. *version/application.KnowledgeService satisfies it.
type KnowledgeService interface {
	ApprovedKnowledgeSection(ctx context.Context, versionID int64) (versionapp.KnowledgeSection, error)
}

// KnowledgeAdapter implements application.KnowledgeReader on top of the P3
// knowledge service.
type KnowledgeAdapter struct {
	knowledge KnowledgeService
}

// NewKnowledgeAdapter wires the adapter to the P3 knowledge service.
func NewKnowledgeAdapter(knowledge KnowledgeService) *KnowledgeAdapter {
	return &KnowledgeAdapter{knowledge: knowledge}
}

var _ mindmapapp.KnowledgeReader = (*KnowledgeAdapter)(nil)

// ApprovedKnowledgeSection returns the approved-only prompt segment of the
// version. The budget was already applied by the P3 service (env-driven
// EZTEST_KNOWLEDGE_MAX_INJECT/MAX_BYTES); the adapter only maps the projection
// and the error kind.
func (a *KnowledgeAdapter) ApprovedKnowledgeSection(ctx context.Context, versionID int64) (mindmapapp.KnowledgeSection, error) {
	if a == nil || a.knowledge == nil {
		return mindmapapp.KnowledgeSection{}, errors.New("mindmap: knowledge service is not configured")
	}
	section, err := a.knowledge.ApprovedKnowledgeSection(ctx, versionID)
	if err != nil {
		return mindmapapp.KnowledgeSection{}, mapVersionError(err)
	}
	return mindmapapp.KnowledgeSection{
		Text:      section.Text,
		Refs:      append([]int64{}, section.Refs...),
		Truncated: section.Truncated,
	}, nil
}

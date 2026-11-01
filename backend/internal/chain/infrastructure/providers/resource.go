package providers

import (
	"context"
	"strings"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// SUTReader is the resource application SUT read surface. The resource service
// satisfies it.
type SUTReader interface {
	GetSUT(ctx context.Context, id int64) (resourceapp.SUTView, error)
}

// ResourceProvider resolves display labels for version SUTs and pages (spec
// §8.1). It discovers the version bindings through the version application and
// reads the authoritative SUT/environment/page names from the resource
// application; it never reads resource tables.
type ResourceProvider struct {
	versions VersionReader
	suts     SUTReader
}

// NewResourceProvider wires the version binding reader and the resource SUT
// reader.
func NewResourceProvider(versions VersionReader, suts SUTReader) *ResourceProvider {
	return &ResourceProvider{versions: versions, suts: suts}
}

var _ chainapp.ResourceChainProvider = (*ResourceProvider)(nil)

// SUTNames returns version-SUT-ID to SUT name labels. Bindings supply the ID
// mapping and the resource service supplies the canonical name.
func (p *ResourceProvider) SUTNames(ctx context.Context, versionID int64) (map[int64]string, error) {
	view, err := p.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(view.Bindings))
	for _, binding := range view.Bindings {
		if binding.ID <= 0 {
			continue
		}
		name := strings.TrimSpace(binding.SUT.Name)
		if sutID, ok := decimalID(binding.SUT.ID); ok {
			sut, err := p.suts.GetSUT(ctx, sutID)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(sut.Name) != "" {
				name = strings.TrimSpace(sut.Name)
			}
		}
		if name != "" {
			names[binding.ID] = name
		}
	}
	return names, nil
}

// PageNames returns page-ID to page-name labels for every page of the version's
// bound SUT environments. Bindings also list the selected scope pages, which
// cover page targets that are valid for design proposals.
func (p *ResourceProvider) PageNames(ctx context.Context, versionID int64) (map[int64]string, error) {
	view, err := p.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string)
	seenSUTs := make(map[int64]bool)
	for _, binding := range view.Bindings {
		if binding.ID <= 0 {
			continue
		}
		for _, page := range binding.Pages {
			if id, ok := decimalID(page.ID); ok && strings.TrimSpace(page.Name) != "" {
				names[id] = strings.TrimSpace(page.Name)
			}
		}
		sutID, ok := decimalID(binding.SUT.ID)
		if !ok || seenSUTs[sutID] {
			continue
		}
		seenSUTs[sutID] = true
		sut, err := p.suts.GetSUT(ctx, sutID)
		if err != nil {
			return nil, err
		}
		for _, environment := range sut.Environments {
			for _, page := range environment.Pages {
				if id, ok := decimalID(page.ID); ok && strings.TrimSpace(page.Name) != "" {
					names[id] = strings.TrimSpace(page.Name)
				}
			}
		}
	}
	return names, nil
}

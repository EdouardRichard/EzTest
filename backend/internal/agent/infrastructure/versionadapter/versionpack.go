package versionadapter

import (
	"context"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const versionPackAttachmentMax = 50

// VersionPack renders the version summary used by the context builder: title,
// status, bound SUTs, page/API names, requirement summaries and attachment
// metadata. It reads through the version and requirement application services
// only.
func (a *Adapter) VersionPack(ctx context.Context, versionID int64) (agentapp.VersionPack, error) {
	if a.versions == nil {
		return agentapp.VersionPack{}, a.missing("versions")
	}
	if versionID <= 0 {
		return agentapp.VersionPack{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return agentapp.VersionPack{}, err
	}
	pack := agentapp.VersionPack{
		VersionID: view.ID,
		Title:     view.Title,
		Status:    view.Status,
		SUTs:      make([]string, 0, len(view.Bindings)),
		Pages:     make([]string, 0),
		APIs:      make([]string, 0),
	}
	for _, binding := range view.Bindings {
		pack.SUTs = append(pack.SUTs, fmt.Sprintf("%s (%s)", binding.SUT.Name, binding.Environment.Name))
		for _, page := range binding.Pages {
			pack.Pages = append(pack.Pages, fmt.Sprintf("%s %s", page.Name, page.RouteTemplate))
		}
		for _, api := range binding.APIs {
			pack.APIs = append(pack.APIs, fmt.Sprintf("%s %s", api.Method, api.PathTemplate))
		}
	}

	requirements, err := a.listAllRequirements(ctx, versionID)
	if err != nil {
		return agentapp.VersionPack{}, err
	}
	pack.Requirements = make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		pack.Requirements = append(pack.Requirements,
			fmt.Sprintf("#%d [%s] %s (%s)", requirement.ID, requirement.Status, requirement.Title, requirement.Priority))
	}

	// Attachment metadata is requirement-scoped in P1; collect it from the
	// requirement details, capped so the pack stays bounded.
	pack.Attachments = make([]string, 0)
	if a.requirements != nil {
		for _, requirement := range requirements {
			if len(pack.Attachments) >= versionPackAttachmentMax {
				break
			}
			detail, detailErr := a.requirements.Get(ctx, versionID, requirement.ID)
			if detailErr != nil {
				continue
			}
			for _, attachment := range detail.Attachments {
				pack.Attachments = append(pack.Attachments,
					fmt.Sprintf("%s (%s, %s)", attachment.Name, attachment.MIME, attachment.ExtractionStatus))
				if len(pack.Attachments) >= versionPackAttachmentMax {
					break
				}
			}
		}
	}
	return pack, nil
}

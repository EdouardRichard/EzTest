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
	// EffectiveSUTs, not Environment.SUTs: the pack feeds the design AI's page
	// locators and the model-facing database inventory, so it carries the
	// version's effective scope (approved pages/APIs, active databases) and never
	// the proposed/disabled rows of the raw resource catalog.
	suts := view.Binding.EffectiveSUTs()
	environmentName := view.Binding.Environment.Name
	pack := agentapp.VersionPack{
		VersionID: view.ID,
		Title:     view.Title,
		Status:    view.Status,
		SUTs:      make([]string, 0, len(suts)),
		Pages:     make([]string, 0),
		APIs:      make([]string, 0),
	}
	for _, sut := range suts {
		pack.SUTs = append(pack.SUTs, fmt.Sprintf("%s (%s)", sut.Name, environmentName))
		for _, page := range sut.Pages {
			pack.Pages = append(pack.Pages, fmt.Sprintf("%s %s", page.Name, page.RouteTemplate))
		}
		for _, api := range sut.APIs {
			pack.APIs = append(pack.APIs, fmt.Sprintf("%s %s", api.Method, api.PathTemplate))
		}
		// The pack carries the database inventory so the model can choose
		// instead of guessing. Two id spaces travel with each row and must not be
		// confused (v0.2.4-B): the database id serves the execution db tools,
		// while SUTID -- passed in here and rendered by versionPackContent -- is
		// what a design db assertion takes as its sut_id. The old R-E1-2 rule
		// ("db assertions may only use a bound database_id") is retired: a design
		// db assertion names a SUT and is checked for presence only.
		// Empty when the version binds no database.
		pack.Databases = append(pack.Databases, databaseSummaries(sut.SUTID, sut.Databases)...)
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

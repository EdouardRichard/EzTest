// Package knowledgeadapter adapts the P7 knowledge bounded context to the
// agent runtime and tool ports. It is the only bridge between agent/** and the
// knowledge context: the agent application and tools packages never import a
// knowledge application service.
//
// Wiring is owned by cmd/eztest (Lead, Task 19):
//
//	knowledgeAdapter := knowledgeadapter.New(knowledgeService, versionSystemReader)
//	agentapp.NewRunner(agentapp.RunnerDeps{Knowledge: knowledgeAdapter, KnowledgeUsage: knowledgeAdapter, ...})
//	tools.Deps{P7: tools.P7Deps{Knowledge: knowledgeAdapter}}
//
// versionSystemReader resolves the SUT families bound to a version. The
// version/resource read models do not expose sut.family yet, so the assembly
// root implements VersionSystemReader over the version bindings (a nil reader
// only disables system-scoped knowledge; global knowledge still injects).
package knowledgeadapter

import (
	"context"
	"errors"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// KnowledgeService is the knowledge application surface the adapter consumes.
// *knowledgeapp.Service satisfies it.
type KnowledgeService interface {
	Inject(ctx context.Context, input knowledgeapp.InjectInput) ([]knowledgeapp.Injected, error)
	RecordUsage(ctx context.Context, agentRunID, workItemID int64, items []knowledgeapp.Injected) error
	Create(ctx context.Context, actor platform.Actor, input knowledgeapp.CreateInput) (knowledge.Item, error)
}

// VersionSystemReader resolves the SUT families bound to one version. The
// knowledge system scope matches scope=system items against those family keys
// (spec §4.3 / contracts/agent-profiles-tools.md §10.3). The version/resource
// read models do not expose sut.family yet, so cmd/eztest implements this port
// over the version bindings (Task 19).
type VersionSystemReader interface {
	VersionSystemKeys(ctx context.Context, versionID int64) ([]string, error)
}

// Adapter implements the agent knowledge ports and the knowledge.propose tool
// writer on top of the knowledge application service.
type Adapter struct {
	knowledge KnowledgeService
	systems   VersionSystemReader
}

// New wires the knowledge adapter. systems may be nil: system-scoped knowledge
// is then skipped while global knowledge still injects.
func New(knowledge KnowledgeService, systems VersionSystemReader) *Adapter {
	return &Adapter{knowledge: knowledge, systems: systems}
}

var (
	_ agentapp.KnowledgeReader        = (*Adapter)(nil)
	_ agentapp.KnowledgeUsageRecorder = (*Adapter)(nil)
	_ tools.KnowledgeProposePort      = (*Adapter)(nil)
	_ KnowledgeService                = (*knowledgeapp.Service)(nil)
)

// RetrieveForVersion injects the approved knowledge visible to a version. The
// caller may pass systemKeys explicitly; nil/empty lets the adapter derive the
// version's SUT families. The knowledge service owns the approved/source_stale
// filters, the ts_rank ordering and the settings-driven item/token budget.
func (a *Adapter) RetrieveForVersion(ctx context.Context, versionID int64, systemKeys []string) ([]agentapp.KnowledgeRef, int, error) {
	if versionID <= 0 {
		return nil, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if a.knowledge == nil {
		return nil, 0, errors.New("knowledgeadapter: knowledge service is not configured")
	}
	keys := systemKeys
	if len(keys) == 0 && a.systems != nil {
		derived, err := a.systems.VersionSystemKeys(ctx, versionID)
		if err != nil {
			return nil, 0, fmt.Errorf("knowledgeadapter: resolve version SUT families: %w", err)
		}
		keys = derived
	}
	injected, err := a.knowledge.Inject(ctx, knowledgeapp.InjectInput{
		VersionID:  versionID,
		SystemKeys: keys,
	})
	if err != nil {
		return nil, 0, err
	}
	refs := make([]agentapp.KnowledgeRef, 0, len(injected))
	total := 0
	for _, item := range injected {
		refs = append(refs, agentapp.KnowledgeRef{
			ID:      item.ID,
			Title:   item.Title,
			Snippet: item.Snippet,
			Scope:   string(item.Scope),
			Tokens:  item.Tokens,
		})
		total += item.Tokens
	}
	return refs, total, nil
}

// RecordInjection appends one knowledge_usage row per injected item. The runner
// treats the returned error as log-only (contracts §10.3).
func (a *Adapter) RecordInjection(ctx context.Context, agentRunID, workItemID int64, refs []agentapp.KnowledgeRef) error {
	if len(refs) == 0 {
		return nil
	}
	if a.knowledge == nil {
		return errors.New("knowledgeadapter: knowledge service is not configured")
	}
	items := make([]knowledgeapp.Injected, 0, len(refs))
	for _, ref := range refs {
		items = append(items, knowledgeapp.Injected{
			ID:      ref.ID,
			Title:   ref.Title,
			Snippet: ref.Snippet,
			Scope:   knowledge.Scope(ref.Scope),
			Tokens:  ref.Tokens,
		})
	}
	return a.knowledge.RecordUsage(ctx, agentRunID, workItemID, items)
}

// ProposeKnowledge creates one AI knowledge draft. The service enforces
// status=draft, source_version_id=promoted_from_version_id=current version and
// created_by=run actor; the adapter strips any admin role from the run actor so
// an AI proposal never auto-approves even when knowledge.review_required=false
// (contracts/agent-profiles-tools.md §10.2).
func (a *Adapter) ProposeKnowledge(ctx context.Context, versionID, agentRunID int64, in tools.KnowledgeProposal) (tools.KnowledgeProposalResult, error) {
	if versionID <= 0 {
		return tools.KnowledgeProposalResult{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if a.knowledge == nil {
		return tools.KnowledgeProposalResult{}, errors.New("knowledgeadapter: knowledge service is not configured")
	}
	actor, _ := platform.ActorFrom(ctx)
	actor = platform.Actor{ID: actor.ID}

	sourceVersionID := versionID
	runID := agentRunID
	item, err := a.knowledge.Create(ctx, actor, knowledgeapp.CreateInput{
		Title:                 in.Title,
		Content:               in.Content,
		Scope:                 knowledge.Scope(in.Scope),
		SystemKey:             in.SystemKey,
		Tags:                  in.Tags,
		SourceVersionID:       &sourceVersionID,
		PromotedFromVersionID: &sourceVersionID,
		AgentRunID:            &runID,
	})
	if err != nil {
		return tools.KnowledgeProposalResult{}, err
	}
	return tools.KnowledgeProposalResult{
		ID:       item.ID,
		Status:   string(item.Status),
		Scope:    string(item.Scope),
		Revision: item.Revision,
	}, nil
}

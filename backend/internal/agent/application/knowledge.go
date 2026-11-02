package agentapp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// DefaultKnowledgeBudgetTokens mirrors the knowledge.injection_budget_tokens
// default (contracts/settings.md §4g). The context builder truncates the
// injected references to this budget as a last line of defence; the knowledge
// application service already applies the same setting when it selects items.
const DefaultKnowledgeBudgetTokens = 4000

// knowledgeSectionName is the context section id of the injected knowledge
// (contracts/agent-profiles-tools.md §10.3).
const knowledgeSectionName = "knowledge"

// KnowledgeRef is one approved knowledge item injected into a run context. It
// is a reference only: the full content, reviewer and internal paths never
// leave the knowledge API through the injection path.
type KnowledgeRef struct {
	ID      int64
	Title   string
	Snippet string
	Scope   string
	Tokens  int
}

// KnowledgeReader retrieves the approved knowledge visible to a version. The
// adapter owns the settings-driven item/token budget, the source_stale filter
// and the ts_rank ordering; systemKeys is an optional caller override (nil or
// empty lets the adapter derive the version's SUT families). query is the
// version/requirement title text used for full-text ranking (empty keeps the
// repository's id ordering). The second return value is the sum of the injected
// token estimates.
type KnowledgeReader interface {
	RetrieveForVersion(ctx context.Context, versionID int64, systemKeys []string, query string) ([]KnowledgeRef, int, error)
}

// KnowledgeQueryMaxChars bounds the derived FTS query. The repository uses
// websearch_to_tsquery, which tolerates punctuation; the cap only prevents a
// huge version/requirement title set from bloating every prompt request.
const KnowledgeQueryMaxChars = 512

// KnowledgeQuery builds the full-text query for one run from the version title
// and the requirement titles in the context pack. Duplicates are removed and
// whitespace is normalized; an empty pack yields "".
func KnowledgeQuery(pack VersionPack) string {
	parts := make([]string, 0, len(pack.Requirements)+1)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			return
		}
		if _, duplicate := seen[value]; duplicate {
			return
		}
		seen[value] = struct{}{}
		parts = append(parts, value)
	}
	add(pack.Title)
	for _, requirement := range pack.Requirements {
		add(requirement)
	}
	query := strings.Join(parts, " ")
	runes := []rune(query)
	if len(runes) > KnowledgeQueryMaxChars {
		query = strings.TrimSpace(string(runes[:KnowledgeQueryMaxChars]))
	}
	return query
}

// KnowledgeUsageRecorder appends one knowledge_usage row per injected item. The
// runner treats a failure as log-only (it never fails the run).
type KnowledgeUsageRecorder interface {
	RecordInjection(ctx context.Context, agentRunID, workItemID int64, refs []KnowledgeRef) error
}

// knowledgeBudgetTokens resolves the injection budget used by the context
// builder. A missing setting falls back to the documented default; a negative
// value does the same, while an explicit zero disables injection.
func (b *ContextBuilder) knowledgeBudgetTokens(ctx context.Context) int {
	return int(b.settingInt(ctx, "knowledge.injection_budget_tokens", DefaultKnowledgeBudgetTokens))
}

// fitKnowledge truncates the injected references to the token budget in
// repository (ts_rank) order: the first reference over budget and everything
// after it are dropped (contracts/agent-profiles-tools.md §10.3).
func fitKnowledge(refs []KnowledgeRef, budget int) []KnowledgeRef {
	if len(refs) == 0 || budget <= 0 {
		return nil
	}
	fitted := make([]KnowledgeRef, 0, len(refs))
	remaining := budget
	for _, ref := range refs {
		tokens := ref.Tokens
		if tokens < 0 {
			tokens = 0
		}
		if tokens > remaining {
			break
		}
		remaining -= tokens
		fitted = append(fitted, ref)
	}
	if len(fitted) == 0 {
		return nil
	}
	return fitted
}

// knowledgeContent renders the injected references as one context section:
// `[K-<id>] <title>（<scope>）：<snippet>` per line. An empty set renders no
// section at all so an un-injected run never carries an empty heading.
func knowledgeContent(refs []KnowledgeRef) string {
	if len(refs) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("# 知识\n")
	for _, ref := range refs {
		fmt.Fprintf(&builder, "[K-%d] %s（%s）：%s\n", ref.ID, ref.Title, ref.Scope, ref.Snippet)
	}
	return strings.TrimRight(builder.String(), "\n")
}

// knowledgeRefsResult renders injected references for the context.version_pack
// tool result (contracts/agent-profiles-tools.md §10.3): IDs are decimal
// strings and the full content is never exposed.
func knowledgeRefsResult(refs []KnowledgeRef) []map[string]any {
	knowledge := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		knowledge = append(knowledge, map[string]any{
			"id":      idText(ref.ID),
			"title":   ref.Title,
			"snippet": ref.Snippet,
			"scope":   ref.Scope,
			"tokens":  ref.Tokens,
		})
	}
	return knowledge
}

// knowledgeForVersion loads the injected knowledge for one run. Injection is
// supplemental context: a retrieval failure is logged and the run continues
// without knowledge instead of blocking the task.
func (r *Runner) knowledgeForVersion(ctx context.Context, pack VersionPack) []KnowledgeRef {
	if r.knowledge == nil {
		return nil
	}
	query := KnowledgeQuery(pack)
	refs, _, err := r.knowledge.RetrieveForVersion(ctx, pack.VersionID, nil, query)
	if err != nil {
		r.logKnowledgeFailure(ctx, "knowledge retrieval failed", pack.VersionID, err)
		return nil
	}
	return refs
}

// recordKnowledgeUsage writes the knowledge_usage rows for one run. The
// contract makes this best-effort: a failure is logged and never fails the run.
func (r *Runner) recordKnowledgeUsage(ctx context.Context, state *runState) {
	refs := state.input.VersionPack.Knowledge
	if r.knowledgeUsage == nil || len(refs) == 0 || !packetHasKnowledge(state.packet) {
		return
	}
	if err := r.knowledgeUsage.RecordInjection(ctx, state.run.ID, state.item.ID, refs); err != nil {
		r.logKnowledgeFailure(ctx, "knowledge usage record failed", state.run.ID, err)
	}
}

// packetHasKnowledge reports whether the persisted context packet actually
// carries the knowledge section (compaction may have dropped it).
func packetHasKnowledge(packet ContextPacket) bool {
	for _, section := range packet.Sections {
		if section.Name == knowledgeSectionName {
			return true
		}
	}
	return false
}

func (r *Runner) logKnowledgeFailure(ctx context.Context, message string, id int64, err error) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.WarnContext(ctx, message, "scope_id", id, "error", err)
}

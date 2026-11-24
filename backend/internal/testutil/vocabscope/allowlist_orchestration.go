package vocabscope

import "github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"

// OrchestrationGateAllowlist is the adjudicated exemption table of the orchestration / codeintel / knowledge / design / storage
// infrastructure scan surface. Moved verbatim from its gate by T11e-6 so the
// repository-level gate can reuse the same adjudications (no fifth copy).
//
// OrchestrationGateAllowlist adjudicates the genuine collisions between a domain value
// and an unrelated wire-format / prompt / log use of the same string. Each entry
// is fragment-scoped so it cannot exempt a live predicate, and every fragment is
// the exact text of ONE source line (whitespace-normalized, so gofmt alignment
// does not matter).
//
// Repaid in the same change set (T11e-3): every SQL status/kind/scope/trigger
// predicate of the five contexts is now bound as a query parameter and the one
// Go source-kind read references its domain constant, so NO reachable status
// predicate is exempted here. T11e-6 settled the exposure T11e-5 measured: the
// route_to_api edge predicate, the two requirement-priority defaults and the two
// design-conversation predicates are now bound as query parameters, and the
// extraction prompt's JSON-schema type placeholder joined this table as an
// adjudicated collision.
// What remains is non-status vocabulary, ONE FRAGMENT PER OCCURRENCE
// (T11e-6 M5 + review Minor 3: a site is a (line, occurrence-index) pair, so a
// line carrying the value twice needs two fragments). The value and fragment
// COUNTS are deliberately NOT written here: they are measured by the gates from
// this table, and a restated number is a second truth that drifts (review M1:
// round 1 removed the numbers from the gates but left them stale here).
// It covers non-status vocabulary or cross-context vocabulary with no Go
// authority (each reason names the producer/consumer evidence).
func OrchestrationGateAllowlist() map[string]vocablint.AllowEntry {
	return map[string]vocablint.AllowEntry{
		"error": {
			Reason: "slog log FIELD key in the leader-election retry warning (leader.go), " +
				"not quality.StatusError (an eval-run status). The log key convention has no " +
				"domain constant; the colliding authority owns a different concept.",
			Fragments: vocablint.FileFragments("backend/internal/orchestration/infrastructure/postgres/leader.go", `l.logger.Warn("orchestration leader election retry", "error", err, "delay", l.retry)`),
		},

		"unsupported": {
			Reason: "code_entity.meta JSON FIELD name in the file-projection meta blob " +
				"(entity_repo.go UpsertFileEntities): the serialized twin of TreeEntry.Unsupported, " +
				"consumed as a wire field by the code tree views — not version.AttachmentUnsupported " +
				"(an attachment format kind).",
			Fragments: vocablint.FileFragments("backend/internal/codeintel/infrastructure/postgres/entity_repo.go", `"unsupported":%t`),
		},

		"evidence": {
			Reason: "JSON FIELD names inside the frozen LLM prompt OUTPUT-SHAPE templates " +
				"(codeintel llm/prompt.go ExtractionSystemPrompt / ImpactSystemPrompt): they teach " +
				"the model the response schema's evidence arrays, not delivery.IssueRefKindEvidence " +
				"(a report/issue ref kind). The prompt text is a model-facing contract, not a " +
				"status predicate.",
			Fragments: vocablint.FileFragments("backend/internal/codeintel/infrastructure/llm/prompt.go", `"evidence":[{"start_line":1,"end_line":1}]}`, `"evidence":[{"start_line":1,"end_line":1}]}`, `"evidence":[{"file":"repo-relative path","start_line":1,"end_line":1}]}`),
		},

		"file": {
			Reason: "JSON FIELD name inside the ImpactSystemPrompt output-shape template " +
				"(llm/prompt.go): the evidence entry's file path field, not codeintel.EntityFile / " +
				"execution.EvidenceFile (entity/evidence kinds).",
			Fragments: vocablint.FileFragments("backend/internal/codeintel/infrastructure/llm/prompt.go", `"file":"repo-relative path"`),
		},

		"reason": {
			Reason: "JSON FIELD name inside the ImpactSystemPrompt output-shape template " +
				"(llm/prompt.go): the candidate's explanation field, not " +
				"execution.HumanPayloadReason/HumanResponseReason (human-request payload/response " +
				"vocabulary).",
			Fragments: vocablint.FileFragments("backend/internal/codeintel/infrastructure/llm/prompt.go", `"reason":"short explanation"`),
		},

		"system": {
			Reason: "OpenAI chat-completions WIRE ROLE in the codeintel LLM transcripts " +
				"(llm/prompt.go ExtractionMessages/ImpactMessages): llmapp.ChatMessage.Role is a " +
				"plain string wire field and the llm context defines no role constants (same " +
				"adjudication as version vision.go in T11e-2). storage.GCActorSystem / " +
				"version.MessageKindSystem own different concepts.",
			Fragments: vocablint.FileFragments("backend/internal/codeintel/infrastructure/llm/prompt.go", `{Role: "system", Content: ExtractionSystemPrompt},`, `{Role: "system", Content: ImpactSystemPrompt},`),
		},

		"user": {
			Reason: "OpenAI chat-completions WIRE ROLE in the codeintel LLM transcripts " +
				"(llm/prompt.go, both message builders): not storage.GCActorUser (a GC actor " +
				"kind). Same wire-role adjudication as 'system' above.",
			Fragments: vocablint.FileFragments("backend/internal/codeintel/infrastructure/llm/prompt.go", `{Role: "user", Content: builder.String()},`, `{Role: "user", Content: builder.String()},`),
		},
		"string": {
			Reason: "The JSON-Schema TYPE PLACEHOLDER inside the fixed T1 extraction prompt " +
				"(codeintel/infrastructure/llm/prompt.go ExtractionSystemPrompt: \"name\":\"string\", " +
				"\"src_kind\":\"string\", ...), telling the model which JSON type to emit. It is not " +
				"settings.TypeString (a settings value type). The prompt text is an LLM contract; " +
				"rewriting it would change extraction behaviour, so both prompt lines are exempted " +
				"as written (one fragment per line).",
			Fragments: vocablint.FileFragments("backend/internal/codeintel/infrastructure/llm/prompt.go", `{"facts":[{"kind":"route|page|api|client_call|component|symbol|module","name":"string","signature":"string","start_line":1,"end_line":1,"confidence":"high|medium|low","body":{},"evidence":[{"start_line":1,"end_line":1}]}],`, `{"facts":[{"kind":"route|page|api|client_call|component|symbol|module","name":"string","signature":"string","start_line":1,"end_line":1,"confidence":"high|medium|low","body":{},"evidence":[{"start_line":1,"end_line":1}]}],`, `"edges":[{"src_kind":"string","src_name":"string","dst_kind":"string","dst_name":"string","rel":"contains|imports|defines|calls|renders|route_to_api","confidence":"high|medium|low","evidence":[{"start_line":1,"end_line":1}]}]}`, `"edges":[{"src_kind":"string","src_name":"string","dst_kind":"string","dst_name":"string","rel":"contains|imports|defines|calls|renders|route_to_api","confidence":"high|medium|low","evidence":[{"start_line":1,"end_line":1}]}]}`, `"edges":[{"src_kind":"string","src_name":"string","dst_kind":"string","dst_name":"string","rel":"contains|imports|defines|calls|renders|route_to_api","confidence":"high|medium|low","evidence":[{"start_line":1,"end_line":1}]}]}`, `"edges":[{"src_kind":"string","src_name":"string","dst_kind":"string","dst_name":"string","rel":"contains|imports|defines|calls|renders|route_to_api","confidence":"high|medium|low","evidence":[{"start_line":1,"end_line":1}]}]}`),
		},
	}
}

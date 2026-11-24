package vocabscope

import "github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"

// AgentGateAllowlist is the adjudicated exemption table of the agent / chain / execution / llm / search / eventing
// infrastructure scan surface. Moved verbatim from its gate by T11e-6 so the
// repository-level gate can reuse the same adjudications (no fifth copy).
//
// AgentGateAllowlist adjudicates the genuine collisions between a domain value
// and an unrelated wire-format / DTO / log use of the same string. Each entry is
// fragment-scoped so it cannot exempt a live predicate, and every fragment is
// the exact text of ONE source line (whitespace-normalized, so gofmt alignment
// does not matter).
//
// Repaid in the same change set (T11e-4): every reachable status/kind predicate
// of the six contexts now reads a domain constant (Go) or binds one as a query
// parameter (SQL), so NO live predicate is exempted here. T11e-6 settled the
// exposure T11e-5 measured: the prompt-seed insert binds agent.PromptSourceSeed,
// the two hand-written executor_unavailable error codes now read the client's own
// CodeExecutorUnavailable, and the remaining sites joined this table as
// adjudicated collisions.
// What remains is non-status vocabulary, ONE FRAGMENT PER OCCURRENCE
// (T11e-6 M5 + review Minor 3: a site is a (line, occurrence-index) pair, so a
// line carrying the value twice needs two fragments). The value and fragment
// COUNTS are deliberately NOT written here: they are measured by the gates from
// this table, and a restated number is a second truth that drifts (review M1:
// round 1 removed the numbers from the gates but left them stale here).
// It covers non-status vocabulary (tool and message-part payload field names,
// slog keys, error-envelope field/entity/code names, SQL table names and
// aliases, OpenAI wire roles/values, URL scheme, executor protocol codes and
// ops, search diagnostic labels) plus the one Ruling T11d-R4 named legacy
// value. Entries documenting a vocabulary with NO Go authority
// anywhere (page/api as the catalog inventory discriminator, the chain pseudo-SUT
// display status, the discovery-observation source_kind) are escalated in the
// task report rather than given a fabricated authority.
func AgentGateAllowlist() map[string]vocablint.AllowEntry {
	return map[string]vocablint.AllowEntry{
		"account_role": {
			Reason: "The case-step tool payload FIELD name (designadapter caseDetailPayload steps: the step's " +
				"account role), not execution.HumanPayloadAccountRole (a human_request payload key, " +
				"execution/domain/human.go:333).",
			Fragments: vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go", `"account_role": step.AccountRole,`),
		},

		"active": {
			Reason: "The SYNTHETIC display status of the chain PSEUDO SUT label rows (providers/version.go " +
				"SUTs): these rows are label carriers, not catalog entities, and the real SUT lifecycle " +
				"is proposed/approved/rejected (resource/domain/sut.go), so no domain constant owns " +
				"\"active\" here. resource.StatusActive / identity.StatusActive / " +
				"orchestration.StatusActive / llm.StatusActive own other entities' lifecycles. ESCALATED " +
				"(report §12.3): if the controller wants a chain-owned label vocabulary it must be " +
				"created in chain/domain, which this batch did not do. Short-fragment precision limit " +
				"(H-A22): verified to hit only version.go:139 on today's surface.",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/version.go", `Status: "active",`),
		},

		"api": {
			Reason: "Two non-status uses. (a) tools.ResourcePage.Kind, the same unowned catalog INVENTORY " +
				"DISCRIMINATOR as 'page' above (escalated, report §12.2). (b) platform.NotFound's ENTITY " +
				"name in execution adapters/version_adapter.go SUTAPI. Neither codeintel.EntityAPI nor " +
				"execution.AssertionKindAPI owns either use. The short fragment is declared TWICE " +
				"because it sanctions two sites (resources.go:113/:181); the 1:1 check now enforces " +
				"that count instead of relying on a hand-verified precision claim (T11e-6 M5).",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/resources.go", `SUTID: sut.SUTID, ID: api.ID, Kind: "api", Name: api.Name, Path: api.PathTemplate, Method: api.Method,`, `Kind: "api",`, `Kind: "api",`),
				vocablint.FileFragments("backend/internal/execution/infrastructure/adapters/version_adapter.go", `return executionapp.SUTAPI{}, platform.NotFound("api")`),
			),
		},

		"assistant": {
			Reason: "The chat WIRE ROLE on the Responses input path (llm/infrastructure/responses.go: the " +
				"message-role switch and the replayed assistant message item). The llm context declares " +
				"no role authority and importing agent/domain from llm infrastructure would invert the " +
				"four-layer import direction. agent.WorkItemCreatedByAssistant is a work-item created_by " +
				"actor, a genuinely different concept; agent.MessageRoleAssistant / " +
				"version.MessageRoleAssistant however own the SAME role value that " +
				"agent/application/runner.go:1486 hand-writes as Role: \"assistant\" for the LLM request " +
				"of a persisted assistant message -- a cross-layer second truth on the PRODUCER side, " +
				"application layer and therefore the R2 / H-A19 open class (report appendix C), not " +
				"'a different concept' (T11e-6 review Minor 2 wording correction).",
			Fragments: vocablint.FileFragments("backend/internal/llm/infrastructure/responses.go", `case "assistant":`, `payload.Input = append(payload.Input, responsesMessageItem{Role: "assistant", Content: message.Content})`),
		},

		"automation": {
			Reason: "The platform error-envelope CODE of a target-database failure (execution " +
				"adapters/db_adapter.go targetDBError: platform.NewError(500, \"automation\", ...)). It is " +
				"an HTTP error code, not execution.FailureClassAutomation / " +
				"delivery.FailureClassAutomation (the case-run failure taxonomy).",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/adapters/db_adapter.go", `return platform.NewError(500, "automation", message+": "+cause.Error())`),
		},

		"blocked": {
			Reason: "The chain wave-counts meta KEY (WaveCounts.Blocked's json tag, wave.go:97), not a " +
				"verdict/failure-class predicate.",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"blocked": counts.Blocked,`),
		},

		"case": {
			Reason: "The same notConfigured DIAGNOSTIC label (search design provider), not " +
				"storage.OwnerKindCase.",
			Fragments: vocablint.FileFragments("backend/internal/search/infrastructure/providers/design.go", `return nil, notConfigured("case")`),
		},

		"case_run": {
			Reason: "Two non-status uses. (a) The knowledge_item.recipe PROVENANCE discriminator " +
				"(knowledgeadapter successRecipe): recipe is a free JSONB document and knowledge/domain " +
				"defines no recipe vocabulary, so this label has no Go authority; " +
				"delivery.IssueRefKindCaseRun / chain.NodeCaseRun own issue-ref and chain-node kinds. (b) " +
				"platform.NotFound's ENTITY name in the case-run existence guard (postgres/case_run.go " +
				"existsOrNotFound, two call sites).",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/knowledgeadapter/adapter.go", `recipe := map[string]any{"source": "case_run"}`),
				vocablint.FileFragments("backend/internal/execution/infrastructure/postgres/case_run.go", `return false, r.existsOrNotFound(ctx, "case_run", id, "transition case run")`, `return false, r.existsOrNotFound(ctx, "case_run", id, "claim case run lease")`),
			),
		},

		"code": {
			Reason: "The conversation message-part Data payload FIELD name for the job_card failure code and " +
				"the error part code (versionadapter/conversations.go, encode and decode), not " +
				"execution.HumanResponseCode (a human_request response key) / resource.APISourceCode (an " +
				"API catalog source).",
			Fragments: vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/conversations.go", `"code": part.Code,`, `"code": part.Code,`, `failureCode, _ := dataString(part.Data, "code")`, `code, _ := dataString(part.Data, "code")`),
		},

		"confirmed": {
			Reason: "design.graph_read tool payload FIELD name of the stats block (graphPayload stats " +
				"counters, twin of GraphStats.Confirmed), not design.CaseStatusConfirmed / " +
				"delivery.IssueStatusConfirmed (entity lifecycles).",
			Fragments: vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go", `"confirmed": view.Stats.Confirmed,`),
		},

		"download": {
			Reason: "The executor PROTOCOL v2 workspace FILES OPERATION name (executor/client.go " +
				"FileDownload, alongside upload/list), not execution.EvidenceDownload (an evidence kind). " +
				"The artifact-kind side of the same word is pinned cross-language by the recording.mjs " +
				"ARTIFACT_KINDS pin.",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/executor/client.go", `FileDownload = "download"`),
		},

		"draft": {
			Reason: "design.graph_read tool payload FIELD name of the stats block (designadapter " +
				"graphPayload: the draft/confirmed/stale case COUNTERS, the serialized twins of " +
				"GraphStats.Draft/Confirmed/Stale). A counter key is not a status predicate; " +
				"design.CaseStatusDraft / knowledge.StatusDraft / version.RequirementDraft own entity " +
				"lifecycles.",
			Fragments: vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go", `"draft": view.Stats.Draft,`),
		},

		"draining": {
			Reason: "The executor PROTOCOL v2 typed conflict CODE " +
				"(execution/infrastructure/executor/client.go CodeDraining), emitted by " +
				"executor/src/sessions.mjs:115 and switched on in executor/src/server.mjs:116. It is a " +
				"wire error code, not execution.ExecutorDraining / orchestration.StatusDraining (registry " +
				"and instance lifecycles).",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/executor/client.go", `CodeDraining = "draining"`),
		},

		"environment": {
			Reason: "platform.NotFound's ENTITY name (execution adapters/new_sut.go when the version's bound " +
				"environment id does not resolve), not the execution/delivery failure CLASS " +
				"\"environment\".",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/adapters/new_sut.go", `return executionapp.NewSUTResult{}, platform.NotFound("environment")`),
		},

		"error": {
			Reason: "Three non-status uses. (a) The conversation tool_call message-part Data payload FIELD " +
				"name (encode and decode). (b) slog FIELD keys: eventing listen.go retry/skip warnings, " +
				"execution delta_lock unlock/force-close warnings, executor client structured error " +
				"attribute. (c) The OpenAI chat-completions finish_reason VALUE returned for a failed " +
				"Responses reply, and the Responses SSE EVENT TYPE \"error\" " +
				"(llm/infrastructure/responses.go). quality.StatusError is an eval-run status; the llm " +
				"context defines no finish_reason/event-type constants (the vocabulary is the upstream " +
				"OpenAI wire contract).",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/conversations.go", `"error": part.Error,`, `errText, _ := dataString(part.Data, "error")`),
				vocablint.FileFragments("backend/internal/eventing/infrastructure/listen.go", `l.logger.Warn("event listener disconnected; retrying", "error", err, "delay", delay)`, `l.logger.Warn("event notification skipped", "error", err)`),
				vocablint.FileFragments("backend/internal/execution/infrastructure/adapters/delta_lock.go", `"lock_key", key, "error", err)`, `"lock_key", key, "error", closeErr)`),
				vocablint.FileFragments("backend/internal/execution/infrastructure/executor/client.go", `slog.String("error", err.Message),`),
				vocablint.FileFragments("backend/internal/llm/infrastructure/responses.go", `return "error"`, `case "error", "response.failed":`),
			),
		},

		"evidence": {
			Reason: "Two non-status uses. (a) The chain wave-counts meta KEY (WaveCounts.Evidence's json tag, " +
				"wave.go:102) -- NOT chain/domain/build.go's countEvidence, which owns the " +
				"ChainView.Counts aggregate, a different map. (b) The SQL TABLE NAME passed to " +
				"prefixedColumns in the evidence / evidence_ref projections (execution " +
				"postgres/evidence.go, evidence_read.go). delivery.IssueRefKindEvidence is an " +
				"issue/report ref kind; the content_ref owner kind of the same name is now bound through " +
				"storageapp.OwnerKindEvidence in this batch.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"evidence": counts.Evidence,`),
				vocablint.FileFragments("backend/internal/execution/infrastructure/postgres/evidence.go", "SELECT `+prefixedColumns(\"evidence\", evidenceColumns)+`"),
				vocablint.FileFragments("backend/internal/execution/infrastructure/postgres/evidence_read.go", "SELECT `+prefixedColumns(\"evidence_ref\", evidenceRefColumns)+`, `+prefixedColumns(\"evidence\", evidenceColumns)+`"),
			),
		},

		"failed": {
			Reason: "Two non-status uses. (a) The chain wave-counts meta KEY (WaveCounts.Failed's json tag, " +
				"wave.go:96). (b) The OpenAI Responses WIRE STATUS of the response object " +
				"(llm/infrastructure/responses.go parseResponsesResponse / responsesFinishReason): the " +
				"upstream status enum completed/incomplete/failed/cancelled, not any EzTest entity " +
				"status. The llm context defines no wire-status constants.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"failed": counts.Failed,`),
				vocablint.FileFragments("backend/internal/llm/infrastructure/responses.go", `if payload.Status == "failed" {`, `case "failed":`),
			),
		},

		"http": {
			Reason: "The URL SCHEME accepted for the LLM base URL (llm/infrastructure/openai.go endpointURL), " +
				"not execution.EvidenceHTTP (an evidence kind).",
			Fragments: vocablint.FileFragments("backend/internal/llm/infrastructure/openai.go", `if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {`),
		},

		"image": {
			Reason: "The remote model catalog's input_modalities WIRE VALUE (llm/infrastructure/openai.go " +
				"remoteModelPayload.remoteModel: a model that lists the \"image\" modality supports " +
				"vision), not version.AttachmentImage (an attachment kind).",
			Fragments: vocablint.FileFragments("backend/internal/llm/infrastructure/openai.go", `if strings.EqualFold(strings.TrimSpace(modality), "image") {`),
		},

		"incomplete": {
			Reason: "Two OpenAI WIRE values in responsesFinishReason: the Responses response STATUS " +
				"\"incomplete\" and the chat-completions FINISH_REASON \"incomplete\" it maps to. " +
				"execution.VerdictReasonIncomplete is a server-side verdict reason, a different " +
				"vocabulary; the llm context defines no wire-status/finish_reason constants.",
			Fragments: vocablint.FileFragments("backend/internal/llm/infrastructure/responses.go", `case "incomplete":`, `return "incomplete"`),
		},

		"inconclusive": {
			Reason:    "The chain wave-counts meta KEY (WaveCounts.Inconclusive's json tag, wave.go:98).",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"inconclusive": counts.Inconclusive,`),
		},

		"interrupted": {
			Reason:    "The chain wave-counts meta KEY (WaveCounts.Interrupted's json tag, wave.go:100).",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"interrupted": counts.Interrupted,`),
		},

		"issue": {
			Reason: "The DIAGNOSTIC label naming the unconfigured query service in the search provider's " +
				"error message (search/infrastructure/providers/providers.go:35 notConfigured -> \"search: " +
				"%s query service is not configured\"), not chain.NodeIssue. The search context has no " +
				"domain layer and no type enum.",
			Fragments: vocablint.FileFragments("backend/internal/search/infrastructure/providers/delivery.go", `return nil, notConfigured("issue")`),
		},

		"model": {
			Reason: "The OpenAI chat-completions WIRE REQUEST FIELD name in the connectivity probe " +
				"(llm/infrastructure/openai.go chatProbe), not execution.FailureClassModel / " +
				"delivery.ReportFailureClassModel (a failure class).",
			Fragments: vocablint.FileFragments("backend/internal/llm/infrastructure/openai.go", `"model": probeModel,`),
		},

		"page": {
			Reason: "tools.ResourcePage.Kind -- the model-facing SUT INVENTORY DISCRIMINATOR of the sut.read " +
				"/ sut.page_list tool payloads (agent/application/tools/registry.go:637 is a plain string " +
				"field). NO Go authority owns the catalog resource-kind vocabulary: " +
				"execution.AssetKindPage is the asset_registration FACT vocabulary (a wider set incl. db, " +
				"execution/domain/asset_registration.go:14) and codeintel.EntityPage is a code-entity " +
				"kind; resource/domain defines no kind enum at all (grep of every domain const). " +
				"ESCALATED to controller (report §12.2): creating the catalog-kind authority belongs to " +
				"the resource context, outside this batch's write scope. The short fragment is declared " +
				"TWICE because it sanctions two sites (resources.go:108/:172); the 1:1 check enforces " +
				"that count (T11e-6 M5).",
			Fragments: vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/resources.go", `SUTID: sut.SUTID, ID: page.ID, Kind: "page", Name: page.Name, Path: page.RouteTemplate,`, `Kind: "page",`, `Kind: "page",`),
		},

		"passed": {
			Reason: "The chain wave-counts meta KEY (WaveCounts.Passed's json tag, wave.go:95). The terminal " +
				"RESULT projection in the same file reads execution.CaseRunPassed through the domain " +
				"constant after T11e-4.",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"passed": counts.Passed,`),
		},

		"paused": {
			Reason: "The chain wave-counts meta KEY (WaveCounts.Paused's json tag, wave.go:94), not " +
				"execution.CaseRunPaused / WavePaused / version.StagePaused.",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"paused": counts.Paused,`),
		},

		"pending": {
			Reason: "Two adjudicated uses. (a) The chain wave-counts meta KEY (WaveCounts.Pending's json tag, " +
				"execution/domain/wave.go:92). (b) Ruling T11d-R4 NAMED LEGACY VALUE: the pre-0.2.1 " +
				"work_item status \"pending\" has no WorkItemStatus constant and the frozen work_item CHECK " +
				"forbids it on new rows; finishableWorkItemStatuses keeps it only to tolerate historical " +
				"rows (postgres/workqueue.go:44-51 comment). Creating a constant would create a dangling " +
				"status (constitution 三.7), so the legacy value stays named.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/postgres/workqueue.go", `string(agent.WorkItemWaitingHuman), "pending", string(agent.WorkItemOpen),`),
				vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"pending": counts.Pending,`),
			),
		},

		"question": {
			Reason: "The human_request.QUESTION COLUMN label passed to encodeJSONValue for the \"encode " +
				"question\" error message (execution postgres/human.go:56, helper at :314), not " +
				"version.MessageKindQuestion (a conversation message kind).",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/postgres/human.go", `question, err = encodeJSONValue("question", request.Question)`),
		},

		"reason": {
			Reason: "Four non-status uses. (a) platform.Invalid's FIELD name (postgres/job_control.go " +
				"CancelJob). (b) A GENERIC version_fact summary probe key list (versionadapter " +
				"briefingFactSummary tries summary/title/message/reason/description across arbitrary fact " +
				"kinds -- not the human_request payload). (c) The conversation message-part Data payload " +
				"FIELD name (rollback_proposal / job_card, encode and decode). (d) The design.gap " +
				"WORK-ITEM payload key (execution adapters/work_queue_adapter.go EnsureGap). " +
				"execution.HumanPayloadReason/HumanResponseReason own the human_request payload/response " +
				"keys; the two genuine human_request SQL reads of that key are parameterized in this " +
				"batch.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/postgres/job_control.go", `return agent.WorkItem{}, platform.Invalid("reason", "must not be empty")`),
				vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/briefing.go", `for _, key := range []string{"summary", "title", "message", "reason", "description"} {`),
				vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/conversations.go", `"reason": part.Reason,`, `"reason": part.Reason,`, `reason, _ := dataString(part.Data, "reason")`, `failureReason, _ := dataString(part.Data, "reason")`),
				vocablint.FileFragments("backend/internal/execution/infrastructure/adapters/work_queue_adapter.go", `"reason": reason,`),
			),
		},

		"report": {
			Reason: "The same notConfigured DIAGNOSTIC label (search delivery provider), not " +
				"delivery.IssueRefKindReport / chain.NodeReport / storage.OwnerKindReport.",
			Fragments: vocablint.FileFragments("backend/internal/search/infrastructure/providers/delivery.go", `return nil, notConfigured("report")`),
		},

		"requirement": {
			Reason: "The same notConfigured DIAGNOSTIC label (search requirement provider), not " +
				"delivery.ReportRefKindRequirement / chain.NodeRequirement / design.SourceRequirement.",
			Fragments: vocablint.FileFragments("backend/internal/search/infrastructure/providers/version.go", `return nil, notConfigured("requirement")`),
		},

		"running": {
			Reason: "The chain wave-counts meta KEY (execution.WaveCounts.Running's json tag, " +
				"execution/domain/wave.go:93), not a status predicate. The counts keys are the node meta " +
				"wire contract; the frontend side is pinned against the same struct tags by " +
				"frontend/src/features/execution/wave-counts.authority.ts.",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"running": counts.Running,`),
		},

		"skipped": {
			Reason:    "The chain wave-counts meta KEY (WaveCounts.Skipped's json tag, wave.go:99).",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"skipped": counts.Skipped,`),
		},

		"stale": {
			Reason: "Two wire FIELD names. (a) design.graph_read stats counter key (graphPayload, twin of " +
				"GraphStats.Stale). (b) The chain case-version node meta key whose value is a BOOLEAN " +
				"staleness flag (providers/design.go casesFromGraph: \"stale\": caseVersionStale(...)), not " +
				"a status value -- the status predicate itself reads design.CaseStatusStale/Superseded " +
				"through the domain constants after T11e-4.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go", `"stale": view.Stats.Stale,`),
				vocablint.FileFragments("backend/internal/chain/infrastructure/providers/design.go", `"stale": caseVersionStale(card.Status),`),
			),
		},

		"status": {
			Reason: "Three non-status uses. (a) design tool payload FIELD name (designadapter " +
				"requirement/case/case-version payloads: item.Status / view.Status / selected.Status). " +
				"(b) The conversation message-part Data payload FIELD name on BOTH the encode and the " +
				"decode side (versionadapter/conversations.go requirement_card / tool_call / job_card / " +
				"case_card), whose wire shape is version.MessagePart's Data document. (c) " +
				"platform.Invalid's FIELD name in the FinishRun error envelope (postgres/runtime.go). " +
				"agent.PostKindStatus is a team-post kind, a different concept.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go", `"status": item.Status,`, `"status": item.Status,`, `"status": view.Status,`, `"status": view.Status,`, `"status": selected.Status,`),
				vocablint.FileFragments("backend/internal/agent/infrastructure/postgres/runtime.go", `return platform.Invalid("status", "must be one of running, done, failed, aborted, interrupted")`),
				vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/conversations.go", `"status": part.Status,`, `"status": part.Status,`, `"status": part.Status,`, `"status": part.Status,`, `status, _ := dataString(part.Data, "status")`, `status, _ := dataString(part.Data, "status")`, `status, _ := dataString(part.Data, "status")`, `status, _ := dataString(part.Data, "status")`),
			),
		},

		"superseded": {
			Reason:    "The chain wave-counts meta KEY (WaveCounts.Superseded's json tag, wave.go:101).",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"superseded": counts.Superseded,`),
		},

		"sut": {
			Reason: "Two non-status uses. (a) platform.NotFound's ENTITY name in five adapters " +
				"(versionadapter ReadSUT/ListPages/ListDatabases, execution credential_adapter " +
				"Account/Database). (b) The chain PSEUDO node kind of the SUT label rows " +
				"(providers/version.go nodeKindSUT): chain/domain deliberately has no sut node kind -- " +
				"ChainService only consumes these rows' RefID/Label pairs to resolve SUT labels. " +
				"delivery.ReportRefKindSUT owns a report-ref kind, a different concept.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/resources.go", `return tools.SUTDetail{}, platform.NotFound("sut")`, `return nil, platform.NotFound("sut")`, `return nil, platform.NotFound("sut")`),
				vocablint.FileFragments("backend/internal/chain/infrastructure/providers/version.go", `const nodeKindSUT chain.NodeKind = "sut"`),
				vocablint.FileFragments("backend/internal/execution/infrastructure/adapters/credential_adapter.go", `return executionapp.AccountCredential{}, platform.NotFound("sut")`, `return executionapp.DatabaseCredential{}, platform.NotFound("sut")`),
			),
		},

		"sut_id": {
			Reason: "Four non-status uses. (a) The assertion tool payload FIELD name (designadapter " +
				"caseDetailPayload assertions). (b) platform.Invalid's FIELD name (versionadapter " +
				"ReadSUT). (c) The chain SUT label node meta KEY (providers/version.go). (d) A slog FIELD " +
				"key in the two discovery-observation skip warnings " +
				"(postgres/discovery_observation_repo.go). execution.HumanPayloadSUTID owns the " +
				"human_request payload key, none of these.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go", `"sut_id": formatOptionalID(assertion.SUTID),`),
				vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/resources.go", `return tools.SUTDetail{}, platform.Invalid("sut_id", "must be a positive decimal ID")`),
				vocablint.FileFragments("backend/internal/chain/infrastructure/providers/version.go", `"sut_id": sut.ID,`),
				vocablint.FileFragments("backend/internal/execution/infrastructure/postgres/discovery_observation_repo.go", `"sut_id", observation.SUTID,`, `"sut_id", observation.SUTID,`),
			),
		},

		"text": {
			Reason: "The OpenAI WIRE CONTENT-PART TYPE: chat-completions content parts (openai_chat.go) and " +
				"Responses reasoning content parts (responses.go, alongside \"reasoning_text\"). Not " +
				"execution.EvidenceText / AssertionKindText / version.MessageKindText.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/llm/infrastructure/openai_chat.go", `parts = append(parts, chatContentPart{Type: "text", Text: m.Content})`),
				vocablint.FileFragments("backend/internal/llm/infrastructure/responses.go", `if part.Type == "reasoning_text" || part.Type == "text" {`),
			),
		},

		"trigger": {
			Reason: "The chain wave node meta KEY carrying run_wave.trigger (providers/execution.go " +
				"waveNode), not agent.WorkItemCreatedByTrigger (a work-item created_by actor).",
			Fragments: vocablint.FileFragments("backend/internal/chain/infrastructure/providers/execution.go", `"trigger": wave.Trigger,`),
		},

		"user": {
			Reason: "The OpenAI WIRE ROLE in the two connectivity probes (chat-completions messages and " +
				"Responses input items, llm/infrastructure/openai.go). llmapp.ChatMessage.Role and " +
				"responsesMessageItem.Role are plain string wire fields and the llm context defines no " +
				"role constants (same adjudication as T11e-2 version vision.go and T11e-3 codeintel " +
				"prompt.go). storage.GCActorUser is a GC actor kind; agent.MessageRoleUser / " +
				"version.MessageRoleUser own the persisted TRANSCRIPT role of an agent/conversation " +
				"message, not the upstream request role. These two sites are CONNECTIVITY PROBES " +
				"(a synthetic ping), not transcript messages, so no persisted-role authority applies " +
				"to them; the application-side transcript producers that DO hand-write the wire role " +
				"are the R2 / H-A19 open class (report appendix C).",
			Fragments: vocablint.FileFragments("backend/internal/llm/infrastructure/openai.go", `{"role": "user", "content": "ping"},`, `responsesMessageItem{Role: "user", Content: "ping"},`),
		},

		"version": {
			Reason: "The same notConfigured DIAGNOSTIC label (search version provider), not " +
				"delivery.ReportKindVersion.",
			Fragments: vocablint.FileFragments("backend/internal/search/infrastructure/providers/version.go", `return nil, notConfigured("version")`),
		},
		"requirements": {
			Reason: "Two non-status uses. (a) The design.graph_read tool payload FIELD name of the " +
				"stats block and of the requirement list (designadapter graphPayload " +
				"view.Stats.Requirements / requirements). (b) The notConfigured DIAGNOSTIC label of the " +
				"version requirement adapter (versionadapter/requirements.go a.missing(...), five call " +
				"sites), the same class as search's notConfigured(\"version\"). chain.countRequirements " +
				"(through chainCountKeys) owns a CHAIN NODE counter, a different map. Two fragments for " +
				"the two identical RequirementSummary lines keep the registry 1:1 (M5).",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go",
					`"requirements": view.Stats.Requirements,`, `"requirements":    requirements,`),
				vocablint.FileFragments("backend/internal/agent/infrastructure/versionadapter/requirements.go",
					`return tools.RequirementDetail{}, a.missing("requirements")`,
					`return tools.RequirementSummary{}, a.missing("requirements")`,
					`return tools.RequirementSummary{}, a.missing("requirements")`,
					`return a.missing("requirements")`,
					`return nil, a.missing("requirements")`),
			),
		},

		"scenarios": {
			Reason: "The design.graph_read / requirement-detail tool payload FIELD names " +
				"(designadapter view.Stats.Scenarios and the scenario list), not chain.countScenarios " +
				"(a chain node counter).",
			Fragments: vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go",
				`"scenarios":    view.Stats.Scenarios,`, `"scenarios":              scenarios,`),
		},

		"cases": {
			Reason: "The design tool payload FIELD names (designadapter view.Stats.Cases and the " +
				"scenario's case list), not chain.countCases (a chain node counter).",
			Fragments: vocablint.FileFragments("backend/internal/agent/infrastructure/designadapter/adapter.go",
				`"cases":        view.Stats.Cases,`, `"cases":                  cases,`),
		},

		"source": {
			Reason: "Two non-status uses. (a) The knowledge_item.recipe JSONB provenance KEY " +
				"(knowledgeadapter successRecipe), whose VALUE \"case_run\" is a separate named debt. " +
				"(b) The chain issue node meta KEY (providers/delivery.go issue.Source). " +
				"delivery.IssueRefRoleSource owns a delivery_issue_ref.role, a different column.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/agent/infrastructure/knowledgeadapter/adapter.go",
					`recipe := map[string]any{"source": "case_run"}`),
				vocablint.FileFragments("backend/internal/chain/infrastructure/providers/delivery.go",
					`"source":         issue.Source,`),
			),
		},

		"run": {
			Reason: "The SQL TABLE ALIAS of case_run in LatestByCaseVersions " +
				"(execution/infrastructure/postgres/case_run.go: FROM case_run run, " +
				"prefixedColumns(\"run\", ...)), not agent.PromptVariableStabilityRun (a prompt " +
				"variable stability class). An alias is not a status read.",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/postgres/case_run.go",
				"SELECT DISTINCT ON (run.case_version_id) `+prefixedColumns(\"run\", caseRunColumns)+`"),
		},

		"human_request": {
			Reason: "The platform.NotFound ENTITY name in the error envelope " +
				"(execution/infrastructure/postgres/human.go existsOrNotFound), the same adjudicated " +
				"class as case_run / sut. execution.EvidenceOwnerHumanRequest owns an evidence owner " +
				"kind, a different concept.",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/postgres/human.go",
				`return false, r.existsOrNotFound(ctx, "human_request", id, "transition human request")`,
				`return r.existsOrNotFound(ctx, "human_request", id, "attach human request message")`),
		},

		"auto": {
			Reason: "The OpenAI WIRE values tool_choice=\"auto\" (openai_chat.go and responses.go " +
				"request payloads) and reasoning.summary=\"auto\" (responses.go). They are provider " +
				"protocol literals, not llm.ThinkingTypeAuto (a settings thinking type) nor " +
				"execution.WaveTriggerAuto (a wave trigger).",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/llm/infrastructure/openai_chat.go",
					`payload.ToolChoice = "auto"`),
				vocablint.FileFragments("backend/internal/llm/infrastructure/responses.go",
					`payload.ToolChoice = "auto"`, `reasoning.Summary = "auto"`),
			),
		},

		"tool": {
			Reason: "The chat WIRE role switched on when translating a normalized request message " +
				"into a function_call_output item (llm/infrastructure/responses.go). The llm context " +
				"declares NO role authority of its own, and importing agent/domain from llm " +
				"infrastructure would invert the four-layer import direction -- the same shape as " +
				"the already-exempted assistant / user wire roles. NOT 'a different concept' " +
				"(T11e-6 review Minor 2 corrected that wording): agent/application/runner.go:1758 " +
				"persists Role: agent.MessageRoleTool and :1768 hand-writes Role: \"tool\" for the " +
				"very same message's LLM request, so the producer side IS a cross-layer second " +
				"truth. That site is application layer, outside this gate's infra surface, and is " +
				"registered as the R2 / H-A19 open class (task-11e-6-report.md appendix C); the " +
				"domain constant already exists, so repaying it costs one line.",
			Fragments: vocablint.FileFragments("backend/internal/llm/infrastructure/responses.go",
				`case "tool":`),
		},

		"executor_timeout": {
			Reason: "The executor PROTOCOL v2 typed platform CODE " +
				"(execution/infrastructure/executor/client.go CodeExecutorTimeout), the same " +
				"adjudicated class as CodeDraining. It is a WIRE error code, not the persisted " +
				"case_run.summary.terminal_cause: execution/application.classifyExecutorError maps it " +
				"onto execution.TerminalCauseExecutorTimeout OR onto " +
				"TerminalCauseExecutorContextCreateFailed for a session create -- a non-identity " +
				"mapping, so the two vocabularies are distinct even where they share a spelling.",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/executor/client.go",
				`CodeExecutorTimeout = "executor_timeout"`),
		},

		"executor_unavailable": {
			Reason: "The executor PROTOCOL typed platform CODE this client produces for an " +
				"unreachable executor / protocol failure (CodeExecutorUnavailable). T11e-6 REPAID the " +
				"two hand-written use sites in unavailable()/protocolError() by declaring the constant " +
				"beside its siblings; only the declaration remains. classifyExecutorError maps the " +
				"wire code onto execution.TerminalCauseExecutorUnavailable, onto " +
				"TerminalCauseExecutorContextCreateFailed for a session create, or onto the " +
				"executor_lost FAILURE CLASS for a dial failure -- three different targets, so the " +
				"wire code is not the domain terminal cause.",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/executor/client.go",
				`CodeExecutorUnavailable = "executor_unavailable"`),
		},

		"lease_expired": {
			Reason: "The executor PROTOCOL v2 typed conflict CODE (client.go CodeLeaseExpired, " +
				"protocol v2 lease fencing), switched on by execution/application.sessionGone. " +
				"execution.TerminalCauseLeaseExpired is the persisted terminal cause of an expired " +
				"lease on an ONLINE executor with no recorded transport failure " +
				"(application/recovery.go) -- a different decision, same spelling.",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/executor/client.go",
				`CodeLeaseExpired       = "lease_expired"`),
		},

		"browser_cli_unavailable": {
			Reason: "The executor's typed 503 WIRE code for a missing official playwright CLI " +
				"runtime, produced by executor/src/browser_cli.mjs and mirrored by client.go " +
				"CodeBrowserCliUnavailable. execution.executorLimitationCodes is the domain READER " +
				"side that classifies the same wire code into terminal_cause=executor_capability; the " +
				"client constant is the producer side of the protocol, not a status read.",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/executor/client.go",
				`CodeBrowserCliUnavailable = "browser_cli_unavailable"`),
		},

		"execution": {
			Reason: "The execution_discovery_observation.source_kind constant of the upsert. Its " +
				"FIRST TRUTH is the DB CHECK chk_execution_discovery_observation_source_kind " +
				"(execution_0006:35-37, IN ('execution','p9')); there is NO Go authority. " +
				"agent.TeamExecution / chain.LayerExecution / chain.DegradedLayerOrder are other " +
				"concepts (a team, a chain layer). NAMED RESIDUAL H-A24: the CHECK also admits the " +
				"phase code 'p9', which AGENTS.md 2.2 forbids as a name, so a Go authority covering " +
				"the whole CHECK cannot be created without a migration + data change the owner must " +
				"approve. Escalated to the controller (W5 naming cleanup / W7 adjudication); this " +
				"task neither invents a partial mirror nor edits an append-only migration.",
			Fragments: vocablint.FileFragments("backend/internal/execution/infrastructure/postgres/discovery_observation_repo.go",
				`'execution', $13, $14`),
		},
	}
}

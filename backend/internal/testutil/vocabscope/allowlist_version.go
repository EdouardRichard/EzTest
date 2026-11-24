package vocabscope

import "github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"

// VersionGateAllowlist is the adjudicated exemption table of the version
// infrastructure scan surface. Moved verbatim from its gate by T11e-6 so the
// repository-level gate can reuse the same adjudications (no fifth copy).
//
// VersionGateAllowlist adjudicates the genuine collisions between a
// domain value and an unrelated use of the same string in the version
// infrastructure. Each entry is fragment-scoped (occurrence coverage) so it can
// never exempt a live predicate that merely shares the line. Every fragment is the
// exact text of ONE source line (whitespace-normalized, so gofmt alignment does not
// matter). Repaid in the same change set (T11e-2): every SQL status predicate is now
// bound as a query parameter and every Go status read references its domain
// constant, so NO status predicate is exempted here. T11e-6 settled the exposure
// T11e-5 measured: the two job_card part predicates and the reconcilable-conversation
// kind list now bind version.MessagePartJobCard / ConversationClarification /
// ConversationDesign as query parameters.
// What remains is non-status vocabulary, ONE FRAGMENT PER OCCURRENCE
// (T11e-6 M5 + review Minor 3: a site is a (line, occurrence-index) pair, so a
// line carrying the value twice needs two fragments). The value and fragment
// COUNTS are deliberately NOT written here: they are measured by the gates from
// this table, and a restated number is a second truth that drifts (review M1:
// round 1 removed the numbers from the gates but left them stale here).
// It covers non-status vocabulary: extraction element kinds, resource-kind
// selectors, fact-payload keys, an XML tag name, import paths, an LLM wire role
// and a display placeholder.
//
// The handoff_repo.go entries reuse the SAME fragments and reasons as the handoff
// gate (handoffLiteralAllowlist in handoff_repo_lint_test.go), never a second
// wording (task-11d-report.md §4.3).
func VersionGateAllowlist() map[string]vocablint.AllowEntry {
	return map[string]vocablint.AllowEntry{
		"user": {
			Reason: "The LLM chat-message role in the OpenAI-compatible ChatRequest " +
				"(llmapp.ChatMessage.Role is a plain string wire field with no domain constant). " +
				"A different concept from identity.RoleUser / agent.MessageRoleUser / " +
				"version.MessageRoleUser (the admin/user functional role and the conversation " +
				"message role) and storage.GCActorUser (the GC actor kind); sourcing any of them " +
				"would assert a false authority over the LLM protocol role.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/adapters/vision.go", `Role:    "user",`),
		},

		"model": {
			Reason: "The error-envelope DETAIL key read from a ResolveModel failure " +
				"(apiErr.Details[\"model\"]), not the delivery/execution failure CLASS 'model'.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/adapters/vision.go", `availability.ModelName = detailString(apiErr.Details, "model")`),
		},

		"html": {
			Reason: "The Go standard-library html package import (aliased htmlpkg, as the " +
				"file already does), not version.AttachmentHTML / storage.FormatHTML.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/attachments/html.go", `htmlpkg "html"`),
		},

		"image": {
			Reason: "Two non-status uses. (a) The Go standard-library image package import " +
				"(aliased imagepkg so the import fragment is distinctive; a bare \"image\" fragment " +
				"would value-level-exempt). (b) The attachment EXTRACTION ELEMENT kind " +
				"(ExtractionElement.Kind / SkippedElement.Kind / elementCounts key), a parser-local " +
				"vocabulary consumed by version/application/extraction.go's segmentBlockBuilder; no " +
				"domain constant owns it, and it is a different concept from version.AttachmentImage " +
				"(the attachment FORMAT kind) and version.MessagePartImage (a message part)." +
				"NAMED RESIDUAL H-A20a (T11e-6 disposition): the attachment EXTRACTION ELEMENT kind" +
				"vocabulary has no authority; it is produced by version/infrastructure/attachments and" +
				"consumed by version/application/extraction.go's kind switches. Proposed authority: an" +
				"exported version/application.ExtractionElementKind* group (the layer that owns" +
				"ExtractionElement), sourced by both sides like deliveryapp.IssueActorKind*. Building it" +
				"rewires ~40 sites across two layers and adds protected values, so it needs its own task" +
				"with a fresh exposure measurement; owner adjudication requested.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/version/infrastructure/attachments/pdf.go", `imagepkg "image"`, `if err := builder.skip("image", skip.source, skip.reason); err != nil {`),
				vocablint.FileFragments("backend/internal/version/infrastructure/attachments/ooxml.go", `if err := builder.skip("image", reference.Part+"#"+reference.ID, "image relationship has no target"); err != nil {`, `if err := builder.skip("image", mediaPath, "referenced image is missing from the package"); err != nil {`, `if err := builder.skip("image", slide.file.Name+"#"+id, "image relationship has no target"); err != nil {`, `if err := builder.skip("image", notesPath+"#"+id, "image relationship has no target"); err != nil {`, `return builder.skip("image", name, "media part is missing")`),
				vocablint.FileFragments("backend/internal/version/infrastructure/attachments/parser.go", `if kind == "image" {`, `b.elementCounts["image"]++`, `return b.skip("image", source, "embedded image object is empty")`, `return b.skip("image", source, "embedded image is not png/jpeg/webp: "+err.Error())`),
			),
		},

		"note": {
			Reason: "The attachment EXTRACTION ELEMENT kind for a pptx speaker-notes part " +
				"(ExtractionElement.Kind / SkippedElement.Kind), a parser-local vocabulary with no " +
				"domain constant; a different concept from execution.HumanResponseNote (a " +
				"human-request response kind)." +
				"NAMED RESIDUAL H-A20a (T11e-6 disposition): the attachment EXTRACTION ELEMENT kind" +
				"vocabulary has no authority; it is produced by version/infrastructure/attachments and" +
				"consumed by version/application/extraction.go's kind switches. Proposed authority: an" +
				"exported version/application.ExtractionElementKind* group (the layer that owns" +
				"ExtractionElement), sourced by both sides like deliveryapp.IssueActorKind*. Building it" +
				"rewires ~40 sites across two layers and adds protected values, so it needs its own task" +
				"with a fresh exposure measurement; owner adjudication requested.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/attachments/ooxml.go", `if err := builder.skip("note", notesPath, "speaker notes part is missing"); err != nil {`, `Kind: "note", Text: text, Slide: slide.number, Source: notesPath,`),
		},

		"page": {
			Reason: "Two non-status uses. (a) The attachment EXTRACTION ELEMENT kind for a PDF " +
				"page (ExtractionElement.Kind / SkippedElement.Kind), a parser-local vocabulary with " +
				"no domain constant; different from codeintel.EntityPage / design.SourcePage / " +
				"execution.AssetKindPage. (b) The resource-kind SELECTOR in ReferencingVersions: " +
				"resource/application/service.go's checkUsage callers pass \"page\" as a raw literal " +
				"(also the audit/error entity name); no domain constant owns this cross-context " +
				"resource-kind convention." +
				"NAMED RESIDUAL H-A20a (T11e-6 disposition): the attachment EXTRACTION ELEMENT kind" +
				"vocabulary has no authority; it is produced by version/infrastructure/attachments and" +
				"consumed by version/application/extraction.go's kind switches. Proposed authority: an" +
				"exported version/application.ExtractionElementKind* group (the layer that owns" +
				"ExtractionElement), sourced by both sides like deliveryapp.IssueActorKind*. Building it" +
				"rewires ~40 sites across two layers and adds protected values, so it needs its own task" +
				"with a fresh exposure measurement; owner adjudication requested. NAMED RESIDUAL H-A20b" +
				"(T11e-6 disposition): the cross-context RESOURCE-KIND selector vocabulary" +
				"{page,sut,environment,api} has no authority; resource/application/service.go's checkUsage" +
				"callers pass raw literals and this switch reads them. Proposed authority: a" +
				"resource/domain kind enum (the context that owns the catalog), sourced by both contexts." +
				"Cross-context wiring outside this task's write scope; owner adjudication requested.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/version/infrastructure/attachments/pdf.go", `Kind: "page", Text: page.text, Page: page.number, Source: fmt.Sprintf("pdf:page:%d", page.number),`, `if err := builder.skip("page", fmt.Sprintf("pdf:page:%d", pageNumber), "page has no extractable text layer"); err != nil {`),
				vocablint.FileFragments("backend/internal/version/infrastructure/postgres.go", `case "page":`),
			),
		},

		"text": {
			Reason: "Two non-status uses. (a) The OOXML xlsx-comment XML element local name " +
				"(element.Name.Local == \"text\"), a markup tag, not a text-kind vocabulary. " +
				"(b) The fact-payload summary key list (handoffFactSummary) — same fragment and " +
				"reason as the handoff gate, not the MessagePartText discriminator." +
				"NAMED RESIDUAL H-A20c (T11e-6 disposition): the version_fact PAYLOAD-KEY vocabulary has no" +
				"authority; writers and readers each spell the JSON key. Proposed authority:" +
				"version/domain.FactPayloadKey* sourced by every writer/reader. Own task; owner" +
				"adjudication requested.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/version/infrastructure/attachments/ooxml.go", `case "text":`, `case "text":`),
				vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo.go", `"summary", "text", "value", "decision", "title"`),
			),
		},

		"unknown": {
			Reason: "The display placeholder author name for an xlsx comment with no author, " +
				"not delivery.ReportFailureClassUnknown / execution.FailureClassUnknown (a failure " +
				"class).",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/attachments/ooxml.go", `author = "unknown"`),
		},

		"waived": {
			Reason: "The check_result JSON FIELD name (ClosureCheckResult.Waived's json tag, " +
				"version/domain/requirement_closure.go:97) read via JSONB extraction, not the " +
				"closure-kind VALUE version.ClosureKindWaived / delivery.ReportClosureKindWaived.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres.go", `SELECT (rc.check_result->>'waived')::boolean`),
		},

		"sut": {
			Reason: "The resource-kind SELECTOR in ReferencingVersions: " +
				"resource/application/service.go's checkUsage callers pass \"sut\" as a raw literal " +
				"(also the audit/error entity name); no domain constant owns this cross-context " +
				"resource-kind convention. delivery.ReportRefKindSUT is a report-ref kind, a " +
				"different concept. (Newly protected value: T11e-1 added delivery.ReportRefKindSUT.)" +
				"NAMED RESIDUAL H-A20b (T11e-6 disposition): the cross-context RESOURCE-KIND selector" +
				"vocabulary {page,sut,environment,api} has no authority; resource/application/service.go's" +
				"checkUsage callers pass raw literals and this switch reads them. Proposed authority: a" +
				"resource/domain kind enum (the context that owns the catalog), sourced by both contexts." +
				"Cross-context wiring outside this task's write scope; owner adjudication requested.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres.go", `case "sut":`),
		},

		"environment": {
			Reason: "The resource-kind SELECTOR in ReferencingVersions: " +
				"resource/application/service.go's checkUsage callers pass \"environment\" as a raw " +
				"literal (also the audit/error entity name); no domain constant owns this " +
				"cross-context resource-kind convention. A different concept from the " +
				"delivery/execution failure CLASS 'environment'." +
				"NAMED RESIDUAL H-A20b (T11e-6 disposition): the cross-context RESOURCE-KIND selector" +
				"vocabulary {page,sut,environment,api} has no authority; resource/application/service.go's" +
				"checkUsage callers pass raw literals and this switch reads them. Proposed authority: a" +
				"resource/domain kind enum (the context that owns the catalog), sourced by both contexts." +
				"Cross-context wiring outside this task's write scope; owner adjudication requested.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres.go", `case "environment":`),
		},

		"api": {
			Reason: "The resource-kind SELECTOR in ReferencingVersions: " +
				"resource/application/service.go's checkUsage callers pass \"api\" as a raw literal " +
				"(also the audit/error entity name); no domain constant owns this cross-context " +
				"resource-kind convention. A different concept from codeintel.EntityAPI / " +
				"design.SourceAPI / execution.AssetKindAPI / execution.AssertionKindAPI." +
				"NAMED RESIDUAL H-A20b (T11e-6 disposition): the cross-context RESOURCE-KIND selector" +
				"vocabulary {page,sut,environment,api} has no authority; resource/application/service.go's" +
				"checkUsage callers pass raw literals and this switch reads them. Proposed authority: a" +
				"resource/domain kind enum (the context that owns the catalog), sourced by both contexts." +
				"Cross-context wiring outside this task's write scope; owner adjudication requested.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres.go", `case "api":`),
		},

		"decision": {
			Reason: "fact-payload summary key list (handoffFactSummary), not a version_fact.kind filter" +
				"NAMED RESIDUAL H-A20c (T11e-6 disposition): the version_fact PAYLOAD-KEY vocabulary has no" +
				"authority; writers and readers each spell the JSON key. Proposed authority:" +
				"version/domain.FactPayloadKey* sourced by every writer/reader. Own task; owner" +
				"adjudication requested.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo.go", `"summary", "text", "value", "decision", "title"`),
		},

		"requirement": {
			Reason: "the handoff open-item Kind (handoff-local) and the fact-payload key (an unowned cross-context convention). The MessageRef.Kind ref predicate now sources version.MessageRefKindRequirement, so it is NOT exempted here. Of the nine constants whose value is 'requirement' (ConfirmationScopeRequirement, chain.NodeRequirement, delivery.IssueRefKindRequirement, design.SourceRequirement, ...), none owns the fact-payload-key vocabulary." +
				"NAMED RESIDUAL H-A20c (T11e-6 disposition): the version_fact PAYLOAD-KEY vocabulary has no" +
				"authority; writers and readers each spell the JSON key. Proposed authority:" +
				"version/domain.FactPayloadKey* sourced by every writer/reader. Own task; owner" +
				"adjudication requested.",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo.go", `"requirement_id", "requirement"`, `v.payload->>'requirement'`, `v.payload->>'requirement'`),
		},

		"reason": {
			Reason: "The stage_transition version_fact PAYLOAD key: insertStageTransitionFact " +
				"(stage.go) writes {from,to,reason,actor} and StageReasonReader.LastStageReason " +
				"(stage_reason.go) reads payload->>'reason'. An unowned fact-payload convention with " +
				"no domain constant; execution.HumanPayloadReason / HumanResponseReason own the " +
				"human-request payload/response vocabulary, a different concept." +
				"NAMED RESIDUAL H-A20c (T11e-6 disposition): the version_fact PAYLOAD-KEY vocabulary has no" +
				"authority; writers and readers each spell the JSON key. Proposed authority:" +
				"version/domain.FactPayloadKey* sourced by every writer/reader. Own task; owner" +
				"adjudication requested.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/version/infrastructure/stage.go", `"reason": next.Reason,`),
				vocablint.FileFragments("backend/internal/version/infrastructure/stage_reason.go", `SELECT COALESCE(payload->>'reason', '')`),
			),
		},
	}
}

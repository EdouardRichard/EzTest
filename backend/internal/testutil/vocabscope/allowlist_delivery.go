package vocabscope

import "github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"

// DeliveryGateAllowlist is the adjudicated exemption table of the delivery
// infrastructure scan surface. Moved verbatim from its gate by T11e-6 so the
// repository-level gate can reuse the same adjudications (no fifth copy).
//
// DeliveryGateAllowlist adjudicates the genuine collisions between a domain
// value and an unrelated wire-format / template / log use of the same string.
// Each entry is fragment-scoped so it cannot exempt a live predicate, and every
// fragment is the exact text of ONE source line (whitespace-normalized, so gofmt
// alignment does not matter).
//
// Repaid in the same change set (T11e-1): every SQL status/kind predicate is now
// bound as a query parameter and every Go status/kind read references its domain
// constant, so NO status predicate is exempted here. T11e-6 settled the exposure
// T11e-5 measured: the issue-severity label now reads delivery.IssueSeverityMinor
// and the recurrence status-history reason reads delivery.IssueHistoryReasonRecurrence
// (both authorities created in this change set), while the v3 conclusion levels and
// the frozen template section anchors joined this table as adjudicated collisions.
// What remains is non-status vocabulary, ONE FRAGMENT PER OCCURRENCE
// (T11e-6 M5 + review Minor 3: a site is a (line, occurrence-index) pair, so a
// line carrying the value twice needs two fragments). The value and fragment
// COUNTS are deliberately NOT written here: they are measured by the gates from
// this table, and a restated number is a second truth that drifts (review M1:
// round 1 removed the numbers from the gates but left them stale here).
// It covers non-status vocabulary plus the one non-parameterizable ON CONFLICT
// index predicate (Ruling T11d-R4), which carries its own value pin.
func DeliveryGateAllowlist() map[string]vocablint.AllowEntry {
	return map[string]vocablint.AllowEntry{
		"version": {
			Reason: "Two non-status uses. (a) The version-level waiver version_fact KEY: " +
				"version/application/waiver_service.go:347 writes Key: \"version\" as a raw literal, so this " +
				"fact-key convention has no domain constant to source (delivery.ReportKindVersion is the " +
				"report scope, codeintel.AnchorSubjectVersion is an anchor subject — neither owns it). " +
				"(b) platform.NotFound's entity name in the error envelope, not a report kind.",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/adapters/read_model.go", `if latest := latestByKey["version"]; latest != nil {`, `snapshot.WaivedAt = latestAtByKey["version"]`, `return platform.NotFound("version")`),
		},

		"reason": {
			Reason: "The waiver version_fact PAYLOAD key: version/application/waiver_service.go:293/352 writes " +
				"\"reason\": reason as a raw literal. execution.HumanPayloadReason/HumanResponseReason own the " +
				"human-request payload/response vocabulary, a different concept; sourcing them here would " +
				"assert a false authority over an unowned cross-context fact-payload convention.",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/adapters/read_model.go", `snapshot.WaiverReason = factString(latest.Payload["reason"])`, `requirement.WaiverReason = factString(latest.Payload["reason"])`),
		},

		"evidence": {
			Reason: "Two non-status uses. (a) The run_wave.counts AGGREGATE key (WaveCounts.Evidence's json tag, " +
				"alongside \"total\"), not an issue/report ref kind. (b) The frozen report HTML section id and " +
				"data-section attribute of the evidence appendix (template v1/v2 and v3); the v3 bytes are " +
				"pinned by TestTemplateV3Golden.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/delivery/infrastructure/adapters/read_model.go", `"evidence": source.Counts.Evidence,`),
				vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template.go", `<section id="evidence" data-section="evidence">`, `<section id="evidence" data-section="evidence">`),
				vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template_v3.go", `<section id="evidence" data-section="evidence">`, `<section id="evidence" data-section="evidence">`),
			),
		},

		"html": {
			Reason: "Two non-status uses. (a) platform.Invalid's FIELD name in the error envelope (the " +
				"PutReportHTML parameter), not version.AttachmentHTML / storage.FormatHTML. (b) The renderer " +
				"service's frozen request-body field name: renderer/src/server.mjs:190 reads body.html.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/delivery/infrastructure/content/store.go", `platform.Invalid("html", "report HTML is required")`),
				vocablint.FileFragments("backend/internal/delivery/infrastructure/renderclient/client.go", `json.Marshal(map[string]string{"html": string(html)})`),
			),
		},

		"pdf": {
			Reason: "platform.Invalid's FIELD name in the error envelope (the PutReportPDF parameter), " +
				"not version.AttachmentPDF / storage.FormatPDF.",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/content/store.go", `platform.Invalid("pdf", "report PDF is required")`),
		},

		"report": {
			Reason: "The html/template NAME passed to template.New — an internal template identifier, " +
				"not delivery.IssueRefKindReport / chain.NodeReport / storage.OwnerKindReport.",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/render/render.go", `template.New("report")`),
		},

		"requirement": {
			Reason: "The v3 defect card's data-element attribute (a fixed HTML element name), " +
				"not delivery.ReportRefKindRequirement / chain.NodeRequirement.",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template_v3.go", `<div class="card-element" data-element="requirement">`),
		},

		"environment": {
			Reason: "The v3 non-issue-failure HTML section id and data-section attribute, " +
				"not the execution/delivery failure CLASS 'environment'.",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template_v3.go", `<section id="environment" data-section="environment">`, `<section id="environment" data-section="environment">`),
		},

		"waived": {
			Reason: "The v3 waived-content HTML section id / data-section attribute and its CSS class name, " +
				"not delivery.ReportClosureKindWaived / version.ClosureKindWaived (the closure-kind VALUE " +
				"itself is compared through the domain constant everywhere it is read).",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template_v3.go", `<section id="waived" data-section="waived">`, `<section id="waived" data-section="waived">`, `<div class="waived">`),
		},

		"http": {
			Reason: "The URL SCHEME accepted for the renderer endpoint, not execution.EvidenceHTTP " +
				"(an evidence kind).",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/renderclient/client.go", `if parsed.Scheme != "http" && parsed.Scheme != "https" {`),
		},

		"queued": {
			Reason: "Ruling T11d-R4: the ON CONFLICT partial-unique-index inference predicate cannot bind " +
				"parameters — PostgreSQL proves index membership by predicate implication, which fails once " +
				"pgx's cached statement goes to a generic plan (real DB: 4 executions green, SQLSTATE 42P10 " +
				"from the 5th; task-11d-report.md §6). Compensating control: " +
				"TestAnalysisConflictPredicateMatchesDomain pins these literals to analysisActiveStatuses() " +
				"(domain-derived) and TestAnalysisConflictPredicateMatchesIndex pins them to the live index.",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/postgres/analysis_repo.go", `ON CONFLICT (version_id, kind, dedupe_key) WHERE status IN ('queued', 'running') DO NOTHING`),
		},

		"running": {
			Reason: "Ruling T11d-R4: same ON CONFLICT partial-index inference predicate as 'queued' — " +
				"not parameterizable (generic-plan SQLSTATE 42P10), pinned by " +
				"TestAnalysisConflictPredicateMatchesDomain / TestAnalysisConflictPredicateMatchesIndex.",
			Fragments: vocablint.FileFragments("backend/internal/delivery/infrastructure/postgres/analysis_repo.go", `ON CONFLICT (version_id, kind, dedupe_key) WHERE status IN ('queued', 'running') DO NOTHING`),
		},
		"unverified": {
			Reason: "The v3 report CONCLUSION LEVEL (render_v3.go conclusionView.Level, rendered as " +
				".conclusion[data-conclusion=...] in template_v3.go), not delivery.ACVerificationUnverified " +
				"(an acceptance-criterion verification status). The conclusion-level vocabulary " +
				"{empty,defect,unverified,ok} has NO Go authority today (H-A26) and its CSS selectors are " +
				"TestTemplateV3Golden frozen bytes, so the template may not be edited. T11e-6 adjudicates " +
				"the collision and escalates the missing authority to the controller; building it is a " +
				"W3/W5 decision, not a mirror constant this task may invent. THREE fragments for three " +
				"occurrences (one per line here): the short text is a substring of both long return " +
				"lines, so one copy per occurrence keeps the registry 1:1 (M5 + review Minor 3).",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/delivery/infrastructure/render/render_v3.go",
					`Level: "unverified",`, `Level: "unverified",`, `Level: "unverified",`),
				vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template_v3.go",
					`.conclusion[data-conclusion="unverified"] { border-left-color: #d4a72c; background: #fff8c5; }`),
			),
		},

		"ok": {
			Reason: "The v3 report CONCLUSION LEVEL (render_v3.go conclusionView.Level and the frozen " +
				".conclusion[data-conclusion=\"ok\"] CSS rule), not quality.ReasonOK (an evaluation reason " +
				"code). Same adjudication and same escalation as \"unverified\" above (H-A26).",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/delivery/infrastructure/render/render_v3.go",
					`return conclusionView{Level: "ok", Text: "未发现缺陷，已执行用例全部通过，验收标准均已验证，一切正常。"}`),
				vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template_v3.go",
					`.conclusion[data-conclusion="ok"] { border-left-color: #1a7f37; background: #dafbe1; }`),
			),
		},

		"issues": {
			Reason: "The frozen HTML SECTION anchor of the report templates (template.go v1/v2 " +
				"<section id=\"issues\">, template_v3.go <section id=\"defects\" data-section=\"issues\">), " +
				"not chain.countIssues (a chain node counter) nor delivery.ReportTruncationIssues (a " +
				"truncation bucket). Section anchors are part of the rendered report contract; the v3 " +
				"template bytes are golden-frozen.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template.go", `<section id="issues" data-section="issues">`, `<section id="issues" data-section="issues">`),
				vocablint.FileFragments("backend/internal/delivery/infrastructure/render/template_v3.go",
					`<section id="defects" data-section="issues">`),
			),
		},
	}
}

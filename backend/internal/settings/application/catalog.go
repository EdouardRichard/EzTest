// Package settingsapp implements the settings-center use cases and freezes the
// P0 settings catalog.
package settingsapp

import (
	"encoding/json"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

// Definitions returns the frozen P0 settings catalog. Defaults come from the
// 0.2 design's OD-4/OD-8/OD-9 values; values the design left open were frozen
// by the P0 spec (P0-D7/P0-D19).
func Definitions(executorURL string) []settings.Definition {
	str := func(value string) json.RawMessage {
		raw, _ := json.Marshal(value)
		return raw
	}
	num := func(value float64) json.RawMessage {
		return json.RawMessage(strconv.FormatFloat(value, 'f', -1, 64))
	}
	boolean := func(value bool) json.RawMessage {
		if value {
			return json.RawMessage("true")
		}
		return json.RawMessage("false")
	}
	number := func(min, max float64) *float64 {
		return &min
	}
	numberRange := func(min, max float64) (*float64, *float64) {
		return &min, &max
	}

	minNode, maxNode := numberRange(0, 1023)
	minConcurrency, maxConcurrency := numberRange(1, 32)
	minPerSUT, maxPerSUT := numberRange(1, 16)
	minCross, maxCross := numberRange(1, 8)
	minTurns, maxTurns := numberRange(1, 100)
	minRuns, maxRuns := numberRange(1, 64)
	minThreshold, maxThreshold := numberRange(0.5, 0.9)

	return []settings.Definition{
		{Key: "runtime.snowflake_node_id", Category: "runtime", Type: settings.TypeNumber, Default: num(1), Integer: true, Min: minNode, Max: maxNode, RestartRequired: true, Description: "Snowflake node ID (0-1023); unique per API instance."},
		{Key: "paths.data_root", Category: "paths", Type: settings.TypeString, Default: str("./.data"), RestartRequired: true, Description: "Root directory for runtime content (relative to the process CWD)."},
		{Key: "paths.attachment_dir", Category: "paths", Type: settings.TypeString, Default: str("./.data/attachments"), Description: "Requirement attachment directory."},
		{Key: "paths.evidence_dir", Category: "paths", Type: settings.TypeString, Default: str("./.data/evidence"), Description: "Execution evidence directory."},
		{Key: "paths.report_dir", Category: "paths", Type: settings.TypeString, Default: str("./.data/reports"), Description: "Report HTML/PDF directory."},
		{Key: "paths.export_dir", Category: "paths", Type: settings.TypeString, Default: str("./.data/exports"), Description: "Export artifact directory."},

		{Key: "execution.global_concurrency", Category: "execution", Type: settings.TypeNumber, Default: num(3), Integer: true, Min: minConcurrency, Max: maxConcurrency, Description: "Maximum concurrent case runs across the instance."},
		{Key: "execution.per_sut_concurrency", Category: "execution", Type: settings.TypeNumber, Default: num(2), Integer: true, Min: minPerSUT, Max: maxPerSUT, Description: "Maximum concurrent case runs per version SUT."},
		{Key: "execution.cross_system_concurrency", Category: "execution", Type: settings.TypeNumber, Default: num(1), Integer: true, Min: minCross, Max: maxCross, Description: "Maximum concurrent cross-SUT case runs."},
		{Key: "execution.executor_url", Category: "execution", Type: settings.TypeString, Default: str(executorURL), Description: "Playwright executor base URL (seeded from EZTEST_EXECUTOR_PORT)."},
		{Key: "execution.evidence_max_bytes", Category: "execution", Type: settings.TypeNumber, Default: num(5 * 1024 * 1024), Integer: true, Min: number(0, 0), Description: "Maximum single evidence file size in bytes."},
		{Key: "execution.case_timeout_seconds", Category: "execution", Type: settings.TypeNumber, Default: num(900), Integer: true, Min: number(1, 0), Description: "Hard timeout for one case run in seconds."},

		{Key: "agent.max_turns", Category: "agent", Type: settings.TypeNumber, Default: num(12), Integer: true, Min: minTurns, Max: maxTurns, Description: "Maximum model turns per agent run."},
		{Key: "agent.run_soft_timeout_seconds", Category: "agent", Type: settings.TypeNumber, Default: num(300), Integer: true, Min: number(1, 0), Description: "Soft timeout for one agent run in seconds."},
		{Key: "agent.run_hard_timeout_seconds", Category: "agent", Type: settings.TypeNumber, Default: num(600), Integer: true, Min: number(1, 0), Description: "Hard timeout for one agent run in seconds."},
		{Key: "agent.max_concurrent_runs", Category: "agent", Type: settings.TypeNumber, Default: num(4), Integer: true, Min: minRuns, Max: maxRuns, Description: "Maximum concurrent agent runs."},
		{Key: "agent.tool_result_max_bytes", Category: "agent", Type: settings.TypeNumber, Default: num(32768), Integer: true, Min: number(1024, 0), Description: "Tool result inline limit before spilling to content storage."},
		{Key: "llm.request_timeout_seconds", Category: "llm", Type: settings.TypeNumber, Default: num(60), Integer: true, Min: number(1, 0), Description: "Default LLM request timeout."},
		{Key: "llm.max_retries", Category: "llm", Type: settings.TypeNumber, Default: num(2), Integer: true, Min: number(0, 0), Description: "Default LLM retry count."},

		{Key: "context.window_tokens", Category: "context", Type: settings.TypeNumber, Default: num(64000), Integer: true, Min: number(1024, 0), Description: "Model context window used by the context budgeter."},
		{Key: "context.output_reserve_tokens", Category: "context", Type: settings.TypeNumber, Default: num(8000), Integer: true, Min: number(0, 0), Description: "Tokens reserved for the model response."},
		{Key: "context.compact_threshold", Category: "context", Type: settings.TypeNumber, Default: num(0.7), Min: minThreshold, Max: maxThreshold, Description: "Usage ratio that triggers rolling summarization."},
		{Key: "context.keep_recent_messages", Category: "context", Type: settings.TypeNumber, Default: num(12), Integer: true, Min: number(0, 0), Description: "Recent messages kept verbatim during compaction."},

		{Key: "team_room.digest_every_n_posts", Category: "team_room", Type: settings.TypeNumber, Default: num(20), Integer: true, Min: number(1, 0), Description: "Generate a team-room digest every N posts."},
		{Key: "team_room.digest_interval_minutes", Category: "team_room", Type: settings.TypeNumber, Default: num(10), Integer: true, Min: number(1, 0), Description: "Generate a team-room digest at least every N minutes."},
		{Key: "team_room.digest_keep_versions", Category: "team_room", Type: settings.TypeNumber, Default: num(5), Integer: true, Min: number(1, 0), Description: "Number of team-room digest versions to retain."},

		{Key: "retention.temp_hours", Category: "retention", Type: settings.TypeNumber, Default: num(24), Integer: true, Min: number(1, 0), Description: "Retention for temporary files."},
		{Key: "retention.replaced_export_days", Category: "retention", Type: settings.TypeNumber, Default: num(7), Integer: true, Min: number(1, 0), Description: "Retention for replaced exports."},
		{Key: "retention.unreferenced_content_days", Category: "retention", Type: settings.TypeNumber, Default: num(30), Integer: true, Min: number(1, 0), Description: "Retention for unreferenced content."},
		{Key: "retention.gc_grace_hours", Category: "retention", Type: settings.TypeNumber, Default: num(24), Integer: true, Min: number(1, 0), Description: "GC grace period."},
		{Key: "retention.gc_dry_run", Category: "retention", Type: settings.TypeBool, Default: boolean(true), Description: "Run GC in dry-run mode by default."},

		{Key: "report.template_version", Category: "report", Type: settings.TypeString, Default: str("1"), Description: "Built-in report template version."},
		{Key: "report.render_pdf", Category: "report", Type: settings.TypeBool, Default: boolean(true), Description: "Render PDF alongside the authoritative HTML report."},
		{Key: "report.section_summary", Category: "report", Type: settings.TypeBool, Default: boolean(true), Description: "Include the summary section in reports."},
		{Key: "report.section_results", Category: "report", Type: settings.TypeBool, Default: boolean(true), Description: "Include the results section in reports."},
		{Key: "report.section_issues", Category: "report", Type: settings.TypeBool, Default: boolean(true), Description: "Include the issues section in reports."},
		{Key: "report.section_coverage", Category: "report", Type: settings.TypeBool, Default: boolean(true), Description: "Include the coverage section in reports."},
		{Key: "report.section_evidence", Category: "report", Type: settings.TypeBool, Default: boolean(true), Description: "Include the evidence section in reports."},

		{Key: "knowledge.injection_budget_tokens", Category: "knowledge", Type: settings.TypeNumber, Default: num(4000), Integer: true, Min: number(0, 0), Description: "Token budget for knowledge injected into a prompt."},
		{Key: "knowledge.review_required", Category: "knowledge", Type: settings.TypeBool, Default: boolean(true), Description: "Require admin review before knowledge is approved."},

		{Key: "security.audit_retention_days", Category: "security", Type: settings.TypeNumber, Default: num(90), Integer: true, Min: number(1, 0), Description: "Audit log retention in days."},
		{Key: "security.mask_secrets", Category: "security", Type: settings.TypeBool, Default: boolean(true), Description: "Mask secrets in admin responses (must stay on in 0.2)."},

		{Key: "feature.auto_gap_design", Category: "feature", Type: settings.TypeBool, Default: boolean(true), Description: "Allow automatic design proposals for coverage gaps."},
		{Key: "feature.experimental", Category: "feature", Type: settings.TypeBool, Default: boolean(false), Description: "Enable experimental features."},
	}
}

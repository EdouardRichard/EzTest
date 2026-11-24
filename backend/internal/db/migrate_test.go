package db

import (
	"strings"
	"testing"
)

func TestOrderCoversEverySQLFile(t *testing.T) {
	migrations, err := orderedMigrations()
	if err != nil {
		t.Fatalf("orderedMigrations: %v", err)
	}
	// The count is derived from order.txt (non-comment, non-empty lines) so
	// appended migrations do not need to touch this test.
	wantCount := 0
	orderRaw, err := migrationsFS.ReadFile(orderFile)
	if err != nil {
		t.Fatalf("read %s: %v", orderFile, err)
	}
	for _, line := range strings.Split(string(orderRaw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		wantCount++
	}
	if len(migrations) != wantCount {
		t.Fatalf("len = %d, want %d (derived from order.txt)", len(migrations), wantCount)
	}
	for _, item := range migrations {
		if !strings.HasSuffix(item.Name, ".sql") {
			t.Fatalf("migration %q does not end in .sql", item.Name)
		}
		if len(item.Checksum) != 64 {
			t.Fatalf("migration %q checksum = %q", item.Name, item.Checksum)
		}
		if strings.TrimSpace(item.SQL) == "" {
			t.Fatalf("migration %q is empty", item.Name)
		}
	}
	if migrations[0].Name != "settings_0001_settings.sql" {
		t.Fatalf("first migration = %q", migrations[0].Name)
	}
	wantTail := []string{
		"codeintel_0001_code_index.sql",
		"codeintel_0002_code_analysis.sql",
		"codeintel_0003_anchor_review.sql",
		"visual_0001_visual_assertion.sql",
		"execution_0004_visual_evidence.sql",
		"version_0006_version_sut_code_baseline.sql",
		"agent_0004_code_team.sql",
		"quality_0001_eval.sql",
		"agent_0005_profile_prompt_revision.sql",
		"knowledge_0001_knowledge.sql",
		"storage_0003_gc.sql",
		"delivery_0004_report_pdf.sql",
		"orchestration_0001_instance.sql",
		"resource_0002_sut_family.sql",
		"delivery_0005_issue_recurrence.sql",
		"agent_0006_ops_team.sql",
		"orchestration_0002_instance_inflight.sql",
		"execution_0005_case_run_sut_context_executor.sql",
		"version_0007_stage_plan_fact.sql",
		"agent_0007_work_item021.sql",
		"llm_0002_model021.sql",
		"knowledge_0002_binding021.sql",
		"resource_0003_page_url.sql",
		"version_0008_suspended_from.sql",
		"llm_0003_protocol021.sql",
		"knowledge_0003_trgm.sql",
		"version_0009_requirement_source_key.sql",
		"llm_0004_v022_model_param_sources.sql",
		"agent_0008_v022_writer_runtime.sql",
		"version_0010_v022_binding_handoff.sql",
		"version_0011_v022_attachment_observation.sql",
		"execution_0006_v022_wave_observation.sql",
		"execution_0007_v022_human_request_question.sql",
		"design_0002_v022_case_auto_confirm.sql",
		"version_0012_v022_plan_revision.sql",
		"version_0013_v022_extraction_report.sql",
		"version_0014_v022_attachment_conversation.sql",
		"delivery_0002_v022_issue_manual_retest.sql",
		"design_0003_v022_case_rollback_trigger.sql",
		"quality_0002_v022_eval_scope.sql",
		"agent_0009_profile_seed_version.sql",
		"visual_0002_v024_drop_visual.sql",
		"resource_0004_v024_hierarchy_inversion.sql",
		"version_0015_v024_environment_binding.sql",
		"version_0016_v024_environment_not_null.sql",
		"delivery_0003_v024_rekey.sql",
		"version_0017_v024_requirement_contract.sql",
		"design_0004_v024_case_contract.sql",
		"execution_0008_v024_rekey.sql",
		"resource_0005_v024_sut_registration.sql",
		"execution_0009_v024_evidence_kinds.sql",
		"execution_0010_v024_executor_sessions.sql",
		"agent_0010_v024_skills.sql",
		"agent_0011_v024_skill_usage.sql",
		"version_0018_v024_stream_state.sql",
		"agent_0012_v024_stream_state.sql",
		"agent_0013_v024_activity_seq.sql",
		"design_0005_v024_judgment.sql",
		"execution_0011_v024_f_sut_resolution.sql",
		"execution_0012_v024_f_asset_registration.sql",
		"execution_0013_v024_f_session_fixture.sql",
		"execution_0014_v024_f_code_baseline_immutability.sql",
		"execution_0015_v024_f_sut_resolution_ambiguous.sql",
		"execution_0016_v024_f_sut_context_fixture.sql",
		"delivery_0006_v024_g_issue_structured.sql",
		"version_0019_v024_g_waiver.sql",
		"delivery_0007_v024_g_display_fingerprint.sql",
		"execution_0017_v024_h_drop_human_other.sql",
	}
	tail := migrations[len(migrations)-len(wantTail):]
	for i, want := range wantTail {
		if tail[i].Name != want {
			t.Fatalf("migration %d = %q, want %q", len(migrations)-len(wantTail)+i+1, tail[i].Name, want)
		}
	}
}

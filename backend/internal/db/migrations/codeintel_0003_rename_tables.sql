-- D34 table rename: codeintel (spec 3.2).
-- Only table names change; columns, constraints and existing index names are
-- intentionally left untouched. code_analysis_cache keeps its name.
ALTER TABLE IF EXISTS code_change_sets RENAME TO code_change_set;
ALTER TABLE IF EXISTS code_index_runs RENAME TO code_index_run;
ALTER TABLE IF EXISTS code_entities RENAME TO code_entity;
ALTER TABLE IF EXISTS code_edges RENAME TO code_edge;
ALTER TABLE IF EXISTS code_fact_versions RENAME TO code_fact_version;
ALTER TABLE IF EXISTS code_analyses RENAME TO code_analysis;
ALTER TABLE IF EXISTS code_impact_candidates RENAME TO code_impact_candidate;
ALTER TABLE IF EXISTS code_anchors RENAME TO code_anchor;

ALTER SEQUENCE IF EXISTS code_change_sets_id_seq RENAME TO code_change_set_id_seq;
ALTER SEQUENCE IF EXISTS code_index_runs_id_seq RENAME TO code_index_run_id_seq;
ALTER SEQUENCE IF EXISTS code_entities_id_seq RENAME TO code_entity_id_seq;
ALTER SEQUENCE IF EXISTS code_edges_id_seq RENAME TO code_edge_id_seq;
ALTER SEQUENCE IF EXISTS code_fact_versions_id_seq RENAME TO code_fact_version_id_seq;
ALTER SEQUENCE IF EXISTS code_analyses_id_seq RENAME TO code_analysis_id_seq;
ALTER SEQUENCE IF EXISTS code_impact_candidates_id_seq RENAME TO code_impact_candidate_id_seq;
ALTER SEQUENCE IF EXISTS code_anchors_id_seq RENAME TO code_anchor_id_seq;

-- D34: table naming normalization (plural -> singular). Fields/constraints/indexes unchanged.
ALTER TABLE IF EXISTS llm_configs RENAME TO llm_config;
ALTER SEQUENCE IF EXISTS llm_configs_id_seq RENAME TO llm_config_id_seq;

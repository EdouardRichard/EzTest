-- 0.2.1 llm context: thinking mode, reasoning effort, the explicit default
-- model and model capability/deprecation metadata.
-- The 0.2 table is llm_config (the 0.2.1 plan calls it llm_profile).
-- See docs/version-V0.2.1/spec.md §7.2, §7.3, §12.2.

ALTER TABLE llm_config
    ADD COLUMN thinking_type TEXT NOT NULL DEFAULT 'auto'
        CHECK (thinking_type IN ('auto', 'enabled', 'disabled')),
    ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT 'none'
        CHECK (reasoning_effort IN ('none', 'low', 'medium', 'high', 'xhigh', 'max')),
    ADD COLUMN default_model_id BIGINT NULL,
    ADD CONSTRAINT fk_llm_config_default_model
        FOREIGN KEY (default_model_id) REFERENCES llm_model (id) ON DELETE SET NULL;

ALTER TABLE llm_model
    ADD COLUMN capabilities JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(capabilities) = 'object'),
    ADD COLUMN deprecated BOOLEAN NOT NULL DEFAULT false;

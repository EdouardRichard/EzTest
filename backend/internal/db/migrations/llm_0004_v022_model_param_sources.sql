-- v0.22 llm context: per-field provenance for the model context window and
-- output limit. A catalog refresh may only overwrite a parameter whose source
-- is still 'catalog'; a value set by an administrator ('manual') survives.
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-1 and
-- docs/version-V0.22/A-model-token-budget.md §4.2.

ALTER TABLE llm_model
    ADD COLUMN window_source TEXT NOT NULL DEFAULT 'catalog'
        CONSTRAINT chk_llm_model_window_source
        CHECK (window_source IN ('catalog', 'manual')),
    ADD COLUMN output_source TEXT NOT NULL DEFAULT 'catalog'
        CONSTRAINT chk_llm_model_output_source
        CHECK (output_source IN ('catalog', 'manual'));

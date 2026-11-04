-- 0.2.1 llm context: protocol stores the wire protocol actually spoken by a
-- configuration. llm_0001_llm.sql declared its CHECK inline, so PostgreSQL
-- auto-named it llm_config_protocol_check and accepted only the legacy
-- openai/openai_compatible values. Replace it with a named constraint that also
-- admits the canonical openai_chat_completions/openai_responses values.
-- Additive only: the legacy values stay valid and no stored row is rewritten
-- (NormalizeProtocol reads them as openai_chat_completions).
-- See docs/version-V0.2.1/spec.md §7.1.

ALTER TABLE llm_config
    DROP CONSTRAINT llm_config_protocol_check;

ALTER TABLE llm_config
    ADD CONSTRAINT chk_llm_config_protocol CHECK (
        protocol IN (
            'openai', 'openai_compatible',
            'openai_chat_completions', 'openai_responses'
        )
    );

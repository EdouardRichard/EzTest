-- 0.2.1 knowledge context: structured bindings and the verification lifecycle
-- (suspect/stale) required by the closed knowledge loop.
-- The existing draft/approved/rejected/revoked values are kept, so the CHECK
-- is widened rather than replaced (additive migration, fresh 0.2.1 database).
-- See docs/version-V0.2.1/spec.md §10.2, §12.2.

ALTER TABLE knowledge_item
    DROP CONSTRAINT IF EXISTS knowledge_item_status_check,
    DROP CONSTRAINT IF EXISTS chk_knowledge_item_status,
    ADD COLUMN bindings JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(bindings) = 'object'),
    ADD COLUMN last_verified_at TIMESTAMPTZ NULL,
    ADD CONSTRAINT chk_knowledge_item_status
        CHECK (status IN ('draft', 'approved', 'rejected', 'revoked', 'suspect', 'stale'));

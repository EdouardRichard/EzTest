-- 0.2.1 agent context: the extended work_item contract, actual model/token
-- accounting on agent_run, and streamed message parts.
-- Additive only; lease_epoch already exists from agent_0001_work_item.sql.
-- See docs/version-V0.2.1/spec.md §5.4, §6.5, §12.2.

ALTER TABLE work_item
    ADD COLUMN kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN goal TEXT NOT NULL DEFAULT '',
    ADD COLUMN target JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(target) = 'object'),
    ADD COLUMN inputs JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(inputs) = 'object'),
    ADD COLUMN expected JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(expected) = 'object'),
    ADD COLUMN budget JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(budget) = 'object'),
    ADD COLUMN created_by TEXT NOT NULL DEFAULT 'state_machine'
        CONSTRAINT chk_work_item_created_by
        CHECK (created_by IN ('state_machine', 'planner', 'assistant', 'trigger')),
    ADD COLUMN idempotency_key TEXT NULL,
    ADD COLUMN result JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(result) = 'object');

-- 0.2 stored a single logical dependency as BIGINT; 0.2.1 needs the full set.
-- Keep the column nullable: existing P1 code reads NULL as "no dependency", and
-- P2-T1 starts writing arrays together with the new domain field.
ALTER TABLE work_item
    ALTER COLUMN depends_on TYPE BIGINT[]
    USING CASE WHEN depends_on IS NULL THEN NULL ELSE ARRAY[depends_on] END;

-- Idempotency is scoped to (version_id, kind, idempotency_key). Legacy rows
-- leave idempotency_key NULL, and PostgreSQL permits multiple NULL keys.
CREATE UNIQUE INDEX uk_work_item_idempotency
    ON work_item (version_id, kind, idempotency_key);

ALTER TABLE agent_run
    ADD COLUMN model TEXT NOT NULL DEFAULT '',
    ADD COLUMN config_id BIGINT NULL,
    ADD COLUMN fallback_from TEXT NULL,
    ADD COLUMN tokens JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(tokens) = 'object'),
    ADD COLUMN cache JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(cache) = 'object');

CREATE INDEX idx_agent_run_config_model ON agent_run (config_id, model);

-- Streaming transcript parts. agent_message remains the ordered message head.
CREATE TABLE agent_message_part (
    id         BIGINT PRIMARY KEY,
    message_id BIGINT NOT NULL
               CONSTRAINT fk_agent_message_part_message
               REFERENCES agent_message (id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL CHECK (seq > 0),
    part_type  TEXT NOT NULL
               CONSTRAINT chk_agent_message_part_type
               CHECK (part_type IN (
                   'text', 'thinking', 'tool_call', 'job_card',
                   'attachment', 'requirement_card', 'case_card', 'error'
               )),
    payload    JSONB NOT NULL DEFAULT '{}'::jsonb
               CONSTRAINT chk_agent_message_part_payload
               CHECK (jsonb_typeof(payload) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_agent_message_part_message_seq
    ON agent_message_part (message_id, seq);

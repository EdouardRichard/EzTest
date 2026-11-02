-- 0.2.1 version context: stage machine state, the three-file plan/fact stores,
-- per-conversation model overrides, streaming conversation parts and structured
-- attachment parse metadata.
-- Additive only: no 0.2 row is rewritten (0.2.1 starts from a fresh database).
-- See docs/version-V0.2.1/spec.md §6.1, §12.1, §12.2.

ALTER TABLE version
    ADD COLUMN stage TEXT NOT NULL DEFAULT 'created'
        CONSTRAINT chk_version_stage CHECK (stage IN (
            'created', 'clarifying', 'requirement_review', 'designing',
            'case_review', 'executing', 'reporting', 'completed',
            'blocked', 'paused'
        )),
    ADD COLUMN stage_entered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN blocker TEXT NULL;

CREATE INDEX idx_version_stage ON version (stage, id);

-- task_plan.md: the ordered plan of one version. `sort_order` matches the
-- existing sort_order convention (design_0001_case_model.sql); the logical
-- field is still the plan item's order.
CREATE TABLE version_plan_item (
    id          BIGINT PRIMARY KEY,
    version_id  BIGINT NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    phase       TEXT NOT NULL CHECK (phase <> ''),
    title       TEXT NOT NULL CHECK (title <> ''),
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status <> ''),
    depends_on  BIGINT[] NOT NULL DEFAULT '{}'::BIGINT[],
    work_item_id BIGINT NULL,
    decision    TEXT NOT NULL DEFAULT '',
    sort_order  INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_version_plan_item_version_status_order
    ON version_plan_item (version_id, status, sort_order);

-- findings.md: append-only facts. A conflict is a new row with a different
-- content_hash; superseded_by marks the losing row. Facts never overwrite.
CREATE TABLE version_fact (
    id             BIGINT PRIMARY KEY,
    version_id     BIGINT NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind <> ''),
    key            TEXT NOT NULL CHECK (key <> ''),
    payload        JSONB NOT NULL DEFAULT '{}'::jsonb
                   CHECK (jsonb_typeof(payload) = 'object'),
    confidence     NUMERIC(4,3) NULL
                   CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    source         TEXT NOT NULL CHECK (source <> ''),
    source_ref     TEXT NOT NULL DEFAULT '',
    evidence_refs  BIGINT[] NOT NULL DEFAULT '{}'::BIGINT[],
    content_hash   TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    superseded_by  BIGINT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_version_fact_version_kind_key_hash
    ON version_fact (version_id, kind, key, content_hash);
CREATE INDEX idx_version_fact_current
    ON version_fact (version_id, kind, key) WHERE superseded_by IS NULL;

ALTER TABLE conversation
    ADD COLUMN model_override TEXT NULL;

-- Streaming UI parts of one conversation message. conversation_message is the
-- ordered message head rendered by the UI and lives in this (version) context,
-- so the FK is a legitimate same-context reference. The agent-context
-- agent_message transcript keeps its own history and must not be FK-linked
-- across contexts (Agent.md §6.9.7). The unique index also serves ordered reads
-- by message, so no separate (message_id, seq) index is needed.
CREATE TABLE conversation_message_part (
    id         BIGINT PRIMARY KEY,
    message_id BIGINT NOT NULL
               CONSTRAINT fk_conversation_message_part_message
               REFERENCES conversation_message (id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL CHECK (seq > 0),
    part_type  TEXT NOT NULL
               CONSTRAINT chk_conversation_message_part_type
               CHECK (part_type IN (
                   'text', 'thinking', 'tool_call', 'job_card',
                   'attachment', 'requirement_card', 'case_card', 'error'
               )),
    payload    JSONB NOT NULL DEFAULT '{}'::jsonb
               CONSTRAINT chk_conversation_message_part_payload
               CHECK (jsonb_typeof(payload) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_conversation_message_part_message_seq
    ON conversation_message_part (message_id, seq);

-- 0.2.1 keeps the 0.2 extraction_* pair for compatibility and adds the
-- structured parse contract used by the new parser/vision pipeline.
ALTER TABLE requirement_attachment
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'document'
        CHECK (kind IN ('document', 'image')),
    ADD COLUMN parse_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (parse_status IN ('pending', 'processing', 'done', 'failed', 'skipped')),
    ADD COLUMN parse_error TEXT NULL,
    ADD COLUMN page_refs JSONB NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(page_refs) = 'array');

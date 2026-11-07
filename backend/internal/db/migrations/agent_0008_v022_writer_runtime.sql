-- v0.22 A/B core schema: the single-writer conversation lease, message kinds,
-- summary compaction watermark, run telemetry/activity trace and requirement
-- extraction run/segment bookkeeping.
--
-- Additive only; the 0.21 rows stay untouched (v0.22 rebuilds its database).
-- The conversation/requirement tables are version-context domain tables; they
-- live in this agent_0008 file because the W0 plan pairs the whole v0.22 A/B
-- core schema into exactly two migrations (lead ruling C2). Cross-context
-- references stay logical BIGINT; foreign keys are only used inside the version
-- context (Agent.md §6.9.7).
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-2..DB-9,
-- docs/version-V0.22/B-clarification-messaging.md §5.1 and
-- docs/version-V0.22/A-model-token-budget.md §5.2, §6.4, §7.3.

-- DB-2: one active turn per conversation plus its writer lease. The 0.22 spec
-- pins the invariant as a partial unique index on conversation(id) while a turn
-- is active; idle conversations (active_turn_id IS NULL) stay outside the index.
ALTER TABLE conversation
    ADD COLUMN active_turn_id BIGINT NULL,
    ADD COLUMN active_agent TEXT NULL,
    ADD COLUMN lease_until TIMESTAMPTZ NULL;

CREATE UNIQUE INDEX uk_conversation_active_turn
    ON conversation (id) WHERE active_turn_id IS NOT NULL;

-- DB-3: message authorship and the message/artifact split.
ALTER TABLE conversation_message
    ADD COLUMN author_agent TEXT NOT NULL DEFAULT '',
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'text'
        CONSTRAINT chk_conversation_message_kind
        CHECK (kind IN ('text', 'question', 'answer', 'system', 'artifact'));

-- DB-4: incremental compaction watermark and idempotency metadata.
-- compaction_id stays TEXT: it is an opaque compaction attempt token, not a
-- snowflake id (A §6.4).
ALTER TABLE conversation_summary
    ADD COLUMN covered_message_id BIGINT NULL,
    ADD COLUMN compaction_id TEXT NULL,
    ADD COLUMN summary_meta JSONB NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT chk_conversation_summary_meta
        CHECK (jsonb_typeof(summary_meta) = 'object');

-- DB-5: run telemetry. model/config_id already exist from agent_0007_work_item021.sql;
-- v0.22 only adds the cache and wall-clock dimensions, keeping usage JSONB.
ALTER TABLE agent_run
    ADD COLUMN cached_tokens BIGINT NOT NULL DEFAULT 0
        CONSTRAINT chk_agent_run_cached_tokens CHECK (cached_tokens >= 0),
    ADD COLUMN duration_ms BIGINT NOT NULL DEFAULT 0
        CONSTRAINT chk_agent_run_duration_ms CHECK (duration_ms >= 0);

-- DB-6: per-run step trace for replay and the Agent Center run detail. run_id
-- is a logical agent-context reference (no cross-context FK, Agent.md §6.9.7).
CREATE TABLE agent_activity (
    id          BIGINT NOT NULL,
    run_id      BIGINT NOT NULL,
    type        TEXT NOT NULL CHECK (type <> ''),
    title       TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ NULL,
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb
                CONSTRAINT chk_agent_activity_payload
                CHECK (jsonb_typeof(payload) = 'object'),
    error       TEXT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_agent_activity PRIMARY KEY (id)
);

CREATE INDEX idx_agent_activity_run ON agent_activity (run_id, id);

-- DB-7: deterministic requirement-document extraction runs. version_id and
-- attachment_id are same-context (version) references, so the FKs are safe.
CREATE TABLE requirement_extraction_run (
    id                BIGINT NOT NULL,
    version_id        BIGINT NOT NULL REFERENCES version (id),
    attachment_id     BIGINT NOT NULL REFERENCES requirement_attachment (id),
    input_sha256      TEXT NOT NULL,
    status            TEXT NOT NULL
                      CONSTRAINT chk_requirement_extraction_run_status
                      CHECK (status IN ('pending', 'running', 'paused', 'done', 'failed')),
    total_segments    INT NOT NULL DEFAULT 0 CHECK (total_segments >= 0),
    done_segments     INT NOT NULL DEFAULT 0 CHECK (done_segments >= 0),
    failed_segments   INT NOT NULL DEFAULT 0 CHECK (failed_segments >= 0),
    expected_count    INT NULL CHECK (expected_count IS NULL OR expected_count >= 0),
    extracted_count   INT NOT NULL DEFAULT 0 CHECK (extracted_count >= 0),
    error             TEXT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_requirement_extraction_run PRIMARY KEY (id)
);

-- DB-8: per-segment cursor. The unique (run_id, seq) key makes segment replay
-- idempotent; renewed_at is the lease heartbeat for paused/resumed runs.
CREATE TABLE requirement_extraction_segment (
    id               BIGINT NOT NULL,
    run_id           BIGINT NOT NULL
                     CONSTRAINT fk_requirement_extraction_segment_run
                     REFERENCES requirement_extraction_run (id) ON DELETE CASCADE,
    seq              INT NOT NULL CHECK (seq >= 0),
    heading_path     TEXT NOT NULL DEFAULT '',
    char_start       INT NOT NULL CHECK (char_start >= 0),
    char_end         INT NOT NULL CHECK (char_end >= 0),
    text_sha256      TEXT NOT NULL,
    status           TEXT NOT NULL
                     CONSTRAINT chk_requirement_extraction_segment_status
                     CHECK (status IN ('pending', 'running', 'done', 'failed', 'skipped')),
    attempts         INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    extracted_count  INT NOT NULL DEFAULT 0 CHECK (extracted_count >= 0),
    error            TEXT NULL,
    renewed_at       TIMESTAMPTZ NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_requirement_extraction_segment PRIMARY KEY (id),
    CONSTRAINT uk_requirement_extraction_segment_run_seq UNIQUE (run_id, seq)
);

-- DB-9: requirement provenance for the extraction/reconciliation pass.
ALTER TABLE requirement
    ADD COLUMN extraction_run_id BIGINT NULL,
    ADD COLUMN source_quote TEXT NOT NULL DEFAULT '',
    ADD COLUMN external_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN confidence NUMERIC(4,3) NULL
        CONSTRAINT chk_requirement_confidence
        CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1));

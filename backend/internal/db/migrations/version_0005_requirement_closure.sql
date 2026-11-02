-- P4 version context: requirement closure rounds (#24).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p4-delivery-chain-design.md §3.5.
-- requirement/requirement_version are same-context FKs; agent_run references
-- are logical BIGINT only. IDs are application-generated snowflakes (D29).
-- Closure rows are never deleted by the domain; reopen increments reopen_count
-- and the next close inserts close_round = max(existing) + 1.

CREATE TABLE requirement_closure (
    id                      BIGINT NOT NULL,
    requirement_id          BIGINT NOT NULL REFERENCES requirement (id) ON DELETE CASCADE,
    requirement_version_id  BIGINT NOT NULL REFERENCES requirement_version (id) ON DELETE CASCADE,
    close_round             INTEGER NOT NULL CHECK (close_round > 0),
    check_result            JSONB NOT NULL,
    evidence_refs           JSONB NOT NULL DEFAULT '[]'::jsonb,
    closed_by_run_id        BIGINT NULL,
    closed_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    reopen_count            INTEGER NOT NULL DEFAULT 0 CHECK (reopen_count >= 0),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_requirement_closure PRIMARY KEY (id),
    CONSTRAINT uk_requirement_closure_requirement_id_close_round
        UNIQUE (requirement_id, close_round)
);

CREATE INDEX idx_requirement_closure_requirement_id_id
    ON requirement_closure (requirement_id, id DESC);
CREATE INDEX idx_requirement_closure_requirement_version_id
    ON requirement_closure (requirement_version_id);

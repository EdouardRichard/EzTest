-- v0.2.4-F execution context (Task 2, SC-F2 / P16): the append-only SUT
-- resolution fact. One row records how a case_run resolved its target SUTs and
-- which page/API assets the run actually touched/verified valid. The touched
-- asset ids are the server-truth source for the knowledge page/API dimension
-- (SC-F10): a directory-hit run that never explores still records the assets it
-- used, so the dimension is non-empty without fabricating ids.
--
-- Append-only (version_fact / sut_registration precedent): rows are only ever
-- inserted, never rewritten or deleted; the repository exposes Append/List only.
-- The table carries created_at and NO updated_at — the append-only schema
-- signature. Resolving the same run again appends a second row (latest wins),
-- so there is deliberately NO unique owner key.
--
-- References: case_run_id is a NOT NULL within-context reference, so it carries a
-- real foreign key to case_run (the evidence/step_result/case_run_sut_context
-- precedent for a NOT NULL case_run_id; AGENTS §2.3 within-context real FK).
-- sut_ids/touched_page_ids/touched_api_ids are logical BIGINT arrays into the
-- resource context (no cross-context FK, constitution 五.7); the application
-- validates their existence before appending (server-truth). resolution_method is
-- free text (catalog_hit, exploration, ...), owned by the P16 resolution service,
-- like sut_registration.resolution_method.
--
-- Append-only migration: supersedes nothing, adds one table on top of the
-- existing execution schema. No destructive change, no backfill.

CREATE TABLE sut_resolution (
    id                BIGINT NOT NULL,
    case_run_id       BIGINT NOT NULL
        CONSTRAINT fk_sut_resolution_case_run REFERENCES case_run (id),
    sut_ids           BIGINT[] NOT NULL DEFAULT '{}'::BIGINT[],
    resolution_method TEXT NOT NULL DEFAULT '',
    touched_page_ids  BIGINT[] NOT NULL DEFAULT '{}'::BIGINT[],
    touched_api_ids   BIGINT[] NOT NULL DEFAULT '{}'::BIGINT[],
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by        BIGINT NOT NULL,
    CONSTRAINT pk_sut_resolution PRIMARY KEY (id)
);

-- Per-run read path (knowledge dimension projection reads a run's resolutions).
CREATE INDEX idx_sut_resolution_case_run ON sut_resolution (case_run_id, id);